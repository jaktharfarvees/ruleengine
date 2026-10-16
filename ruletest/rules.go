package ruletest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

// LoadRule loads one shared rule fixture by its path under testdata/rules,
// such as "leave/eligibility-min-service-root.json", and validates its shape.
// The fixtures are the contract shared with Rule Studio, so a service team and
// the UI can assert the same resolution outcomes from the same files.
func LoadRule(name string) (*rule.RuleDefinition, error) {
	clean, err := fixtureName(name)
	if err != nil {
		return nil, err
	}
	root, err := ruleFixtureRoot()
	if err != nil {
		return nil, err
	}
	return readRule(filepath.Join(root, filepath.FromSlash(clean)), clean)
}

// LoadRules loads every rule fixture in one directory under testdata/rules,
// such as "conflict", ordered by file name so a test over them is stable.
func LoadRules(directory string) ([]*rule.RuleDefinition, error) {
	clean, err := fixtureName(directory + "/placeholder.json")
	if err != nil {
		return nil, err
	}
	root, err := ruleFixtureRoot()
	if err != nil {
		return nil, err
	}
	group := path.Dir(clean)
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(group)))
	if err != nil {
		return nil, fmt.Errorf("read rule fixture directory %q: %w", directory, err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("rule fixture directory %q holds no fixtures", directory)
	}
	definitions := make([]*rule.RuleDefinition, 0, len(names))
	for _, name := range names {
		relative := path.Join(group, name)
		definition, err := readRule(filepath.Join(root, filepath.FromSlash(relative)), relative)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func readRule(fixturePath, name string) (*rule.RuleDefinition, error) {
	data, err := os.ReadFile(fixturePath) //nolint:gosec // The sanitized name is confined to repository fixtures.
	if err != nil {
		return nil, fmt.Errorf("read rule fixture %q: %w", name, err)
	}
	var definition rule.RuleDefinition
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&definition); err != nil {
		return nil, fmt.Errorf("decode rule fixture %q: %w", name, err)
	}
	if err := definition.Validate(); err != nil {
		return nil, fmt.Errorf("validate rule fixture %q: %w", name, err)
	}
	return &definition, nil
}

// fixtureName rejects anything that could reach outside the fixture tree.
func fixtureName(name string) (string, error) {
	slashed := filepath.ToSlash(name)
	cleaned := path.Clean(slashed)
	if cleaned != slashed || strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "..") ||
		!strings.HasSuffix(cleaned, ".json") || strings.Count(cleaned, "/") != 1 {
		return "", fmt.Errorf("invalid rule fixture name %q; want \"<group>/<file>.json\"", name)
	}
	return cleaned, nil
}

func ruleFixtureRoot() (string, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("locate ruletest package source")
	}
	return filepath.Join(filepath.Dir(source), "..", "testdata", "rules"), nil
}

// LoadManifest loads one shared dataset manifest fixture by dataset name, such
// as "attendance.days", and validates it.
func LoadManifest(name string) (dataset.Manifest, error) {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return dataset.Manifest{}, fmt.Errorf("invalid dataset fixture name %q", name)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return dataset.Manifest{}, fmt.Errorf("locate ruletest package source")
	}
	path := filepath.Join(filepath.Dir(source), "..", "testdata", "datasets", name+".json")
	data, err := os.ReadFile(path) //nolint:gosec // The sanitized name is confined to repository fixtures.
	if err != nil {
		return dataset.Manifest{}, fmt.Errorf("read dataset fixture %q: %w", name, err)
	}
	var manifest dataset.Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return dataset.Manifest{}, fmt.Errorf("decode dataset fixture %q: %w", name, err)
	}
	if err := manifest.Validate(); err != nil {
		return dataset.Manifest{}, fmt.Errorf("validate dataset fixture %q: %w", name, err)
	}
	return manifest, nil
}

// LoadManifests loads several manifests, keyed by dataset name, ready to hand
// to fact.NewResolver.
func LoadManifests(names ...string) (map[string]dataset.Manifest, error) {
	manifests := make(map[string]dataset.Manifest, len(names))
	for _, name := range names {
		manifest, err := LoadManifest(name)
		if err != nil {
			return nil, err
		}
		manifests[name] = manifest
	}
	return manifests, nil
}

// LoadRegistry loads shared field-registry fixtures, such as "leave", and
// validates them into an immutable registry.
func LoadRegistry(domains ...string) (*registry.Registry, error) {
	fields, err := LoadRegistryFields(domains...)
	if err != nil {
		return nil, err
	}
	return registry.NewRegistry(fields)
}

// LoadRegistryFields loads shared field-registry fixtures without building a
// registry, for a caller assembling a store bundle from its parts.
func LoadRegistryFields(domains ...string) ([]registry.FieldDefinition, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("locate ruletest package source")
	}
	var fields []registry.FieldDefinition
	for _, domain := range domains {
		if domain == "" || filepath.Base(domain) != domain || strings.ContainsAny(domain, `/\.`) {
			return nil, fmt.Errorf("invalid registry fixture name %q", domain)
		}
		path := filepath.Join(filepath.Dir(source), "..", "testdata", "registry", domain+"-fields.json")
		data, err := os.ReadFile(path) //nolint:gosec // The sanitized name is confined to repository fixtures.
		if err != nil {
			return nil, fmt.Errorf("read registry fixture %q: %w", domain, err)
		}
		var fixture struct {
			Fields []registry.FieldDefinition `json:"fields"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&fixture); err != nil {
			return nil, fmt.Errorf("decode registry fixture %q: %w", domain, err)
		}
		fields = append(fields, fixture.Fields...)
	}
	return fields, nil
}

// Budget returns a fact budget sized for tests: generous enough that a test
// does not trip it by accident, small enough that a test can trip it on
// purpose by asking for more.
func Budget() fact.Budget {
	return fact.Budget{
		MaxDatasetFacts:  4,
		PerFactTimeout:   2 * time.Second,
		TotalFactTimeout: 10 * time.Second,
		MaxRowsScanned:   500,
	}
}
