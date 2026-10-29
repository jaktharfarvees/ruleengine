package pool

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/audit"
	"github.com/arrowhr/ruleengine/telemetry"
)

func mustFor(t *testing.T, p *Pool, tenant string) *Engine {
	t.Helper()
	engine, err := p.For(context.Background(), tenant)
	if err != nil {
		t.Fatalf("For(%q) = %v", tenant, err)
	}
	return engine
}

func TestForBuildsOnceUnderConcurrency(t *testing.T) {
	e := newEnv()
	gate := make(chan struct{})
	e.gates["A"] = gate
	p, _ := newPool(t, e)

	const callers = 64
	handles := make([]*Engine, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() { handles[i], errs[i] = p.For(context.Background(), "A") })
	}
	// Hold the build until the factory has been entered, so the other callers
	// are certain to find it in flight.
	if tenant := <-e.entered; tenant != "A" {
		t.Fatalf("factory entered for %q", tenant)
	}
	close(gate)
	wg.Wait()

	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if handles[i] != handles[0] {
			t.Fatalf("caller %d got a different engine", i)
		}
	}
	if got := e.count("A"); got != 1 {
		t.Fatalf("factory ran %d times, want 1", got)
	}
}

func TestGetNeverBuilds(t *testing.T) {
	e := newEnv()
	p, _ := newPool(t, e)

	if engine, ok := p.Get("A"); ok || engine != nil {
		t.Fatalf("Get on an empty pool = %v, %v", engine, ok)
	}
	if got := e.count("A"); got != 0 {
		t.Fatalf("Get ran the factory %d times", got)
	}
	built := mustFor(t, p, "A")
	got, ok := p.Get("A")
	if !ok || got != built {
		t.Fatalf("Get after For = %v, %v, want the built engine", got, ok)
	}
	// A tenant whose build failed is not in the pool, and Get does not retry it.
	e.fail["bad"] = errors.New("down")
	if _, err := p.For(context.Background(), "bad"); err == nil {
		t.Fatal("expected a build failure")
	}
	before := e.count("bad")
	if _, ok := p.Get("bad"); ok || e.count("bad") != before {
		t.Fatal("Get must not build a failed tenant")
	}
}

func TestWarm(t *testing.T) {
	e := newEnv()
	p, _ := newPool(t, e, WithWarmConcurrency(2))

	if err := p.Warm(context.Background(), "A", "B", "C", "A"); err != nil {
		t.Fatalf("Warm = %v", err)
	}
	if p.Len() != 3 {
		t.Fatalf("Len = %d, want 3", p.Len())
	}
	for _, tenant := range []string{"A", "B", "C"} {
		if e.count(tenant) != 1 {
			t.Fatalf("%s built %d times, want 1 (duplicates are collapsed)", tenant, e.count(tenant))
		}
		if _, ok := p.Get(tenant); !ok {
			t.Fatalf("%s is not warm", tenant)
		}
	}
}

func TestWarmReportsFailuresAndKeepsGoing(t *testing.T) {
	e := newEnv()
	e.fail["bad"] = errors.New("down")
	p, _ := newPool(t, e)

	err := p.Warm(context.Background(), "A", "bad", "", "B")
	if !errors.Is(err, ErrBuildFailed) || !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("Warm = %v, want build failure and empty-ID errors joined", err)
	}
	for _, tenant := range []string{"A", "B"} {
		if _, ok := p.Get(tenant); !ok {
			t.Fatalf("%s should be warm despite the other failures", tenant)
		}
	}
	if err := p.Warm(context.Background()); err != nil {
		t.Fatalf("Warm with no tenants = %v", err)
	}
}

func TestWarmWithCancelledContext(t *testing.T) {
	e := newEnv()
	p, _ := newPool(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Warm(ctx, "A", "B"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Warm = %v, want context.Canceled", err)
	}
}

func TestFailureBackoff(t *testing.T) {
	e := newEnv()
	cause := errors.New("rule service down")
	e.fail["bad"] = cause
	p, clk := newPool(t, e, WithFailureBackoff(time.Minute))

	_, err := p.For(context.Background(), "bad")
	var buildErr *BuildError
	if !errors.As(err, &buildErr) || !errors.Is(err, ErrBuildFailed) || !errors.Is(err, cause) {
		t.Fatalf("For = %v, want a *BuildError wrapping the cause", err)
	}
	if buildErr.Tenant != "bad" || !buildErr.RetryAt.Equal(clk.Now().Add(time.Minute)) {
		t.Fatalf("BuildError = %+v", buildErr)
	}

	clk.Advance(30 * time.Second)
	for range 5 {
		if _, err := p.For(context.Background(), "bad"); !errors.Is(err, ErrBuildFailed) {
			t.Fatalf("For inside the backoff = %v", err)
		}
	}
	if got := e.count("bad"); got != 1 {
		t.Fatalf("factory ran %d times inside the backoff, want 1", got)
	}

	clk.Advance(31 * time.Second)
	delete(e.fail, "bad")
	if _, err := p.For(context.Background(), "bad"); err != nil {
		t.Fatalf("For after the backoff = %v", err)
	}
	if got := e.count("bad"); got != 2 {
		t.Fatalf("factory ran %d times, want 2 (one retry after the backoff)", got)
	}
}

func TestZeroBackoffRetriesEveryTime(t *testing.T) {
	e := newEnv()
	e.fail["bad"] = errors.New("down")
	p, _ := newPool(t, e, WithFailureBackoff(0))
	for range 3 {
		_, _ = p.For(context.Background(), "bad")
	}
	if got := e.count("bad"); got != 3 {
		t.Fatalf("factory ran %d times, want 3", got)
	}
}

func TestOneTenantFailingDoesNotAffectAnother(t *testing.T) {
	e := newEnv()
	e.fail["bad"] = errors.New("down")
	badGate := make(chan struct{})
	e.gates["slow"] = badGate
	p, _ := newPool(t, e)

	good := mustFor(t, p, "good")

	// A tenant whose build is stuck must not hold up an unrelated one.
	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		_, _ = p.For(context.Background(), "slow")
	}()
	for tenant := range e.entered {
		if tenant == "slow" {
			break
		}
	}
	other := mustFor(t, p, "other")
	if _, err := p.For(context.Background(), "bad"); !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("bad tenant = %v", err)
	}
	if got, ok := p.Get("good"); !ok || got != good {
		t.Fatal("the failing tenant disturbed an existing engine")
	}
	if err := other.Ready(); err != nil {
		t.Fatalf("other tenant Ready = %v", err)
	}
	if _, err := good.Evaluate(context.Background(), request("good")); err != nil {
		t.Fatalf("good tenant Evaluate = %v", err)
	}
	close(badGate)
	waitFor(t, slowDone, "the slow build")
}

func TestFailureIsNotRememberedForCallerCancellation(t *testing.T) {
	e := newEnv()
	gate := make(chan struct{})
	e.gates["A"] = gate
	p, _ := newPool(t, e)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := p.For(ctx, "A")
		result <- err
	}()
	<-e.entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("For = %v, want the caller's own context error", err)
	}

	p.mu.Lock()
	remembered := len(p.failures)
	p.mu.Unlock()
	if remembered != 0 {
		t.Fatalf("%d failures remembered for a caller's own cancellation", remembered)
	}

	// The build carried on, so the next caller is served by it.
	close(gate)
	if _, err := p.For(context.Background(), "A"); err != nil {
		t.Fatalf("For after cancellation = %v", err)
	}
	if got := e.count("A"); got != 1 {
		t.Fatalf("factory ran %d times, want 1", got)
	}
}

func TestFailureIsNotRememberedWhenThePoolCloses(t *testing.T) {
	e := newEnv()
	e.gates["A"] = make(chan struct{}) // never opened: the build ends only when the pool closes
	metrics := newRecMetrics()
	p, _ := newPool(t, e, WithMetrics(metrics))

	result := make(chan error, 1)
	go func() {
		_, err := p.For(context.Background(), "A")
		result <- err
	}()
	<-e.entered
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := <-result; !errors.Is(err, ErrPoolClosed) || errors.Is(err, ErrBuildFailed) {
		t.Fatalf("For = %v, want ErrPoolClosed and not a build failure", err)
	}
	if got := metrics.total(telemetry.MetricPoolBuildFailures); got != 0 {
		t.Fatalf("build failures = %v, want 0", got)
	}
}

func TestBuildFailuresThatAreRealAreRemembered(t *testing.T) {
	t.Run("tenant mismatch", func(t *testing.T) {
		e := newEnv()
		e.override["A"] = "B"
		p, _ := newPool(t, e)
		_, err := p.For(context.Background(), "A")
		if !errors.Is(err, ErrTenantMismatch) || !errors.Is(err, ErrBuildFailed) {
			t.Fatalf("For = %v, want ErrTenantMismatch", err)
		}
		if p.Len() != 0 {
			t.Fatal("a mismatched engine was published")
		}
		waitFor(t, e.spyFor("A").closed, "the rejected engine to be closed")
	})
	t.Run("not ready", func(t *testing.T) {
		e := newEnv()
		e.noHier["A"] = true
		p, _ := newPool(t, e)
		_, err := p.For(context.Background(), "A")
		if !errors.Is(err, ErrNotReady) || !errors.Is(err, ruleengine.ErrNotReady) {
			t.Fatalf("For = %v, want ErrNotReady", err)
		}
		waitFor(t, e.spyFor("A").closed, "the unready engine to be closed")
	})
	t.Run("panicking factory", func(t *testing.T) {
		e := newEnv()
		e.panics["A"] = true
		p, _ := newPool(t, e)
		if _, err := p.For(context.Background(), "A"); !errors.Is(err, ErrBuildFailed) {
			t.Fatalf("For = %v, want a build failure", err)
		}
		mustFor(t, p, "B")
	})
	t.Run("nil engine", func(t *testing.T) {
		p, err := New(func(context.Context, string) (*ruleengine.Engine, error) { return nil, nil })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Close(context.Background()) })
		if _, err := p.For(context.Background(), "A"); !errors.Is(err, ErrBuildFailed) {
			t.Fatalf("For = %v, want a build failure", err)
		}
	})
}

func TestIdleEviction(t *testing.T) {
	e := newEnv()
	p, clk := newPool(t, e, WithIdleTTL(10*time.Minute))

	mustFor(t, p, "A")
	mustFor(t, p, "B")
	clk.Advance(6 * time.Minute)
	if _, ok := p.Get("B"); !ok { // B is used again; A is not
		t.Fatal("B should still be pooled")
	}
	clk.Advance(6 * time.Minute) // A idle 12m, B idle 6m

	if _, ok := p.Get("B"); !ok {
		t.Fatal("B was evicted while it was in use")
	}
	waitFor(t, e.spyFor("A").closed, "the idle engine to be closed")
	if _, ok := p.Get("A"); ok {
		t.Fatal("A should have been evicted for idleness")
	}
	if p.Len() != 1 {
		t.Fatalf("Len = %d, want 1", p.Len())
	}
	if e.spyFor("B").isClosed.Load() {
		t.Fatal("B was closed")
	}
	// An evicted tenant is built again on demand.
	mustFor(t, p, "A")
	if got := e.count("A"); got != 2 {
		t.Fatalf("A built %d times, want 2", got)
	}
}

func TestSweepIsExplicit(t *testing.T) {
	e := newEnv()
	p, clk := newPool(t, e, WithIdleTTL(time.Minute))
	mustFor(t, p, "A")
	mustFor(t, p, "B")
	if got := p.Sweep(); got != 0 {
		t.Fatalf("Sweep removed %d engines before any were idle", got)
	}
	clk.Advance(2 * time.Minute)
	if got := p.Sweep(); got != 2 {
		t.Fatalf("Sweep removed %d engines, want 2", got)
	}
	if !e.spyFor("A").isClosed.Load() || !e.spyFor("B").isClosed.Load() {
		t.Fatal("Sweep returned before the engines were closed")
	}
	if p.Len() != 0 {
		t.Fatalf("Len = %d, want 0", p.Len())
	}
}

func TestNoIdleTTLNeverEvicts(t *testing.T) {
	e := newEnv()
	p, clk := newPool(t, e)
	mustFor(t, p, "A")
	clk.Advance(1000 * time.Hour)
	if _, ok := p.Get("A"); !ok || p.Sweep() != 0 {
		t.Fatal("an engine was evicted although no idle TTL was set")
	}
}

func TestMaxTenantsEvictsLeastRecentlyUsed(t *testing.T) {
	e := newEnv()
	p, clk := newPool(t, e, WithMaxTenants(2))

	mustFor(t, p, "A")
	clk.Advance(time.Second)
	mustFor(t, p, "B")
	clk.Advance(time.Second)
	if _, ok := p.Get("A"); !ok { // A is now more recent than B
		t.Fatal("A missing")
	}
	mustFor(t, p, "C")

	if p.Len() != 2 {
		t.Fatalf("Len = %d, want 2", p.Len())
	}
	if _, ok := p.Get("B"); ok {
		t.Fatal("B was the least recently used and should have been evicted")
	}
	for _, tenant := range []string{"A", "C"} {
		if _, ok := p.Get(tenant); !ok {
			t.Fatalf("%s should have survived", tenant)
		}
	}
	if !e.spyFor("B").isClosed.Load() {
		t.Fatal("the evicted engine was not closed")
	}
}

func TestEvictRemovesEngineAndForgetsFailure(t *testing.T) {
	e := newEnv()
	p, _ := newPool(t, e)
	mustFor(t, p, "A")
	if !p.Evict("A") || p.Evict("A") {
		t.Fatal("Evict should report true once, then false")
	}
	if !e.spyFor("A").isClosed.Load() {
		t.Fatal("Evict returned before closing the idle engine")
	}

	e.fail["bad"] = errors.New("down")
	_, _ = p.For(context.Background(), "bad")
	delete(e.fail, "bad")
	p.Evict("bad")
	if _, err := p.For(context.Background(), "bad"); err != nil {
		t.Fatalf("For after Evict cleared the failure = %v", err)
	}
}

func TestEngineIsNeverClosedWhileEvaluating(t *testing.T) {
	e := newEnv()
	e.logGate = make(chan struct{})
	e.logEnter = make(chan struct{}, 4)
	p, _ := newPool(t, e)
	handle := mustFor(t, p, "A")
	logger := e.spyFor("A")

	finished := make(chan struct{})
	var decisionErr error
	go func() {
		defer close(finished)
		_, decisionErr = handle.Evaluate(context.Background(), request("A"))
	}()
	waitFor(t, e.logEnter, "the evaluation to start") // Evaluate is now inside the logger

	if !p.Evict("A") {
		t.Fatal("Evict reported nothing to evict")
	}
	if logger.isClosed.Load() {
		t.Fatal("the engine was closed under a running Evaluate")
	}
	if _, err := handle.Evaluate(context.Background(), request("A")); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("Evaluate on a retired engine = %v, want ErrEngineClosed", err)
	}
	if _, ok := p.Get("A"); ok {
		t.Fatal("the retired engine is still handed out")
	}

	close(e.logGate)
	waitFor(t, finished, "the evaluation to finish")
	if decisionErr != nil {
		t.Fatalf("the running Evaluate failed: %v", decisionErr)
	}
	waitFor(t, logger.closed, "the engine to close after its last evaluation")
	if got := logger.violations.Load(); got != 0 {
		t.Fatalf("%d use-after-close violations", got)
	}
}

// TestLastReleaseClosesTheEngineOffTheCallersGoroutine pins that a request
// which happens to be the last call on a retired engine returns promptly even
// though the engine's Close is slow.
func TestLastReleaseClosesTheEngineOffTheCallersGoroutine(t *testing.T) {
	e := newEnv()
	e.logGate = make(chan struct{})
	e.logEnter = make(chan struct{}, 4)
	e.closeGate = make(chan struct{})
	e.closeEntered = make(chan struct{}, 4)
	p, _ := newPool(t, e)
	handle := mustFor(t, p, "A")
	logger := e.spyFor("A")

	returned := make(chan error, 1)
	go func() {
		_, err := handle.Evaluate(context.Background(), request("A"))
		returned <- err
	}()
	waitFor(t, e.logEnter, "the evaluation to start")
	if !p.Evict("A") {
		t.Fatal("Evict reported nothing to evict")
	}

	// The evaluation is now the last call on a retired entry. Releasing it
	// must start the slow Close without waiting for it.
	close(e.logGate)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Evaluate failed: %v", err)
		}
	case <-time.After(guard):
		t.Fatal("Evaluate waited for the engine's slow Close")
	}
	waitFor(t, e.closeEntered, "the engine's Close to start")
	if logger.isClosed.Load() {
		t.Fatal("the slow Close finished before it was released")
	}

	// Pool.Close still waits for the tracked close rather than leaking it.
	closed := make(chan error, 1)
	go func() { closed <- p.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Pool.Close returned (%v) while an engine was still closing", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(e.closeGate)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Pool.Close = %v", err)
		}
	case <-time.After(guard):
		t.Fatal("Pool.Close did not return after the engine closed")
	}
	waitFor(t, logger.closed, "the engine to close")
}

func TestCloseDoesNotCloseAnEngineThatIsStillEvaluating(t *testing.T) {
	e := newEnv()
	e.logGate = make(chan struct{})
	e.logEnter = make(chan struct{}, 4)
	p, _ := newPool(t, e)
	handle := mustFor(t, p, "A")
	logger := e.spyFor("A")

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = handle.Evaluate(context.Background(), request("A"))
	}()
	waitFor(t, e.logEnter, "the evaluation to start")

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Close(expired); !errors.Is(err, ErrCloseTimeout) {
		t.Fatalf("Close = %v, want ErrCloseTimeout", err)
	}
	if logger.isClosed.Load() {
		t.Fatal("Close closed an engine under a running Evaluate")
	}

	close(e.logGate)
	waitFor(t, finished, "the evaluation to finish")
	waitFor(t, logger.closed, "the engine to close after its last evaluation")
	if got := logger.violations.Load(); got != 0 {
		t.Fatalf("%d use-after-close violations", got)
	}
}

func TestEvictionRacingEvaluations(t *testing.T) {
	e := newEnv()
	p, _ := newPool(t, e, WithMaxTenants(2))
	tenants := []string{"A", "B", "C", "D"}

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for i := range 100 {
				tenant := tenants[(worker+i)%len(tenants)]
				handle, err := p.For(context.Background(), tenant)
				if err != nil {
					t.Errorf("For: %v", err)
					return
				}
				if _, err := handle.Evaluate(context.Background(), request(tenant)); err != nil && !errors.Is(err, ErrEngineClosed) {
					t.Errorf("Evaluate: %v", err)
					return
				}
				if i%10 == 0 {
					p.Evict(tenants[i%len(tenants)])
				}
			}
		})
	}
	wg.Wait()
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, logger := range e.all {
		if got := logger.violations.Load(); got != 0 {
			t.Fatalf("%d use-after-close violations", got)
		}
		if !logger.isClosed.Load() {
			t.Fatal("an engine was never closed")
		}
	}
}

func TestCloseLeavesNoGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()
	e := newEnv()
	e.reload = time.Hour // each engine starts a background reconcile goroutine
	ticks := make(chan time.Time)
	p, _ := newPool(t, e, WithSweepInterval(time.Minute), withTicker(ticks), WithIdleTTL(time.Hour))

	if err := p.Warm(context.Background(), "A", "B", "C", "D", "E"); err != nil {
		t.Fatalf("Warm = %v", err)
	}
	if during := runtime.NumGoroutine(); during <= baseline {
		t.Fatalf("expected reconcile and janitor goroutines while open (%d <= %d)", during, baseline)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	for range 1_000_000 {
		if runtime.NumGoroutine() <= baseline {
			break
		}
		runtime.Gosched()
	}
	if after := runtime.NumGoroutine(); after > baseline {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines: %d before, %d after Close\n%s", baseline, after, buf[:runtime.Stack(buf, true)])
	}
	for _, logger := range e.all {
		if !logger.isClosed.Load() {
			t.Fatal("an engine was not closed")
		}
	}
}

func TestCloseIsIdempotentAndRefusesFurtherUse(t *testing.T) {
	e := newEnv()
	p, _ := newPool(t, e)
	handle := mustFor(t, p, "A")

	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if _, err := p.For(context.Background(), "A"); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("For after Close = %v", err)
	}
	if _, ok := p.Get("A"); ok {
		t.Fatal("Get after Close returned an engine")
	}
	if _, err := handle.Evaluate(context.Background(), request("A")); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("Evaluate on a handle after Close = %v", err)
	}
	if err := handle.Ready(); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("Ready after Close = %v", err)
	}
	if p.Sweep() != 0 || p.Len() != 0 {
		t.Fatal("a closed pool still holds engines")
	}
}

func TestEmptyTenantID(t *testing.T) {
	e := newEnv()
	p, _ := newPool(t, e)
	if _, err := p.For(context.Background(), ""); !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("For = %v", err)
	}
	if _, ok := p.Get(""); ok {
		t.Fatal("Get(\"\") returned an engine")
	}
	if err := p.Warm(context.Background(), ""); !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("Warm = %v", err)
	}
	if p.Evict("") {
		t.Fatal("Evict(\"\") reported a removal")
	}
	if len(e.calls) != 0 {
		t.Fatal("the factory ran for an empty tenant ID")
	}
}

func TestTenantIDsAreMatchedExactly(t *testing.T) {
	e := newEnv()
	p, _ := newPool(t, e)
	lower, upper, padded := mustFor(t, p, "rwb"), mustFor(t, p, "RWB"), mustFor(t, p, " RWB")
	if lower == upper || upper == padded || lower == padded {
		t.Fatal("tenant IDs differing in case or whitespace shared an engine")
	}
	if p.Len() != 3 || upper.Tenant() != "RWB" || padded.Tenant() != " RWB" {
		t.Fatalf("Len = %d, tenants = %q %q", p.Len(), upper.Tenant(), padded.Tenant())
	}
}

func TestHandleDelegatesToTheEngine(t *testing.T) {
	e := newEnv()
	p, _ := newPool(t, e)
	var resolver ruleengine.Resolver = mustFor(t, p, "A")
	decision, err := resolver.Evaluate(context.Background(), request("A"))
	if err != nil {
		t.Fatalf("Evaluate = %v", err)
	}
	if !decision.Failed {
		t.Fatal("a tenant with no rules should fail closed")
	}
	handle := mustFor(t, p, "A")
	if err := handle.Ready(); err != nil {
		t.Fatalf("Ready = %v", err)
	}
	_ = handle.Stats()
	if _, err := handle.EvaluateAll(context.Background(), nil, request("A")); err == nil {
		t.Fatal("EvaluateAll with no points should be refused by the engine")
	}
	p.Evict("A")
	if got := handle.Stats(); got.Rules != 0 {
		t.Fatalf("Stats on a retired engine = %+v, want zero", got)
	}
	if _, err := handle.EvaluateAll(context.Background(), nil, request("A")); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("EvaluateAll on a retired engine = %v", err)
	}
}

func TestJanitorSweepsOnTicks(t *testing.T) {
	e := newEnv()
	ticks := make(chan time.Time)
	p, clk := newPool(t, e, WithIdleTTL(time.Minute), WithSweepInterval(time.Second), withTicker(ticks))
	mustFor(t, p, "A")
	clk.Advance(2 * time.Minute)

	ticks <- clk.Now() // received: the janitor is sweeping
	ticks <- clk.Now() // received only once the first sweep has finished
	if p.Len() != 0 {
		t.Fatalf("Len = %d, want the janitor to have evicted A", p.Len())
	}
	if !e.spyFor("A").isClosed.Load() {
		t.Fatal("the janitor did not close the idle engine")
	}
}

func TestNoJanitorWithoutASweepInterval(t *testing.T) {
	e := newEnv()
	p, _ := newPool(t, e)
	if p.janitorDone != nil {
		t.Fatal("a janitor was started although the sweep interval is zero")
	}
}

func TestMetrics(t *testing.T) {
	e := newEnv()
	e.fail["bad"] = errors.New("down")
	metrics := newRecMetrics()
	p, _ := newPool(t, e, WithMetrics(metrics), WithMaxTenants(1))

	mustFor(t, p, "A")                        // miss, build, created
	mustFor(t, p, "A")                        // hit
	mustFor(t, p, "B")                        // miss, build, created, A evicted for capacity
	_, _ = p.For(context.Background(), "bad") // miss, build, failure
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := map[string]float64{
		telemetry.MetricPoolHits:                                    1,
		telemetry.MetricPoolMisses:                                  3,
		telemetry.MetricPoolBuilds:                                  3,
		telemetry.MetricPoolBuildFailures:                           1,
		telemetry.MetricPoolEnginesCreated:                          2,
		telemetry.MetricPoolEnginesClosed:                           2,
		telemetry.MetricPoolEvictions + "|reason=" + reasonCapacity: 1,
		telemetry.MetricPoolEvictions + "|reason=" + reasonClose:    1,
		telemetry.MetricPoolEvictions + "|reason=" + reasonIdle:     0,
		telemetry.MetricPoolEvictions + "|reason=" + reasonExplicit: 0,
	}
	for key, expected := range want {
		if got := metrics.total(key); got != expected {
			t.Errorf("%s = %v, want %v", key, got, expected)
		}
	}
	for key := range metrics.keys {
		if key != telemetry.LabelReason {
			t.Errorf("metric label %q: only %q is allowed, and never a tenant", key, telemetry.LabelReason)
		}
	}
}

func TestNewValidatesItsConfiguration(t *testing.T) {
	factory := func(context.Context, string) (*ruleengine.Engine, error) { return nil, nil }
	if _, err := New(nil); !errors.Is(err, ruleengine.ErrConfig) {
		t.Fatalf("New(nil) = %v", err)
	}
	for name, option := range map[string]Option{
		"idle ttl":    WithIdleTTL(-1),
		"max tenants": WithMaxTenants(-1),
		"backoff":     WithFailureBackoff(-1),
		"build":       WithBuildTimeout(-1),
		"close":       WithCloseTimeout(-1),
		"warm":        WithWarmConcurrency(0),
		"sweep":       WithSweepInterval(-1),
	} {
		if _, err := New(factory, option); !errors.Is(err, ruleengine.ErrConfig) {
			t.Errorf("%s: New = %v, want ErrConfig", name, err)
		}
	}
	// Nil options and nil collaborators are ignored rather than crashing.
	p, err := New(factory, nil, WithMetrics(nil), WithLogger(nil), WithClock(nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Close(context.Background())
}

type durableSpy struct {
	logged, durable int
	closed          bool
}

func (d *durableSpy) Log(audit.Record) { d.logged++ }
func (d *durableSpy) LogDurably(context.Context, audit.Record) error {
	d.durable++
	return nil
}
func (d *durableSpy) Close() error { d.closed = true; return nil }

func TestNoCloseLogger(t *testing.T) {
	plain := newSpy(nil, nil)
	wrapped := NoCloseLogger(plain)
	wrapped.Log(audit.Record{})
	if plain.logs.Load() != 1 {
		t.Fatal("the wrapper did not forward Log")
	}
	if _, ok := wrapped.(interface{ Close() error }); ok {
		t.Fatal("the wrapper exposes Close")
	}
	if _, ok := wrapped.(audit.DurableDecisionLogger); ok {
		t.Fatal("a best-effort logger became durable")
	}

	durable := &durableSpy{}
	wrappedDurable := NoCloseLogger(durable)
	durableLogger, ok := wrappedDurable.(audit.DurableDecisionLogger)
	if !ok {
		t.Fatal("the wrapper dropped LogDurably")
	}
	wrappedDurable.Log(audit.Record{})
	if err := durableLogger.LogDurably(context.Background(), audit.Record{}); err != nil {
		t.Fatal(err)
	}
	if durable.logged != 1 || durable.durable != 1 {
		t.Fatalf("forwarded %d Log and %d LogDurably calls", durable.logged, durable.durable)
	}
	if _, ok := wrappedDurable.(interface{ Close() error }); ok {
		t.Fatal("the durable wrapper exposes Close")
	}

	if NoCloseLogger(nil) == nil {
		t.Fatal("NoCloseLogger(nil) should be a no-op logger")
	}

	// Closing an engine built on the wrapper leaves the shared logger open.
	engine, err := ruleengine.New(
		ruleengine.Config{Tenant: "A", Domain: "leave", FactBudget: testBudget()},
		ruleengine.WithDecisionLogger(NoCloseLogger(durable)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if durable.closed {
		t.Fatal("closing the engine closed the shared logger")
	}
}
