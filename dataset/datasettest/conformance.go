package datasettest

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/dataset/internal/untenanted"
	"github.com/arrowhr/ruleengine/internal/decimalx"
	"github.com/arrowhr/ruleengine/registry"
)

// tenantA and tenantB are the two tenants the isolation check uses. They are
// deliberately unlike each other so a prefix match or a truncated column
// cannot make one look like the other.
const (
	tenantA = "CONFORMANCE-A"
	tenantB = "OTHER-TENANT-B"
)

// Seeder inserts rows for one tenant into an Executor the suite just built.
//
// The suite calls it with the Executor returned by the constructor, so an
// implementation normally type-asserts back to its own concrete type. Insert
// at least one row that the suite's query will match: the rows are what make
// the tenant-isolation and aggregate-type checks meaningful.
//
// Return an error rather than failing the test; the suite reports it.
type Seeder func(executor dataset.Executor, tenant string) error

// Option configures ConformanceSuite.
type Option func(*options)

type options struct {
	seed           Seeder
	skipIsolation  bool
	isolationGiven bool
}

// WithTenantSeeder supplies the function the suite uses to insert rows for a
// tenant. Without it the tenant-isolation check fails, because a suite with no
// data cannot tell an isolating Executor from one that ignores the predicate.
func WithTenantSeeder(seed Seeder) Option {
	return func(o *options) {
		o.seed = seed
		o.isolationGiven = true
	}
}

// WithoutTenantIsolation opts out of the tenant-isolation check.
//
// It exists for an Executor that genuinely cannot be seeded from a test, such
// as one reading a fixed read-only replica. The suite logs a warning when it
// is used, because the check it skips is the one that protects a tenant
// boundary, and the property then has to be proved some other way.
func WithoutTenantIsolation() Option {
	return func(o *options) {
		o.skipIsolation = true
		o.isolationGiven = true
	}
}

// ConformanceSuite checks one service's Executor against the contract in the
// dataset.Executor documentation.
//
// newExecutor is called once per sub-test so that one case cannot leave state
// behind for the next, and so the isolation check can compare a one-tenant
// store against a two-tenant one.
//
// Every check reports with Error and returns rather than relying on Fatal to
// unwind. A real *testing.T aborts the goroutine, but library code must be
// correct for any TB and must never be the reason a test binary panics.
func ConformanceSuite(
	t testing.TB, newExecutor func() dataset.Executor, manifest dataset.Manifest, opts ...Option,
) {
	t.Helper()
	if newExecutor == nil {
		t.Error("ConformanceSuite needs a constructor")
		return
	}
	if err := manifest.Validate(); err != nil {
		t.Errorf("the manifest under test is itself invalid: %v", err)
		return
	}

	resolved := &options{}
	for _, option := range opts {
		if option != nil {
			option(resolved)
		}
	}
	if resolved.skipIsolation {
		t.Logf("WARNING: tenant isolation is not being checked for %q. "+
			"That check is the reason this suite exists; prove the boundary another way.",
			manifest.Canonical().DatasetID)
	}

	runner, ok := t.(interface {
		Run(string, func(*testing.T)) bool
	})
	if !ok {
		// A benchmark or a custom TB cannot host sub-tests; run the checks
		// inline rather than refusing to check anything.
		conformanceChecks(t, newExecutor, manifest, resolved)
		return
	}
	for _, check := range conformanceCases() {
		runner.Run(check.name, func(t *testing.T) {
			check.run(t, newExecutor, manifest, resolved)
		})
	}
}

func conformanceChecks(
	t testing.TB, newExecutor func() dataset.Executor, manifest dataset.Manifest, opts *options,
) {
	for _, check := range conformanceCases() {
		check.run(t, newExecutor, manifest, opts)
	}
}

type conformanceCase struct {
	name string
	run  func(t testing.TB, newExecutor func() dataset.Executor, manifest dataset.Manifest, opts *options)
}

func conformanceCases() []conformanceCase {
	return []conformanceCase{
		{"declares the manifest under test", checkManifestDeclared},
		{"refuses a query with no tenant predicate", checkRefusesUntenantedQuery},
		{"isolates one tenant from another", checkTenantIsolation},
		{"honours the row limit it reports", checkRowLimitRespected},
		{"honours context cancellation", checkCancellation},
		{"is deterministic for the same query", checkDeterminism},
		{"returns the documented type per aggregate", checkAggregateTypes},
	}
}

func checkManifestDeclared(
	t testing.TB, newExecutor func() dataset.Executor, manifest dataset.Manifest, _ *options,
) {
	wanted := manifest.Canonical().DatasetID
	for _, declared := range newExecutor().Manifests() {
		if declared.Canonical().DatasetID != wanted {
			continue
		}
		if err := declared.Validate(); err != nil {
			t.Errorf("executor declares an invalid manifest for %q: %v", wanted, err)
			return
		}
		return
	}
	t.Errorf("executor does not declare the manifest for %q; the library cannot route a fact to it", wanted)
}

// untenantedCopy strips the injected tenant predicate from a compiled query.
func untenantedCopy(query dataset.Query) dataset.Query {
	stripped, _ := untenanted.Strip(query).(dataset.Query)
	return stripped
}

// checkRefusesUntenantedQuery is the reason this suite exists.
//
// Compile always injects a tenant predicate, so a correct query cannot test
// this. untenantedCopy builds the one query Compile will never
// produce, and the Executor must refuse it. An Executor that answers anyway
// would, the day a library bug or a hand-built query reached it, return
// another tenant's data.
func checkRefusesUntenantedQuery(
	t testing.TB, newExecutor func() dataset.Executor, manifest dataset.Manifest, _ *options,
) {
	query, ok := conformanceQuery(t, manifest)
	if !ok {
		return
	}
	stripped := untenantedCopy(query)
	if _, present := stripped.TenantPredicate(); present {
		t.Error("the suite failed to strip the tenant predicate")
		return
	}
	if _, err := newExecutor().Run(context.Background(), stripped); err == nil {
		t.Error("executor ran a query carrying no tenant predicate; " +
			"it must assert the predicate is present and refuse")
	}
}

// checkTenantIsolation is the stronger half of the tenant boundary: refusing
// an untenanted query proves the Executor looks for the predicate, while this
// proves it applies it.
//
// It runs the same query twice as tenant A: once against a store holding only
// A's rows, then against a fresh store holding A's and B's.
//
// A difference in Value is a failure: it means the predicate is not applied
// and one tenant is reading another's data.
//
// A difference in RowsScanned is reported as a warning rather than a failure.
// It means the Executor reads rows it then discards, so another tenant's
// volume is charged to this tenant's MaxRowsScanned budget — a real
// multi-tenancy hazard, because one large tenant can then exhaust the budget
// of every other. But it is a property of the storage, not a breach of the
// contract: a store with no index on the tenant column has no choice but to
// scan and filter, and reporting that honestly is exactly what the contract
// asks for. An indexed store should show no growth, and a team that sees this
// warning should check that its tenant column is indexed.
func checkTenantIsolation(
	t testing.TB, newExecutor func() dataset.Executor, manifest dataset.Manifest, opts *options,
) {
	if opts.skipIsolation {
		t.Log("skipped by WithoutTenantIsolation")
		return
	}
	if opts.seed == nil {
		t.Error("no tenant seeder was supplied, so tenant isolation cannot be checked. " +
			"Pass datasettest.WithTenantSeeder to insert rows for a tenant, or " +
			"datasettest.WithoutTenantIsolation to opt out deliberately")
		return
	}
	query, ok := conformanceQuery(t, manifest)
	if !ok {
		return
	}

	alone := newExecutor()
	if err := opts.seed(alone, tenantA); err != nil {
		t.Errorf("seeding tenant %q failed: %v", tenantA, err)
		return
	}
	expected, err := alone.Run(context.Background(), query)
	if err != nil {
		t.Errorf("executor refused a valid query for a single tenant: %v", err)
		return
	}

	shared := newExecutor()
	if err := opts.seed(shared, tenantA); err != nil {
		t.Errorf("seeding tenant %q failed: %v", tenantA, err)
		return
	}
	if err := opts.seed(shared, tenantB); err != nil {
		t.Errorf("seeding tenant %q failed: %v", tenantB, err)
		return
	}
	got, err := shared.Run(context.Background(), query)
	if err != nil {
		t.Errorf("executor refused the same query once a second tenant had rows: %v", err)
		return
	}

	if scalarKey(got.Value) != scalarKey(expected.Value) {
		t.Errorf("tenant %q saw %v alone but %v once tenant %q had rows; "+
			"the injected tenant predicate is not being applied",
			tenantA, expected.Value, got.Value, tenantB)
	}
	if got.RowsScanned != expected.RowsScanned {
		t.Logf("WARNING: rows scanned for tenant %q went from %d to %d once tenant %q had rows. "+
			"The answer is correct, but another tenant's volume is being charged to this "+
			"tenant's MaxRowsScanned budget, so one large tenant can exhaust it for the rest. "+
			"Check that the tenant column is indexed and the predicate is pushed down.",
			tenantA, expected.RowsScanned, got.RowsScanned, tenantB)
	}
}

func checkRowLimitRespected(
	t testing.TB, newExecutor func() dataset.Executor, manifest dataset.Manifest, opts *options,
) {
	query, ok := conformanceQuery(t, manifest)
	if !ok {
		return
	}
	executor := seeded(t, newExecutor, opts)
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

func checkCancellation(
	t testing.TB, newExecutor func() dataset.Executor, manifest dataset.Manifest, _ *options,
) {
	query, ok := conformanceQuery(t, manifest)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newExecutor().Run(ctx, query); err == nil {
		t.Error("executor ignored a cancelled context; the fact budget relies on cancellation being honoured")
	}
}

func checkDeterminism(
	t testing.TB, newExecutor func() dataset.Executor, manifest dataset.Manifest, opts *options,
) {
	query, ok := conformanceQuery(t, manifest)
	if !ok {
		return
	}
	executor := seeded(t, newExecutor, opts)
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

// checkAggregateTypes runs one query per aggregate the manifest offers and
// checks the Go type of Result.Value against the documented contract.
//
// A nil Value is always acceptable: it is how an Executor says nothing
// matched. So without a seeder this check can only confirm that nothing of the
// wrong type comes back; with one, it confirms the types themselves.
func checkAggregateTypes(
	t testing.TB, newExecutor func() dataset.Executor, manifest dataset.Manifest, opts *options,
) {
	canonical := manifest.Canonical()
	for _, function := range canonical.AllowedAggregates {
		query, ok := aggregateQuery(t, manifest, function)
		if !ok {
			continue
		}
		executor := seeded(t, newExecutor, opts)
		result, err := executor.Run(context.Background(), query)
		if err != nil {
			t.Errorf("executor refused a valid %q query: %v", function, err)
			continue
		}
		if result.Value == nil {
			// Contractual: no row matched. With a seeder that matches the
			// query this does not happen, and the type is then checked.
			continue
		}
		checkAggregateValue(t, function, query.Aggregate().On, canonical, result.Value)
	}
}

func checkAggregateValue(
	t testing.TB, function dataset.AggregateFn, on string,
	manifest dataset.Manifest, value any,
) {
	// No aggregate may ever be binary floating point. Money and rates are
	// exact, and a float cannot be compared by the engine: it refuses one.
	if isFloat(value) {
		t.Errorf("%q returned %T (%v); binary floating point is never an acceptable "+
			"aggregate result, because the engine refuses to compare one",
			function, value, value)
		return
	}

	switch function {
	case dataset.AggregateCount, dataset.AggregateStreak:
		if !isGoInteger(value) {
			t.Errorf("%q returned %T (%v), want a Go integer: a row count is a whole number of rows",
				function, value, value)
		}
	case dataset.AggregateExists:
		if _, ok := value.(bool); !ok {
			t.Errorf("%q returned %T (%v), want bool", function, value, value)
		}
	case dataset.AggregateLatest:
		column, found := manifest.Column(on)
		if !found {
			t.Errorf("%q names column %q, which the manifest does not declare", function, on)
			return
		}
		if !column.DataType.Accepts(value, column.AllowedValues) {
			t.Errorf("%q returned %T (%v) for column %q, which the manifest types as %q; "+
				"a latest value is the cell itself and must satisfy its own column's type",
				function, value, value, on, column.DataType)
		}
	case dataset.AggregateSum, dataset.AggregateAvg, dataset.AggregateMin, dataset.AggregateMax:
		if _, exact := decimalx.FromValue(value); !exact {
			t.Errorf("%q returned %T (%v), which is not an exact number. Return the library's "+
				"decimal, a string, a json.Number or an integer; see dataset.Result",
				function, value, value)
		}
	default:
		t.Errorf("the suite does not know the result contract for aggregate %q; "+
			"this is a gap in datasettest, not in the executor", function)
	}
}

// seeded builds an Executor and, when a seeder was supplied, gives it rows for
// tenant A. A check that needs data gets it; one that does not is unaffected.
func seeded(t testing.TB, newExecutor func() dataset.Executor, opts *options) dataset.Executor {
	executor := newExecutor()
	if opts.seed == nil {
		return executor
	}
	if err := opts.seed(executor, tenantA); err != nil {
		t.Errorf("seeding tenant %q failed: %v", tenantA, err)
	}
	return executor
}

// conformanceQuery compiles the simplest valid query the manifest allows: a
// row count over the whole dataset for tenant A, windowed if any column
// permits it. The second result is false when the manifest cannot express one,
// in which case the failure has already been reported.
func conformanceQuery(t testing.TB, manifest dataset.Manifest) (dataset.Query, bool) {
	return aggregateQuery(t, manifest, pickAggregate(manifest))
}

// aggregateQuery compiles a query for one specific aggregate, as tenant A.
func aggregateQuery(
	t testing.TB, manifest dataset.Manifest, function dataset.AggregateFn,
) (dataset.Query, bool) {
	source := registry.FactSource{
		Kind:      registry.FactDataset,
		DatasetID: manifest.Canonical().DatasetID,
		Aggregate: &registry.FactAggregate{Fn: function},
	}
	if function.NeedsColumn() {
		// A numeric aggregate needs a numeric column: summing a boolean is a
		// question the manifest permits by column but the aggregate cannot
		// answer, and asking it would blame the executor for the suite's
		// choice of column.
		want := func(c dataset.Column) bool { return c.Aggregatable }
		if isNumericAggregate(function) {
			want = func(c dataset.Column) bool { return c.Aggregatable && isNumericType(c.DataType) }
		}
		column, found := firstColumn(manifest, want)
		if !found {
			if isNumericAggregate(function) {
				// Not the executor's fault, and not necessarily a mistake: a
				// dataset may offer sum for a column it adds later.
				t.Logf("manifest %q offers the %q aggregate but declares no numeric "+
					"aggregatable column, so its result type cannot be checked",
					manifest.Canonical().DatasetID, function)
				return dataset.Query{}, false
			}
			t.Errorf("manifest %q offers the %q aggregate but declares no aggregatable column",
				manifest.Canonical().DatasetID, function)
			return dataset.Query{}, false
		}
		source.Aggregate.Column = column.Name
	}
	if column, found := firstColumn(manifest, func(c dataset.Column) bool { return c.Windowable }); found {
		source.Window = &registry.FactWindow{
			Column: column.Name, RelativeTo: dataset.TokenValidAt, LastDays: 1,
		}
	}
	validAt := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	query, _, err := dataset.Compile(source, manifest, dataset.Params{
		Tenant: tenantA, EmployeeID: "E0001", NodeID: "N0001",
		AsOf: validAt, ValidAt: validAt, RowLimit: 100,
	})
	if err != nil {
		t.Errorf("the suite could not compile a %q query for %q: %v",
			function, manifest.Canonical().DatasetID, err)
		return dataset.Query{}, false
	}
	return query, true
}

// pickAggregate prefers a row aggregate, which every store can answer, and
// falls back to whatever the dataset does offer.
//
// It reads AllowedAggregates, the v2 contract, through Canonical so that a
// manifest built either way works and a v2-only manifest does not index an
// empty deprecated slice.
func pickAggregate(manifest dataset.Manifest) dataset.AggregateFn {
	canonical := manifest.Canonical()
	for _, preferred := range []dataset.AggregateFn{
		dataset.AggregateCount, dataset.AggregateExists,
		dataset.AggregateMax, dataset.AggregateSum,
	} {
		if canonical.Offers(preferred) {
			return preferred
		}
	}
	if len(canonical.AllowedAggregates) == 0 {
		// Validate rejects such a manifest, so the suite has already reported
		// it; returning a known function keeps this from panicking.
		return dataset.AggregateCount
	}
	return canonical.AllowedAggregates[0]
}

func firstColumn(
	manifest dataset.Manifest, want func(dataset.Column) bool,
) (dataset.Column, bool) {
	for _, column := range manifest.Canonical().Columns {
		if want(column) {
			return column, true
		}
	}
	return dataset.Column{}, false
}

func isNumericAggregate(function dataset.AggregateFn) bool {
	switch function {
	case dataset.AggregateSum, dataset.AggregateAvg,
		dataset.AggregateMin, dataset.AggregateMax:
		return true
	default:
		return false
	}
}

func isNumericType(dataType registry.DataType) bool {
	switch dataType {
	case registry.DataInteger, registry.DataDecimal,
		registry.DataNumber, registry.DataMoney:
		return true
	default:
		return false
	}
}

func isFloat(value any) bool {
	switch value.(type) {
	case float32, float64:
		return true
	default:
		return false
	}
}

func isGoInteger(value any) bool {
	switch reflect.ValueOf(value).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}

// scalarKey renders a value for comparison across two runs. It is deliberately
// representation-insensitive: determinism is about the answer, not the Go type
// it arrives in, and checkAggregateTypes is what pins the type.
func scalarKey(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(typed)
	}
}
