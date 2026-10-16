package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/arrowhr/ruleengine/internal/decimalx"
	"github.com/arrowhr/ruleengine/rule"
)

// ValidateInputs verifies that every declared input is registered for domain,
// is present, has an input-capable role, and satisfies its field contract.
func (r *Registry) ValidateInputs(domain string, declared []string, context map[string]any) error {
	seen := make(map[string]struct{}, len(declared))
	for _, name := range declared {
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate declared input %q", name)
		}
		seen[name] = struct{}{}
		field, exists := r.field(name)
		if !exists || field.Domain != domain {
			return fmt.Errorf("%w: input field %q for domain %q", ErrFieldNotRegistered, name, domain)
		}
		if field.Role != RoleInput && field.Role != RoleSubjectAttribute {
			return fmt.Errorf("%w: field %q is %q, not an input", ErrFieldRoleMismatch, name, field.Role)
		}
		value, present := context[name]
		if !present || !field.accepts(value) {
			return fmt.Errorf("%w: input field %q requires %s", ErrFieldTypeMismatch, name, field.DataType)
		}
	}
	return nil
}

// ValidateOutputs verifies that every declared output is registered, present,
// output-capable, and correctly typed.
func (r *Registry) ValidateOutputs(declared []string, output map[string]any) error {
	seen := make(map[string]struct{}, len(declared))
	for _, name := range declared {
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate declared output %q", name)
		}
		seen[name] = struct{}{}
		field, exists := r.field(name)
		if !exists {
			return fmt.Errorf("%w: output field %q", ErrFieldNotRegistered, name)
		}
		if field.Role != RoleOutput {
			return fmt.Errorf("%w: field %q is %q, not an output", ErrFieldRoleMismatch, name, field.Role)
		}
		value, present := output[name]
		if !present || !field.accepts(value) {
			return fmt.Errorf("%w: output field %q requires %s", ErrFieldTypeMismatch, name, field.DataType)
		}
	}
	return nil
}

// ValidateFilters verifies that filters use registered subject attributes and
// values allowed by their field definitions. Hierarchy placement belongs in
// Scope.Anchor and is therefore rejected as a filter.
func (r *Registry) ValidateFilters(filters []rule.FilterCondition) error {
	for index, filter := range filters {
		field, exists := r.field(filter.Attribute)
		if !exists {
			cause := errors.Join(ErrFieldNotRegistered, ErrHierarchyAttributeFilter)
			return fmt.Errorf("filter %d field %q is not a registered subject attribute; hierarchy placement must use scope.anchor: %w", index, filter.Attribute, cause)
		}
		if field.Role != RoleSubjectAttribute {
			cause := errors.Join(ErrFieldRoleMismatch, ErrHierarchyAttributeFilter)
			return fmt.Errorf("filter %d field %q has role %q; hierarchy placement must use scope.anchor: %w", index, filter.Attribute, field.Role, cause)
		}
		if err := validateFilterValue(field, filter); err != nil {
			return fmt.Errorf("filter %d field %q: %w", index, filter.Attribute, err)
		}
	}
	return nil
}

func (r *Registry) field(name string) (FieldDefinition, bool) {
	if r == nil {
		return FieldDefinition{}, false
	}
	field, exists := r.fields[name]
	return field, exists
}

func validateFilterValue(field FieldDefinition, filter rule.FilterCondition) error {
	switch filter.Op {
	case rule.FilterExists, rule.FilterNotExists:
		if filter.Value != nil {
			return fmt.Errorf("existence operator cannot carry a value")
		}
		return nil
	case rule.FilterEquals:
		if !field.accepts(filter.Value) {
			return fmt.Errorf("%w: equals value requires %s", ErrFieldTypeMismatch, field.DataType)
		}
		return nil
	case rule.FilterIn, rule.FilterNotIn:
		values, ok := filterValues(filter.Value)
		if !ok || len(values) == 0 {
			return fmt.Errorf("set operator requires a non-empty array")
		}
		for _, value := range values {
			if !field.accepts(value) {
				return fmt.Errorf("%w: set value requires %s", ErrFieldTypeMismatch, field.DataType)
			}
		}
		return nil
	case rule.FilterRange:
		values, ok := filterValues(filter.Value)
		if !ok || len(values) != 2 {
			return fmt.Errorf("range operator requires exactly two values")
		}
		if !field.accepts(values[0]) || !field.accepts(values[1]) {
			return fmt.Errorf("%w: range bounds require %s", ErrFieldTypeMismatch, field.DataType)
		}
		return nil
	default:
		return fmt.Errorf("unsupported filter operator %q", filter.Op)
	}
}

func filterValues(value any) ([]any, bool) {
	if value == nil {
		return nil, false
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Array && reflected.Kind() != reflect.Slice {
		return nil, false
	}
	values := make([]any, reflected.Len())
	for index := range values {
		values[index] = reflected.Index(index).Interface()
	}
	return values, true
}

func (field FieldDefinition) accepts(value any) bool {
	return field.DataType.Accepts(value, field.AllowedValues)
}

// Accepts reports whether value is a usable representation of d. For an enum,
// allowed is the closed value set and a value outside it is refused.
//
// It is exported because the same question is asked of a dataset column, whose
// declared type is a DataType: a filter value has to be type-checked at
// compile time by exactly the rules that will type-check the resulting fact.
func (d DataType) Accepts(value any, allowed []string) bool {
	if value == nil {
		return false
	}
	switch d {
	case DataString:
		_, ok := value.(string)
		return ok
	case DataInteger:
		return isInteger(value)
	case DataDecimal, DataNumber, DataMoney:
		_, ok := decimalx.FromValue(value)
		return ok
	case DataBoolean:
		_, ok := value.(bool)
		return ok
	case DataDate:
		return isDate(value)
	case DataDateTime:
		return isDateTime(value)
	case DataEnum:
		text, ok := value.(string)
		return ok && containsString(allowed, text)
	default:
		return false
	}
}

func isInteger(value any) bool {
	// An exact decimal that happens to be whole is an integer. Arithmetic in a
	// derived field or a rule output produces one of these, so rejecting it
	// would make an integer-typed field impossible to compute.
	if decimal, ok := value.(decimalx.Decimal); ok {
		return decimal.IsInteger()
	}
	if number, ok := value.(json.Number); ok {
		decimal, valid := decimalx.FromValue(number)
		return valid && decimal.IsInteger()
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	case reflect.Float32, reflect.Float64:
		number := reflected.Float()
		return !math.IsNaN(number) && !math.IsInf(number, 0) && math.Trunc(number) == number
	default:
		return false
	}
}

func isDate(value any) bool {
	if _, ok := value.(time.Time); ok {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	_, err := time.Parse(time.DateOnly, text)
	return err == nil
}

func isDateTime(value any) bool {
	if _, ok := value.(time.Time); ok {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, text)
	return err == nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
