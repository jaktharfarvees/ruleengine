# Observability

The library exposes vendor-neutral metrics and tracing interfaces. It imports
neither Prometheus nor OpenTelemetry; each service adapts `telemetry.Metrics`
and `telemetry.Tracer` to the observability stack it already operates.

Audit evidence is separate. Metrics and traces diagnose operation and must
never contain subject identifiers or fact values. The access-controlled audit
store holds the decision record required for compliance and replay.

## Metrics

| Metric | Labels | Meaning |
|---|---|---|
| `rule_eval_duration_ms` | `domain`, `rule_set`, `name`, `outcome` | End-to-end latency for one decision |
| `rule_eval_total` | `domain`, `outcome`, `reason_code` | Decision throughput and outcome |
| `rule_eval_errors_total` | `reason` | Fail-closed and operational failures |
| `rule_candidates_considered` | `domain` | Resolver candidate-set size |
| `precedence_tie_total` | `domain`, `rule_set`, `name` | Ambiguous published precedence |
| `cross_tenant_rule_dropped_total` | `domain` | Tenant-boundary anomaly |
| `fact_fetch_duration_ms` | `field`, `kind` | Fact-resolution latency |
| `fact_rows_scanned` | `field` | Dataset executor scan size |
| `fact_budget_exceeded_total` | `domain` | Refused fact budget or row limit |
| `fact_cache_hits_total` | `field` | Per-request fact de-duplication |
| `rule_cache_entries` | `kind` | Rules, fields, datasets and hierarchy nodes loaded |
| `rule_cache_stale_seconds` | none | Age of the last coherent rule snapshot |
| `hierarchy_cache_stale_seconds` | none | Age of the hierarchy in that snapshot |
| `audit_dropped_total` | none | Best-effort audit records that could not be queued |

Metric labels are bounded contract data. Never add tenant, employee, node,
correlation, decision, run, or idempotency identifiers as labels.

## Trace propagation and span tree

`EvalRequest.Traceparent` and `CorrelationID` are placed in the root context as
`telemetry.Propagation` before `ruleengine.Evaluate` starts. A tracer adapter
reads that value, parses the W3C `traceparent` with its tracing SDK, and returns
a context carrying the new root span. The library passes that returned context
to every child stage and all fact I/O.

```text
ruleengine.Evaluate
├── resolve
├── version
├── facts
│   └── fact.<passed|derived|native|dataset>
├── validate
├── engine
└── map
```

Each failed stage records only the stable reason code in
`rule.reason_code`. Fact spans may additionally carry the registered field name
and fact kind; they never carry values. The response and audit record retain
the original `traceparent` and `correlationId` so an event publisher can forward
the same cross-service envelope.

A tracer adapter's root start normally begins like this:

```go
func (t tracerAdapter) Start(ctx context.Context, name string) (context.Context, telemetry.Span) {
    if propagation, ok := telemetry.PropagationFromContext(ctx); ok {
        ctx = t.remoteParent(ctx, propagation.Traceparent)
        // Add correlationId as trace baggage or a span attribute, not a metric label.
        ctx = t.withCorrelation(ctx, propagation.CorrelationID)
    }
    ctx, span := t.provider.Start(ctx, name)
    return ctx, spanAdapter{span: span}
}
```

## Minimum alerts

- sustained `rule_eval_errors_total` by reason;
- any precedence tie or cross-tenant drop;
- rule or hierarchy cache staleness above the service's tolerance;
- fact latency approaching the configured timeout or repeated budget breaches;
- any durable-audit persistence failure, or best-effort audit drops;
- unexpected evaluation latency or output-distribution changes during shadow
  and canary rollout.

Use `decisionId` and `correlationId` in structured decision logs, but not as
metric labels. Registry-marked PII remains redacted from logs and traces.
