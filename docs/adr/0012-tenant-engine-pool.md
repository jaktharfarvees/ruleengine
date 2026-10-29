# 12. A tenant engine pool owns the lifecycle of per-tenant engines

Date: 2026-10-28

## Status

Accepted.

## Context

An `Engine` serves one tenant: `Config.Tenant` is fixed at construction.
Tenants are onboarded continuously, so a multi-tenant service cannot hand-build
a map of engines at start-up and cannot restart to add a tenant. Left to each
service, the map, its locking, the build-once logic, eviction and shutdown
would be written fourteen times and tested in none of them.

Three properties of `Engine` shape the design:

- `New` does no I/O and starts no goroutine. `Reload` does network I/O and
  holds the tenant's whole bundle. `Start` spawns one reconcile goroutine and
  ticker per engine when `CacheReloadInterval` is set.
- `Evaluate` is safe for concurrent use, but `Close` neither waits for running
  evaluations nor rejects new ones. `audit.Async.Log` after `Close` sends on a
  closed channel and panics, so closing an engine under a running `Evaluate` is
  a crash, not a stale read.
- `Engine.Close` closes the decision logger. A logger shared by several
  engines is therefore closed by the first engine to go.

## Decision

Add a `pool` package. The service supplies a `Factory`; the pool never learns
where rules or the hierarchy come from.

- **API.** `New(factory, opts...)`, `For` (cached or built once), `Get`
  (lookup only, no I/O), `Warm`, `Evict`, `Sweep`, `Len`, `Close`.
- **Handle, not engine.** `For` and `Get` return `*pool.Engine`, a handle with
  no way to reach the engine underneath. Each call through it is counted. An
  evicted or closing entry is retired first, which refuses new calls, and the
  real engine is closed only when the in-flight count reaches zero, by whoever
  finishes last. A bare `*ruleengine.Engine` cannot give this guarantee because
  the pool cannot see what a caller does with it later. `Close(ctx)` that times
  out reports `ErrCloseTimeout` and leaves a still-evaluating engine open
  rather than closing it under a running call.
- **Build once.** A per-tenant in-flight record is shared by concurrent
  callers. The build runs on its own goroutine with the first caller's values
  but not its cancellation, bounded by a build timeout and cancelled when the
  pool closes. A caller whose context ends stops waiting; the build continues
  and serves the next caller.
- **Make ready.** After the factory returns, the pool rejects an engine whose
  `Tenant()` differs from the request, then calls `Reload`, `Start` and `Ready`.
  Any failure closes the engine and is a build failure. `Engine.Tenant()` is the
  one addition to the root package.
- **Failures.** A typed `*BuildError` is remembered for a short backoff per
  tenant. Failures caused by the caller's own context or by the pool closing
  are not remembered. Other tenants are unaffected, and a stuck build holds no
  pool-wide lock.
- **Eviction.** Idle time and an optional maximum number of tenants, least
  recently used first, using the module's clock so tests need no sleeps. Idle
  engines are swept lazily on the next call, by an exported `Sweep`, or by an
  opt-in janitor (`WithSweepInterval`, off by default). A lazy sweep closes
  engines on a separate goroutine so a request never waits on another tenant's
  drain.
- **Shared logger.** `NoCloseLogger` hides `Close` and keeps `LogDurably`. The
  service owns a shared logger and closes it once, after `Pool.Close`.
- **Metrics.** Seven counters in `telemetry/names.go`: hits, misses, builds,
  build failures, evictions (label `reason`), engines created, engines
  closed. Size is created minus closed. No metric carries a tenant label,
  because tenant count is unbounded cardinality.

## Not decided here

Two further savings were considered and deliberately left out, because each
changes existing packages:

- **A shared change feed**, one reconcile for all tenants instead of one per
  engine. It needs an externally driven mode on `Engine`, a multi-tenant cursor
  in `store`, and a per-tenant filter in the loader. The risks are cross-tenant
  leakage through fan-out, a cursor that is global rather than per engine, and
  a public API change.
- **Sharing platform-wide data** (field registry, dataset manifests) across
  engines. It needs `store.Bundle` and `Snapshot` to separate platform data
  from tenant data, and `fact.NewResolverWithTracer` to accept shared inputs.
  The risk is losing the single atomic rules/registry/manifests snapshot, so a
  tenant could see a registry newer or older than the one its rules were
  validated against.

## Consequences

- Services share one tested implementation and add tenants without restart.
- Memory and goroutines still scale with tenants held: each engine has its own
  bundle and, if `CacheReloadInterval` is set, its own reconcile goroutine. Use
  `WithMaxTenants` and `WithIdleTTL` to bound them.
- A handle becomes invalid when its engine is evicted. Callers that hold a
  handle across requests must handle `ErrEngineClosed` by asking the pool
  again, or simply call `For` per request, which costs one lock and a map
  lookup.
- Every engine in a pool reports the same unlabelled cache metrics, because
  the library's cache gauges carry no tenant label. Do not alert on a single
  engine's cache staleness from a pooled service.
- The factory must give each engine its own `CacheReloadInterval` policy; the
  pool does not override it.
