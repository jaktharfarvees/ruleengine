// Package contextbuilder declares how a service supplies facts it computes
// itself, for fields whose source is a function rather than a dataset.
//
// It is the narrower, older sibling of a dataset executor: where an executor
// answers a compiled query the library built, a context builder answers "give
// me these fields for this subject", with the service deciding how. Prefer a
// declared dataset or derived field where one will do, because those are
// authored and versioned; reach for a builder when the computation genuinely
// lives in the service's code.
package contextbuilder

import "context"

// ContextBuilder supplies values for named fields.
//
// fields are registered field names, subjectID identifies who they are for,
// and period is the pay or accrual period the caller is working in, empty when
// the decision is not tied to one.
//
// An implementation must return every field it can and omit the rest rather
// than guessing: a missing field becomes a fail-closed decision naming it,
// which is recoverable, while a guessed zero is a wrong answer that looks
// right. It must honour cancellation, because it runs inside a bounded fetch.
type ContextBuilder interface {
	Build(ctx context.Context, fields []string, subjectID string, period string) (map[string]any, error)
}

// Static is a ContextBuilder backed by a fixed map, for tests and for a
// service wiring up a decision point before its real builder exists.
type Static map[string]any

// Build returns the requested subset of s, omitting fields it does not hold.
func (s Static) Build(ctx context.Context, fields []string, _ string, _ string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make(map[string]any, len(fields))
	for _, field := range fields {
		if value, held := s[field]; held {
			values[field] = value
		}
	}
	return values, nil
}
