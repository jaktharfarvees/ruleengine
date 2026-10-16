package schema

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

func TestEmbeddedSchemas(t *testing.T) {
	want := []string{DatasetManifest, EvaluationRequest, EvaluationResponse, FieldRegistry, OrgHierarchy, RuleDefinition, RunManifest}
	got := Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("Names()[%d] = %q, want %q", index, got[index], want[index])
		}
		data, err := Bytes(got[index])
		if err != nil || !json.Valid(data) {
			t.Fatalf("Bytes(%q) returned invalid JSON: %v", got[index], err)
		}
	}
	if _, err := Bytes("missing.json"); err == nil {
		t.Fatal("unknown schema was accepted")
	}
}

// TestRuleFixtureRoundTripAndValidation covers every committed rule fixture.
// The fixtures are the shared contract with Rule Studio, so each one must pass
// schema validation, shape validation and a JSON round trip; validating only
// one of them would let the rest drift.
func TestRuleFixtureRoundTripAndValidation(t *testing.T) {
	root := filepath.Join("..", "testdata", "rules")
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".json" {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	// The fixture set is named in the build spec; an accidental deletion must
	// fail rather than quietly shrink the contract.
	if len(files) < 11 {
		t.Fatalf("found %d rule fixtures, want at least 11", len(files))
	}
	for _, file := range files {
		t.Run(filepath.ToSlash(strings.TrimPrefix(file, root+string(filepath.Separator))), func(t *testing.T) {
			data, err := os.ReadFile(file) //nolint:gosec // The path comes from a walk of a repository-owned fixture tree.
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(RuleDefinition, data); err != nil {
				t.Fatalf("schema validation failed: %v", err)
			}
			var definition rule.RuleDefinition
			if err := json.Unmarshal(data, &definition); err != nil {
				t.Fatal(err)
			}
			if err := definition.Validate(); err != nil {
				t.Fatalf("rule validation failed: %v", err)
			}
			roundTrip, err := json.Marshal(definition)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate("v2/"+RuleDefinition, roundTrip); err != nil {
				t.Fatalf("round-trip schema validation failed: %v", err)
			}
		})
	}
}

func TestInvalidRuleFixtures(t *testing.T) {
	tests := []struct {
		name, file string
		shape      bool
	}{
		{name: "missing anchor value", file: "no-anchor.json", shape: true},
		{name: "authored precedence", file: "precedence-field-present.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "testdata", "invalid", tt.file)) //nolint:gosec // Table entries are repository-owned fixtures.
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(RuleDefinition, data); err == nil {
				t.Fatal("schema accepted invalid rule")
			}
			if tt.shape {
				var definition rule.RuleDefinition
				if err := json.Unmarshal(data, &definition); err != nil {
					t.Fatal(err)
				}
				if err := definition.Validate(); err == nil {
					t.Fatal("rule.Validate accepted invalid shape")
				}
			}
		})
	}
}

func TestSchemaExamples(t *testing.T) {
	tests := []struct{ name, document string }{
		{EvaluationRequest, `{"schemaVersion":2,"tenant":{"id":"RWB"},"routing":{"domain":"leave","ruleSet":"eligibility","name":"min-service"},"subject":{"type":"employee","identifier":{"system":"hr","value":"E1","idType":"employeeId"},"nodeId":"BR001"},"asOf":"2026-10-14T08:00:00Z"}`},
		{EvaluationResponse, `{"decisionId":"d1","reasonCodes":["OK"],"failed":false,"asOf":"2026-10-14T08:00:00Z","validAt":"2026-10-14T08:00:00Z","libVersion":"0.0.0-dev"}`},
		{FieldRegistry, `{"fields":[{"name":"eligible","domain":"leave","dataType":"boolean","pii":false,"ownerService":"leave","status":"active","role":"output","effective":{}}]}`},
		{DatasetManifest, `{"dataset":"leave.applications","ownerService":"leave","domain":"leave","grain":"application","tenantScoped":true,"status":"published","version":"1","publishedAt":"2026-01-01T00:00:00Z","maxWindowDays":366,"aggregates":["sum"],"columns":[{"name":"tenantId","type":"string","filterable":true,"aggregatable":false,"windowable":false,"pii":false}]}`},
		{OrgHierarchy, `{"types":[{"typeId":"group","typeName":"Group","order":0,"tenant":"RWB","status":"ACTIVE"}],"nodes":[{"nodeId":"G001","tenant":"RWB","typeId":"group","name":"Root","status":"ACTIVE"}]}`},
		{RunManifest, `{"runId":"run-1","tenant":"RWB","validAt":"2026-10-14T08:00:00Z","rulePins":{},"factPins":{},"status":"open","openedAt":"2026-10-14T08:00:00Z"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(tt.name, []byte(tt.document)); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestSchemaRejectsMalformedValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "malformed JSON", value: []byte(`{"schemaVersion":`)},
		{name: "trailing JSON", value: []byte(`{} {}`)},
		{name: "unknown property", value: []byte(`{"schemaVersion":2,"extra":true}`)},
		{name: "wrong integer", value: []byte(`{"schemaVersion":2.5}`)},
		{name: "bad date", value: []byte(`{"schemaVersion":2,"tenant":{"id":"t"},"routing":{"domain":"d","ruleSet":"r","name":"n"},"subject":{"type":"employee","identifier":{"system":"s","value":"v","idType":"id"}},"asOf":"today"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(EvaluationRequest, tt.value); err == nil {
				t.Fatal("invalid value was accepted")
			}
		})
	}
}

func TestHierarchyFixturesMatchSchema(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "testdata", "hierarchy"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			fixture := filepath.Join("..", "testdata", "hierarchy", entry.Name())
			data, err := os.ReadFile(fixture) //nolint:gosec // Directory entries come from repository-owned fixtures.
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(OrgHierarchy, data); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestRegistryFixturesMatchSchemaAndModel(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "testdata", "registry"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			fixture := filepath.Join("..", "testdata", "registry", entry.Name())
			data, err := os.ReadFile(fixture) //nolint:gosec // Directory entries come from repository-owned fixtures.
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(FieldRegistry, data); err != nil {
				t.Fatalf("schema validation failed: %v", err)
			}
			var envelope struct {
				Fields []registry.FieldDefinition `json:"fields"`
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.NewRegistry(envelope.Fields); err != nil {
				t.Fatalf("registry model validation failed: %v", err)
			}
		})
	}
}

// TestDatasetFixturesMatchSchemaAndModel pins the dataset manifests against
// both the published schema and the Go type. A manifest that satisfies one but
// not the other would pass authoring and fail at evaluation, or the reverse.
func TestDatasetFixturesMatchSchemaAndModel(t *testing.T) {
	root := filepath.Join("..", "testdata", "datasets")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		seen++
		t.Run(entry.Name(), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, entry.Name())) //nolint:gosec // A repository-owned fixture tree.
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(DatasetManifest, data); err != nil {
				t.Fatalf("schema validation failed: %v", err)
			}
			var manifest dataset.Manifest
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&manifest); err != nil {
				t.Fatal(err)
			}
			if err := manifest.Validate(); err != nil {
				t.Fatalf("model validation failed: %v", err)
			}
			// The file name is the dataset identifier, so a fact declaring a
			// dataset can be routed to its manifest by name alone.
			if want := strings.TrimSuffix(entry.Name(), ".json"); manifest.Dataset != want {
				t.Fatalf("manifest declares %q but is filed as %q", manifest.Dataset, want)
			}
			roundTrip, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(DatasetManifest, roundTrip); err != nil {
				t.Fatalf("round-trip schema validation failed: %v", err)
			}
		})
	}
	if seen < 4 {
		t.Fatalf("found %d dataset fixtures, want at least 4", seen)
	}
}
