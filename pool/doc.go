// Package pool keeps one rule engine per tenant, creating each on first use.
//
// A ruleengine.Engine serves one tenant, because Config.Tenant is fixed. A
// service for many tenants, which are onboarded continuously, should not hold a
// hand-built map of engines or restart to add one. Pool creates, caches, warms
// and evicts them, so every service runs the same tested code.
//
// # The factory
//
// The service supplies a Factory, which builds the engine for a tenant. It
// decides where that tenant's rules and hierarchy come from (files, the Rule
// Service, a settings service); the pool knows nothing about that. The engine
// it returns must have a loader and be configured for exactly the requested
// tenant. The pool then calls Reload, then Start, then checks Ready, and
// publishes the engine only if all three succeed. The factory controls
// background reconciliation through Config.CacheReloadInterval.
//
// # Using the pool
//
//   - For returns the cached engine, or builds it on first use. Concurrent
//     calls for one tenant share a single build.
//   - Get is a lookup only. It never builds and performs no I/O, so it is safe
//     inside a database transaction.
//   - Warm builds engines ahead of time, at start-up or when a tenant is
//     created.
//   - Evict removes a tenant, for example when it is deleted.
//
// A tenant ID is matched exactly: there is no case folding or trimming, and the
// empty ID is an error.
//
// # Failures
//
// A failed build returns a *BuildError and is remembered for a short backoff
// (WithFailureBackoff), so a broken tenant is not rebuilt on every request.
// Other tenants are never affected. Only real factory, Reload, Start or Ready
// failures are remembered: a caller giving up on its own context, or the pool
// closing, is not.
//
// # Eviction
//
// An engine is evicted after WithIdleTTL without use, and the least recently
// used engines are evicted beyond WithMaxTenants. Idle engines are swept
// lazily on the pool's next call, by calling Sweep, or by an opt-in background
// janitor (WithSweepInterval). The pool starts no goroutine unless you ask.
//
// # Engine lifetime
//
// For and Get return an *Engine, a handle rather than the underlying engine.
// Every Evaluate through a handle is counted, and the pool closes the real
// engine only when no evaluation is running and none can start. This matters
// because evaluating after Engine.Close can panic through an asynchronous
// audit logger. A handle used after its engine was evicted returns
// ErrEngineClosed; ask the pool for the tenant again.
//
// # Shared decision loggers
//
// Closing an engine closes its decision logger. A logger shared by every
// tenant's engine must therefore be wrapped in NoCloseLogger. The service owns
// it, and closes it once, after Pool.Close returns.
//
// # Shutdown
//
// Close stops the janitor and in-flight builds and closes every engine within a
// time limit. It leaves no goroutine running.
package pool
