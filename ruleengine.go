package ruleengine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/store"
	"github.com/arrowhr/ruleengine/telemetry"
)

// Resolver is the one evaluation contract a consumer depends on.
type Resolver interface {
	Evaluate(ctx context.Context, request EvalRequest) (Decision, error)
}

// Engine evaluates decisions for one service.
//
// It is safe for concurrent use by many goroutines, which is the normal case
// inside an HTTP handler: reads of the rule and hierarchy caches are lock-free
// and every evaluation keeps its state on its own stack.
//
// Construct one per service and keep it. Close it on shutdown so the
// background reconcile stops and buffered audit records are drained.
type Engine struct {
	config    Config
	options   *options
	cache     *store.Cache
	versions  *store.VersionResolver
	resolverM sync.RWMutex
	facts     *fact.Resolver

	reconcileOnce sync.Once
	stop          chan struct{}
	stopped       chan struct{}
	closeOnce     sync.Once
	closeErr      error
}

// New builds an engine from a configuration and the collaborators a service
// supplies. It opens no connection and loads nothing: call Reload, or let
// Start reconcile in the background, so that a service decides when its first
// network call happens.
func New(config Config, opts ...Option) (*Engine, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	resolved := defaultOptions()
	for _, option := range opts {
		if option != nil {
			option(resolved)
		}
	}
	if config.Validation == ValidationOff {
		// Loud rather than silent: a decision made on unvalidated input is not
		// one anybody should rely on, and this mode exists only for tests.
		resolved.logger.Warn("rule engine boundary validation is off; this is for tests only",
			"tenant", config.Tenant, "domain", config.Domain)
	}
	instance := &Engine{
		config:  config,
		options: resolved,
		cache:   store.NewCache(store.WithMetrics(resolved.metrics), store.WithClock(resolved.clock)),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	instance.versions = store.NewVersionResolver(instance.cache, resolved.manifestStore)
	return instance, nil
}

// Reload loads a complete bundle and publishes it. A failure leaves whatever
// was serving in place.
func (e *Engine) Reload(ctx context.Context) error {
	if e.options.loader == nil {
		return fmt.Errorf("%w: no loader was supplied", ErrConfig)
	}
	if err := e.cache.Reload(ctx, e.options.loader); err != nil {
		return err
	}
	return e.rebuildFactResolver()
}

// ApplyBundle publishes a bundle the caller already has, for a test or for a
// service that loads rules its own way.
func (e *Engine) ApplyBundle(bundle store.Bundle) error {
	if err := e.cache.Replace(bundle); err != nil {
		return err
	}
	return e.rebuildFactResolver()
}

// Start begins background reconciliation, and is a no-op when
// CacheReloadInterval is zero: a service that wants no goroutine gets none.
//
// It is started only by this explicit call, never by New, so that a batch
// process can pin its rules for a whole run without a timer changing them
// underneath it. Starting twice is harmless.
func (e *Engine) Start() error {
	if e.config.CacheReloadInterval == 0 {
		return nil
	}
	if e.options.changesLoader == nil && e.options.loader == nil {
		return fmt.Errorf("%w: a cache reload interval needs a loader", ErrConfig)
	}
	e.reconcileOnce.Do(func() { go e.reconcile() })
	return nil
}

func (e *Engine) reconcile() {
	defer close(e.stopped)
	ticker := time.NewTicker(e.config.CacheReloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), e.config.CacheReloadInterval)
			var err error
			if e.options.changesLoader != nil {
				err = e.cache.Reconcile(ctx, e.options.changesLoader)
			} else {
				err = e.cache.Reload(ctx, e.options.loader)
			}
			if err == nil {
				err = e.rebuildFactResolver()
			}
			cancel()
			if err != nil {
				// A failed reconcile is reported and retried on the next tick.
				// The previous snapshot keeps serving, and the cache's
				// staleness metric is what tells an operator how old it is.
				e.options.logger.Warn("rule cache reconcile failed",
					"tenant", e.config.Tenant, "domain", e.config.Domain, "error", err.Error())
			}
		}
	}
}

// rebuildFactResolver rebuilds the fact resolver against the newly published
// snapshot, because the registry and the dataset manifests it closes over both
// come from the bundle.
func (e *Engine) rebuildFactResolver() error {
	snapshot := e.cache.Load()
	if snapshot == nil {
		return ErrNotReady
	}
	resolver, err := fact.NewResolver(
		snapshot.Registry(), snapshot.Manifests(), e.options.executor,
		e.options.natives, e.config.FactBudget, e.options.metrics,
	)
	if err != nil {
		return err
	}
	e.resolverM.Lock()
	e.facts = resolver
	e.resolverM.Unlock()
	return nil
}

func (e *Engine) factResolver() *fact.Resolver {
	e.resolverM.RLock()
	defer e.resolverM.RUnlock()
	return e.facts
}

// Ready reports whether the engine can decide anything yet.
//
// A service calls it from its own readiness probe, so that it does not accept
// traffic it would have to fail closed. An engine with no published snapshot,
// no hierarchy for its tenant, or no fact resolver is not ready.
func (e *Engine) Ready() error {
	if e == nil {
		return ErrNotReady
	}
	snapshot := e.cache.Load()
	if snapshot == nil {
		return fmt.Errorf("%w: no rule snapshot has been published", ErrNotReady)
	}
	hierarchySnapshot := snapshot.Hierarchy()
	if hierarchySnapshot == nil || hierarchySnapshot.Empty(e.config.Tenant) {
		return fmt.Errorf("%w: %w for tenant", ErrNotReady, ErrHierarchyUnavailable)
	}
	if e.factResolver() == nil {
		return fmt.Errorf("%w: no fact resolver is built", ErrNotReady)
	}
	return nil
}

// Stats reports what the engine currently holds, for an operational endpoint.
func (e *Engine) Stats() store.Stats { return e.cache.Stats() }

// Close stops background reconciliation and releases the audit logger. It is
// safe to call more than once.
func (e *Engine) Close() error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		close(e.stop)
		// Only wait for the goroutine if one was ever started.
		started := false
		e.reconcileOnce.Do(func() { started = true })
		if !started {
			<-e.stopped
		}
		if closer, ok := e.options.decisionLogger.(interface{ Close() error }); ok {
			e.closeErr = closer.Close()
		}
	})
	return e.closeErr
}

// Evaluate runs the six-stage pipeline for one decision point.
//
// A business refusal is a Decision with Failed set and a reason code, not a Go
// error. A Go error means the caller should retry, alert, or fix its
// configuration — see the package documentation.
func (e *Engine) Evaluate(ctx context.Context, request EvalRequest) (Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session := e.newSession(ctx)
	defer session.finish()
	return session.evaluate(ctx, request, Point{Key: request.Routing.RuleKey, DecisionType: ""})
}

// Point is one decision a service wants made about a subject.
type Point struct {
	// Key is the rule to evaluate.
	Key rule.RuleKey
	// DecisionType is the output contract the caller expects, checked against
	// the winning rule so a renamed contract is caught rather than silently
	// satisfied.
	DecisionType string
	// Optional records the point in Results but keeps it out of AllPassed, for
	// a check that informs rather than decides.
	Optional bool
}

// EvaluateAll evaluates several decision points for one subject.
//
// This is what a service actually calls: a leave application has a handful of
// checks, and a user should see every problem at once rather than one per
// submission. So every point is evaluated and every failure collected, rather
// than stopping at the first.
//
// Facts are resolved once across all points. A fact three points need is
// fetched once, and the budget covers the whole call rather than each point,
// because asking the same question three times would both exhaust the budget
// and risk three answers.
func (e *Engine) EvaluateAll(ctx context.Context, points []Point, request EvalRequest) (Results, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(points) == 0 {
		return Results{}, fmt.Errorf("%w: at least one decision point is required", ErrConfig)
	}
	session := e.newSession(ctx)
	defer session.finish()

	results := Results{decisions: make([]Decision, 0, len(points)), optional: make([]bool, 0, len(points))}
	for _, point := range points {
		pointRequest := request
		pointRequest.Routing.RuleKey = point.Key
		decision, err := session.evaluate(ctx, pointRequest, point)
		if err != nil {
			return Results{}, err
		}
		results.decisions = append(results.decisions, decision)
		results.optional = append(results.optional, point.Optional)
	}
	return results, nil
}

// Results is the outcome of evaluating several decision points.
type Results struct {
	decisions []Decision
	optional  []bool
}

// AllPassed reports whether every required point succeeded. An optional point
// contributes to Results but never to this answer.
func (r Results) AllPassed() bool {
	for index, decision := range r.decisions {
		if decision.Failed && !r.optional[index] {
			return false
		}
	}
	return true
}

// FirstFailure returns the first failed decision in the order the points were
// given, including an optional one, so a caller can report the earliest
// problem.
func (r Results) FirstFailure() (Decision, bool) {
	for _, decision := range r.decisions {
		if decision.Failed {
			return decision, true
		}
	}
	return Decision{}, false
}

// Failures returns every failed decision, which is what a caller shows a user
// who should see all their problems at once.
func (r Results) Failures() []Decision {
	failures := make([]Decision, 0, len(r.decisions))
	for _, decision := range r.decisions {
		if decision.Failed {
			failures = append(failures, decision)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return failures
}

// ByKey returns the decision for one rule key.
func (r Results) ByKey(key rule.RuleKey) (Decision, bool) {
	for _, decision := range r.decisions {
		if decision.RuleKey == key {
			return decision, true
		}
	}
	return Decision{}, false
}

// All returns every decision, in the order the points were given.
func (r Results) All() []Decision {
	return append([]Decision(nil), r.decisions...)
}

// session is one request's evaluation state: the fact session that makes
// de-duplication span every point, and the span that covers the whole call.
type session struct {
	engine *Engine
	facts  *fact.Session
	span   telemetry.Span
}

func (e *Engine) newSession(ctx context.Context) *session {
	_, span := e.options.tracer.Start(ctx, telemetry.SpanEvaluate)
	current := &session{engine: e, span: span}
	if resolver := e.factResolver(); resolver != nil {
		current.facts = resolver.NewSession()
	}
	return current
}

func (s *session) finish() { s.span.End() }

var _ Resolver = (*Engine)(nil)
