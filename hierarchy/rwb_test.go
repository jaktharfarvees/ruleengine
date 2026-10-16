package hierarchy_test

import (
	"testing"

	"github.com/arrowhr/ruleengine/ruletest"
)

func TestRWBAttributeInheritanceRT09(t *testing.T) {
	snapshot, err := ruletest.LoadRWB()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ attribute, value, source string }{
		{"country", "Canada", "C002"},
		{"currency", "CAD", "C002"},
		{"locale", "en-CA", "C002"},
		{"timezone", "Asia/Colombo", "BR001"},
	}
	for _, tt := range tests {
		value, source, ok := snapshot.ResolveAttribute("RWB", "BR001", tt.attribute)
		if !ok || value != tt.value || source != tt.source {
			t.Fatalf("ResolveAttribute(%q) = %q from %q (%v), want %q from %q", tt.attribute, value, source, ok, tt.value, tt.source)
		}
	}
}

func TestHierarchyVariants(t *testing.T) {
	tests := []struct {
		name     string
		node     string
		wantPath int
		wantMax  int
	}{
		{"rwb.json", "BR001", 4, 3},
		{"rwb-with-division.json", "BR001", 5, 4},
		{"rwb-nested-department.json", "BR001", 5, 3},
		{"rwb-inactive.json", "BR001", 4, 3},
		{"earrow-shallow.json", "E-D1", 2, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := ruletest.LoadHierarchy(tt.name)
			if err != nil {
				t.Fatal(err)
			}
			tenant := "RWB"
			if tt.name == "earrow-shallow.json" {
				tenant = "Earrow"
			}
			path, err := snapshot.Path(tenant, tt.node)
			if err != nil || len(path) != tt.wantPath || snapshot.MaxOrder(tenant) != tt.wantMax {
				t.Fatalf("path=%#v max=%d error=%v", path, snapshot.MaxOrder(tenant), err)
			}
		})
	}
}

func TestVariantSpecificContracts(t *testing.T) {
	nested, err := ruletest.LoadHierarchy("rwb-nested-department.json")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := nested.Path("RWB", "BR001")
	if path[2].TypeID != "department" || path[3].TypeID != "department" || path[2].Depth >= path[3].Depth {
		t.Fatalf("nested department path = %#v", path)
	}
	inactive, err := ruletest.LoadHierarchy("rwb-inactive.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{"BR001", "D003"} {
		node, ok := inactive.Node("RWB", nodeID)
		if !ok || node.Status != "INACTIVE" {
			t.Fatalf("node %q = %#v, %v", nodeID, node, ok)
		}
	}
}
