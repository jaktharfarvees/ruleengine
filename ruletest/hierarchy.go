package ruletest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/arrowhr/ruleengine/hierarchy"
)

// HierarchyFixture is the JSON envelope used by shared hierarchy fixtures.
type HierarchyFixture struct {
	Types []hierarchy.Type `json:"types"`
	Nodes []hierarchy.Node `json:"nodes"`
}

// LoadRWB loads the canonical RWB hierarchy fixture.
func LoadRWB() (*hierarchy.Snapshot, error) { return LoadHierarchy("rwb.json") }

// LoadHierarchy loads a named shared hierarchy fixture and validates it into an
// immutable snapshot. Names cannot escape the fixture directory.
func LoadHierarchy(name string) (*hierarchy.Snapshot, error) {
	fixture, err := LoadHierarchyFixture(name)
	if err != nil {
		return nil, err
	}
	snapshot, err := hierarchy.NewSnapshot(fixture.Types, fixture.Nodes)
	if err != nil {
		return nil, fmt.Errorf("validate hierarchy fixture %q: %w", name, err)
	}
	return snapshot, nil
}

// LoadHierarchyFixture loads a named shared hierarchy fixture without building
// a snapshot, for a caller assembling a store bundle from its parts.
func LoadHierarchyFixture(name string) (HierarchyFixture, error) {
	if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return HierarchyFixture{}, fmt.Errorf("invalid hierarchy fixture name %q", name)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return HierarchyFixture{}, fmt.Errorf("locate ruletest package source")
	}
	fixturePath := filepath.Join(filepath.Dir(source), "..", "testdata", "hierarchy", name)
	data, err := os.ReadFile(fixturePath) //nolint:gosec // The sanitized name is confined to repository fixtures.
	if err != nil {
		return HierarchyFixture{}, fmt.Errorf("read hierarchy fixture %q: %w", name, err)
	}
	var fixture HierarchyFixture
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		return HierarchyFixture{}, fmt.Errorf("decode hierarchy fixture %q: %w", name, err)
	}
	return fixture, nil
}
