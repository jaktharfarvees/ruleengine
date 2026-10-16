package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureRoot = "../../testdata"

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }

func requestPath(name string) string {
	return filepath.Join(fixtureRoot, "requests", name)
}

// TestExplainsAnRT01Outcome is the phase exit criterion: the tool has to
// explain, from the command line, why a deeper anchor won.
//
// RT01 is the behaviour the whole precedence model rests on, so the report has
// to name the winner, the anchor it won at, the subject's path, and for the
// loser the reason it lost. A report that only said "eligible: true" would not
// answer the question anybody actually asks.
func TestExplainsAnRT01Outcome(t *testing.T) {
	var out bytes.Buffer
	err := run(&out, fixtureRoot, requestPath("leave-eligibility-br001.json"), "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	report := out.String()
	for _, want := range []string{
		"1.1.0",                         // the version that won
		"resolved at C002",              // the anchor it won at
		"BR001 -> D002 -> C002 -> G001", // the subject's path, deepest first
		"WINNER",                        // which candidate won
		"eliminated",                    // and that the other did not
		"Company(1) outranks Group(0)",  // why, in the tenant's own level names
		"eligible=true",                 // what it decided
		"joinDate",                      // and where the facts came from
		"serviceMonths",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q:\n%s", want, report)
		}
	}
}

// TestResolvesDatasetFactsFromRows pins the offline fact path: without rows a
// dataset field falls back to its declared onEmpty, and with them it reads the
// value, which is the difference an operator is usually trying to see.
func TestResolvesDatasetFactsFromRows(t *testing.T) {
	rows := filepath.Join(fixtureRoot, "facts", "leave-balances.json")

	var withRows bytes.Buffer
	if err := run(&withRows, fixtureRoot, requestPath("leave-balance-br001.json"), rows, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withRows.String(), "allowed=true") {
		t.Fatalf("three days against a 12.5 balance was refused:\n%s", withRows.String())
	}
	if !strings.Contains(withRows.String(), "availableBalance") {
		t.Fatalf("the dataset fact was not reported:\n%s", withRows.String())
	}

	// With no rows the field's onEmpty of zero applies, so the same request
	// is refused by the rule rather than failing.
	var withoutRows bytes.Buffer
	if err := run(&withoutRows, fixtureRoot, requestPath("leave-balance-br001.json"), "", false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withoutRows.String(), "allowed=false") {
		t.Fatalf("an empty dataset did not fall back to onEmpty:\n%s", withoutRows.String())
	}
}

// TestFailedDecisionIsDistinguishedFromAToolFailure pins the exit contract: a
// decision that failed closed is a result, not a crash, so a script can tell
// "the rules said no" from "the tool could not run".
func TestFailedDecisionIsDistinguishedFromAToolFailure(t *testing.T) {
	// A subject at a node the hierarchy does not contain fails closed.
	data, err := readFixtureRequest(t, "leave-eligibility-br001.json")
	if err != nil {
		t.Fatal(err)
	}
	data["subject"].(map[string]any)["nodeId"] = "NOPE"
	path := writeTempRequest(t, data)

	var out bytes.Buffer
	runErr := run(&out, fixtureRoot, path, "", false, false)
	if !errors.Is(runErr, ErrDecisionFailed) {
		t.Fatalf("run = %v, want ErrDecisionFailed", runErr)
	}
	// It still explains itself, and says what to check first.
	if !strings.Contains(out.String(), "SUBJECT_NODE_UNRESOLVABLE") {
		t.Fatalf("the failure was not explained:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "unknown in this tenant") {
		t.Fatalf("the report gave no hint:\n%s", out.String())
	}

	// A tool failure is a different error entirely.
	for _, broken := range []struct{ name, root, request string }{
		{"no request", fixtureRoot, ""},
		{"missing request", fixtureRoot, filepath.Join(fixtureRoot, "requests", "absent.json")},
		{"missing root", filepath.Join(fixtureRoot, "absent"), requestPath("leave-eligibility-br001.json")},
	} {
		t.Run(broken.name, func(t *testing.T) {
			err := run(&bytes.Buffer{}, broken.root, broken.request, "", false, false)
			if err == nil || errors.Is(err, ErrDecisionFailed) {
				t.Fatalf("run = %v, want a tool failure", err)
			}
		})
	}
}

// TestJSONOutputIsADecision pins the machine-readable form, which is what a
// script asserting an outcome reads.
func TestJSONOutputIsADecision(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out, fixtureRoot, requestPath("leave-eligibility-br001.json"), "", true, true); err != nil {
		t.Fatal(err)
	}
	var decision struct {
		DecisionID  string   `json:"decisionId"`
		RuleVersion string   `json:"ruleVersion"`
		ReasonCodes []string `json:"reasonCodes"`
		Evaluated   []struct {
			Winner bool `json:"winner"`
		} `json:"evaluated"`
		Trace *struct{} `json:"trace"`
	}
	if err := json.Unmarshal(out.Bytes(), &decision); err != nil {
		t.Fatalf("output is not a decision: %v\n%s", err, out.String())
	}
	if decision.DecisionID == "" || decision.RuleVersion != "1.1.0" {
		t.Fatalf("decision = %+v", decision)
	}
	if len(decision.Evaluated) != 2 {
		t.Fatalf("the JSON carries %d candidates, want both", len(decision.Evaluated))
	}
	// -trace was asked for, so the per-row trace has to be there.
	if decision.Trace == nil {
		t.Fatal("-trace produced no trace")
	}
}

// TestRejectsUnusableRows pins that a malformed rows file is reported rather
// than silently leaving the facts empty.
func TestRejectsUnusableRows(t *testing.T) {
	directory := t.TempDir()
	broken := filepath.Join(directory, "rows.json")
	if err := writeFile(broken, "{not json"); err != nil {
		t.Fatal(err)
	}
	err := run(&bytes.Buffer{}, fixtureRoot, requestPath("leave-balance-br001.json"), broken, false, false)
	if err == nil || !strings.Contains(err.Error(), "rows") {
		t.Fatalf("run = %v, want a rows failure", err)
	}

	// Rows for a dataset nobody published cannot be seeded.
	unknown := filepath.Join(directory, "unknown.json")
	if err := writeFile(unknown, `{"no.such.dataset":[{"tenantId":"RWB"}]}`); err != nil {
		t.Fatal(err)
	}
	err = run(&bytes.Buffer{}, fixtureRoot, requestPath("leave-balance-br001.json"), unknown, false, false)
	if err == nil {
		t.Fatal("rows for an unpublished dataset were accepted")
	}
}

// --- helpers ---

func readFixtureRequest(t *testing.T, name string) (map[string]any, error) {
	t.Helper()
	data, err := readFile(requestPath(name))
	if err != nil {
		return nil, err
	}
	var request map[string]any
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, err
	}
	return request, nil
}

func writeTempRequest(t *testing.T, request map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "request.json")
	if err := writeFile(path, string(encoded)); err != nil {
		t.Fatal(err)
	}
	return path
}
