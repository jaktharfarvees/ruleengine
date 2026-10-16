package ruletest

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	ruleengine "github.com/arrowhr/ruleengine"
)

// Expectation is what a test asserts about a decision.
//
// It is deliberately not a whole Decision. A decision carries a fresh
// identifier and timing on every run, so comparing one wholesale would compare
// the parts that must differ. This names the parts a rule owner cares about,
// and an empty field is simply not checked.
type Expectation struct {
	// Failed is whether the decision should stand.
	Failed bool
	// ReasonCodes are the codes the decision must carry, in any order. Extra
	// codes are a failure: a decision that quietly acquires a note is a
	// decision nobody reviewed.
	ReasonCodes []ruleengine.ReasonCode
	// RuleVersion is the version that must have decided.
	RuleVersion string
	// ResolvedNodeID is the anchor that must have won.
	ResolvedNodeID string
	// EmployeeException asserts the winner was an employee-level exception.
	EmployeeException bool
	// Result is the output the rule must have produced. Only the named keys
	// are compared, so a test can assert one field of a larger result.
	Result map[string]any
}

// DecisionEquals asserts a decision matches an expectation, reporting every
// difference at once rather than stopping at the first.
//
// Differences are reported in the terms a rule owner uses — which version
// decided, which anchor won, what it produced — because that is what a failing
// assertion has to explain.
func DecisionEquals(t testing.TB, got ruleengine.Decision, want Expectation) {
	if helper, ok := t.(interface{ Helper() }); ok {
		helper.Helper()
	}
	var problems []string

	if got.Failed != want.Failed {
		problems = append(problems, fmt.Sprintf(
			"failed = %t, want %t (reason codes: %s)", got.Failed, want.Failed, codeList(got.ReasonCodes)))
	}
	if want.ReasonCodes != nil {
		if diff := diffCodes(want.ReasonCodes, got.ReasonCodes); diff != "" {
			problems = append(problems, diff)
		}
	}
	if want.RuleVersion != "" && got.RuleVersion != want.RuleVersion {
		problems = append(problems, fmt.Sprintf(
			"rule version = %q, want %q", got.RuleVersion, want.RuleVersion))
	}
	if want.ResolvedNodeID != "" {
		switch {
		case got.ResolvedAt == nil:
			problems = append(problems, fmt.Sprintf(
				"no anchor resolved, want %q", want.ResolvedNodeID))
		case got.ResolvedAt.NodeID != want.ResolvedNodeID:
			problems = append(problems, fmt.Sprintf(
				"resolved at %q, want %q", got.ResolvedAt.NodeID, want.ResolvedNodeID))
		}
	}
	if want.EmployeeException && (got.ResolvedAt == nil || !got.ResolvedAt.IsEmployeeException) {
		problems = append(problems, "the winner is not an employee exception")
	}
	for name, expected := range want.Result {
		actual, produced := got.Result[name]
		if !produced {
			problems = append(problems, fmt.Sprintf("result is missing %q, want %v", name, expected))
			continue
		}
		if !sameScalar(actual, expected) {
			problems = append(problems, fmt.Sprintf(
				"result[%q] = %v (%T), want %v (%T)", name, actual, actual, expected, expected))
		}
	}

	if len(problems) == 0 {
		return
	}
	t.Errorf("decision does not match:\n  %s\n%s", strings.Join(problems, "\n  "), explain(got))
}

// AssertPassed asserts a decision stands, and says why if it did not.
func AssertPassed(t testing.TB, got ruleengine.Decision) {
	if helper, ok := t.(interface{ Helper() }); ok {
		helper.Helper()
	}
	if got.Failed {
		t.Errorf("decision failed with %s\n%s", codeList(got.ReasonCodes), explain(got))
	}
}

// AssertFailed asserts a decision failed closed carrying a specific code.
func AssertFailed(t testing.TB, got ruleengine.Decision, want ruleengine.ReasonCode) {
	if helper, ok := t.(interface{ Helper() }); ok {
		helper.Helper()
	}
	if !got.Failed {
		t.Errorf("decision passed, want it to fail with %s\n%s", want, explain(got))
		return
	}
	for _, code := range got.ReasonCodes {
		if code == want {
			return
		}
	}
	t.Errorf("decision failed with %s, want %s\n%s", codeList(got.ReasonCodes), want, explain(got))
}

// AssertResolvedAt asserts which anchor won, which is the assertion a
// precedence test is really making.
func AssertResolvedAt(t testing.TB, got ruleengine.Decision, nodeID string) {
	if helper, ok := t.(interface{ Helper() }); ok {
		helper.Helper()
	}
	if got.ResolvedAt == nil {
		t.Errorf("no anchor resolved, want %q\n%s", nodeID, explain(got))
		return
	}
	if got.ResolvedAt.NodeID != nodeID {
		t.Errorf("resolved at %q, want %q\n%s", got.ResolvedAt.NodeID, nodeID, explain(got))
	}
}

// Explain renders a decision as the report a person reads when a test fails:
// which rule won, where, and what happened to everything else.
//
// It is exported because it is just as useful from a service's own test or a
// support session as it is here.
func Explain(decision ruleengine.Decision) string { return explain(decision) }

func explain(decision ruleengine.Decision) string {
	var report strings.Builder
	report.WriteString("  decision:\n")
	fmt.Fprintf(&report, "    rule        %s @ %s\n", orNone(decision.RuleKey.String()), orNone(decision.RuleVersion))
	if decision.ResolvedAt != nil {
		anchor := decision.ResolvedAt.NodeID
		if decision.ResolvedAt.IsEmployeeException {
			anchor = "employee exception"
		}
		fmt.Fprintf(&report, "    resolved at %s (%s, order %d, depth %d)\n",
			anchor, orNone(decision.ResolvedAt.TypeName),
			decision.ResolvedAt.Order, decision.ResolvedAt.Depth)
	}
	if len(decision.SubjectPath) > 0 {
		nodes := make([]string, 0, len(decision.SubjectPath))
		for _, entry := range decision.SubjectPath {
			nodes = append(nodes, entry.NodeID)
		}
		fmt.Fprintf(&report, "    path        %s (root to leaf)\n", strings.Join(nodes, " -> "))
	}
	fmt.Fprintf(&report, "    reasons     %s\n", codeList(decision.ReasonCodes))
	if len(decision.Result) > 0 {
		fmt.Fprintf(&report, "    result      %s\n", resultLine(decision.Result))
	}
	if decision.Tie != nil {
		fmt.Fprintf(&report, "    tie         %s\n", decision.Tie.Message)
	}
	if len(decision.Evaluated) > 0 {
		report.WriteString("    candidates:\n")
		for _, evaluated := range decision.Evaluated {
			outcome := "eliminated"
			if evaluated.Winner {
				outcome = "WINNER"
			}
			anchor := evaluated.Anchor.NodeID
			if anchor == "" {
				anchor = "employee:" + evaluated.Anchor.EmployeeID
			}
			fmt.Fprintf(&report, "      %-10s %s @ %s anchored %s", outcome,
				evaluated.RuleKey, evaluated.RuleVersion, anchor)
			if evaluated.Reason != "" {
				fmt.Fprintf(&report, " — %s", evaluated.Reason)
			}
			report.WriteByte('\n')
		}
	}
	if len(decision.FactsResolved) > 0 {
		report.WriteString("    facts:\n")
		for _, resolved := range decision.FactsResolved {
			origin := string(resolved.Kind)
			switch {
			case resolved.FromRequest:
				origin += " (given)"
			case resolved.Cached:
				origin += " (reused)"
			}
			fmt.Fprintf(&report, "      %-28s %s\n", resolved.Field, origin)
		}
	}
	return report.String()
}

// diffCodes compares reason-code sets, naming what is missing and what is
// unexpected rather than printing two lists for the reader to compare.
func diffCodes(want, got []ruleengine.ReasonCode) string {
	expected := make(map[ruleengine.ReasonCode]struct{}, len(want))
	for _, code := range want {
		expected[code] = struct{}{}
	}
	actual := make(map[ruleengine.ReasonCode]struct{}, len(got))
	for _, code := range got {
		actual[code] = struct{}{}
	}
	var missing, unexpected []string
	for code := range expected {
		if _, present := actual[code]; !present {
			missing = append(missing, code.String())
		}
	}
	for code := range actual {
		if _, wanted := expected[code]; !wanted {
			unexpected = append(unexpected, code.String())
		}
	}
	if len(missing) == 0 && len(unexpected) == 0 {
		return ""
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	var message strings.Builder
	message.WriteString("reason codes differ:")
	if len(missing) > 0 {
		fmt.Fprintf(&message, " missing %s", strings.Join(missing, ", "))
	}
	if len(unexpected) > 0 {
		fmt.Fprintf(&message, " unexpected %s", strings.Join(unexpected, ", "))
	}
	return message.String()
}

func codeList(codes []ruleengine.ReasonCode) string {
	if len(codes) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(codes))
	for _, code := range codes {
		names = append(names, code.String())
	}
	return strings.Join(names, ", ")
}

func resultLine(result map[string]any) string {
	names := make([]string, 0, len(result))
	for name := range result {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%v", name, result[name]))
	}
	return strings.Join(parts, " ")
}

func orNone(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}
