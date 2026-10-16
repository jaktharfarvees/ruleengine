package filestore

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/store"
)

func TestLoadTestdataLayout(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	loader, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Rules) != 11 || len(bundle.Fields) == 0 || len(bundle.Manifests) != 4 || len(bundle.Hierarchy.Nodes) == 0 || bundle.Cursor == "" {
		t.Fatalf("loaded bundle counts = rules:%d fields:%d manifests:%d nodes:%d cursor:%q", len(bundle.Rules), len(bundle.Fields), len(bundle.Manifests), len(bundle.Hierarchy.Nodes), bundle.Cursor)
	}
	cache := store.NewCache()
	if err := cache.Replace(bundle); err != nil {
		t.Fatalf("loaded testdata does not form a valid snapshot: %v", err)
	}
	candidates := cache.Candidates("RWB", "leave", "eligibility", "min-service", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	if len(candidates) != 2 {
		t.Fatalf("min-service candidate count = %d, want 2", len(candidates))
	}
	path, err := cache.Hierarchy().Path("RWB", "BR001")
	if err != nil || len(path) != 4 {
		t.Fatalf("RWB BR001 path = %#v, %v; want four nodes", path, err)
	}
	changed, err := loader.LoadChanges(context.Background(), bundle.Cursor)
	if err != nil || changed.Cursor != bundle.Cursor {
		t.Fatalf("unchanged file cursor = %q, %v; want %q", changed.Cursor, err, bundle.Cursor)
	}
}

func TestNewDoesNotTouchFilesystem(t *testing.T) {
	loader, err := New(filepath.Join(t.TempDir(), "not-created"))
	if err != nil || loader == nil {
		t.Fatalf("New = %v, %v; construction should be lazy", loader, err)
	}
	if _, err := loader.LoadAll(context.Background()); err == nil {
		t.Fatal("LoadAll missing root succeeded")
	}
}

func TestLoaderOptionsAndHierarchySelection(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("New accepted an empty root")
	}
	if _, err := New(t.TempDir(), WithHierarchyFiles("../escape.json")); err == nil {
		t.Fatal("WithHierarchyFiles accepted traversal")
	}
	root := t.TempDir()
	hierarchyDir := filepath.Join(root, "hierarchy")
	if err := os.MkdirAll(hierarchyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.json", "b.json"} {
		if err := os.WriteFile(filepath.Join(hierarchyDir, name), []byte(`{"types":[],"nodes":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	loader, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.selectedHierarchyFiles(); err == nil {
		t.Fatal("multiple non-canonical hierarchy files were selected implicitly")
	}
	selected, err := New(root, WithHierarchyFiles("b.json"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := selected.selectedHierarchyFiles()
	if err != nil || len(files) != 1 || filepath.Base(files[0]) != "b.json" {
		t.Fatalf("selected hierarchy files = %#v, %v", files, err)
	}
}

func TestStrictJSONReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.json")
	if err := os.WriteFile(path, []byte(`{"dataset":"one"} {"dataset":"two"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var target map[string]any
	if err := readJSON(context.Background(), path, &target, sha256.New()); err == nil {
		t.Fatal("readJSON accepted multiple values")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := readJSON(ctx, path, &target, sha256.New()); err == nil {
		t.Fatal("readJSON ignored cancellation")
	}
}
