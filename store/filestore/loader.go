package filestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/store"
)

// Loader reads rules/, registry/, datasets/, and hierarchy/ below Root.
type Loader struct {
	root           string
	hierarchyFiles []string
}

// Option configures a file Loader.
type Option func(*Loader) error

// WithHierarchyFiles selects hierarchy JSON files relative to Root/hierarchy.
// It is required when a directory holds multiple alternative snapshots and no
// canonical hierarchy.json or rwb.json is present.
func WithHierarchyFiles(names ...string) Option {
	return func(loader *Loader) error {
		for _, name := range names {
			if name == "" || filepath.Base(name) != name || !strings.HasSuffix(strings.ToLower(name), ".json") {
				return fmt.Errorf("invalid hierarchy file %q", name)
			}
		}
		loader.hierarchyFiles = append([]string(nil), names...)
		return nil
	}
}

// New constructs a loader without touching the filesystem.
func New(root string, options ...Option) (*Loader, error) {
	if root == "" {
		return nil, fmt.Errorf("file store root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve file store root: %w", err)
	}
	loader := &Loader{root: absolute}
	for _, option := range options {
		if err := option(loader); err != nil {
			return nil, err
		}
	}
	return loader, nil
}

// LoadAll reads and strictly decodes a complete coherent Bundle.
func (l *Loader) LoadAll(ctx context.Context) (store.Bundle, error) {
	if l == nil {
		return store.Bundle{}, fmt.Errorf("file loader is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var bundle store.Bundle
	hash := sha256.New()
	ruleFiles, err := jsonFiles(filepath.Join(l.root, "rules"), true)
	if err != nil {
		return store.Bundle{}, err
	}
	for _, path := range ruleFiles {
		var definition rule.RuleDefinition
		if err := readJSON(ctx, path, &definition, hash); err != nil {
			return store.Bundle{}, err
		}
		bundle.Rules = append(bundle.Rules, definition)
	}
	registryFiles, err := jsonFiles(filepath.Join(l.root, "registry"), false)
	if err != nil {
		return store.Bundle{}, err
	}
	for _, path := range registryFiles {
		var document struct {
			Fields []registry.FieldDefinition `json:"fields"`
		}
		if err := readJSON(ctx, path, &document, hash); err != nil {
			return store.Bundle{}, err
		}
		bundle.Fields = append(bundle.Fields, document.Fields...)
	}
	manifestFiles, err := jsonFiles(filepath.Join(l.root, "datasets"), false)
	if err != nil {
		return store.Bundle{}, err
	}
	for _, path := range manifestFiles {
		var manifest dataset.Manifest
		if err := readJSON(ctx, path, &manifest, hash); err != nil {
			return store.Bundle{}, err
		}
		bundle.Manifests = append(bundle.Manifests, manifest)
	}
	hierarchyFiles, err := l.selectedHierarchyFiles()
	if err != nil {
		return store.Bundle{}, err
	}
	for _, path := range hierarchyFiles {
		var document struct {
			Types []hierarchy.Type `json:"types"`
			Nodes []hierarchy.Node `json:"nodes"`
		}
		if err := readJSON(ctx, path, &document, hash); err != nil {
			return store.Bundle{}, err
		}
		bundle.Hierarchy.Types = append(bundle.Hierarchy.Types, document.Types...)
		bundle.Hierarchy.Nodes = append(bundle.Hierarchy.Nodes, document.Nodes...)
	}
	bundle.Cursor = hex.EncodeToString(hash.Sum(nil))
	return bundle, nil
}

// LoadChanges rereads the coherent file snapshot. The content cursor lets the
// cache and callers identify a no-op reconcile without weakening atomicity.
func (l *Loader) LoadChanges(ctx context.Context, _ string) (store.Bundle, error) {
	return l.LoadAll(ctx)
}

func (l *Loader) selectedHierarchyFiles() ([]string, error) {
	directory := filepath.Join(l.root, "hierarchy")
	if len(l.hierarchyFiles) > 0 {
		paths := make([]string, len(l.hierarchyFiles))
		for index, name := range l.hierarchyFiles {
			paths[index] = filepath.Join(directory, name)
		}
		return paths, nil
	}
	files, err := jsonFiles(directory, false)
	if err != nil {
		return nil, err
	}
	if len(files) == 1 {
		return files, nil
	}
	for _, preferred := range []string{"hierarchy.json", "rwb.json"} {
		candidate := filepath.Join(directory, preferred)
		for _, path := range files {
			if path == candidate {
				return []string{path}, nil
			}
		}
	}
	return nil, fmt.Errorf("hierarchy directory contains %d snapshots; select files explicitly", len(files))
}

func jsonFiles(root string, recursive bool) ([]string, error) {
	var files []string
	if recursive {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %q: %w", root, err)
		}
	} else {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", root, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				files = append(files, filepath.Join(root, entry.Name()))
			}
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("%q contains no JSON artifacts", root)
	}
	return files, nil
}

func readJSON(ctx context.Context, path string, target any, hash io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := os.ReadFile(path) //nolint:gosec // Paths are confined to the configured artifact root.
	if err != nil {
		return fmt.Errorf("read artifact %q: %w", path, err)
	}
	if _, err := hash.Write([]byte(filepath.ToSlash(path))); err != nil {
		return fmt.Errorf("hash artifact path: %w", err)
	}
	if _, err := hash.Write(data); err != nil {
		return fmt.Errorf("hash artifact: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode artifact %q: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode artifact %q: multiple JSON values", path)
		}
		return fmt.Errorf("decode artifact %q: %w", path, err)
	}
	return nil
}
