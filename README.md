# ArrowHR Rule Engine

`github.com/arrowhr/ruleengine` is the in-process policy engine shared by every
ArrowHR service. It resolves a tenant's rules against an effective-dated
organization hierarchy, obtains only the facts a rule declares, evaluates
deterministic decision tables, and returns a complete explanation. It exposes
no server, opens no database connection, and performs no network I/O during a
decision.

One sentence carries the whole design:

> **The code decides what is checked. The rules decide how it is checked for
> this employee.**

---

## Contents

- [Status and compatibility](#status-and-compatibility)
- [Install](#install)
- [The model in five minutes](#the-model-in-five-minutes)
- [Quick start](#quick-start)
- [The six-stage pipeline](#the-six-stage-pipeline)
- [Resolution rules](#resolution-rules)
- [Fact sources](#fact-sources)
- [Writing a DatasetExecutor](#writing-a-datasetexecutor)
- [Configuration reference](#configuration-reference)
- [Reason codes](#reason-codes)
- [Errors](#errors)
- [Observability](#observability)
- [Determinism and replay](#determinism-and-replay)
- [Money-moving decisions](#money-moving-decisions)
- [Testing your integration](#testing-your-integration)
- [**Service onboarding guide**](#service-onboarding-guide) — step by step, per microservice
- [Performance](#performance)
- [Security](#security)
- [Project layout](#project-layout)
- [Versioning and release](#versioning-and-release)
- [Contributing](#contributing)
- [Further reading](#further-reading)

---

## Status and compatibility

| | |
|---|---|
| Module | `github.com/arrowhr/ruleengine` |
| Status | **Unreleased.** Do not tag or publish until the full repository gate is green in the target CI environment. |
| Minimum Go | **1.26** (`go.mod`). CI builds with the exact release in `.go-version` and also exercises the minimum. |
| Third-party dependencies | **none.** The module has no `require` block and no `go.sum`. |
| CGO | not required. Pure Go, including both decision engines. |
| Versioning | Semantic Versioning. The wire contracts in `schema/v2` are governed as `FULL_TRANSITIVE` — additive-only. |
| Support window | the current minor and the one before it (N-1). |
| Platform | any platform Go supports. No OS-specific code. |

A breaking change to any of the nine published artifacts is a major version and
a new `schema/vN` directory, never an edit to `schema/v2`.

## Install

The canonical module path is private to ArrowHR:

```sh
go env -w GOPRIVATE=github.com/arrowhr/*
go env -w GONOSUMDB=github.com/arrowhr/*
go get github.com/arrowhr/ruleengine@<approved-version>
```

## The model in five minutes

### Rule keys name what is decided

```
urn:arrowhr:rule:leave:eligibility:min-service:v1
                 │     │           │           └── major version
                 │     │           └── name
                 │     └── rule set
                 └── domain
```

A key is a **family**, not a single rule. Many versions of it may exist, each
anchored somewhere different in the tenant's tree. The major version in the key
changes only when the input or output contract breaks.

### Anchors, and why precedence is not authored

A tenant's organization is **not** a fixed ladder. It is whatever that tenant
defines: an ordered list of levels, each with a type the tenant names, as few as
one or as many as they need. Every rule is anchored to **exactly one node**.

```
G001  RWB                        (group,      order 0)
 ├─ C001  RWB International      (company,    order 1)
 │   └─ D001  Accounting         (department, order 2)
 ├─ C002  RWB CA / Canada        (company,    order 1)
 │   ├─ D002  Engineering        (department, order 2)
 │   │   ├─ BR001  Colombo 03    (branch,     order 3)   ← the subject
 │   │   └─ BR002  Kandy         (branch,     order 3)
 │   └─ D003  Finance            (department, order 2)
 │       └─ BR003  Galle         (branch,     order 3)
 └─ C004  RWB / Sri Lanka        (company,    order 1)
     └─ D004  Operations         (department, order 2)
         └─ BR004  Colombo 07    (branch,     order 3)

An employee at BR001 has the path:   G001 → C002 → D002 → BR001
```

> Compute the subject's node path. Among the rules whose anchor lies on that
> path, **the one anchored furthest from the root wins** — whatever the levels
> are called and however many there are.

So a rule anchored at `C002` beats one anchored at `G001` for this employee, and
a rule anchored at `BR003` is not a candidate at all: Galle is not on the path.
A tenant can insert a `Team` level between Department and Branch years after
go-live without touching a rule, a schema, or a line of code.

**Precedence is derived, never authored.** There is no precedence field on a
rule. `hierarchy.DerivePrecedence` is the only thing that produces one, and it
reads the tenant's own tree.

### Filters narrow; they never promote

`attributeFilters` restrict which subjects a rule covers **within its anchor** —
`designation = Senior`, say. A filtered rule anchored at `G001` still loses to
an unfiltered rule anchored at `C002`. This is the single most common
misreading of the model, and a test exists to keep it true.

### Wholesale override is a new version, not a new field

To give Canada a different minimum service period you publish **version 1.1.0
of the same key**, anchored at `C002`. The store selects the latest effective
version per scope, then resolution picks the deepest anchor. There is no
"override" concept to learn.

### Facts are declared, never guessed

A rule may only reference a field in the `FieldRegistry`, and that registry
says where the value comes from: `passed`, `derived`, `native`, or `dataset`.
A field nobody registered is a fail-closed decision naming it — never a silent
zero.

## Quick start

This is `Example` in `example_test.go`, which CI compiles and runs against its
expected output, so it cannot drift from the API it documents. The only change
here is that the deprecated, inert `FailClosedDomains` line is omitted.

```go
// Where rules come from. A deployed service swaps this for
// store/servicestore, which reads them from the Rule Service, and changes
// nothing else.
loader, err := filestore.New("testdata")
if err != nil {
    log.Fatal(err)
}

// Where dataset-sourced facts come from. A real service implements
// dataset.Executor over its own storage and proves it with
// datasettest.ConformanceSuite.
manifests, err := ruletest.LoadManifests("leave.balances", "leave.applications")
if err != nil {
    log.Fatal(err)
}
executor, err := ruletest.NewInMemoryExecutor(manifests["leave.balances"], manifests["leave.applications"])
if err != nil {
    log.Fatal(err)
}

engine, err := ruleengine.New(ruleengine.Config{
    Tenant: "RWB",
    Domain: "leave",
    // A fact budget is required, not defaulted: these numbers decide how
    // much one decision may cost, and a silent default on them would be a
    // silent change to a payroll figure.
    FactBudget: fact.Budget{
        MaxDatasetFacts:  8,
        PerFactTimeout:   250 * time.Millisecond,
        TotalFactTimeout: 2 * time.Second,
        MaxRowsScanned:   5000,
    },
},
    ruleengine.WithLoader(loader),
    ruleengine.WithExecutor(executor),
)
if err != nil {
    log.Fatal(err)
}
defer func() { _ = engine.Close() }()

// Load once at start-up. With a CacheReloadInterval set, also call
// engine.Start() to reconcile in the background.
if err := engine.Reload(context.Background()); err != nil {
    log.Fatal(err)
}
if err := engine.Ready(); err != nil {
    log.Fatal(err)
}

// One decision point. The rule key names what is being decided; the rules
// decide how it is decided for this particular employee.
decision, err := engine.Evaluate(context.Background(), ruleengine.EvalRequest{
    SchemaVersion: 2,
    Tenant:        ruleengine.Tenant{ID: "RWB"},
    Routing:       ruleengine.Routing{RuleKey: "urn:arrowhr:rule:leave:eligibility:min-service:v1"},
    Subject: ruleengine.Subject{
        Type: "employee",
        Identifier: ruleengine.Identifier{
            System: "urn:arrowhr:ns:employee-id", Value: "E1001", IDType: "employeeId",
        },
        // The subject's node is what precedence is derived from: the
        // deepest rule anchor on this employee's path wins.
        NodeID: "BR001",
    },
    // Facts the caller already has. Anything else the rule declares is
    // fetched by the engine, within the budget.
    Context:       map[string]any{"joinDate": "2026-05-01"},
    AsOf:          time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC),
    CorrelationID: "example-1",
})
if err != nil {
    // A Go error means retry, alert or fix configuration. A business
    // refusal is not an error: it is a Decision with Failed set.
    log.Fatal(err)
}

if decision.Failed {
    fmt.Println("refused:", decision.ReasonCodes)
    return
}
fmt.Printf("rule %s decided at %s: eligible=%v\n",
    decision.RuleVersion, decision.ResolvedAt.NodeID, decision.Result["eligible"])

// Output:
// rule 1.1.0 decided at C002: eligible=true
```

### Several checks at once

This is the call a service actually makes. A user sees every problem with their
request in one response, and each fact is fetched once across the whole call.

```go
points := []ruleengine.Point{
    {Key: "urn:arrowhr:rule:leave:eligibility:min-service:v1", DecisionType: "leave-eligibility"},
    {Key: "urn:arrowhr:rule:leave:validation:balance:v1", DecisionType: "leave-validation"},
}

results, err := engine.EvaluateAll(ctx, points, request)
if err != nil {
    return err
}
if !results.AllPassed() {
    return refusal(results.Failures())
}

// Output:
// all passed: true
//   min-service  map[eligible:true reasonCode:OK]
//   balance      map[allowed:true reasonCode:BALANCE_SUFFICIENT]
```

### Explaining a decision

```go
for _, evaluated := range decision.Evaluated {
    if evaluated.Winner {
        fmt.Printf("winner     %s anchored %s\n", evaluated.RuleVersion, evaluated.Anchor.NodeID)
        continue
    }
    fmt.Printf("eliminated %s anchored %s: %s\n",
        evaluated.RuleVersion, evaluated.Anchor.NodeID, evaluated.Reason)
}

// Output:
// winner     1.1.0 anchored C002
// eliminated 1.0.0 anchored G001: Company(1) outranks Group(0)
```

## The six-stage pipeline

```
EvalRequest
     │
     ▼
┌──────────────┐  catalog lookup (tenant, domain, ruleSet, asOf)
│ 1. Resolve   │  → subject node path, root to leaf
│              │  → keep anchors on the path, deepest wins
└──────┬───────┘  → filters narrow within an anchor
       ▼
┌──────────────┐  run manifest pin │ ruleVersionPin │ effective-at(validAt)
│ 2. Version   │  versions are collapsed per scope before resolution,
└──────┬───────┘  so a routine version bump is never a conflict
       ▼
┌──────────────┐  for each declared input not already in request.Context:
│ 3. FetchFacts│  passed (no-op) │ derived (compute) │ native (ContextBuilder)
└──────┬───────┘  │ dataset (Compile → your Executor). Budgeted, de-duplicated.
       ▼
┌──────────────┐  request context, now complete, and the rule's declared
│ 4. Validate  │  inputs and outputs, against the FieldRegistry.
└──────┬───────┘  An undeclared field fails loudly.
       ▼
┌──────────────┐  Engine.Evaluate(logic, input).
│ 5. Evaluate  │  Pure: no clock, no I/O, no randomness.
└──────┬───────┘
       ▼
┌──────────────┐  build the Decision: resolvedAt, evaluated[], anchorPath,
│ 6. Map + Log │  factsResolved, reasonCodes. Audit record emitted
└──────┬───────┘  asynchronously (synchronously when durable audit is required).
       ▼
   Decision
```

Each stage opens its own trace span: `resolve`, `version`, `facts`,
`validate`, `engine`, `map`, inside `ruleengine.Evaluate`.

## Resolution rules

`resolve.Resolve` is pure — given a path, a candidate set and a hierarchy
snapshot it is a function. In order:

1. The subject's path is computed **root to leaf**.
2. Drop any candidate whose **tenant** differs; record `cross-tenant`. The
   catalog is tenant-keyed, so this is defence in depth and an alertable
   anomaly if it fires.
3. Drop any candidate whose **status** is not evaluable. Only `active` and
   `scheduled` are.
4. Drop any candidate whose **effective interval** does not contain the
   business time.
5. Keep candidates whose **anchor is on the path**; record `not-on-path` for
   the rest.
6. Keep candidates whose **filters all match**; record the attribute that
   failed for the rest.
7. Rank by **anchor depth, descending**. Prefer greater depth.
8. Break a remaining tie by **filter count, descending** — more specific wins.
9. Break what remains by **`tieBreakPriority`**, lowest first.
10. If candidates are still indistinguishable, select the lexically earliest
    deterministically and attach a `Tie` explanation.
11. Mark the winner; mark every other survivor with the reason it lost, naming
    both level names and depths.

Candidates are sorted before any decision is taken, so two runs over the same
data produce the same `Evaluated` list byte for byte, independent of map
iteration or file order.

**The tie rule, in one sentence.** Two rules that remain equally specific at one
anchor after every tie-break yield a stable, order-independent winner plus a
non-fatal `PRECEDENCE_CONFLICT` and a `Tie` explanation naming every candidate,
because a held decision in a payroll run is worse than a reported one that an
operator must then correct at publish time.

**The fail-closed rule, in one sentence.** Anything the library cannot resolve
soundly — no hierarchy, an unknown subject node, no rule on the path, an
unresolvable fact, an undeclared field, an unavailable pin — produces a
`Decision` with `Failed` set, a fatal reason code and the candidate list built
so far, never an empty result with a nil error and never a substituted default
for a money or compliance outcome.

A business refusal is **also** a `Decision`, with `Failed` **unset**: the rules
said no and nothing is broken.

## Fact sources

A registered field declares exactly one source.

| Kind | Who provides it | When to choose it | Declared with |
|---|---|---|---|
| `passed` | the caller, in `request.Context` | the service already has the value in hand | nothing more |
| `derived` | the library, from already-resolved fields | a formula over other registered fields | `expression`, e.g. `monthsBetween($joinDate, $validAt)` |
| `native` | your `ContextBuilder` | a computation that genuinely lives in your code — joins, cross-service reads, shift and calendar logic | `builderFn` |
| `dataset` | `dataset.Compile` → your `Executor` | an aggregate over your own rows that an author should be able to express declaratively | `datasetId`, `filter`, `window`, `aggregate` |

Prefer `dataset` or `derived` where either will do, because those are authored,
versioned and lintable. Reach for `native` when the computation is code.

A `dataset` recipe passes twelve guardrails before it becomes a query:

| | Guardrail | Reason code on refusal |
|---|---|---|
| G1 | the dataset is published (`deprecated` is allowed, with a note) | `DATASET_NOT_PUBLISHED` |
| G2 | no author string contains query or script syntax | `FACT_RAW_QUERY_REJECTED` |
| G3 | every filter column exists and is `filterable` | `FACT_COLUMN_NOT_ALLOWED` |
| G4 | the window column is `windowable`, the aggregate column `aggregatable` | `FACT_COLUMN_NOT_ALLOWED` |
| G5 | the window is positive and within `maxWindowDays` | `FACT_WINDOW_TOO_WIDE` |
| G6 | the aggregate is one the dataset offers | `FACT_AGGREGATE_UNSUPPORTED` |
| G7 | filter values match the column type and its `allowedValues` | `FACT_UNRESOLVABLE` |
| G8 | only the four substitution tokens are honoured | `FACT_RAW_QUERY_REJECTED` |
| G9 | the tenant predicate is injected; an author cannot write one | `FACT_COLUMN_NOT_ALLOWED` |
| G10 | a row limit is always set — there is no unbounded query | `FACT_UNRESOLVABLE` |
| G11 | a relative window is resolved to absolute times | — (always applied) |
| G12 | a personal column used as a filter is noted | `PII_COLUMN_IN_FILTER` (non-fatal) |

The only four tokens a filter value may contain, each as the **whole** value:

```
$subject.employeeId   $subject.nodeId   $asOf   $validAt
```

`$input.*` is **refused**, not partially supported: a filter cannot yet
reference a value from the request. Until it can, use a `native` fact for a
per-request filter.

Order-dependent aggregates (`latest`, `streak`) **require a window**, because
without one the result depends on row order and the same request would not
replay to the same answer.

## Writing a DatasetExecutor

Every service implements this once, over its own storage.

```go
type Executor interface {
    Manifests() []dataset.Manifest
    Run(ctx context.Context, query dataset.Query) (dataset.Result, error)
}
```

By the time a `Query` reaches you, the library has already checked everything
checkable: the dataset is published, every column is permitted for the use it
is put to, the window is within bounds and resolved to absolute times, values
type-check, no token survives, and a row limit is set. A `Query` has no
exported fields and no constructor, so one cannot be built outside `Compile`.

**The one thing you owe: assert the tenant predicate is present, and apply it.**

```go
func (e *LeaveExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
    // 1. Refuse a query that is not tenant-scoped. Do not assume the library
    //    added one — assert it.
    tenantPredicate, ok := query.TenantPredicate()
    if !ok {
        return dataset.Result{}, fmt.Errorf("refusing an unscoped query for %s", query.Dataset())
    }

    // 2. Map the dataset to your own table.
    table, ok := e.tables[query.Dataset()]
    if !ok {
        return dataset.Result{}, fmt.Errorf("unknown dataset %s", query.Dataset())
    }

    // 3. Translate predicates into your dialect with bound parameters only.
    where := []string{fmt.Sprintf("%s = ?", quoteIdent(tenantPredicate.Column))}
    args := []any{tenantPredicate.Values[0]}
    for _, predicate := range query.Predicates() {
        if predicate.Injected {
            continue // already applied above
        }
        clause, bound := translate(predicate) // never string-concatenate a value
        where = append(where, clause)
        args = append(args, bound...)
    }

    // 4. A window is a half-open interval: From inclusive, To exclusive.
    if window, ok := query.Window(); ok {
        where = append(where, fmt.Sprintf("%s >= ? AND %s < ?",
            quoteIdent(window.Column), quoteIdent(window.Column)))
        args = append(args, window.From, window.To)
    }

    // 5. Honour the row limit, and report rows scanned honestly: the fact
    //    budget checks it after the fact, and an executor that overruns
    //    fails the decision closed rather than being trusted.
    aggregate := query.Aggregate()
    sql := fmt.Sprintf("SELECT %s FROM %s WHERE %s LIMIT %d",
        selectExpr(aggregate), table, strings.Join(where, " AND "), query.RowLimit())

    // 6. Honour cancellation: you are running inside a bounded fetch.
    row := e.db.QueryRowContext(ctx, sql, args...)
    ...
    return dataset.Result{Value: value, RowsScanned: scanned}, nil
}
```

### What to return

`Result.Value` is **nil when no row matched**, whatever the aggregate — a zero
instead is wrong, because the caller cannot tell an absent balance from a
balance of zero. When a row did match:

| Aggregate | Value must be |
|---|---|
| `count`, `streak` | a Go integer |
| `exists` | `bool` |
| `latest` | the cell itself, satisfying its own column's declared `dataType` |
| `sum`, `avg`, `min`, `max` | an exactly representable number: a string, a `json.Number`, a Go integer, or the library's decimal |

**Never binary floating point, for any aggregate.** A `float64` cannot
represent most decimal money values exactly, the engine refuses to compare one,
and strict validation rejects it for a `decimal`, `number` or `money` field.
Return `"1234.56"`, not `1234.56`. The library's decimal type is internal, so
your executor cannot name it — which costs nothing, since a decimal string is
accepted everywhere the decimal itself is.

`RowsScanned` is charged to the request's `MaxRowsScanned` budget, so report it
honestly: an executor that reads every tenant's rows and filters in memory
spends one tenant's budget on another's data.

### Prove it

```go
func TestExecutorConformance(t *testing.T) {
    datasettest.ConformanceSuite(t,
        func() dataset.Executor { return NewLeaveExecutor(testDB(t)) },
        manifestUnderTest,
        // Insert at least one row for the given tenant. Without this the
        // tenant-isolation check fails, because a suite with no data cannot
        // tell an isolating executor from one that ignores the predicate.
        datasettest.WithTenantSeeder(seedRowsFor),
    )
}
```

The suite lives in `dataset/datasettest`, so the shipped `dataset` package
carries no dependency on `testing`. It checks seven properties the library
cannot enforce from outside: that you declare the manifest, refuse a query with
no tenant predicate, actually isolate one tenant's rows from another's, stay
inside the row limit, honour cancellation, answer deterministically, and return
the documented type for every aggregate your manifest offers.

Refusing an untenanted query proves you *look* for the predicate; the isolation
check proves you *apply* it. If rows scanned grows once a second tenant has
data the suite warns rather than fails — the answer is right, but another
tenant's volume is being charged to this tenant's budget, so check that your
tenant column is indexed. `datasettest.WithoutTenantIsolation()` opts out
loudly, for an executor that genuinely cannot be seeded from a test.

Until the real executor exists, use `ruletest.NewInMemoryExecutor` so the rest
of the work is not blocked. `query.Explain()` renders a query in prose, which is
the fastest way to see what you have been asked for:

```
The most recent balanceDays from leave.balances where employeeId is E1042
and leaveType is ANNUAL, for asOfDate from 2025-10-14 up to 2026-10-15,
scoped to this tenant, at most 1000 rows
```

## Configuration reference

Everything arrives through `Config` and options. **The library reads no
environment variables**, so a service can audit its own configuration in one
place.

### Config

| Field | Default | Consequence |
|---|---|---|
| `Tenant` | required | every request is checked against it; one service cannot evaluate another tenant's subject |
| `Domain` | required | every request is checked against it; one service cannot evaluate another domain's rules |
| `FactBudget` | **required, never defaulted** | bounds what one request may spend fetching facts. An unset budget is a configuration error, because a silent default on a fact budget is a silent change to a payroll figure |
| `Validation` | `ValidationStrict` | boundary strictness; see below |
| `CacheReloadInterval` | `0` | how often the cache reconciles in the background. **Zero means no goroutine at all** — the right choice for a batch process that wants its rules pinned for the whole run |
| `MaxCandidatesPerKey` | `200` when zero | caps how many candidates one key may resolve over. Exceeding it is a data error worth failing on rather than a cost paid per decision. Zero selects the built-in default, not "unbounded"; a negative value is rejected |
| `TraceEnabled` | `false` | asks for a per-row evaluation trace. Useful for support and Rule Studio; costs allocation per decision |
| `RequireDurableAudit` | `false` | refuses construction unless the decision logger implements `audit.DurableDecisionLogger`. Enable it for money, compliance and any decision whose record may not be lost |
| `RequireIdempotency` | `false` | marks decisions money-moving: every request must carry a UUID `IdempotencyKey` and an `IdempotencyStore` must be configured |
| `IdempotencyRetention` | `24h` | how long completed keys stay reserved. A value below 24 h is rejected; payroll should choose longer |
| `FailClosedDomains` | — | **deprecated and inert.** Default eligibility now comes solely from exact, tenant-authorized `RuleCatalog.defaults` entries. Publish or omit a catalog default instead |

`Config.Validate()` is called by `New` and returns `ErrConfig` wrapped with the
specific problem.

### Validation modes

| Mode | Schema | Registry | Use |
|---|---|---|---|
| `ValidationStrict` | yes | yes | the default, and the only mode a production service should run |
| `ValidationSchemaOnly` | yes | no | trades the registry's guarantees for its per-decision cost |
| `ValidationOff` | no | no | a trusted internal caller that has already validated, or a batch replaying a frozen run |

Validation is roughly **90% of a decision's cost** (see
[Performance](#performance)). Rather than relaxing it, call
`ruleengine.ValidateRequest(request)` once at your HTTP boundary and keep the
engine strict where the decision is made.

> `ValidateRequest` checks the raw envelope against the published schema and
> performs no routing derivation, so it requires `Routing.Domain` to be set
> explicitly. `Evaluate` derives routing from `Routing.RuleKey` when the
> domain is absent, so a request that `Evaluate` accepts can still fail
> `ValidateRequest`. **Populate `Routing.Domain` on every request** — it is
> what the wire contract requires, and it makes the two agree.

### The fact budget

```go
FactBudget: fact.Budget{
    MaxDatasetFacts:  8,                      // distinct queries, counted AFTER de-duplication
    PerFactTimeout:   250 * time.Millisecond, // bounds one executor call
    TotalFactTimeout: 2 * time.Second,        // bounds the whole fetch stage
    MaxRowsScanned:   5000,                   // compiled into every query AND checked afterwards
}
```

Guidance:

- **`MaxDatasetFacts`** — count the distinct dataset facts across all the
  decision points one request evaluates, then add headroom. Leave needs about
  8; a payroll line needs more.
- **`PerFactTimeout`** — your storage's p99 for the slowest manifest, not its
  median.
- **`TotalFactTimeout`** — must leave room inside your own request deadline for
  everything after the decision.
- **`MaxRowsScanned`** — a ceiling, not a target. The library compiles it into
  every query and re-checks what the executor reports: an executor that
  overruns fails the decision closed rather than being trusted.

A batch process should set these higher than an interactive one, and pin its
rules with a run manifest.

### Options

| Option | Default | What it is for |
|---|---|---|
| `WithLoader` | none | where rules come from: `store/filestore` for local work, `store/servicestore` for the Rule Service, or your own `store.Loader` |
| `WithExecutor` | none | your `dataset.Executor`. Without one, any `dataset` fact fails closed |
| `WithContextBuilder` | none | your `contextbuilder.ContextBuilder` for `native` fields |
| `WithNativeFacts` | none | a `fact.NativeRegistry` of individually registered native functions — finer-grained than a single builder |
| `WithDecisionLogger` | `audit.Nop()` | where decision records go. Wrap in `audit.Async` for a non-blocking hot path |
| `WithManifestStore` | none | the `manifest.InMemoryStore` holding run manifests. Required for pinned batch runs |
| `WithIdempotencyStore` | none | required when `RequireIdempotency` is set. `NewMemoryIdempotencyStore` is single-process only; production needs shared storage |
| `WithMetrics` | `telemetry.Nop()` | your metrics adapter |
| `WithTracer` | `telemetry.NopTracer()` | your tracing adapter |
| `WithLogger` | discard | a `*slog.Logger` for the library's own operational logging |
| `WithEngine` | `decisiontable` | an alternate `engine.Engine`. Rarely needed |
| `WithClock` | system clock | an injectable clock for cache lifecycle and audit timestamps. **Never** affects a decision's outcome: business time arrives on the request |

### Engine lifecycle

```go
engine, err := ruleengine.New(config, options...)  // validates; opens nothing
err = engine.Reload(ctx)                           // first full load, at start-up
err = engine.Start()                               // background reconcile; no-op if interval is 0
err = engine.Ready()                               // gate your readiness probe on this
decision, err := engine.Evaluate(ctx, request)     // per decision
results, err := engine.EvaluateAll(ctx, pts, req)  // several points, shared facts
stats := engine.Stats()                            // cache counts and staleness
err = engine.Close()                               // stops reconcile, drains buffered audit
```

Construct **one engine per service** and keep it. It is safe for concurrent use
by many goroutines: cache reads are lock-free and every evaluation keeps its
state on its own stack.

## Reason codes

`ruleengine.ReasonCodes()` returns the whole catalogue, so you can build an
operations table from it rather than transcribing this one. `Fatal()` reports
whether a code forces a fail-closed decision; **an unknown code is fatal**, so
malformed external data cannot accidentally fail open.

| Code | Fatal | What it means | Operator action |
|---|---|---|---|
| `OK` | no | the rules decided | none |
| `SCHEMA_INVALID` | **yes** | the request does not satisfy the published envelope | fix the caller |
| `HIERARCHY_UNAVAILABLE` | **yes** | no hierarchy snapshot is loaded | check the loader and readiness; the engine should not have been serving |
| `HIERARCHY_NOT_CONFIGURED` | **yes** | the tenant has no resolvable tree, or the subject's `nodeId` is unknown, inactive, or absent | onboard the tenant's hierarchy, or fix the employee record |
| `NO_RULE_RESOLVED` | **yes** | no candidate's anchor is on the subject's path | publish a root-anchored baseline for this tenant |
| `DEFAULT_APPLIED` | no | an authorized catalog default was used in place of a rule | confirm the default is still wanted |
| `PRECEDENCE_CONFLICT` | no | equal-rank rules escaped publication; a deterministic winner was used | **alert.** Correct the catalog: set a priority or narrow a filter |
| `CROSS_TENANT_RULE_DROPPED` | no | a candidate from another tenant was discarded | **alert.** This should be impossible through the catalog |
| `PINNED_VERSION_UNAVAILABLE` | **yes** | the pinned version is missing, or is not an approved status | check the run manifest and the published version set |
| `FIELD_NOT_REGISTERED` | **yes** | a rule or request names a field the registry does not define | run `rulelint` over rules and registry together |
| `FIELD_DEPRECATED` | no | a deprecated field was used | migrate the rule before the field is withdrawn |
| `FIELD_TYPE_MISMATCH` | **yes** | a value does not match its registered type | fix the caller or the registry |
| `FACT_UNRESOLVABLE` | **yes** | a declared input could not be obtained | missing passed value, no executor, or no native builder |
| `FACT_BUDGET_EXCEEDED` | **yes** | the request wanted more facts than the budget allows | raise the budget deliberately, or reduce the rule's declared inputs |
| `FACT_TIMEOUT` | **yes** | a fetch exceeded its timeout | investigate the executor's storage |
| `FACT_RAW_QUERY_REJECTED` | **yes** | an author value contained query syntax or a bad token | fix the recipe; `rulelint` catches this at publish |
| `FACT_COLUMN_NOT_ALLOWED` | **yes** | a column was used for something its manifest does not permit | fix the recipe or extend the manifest deliberately |
| `FACT_AGGREGATE_UNSUPPORTED` | **yes** | the aggregate is not in `allowedAggregates` | fix the recipe or extend the manifest |
| `FACT_WINDOW_TOO_WIDE` | **yes** | the window exceeds `maxWindowDays` | narrow the window |
| `FACT_DERIVED_CYCLE` | **yes** | derived fields reference each other in a cycle | fix the registry |
| `FACT_SHADOWED_ATTRIBUTE` | no | a passed value shadowed a resolvable attribute | usually a caller sending more than it needs |
| `DATASET_NOT_PUBLISHED` | **yes** | a live rule reaches an unpublished dataset | publish the manifest, or fix the recipe |
| `DATASET_RECIPE_INVALID` | **yes** | a recipe failed a guardrail | fix at authoring; this should never appear at decision time |
| `DATASET_DEPRECATED` | no | a deprecated dataset was queried | migrate before it is withdrawn |
| `PII_COLUMN_IN_FILTER` | no | a personal column was used as a filter | confirm it is intended; check the redaction path |
| `ENGINE_ERROR` | **yes** | evaluation itself failed | a library or logic defect; capture the decision id and escalate |
| `OUTPUT_CONTRACT_VIOLATION` | **yes** | the result does not satisfy the declared outputs | fix the rule or the registry |
| `CACHE_STALE` | no | the decision used a snapshot older than the staleness threshold | check the loader; the decision still stands |

Two v1 codes are retained for source compatibility, are never emitted, and are
not in the catalogue: `PRECEDENCE_TIE_UNRESOLVED` (use `PRECEDENCE_CONFLICT`)
and `SUBJECT_NODE_UNRESOLVABLE` (use `HIERARCHY_NOT_CONFIGURED`). Do not key
alerts off either.

The table above is the catalogue in pipeline order, so it matches
`ReasonCodes()` row for row.

## Errors

The rule that matters:

> **A business refusal is a `Decision`. A Go error means retry, alert, or fix
> your configuration.**

So `if err != nil` and `if decision.Failed` mean different things and need
different handling. Never treat a refusal as an outage, and never swallow an
error as a refusal.

Sentinels, all matchable with `errors.Is`:

| Sentinel | Raised when |
|---|---|
| `ErrConfig` | the configuration is invalid; returned by `New` and `Config.Validate` |
| `ErrNotReady` | no snapshot has been published yet |
| `ErrHierarchyUnavailable` | no hierarchy is loaded for the tenant |
| `ErrPinnedVersionUnavailable` | a pin cannot be satisfied |
| `ErrFactBudget` | the fact budget was exhausted |
| `ErrFactTimeout` | a fact fetch timed out |
| `ErrEngine` | the decision engine failed |
| `ErrIdempotency` | an idempotency-store operation failed |
| `ErrIdempotencyConflict` | the same key arrived with a different request fingerprint |

Operational failures carry structured, non-PII context:

```go
var ruleErr *ruleengine.Error
if errors.As(err, &ruleErr) {
    log.Error("decision failed",
        "code", ruleErr.Code,       // the ReasonCode
        "field", ruleErr.Field,     // which field, when relevant
        "ruleKey", ruleErr.RuleKey,
        "dataset", ruleErr.Dataset)
}
```

## Observability

### Metrics

| Metric | Type | Labels |
|---|---|---|
| `rule_eval_duration_ms` | histogram | domain, rule_set, name |
| `rule_eval_total` | counter | domain, rule_set, name, outcome |
| `rule_eval_errors_total` | counter | domain, reason_code |
| `rule_candidates_considered` | histogram | domain, rule_set |
| `precedence_tie_total` | counter | domain, rule_set |
| `cross_tenant_rule_dropped_total` | counter | domain |
| `fact_fetch_duration_ms` | histogram | field, kind |
| `fact_rows_scanned` | histogram | field |
| `fact_budget_exceeded_total` | counter | domain, reason |
| `fact_cache_hits_total` | counter | field |
| `rule_cache_entries` | gauge | — |
| `rule_cache_stale_seconds` | gauge | — |
| `hierarchy_cache_stale_seconds` | gauge | — |
| `audit_dropped_total` | counter | — |

### The alerts that matter

| Condition | Why it matters |
|---|---|
| `precedence_tie_total` > 0 | two rules are equally specific at one anchor. A publishing mistake that a deterministic fallback is currently papering over. **A near-miss, not noise.** |
| `cross_tenant_rule_dropped_total` > 0 | a tenant boundary was crossed. Should be impossible through a tenant-keyed catalog |
| `audit_dropped_total` > 0 | decision evidence was lost. If this can matter, you want `RequireDurableAudit` |
| `rule_cache_stale_seconds` above your reload interval × 3 | reconcile is failing. Decisions still stand on the last good snapshot, which is why this is a warning rather than an outage |
| `rule_eval_errors_total{reason_code="NO_RULE_RESOLVED"}` rising | usually a tenant missing a root-anchored baseline — the most common adoption failure |
| `fact_budget_exceeded_total` rising | a dataset grew, or a rule's declared inputs did |

### Tracing and logs

Spans: `ruleengine.Evaluate` wrapping `resolve`, `version`, `facts`,
`validate`, `engine`, `map`. Every span is closed on every path, including a
fail-closed path, and records the reason code that ended it. W3C `traceparent`
and `correlationId` arrive on the request and are carried onto the `Decision`
and the audit record.

**The library's operational logs carry no personal data.** `FactResolved` and
`audit.Fact` have no value field at all — only the field name, its kind, rows
scanned and elapsed time. Span attributes must not carry personal data either.

The audit record is different on purpose: it carries identifiers, because a
decision cannot be explained without them, and the audit store is
access-controlled. With `RequireDurableAudit` it also carries the exact
effective inputs for replay; those can contain PII and must never be copied
into operational logs. `audit.SlogSink` is for development — it emits
`decisionId`, `correlationId` and `inputsHash`, but no subject identifier and
no result values.

## Determinism and replay

> Same `{ruleKey, ruleVersion, asOf/validAt, input}` ⇒ same output.

- The engine is pure: no clock reads, no I/O, no randomness. **Time enters only
  as `AsOf` and `ValidAt`.**
- Candidate ordering is explicit, so the `Evaluated` list is byte-identical
  across replicas.
- A `WithClock` clock drives cache lifecycle and audit timestamps only. Moving
  it a year forward does not change a decision.
- Hierarchy snapshots are effective-dated and swap atomically with the rule
  set, so a decision can never resolve a node path against one hierarchy
  version and a rule anchor against another.

### Reproducing a batch run

A run manifest freezes a run. It must be **fully** frozen: the library refuses
a manifest that pins rules without also declaring its fact pins and hierarchy
snapshot.

```go
manifests := manifest.NewInMemoryStore()
err := manifests.Put(manifest.RunManifest{
    RunID:               "payroll-2026-10",
    Tenant:              "RWB",
    ValidAt:             periodEnd,
    RulePins:            map[string]string{"urn:arrowhr:rule:payroll:overtime:threshold:v1": "2.1.0"},
    FactPins:            map[string]string{},
    HierarchySnapshotID: "h-2026-10",
    Status:              manifest.StatusOpen,
    OpenedAt:            time.Now(),
})

engine, _ := ruleengine.New(config, ruleengine.WithManifestStore(manifests))

// Every request in the run carries the RunID. None resolves "latest".
request.RunID = "payroll-2026-10"
```

Re-running the same `RunID` reproduces byte-identical decisions, `DecisionID`
aside — that is unique per evaluation by design.

**A retroactive run** sets `AsOf` to now and `ValidAt` to the historical
business time, and gets the rule versions **and** the organization tree as they
were. A reorganization in September cannot change an August result.

## Money-moving decisions

For a decision that moves money, make retries safe:

```go
engine, err := ruleengine.New(ruleengine.Config{
    Tenant: "RWB", Domain: "payroll", FactBudget: payrollBudget,
    RequireIdempotency:   true,  // every request must carry a UUID idempotencyKey
    RequireDurableAudit:  true,  // refuse to start without a durable audit logger
    IdempotencyRetention: 90 * 24 * time.Hour,
},
    ruleengine.WithIdempotencyStore(sharedStore),  // NOT the in-memory one
    ruleengine.WithDecisionLogger(outboxLogger),
)
```

A retry with the same key returns the first stored result without re-evaluating.
The same key with a different request fingerprint returns
`ErrIdempotencyConflict`. `NewMemoryIdempotencyStore` coalesces within one
process only — production needs storage shared by every replica.

## Testing your integration

`ruletest` is a public package, meant for your service's tests.

```go
func TestLeaveEligibility(t *testing.T) {
    bundle := ruletest.NewFixtureBuilder("RWB").
        WithRWBHierarchy().
        WithRegistry("leave").
        WithManifests("leave.balances").
        Rule("leave", "eligibility", "min-service").
        AnchoredAt("G001").
        Needs("serviceMonths").Produces("eligible", "reasonCode").
        Row("eligible", map[string]string{"serviceMonths": ">= 6"},
            map[string]any{"eligible": true, "reasonCode": "OK"}).
        Row("not-yet", map[string]string{},
            map[string]any{"eligible": false, "reasonCode": "MIN_SERVICE_NOT_MET"}).
        Add().
        MustBuild(t)

    engine := newTestEngine(t, bundle)
    decision, err := engine.Evaluate(context.Background(), request)
    if err != nil {
        t.Fatal(err)
    }
    ruletest.DecisionEquals(t, decision, ruletest.Expectation{
        ResolvedNodeID: "G001",
        Result:         map[string]any{"eligible": true},
    })
}
```

| | For |
|---|---|
| `NewFixtureBuilder`, `NewHierarchyBuilder` | rules and trees in a few lines instead of twenty fields |
| `RunGolden`, `RunGoldenOn`, `LoadGolden` | your own golden cases, as one sub-test each |
| `NewInMemoryExecutor` | dataset facts before the real executor exists |
| `DecisionEquals`, `AssertPassed`, `AssertFailed`, `AssertResolvedAt` | assertions that report every difference at once |
| `Explain` | the report to print when one fails |
| `NewFakeClock` | cache staleness and reconcile intervals without sleeping |
| `LoadRWB`, `LoadRule`, `LoadManifest`, `LoadRegistry`, `Budget` | the shared fixtures |

**Golden cases are the artifact a business owner can check.** They name no Go
type and no engine, so the same file proves the rule to your team, to Rule
Studio, and to whoever asks in a year why the number was what it was.

### Offline tools

```sh
make tools                 # builds bin/rulelint and bin/ruleplay

bin/rulelint ./rules ./registry        # publish-time checks, for CI
bin/ruleplay -root ./rules -request ./request.json -rows ./rows.json -trace
```

`rulelint` catches a rule declaring an unregistered field, a filter on a
hierarchy attribute, a dataset field its manifest does not permit, an anchor in
no hierarchy, unreachable rows, deprecated fields, and two equally specific
rules at one anchor — all before anything is published. Run it in CI.

`ruleplay` explains one decision: every candidate, why each lost, and which
facts were fetched. It is the fastest route to "why did this employee get that
answer".

---

## Service onboarding guide

Every one of the fourteen services follows the same eight steps. Budget two to
four weeks per service, most of it in steps 1 and 2 — deciding what is
rule-relevant and naming the fields. The wiring is an afternoon.

### Before you start

- The service's feature sheet reviewed, and its rule-relevant checks listed.
- `FieldRegistry` entries drafted for every field those checks need.
- Dataset manifests drafted for the shapes behind them.
- An owner named for the executor and for any native functions.
- A shadow-metrics dashboard cloned from another service's.

### Step 1 — Decide what is rule-relevant

Not every `if` is a rule. The test is whether a tenant could reasonably want it
different.

| Rule-relevant | Not |
|---|---|
| minimum service before casual leave | whether a leave request has an end date |
| maximum consecutive days | whether the employee exists |
| overtime threshold and multiplier | how overtime is stored |
| whether three late days cost half a day | what "late" means technically |

Keep the second column in code. A rule nobody will ever vary is a deployment
cost with no benefit. Of Leave's thirteen features, ten had rule work — about
twenty-eight rule keys. That ratio is typical.

### Step 2 — Declare the fields

```json
{ "name": "serviceMonths", "domain": "leave", "dataType": "integer",
  "unit": "months", "pii": false, "ownerService": "employee-service",
  "status": "active", "role": "input",
  "factSource": { "kind": "derived",
                  "expression": "monthsBetween($joinDate, $validAt)" },
  "effective": {} }
```

Two things teams get wrong:

- **Field names are not the tenant's to choose.** The registry defines them and
  tenants conform, or are normalised by a per-tenant alias upstream. One tenant
  calling it `monthsOfService` is how a rule set stops being portable.
- **Hierarchy placement is not a field.** Department, company and branch are
  implicit in the subject's `nodeId`. The registry rejects a filter on one, and
  `rulelint` catches it before publishing.

### Step 3 — Publish the manifests

Roughly five per service. Each describes one data shape an author may build a
field from, and its limits. The three permission booleans — `filterable`,
`windowable`, `aggregatable` — are **promises about your indexes**, not
descriptions. A column you do not mark cannot be used that way, however
sensible it would be.

### Step 4 — Implement the executor

See [Writing a DatasetExecutor](#writing-a-datasetexecutor). Prove it with
`datasettest.ConformanceSuite`; use `ruletest.NewInMemoryExecutor` until then.

### Step 5 — Wire the engine, once, at start-up

```go
engine, err := ruleengine.New(ruleengine.Config{
    Tenant: tenantID,
    Domain: "leave",
    FactBudget: fact.Budget{
        MaxDatasetFacts:  8,
        PerFactTimeout:   250 * time.Millisecond,
        TotalFactTimeout: 2 * time.Second,
        MaxRowsScanned:   5000,
    },
    CacheReloadInterval: 30 * time.Second,
},
    ruleengine.WithLoader(servicestore.New(servicestore.Config{
        BaseURL: ruleServiceURL, Service: "leave-service", Tenant: tenantID,
        Headers: authHeaders,
    })),
    ruleengine.WithExecutor(myExecutor),
    ruleengine.WithDecisionLogger(auditLogger),
    ruleengine.WithMetrics(myMetrics),
    ruleengine.WithTracer(myTracer),
    ruleengine.WithLogger(myLogger),
)
if err != nil { return err }
if err := engine.Reload(ctx); err != nil { return err }
if err := engine.Start(); err != nil { return err }
defer engine.Close()
```

Gate traffic on readiness, so the service never accepts a request it would only
fail closed:

```go
mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
    if err := engine.Ready(); err != nil {
        http.Error(w, err.Error(), http.StatusServiceUnavailable)
        return
    }
    w.WriteHeader(http.StatusOK)
})
```

A batch process wants `CacheReloadInterval: 0` and no `Start()`, so its rules
cannot change underneath a run.

### Step 6 — Register the decision points

Name the checks a request runs through, in one place:

```go
var leaveApplicationPoints = []ruleengine.Point{
    {Key: "urn:arrowhr:rule:leave:eligibility:min-service:v1",    DecisionType: "leave-eligibility"},
    {Key: "urn:arrowhr:rule:leave:validation:max-consecutive:v1", DecisionType: "leave-validation"},
    {Key: "urn:arrowhr:rule:leave:validation:balance:v1",         DecisionType: "leave-validation"},
}

results, err := engine.EvaluateAll(ctx, leaveApplicationPoints, request)
if err != nil {
    return fmt.Errorf("evaluate leave application: %w", err)
}
if !results.AllPassed() {
    return refusal(results.Failures())
}
```

A new tenant policy changes a rule. A new *kind* of check changes this list.

**What triggers an evaluation:** data, or a code point reaching a decision —
never a user asking for a rule to be applied. "Three consecutive late days" is
a *fact* that attendance resolves; the rule's condition matches on it. There is
no "evaluate my rules" button.

### Step 7 — Shadow mode

Do not cut over on faith. Run the engine beside the code it will replace.

```go
func (s *Service) checkEligibility(ctx context.Context, a Application) bool {
    legacy := s.legacyEligibility(a)   // stays authoritative

    decision, err := s.engine.Evaluate(ctx, s.requestFor(a, eligibilityKey))
    switch {
    case err != nil:
        s.log.Warn("shadow evaluation errored", "error", err)
    case decision.Failed:
        s.metrics.Counter("rule_shadow_failed", label("check", "eligibility")).Inc()
        s.log.Warn("shadow failed closed", "reasons", decision.ReasonCodes)
    default:
        proposed, _ := decision.Result["eligible"].(bool)
        if proposed != legacy {
            // The line a team watches during adoption.
            s.metrics.Counter("rule_shadow_disagreement", label("check", "eligibility")).Inc()
            s.log.Warn("shadow disagreement",
                "legacy", legacy, "rule", proposed,
                "ruleVersion", decision.RuleVersion, "decisionId", decision.DecisionID)
        }
    }
    return legacy   // unchanged until this check is cut over
}
```

Shadow mode never fails the request, never changes the answer, and records
enough to explain a disagreement afterwards — the rule version and the decision
id are the two things you will want. A disagreement is either a bug in the rule
or a behaviour the legacy code had that nobody had written down. Both are worth
finding before a tenant does.

`examples/leaveservice` is this, runnable, and produces a real disagreement.

### Step 8 — Cut over, one check at a time

```
off → shadow → enforce(canary tenants) → enforce(all)
```

Gate each step on metrics, not dates: disagreements at zero, or every remaining
one explained and accepted. Keep the legacy code for at least one release after
enforcing, so a rollback is a config change.

> **Do not delete a legacy `if` before a root-anchored baseline rule exists for
> every tenant.** Without one, a tenant that never configured a rule gets
> `NO_RULE_RESOLVED` — a fail-closed decision where the old code had an answer.
> This is the single most likely way to break a tenant during adoption.

Enforce per check, not per service.

---

### The three integration archetypes

Almost every service is one of these three. Find yours and copy it.

#### Archetype A — passed and derived facts only (the simplest)

The caller already has everything the rules need. **No executor, no context
builder.** Employee Management, Letters, Documents, Admin and Assets are
usually this.

```go
engine, _ := ruleengine.New(ruleengine.Config{
    Tenant: tenantID, Domain: "employee",
    // Every budget field must be positive, even with no dataset facts: the
    // library refuses a partially specified budget rather than inventing the
    // missing half. Keep the numbers small to make the intent obvious.
    FactBudget: fact.Budget{
        MaxDatasetFacts:  1,
        PerFactTimeout:   50 * time.Millisecond,
        TotalFactTimeout: 200 * time.Millisecond,
        MaxRowsScanned:   1,
    },
    CacheReloadInterval: 30 * time.Second,
}, ruleengine.WithLoader(loader))

decision, _ := engine.Evaluate(ctx, ruleengine.EvalRequest{
    SchemaVersion: 2,
    Tenant:  ruleengine.Tenant{ID: tenantID},
    Routing: ruleengine.Routing{
        Domain:  "employee",
        RuleKey: "urn:arrowhr:rule:employee:probation:duration:v1",
    },
    Subject: ruleengine.Subject{
        Type: "employee",
        Identifier: ruleengine.Identifier{
            System: "urn:arrowhr:ns:employee-id", Value: "E1001", IDType: "employeeId",
        },
        NodeID: "BR001",
    },
    Context: map[string]any{"joinDate": "2026-05-01", "employmentType": "permanent"},
    AsOf:    time.Now().UTC(),
})
```

#### Archetype B — dataset facts (the common case)

A rule needs an aggregate over your own rows. **Implement an executor.** Leave,
Benefits, Performance, Recruitment, Learning & Development and Reporting are
this.

The field declares the recipe; the author never writes a query:

```json
{ "name": "availableBalance", "domain": "leave", "dataType": "decimal",
  "unit": "days", "ownerService": "leave-service", "status": "active",
  "role": "input",
  "factSource": {
    "kind": "dataset",
    "datasetId": "leave.balances",
    "filter": [
      { "column": "employeeId", "op": "equals", "value": "$subject.employeeId" },
      { "column": "leaveType",  "op": "equals", "value": "ANNUAL" }
    ],
    "window":    { "column": "asOfDate", "relativeTo": "$asOf", "lastDays": 90 },
    "aggregate": { "fn": "latest", "column": "balanceDays" },
    "onEmpty":   "0"
  } }
```

Remember that `latest` **requires** a window. Wire the executor and nothing
else changes:

```go
engine, _ := ruleengine.New(config,
    ruleengine.WithLoader(loader),
    ruleengine.WithExecutor(leaveExecutor),   // the only addition
)
```

The decision tells you what it cost:

```go
for _, f := range decision.FactsResolved {
    log.Info("fact", "field", f.Field, "kind", f.Kind,
        "dataset", f.Dataset, "rows", f.RowsScanned, "ms", f.ElapsedMS)
}
// field=availableBalance kind=dataset dataset=leave.balances rows=1 ms=0
```

#### Archetype C — native facts (when the computation is code)

A fact a dataset cannot express: a cross-service join, shift and calendar
logic, a streak over a roster. Attendance is the canonical case; IAM's
attribute rules are similar.

Declare the field as `native` with a `builderFn`, then register the function:

```go
native := fact.NewNativeRegistry()
err := native.Register("consecutiveLateDays",
    func(ctx context.Context, params fact.Params) (any, error) {
        // Times arrive in params. Never read a clock here: the same request
        // replayed for a payroll correction must produce the same value.
        return attendance.CountConsecutiveLate(ctx,
            params.Tenant, params.EmployeeID, params.ValidAt)
    })

engine, _ := ruleengine.New(config,
    ruleengine.WithLoader(loader),
    ruleengine.WithNativeFacts(native),
)
```

Or supply one builder for several fields at once:

```go
type AttendanceBuilder struct{ store *Store }

func (b *AttendanceBuilder) Build(
    ctx context.Context, fields []string, subjectID, period string,
) (map[string]any, error) {
    out := make(map[string]any, len(fields))
    for _, field := range fields {
        switch field {
        case "consecutiveLateDays":
            v, err := b.store.ConsecutiveLate(ctx, subjectID, period)
            if err != nil { return nil, err }
            out[field] = v
        case "averageLatenessMinutes":
            v, err := b.store.AverageLateness(ctx, subjectID, period)
            if err != nil { return nil, err }
            out[field] = v
        }
        // Omit what you cannot answer. A missing field becomes a fail-closed
        // decision naming it, which is recoverable. A guessed zero is a wrong
        // answer that looks right.
    }
    return out, nil
}

engine, _ := ruleengine.New(config,
    ruleengine.WithLoader(loader),
    ruleengine.WithContextBuilder(&AttendanceBuilder{store: store}),
)
```

A `ContextBuilder` may satisfy **only** fields registered as `native`. It
cannot shadow a `dataset` or `derived` field.

---

### Per-service specifics

Rollout order: pilot, then the rule-heavy services, then their consumers, then
the rest.

| Wave | Service | Domain | Archetype | What is distinctive |
|---|---|---|---|---|
| Pilot | **Leave** | `leave` | B | ~28 keys across eligibility, validation, accrual. Balances and applications as datasets. The reference integration — read `examples/leaveservice` |
| 1 | **Attendance** | `attendance` | C | Streaks, shifts and calendars that no dataset expresses. Penalties are exact decimal day-fractions, never floats |
| 1 | **Payroll** | `payroll` | B + C | **Run manifests are mandatory.** Set `RequireIdempotency` and `RequireDurableAudit`; retention ≥ 90 days. Rules return `{threshold, multiplier}` — the service does the arithmetic |
| 2 | **Benefits** | `benefits` | B | Entitlement bands by grade and service. Watch `maxWindowDays` on long-horizon datasets |
| 2 | **Employee Management** | `employee` | A | Probation, confirmation and notice periods. Owns `joinDate` and most derived service fields — the registry's upstream |
| 2 | **Performance** | `performance` | B | Review-cycle eligibility and rating bands. Cycle windows are datasets, not request-relative |
| 3 | **Recruitment** | `recruitment` | B | Approval thresholds by headcount and grade |
| 3 | **Learning & Development** | `learning` | B | Mandatory-training rules; completion history as a dataset |
| 3 | **Letters** | `letters` | A | Which template and which approval chain. Almost entirely passed facts |
| 4 | **IAM** | `iam` | C | **Authorization stays in Keycloak and IAM.** Use the engine only for attribute-based data rules, never to decide who may call what |
| 4 | **Assets** | `assets` | A | Allocation entitlement by grade and node |
| 4 | **Documents** | `documents` | A | Retention and visibility rules. Mind `pii` flags on every field |
| 4 | **Reporting** | `reporting` | B | Read-only. Consider `ValidationSchemaOnly` here — and only here — if validation cost shows up in a wide report |
| 4 | **Admin** | `admin` | A | Tenant-level configuration rules |

What **every** service owns for itself, shared with nobody except the library:

- its own `FieldRegistry` entries for its own domain,
- its own dataset manifests,
- its own `Executor` and `ContextBuilder`,
- its own decision-point list,
- its own fact budget.

Action contracts (a per-service list of the facts each action supplies) are not part
of 1.0. Until they exist, a value taken from the current request is supplied by a
native fact in the service's `ContextBuilder`; see `docs/OPEN_QUESTIONS.md`.

Nothing is shared between services except this library. A cross-service effect
travels as an event the owning service publishes, never as a direct call into
another service's rules.

### Debugging

| Symptom | First thing to check |
|---|---|
| `NO_RULE_RESOLVED` | is any anchor on the subject's path? Run `ruleplay` |
| `HIERARCHY_NOT_CONFIGURED` | is `nodeId` set, known in this tenant, and active? |
| `PRECEDENCE_CONFLICT` | two rules equally specific at one anchor. A publishing mistake |
| `FACT_UNRESOLVABLE` | a passed field missing from the request, a dataset with no executor, or a native field with no builder |
| `FIELD_NOT_REGISTERED` | run `rulelint` over rules and registry together |
| the wrong rule won | `ruleplay` prints every candidate and why each lost |
| decisions got slow | fact count or validation; see [Performance](#performance) |
| `ErrNotReady` in production | your readiness probe is not gated on `engine.Ready()` |

---

## Performance

Measured on a development laptop with an in-memory executor, so these are the
library's share of a decision — resolution, version selection, compiling and
de-duplicating fact queries, validation, evaluation and building the decision.
Reproduce with `make bench`.

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `Pipeline/datasetFacts=0` | 155,057 | 63,247 | 836 |
| `Pipeline/datasetFacts=1` | 206,504 | 79,561 | 1,260 |
| `Pipeline/datasetFacts=5` | 347,565 | 142,354 | 2,937 |
| `PipelineEvaluateAll` (3 points, 5 facts) | 768,528 | 292,892 | 5,108 |
| `ValidationModes/strict` | 159,826 | 63,253 | 836 |
| `ValidationModes/schemaOnly` | 160,867 | 63,118 | 829 |
| `ValidationModes/off` | **16,586** | 10,269 | 108 |
| `hierarchy.Path` (4-level) | 122 | 160 | 1 |
| `resolve.Resolve/candidates=1` | 434 | 464 | 2 |
| `resolve.Resolve/candidates=10` | 3,194 | 2,672 | 11 |
| `resolve.Resolve/candidates=100` | 36,687 | 25,059 | 102 |

The cost model:

- **Validation dominates — about 90% of time and 87% of allocations.** It is
  the schema walk, not the registry: `schemaOnly` costs the same as `strict`
  to within noise. Call `ValidateRequest` once at your HTTP boundary rather
  than relaxing the mode at the decision.
- **Each extra dataset fact costs roughly 40 µs and ~420 allocations** of
  library work, plus whatever your storage takes. Fact count is the lever that
  matters.
- **De-duplication is not an optimisation, it is the contract.** Across the
  points of one `EvaluateAll`, a repeated fact is fetched once. Use
  `EvaluateAll` rather than a loop: three points sharing five facts cost less
  than three separate evaluations would.
- **Resolution is cheap and scales linearly in candidates.** A path lookup is
  ~122 ns and one allocation (it returns a defensive copy). Candidate count is
  rarely your problem; keep an eye on it only past a few dozen per key.

The programme budget is a full evaluation at p99 under 5 ms excluding your
storage. CI fails the build on an allocation regression beyond 20%, and on a
wall-clock regression beyond 2× — wall-clock on shared runners is too noisy for
a tighter gate.

## Security

- **No server, no listener, no outbound connection** during a decision. The
  only network I/O is a loader you supply, between decisions.
- **No database driver anywhere in the module.** `dataset` compiles a
  structured `Query`; your executor runs it. The library holds no credential.
- **An author cannot write a cross-tenant query.** The tenant predicate is
  injected by `Compile` and flagged `Injected`; an author-supplied one is
  refused. An executor is expected to assert it rather than trust it.
- **No raw query syntax reaches storage.** Author values are refused, not
  sanitised, and a 42-entry injection corpus plus `FuzzRejectQuerySyntax` keep
  it that way.
- **No unbounded query.** Every compiled query carries a row limit, and the
  reported rows scanned is re-checked afterwards.
- **No personal data in operational logs or span attributes.** Fact values are
  not recorded at all. PII-flagged results are redacted before a record reaches
  a logger. Exact inputs exist only under `RequireDurableAudit`, for the
  access-controlled audit store.
- **No binary floating point for money.** Exact decimals, ISO currency
  validation, half-even minor-unit rounding; `float64` money is rejected by a
  lint rule.
- **Closed schemas.** `additionalProperties: false` on every artifact, so an
  unexpected field is a rejection rather than a silent ignore.
- CI runs `govulncheck`, CodeQL and dependency review on every push.

Report a suspected vulnerability per `SECURITY.md`. Do not open a public issue.

## Engine pool

One `Engine` serves one tenant. A service that serves many tenants uses the
`pool` package rather than a hand-built map of engines, so tenants can be
onboarded without a restart:

```go
tenants, _ := pool.New(func(ctx context.Context, tenantID string) (*ruleengine.Engine, error) {
    return ruleengine.New(configFor(tenantID),
        ruleengine.WithLoader(loaderFor(tenantID)),
        ruleengine.WithDecisionLogger(pool.NoCloseLogger(audit)))
}, pool.WithIdleTTL(30*time.Minute), pool.WithMaxTenants(500))
defer tenants.Close(ctx)

engine, err := tenants.For(ctx, tenantID) // builds once, then cached
decision, err := engine.Evaluate(ctx, request)
```

`For` builds on first use, `Get` only looks up (no I/O, safe inside a database
transaction), and `Warm` builds ahead of time. A failed tenant is remembered for
a short backoff and never affects another. The pool closes an engine only when
no `Evaluate` on it is running. See `pool` and ADR 0012.

## Project layout

| Package | Responsibility |
|---|---|
| `.` (root) | the facade: `Engine`, `EvalRequest`, `Decision`, `Config`, options, the six-stage pipeline. Holds no algorithm |
| `rule` | `RuleDefinition`, anchors, filters, effective dating, `RuleCatalog`, URN keys. The vocabulary everything else speaks |
| `hierarchy` | org trees, immutable snapshots, root-to-leaf paths, inherited attributes, derived precedence |
| `resolve` | deepest-anchor-wins over a path. **Pure** — no I/O, no clock, no logging |
| `registry` | the `FieldRegistry`, loud-fail validation, and the four fact-source declarations |
| `dataset` | manifests, the twelve guardrails, the compiled `Query`, the `Executor` interface |
| `dataset/datasettest` | the executor conformance suite, kept out of `dataset` so no consumer links `testing` |
| `fact` | fact orchestration across all four kinds, the budget, de-duplication, the native registry |
| `contextbuilder` | the `ContextBuilder` interface for `native` fields |
| `engine` | the `Engine` interface and the conformance harness |
| `engine/decisiontable` | the production decision-table engine: hit policies, operators, `$field` references |
| `engine/testengine` | a simple reference engine; the conformance target |
| `store` | the `Loader` contract, immutable snapshots, atomic copy-on-write cache, version selection |
| `store/filestore` | a loader over a directory of files, for local work and tests |
| `store/servicestore` | a bounded, retrying HTTPS loader for the Rule Service |
| `manifest` | run manifests and their local store |
| `audit` | decision records, async and outbox loggers, the slog sink |
| `authz` | subtree role grants, shared with the Rule Service and Rule Studio. **Not called during evaluation** |
| `telemetry` | metric, span and label names, and the adapter interfaces |
| `schema` | the embedded JSON Schema v2 contracts |
| `ruletest` | public fixtures, builders, golden runner and assertions, for **your** tests |
| `cmd/rulelint` | the publish-time linter. Run it in CI |
| `cmd/ruleplay` | explains one decision offline |
| `cmd/schemacompat` | enforces the additive-only schema gate |
| `examples/leaveservice` | a runnable reference integration, including shadow mode |
| `internal/…` | `expr`, `decimalx`, `urnkey`, `clock`, `deepcopy`, `schemacompat`. Not importable by consumers |

Dependencies run one way. The root package is the only wiring point: it imports
the subsystems and adapts between them, and none of them imports it back.
`architecture_test.go` enforces this by parsing imports, because an import
cycle discovered later is a refactor rather than a fix.

## Versioning and release

- Semantic Versioning. A breaking change to any published artifact is a major
  version and a new `schema/vN` directory.
- `schema/v2` is `FULL_TRANSITIVE`: additive-only, enforced by
  `internal/schemacompat` against a frozen baseline in `schema/compat/v2` and
  by `make schema-compat` in CI.
- Every release needs a `## [x.y.z]` entry in `CHANGELOG.md`; the release
  workflow greps for it and fails the tag without one.
- The supported window is the current minor and the one before it.

## Contributing

```sh
make check     # vet lint test race cover tidy schema schema-compat docs
```

The full gate must be green before review. See `CONTRIBUTING.md` for the
commit conventions and `CODEOWNERS` for reviewers. Significant decisions are
recorded as ADRs in `docs/adr`; add one rather than changing an existing
record.

Coverage floors: 85% overall, and 95% for `resolve`, `fact`, `dataset`,
`engine/decisiontable` and `store`.

## Further reading

| | |
|---|---|
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | where the library sits, what it will not do, and why |
| [`docs/RESOLUTION.md`](docs/RESOLUTION.md) | the resolution algorithm in full, with a worked trace |
| [`docs/FACTS.md`](docs/FACTS.md) | the four fact kinds, the guardrails, and a worked SQL executor |
| [`docs/INTEGRATION.md`](docs/INTEGRATION.md) | the per-service playbook in depth |
| [`docs/OBSERVABILITY.md`](docs/OBSERVABILITY.md) | metrics, spans and which alerts matter |
| [`docs/OPEN_QUESTIONS.md`](docs/OPEN_QUESTIONS.md) | what is still undecided, and the known gaps |
| [`docs/adr/`](docs/adr) | why each significant choice was made |

## License

See [`LICENSE`](LICENSE).
