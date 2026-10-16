package dataset

import (
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/registry"
)

func validManifest() Manifest {
	return Manifest{
		Dataset: "attendance.days", OwnerService: "attendance-service", Domain: "attendance",
		Grain: "one row per employee per work day", TenantScoped: true,
		Status: Published, Version: "1.0.0",
		PublishedAt:   time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		MaxWindowDays: 365, RowLimit: 2000,
		Aggregates: []AggregateFn{AggregateCount, AggregateSum, AggregateAvg, AggregateStreak, AggregateLatest, AggregateExists},
		Columns: []Column{
			{Name: "employeeId", Type: registry.DataString, Filterable: true, PII: true},
			{Name: "workDate", Type: registry.DataDate, Filterable: true, Windowable: true},
			{Name: "isLate", Type: registry.DataBoolean, Filterable: true, Aggregatable: true},
			{Name: "lateMinutes", Type: registry.DataInteger, Filterable: true, Aggregatable: true},
			{Name: "shiftCode", Type: registry.DataString, Filterable: true},
			{Name: "workedHours", Type: registry.DataDecimal, Aggregatable: true},
			{Name: "status", Type: registry.DataEnum, Filterable: true, Values: []string{"OK", "LATE"}},
		},
	}
}

func TestManifestValidate(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("the valid manifest was refused: %v", err)
	}
	tests := []struct {
		name    string
		breakIt func(*Manifest)
		want    string
	}{
		{"no dataset", func(m *Manifest) { m.Dataset = "" }, "dataset identifier"},
		{"no owner", func(m *Manifest) { m.OwnerService = "" }, "owner service"},
		{"no domain", func(m *Manifest) { m.Domain = "" }, "owner service"},
		{"no grain", func(m *Manifest) { m.Grain = "" }, "grain"},
		{"not tenant scoped", func(m *Manifest) { m.TenantScoped = false }, "tenant scoped"},
		{"no version", func(m *Manifest) { m.Version = "" }, "version"},
		{"no status", func(m *Manifest) { m.Status = "" }, "status"},
		{"no window maximum", func(m *Manifest) { m.MaxWindowDays = 0 }, "maxWindowDays"},
		{"negative row limit", func(m *Manifest) { m.RowLimit = -1 }, "rowLimit"},
		{"no aggregates", func(m *Manifest) { m.Aggregates = nil }, "no aggregates"},
		{"unknown aggregate", func(m *Manifest) { m.Aggregates = []AggregateFn{"median"} }, "unknown aggregate"},
		{"duplicate aggregate", func(m *Manifest) {
			m.Aggregates = []AggregateFn{AggregateCount, AggregateCount}
		}, "twice"},
		{"no columns", func(m *Manifest) { m.Columns = nil }, "no columns"},
		{"unnamed column", func(m *Manifest) { m.Columns[0].Name = "" }, "no name"},
		{"duplicate column", func(m *Manifest) { m.Columns[1].Name = "employeeId" }, "twice"},
		{"declares the tenant column", func(m *Manifest) { m.Columns[0].Name = TenantColumn }, "implicit"},
		{"unknown column type", func(m *Manifest) { m.Columns[0].Type = "blob" }, "unknown type"},
		{"enum with no values", func(m *Manifest) { m.Columns[6].Values = nil }, "no values"},
		{"values on a non-enum", func(m *Manifest) { m.Columns[0].Values = []string{"x"} }, "not an enum"},
		{"windowable non-date", func(m *Manifest) { m.Columns[4].Windowable = true }, "not a date"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.breakIt(&manifest)
			err := manifest.Validate()
			if err == nil {
				t.Fatal("an invalid manifest validated")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not mention %q", err, test.want)
			}
		})
	}
}

func TestManifestLookups(t *testing.T) {
	manifest := validManifest()
	if column, ok := manifest.Column("isLate"); !ok || !column.Aggregatable {
		t.Fatalf("Column(\"isLate\") = %+v, %t", column, ok)
	}
	if _, ok := manifest.Column("missing"); ok {
		t.Fatal("an undeclared column was found")
	}
	if !manifest.Offers(AggregateStreak) || manifest.Offers(AggregateMin) {
		t.Fatal("Offers disagrees with the declared aggregate list")
	}
}

func TestManifestQueryable(t *testing.T) {
	tests := []struct {
		status             string
		queryable, retired bool
	}{
		{Published, true, false},
		{Deprecated, true, true},
		{"draft", false, false},
		{"retired", false, false},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			manifest := validManifest()
			manifest.Status = test.status
			queryable, deprecated := manifest.Queryable()
			if queryable != test.queryable || deprecated != test.retired {
				t.Fatalf("Queryable() = %t, %t; want %t, %t", queryable, deprecated, test.queryable, test.retired)
			}
		})
	}
}

func TestAggregateFnProperties(t *testing.T) {
	all := []AggregateFn{
		AggregateCount, AggregateSum, AggregateMax, AggregateMin,
		AggregateAvg, AggregateStreak, AggregateLatest, AggregateExists,
	}
	for _, aggregate := range all {
		if !aggregate.Valid() || aggregate.String() == "" {
			t.Errorf("%q should be a valid aggregate", aggregate)
		}
	}
	if AggregateFn("median").Valid() || AggregateFn("").Valid() {
		t.Fatal("an unknown aggregate was accepted")
	}
	// count and exists are about rows, not values, so they name no column.
	for _, aggregate := range []AggregateFn{AggregateCount, AggregateExists} {
		if aggregate.NeedsColumn() {
			t.Errorf("%q should not need a column", aggregate)
		}
	}
	for _, aggregate := range []AggregateFn{AggregateSum, AggregateAvg, AggregateStreak, AggregateLatest, AggregateMax, AggregateMin} {
		if !aggregate.NeedsColumn() {
			t.Errorf("%q should need a column", aggregate)
		}
	}
	// Only order-dependent aggregates require a window.
	if !orderDependent(AggregateStreak) || !orderDependent(AggregateLatest) || orderDependent(AggregateSum) {
		t.Fatal("orderDependent disagrees with the documented set")
	}
}

func TestFactKindProperties(t *testing.T) {
	for _, kind := range []registry.FactKind{
		registry.FactPassed, registry.FactDerived, registry.FactDataset, registry.FactNative,
	} {
		if !kind.Valid() || kind.String() == "" {
			t.Errorf("%q should be a valid kind", kind)
		}
	}
	if registry.FactKind("remote").Valid() {
		t.Fatal("an unknown kind was accepted")
	}
}
