package ruleengine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/contextbuilder"
	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
	"github.com/arrowhr/ruleengine/store"
	"github.com/arrowhr/ruleengine/store/filestore"
)

const (
	tenant   = "RWB"
	employee = "E1001"
	// BR001 sits under D002, under C002, under G001 in the RWB fixture.
	branch = "BR001"
)

var asOf = time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)

// leaveEngine wires a real engine over the committed fixtures: the file loader
// supplies rules, fields, manifests and the hierarchy, and an in-memory
// executor answers the dataset-sourced fields.
func leaveEngine(t *testing.T, opts ...ruleengine.Option) (*ruleengine.Engine, *ruletest.InMemoryExecutor) {
	t.Helper()
	manifests, err := ruletest.LoadManifests("leave.balances", "leave.applications")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := ruletest.NewInMemoryExecutor(manifests["leave.balances"], manifests["leave.applications"])
	if err != nil {
		t.Fatal(err)
	}
	loader, err := filestore.New("testdata")
	if err != nil {
		t.Fatal(err)
	}
	options := append([]ruleengine.Option{
		ruleengine.WithLoader(loader),
		ruleengine.WithExecutor(executor),
	}, opts...)
	instance, err := ruleengine.New(ruleengine.Config{
		Tenant: tenant, Domain: "leave", FactBudget: ruletest.Budget(),
	}, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return instance, executor
}

// leaveRequest builds a well-formed request for one employee at BR001.
func leaveRequest(ruleKey string, context map[string]any) ruleengine.EvalRequest {
	return ruleengine.EvalRequest{
		SchemaVersion: 2,
		Tenant:        ruleengine.Tenant{ID: tenant},
		// Only the domain and the key: the engine fills the rule set and name
		// from the key, which is how a caller with a list of keys works.
		Routing: ruleengine.Routing{Domain: "leave", RuleKey: rule.RuleKey(ruleKey)},
		Subject: ruleengine.Subject{
			Type: "employee",
			Identifier: ruleengine.Identifier{
				System: "urn:arrowhr:ns:employee-id", Value: employee, IDType: "employeeId",
			},
			NodeID: branch,
		},
		Context:       context,
		AsOf:          asOf,
		CorrelationID: "corr-1",
	}
}

// TestPipelineEvaluatesTheRWBFixtures is the phase's headline check: the whole
// pipeline, over the committed fixtures, picks the Canadian company's rule for
// an employee beneath it rather than the group baseline, and explains why.
func TestPipelineEvaluatesTheRWBFixtures(t *testing.T) {
	instance, _ := leaveEngine(t)
	if err := instance.Ready(); err != nil {
		t.Fatalf("Ready = %v, want ready after a successful load", err)
	}

	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Failed {
		t.Fatalf("decision failed: %v", decision.ReasonCodes)
	}

	// C002 is the Canadian company and is deeper on BR001's path than G001, so
	// its three-month rule applies rather than the group's six-month one. The
	// employee joined five months before asOf: eligible under C002, not under
	// G001. That difference is the whole point of deepest-anchor-wins.
	if decision.RuleVersion != "1.1.0" {
		t.Fatalf("rule version = %q, want the C002 override 1.1.0", decision.RuleVersion)
	}
	if decision.ResolvedAt == nil || decision.ResolvedAt.NodeID != "C002" {
		t.Fatalf("resolvedAt = %+v, want C002", decision.ResolvedAt)
	}
	if eligible, ok := decision.Result["eligible"].(bool); !ok || !eligible {
		t.Fatalf("result = %#v, want eligible true", decision.Result)
	}
	if decision.Result["reasonCode"] != "ELIGIBLE" {
		t.Fatalf("result reasonCode = %#v", decision.Result["reasonCode"])
	}

	// The decision has to explain itself: the full path, every candidate and
	// why each was in or out.
	if len(decision.SubjectPath) != 4 {
		t.Fatalf("subject path = %+v, want four nodes", decision.SubjectPath)
	}
	if decision.SubjectPath[0].NodeID != branch || decision.SubjectPath[3].NodeID != "G001" {
		t.Fatalf("subject path is not deepest-first: %+v", decision.SubjectPath)
	}
	if len(decision.Evaluated) != 2 {
		t.Fatalf("evaluated = %+v, want both candidates", decision.Evaluated)
	}
	var winners, losers int
	for _, evaluated := range decision.Evaluated {
		if evaluated.Winner {
			winners++
			continue
		}
		losers++
		if evaluated.Reason == "" {
			t.Fatalf("candidate %s was eliminated without a reason", evaluated.RuleKey)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("evaluated %d winners and %d losers", winners, losers)
	}

	// serviceMonths is derived from the passed joinDate, so the fact record
	// shows the derivation and the dependency.
	if len(decision.FactsResolved) != 2 {
		t.Fatalf("facts = %+v, want serviceMonths and its joinDate dependency", decision.FactsResolved)
	}
	kinds := map[string]fact.Kind{}
	for _, resolved := range decision.FactsResolved {
		kinds[resolved.Field] = resolved.Kind
	}
	if kinds["serviceMonths"] != fact.KindDerived || kinds["joinDate"] != fact.KindPassed {
		t.Fatalf("fact kinds = %v", kinds)
	}

	// Hierarchy-derived attributes carry the node that declared them. BR001
	// declares its own timezone while country comes from C002 above it.
	country, ok := decision.ResolvedAttributes["country"]
	if !ok || country.Value != "Canada" || country.SourceNodeID != "C002" {
		t.Fatalf("country = %+v, want Canada from C002", country)
	}
	timezone, ok := decision.ResolvedAttributes["timezone"]
	if !ok || timezone.SourceNodeID != branch {
		t.Fatalf("timezone = %+v, want it sourced from %s", timezone, branch)
	}

	// The envelope is carried through for correlation and replay.
	if decision.DecisionID == "" || decision.CorrelationID != "corr-1" || decision.LibVersion == "" {
		t.Fatalf("decision envelope is incomplete: %+v", decision)
	}
	if !decision.ValidAt.Equal(asOf) {
		t.Fatalf("validAt = %s, want it to default to asOf", decision.ValidAt)
	}
	if len(decision.ReasonCodes) != 1 || decision.ReasonCodes[0] != ruleengine.ReasonOK {
		t.Fatalf("reason codes = %v, want only OK", decision.ReasonCodes)
	}
}

// TestGroupBaselineAppliesOffTheOverriddenBranch pins the other half of
// deepest-anchor-wins: an employee outside C002's subtree gets the group rule,
// and the same joinDate now makes them ineligible.
func TestGroupBaselineAppliesOffTheOverriddenBranch(t *testing.T) {
	instance, _ := leaveEngine(t)
	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	// BR004 sits under D004, under C004 — a different company from C002.
	request.Subject.NodeID = "BR004"

	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Failed {
		t.Fatalf("decision failed: %v", decision.ReasonCodes)
	}
	if decision.RuleVersion != "1.0.0" || decision.ResolvedAt.NodeID != "G001" {
		t.Fatalf("version %q at %q, want the 1.0.0 baseline at G001",
			decision.RuleVersion, decision.ResolvedAt.NodeID)
	}
	// Five months of service does not meet the group's six-month rule.
	if eligible, _ := decision.Result["eligible"].(bool); eligible {
		t.Fatalf("result = %#v, want ineligible under the six-month baseline", decision.Result)
	}
	if decision.Result["reasonCode"] != "MIN_SERVICE_NOT_MET" {
		t.Fatalf("result reasonCode = %#v", decision.Result["reasonCode"])
	}
	// A business refusal is still a successful decision, not a failure.
	if decision.Failed {
		t.Fatal("a rule deciding against the subject must not fail the decision")
	}
}

// TestDatasetSourcedFactReachesTheDecision pins the whole fact chain through
// the pipeline: a declared dataset field, compiled and executed, deciding an
// outcome.
func TestDatasetSourcedFactReachesTheDecision(t *testing.T) {
	instance, executor := leaveEngine(t)
	addBalance(t, executor, "12.5", asOf.AddDate(0, 0, -1))

	request := leaveRequest("urn:arrowhr:rule:leave:validation:balance:v1", map[string]any{"requestedDays": "3"})
	request.Routing = ruleengine.Routing{Domain: "leave",
		RuleKey: rule.RuleKey("urn:arrowhr:rule:leave:validation:balance:v1")}

	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Failed {
		t.Fatalf("decision failed: %v", decision.ReasonCodes)
	}
	if allowed, _ := decision.Result["allowed"].(bool); !allowed {
		t.Fatalf("result = %#v, want three days allowed against a 12.5 balance", decision.Result)
	}
	var balance FactRecord
	for _, resolved := range decision.FactsResolved {
		if resolved.Field == "availableBalance" {
			balance = FactRecord{Kind: resolved.Kind, Dataset: resolved.Dataset}
		}
	}
	if balance.Kind != fact.KindDataset || balance.Dataset != "leave.balances" {
		t.Fatalf("availableBalance fact = %+v, want a leave.balances dataset fact", balance)
	}

	// More days than the balance is refused by the rule, not by a failure.
	request.Context = map[string]any{"requestedDays": "30"}
	decision, err = instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Failed {
		t.Fatalf("decision failed: %v", decision.ReasonCodes)
	}
	if allowed, _ := decision.Result["allowed"].(bool); allowed {
		t.Fatalf("result = %#v, want thirty days refused", decision.Result)
	}
}

// FactRecord is a small shape for asserting on one fact record.
type FactRecord struct {
	Kind    fact.Kind
	Dataset string
}

// TestReadyReportsNotReadyWithAnEmptyHierarchy is a phase exit criterion. A
// service must be able to tell it cannot decide yet, so it does not accept
// traffic it would only fail closed.
func TestReadyReportsNotReadyWithAnEmptyHierarchy(t *testing.T) {
	instance, err := ruleengine.New(ruleengine.Config{
		Tenant: tenant, Domain: "leave", FactBudget: ruletest.Budget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	// Nothing published at all.
	if err := instance.Ready(); err == nil {
		t.Fatal("an engine with no snapshot reported ready")
	}
	// Rules published, but no hierarchy for this tenant: precedence is derived
	// from the hierarchy, so there is nothing to derive it from.
	fields, err := ruletest.LoadRegistry("leave")
	if err != nil {
		t.Fatal(err)
	}
	_ = fields
	definition, err := ruletest.LoadRule("leave/eligibility-min-service-root.json")
	if err != nil {
		t.Fatal(err)
	}
	err = instance.ApplyBundle(store.Bundle{Rules: []rule.RuleDefinition{*definition}, LoadedAt: asOf})
	if err != nil {
		t.Fatal(err)
	}
	if readyErr := instance.Ready(); readyErr == nil {
		t.Fatal("an engine with no hierarchy for its tenant reported ready")
	}

	// An evaluation in that state fails closed rather than panicking.
	decision, err := instance.Evaluate(context.Background(),
		leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Failed {
		t.Fatal("an engine with no hierarchy produced a passing decision")
	}
}

func addBalance(t *testing.T, executor *ruletest.InMemoryExecutor, days string, asOfDate time.Time) {
	t.Helper()
	if err := executor.Add("leave.balances", ruletest.Row{
		dataset.TenantColumn: tenant,
		"employeeId":         employee,
		"leaveType":          "ANNUAL",
		"balanceDays":        days,
		"asOfDate":           asOfDate.Format(time.DateOnly),
	}); err != nil {
		t.Fatal(err)
	}
}

// TestContextBuilderSuppliesFactsTheServiceComputes pins the builder path: a
// field the service computes itself reaches the decision, and a value the
// request already carries is not overwritten by it.
func TestContextBuilderSuppliesFactsTheServiceComputes(t *testing.T) {
	// A builder supplies a declared input, not its dependencies: the rule
	// declares serviceMonths, so that is what the pipeline asks for, and a
	// service that computes tenure itself answers with it directly.
	built := &recordingBuilder{values: contextbuilder.Static{"serviceMonths": 5}}
	instance := engineOver(t, ruleengine.Config{
		Tenant: tenant, Domain: "leave", FactBudget: ruletest.Budget(),
	}, ruleengine.WithContextBuilder(built))

	// Nothing in the request context: the builder supplies serviceMonths, so
	// the derived field is taken as given rather than computed.
	decision, err := instance.Evaluate(context.Background(),
		leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Failed {
		t.Fatalf("decision failed: %v", decision.ReasonCodes)
	}
	if eligible, _ := decision.Result["eligible"].(bool); !eligible {
		t.Fatalf("result = %#v, want eligible on five built months", decision.Result)
	}
	for _, resolved := range decision.FactsResolved {
		if resolved.Field == "serviceMonths" && !resolved.FromRequest {
			t.Fatal("the built value was recomputed rather than taken as given")
		}
	}

	// The builder is asked for the winning rule's declared inputs, for this
	// subject, in the period the decision falls in.
	if built.subjectID != employee {
		t.Fatalf("builder saw subject %q, want %q", built.subjectID, employee)
	}
	if built.period != "2026-10" {
		t.Fatalf("builder saw period %q, want the decision's month", built.period)
	}
	if len(built.fields) != 1 || built.fields[0] != "serviceMonths" {
		t.Fatalf("builder was asked for %v, want the rule's declared inputs", built.fields)
	}

	// A value the caller already supplied wins: the request is more specific
	// than anything the service recomputes for it.
	built.values = contextbuilder.Static{"serviceMonths": 99}
	decision, err = instance.Evaluate(context.Background(),
		leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
			map[string]any{"serviceMonths": 1}))
	if err != nil {
		t.Fatal(err)
	}
	// One month meets neither rule, which the builder's 99 would have.
	if eligible, _ := decision.Result["eligible"].(bool); eligible {
		t.Fatalf("result = %#v, want the request's own value to win", decision.Result)
	}
}

// TestContextBuilderFailureFailsClosed pins that a builder that cannot answer
// holds the decision rather than letting it proceed on missing facts.
func TestContextBuilderFailureFailsClosed(t *testing.T) {
	instance := engineOver(t, ruleengine.Config{
		Tenant: tenant, Domain: "leave", FactBudget: ruletest.Budget(),
	}, ruleengine.WithContextBuilder(&recordingBuilder{err: errors.New("employee service unavailable")}))

	decision, err := instance.Evaluate(context.Background(),
		leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Failed || !hasReason(decision, ruleengine.ReasonFactUnresolvable) {
		t.Fatalf("decision = %v, want a fact failure", decision.ReasonCodes)
	}
}

// recordingBuilder records what the pipeline asked it for.
type recordingBuilder struct {
	values    contextbuilder.Static
	err       error
	fields    []string
	subjectID string
	period    string
}

func (b *recordingBuilder) Build(
	ctx context.Context, fields []string, subjectID string, period string,
) (map[string]any, error) {
	b.fields = append([]string(nil), fields...)
	b.subjectID = subjectID
	b.period = period
	if b.err != nil {
		return nil, b.err
	}
	return b.values.Build(ctx, fields, subjectID, period)
}

// TestRoutingIsDerivedFromTheRuleKey pins that a caller need not repeat what
// the key already says, which is what makes EvaluateAll usable with a bare
// list of keys, and that a routing block contradicting its own key is refused
// rather than silently resolved one way or the other.
func TestRoutingIsDerivedFromTheRuleKey(t *testing.T) {
	instance, _ := leaveEngine(t)
	key := rule.RuleKey("urn:arrowhr:rule:leave:eligibility:min-service:v1")

	// Only the key: domain, rule set and name are all filled from it.
	request := leaveRequest("", map[string]any{"joinDate": "2026-05-01"})
	request.Routing = ruleengine.Routing{RuleKey: key}
	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Failed {
		t.Fatalf("a request carrying only a rule key failed: %v", decision.ReasonCodes)
	}
	if decision.RuleVersion != "1.1.0" {
		t.Fatalf("rule version = %q, want 1.1.0", decision.RuleVersion)
	}

	// A routing block naming a different rule than its key is refused: one of
	// the two is a mistake and there is no way to tell which.
	contradictions := []ruleengine.Routing{
		{Domain: "leave", RuleSet: "validation", Name: "min-service", RuleKey: key},
		{Domain: "leave", RuleSet: "eligibility", Name: "balance", RuleKey: key},
	}
	for _, routing := range contradictions {
		request := leaveRequest("", map[string]any{"joinDate": "2026-05-01"})
		request.Routing = routing
		decision, err := instance.Evaluate(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if !decision.Failed || !hasReason(decision, ruleengine.ReasonSchemaInvalid) {
			t.Fatalf("routing %+v was accepted; reasons %v", routing, decision.ReasonCodes)
		}
	}
}
