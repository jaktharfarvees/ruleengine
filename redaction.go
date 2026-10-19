package ruleengine

import (
	"fmt"

	"github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/resolve"
	"github.com/arrowhr/ruleengine/telemetry"
)

// redactedValue is deliberately stable so log processors and support tooling
// can distinguish an intentionally hidden value from a missing one.
const redactedValue = "[REDACTED]"

// redactor is the single boundary between registry-governed personal data and
// explainability/audit output. Keeping this policy here prevents each engine
// implementation and logger from inventing a different interpretation of PII.
type redactor struct {
	fields *registry.Registry
}

// sanitizedOperationalError preserves errors.Is/errors.As semantics for a
// caller without exposing an uncontrolled dependency error string.
type sanitizedOperationalError struct {
	message string
	cause   error
}

func (e *sanitizedOperationalError) Error() string { return e.message }
func (e *sanitizedOperationalError) Unwrap() error { return e.cause }

func (r redactor) isPII(field string) bool {
	definition, found := r.fields.Field(field)
	return found && definition.PII
}

// trace returns an independent trace with values and authored expressions for
// PII fields removed. Expressions are sensitive too: a comparison can contain
// a literal email, NIC, name, or other personal value.
func (r redactor) trace(source *engine.Trace) *engine.Trace {
	if source == nil {
		return nil
	}
	result := &engine.Trace{Rows: make([]engine.RowTrace, len(source.Rows))}
	for rowIndex, sourceRow := range source.Rows {
		row := engine.RowTrace{RowID: sourceRow.RowID, Matched: sourceRow.Matched}
		if sourceRow.Conditions != nil {
			row.Conditions = make([]engine.ConditionTrace, len(sourceRow.Conditions))
		}
		for conditionIndex, sourceCondition := range sourceRow.Conditions {
			condition := sourceCondition
			if r.isPII(condition.Field) {
				condition.Expr = redactedValue
				condition.Actual = redactedValue
			}
			row.Conditions[conditionIndex] = condition
		}
		result.Rows[rowIndex] = row
	}
	return result
}

// auditResult copies the result map and removes values whose output fields are
// marked as PII. The Decision returned to the service retains its real result;
// only the secondary decision-log representation is redacted.
func (r redactor) auditResult(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for field, value := range source {
		if r.isPII(field) {
			result[field] = redactedValue
			continue
		}
		result[field] = value
	}
	return result
}

// candidateReason keeps the useful fact that a filter rejected a candidate,
// while preventing either its actual value or the authored comparison value
// from being copied into a decision response or audit record.
func (r redactor) candidateReason(candidate resolve.Candidate) string {
	if candidate.FailedOn == nil || !r.isPII(candidate.FailedOn.Attribute) {
		return candidate.Reason
	}
	return fmt.Sprintf("field %q did not satisfy the rule filter", candidate.FailedOn.Attribute)
}

// recordSpanFailure records only the stable reason classification. Dependency
// errors are intentionally not passed through because their strings are
// outside this library's control and can contain employee data.
func recordSpanFailure(span telemetry.Span, code ReasonCode) {
	recordSpanError(span, code.String())
}

func recordSpanError(span telemetry.Span, classification string) {
	telemetry.RecordFailure(span, classification)
}
