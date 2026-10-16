package ruletest_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
)

// The in-memory executor is the reference every service team tests its own
// rules against before writing a real one. If its matching or its aggregates
// were wrong, fourteen teams would be wrong in the same way and would not find
// out until production, so the whole matrix is pinned here.

var probeAt = time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC)

// probeManifest permits every operator and aggregate, so one manifest can
// exercise the lot.
func probeManifest() dataset.Manifest {
	return dataset.Manifest{
		Dataset: "probe.rows", OwnerService: "probe", Domain: "probe",
		Grain: "one row per probe", TenantScoped: true,
		Status: dataset.Published, Version: "1.0.0", PublishedAt: probeAt,
		MaxWindowDays: 365,
		Aggregates: []dataset.AggregateFn{
			dataset.AggregateCount, dataset.AggregateSum, dataset.AggregateMax,
			dataset.AggregateMin, dataset.AggregateAvg, dataset.AggregateStreak,
			dataset.AggregateLatest, dataset.AggregateExists,
		},
		Columns: []dataset.Column{
			{Name: "employeeId", Type: registry.DataString, Filterable: true, PII: true},
			{Name: "day", Type: registry.DataDate, Filterable: true, Windowable: true},
			{Name: "code", Type: registry.DataEnum, Filterable: true, Values: []string{"A", "B", "C"}},
			{Name: "flag", Type: registry.DataBoolean, Filterable: true, Aggregatable: true},
			{Name: "minutes", Type: registry.DataInteger, Filterable: true, Aggregatable: true},
			{Name: "amount", Type: registry.DataDecimal, Aggregatable: true},
			{Name: "note", Type: registry.DataString, Filterable: true},
		},
	}
}

func probeExecutor(t *testing.T, rows ...ruletest.Row) *ruletest.InMemoryExecutor {
	t.Helper()
	executor, err := ruletest.NewInMemoryExecutor(probeManifest())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if err := executor.Add("probe.rows", row); err != nil {
			t.Fatal(err)
		}
	}
	return executor
}

// probeRow is one row, n days before probeAt.
func probeRow(daysAgo int, code string, flag bool, minutes int, amount string) ruletest.Row {
	return ruletest.Row{
		dataset.TenantColumn: "T1",
		"employeeId":         "E1",
		"day":                probeAt.AddDate(0, 0, -daysAgo).Format(time.DateOnly),
		"code":               code,
		"flag":               flag,
		"minutes":            minutes,
		"amount":             amount,
		"note":               "n" + code,
	}
}

func probeQuery(t *testing.T, aggregate dataset.AggregateFn, on string, filters ...rule.FilterCondition) dataset.Query {
	t.Helper()
	source := registry.FactSource{
		Kind: registry.FactDataset, Dataset: "probe.rows",
		Filters:   filters,
		Window:    &registry.FactWindow{Column: "day", RelativeTo: dataset.TokenValidAt, LastDays: 30},
		Aggregate: &registry.FactAggregate{Fn: aggregate, On: on},
	}
	query, _, err := dataset.Compile(source, probeManifest(), dataset.Params{
		Tenant: "T1", EmployeeID: "E1", NodeID: "N1",
		AsOf: probeAt, ValidAt: probeAt, RowLimit: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	return query
}

func TestInMemoryExecutorAppliesEveryOperator(t *testing.T) {
	executor := probeExecutor(t,
		probeRow(1, "A", true, 10, "1.5"),
		probeRow(2, "B", false, 20, "2.5"),
		probeRow(3, "C", true, 30, "3.5"),
	)
	tests := []struct {
		name   string
		filter rule.FilterCondition
		want   int
	}{
		{"equals on an enum", rule.FilterCondition{Attribute: "code", Op: rule.FilterEquals, Value: "A"}, 1},
		{"equals on a boolean", rule.FilterCondition{Attribute: "flag", Op: rule.FilterEquals, Value: true}, 2},
		{"equals on an integer", rule.FilterCondition{Attribute: "minutes", Op: rule.FilterEquals, Value: 20}, 1},
		{"in", rule.FilterCondition{Attribute: "code", Op: rule.FilterIn, Value: []any{"A", "C"}}, 2},
		{"notIn", rule.FilterCondition{Attribute: "code", Op: rule.FilterNotIn, Value: []any{"A"}}, 2},
		{"range", rule.FilterCondition{Attribute: "minutes", Op: rule.FilterRange, Value: []any{15, 30}}, 2},
		{"range is inclusive", rule.FilterCondition{Attribute: "minutes", Op: rule.FilterRange, Value: []any{10, 10}}, 1},
		{"exists", rule.FilterCondition{Attribute: "note", Op: rule.FilterExists}, 3},
		{"notExists", rule.FilterCondition{Attribute: "note", Op: rule.FilterNotExists}, 0},
		{"equals matching nothing", rule.FilterCondition{Attribute: "code", Op: rule.FilterEquals, Value: "B"}, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := executor.Run(context.Background(),
				probeQuery(t, dataset.AggregateCount, "", test.filter))
			if err != nil {
				t.Fatal(err)
			}
			if result.Value != test.want {
				t.Fatalf("count = %v, want %d", result.Value, test.want)
			}
		})
	}
}

func TestInMemoryExecutorComputesEveryAggregate(t *testing.T) {
	executor := probeExecutor(t,
		probeRow(1, "A", true, 10, "1.5"),
		probeRow(2, "B", true, 20, "2.5"),
		probeRow(3, "C", false, 30, "3.5"),
	)
	tests := []struct {
		name      string
		aggregate dataset.AggregateFn
		on        string
		want      string
	}{
		{"count", dataset.AggregateCount, "", "3"},
		{"exists", dataset.AggregateExists, "", "true"},
		{"sum of decimals", dataset.AggregateSum, "amount", "7.5"},
		{"sum of integers stays integral", dataset.AggregateSum, "minutes", "60"},
		{"max", dataset.AggregateMax, "minutes", "30"},
		{"min", dataset.AggregateMin, "minutes", "10"},
		{"avg", dataset.AggregateAvg, "minutes", "20"},
		// Rows arrive most recent first, so the latest is the one day ago.
		{"latest", dataset.AggregateLatest, "amount", "1.5"},
		// Two consecutive most-recent rows are flagged, the third is not.
		{"streak", dataset.AggregateStreak, "flag", "2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := executor.Run(context.Background(), probeQuery(t, test.aggregate, test.on))
			if err != nil {
				t.Fatal(err)
			}
			if got := renderValue(result.Value); got != test.want {
				t.Fatalf("%s = %s, want %s", test.aggregate, got, test.want)
			}
		})
	}
}

// TestInMemoryExecutorAnswersNothingForNoRows pins that an empty match returns
// a nil value rather than a zero: the caller cannot tell "no rows" from "the
// answer is zero" otherwise, and the field's declared onEmpty exists precisely
// to answer that question.
func TestInMemoryExecutorAnswersNothingForNoRows(t *testing.T) {
	executor := probeExecutor(t)
	for _, aggregate := range []struct {
		fn   dataset.AggregateFn
		on   string
		want any
	}{
		{dataset.AggregateCount, "", 0},
		{dataset.AggregateExists, "", false},
		{dataset.AggregateStreak, "flag", 0},
		{dataset.AggregateLatest, "amount", nil},
		{dataset.AggregateSum, "amount", nil},
		{dataset.AggregateAvg, "minutes", nil},
	} {
		result, err := executor.Run(context.Background(), probeQuery(t, aggregate.fn, aggregate.on))
		if err != nil {
			t.Fatal(err)
		}
		if renderValue(result.Value) != renderValue(aggregate.want) {
			t.Errorf("%s over no rows = %v, want %v", aggregate.fn, result.Value, aggregate.want)
		}
	}
}

// TestInMemoryExecutorWindowsByDate pins the window boundary: From inclusive,
// To exclusive, so a day belongs to exactly one window.
func TestInMemoryExecutorWindowsByDate(t *testing.T) {
	executor := probeExecutor(t,
		probeRow(1, "A", true, 10, "1"),  // inside
		probeRow(29, "B", true, 10, "1"), // inside
		probeRow(31, "C", true, 10, "1"), // outside a 30-day window
	)
	result, err := executor.Run(context.Background(), probeQuery(t, dataset.AggregateCount, ""))
	if err != nil {
		t.Fatal(err)
	}
	if result.Value != 2 {
		t.Fatalf("count = %v, want the two rows inside the window", result.Value)
	}
	// Every row is still scanned, which is what the row limit protects.
	if result.RowsScanned != 3 {
		t.Fatalf("rows scanned = %d, want all 3", result.RowsScanned)
	}
}

// TestInMemoryExecutorIsTenantScoped pins the property the conformance suite
// exists to enforce: another tenant's rows are never counted.
func TestInMemoryExecutorIsTenantScoped(t *testing.T) {
	executor := probeExecutor(t, probeRow(1, "A", true, 10, "1"))
	other := probeRow(1, "A", true, 10, "1")
	other[dataset.TenantColumn] = "T2"
	if err := executor.Add("probe.rows", other); err != nil {
		t.Fatal(err)
	}
	result, err := executor.Run(context.Background(), probeQuery(t, dataset.AggregateCount, ""))
	if err != nil {
		t.Fatal(err)
	}
	if result.Value != 1 {
		t.Fatalf("count = %v, want only this tenant's row", result.Value)
	}
}

// TestInMemoryExecutorRefusesUnusableInput pins that it reports a mistake
// rather than answering with something misleading.
func TestInMemoryExecutorRefusesUnusableInput(t *testing.T) {
	executor := probeExecutor(t)

	// A row with no tenant column could never be filtered, so it is refused
	// on the way in rather than silently matching every tenant.
	if err := executor.Add("probe.rows", ruletest.Row{"employeeId": "E1"}); err == nil {
		t.Fatal("a row with no tenant column was accepted")
	}
	// A dataset the executor does not serve.
	if err := executor.Add("no.such.dataset", ruletest.Row{dataset.TenantColumn: "T1"}); err == nil {
		t.Fatal("a row for an unserved dataset was accepted")
	}
	// An invalid manifest.
	broken := probeManifest()
	broken.OwnerService, broken.SourceService = "", ""
	if _, err := ruletest.NewInMemoryExecutor(broken); err == nil {
		t.Fatal("an invalid manifest was accepted")
	}
	// A row whose window column is not a date cannot be placed in the window.
	bad := probeRow(1, "A", true, 10, "1")
	bad["day"] = "not-a-date"
	if err := executor.Add("probe.rows", bad); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Run(context.Background(), probeQuery(t, dataset.AggregateCount, "")); err == nil {
		t.Fatal("an unusable window value was tolerated")
	}
}

// TestInMemoryExecutorHonoursCancellationAndDelay pins the two behaviours the
// fact budget relies on.
func TestInMemoryExecutorHonoursCancellationAndDelay(t *testing.T) {
	executor := probeExecutor(t, probeRow(1, "A", true, 10, "1"))
	query := probeQuery(t, dataset.AggregateCount, "")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executor.Run(cancelled, query); err == nil {
		t.Fatal("a cancelled query was served")
	}

	executor.SetDelay(50 * time.Millisecond)
	timed, stop := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer stop()
	if _, err := executor.Run(timed, query); err == nil {
		t.Fatal("the delay did not outlast the deadline")
	}
	executor.SetDelay(0)
	if _, err := executor.Run(context.Background(), query); err != nil {
		t.Fatal(err)
	}
}

// TestInMemoryExecutorDeclaresItsManifests pins the routing contract: the
// library can only send a fact to an executor that says it serves the dataset.
func TestInMemoryExecutorDeclaresItsManifests(t *testing.T) {
	executor := probeExecutor(t)
	manifests := executor.Manifests()
	if len(manifests) != 1 || manifests[0].Dataset != "probe.rows" {
		t.Fatalf("Manifests = %+v", manifests)
	}
	// A query for a dataset it does not serve is refused.
	other := probeManifest()
	other.Dataset = "elsewhere.rows"
	source := registry.FactSource{
		Kind: registry.FactDataset, Dataset: "elsewhere.rows",
		Window:    &registry.FactWindow{Column: "day", RelativeTo: dataset.TokenValidAt, LastDays: 7},
		Aggregate: &registry.FactAggregate{Fn: dataset.AggregateCount},
	}
	query, _, err := dataset.Compile(source, other, dataset.Params{
		Tenant: "T1", EmployeeID: "E1", AsOf: probeAt, ValidAt: probeAt, RowLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Run(context.Background(), query); err == nil {
		t.Fatal("a query for an unserved dataset was answered")
	}
}

// renderValue prints a result value for comparison, so an exact decimal and
// the number a test wrote compare as the same thing.
func renderValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "nil"
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case interface{ String() string }:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}
