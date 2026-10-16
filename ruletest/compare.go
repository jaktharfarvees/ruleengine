package ruletest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/arrowhr/ruleengine/internal/decimalx"
)

// Comparison here is hand-written rather than reflective because the values
// being compared are authored JSON on one side and engine output on the other,
// and those spell the same number differently. A golden case writes 6.5 and
// the engine returns an exact decimal; a test that failed on that would be
// useless. See docs/adr/0005-dependency-choices.md for why there is no
// diffing library.

// diffResults reports how an actual result differs from an expected one, or an
// empty string when they agree. Every difference is listed, so one run shows
// everything that changed.
func diffResults(want, got map[string]any) string {
	names := make([]string, 0, len(want)+len(got))
	seen := make(map[string]struct{}, len(want)+len(got))
	for _, source := range []map[string]any{want, got} {
		for name := range source {
			if _, done := seen[name]; done {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var lines []string
	for _, name := range names {
		expected, wanted := want[name]
		actual, produced := got[name]
		switch {
		case wanted && !produced:
			lines = append(lines, fmt.Sprintf("  %s: missing, want %s", name, render(expected)))
		case !wanted && produced:
			lines = append(lines, fmt.Sprintf("  %s: unexpected %s", name, render(actual)))
		case !sameScalar(actual, expected):
			lines = append(lines, fmt.Sprintf("  %s: got %s, want %s", name, render(actual), render(expected)))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// sameScalar reports whether two authored or produced values mean the same
// thing. Numbers compare as exact decimals whichever way they were spelled, so
// a money value is never compared through binary floating point.
func sameScalar(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if leftBool, ok := left.(bool); ok {
		rightBool, isBool := right.(bool)
		return isBool && leftBool == rightBool
	}
	if leftNumber, leftIsNumber := exactNumber(left); leftIsNumber {
		if rightNumber, rightIsNumber := exactNumber(right); rightIsNumber {
			return leftNumber.Cmp(rightNumber) == 0
		}
		return false
	}
	if leftText, ok := left.(string); ok {
		rightText, isText := right.(string)
		return isText && leftText == rightText
	}
	return reflect.DeepEqual(left, right)
}

// exactNumber converts a value to an exact decimal when it is one.
//
// A plain string is only treated as a number when it parses as one, because an
// enum value like "ANNUAL" must stay text while a money value authored as
// "12.50" must compare numerically. A value that is a number on one side and
// text on the other is reported as different rather than coerced.
func exactNumber(value any) (decimalx.Decimal, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := decimalx.Parse(string(typed))
		return parsed, err == nil
	case string:
		parsed, err := decimalx.Parse(typed)
		return parsed, err == nil
	case decimalx.Decimal:
		return typed, true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return decimalx.FromValue(typed)
	case float32, float64:
		// Through the shortest representation that round-trips, so a caller's
		// 0.1 becomes the exact decimal 0.1 rather than its binary form.
		parsed, err := decimalx.Parse(fmt.Sprintf("%v", typed))
		return parsed, err == nil
	default:
		return decimalx.Decimal{}, false
	}
}

// render prints a value the way a reader recognises it, with its type when the
// type is the surprise.
func render(value any) string {
	switch typed := value.(type) {
	case nil:
		return "nil"
	case string:
		return fmt.Sprintf("%q", typed)
	case bool:
		return fmt.Sprintf("%t", typed)
	case json.Number:
		return string(typed)
	default:
		return fmt.Sprintf("%v (%T)", value, value)
	}
}
