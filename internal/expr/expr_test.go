package expr

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/internal/decimalx"
)

func TestArithmeticIsExactAndPrecedenceAware(t *testing.T) {
	tests := []struct {
		source string
		facts  map[string]any
		want   string
	}{
		{"0.1 + 0.2", nil, "0.3"},
		{"2 + 3 * 4", nil, "14"},
		{"(2 + 3) * 4", nil, "20"},
		{"-$amount + +2", map[string]any{"amount": json.Number("3.5")}, "-1.5"},
		{"base / 4", map[string]any{"base": 10}, "2.5"},
		{"round(10 / 3, 2)", nil, "3.33"},
	}
	for _, test := range tests {
		t.Run(test.source, func(t *testing.T) {
			parsed, err := Parse(test.source)
			if err != nil {
				t.Fatal(err)
			}
			value, err := parsed.Eval(test.facts)
			if err != nil {
				t.Fatal(err)
			}
			decimal, ok := value.(decimalx.Decimal)
			if !ok || decimal.String() != test.want {
				t.Fatalf("Eval() = %#v, want %s", value, test.want)
			}
		})
	}
}

func TestFunctionsAndReferences(t *testing.T) {
	facts := map[string]any{
		"joined": "2025-01-31", "validAt": "2026-03-30",
		"start": time.Date(2026, 1, 1, 10, 0, 0, 0, time.FixedZone("test", 3600)),
		"end":   "2026-01-04",
		"none":  nil,
	}
	tests := []struct {
		source string
		want   any
	}{
		{"monthsBetween(joined, $validAt)", decimalx.FromInt(13)},
		{"daysBetween(start, end)", decimalx.FromInt(3)},
		{"min(3, 1.2, 2)", decimalx.MustParse("1.2")},
		{"max(3, 1.2, 2)", decimalx.FromInt(3)},
		{"coalesce(none, 'fallback')", "fallback"},
	}
	for _, test := range tests {
		parsed, err := Parse(test.source)
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.source, err)
		}
		value, err := parsed.Eval(facts)
		if err != nil || !reflect.DeepEqual(value, test.want) {
			t.Fatalf("Eval(%q) = %#v, %v; want %#v", test.source, value, err, test.want)
		}
	}
	parsed, _ := Parse("$validAt + joined")
	if got := parsed.References(); !reflect.DeepEqual(got, []string{"joined", "validAt"}) {
		t.Fatalf("References() = %v", got)
	}
}

func TestClosedGrammarAndEvaluationErrors(t *testing.T) {
	parseErrors := []string{
		"", "unknown(1)", "a[0]", "1 +", "'unterminated", "1e", "()",
		strings.Repeat("1+", 2050) + "1",
	}
	for _, source := range parseErrors {
		if _, err := Parse(source); err == nil {
			t.Errorf("Parse(%q) succeeded", source)
		}
	}
	tests := []struct {
		source string
		facts  map[string]any
		match  string
	}{
		{"1 / 0", nil, "zero"},
		{"$missing + 1", nil, "missing field"},
		{"$value + 1", map[string]any{"value": 1.5}, "exact decimal"},
		{"'a' + 'b'", nil, "exact decimal"},
		{"round(1, -1)", nil, "between 0 and 18"},
		{"round(1, 1.2)", nil, "integer"},
		{"daysBetween('bad', '2026-01-01')", nil, "invalid date"},
		{"min()", nil, "at least one"},
		{"coalesce()", nil, "at least one"},
	}
	for _, test := range tests {
		parsed, err := Parse(test.source)
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.source, err)
		}
		if _, err := parsed.Eval(test.facts); err == nil || !strings.Contains(err.Error(), test.match) {
			t.Errorf("Eval(%q) error = %v, want %q", test.source, err, test.match)
		}
	}
	if _, err := (*Expression)(nil).Eval(nil); err == nil {
		t.Fatal("nil expression evaluated")
	}
}

func TestMetadataAndLiteralsAreDefensive(t *testing.T) {
	functions := FunctionNames()
	if !sortStrings(functions) || len(functions) != 6 {
		t.Fatalf("FunctionNames() = %v", functions)
	}
	functions[0] = "mutated"
	if FunctionNames()[0] == "mutated" {
		t.Fatal("FunctionNames aliases internal state")
	}
	operators := OperatorSymbols()
	operators[0] = "mutated"
	if OperatorSymbols()[0] == "mutated" {
		t.Fatal("OperatorSymbols aliases internal state")
	}
	literal := Literal("value")
	if value, ok := literal.Constant(); !ok || value != "value" {
		t.Fatalf("Constant() = %#v, %v", value, ok)
	}
	parsed, _ := Parse("1 + 2")
	if _, ok := parsed.Constant(); ok {
		t.Fatal("compound expression reported constant")
	}
}

func TestLexerStringsNumbersAndLimits(t *testing.T) {
	valid := map[string]any{
		`"a\n\r\t\\\"\'\u0041"`: "a\n\r\t\\\"'A",
		`'single\'quote'`:       "single'quote",
		".5e+2":                 decimalx.MustParse("50"),
		"true":                  true,
		"false":                 false,
		"null":                  nil,
	}
	for source, want := range valid {
		parsed, err := Parse(source)
		if err != nil {
			t.Fatalf("Parse(%q): %v", source, err)
		}
		value, err := parsed.Eval(nil)
		if err != nil || !reflect.DeepEqual(value, want) {
			t.Fatalf("Eval(%q) = %#v, %v; want %#v", source, value, err, want)
		}
	}
	invalid := []string{
		`"\x"`, `"\u12"`, `"\uZZZZ"`, `"trailing\`, "$", ".", "1e+", "@", "1 2",
		"min(1,)", "min(1", strings.Repeat("1+", 260) + "1",
		strings.Repeat("(", 65) + "1" + strings.Repeat(")", 65),
	}
	for _, source := range invalid {
		if _, err := Parse(source); err == nil {
			t.Errorf("Parse(%q) succeeded", source)
		}
	}
}

func TestFunctionBoundaryPaths(t *testing.T) {
	tests := []struct {
		source string
		facts  map[string]any
		want   any
		match  string
	}{
		{"coalesce(null, null)", nil, nil, ""},
		{"monthsBetween('2026-03-30', '2025-01-31')", nil, decimalx.FromInt(-13), ""},
		{"monthsBetween('2025-01-01', '2026-01-01T12:00:00Z')", nil, decimalx.FromInt(12), ""},
		{"min('bad', 1)", nil, nil, "argument 1"},
		{"max(1, 'bad')", nil, nil, "argument 2"},
		{"round('bad', 2)", nil, nil, "argument 1"},
		{"round(1)", nil, nil, "exactly two"},
		{"round(1, 19)", nil, nil, "between 0 and 18"},
		{"round(1, 999999999999999999999999)", nil, nil, "between 0 and 18"},
		{"daysBetween('2026-01-01')", nil, nil, "exactly two"},
		{"daysBetween('2026-01-01', 2)", nil, nil, "argument 2"},
		{"monthsBetween(1, '2026-01-01')", nil, nil, "argument 1"},
		{"min($missing, 1)", nil, nil, "argument 1"},
		{"1 - 2", nil, decimalx.FromInt(-1), ""},
	}
	for _, test := range tests {
		parsed, err := Parse(test.source)
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.source, err)
		}
		value, err := parsed.Eval(test.facts)
		if test.match != "" {
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Errorf("Eval(%q) error = %v, want %q", test.source, err, test.match)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(value, test.want) {
			t.Errorf("Eval(%q) = %#v, %v; want %#v", test.source, value, err, test.want)
		}
	}
	var nilExpression *Expression
	if nilExpression.References() != nil {
		t.Fatal("nil expression has references")
	}
	if _, ok := nilExpression.Constant(); ok {
		t.Fatal("nil expression is constant")
	}
}

func sortStrings(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] > values[index] {
			return false
		}
	}
	return true
}

func FuzzParseAndEval(f *testing.F) {
	for _, seed := range []string{"0.1 + 0.2", "$rate * 2", "round(1/3, 2)", "unknown(", "'text'"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		parsed, err := Parse(source)
		if err != nil {
			return
		}
		_, _ = parsed.Eval(map[string]any{"rate": json.Number("1.25")})
	})
}
