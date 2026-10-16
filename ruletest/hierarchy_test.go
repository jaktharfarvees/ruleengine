package ruletest

import "testing"

func TestLoadRWB(t *testing.T) {
	snapshot, err := LoadRWB()
	if err != nil {
		t.Fatal(err)
	}
	path, err := snapshot.Path("RWB", "BR001")
	if err != nil || len(path) != 4 || path[0].NodeID != "G001" || path[3].NodeID != "BR001" {
		t.Fatalf("Path() = %#v, %v", path, err)
	}
}

func TestLoadHierarchyRejectsUnsafeOrMissingNames(t *testing.T) {
	for _, name := range []string{"", "../rwb.json", "rwb.txt", "missing.json"} {
		if _, err := LoadHierarchy(name); err == nil {
			t.Fatalf("LoadHierarchy(%q) returned no error", name)
		}
	}
}
