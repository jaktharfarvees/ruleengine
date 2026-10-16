# 7. Keep resolution property tests runnable offline

Date: 2026-10-14

## Status

Accepted for the initial offline build environment.

## Context

Phase 3 requires randomized tests for precedence monotonicity, determinism, and
no-panic behavior. The build specification selects `pgregory.net/rapid`, but
the build environment cannot download that module and it is absent from the
local module cache. Adding an unresolved test dependency would make every test
run fail before compilation.

## Decision

The three required properties use deterministic, fixed-seed generators built
with the standard library. Each property runs 1,000 generated cases, reports
the failing iteration, and remains reproducible locally and in CI. The tests
exercise generated hierarchy depths, candidate counts, orderings, anchors,
nil candidates, missing nodes, malformed filters, and absent snapshots.

## Consequences

The Phase 3 property gate remains reproducible without network access. The
tests do not currently provide Rapid's automatic shrinking. Adopt the approved
Rapid dependency and port these generators when dependency resolution is
available; the properties and fixed regression suite remain unchanged.
