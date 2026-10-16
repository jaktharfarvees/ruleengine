package ruleengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/arrowhr/ruleengine/audit"
	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/internal/decimalx"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/resolve"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/store"
	"github.com/arrowhr/ruleengine/telemetry"
)

// resolvedAttributeNames are the hierarchy-derived values every decision
// records. Each resolves from the nearest ancestor that declares it, so a
// decision can show not just which currency applied but which node said so.
var resolvedAttributeNames = []string{"country", "currency", "locale", "timezone"}

// staleReconcileMultiple is how many missed reconciles make a cache stale
// enough to say so on the decision. One missed tick is normal jitter; three is
// a service deciding on rules nobody has refreshed.
const staleReconcileMultiple = 3

// evaluate runs the six stages for one decision point.
//
// Every stage either advances the decision or ends it closed. The split
// between the two returns is the contract documented in doc.go: a business
// refusal is a Decision with Failed set, and a Go error means the caller
// should retry, alert or fix configuration.
func (s *session) evaluate(ctx context.Context, request EvalRequest, point Point) (Decision, error) {
	// The rule key already names the domain, rule set and name, so a caller
	// that supplied a key need not repeat them. Filling them in here keeps
	// EvaluateAll usable, where the caller gives a list of keys and nothing
	// else, and keeps the request that reaches the schema complete.
	request.Routing = completeRouting(request.Routing)
	if request.Context == nil {
		request.Context = map[string]any{}
	}
	state := &evaluation{
		session: s,
		config:  s.engine.config,
		request: request,
		point:   point,
		decision: Decision{
			DecisionID:    newDecisionID(),
			RuleKey:       request.Routing.RuleKey,
			AsOf:          request.AsOf,
			RunID:         request.RunID,
			CorrelationID: request.CorrelationID,
			Traceparent:   request.Traceparent,
			LibVersion:    Version,
		},
	}

	snapshot, winner, failed := state.stageResolve(ctx)
	if failed != nil {
		return state.finish(ctx, *failed)
	}
	if state.defaulted {
		return state.finish(ctx, ReasonDefaultApplied)
	}
	if failed := state.stageVersion(ctx, winner); failed != nil {
		return state.finish(ctx, *failed)
	}
	facts, failed := state.stageFetchFacts(ctx, winner)
	if failed != nil {
		return state.finish(ctx, *failed)
	}
	state.inputsHash = hashInputs(facts, request.Subject.Attributes)
	if failed := state.stageValidate(ctx, snapshot, winner, facts); failed != nil {
		return state.finish(ctx, *failed)
	}
	if failed := state.stageEvaluate(ctx, winner, facts); failed != nil {
		return state.finish(ctx, *failed)
	}
	return state.finish(ctx, ReasonOK)
}

// evaluation is one decision's working state.
type evaluation struct {
	session   *session
	config    Config
	request   EvalRequest
	point     Point
	decision  Decision
	hierarchy *hierarchy.Snapshot
	// reasons accumulates non-fatal codes until the decision is built.
	reasons []ReasonCode
	// retriedLoad records that a pin miss already cost one loader fetch, so a
	// decision cannot trigger more than one.
	retriedLoad bool
	defaulted   bool
	inputsHash  string
}

func (s *evaluation) note(code ReasonCode) { s.reasons = append(s.reasons, code) }

// stage 1: Resolve. Validate the envelope, find the subject in the hierarchy,
// and pick the rule that governs them.
func (s *evaluation) stageResolve(ctx context.Context) (*store.Snapshot, *rule.RuleDefinition, *ReasonCode) {
	ctx, span := s.session.engine.options.tracer.Start(ctx, telemetry.SpanResolve)
	defer span.End()

	if code := s.validateRequest(); code != nil {
		span.SetAttr("reason", code.String())
		return nil, nil, code
	}
	// Business time governs every date comparison from here on. A request that
	// states none is deciding about now, so AsOf stands in.
	s.decision.ValidAt = s.request.AsOf
	if s.request.ValidAt != nil {
		s.decision.ValidAt = *s.request.ValidAt
	}

	snapshot := s.session.snapshot
	if snapshot == nil {
		return nil, nil, fail(ReasonHierarchyUnavailable)
	}
	hierarchyPin := ""
	if s.request.RunID != "" {
		run, err := s.session.engine.versions.RunManifest(ctx, s.request.Tenant.ID, s.request.RunID)
		if err != nil {
			return nil, nil, fail(ReasonPinnedVersionUnavailable)
		}
		if s.request.ValidAt != nil && !s.request.ValidAt.Equal(run.ValidAt) {
			return nil, nil, fail(ReasonPinnedVersionUnavailable)
		}
		s.decision.ValidAt = run.ValidAt
		hierarchyPin = run.HierarchySnapshotID
	}
	hierarchySnapshot, hierarchyID, found := snapshot.HierarchyAt(s.request.Tenant.ID, s.decision.ValidAt, hierarchyPin)
	if !found {
		return nil, nil, fail(ReasonHierarchyNotConfigured)
	}
	s.hierarchy = hierarchySnapshot
	s.decision.HierarchySnapshotID = hierarchyID
	if stale := s.session.engine.cache.StaleSeconds(); s.staleThreshold() > 0 && stale > s.staleThreshold() {
		// The decision proceeds: refusing every decision because a reconcile
		// is late would turn a degraded cache into an outage. It is recorded
		// so the audit trail shows the rules may have been old.
		s.note(ReasonCacheStale)
	}

	// Version selection comes first, because in this model a version carries
	// its own anchor: publishing a company override is a new version of the
	// same rule key (plan Appendix A.2). So a pin restricts which rules can
	// win rather than contradicting the one that did, and a run pinned to an
	// older version correctly sees the rule set as it was then.
	family := s.request.Routing.RuleKey.Family()
	versionRequest := store.VersionRequest{
		Tenant:      s.request.Tenant.ID,
		RuleKey:     s.request.Routing.RuleKey,
		RunID:       s.request.RunID,
		ExplicitPin: s.request.RuleVersionPin,
		EffectiveAt: s.decision.ValidAt,
	}
	candidates, versionErr := s.session.engine.versions.ResolveSnapshot(ctx, snapshot, versionRequest)
	if versionErr != nil {
		if isPinFailure(versionErr) && s.session.engine.options.loader != nil && !s.retriedLoad {
			s.retriedLoad = true
			if reloadErr := s.session.engine.Reload(ctx); reloadErr == nil {
				s.session.refresh()
				snapshot = s.session.snapshot
				candidates, versionErr = s.session.engine.versions.ResolveSnapshot(ctx, snapshot, versionRequest)
			}
		}
		if code := s.versionFailure(versionErr); code != nil {
			span.RecordError(versionErr)
			return nil, nil, code
		}
		candidates = nil
	}
	if len(candidates) > s.config.maxCandidates() {
		// A catalog bucket this large is a data error, not a cost to pay per
		// decision.
		return nil, nil, fail(ReasonNoRuleResolved)
	}
	s.session.engine.options.metrics.Histogram(telemetry.MetricCandidatesConsidered,
		telemetry.Label{Key: telemetry.LabelDomain, Value: family.Domain}).Observe(float64(len(candidates)))

	result := resolve.Resolve(resolve.Input{
		Tenant:     s.request.Tenant.ID,
		Family:     family,
		NodeID:     s.request.Subject.NodeID,
		EmployeeID: s.request.Subject.Identifier.Value,
		Attributes: s.request.Subject.Attributes,
		Context:    s.request.Context,
		ValidAt:    s.decision.ValidAt,
	}, candidates, hierarchySnapshot)

	s.decision.SubjectPath = result.Path
	s.decision.AnchorPath = pathNodeIDs(result.Path)
	s.decision.Evaluated = evaluatedFrom(result)
	s.recordAnomalies(result, family)

	if result.Tie != nil {
		s.decision.Tie = &Tie{
			Candidates: result.Tie.Candidates, Order: result.Tie.Order,
			Depth: result.Tie.Depth, Message: result.Tie.Message,
		}
		s.session.engine.options.metrics.Counter(telemetry.MetricPrecedenceTie,
			telemetry.Label{Key: telemetry.LabelDomain, Value: family.Domain},
			telemetry.Label{Key: telemetry.LabelRuleSet, Value: family.RuleSet},
			telemetry.Label{Key: telemetry.LabelName, Value: family.Name},
		).Inc()
		s.note(ReasonPrecedenceConflict)
	}
	if result.FailClosed || result.Winner == nil {
		switch result.ReasonCode {
		case "HIERARCHY_UNAVAILABLE":
			return nil, nil, fail(ReasonHierarchyNotConfigured)
		case "SUBJECT_NODE_UNRESOLVABLE":
			return nil, nil, fail(ReasonHierarchyNotConfigured)
		default:
			if s.applyDefault(snapshot, family) {
				return snapshot, nil, nil
			}
			return nil, nil, fail(ReasonNoRuleResolved)
		}
	}

	precedence := result.Winner.Precedence
	s.decision.ResolvedAt = &precedence
	s.decision.ResolvedAtNode = precedence.NodeID
	s.decision.ResolvedAttributes = s.resolveAttributes(hierarchySnapshot)
	return snapshot, result.Winner.Rule, nil
}

// stage 2: Version. Record which immutable version decided, and check it
// satisfies the output contract the caller expects.
//
// Selection itself happened in stage 1, because a version carries its anchor:
// choosing the rule and choosing the version are one act in this model.
func (s *evaluation) stageVersion(ctx context.Context, winner *rule.RuleDefinition) *ReasonCode {
	_, span := s.session.engine.options.tracer.Start(ctx, telemetry.SpanVersion)
	defer span.End()

	s.decision.RuleKey = winner.RuleKey
	s.decision.RuleVersion = winner.Version
	s.decision.DecisionType = winner.DecisionType
	if s.point.DecisionType != "" && winner.DecisionType != s.point.DecisionType {
		// The caller expects a different output contract than the rule
		// promises, so the result would not be the shape it parses.
		return fail(ReasonOutputContractViolation)
	}
	return nil
}

// versionFailure maps a version-selection failure. A pin that cannot be
// obtained is retried against the loader once, because a batch run opened
// before this replica last reloaded is the ordinary cause and one fetch is
// cheaper than a failed payroll line.
func (s *evaluation) versionFailure(err error) *ReasonCode {
	if errors.Is(err, store.ErrEffectiveVersionUnavailable) {
		// Nothing governs this business time, which resolution reports as no
		// rule rather than as a pin problem.
		return nil
	}
	if isPinFailure(err) {
		return fail(ReasonPinnedVersionUnavailable)
	}
	if errors.Is(err, store.ErrCacheUnavailable) {
		return fail(ReasonHierarchyUnavailable)
	}
	return fail(ReasonNoRuleResolved)
}

// stage 3: Fetch facts. Resolve only the winning rule's declared inputs, under
// the request's fact budget.
func (s *evaluation) stageFetchFacts(ctx context.Context, winner *rule.RuleDefinition) (map[string]any, *ReasonCode) {
	ctx, span := s.session.engine.options.tracer.Start(ctx, telemetry.SpanFacts)
	defer span.End()

	resolver := s.session.facts
	if resolver == nil {
		return nil, fail(ReasonFactUnresolvable)
	}

	// Whatever the caller supplied, plus native facts the service computes
	// itself. The builder is never asked for passed, derived, or dataset fields:
	// allowing it to fill those names would bypass their declared source and
	// the dataset compiler's security guardrails.
	have := make(map[string]any, len(s.request.Context))
	for name, value := range s.request.Context {
		have[name] = value
	}
	if builder := s.session.engine.options.contextBuilder; builder != nil {
		nativeInputs := make([]string, 0, len(winner.DeclaredInputs))
		fields := s.session.snapshot.Registry()
		for _, name := range winner.DeclaredInputs {
			if _, supplied := have[name]; supplied {
				continue
			}
			definition, known := fields.Field(name)
			if known && definition.FactSource.Kind == registry.FactNative {
				nativeInputs = append(nativeInputs, name)
			}
		}
		if len(nativeInputs) > 0 {
			built, err := builder.Build(ctx, nativeInputs, s.request.Subject.Identifier.Value, s.period())
			if err != nil {
				span.RecordError(err)
				return nil, fail(ReasonFactUnresolvable)
			}
			for _, name := range nativeInputs {
				if value, found := built[name]; found {
					have[name] = value
				}
			}
		}
	}

	pins := s.request.FactSourcePins
	if s.request.RunID != "" {
		resolved, err := s.factPins(ctx, winner.DeclaredInputs)
		if err != nil {
			span.RecordError(err)
			return nil, fail(ReasonPinnedVersionUnavailable)
		}
		pins = resolved
	}

	resolution, err := resolver.Resolve(ctx, winner.DeclaredInputs, have, fact.Params{
		Tenant:     s.request.Tenant.ID,
		EmployeeID: s.request.Subject.Identifier.Value,
		NodeID:     s.request.Subject.NodeID,
		AsOf:       s.request.AsOf,
		ValidAt:    s.decision.ValidAt,
	}, pins)
	if err != nil {
		span.RecordError(err)
		return nil, s.factFailure(err)
	}
	for _, note := range resolution.Notes {
		if code, parseErr := ParseReasonCode(note); parseErr == nil {
			s.note(code)
		}
	}
	s.decision.FactsResolved = factsResolvedFrom(resolution.Facts)
	return resolution.Values, nil
}

// factPins merges a run's frozen fact-source versions with any the request
// carries, so a batch decision uses the recipes the run opened with.
func (s *evaluation) factPins(ctx context.Context, fields []string) (map[string]string, error) {
	pins := make(map[string]string, len(fields))
	for name, version := range s.request.FactSourcePins {
		pins[name] = version
	}
	for _, field := range fields {
		version, found, err := s.session.engine.versions.FactPin(
			ctx, s.request.Tenant.ID, s.request.RunID, field)
		if err != nil {
			return nil, err
		}
		if found {
			pins[field] = version
		}
	}
	return pins, nil
}

// stage 4: Validate. Check every declared input against the registry before
// the engine sees it.
func (s *evaluation) stageValidate(
	ctx context.Context, snapshot *store.Snapshot, winner *rule.RuleDefinition, facts map[string]any,
) *ReasonCode {
	_, span := s.session.engine.options.tracer.Start(ctx, telemetry.SpanValidate)
	defer span.End()
	// Checking facts against the registry is what strict validation adds.
	if s.config.Validation != ValidationStrict {
		return nil
	}
	fields := snapshot.Registry()
	if fields == nil {
		return fail(ReasonFieldNotRegistered)
	}
	if err := fields.ValidateInputs(winner.Domain, winner.DeclaredInputs, facts); err != nil {
		span.RecordError(err)
		return registryFailure(err)
	}
	for _, name := range winner.DeclaredInputs {
		if definition, known := fields.Field(name); known && definition.Status == "deprecated" {
			s.note(ReasonFieldDeprecated)
		}
	}
	return nil
}

// stage 5: Evaluate. Run the rule's logic over the facts.
func (s *evaluation) stageEvaluate(
	ctx context.Context, winner *rule.RuleDefinition, facts map[string]any,
) *ReasonCode {
	ctx, span := s.session.engine.options.tracer.Start(ctx, telemetry.SpanEngine)
	defer span.End()

	// Subject attributes are available to rule logic, but a fetched fact wins
	// a name collision and the shadow is recorded: a fact silently overridden
	// by an attribute of the same name is a correctness trap nobody would find.
	input := make(map[string]any, len(facts)+len(s.request.Subject.Attributes))
	for name, value := range s.request.Subject.Attributes {
		input[name] = value
	}
	for name, value := range facts {
		if _, shadowed := s.request.Subject.Attributes[name]; shadowed {
			s.note(ReasonFactShadowedAttribute)
		}
		input[name] = value
	}

	output, err := s.session.engine.options.engine.Evaluate(ctx, winner.Logic, engine.Input{
		Facts: input, Trace: s.config.TraceEnabled,
	})
	if err != nil {
		span.RecordError(err)
		return fail(ReasonEngineError)
	}
	s.decision.Result = output.Result
	s.decision.Trace = output.Trace

	if s.config.Validation != ValidationStrict {
		return nil
	}
	snapshot := s.session.snapshot
	if snapshot == nil || snapshot.Registry() == nil {
		return fail(ReasonOutputContractViolation)
	}
	if err := snapshot.Registry().ValidateOutputs(winner.DeclaredOutputs, output.Result); err != nil {
		span.RecordError(err)
		return fail(ReasonOutputContractViolation)
	}
	return nil
}

// stage 6: Map and log. Build the decision, record it, and measure it.
func (s *evaluation) finish(ctx context.Context, governing ReasonCode) (Decision, error) {
	_, span := s.session.engine.options.tracer.Start(ctx, telemetry.SpanMap)
	defer span.End()

	s.decision.Failed = governing.Fatal()
	s.decision.ReasonCodes = orderedReasons(governing, s.reasons)
	if s.decision.ValidAt.IsZero() {
		s.decision.ValidAt = s.request.AsOf
	}

	family := s.request.Routing.RuleKey.Family()
	outcome := "ok"
	if s.decision.Failed {
		outcome = "failed"
	}
	options := s.session.engine.options
	options.metrics.Counter(telemetry.MetricEvalTotal,
		telemetry.Label{Key: telemetry.LabelDomain, Value: family.Domain},
		telemetry.Label{Key: telemetry.LabelOutcome, Value: outcome},
		telemetry.Label{Key: telemetry.LabelReasonCode, Value: governing.String()},
	).Inc()
	if s.decision.Failed {
		options.metrics.Counter(telemetry.MetricEvalErrors,
			telemetry.Label{Key: telemetry.LabelReason, Value: governing.String()}).Inc()
	}
	s.session.span.SetAttr("reason", governing.String())

	record := s.auditRecord()
	if durable, ok := options.decisionLogger.(audit.DurableDecisionLogger); ok {
		if err := durable.LogDurably(ctx, record); err != nil {
			span.RecordError(err)
			return s.decision, fmt.Errorf("persist decision audit %q: %w", s.decision.DecisionID, err)
		}
	} else {
		// Best-effort loggers remain asynchronous and do not extend decision
		// latency. RequireDurableAudit opts into the stronger contract above.
		options.decisionLogger.Log(record)
	}
	return s.decision, nil
}

func (s *evaluation) auditRecord() audit.Record {
	family := s.request.Routing.RuleKey.Family()
	record := audit.Record{
		DecisionID: s.decision.DecisionID,
		Tenant:     s.request.Tenant.ID,
		Domain:     family.Domain, RuleSet: family.RuleSet, Name: family.Name,
		RuleKey: s.decision.RuleKey.String(), RuleVersion: s.decision.RuleVersion,
		DecisionType: s.decision.DecisionType,
		SubjectType:  s.request.Subject.Type,
		SubjectID:    s.request.Subject.Identifier.Value,
		NodeID:       s.request.Subject.NodeID,
		Result:       s.decision.Result,
		InputsHash:   s.inputsHash,
		Failed:       s.decision.Failed,
		AsOf:         s.decision.AsOf, ValidAt: s.decision.ValidAt,
		RunID: s.decision.RunID, HierarchySnapshotID: s.decision.HierarchySnapshotID,
		IdempotencyKey: s.request.IdempotencyKey,
		CorrelationID:  s.decision.CorrelationID, Traceparent: s.decision.Traceparent,
		LibVersion: s.decision.LibVersion,
	}
	if s.decision.ResolvedAt != nil {
		record.ResolvedNodeID = s.decision.ResolvedAt.NodeID
	}
	for _, entry := range s.decision.SubjectPath {
		record.SubjectPath = append(record.SubjectPath, entry.NodeID)
	}
	for _, code := range s.decision.ReasonCodes {
		record.ReasonCodes = append(record.ReasonCodes, code.String())
	}
	for _, evaluated := range s.decision.Evaluated {
		record.Candidates = append(record.Candidates, audit.Candidate{
			RuleKey: evaluated.RuleKey.String(), RuleVersion: evaluated.RuleVersion,
			AnchorNodeID: evaluated.Anchor.NodeID, AnchorKind: string(evaluated.Anchor.Kind),
			Order: evaluated.Order, Depth: evaluated.Depth, Winner: evaluated.Winner,
			EliminatedBy: string(evaluated.EliminatedBy), Reason: evaluated.Reason,
		})
	}
	for _, resolved := range s.decision.FactsResolved {
		record.Facts = append(record.Facts, audit.Fact{
			Field: resolved.Field, Kind: resolved.Kind.String(),
			FactSourceVersion: resolved.FactSourceVersion, Dataset: resolved.Dataset,
			RowsScanned: resolved.RowsScanned, ElapsedMS: resolved.ElapsedMS,
			Cached: resolved.Cached, FromRequest: resolved.FromRequest,
		})
	}
	return record
}

func hashInputs(facts, attributes map[string]any) string {
	payload := struct {
		Facts      map[string]any `json:"facts"`
		Attributes map[string]any `json:"attributes"`
	}{Facts: facts, Attributes: attributes}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// validateRequest checks the envelope before anything is looked up.
func (s *evaluation) validateRequest() *ReasonCode {
	if s.request.Tenant.ID == "" || s.request.Tenant.ID != s.config.Tenant {
		// A request for another tenant is refused outright rather than
		// resolved and found empty (INV7).
		s.session.engine.options.metrics.Counter(telemetry.MetricCrossTenantDropped,
			telemetry.Label{Key: telemetry.LabelDomain, Value: s.config.Domain}).Inc()
		return fail(ReasonSchemaInvalid)
	}
	if s.request.AsOf.IsZero() {
		return fail(ReasonSchemaInvalid)
	}
	if s.request.Currency != "" {
		if _, err := decimalx.CurrencyScaleStrict(s.request.Currency); err != nil {
			return fail(ReasonSchemaInvalid)
		}
	}
	parts, err := s.request.Routing.RuleKey.Parse()
	if err != nil {
		return fail(ReasonSchemaInvalid)
	}
	if parts.Domain != s.config.Domain {
		return fail(ReasonSchemaInvalid)
	}
	// A routing block that names a different rule than its own key is a
	// request nobody should act on: one of the two is a mistake and there is
	// no way to tell which.
	if s.request.Routing.Domain != parts.Domain ||
		s.request.Routing.RuleSet != parts.RuleSet ||
		s.request.Routing.Name != parts.Name {
		return fail(ReasonSchemaInvalid)
	}
	if s.request.Subject.Type == "" || s.request.Subject.Identifier.Value == "" {
		return fail(ReasonSchemaInvalid)
	}
	// An employee decision without a node cannot be placed in the hierarchy,
	// and precedence is derived from that placement.
	if s.request.Subject.Type == "employee" && s.request.Subject.NodeID == "" {
		return fail(ReasonHierarchyNotConfigured)
	}
	if s.config.Validation != ValidationOff {
		if err := validateRequestSchema(s.request); err != nil {
			return fail(ReasonSchemaInvalid)
		}
	}
	return nil
}

func (s *evaluation) period() string {
	if s.decision.ValidAt.IsZero() {
		return ""
	}
	return s.decision.ValidAt.UTC().Format("2006-01")
}

func (s *evaluation) staleThreshold() float64 {
	if s.config.CacheReloadInterval <= 0 {
		return 0
	}
	return (s.config.CacheReloadInterval * staleReconcileMultiple).Seconds()
}

// recordAnomalies reports the eliminations that are alertable rather than
// routine. A cross-tenant candidate is impossible by construction under D17,
// so seeing one means data has crossed a boundary it should not have.
func (s *evaluation) recordAnomalies(result resolve.Result, family rule.Family) {
	for _, candidate := range result.Candidates {
		if candidate.EliminatedBy != resolve.EliminatedCrossTenant {
			continue
		}
		s.note(ReasonCrossTenantRuleDropped)
		s.session.engine.options.metrics.Counter(telemetry.MetricCrossTenantDropped,
			telemetry.Label{Key: telemetry.LabelDomain, Value: family.Domain}).Inc()
	}
}

func (s *evaluation) resolveAttributes(snapshot *hierarchy.Snapshot) map[string]ResolvedAttribute {
	if snapshot == nil || s.request.Subject.NodeID == "" {
		return nil
	}
	attributes := make(map[string]ResolvedAttribute, len(resolvedAttributeNames))
	for _, name := range resolvedAttributeNames {
		value, sourceNodeID, ok := snapshot.ResolveAttribute(
			s.request.Tenant.ID, s.request.Subject.NodeID, name)
		if ok {
			attributes[name] = ResolvedAttribute{Value: value, SourceNodeID: sourceNodeID}
		}
	}
	if len(attributes) == 0 {
		return nil
	}
	return attributes
}

// factFailure maps a fact-resolution failure onto the reason catalogue, so the
// decision carries the specific cause rather than a generic one.
func (s *evaluation) factFailure(err error) *ReasonCode {
	var compile *dataset.Fault
	if errors.As(err, &compile) {
		if detail, parseErr := ParseReasonCode(compile.Code); parseErr == nil {
			s.note(detail)
		}
		return fail(ReasonDatasetRecipeInvalid)
	}
	if code, parseErr := ParseReasonCode(fact.Code(err)); parseErr == nil {
		return &code
	}
	var budget *fact.Fault
	if errors.As(err, &budget) {
		return fail(ReasonFactUnresolvable)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fail(ReasonFactTimeout)
	}
	return fail(ReasonFactUnresolvable)
}

func (s *evaluation) applyDefault(snapshot *store.Snapshot, family rule.Family) bool {
	policy, found := snapshot.DefaultPolicy(s.request.Tenant.ID, family)
	if !found || (s.point.DecisionType != "" && policy.DecisionType != s.point.DecisionType) {
		return false
	}
	result := make(map[string]any, len(policy.Result)+1)
	outputs := make([]string, 0, len(policy.Result)+1)
	for name, value := range policy.Result {
		result[name] = value
		outputs = append(outputs, name)
	}
	if _, present := result["reasonCode"]; !present {
		result["reasonCode"] = policy.ReasonCode
		outputs = append(outputs, "reasonCode")
	}
	if s.config.Validation == ValidationStrict {
		if fields := snapshot.Registry(); fields == nil || fields.ValidateOutputs(outputs, result) != nil {
			return false
		}
	}
	s.decision.DecisionType = policy.DecisionType
	s.decision.Result = result
	s.defaulted = true
	return true
}

// registryFailure maps a validation failure onto the specific reason code, so
// an unregistered field and a mistyped value are told apart.
func registryFailure(err error) *ReasonCode {
	switch {
	case errors.Is(err, registry.ErrFieldNotRegistered):
		return fail(ReasonFieldNotRegistered)
	case errors.Is(err, registry.ErrFieldTypeMismatch):
		return fail(ReasonFieldTypeMismatch)
	default:
		return fail(ReasonFieldTypeMismatch)
	}
}

// isPinFailure reports whether an error means a pinned version could not be
// obtained, as distinct from a rule that does not exist at all.
func isPinFailure(err error) bool {
	return errors.Is(err, store.ErrPinnedVersionUnavailable) ||
		errors.Is(err, store.ErrRunManifestUnavailable) ||
		errors.Is(err, store.ErrRunTenantMismatch)
}

func evaluatedFrom(result resolve.Result) []Evaluated {
	evaluated := make([]Evaluated, 0, len(result.Candidates))
	for _, candidate := range result.Candidates {
		if candidate.Rule == nil {
			continue
		}
		evaluated = append(evaluated, Evaluated{
			RuleKey: candidate.Rule.RuleKey, RuleVersion: candidate.Rule.Version,
			AnchorNodeID: candidate.Rule.EffectiveAnchor().NodeID,
			Anchor:       candidate.Rule.EffectiveAnchor(),
			Order:        candidate.Precedence.Order, Depth: candidate.Precedence.Depth,
			OnPath: candidate.OnPath, FiltersMatched: candidate.FiltersMatched,
			Winner: candidate.Winner, EliminatedBy: candidate.EliminatedBy,
			Reason: candidate.Reason,
		})
	}
	if len(evaluated) == 0 {
		return nil
	}
	return evaluated
}

func pathNodeIDs(path []hierarchy.PathEntry) []string {
	if len(path) == 0 {
		return nil
	}
	ids := make([]string, len(path))
	for index, entry := range path {
		ids[index] = entry.NodeID
	}
	return ids
}

func factsResolvedFrom(resolved []fact.Resolved) []FactResolved {
	if len(resolved) == 0 {
		return nil
	}
	facts := make([]FactResolved, 0, len(resolved))
	for _, item := range resolved {
		facts = append(facts, FactResolved{
			Field: item.Field, Kind: item.Kind, FactSourceVersion: item.FactSourceVersion,
			Dataset: item.Dataset, RowsScanned: item.RowsScanned, ElapsedMS: item.ElapsedMS,
			Cached: item.Cached, FromRequest: item.FromRequest,
		})
	}
	return facts
}

// orderedReasons puts the governing code first and the rest in a stable order,
// so two runs of the same request produce the same explanation.
func orderedReasons(governing ReasonCode, notes []ReasonCode) []ReasonCode {
	seen := map[ReasonCode]struct{}{governing: {}}
	rest := make([]ReasonCode, 0, len(notes))
	for _, note := range notes {
		if _, done := seen[note]; done {
			continue
		}
		seen[note] = struct{}{}
		rest = append(rest, note)
	}
	sort.Slice(rest, func(left, right int) bool { return rest[left] < rest[right] })
	return append([]ReasonCode{governing}, rest...)
}

// completeRouting fills a routing block from its rule key. A field the caller
// set is left alone, so a disagreement between the two is still visible to
// validation rather than silently overwritten.
func completeRouting(routing Routing) Routing {
	parts, err := routing.RuleKey.Parse()
	if err != nil {
		return routing
	}
	if routing.Domain == "" {
		routing.Domain = parts.Domain
	}
	if routing.RuleSet == "" {
		routing.RuleSet = parts.RuleSet
	}
	if routing.Name == "" {
		routing.Name = parts.Name
	}
	return routing
}

func fail(code ReasonCode) *ReasonCode { return &code }
