package store

import (
	"reflect"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/rule"
)

func cloneRule(definition rule.RuleDefinition) rule.RuleDefinition {
	definition.DeclaredInputs = append([]string(nil), definition.DeclaredInputs...)
	definition.DeclaredOutputs = append([]string(nil), definition.DeclaredOutputs...)
	definition.Guards = append([]string(nil), definition.Guards...)
	definition.Metadata.Tags = append([]string(nil), definition.Metadata.Tags...)
	definition.AttributeFilters = append([]rule.FilterCondition(nil), definition.AttributeFilters...)
	for index := range definition.AttributeFilters {
		definition.AttributeFilters[index].Value = cloneAny(definition.AttributeFilters[index].Value)
	}
	definition.Scope.Filters = append([]rule.FilterCondition(nil), definition.Scope.Filters...)
	for index := range definition.Scope.Filters {
		definition.Scope.Filters[index].Value = cloneAny(definition.Scope.Filters[index].Value)
	}
	definition.Logic.Rows = append([]rule.DecisionRow(nil), definition.Logic.Rows...)
	for index := range definition.Logic.Rows {
		row := &definition.Logic.Rows[index]
		row.When = cloneStringMap(row.When)
		row.Then = cloneAnyMap(row.Then)
	}
	if definition.TieBreakPriority != nil {
		priority := *definition.TieBreakPriority
		definition.TieBreakPriority = &priority
	}
	if definition.Effective.ValidTo != nil {
		validTo := *definition.Effective.ValidTo
		definition.Effective.ValidTo = &validTo
	}
	if definition.Metadata.ApprovedAt != nil {
		approvedAt := *definition.Metadata.ApprovedAt
		definition.Metadata.ApprovedAt = &approvedAt
	}
	return definition
}

func cloneManifest(manifest dataset.Manifest) dataset.Manifest {
	manifest = manifest.Canonical()
	manifest.AllowedAggregates = append([]dataset.AggregateFn(nil), manifest.AllowedAggregates...)
	manifest.Columns = append([]dataset.Column(nil), manifest.Columns...)
	for index := range manifest.Columns {
		manifest.Columns[index].AllowedValues = append([]string(nil), manifest.Columns[index].AllowedValues...)
		manifest.Columns[index].Values = append([]string(nil), manifest.Columns[index].Values...)
	}
	return manifest
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneAny(value)
	}
	return result
}

func cloneAny(value any) any {
	if value == nil {
		return nil
	}
	return cloneReflect(reflect.ValueOf(value)).Interface()
}

func cloneReflect(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(cloneReflect(value.Elem()))
		return cloned
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Elem().Type())
		cloned.Elem().Set(cloneReflect(value.Elem()))
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			cloned.Index(index).Set(cloneReflect(value.Index(index)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			cloned.Index(index).Set(cloneReflect(value.Index(index)))
		}
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			cloned.SetMapIndex(iterator.Key(), cloneReflect(iterator.Value()))
		}
		return cloned
	default:
		return value
	}
}
