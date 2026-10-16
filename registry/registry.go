package registry

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/arrowhr/ruleengine/rule"
)

var (
	// ErrInvalidRegistry means a field definition violates the registry shape.
	ErrInvalidRegistry = errors.New("invalid field registry")
	// ErrFieldNotRegistered means a referenced field is absent or unavailable to its domain.
	ErrFieldNotRegistered = errors.New("field not registered")
	// ErrFieldTypeMismatch means a value does not satisfy its registered type.
	ErrFieldTypeMismatch = errors.New("field type mismatch")
	// ErrFieldRoleMismatch means a field was used in a role it does not declare.
	ErrFieldRoleMismatch = errors.New("field role mismatch")
	// ErrHierarchyAttributeFilter means hierarchy structure was expressed as a filter instead of an anchor.
	ErrHierarchyAttributeFilter = errors.New("hierarchy attribute cannot be used as a filter")
)

// Registry is an immutable field-definition snapshot safe for concurrent use.
type Registry struct {
	fields map[string]FieldDefinition
}

// NewRegistry validates and copies fields into an immutable registry.
func NewRegistry(fields []FieldDefinition) (*Registry, error) {
	registry := &Registry{fields: make(map[string]FieldDefinition, len(fields))}
	for index := range fields {
		field := cloneField(fields[index])
		if err := validateDefinition(field); err != nil {
			return nil, fmt.Errorf("%w: field at index %d: %v", ErrInvalidRegistry, index, err)
		}
		if _, exists := registry.fields[field.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate field %q", ErrInvalidRegistry, field.Name)
		}
		registry.fields[field.Name] = field
	}
	return registry, nil
}

// Field returns an independent copy of the named field definition.
func (r *Registry) Field(name string) (FieldDefinition, bool) {
	if r == nil {
		return FieldDefinition{}, false
	}
	field, exists := r.fields[name]
	if !exists {
		return FieldDefinition{}, false
	}
	return cloneField(field), true
}

func validateDefinition(field FieldDefinition) error {
	if field.Name == "" || field.Domain == "" || field.OwnerService == "" || field.Status == "" {
		return fmt.Errorf("name, domain, ownerService, and status are required")
	}
	if !field.DataType.Valid() {
		return fmt.Errorf("field %q has unsupported data type %q", field.Name, field.DataType)
	}
	if !field.Role.valid() {
		return fmt.Errorf("field %q has unsupported role %q", field.Name, field.Role)
	}
	if field.Effective.ValidTo != nil && (field.Effective.ValidFrom.IsZero() || !field.Effective.ValidTo.After(field.Effective.ValidFrom)) {
		return fmt.Errorf("field %q has an invalid effective interval", field.Name)
	}
	if field.DataType == DataEnum {
		if len(field.AllowedValues) == 0 {
			return fmt.Errorf("enum field %q requires allowedValues", field.Name)
		}
		seen := make(map[string]struct{}, len(field.AllowedValues))
		for _, value := range field.AllowedValues {
			if value == "" {
				return fmt.Errorf("enum field %q contains an empty allowed value", field.Name)
			}
			if _, exists := seen[value]; exists {
				return fmt.Errorf("enum field %q repeats allowed value %q", field.Name, value)
			}
			seen[value] = struct{}{}
		}
	} else if len(field.AllowedValues) != 0 {
		return fmt.Errorf("non-enum field %q cannot declare allowedValues", field.Name)
	}
	if field.FactSource.Kind != "" && !field.FactSource.Kind.Valid() {
		return fmt.Errorf("field %q has unsupported fact-source kind %q", field.Name, field.FactSource.Kind)
	}
	return nil
}

func cloneField(field FieldDefinition) FieldDefinition {
	field.AllowedValues = append([]string(nil), field.AllowedValues...)
	field.FactSource = cloneFactSource(field.FactSource)
	if field.Effective.ValidTo != nil {
		validTo := *field.Effective.ValidTo
		field.Effective.ValidTo = &validTo
	}
	return field
}

func cloneFactSource(source FactSource) FactSource {
	source.Filters = append([]rule.FilterCondition(nil), source.Filters...)
	for index := range source.Filters {
		source.Filters[index].Value = cloneValue(source.Filters[index].Value)
	}
	if source.Window != nil {
		window := *source.Window
		source.Window = &window
	}
	if source.Aggregate != nil {
		aggregate := *source.Aggregate
		source.Aggregate = &aggregate
	}
	source.OnEmpty = cloneValue(source.OnEmpty)
	return source
}

func cloneValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneReflect(reflect.ValueOf(value)).Interface()
}

func cloneReflect(reflected reflect.Value) reflect.Value {
	switch reflected.Kind() {
	case reflect.Interface:
		if reflected.IsNil() {
			return reflect.Zero(reflected.Type())
		}
		copy := reflect.New(reflected.Type()).Elem()
		copy.Set(cloneReflect(reflected.Elem()))
		return copy
	case reflect.Pointer:
		if reflected.IsNil() {
			return reflect.Zero(reflected.Type())
		}
		copy := reflect.New(reflected.Elem().Type())
		copy.Elem().Set(cloneReflect(reflected.Elem()))
		return copy
	case reflect.Slice:
		if reflected.IsNil() {
			return reflect.Zero(reflected.Type())
		}
		copy := reflect.MakeSlice(reflected.Type(), reflected.Len(), reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			copy.Index(index).Set(cloneReflect(reflected.Index(index)))
		}
		return copy
	case reflect.Array:
		copy := reflect.New(reflected.Type()).Elem()
		for index := 0; index < reflected.Len(); index++ {
			copy.Index(index).Set(cloneReflect(reflected.Index(index)))
		}
		return copy
	case reflect.Map:
		if reflected.IsNil() {
			return reflect.Zero(reflected.Type())
		}
		copy := reflect.MakeMapWithSize(reflected.Type(), reflected.Len())
		iterator := reflected.MapRange()
		for iterator.Next() {
			copy.SetMapIndex(iterator.Key(), cloneReflect(iterator.Value()))
		}
		return copy
	default:
		return reflected
	}
}

// Valid reports whether d belongs to the closed data-type set.
func (d DataType) Valid() bool {
	switch d {
	case DataString, DataInteger, DataDecimal, DataBoolean, DataDate, DataDateTime, DataEnum, DataMoney:
		return true
	default:
		return false
	}
}

func (r Role) valid() bool {
	return r == RoleInput || r == RoleOutput || r == RoleSubjectAttribute
}
