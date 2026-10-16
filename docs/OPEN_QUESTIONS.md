# Open questions

Decisions this library needs from the architect or the owner, and gaps a later
phase closes. An entry stays here until it is closed by an ADR, a specification
change, or the phase that implements it.

## Needs a decision from the owner

### Q1. Ratify the D22 refinement: the executor receives a compiled query

The build specification is stricter than plan v2.0 Appendix B, which passed a
raw `FactSource` to a service's executor. [ADR 3](adr/0003-library-compiles-service-executes.md)
records the reasoning. The practical difference is that every guardrail moves
into the library and a service team cannot omit one. Needs confirming at the
Phase 0 ratification of D22.

### Q2. Confirm the module path and the VCS host before the first tag

`go.mod` declares `github.com/arrowhr/ruleengine`. A published tag must not
carry a placeholder, because fourteen services will pin it.

### Q3. Confirm the Go version floor

`go.mod` sets `go 1.24` as the floor. A library should not force a toolchain
upgrade on its consumers, so this only rises when every service team is already
past it.

### Q4. Agree the fact budget defaults

An unset `fact.Budget` is treated as a configuration error rather than silently
defaulting, because a silent default on a fact budget is a silent change to a
payroll figure. The starting numbers for `MaxDatasetFacts`, `PerFactTimeout`,
`TotalFactTimeout` and `MaxRowsScanned` are still open (plan O16).

### Q5. As-of hierarchy queries, or a node path recorded in the run manifest

A retroactive run must resolve the subject's node path as it was at `validAt`,
not as it is today (plan section 5.4). Either the hierarchy read model supports
as-of querying inside the library, or the run manifest records the path it used.
The second is simpler and is enough for replay; the first is needed if a
retroactive run has to resolve a subject the manifest never saw (plan O18).

### Q6. Does statutory jurisdiction arrive on the request or come from the node

`EvalRequest.EmploymentCountry` carries it today, while `ResolveAttribute` can
also derive country from the nearest declaring ancestor. Both exist; which one
is authoritative for statutory rules is open (plan O19).

### Q7. Where telemetry adapters live

The module ships `telemetry` interfaces and a no-op. Whether OpenTelemetry and
Prometheus adapters ship as separate modules in this repository or are written
by each consumer is open.

### Q8. Which audit sinks the library should ship

`audit` will ship an slog sink for development. Whether it also ships an HTTP
sink to the Rule Service, or every service writes its own, is open.

### Q9. Confirm the three dependency substitutions

The module currently has no third-party dependencies because the build
environment cannot resolve them. `internal/decimalx`, the closed schema
validator and the hand-written property generators stand in for
`shopspring/decimal`, `santhosh-tekuri/jsonschema/v6` and `pgregory.net/rapid`.
See [ADR 5](adr/0005-dependency-choices.md). The decimal substitute is on the
money path and is the one to revisit first.

### Q10. ~~Should the manifest reader take a context and return an error~~ — resolved

Resolved per the specification: the interface is `manifest.Store` with
`Get(ctx, runID) (RunManifest, bool, error)`, and the in-memory implementation
is `manifest.InMemoryStore`. A remote reader can now honour a decision's
deadline, and "no manifest for this run" is distinguishable from "the store is
unreachable".

### Q11. ~~Who owns the background reconcile loop~~ — resolved

Resolved per the specification: the root facade owns it. `Engine.Start` begins
reconciliation and is a no-op when `CacheReloadInterval` is zero, so a service
that wants no goroutine gets none and a batch run can pin its rules for a whole
run. `Engine.Close` stops it and drains the audit logger.

### Q12. There is no "registry only" validation mode, and strict is expensive

Boundary validation is 88% of a decision's time and 90% of its allocations:
135 microseconds and 743 allocations with `ValidationStrict`, against 17
microseconds and 77 allocations with `ValidationOff`. `ValidationSchemaOnly`
costs the same as strict, because the cost is the schema walk rather than the
registry checks.

The three modes the specification names are schema-and-registry, schema-only,
and nothing. The mode a production service most wants is the one missing:
**registry but not schema** — "I validated the JSON at my HTTP boundary; check
my facts against the field contracts". Without it, a service that has already
validated at its edge either pays twice or gives up the registry's guarantees
entirely.

`ValidateRequest` is exported so a service can do the schema check at its own
boundary, but there is no mode that then skips it per decision. Either add a
fourth mode, or redefine `ValidationSchemaOnly` to mean registry-only, which is
what its position between strict and off suggests. A decision for the owner,
since it changes a documented enum.

## Known gaps a later phase closes

### G1. The engine caches a compiled table by content, not by rule identity

`engine.Engine.Evaluate` receives a `rule.RuleLogic` value and no rule identity,
so `engine/decisiontable` identifies an authored table by fingerprinting its
content on every call. The fingerprint is allocation-light and costs about 60
microseconds for a hundred-row table, which fits the pipeline budget, but it is
still work proportional to the table on every evaluation.

The exported `Compile` and `Table.Evaluate` already allow the alternative:
compile each rule version once when it enters the rule cache — versions are
immutable, so a compiled table is valid for the life of the version — and
evaluate the stored `*Table` directly. Taking that path needs a decision in the
phase that builds the store and the root facade, because it either widens the
`engine.Engine` interface or has the store hold engine-specific compiled values.

### G2. ~~A dataset manifest has no per-dataset row limit~~ — resolved

`dataset.Manifest.RowLimit` and the schema's `rowLimit` now carry the optional
per-dataset cap from plan Appendix F.1, and `Compile` applies the lower of it
and `Budget.MaxRowsScanned`. The two remain different controls: the budget
protects the decision, the cap protects the store.

### G3. ~~The dataset fixture set is incomplete~~ — resolved

`testdata/datasets/` now holds the four manifests, every dataset-kind field
carries its filters and window, and `testdata/security/injection-corpus.txt` is
committed and asserted by FT07 and by the `dataset` guard tests.

### G4. `ConflictPolicy` is carried but not consulted

`rule.RuleDefinition.ConflictPolicy` is accepted, schema-validated and ignored
by resolution, which applies the fixed ladder in
[ADR 4](adr/0004-fail-closed-on-tie.md): filter specificity, then
`TieBreakPriority`, then fail closed. Plan section 5.2 describes the same ladder
as the meaning of `most-specific-wins`, so behaviour matches intent, but an
author setting `conflictPolicy` to anything else today changes nothing. Either
the Rule Service must reject values the library does not implement, or the field
should be removed from the authored contract.

### G5. Attendance `penalty` is a decimal, not a reason string

Plan Appendix A.8 shows a golden case expecting `penalty: "HALF_DAY_NO_PAY"`,
but `testdata/registry/attendance-fields.json` declares `penalty` as `decimal`
and `halfDayNoPay` as `boolean`. The fixtures follow the registry: `penalty` is
the deducted day fraction (`0.5`), and `halfDayNoPay` carries the flag. Worth
confirming with the attendance owner, since the plan example reads the other
way.

### G6. `maxConsecutiveCasualDays` counts applications, not calendar days

`streak` is defined as the run of most-recent rows, ordered by the window
column, in which the aggregate column holds — the definition FT01 pins, where
an employee whose latest day is not late has a streak of zero. Over
`leave.applications` that yields the run of consecutive approved casual
*applications* ending at `validAt`, which is what the field's description now
says.

A calendar-day run ("the longest stretch of consecutive dates covered by casual
leave") is a different question, and the manifest grammar cannot express it: it
needs a window over a date pair rather than a single column, and an aggregate
that reasons about gaps. Related to plan open decisions A-O1 and A-O2. Worth
confirming with the leave owner which one the rule actually wants.

### G7. A column-pair window is refused rather than supported

`FactWindow` carries `FromColumn` and `ToColumn` as well as `LastDays`, but
`Compile` refuses the column-pair form instead of narrowing it to one of the
two columns. A leave application spanning a window boundary therefore cannot
yet be filtered on "overlaps this window", only on "started in it". Same
grammar gap as G6.

### G8. ~~A pinned version is not re-fetched from the loader before failing~~ — resolved

Resolved per the specification: the pipeline asks its loader once when a pin
misses, then fails closed with `PINNED_VERSION_UNAVAILABLE`. One decision can
trigger at most one such fetch.

### G9. `Config.FailClosedDomains` has no effect yet

The pipeline stage says no rule resolving should fail closed *"unless the rule
set declares a low-risk default and the domain is not in
Config.FailClosedDomains"*. Nothing in the rule model lets a rule set declare
such a default — there is no field for it on `RuleDefinition` or anywhere else
— so every domain fails closed and the configuration field gates nothing.

That is the safe behaviour, and the field is kept so a configuration can state
the intent and behaviour cannot change under it later. Closing this needs a
decision on where a default lives and who may author one, which is a Rule
Service and authoring question rather than a library one.

### G10. The `WithClock` option names an internal type

`WithClock(clock.Clock)` is public but its parameter type lives in `internal/`,
so a consumer can pass a value with a `Now() time.Time` method but cannot
declare a variable of that type. It works, and it is what the specification
says, but it reads badly from outside. `store` already declares its own `Clock`
for the same reason; promoting one to the root package would make the option
self-describing.
