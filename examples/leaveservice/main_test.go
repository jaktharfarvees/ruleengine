package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestExampleRuns is the phase exit criterion. The example is the wiring a
// service team copies, so it has to keep working: a change that breaks it
// should fail here rather than in somebody else's repository.
func TestExampleRuns(t *testing.T) {
	// The example reads the repository fixtures relative to the working
	// directory, which for a test is the package directory.
	t.Chdir("../..")
	if err := run(quietLogger()); err != nil {
		t.Fatalf("the example failed: %v", err)
	}
}

// TestDecisionPointsAreNamedOnce pins the division of labour the example
// exists to teach: the code names the checks, the rules decide the outcomes.
// A point naming a rule that is not in the fixtures would make the example
// teach a broken pattern.
func TestDecisionPointsAreNamedOnce(t *testing.T) {
	if len(decisionPoints) < 2 {
		t.Fatalf("the example shows %d decision points, want at least two", len(decisionPoints))
	}
	for _, point := range decisionPoints {
		if _, err := point.Key.Parse(); err != nil {
			t.Errorf("decision point %q is not a rule key: %v", point.Key, err)
		}
		if point.DecisionType == "" {
			t.Errorf("decision point %q declares no decision type, so a renamed output contract would pass unnoticed", point.Key)
		}
	}
}

// TestShadowModeDisagrees pins the example's teaching point: the legacy
// six-month check and the Canadian company's three-month rule disagree for
// this employee, which is exactly the situation shadow mode exists to surface
// before anybody cuts over.
func TestShadowModeDisagrees(t *testing.T) {
	t.Chdir("../..")
	engine, closeEngine, err := newEngine(quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer closeEngine()

	application := leaveApplication{
		EmployeeID: "E1001", NodeID: "BR001", EmployeeGroup: "Development team",
		JoinDate: "2026-05-01", RequestedDays: "3",
		AsOf: time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC),
	}

	// Five months of service: the legacy rule refuses, the company rule allows.
	if legacyEligibility(application) {
		t.Fatal("the legacy check should refuse five months of service")
	}
	decision, err := engine.Evaluate(context.Background(),
		requestWithKey(application, decisionPoints[0].Key))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Failed {
		t.Fatalf("the shadow decision failed: %v", decision.ReasonCodes)
	}
	if eligible, _ := decision.Result["eligible"].(bool); !eligible {
		t.Fatalf("the C002 rule should allow five months: %#v", decision.Result)
	}
	if decisionAnchor(decision) != "C002" {
		t.Fatalf("the decision resolved at %q, want the Canadian company", decisionAnchor(decision))
	}
}

// TestLegacyCheckIsTotal pins that the code the rule replaces answers for
// every input, including a malformed date, so shadow mode always has two
// answers to compare.
func TestLegacyCheckIsTotal(t *testing.T) {
	base := leaveApplication{AsOf: time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)}
	tests := []struct {
		joinDate string
		want     bool
	}{
		{"2020-01-01", true},
		{"2026-05-01", false},
		{"not-a-date", false},
		{"", false},
	}
	for _, test := range tests {
		application := base
		application.JoinDate = test.joinDate
		if got := legacyEligibility(application); got != test.want {
			t.Errorf("legacyEligibility(%q) = %t, want %t", test.joinDate, got, test.want)
		}
	}
}

// TestRequestCarriesWhatTheRulesNeed pins the request the example builds: the
// subject's node, because precedence is derived from hierarchy placement, and
// attributes only for what narrows a rule within its anchor.
func TestRequestCarriesWhatTheRulesNeed(t *testing.T) {
	application := leaveApplication{
		EmployeeID: "E1001", NodeID: "BR001", EmployeeGroup: "Development team",
		JoinDate: "2026-05-01", RequestedDays: "3",
		AsOf: time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC),
	}
	request := requestFor(application)
	if request.Subject.NodeID != "BR001" {
		t.Fatal("the request carries no node, so precedence could not be derived")
	}
	if request.Tenant.ID != tenant || request.Routing.Domain != domain {
		t.Fatalf("request names tenant %q domain %q", request.Tenant.ID, request.Routing.Domain)
	}
	if request.AsOf.IsZero() {
		t.Fatal("the request carries no decision time")
	}
	// Hierarchy placement must not be smuggled in as an attribute: that is
	// what nodeId is for, and a filter on it would be rejected anyway.
	for _, forbidden := range []string{"department", "company", "branch", "legalEntity"} {
		if _, present := request.Subject.Attributes[forbidden]; present {
			t.Errorf("the request sends %q as an attribute; placement belongs in nodeId", forbidden)
		}
	}
	if _, present := request.Context["joinDate"]; !present {
		t.Fatal("the request supplies no joinDate, which the rules declare")
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
