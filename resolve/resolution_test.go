package resolve_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/resolve"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
)

var phase3ValidAt = time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)

func TestRT01DeeperAnchorWins(t *testing.T) {
	snapshot := loadHierarchy(t, "rwb.json")
	branch := nodeRule("branch", "BR001")
	department := nodeRule("department", "D002")
	result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{department, branch}, snapshot)
	assertWinner(t, result, branch)
	loser := candidateFor(t, result, department)
	if loser.EliminatedBy != resolve.EliminatedLowerOrder ||
		!strings.Contains(loser.Reason, "Branch(3)") || !strings.Contains(loser.Reason, "Department(2)") {
		t.Fatalf("department candidate = %#v", loser)
	}
}

func TestRT02DepartmentRuleCoversBothBranches(t *testing.T) {
	snapshot := loadHierarchy(t, "rwb.json")
	department := nodeRule("department", "D002")
	for _, nodeID := range []string{"BR001", "BR002"} {
		result := resolve.Resolve(inputAt(nodeID), []*rule.RuleDefinition{department}, snapshot)
		assertWinner(t, result, department)
	}
}

func TestRT03RootBaselineApplies(t *testing.T) {
	root := nodeRule("root", "G001")
	result := resolve.Resolve(inputAt("BR003"), []*rule.RuleDefinition{root}, loadHierarchy(t, "rwb.json"))
	assertWinner(t, result, root)
	if result.Winner.Precedence.Order != 0 {
		t.Fatalf("root order = %d", result.Winner.Precedence.Order)
	}
}

func TestRT04EmployeeExceptionWins(t *testing.T) {
	rules := []*rule.RuleDefinition{
		nodeRule("root", "G001"),
		nodeRule("company", "C002"),
		nodeRule("department", "D002"),
		nodeRule("branch", "BR001"),
		employeeRule("employee", "E9001"),
	}
	result := resolve.Resolve(inputAt("BR001"), rules, loadHierarchy(t, "rwb.json"))
	assertWinner(t, result, rules[4])
	if !result.Winner.Precedence.IsEmployeeException {
		t.Fatal("employee winner lacks employee-exception precedence")
	}
	for _, definition := range rules[:4] {
		if candidateFor(t, result, definition).EliminatedBy != resolve.EliminatedLowerOrder {
			t.Fatalf("node rule %q was not lower-order", definition.Name)
		}
	}
}

func TestRT05OffPathAnchorNeverWins(t *testing.T) {
	offPath := nodeRule("off-path", "BR003")
	result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{offPath}, loadHierarchy(t, "rwb.json"))
	assertFailed(t, result, "NO_RULE_RESOLVED")
	if candidateFor(t, result, offPath).EliminatedBy != resolve.EliminatedNotOnPath {
		t.Fatalf("candidate = %#v", candidateFor(t, result, offPath))
	}
}

func TestRT06FiltersNarrowButNeverPromote(t *testing.T) {
	root := nodeRule("filtered-root", "G001")
	root.Scope.Filters = []rule.FilterCondition{filter("country", rule.FilterEquals, "Canada")}
	company := nodeRule("company", "C002")
	input := inputAt("BR001")
	input.Attributes = map[string]any{"country": "Canada"}
	result := resolve.Resolve(input, []*rule.RuleDefinition{root, company}, loadHierarchy(t, "rwb.json"))
	assertWinner(t, result, company)
	if candidateFor(t, result, root).EliminatedBy != resolve.EliminatedLowerOrder {
		t.Fatalf("root candidate = %#v", candidateFor(t, result, root))
	}
}

func TestRT07OverlappingFiltersTieAndFailClosed(t *testing.T) {
	finance := nodeRule("finance-filter", "D003")
	finance.Scope.Filters = []rule.FilterCondition{filter("employeeGroup", rule.FilterEquals, "Finance")}
	permanent := nodeRule("worker-filter", "D003")
	permanent.Scope.Filters = []rule.FilterCondition{filter("workerCategory", rule.FilterEquals, "PERMANENT")}
	input := inputAt("BR003")
	input.Attributes = map[string]any{"employeeGroup": "Finance", "workerCategory": "PERMANENT"}
	result := resolve.Resolve(input, []*rule.RuleDefinition{permanent, finance}, loadHierarchy(t, "rwb.json"))
	assertTie(t, result, finance, permanent)
}

func TestRT08InsertedHierarchyLevelDoesNotChangeWinner(t *testing.T) {
	company := nodeRule("company", "C002")
	department := nodeRule("department", "D002")
	for _, fixture := range []string{"rwb.json", "rwb-with-division.json"} {
		result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{company, department}, loadHierarchy(t, fixture))
		assertWinner(t, result, department)
	}
}

func TestRT09AttributesResolveIndependently(t *testing.T) {
	snapshot := loadHierarchy(t, "rwb.json")
	tests := []struct{ attribute, value, source string }{
		{"country", "Canada", "C002"},
		{"currency", "CAD", "C002"},
		{"locale", "en-CA", "C002"},
		{"timezone", "Asia/Colombo", "BR001"},
	}
	for _, test := range tests {
		value, source, ok := snapshot.ResolveAttribute("RWB", "BR001", test.attribute)
		if !ok || value != test.value || source != test.source {
			t.Fatalf("%s = %q from %q (%v)", test.attribute, value, source, ok)
		}
	}
}

func TestRT10InactiveNodesFailClosed(t *testing.T) {
	snapshot := loadHierarchy(t, "rwb-inactive.json")
	result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{nodeRule("root", "G001")}, snapshot)
	assertFailed(t, result, "SUBJECT_NODE_UNRESOLVABLE")

	inactiveAnchor := nodeRule("inactive-department", "D003")
	result = resolve.Resolve(inputAt("BR003"), []*rule.RuleDefinition{inactiveAnchor}, snapshot)
	assertFailed(t, result, "NO_RULE_RESOLVED")
	if candidateFor(t, result, inactiveAnchor).EliminatedBy != resolve.EliminatedNotOnPath {
		t.Fatalf("inactive anchor candidate = %#v", candidateFor(t, result, inactiveAnchor))
	}
}

func TestRT11CrossTenantCandidateDropped(t *testing.T) {
	crossTenant := nodeRule("cross-tenant", "G001")
	crossTenant.Tenant = "Earrow"
	root := nodeRule("root", "G001")
	result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{crossTenant, root}, loadHierarchy(t, "rwb.json"))
	assertWinner(t, result, root)
	if candidateFor(t, result, crossTenant).EliminatedBy != resolve.EliminatedCrossTenant {
		t.Fatalf("cross-tenant candidate = %#v", candidateFor(t, result, crossTenant))
	}
}

func TestRT12ExplicitPriorityBreaksTie(t *testing.T) {
	priority10, priority20 := 10, 20
	first := nodeRule("priority-a", "D003")
	first.TieBreakPriority = &priority10
	first.Scope.Filters = []rule.FilterCondition{filter("employeeGroup", rule.FilterEquals, "Finance")}
	second := nodeRule("priority-b", "D003")
	second.TieBreakPriority = &priority20
	second.Scope.Filters = []rule.FilterCondition{filter("workerCategory", rule.FilterEquals, "PERMANENT")}
	input := inputAt("BR003")
	input.Attributes = map[string]any{"employeeGroup": "Finance", "workerCategory": "PERMANENT"}
	result := resolve.Resolve(input, []*rule.RuleDefinition{second, first}, loadHierarchy(t, "rwb.json"))
	assertWinner(t, result, first)
	if candidateFor(t, result, second).EliminatedBy != resolve.EliminatedTieBreak {
		t.Fatalf("priority loser = %#v", candidateFor(t, result, second))
	}
}

func TestRT13RepeatedHierarchyTypeUsesDepth(t *testing.T) {
	outer := nodeRule("outer-department", "D002")
	inner := nodeRule("inner-department", "D002A")
	result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{outer, inner}, loadHierarchy(t, "rwb-nested-department.json"))
	assertWinner(t, result, inner)
	loser := candidateFor(t, result, outer)
	if loser.Precedence.Order != result.Winner.Precedence.Order || loser.Precedence.Depth >= result.Winner.Precedence.Depth {
		t.Fatalf("winner=%#v loser=%#v", result.Winner.Precedence, loser.Precedence)
	}
}

func TestRT14EvaluatedListIsDeterministic(t *testing.T) {
	snapshot := loadHierarchy(t, "rwb.json")
	input := inputAt("BR001")
	input.Attributes = map[string]any{"country": "Canada"}
	rules := []*rule.RuleDefinition{
		nodeRule("root-b", "G001"), nodeRule("department", "D002"),
		nodeRule("off-path", "BR003"), nodeRule("root-a", "G001"),
		nodeRule("branch", "BR001"), nil,
	}
	rules[0].Scope.Filters = []rule.FilterCondition{filter("country", rule.FilterEquals, "Canada")}
	want, err := json.Marshal(resolve.Resolve(input, rules, snapshot))
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(14))
	for iteration := 0; iteration < 100; iteration++ {
		shuffled := append([]*rule.RuleDefinition(nil), rules...)
		random.Shuffle(len(shuffled), func(left, right int) { shuffled[left], shuffled[right] = shuffled[right], shuffled[left] })
		got, err := json.Marshal(resolve.Resolve(input, shuffled, snapshot))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("iteration %d produced nondeterministic result\nwant %s\n got %s", iteration, want, got)
		}
	}
}

func TestResolutionStageEliminations(t *testing.T) {
	now := phase3ValidAt
	expiredAt := now.Add(-time.Hour)
	tests := []struct {
		name string
		rule *rule.RuleDefinition
		want resolve.EliminatedBy
	}{
		{"nil", nil, resolve.EliminatedStatus},
		{"draft", func() *rule.RuleDefinition { r := nodeRule("draft", "G001"); r.Status = rule.StatusDraft; return r }(), resolve.EliminatedStatus},
		{"expired", func() *rule.RuleDefinition {
			r := nodeRule("expired", "G001")
			r.Effective.ValidTo = &expiredAt
			return r
		}(), resolve.EliminatedEffectiveDate},
		{"filter", func() *rule.RuleDefinition {
			r := nodeRule("filter", "G001")
			r.Scope.Filters = []rule.FilterCondition{filter("team", rule.FilterEquals, "A")}
			return r
		}(), resolve.EliminatedFilters},
		{"malformed anchor", func() *rule.RuleDefinition { r := nodeRule("malformed", ""); return r }(), resolve.EliminatedNotOnPath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{test.rule}, loadHierarchy(t, "rwb.json"))
			if len(result.Candidates) != 1 || result.Candidates[0].EliminatedBy != test.want {
				t.Fatalf("result = %#v", result)
			}
			if test.want == resolve.EliminatedFilters && result.Candidates[0].FailedOn == nil {
				t.Fatal("filter elimination did not record FailedOn")
			}
		})
	}
}

func TestResolutionBoundaryFailures(t *testing.T) {
	result := resolve.Resolve(inputAt("BR001"), nil, nil)
	assertFailed(t, result, "HIERARCHY_UNAVAILABLE")

	snapshot := loadHierarchy(t, "rwb.json")
	result = resolve.Resolve(inputAt("missing"), nil, snapshot)
	assertFailed(t, result, "SUBJECT_NODE_UNRESOLVABLE")

	result = resolve.Resolve(inputAt("BR001"), nil, snapshot)
	assertFailed(t, result, "NO_RULE_RESOLVED")
}

func TestEmployeeExceptionsTie(t *testing.T) {
	first := employeeRule("employee-a", "E9001")
	second := employeeRule("employee-b", "E9001")
	root := nodeRule("root", "G001")
	result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{root, second, first}, loadHierarchy(t, "rwb.json"))
	assertTie(t, result, first, second)
	if candidateFor(t, result, root).EliminatedBy != resolve.EliminatedLowerOrder {
		t.Fatalf("root candidate = %#v", candidateFor(t, result, root))
	}
}

func TestFilterSpecificityBreaksSameRank(t *testing.T) {
	unfiltered := nodeRule("unfiltered", "D002")
	filtered := nodeRule("filtered", "D002")
	filtered.Scope.Filters = []rule.FilterCondition{filter("team", rule.FilterEquals, "Engineering")}
	input := inputAt("BR001")
	input.Attributes = map[string]any{"team": "Engineering"}
	result := resolve.Resolve(input, []*rule.RuleDefinition{unfiltered, filtered}, loadHierarchy(t, "rwb.json"))
	assertWinner(t, result, filtered)
}

func TestPartiallyPrioritizedCandidatesStillTie(t *testing.T) {
	priority := 1
	withPriority := nodeRule("with-priority", "D002")
	withPriority.TieBreakPriority = &priority
	withoutPriority := nodeRule("without-priority", "D002")
	result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{withPriority, withoutPriority}, loadHierarchy(t, "rwb.json"))
	assertTie(t, result, withPriority, withoutPriority)
}

func TestEqualPrioritiesStillTie(t *testing.T) {
	priority := 10
	first := nodeRule("equal-a", "D002")
	first.TieBreakPriority = &priority
	second := nodeRule("equal-b", "D002")
	second.TieBreakPriority = &priority
	result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{second, first}, loadHierarchy(t, "rwb.json"))
	assertTie(t, result, first, second)
}

func TestHigherPriorityIsEliminatedWhenLowestPrioritiesTie(t *testing.T) {
	priority10, priority20 := 10, 20
	first := nodeRule("lowest-a", "D002")
	first.TieBreakPriority = &priority10
	second := nodeRule("lowest-b", "D002")
	second.TieBreakPriority = &priority10
	higher := nodeRule("higher", "D002")
	higher.TieBreakPriority = &priority20
	result := resolve.Resolve(inputAt("BR001"), []*rule.RuleDefinition{higher, second, first}, loadHierarchy(t, "rwb.json"))
	assertTie(t, result, first, second)
	if candidateFor(t, result, higher).EliminatedBy != resolve.EliminatedTieBreak {
		t.Fatalf("higher-priority candidate = %#v", candidateFor(t, result, higher))
	}
}

func TestAnchorOnPath(t *testing.T) {
	path := []hierarchy.PathEntry{{NodeID: "D001"}, {NodeID: "G001"}}
	tests := []struct {
		name     string
		anchor   rule.Anchor
		employee string
		want     bool
	}{
		{"node", rule.Anchor{Kind: rule.AnchorNode, NodeID: "G001"}, "", true},
		{"node missing", rule.Anchor{Kind: rule.AnchorNode, NodeID: "X"}, "", false},
		{"empty node", rule.Anchor{Kind: rule.AnchorNode}, "", false},
		{"employee", rule.Anchor{Kind: rule.AnchorEmployee, EmployeeID: "E1"}, "E1", true},
		{"employee mismatch", rule.Anchor{Kind: rule.AnchorEmployee, EmployeeID: "E1"}, "E2", false},
		{"empty employee", rule.Anchor{Kind: rule.AnchorEmployee}, "", false},
		{"unknown", rule.Anchor{Kind: rule.AnchorKind("unknown")}, "", false},
	}
	for _, test := range tests {
		if got := resolve.AnchorOnPath(test.anchor, path, test.employee); got != test.want {
			t.Errorf("%s: AnchorOnPath() = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestPropertyPrecedenceMonotonicity(t *testing.T) {
	snapshot := loadHierarchy(t, "rwb.json")
	path, err := snapshot.Path("RWB", "BR001")
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(301))
	for iteration := 0; iteration < 1000; iteration++ {
		deeperIndex := random.Intn(len(path) - 1)
		shallowerIndex := deeperIndex + 1 + random.Intn(len(path)-deeperIndex-1)
		deeper := nodeRule(fmt.Sprintf("deep-%d", iteration), path[deeperIndex].NodeID)
		shallower := nodeRule(fmt.Sprintf("shallow-%d", iteration), path[shallowerIndex].NodeID)
		definitions := []*rule.RuleDefinition{deeper, shallower}
		if random.Intn(2) == 0 {
			definitions[0], definitions[1] = definitions[1], definitions[0]
		}
		result := resolve.Resolve(inputAt("BR001"), definitions, snapshot)
		if result.Winner == nil || result.Winner.Rule != deeper {
			t.Fatalf("iteration %d: deeper anchor did not win: %#v", iteration, result)
		}
	}
}

func TestPropertyDeterminism(t *testing.T) {
	random := rand.New(rand.NewSource(302))
	for iteration := 0; iteration < 1000; iteration++ {
		typeCount := 1 + random.Intn(8)
		types := make([]hierarchy.Type, typeCount)
		nodes := make([]hierarchy.Node, typeCount)
		for index := 0; index < typeCount; index++ {
			typeID := fmt.Sprintf("type-%d", index)
			nodeID := fmt.Sprintf("node-%d", index)
			types[index] = hierarchy.Type{TypeID: typeID, TypeName: fmt.Sprintf("Type %d", index), Order: index, Tenant: "T", Status: "ACTIVE"}
			nodes[index] = hierarchy.Node{NodeID: nodeID, Tenant: "T", TypeID: typeID, Name: nodeID, Status: "ACTIVE"}
			if index > 0 {
				nodes[index].ParentNodeID = nodes[index-1].NodeID
			}
		}
		snapshot, err := hierarchy.NewSnapshot(types, nodes)
		if err != nil {
			t.Fatal(err)
		}
		candidateCount := 1 + random.Intn(10)
		definitions := make([]*rule.RuleDefinition, candidateCount)
		for index := range definitions {
			definitions[index] = nodeRuleForTenant(fmt.Sprintf("random-%d-%d", iteration, index), nodes[random.Intn(len(nodes))].NodeID, "T")
		}
		input := inputAtTenant(nodes[len(nodes)-1].NodeID, "T")
		first := resolve.Resolve(input, definitions, snapshot)
		random.Shuffle(len(definitions), func(left, right int) { definitions[left], definitions[right] = definitions[right], definitions[left] })
		second := resolve.Resolve(input, definitions, snapshot)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("iteration %d produced different results\nfirst=%#v\nsecond=%#v", iteration, first, second)
		}
	}
}

func TestPropertyNoPanic(t *testing.T) {
	random := rand.New(rand.NewSource(303))
	snapshot := loadHierarchy(t, "rwb.json")
	for iteration := 0; iteration < 1000; iteration++ {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("iteration %d panicked: %v", iteration, recovered)
				}
			}()
			input := inputAt([]string{"", "missing", "BR001", "BR003"}[random.Intn(4)])
			input.EmployeeID = []string{"", "E1", "E9001"}[random.Intn(3)]
			definitions := make([]*rule.RuleDefinition, random.Intn(12))
			for index := range definitions {
				if random.Intn(5) == 0 {
					continue
				}
				definition := nodeRule(fmt.Sprintf("malformed-%d-%d", iteration, index), []string{"", "missing", "G001", "D002", "BR003"}[random.Intn(5)])
				if random.Intn(4) == 0 {
					definition.Scope.Anchor.Kind = rule.AnchorKind("bad")
				}
				if random.Intn(4) == 0 {
					definition.Scope.Filters = []rule.FilterCondition{{Attribute: "x", Op: rule.FilterOperator("bad"), Value: map[string]any{"bad": true}}}
				}
				definitions[index] = definition
			}
			candidateSnapshot := snapshot
			if random.Intn(10) == 0 {
				candidateSnapshot = nil
			}
			_ = resolve.Resolve(input, definitions, candidateSnapshot)
		}()
	}
}

// BenchmarkResolve covers the candidate-set sizes in the performance budget:
// one rule, a typical rule set, and a large one.
func BenchmarkResolve(b *testing.B) {
	snapshot, err := ruletest.LoadRWB()
	if err != nil {
		b.Fatal(err)
	}
	anchors := []string{"G001", "C002", "D002", "BR001"}
	for _, size := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("candidates=%d", size), func(b *testing.B) {
			definitions := make([]*rule.RuleDefinition, size)
			for index := range definitions {
				definitions[index] = nodeRule(fmt.Sprintf("benchmark-%03d", index), anchors[index%len(anchors)])
				priority := index + 1
				definitions[index].TieBreakPriority = &priority
			}
			input := inputAt("BR001")
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				benchmarkResult = resolve.Resolve(input, definitions, snapshot)
			}
		})
	}
}

var benchmarkResult resolve.Result

func loadHierarchy(t testing.TB, name string) *hierarchy.Snapshot {
	t.Helper()
	snapshot, err := ruletest.LoadHierarchy(name)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func inputAt(nodeID string) resolve.Input { return inputAtTenant(nodeID, "RWB") }

func inputAtTenant(nodeID, tenant string) resolve.Input {
	return resolve.Input{Tenant: tenant, NodeID: nodeID, EmployeeID: "E9001", ValidAt: phase3ValidAt}
}

func nodeRule(name, nodeID string) *rule.RuleDefinition {
	return nodeRuleForTenant(name, nodeID, "RWB")
}

func nodeRuleForTenant(name, nodeID, tenant string) *rule.RuleDefinition {
	return &rule.RuleDefinition{
		RuleKey: rule.RuleKey(fmt.Sprintf("urn:arrowhr:rule:test:resolution:%s:v1", name)),
		Domain:  "test", RuleSet: "resolution", Name: name, Version: "1.0.0",
		Tenant: tenant, Status: rule.StatusActive,
		Effective: rule.Effective{ValidFrom: phase3ValidAt.Add(-time.Hour)},
		Scope:     rule.Scope{Anchor: rule.Anchor{Kind: rule.AnchorNode, NodeID: nodeID}},
	}
}

func employeeRule(name, employeeID string) *rule.RuleDefinition {
	definition := nodeRule(name, "")
	definition.Scope.Anchor = rule.Anchor{Kind: rule.AnchorEmployee, EmployeeID: employeeID, Reason: "approved exception"}
	return definition
}

func assertWinner(t testing.TB, result resolve.Result, want *rule.RuleDefinition) {
	t.Helper()
	if result.FailClosed || result.Tie != nil || result.Winner == nil || result.Winner.Rule != want || !result.Winner.Winner || result.ReasonCode != "OK" {
		t.Fatalf("unexpected winner result: %#v", result)
	}
}

func assertFailed(t testing.TB, result resolve.Result, reason string) {
	t.Helper()
	if !result.FailClosed || result.Winner != nil || result.ReasonCode != reason {
		t.Fatalf("unexpected failed result: %#v", result)
	}
}

func assertTie(t testing.TB, result resolve.Result, definitions ...*rule.RuleDefinition) {
	t.Helper()
	if !result.FailClosed || result.Winner != nil || result.Tie == nil || result.ReasonCode != "PRECEDENCE_TIE_UNRESOLVED" {
		t.Fatalf("unexpected tie result: %#v", result)
	}
	if len(result.Tie.Candidates) != len(definitions) {
		t.Fatalf("tie candidates = %#v", result.Tie.Candidates)
	}
	want := make(map[rule.RuleKey]bool, len(definitions))
	for _, definition := range definitions {
		want[definition.RuleKey] = true
	}
	for _, candidate := range result.Tie.Candidates {
		if !want[candidate.Key] {
			t.Fatalf("unexpected tied candidate %#v", candidate)
		}
	}
}

func candidateFor(t testing.TB, result resolve.Result, definition *rule.RuleDefinition) resolve.Candidate {
	t.Helper()
	for _, candidate := range result.Candidates {
		if candidate.Rule == definition {
			return candidate
		}
	}
	t.Fatalf("candidate %q not found", definition.Name)
	return resolve.Candidate{}
}
