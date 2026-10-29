# Facts

Where a rule's input values come from, and how to write the executor that
supplies them.

A rule declares the fields it needs; the registry declares where each one comes
from. The service never guesses what a rule wants, and the rule never knows
where the data lives.

## The four kinds

Every registered field declares exactly one.

| Kind | Where the value comes from | Use it when |
|---|---|---|
| `passed` | the request's `context` | the caller already has it |
| `derived` | a formula over other registered fields | it is arithmetic over things already declared |
| `dataset` | a compiled query over a published dataset | an author should be able to build it with no code |
| `native` | a Go function the service registers | the computation genuinely lives in service code |

### passed

```json
{ "name": "requestedDays", "domain": "leave", "dataType": "decimal",
  "role": "input", "factSource": { "kind": "passed" } }
```

The caller puts it in `request.Context`. If it is missing, the decision fails
with `FACT_UNRESOLVABLE` naming the field — deliberately **not** the declared
`onEmpty`. A caller that forgot to send a value has a bug, not an empty result.

### derived

```json
{ "name": "serviceMonths", "domain": "leave", "dataType": "integer",
  "role": "input",
  "factSource": { "kind": "derived",
                  "expression": "monthsBetween($joinDate, $validAt)" } }
```

Dependencies resolve depth-first, so a formula over a dataset fact works
without the author ordering anything. `asOf` and `validAt` are available by
name without being registered.

The grammar is closed: numbers, dates, references to registered fields,
`+ - * /`, parentheses, and `monthsBetween`, `daysBetween`, `round`, `min`,
`max`, `coalesce`. Nothing else — no arbitrary identifiers, no string
concatenation, no indexing, no other function. Division by zero is an error,
never `Inf` or `NaN`, and money arithmetic is exact decimal throughout.

A cycle is detected rather than recursed into: `FACT_DERIVED_CYCLE`, naming
both fields. A stack overflow during a payroll run would take the process down.

### dataset

```json
{ "name": "availableBalance", "domain": "leave", "dataType": "decimal",
  "role": "input",
  "factSource": {
    "kind": "dataset", "dataset": "leave.balances",
    "filters": [
      { "attribute": "employeeId", "op": "equals", "value": "$subject.employeeId" },
      { "attribute": "leaveType",  "op": "equals", "value": "ANNUAL" }
    ],
    "window":    { "column": "asOfDate", "relativeTo": "$validAt", "lastDays": 366 },
    "aggregate": { "fn": "latest", "on": "balanceDays", "unit": "days" },
    "onEmpty":   "0"
  } }
```

This is the one that matters most, because **a business administrator can
author it with no code change**. The manifest says which columns exist and how
each may be used; the field says what to ask of them. The library compiles the
two into a validated query and the service runs it.

### native

```json
{ "name": "accruedBalanceToDate", "domain": "leave", "dataType": "decimal",
  "role": "input",
  "factSource": { "kind": "native", "nativeRef": "leave.accruedBalanceToDate" } }
```

```go
natives := fact.NewNativeRegistry()
err := natives.Register("leave.accruedBalanceToDate",
    func(ctx context.Context, p fact.Params) (any, error) {
        return accrual.ToDate(ctx, p.Tenant, p.EmployeeID, p.ValidAt)
    })
```

The function receives `Params` and a context carrying the per-fact timeout —
never the request envelope, so it cannot reach for data the field never
declared. It must not read a clock: the times it may use are in `Params`, which
is what makes a replay reproduce the same value.

Prefer a dataset or derived field where one will do. Those are authored and
versioned; a native function is a deployment.

## Manifests

A manifest is a read-only description of one of a service's own data shapes,
authored in that service's repository and published like any other artifact.

```json
{
	"datasetId": "leave.balances",
  "sourceService": "leave-service",
  "domain": "leave",
  "grain": "one row per employee per leave type per balance snapshot date",
  "tenantScoped": true,
  "status": "published",
  "version": "1.0.0",
  "publishedAt": "2026-01-01T00:00:00Z",
  "maxWindowDays": 366,
  "rowLimit": 1000,
	"allowedAggregates": ["latest", "sum", "avg", "min", "max"],
  "columns": [
    { "name": "employeeId",  "dataType": "string",  "filterable": true,  "aggregatable": false, "windowable": false, "pii": true },
    { "name": "leaveType",   "dataType": "enum",    "filterable": true,  "aggregatable": false, "windowable": false, "pii": false,
      "allowedValues": ["CASUAL", "ANNUAL", "SICK", "UNPAID"] },
    { "name": "balanceDays", "dataType": "decimal", "filterable": false, "aggregatable": true,  "windowable": false, "pii": false },
    { "name": "asOfDate",    "dataType": "date",    "filterable": true,  "aggregatable": false, "windowable": true,  "pii": false }
  ]
}
```

Things worth saying plainly:

- **The three booleans are permissions, not descriptions.** A column that is
  not `filterable` cannot be filtered on however sensible that would be,
  because you have not promised an index for it. Marking everything true is how
  a table scan reaches production.
- **`grain` and `tenantScoped` are optional descriptive metadata in v2.** They
  do not weaken execution: `Compile` always injects the tenant predicate.
- **There is no tenant column in the manifest.** It is implicit, which is
  precisely why an author cannot filter on it. See G9.
- **`maxWindowDays` and `rowLimit` are your protection**, not the caller's.
  Set them to what the index actually supports.

## The guardrails

Every check lives in `dataset.Compile`. A service's executor inherits all of
them and cannot skip one — which is the whole reason the library compiles and
the service only executes. See
[ADR 3](adr/0003-library-compiles-service-executes.md).

| | Check | Reason code |
|---|---|---|
| G1 | the dataset exists and is published (`deprecated` still answers) | `DATASET_NOT_PUBLISHED` / `DATASET_DEPRECATED` |
| G2 | no author string contains query or script syntax | `FACT_RAW_QUERY_REJECTED` |
| G3 | every filter column exists and is `filterable` | `FACT_COLUMN_NOT_ALLOWED` |
| G4 | the window column is `windowable`; the aggregate column is `aggregatable` | `FACT_COLUMN_NOT_ALLOWED` |
| G5 | the window is positive and within `maxWindowDays` | `FACT_WINDOW_TOO_WIDE` |
| G6 | the aggregate is one the dataset offers | `FACT_AGGREGATE_UNSUPPORTED` |
| G7 | filter values match the column type, and the enum's value set | `FACT_UNRESOLVABLE` |
| G8 | only the four substitution tokens are honoured | `FACT_RAW_QUERY_REJECTED` |
| G9 | the tenant predicate is injected; an authored one is an error | `FACT_COLUMN_NOT_ALLOWED` |
| G10 | a row limit is always set | — |
| G11 | a relative window is resolved to absolute times | `FACT_RAW_QUERY_REJECTED` |
| G12 | a personal column filtered other than the subject key is noted | `PII_COLUMN_IN_FILTER` |

Two more the library adds beyond that list:

- **`streak` and `latest` require a window.** Both are defined by row order,
  and row order over an unbounded set is whatever the store returns. Without a
  window the replay contract would fail silently.
- **The row limit is the lower of the budget's and the manifest's.** They are
  different controls: the budget protects the decision, the manifest cap
  protects the store.

### The four tokens, and only these

`$subject.employeeId`, `$subject.nodeId`, `$asOf`, `$validAt`.

A dollar sign is allowed **only when the entire value is exactly one of them**.
`$subject.salary` is refused as unknown; `E-$subject.employeeId` is refused as
a fragment, because request data concatenated into an authored string is the
shape every injection takes.

### Refused, not sanitised

A value containing a semicolon, `--`, `/*`, `*/`, a backtick, a quote, an
equals sign, a null byte, or any of `select insert update delete drop union
exec where from join` as a whole word is **refused**. The corpus of strings
that must be refused is committed at
[`testdata/security/injection-corpus.txt`](../testdata/security/injection-corpus.txt)
and asserted by FT07, so a change to the guard faces all of them again.

A sanitiser has to be right about every dialect an executor might use. A
refusal only has to be right that a legitimate filter value is never a SQL
fragment — which it is, because a filterable column is an identifier, a code, a
date, a number or a boolean. Free text is not filterable; if a tenant needs to
filter on it, give them a normalised column rather than a relaxed guard.

Quote and equals characters are banned for a specific reason: the boolean
tautology family (`' OR 'a'='a`, `1 OR 1=1`) carries no keyword to match on.
Verified: "Research and Development" and "Information Technology" still pass.

## Writing a DatasetExecutor

This is the section service teams will actually read.

```go
type Executor interface {
    Manifests() []Manifest
    Run(ctx context.Context, query Query) (Result, error)
}
```

### What the library has already checked

By the time `Run` is called, all of this is true. Do not re-check it:

- the dataset is published and is one you declared;
- every predicate column exists and is filterable;
- the window column is windowable and the window is within your maximum;
- the aggregate is one you offer, over an aggregatable column;
- every filter value is typed correctly, and within the enum's set;
- the window is **absolute** — two `time.Time` values, no tokens;
- a row limit is set and is positive;
- no author-controlled string contains query syntax.

A `Query` cannot be constructed outside `Compile`: its fields are unexported
and it exposes read accessors only.

### What your implementation must do

- **Assert the tenant predicate is present and apply it.** Do not assume the
  library added it. This is the one check that, omitted, leaks another tenant's
  data.
- Apply every predicate, the window, and the row limit.
- **Report `RowsScanned` honestly.** The budget checks it after the fact, and an
  executor reporting more than its limit fails the decision closed.
- Honour cancellation and the deadline.
- Return a **nil** `Value` when nothing matched. Not a zero — the caller cannot
  tell "no rows" from "the answer is zero", and the field's `onEmpty` exists to
  answer exactly that.
- **Never read a clock.** The window is already absolute.
- Order rows for `streak` and `latest`: most recent first, by the window column.

### A worked SQL executor

Illustrative, not code in this module — a real one belongs in your service with
your connection pool and your tests.

```go
type SQLExecutor struct {
    db        *sql.DB
    manifests map[string]dataset.Manifest
}

func (e *SQLExecutor) Manifests() []dataset.Manifest { /* your published set */ }

func (e *SQLExecutor) Run(ctx context.Context, q dataset.Query) (dataset.Result, error) {
    // 1. The one check you owe the library. A query with no tenant predicate
    //    would return every tenant's rows, so refuse it rather than trust it.
    tenant, scoped := q.TenantPredicate()
    if !scoped || len(tenant.Values) != 1 {
        return dataset.Result{}, fmt.Errorf("query for %q carries no tenant predicate", q.Dataset())
    }

    // 2. Map the dataset name to your own table. Never interpolate the name.
    table, known := e.tables[q.Dataset()]
    if !known {
        return dataset.Result{}, fmt.Errorf("unserved dataset %q", q.Dataset())
    }

    // 3. Build the WHERE clause from the predicates. Every value is a bound
    //    parameter; column names come from your own allowlist, keyed by the
    //    manifest's column names, so nothing from the query becomes SQL text.
    var where []string
    var args []any
    for _, p := range q.Predicates() {
        column, permitted := e.columns[q.Dataset()][p.Column]
        if !permitted {
            return dataset.Result{}, fmt.Errorf("column %q is not mapped", p.Column)
        }
        switch p.Op {
        case rule.FilterEquals:
            where = append(where, column+" = ?")
            args = append(args, p.Values[0])
        case rule.FilterIn:
            where = append(where, column+" IN ("+placeholders(len(p.Values))+")")
            args = append(args, p.Values...)
        case rule.FilterRange:
            where = append(where, column+" BETWEEN ? AND ?")
            args = append(args, p.Values[0], p.Values[1])
        case rule.FilterExists:
            where = append(where, column+" IS NOT NULL")
        // … notIn, notExists
        default:
            return dataset.Result{}, fmt.Errorf("unsupported operator %q", p.Op)
        }
    }

    // 4. The window is absolute: From inclusive, To exclusive, so a day
    //    belongs to exactly one window.
    order := ""
    if w, windowed := q.Window(); windowed {
        column := e.columns[q.Dataset()][w.Column]
        where = append(where, column+" >= ? AND "+column+" < ?")
        args = append(args, w.From, w.To)
        order = " ORDER BY " + column + " DESC"
    }

    // 5. Push the aggregate down where you can. A streak needs the ordered
    //    rows, so it is computed here over a bounded read.
    agg := q.Aggregate()
    // … build SELECT from agg.Fn and agg.On, with LIMIT q.RowLimit()

    // 6. Report rows scanned honestly, and nil when nothing matched.
    return dataset.Result{Value: value, RowsScanned: scanned}, nil
}
```

Notes on that sketch:

- The only strings that become SQL are **your** table and column names, from
  **your** maps. Nothing from the `Query` is interpolated — values are always
  bound parameters.
- Pushing the aggregate into SQL is the point of the row limit: `SELECT
  SUM(...)` over an indexed range beats reading rows into Go.
- For `streak`, read the ordered window under the row limit and count in Go.
  There is no portable SQL for it, which is why a dataset may legitimately
  withhold the aggregate — `payroll.overtime` does, and FT05 proves the refusal.

### What Result.Value must contain

`Value` is **nil when no row matched**, whatever the aggregate. That is how you
say "nothing"; a zero instead is wrong, because the caller cannot tell an
absent balance from a balance of zero — the field's `onEmpty` is the author's
answer to that question.

When a row did match:

| Aggregate | Value must be |
|---|---|
| `count`, `streak` | a Go integer — a count of rows is a whole number of rows |
| `exists` | `bool` |
| `latest` | the cell itself, satisfying its own column's declared `dataType` |
| `sum`, `avg`, `min`, `max` | an exactly representable number: a string, a `json.Number`, a Go integer, or the library's decimal |

**Never binary floating point, for any aggregate.** A `float64` cannot
represent most decimal money values exactly, the engine refuses to compare one,
and strict boundary validation rejects it for a field typed `decimal`, `number`
or `money`. Return `"1234.56"`, not `1234.56`.

The library's decimal type is internal, so your executor cannot name it. That
costs nothing: a decimal string is accepted everywhere the decimal itself is,
and it is the normal shape to read out of a driver.

### Logical names are yours to map

A `Query` names a dataset identifier and the column names the manifest
declares. Which table, collection or physical column those mean is **your**
business — the library neither records nor needs the mapping, which is why the
sketch above keeps its own `e.tables` and `e.columns` maps.

The one name the library fixes is the tenant column, `dataset.TenantColumn`,
which is `"tenantId"`. A manifest must not declare it, and a store that spells
it `tenant_id` or `org_id` maps it like any other column.

### Prove it

```go
func TestExecutorConformance(t *testing.T) {
    for _, manifest := range publishedManifests {
        datasettest.ConformanceSuite(t,
            func() dataset.Executor { return newSQLExecutor(t) },
            manifest,
            // Insert at least one row for the given tenant. Without this the
            // tenant-isolation check fails, because a suite with no data
            // cannot tell an isolating executor from one that ignores the
            // predicate.
            datasettest.WithTenantSeeder(func(e dataset.Executor, tenant string) error {
                return seedFixtureRows(e.(*sqlExecutor), tenant)
            }),
        )
    }
}
```

The suite lives in `dataset/datasettest` so that the shipped `dataset` package
carries no dependency on `testing`. It checks the properties the library cannot
enforce from outside: that you declare the manifest, **refuse a query with no
tenant predicate**, **actually isolate one tenant's rows from another's**, stay
inside the row limit, honour cancellation, answer the same query the same way
twice, and **return the documented type for every aggregate your manifest
offers**.

Two of those are worth spelling out:

- It builds an untenanted query, via a helper internal to the dataset module, and requires you
  to reject it. Refusing proves you *look* for the predicate; the isolation
  check proves you *apply* it, by running the same query against a store
  holding one tenant's rows and then two.
- If rows scanned grows once a second tenant has data, the suite **warns**
  rather than fails: the answer is right, but another tenant's volume is being
  charged to this tenant's `MaxRowsScanned`, so one large tenant can exhaust
  the budget for the rest. Check that your tenant column is indexed and the
  predicate is pushed into the query.

If your executor genuinely cannot be seeded from a test, pass
`datasettest.WithoutTenantIsolation()`. The suite logs a warning and you must
prove the tenant boundary another way.

For development, `ruletest.NewInMemoryExecutor` serves synthetic rows and
passes the suite itself, so a team can test its rules before writing a real
executor.

## The budget

There is no default. An unset budget is a configuration error, because a silent
default on a fact budget is a silent change to a payroll figure.

```go
FactBudget: fact.Budget{
    MaxDatasetFacts:  8,
    PerFactTimeout:   250 * time.Millisecond,
    TotalFactTimeout: 2 * time.Second,
    MaxRowsScanned:   5000,
}
```

| Field | Bounds | Pick it from |
|---|---|---|
| `MaxDatasetFacts` | distinct queries per **request**, after de-duplication | the most facts any one decision point declares, with headroom |
| `PerFactTimeout` | one executor call | your store's p99 for an indexed range read |
| `TotalFactTimeout` | the whole fetch stage, across every decision point | your service's own request budget, less the rest of its work |
| `MaxRowsScanned` | compiled into every query **and** checked afterwards | what your index supports without a scan |

**Every breach fails the whole decision.** There is no partial fact set, ever. A
missing fact silently becomes a different decision; a held decision is
recoverable and a wrong payroll figure is not.

The budget covers a **request**, not a decision point. Four checks sharing two
facts execute two queries, and the budget is spent once. That de-duplication is
mandatory rather than an optimisation: asking the same question three times
would both exhaust the budget and risk three answers.

## Reason codes

| Code | Fatal | What to look at |
|---|---|---|
| `FACT_UNRESOLVABLE` | yes | a passed field not supplied, or a declaration with nothing usable |
| `FACT_BUDGET_EXCEEDED` | yes | too many dataset facts, or an executor over its row limit |
| `FACT_TIMEOUT` | yes | the store, or a `PerFactTimeout` set below its real latency |
| `FACT_RAW_QUERY_REJECTED` | yes | **alert.** The authoring UI should have made this impossible |
| `FACT_COLUMN_NOT_ALLOWED` | yes | the manifest does not permit that use of the column |
| `FACT_AGGREGATE_UNSUPPORTED` | yes | the dataset does not offer it |
| `FACT_WINDOW_TOO_WIDE` | yes | the window against `maxWindowDays` |
| `FACT_DERIVED_CYCLE` | yes | two derived fields referring to each other |
| `FACT_SHADOWED_ATTRIBUTE` | no | a fact and a subject attribute share a name; the fact won |
| `DATASET_NOT_PUBLISHED` | yes | the manifest is missing or not published to this service |
| `DATASET_DEPRECATED` | no | migrate the field; the dataset is retiring |
| `PII_COLUMN_IN_FILTER` | no | a personal column other than the subject key in a filter |
