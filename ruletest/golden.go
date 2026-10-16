package ruletest

import (
	"bytes"
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
	"github.com/arrowhr/ruleengine/rule"
)

// GoldenCase is one recorded expectation about a rule's logic: these facts in,
// that result out.
//
// A golden case is the artifact a business owner can actually check. It names
// no engine and no Go type, so the same file proves the rule to a service
// team, to Rule Studio, and to whoever asks in a year why the number was what
// it was.
type GoldenCase struct {
	// Name describes the case in the words of whoever owns the rule.
	Name string `json:"name"`
	// Requires lists engine capabilities the case needs, so a case can be
	// skipped explicitly on an engine that lacks one rather than silently
	// passing. Known values: first, priority, collect, arithmetic, fieldRefs,
	// trace, decimal.
	Requires []string `json:"requires,omitempty"`
	// Logic is the decision table under test.
	Logic rule.RuleLogic `json:"logic"`
	// DeclaredInputs and DeclaredOutputs mirror the rule's contract, so a
	// case also proves the rule produces everything it promised.
	DeclaredInputs  []string `json:"declaredInputs,omitempty"`
	DeclaredOutputs []string `json:"declaredOutputs,omitempty"`
	// Input is what the rule sees.
	Input engine.Input `json:"input"`
	// Expected is what it must produce.
	Expected engine.Output `json:"expected"`
}

// RunGolden runs every golden case under dir against the production engine.
//
// A service team points this at its own golden directory and gets one sub-test
// per case. It is the cheapest way to keep a rule honest: a change to the
// rule, the engine or the expression grammar that alters an outcome fails here
// with the case's own name.
func RunGolden(t testing.TB, dir string) {
	RunGoldenOn(t, dir, decisiontable.New())
}

// RunGoldenOn runs every golden case under dir against specific engines, which
// is how the library proves its reference and production engines agree.
func RunGoldenOn(t testing.TB, dir string, engines ...engine.Engine) {
	if len(engines) == 0 {
		t.Error("RunGoldenOn needs at least one engine")
		return
	}
	cases, err := LoadGolden(dir)
	if err != nil {
		t.Errorf("load golden cases: %v", err)
		return
	}
	if len(cases) == 0 {
		// An empty golden directory is almost always a wrong path rather than
		// a rule nobody has examples for.
		t.Errorf("no golden cases found under %s", dir)
		return
	}

	runner, hasSubtests := t.(interface {
		Run(string, func(*testing.T)) bool
	})
	for _, golden := range cases {
		for _, implementation := range engines {
			name := golden.Name + "/" + implementation.Name()
			if !hasSubtests {
				checkGolden(t, golden, implementation)
				continue
			}
			runner.Run(name, func(t *testing.T) { checkGolden(t, golden, implementation) })
		}
	}
}

// LoadGolden reads every golden case under dir, ordered by file path so a run
// is reproducible.
func LoadGolden(dir string) ([]GoldenCase, error) {
	var paths []string
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".json" {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk golden directory %q: %w", dir, err)
	}
	sort.Strings(paths)

	cases := make([]GoldenCase, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // The path comes from a walk the caller asked for.
		if err != nil {
			return nil, fmt.Errorf("read golden case %q: %w", path, err)
		}
		var golden GoldenCase
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		// Numbers stay exact: a golden case about money must not be compared
		// through binary floating point.
		decoder.UseNumber()
		if err := decoder.Decode(&golden); err != nil {
			return nil, fmt.Errorf("decode golden case %q: %w", path, err)
		}
		if golden.Name == "" {
			golden.Name = filepath.Base(path)
		}
		cases = append(cases, golden)
	}
	return cases, nil
}

func checkGolden(t testing.TB, golden GoldenCase, implementation engine.Engine) {
	capabilities := implementation.Capabilities()
	for _, requirement := range golden.Requires {
		supported, known := supports(capabilities, requirement)
		if !known {
			// A misspelt requirement would otherwise skip everywhere and read
			// as a pass, which is the one thing a golden suite must not do.
			t.Errorf("case %q requires unknown capability %q", golden.Name, requirement)
			return
		}
		if !supported {
			if skipper, ok := t.(interface{ Skipf(string, ...any) }); ok {
				skipper.Skipf("%s does not support %s", implementation.Name(), requirement)
				return
			}
			return
		}
	}

	output, err := implementation.Evaluate(context.Background(), golden.Logic, golden.Input)
	if err != nil {
		t.Errorf("case %q: engine %s refused the logic: %v", golden.Name, implementation.Name(), err)
		return
	}
	if diff := diffResults(golden.Expected.Result, output.Result); diff != "" {
		t.Errorf("case %q on %s: result differs\n%s", golden.Name, implementation.Name(), diff)
	}
	if len(golden.Expected.MatchedRows) > 0 && !reflect.DeepEqual(golden.Expected.MatchedRows, output.MatchedRows) {
		t.Errorf("case %q on %s: matched rows = %v, want %v",
			golden.Name, implementation.Name(), output.MatchedRows, golden.Expected.MatchedRows)
	}
	// A case also proves the rule keeps its own promise: everything it
	// declared as an output has to be there.
	for _, declared := range golden.DeclaredOutputs {
		if _, produced := output.Result[declared]; !produced {
			t.Errorf("case %q on %s: declared output %q was not produced",
				golden.Name, implementation.Name(), declared)
		}
	}
	if golden.Input.Trace && output.Trace == nil {
		t.Errorf("case %q on %s: a trace was requested but none was produced",
			golden.Name, implementation.Name())
	}
}

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
