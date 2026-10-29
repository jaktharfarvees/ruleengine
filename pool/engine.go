package pool

import (
	"container/list"
	"context"
	"sync"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/store"
)

// Engine is a tenant's engine as the pool hands it out.
//
// It is a handle, not the engine itself, and deliberately has no way to reach
// the engine underneath. Every call through it is counted, and the pool closes
// the real engine only when no call is running and no new one can start. A
// bare *ruleengine.Engine could not give that guarantee, because the pool
// cannot see what a caller does with it after For returns: closing one under a
// running Evaluate can panic through the audit logger.
//
// A handle is cheap and safe for concurrent use. It stays valid until the pool
// evicts or closes its engine; after that every call returns ErrEngineClosed
// and the caller asks the pool for the tenant again.
type Engine struct {
	entry *entry
}

// Evaluate runs one decision point on the tenant's engine. It returns
// ErrEngineClosed if the pool has retired the engine.
func (h *Engine) Evaluate(ctx context.Context, request ruleengine.EvalRequest) (ruleengine.Decision, error) {
	if !h.entry.acquire() {
		return ruleengine.Decision{}, ErrEngineClosed
	}
	defer h.entry.release()
	return h.entry.engine.Evaluate(ctx, request)
}

// EvaluateAll evaluates several decision points for one subject on the
// tenant's engine. It returns ErrEngineClosed if the pool has retired the
// engine.
func (h *Engine) EvaluateAll(ctx context.Context, points []ruleengine.Point, request ruleengine.EvalRequest) (ruleengine.Results, error) {
	if !h.entry.acquire() {
		return ruleengine.Results{}, ErrEngineClosed
	}
	defer h.entry.release()
	return h.entry.engine.EvaluateAll(ctx, points, request)
}

// Ready reports whether the tenant's engine can decide. It returns
// ErrEngineClosed if the pool has retired the engine.
func (h *Engine) Ready() error {
	if !h.entry.acquire() {
		return ErrEngineClosed
	}
	defer h.entry.release()
	return h.entry.engine.Ready()
}

// Stats reports what the tenant's engine currently holds. It returns the zero
// value if the pool has retired the engine.
func (h *Engine) Stats() store.Stats {
	if !h.entry.acquire() {
		return store.Stats{}
	}
	defer h.entry.release()
	return h.entry.engine.Stats()
}

// Tenant returns the tenant this engine serves.
func (h *Engine) Tenant() string { return h.entry.tenant }

var _ ruleengine.Resolver = (*Engine)(nil)

// entry is one pooled engine and the bookkeeping that decides when it may be
// closed.
type entry struct {
	pool   *Pool
	tenant string
	engine *ruleengine.Engine
	handle *Engine

	// mu guards the lifecycle fields below. It is never held across a call
	// into the engine.
	mu       sync.Mutex
	inflight int
	retired  bool // no new call may start
	closing  bool // Close has been claimed by exactly one goroutine
	// closed is closed once the engine's Close has returned. closeErr is
	// written before that and read only after it.
	closed   chan struct{}
	closeErr error

	// Guarded by the pool's lock, not mu.
	elem     *list.Element
	lastUsed time.Time
}

func newEntry(p *Pool, tenant string, engine *ruleengine.Engine) *entry {
	e := &entry{pool: p, tenant: tenant, engine: engine, closed: make(chan struct{})}
	e.handle = &Engine{entry: e}
	return e
}

// acquire registers a running call. It fails once the entry is retired, which
// is what makes closing safe: after retire, the in-flight count only falls.
func (e *entry) acquire() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.retired {
		return false
	}
	e.inflight++
	return true
}

// release ends a call, and closes the engine if this was the last call of a
// retired entry.
func (e *entry) release() {
	e.mu.Lock()
	e.inflight--
	closeNow := e.retired && e.inflight == 0 && !e.closing
	if closeNow {
		e.closing = true
	}
	e.mu.Unlock()
	if closeNow {
		// The pool already counted this close when it removed the entry, so
		// Pool.Close waits for it. It runs off the caller's goroutine because
		// the caller is a request that has just got its answer, and must not
		// also wait for an engine to drain.
		go e.pool.closeEngine(e)
	}
}

// retire stops new calls and reports whether the caller must close the engine
// now, which is only so when nothing is running.
func (e *entry) retire() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.retired = true
	if e.inflight == 0 && !e.closing {
		e.closing = true
		return true
	}
	return false
}
