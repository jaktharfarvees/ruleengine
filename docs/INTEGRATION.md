# Integration

The playbook for putting this library into one service. Every service follows
the same one.

Budget two to four weeks per service, most of it in steps 1 and 2 — deciding
which checks are rule-relevant and naming the fields. The wiring is an
afternoon.

## Definition of ready

Before writing code:

- the service's feature sheet reviewed, and its rule-relevant checks listed;
- `FieldRegistry` entries drafted for every field those checks need;
- dataset manifests drafted for the data shapes behind them;
- an owner named for the executor and for any native functions;
- the shadow-metrics dashboard cloned from another service's.

## Step 1 — Decide what is rule-relevant

Not every `if` is a rule. The test is whether a tenant could reasonably want it
different.

| Rule-relevant | Not |
|---|---|
| minimum service before casual leave | whether a leave request has an end date |
| maximum consecutive days | whether the employee exists |
| overtime threshold and multiplier | how overtime is stored |
| whether three late days cost half a day | what "late" means technically |

Keep the second column in code. A rule that nobody will ever vary is a rule
with a deployment cost and no benefit.

Of Leave's thirteen features, ten had rule work — about twenty-eight rule keys.
That ratio is typical.

## Step 2 — Declare the fields

A rule can only reference a registered field. The registry is the contract
between the author and your service: it fixes the name, the type, the unit, the
PII flag, and **where the value comes from**.

```json
{ "name": "serviceMonths", "domain": "leave", "dataType": "integer",
  "unit": "months", "pii": false, "sourceService": "employee-service",
  "status": "active", "role": "input",
  "factSource": { "kind": "derived",
                  "expression": "monthsBetween($joinDate, $validAt)" },
  "effective": {} }
```

Two things teams get wrong here:

- **Field names are not the tenant's to choose.** The registry defines them and
  tenants conform, or are normalised through a per-tenant alias upstream. One
  tenant calling it `monthsOfService` is how a rule set stops being portable.
- **Hierarchy placement is not a field.** Department, company and branch are
  implicit in the subject's `nodeId`. The registry rejects a filter on one, and
  `rulelint` catches it before publishing.

See [FACTS.md](FACTS.md) for choosing between the four kinds.

## Step 3 — Publish the manifests

Roughly five per service. Each describes one data shape an author may build a
field from, and its limits. The three permission booleans are promises about
your indexes — see [FACTS.md](FACTS.md#manifests).

## Step 4 — Implement the executor

```go
type Executor interface {
    Manifests() []Manifest
    Run(ctx context.Context, query Query) (Result, error)
}
```

The library has already checked everything checkable. Your job is to translate
a structured value into your dialect and run it. The one thing you owe:
**assert the tenant predicate is present and apply it.**

Prove it with `datasettest.ConformanceSuite`. Until then, use
`ruletest.NewInMemoryExecutor` so the rest of the work is not blocked.

[FACTS.md](FACTS.md#writing-a-datasetexecutor) has a worked SQL executor.

## Step 5 — Wire the engine

Once, at start-up. Keep it; it is safe for concurrent use and its caches are
why evaluation needs no network hop.

Construct the Rule Service loader with an HTTPS endpoint and a client carrying
the service's mTLS or JWT configuration:

```go
loader, err := servicestore.New(servicestore.Config{
    BaseURL:          "https://rule-service.internal",
    Service:          "leave",
    Tenant:           tenantID,
    Client:           serviceHTTPClient,
    Headers:          map[string]string{"Authorization": "Bearer " + token},
    MaxAttempts:      3,
    RetryBackoff:     100 * time.Millisecond,
    RequestTimeout:   5 * time.Second,
    MaxResponseBytes: 32 << 20,
})
if err != nil { return err }
```

The loader refuses redirects so tenant and authorization data cannot be sent
to another origin, accepts only JSON success responses, and leaves the last
good cache intact when loading fails. Plain HTTP requires
`AllowInsecureHTTP: true` and is restricted to loopback endpoints for local
development. `New` remains lazy; the first network call occurs in `Reload`.

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
    RequireDurableAudit: true,
    CacheReloadInterval: 30 * time.Second,
},
    ruleengine.WithLoader(loader),
    ruleengine.WithExecutor(myExecutor),
    ruleengine.WithDecisionLogger(auditLogger),
    ruleengine.WithMetrics(myMetrics),
    ruleengine.WithTracer(myTracer),
    ruleengine.WithLogger(myLogger),
)
if err != nil { return err }
if err := engine.Reload(ctx); err != nil { return err }
if err := engine.Start(); err != nil { return err }   // background reconcile
defer engine.Close()
```

The tracer adapter must read `telemetry.PropagationFromContext` when it starts
the root span, then return the SDK's span-bearing context. This preserves the
request's W3C `traceparent` through every evaluation stage and fact call. See
[OBSERVABILITY.md](OBSERVABILITY.md) for the complete metric and span contract.

Then gate traffic on readiness, so the service never accepts a request it would
only fail closed:

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

### Run manifests stay local

Create one process-local manifest store at service startup and populate it
from the run-open response or the service's distribution consumer before any
employee in that run is evaluated:

```go
runManifests := manifest.NewInMemoryStore()
if err := runManifests.Put(openedRunManifest); err != nil {
    return fmt.Errorf("cache run manifest: %w", err)
}

engine, err := ruleengine.New(config,
    ruleengine.WithLoader(loader),
    ruleengine.WithManifestStore(runManifests),
)
```

The Rule Service call or event handling belongs in the control path, not in
`Evaluate`. The engine accepts only its local `InMemoryStore`; a missing
manifest fails closed as `PINNED_VERSION_UNAVAILABLE`. One immutable manifest
view supplies hierarchy, rule, and fact-source pins for the entire evaluation
session, including every point in `EvaluateAll`.

### Money-moving services

Payroll disbursement, final settlement, and other operations that can move
money must enable both safeguards and use an idempotency store shared by every
replica:

```go
engine, err := ruleengine.New(ruleengine.Config{
    Tenant:               tenantID,
    Domain:               "payroll",
    RequireDurableAudit:  true,
    RequireIdempotency:   true,
    IdempotencyRetention: 7 * 24 * time.Hour,
},
    ruleengine.WithDecisionLogger(outboxLogger),
    ruleengine.WithIdempotencyStore(sharedIdempotencyStore),
)
```

Generate one canonical RFC UUID when the business operation is created and
reuse it on every retry. The first successful result is retained and replayed;
the engine rejects the same key with different inputs as
`ErrIdempotencyConflict`. The store's atomic `Do` operation must coalesce
concurrent calls and preserve the complete `IdempotencyResult`. Use at least
24 hours of retention and a longer period for payroll. Do not use
`NewMemoryIdempotencyStore` in a replicated production service because its
state is local to one process.

[examples/leaveservice](../examples/leaveservice) is this, runnable.

## Step 6 — Register the decision points

Name the checks a request runs through, in one place:

```go
var leaveApplicationPoints = []ruleengine.Point{
    {Key: "urn:arrowhr:rule:leave:eligibility:min-service:v1",    DecisionType: "leave-eligibility"},
    {Key: "urn:arrowhr:rule:leave:validation:max-consecutive:v1", DecisionType: "leave-validation"},
    {Key: "urn:arrowhr:rule:leave:validation:balance:v1",         DecisionType: "leave-validation"},
}
```

This list is the division of labour the whole design rests on:

> **The code decides what is checked. The rules decide how it is checked for
> this employee.**

A new tenant policy changes a rule. A new *kind* of check changes this list.

Then evaluate them together:

```go
results, err := engine.EvaluateAll(ctx, leaveApplicationPoints, request)
if err != nil {
    return fmt.Errorf("evaluate leave application: %w", err)   // retry or alert
}
if !results.AllPassed() {
    return refusal(results.Failures())   // every problem at once
}
```

`EvaluateAll` rather than a loop, for two reasons: a user sees every problem
with their request in one response, and each fact is fetched once across the
whole call.

### What triggers an evaluation

Data, or a code point reaching a decision — never a user asking for a rule to
be applied. "Three consecutive late days" is a *fact* that attendance resolves;
the rule's condition matches on it. There is no "evaluate my rules" button.

## Step 7 — Shadow mode

Do not cut over on faith. Run the engine beside the code it will replace and
compare.

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

Three properties make this safe: shadow mode never fails the request, never
changes the answer, and records enough to explain a disagreement after the
fact — the rule version and the decision id are the two things you will want.

A disagreement is either a bug in the rule or a behaviour the legacy code had
that nobody had written down. Both are worth finding before a tenant does. The
example produces a real one.

## Step 8 — Cut over, one check at a time

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

Enforce per check, not per service. Eligibility can be enforced while balance
validation is still shadowed.

## Using the pool

A service that decides for many tenants keeps one `pool.Pool`, not a map of
engines. The factory is yours: it chooses where a tenant's rules and hierarchy
come from, and returns an engine that has a loader and `Config.Tenant` equal to
the requested tenant. The pool calls `Reload`, then `Start`, then checks `Ready`
before it publishes the engine.

- Call `Warm` at start-up and when a `tenant.created` event arrives.
- Call `For(ctx, tenantID)` on the request path and `Get(tenantID)` inside a
  database transaction, where building would mean I/O.
- Call `Evict(tenantID)` when a tenant is deleted.
- Wrap a decision logger shared by every tenant in `pool.NoCloseLogger`, close
  it yourself once, after `Pool.Close` returns.
- A handle from the pool returns `pool.ErrEngineClosed` after its engine was
  evicted; ask the pool for the tenant again.

## Testing your integration

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
            map[string]any{"eligible": true, "reasonCode": "ELIGIBLE"}).
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

What `ruletest` gives you:

| | For |
|---|---|
| `FixtureBuilder`, `HierarchyBuilder` | rules and trees in a few lines instead of twenty fields |
| `RunGolden` | your own golden cases, as one sub-test each |
| `InMemoryExecutor` | dataset facts before the real executor exists |
| `DecisionEquals`, `AssertPassed`, `AssertFailed`, `AssertResolvedAt` | assertions that report every difference at once |
| `Explain` | the report to print when one fails |
| `FakeClock` | cache staleness and reconcile intervals without sleeping |
| `LoadRWB`, `LoadRule`, `LoadManifest`, `LoadRegistry` | the shared fixtures |

**Golden cases are the artifact a business owner can check.** They name no Go
type and no engine, so the same file proves the rule to your team, to Rule
Studio, and to whoever asks in a year why the number was what it was.

Run `rulelint` over your rules in CI. It catches a rule declaring a field
nobody registered, a filter on a hierarchy attribute, a dataset field its
manifest does not permit, and two equally specific rules at one anchor — all
before anything is published.

## Debugging

| Symptom | First thing to check |
|---|---|
| `NO_RULE_RESOLVED` | is any anchor on the subject's path? Run `ruleplay` |
| `HIERARCHY_NOT_CONFIGURED` | is `nodeId` set, known in this tenant, active, and covered by an effective snapshot? |
| `PRECEDENCE_CONFLICT` | equal-rank rules escaped publication; inspect the deterministic winner and fix the catalog |
| `FACT_UNRESOLVABLE` | a passed field missing from the request, or a dataset with no executor |
| `FIELD_NOT_REGISTERED` | run `rulelint` over rules and registry together |
| the wrong rule won | `ruleplay` prints every candidate and why each lost |
| decisions got slow | see [PERFORMANCE.md](PERFORMANCE.md); usually fact count or validation |

`ruleplay` is the fastest route for all of these:

```sh
ruleplay -root ./rules -request ./request.json -rows ./rows.json
```

## Rollout order

Pilot first, then the rule-heavy services, then their consumers, then the rest.

| Wave | Services |
|---|---|
| Pilot | Leave |
| 1 | Attendance, Payroll |
| 2 | Benefits, Employee Management, Performance |
| 3 | Recruitment, Learning & Development, Letters |
| 4 | IAM, Assets, Documents, Reporting, Admin |

Payroll needs run manifests and decimal correctness, so it follows the phase
that delivers them. Authorisation stays in Keycloak and IAM; the rule engine is
only for attribute-based data rules there.
