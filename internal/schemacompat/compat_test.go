package schemacompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAllowsOnlyOptionalStructuralAdditions(t *testing.T) {
	baseline := schema(`{
  "$id":"urn:test:v2",
  "type":"object",
  "additionalProperties":false,
  "required":["id"],
  "properties":{"id":{"type":"string"}},
  "$defs":{"state":{"type":"string","enum":["active"]}}
}`)
	current := schema(`{
  "$id":"urn:test:v2",
  "title":"Updated documentation",
  "type":"object",
  "additionalProperties":false,
  "required":["id"],
  "properties":{"id":{"type":"string"},"note":{"type":"string","minLength":1}},
  "$defs":{"state":{"type":"string","enum":["active"]},"tag":{"type":"string"}}
}`)
	violations, err := Check("test.json", baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("optional additions were rejected: %v", violations)
	}
}

func TestCheckRejectsIncompatibleChanges(t *testing.T) {
	baseline := schema(`{
  "$id":"urn:test:v2",
  "type":"object",
  "additionalProperties":false,
  "required":["id"],
  "properties":{
    "id":{"type":"string","minLength":1},
    "status":{"type":"string","enum":["active","retired"]},
    "nested":{"type":"object","properties":{"enabled":{"type":"boolean"}}}
  }
}`)
	tests := []struct {
		name, current, want string
	}{
		{"changed id", `{"$id":"urn:test:v3","type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","minLength":1},"status":{"type":"string","enum":["active","retired"]},"nested":{"type":"object","properties":{"enabled":{"type":"boolean"}}}}}`, "$.$id"},
		{"removed property", `{"$id":"urn:test:v2","type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","minLength":1},"nested":{"type":"object","properties":{"enabled":{"type":"boolean"}}}}}`, "properties.status"},
		{"new required property", `{"$id":"urn:test:v2","type":"object","additionalProperties":false,"required":["id","name"],"properties":{"id":{"type":"string","minLength":1},"name":{"type":"string"},"status":{"type":"string","enum":["active","retired"]},"nested":{"type":"object","properties":{"enabled":{"type":"boolean"}}}}}`, "required field \"name\" was added"},
		{"removed required marker", `{"$id":"urn:test:v2","type":"object","additionalProperties":false,"properties":{"id":{"type":"string","minLength":1},"status":{"type":"string","enum":["active","retired"]},"nested":{"type":"object","properties":{"enabled":{"type":"boolean"}}}}}`, "required field \"id\" was removed"},
		{"changed nested type", `{"$id":"urn:test:v2","type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","minLength":1},"status":{"type":"string","enum":["active","retired"]},"nested":{"type":"object","properties":{"enabled":{"type":"string"}}}}}`, "properties.nested.properties.enabled.type"},
		{"changed enum", `{"$id":"urn:test:v2","type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","minLength":1},"status":{"type":"string","enum":["active","retired","draft"]},"nested":{"type":"object","properties":{"enabled":{"type":"boolean"}}}}}`, "published enum changed"},
		{"tightened constraint", `{"$id":"urn:test:v2","type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","minLength":2},"status":{"type":"string","enum":["active","retired"]},"nested":{"type":"object","properties":{"enabled":{"type":"boolean"}}}}}`, "properties.id.minLength"},
		{"opened object", `{"$id":"urn:test:v2","type":"object","additionalProperties":true,"required":["id"],"properties":{"id":{"type":"string","minLength":1},"status":{"type":"string","enum":["active","retired"]},"nested":{"type":"object","properties":{"enabled":{"type":"boolean"}}}}}`, "additionalProperties"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations, err := Check("test.json", baseline, schema(tt.current))
			if err != nil {
				t.Fatal(err)
			}
			if len(violations) == 0 {
				t.Fatal("incompatible change was accepted")
			}
			joined := violationsText(violations)
			if !strings.Contains(joined, tt.want) {
				t.Fatalf("violations %q do not contain %q", joined, tt.want)
			}
		})
	}
}

func TestCheckMatchesReorderedSchemaAlternatives(t *testing.T) {
	baseline := schema(`{"anyOf":[{"type":"string"},{"type":"integer"}]}`)
	current := schema(`{"anyOf":[{"type":"integer"},{"type":"string"}]}`)
	violations, err := Check("union.json", baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("alternative reordering was rejected: %v", violations)
	}
}

func TestCheckRejectsChangedConditional(t *testing.T) {
	baseline := schema(`{"type":"object","if":{"properties":{"kind":{"const":"derived"}}},"then":{"required":["expression"]}}`)
	current := schema(`{"type":"object","if":{"properties":{"kind":{"const":"native"}}},"then":{"required":["expression"]}}`)
	violations, err := Check("conditional.json", baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) == 0 || !strings.Contains(violationsText(violations), "$.if.properties.kind.const") {
		t.Fatalf("changed conditional violations = %v", violations)
	}
}

func TestCheckDirectoriesRejectsRemovedSchema(t *testing.T) {
	baseline := t.TempDir()
	current := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseline, "published.json"), schema(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	violations, err := CheckDirectories(baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || !strings.Contains(violations[0].Reason, "removed") {
		t.Fatalf("removed schema violations = %v", violations)
	}
}

func TestCheckSnapshotsIsTransitive(t *testing.T) {
	root := t.TempDir()
	current := t.TempDir()
	initial := filepath.Join(root, "initial")
	second := filepath.Join(root, "second")
	for _, directory := range []string{initial, second} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(directory, document string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, "contract.json"), schema(document), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(initial, `{"type":"object","properties":{"id":{"type":"string"}}}`)
	write(second, `{"type":"object","properties":{"id":{"type":"string"},"note":{"type":"string"}}}`)
	write(current, `{"type":"object","properties":{"id":{"type":"string"}}}`)

	violations, err := CheckSnapshots(root, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Schema != "second/contract.json" || !strings.Contains(violations[0].Error(), "properties.note") {
		t.Fatalf("transitive violations = %v", violations)
	}
}

func TestCheckRejectsInvalidDocuments(t *testing.T) {
	if _, err := Check("bad.json", []byte(`{`), schema(`{}`)); err == nil {
		t.Fatal("invalid baseline was accepted")
	}
	if _, err := Check("bad.json", schema(`{}`), []byte(`[]`)); err == nil {
		t.Fatal("non-object current schema was accepted")
	}
}

func schema(document string) []byte { return []byte(document) }

func violationsText(violations []Violation) string {
	parts := make([]string, len(violations))
	for index, violation := range violations {
		parts[index] = violation.Error()
	}
	return strings.Join(parts, "\n")
}
