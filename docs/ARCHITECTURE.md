# Architecture

Where this library sits, what it does not do, and why the pieces are arranged
as they are.

## The library's place in the programme

ArrowHR is fourteen Go microservices over a multi-tenant HR and payroll
platform. Every one of them has rules that differ per tenant: casual leave
after four months at one company and six at another, overtime above forty hours
here and forty-five there. Before this library those differences lived in `if`
statements, which meant a tenant policy change was a deployment.

There were two ways to fix that. One is a rule service each decision calls
over the network. The other is **Option 2, the one chosen**: each service
caches the rules in memory and evaluates them in process.

| | Central rule service | **Option 2 (chosen)** |
|---|---|---|
| Hot path | a network hop per decision | none |
| Failure mode | rule service down means no decisions | last good snapshot keeps serving |
| Payroll batch of 500,000 | 500,000 round trips | zero |
| Cost we own | the latency and the coupling | **cache invalidation** |

Cache invalidation is a real cost and this library pays it deliberately: an
immutable snapshot swapped atomically, a staleness metric, and a failed reload
that leaves the previous snapshot serving. That is the whole of `store`.

## Governance versus execution

The split that matters most:

```
┌─────────────────────────────┐        ┌──────────────────────────────┐
│  Rule Service (separate)    │        │  This library (in process)   │
│                             │        │                              │
│  authoring and validation   │        │  resolution                  │
│  approval and publishing    │──────▶ │  version selection           │
│  impact analysis            │ rules  │  fact fetching               │
│  the catalog and its index  │        │  evaluation                  │
│  run manifests              │        │  explanation                 │
│  audit storage              │        │                              │
│  Rule Studio's node picker  │        │  decides, every time, fast   │
└─────────────────────────────┘        └──────────────────────────────┘
       governance                              execution
```

The Rule Service decides **what may be published**. This library decides **what
the published rules mean for one employee right now**. It performs no
authorisation at evaluation time, because a published rule is already
authorised — re-checking it per decision would put an authorisation question on
the payroll hot path.

`authz` is the one exception, and it is not called from `Evaluate`: it exists so
the Rule Service and Rule Studio enforce subtree grants from the same code
rather than two implementations that drift.

## What the library will not do

These are deliberate, not unfinished:

- **No server.** No HTTP, no gRPC, no listener. It opens no connection of its
  own; a loader the consumer supplies does that.
- **No database.** No driver, no SQL, no query builder. It compiles a validated
  `Query` value; the owning service translates and runs it.
- **No message broker.** Cross-service effects travel as events the services
  publish themselves.
- **No rule storage.** It consumes a `Loader`; persistence is the Rule
  Service's.
- **No rule authoring.** Validation here is only what is needed to evaluate
  safely. Publish-time governance — impact analysis, approval, overlap lint —
  belongs upstream.
- **No environment variables.** Everything arrives through `Config` and
  options, so a service can audit its own configuration in one place.

## The artifacts

Six are schema-governed and shared across the programme; three more are
internal to evaluation.

| Artifact | Stored | Who authors it | What it settles |
|---|---|---|---|
| `EvaluationRequest` | no | the calling service | decide this, for this subject, at this time |
| `EvaluationResponse` | audit copy | the library | the decision and how it was reached |
| `RuleDefinition` | yes | business admins, via Rule Studio | one version: anchor, effective dates, logic |
| `FieldRegistry` | yes | each service team | approved field names, types, units, PII, fact source |
| `RuleCatalog` | yes | the Rule Service | indexed lookup to a candidate set |
| `DatasetManifest` | yes | each service team | which columns a field may be built from, and the limits |
| `RunManifest` | yes | the Rule Service | the versions one batch run is frozen to |
| `OrgHierarchy` | read model | the org/employee service | the tree precedence is derived from |
| `RoleGrant` | yes | tenant admins | which subtree an author may write in |

The published JSON Schemas live in [`schema/v2`](../schema/v2) and are embedded
in the binary, so a request is validated against the same contract a service
was built to.

## Package dependency graph

Dependencies run one way. The root package is the only wiring point: it imports
the subsystems and adapts between them, and none of them imports it back.

```
                       ruleengine  (facade: Engine, EvalRequest, Decision)
                            │
     ┌──────────┬───────────┼───────────┬──────────┬──────────┐
     ▼          ▼           ▼           ▼          ▼          ▼
   store      fact       resolve     engine      audit    telemetry
     │          │           │           │
     │     ┌────┴────┐      │     decisiontable
     │     ▼         ▼      ▼      testengine
     │  dataset   registry  hierarchy
     │     │         │      │
     └─────┴────┬────┴──────┘
                ▼
              rule  ──▶ internal/urnkey

   internal/  expr · decimalx · urnkey · clock      (never imported by consumers)
   ruletest   wraps everything, for service tests   (may import the root)
   cmd/, examples/                                   (wire it together)
```

Why this shape:

- **`rule` is the floor.** A rule key, an anchor, a filter and a decision table
  are vocabulary everything else speaks.
- **`registry` owns the field contract**, including where a field's value comes
  from. The declaration lives with the contract it belongs to; resolving it is a
  different job and lives in `fact`. See
  [ADR 9](adr/0009-fact-source-declaration-lives-with-the-field.md).
- **`dataset` compiles, `fact` orchestrates.** `dataset` turns a declaration
  into a validated query and knows nothing about budgets; `fact` spends the
  budget and decides what to fetch.
- **`resolve` is pure.** No I/O, no clock, no logging. Given a path, candidates
  and a hierarchy it is a function, which is why it can be property-tested.
- **The root package holds no algorithm.** It validates, sequences the six
  stages, and builds the decision.

`architecture_test.go` enforces the one-way rule by parsing imports, because an
import cycle discovered later is a refactor rather than a fix.

## The two operations

Everything a service does reduces to two calls:

1. **Resolve a version**, once per cycle for a batch, or implicitly per
   decision for a live one.
2. **Evaluate**, per record.

There is one evaluate contract — `{tenant, ruleKey, versionId, input} → values`
— not one per domain. Differences between services live in the catalog, the
field registry and the dataset manifests, never in the transport.

## The engine returns values; the service does the arithmetic

A rule yields `{threshold: 40, multiplier: 2.0}`. It does not yield a payslip
line. The service multiplies, rounds to the currency's minor units and writes
the record.

This is deliberate. Money arithmetic belongs where the money is, under that
service's tests and its auditors. A rule engine that computed net pay would be
a second payroll implementation nobody asked for.

## Further reading

- [RESOLUTION.md](RESOLUTION.md) — how a rule is chosen, in full
- [FACTS.md](FACTS.md) — the four fact kinds and the guardrails
- [INTEGRATION.md](INTEGRATION.md) — the per-service playbook
- [OBSERVABILITY.md](OBSERVABILITY.md) — metrics, spans and which alerts matter
- [PERFORMANCE.md](PERFORMANCE.md) — budgets and the cost model
- [OPEN_QUESTIONS.md](OPEN_QUESTIONS.md) — what is still undecided
- [adr/](adr) — why each significant choice was made
