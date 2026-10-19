package ruleengine

import (
	"errors"
	"testing"

	"github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/resolve"
	"github.com/arrowhr/ruleengine/rule"
)

func TestRedactorUsesRegistryPIIForEveryExplainabilitySurface(t *testing.T) {
	fields, err := registry.NewRegistry([]registry.FieldDefinition{
		{Name: "email", Domain: "leave", DataType: registry.DataString, PII: true, SourceService: "people", Status: "active", Role: registry.RoleSubjectAttribute},
		{Name: "allowed", Domain: "leave", DataType: registry.DataBoolean, SourceService: "leave", Status: "active", Role: registry.RoleOutput},
	})
	if err != nil {
		t.Fatal(err)
	}
	redaction := redactor{fields: fields}
	trace := &engine.Trace{Rows: []engine.RowTrace{{
		RowID: "personal", Matched: false,
		Conditions: []engine.ConditionTrace{
			{Field: "email", Expr: `== "person@example.com"`, Actual: "person@example.com"},
			{Field: "allowed", Expr: "== true", Actual: true, Matched: true},
		},
	}}}

	got := redaction.trace(trace)
	if got.Rows[0].Conditions[0].Expr != redactedValue || got.Rows[0].Conditions[0].Actual != redactedValue {
		t.Fatalf("PII condition was not fully redacted: %#v", got.Rows[0].Conditions[0])
	}
	if got.Rows[0].Conditions[1] != trace.Rows[0].Conditions[1] {
		t.Fatalf("non-PII condition changed: %#v", got.Rows[0].Conditions[1])
	}
	if trace.Rows[0].Conditions[0].Actual != "person@example.com" {
		t.Fatal("redaction mutated the engine-owned source trace")
	}

	candidate := resolve.Candidate{
		FailedOn: &rule.FilterCondition{Attribute: "email", Op: rule.FilterEquals, Value: "person@example.com"},
		Reason:   "email is other@example.com, rule requires person@example.com",
	}
	reason := redaction.candidateReason(candidate)
	if reason != `field "email" did not satisfy the rule filter` {
		t.Fatalf("redacted candidate reason = %q", reason)
	}
}

func TestSanitizedOperationalErrorPreservesCauseWithoutItsMessage(t *testing.T) {
	cause := errors.New("person@example.com")
	err := &sanitizedOperationalError{message: "audit persistence failed", cause: cause}
	if err.Error() != "audit persistence failed" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("sanitized error did not preserve its cause")
	}
}
