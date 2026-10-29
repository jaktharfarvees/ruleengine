package pool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/audit"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/ruletest"
	"github.com/arrowhr/ruleengine/store"
	"github.com/arrowhr/ruleengine/telemetry"
)

// guard is the longest a test waits for something that should already be
// happening. It is a failure bound, not a delay: nothing here sleeps for it.
const guard = 10 * time.Second

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(guard):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// tenantBundle is the smallest bundle an engine reports Ready on: one
// hierarchy node for the tenant.
func tenantBundle(tenant string) store.Bundle {
	return store.Bundle{Hierarchy: store.HierarchyBundle{
		Types: []hierarchy.Type{{TypeID: "org", TypeName: "Org", Order: 0, Tenant: tenant, Status: "ACTIVE"}},
		Nodes: []hierarchy.Node{{
			NodeID: "n1", Code: "n1", Tenant: tenant, TypeID: "org",
			Name: tenant, Status: "ACTIVE", Timezone: "UTC",
		}},
	}}
}

type staticLoader struct{ bundle store.Bundle }

func (l staticLoader) LoadAll(context.Context) (store.Bundle, error) { return l.bundle, nil }

// spy is an engine's decision logger. It records the hazard the pool exists to
// prevent: audit.Async panics when it is logged to after Close, so the spy
// counts a Log that follows Close, and a Close that lands inside a Log.
type spy struct {
	closeOnce  sync.Once
	closed     chan struct{}
	isClosed   atomic.Bool
	inLog      atomic.Int32
	violations atomic.Int32
	logs       atomic.Int32

	entered chan struct{} // receives one value per Log that has started
	gate    chan struct{} // when non-nil, Log blocks until it is closed

	closeGate    chan struct{} // when non-nil, Close blocks until it is closed
	closeEntered chan struct{} // receives one value when Close starts
}

func newSpy(entered, gate chan struct{}) *spy {
	return &spy{closed: make(chan struct{}), entered: entered, gate: gate}
}

func (s *spy) Log(audit.Record) {
	s.inLog.Add(1)
	defer s.inLog.Add(-1)
	if s.isClosed.Load() {
		s.violations.Add(1)
	}
	s.logs.Add(1)
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	if s.gate != nil {
		<-s.gate
	}
	if s.isClosed.Load() {
		s.violations.Add(1)
	}
}

func (s *spy) Close() error {
	if s.closeEntered != nil {
		select {
		case s.closeEntered <- struct{}{}:
		default:
		}
	}
	if s.closeGate != nil {
		<-s.closeGate
	}
	if s.inLog.Load() > 0 {
		s.violations.Add(1)
	}
	s.isClosed.Store(true)
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

// env is a configurable tenant factory.
type env struct {
	mu       sync.Mutex
	calls    map[string]int
	fail     map[string]error
	gates    map[string]chan struct{} // factory blocks until the gate is closed
	override map[string]string        // engine tenant to build instead of the requested one
	noHier   map[string]bool          // build an engine whose bundle has no hierarchy
	spies    map[string]*spy
	all      []*spy
	panics   map[string]bool

	entered  chan string // receives the tenant each time the factory is entered
	logGate  chan struct{}
	logEnter chan struct{}
	reload   time.Duration

	closeGate    chan struct{} // engines' decision loggers block in Close until closed
	closeEntered chan struct{}
}

func newEnv() *env {
	return &env{
		calls: map[string]int{}, fail: map[string]error{}, gates: map[string]chan struct{}{},
		override: map[string]string{}, noHier: map[string]bool{}, spies: map[string]*spy{},
		panics:  map[string]bool{},
		entered: make(chan string, 256),
	}
}

func (e *env) count(tenant string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[tenant]
}

func (e *env) spyFor(tenant string) *spy {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.spies[tenant]
}

func (e *env) factory(ctx context.Context, tenant string) (*ruleengine.Engine, error) {
	e.mu.Lock()
	e.calls[tenant]++
	gate, failure := e.gates[tenant], e.fail[tenant]
	engineTenant, overridden := e.override[tenant]
	noHier, panics := e.noHier[tenant], e.panics[tenant]
	e.mu.Unlock()
	select {
	case e.entered <- tenant:
	default:
	}
	if panics {
		panic("boom")
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if failure != nil {
		return nil, failure
	}
	if !overridden {
		engineTenant = tenant
	}
	logger := newSpy(e.logEnter, e.logGate)
	logger.closeGate, logger.closeEntered = e.closeGate, e.closeEntered
	bundle := tenantBundle(engineTenant)
	if noHier {
		bundle = store.Bundle{}
	}
	engine, err := ruleengine.New(
		ruleengine.Config{
			Tenant: engineTenant, Domain: "leave", FactBudget: ruletest.Budget(),
			CacheReloadInterval: e.reload,
		},
		ruleengine.WithLoader(staticLoader{bundle: bundle}),
		ruleengine.WithDecisionLogger(logger),
	)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.spies[tenant] = logger
	e.all = append(e.all, logger)
	e.mu.Unlock()
	return engine, nil
}

func request(tenant string) ruleengine.EvalRequest {
	return ruleengine.EvalRequest{
		SchemaVersion: 2, Tenant: ruleengine.Tenant{ID: tenant},
		Routing: ruleengine.Routing{Domain: "leave", RuleSet: "validation", Name: "x"},
		Subject: ruleengine.Subject{
			Type:       "employee",
			Identifier: ruleengine.Identifier{System: "urn:arrowhr:ns:employee-id", Value: "E1", IDType: "employeeId"},
			NodeID:     "n1",
		},
		Context: map[string]any{}, AsOf: time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
	}
}

// newPool builds a pool over the env with a fake clock and closes it at the
// end of the test.
func newPool(t *testing.T, e *env, opts ...Option) (*Pool, *ruletest.FakeClock) {
	t.Helper()
	clk := ruletest.NewFakeClock(time.Time{})
	all := append([]Option{WithClock(clk)}, opts...)
	p, err := New(e.factory, all...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p, clk
}

// recMetrics records counter totals by name, and every label key it is given.
type recMetrics struct {
	mu     sync.Mutex
	totals map[string]float64
	keys   map[string]bool
}

func newRecMetrics() *recMetrics {
	return &recMetrics{totals: map[string]float64{}, keys: map[string]bool{}}
}

func (m *recMetrics) Counter(name string, labels ...telemetry.Label) telemetry.Counter {
	key := name
	m.mu.Lock()
	for _, label := range labels {
		m.keys[label.Key] = true
		key += "|" + label.Key + "=" + label.Value
	}
	m.mu.Unlock()
	return recCounter{m: m, key: key}
}

func (m *recMetrics) Histogram(string, ...telemetry.Label) telemetry.Histogram {
	return recCounter{m: m, key: "histogram"}
}

func (m *recMetrics) total(key string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totals[key]
}

type recCounter struct {
	m   *recMetrics
	key string
}

func (c recCounter) Inc() { c.Add(1) }

func (c recCounter) Add(delta float64) {
	c.m.mu.Lock()
	c.m.totals[c.key] += delta
	c.m.mu.Unlock()
}

func (c recCounter) Observe(value float64) { c.Add(value) }
