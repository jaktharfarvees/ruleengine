package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/manifest"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

var january = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestCandidatesIndexIncludesName(t *testing.T) {
	cache := NewCache()
	bundle := Bundle{Rules: []rule.RuleDefinition{
		testRule("alpha", "1.0.0", january),
		testRule("beta", "1.0.0", january),
	}}
	if err := cache.Replace(bundle); err != nil {
		t.Fatal(err)
	}
	alpha := cache.Candidates("T1", "leave", "eligibility", "alpha", january.Add(time.Hour))
	if len(alpha) != 1 || alpha[0].Name != "alpha" {
		t.Fatalf("Candidates(alpha) = %#v, want exactly alpha", alpha)
	}
	if got := cache.Candidates("T1", "leave", "eligibility", "", january.Add(time.Hour)); got != nil {
		t.Fatalf("Candidates without name = %#v, want nil rather than a rule-set scan", got)
	}
}

func TestFailedReloadLeavesOldSnapshotServing(t *testing.T) {
	cache := NewCache()
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{testRule("alpha", "1.0.0", january)}}); err != nil {
		t.Fatal(err)
	}
	before := cache.Load()
	invalid := testRule("alpha", "not-semver", january)
	if err := cache.Reload(context.Background(), staticLoader{bundle: Bundle{Rules: []rule.RuleDefinition{invalid}}}); err == nil {
		t.Fatal("Reload(invalid) succeeded, want validation error")
	}
	if cache.Load() != before {
		t.Fatal("failed reload replaced the serving snapshot")
	}
	if found := cache.Version("T1", before.Versions("T1", testRuleKey("alpha"))[0].RuleKey, "1.0.0"); len(found) != 1 {
		t.Fatal("old version is no longer serving after failed reload")
	}
}

func TestSnapshotOwnsInputData(t *testing.T) {
	definition := testRule("alpha", "1.0.0", january)
	bundle := Bundle{Rules: []rule.RuleDefinition{definition}}
	cache := NewCache()
	if err := cache.Replace(bundle); err != nil {
		t.Fatal(err)
	}
	bundle.Rules[0].Name = "mutated"
	bundle.Rules[0].Logic.Rows[0].Then["allowed"] = false
	stored := cache.Version("T1", definition.RuleKey, definition.Version)
	if len(stored) != 1 || stored[0].Name != "alpha" || stored[0].Logic.Rows[0].Then["allowed"] != true {
		t.Fatalf("stored rule changed through input mutation: %#v", stored)
	}
}

func TestExactVersionPreservesAnchoredVariants(t *testing.T) {
	baseline := testRule("alpha", "1.0.0", january)
	exception := cloneRule(baseline)
	exception.Scope.Anchor = rule.Anchor{Kind: rule.AnchorEmployee, EmployeeID: "E1", Reason: "approved exception"}
	cache := NewCache()
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{baseline, exception}}); err != nil {
		t.Fatal(err)
	}
	if variants := cache.Version("T1", baseline.RuleKey, "1.0.0"); len(variants) != 2 {
		t.Fatalf("Version variant count = %d, want 2", len(variants))
	}
	duplicate := cloneRule(baseline)
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{baseline, duplicate}}); err == nil {
		t.Fatal("duplicate scope at the same version was accepted")
	}
}

func TestSnapshotArtifactsStatsAndFreshness(t *testing.T) {
	now := &mutableClock{value: january}
	cache := NewCache(WithClock(now))
	bundle := Bundle{
		Fields: []registry.FieldDefinition{{
			Name: "input", Domain: "leave", DataType: registry.DataString, OwnerService: "leave", Status: "active", Role: registry.RoleInput,
		}},
		Manifests: []dataset.Manifest{{
			Dataset: "leave.items", OwnerService: "leave", Domain: "leave", Grain: "one row", TenantScoped: true,
			Status: dataset.Published, Version: "1.0.0", PublishedAt: january, MaxWindowDays: 1,
			Aggregates: []dataset.AggregateFn{dataset.AggregateCount},
			Columns:    []dataset.Column{{Name: "id", Type: registry.DataString, Filterable: true}},
		}},
		Hierarchy: HierarchyBundle{
			Types: []hierarchy.Type{{TypeID: "root", TypeName: "Root", Tenant: "T1", Status: "ACTIVE"}},
			Nodes: []hierarchy.Node{{NodeID: "root", Tenant: "T1", TypeID: "root", Name: "Root", Status: "ACTIVE"}},
		},
		Cursor: "c1",
	}
	if err := cache.Replace(bundle); err != nil {
		t.Fatal(err)
	}
	if cache.Registry() == nil {
		t.Fatal("Registry returned nil")
	}
	manifest, found := cache.Manifest("leave.items")
	if !found || manifest.Dataset != "leave.items" {
		t.Fatalf("Manifest = %#v, %v", manifest, found)
	}
	manifest.Columns[0].Name = "mutated"
	again, _ := cache.Manifest("leave.items")
	if again.Columns[0].Name != "id" {
		t.Fatal("Manifest returned mutable snapshot state")
	}
	if cache.Hierarchy() == nil || cache.Stats().Cursor != "c1" || cache.Stats().HierarchyNodes != 1 {
		t.Fatalf("snapshot accessors returned incomplete state: %#v", cache.Stats())
	}
	now.value = now.value.Add(5 * time.Second)
	if got := cache.StaleSeconds(); got != 5 {
		t.Fatalf("StaleSeconds = %v, want 5", got)
	}
}

func TestReconcileUsesCursorAndKeepsOldSnapshotOnFailure(t *testing.T) {
	cache := NewCache()
	initial := Bundle{Rules: []rule.RuleDefinition{testRule("alpha", "1.0.0", january)}, Cursor: "old"}
	if err := cache.Replace(initial); err != nil {
		t.Fatal(err)
	}
	before := cache.Load()
	loader := &changesLoader{err: errors.New("service unavailable")}
	if err := cache.Reconcile(context.Background(), loader); err == nil {
		t.Fatal("Reconcile failure was hidden")
	}
	if loader.since != "old" || cache.Load() != before {
		t.Fatalf("failed reconcile since=%q replaced=%v", loader.since, cache.Load() != before)
	}
	loader.err = nil
	loader.bundle = Bundle{Rules: []rule.RuleDefinition{testRule("alpha", "1.0.1", january)}, Cursor: "new"}
	if err := cache.Reconcile(context.Background(), loader); err != nil {
		t.Fatal(err)
	}
	if cache.Stats().Cursor != "new" {
		t.Fatalf("reconcile cursor = %q, want new", cache.Stats().Cursor)
	}
}

func TestCacheSwapWith64Readers(t *testing.T) {
	cache := NewCache()
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{testRule("alpha", "1.0.0", january)}}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	errorsFound := make(chan error, 64)
	var readers sync.WaitGroup
	for reader := 0; reader < 64; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					candidates := cache.Candidates("T1", "leave", "eligibility", "alpha", january.Add(24*time.Hour))
					if len(candidates) != 1 || candidates[0].Name != "alpha" || len(candidates[0].Logic.Rows) != 1 {
						errorsFound <- fmt.Errorf("reader observed incomplete snapshot: %#v", candidates)
						return
					}
				}
			}
		}()
	}
	for patch := 1; patch <= 200; patch++ {
		version := fmt.Sprintf("1.0.%d", patch)
		if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{testRule("alpha", version, january)}}); err != nil {
			close(stop)
			readers.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func TestHistoricalHierarchySelectionAndOverlapRejection(t *testing.T) {
	february := january.AddDate(0, 1, 0)
	oldTo := february
	oldHierarchy := HierarchyBundle{
		SnapshotID: "org-jan", ValidFrom: january, ValidTo: &oldTo,
		Types: []hierarchy.Type{
			{TypeID: "root", TypeName: "Root", Order: 0, Tenant: "T1", Status: "ACTIVE"},
			{TypeID: "leaf", TypeName: "Leaf", Order: 1, Tenant: "T1", Status: "ACTIVE"},
		},
		Nodes: []hierarchy.Node{
			{NodeID: "OLD", Tenant: "T1", TypeID: "root", Name: "Old", Status: "ACTIVE"},
			{NodeID: "E1", Tenant: "T1", TypeID: "leaf", Name: "Employee", ParentNodeID: "OLD", Status: "ACTIVE"},
		},
	}
	newHierarchy := HierarchyBundle{
		SnapshotID: "org-feb", ValidFrom: february,
		Types: append([]hierarchy.Type(nil), oldHierarchy.Types...),
		Nodes: []hierarchy.Node{
			{NodeID: "NEW", Tenant: "T1", TypeID: "root", Name: "New", Status: "ACTIVE"},
			{NodeID: "E1", Tenant: "T1", TypeID: "leaf", Name: "Employee", ParentNodeID: "NEW", Status: "ACTIVE"},
		},
	}
	cache := NewCache()
	if err := cache.Replace(Bundle{Hierarchies: []HierarchyBundle{newHierarchy, oldHierarchy}}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		at, pin, root string
	}{
		{january.Add(time.Hour).Format(time.RFC3339), "", "OLD"},
		{february.Add(time.Hour).Format(time.RFC3339), "", "NEW"},
		{january.Add(time.Hour).Format(time.RFC3339), "org-jan", "OLD"},
	} {
		at, _ := time.Parse(time.RFC3339, test.at)
		snapshot, _, found := cache.Load().HierarchyAt("T1", at, test.pin)
		if !found {
			t.Fatalf("HierarchyAt(%s, %q) was not found", test.at, test.pin)
		}
		path, err := snapshot.Path("T1", "E1")
		if err != nil || len(path) != 2 || path[0].NodeID != test.root {
			t.Fatalf("HierarchyAt(%s, %q) path = %v, %v", test.at, test.pin, path, err)
		}
	}
	if cache.Stats().HierarchyNodes != 2 || cache.Stats().HierarchyTypes != 2 {
		t.Fatalf("historical hierarchy stats = %+v", cache.Stats())
	}
	overlap := oldHierarchy
	overlap.SnapshotID = "overlap"
	overlap.ValidFrom = january.Add(24 * time.Hour)
	if err := cache.Replace(Bundle{Hierarchies: []HierarchyBundle{oldHierarchy, overlap}}); err == nil {
		t.Fatal("overlapping hierarchy intervals were accepted")
	}
}

func TestVersionResolverPrecedence(t *testing.T) {
	cache := NewCache()
	old := testRule("alpha", "1.0.0", january)
	current := testRule("alpha", "1.1.0", january.Add(31*24*time.Hour))
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{old, current}}); err != nil {
		t.Fatal(err)
	}
	runs := manifest.NewInMemoryStore()
	if err := runs.Put(manifest.RunManifest{
		RunID: "run-1", Tenant: "T1", ValidAt: january, Status: manifest.StatusOpen, OpenedAt: january,
		RulePins: map[string]string{old.RuleKey.String(): "1.0.0"}, FactPins: map[string]string{"balance": "2"},
		HierarchySnapshotID: "default",
	}); err != nil {
		t.Fatal(err)
	}
	resolver := NewVersionResolver(cache, runs)
	selected, err := resolver.Resolve(context.Background(), VersionRequest{
		Tenant: "T1", RuleKey: old.RuleKey, RunID: "run-1", ExplicitPin: "1.1.0", EffectiveAt: january,
	})
	if err != nil || len(selected) != 1 || selected[0].Version != "1.0.0" {
		t.Fatalf("manifest resolution = %v, %v; want 1.0.0", selected, err)
	}
	selected, err = resolver.Resolve(context.Background(), VersionRequest{Tenant: "T1", RuleKey: old.RuleKey, ExplicitPin: "1.0.0", EffectiveAt: january.Add(60 * 24 * time.Hour)})
	if err != nil || len(selected) != 1 || selected[0].Version != "1.0.0" {
		t.Fatalf("explicit-pin resolution = %v, %v; want 1.0.0", selected, err)
	}
	// With no pin, the latest version effective at the requested time wins.
	// Both versions are effective here, because an immutable 1.0.0 carries no
	// validTo, and returning both would make resolution report a tie.
	selected, err = resolver.Resolve(context.Background(), VersionRequest{Tenant: "T1", RuleKey: old.RuleKey, EffectiveAt: january.Add(60 * 24 * time.Hour)})
	if err != nil || len(selected) != 1 || selected[0].Version != "1.1.0" {
		t.Fatalf("effective resolution = %v, %v; want only the latest, 1.1.0", versionsOf(selected), err)
	}
	// Before 1.1.0 takes effect, the same request resolves to 1.0.0.
	selected, err = resolver.Resolve(context.Background(), VersionRequest{Tenant: "T1", RuleKey: old.RuleKey, EffectiveAt: january.Add(24 * time.Hour)})
	if err != nil || len(selected) != 1 || selected[0].Version != "1.0.0" {
		t.Fatalf("earlier effective resolution = %v, %v; want 1.0.0", versionsOf(selected), err)
	}
	factPin, found, err := resolver.FactPin(context.Background(), "T1", "run-1", "balance")
	if err != nil || !found || factPin != "2" {
		t.Fatalf("FactPin = %q, %v, %v; want 2, true, nil", factPin, found, err)
	}
}

func TestVersionResolverReportsMissingPin(t *testing.T) {
	cache := NewCache()
	definition := testRule("alpha", "1.0.0", january)
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{definition}}); err != nil {
		t.Fatal(err)
	}
	_, err := NewVersionResolver(cache, nil).Resolve(context.Background(), VersionRequest{Tenant: "T1", RuleKey: definition.RuleKey, ExplicitPin: "9.0.0"})
	if !errors.Is(err, ErrPinnedVersionUnavailable) {
		t.Fatalf("Resolve missing pin error = %v, want ErrPinnedVersionUnavailable", err)
	}
}

func TestVersionResolverManifestFailures(t *testing.T) {
	cache := NewCache()
	definition := testRule("alpha", "1.0.0", january)
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{definition}}); err != nil {
		t.Fatal(err)
	}
	runs := manifest.NewInMemoryStore()
	if err := runs.Put(manifest.RunManifest{RunID: "other", Tenant: "T2", ValidAt: january, OpenedAt: january, Status: manifest.StatusOpen, RulePins: map[string]string{}, FactPins: map[string]string{}, HierarchySnapshotID: "default"}); err != nil {
		t.Fatal(err)
	}
	resolver := NewVersionResolver(cache, runs)
	if _, err := resolver.Resolve(context.Background(), VersionRequest{Tenant: "T1", RuleKey: definition.RuleKey, RunID: "missing"}); !errors.Is(err, ErrRunManifestUnavailable) {
		t.Fatalf("missing manifest error = %v", err)
	}
	if _, err := resolver.Resolve(context.Background(), VersionRequest{Tenant: "T1", RuleKey: definition.RuleKey, RunID: "other"}); !errors.Is(err, ErrRunTenantMismatch) {
		t.Fatalf("tenant mismatch error = %v", err)
	}
	if _, err := resolver.Resolve(context.Background(), VersionRequest{Tenant: "T1", RuleKey: definition.RuleKey, EffectiveAt: january.Add(-time.Hour)}); !errors.Is(err, ErrEffectiveVersionUnavailable) {
		t.Fatalf("no effective version error = %v", err)
	}
}

func TestSemanticVersions(t *testing.T) {
	tests := []string{"", "1", "1.2", "01.2.3", "1.x.3", "1.2.3-"}
	for _, value := range tests {
		if _, err := parseSemanticVersion(value); err == nil {
			t.Errorf("parseSemanticVersion(%q) succeeded", value)
		}
	}
	release, err := parseSemanticVersion("1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	prerelease, err := parseSemanticVersion("1.2.3-rc.1+build")
	if err != nil {
		t.Fatal(err)
	}
	older, _ := parseSemanticVersion("1.1.9")
	if compareSemanticVersions(release, prerelease) <= 0 || compareSemanticVersions(older, release) >= 0 || compareSemanticVersions(release, release) != 0 {
		t.Fatal("semantic version ordering is incorrect")
	}
}

func BenchmarkCandidates(b *testing.B) {
	cache := NewCache()
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{testRule("alpha", "1.0.0", january), testRule("beta", "1.0.0", january)}}); err != nil {
		b.Fatal(err)
	}
	at := january.Add(time.Hour)
	b.ReportAllocs()
	for range b.N {
		_ = cache.Candidates("T1", "leave", "eligibility", "alpha", at)
	}
}

type staticLoader struct {
	bundle Bundle
	err    error
}

func (l staticLoader) LoadAll(context.Context) (Bundle, error) { return l.bundle, l.err }

type changesLoader struct {
	bundle Bundle
	err    error
	since  string
}

func (l *changesLoader) LoadChanges(_ context.Context, since string) (Bundle, error) {
	l.since = since
	return l.bundle, l.err
}

type mutableClock struct{ value time.Time }

func (c *mutableClock) Now() time.Time { return c.value }

func testRule(name, version string, from time.Time) rule.RuleDefinition {
	return rule.RuleDefinition{
		RuleKey: testRuleKey(name), Domain: "leave", RuleSet: "eligibility", Name: name,
		Version: version, ChangeType: rule.ChangeMajor, Status: rule.StatusActive, Tenant: "T1",
		Scope:     rule.Scope{Anchor: rule.Anchor{Kind: rule.AnchorNode, NodeID: "root"}},
		Effective: rule.Effective{ValidFrom: from}, DeclaredInputs: []string{"input"}, DeclaredOutputs: []string{"allowed"},
		DecisionType: "test", Logic: rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{ID: "row", Then: map[string]any{"allowed": true}}}},
		EngineRef: rule.EngineRef{Engine: "decisiontable"}, Metadata: rule.Metadata{Owner: "test", CreatedBy: "test", CreatedAt: from},
	}
}

func testRuleKey(name string) rule.RuleKey {
	return rule.RuleKey("urn:arrowhr:rule:leave:eligibility:" + name + ":v1")
}
