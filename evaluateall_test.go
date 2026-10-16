package ruleengine_test

import (
	"context"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
)

// countingExecutor counts the queries that actually reach storage, which is
// how de-duplication is proved rather than assumed.
type countingExecutor struct {
	dataset.Executor
	calls int
}

func (c *countingExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	c.calls++
	return c.Executor.Run(ctx, query)
}

// TestEvaluateAllFetchesASharedFactOnce is a phase exit criterion.
//
// A leave application runs several checks for one employee, and more than one
// of them needs the balance. Fetching it per check would both multiply the
// cost and risk the checks disagreeing about the same number, so the fetch is
// shared across the whole call.
func TestEvaluateAllFetchesASharedFactOnce(t *testing.T) {
	manifests, err := ruletest.LoadManifests("leave.balances", "leave.applications")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := ruletest.NewInMemoryExecutor(manifests["leave.balances"], manifests["leave.applications"])
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingExecutor{Executor: inner}
	instance := engineOver(t, ruleengine.Config{
		Tenant: tenant, Domain: "leave", FactBudget: ruletest.Budget(),
	}, ruleengine.WithExecutor(counting))
	addBalanceTo(t, inner, "12.5", asOf.AddDate(0, 0, -1))

	// Two points, both of which declare availableBalance.
	points := []ruleengine.Point{
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:validation:balance:v1")},
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:validation:balance-again:v1")},
	}
	request := leaveRequest("", map[string]any{"requestedDays": "2"})
	results, err := instance.EvaluateAll(context.Background(), points, request)
	if err != nil {
		t.Fatal(err)
	}
	if !results.AllPassed() {
		for _, decision := range results.Failures() {
			t.Errorf("point %s failed: %v", decision.RuleKey, decision.ReasonCodes)
		}
		t.FailNow()
	}
	if counting.calls != 1 {
		t.Fatalf("executor ran %d queries for a fact two points share, want 1", counting.calls)
	}

	// The second point records that it reused the fact rather than hiding it,
	// so an audit record still shows the point used the balance.
	second, found := results.ByKey(points[1].Key)
	if !found {
		t.Fatal("the second point is missing from the results")
	}
	var reused bool
	for _, resolved := range second.FactsResolved {
		if resolved.Field == "availableBalance" {
			reused = resolved.Cached
		}
	}
	if !reused {
		t.Fatalf("the second point's balance was not recorded as reused: %+v", second.FactsResolved)
	}
}

// TestEvaluateAllCollectsEveryFailure pins the run-all default: a user should
// see every problem with their request at once, not one per submission.
func TestEvaluateAllCollectsEveryFailure(t *testing.T) {
	instance, executor := leaveEngine(t)
	addBalance(t, executor, "1", asOf.AddDate(0, 0, -1))

	points := []ruleengine.Point{
		// Passes: five months of service meets C002's three-month rule.
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:eligibility:min-service:v1")},
		// Fails closed: the rule declares requestedDays, which is not supplied.
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:validation:balance:v1")},
		// Fails closed too, and must still be reached.
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:validation:max-consecutive:v1")},
	}
	request := leaveRequest("", map[string]any{"joinDate": "2026-05-01"})
	request.Subject.Attributes = map[string]any{"employeeGroup": "Development team"}

	results, err := instance.EvaluateAll(context.Background(), points, request)
	if err != nil {
		t.Fatal(err)
	}
	all := results.All()
	if len(all) != 3 {
		t.Fatalf("results = %d decisions, want one per point", len(all))
	}
	if results.AllPassed() {
		t.Fatal("AllPassed reported true with failing points")
	}
	failures := results.Failures()
	if len(failures) != 2 {
		t.Fatalf("failures = %d, want both failing points reported", len(failures))
	}
	first, found := results.FirstFailure()
	if !found || first.RuleKey != points[1].Key {
		t.Fatalf("FirstFailure = %v, want the second point", first.RuleKey)
	}
	// Every point is present whatever happened, in the order given.
	for index, point := range points {
		if all[index].RuleKey != point.Key {
			t.Fatalf("results[%d] = %s, want %s", index, all[index].RuleKey, point.Key)
		}
	}
}

// TestOptionalPointDoesNotFailTheSet pins that an informational check records
// its outcome without blocking the request.
func TestOptionalPointDoesNotFailTheSet(t *testing.T) {
	instance, _ := leaveEngine(t)
	points := []ruleengine.Point{
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:eligibility:min-service:v1")},
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:validation:balance:v1"), Optional: true},
	}
	request := leaveRequest("", map[string]any{"joinDate": "2026-05-01"})

	results, err := instance.EvaluateAll(context.Background(), points, request)
	if err != nil {
		t.Fatal(err)
	}
	if !results.AllPassed() {
		t.Fatalf("an optional failure blocked the set: %v", results.Failures())
	}
	// It is still reported, so a caller can show it.
	if failures := results.Failures(); len(failures) != 1 {
		t.Fatalf("failures = %v, want the optional failure recorded", failures)
	}
	if _, found := results.FirstFailure(); !found {
		t.Fatal("FirstFailure hid an optional failure")
	}
}

// TestEvaluateAllSharesTheFactBudget pins that the budget covers the call
// rather than each point: four points that each need a fact cannot together
// spend more than one request's budget.
func TestEvaluateAllSharesTheFactBudget(t *testing.T) {
	budget := ruletest.Budget()
	budget.MaxDatasetFacts = 1
	manifests, err := ruletest.LoadManifests("leave.balances", "leave.applications")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := ruletest.NewInMemoryExecutor(manifests["leave.balances"], manifests["leave.applications"])
	if err != nil {
		t.Fatal(err)
	}
	instance := engineOver(t, ruleengine.Config{
		Tenant: tenant, Domain: "leave", FactBudget: budget,
	}, ruleengine.WithExecutor(executor))
	addBalanceTo(t, executor, "20", asOf.AddDate(0, 0, -1))

	// Two points needing two different dataset facts, against a budget of one.
	points := []ruleengine.Point{
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:validation:balance:v1")},
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:validation:taken:v1")},
	}
	results, err := instance.EvaluateAll(context.Background(), points,
		leaveRequest("", map[string]any{"requestedDays": "2"}))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := results.ByKey(points[1].Key)
	if !second.Failed {
		t.Fatal("the second point was served beyond the request's fact budget")
	}
	if !hasReason(second, ruleengine.ReasonFactBudgetExceeded) {
		t.Fatalf("second point reasons = %v, want the budget breach", second.ReasonCodes)
	}
}

// TestEvaluateAllRejectsAnEmptyPointSet pins that an empty call is a caller
// error rather than a silent pass.
func TestEvaluateAllRejectsAnEmptyPointSet(t *testing.T) {
	instance, _ := leaveEngine(t)
	if _, err := instance.EvaluateAll(context.Background(), nil, leaveRequest("", nil)); err == nil {
		t.Fatal("an empty point set was accepted")
	}
}

func addBalanceTo(t *testing.T, executor *ruletest.InMemoryExecutor, days string, asOfDate time.Time) {
	t.Helper()
	addBalance(t, executor, days, asOfDate)
}

func hasReason(decision ruleengine.Decision, want ruleengine.ReasonCode) bool {
	for _, code := range decision.ReasonCodes {
		if code == want {
			return true
		}
	}
	return false
}
