# 3. The library compiles and validates a dataset query; the service only executes it

Date: 2026-10-15

## Status

Accepted as the design the `dataset` and `fact` packages are built to. Refines
locked decision D22 and is stricter than plan v2.0 Appendix B; ratification is
open with the owner and is tracked in `docs/OPEN_QUESTIONS.md`.

## Context

Field values come from declared fact sources over published datasets, in four
kinds: `passed`, `derived`, `dataset` and `native` (D19). A `dataset`-kind field
is authored in the UI by a business administrator: a dataset, some filters, an
optional window and an aggregate. There is no raw SQL anywhere, in any mode.

Each of the fourteen services owns its own storage, so each must translate such
a field into its own dialect. Plan v2.0 Appendix B passed the raw `FactSource`
to each service's executor, which means every service team independently
implements the tenant predicate, the window cap, the column-permission check,
the aggregate check and the injection guard. Fourteen implementations is
fourteen chances to omit one, and the one most likely to be omitted — the tenant
predicate — leaks another tenant's data.

## Decision

Compilation and every guardrail live in the library. `dataset.Compile` takes a
`FactSource`, the published `Manifest` and the request `Params`, and returns a
`Query`. The `Executor` interface a service implements receives only a `Query`
that `Compile` produced.

- `Query` has no exported fields and no exported constructor, so it cannot be
  usefully built outside the package. It exposes read accessors plus
  `Fingerprint` for per-request de-duplication and `Explain` for the UI preview.
- Guardrails G1 to G12 are applied by `Compile`: dataset published, author
  strings refused rather than sanitised if they contain query or script syntax,
  filter and window and aggregate columns permitted by the manifest, window
  within the dataset maximum, aggregate offered by the dataset, filter values
  type-checked, only the four substitution tokens honoured, the tenant predicate
  injected and marked `Injected`, a row limit always set, and the window
  resolved to absolute times so an executor never interprets a token.
- `dataset.ConformanceSuite` is published so a service team can prove its
  executor honours the tenant predicate rather than ignoring it.

## Consequences

There is one reviewed implementation of the guardrails instead of fourteen, and
a service team cannot accidentally skip one. A service's executor becomes small:
translate a structured value into a query and run it.

The cost is a narrower contract than Appendix B. An executor cannot see the
author's original intent, only the compiled query, so a storage engine that
needs something `Query` cannot express requires a change to `Query` and to
`Compile` rather than a local workaround. That is the intended trade: a
guardrail that can be worked around locally is not a guardrail. One known
expressiveness gap is already recorded as plan open decisions A-O1 and A-O2.
