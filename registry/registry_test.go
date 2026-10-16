package registry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/rule"
)

func TestNewRegistryAndImmutableLookup(t *testing.T) {
	allowed := []string{"Finance", "Engineering"}
	filterValues := []string{"APPROVED"}
	onEmpty := map[string]any{"nested": []any{"original"}, "empty": nil}
	window := &FactWindow{Column: "startDate", LastDays: 30}
	aggregate := &FactAggregate{Fn: "sum", On: "days"}
	validTo := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fields := []FieldDefinition{{
		Name: "employeeGroup", Domain: "leave", DataType: DataEnum,
		AllowedValues: allowed, OwnerService: "employee", Status: "active", Role: RoleSubjectAttribute,
		FactSource: FactSource{
			Kind: FactDataset, Filters: []rule.FilterCondition{{Attribute: "status", Op: rule.FilterIn, Value: filterValues}},
			Window: window, Aggregate: aggregate, OnEmpty: onEmpty,
		},
		Effective: rule.Effective{ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ValidTo: &validTo},
	}}
	registry, err := NewRegistry(fields)
	if err != nil {
		t.Fatal(err)
	}
	allowed[0] = "mutated"
	filterValues[0] = "mutated"
	window.Column = "mutated"
	aggregate.On = "mutated"
	onEmpty["nested"].([]any)[0] = "mutated"
	validTo = validTo.Add(time.Hour)

	field, ok := registry.Field("employeeGroup")
	if !ok || field.AllowedValues[0] != "Finance" || field.FactSource.Window.Column != "startDate" ||
		field.FactSource.Aggregate.On != "days" || field.FactSource.Filters[0].Value.([]string)[0] != "APPROVED" ||
		field.FactSource.OnEmpty.(map[string]any)["nested"].([]any)[0] != "original" ||
		!field.Effective.ValidTo.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("stored field was mutated: %#v", field)
	}
	if empty, exists := field.FactSource.OnEmpty.(map[string]any)["empty"]; !exists || empty != nil {
		t.Fatalf("nil map value was not preserved: %#v", field.FactSource.OnEmpty)
	}
	field.AllowedValues[0] = "returned mutation"
	field.FactSource.Window.Column = "returned mutation"
	again, _ := registry.Field("employeeGroup")
	if again.AllowedValues[0] != "Finance" || again.FactSource.Window.Column != "startDate" {
		t.Fatal("Field returned mutable registry state")
	}
	if _, ok := registry.Field("missing"); ok {
		t.Fatal("missing field found")
	}
	var nilRegistry *Registry
	if _, ok := nilRegistry.Field("anything"); ok {
		t.Fatal("nil registry found a field")
	}
}

func TestNewRegistryRejectsInvalidDefinitions(t *testing.T) {
	valid := field("name", DataString, RoleInput)
	tests := []struct {
		name   string
		fields []FieldDefinition
	}{
		{"missing identity", []FieldDefinition{{}}},
		{"bad type", []FieldDefinition{mutate(valid, func(field *FieldDefinition) { field.DataType = "object" })}},
		{"bad role", []FieldDefinition{mutate(valid, func(field *FieldDefinition) { field.Role = "other" })}},
		{"enum no values", []FieldDefinition{mutate(valid, func(field *FieldDefinition) { field.DataType = DataEnum })}},
		{"enum empty value", []FieldDefinition{mutate(valid, func(field *FieldDefinition) { field.DataType = DataEnum; field.AllowedValues = []string{""} })}},
		{"enum duplicate", []FieldDefinition{mutate(valid, func(field *FieldDefinition) { field.DataType = DataEnum; field.AllowedValues = []string{"A", "A"} })}},
		{"values on string", []FieldDefinition{mutate(valid, func(field *FieldDefinition) { field.AllowedValues = []string{"A"} })}},
		{"bad fact kind", []FieldDefinition{mutate(valid, func(field *FieldDefinition) { field.FactSource.Kind = "remote" })}},
		{"bad effective interval", []FieldDefinition{mutate(valid, func(field *FieldDefinition) {
			end := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			field.Effective.ValidTo = &end
		})}},
		{"duplicate", []FieldDefinition{valid, valid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRegistry(test.fields); !errors.Is(err, ErrInvalidRegistry) {
				t.Fatalf("NewRegistry() error = %v", err)
			}
		})
	}
}

func TestValidateInputs(t *testing.T) {
	registry := mustRegistry(t,
		field("months", DataInteger, RoleInput),
		field("amount", DataDecimal, RoleInput),
		field("country", DataEnum, RoleSubjectAttribute, "CA", "LK"),
		field("output", DataString, RoleOutput),
	)
	valid := map[string]any{"months": json.Number("6"), "amount": "12.50", "country": "CA"}
	if err := registry.ValidateInputs("leave", []string{"months", "amount", "country"}, valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		domain   string
		declared []string
		context  map[string]any
		want     error
	}{
		{"unknown", "leave", []string{"missing"}, valid, ErrFieldNotRegistered},
		{"wrong domain", "attendance", []string{"months"}, valid, ErrFieldNotRegistered},
		{"wrong role", "leave", []string{"output"}, map[string]any{"output": "x"}, ErrFieldRoleMismatch},
		{"missing value", "leave", []string{"months"}, nil, ErrFieldTypeMismatch},
		{"wrong type", "leave", []string{"months"}, map[string]any{"months": 6.5}, ErrFieldTypeMismatch},
		{"enum outside", "leave", []string{"country"}, map[string]any{"country": "US"}, ErrFieldTypeMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := registry.ValidateInputs(test.domain, test.declared, test.context); !errors.Is(err, test.want) {
				t.Fatalf("ValidateInputs() error = %v, want %v", err, test.want)
			}
		})
	}
	if err := registry.ValidateInputs("leave", []string{"months", "months"}, valid); err == nil {
		t.Fatal("duplicate input succeeded")
	}
	var nilRegistry *Registry
	if err := nilRegistry.ValidateInputs("leave", []string{"months"}, valid); !errors.Is(err, ErrFieldNotRegistered) {
		t.Fatalf("nil registry error = %v", err)
	}
}

func TestValidateOutputs(t *testing.T) {
	registry := mustRegistry(t, field("eligible", DataBoolean, RoleOutput), field("input", DataString, RoleInput))
	if err := registry.ValidateOutputs([]string{"eligible"}, map[string]any{"eligible": true, "extra": "allowed"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		declared []string
		output   map[string]any
		want     error
	}{
		{"unknown", []string{"missing"}, nil, ErrFieldNotRegistered},
		{"wrong role", []string{"input"}, map[string]any{"input": "x"}, ErrFieldRoleMismatch},
		{"missing", []string{"eligible"}, nil, ErrFieldTypeMismatch},
		{"wrong type", []string{"eligible"}, map[string]any{"eligible": "true"}, ErrFieldTypeMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := registry.ValidateOutputs(test.declared, test.output); !errors.Is(err, test.want) {
				t.Fatalf("ValidateOutputs() error = %v, want %v", err, test.want)
			}
		})
	}
	if err := registry.ValidateOutputs([]string{"eligible", "eligible"}, map[string]any{"eligible": true}); err == nil {
		t.Fatal("duplicate output succeeded")
	}
}

func TestValidateFiltersRefusesHierarchyAttribute(t *testing.T) {
	registry := mustRegistry(t,
		field("employeeGroup", DataEnum, RoleSubjectAttribute, "Finance", "Development team"),
		field("jobLevel", DataInteger, RoleSubjectAttribute),
		field("input", DataString, RoleInput),
	)
	valid := []rule.FilterCondition{
		{Attribute: "employeeGroup", Op: rule.FilterIn, Value: []string{"Finance"}},
		{Attribute: "jobLevel", Op: rule.FilterRange, Value: []int{2, 5}},
		{Attribute: "employeeGroup", Op: rule.FilterExists},
	}
	if err := registry.ValidateFilters(valid); err != nil {
		t.Fatal(err)
	}
	hierarchyFilter := []rule.FilterCondition{{Attribute: "department", Op: rule.FilterEquals, Value: "Engineering"}}
	err := registry.ValidateFilters(hierarchyFilter)
	if !errors.Is(err, ErrHierarchyAttributeFilter) || !errors.Is(err, ErrFieldNotRegistered) {
		t.Fatalf("hierarchy filter error = %v", err)
	}

	tests := []struct {
		name   string
		filter rule.FilterCondition
		want   error
	}{
		{"wrong role", rule.FilterCondition{Attribute: "input", Op: rule.FilterEquals, Value: "x"}, ErrFieldRoleMismatch},
		{"enum outside", rule.FilterCondition{Attribute: "employeeGroup", Op: rule.FilterEquals, Value: "Legal"}, ErrFieldTypeMismatch},
		{"empty set", rule.FilterCondition{Attribute: "employeeGroup", Op: rule.FilterIn, Value: []string{}}, nil},
		{"bad range", rule.FilterCondition{Attribute: "jobLevel", Op: rule.FilterRange, Value: []int{1}}, nil},
		{"exists value", rule.FilterCondition{Attribute: "employeeGroup", Op: rule.FilterExists, Value: "x"}, nil},
		{"unsupported", rule.FilterCondition{Attribute: "employeeGroup", Op: "contains", Value: "Fin"}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := registry.ValidateFilters([]rule.FilterCondition{test.filter})
			if err == nil || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("ValidateFilters() error = %v", err)
			}
		})
	}
}

func TestEveryDataType(t *testing.T) {
	fields := []FieldDefinition{
		field("text", DataString, RoleInput), field("integer", DataInteger, RoleInput),
		field("decimal", DataDecimal, RoleInput), field("boolean", DataBoolean, RoleInput),
		field("date", DataDate, RoleInput), field("datetime", DataDateTime, RoleInput),
		field("enum", DataEnum, RoleInput, "A"), field("money", DataMoney, RoleInput),
	}
	registry := mustRegistry(t, fields...)
	context := map[string]any{
		"text": "value", "integer": float64(2), "decimal": json.Number("1.25"), "boolean": true,
		"date": "2026-10-14", "datetime": time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC),
		"enum": "A", "money": "10.25",
	}
	declared := []string{"text", "integer", "decimal", "boolean", "date", "datetime", "enum", "money"}
	if err := registry.ValidateInputs("leave", declared, context); err != nil {
		t.Fatal(err)
	}
	context["date"] = "today"
	if err := registry.ValidateInputs("leave", declared, context); !errors.Is(err, ErrFieldTypeMismatch) {
		t.Fatalf("invalid date error = %v", err)
	}
	context["date"] = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	context["datetime"] = "2026-10-14T01:02:03Z"
	if err := registry.ValidateInputs("leave", declared, context); err != nil {
		t.Fatal(err)
	}
	context["money"] = 10.25
	if err := registry.ValidateInputs("leave", declared, context); !errors.Is(err, ErrFieldTypeMismatch) {
		t.Fatalf("binary money error = %v", err)
	}
}

func TestInvalidRuleFixtureFilterIsRefused(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "invalid", "filter-uses-department.json"))
	if err != nil {
		t.Fatal(err)
	}
	var definition rule.RuleDefinition
	if err := json.Unmarshal(data, &definition); err != nil {
		t.Fatal(err)
	}
	registry := mustRegistry(t, field("employeeGroup", DataEnum, RoleSubjectAttribute, "Finance"))
	if err := registry.ValidateFilters(definition.Scope.Filters); !errors.Is(err, ErrHierarchyAttributeFilter) {
		t.Fatalf("hierarchy filter error = %v", err)
	}
}

func field(name string, dataType DataType, role Role, allowed ...string) FieldDefinition {
	return FieldDefinition{Name: name, Domain: "leave", DataType: dataType, AllowedValues: allowed, OwnerService: "owner", Status: "active", Role: role}
}

func mutate(field FieldDefinition, change func(*FieldDefinition)) FieldDefinition {
	change(&field)
	return field
}

func mustRegistry(t testing.TB, fields ...FieldDefinition) *Registry {
	t.Helper()
	registry, err := NewRegistry(fields)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
