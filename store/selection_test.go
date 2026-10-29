package store

import (
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/resolve"
	"github.com/arrowhr/ruleengine/rule"
)

// versionsOf renders a selection for a readable failure message.
func versionsOf(definitions []*rule.RuleDefinition) string {
	parts := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		anchor := definition.Scope.Anchor.NodeID
		parts = append(parts, definition.Version+"@"+anchor)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// anchoredRule builds one authored rule in a single family, so that a test can
// vary only the version, the anchor and the filters.
func anchoredRule(version, nodeID string, validFrom time.Time, filters ...rule.FilterCondition) rule.RuleDefinition {
	definition := testRule("alpha", version, validFrom)
	definition.Scope = rule.Scope{
		Anchor:  rule.Anchor{Kind: rule.AnchorNode, NodeID: nodeID},
		Filters: filters,
	}
	return definition
}

// twoLevelHierarchy is a group with one company beneath it, so a test can put
// two rules at different depths.
func twoLevelHierarchy() HierarchyBundle {
	return HierarchyBundle{
		Types: []hierarchy.Type{
			{TypeID: "group", TypeName: "Group", Order: 0, Tenant: "T1", Status: "ACTIVE"},
			{TypeID: "company", TypeName: "Company", Order: 1, Tenant: "T1", Status: "ACTIVE"},
		},
		Nodes: []hierarchy.Node{
			{NodeID: "G001", Code: "G001", Tenant: "T1", TypeID: "group", Name: "Group", Status: "ACTIVE"},
			{NodeID: "C002", Code: "C002", Tenant: "T1", TypeID: "company", Name: "Company", ParentNodeID: "G001", Status: "ACTIVE"},
		},
	}
}

func selectionCache(t *testing.T, rules ...rule.RuleDefinition) *Cache {
	t.Helper()
	cache := NewCache()
	if err := cache.Replace(Bundle{Rules: rules, Hierarchy: twoLevelHierarchy(), LoadedAt: january}); err != nil {
		t.Fatal(err)
	}
	return cache
}

// TestVersionBumpDoesNotBecomeATie is the regression test for the defect this
// package existed to avoid: publishing a new version of a rule while the old
// immutable version keeps no validTo left both effective, resolution saw two
// candidates at one anchor, and every decision for that rule failed closed
// with PRECEDENCE_TIE_UNRESOLVED.
func TestVersionBumpDoesNotBecomeATie(t *testing.T) {
	june := january.Add(150 * 24 * time.Hour)
	at := january.Add(200 * 24 * time.Hour)
	cache := selectionCache(t,
		anchoredRule("1.0.0", "G001", january),
		anchoredRule("1.1.0", "G001", june),
	)

	candidates := cache.Candidates("T1", "leave", "eligibility", "alpha", at)
	if len(candidates) != 1 || candidates[0].Version != "1.1.0" {
		t.Fatalf("Candidates = %s, want only the latest 1.1.0", versionsOf(candidates))
	}

	result := resolve.Resolve(resolve.Input{
		Tenant: "T1", NodeID: "C002", EmployeeID: "E1", ValidAt: at,
	}, candidates, cache.Hierarchy())
	if result.Winner == nil || result.FailClosed || result.Tie != nil {
		t.Fatalf("resolution failed closed on a version bump: reason=%s tie=%v", result.ReasonCode, result.Tie)
	}
	if result.Winner.Rule.Version != "1.1.0" {
		t.Fatalf("winner = %s, want 1.1.0", result.Winner.Rule.Version)
	}
}

// TestLatestVersionIsPerRuleNotPerFamily pins that collapsing versions does
// not collapse distinct rules. Two anchors are two rules whose depth
// resolution must still compare, each at its own latest version.
func TestLatestVersionIsPerRuleNotPerFamily(t *testing.T) {
	june := january.Add(150 * 24 * time.Hour)
	at := january.Add(200 * 24 * time.Hour)
	cache := selectionCache(t,
		anchoredRule("1.0.0", "G001", january),
		anchoredRule("1.2.0", "G001", june),
		anchoredRule("1.1.0", "C002", january),
	)

	candidates := cache.Candidates("T1", "leave", "eligibility", "alpha", at)
	if len(candidates) != 2 {
		t.Fatalf("Candidates = %s, want one per anchor", versionsOf(candidates))
	}
	seen := map[string]string{}
	for _, candidate := range candidates {
		seen[candidate.Scope.Anchor.NodeID] = candidate.Version
	}
	if seen["G001"] != "1.2.0" || seen["C002"] != "1.1.0" {
		t.Fatalf("Candidates = %s, want 1.2.0@G001 and 1.1.0@C002", versionsOf(candidates))
	}

	// The deeper anchor still wins, at its own version.
	result := resolve.Resolve(resolve.Input{
		Tenant: "T1", NodeID: "C002", EmployeeID: "E1", ValidAt: at,
	}, candidates, cache.Hierarchy())
	if result.Winner == nil || result.Winner.Rule.Scope.Anchor.NodeID != "C002" {
		t.Fatalf("winner = %v, want the C002 rule", result.Winner)
	}
}

// TestFiltersMakeADistinctRule pins that two rules at one anchor separated
// only by their filters stay two rules, each with its own version lineage.
// Collapsing them would silently drop a policy.
func TestFiltersMakeADistinctRule(t *testing.T) {
	june := january.Add(150 * 24 * time.Hour)
	at := january.Add(200 * 24 * time.Hour)
	finance := rule.FilterCondition{Attribute: "employeeGroup", Op: rule.FilterEquals, Value: "Finance"}
	permanent := rule.FilterCondition{Attribute: "workerCategory", Op: rule.FilterEquals, Value: "PERMANENT"}

	cache := selectionCache(t,
		anchoredRule("1.0.0", "G001", january, finance),
		anchoredRule("1.1.0", "G001", june, finance),
		anchoredRule("1.0.0", "G001", january, permanent),
	)
	candidates := cache.Candidates("T1", "leave", "eligibility", "alpha", at)
	if len(candidates) != 2 {
		t.Fatalf("Candidates = %s, want the latest of each filtered rule", versionsOf(candidates))
	}
	byFilter := map[string]string{}
	for _, candidate := range candidates {
		byFilter[candidate.Scope.Filters[0].Attribute] = candidate.Version
	}
	if byFilter["employeeGroup"] != "1.1.0" || byFilter["workerCategory"] != "1.0.0" {
		t.Fatalf("Candidates = %s, want employeeGroup at 1.1.0 and workerCategory at 1.0.0", versionsOf(candidates))
	}
}

// TestFilterOrderIsNotRuleIdentity pins that reordering a filter list does not
// create a second rule. Authored order says nothing about which subjects a
// rule covers, so two orderings of one filter set are one rule and the newer
// version must win rather than tie with the older.
func TestFilterOrderIsNotRuleIdentity(t *testing.T) {
	june := january.Add(150 * 24 * time.Hour)
	at := january.Add(200 * 24 * time.Hour)
	finance := rule.FilterCondition{Attribute: "employeeGroup", Op: rule.FilterEquals, Value: "Finance"}
	permanent := rule.FilterCondition{Attribute: "workerCategory", Op: rule.FilterEquals, Value: "PERMANENT"}

	cache := selectionCache(t,
		anchoredRule("1.0.0", "G001", january, finance, permanent),
		anchoredRule("1.1.0", "G001", june, permanent, finance),
	)
	candidates := cache.Candidates("T1", "leave", "eligibility", "alpha", at)
	if len(candidates) != 1 || candidates[0].Version != "1.1.0" {
		t.Fatalf("Candidates = %s, want only 1.1.0", versionsOf(candidates))
	}
	// The same identity makes two definitions at one version a duplicate.
	cache = NewCache()
	err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{
		anchoredRule("1.0.0", "G001", january, finance, permanent),
		anchoredRule("1.0.0", "G001", january, permanent, finance),
	}, Hierarchy: twoLevelHierarchy(), LoadedAt: january})
	if err == nil || !strings.Contains(err.Error(), "duplicate rule variant") {
		t.Fatalf("reordered duplicate accepted: %v", err)
	}
}

// TestSupersededVersionStillServesAnOlderBusinessTime pins the retroactive
// case: an August run must resolve August's version even after September's is
// published, which is why collapsing happens at the requested time rather than
// at load.
func TestSupersededVersionStillServesAnOlderBusinessTime(t *testing.T) {
	august := january.Add(210 * 24 * time.Hour)
	september := january.Add(240 * 24 * time.Hour)
	cache := selectionCache(t,
		anchoredRule("1.0.0", "G001", january),
		anchoredRule("2.0.0", "G001", september),
	)
	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{"during August", august, "1.0.0"},
		{"after September", september.Add(24 * time.Hour), "2.0.0"},
		{"on the day September takes effect", september, "2.0.0"},
		{"the instant before", september.Add(-time.Nanosecond), "1.0.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidates := cache.Candidates("T1", "leave", "eligibility", "alpha", test.at)
			if len(candidates) != 1 || candidates[0].Version != test.want {
				t.Fatalf("Candidates = %s, want %s", versionsOf(candidates), test.want)
			}
		})
	}
}

// TestLatestEffectiveSkipsANewerIneffectiveVersion pins that "latest" means
// latest among the versions effective at the requested time, not latest
// overall: a version scheduled for next month must not shadow the one in force.
func TestLatestEffectiveSkipsANewerIneffectiveVersion(t *testing.T) {
	future := january.Add(300 * 24 * time.Hour)
	at := january.Add(100 * 24 * time.Hour)
	cache := selectionCache(t,
		anchoredRule("1.0.0", "G001", january),
		anchoredRule("2.0.0", "G001", future),
	)
	candidates := cache.Candidates("T1", "leave", "eligibility", "alpha", at)
	if len(candidates) != 1 || candidates[0].Version != "1.0.0" {
		t.Fatalf("Candidates = %s, want 1.0.0; a scheduled version must not shadow the one in force", versionsOf(candidates))
	}
}

// TestCollapseOrdersVersionsSemantically pins that 1.10.0 is newer than
// 1.9.0. A string comparison puts 1.9.0 first and would select a superseded
// rule from the tenth minor release onwards.
func TestCollapseOrdersVersionsSemantically(t *testing.T) {
	at := january.Add(24 * time.Hour)
	cache := selectionCache(t,
		anchoredRule("1.9.0", "G001", january),
		anchoredRule("1.10.0", "G001", january),
	)
	candidates := cache.Candidates("T1", "leave", "eligibility", "alpha", at)
	if len(candidates) != 1 || candidates[0].Version != "1.10.0" {
		t.Fatalf("Candidates = %s, want 1.10.0", versionsOf(candidates))
	}
	// The same ordering must hold for the family index.
	versions := cache.Versions("T1", candidates[0].RuleKey)
	if len(versions) != 2 || versions[0].Version != "1.10.0" {
		t.Fatalf("Versions = %s, want 1.10.0 first", versionsOf(versions))
	}
}

// TestPinnedVersionMustBeEvaluable pins that a pin names a version, not
// permission to use it. An unapproved draft must never decide an outcome,
// however it was reached.
func TestPinnedVersionMustBeEvaluable(t *testing.T) {
	at := january.Add(24 * time.Hour)
	for _, status := range []rule.Status{
		rule.StatusDraft, rule.StatusPendingApproval, rule.StatusDeprecated, rule.StatusRetired,
	} {
		t.Run(string(status), func(t *testing.T) {
			definition := anchoredRule("2.0.0", "G001", january)
			definition.Status = status
			cache := selectionCache(t, anchoredRule("1.0.0", "G001", january), definition)

			_, err := NewVersionResolver(cache, nil).Resolve(VersionRequest{
				Tenant: "T1", RuleKey: definition.RuleKey, ExplicitPin: "2.0.0", EffectiveAt: at,
			})
			if err == nil {
				t.Fatalf("a %s version was selected by an explicit pin", status)
			}
			if !strings.Contains(err.Error(), "pinned version unavailable") {
				t.Fatalf("error = %v, want a pinned-version failure", err)
			}
			// The evaluable version is still reachable, so the refusal is
			// about the pinned version and not about the rule.
			selected, err := NewVersionResolver(cache, nil).Resolve(VersionRequest{
				Tenant: "T1", RuleKey: definition.RuleKey, EffectiveAt: at,
			})
			if err != nil || len(selected) != 1 || selected[0].Version != "1.0.0" {
				t.Fatalf("unpinned resolution = %s, %v; want 1.0.0", versionsOf(selected), err)
			}
		})
	}
}

// TestScheduledStatusIsSelectable pins the other side of the status rule:
// "scheduled" is evaluable within its effective interval, so a rule published
// ahead of time starts applying on its own date without a deployment.
func TestScheduledStatusIsSelectable(t *testing.T) {
	future := january.Add(300 * 24 * time.Hour)
	definition := anchoredRule("2.0.0", "G001", future)
	definition.Status = rule.StatusScheduled
	cache := selectionCache(t, anchoredRule("1.0.0", "G001", january), definition)

	before := cache.Candidates("T1", "leave", "eligibility", "alpha", january.Add(24*time.Hour))
	if len(before) != 1 || before[0].Version != "1.0.0" {
		t.Fatalf("before the date: %s, want 1.0.0", versionsOf(before))
	}
	after := cache.Candidates("T1", "leave", "eligibility", "alpha", future.Add(24*time.Hour))
	if len(after) != 1 || after[0].Version != "2.0.0" {
		t.Fatalf("after the date: %s, want 2.0.0", versionsOf(after))
	}
}

// TestCandidatesDoNotShareSnapshotState pins that a caller cannot reach into
// the published snapshot through a returned slice: resolution and a service's
// own code both receive their own slice.
func TestCandidatesDoNotShareSnapshotState(t *testing.T) {
	at := january.Add(24 * time.Hour)
	cache := selectionCache(t,
		anchoredRule("1.0.0", "G001", january),
		anchoredRule("1.0.0", "C002", january),
	)
	first := cache.Candidates("T1", "leave", "eligibility", "alpha", at)
	if len(first) != 2 {
		t.Fatalf("Candidates = %s", versionsOf(first))
	}
	first[0] = nil
	second := cache.Candidates("T1", "leave", "eligibility", "alpha", at)
	if len(second) != 2 || second[0] == nil {
		t.Fatal("Candidates handed out the snapshot's own slice")
	}
	versions := cache.Versions("T1", second[0].RuleKey)
	versions[0] = nil
	if again := cache.Versions("T1", second[0].RuleKey); again[0] == nil {
		t.Fatal("Versions handed out the snapshot's own slice")
	}
	exact := cache.Version("T1", second[0].RuleKey, "1.0.0")
	exact[0] = nil
	if again := cache.Version("T1", second[0].RuleKey, "1.0.0"); again[0] == nil {
		t.Fatal("Version handed out the snapshot's own slice")
	}
}

// TestManifestsEnumerationFeedsAFactResolver pins that a service can build its
// fact resolver from the cache. The resolver takes the whole manifest set, so
// a cache that could only be asked for one dataset at a time could not supply
// it.
func TestManifestsEnumerationFeedsAFactResolver(t *testing.T) {
	cache := NewCache()
	if err := cache.Replace(Bundle{
		Rules:     []rule.RuleDefinition{anchoredRule("1.0.0", "G001", january)},
		Manifests: []dataset.Manifest{testManifest("leave.items"), testManifest("attendance.items")},
		Hierarchy: twoLevelHierarchy(),
		LoadedAt:  january,
	}); err != nil {
		t.Fatal(err)
	}
	manifests := cache.Manifests()
	if len(manifests) != 2 {
		t.Fatalf("Manifests() returned %d, want 2", len(manifests))
	}
	for name, manifest := range manifests {
		if manifest.DatasetID != name {
			t.Fatalf("manifest keyed %q declares %q", name, manifest.DatasetID)
		}
	}
	// The copy must be independent: a resolver holding the map must not be
	// able to alter what the next reader sees.
	manifests["leave.items"].Columns[0].Name = "mutated"
	delete(manifests, "attendance.items")
	again := cache.Manifests()
	if len(again) != 2 || again["leave.items"].Columns[0].Name != "id" {
		t.Fatal("Manifests() shared state with the snapshot")
	}
	// An empty cache answers with an empty map rather than nil, so a caller
	// can range over it without a guard.
	if got := NewCache().Manifests(); got == nil || len(got) != 0 {
		t.Fatalf("empty cache Manifests() = %#v, want an empty map", got)
	}
}

// testManifest builds a minimal published dataset manifest.
func testManifest(name string) dataset.Manifest {
	return dataset.Manifest{
		DatasetID: name, SourceService: "leave", Domain: "leave", Grain: "one row", TenantScoped: true,
		Status: dataset.Published, Version: "1.0.0", PublishedAt: january, MaxWindowDays: 1,
		AllowedAggregates: []dataset.AggregateFn{dataset.AggregateCount},
		Columns:           []dataset.Column{{Name: "id", Type: registry.DataString, Filterable: true}},
	}
}
