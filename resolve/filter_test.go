package resolve_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/resolve"
	"github.com/arrowhr/ruleengine/rule"
)

func TestFiltersMatchOperators(t *testing.T) {
	day := time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		filter     rule.FilterCondition
		attributes map[string]any
		want       bool
	}{
		{"equals string", filter("team", rule.FilterEquals, "Finance"), map[string]any{"team": "Finance"}, true},
		{"equals numeric JSON shape", filter("months", rule.FilterEquals, float64(6)), map[string]any{"months": 6}, true},
		{"equals signed integer", filter("months", rule.FilterEquals, int64(6)), map[string]any{"months": int8(6)}, true},
		{"equals unsigned integer", filter("months", rule.FilterEquals, uint64(6)), map[string]any{"months": uint16(6)}, true},
		{"equals float32", filter("months", rule.FilterEquals, float32(6)), map[string]any{"months": float32(6)}, true},
		{"equals json number", filter("months", rule.FilterEquals, json.Number("6")), map[string]any{"months": 6}, true},
		{"invalid json number", filter("months", rule.FilterEquals, json.Number("six")), map[string]any{"months": 6}, false},
		{"equals type mismatch", filter("months", rule.FilterEquals, "6"), map[string]any{"months": 6}, false},
		{"equals missing", filter("team", rule.FilterEquals, "Finance"), nil, false},
		{"in interface slice", filter("team", rule.FilterIn, []any{"Finance", "Engineering"}), map[string]any{"team": "Finance"}, true},
		{"in typed slice", filter("level", rule.FilterIn, []int{2, 3}), map[string]any{"level": float64(3)}, true},
		{"in scalar malformed", filter("level", rule.FilterIn, 3), map[string]any{"level": 3}, false},
		{"in nil malformed", filter("level", rule.FilterIn, nil), map[string]any{"level": 3}, false},
		{"not in", filter("team", rule.FilterNotIn, []string{"Legal"}), map[string]any{"team": "Finance"}, true},
		{"not in missing", filter("team", rule.FilterNotIn, []string{"Legal"}), nil, false},
		{"range lower inclusive", filter("months", rule.FilterRange, []int{6, 12}), map[string]any{"months": 6}, true},
		{"range upper inclusive", filter("months", rule.FilterRange, [2]float64{6, 12}), map[string]any{"months": 12}, true},
		{"range outside", filter("months", rule.FilterRange, []int{6, 12}), map[string]any{"months": 13}, false},
		{"range string", filter("grade", rule.FilterRange, []string{"A", "C"}), map[string]any{"grade": "B"}, true},
		{"range time", filter("day", rule.FilterRange, []time.Time{day.Add(-time.Hour), day.Add(time.Hour)}), map[string]any{"day": day}, true},
		{"range wrong bound type", filter("months", rule.FilterRange, []any{"6", 12}), map[string]any{"months": 8}, false},
		{"range incomparable", filter("enabled", rule.FilterRange, []bool{false, true}), map[string]any{"enabled": true}, false},
		{"range malformed", filter("months", rule.FilterRange, []int{6}), map[string]any{"months": 8}, false},
		{"exists with nil value", filter("manager", rule.FilterExists, nil), map[string]any{"manager": nil}, true},
		{"not exists", filter("manager", rule.FilterNotExists, nil), map[string]any{}, true},
		{"unsupported", filter("team", rule.FilterOperator("contains"), "Fin"), map[string]any{"team": "Finance"}, false},
		{"nan never equals", filter("value", rule.FilterEquals, math.NaN()), map[string]any{"value": math.NaN()}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matched, failed := resolve.FiltersMatch([]rule.FilterCondition{test.filter}, test.attributes)
			if matched != test.want {
				t.Fatalf("FiltersMatch() = %v, want %v", matched, test.want)
			}
			if test.want && failed != nil {
				t.Fatalf("successful filter returned failure %#v", failed)
			}
			if !test.want && failed == nil {
				t.Fatal("failed filter was not returned")
			}
		})
	}
}

func TestFiltersMatchStopsAtFirstFailure(t *testing.T) {
	filters := []rule.FilterCondition{
		filter("country", rule.FilterEquals, "Canada"),
		filter("team", rule.FilterEquals, "Engineering"),
	}
	matched, failed := resolve.FiltersMatch(filters, map[string]any{"country": "Sri Lanka", "team": "Finance"})
	if matched || failed != &filters[0] {
		t.Fatalf("matched=%v failed=%p, want first filter %p", matched, failed, &filters[0])
	}
	matched, failed = resolve.FiltersMatch(nil, nil)
	if !matched || failed != nil {
		t.Fatalf("empty filters = %v, %#v", matched, failed)
	}
}

func TestDescribeFilterFailure(t *testing.T) {
	tests := []struct {
		condition rule.FilterCondition
		attrs     map[string]any
		want      string
	}{
		{filter("employeeGroup", rule.FilterEquals, "Development team"), map[string]any{"employeeGroup": "Finance"}, "employee group is Finance, rule requires Development team"},
		{filter("team", rule.FilterIn, []string{"A", "B"}), map[string]any{"team": "C"}, `team is C, rule requires one of ["A","B"]`},
		{filter("team", rule.FilterNotIn, []string{"A"}), map[string]any{"team": "A"}, `team is A, rule excludes ["A"]`},
		{filter("months", rule.FilterRange, []int{6, 12}), nil, "months is missing, rule requires range [6,12]"},
		{filter("manager", rule.FilterExists, nil), nil, "manager is missing, rule requires it to exist"},
		{filter("manager", rule.FilterNotExists, nil), map[string]any{"manager": "present"}, "manager is present, rule requires it to be absent"},
		{filter("team", rule.FilterOperator("bad"), nil), nil, `team does not match unsupported filter "bad"`},
		{filter("callback", rule.FilterEquals, nil), map[string]any{"callback": func() {}}, "callback is 0x"},
	}
	for _, test := range tests {
		got := resolve.DescribeFilterFailure(test.condition, test.attrs)
		if test.want == "callback is 0x" {
			if !strings.HasPrefix(got, test.want) {
				t.Errorf("DescribeFilterFailure() = %q, want prefix %q", got, test.want)
			}
			continue
		}
		if got != test.want {
			t.Errorf("DescribeFilterFailure() = %q, want %q", got, test.want)
		}
	}
}

func filter(attribute string, operator rule.FilterOperator, value any) rule.FilterCondition {
	return rule.FilterCondition{Attribute: attribute, Op: operator, Value: value}
}

func TestRangeAndEqualityUseExactDecimals(t *testing.T) {
	// A money or rate boundary must not be decided through binary floating
	// point: 0.1 + 0.2 is not 0.3 in float64, and a payroll band edge is
	// exactly where a filter has to be right.
	tests := []struct {
		name   string
		filter rule.FilterCondition
		attrs  map[string]any
		want   bool
	}{
		{
			name:   "decimal string at the lower bound",
			filter: rule.FilterCondition{Attribute: "rate", Op: rule.FilterRange, Value: []any{json.Number("0.1"), json.Number("0.3")}},
			attrs:  map[string]any{"rate": json.Number("0.1")},
			want:   true,
		},
		{
			name:   "decimal string just outside the upper bound",
			filter: rule.FilterCondition{Attribute: "rate", Op: rule.FilterRange, Value: []any{json.Number("0.1"), json.Number("0.3")}},
			attrs:  map[string]any{"rate": json.Number("0.30000000000000004")},
			want:   false,
		},
		{
			name:   "high precision equality is not rounded together",
			filter: rule.FilterCondition{Attribute: "amount", Op: rule.FilterEquals, Value: json.Number("1.000000000000000001")},
			attrs:  map[string]any{"amount": json.Number("1.000000000000000002")},
			want:   false,
		},
		{
			name:   "large integers beyond float53 stay distinct",
			filter: rule.FilterCondition{Attribute: "cents", Op: rule.FilterEquals, Value: json.Number("9007199254740993")},
			attrs:  map[string]any{"cents": json.Number("9007199254740992")},
			want:   false,
		},
		{
			name:   "a native float compares by its shortest representation",
			filter: rule.FilterCondition{Attribute: "rate", Op: rule.FilterEquals, Value: json.Number("0.1")},
			attrs:  map[string]any{"rate": 0.1},
			want:   true,
		},
		{
			name:   "a numeric-looking enum still compares as text",
			filter: rule.FilterCondition{Attribute: "grade", Op: rule.FilterEquals, Value: "07"},
			attrs:  map[string]any{"grade": "7"},
			want:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matched, failed := resolve.FiltersMatch([]rule.FilterCondition{test.filter}, test.attrs)
			if matched != test.want {
				t.Fatalf("attrs %v against %v: matched = %t, want %t (failed on %v)", test.attrs, test.filter, matched, test.want, failed)
			}
		})
	}
}
