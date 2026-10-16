package ruletest_test

import (
	"testing"

	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
)

func TestLoadRule(t *testing.T) {
	definition, err := ruletest.LoadRule("leave/eligibility-min-service-root.json")
	if err != nil {
		t.Fatal(err)
	}
	if definition.Tenant != "RWB" || definition.Scope.Anchor.NodeID != "G001" {
		t.Fatalf("loaded rule = %+v", definition.Scope)
	}
	if definition.Status != rule.StatusActive {
		t.Fatalf("status = %q, want active", definition.Status)
	}
}

func TestLoadRuleRejectsUnsafeOrMissingNames(t *testing.T) {
	// A fixture name is "<group>/<file>.json" and nothing else: it must not be
	// able to reach outside the repository's fixture tree.
	for _, name := range []string{
		"", "leave", "leave/../../go.mod", "/leave/x.json", "leave/x.yaml",
		"x.json", "a/b/c.json", "leave/./x.json", "leave/missing.json",
	} {
		if _, err := ruletest.LoadRule(name); err == nil {
			t.Fatalf("LoadRule(%q) was accepted", name)
		}
	}
}

func TestLoadRules(t *testing.T) {
	group, err := ruletest.LoadRules("payroll")
	if err != nil {
		t.Fatal(err)
	}
	if len(group) != 2 {
		t.Fatalf("payroll group has %d fixtures, want 2", len(group))
	}
	// LoadRules orders by file name so a table test over a group is stable.
	if group[0].EffectiveAnchor().NodeID != "BR001" || group[1].EffectiveAnchor().NodeID != "G001" {
		t.Fatalf("group is not in file-name order: %q then %q", group[0].EffectiveAnchor().NodeID, group[1].EffectiveAnchor().NodeID)
	}
	for _, name := range []string{"", "..", "/payroll", "payroll/nested"} {
		if _, err := ruletest.LoadRules(name); err == nil {
			t.Fatalf("LoadRules(%q) was accepted", name)
		}
	}
}
