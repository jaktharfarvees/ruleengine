package hierarchy

import (
	"strings"
	"sync"
	"testing"

	"github.com/arrowhr/ruleengine/rule"
)

func smallFixture() ([]Type, []Node) {
	return []Type{
		{TypeID: "group", TypeName: "Group", Order: 0, Tenant: "T", Status: "ACTIVE"},
		{TypeID: "department", TypeName: "Department", Order: 1, Tenant: "T", Status: "ACTIVE"},
	}, []Node{
		{NodeID: "root", Tenant: "T", TypeID: "group", Name: "Root", Status: "ACTIVE", Country: "Canada"},
		{NodeID: "child", Tenant: "T", TypeID: "department", Name: "Child", ParentNodeID: "root", Status: "ACTIVE", Timezone: "UTC"},
	}
}

func mustSmallSnapshot(t testing.TB) *Snapshot {
	t.Helper()
	types, nodes := smallFixture()
	snapshot, err := NewSnapshot(types, nodes)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	return snapshot
}

func TestSnapshotLookups(t *testing.T) {
	snapshot := mustSmallSnapshot(t)
	path, err := snapshot.Path("T", "child")
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[0].NodeID != "root" || path[0].Depth != 0 || path[1].NodeID != "child" || path[1].Depth != 1 {
		t.Fatalf("Path() = %#v, want root then child", path)
	}
	if node, ok := snapshot.Node("T", "child"); !ok || node.Name != "Child" {
		t.Fatalf("Node() = %#v, %v", node, ok)
	}
	if hierarchyType, ok := snapshot.Type("T", "department"); !ok || hierarchyType.Order != 1 {
		t.Fatalf("Type() = %#v, %v", hierarchyType, ok)
	}
	if snapshot.MaxOrder("T") != 1 || snapshot.Empty("T") || snapshot.Empty("") {
		t.Fatalf("unexpected snapshot metadata")
	}
	if _, err := snapshot.Path("T", "missing"); err == nil {
		t.Fatal("Path accepted an unknown node")
	}
	if _, ok := snapshot.Node("T", "missing"); ok {
		t.Fatal("Node found an unknown node")
	}
	if _, ok := snapshot.Type("T", "missing"); ok {
		t.Fatal("Type found an unknown type")
	}
}

func TestNilAndEmptySnapshot(t *testing.T) {
	var snapshot *Snapshot
	if !snapshot.Empty("") || snapshot.MaxOrder("T") != 0 {
		t.Fatal("nil snapshot metadata is not safe")
	}
	if _, err := snapshot.Path("T", "node"); err == nil {
		t.Fatal("nil snapshot Path returned no error")
	}
	if _, ok := snapshot.Node("T", "node"); ok {
		t.Fatal("nil snapshot returned a node")
	}
	if _, ok := snapshot.Type("T", "type"); ok {
		t.Fatal("nil snapshot returned a type")
	}
	empty, err := NewSnapshot(nil, nil)
	if err != nil || !empty.Empty("") || !empty.Empty("T") {
		t.Fatalf("empty snapshot = %#v, %v", empty, err)
	}
}

func TestNewSnapshotRejectsInvalidData(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]Type, []Node) ([]Type, []Node)
		text   string
	}{
		{"invalid type", func(types []Type, nodes []Node) ([]Type, []Node) { types[0].Tenant = ""; return types, nodes }, "type"},
		{"duplicate type", func(types []Type, nodes []Node) ([]Type, []Node) { return append(types, types[0]), nodes }, "duplicate"},
		{"duplicate order", func(types []Type, nodes []Node) ([]Type, []Node) { types[1].Order = 0; return types, nodes }, "share order"},
		{"invalid node", func(types []Type, nodes []Node) ([]Type, []Node) { nodes[1].Name = ""; return types, nodes }, "node"},
		{"duplicate node", func(types []Type, nodes []Node) ([]Type, []Node) { return types, append(nodes, nodes[0]) }, "duplicate"},
		{"unknown type", func(types []Type, nodes []Node) ([]Type, []Node) { nodes[1].TypeID = "missing"; return types, nodes }, "unknown hierarchy type"},
		{"orphan", func(types []Type, nodes []Node) ([]Type, []Node) {
			nodes[1].ParentNodeID = "missing"
			return types, nodes
		}, "missing parent"},
		{"two roots", func(types []Type, nodes []Node) ([]Type, []Node) { nodes[1].ParentNodeID = ""; return types, nodes }, "exactly one root"},
		{"cycle", func(types []Type, nodes []Node) ([]Type, []Node) {
			nodes = append(nodes,
				Node{NodeID: "a", Tenant: "T", TypeID: "department", Name: "A", ParentNodeID: "b", Status: "ACTIVE"},
				Node{NodeID: "b", Tenant: "T", TypeID: "department", Name: "B", ParentNodeID: "a", Status: "ACTIVE"},
			)
			return types, nodes
		}, "cycle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			types, nodes := smallFixture()
			types, nodes = tt.mutate(types, nodes)
			_, err := NewSnapshot(types, nodes)
			if err == nil || !strings.Contains(err.Error(), tt.text) {
				t.Fatalf("NewSnapshot() error = %v, want text %q", err, tt.text)
			}
		})
	}
}

func TestDerivePrecedence(t *testing.T) {
	snapshot := mustSmallSnapshot(t)
	precedence, err := snapshot.DerivePrecedence("T", rule.Anchor{Kind: rule.AnchorNode, NodeID: "child"})
	if err != nil || precedence.Order != 1 || precedence.Depth != 1 || precedence.TypeName != "Department" || precedence.NodeName != "Child" {
		t.Fatalf("node precedence = %#v, %v", precedence, err)
	}
	for _, anchor := range []rule.Anchor{{Kind: rule.AnchorKind("employee")}, {Kind: rule.AnchorNode, NodeID: "missing"}, {Kind: "other"}} {
		if _, err := snapshot.DerivePrecedence("T", anchor); err == nil {
			t.Fatalf("DerivePrecedence(%#v) returned no error", anchor)
		}
	}
	types, nodes := smallFixture()
	nodes[1].Status = "INACTIVE"
	inactive, err := NewSnapshot(types, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inactive.DerivePrecedence("T", rule.Anchor{Kind: rule.AnchorNode, NodeID: "child"}); err == nil {
		t.Fatal("inactive anchor produced precedence")
	}
}

func TestResolveAttribute(t *testing.T) {
	snapshot := mustSmallSnapshot(t)
	for _, tt := range []struct{ attribute, value, source string }{
		{"country", "Canada", "root"}, {"employmentCountry", "Canada", "root"},
		{"timezone", "UTC", "child"},
	} {
		value, source, ok := snapshot.ResolveAttribute("T", "child", tt.attribute)
		if !ok || value != tt.value || source != tt.source {
			t.Fatalf("ResolveAttribute(%q) = %q, %q, %v", tt.attribute, value, source, ok)
		}
	}
	for _, attribute := range []string{"currency", "locale", "unsupported"} {
		if _, _, ok := snapshot.ResolveAttribute("T", "child", attribute); ok {
			t.Fatalf("ResolveAttribute(%q) unexpectedly succeeded", attribute)
		}
	}
	if _, _, ok := snapshot.ResolveAttribute("T", "missing", "country"); ok {
		t.Fatal("unknown node resolved an attribute")
	}
}

func TestCacheSwap(t *testing.T) {
	first := mustSmallSnapshot(t)
	types, nodes := smallFixture()
	nodes[1].Name = "Replacement"
	second, err := NewSnapshot(types, nodes)
	if err != nil {
		t.Fatal(err)
	}
	var cache Cache
	if cache.Load() != nil {
		t.Fatal("zero cache is not empty")
	}
	cache.Swap(first)
	const readers = 64
	var wait sync.WaitGroup
	wait.Add(readers)
	for range readers {
		go func() {
			defer wait.Done()
			for range 100 {
				if cache.Load() == nil {
					t.Error("cache load returned nil during swaps")
					return
				}
			}
		}()
	}
	for range 100 {
		cache.Swap(second)
		cache.Swap(first)
	}
	wait.Wait()
	cache.Swap(nil)
	if cache.Load() != nil {
		t.Fatal("cache did not clear")
	}
}

func TestPathReturnsDefensiveCopy(t *testing.T) {
	snapshot := mustSmallSnapshot(t)
	path, err := snapshot.Path("T", "child")
	if err != nil || len(path) != 2 {
		t.Fatalf("Path() = %#v, %v", path, err)
	}
	path[0].NodeID = "mutated"
	again, err := snapshot.Path("T", "child")
	if err != nil || again[0].NodeID == "mutated" {
		t.Fatalf("caller mutation escaped into snapshot: %#v, %v", again, err)
	}
}

func BenchmarkPath(b *testing.B) {
	snapshot := mustSmallSnapshot(b)
	path, err := snapshot.Path("T", "child")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		path, _ = snapshot.Path("T", "child")
	}
	if len(path) != 2 {
		b.Fatal("unexpected path")
	}
}

// BenchmarkCacheLoadDuringSwap proves the budget that readers see no
// measurable latency change while the cache is being replaced: compare the
// quiet and contended sub-benchmarks.
func BenchmarkCacheLoadDuringSwap(b *testing.B) {
	first := mustSmallSnapshot(b)
	types, nodes := smallFixture()
	nodes[1].Name = "Replacement"
	second, err := NewSnapshot(types, nodes)
	if err != nil {
		b.Fatal(err)
	}
	for _, swapping := range []bool{false, true} {
		name := "quiet"
		if swapping {
			name = "swapping"
		}
		b.Run(name, func(b *testing.B) {
			var cache Cache
			cache.Swap(first)
			stop := make(chan struct{})
			if swapping {
				done := make(chan struct{})
				go func() {
					defer close(done)
					for {
						select {
						case <-stop:
							return
						default:
							cache.Swap(second)
							cache.Swap(first)
						}
					}
				}()
				b.Cleanup(func() { close(stop); <-done })
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if cache.Load() == nil {
					b.Fatal("cache load returned nil")
				}
			}
		})
	}
}
