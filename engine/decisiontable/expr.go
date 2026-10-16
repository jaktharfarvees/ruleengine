package decisiontable

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/arrowhr/ruleengine/internal/decimalx"
	"github.com/arrowhr/ruleengine/internal/expr"
)

type compiledOutput struct {
	literal    any
	expression *expr.Expression
	arithmetic bool
}

func compileOutput(value any) (compiledOutput, error) {
	if containsFloat(reflect.ValueOf(value)) {
		return compiledOutput{}, fmt.Errorf("binary floating-point output values are not supported")
	}
	if containsUncarriable(reflect.ValueOf(value)) {
		return compiledOutput{}, fmt.Errorf("output values must be authored data, not a function or channel")
	}
	source, stringValue := value.(string)
	if !stringValue || !looksLikeExpression(source) {
		return compiledOutput{literal: clone(value)}, nil
	}
	parsed, err := expr.Parse(source)
	if err != nil {
		return compiledOutput{}, err
	}
	directReference := strings.HasPrefix(strings.TrimSpace(source), "$") &&
		len(parsed.References()) == 1 && strings.TrimSpace(source) == "$"+parsed.References()[0]
	return compiledOutput{expression: parsed, arithmetic: !directReference}, nil
}

func looksLikeExpression(source string) bool {
	trimmed := strings.TrimSpace(source)
	if strings.Contains(trimmed, "$") || strings.HasPrefix(trimmed, "(") {
		return true
	}
	for _, function := range expr.FunctionNames() {
		if strings.HasPrefix(trimmed, function+"(") {
			return true
		}
	}
	for _, operator := range expr.OperatorSymbols() {
		if strings.Contains(trimmed, " "+operator+" ") {
			return true
		}
	}
	return false
}

func resolveOutputs(outputs map[string]compiledOutput, facts map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(outputs))
	keys := make([]string, 0, len(outputs))
	for key := range outputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		output := outputs[key]
		if output.expression == nil {
			result[key] = clone(output.literal)
			continue
		}
		value, err := output.expression.Eval(facts)
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", key, err)
		}
		if output.arithmetic {
			if currency, ok := facts["currency"].(string); ok {
				if decimal, exact := decimalx.FromValue(value); exact {
					value = decimalx.QuantizeCurrency(decimal, currency)
				}
			}
		}
		if containsFloat(reflect.ValueOf(value)) {
			return nil, fmt.Errorf("output %q resolved to a binary floating-point value", key)
		}
		result[key] = clone(value)
	}
	return result, nil
}

func containsFloat(value reflect.Value) bool {
	if !value.IsValid() {
		return false
	}
	switch value.Kind() {
	case reflect.Float32, reflect.Float64:
		return true
	case reflect.Interface, reflect.Pointer:
		return !value.IsNil() && containsFloat(value.Elem())
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if containsFloat(value.Index(index)) {
				return true
			}
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if containsFloat(iterator.Value()) {
				return true
			}
		}
	}
	return false
}

// containsUncarriable reports whether value holds something an authored rule
// can never carry. Rules arrive as JSON, so a function or channel in an output
// is a caller programming error; it is refused at load time rather than
// reaching a decision or an audit record.
func containsUncarriable(value reflect.Value) bool {
	if !value.IsValid() {
		return false
	}
	switch value.Kind() {
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return true
	case reflect.Interface, reflect.Pointer:
		return !value.IsNil() && containsUncarriable(value.Elem())
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if containsUncarriable(value.Index(index)) {
				return true
			}
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if containsUncarriable(iterator.Value()) {
				return true
			}
		}
	}
	return false
}

func clone(value any) any {
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
		copy := reflect.New(value.Type()).Elem()
		copy.Set(cloneReflect(value.Elem()))
		return copy
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.New(value.Elem().Type())
		copy.Elem().Set(cloneReflect(value.Elem()))
		return copy
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			copy.Index(index).Set(cloneReflect(value.Index(index)))
		}
		return copy
	case reflect.Array:
		copy := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			copy.Index(index).Set(cloneReflect(value.Index(index)))
		}
		return copy
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			copy.SetMapIndex(iterator.Key(), cloneReflect(iterator.Value()))
		}
		return copy
	default:
		return value
	}
}
