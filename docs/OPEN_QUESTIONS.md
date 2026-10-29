# Known limits and owner decisions

This file contains only unresolved product or integration choices. Completed
implementation gaps belong in the changelog and ADRs.

## Known grammar limits

### Dataset filters cannot reference arbitrary request fields

The allowlisted tokens are `$subject.employeeId`, `$subject.nodeId`, `$asOf`,
and `$validAt`. `$input.*` is rejected rather than partially supported. A
request-dependent lookup should currently use a registered `native` fact via
the consumer's `ContextBuilder`.

### Dataset windows are point-relative only

`lastDays` may be relative to `$asOf` or `$validAt`. A request date range or a
two-column overlap such as `startDate <= requestedEnd && endDate >=
requestedStart` is not expressible. Use a native fact until the dataset grammar
gets a versioned extension.

### Streak operates on ordered rows

`streak` counts the consecutive most-recent rows satisfying its aggregate
column. It does not infer uninterrupted calendar days across start/end date
pairs. Leave owners must use a native fact where calendar continuity is the
business definition.

## Consumer-owned choices

- Confirm the production module host and first semantic version before tagging.
- Confirm that every consuming service supports the Go version declared in
  `go.mod`.
- Set explicit fact budgets per service and decision profile; the library does
  not invent payroll-safe defaults.
- Decide whether statutory jurisdiction is supplied by the request or resolved
  from the effective hierarchy, and use one authoritative source per decision
  point.
- Choose telemetry adapters. The module intentionally exposes interfaces and
  ships no vendor-specific OpenTelemetry or Prometheus dependency.
- Implement the service's `dataset.Executor`, native facts, durable audit
  outbox, and Rule Service loader. Database credentials never belong in this
  module.

## Deliberate compatibility items

- `Config.FailClosedDomains`, legacy Go aliases such as `Scope` and the
  registry's `Dataset` and `OwnerService` (the `dataset.Manifest` aliases are
  gone), and the former public tie reason constants remain for source
  compatibility. V2 JSON emits only the current vocabulary, and runtime does
  not emit retired reason codes.
- `ConflictPolicy` is accepted for forward compatibility. Runtime precedence
  follows the single v2 policy documented in
  [RESOLUTION.md](RESOLUTION.md); authoring services should reject unsupported
  policy values until a new contract defines their semantics.
