# 9. The fact-source declaration lives with the field contract

Date: 2026-10-15

## Status

Accepted. Resolves an import cycle in the build specification's type
assignments; the names the specification uses are preserved as aliases.

## Context

The build specification assigns types to packages like this:

- `registry.FieldDefinition.FactSource` is a `fact.FactSource`
- `dataset.Column.Type` is a `registry.DataType`
- `dataset.Compile(fs fact.FactSource, m Manifest, p Params)`
- `fact.NewResolver(reg *registry.Registry, man map[string]dataset.Manifest, x dataset.Executor, …)`

Those four cannot all hold. The resolver orchestrates all four fact kinds, so
`fact` must import `registry` (to read a field's contract) and `dataset` (to
compile and run a query). But `registry` imports `fact` for `FactSource`, and
`dataset` imports `fact` for `Compile`'s parameter. Either way the graph cycles:
`fact → registry → fact`, and `fact → dataset → fact`.

INV1 fixes the direction — the root package is the only wiring point and
subsystems depend one way — but it does not say which way these three go.

Three options were considered:

1. **Duplicate the types.** Give `dataset` its own column-type enum and give
   `fact` its own view of a field definition. Two definitions of "what type is
   this value" is exactly the drift INV1 exists to prevent: a filter value
   type-checked by one and the resulting fact type-checked by the other could
   disagree.
2. **Move the resolver out of `fact`.** It would have to go to a new package or
   to the root. The root holds the facade and transport types and no business
   algorithm, and inventing a package is a larger departure than this one.
3. **Move the declaration types down.** `FactSource` and its companions become
   part of the field contract in `registry`, and `fact` becomes the layer above
   that resolves them.

## Decision

Option 3. `FactKind`, `FactSource`, `FactWindow`, `FactAggregate` and
`AggregateFn` are defined in `registry`.

This is not only a mechanical fix. Plan v2.0 section 4.4 already makes
`factSource` a **FieldRegistry** field: the declaration of where a value comes
from is part of the field's published contract, authored and versioned with it.
Resolving that declaration — fetching, budgeting, de-duplicating — is a
different job, and that is what `fact` does. The packages now split along the
line the plan already drew.

The specification's names are kept as aliases so no documented API is lost:

```go
// in fact
type Kind = registry.FactKind
type FactSource = registry.FactSource
type Window = registry.FactWindow
type Aggregate = registry.FactAggregate
type AggregateFn = registry.AggregateFn

// in dataset
type AggregateFn = registry.AggregateFn
```

`fact.FactSource` and `registry.FactSource` are the same type, not two, so a
caller may write whichever reads better. `dataset.Compile` takes that type;
`fact.Kind` is still what `Decision.FactsResolved` carries.

The resulting graph is acyclic:

```
rule ← registry ← dataset ← fact
  ↖ hierarchy ← resolve        ↖ telemetry
```

`architecture_test.go` enforces that no subsystem imports the root package, and
the compiler enforces the rest.

## Consequences

One definition of every declaration type, so a filter value and the fact it
produces are type-checked by the same rules. `registry` no longer depends on
`fact`, which also makes it testable on its own.

Two further signatures diverge from the specification for the same reason, and
are recorded here rather than as separate ADRs:

- `dataset.Compile` returns `(Query, []string, error)`. The third value carries
  the non-fatal reason codes compilation produces — `DATASET_DEPRECATED` and
  `PII_COLUMN_IN_FILTER` — which are in the catalogue and had nowhere else to
  come from.
- `fact.Resolver.Resolve` returns `(Resolution, error)` where `Resolution`
  packages the values, the per-fact records and the notes. The specification's
  shape would have been four return values, and the notes still had nowhere to
  go.

Neither changes behaviour; both are packaging. If the specification is revised,
these are the two places to reconcile.
