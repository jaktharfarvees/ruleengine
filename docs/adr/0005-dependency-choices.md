# 5. Dependency choices

Date: 2026-10-15

## Status

Accepted, with three substitutions that are open for the owner to confirm.

## Context

The library is consumed by fourteen services that upgrade independently. Every
dependency it takes becomes a dependency those services inherit, including its
own transitive set and its vulnerability surface. The build specification
therefore allows a short list and forbids whole categories outright.

## Decision

Standard library first. `log/slog` for logging, accepted from the caller and
defaulting to a discard handler; `errors.Is`/`errors.As` with typed errors;
`encoding/json`; `sync/atomic.Pointer` for copy-on-write cache swaps; `embed`
for the schemas and default fixtures.

Forbidden outright, because each would propagate to fourteen services or invite
an unsafe shortcut: any logging framework, `testify`, any SQL driver or query
builder or ORM, any message-broker client, and any OpenTelemetry or Prometheus
package in the main module. Telemetry is defined as interfaces with a no-op
implementation, and a publisher or executor is an interface the service
implements.

The specification's approved runtime list is `github.com/shopspring/decimal`,
`github.com/google/uuid` and `github.com/santhosh-tekuri/jsonschema/v6`, with
`github.com/google/go-cmp` and `pgregory.net/rapid` for tests.

The module currently declares **no third-party dependencies at all**. Three
approved dependencies were replaced by internal implementations because the
build environment cannot resolve modules that are not already cached, and an
unresolved dependency makes the library impossible to compile or test:

| Approved | Substitute | Recorded in |
|---|---|---|
| `shopspring/decimal` | `internal/decimalx` over `math/big.Rat` | [ADR 8](0008-offline-exact-decimal.md) |
| `santhosh-tekuri/jsonschema/v6` | a closed validator in `schema` | [ADR 6](0006-closed-schema-validator.md) |
| `pgregory.net/rapid` | hand-written generators in the property tests | [ADR 7](0007-offline-property-tests.md) |

`go-cmp` is unused for the same reason; test diffs are written by hand.
`google/uuid` is also unused: the root facade generates decision identifiers
and the idempotency boundary validates canonical UUIDs with small internal,
standard-library implementations.

## Consequences

Consumers inherit nothing, so no `go.sum` exists until the module gains an
external dependency, and the runtime supply-chain surface is the standard
library. CI pins its tool and action revisions, runs `govulncheck`, CodeQL and
dependency review, and verifies that module metadata is tidy. Dependabot
monitors both Go modules and GitHub Actions so a dependency added later remains
visible and updateable.

The cost is that three well-tested libraries are replaced by code this team
maintains, which is only defensible because each substitute is internal
(`internal/decimalx`) or small and closed (the schema validator), and because
each has an ADR recording how to switch back. The decimal substitute in
particular is on the money path and is the one to revisit first once
dependencies can be resolved; its API is internal precisely so that swap breaks
no consumer.
