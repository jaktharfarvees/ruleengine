package expr

import (
	"fmt"
	"sort"
	"time"

	"github.com/arrowhr/ruleengine/internal/decimalx"
)

type function func([]any) (any, error)

var functionTable = map[string]function{
	"coalesce":      evalCoalesce,
	"daysBetween":   evalDaysBetween,
	"max":           evalMax,
	"min":           evalMin,
	"monthsBetween": evalMonthsBetween,
	"round":         evalRound,
}

// FunctionNames returns the sorted allowlist accepted by Parse.
func FunctionNames() []string {
	names := make([]string, 0, len(functionTable))
	for name := range functionTable {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func evalCoalesce(arguments []any) (any, error) {
	if len(arguments) == 0 {
		return nil, fmt.Errorf("coalesce requires at least one argument")
	}
	for _, argument := range arguments {
		if argument != nil {
			return argument, nil
		}
	}
	return nil, nil
}

func evalMin(arguments []any) (any, error) { return evalExtreme("min", arguments, -1) }
func evalMax(arguments []any) (any, error) { return evalExtreme("max", arguments, 1) }

func evalExtreme(name string, arguments []any, direction int) (any, error) {
	if len(arguments) == 0 {
		return nil, fmt.Errorf("%s requires at least one argument", name)
	}
	selected, err := requireDecimal(arguments[0])
	if err != nil {
		return nil, fmt.Errorf("%s argument 1: %w", name, err)
	}
	for index, argument := range arguments[1:] {
		candidate, err := requireDecimal(argument)
		if err != nil {
			return nil, fmt.Errorf("%s argument %d: %w", name, index+2, err)
		}
		if candidate.Cmp(selected) == direction {
			selected = candidate
		}
	}
	return selected, nil
}

func evalRound(arguments []any) (any, error) {
	if len(arguments) != 2 {
		return nil, fmt.Errorf("round requires exactly two arguments")
	}
	value, err := requireDecimal(arguments[0])
	if err != nil {
		return nil, fmt.Errorf("round argument 1: %w", err)
	}
	scale, err := requireDecimal(arguments[1])
	if err != nil || !scale.IsInteger() {
		return nil, fmt.Errorf("round scale must be an integer")
	}
	var digits int32
	if _, scanErr := fmt.Sscan(scale.String(), &digits); scanErr != nil || digits < 0 || digits > 18 {
		return nil, fmt.Errorf("round scale must be between 0 and 18")
	}
	return value.Quantize(digits), nil
}

func evalDaysBetween(arguments []any) (any, error) {
	start, end, err := dateArguments("daysBetween", arguments)
	if err != nil {
		return nil, err
	}
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	return decimalx.FromInt(int64(end.Sub(start) / (24 * time.Hour))), nil
}

func evalMonthsBetween(arguments []any) (any, error) {
	start, end, err := dateArguments("monthsBetween", arguments)
	if err != nil {
		return nil, err
	}
	months := (end.Year()-start.Year())*12 + int(end.Month()-start.Month())
	if months > 0 && end.Day() < start.Day() {
		months--
	} else if months < 0 && end.Day() > start.Day() {
		months++
	}
	return decimalx.FromInt(int64(months)), nil
}

func dateArguments(name string, arguments []any) (time.Time, time.Time, error) {
	if len(arguments) != 2 {
		return time.Time{}, time.Time{}, fmt.Errorf("%s requires exactly two arguments", name)
	}
	start, err := asDate(arguments[0])
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%s argument 1: %w", name, err)
	}
	end, err := asDate(arguments[1])
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%s argument 2: %w", name, err)
	}
	return start, end, nil
}

func asDate(value any) (time.Time, error) {
	if typed, ok := value.(time.Time); ok {
		return typed, nil
	}
	text, ok := value.(string)
	if !ok {
		return time.Time{}, fmt.Errorf("value %T is not a date", value)
	}
	for _, layout := range []string{time.DateOnly, time.RFC3339Nano} {
		parsed, err := time.Parse(layout, text)
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date string")
}
