package resolve

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/arrowhr/ruleengine/internal/decimalx"
	"github.com/arrowhr/ruleengine/rule"
)

// FiltersMatch reports whether every filter matches attrs and returns the first
// failing authored condition. Filters are evaluated in authored order.
func FiltersMatch(filters []rule.FilterCondition, attrs map[string]any) (bool, *rule.FilterCondition) {
	for index := range filters {
		filter := &filters[index]
		value, exists := attrs[filter.Attribute]
		if !filterMatches(*filter, value, exists) {
			return false, filter
		}
	}
	return true, nil
}

func filterMatches(filter rule.FilterCondition, actual any, exists bool) bool {
	switch filter.Op {
	case rule.FilterExists:
		return exists
	case rule.FilterNotExists:
		return !exists
	case rule.FilterEquals:
		return exists && valuesEqual(actual, filter.Value)
	case rule.FilterIn, rule.FilterNotIn:
		matched := false
		if exists {
			matched = contains(filter.Value, actual)
		}
		if filter.Op == rule.FilterNotIn {
			return exists && !matched
		}
		return exists && matched
	case rule.FilterRange:
		if !exists {
			return false
		}
		bounds, ok := sequence(filter.Value)
		if !ok || len(bounds) != 2 {
			return false
		}
		low, lowOK := compareValues(actual, bounds[0])
		high, highOK := compareValues(actual, bounds[1])
		return lowOK && highOK && low >= 0 && high <= 0
	default:
		return false
	}
}

func contains(collection, target any) bool {
	values, ok := sequence(collection)
	if !ok {
		return false
	}
	for _, value := range values {
		if valuesEqual(target, value) {
			return true
		}
	}
	return false
}

func sequence(value any) ([]any, bool) {
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

func valuesEqual(left, right any) bool {
	if comparison, ok := compareValues(left, right); ok {
		return comparison == 0
	}
	return reflect.DeepEqual(left, right)
}

func compareValues(left, right any) (int, bool) {
	if leftNumber, ok := decimalOf(left); ok {
		rightNumber, rightOK := decimalOf(right)
		if !rightOK {
			return 0, false
		}
		return leftNumber.Cmp(rightNumber), true
	}
	leftTime, leftIsTime := left.(time.Time)
	rightTime, rightIsTime := right.(time.Time)
	if leftIsTime || rightIsTime {
		if !leftIsTime || !rightIsTime {
			return 0, false
		}
		switch {
		case leftTime.Before(rightTime):
			return -1, true
		case leftTime.After(rightTime):
			return 1, true
		default:
			return 0, true
		}
	}
	leftString, leftIsString := left.(string)
	rightString, rightIsString := right.(string)
	if leftIsString || rightIsString {
		if !leftIsString || !rightIsString {
			return 0, false
		}
		return strings.Compare(leftString, rightString), true
	}
	return 0, false
}

// decimalOf converts a numeric attribute value to an exact decimal. Scope
// filters decide money and rate boundaries, so a `range` from 0.1 to 0.3 has
// to compare exactly rather than through binary floating point. A string is
// deliberately not a number here: an enum attribute whose values happen to
// look numeric must still compare as text.
func decimalOf(value any) (decimalx.Decimal, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := decimalx.Parse(string(typed))
		return parsed, err == nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return decimalx.FromValue(typed)
	case float32:
		return parseFloat(float64(typed), 32)
	case float64:
		return parseFloat(typed, 64)
	default:
		return decimalReflected(value)
	}
}

// decimalReflected covers named numeric types, which authored JSON never
// produces but a Go caller assembling subject attributes can. It is kept off
// the common path so resolution does not reflect once per candidate rule.
func decimalReflected(value any) (decimalx.Decimal, bool) {
	if value == nil {
		return decimalx.Decimal{}, false
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return decimalx.FromInt(reflected.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return decimalx.FromValue(reflected.Uint())
	case reflect.Float32:
		return parseFloat(reflected.Float(), 32)
	case reflect.Float64:
		return parseFloat(reflected.Float(), 64)
	default:
		return decimalx.Decimal{}, false
	}
}

// parseFloat converts through the shortest representation that round-trips,
// so a caller's 0.1 becomes the exact decimal 0.1 rather than its binary
// approximation.
func parseFloat(value float64, bits int) (decimalx.Decimal, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return decimalx.Decimal{}, false
	}
	parsed, err := decimalx.Parse(strconv.FormatFloat(value, 'g', -1, bits))
	return parsed, err == nil
}

// DescribeFilterFailure returns a stable, human-readable explanation of why c
// did not match attrs.
func DescribeFilterFailure(condition rule.FilterCondition, attrs map[string]any) string {
	actual, exists := attrs[condition.Attribute]
	attribute := humanize(condition.Attribute)
	switch condition.Op {
	case rule.FilterExists:
		return fmt.Sprintf("%s is missing, rule requires it to exist", attribute)
	case rule.FilterNotExists:
		return fmt.Sprintf("%s is present, rule requires it to be absent", attribute)
	case rule.FilterEquals:
		return fmt.Sprintf("%s is %s, rule requires %s", attribute, displayActual(actual, exists), display(condition.Value))
	case rule.FilterIn:
		return fmt.Sprintf("%s is %s, rule requires one of %s", attribute, displayActual(actual, exists), display(condition.Value))
	case rule.FilterNotIn:
		return fmt.Sprintf("%s is %s, rule excludes %s", attribute, displayActual(actual, exists), display(condition.Value))
	case rule.FilterRange:
		return fmt.Sprintf("%s is %s, rule requires range %s", attribute, displayActual(actual, exists), display(condition.Value))
	default:
		return fmt.Sprintf("%s does not match unsupported filter %q", attribute, condition.Op)
	}
}

func displayActual(value any, exists bool) string {
	if !exists {
		return "missing"
	}
	return display(value)
}

func display(value any) string {
	if value == nil {
		return "null"
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err == nil {
		return string(encoded)
	}
	return fmt.Sprintf("%v", value)
}

func humanize(value string) string {
	var result strings.Builder
	for index, current := range value {
		if index > 0 && unicode.IsUpper(current) {
			result.WriteByte(' ')
		}
		result.WriteRune(unicode.ToLower(current))
	}
	return result.String()
}
