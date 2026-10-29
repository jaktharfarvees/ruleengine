package ruleengine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arrowhr/ruleengine/audit"
	"github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/manifest"
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
	config   Config
	options  *options
	cache    *store.Cache
	versions *store.VersionResolver
	runtime  atomic.Pointer[runtimeSnapshot]

	reconcileOnce sync.Once
	stop          chan struct{}
	stopped       chan struct{}
	closeOnce     sync.Once
	closeErr      error
}

// runtimeSnapshot is the single immutable view an evaluation uses from start
// to finish. Publishing the store snapshot and its fact resolver together
// prevents a reload from combining rules from one bundle with registry or
// dataset metadata from another.
type runtimeSnapshot struct {
	store *store.Snapshot
	facts *fact.Resolver
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
	if config.RequireDurableAudit {
		if _, durable := resolved.decisionLogger.(audit.DurableDecisionLogger); !durable {
			return nil, fmt.Errorf("%w: durable audit is required but the decision logger is best-effort", ErrConfig)
		}
	}
	if config.RequireIdempotency && resolved.idempotencyStore == nil {
		return nil, fmt.Errorf("%w: idempotency is required but no idempotency store was supplied", ErrConfig)
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
		cache: store.NewCache(store.WithMetrics(resolved.metrics), store.WithClock(resolved.clock),
			store.WithRuleCheck(engineRefCheck(resolved.engine))),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	instance.versions = store.NewVersionResolver(instance.cache, resolved.manifestStore)
	return instance, nil
}

// engineRefCheck rejects a rule whose engineRef names an engine other than the
// one this Engine evaluates with. Every rule in a bundle is evaluated by that
// one engine, so a mismatch would otherwise surface only when the rule was
// selected, as a failed decision rather than a failed load.
func engineRefCheck(configured engine.Engine) func(rule.RuleDefinition) error {
	return func(definition rule.RuleDefinition) error {
		if name := configured.Name(); definition.EngineRef.Engine != name {
			return fmt.Errorf("%w: rule %q declares engineRef.engine %q but the configured engine is %q",
				ErrConfig, definition.RuleKey, definition.EngineRef.Engine, name)
		}
		return nil
	}
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
					"tenant", e.config.Tenant, "domain", e.config.Domain,
					"errorType", fmt.Sprintf("%T", err))
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
	resolver, err := fact.NewResolverWithTracer(
		snapshot.Registry(), snapshot.Manifests(), e.options.executor,
		e.options.natives, e.config.FactBudget, e.options.metrics, e.options.tracer,
	)
	if err != nil {
		return err
	}
	e.runtime.Store(&runtimeSnapshot{store: snapshot, facts: resolver})
	return nil
}

func (e *Engine) runtimeSnapshot() *runtimeSnapshot {
	if e == nil {
		return nil
	}
	return e.runtime.Load()
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
	runtime := e.runtimeSnapshot()
	if runtime == nil || runtime.store == nil || runtime.facts == nil {
		return fmt.Errorf("%w: no rule snapshot has been published", ErrNotReady)
	}
	snapshot := runtime.store
	hierarchySnapshot := snapshot.Hierarchy()
	if hierarchySnapshot == nil || hierarchySnapshot.Empty(e.config.Tenant) {
		return fmt.Errorf("%w: %w for tenant", ErrNotReady, ErrHierarchyUnavailable)
	}
	return nil
}

// Tenant returns the tenant this engine was configured for, which is fixed for
// its lifetime. A caller that builds engines for many tenants uses it to check
// that the engine it received is the one it asked for.
func (e *Engine) Tenant() string {
	if e == nil {
		return ""
	}
	return e.config.Tenant
}

// Stats reports what the engine currently holds, for an operational endpoint.
func (e *Engine) Stats() store.Stats {
	if runtime := e.runtimeSnapshot(); runtime != nil && runtime.store != nil {
		return runtime.store.Stats()
	}
	return store.Stats{}
}

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
	point := Point{Key: request.Routing.RuleKey, DecisionType: ""}
	if e.config.RequireIdempotency && validUUID(request.IdempotencyKey) {
		stored, err := e.idempotent(ctx, request, []Point{point}, func(callCtx context.Context) (IdempotencyResult, error) {
			decision, evaluateErr := e.evaluateOnce(callCtx, request, point)
			return IdempotencyResult{Decisions: []Decision{decision}}, evaluateErr
		})
		if err != nil {
			return Decision{}, err
		}
		if len(stored.Decisions) != 1 {
			return Decision{}, &sanitizedOperationalError{
				message: "idempotency store returned an invalid single-decision result",
				cause:   ErrIdempotency,
			}
		}
		return stored.Decisions[0], nil
	}
	return e.evaluateOnce(ctx, request, point)
}

func (e *Engine) evaluateOnce(ctx context.Context, request EvalRequest, point Point) (Decision, error) {
	session := e.newSession(ctx, request)
	defer session.finish()
	return session.evaluate(request, point)
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
	if e.config.RequireIdempotency && validUUID(request.IdempotencyKey) {
		stored, err := e.idempotent(ctx, request, points, func(callCtx context.Context) (IdempotencyResult, error) {
			results, evaluateErr := e.evaluateAllOnce(callCtx, points, request)
			return IdempotencyResult{Decisions: results.decisions, Optional: results.optional}, evaluateErr
		})
		if err != nil {
			return Results{}, err
		}
		if len(stored.Decisions) != len(points) || len(stored.Optional) != len(points) {
			return Results{}, &sanitizedOperationalError{
				message: "idempotency store returned an invalid multi-decision result",
				cause:   ErrIdempotency,
			}
		}
		return Results{decisions: stored.Decisions, optional: stored.Optional}, nil
	}
	return e.evaluateAllOnce(ctx, points, request)
}

func (e *Engine) evaluateAllOnce(ctx context.Context, points []Point, request EvalRequest) (Results, error) {
	session := e.newSession(ctx, request)
	defer session.finish()

	results := Results{decisions: make([]Decision, 0, len(points)), optional: make([]bool, 0, len(points))}
	for _, point := range points {
		pointRequest := request
		pointRequest.Routing.RuleKey = point.Key
		decision, err := session.evaluate(pointRequest, point)
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
	engine       *Engine
	ctx          context.Context
	snapshot     *store.Snapshot
	facts        *fact.Session
	span         telemetry.Span
	runManifests map[string]manifest.RunManifest
}

func (e *Engine) newSession(ctx context.Context, request EvalRequest) *session {
	ctx = telemetry.ContextWithPropagation(ctx, telemetry.Propagation{
		Traceparent: request.Traceparent, CorrelationID: request.CorrelationID,
	})
	traceContext, span := e.options.tracer.Start(ctx, telemetry.SpanEvaluate)
	current := &session{engine: e, ctx: traceContext, span: span}
	if runtime := e.runtimeSnapshot(); runtime != nil {
		current.snapshot = runtime.store
		if runtime.facts != nil {
			current.facts = runtime.facts.NewSession()
		}
	}
	return current
}

// refresh adopts the latest fully-built runtime after an explicit loader
// retry. It never observes the cache's intermediate state.
func (s *session) refresh() {
	if runtime := s.engine.runtimeSnapshot(); runtime != nil {
		s.snapshot = runtime.store
		if runtime.facts != nil {
			s.facts = runtime.facts.NewSession()
		}
	}
}

// runManifest returns one immutable local view for the whole evaluation
// session. EvaluateAll therefore cannot observe different manifest state
// between decision points and performs only one local lookup per run ID.
func (s *session) runManifest(tenant, runID string) (manifest.RunManifest, error) {
	if run, found := s.runManifests[runID]; found {
		if run.Tenant != tenant {
			return manifest.RunManifest{}, fmt.Errorf("%w: run %q", store.ErrRunTenantMismatch, runID)
		}
		return run, nil
	}
	run, err := s.engine.versions.RunManifest(tenant, runID)
	if err != nil {
		return manifest.RunManifest{}, err
	}
	if s.runManifests == nil {
		s.runManifests = make(map[string]manifest.RunManifest)
	}
	s.runManifests[runID] = run
	return run, nil
}

func (s *session) finish() { s.span.End() }

var _ Resolver = (*Engine)(nil)
