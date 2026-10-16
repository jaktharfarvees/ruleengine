package dataset

import (
	"context"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/registry"
)

// ConformanceSuite checks one service's Executor against the contract in the
// Executor documentation. A service team runs it from its own test package:
//
//	func TestExecutorConformance(t *testing.T) {
//	    dataset.ConformanceSuite(t, func() dataset.Executor { return newExecutor() }, manifest)
//	}
//
// newExecutor is called per sub-test so one case cannot leave state behind for
// the next. The suite is deliberately small: it checks the properties the
// library cannot enforce from the outside, above all that the executor applies
// the injected tenant predicate instead of assuming it is there.
//
// Every check reports with Error and returns rather than relying on Fatal to
// unwind. A real *testing.T aborts the goroutine, but library code must be
// correct for any TB and must never be the reason a test binary panics.
func ConformanceSuite(t testing.TB, newExecutor func() Executor, manifest Manifest) {
	if newExecutor == nil {
		t.Error("ConformanceSuite needs a constructor")
		return
	}
	if err := manifest.Validate(); err != nil {
		t.Errorf("the manifest under test is itself invalid: %v", err)
		return
	}

	runner, ok := t.(interface {
		Run(string, func(*testing.T)) bool
	})
	if !ok {
		// A benchmark or a custom TB cannot host sub-tests; run the checks
		// inline rather than refusing to check anything.
		conformanceChecks(t, newExecutor, manifest)
		return
	}
	for _, check := range conformanceCases() {
		runner.Run(check.name, func(t *testing.T) { check.run(t, newExecutor(), manifest) })
	}
}

func conformanceChecks(t testing.TB, newExecutor func() Executor, manifest Manifest) {
	for _, check := range conformanceCases() {
		check.run(t, newExecutor(), manifest)
	}
}

type conformanceCase struct {
	name string
	run  func(t testing.TB, executor Executor, manifest Manifest)
}

func conformanceCases() []conformanceCase {
	return []conformanceCase{
		{"declares the manifest under test", checkManifestDeclared},
		{"refuses a query with no tenant predicate", checkRefusesUntenantedQuery},
		{"honours the row limit it reports", checkRowLimitRespected},
		{"honours context cancellation", checkCancellation},
		{"is deterministic for the same query", checkDeterminism},
	}
}

func checkManifestDeclared(t testing.TB, executor Executor, manifest Manifest) {
	for _, declared := range executor.Manifests() {
		if declared.Dataset != manifest.Dataset {
			continue
		}
		if err := declared.Validate(); err != nil {
			t.Errorf("executor declares an invalid manifest for %q: %v", manifest.Dataset, err)
			return
		}
		return
	}
	t.Errorf("executor does not declare the manifest for %q; the library cannot route a fact to it", manifest.Dataset)
}

// checkRefusesUntenantedQuery is the reason this suite exists.
//
// Compile always injects a tenant predicate, so a correct query cannot test
// this. The suite builds one without the predicate — which only this package
// can do, because Query's fields are unexported — and requires the executor to
// refuse it. An executor that answers anyway would, the day a library bug or a
// hand-built query reached it, return another tenant's data.
func checkRefusesUntenantedQuery(t testing.TB, executor Executor, manifest Manifest) {
	query, ok := conformanceQuery(t, manifest)
	if !ok {
		return
	}
	untenanted := query
	untenanted.predicates = nil
	for _, predicate := range query.predicates {
		if !predicate.Injected {
			untenanted.predicates = append(untenanted.predicates, predicate)
		}
	}
	if _, present := untenanted.TenantPredicate(); present {
		t.Error("the suite failed to strip the tenant predicate")
		return
	}
	if _, err := executor.Run(context.Background(), untenanted); err == nil {
		t.Error("executor ran a query carrying no tenant predicate; it must assert the predicate is present and refuse")
	}
}

func checkRowLimitRespected(t testing.TB, executor Executor, manifest Manifest) {
	query, ok := conformanceQuery(t, manifest)
	if !ok {
		return
	}
	result, err := executor.Run(context.Background(), query)
	if err != nil {
		t.Errorf("executor refused a valid query: %v", err)
		return
	}
	if result.RowsScanned < 0 {
		t.Errorf("executor reported %d rows scanned", result.RowsScanned)
		return
	}
	if result.RowsScanned > query.RowLimit() {
		t.Errorf("executor scanned %d rows for a limit of %d; the limit is a cap, not a hint",
			result.RowsScanned, query.RowLimit())
	}
}

func checkCancellation(t testing.TB, executor Executor, manifest Manifest) {
	query, ok := conformanceQuery(t, manifest)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executor.Run(ctx, query); err == nil {
		t.Error("executor ignored a cancelled context; the fact budget relies on cancellation being honoured")
	}
}

func checkDeterminism(t testing.TB, executor Executor, manifest Manifest) {
	query, ok := conformanceQuery(t, manifest)
	if !ok {
		return
	}
	first, err := executor.Run(context.Background(), query)
	if err != nil {
		t.Errorf("executor refused a valid query: %v", err)
		return
	}
	second, err := executor.Run(context.Background(), query)
	if err != nil {
		t.Errorf("executor refused the same query twice: %v", err)
		return
	}
	if scalarKey(first.Value) != scalarKey(second.Value) {
		t.Errorf("executor is not deterministic: %v then %v; a payroll replay must reproduce the same fact",
			first.Value, second.Value)
	}
}

// conformanceQuery compiles the simplest valid query the manifest allows: a
// row count over the whole dataset, windowed if any column permits it. The
// second result is false when the manifest cannot express one, in which case
// the failure has already been reported.
func conformanceQuery(t testing.TB, manifest Manifest) (Query, bool) {
	source := registry.FactSource{
		Kind:      registry.FactDataset,
		Dataset:   manifest.Dataset,
		Aggregate: &registry.FactAggregate{Fn: pickAggregate(manifest)},
	}
	if source.Aggregate.Fn.NeedsColumn() {
		column, found := firstColumn(manifest, func(c Column) bool { return c.Aggregatable })
		if !found {
			t.Errorf("manifest %q offers only column aggregates but declares no aggregatable column", manifest.Dataset)
			return Query{}, false
		}
		source.Aggregate.On = column.Name
	}
	if column, found := firstColumn(manifest, func(c Column) bool { return c.Windowable }); found {
		source.Window = &registry.FactWindow{Column: column.Name, RelativeTo: TokenValidAt, LastDays: 1}
	}
	validAt := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	query, _, err := Compile(source, manifest, Params{
		Tenant: "CONFORMANCE", EmployeeID: "E0001", NodeID: "N0001",
		AsOf: validAt, ValidAt: validAt, RowLimit: 100,
	})
	if err != nil {
		t.Errorf("the suite could not compile a query for %q: %v", manifest.Dataset, err)
		return Query{}, false
	}
	return query, true
}

// pickAggregate prefers a row aggregate, which every store can answer, and
// falls back to whatever the dataset does offer.
func pickAggregate(manifest Manifest) AggregateFn {
	for _, preferred := range []AggregateFn{AggregateCount, AggregateExists, AggregateMax, AggregateSum} {
		if manifest.Offers(preferred) {
			return preferred
		}
	}
	return manifest.Aggregates[0]
}

func firstColumn(manifest Manifest, want func(Column) bool) (Column, bool) {
	for _, column := range manifest.Columns {
		if want(column) {
			return column, true
		}
	}
	return Column{}, false
}
