package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/engine/decisiontable"
	"github.com/arrowhr/ruleengine/engine/testengine"
	"github.com/arrowhr/ruleengine/rule"
)

type goldenFixture struct {
	Name     string         `json:"name"`
	Requires []string       `json:"requires"`
	Logic    rule.RuleLogic `json:"logic"`
	Input    engine.Input   `json:"input"`
	Expected engine.Output  `json:"expected"`
}

func TestGoldenConformance(t *testing.T) {
	root := filepath.Join("..", "testdata", "golden")
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".json" {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no golden fixtures found")
	}
	implementations := []engine.Engine{testengine.New(), decisiontable.New()}
	for _, path := range paths {
		fixture := readFixture(t, path)
		for _, implementation := range implementations {
			t.Run(fixture.Name+"/"+implementation.Name(), func(t *testing.T) {
				for _, requirement := range fixture.Requires {
					supported, known := supports(implementation.Capabilities(), requirement)
					if !known {
						// A misspelt requirement would otherwise skip on every
						// engine and read as a pass, which is exactly the
						// silent hole the conformance suite exists to close.
						t.Fatalf("fixture %s requires unknown capability %q", fixture.Name, requirement)
					}
					if !supported {
						t.Skipf("%s explicitly lacks %s capability", implementation.Name(), requirement)
					}
				}
				output, err := implementation.Evaluate(context.Background(), fixture.Logic, fixture.Input)
				if err != nil {
					t.Fatal(err)
				}
				if canonicalJSON(t, output.Result) != canonicalJSON(t, fixture.Expected.Result) || !reflect.DeepEqual(output.MatchedRows, fixture.Expected.MatchedRows) {
					t.Fatalf("golden mismatch\nwant %#v\n got %#v", fixture.Expected, output)
				}
				if fixture.Input.Trace && output.Trace == nil {
					t.Fatal("trace requested but absent")
				}
			})
		}
	}
}

func TestSharedOperatorConformance(t *testing.T) {
	tests := []struct {
		name, condition string
		actual          any
		facts           map[string]any
	}{
		{"equal", "== 1.20", json.Number("1.2"), nil},
		{"not equal", "!= B", "A", nil},
		{"greater", "> -2", -1, nil},
		{"greater equal", ">= 2", 2, nil},
		{"less", "< 3", 2, nil},
		{"less equal", "<= 3", 3, nil},
		{"in", "in [A,B]", "B", nil},
		{"not in", "notIn [A,B]", "C", nil},
		{"between lower", "between 1 and 5", 1, nil},
		{"between upper", "between 1 and 5", 5, nil},
		{"field reference", ">= $minimum", 6, map[string]any{"minimum": 6}},
	}
	for _, implementation := range []engine.Engine{testengine.New(), decisiontable.New()} {
		for _, test := range tests {
			t.Run(implementation.Name()+"/"+test.name, func(t *testing.T) {
				facts := map[string]any{"value": test.actual}
				for key, value := range test.facts {
					facts[key] = value
				}
				logic := rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{ID: "match", When: map[string]string{"value": test.condition}, Then: map[string]any{"matched": true}}}}
				output, err := implementation.Evaluate(context.Background(), logic, engine.Input{Facts: facts})
				if err != nil || !reflect.DeepEqual(output.MatchedRows, []string{"match"}) {
					t.Fatalf("output = %#v, error = %v", output, err)
				}
			})
		}
	}
}

func readFixture(t *testing.T, path string) goldenFixture {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var fixture goldenFixture
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return fixture
}

// supports reports whether an engine offers a fixture's required capability,
// and whether the requirement is a name this suite recognises at all.
func supports(capabilities engine.Capabilities, requirement string) (supported, known bool) {
	switch requirement {
	case "arithmetic":
		return capabilities.Arithmetic, true
	case "fieldRefs":
		return capabilities.FieldRefs, true
	case "trace":
		return capabilities.Trace, true
	case "decimal":
		return capabilities.Decimal, true
	case "first":
		return hasPolicy(capabilities.HitPolicies, rule.HitFirst), true
	case "priority":
		return hasPolicy(capabilities.HitPolicies, rule.HitPriority), true
	case "collect":
		return hasPolicy(capabilities.HitPolicies, rule.HitCollect), true
	default:
		return false, false
	}
}

func hasPolicy(policies []rule.HitPolicy, wanted rule.HitPolicy) bool {
	for _, policy := range policies {
		if policy == wanted {
			return true
		}
	}
	return false
}

func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(fmt.Errorf("canonical JSON: %w", err))
	}
	return string(encoded)
}
