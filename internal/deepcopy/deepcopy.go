// Package deepcopy copies JSON-like rule-engine values without changing their
// concrete Go types. It is used where caller mutation must not alter retained
// audit or idempotency state.
package deepcopy

import "reflect"

// Value returns an independent copy of value. Scalar values are returned as
// is; pointers, interfaces, slices, arrays, and maps are copied recursively.
func Value(value any) any {
	if value == nil {
		return nil
	}
	return copyReflect(reflect.ValueOf(value)).Interface()
}

// Map returns an independent recursive copy of a string-keyed value map.
func Map(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = Value(value)
	}
	return result
}

func copyReflect(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(copyReflect(value.Elem()))
		return cloned
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Elem().Type())
		cloned.Elem().Set(copyReflect(value.Elem()))
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			cloned.Index(index).Set(copyReflect(value.Index(index)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			cloned.Index(index).Set(copyReflect(value.Index(index)))
		}
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			cloned.SetMapIndex(iterator.Key(), copyReflect(iterator.Value()))
		}
		return cloned
	default:
		return value
	}
}
