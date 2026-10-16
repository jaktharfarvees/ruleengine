package ruletest_test

import (
	"strings"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/engine/decisiontable"
	"github.com/arrowhr/ruleengine/engine/testengine"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
)

// recorder captures what an assertion reported, so a test can check the test
// kit itself: a helper that passes everything is worse than no helper.
type recorder struct {
	testing.TB
	failed   bool
	messages []string
}

func (r *recorder) Error(args ...any) {
	r.failed = true
	for _, arg := range args {
		if text, ok := arg.(string); ok {
			r.messages = append(r.messages, text)
		}
	}
}

func (r *recorder) Errorf(format string, args ...any) {
	r.failed = true
	r.messages = append(r.messages, format)
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.failed = true
	r.messages = append(r.messages, format)
}

func (r *recorder) said(substring string) bool {
	for _, message := range r.messages {
		if strings.Contains(message, substring) {
			return true
		}
	}
	return false
}

func TestFakeClockMovesOnlyWhenTold(t *testing.T) {
	start := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	clock := ruletest.NewFakeClock(start)
	if !clock.Now().Equal(start) {
		t.Fatalf("Now = %s, want %s", clock.Now(), start)
	}
	if got := clock.Advance(90 * time.Second); !got.Equal(start.Add(90 * time.Second)) {
		t.Fatalf("Advance = %s", got)
	}
	if !clock.Now().Equal(start.Add(90 * time.Second)) {
		t.Fatal("Advance did not move the clock")
	}
	// Backwards too, which a retroactive-run test occasionally needs.
	clock.Advance(-90 * time.Second)
	if !clock.Now().Equal(start) {
		t.Fatal("a negative step did not move the clock back")
	}
	if got := clock.Set(start.AddDate(1, 0, 0)); !got.Equal(start.AddDate(1, 0, 0)) {
		t.Fatalf("Set = %s", got)
	}
	// A zero start is a readable date rather than the epoch, so a failure
	// message shows a time a reader recognises.
	if ruletest.NewFakeClock(time.Time{}).Now().Year() != 2026 {
		t.Fatal("a zero start did not get a readable default")
	}
}

func TestHierarchyBuilderBuildsAValidatedTree(t *testing.T) {
	snapshot := ruletest.NewHierarchyBuilder("T1").
		Level("group", "Group", 0).
		Level("company", "Company", 1).
		Level("branch", "Branch", 2).
		Root("G1", "group", "Group").Declares("Canada", "CAD", "en-CA", "America/Toronto").
		Node("C1", "company", "Company", "G1").
		Node("B1", "branch", "Branch", "C1").Declares("", "", "", "Asia/Colombo").
		Node("B2", "branch", "Closed branch", "C1").Inactive().
		MustSnapshot(t)

	path, err := snapshot.Path("T1", "B1")
	if err != nil || len(path) != 3 {
		t.Fatalf("path = %+v, %v; want three nodes root first", path, err)
	}
	if path[0].NodeID != "G1" || path[2].NodeID != "B1" {
		t.Fatalf("path is not root first: %+v", path)
	}
	// Each attribute resolves from the nearest ancestor that declares it,
	// independently: the branch declares a timezone, the group a country.
	country, source, ok := snapshot.ResolveAttribute("T1", "B1", "country")
	if !ok || country != "Canada" || source != "G1" {
		t.Fatalf("country = %q from %q, %t", country, source, ok)
	}
	timezone, source, ok := snapshot.ResolveAttribute("T1", "B1", "timezone")
	if !ok || timezone != "Asia/Colombo" || source != "B1" {
		t.Fatalf("timezone = %q from %q, %t", timezone, source, ok)
	}
	if node, _ := snapshot.Node("T1", "B2"); node.Status != "INACTIVE" {
		t.Fatalf("Inactive did not mark the last node: %+v", node)
	}

	// An invalid tree is reported rather than built.
	if _, err := ruletest.NewHierarchyBuilder("T1").
		Level("group", "Group", 0).
		Node("C1", "group", "Orphan", "missing").
		Snapshot(); err == nil {
		t.Fatal("a node with a missing parent was accepted")
	}
}

func TestFixtureBuilderAssemblesALoadableBundle(t *testing.T) {
	bundle := ruletest.NewFixtureBuilder("RWB").
		WithRWBHierarchy().
		WithRegistry("leave").
		WithManifests("leave.balances").
		Rule("leave", "eligibility", "min-service").
		AnchoredAt("G001").
		Needs("serviceMonths").Produces("eligible", "reasonCode").
		Row("eligible", map[string]string{"serviceMonths": ">= 6"},
			map[string]any{"eligible": true, "reasonCode": "ELIGIBLE"}).
		Row("not-yet", map[string]string{},
			map[string]any{"eligible": false, "reasonCode": "MIN_SERVICE_NOT_MET"}).
		Add().
		MustBuild(t)

	if len(bundle.Rules) != 1 || len(bundle.Fields) == 0 || len(bundle.Manifests) != 1 {
		t.Fatalf("bundle = %d rules, %d fields, %d manifests",
			len(bundle.Rules), len(bundle.Fields), len(bundle.Manifests))
	}
	if len(bundle.Hierarchy.Nodes) != 13 {
		t.Fatalf("hierarchy has %d nodes, want the RWB fixture's 13", len(bundle.Hierarchy.Nodes))
	}
	// The defaults it filled in have to make a rule that actually validates,
	// or the builder has only moved the problem.
	if err := bundle.Rules[0].Validate(); err != nil {
		t.Fatalf("the built rule is invalid: %v", err)
	}
	definition := bundle.Rules[0]
	if definition.Version != "1.0.0" || definition.Status != rule.StatusActive {
		t.Fatalf("defaults = %q, %q", definition.Version, definition.Status)
	}
	if definition.RuleKey != "urn:arrowhr:rule:leave:eligibility:min-service:v1" {
		t.Fatalf("key = %q", definition.RuleKey)
	}
}

func TestFixtureBuilderReportsAMistakeWhereItWasMade(t *testing.T) {
	// A rule with no anchor is the mistake the builder exists to catch early.
	if _, err := ruletest.NewFixtureBuilder("T1").
		Rule("leave", "eligibility", "min-service").
		Needs("serviceMonths").Produces("eligible").
		Row("r", map[string]string{}, map[string]any{"eligible": true}).
		Add().
		Build(); err == nil || !strings.Contains(err.Error(), "anchor") {
		t.Fatalf("a rule with no anchor was accepted: %v", err)
	}

	// An output no row produces is caught by the rule's own validation.
	if _, err := ruletest.NewFixtureBuilder("T1").
		Rule("leave", "eligibility", "min-service").
		AnchoredAt("G001").
		Produces("eligible").
		Row("r", map[string]string{}, map[string]any{"undeclared": true}).
		Add().
		Build(); err == nil {
		t.Fatal("a row producing an undeclared output was accepted")
	}

	// A named fixture that does not exist is reported, not ignored.
	if _, err := ruletest.NewFixtureBuilder("T1").WithManifests("no.such.dataset").Build(); err == nil {
		t.Fatal("an unknown manifest was accepted")
	}
	if _, err := ruletest.NewFixtureBuilder("T1").WithRegistry("nosuchdomain").Build(); err == nil {
		t.Fatal("an unknown registry domain was accepted")
	}
}

func TestFixtureBuilderSupportsEveryScopeShape(t *testing.T) {
	june := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	bundle := ruletest.NewFixtureBuilder("RWB").
		WithRWBHierarchy().
		AddPassedField("requestedDays", registry.DataDecimal).
		AddOutputField("allowed", registry.DataBoolean).
		AddSubjectAttribute("employeeGroup", "Finance", "Development team").
		Rule("leave", "validation", "filtered").
		AnchoredAt("D002").
		FilteredBy("employeeGroup", rule.FilterIn, []any{"Development team"}).
		EffectiveFrom(june).EffectiveUntil(june.AddDate(1, 0, 0)).
		TieBreak(10).Version("2.1.0").Status(rule.StatusScheduled).
		DecisionType("leave-validation").HitPolicy(rule.HitPriority).
		Needs("requestedDays").Produces("allowed").
		Row("within", map[string]string{"requestedDays": "<= 1"}, map[string]any{"allowed": true}).
		Add().
		Rule("leave", "validation", "exception").
		ForEmployee("E9001", "approved by the CFO").
		Needs("requestedDays").Produces("allowed").
		Row("always", map[string]string{}, map[string]any{"allowed": true}).
		Add().
		MustBuild(t)

	if len(bundle.Rules) != 2 {
		t.Fatalf("bundle has %d rules", len(bundle.Rules))
	}
	filtered := bundle.Rules[0]
	if filtered.Version != "2.1.0" || filtered.Status != rule.StatusScheduled {
		t.Fatalf("filtered rule = %q, %q", filtered.Version, filtered.Status)
	}
	if len(filtered.Scope.Filters) != 1 || filtered.Scope.Filters[0].Attribute != "employeeGroup" {
		t.Fatalf("filters = %+v", filtered.Scope.Filters)
	}
	if filtered.TieBreakPriority == nil || *filtered.TieBreakPriority != 10 {
		t.Fatalf("tie-break = %v", filtered.TieBreakPriority)
	}
	if filtered.Effective.ValidTo == nil {
		t.Fatal("EffectiveUntil was not applied")
	}
	exception := bundle.Rules[1]
	if exception.Scope.Anchor.Kind != rule.AnchorEmployee || exception.Scope.Anchor.Reason == "" {
		t.Fatalf("employee anchor = %+v", exception.Scope.Anchor)
	}
}

func TestRunGoldenRunsTheCommittedCases(t *testing.T) {
	// The engine golden cases exercise arithmetic and every hit policy, so
	// running them on both engines also proves the skip path works.
	ruletest.RunGoldenOn(t, "../testdata/golden/engine", testengine.New(), decisiontable.New())
	ruletest.RunGolden(t, "../testdata/golden/leave")
}

func TestRunGoldenReportsItsOwnProblems(t *testing.T) {
	// An empty or wrong directory is almost always a wrong path rather than a
	// rule nobody has examples for, so it fails rather than passing silently.
	recorded := &recorder{TB: t}
	ruletest.RunGolden(recorded, t.TempDir())
	if !recorded.failed {
		t.Fatal("an empty golden directory passed")
	}

	recorded = &recorder{TB: t}
	ruletest.RunGolden(recorded, "../testdata/no-such-directory")
	if !recorded.failed {
		t.Fatal("a missing golden directory passed")
	}

	recorded = &recorder{TB: t}
	ruletest.RunGoldenOn(recorded, "../testdata/golden/leave")
	if !recorded.failed || !recorded.said("engine") {
		t.Fatalf("RunGoldenOn with no engine passed: %v", recorded.messages)
	}
}

func TestLoadGoldenReadsEveryCase(t *testing.T) {
	cases, err := ruletest.LoadGolden("../testdata/golden")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 8 {
		t.Fatalf("loaded %d golden cases, want every committed one", len(cases))
	}
	for _, golden := range cases {
		if golden.Name == "" {
			t.Fatal("a case has no name, so a failure could not be attributed")
		}
		if len(golden.Logic.Rows) == 0 {
			t.Fatalf("case %q has no rows", golden.Name)
		}
	}
	if _, err := ruletest.LoadGolden("../testdata/no-such-directory"); err == nil {
		t.Fatal("a missing directory was loaded")
	}
}

func TestDecisionEqualsReportsEveryDifference(t *testing.T) {
	precedence := hierarchy.DerivedPrecedence{NodeID: "C002", TypeName: "Company", Order: 1, Depth: 1}
	decision := ruleengine.Decision{
		RuleKey: "urn:arrowhr:rule:leave:eligibility:min-service:v1", RuleVersion: "1.1.0",
		ResolvedAt:  &precedence,
		ReasonCodes: []ruleengine.ReasonCode{ruleengine.ReasonOK},
		Result:      map[string]any{"eligible": true, "reasonCode": "ELIGIBLE"},
	}

	// Matching expectations pass, including a partial result.
	passing := &recorder{TB: t}
	ruletest.DecisionEquals(passing, decision, ruletest.Expectation{
		RuleVersion: "1.1.0", ResolvedNodeID: "C002",
		ReasonCodes: []ruleengine.ReasonCode{ruleengine.ReasonOK},
		Result:      map[string]any{"eligible": true},
	})
	if passing.failed {
		t.Fatalf("a matching expectation failed: %v", passing.messages)
	}

	// Every difference is reported at once rather than the first.
	failing := &recorder{TB: t}
	ruletest.DecisionEquals(failing, decision, ruletest.Expectation{
		Failed: true, RuleVersion: "1.0.0", ResolvedNodeID: "G001",
		EmployeeException: true,
		ReasonCodes:       []ruleengine.ReasonCode{ruleengine.ReasonNoRuleResolved},
		Result:            map[string]any{"eligible": false, "missing": 1},
	})
	if !failing.failed {
		t.Fatal("a mismatched expectation passed")
	}
	report := strings.Join(failing.messages, "\n")
	if !strings.Contains(report, "does not match") {
		t.Fatalf("report = %q", report)
	}
}

func TestAssertHelpers(t *testing.T) {
	passing := ruleengine.Decision{ReasonCodes: []ruleengine.ReasonCode{ruleengine.ReasonOK}}
	failing := ruleengine.Decision{
		Failed:      true,
		ReasonCodes: []ruleengine.ReasonCode{ruleengine.ReasonNoRuleResolved},
	}

	quiet := &recorder{TB: t}
	ruletest.AssertPassed(quiet, passing)
	ruletest.AssertFailed(quiet, failing, ruleengine.ReasonNoRuleResolved)
	if quiet.failed {
		t.Fatalf("correct assertions reported a problem: %v", quiet.messages)
	}

	loud := &recorder{TB: t}
	ruletest.AssertPassed(loud, failing)
	if !loud.failed {
		t.Fatal("AssertPassed accepted a failed decision")
	}

	loud = &recorder{TB: t}
	ruletest.AssertFailed(loud, passing, ruleengine.ReasonNoRuleResolved)
	if !loud.failed {
		t.Fatal("AssertFailed accepted a passing decision")
	}

	// Failing with the wrong code is still a failure, and says which it got.
	loud = &recorder{TB: t}
	ruletest.AssertFailed(loud, failing, ruleengine.ReasonPrecedenceTieUnresolved)
	if !loud.failed {
		t.Fatal("AssertFailed accepted the wrong reason code")
	}

	precedence := hierarchy.DerivedPrecedence{NodeID: "C002"}
	resolved := ruleengine.Decision{ResolvedAt: &precedence}
	quiet = &recorder{TB: t}
	ruletest.AssertResolvedAt(quiet, resolved, "C002")
	if quiet.failed {
		t.Fatalf("AssertResolvedAt rejected the right node: %v", quiet.messages)
	}
	loud = &recorder{TB: t}
	ruletest.AssertResolvedAt(loud, resolved, "G001")
	ruletest.AssertResolvedAt(loud, ruleengine.Decision{}, "G001")
	if !loud.failed {
		t.Fatal("AssertResolvedAt accepted the wrong node")
	}
}

// TestExplainNamesTheThingsAPersonAsksFor pins the report a failing test or a
// support session actually reads.
func TestExplainNamesTheThingsAPersonAsksFor(t *testing.T) {
	precedence := hierarchy.DerivedPrecedence{NodeID: "C002", TypeName: "Company", Order: 1, Depth: 1}
	report := ruletest.Explain(ruleengine.Decision{
		RuleKey: "urn:arrowhr:rule:leave:eligibility:min-service:v1", RuleVersion: "1.1.0",
		ResolvedAt: &precedence,
		SubjectPath: []hierarchy.PathEntry{
			{NodeID: "BR001"}, {NodeID: "D002"}, {NodeID: "C002"}, {NodeID: "G001"},
		},
		ReasonCodes: []ruleengine.ReasonCode{ruleengine.ReasonOK},
		Result:      map[string]any{"eligible": true},
		Evaluated: []ruleengine.Evaluated{
			{RuleKey: "urn:arrowhr:rule:leave:eligibility:min-service:v1", RuleVersion: "1.1.0",
				Anchor: rule.Anchor{Kind: rule.AnchorNode, NodeID: "C002"}, Winner: true},
			{RuleKey: "urn:arrowhr:rule:leave:eligibility:min-service:v1", RuleVersion: "1.0.0",
				Anchor: rule.Anchor{Kind: rule.AnchorNode, NodeID: "G001"},
				Reason: "Company(1) outranks Group(0)"},
		},
	})
	for _, want := range []string{
		"1.1.0", "C002", "BR001 -> D002 -> C002 -> G001", "OK", "eligible=true",
		"WINNER", "eliminated", "Company(1) outranks Group(0)",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q:\n%s", want, report)
		}
	}

	// A decision that resolved nothing still explains itself.
	empty := ruletest.Explain(ruleengine.Decision{
		ReasonCodes: []ruleengine.ReasonCode{ruleengine.ReasonNoRuleResolved},
	})
	if !strings.Contains(empty, "(none)") || !strings.Contains(empty, "NO_RULE_RESOLVED") {
		t.Fatalf("an unresolved decision reported:\n%s", empty)
	}
}
