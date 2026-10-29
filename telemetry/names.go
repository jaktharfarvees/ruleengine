package telemetry

// Metric and span names live here and nowhere else. A literal metric name
// elsewhere in the module is a review failure: a renamed metric silently breaks
// a dashboard and an alert, and the only defence is a single definition.
const (
	// MetricEvalDuration is the end-to-end evaluation latency in milliseconds.
	// Labels: domain, rule_set, name, outcome.
	MetricEvalDuration = "rule_eval_duration_ms"
	// MetricEvalTotal counts evaluations. Labels: domain, outcome, reason_code.
	MetricEvalTotal = "rule_eval_total"
	// MetricEvalErrors counts operational evaluation failures. Labels: reason.
	MetricEvalErrors = "rule_eval_errors_total"
	// MetricCandidatesConsidered is the candidate-set size. Labels: domain.
	MetricCandidatesConsidered = "rule_candidates_considered"
	// MetricPrecedenceTie counts unresolved precedence ties, which are meant to
	// alert. Labels: domain, rule_set, name.
	MetricPrecedenceTie = "precedence_tie_total"
	// MetricCrossTenantDropped counts candidates discarded for belonging to
	// another tenant, which are meant to alert. Labels: domain.
	MetricCrossTenantDropped = "cross_tenant_rule_dropped_total"

	// MetricFactFetchDuration is one fact fetch in milliseconds.
	// Labels: field, kind.
	MetricFactFetchDuration = "fact_fetch_duration_ms"
	// MetricFactRowsScanned is the rows an executor reported scanning.
	// Labels: field.
	MetricFactRowsScanned = "fact_rows_scanned"
	// MetricFactBudgetExceeded counts decisions failed closed by a fact budget
	// or row limit. Labels: domain.
	MetricFactBudgetExceeded = "fact_budget_exceeded_total"
	// MetricFactCacheHits counts facts served from per-request de-duplication
	// rather than fetched again. Labels: field.
	MetricFactCacheHits = "fact_cache_hits_total"

	// MetricCacheEntries is the size of a loaded cache. Labels: kind.
	MetricCacheEntries = "rule_cache_entries"
	// MetricCacheStaleSeconds is how long the rule cache has gone without a
	// successful reconcile. No labels.
	MetricCacheStaleSeconds = "rule_cache_stale_seconds"
	// MetricHierarchyStaleSeconds is the same measure for the hierarchy cache.
	// No labels.
	MetricHierarchyStaleSeconds = "hierarchy_cache_stale_seconds"
	// MetricAuditDropped counts audit records dropped because the async logger
	// was full. No labels. Logging never blocks a decision, so this is the only
	// signal that a record was lost.
	MetricAuditDropped = "audit_dropped_total"
)

// Span names. The fact stage opens one span for the whole stage and one per
// fact kind, so a slow decision can be attributed without a per-field span.
const (
	// SpanEvaluate covers a whole evaluation.
	SpanEvaluate = "ruleengine.Evaluate"
	// SpanResolve covers rule resolution.
	SpanResolve = "resolve"
	// SpanVersion covers version selection.
	SpanVersion = "version"
	// SpanFacts covers the whole fact-fetch stage.
	SpanFacts = "facts"
	// SpanValidate covers input validation.
	SpanValidate = "validate"
	// SpanEngine covers engine evaluation.
	SpanEngine = "engine"
	// SpanMap covers building and logging the decision.
	SpanMap = "map"
)

// SpanFactKind returns the span name for fetching facts of one kind, such as
// "fact.dataset".
func SpanFactKind(kind string) string { return "fact." + kind }

// Label keys used by the names above.
const (
	// LabelDomain is the rule domain.
	LabelDomain = "domain"
	// LabelRuleSet is the rule set.
	LabelRuleSet = "rule_set"
	// LabelName is the rule name within its set.
	LabelName = "name"
	// LabelOutcome is "ok" or "failed".
	LabelOutcome = "outcome"
	// LabelReasonCode is the decision's governing reason code.
	LabelReasonCode = "reason_code"
	// LabelReason is the cause of an operational error.
	LabelReason = "reason"
	// LabelField is a registered field name. A field name is a contract, not
	// personal data, so it is safe as a label; its value never is.
	LabelField = "field"
	// LabelKind is a fact-source kind.
	LabelKind = "kind"
)

// Span attribute keys. Values attached by the library are bounded contract
// names or identifiers already present in the trace; fact and subject values
// are never attached.
const (
	AttrReasonCode = "rule.reason_code"
	AttrField      = "rule.field"
	AttrFactKind   = "rule.fact_kind"
)

// Engine pool metrics. None carries a tenant label: a tenant identifier is
// unbounded cardinality, and the pool's behaviour is the same for every
// tenant, so a total says everything an alert needs.
const (
	// MetricPoolHits counts lookups served by an engine already in the pool.
	// No labels.
	MetricPoolHits = "ruleengine_pool_hits_total"
	// MetricPoolMisses counts lookups that found no engine in the pool. No
	// labels.
	MetricPoolMisses = "ruleengine_pool_misses_total"
	// MetricPoolBuilds counts attempts to build an engine. No labels.
	MetricPoolBuilds = "ruleengine_pool_builds_total"
	// MetricPoolBuildFailures counts build attempts that failed. No labels.
	MetricPoolBuildFailures = "ruleengine_pool_build_failures_total"
	// MetricPoolEvictions counts engines removed from the pool. Labels: reason
	// ("idle", "capacity", "explicit" or "close").
	MetricPoolEvictions = "ruleengine_pool_evictions_total"
	// MetricPoolEnginesCreated counts engines published into the pool. The pool
	// size is this minus MetricPoolEnginesClosed. No labels.
	MetricPoolEnginesCreated = "ruleengine_pool_engines_created_total"
	// MetricPoolEnginesClosed counts engines the pool has closed. No labels.
	MetricPoolEnginesClosed = "ruleengine_pool_engines_closed_total"
)
