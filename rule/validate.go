package rule

import (
	"fmt"
	"reflect"
)

// ValidationError describes an invalid authored rule shape.
type ValidationError struct {
	Field string
	Err   error
}

// Error returns a value-free validation message.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid rule field %q: %v", e.Field, e.Err)
}

// Unwrap returns the underlying shape failure.
func (e *ValidationError) Unwrap() error { return e.Err }

// Validate performs evaluation-safety shape validation, not publication governance.
func (r RuleDefinition) Validate() error {
	parts, err := r.RuleKey.Parse()
	if err != nil {
		return invalid("ruleKey", err)
	}
	if r.Domain == "" || r.RuleSet == "" || r.Name == "" || r.Tenant == "" || r.Version == "" {
		return invalid("identity", fmt.Errorf("domain, ruleSet, name, tenant, and version are required"))
	}
	if parts.Domain != r.Domain || parts.RuleSet != r.RuleSet || parts.Name != r.Name {
		return invalid("ruleKey", fmt.Errorf("key family does not match identity fields"))
	}
	if r.ChangeType != ChangeMinor && r.ChangeType != ChangeMajor {
		return invalid("changeType", fmt.Errorf("unsupported value"))
	}
	if !r.Status.valid() {
		return invalid("status", fmt.Errorf("unsupported value"))
	}
	if r.Effective.ValidFrom.IsZero() || (r.Effective.ValidTo != nil && !r.Effective.ValidTo.After(r.Effective.ValidFrom)) {
		return invalid("effective", fmt.Errorf("validFrom is required and validTo must be later"))
	}
	if err := validateAnchor(r.Scope.Anchor); err != nil {
		return err
	}
	for i, filter := range r.Scope.Filters {
		if err := validateFilter(filter); err != nil {
			return invalid(fmt.Sprintf("scope.filters[%d]", i), err)
		}
	}
	if !r.Logic.HitPolicy.valid() || len(r.Logic.Rows) == 0 {
		return invalid("logic", fmt.Errorf("a supported hit policy and at least one row are required"))
	}
	outputs, err := names(r.DeclaredOutputs, true)
	if err != nil {
		return invalid("declaredOutputs", err)
	}
	if _, err := names(r.DeclaredInputs, false); err != nil {
		return invalid("declaredInputs", err)
	}
	rows := make(map[string]struct{}, len(r.Logic.Rows))
	for i, row := range r.Logic.Rows {
		if row.ID == "" || len(row.Then) == 0 {
			return invalid(fmt.Sprintf("logic.rows[%d]", i), fmt.Errorf("id and outputs are required"))
		}
		if _, exists := rows[row.ID]; exists {
			return invalid(fmt.Sprintf("logic.rows[%d].id", i), fmt.Errorf("duplicate row id"))
		}
		rows[row.ID] = struct{}{}
		for output := range row.Then {
			if _, declared := outputs[output]; !declared {
				return invalid(fmt.Sprintf("logic.rows[%d].then", i), fmt.Errorf("output %q is not declared", output))
			}
		}
	}
	return nil
}

func validateAnchor(anchor Anchor) error {
	switch anchor.Kind {
	case AnchorNode:
		if anchor.NodeID == "" || anchor.EmployeeID != "" {
			return invalid("scope.anchor", fmt.Errorf("node anchor requires only nodeId"))
		}
	case AnchorEmployee:
		if anchor.EmployeeID == "" || anchor.NodeID != "" || anchor.Reason == "" {
			return invalid("scope.anchor", fmt.Errorf("employee anchor requires employeeId and reason"))
		}
	default:
		return invalid("scope.anchor.kind", fmt.Errorf("unsupported value"))
	}
	return nil
}

func validateFilter(filter FilterCondition) error {
	if filter.Attribute == "" {
		return fmt.Errorf("attribute is required")
	}
	switch filter.Op {
	case FilterEquals:
		if filter.Value == nil {
			return fmt.Errorf("equals requires a value")
		}
	case FilterIn, FilterNotIn:
		value := reflect.ValueOf(filter.Value)
		if !value.IsValid() || (value.Kind() != reflect.Slice && value.Kind() != reflect.Array) || value.Len() == 0 {
			return fmt.Errorf("set operator requires a non-empty array")
		}
	case FilterRange:
		value := reflect.ValueOf(filter.Value)
		if !value.IsValid() || (value.Kind() != reflect.Slice && value.Kind() != reflect.Array) || value.Len() != 2 {
			return fmt.Errorf("range requires a two-value array")
		}
	case FilterExists, FilterNotExists:
		if filter.Value != nil {
			return fmt.Errorf("existence operator must not carry a value")
		}
	default:
		return fmt.Errorf("unsupported filter operator")
	}
	return nil
}

func names(values []string, required bool) (map[string]struct{}, error) {
	if required && len(values) == 0 {
		return nil, fmt.Errorf("at least one value is required")
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return nil, fmt.Errorf("empty value")
		}
		if _, exists := result[value]; exists {
			return nil, fmt.Errorf("duplicate value %q", value)
		}
		result[value] = struct{}{}
	}
	return result, nil
}

func invalid(field string, err error) error { return &ValidationError{Field: field, Err: err} }

func (s Status) valid() bool {
	switch s {
	case StatusDraft, StatusPendingApproval, StatusActive, StatusScheduled, StatusDeprecated, StatusRetired:
		return true
	default:
		return false
	}
}

func (h HitPolicy) valid() bool {
	return h == HitFirst || h == HitPriority || h == HitCollect
}
