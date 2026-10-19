package store

import (
	"errors"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/manifest"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

func TestDefaultPolicyIsExactAndDefensivelyCopied(t *testing.T) {
	policy := rule.DefaultPolicy{
		Domain: "leave", RuleSet: "eligibility", Name: "alpha", DecisionType: "eligibility",
		Result:     map[string]any{"allowed": true, "nested": map[string]any{"reason": "tenant policy"}},
		ReasonCode: "TENANT_DEFAULT", AuthorizedBy: "tenant-author", AuthorizedAt: january,
	}
	cache := NewCache()
	if err := cache.Replace(Bundle{Catalogs: []rule.RuleCatalog{{
		CatalogManifestID: "catalog-1", Tenant: "T1", Defaults: []rule.DefaultPolicy{policy},
	}}}); err != nil {
		t.Fatal(err)
	}
	family := rule.Family{Domain: "leave", RuleSet: "eligibility", Name: "alpha"}
	got, found := cache.Load().DefaultPolicy("T1", family)
	if !found || got.ReasonCode != "TENANT_DEFAULT" {
		t.Fatalf("DefaultPolicy() = %+v, %t", got, found)
	}
	got.Result["allowed"] = false
	got.Result["nested"].(map[string]any)["reason"] = "mutated"
	again, _ := cache.Load().DefaultPolicy("T1", family)
	if again.Result["allowed"] != true || again.Result["nested"].(map[string]any)["reason"] != "tenant policy" {
		t.Fatalf("default leaked caller mutation: %+v", again)
	}
	if _, found := cache.Load().DefaultPolicy("T2", family); found {
		t.Fatal("a tenant default crossed the tenant boundary")
	}
	var absent *Snapshot
	if _, found := absent.DefaultPolicy("T1", family); found {
		t.Fatal("a nil snapshot returned a default")
	}
}

func TestSnapshotRejectsInvalidPublishedArtifacts(t *testing.T) {
	end := january
	validHierarchy := HierarchyBundle{
		SnapshotID: "org-1", ValidFrom: january,
		Types: []hierarchy.Type{{TypeID: "root", TypeName: "Root", Tenant: "T1", Status: "ACTIVE"}},
		Nodes: []hierarchy.Node{{NodeID: "root", TypeID: "root", Tenant: "T1", Name: "Root", Status: "ACTIVE"}},
	}
	validManifest := dataset.Manifest{
		DatasetID: "leave.items", SourceService: "leave", Domain: "leave", Status: dataset.Published,
		MaxWindowDays: 30, AllowedAggregates: []dataset.AggregateFn{dataset.AggregateCount},
		Columns: []dataset.Column{{Name: "employeeId", DataType: registry.DataString, Filterable: true}},
	}
	validDefault := rule.DefaultPolicy{
		Domain: "leave", RuleSet: "eligibility", Name: "alpha", DecisionType: "eligibility",
		Result: map[string]any{"allowed": false}, ReasonCode: "DEFAULT_DENY",
		AuthorizedBy: "author", AuthorizedAt: january,
	}
	invalidVersion := testRule("alpha", "bad", january)
	tests := []struct {
		name   string
		bundle Bundle
	}{
		{
			name: "invalid hierarchy interval",
			bundle: Bundle{Hierarchies: []HierarchyBundle{
				{SnapshotID: "bad", ValidFrom: january, ValidTo: &end},
			}},
		},
		{
			name: "invalid hierarchy graph",
			bundle: Bundle{Hierarchies: []HierarchyBundle{
				{
					SnapshotID: "bad",
					Types:      validHierarchy.Types,
					Nodes: []hierarchy.Node{
						{NodeID: "orphan", TypeID: "missing", Tenant: "T1", Status: "ACTIVE"},
					},
				},
			}},
		},
		{
			name:   "duplicate hierarchy id",
			bundle: Bundle{Hierarchies: []HierarchyBundle{validHierarchy, validHierarchy}},
		},
		{name: "invalid rule", bundle: Bundle{Rules: []rule.RuleDefinition{{}}}},
		{name: "invalid semantic version", bundle: Bundle{Rules: []rule.RuleDefinition{invalidVersion}}},
		{
			name: "invalid catalog",
			bundle: Bundle{Catalogs: []rule.RuleCatalog{
				{CatalogManifestID: "catalog-1", Tenant: "T1"},
			}},
		},
		{
			name: "duplicate default",
			bundle: Bundle{Catalogs: []rule.RuleCatalog{
				{CatalogManifestID: "catalog-1", Tenant: "T1", Defaults: []rule.DefaultPolicy{validDefault}},
				{CatalogManifestID: "catalog-2", Tenant: "T1", Defaults: []rule.DefaultPolicy{validDefault}},
			}},
		},
		{name: "invalid manifest", bundle: Bundle{Manifests: []dataset.Manifest{{DatasetID: "broken"}}}},
		{name: "duplicate manifest", bundle: Bundle{Manifests: []dataset.Manifest{validManifest, validManifest}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := NewCache().Replace(test.bundle); err == nil {
				t.Fatal("invalid bundle was accepted")
			}
		})
	}
}

func TestSnapshotNilAndHierarchyMissBoundaries(t *testing.T) {
	var absent *Snapshot
	if absent.Versions("T1", testRuleKey("alpha")) != nil ||
		absent.Version("T1", testRuleKey("alpha"), "1.0.0") != nil ||
		absent.Registry() != nil || absent.Hierarchy() != nil ||
		len(absent.Manifests()) != 0 || absent.Stats() != (Stats{}) {
		t.Fatal("nil snapshot accessors did not return safe zero values")
	}
	if _, ok := absent.Manifest("missing"); ok {
		t.Fatal("nil snapshot returned a manifest")
	}
	if _, _, ok := absent.HierarchyAt("T1", january, ""); ok {
		t.Fatal("nil snapshot returned a hierarchy")
	}

	cache := NewCache()
	if err := cache.Replace(Bundle{Hierarchy: HierarchyBundle{
		SnapshotID: "org-1", Types: []hierarchy.Type{{TypeID: "root", TypeName: "Root", Tenant: "T1", Status: "ACTIVE"}},
		Nodes: []hierarchy.Node{{NodeID: "root", TypeID: "root", Tenant: "T1", Name: "Root", Status: "ACTIVE"}},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		tenant, pin string
		at          time.Time
	}{{"", "", january}, {"T1", "", time.Time{}}, {"T1", "missing", january}, {"T2", "", january}} {
		if _, _, ok := cache.Load().HierarchyAt(request.tenant, request.at, request.pin); ok {
			t.Fatalf("unexpected hierarchy for %+v", request)
		}
	}
}

func TestVersionResolverBoundaryFailures(t *testing.T) {
	definition := testRule("alpha", "1.0.0", january)
	cache := NewCache()
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{definition}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (*VersionResolver)(nil).Resolve(VersionRequest{}); !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("nil Resolve error = %v", err)
	}
	resolver := NewVersionResolver(cache, nil)
	for _, request := range []VersionRequest{
		{},
		{Tenant: "T1", RuleKey: "bad-key", EffectiveAt: january},
		{Tenant: "T1", RuleKey: definition.RuleKey},
	} {
		if _, err := resolver.Resolve(request); err == nil {
			t.Fatalf("Resolve(%+v) succeeded", request)
		}
	}
	if _, err := resolver.ResolveSnapshot(nil, VersionRequest{}); !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("nil snapshot error = %v", err)
	}
	if _, err := resolver.RunManifest("T1", "run"); !errors.Is(err, ErrRunManifestUnavailable) {
		t.Fatalf("nil manifest store error = %v", err)
	}
	if _, _, err := resolver.FactPin("T1", "", "field"); err == nil {
		t.Fatal("empty runId fact pin succeeded")
	}
	if _, _, err := resolver.FactPin("T1", "run", "field"); !errors.Is(err, ErrRunManifestUnavailable) {
		t.Fatalf("nil manifest store FactPin error = %v", err)
	}

	runs := manifest.NewInMemoryStore()
	if err := runs.Put(manifest.RunManifest{
		RunID: "run", Tenant: "T1", ValidAt: january, HierarchySnapshotID: "default",
		Status: manifest.StatusOpen, OpenedAt: january,
		RulePins: map[string]string{definition.RuleKey.String(): "1.0.0"}, FactPins: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}
	withRuns := NewVersionResolver(cache, runs)
	if _, err := withRuns.Resolve(VersionRequest{Tenant: "T1", RuleKey: definition.RuleKey, RunID: "run", EffectiveAt: january.Add(time.Second)}); !errors.Is(err, ErrPinnedVersionUnavailable) {
		t.Fatalf("mismatched run validAt error = %v", err)
	}
	if _, _, err := withRuns.FactPin("T2", "run", "field"); !errors.Is(err, ErrRunTenantMismatch) {
		t.Fatalf("cross-tenant FactPin error = %v", err)
	}
	if _, found, err := withRuns.FactPin("T1", "run", "missing"); err != nil || found {
		t.Fatalf("missing FactPin = %t, %v", found, err)
	}
}
