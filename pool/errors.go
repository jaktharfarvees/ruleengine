package pool

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrEmptyTenantID is returned when a tenant ID is empty. Tenant IDs are
	// matched exactly, so the empty string is never a wildcard or a default.
	ErrEmptyTenantID = errors.New("pool: tenant ID must not be empty")
	// ErrPoolClosed is returned once Close has been called.
	ErrPoolClosed = errors.New("pool: closed")
	// ErrEngineClosed is returned by an engine handle after the pool has
	// retired its engine, by eviction or by Close. Ask the pool for the tenant's
	// engine again.
	ErrEngineClosed = errors.New("pool: engine has been closed")
	// ErrCloseTimeout is returned by Close when an engine or a build did not
	// finish within the time limit. An engine still evaluating is never closed
	// underneath that evaluation: it is closed when the last evaluation ends.
	ErrCloseTimeout = errors.New("pool: close did not finish in time")
	// ErrBuildFailed is matched by every *BuildError.
	ErrBuildFailed = errors.New("pool: engine build failed")
	// ErrTenantMismatch is the cause of a build failure when the factory
	// returned an engine configured for a different tenant than was requested.
	ErrTenantMismatch = errors.New("pool: factory returned an engine for a different tenant")
	// ErrNotReady is the cause of a build failure when a freshly loaded engine
	// still reports that it cannot decide.
	ErrNotReady = errors.New("pool: built engine is not ready")
)

// BuildError reports that an engine for one tenant could not be built. The
// failure is remembered until RetryAt, so a broken tenant is not rebuilt on
// every request; other tenants are unaffected.
type BuildError struct {
	// Tenant is the tenant whose engine could not be built.
	Tenant string
	// Err is the factory, Reload, Start or Ready failure.
	Err error
	// RetryAt is when the pool will next try to build this tenant. It is the
	// zero time when failures are not remembered.
	RetryAt time.Time
}

// Error describes the failure.
func (e *BuildError) Error() string {
	return fmt.Sprintf("pool: build engine for tenant %q: %v", e.Tenant, e.Err)
}

// Unwrap lets errors.Is match both ErrBuildFailed and the underlying cause.
func (e *BuildError) Unwrap() []error { return []error{ErrBuildFailed, e.Err} }
