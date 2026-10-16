package ruleengine_test

import (
	"context"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/store"
	"github.com/arrowhr/ruleengine/store/filestore"
)

// sharedBundle is the committed fixture tree, loaded the way a service loads
// it. Pipeline tests extend a copy of it rather than adding rules to the
// shared set, which is the contract shared with Rule Studio.
func sharedBundle(t *testing.T) store.Bundle {
	t.Helper()
	loader, err := filestore.New("testdata")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

// engineOver builds an engine over the shared fixtures plus the extra rules
// the pipeline tests need, which exist only to give a decision point a second
// rule declaring the same fact.
func engineOver(t *testing.T, config ruleengine.Config, opts ...ruleengine.Option) *ruleengine.Engine {
	t.Helper()
	bundle := sharedBundle(t)
	bundle.Rules = append(bundle.Rules, pipelineRules()...)
	instance, err := ruleengine.New(config, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.ApplyBundle(bundle); err != nil {
		t.Fatal(err)
	}
	return instance
}

// pipelineRules are test-only rules in the leave domain, anchored at the RWB
// group so they apply to any employee in the tenant.
func pipelineRules() []rule.RuleDefinition {
	return []rule.RuleDefinition{
		// A second rule needing availableBalance, so two points can share it.
		leaveRule("balance-again", []string{"requestedDays", "availableBalance"},
			map[string]string{"requestedDays": "<= $availableBalance"}),
		// A rule needing a different dataset fact, so two points can exceed a
		// one-fact budget between them.
		leaveRule("taken", []string{"casualLeaveTakenThisYear"},
			map[string]string{"casualLeaveTakenThisYear": "<= 20"}),
	}
}

func leaveRule(name string, inputs []string, when map[string]string) rule.RuleDefinition {
	return rule.RuleDefinition{
		RuleKey: rule.RuleKey("urn:arrowhr:rule:leave:validation:" + name + ":v1"),
		Domain:  "leave", RuleSet: "validation", Name: name,
		Version: "1.0.0", ChangeType: rule.ChangeMajor, Status: rule.StatusActive,
		Tenant: tenant,
		Scope:  rule.Scope{Anchor: rule.Anchor{Kind: rule.AnchorNode, NodeID: "G001"}},
		Effective: rule.Effective{
			ValidFrom: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
		DeclaredInputs: inputs, DeclaredOutputs: []string{"allowed", "reasonCode"},
		DecisionType: "leave-validation",
		Logic: rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{
			{ID: "within", When: when, Then: map[string]any{"allowed": true, "reasonCode": "WITHIN_LIMIT"}},
			{ID: "exceeds", When: map[string]string{}, Then: map[string]any{"allowed": false, "reasonCode": "EXCEEDED"}},
		}},
		EngineRef: rule.EngineRef{Engine: "decisiontable"},
		Metadata: rule.Metadata{
			Owner: "People Operations", CreatedBy: "test",
			CreatedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
	}
}
