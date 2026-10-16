package resolve_test

import (
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/resolve"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
)

var fixtureValidAt = time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)

// TestCommittedFixturesResolveAsDocumented drives resolution from the rule
// fixtures on disk rather than from rules built in Go. Those files are the
// contract shared with Rule Studio, so the behaviour they are supposed to
// demonstrate has to be asserted against the files themselves; otherwise they
// can drift from the algorithm without any test noticing.
func TestCommittedFixturesResolveAsDocumented(t *testing.T) {
	snapshot, err := ruletest.LoadRWB()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("company override beats the group baseline", func(t *testing.T) {
		root := mustLoadRule(t, "leave/eligibility-min-service-root.json")
		canada := mustLoadRule(t, "leave/eligibility-min-service-ca.json")
		result := resolve.Resolve(fixtureInput("BR001", "E1001", nil), []*rule.RuleDefinition{root, canada}, snapshot)
		assertFixtureWinner(t, result, canada)
		// BR001 sits under the Canadian company, so the deeper anchor wins.
		if result.Winner.Precedence.NodeID != "C002" {
			t.Fatalf("winning node = %q, want C002", result.Winner.Precedence.NodeID)
		}
	})

	t.Run("employee exception beats the payroll baseline", func(t *testing.T) {
		baseline := mustLoadRule(t, "payroll/overtime-payment-root.json")
		exception := mustLoadRule(t, "payroll/overtime-payment-exception-e9001.json")
		result := resolve.Resolve(fixtureInput("BR001", "E9001", nil), []*rule.RuleDefinition{baseline, exception}, snapshot)
		assertFixtureWinner(t, result, exception)
		if !result.Winner.Precedence.IsEmployeeException {
			t.Fatal("winner is not recorded as an employee exception")
		}
	})

	t.Run("the exception does not reach another employee", func(t *testing.T) {
		baseline := mustLoadRule(t, "payroll/overtime-payment-root.json")
		exception := mustLoadRule(t, "payroll/overtime-payment-exception-e9001.json")
		result := resolve.Resolve(fixtureInput("BR001", "E1001", nil), []*rule.RuleDefinition{baseline, exception}, snapshot)
		assertFixtureWinner(t, result, baseline)
	})

	t.Run("a department filter narrows but does not promote", func(t *testing.T) {
		root := mustLoadRule(t, "leave/validation-balance-root.json")
		department := mustLoadRule(t, "leave/validation-max-consecutive-dept.json")
		candidates := []*rule.RuleDefinition{root, department}
		// The filtered department rule applies to the development team.
		inside := resolve.Resolve(fixtureInput("BR001", "E1001", map[string]any{"employeeGroup": "Development team"}), candidates, snapshot)
		assertFixtureWinner(t, inside, department)
		// Outside the group the filter eliminates it and the baseline applies,
		// because a filter never raises a rule above a deeper anchor.
		outside := resolve.Resolve(fixtureInput("BR001", "E1001", map[string]any{"employeeGroup": "Finance"}), candidates, snapshot)
		assertFixtureWinner(t, outside, root)
	})

	t.Run("the conflict pair fails closed", func(t *testing.T) {
		pair := []*rule.RuleDefinition{
			mustLoadRule(t, "conflict/tie-a.json"),
			mustLoadRule(t, "conflict/tie-b.json"),
		}
		attrs := map[string]any{"employeeGroup": "Finance", "workerCategory": "PERMANENT"}
		result := resolve.Resolve(fixtureInput("BR003", "E1001", attrs), pair, snapshot)
		if result.Winner != nil || result.Tie == nil || !result.FailClosed {
			t.Fatalf("winner = %v, tie = %v, failClosed = %t; want no winner and a tie", result.Winner, result.Tie, result.FailClosed)
		}
		if result.ReasonCode != "PRECEDENCE_TIE_UNRESOLVED" {
			t.Fatalf("reason code = %q, want PRECEDENCE_TIE_UNRESOLVED", result.ReasonCode)
		}
		if len(result.Tie.Candidates) != 2 {
			t.Fatalf("tie lists %d candidates, want 2", len(result.Tie.Candidates))
		}
	})

	t.Run("the priority pair resolves to the lower number", func(t *testing.T) {
		first := mustLoadRule(t, "conflict/tie-priority-a.json")
		second := mustLoadRule(t, "conflict/tie-priority-b.json")
		attrs := map[string]any{"employeeGroup": "Finance", "workerCategory": "PERMANENT"}
		result := resolve.Resolve(fixtureInput("BR003", "E1001", attrs), []*rule.RuleDefinition{second, first}, snapshot)
		assertFixtureWinner(t, result, first)
		found := false
		for _, candidate := range result.Candidates {
			if candidate.Rule != second {
				continue
			}
			found = true
			if candidate.EliminatedBy != resolve.EliminatedTieBreak {
				t.Fatalf("loser eliminated by %q, want tie-break; it ranks equally and lost only on priority", candidate.EliminatedBy)
			}
		}
		if !found {
			t.Fatal("the higher-priority rule is missing from the candidate list")
		}
	})

	t.Run("every conflict fixture loads as a group", func(t *testing.T) {
		group, err := ruletest.LoadRules("conflict")
		if err != nil {
			t.Fatal(err)
		}
		if len(group) != 4 {
			t.Fatalf("conflict group has %d fixtures, want 4", len(group))
		}
		for _, definition := range group {
			if definition.Scope.Anchor.NodeID != "D003" || len(definition.Scope.Filters) != 1 {
				t.Fatalf("%s is not an equally specific same-anchor rule: %+v", definition.RuleKey, definition.Scope)
			}
		}
	})
}

func mustLoadRule(t *testing.T, name string) *rule.RuleDefinition {
	t.Helper()
	definition, err := ruletest.LoadRule(name)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func fixtureInput(nodeID, employeeID string, attrs map[string]any) resolve.Input {
	return resolve.Input{
		Tenant: "RWB", NodeID: nodeID, EmployeeID: employeeID,
		Attributes: attrs, ValidAt: fixtureValidAt,
	}
}

func assertFixtureWinner(t *testing.T, result resolve.Result, want *rule.RuleDefinition) {
	t.Helper()
	if result.Winner == nil {
		t.Fatalf("no winner; reason %q, tie %v", result.ReasonCode, result.Tie)
	}
	if result.Winner.Rule != want {
		t.Fatalf("winner = %s (%s), want %s", result.Winner.Rule.RuleKey, result.Winner.Rule.Description, want.Description)
	}
}
