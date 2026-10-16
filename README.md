# ArrowHR Rule Engine

`github.com/arrowhr/ruleengine` is the in-process policy engine shared by
ArrowHR services. It resolves tenant rules against an effective-dated
organization hierarchy, obtains only declared facts, evaluates deterministic
decision tables, and returns a complete explanation. It exposes no server,
opens no database connection, and performs no network I/O during evaluation.

The module is currently unreleased. Do not tag or publish it until the full
repository gate is green in the target CI environment.

## Guarantees

- Tenant-defined N-level hierarchies; no fixed country/company/department
  precedence ladder.
- Root-to-leaf paths and deepest-anchor-wins resolution.
- Same-node selection by attribute-filter specificity, then explicit priority.
  Publication rejects ambiguous equal-rank rules; runtime uses a stable
  deterministic fallback and reports `PRECEDENCE_CONFLICT` as a safety net.
- Effective-dated immutable rule and hierarchy snapshots. Run manifests pin
  rule versions, fact-source versions, business time, and hierarchy snapshot.
- One atomic runtime snapshot containing rules, registry, manifests, hierarchy,
  and fact resolver. An evaluation cannot observe a partial reload.
- Four declared fact sources: `passed`, `derived`, `native`, and `dataset`.
  `ContextBuilder` may satisfy only fields registered as `native`.
- Dataset recipes compile through guardrails G1-G12 into a bounded structured
  query. The consuming service owns and runs the executor against its own data.
- Pure-Go decision engines, exact decimal arithmetic, ISO currency validation,
  and half-even minor-unit rounding. Binary floating-point money is rejected.
- Stable reason codes, W3C trace propagation, no-PII operational logs, input
  digests, and an optional durable at-least-once audit-outbox contract.
- Embedded closed JSON Schema v2 contracts with an additive-only compatibility
  gate.

## Install

The canonical module path is private to ArrowHR:

```sh
go env -w GOPRIVATE=github.com/arrowhr/*
go env -w GONOSUMDB=github.com/arrowhr/*
go get github.com/arrowhr/ruleengine@<approved-version>
```

The module requires Go 1.24 or later.

## Evaluation model

Each decision passes through six stages:

1. Resolve the subject path, effective hierarchy snapshot, candidate anchors,
   filters, and deterministic winner.
2. Select a run-manifest pin, explicit version pin, or latest effective version.
3. Resolve the winning rule's declared inputs within the configured fact budget.
4. Validate inputs and declared outputs against the field registry.
5. Evaluate pure decision-table logic with no I/O or clock reads.
6. Map the explainable response, emit telemetry, and record the audit event.

A rule is anchored to one node. If the subject path is
`G001 -> C002 -> D002 -> BR001`, a matching rule at `D002` outranks one at
`C002`. Attribute filters narrow applicability at an anchor; they never promote
a shallower rule over a deeper one.

## Minimal wiring

```go
engine, err := ruleengine.New(ruleengine.Config{
    Tenant: "RWB",
    Domain: "leave",
    FactBudget: fact.Budget{
        MaxDatasetFacts:  8,
        PerFactTimeout:   250 * time.Millisecond,
        TotalFactTimeout: 2 * time.Second,
        MaxRowsScanned:   5000,
    },
    CacheReloadInterval: 30 * time.Second,
    RequireDurableAudit: true,
},
    ruleengine.WithLoader(loader),
    ruleengine.WithExecutor(executor),
    ruleengine.WithContextBuilder(contextBuilder),
    ruleengine.WithDecisionLogger(outboxLogger),
    ruleengine.WithMetrics(metrics),
    ruleengine.WithTracer(tracer),
)
if err != nil {
    return err
}
if err := engine.Reload(ctx); err != nil {
    return err
}
if err := engine.Start(); err != nil {
    return err
}
defer engine.Close()
```

Gate service readiness on `engine.Ready()`. Use
`CacheReloadInterval: 0` for batch processes and pin every batch with a
`manifest.RunManifest`.

See [the runnable leave-service example](examples/leaveservice) and the
[integration guide](docs/INTEGRATION.md) for complete construction, decision
points, executor conformance, shadow rollout, and readiness wiring.

## Defaults and failure semantics

Missing or invalid policy fails closed. A tenant may authorize a default only
by publishing a `RuleCatalog.defaults` entry for the exact
`domain/ruleSet/name`. The entry records the decision type, result, business
reason, author, and authorization time. The library contains no global list of
domains that may default; the tenant's authorized catalog is the authority.
Absence of an exact policy always produces `NO_RULE_RESOLVED`.

A failed business decision is returned as `Decision{Failed: true}` with reason
codes. A Go error means infrastructure or configuration failure, such as a
required durable audit record not being committed. Callers must handle both.

Important operational codes include:

| Code | Meaning |
|---|---|
| `HIERARCHY_NOT_CONFIGURED` | No effective subject path exists. |
| `NO_RULE_RESOLVED` | No rule or authorized exact default governs the point. |
| `DEFAULT_APPLIED` | The tenant-authorized catalog default was used. |
| `PRECEDENCE_CONFLICT` | Runtime deterministically resolved an ambiguity that publication should have rejected. |
| `PINNED_VERSION_UNAVAILABLE` | A run or request pin was unavailable after one reload attempt. |
| `DATASET_RECIPE_INVALID` | A dataset recipe violated a compile guardrail. |
| `FIELD_NOT_REGISTERED` | A rule referenced an undeclared field. |
| `OUTPUT_CONTRACT_VIOLATION` | Evaluation did not satisfy its declared outputs. |

## Audit and privacy

`audit.NewAsync` is a bounded best-effort logger for development and low-risk
workflows; it reports dropped records through metrics. For money, compliance,
or any workflow requiring complete evidence, set `RequireDurableAudit: true`
and configure `audit.NewOutboxLogger` over a consumer-owned transactional or
local durable outbox. The outbox must deliver records at least once and use
`decisionId` as the downstream idempotency key.

The secured audit record may contain subject identity and result values.
Operational slog output deliberately excludes subject identity and fact values;
it carries correlation identifiers and a SHA-256 input digest instead.

## Authoring and local tools

Validate a repository of artifacts before publication:

```sh
go run ./cmd/rulelint -root testdata
```

Explain a local decision:

```sh
go run ./cmd/ruleplay \
  -root testdata \
  -request testdata/requests/leave-eligibility-br001.json
```

`rulelint` treats ambiguous same-anchor rules as publication errors. `ruleplay`
prints the root-to-leaf path, winner, rejected candidates, fact provenance, and
business result.

## Quality gate

```sh
make check
```

CI separately runs build, vet, lint, all unit/golden/property/conformance tests,
the race detector, short fuzz campaigns, coverage floors, schema compatibility,
benchmark regression checks, the runnable example, fixture linting, and
`govulncheck`.

On a machine without a C compiler, `go test -race ./...` cannot run even though
the library itself is pure Go; the Linux CI race job is the release authority.

## Repository map

| Path | Responsibility |
|---|---|
| `rule`, `schema/v2` | Authored contracts and embedded schemas |
| `hierarchy`, `resolve` | Effective node paths and precedence |
| `registry`, `fact`, `dataset` | Field contracts and safe fact acquisition |
| `engine/decisiontable` | Production evaluator |
| `engine/testengine` | Independent conformance implementation |
| `store`, `manifest` | Atomic snapshots, loaders, versions, and replay pins |
| `audit`, `telemetry` | Decision evidence and observability interfaces |
| `ruletest`, `testdata` | Consumer helpers, golden cases, and negative fixtures |
| `cmd/rulelint`, `cmd/ruleplay` | Offline publication and support tools |

Architecture decisions and deeper guidance live in [docs](docs). Known grammar
limits, including the intentionally rejected `$input.*` dataset token and
request-relative date ranges, are recorded in
[docs/OPEN_QUESTIONS.md](docs/OPEN_QUESTIONS.md).

## Security and license

Report vulnerabilities through [SECURITY.md](SECURITY.md); never place employee
or tenant data in a public issue. Licensed under MIT; see [LICENSE](LICENSE).
