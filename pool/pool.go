package pool

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/telemetry"
)

// Factory builds the engine for one tenant. It decides where rules and the
// hierarchy come from; the pool knows nothing about that.
//
// The engine it returns must be configured for exactly the tenant it was asked
// for (Config.Tenant), must have a loader (ruleengine.WithLoader), and must not
// have been loaded or started: the pool calls Reload, then Start, then checks
// Ready before it publishes the engine. The factory controls background
// reconciliation through Config.CacheReloadInterval.
//
// The context carries the first caller's values but not its cancellation (see
// Pool.For), and is cancelled when the pool closes or the build timeout passes.
type Factory func(ctx context.Context, tenantID string) (*ruleengine.Engine, error)

// Eviction reasons, used as the single label on the eviction counter.
const (
	reasonIdle     = "idle"
	reasonCapacity = "capacity"
	reasonExplicit = "explicit"
	reasonClose    = "close"
)

// maxRememberedFailures bounds how many failed tenants are kept before expired
// ones are pruned on insert, so a stream of bad tenant IDs cannot grow memory
// without limit.
const maxRememberedFailures = 1024

// Pool creates, caches, warms and evicts one engine per tenant. It is safe for
// concurrent use. Build it with New and call Close on shutdown.
type Pool struct {
	factory Factory
	cfg     *config
	logger  *slog.Logger

	hits, misses, builds, buildFailures telemetry.Counter
	created, closedEngines              telemetry.Counter
	evictions                           map[string]telemetry.Counter

	// base is cancelled when the pool closes, which stops builds in flight.
	base       context.Context
	cancelBase context.CancelFunc

	mu       sync.Mutex
	closed   bool
	entries  map[string]*entry
	lru      *list.List // front is most recently used
	building map[string]*build
	failures map[string]*BuildError

	// closers counts removed engines whose Close has not finished, so Close
	// can wait for them. It is only added to under mu while the pool is open.
	closers sync.WaitGroup

	closeOnce sync.Once
	closeErr  error

	quit        chan struct{}
	janitorDone chan struct{}
}

// build is one in-flight build that every concurrent caller for the tenant
// waits on. err is written before done is closed.
type build struct {
	done chan struct{}
	err  error
}

// New returns a pool that builds engines with factory.
func New(factory Factory, opts ...Option) (*Pool, error) {
	if factory == nil {
		return nil, fmt.Errorf("%w: a pool needs an engine factory", ruleengine.ErrConfig)
	}
	cfg := defaultConfig()
	for _, option := range opts {
		if option != nil {
			option(cfg)
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	base, cancel := context.WithCancel(context.Background())
	metrics := cfg.metrics
	p := &Pool{
		factory:       factory,
		cfg:           cfg,
		logger:        cfg.logger,
		hits:          metrics.Counter(telemetry.MetricPoolHits),
		misses:        metrics.Counter(telemetry.MetricPoolMisses),
		builds:        metrics.Counter(telemetry.MetricPoolBuilds),
		buildFailures: metrics.Counter(telemetry.MetricPoolBuildFailures),
		created:       metrics.Counter(telemetry.MetricPoolEnginesCreated),
		closedEngines: metrics.Counter(telemetry.MetricPoolEnginesClosed),
		evictions:     make(map[string]telemetry.Counter, 4),
		base:          base,
		cancelBase:    cancel,
		entries:       make(map[string]*entry),
		lru:           list.New(),
		building:      make(map[string]*build),
		failures:      make(map[string]*BuildError),
		quit:          make(chan struct{}),
	}
	for _, reason := range []string{reasonIdle, reasonCapacity, reasonExplicit, reasonClose} {
		p.evictions[reason] = metrics.Counter(telemetry.MetricPoolEvictions,
			telemetry.Label{Key: telemetry.LabelReason, Value: reason})
	}
	if cfg.sweepInterval > 0 {
		ticks, stop := cfg.newTicker(cfg.sweepInterval)
		p.janitorDone = make(chan struct{})
		go p.janitor(ticks, stop)
	}
	return p, nil
}

// For returns the tenant's engine, building it on first use.
//
// Concurrent calls for the same tenant share one build; a slow or failing
// tenant never blocks another. The build runs on its own context, detached
// from the caller's cancellation, so one impatient caller cannot fail the
// build for the rest: a caller whose context ends stops waiting and gets its
// own context error, and the build carries on and is cached for the next
// caller. A failed build returns a *BuildError, which is remembered for the
// failure backoff.
func (p *Pool) For(ctx context.Context, tenantID string) (*Engine, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if ctx == nil {
		ctx = context.Background()
	}
	counted := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrPoolClosed
		}
		now := p.cfg.clock.Now()
		idle := p.sweepLocked(now)
		if e, ok := p.entries[tenantID]; ok {
			p.touchLocked(e, now)
			p.mu.Unlock()
			p.finishAsync(idle)
			if !counted {
				p.hits.Inc()
			}
			return e.handle, nil
		}
		if failure, ok := p.failures[tenantID]; ok {
			if now.Before(failure.RetryAt) {
				p.mu.Unlock()
				p.finishAsync(idle)
				return nil, failure
			}
			delete(p.failures, tenantID)
		}
		if b, ok := p.building[tenantID]; ok {
			p.mu.Unlock()
			p.finishAsync(idle)
			if !counted {
				p.misses.Inc()
				counted = true
			}
			select {
			case <-b.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if b.err != nil {
				return nil, b.err
			}
			continue
		}
		b := &build{done: make(chan struct{})}
		p.building[tenantID] = b
		p.mu.Unlock()
		p.finishAsync(idle)
		if !counted {
			p.misses.Inc()
			counted = true
		}
		buildCtx, cancel := p.buildContext(ctx)
		go func() {
			defer cancel()
			p.runBuild(buildCtx, tenantID, b)
		}()
		select {
		case <-b.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if b.err != nil {
			return nil, b.err
		}
	}
}

// Get returns the tenant's engine if the pool already holds it. It never
// builds, never blocks on a build and performs no I/O, so a service can call
// it inside a database transaction.
func (p *Pool) Get(tenantID string) (*Engine, bool) {
	if tenantID == "" {
		return nil, false
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, false
	}
	now := p.cfg.clock.Now()
	idle := p.sweepLocked(now)
	e, ok := p.entries[tenantID]
	if ok {
		p.touchLocked(e, now)
	}
	p.mu.Unlock()
	p.finishAsync(idle)
	if !ok {
		p.misses.Inc()
		return nil, false
	}
	p.hits.Inc()
	return e.handle, true
}

// Warm builds the engines for tenantIDs ahead of use, at the start of a
// service or when a tenant is created. Up to WithWarmConcurrency tenants build
// at once. It tries every tenant and returns the failures joined, so one bad
// tenant does not stop the rest.
func (p *Pool) Warm(ctx context.Context, tenantIDs ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	unique := make([]string, 0, len(tenantIDs))
	seen := make(map[string]struct{}, len(tenantIDs))
	for _, id := range tenantIDs {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	errs := make([]error, len(unique))
	sem := make(chan struct{}, p.cfg.warmConcurrency)
	var wg sync.WaitGroup
	for index, id := range unique {
		if id == "" {
			errs[index] = ErrEmptyTenantID
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			for rest := index; rest < len(unique); rest++ {
				errs[rest] = ctx.Err()
			}
			wg.Wait()
			return errors.Join(errs...)
		}
		wg.Go(func() {
			defer func() { <-sem }()
			_, errs[index] = p.For(ctx, id)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Evict removes the tenant's engine, for example when the tenant is deleted,
// and forgets any remembered build failure so the next request tries again.
// The engine is closed once its running evaluations finish. It reports whether
// an engine was removed.
func (p *Pool) Evict(tenantID string) bool {
	if tenantID == "" {
		return false
	}
	p.mu.Lock()
	delete(p.failures, tenantID)
	e, ok := p.entries[tenantID]
	if ok {
		p.removeLocked(e, reasonExplicit)
	}
	p.mu.Unlock()
	if ok {
		p.finish([]*entry{e})
	}
	return ok
}

// Sweep removes every engine that has been idle longer than the idle TTL and
// forgets expired build failures. It reports how many engines were removed. It
// is a no-op without WithIdleTTL, apart from forgetting failures.
func (p *Pool) Sweep() int {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0
	}
	now := p.cfg.clock.Now()
	idle := p.sweepLocked(now)
	p.pruneFailuresLocked(now)
	p.mu.Unlock()
	p.finish(idle)
	return len(idle)
}

// Len returns how many engines the pool currently holds.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// Close stops the janitor and in-flight builds and closes every engine,
// waiting for running evaluations to finish within ctx and the close timeout.
// An engine that is still evaluating at the deadline is not closed underneath
// that evaluation: Close returns ErrCloseTimeout, and the engine is closed when
// its last evaluation ends. Close is safe to call more than once and returns
// the first call's result.
//
// A decision logger shared across engines is not closed here; the service owns
// it and closes it once, after Close returns (see NoCloseLogger).
func (p *Pool) Close(ctx context.Context) error {
	p.closeOnce.Do(func() { p.closeErr = p.shutdown(ctx) })
	return p.closeErr
}

func (p *Pool) shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if p.cfg.closeTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.cfg.closeTimeout)
		defer cancel()
	}

	p.mu.Lock()
	p.closed = true
	entries := make([]*entry, 0, len(p.entries))
	for _, e := range p.entries {
		entries = append(entries, e)
		p.evictions[reasonClose].Inc()
		p.closers.Add(1)
	}
	p.entries = make(map[string]*entry)
	p.lru.Init()
	pending := make([]*build, 0, len(p.building))
	for _, b := range p.building {
		pending = append(pending, b)
	}
	p.failures = make(map[string]*BuildError)
	p.mu.Unlock()

	p.cancelBase()
	close(p.quit)

	// Retire in parallel, so N engines do not cost N drains. An engine that is
	// still evaluating is closed by its last evaluation instead.
	for _, e := range entries {
		if e.retire() {
			go p.closeEngine(e)
		}
	}

	drained := make(chan struct{})
	go func() {
		p.closers.Wait()
		close(drained)
	}()
	timedOut := false
	wait := func(done <-chan struct{}) {
		if timedOut {
			return
		}
		select {
		case <-done:
		case <-ctx.Done():
			timedOut = true
		}
	}
	if p.janitorDone != nil {
		wait(p.janitorDone)
	}
	wait(drained)
	for _, b := range pending {
		wait(b.done)
	}

	var errs []error
	if timedOut {
		errs = append(errs, ErrCloseTimeout)
	}
	for _, e := range entries {
		select {
		case <-e.closed:
			if e.closeErr != nil {
				errs = append(errs, e.closeErr)
			}
		default:
		}
	}
	return errors.Join(errs...)
}

// buildContext derives the build's context: the caller's values, none of its
// cancellation, ending when the pool closes or the build timeout passes.
func (p *Pool) buildContext(caller context.Context) (context.Context, context.CancelFunc) {
	ctx := context.WithoutCancel(caller)
	var cancel context.CancelFunc
	if p.cfg.buildTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, p.cfg.buildTimeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	stop := context.AfterFunc(p.base, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// runBuild builds one tenant's engine and publishes the outcome. It runs on its
// own goroutine and finishes by closing b.done, so every waiter and Close can
// wait for it.
func (p *Pool) runBuild(ctx context.Context, tenantID string, b *build) {
	p.builds.Inc()
	engine, err := p.construct(ctx, tenantID)

	var (
		discard *ruleengine.Engine
		retired []*entry
	)
	p.mu.Lock()
	delete(p.building, tenantID)
	switch {
	case p.closed:
		discard, b.err = engine, ErrPoolClosed
	case err != nil:
		p.buildFailures.Inc()
		failure := &BuildError{Tenant: tenantID, Err: err}
		if p.cfg.failureBackoff > 0 {
			now := p.cfg.clock.Now()
			failure.RetryAt = now.Add(p.cfg.failureBackoff)
			if len(p.failures) >= maxRememberedFailures {
				p.pruneFailuresLocked(now)
			}
			p.failures[tenantID] = failure
		}
		b.err = failure
	default:
		e := newEntry(p, tenantID, engine)
		e.lastUsed = p.cfg.clock.Now()
		e.elem = p.lru.PushFront(e)
		p.entries[tenantID] = e
		p.created.Inc()
		retired = p.enforceCapacityLocked()
	}
	p.mu.Unlock()

	if discard != nil {
		p.closeQuietly(discard)
	}
	p.finish(retired)
	close(b.done)
}

// construct runs the factory and then makes the engine ready: Reload, Start,
// Ready. An engine that fails any step is closed and never published.
func (p *Pool) construct(ctx context.Context, tenantID string) (*ruleengine.Engine, error) {
	engine, err := p.callFactory(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if engine == nil {
		return nil, errors.New("factory returned a nil engine")
	}
	fail := func(cause error) (*ruleengine.Engine, error) {
		p.closeQuietly(engine)
		return nil, cause
	}
	if engine.Tenant() != tenantID {
		return fail(ErrTenantMismatch)
	}
	if err := engine.Reload(ctx); err != nil {
		return fail(fmt.Errorf("reload: %w", err))
	}
	if err := engine.Start(); err != nil {
		return fail(fmt.Errorf("start: %w", err))
	}
	if err := engine.Ready(); err != nil {
		return fail(fmt.Errorf("%w: %w", ErrNotReady, err))
	}
	return engine, nil
}

// callFactory shields the pool from a panicking factory, which would otherwise
// leave every waiter for the tenant blocked forever.
func (p *Pool) callFactory(ctx context.Context, tenantID string) (engine *ruleengine.Engine, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			engine, err = nil, fmt.Errorf("factory panicked: %T", recovered)
		}
	}()
	return p.factory(ctx, tenantID)
}

// touchLocked marks an entry as just used. The caller holds p.mu.
func (p *Pool) touchLocked(e *entry, now time.Time) {
	e.lastUsed = now
	p.lru.MoveToFront(e.elem)
}

// sweepLocked removes idle engines and returns them to be finished outside the
// lock. The list is ordered by last use, so only its back is ever examined.
// The caller holds p.mu.
func (p *Pool) sweepLocked(now time.Time) []*entry {
	if p.cfg.idleTTL <= 0 {
		return nil
	}
	var idle []*entry
	for back := p.lru.Back(); back != nil; back = p.lru.Back() {
		e, _ := back.Value.(*entry)
		if now.Sub(e.lastUsed) < p.cfg.idleTTL {
			break
		}
		p.removeLocked(e, reasonIdle)
		idle = append(idle, e)
	}
	return idle
}

// enforceCapacityLocked evicts least recently used engines beyond the cap. The
// caller holds p.mu.
func (p *Pool) enforceCapacityLocked() []*entry {
	if p.cfg.maxTenants <= 0 {
		return nil
	}
	var evicted []*entry
	for len(p.entries) > p.cfg.maxTenants {
		e, _ := p.lru.Back().Value.(*entry)
		p.removeLocked(e, reasonCapacity)
		evicted = append(evicted, e)
	}
	return evicted
}

// removeLocked takes an entry out of the pool's maps. The caller holds p.mu and
// must pass the entry to finish afterwards, outside the lock.
func (p *Pool) removeLocked(e *entry, reason string) {
	delete(p.entries, e.tenant)
	p.lru.Remove(e.elem)
	p.evictions[reason].Inc()
	p.closers.Add(1)
}

func (p *Pool) pruneFailuresLocked(now time.Time) {
	for tenant, failure := range p.failures {
		if !now.Before(failure.RetryAt) {
			delete(p.failures, tenant)
		}
	}
}

// finish retires removed entries. An entry with no running call is closed now;
// otherwise the last call to finish closes it.
func (p *Pool) finish(removed []*entry) {
	for _, e := range removed {
		if e.retire() {
			p.closeEngine(e)
		}
	}
}

// finishAsync is finish for the lazy paths, so a request that happens to trip
// an idle eviction does not wait for another tenant's engine to drain.
func (p *Pool) finishAsync(removed []*entry) {
	if len(removed) > 0 {
		go p.finish(removed)
	}
}

// closeEngine closes a pooled engine. It is called exactly once per entry, by
// whichever goroutine claimed it through retire or release.
func (p *Pool) closeEngine(e *entry) {
	defer p.closers.Done()
	e.closeErr = e.engine.Close()
	p.closedEngines.Inc()
	if e.closeErr != nil {
		p.logger.Warn("pool engine close failed", "errorType", fmt.Sprintf("%T", e.closeErr))
	}
	close(e.closed)
}

// closeQuietly closes an engine that was never published.
func (p *Pool) closeQuietly(engine *ruleengine.Engine) {
	if err := engine.Close(); err != nil {
		p.logger.Warn("pool discarded engine close failed", "errorType", fmt.Sprintf("%T", err))
	}
}

// janitor sweeps on every tick until the pool closes.
func (p *Pool) janitor(ticks <-chan time.Time, stop func()) {
	defer close(p.janitorDone)
	defer stop()
	for {
		select {
		case <-p.quit:
			return
		case <-ticks:
			p.Sweep()
		}
	}
}
