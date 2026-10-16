package fact

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Params are the request values a fact resolution may use. They are
// deliberately a small struct rather than the request envelope: a native
// function is service code running inside a decision, and handing it the whole
// envelope would let it reach for data the field never declared (INV1).
type Params struct {
	Tenant     string
	EmployeeID string
	NodeID     string
	AsOf       time.Time
	ValidAt    time.Time
}

// Func computes one native fact. It receives a context carrying the per-fact
// timeout and must honour cancellation.
//
// It must not read a clock: the times it may use arrive in Params, so that the
// same request replayed for a payroll correction produces the same value.
type Func func(ctx context.Context, params Params) (any, error)

// NativeRegistry holds the native fact functions one service registers. It is
// safe for concurrent use after construction and is normally populated once at
// start-up.
type NativeRegistry struct {
	mutex     sync.RWMutex
	functions map[string]Func
}

// NewNativeRegistry constructs an empty registry.
func NewNativeRegistry() *NativeRegistry {
	return &NativeRegistry{functions: make(map[string]Func)}
}

// Register adds a function under a reference a FieldRegistry entry names.
// Registering the same reference twice is an error rather than a replacement:
// two implementations of one fact is a wiring mistake, and the one that would
// win depends on initialisation order.
func (n *NativeRegistry) Register(reference string, function Func) error {
	if n == nil {
		return fmt.Errorf("native registry is nil")
	}
	if reference == "" {
		return fmt.Errorf("a native fact reference is required")
	}
	if function == nil {
		return fmt.Errorf("native fact %q has no function", reference)
	}
	n.mutex.Lock()
	defer n.mutex.Unlock()
	if n.functions == nil {
		n.functions = make(map[string]Func)
	}
	if _, exists := n.functions[reference]; exists {
		return fmt.Errorf("native fact %q is already registered", reference)
	}
	n.functions[reference] = function
	return nil
}

// Has reports whether a reference is registered.
func (n *NativeRegistry) Has(reference string) bool {
	_, ok := n.lookup(reference)
	return ok
}

// References returns the registered references, sorted, for a readiness check
// that compares them against the registry's native fields.
func (n *NativeRegistry) References() []string {
	if n == nil {
		return nil
	}
	n.mutex.RLock()
	defer n.mutex.RUnlock()
	references := make([]string, 0, len(n.functions))
	for reference := range n.functions {
		references = append(references, reference)
	}
	sort.Strings(references)
	return references
}

func (n *NativeRegistry) lookup(reference string) (Func, bool) {
	if n == nil {
		return nil, false
	}
	n.mutex.RLock()
	defer n.mutex.RUnlock()
	function, ok := n.functions[reference]
	return function, ok
}
