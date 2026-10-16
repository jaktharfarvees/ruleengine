package fact

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/internal/expr"
	"github.com/arrowhr/ruleengine/registry"
)

// The request times a derived expression may reference by name, in addition to
// other registered fields. They are not registry fields: every decision has
// them, and requiring each tenant to declare them would be noise.
const (
	fieldAsOf    = "asOf"
	fieldValidAt = "validAt"
)

// fetchDerived evaluates a formula over other already-resolved fields.
//
// Dependencies are resolved depth-first, so a formula over a dataset fact
// works without the author ordering anything. A cycle is detected rather than
// recursed into: a stack overflow in a payroll run would take the process
// down, and the author needs to be told which fields form the loop.
func (s *resolution) fetchDerived(ctx context.Context, field string, source registry.FactSource) (any, Resolved, error) {
	if strings.TrimSpace(source.Expression) == "" {
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("field is declared as derived but carries no expression")}
	}
	// Defence in depth. The real guard is the next line: the grammar is closed,
	// so an expression can only be numbers, dates, field references,
	// arithmetic and an allowlisted function.
	if err := dataset.RejectExpressionText("derived expression", source.Expression); err != nil {
		return nil, Resolved{}, &Fault{Code: dataset.ReasonRawQueryRejected, Field: field, Err: err}
	}
	parsed, err := expr.Parse(source.Expression)
	if err != nil {
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("derived expression is not valid: %w", err)}
	}

	if _, looping := s.active[field]; looping {
		return nil, Resolved{}, &Fault{Code: ReasonDerivedCycle, Field: field,
			Err: fmt.Errorf("derived field is part of a dependency cycle: %s", s.cyclePath(field))}
	}
	s.active[field] = struct{}{}
	defer delete(s.active, field)

	scope := make(map[string]any, len(parsed.References())+2)
	scope[fieldAsOf] = s.params.AsOf
	scope[fieldValidAt] = s.params.ValidAt
	for _, reference := range parsed.References() {
		if reference == fieldAsOf || reference == fieldValidAt {
			continue
		}
		value, err := s.resolve(ctx, reference)
		if err != nil {
			return nil, Resolved{}, err
		}
		scope[reference] = value
	}

	value, err := parsed.Eval(scope)
	if err != nil {
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("derived expression could not be evaluated: %w", err)}
	}
	if value == nil {
		if source.OnEmpty == nil {
			return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
				Err: fmt.Errorf("derived expression produced no value and the field declares no onEmpty")}
		}
		value = source.OnEmpty
	}
	return value, Resolved{}, nil
}

// cyclePath names the fields currently being resolved, so the error points at
// the loop rather than only at the field that closed it.
func (s *resolution) cyclePath(field string) string {
	names := make([]string, 0, len(s.active)+1)
	for name := range s.active {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(append(names, field), " -> ")
}
