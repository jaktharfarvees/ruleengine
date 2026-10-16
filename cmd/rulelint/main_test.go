package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRoot is the repository's committed fixture tree, two directories up.
const fixtureRoot = "../../testdata"

// TestCatchesEveryInvalidFixture is the phase exit criterion, as a test rather
// than something somebody remembers to run.
//
// Each file in testdata/invalid is a mistake that would otherwise reach
// production as a fail-closed decision, so every one of them has to be caught
// here. The registry is included because the cross-artifact checks are where
// some of these mistakes live.
func TestCatchesEveryInvalidFixture(t *testing.T) {
	invalid := filepath.Join(fixtureRoot, "invalid")
	entries, err := os.ReadDir(invalid)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		checked++
		t.Run(entry.Name(), func(t *testing.T) {
			report := lint(fixtureRoot, []string{
				filepath.Join(invalid, entry.Name()),
				filepath.Join(fixtureRoot, "registry"),
			})
			if len(report.problems) == 0 {
				t.Fatalf("%s was accepted; warnings were %v", entry.Name(), report.warnings)
			}
			// The report has to name the file, or an operator cannot act on it.
			if !strings.Contains(strings.Join(report.problems, "\n"), entry.Name()) {
				t.Fatalf("the problem does not name the file: %v", report.problems)
			}
		})
	}
	if checked < 3 {
		t.Fatalf("found %d invalid fixtures, want at least 3", checked)
	}
}

// TestValidFixturesPass pins the other half: the committed tree must lint
// clean, or the tool is unusable as a CI gate.
func TestValidFixturesPass(t *testing.T) {
	report := lint(fixtureRoot, nil)
	if len(report.problems) != 0 {
		t.Fatalf("the committed fixtures reported problems: %v", report.problems)
	}
	if report.rules != 11 || report.datasets != 4 || report.hierarchies != 5 || report.fields == 0 {
		t.Fatalf("counted %d rules, %d datasets, %d hierarchies, %d fields",
			report.rules, report.datasets, report.hierarchies, report.fields)
	}
}

// TestWarnsAboutTheDeliberateTiePair pins the warning worth never ignoring:
// two equally specific rules at one anchor would fail every matching decision
// closed, and publish time is where that has to be caught.
func TestWarnsAboutTheDeliberateTiePair(t *testing.T) {
	report := lint(fixtureRoot, nil)
	joined := strings.Join(report.warnings, "\n")
	if !strings.Contains(joined, "equally specific") || !strings.Contains(joined, "fail closed") {
		t.Fatalf("the tie pair was not reported: %v", report.warnings)
	}
}

// TestInfersEachArtifactKind pins that a caller can point the tool at any file
// without saying what it is.
func TestInfersEachArtifactKind(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{filepath.Join(fixtureRoot, "rules", "leave", "eligibility-min-service-root.json"), "rule"},
		{filepath.Join(fixtureRoot, "registry", "leave-fields.json"), "registry"},
		{filepath.Join(fixtureRoot, "datasets", "leave.balances.json"), "dataset"},
		{filepath.Join(fixtureRoot, "hierarchy", "rwb.json"), "hierarchy"},
		{filepath.Join(fixtureRoot, "golden", "leave", "eligibility-min-service.json"), "golden"},
	}
	for _, test := range tests {
		t.Run(filepath.Base(test.path), func(t *testing.T) {
			data, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := inferKind(data); got != test.want {
				t.Fatalf("inferKind = %q, want %q", got, test.want)
			}
		})
	}
	// A bare array of field definitions is the other registry form.
	if got := inferKind([]byte(`[{"name":"x","domain":"d","dataType":"string"}]`)); got != "registry" {
		t.Fatalf("a bare field array inferred as %q", got)
	}
	// Anything else is refused rather than guessed at.
	for _, data := range []string{`{}`, `{"unrelated":1}`, `"text"`, `42`} {
		if got := inferKind([]byte(data)); got != "" {
			t.Fatalf("inferKind(%s) = %q, want no kind", data, got)
		}
	}
}

// TestReportsUnusableInput pins that the tool explains itself rather than
// failing obscurely.
func TestReportsUnusableInput(t *testing.T) {
	directory := t.TempDir()
	broken := filepath.Join(directory, "broken.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(directory, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"something":"else"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	report := lint(directory, []string{broken, unknown, filepath.Join(directory, "absent.json")})
	joined := strings.Join(report.problems, "\n")
	if !strings.Contains(joined, "not valid JSON") {
		t.Fatalf("invalid JSON was not reported: %v", report.problems)
	}
	if !strings.Contains(joined, "is not a rule") {
		t.Fatalf("an unknown artifact was not reported: %v", report.problems)
	}
	// A missing standard directory is not a problem — a service may have no
	// datasets — but a file the operator named explicitly is.
	if !strings.Contains(joined, "absent.json") {
		t.Fatalf("a named missing file was not reported: %v", report.problems)
	}
}

// TestRejectsRulesTheRegistryDoesNotSupport covers the cross-artifact checks,
// which catch the mistakes that only exist between two valid files.
func TestRejectsRulesTheRegistryDoesNotSupport(t *testing.T) {
	directory := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	registryPath := write("registry.json", `{"fields":[
		{"name":"serviceMonths","domain":"leave","dataType":"integer","pii":false,
		 "ownerService":"leave","status":"active","role":"input","effective":{}},
		{"name":"eligible","domain":"leave","dataType":"boolean","pii":false,
		 "ownerService":"leave","status":"active","role":"output","effective":{}}
	]}`)

	rule := func(inputs, outputs string) string {
		return `{
			"ruleKey":"urn:arrowhr:rule:leave:eligibility:min-service:v1",
			"domain":"leave","ruleSet":"eligibility","name":"min-service",
			"version":"1.0.0","changeType":"MAJOR","status":"active","tenant":"RWB",
			"scope":{"anchor":{"kind":"node","nodeId":"G001"}},
			"effective":{"validFrom":"2026-01-01T00:00:00Z"},
			"declaredInputs":` + inputs + `,"declaredOutputs":` + outputs + `,
			"decisionType":"leave-eligibility",
			"logic":{"hitPolicy":"first","rows":[
				{"id":"r","when":{},"then":{"eligible":true}}]},
			"engineRef":{"engine":"decisiontable"},
			"metadata":{"owner":"o","createdBy":"c","createdAt":"2026-01-01T00:00:00Z"}
		}`
	}

	tests := []struct {
		name, rule, want string
	}{
		{"input nobody registered", rule(`["notAField"]`, `["eligible"]`), "no registry defines"},
		{"output nobody registered", rule(`["serviceMonths"]`, `["eligible","notAField"]`), "no registry defines"},
		{"input used as an output", strings.ReplaceAll(
			rule(`[]`, `["serviceMonths"]`), `"eligible":true`, `"serviceMonths":"1"`), "registered as"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rulePath := write("rule.json", test.rule)
			report := lint(directory, []string{rulePath, registryPath})
			joined := strings.Join(report.problems, "\n")
			if !strings.Contains(joined, test.want) {
				t.Fatalf("problems = %v, want one mentioning %q", report.problems, test.want)
			}
		})
	}
}

// TestWarnsWithoutARegistry pins that skipping the cross-checks is said out
// loud, so nobody reads a clean run as more assurance than it is.
func TestWarnsWithoutARegistry(t *testing.T) {
	report := lint(fixtureRoot, []string{filepath.Join(fixtureRoot, "rules", "leave")})
	joined := strings.Join(report.warnings, "\n")
	if !strings.Contains(joined, "no field registry") {
		t.Fatalf("warnings = %v, want one about the missing registry", report.warnings)
	}
	if len(report.problems) != 0 {
		t.Fatalf("valid rules reported problems: %v", report.problems)
	}
}
