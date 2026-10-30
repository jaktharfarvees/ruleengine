package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/rule"
)

// TestExportedLookupsReturnCopies pins the split between the two kinds of
// lookup. Version selection reads the snapshot's own rules so a decision does
// not deep-copy every candidate; the exported lookups still hand out copies,
// so a diagnostic caller that edits what it got cannot change what concurrent
// decisions read.
func TestExportedLookupsReturnCopies(t *testing.T) {
	definition := testRule("alpha", "1.0.0", january)
	definition.Scope.Filters = []rule.FilterCondition{{Attribute: "grade", Op: rule.FilterIn, Value: []any{"A", "B"}}}
	cache := NewCache()
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{definition}, Hierarchy: twoLevelHierarchy(), LoadedAt: january}); err != nil {
		t.Fatal(err)
	}
	snapshot := cache.Load()
	at := january.Add(time.Hour)
	stored := snapshot.version("T1", definition.RuleKey, "1.0.0")
	if len(stored) != 1 {
		t.Fatalf("version() = %d rules, want 1", len(stored))
	}
	want := cloneRule(*stored[0])

	lookups := map[string][]*rule.RuleDefinition{
		"Candidates": snapshot.Candidates("T1", "leave", "eligibility", "alpha", at),
		"Version":    snapshot.Version("T1", definition.RuleKey, "1.0.0"),
		"Versions":   snapshot.Versions("T1", definition.RuleKey),
	}
	for name, got := range lookups {
		if len(got) != 1 {
			t.Fatalf("%s returned %d rules, want 1", name, len(got))
		}
		if got[0] == stored[0] {
			t.Fatalf("%s returned the snapshot's own rule, not a copy", name)
		}
		got[0].Name = "mutated"
		got[0].DeclaredInputs[0] = "mutated"
		got[0].Scope.Filters[0].Value.([]any)[0] = "mutated"
		got[0].Logic.Rows[0].Then["allowed"] = "mutated"
	}
	if !reflect.DeepEqual(*stored[0], want) {
		t.Fatalf("a caller's edit reached the snapshot:\n got  %#v\n want %#v", *stored[0], want)
	}

	// Version selection must see the same, unedited rule.
	selected := snapshot.candidatesForKey("T1", definition.RuleKey, at)
	if len(selected) != 1 || selected[0] != stored[0] {
		t.Fatalf("candidatesForKey = %v, want the snapshot's own rule", selected)
	}
}

// BenchmarkResolveSnapshot measures version selection as a decision performs
// it, for both an effective-at lookup and an explicit pin. It reads the
// snapshot's own rules, so its cost does not grow with the size of a rule.
func BenchmarkResolveSnapshot(b *testing.B) {
	cache := NewCache()
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{testRule("alpha", "1.0.0", january), testRule("beta", "1.0.0", january)}}); err != nil {
		b.Fatal(err)
	}
	resolver := NewVersionResolver(cache, nil)
	snapshot := cache.Load()
	for _, test := range []struct {
		name    string
		request VersionRequest
	}{
		{"effective", VersionRequest{Tenant: "T1", RuleKey: testRuleKey("alpha"), EffectiveAt: january.Add(time.Hour)}},
		{"pinned", VersionRequest{Tenant: "T1", RuleKey: testRuleKey("alpha"), ExplicitPin: "1.0.0"}},
	} {
		b.Run(test.name, func(b *testing.B) {
			if selected, err := resolver.ResolveSnapshot(snapshot, test.request); err != nil || len(selected) != 1 {
				b.Fatalf("ResolveSnapshot = %v, %v", selected, err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, _ = resolver.ResolveSnapshot(snapshot, test.request)
			}
		})
	}
}
