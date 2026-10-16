package dataset

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

var compileAt = time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC)

func testParams() Params {
	return Params{
		Tenant: "RWB", EmployeeID: "E1001", NodeID: "BR001",
		AsOf: compileAt, ValidAt: compileAt, RowLimit: 500,
	}
}

func datasetSource() registry.FactSource {
	return registry.FactSource{
		Kind: registry.FactDataset, Dataset: "attendance.days",
		Filters: []rule.FilterCondition{
			{Attribute: "employeeId", Op: rule.FilterEquals, Value: TokenEmployeeID},
		},
		Window:    &registry.FactWindow{Column: "workDate", RelativeTo: TokenValidAt, LastDays: 30},
		Aggregate: &registry.FactAggregate{Fn: AggregateStreak, On: "isLate", Unit: "days"},
	}
}

func TestCompileProducesABoundedScopedQuery(t *testing.T) {
	query, notes, err := Compile(datasetSource(), validManifest(), testParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none", notes)
	}
	if query.Dataset() != "attendance.days" || query.DatasetVersion() != "1.0.0" {
		t.Fatalf("query identifies %q at %q", query.Dataset(), query.DatasetVersion())
	}
	// The manifest's own cap is tighter than the budget's, so neither alone
	// decides the limit: the lower of the two does.
	if query.RowLimit() != 500 {
		t.Fatalf("row limit = %d, want the budget's 500", query.RowLimit())
	}
	window, windowed := query.Window()
	if !windowed || window.Column != "workDate" {
		t.Fatalf("window = %+v, %t", window, windowed)
	}
	if !window.To.Equal(compileAt) || !window.From.Equal(compileAt.AddDate(0, 0, -30)) {
		t.Fatalf("window resolved to %s..%s", window.From, window.To)
	}
	if aggregate := query.Aggregate(); aggregate.Fn != AggregateStreak || aggregate.On != "isLate" || aggregate.Unit != "days" {
		t.Fatalf("aggregate = %+v", aggregate)
	}
	// The employee token was substituted from Params, not left as a token.
	predicates := query.Predicates()
	if len(predicates) != 2 || predicates[0].Values[0] != "E1001" {
		t.Fatalf("predicates = %+v", predicates)
	}
}

func TestCompileAppliesTheTighterRowLimit(t *testing.T) {
	manifest := validManifest()
	manifest.RowLimit = 10
	query, _, err := Compile(datasetSource(), manifest, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if query.RowLimit() != 10 {
		t.Fatalf("row limit = %d, want the manifest's tighter 10", query.RowLimit())
	}

	// A manifest with no cap of its own leaves the budget in charge.
	manifest.RowLimit = 0
	query, _, err = Compile(datasetSource(), manifest, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if query.RowLimit() != 500 {
		t.Fatalf("row limit = %d, want the budget's 500", query.RowLimit())
	}
}

func TestCompileRefusals(t *testing.T) {
	tests := []struct {
		name   string
		change func(*registry.FactSource, *Manifest, *Params)
		code   string
	}{
		{"wrong kind", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Kind = registry.FactPassed
		}, ReasonUnresolvable},
		{"invalid manifest", func(_ *registry.FactSource, m *Manifest, _ *Params) {
			m.Grain = ""
		}, ReasonDatasetNotPublished},
		{"declared dataset disagrees", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Dataset = "other.thing"
		}, ReasonDatasetNotPublished},
		{"no tenant", func(_ *registry.FactSource, _ *Manifest, p *Params) {
			p.Tenant = ""
		}, ReasonUnresolvable},
		{"draft dataset", func(_ *registry.FactSource, m *Manifest, _ *Params) {
			m.Status = "draft"
		}, ReasonDatasetNotPublished},
		{"no row limit", func(_ *registry.FactSource, _ *Manifest, p *Params) {
			p.RowLimit = 0
		}, ReasonUnresolvable},
		{"undeclared filter column", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Attribute = "salary"
		}, ReasonColumnNotAllowed},
		{"non-filterable column", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Attribute = "workedHours"
		}, ReasonColumnNotAllowed},
		{"authored tenant filter", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Attribute = TenantColumn
		}, ReasonColumnNotAllowed},
		{"query syntax in a column name", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Attribute = "employeeId; DROP TABLE x"
		}, ReasonRawQueryRejected},
		{"query syntax in a value", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Value = "E1001 UNION SELECT 1"
		}, ReasonRawQueryRejected},
		{"unknown token", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Value = "$subject.salary"
		}, ReasonRawQueryRejected},
		{"token as a fragment", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Value = "E-" + TokenEmployeeID
		}, ReasonRawQueryRejected},
		{"no filter value", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Value = nil
		}, ReasonUnresolvable},
		{"wrong operand count", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Op = rule.FilterRange
			s.Filters[0].Value = []any{"a"}
		}, ReasonUnresolvable},
		{"empty set", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Op = rule.FilterIn
			s.Filters[0].Value = []any{}
		}, ReasonUnresolvable},
		{"existence operator with a value", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Op = rule.FilterExists
			s.Filters[0].Value = "x"
		}, ReasonUnresolvable},
		{"unsupported operator", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters[0].Op = "regex"
		}, ReasonUnresolvable},
		{"value of the wrong type", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters = []rule.FilterCondition{{Attribute: "lateMinutes", Op: rule.FilterEquals, Value: "many"}}
		}, ReasonUnresolvable},
		{"enum value outside the set", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Filters = []rule.FilterCondition{{Attribute: "status", Op: rule.FilterEquals, Value: "SOMETIMES"}}
		}, ReasonUnresolvable},
		{"token without the request value", func(s *registry.FactSource, _ *Manifest, p *Params) {
			p.EmployeeID = ""
		}, ReasonRawQueryRejected},
		{"undeclared window column", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Window.Column = "nowhere"
		}, ReasonColumnNotAllowed},
		{"non-windowable column", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Window.Column = "shiftCode"
		}, ReasonColumnNotAllowed},
		{"column-pair window", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Window.FromColumn = "workDate"
		}, ReasonUnresolvable},
		{"window too wide", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Window.LastDays = 400
		}, ReasonWindowTooWide},
		{"unknown relativeTo", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Window.RelativeTo = "$yesterday"
		}, ReasonRawQueryRejected},
		{"relative to a zero time", func(s *registry.FactSource, _ *Manifest, p *Params) {
			s.Window.RelativeTo = TokenAsOf
			p.AsOf = time.Time{}
		}, ReasonUnresolvable},
		{"no aggregate", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Aggregate = nil
		}, ReasonUnresolvable},
		{"unknown aggregate", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Aggregate.Fn = "median"
		}, ReasonAggregateUnsupported},
		{"aggregate the dataset withholds", func(s *registry.FactSource, m *Manifest, _ *Params) {
			m.Aggregates = []AggregateFn{AggregateCount}
		}, ReasonAggregateUnsupported},
		{"query syntax in the aggregate column", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Aggregate.On = "isLate; DROP TABLE x"
		}, ReasonRawQueryRejected},
		{"query syntax in the aggregate unit", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Aggregate.Unit = "days--"
		}, ReasonRawQueryRejected},
		{"row aggregate naming a column", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Aggregate.Fn = AggregateCount
		}, ReasonUnresolvable},
		{"column aggregate naming none", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Aggregate.On = ""
		}, ReasonUnresolvable},
		{"undeclared aggregate column", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Aggregate.On = "nowhere"
		}, ReasonColumnNotAllowed},
		{"non-aggregatable column", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Aggregate.On = "shiftCode"
		}, ReasonColumnNotAllowed},
		{"order-dependent aggregate with no window", func(s *registry.FactSource, _ *Manifest, _ *Params) {
			s.Window = nil
		}, ReasonUnresolvable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, manifest, params := datasetSource(), validManifest(), testParams()
			test.change(&source, &manifest, &params)
			_, _, err := Compile(source, manifest, params)
			if err == nil {
				t.Fatal("Compile accepted it")
			}
			var fault *Fault
			if !errors.As(err, &fault) {
				t.Fatalf("error is not a Fault: %v", err)
			}
			if fault.Code != test.code {
				t.Fatalf("code = %q, want %q: %v", fault.Code, test.code, err)
			}
		})
	}
}

func TestCompileNotes(t *testing.T) {
	// A deprecated dataset still answers, so a migration can be driven from
	// the audit trail rather than by an outage.
	manifest := validManifest()
	manifest.Status = Deprecated
	_, notes, err := Compile(datasetSource(), manifest, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0] != ReasonDatasetDeprecated {
		t.Fatalf("notes = %v, want %s", notes, ReasonDatasetDeprecated)
	}

	// The subject key is the one personal filter every per-employee fact
	// needs, so it earns no note.
	_, notes, err = Compile(datasetSource(), validManifest(), testParams())
	if err != nil || len(notes) != 0 {
		t.Fatalf("notes = %v, err = %v; the subject key should be unremarkable", notes, err)
	}

	// Any other personal column in a filter is worth recording.
	manifest = validManifest()
	manifest.Columns[4].PII = true // shiftCode
	source := datasetSource()
	source.Filters = append(source.Filters,
		rule.FilterCondition{Attribute: "shiftCode", Op: rule.FilterEquals, Value: "MORNING"})
	_, notes, err = Compile(source, manifest, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0] != ReasonPIIColumnInFilter {
		t.Fatalf("notes = %v, want %s", notes, ReasonPIIColumnInFilter)
	}
}

func TestCompileHandlesEveryOperator(t *testing.T) {
	tests := []struct {
		name   string
		filter rule.FilterCondition
		values int
	}{
		{"equals", rule.FilterCondition{Attribute: "shiftCode", Op: rule.FilterEquals, Value: "MORNING"}, 1},
		{"in", rule.FilterCondition{Attribute: "status", Op: rule.FilterIn, Value: []any{"OK", "LATE"}}, 2},
		{"in from strings", rule.FilterCondition{Attribute: "status", Op: rule.FilterIn, Value: []string{"OK"}}, 1},
		{"notIn", rule.FilterCondition{Attribute: "status", Op: rule.FilterNotIn, Value: []any{"LATE"}}, 1},
		{"range", rule.FilterCondition{Attribute: "lateMinutes", Op: rule.FilterRange, Value: []any{1, 60}}, 2},
		{"exists", rule.FilterCondition{Attribute: "shiftCode", Op: rule.FilterExists}, 0},
		{"notExists", rule.FilterCondition{Attribute: "shiftCode", Op: rule.FilterNotExists}, 0},
		{"a token for the node", rule.FilterCondition{Attribute: "shiftCode", Op: rule.FilterEquals, Value: TokenNodeID}, 1},
		{"a date token", rule.FilterCondition{Attribute: "workDate", Op: rule.FilterEquals, Value: TokenAsOf}, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := datasetSource()
			source.Filters = []rule.FilterCondition{test.filter}
			query, _, err := Compile(source, validManifest(), testParams())
			if err != nil {
				t.Fatal(err)
			}
			predicates := query.Predicates()
			if len(predicates) != 2 {
				t.Fatalf("expected the filter plus the injected tenant predicate, got %d", len(predicates))
			}
			if len(predicates[0].Values) != test.values {
				t.Fatalf("values = %v, want %d", predicates[0].Values, test.values)
			}
		})
	}
}

func TestCompileRejectsAMissingNodeToken(t *testing.T) {
	source := datasetSource()
	source.Filters = []rule.FilterCondition{{Attribute: "shiftCode", Op: rule.FilterEquals, Value: TokenNodeID}}
	params := testParams()
	params.NodeID = ""
	_, _, err := Compile(source, validManifest(), params)
	if err == nil || !strings.Contains(err.Error(), "node identifier") {
		t.Fatalf("error = %v, want a complaint about the missing node identifier", err)
	}
}

func TestFaultFormatting(t *testing.T) {
	var nilFault *Fault
	if nilFault.Error() != "<nil>" || nilFault.Unwrap() != nil {
		t.Fatal("a nil fault should be printable")
	}
	cause := errors.New("because")
	fault := &Fault{Code: ReasonColumnNotAllowed, Dataset: "attendance.days", Column: "workedHours", Err: cause}
	message := fault.Error()
	for _, want := range []string{ReasonColumnNotAllowed, "attendance.days", "workedHours", "because"} {
		if !strings.Contains(message, want) {
			t.Fatalf("Error() = %q, missing %q", message, want)
		}
	}
	if !errors.Is(fault, cause) {
		t.Fatal("Unwrap did not expose the cause")
	}
	bare := &Fault{Code: ReasonUnresolvable}
	if bare.Error() != ReasonUnresolvable {
		t.Fatalf("Error() = %q", bare.Error())
	}
}
