package ruleengine_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/rule"
)

// TestDecisionsLeaveTheSnapshotUnchanged pins what lets version selection hand
// the pipeline the snapshot's own rules instead of a deep copy per decision:
// nothing on the decision path writes to a selected rule, and nothing a caller
// gets back on a Decision aliases one.
//
// Decisions run concurrently, both by effective date and by explicit pin, and
// each caller then edits everything it was handed. Run under -race, a write to
// a shared rule anywhere in resolution, fact resolution, evaluation or
// decision building is reported as a race; without -race, the deep comparison
// still catches it.
func TestDecisionsLeaveTheSnapshotUnchanged(t *testing.T) {
	instance, _ := leaveEngine(t)
	keys := map[rule.RuleKey]struct{}{}
	for _, definition := range sharedBundle(t).Rules {
		if definition.Tenant == tenant {
			keys[definition.RuleKey] = struct{}{}
		}
	}
	before := map[rule.RuleKey][]*rule.RuleDefinition{}
	for key := range keys {
		before[key] = ruleengine.SnapshotVersions(instance, key)
	}

	effective := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	pinned := effective
	pinned.RuleVersionPin = "1.0.0"
	requests := []struct {
		request ruleengine.EvalRequest
		version string
	}{{effective, "1.1.0"}, {pinned, "1.0.0"}}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 25 {
				for _, test := range requests {
					decision, err := instance.Evaluate(context.Background(), test.request)
					if err != nil || decision.Failed || decision.RuleVersion != test.version {
						t.Errorf("decision = version %q, %v, %v; want version %q",
							decision.RuleVersion, decision.ReasonCodes, err, test.version)
						return
					}
					for name := range decision.Result {
						decision.Result[name] = "mutated"
					}
					for index := range decision.SubjectPath {
						decision.SubjectPath[index].NodeID = "mutated"
					}
					for index := range decision.Evaluated {
						decision.Evaluated[index].Anchor.NodeID = "mutated"
					}
				}
			}
		})
	}
	wg.Wait()

	for key, want := range before {
		if got := ruleengine.SnapshotVersions(instance, key); !reflect.DeepEqual(got, want) {
			t.Fatalf("rule %s changed in the snapshot after decisions", key)
		}
	}
}
