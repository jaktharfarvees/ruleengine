package datasettest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/internal/decimalx"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

// validManifest is deliberately a v2-only manifest: AllowedAggregates and
// DataType are set, the deprecated Aggregates and Type fields are not. Reading
// the deprecated field directly would index an empty slice.
func validManifest() dataset.Manifest {
	return dataset.Manifest{
		DatasetID: "attendance.days", SourceService: "attendance-service", Domain: "attendance",
		Grain: "one row per employee per work day", TenantScoped: true,
		Status: dataset.Published, Version: "1.0.0",
		PublishedAt:   time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		MaxWindowDays: 365, RowLimit: 2000,
		AllowedAggregates: []dataset.AggregateFn{
			dataset.AggregateCount, dataset.AggregateSum, dataset.AggregateAvg,
			dataset.AggregateMin, dataset.AggregateMax,
			dataset.AggregateStreak, dataset.AggregateLatest, dataset.AggregateExists,
		},
		Columns: []dataset.Column{
			{Name: "employeeId", DataType: registry.DataString, Filterable: true, PII: true},
			{Name: "workDate", DataType: registry.DataDate, Filterable: true, Windowable: true},
			{Name: "lateMinutes", DataType: registry.DataInteger, Filterable: true, Aggregatable: true},
			{Name: "workedHours", DataType: registry.DataDecimal, Aggregatable: true},
			{Name: "status", DataType: registry.DataEnum, Filterable: true,
				AllowedValues: []string{"OK", "LATE"}},
		},
	}
}

var errNoTenantPredicate = errors.New("query carries no injected tenant predicate")

// fakeExecutor is the smallest executor that honours the whole contract: it
// asserts the tenant predicate, applies it, honours cancellation, stays inside
// the row limit, answers deterministically, and returns the documented type
// for every aggregate.
type fakeExecutor struct {
	manifest dataset.Manifest
	// rows is tenant to row count, which is all the aggregates here need.
	rows map[string]int
}

func newFakeExecutor(manifest dataset.Manifest) *fakeExecutor {
	return &fakeExecutor{manifest: manifest, rows: map[string]int{}}
}

func (f *fakeExecutor) Manifests() []dataset.Manifest { return []dataset.Manifest{f.manifest} }

func (f *fakeExecutor) add(tenant string, rows int) { f.rows[tenant] += rows }

func (f *fakeExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	tenant, scoped := query.TenantPredicate()
	if !scoped || len(tenant.Values) != 1 {
		return dataset.Result{}, errNoTenantPredicate
	}
	// Apply the predicate: only this tenant's rows are visible, and only this
	// tenant's rows count towards RowsScanned.
	name, _ := tenant.Values[0].(string)
	scanned := f.rows[name]
	if scanned > query.RowLimit() {
		scanned = query.RowLimit()
	}
	return dataset.Result{Value: f.value(query.Aggregate(), scanned), RowsScanned: scanned}, nil
}

func (f *fakeExecutor) value(spec dataset.AggregateSpec, rows int) any {
	if rows == 0 {
		// Nothing matched. The contract is nil, not a zero the caller cannot
		// tell apart from a real result.
		return nil
	}
	switch spec.Fn {
	case dataset.AggregateCount, dataset.AggregateStreak:
		return rows
	case dataset.AggregateExists:
		return true
	case dataset.AggregateLatest:
		column, _ := f.manifest.Canonical().Column(spec.On)
		return latestFor(column)
	case dataset.AggregateSum, dataset.AggregateAvg,
		dataset.AggregateMin, dataset.AggregateMax:
		return decimalx.FromInt(int64(rows))
	default:
		return nil
	}
}

// latestFor returns a value of the column's own declared type, which is what
// a latest aggregate is: the cell itself.
func latestFor(column dataset.Column) any {
	switch column.DataType {
	case registry.DataInteger:
		return 7
	case registry.DataDecimal, registry.DataNumber, registry.DataMoney:
		return decimalx.FromInt(7)
	case registry.DataBoolean:
		return true
	case registry.DataDate:
		return "2026-06-01"
	case registry.DataDateTime:
		return "2026-06-01T00:00:00Z"
	case registry.DataEnum:
		if len(column.AllowedValues) > 0 {
			return column.AllowedValues[0]
		}
		return ""
	default:
		return "value"
	}
}

// seedFake is the Seeder a service would write for its own executor.
func seedFake(executor dataset.Executor, tenant string) error {
	fake, ok := executor.(*fakeExecutor)
	if !ok {
		return errors.New("seeder was handed an executor it does not know")
	}
	fake.add(tenant, 3)
	return nil
}

func TestConformanceSuiteAcceptsACompliantExecutor(t *testing.T) {
	manifest := validManifest()
	ConformanceSuite(t, func() dataset.Executor {
		return newFakeExecutor(manifest)
	}, manifest, WithTenantSeeder(seedFake))
}

func TestConformanceSuiteCatchesEveryViolation(t *testing.T) {
	manifest := validManifest()
	tests := []struct {
		name     string
		executor func() dataset.Executor
		opts     []Option
		want     string
	}{
		{
			name:     "ignores the tenant predicate",
			executor: func() dataset.Executor { return &unscopedExecutor{manifest: manifest} },
			opts:     []Option{WithTenantSeeder(seedUnscoped)},
			want:     "tenant predicate",
		},
		{
			name:     "asserts the predicate but does not apply it",
			executor: func() dataset.Executor { return newLeakyExecutor(manifest) },
			opts:     []Option{WithTenantSeeder(seedLeaky)},
			want:     "not being applied",
		},
		{
			name:     "does not declare the manifest",
			executor: func() dataset.Executor { return &silentExecutor{} },
			want:     "does not declare the manifest",
		},
		{
			name:     "declares an invalid manifest",
			executor: func() dataset.Executor { return &brokenManifestExecutor{manifest: manifest} },
			want:     "invalid manifest",
		},
		{
			name:     "exceeds the row limit",
			executor: func() dataset.Executor { return &greedyExecutor{manifest: manifest} },
			want:     "rows for a limit of",
		},
		{
			name:     "reports negative rows",
			executor: func() dataset.Executor { return &negativeExecutor{manifest: manifest} },
			want:     "rows scanned",
		},
		{
			name:     "ignores cancellation",
			executor: func() dataset.Executor { return &stubbornExecutor{manifest: manifest} },
			want:     "cancelled context",
		},
		{
			name:     "is not deterministic",
			executor: func() dataset.Executor { return &driftingExecutor{manifest: manifest} },
			want:     "not deterministic",
		},
		{
			name:     "refuses a valid query",
			executor: func() dataset.Executor { return &refusingExecutor{manifest: manifest} },
			want:     "refused a valid query",
		},
		{
			name:     "returns a float for a numeric aggregate",
			executor: func() dataset.Executor { return newTypedExecutor(manifest, float64(1.5)) },
			opts:     []Option{WithTenantSeeder(seedTyped)},
			want:     "binary floating point",
		},
		{
			name:     "returns a string row count",
			executor: func() dataset.Executor { return newCountAsStringExecutor(manifest) },
			opts:     []Option{WithTenantSeeder(seedCountAsString)},
			want:     "want a Go integer",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingTB{TB: t}
			// A case that supplies a seeder wants the isolation and type
			// checks to run; one that does not would otherwise fail on the
			// missing seeder rather than on the violation under test.
			opts := test.opts
			if len(opts) == 0 {
				opts = []Option{WithoutTenantIsolation()}
			}
			ConformanceSuite(recorder, test.executor, manifest, opts...)
			if !recorder.failed {
				t.Fatal("the suite accepted a non-compliant executor")
			}
			if !strings.Contains(recorder.message, test.want) {
				t.Fatalf("failure was %q, want it to mention %q", recorder.message, test.want)
			}
		})
	}
}

// TestTenantIsolationRequiresASeeder pins the rule that silence is not a pass:
// without a seeder the check fails, because a suite with no data cannot tell
// an isolating executor from one that ignores the predicate.
func TestTenantIsolationRequiresASeeder(t *testing.T) {
	manifest := validManifest()
	recorder := &recordingTB{TB: t}
	ConformanceSuite(recorder, func() dataset.Executor {
		return newFakeExecutor(manifest)
	}, manifest)
	if !recorder.failed {
		t.Fatal("the suite passed an executor whose tenant isolation was never checked")
	}
	if !strings.Contains(recorder.message, "no tenant seeder was supplied") {
		t.Fatalf("failure was %q, want it to name the missing seeder", recorder.message)
	}
	if !strings.Contains(recorder.message, "WithTenantSeeder") {
		t.Fatalf("the failure does not say how to fix it: %q", recorder.message)
	}
}

// TestWideScanIsWarnedNotFailed pins the deliberate asymmetry in the isolation
// check: a wrong Value fails, while rows scanned growing with another tenant's
// volume only warns. A store with no index on the tenant column must scan and
// filter, and reporting that honestly is what the contract asks for.
func TestWideScanIsWarnedNotFailed(t *testing.T) {
	manifest := validManifest()
	recorder := &recordingTB{TB: t}
	checkTenantIsolation(recorder, func() dataset.Executor {
		return newWideScanExecutor(manifest)
	}, manifest, &options{seed: seedWideScan})
	if recorder.failed {
		t.Fatalf("a correct answer from a wide-scanning executor was failed: %q", recorder.message)
	}
	if !strings.Contains(recorder.logged, "WARNING") ||
		!strings.Contains(recorder.logged, "MaxRowsScanned") {
		t.Fatalf("the wide scan was not warned about: %q", recorder.logged)
	}
}

// TestTenantIsolationOptOutWarns pins that the opt-out passes but is loud.
func TestTenantIsolationOptOutWarns(t *testing.T) {
	manifest := validManifest()
	recorder := &recordingTB{TB: t}
	ConformanceSuite(recorder, func() dataset.Executor {
		return newFakeExecutor(manifest)
	}, manifest, WithoutTenantIsolation())
	if recorder.failed {
		t.Fatalf("the explicit opt-out still failed: %q", recorder.message)
	}
	if !strings.Contains(recorder.logged, "WARNING") ||
		!strings.Contains(recorder.logged, "tenant isolation") {
		t.Fatalf("the opt-out did not warn: %q", recorder.logged)
	}
}

// TestAggregateTypeMatrixCoversEveryOfferedAggregate pins that the matrix runs
// one query per aggregate rather than only the one pickAggregate prefers.
func TestAggregateTypeMatrixCoversEveryOfferedAggregate(t *testing.T) {
	manifest := validManifest()
	executor := newCountingExecutor(manifest)
	recorder := &recordingTB{TB: t}
	checkAggregateTypes(recorder, func() dataset.Executor { return executor }, manifest,
		&options{seed: func(dataset.Executor, string) error { return nil }})
	if recorder.failed {
		t.Fatalf("the matrix rejected a compliant executor: %q", recorder.message)
	}
	want := len(manifest.AllowedAggregates)
	if executor.seen != want {
		t.Fatalf("the matrix ran %d queries for %d offered aggregates", executor.seen, want)
	}
	for _, function := range manifest.AllowedAggregates {
		if !executor.functions[function] {
			t.Errorf("the matrix never queried the %q aggregate", function)
		}
	}
}

// TestPickAggregateReadsAllowedAggregates is the regression test for item 4: a
// v2-only manifest has an empty deprecated Aggregates slice, and indexing it
// panicked.
func TestPickAggregateReadsAllowedAggregates(t *testing.T) {
	manifest := validManifest()
	if len(manifest.Aggregates) != 0 {
		t.Fatalf("the fixture is not v2-only: Aggregates = %v", manifest.Aggregates)
	}

	// The preferred row aggregate is offered, so it is chosen.
	if got := pickAggregate(manifest); got != dataset.AggregateCount {
		t.Fatalf("pickAggregate = %q, want the row count it prefers", got)
	}

	// None of the preferred four is offered, so it falls back to the first
	// AllowedAggregates entry. Before the fix this read Aggregates[0] and
	// panicked with an index out of range.
	fallback := validManifest()
	fallback.AllowedAggregates = []dataset.AggregateFn{dataset.AggregateAvg}
	if got := pickAggregate(fallback); got != dataset.AggregateAvg {
		t.Fatalf("pickAggregate = %q, want avg", got)
	}

	// A v1-style manifest still works, through Canonical.
	legacy := validManifest()
	legacy.AllowedAggregates = nil
	legacy.Aggregates = []dataset.AggregateFn{dataset.AggregateAvg}
	if got := pickAggregate(legacy); got != dataset.AggregateAvg {
		t.Fatalf("pickAggregate on a v1 manifest = %q, want avg", got)
	}

	// An empty manifest is reported by Validate, not a panic here.
	empty := validManifest()
	empty.AllowedAggregates = nil
	if got := pickAggregate(empty); got != dataset.AggregateCount {
		t.Fatalf("pickAggregate on an empty manifest = %q", got)
	}
}

func TestConformanceSuiteRefusesBadInput(t *testing.T) {
	recorder := &recordingTB{TB: t}
	ConformanceSuite(recorder, nil, validManifest())
	if !recorder.failed || !strings.Contains(recorder.message, "constructor") {
		t.Fatalf("a nil constructor was accepted: %q", recorder.message)
	}

	broken := validManifest()
	broken.SourceService = ""
	recorder = &recordingTB{TB: t}
	ConformanceSuite(recorder, func() dataset.Executor {
		return newFakeExecutor(broken)
	}, broken)
	if !recorder.failed || !strings.Contains(recorder.message, "itself invalid") {
		t.Fatalf("an invalid manifest under test was accepted: %q", recorder.message)
	}
}

// TestConformanceSuiteRunsInlineWithoutSubtests covers a TB that cannot host
// sub-tests, such as a benchmark: the checks still run rather than silently
// checking nothing.
func TestConformanceSuiteRunsInlineWithoutSubtests(t *testing.T) {
	manifest := validManifest()
	recorder := &recordingTB{TB: t}
	conformanceChecks(recorder, func() dataset.Executor {
		return &unscopedExecutor{manifest: manifest}
	}, manifest, &options{skipIsolation: true})
	if !recorder.failed {
		t.Fatal("the inline checks accepted an unscoped executor")
	}
}

// TestConformanceQueryAdaptsToTheManifest covers the suite building a query
// for a dataset that offers no row aggregate and no windowable column.
func TestConformanceQueryAdaptsToTheManifest(t *testing.T) {
	manifest := validManifest()
	manifest.AllowedAggregates = []dataset.AggregateFn{dataset.AggregateSum}
	manifest.Columns[1].Windowable = false // workDate
	query, ok := conformanceQuery(t, manifest, tenantA)
	if !ok {
		t.Fatal("the suite could not build a query")
	}
	if query.Aggregate().Fn != dataset.AggregateSum || query.Aggregate().On == "" {
		t.Fatalf("aggregate = %+v, want a column sum", query.Aggregate())
	}
	if _, windowed := query.Window(); windowed {
		t.Fatal("the suite windowed a dataset with no windowable column")
	}

	// A dataset offering only a column aggregate but declaring no aggregatable
	// column is a contradiction the suite reports rather than panicking on.
	manifest = validManifest()
	manifest.AllowedAggregates = []dataset.AggregateFn{dataset.AggregateLatest}
	for index := range manifest.Columns {
		manifest.Columns[index].Aggregatable = false
	}
	recorder := &recordingTB{TB: t}
	if _, built := conformanceQuery(recorder, manifest, tenantA); built {
		t.Fatal("the suite built a query from a contradictory manifest")
	}
	if !recorder.failed || !strings.Contains(recorder.message, "aggregatable") {
		t.Fatalf("the contradiction was not reported: %q", recorder.message)
	}

	// A numeric aggregate with no numeric column is logged, not failed: the
	// suite cannot check a type it cannot ask for, and that is not the
	// executor's fault.
	numeric := validManifest()
	numeric.AllowedAggregates = []dataset.AggregateFn{dataset.AggregateSum}
	for index := range numeric.Columns {
		numeric.Columns[index].Aggregatable = numeric.Columns[index].Name == "status"
	}
	recorder = &recordingTB{TB: t}
	if _, built := conformanceQuery(recorder, numeric, tenantA); built {
		t.Fatal("the suite summed a non-numeric column")
	}
	if recorder.failed {
		t.Fatalf("a manifest with no numeric column was failed rather than logged: %q", recorder.message)
	}
	if !strings.Contains(recorder.logged, "no numeric") {
		t.Fatalf("the skip was not explained: %q", recorder.logged)
	}
}

// TestUntenantedCopyStripsOnlyTheInjectedPredicate pins the helper the suite
// depends on: the author's own filters must survive, or the check would be
// testing a differently-shaped query.
func TestUntenantedCopyStripsOnlyTheInjectedPredicate(t *testing.T) {
	manifest := validManifest()
	source := registry.FactSource{
		Kind: registry.FactDataset, DatasetID: manifest.DatasetID,
		Filter: []rule.FilterCondition{
			{Attribute: "employeeId", Op: rule.FilterEquals, Value: dataset.TokenEmployeeID},
		},
		Aggregate: &registry.FactAggregate{Fn: dataset.AggregateCount},
	}
	validAt := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	query, _, err := dataset.Compile(source, manifest, dataset.Params{
		Tenant: tenantA, EmployeeID: "E0001", NodeID: "N0001",
		AsOf: validAt, ValidAt: validAt, RowLimit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := query.TenantPredicate(); !present {
		t.Fatal("Compile produced no tenant predicate")
	}

	stripped := dataset.UntenantedCopy(query)
	if _, present := stripped.TenantPredicate(); present {
		t.Fatal("the tenant predicate survived")
	}
	var sawEmployee bool
	for _, predicate := range stripped.Predicates() {
		if predicate.Column == "employeeId" {
			sawEmployee = true
		}
		if predicate.Injected {
			t.Errorf("an injected predicate survived: %+v", predicate)
		}
	}
	if !sawEmployee {
		t.Error("the author's own filter was stripped too")
	}
	// The original must be untouched: the suite reuses it.
	if _, present := query.TenantPredicate(); !present {
		t.Error("UntenantedCopy mutated its argument")
	}
}

// --- non-compliant executors ---

type unscopedExecutor struct{ manifest dataset.Manifest }

func (u *unscopedExecutor) Manifests() []dataset.Manifest {
	return []dataset.Manifest{u.manifest}
}

func (u *unscopedExecutor) Run(ctx context.Context, _ dataset.Query) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	return dataset.Result{Value: 0, RowsScanned: 0}, nil
}

func seedUnscoped(dataset.Executor, string) error { return nil }

// leakyExecutor asserts the predicate is present, then ignores it. This is the
// executor the old suite could not catch.
type leakyExecutor struct {
	manifest dataset.Manifest
	rows     int
}

func newLeakyExecutor(manifest dataset.Manifest) *leakyExecutor {
	return &leakyExecutor{manifest: manifest}
}

func (l *leakyExecutor) Manifests() []dataset.Manifest {
	return []dataset.Manifest{l.manifest}
}

func (l *leakyExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return dataset.Result{}, errNoTenantPredicate
	}
	if l.rows == 0 {
		return dataset.Result{}, nil
	}
	return dataset.Result{Value: l.rows, RowsScanned: l.rows}, nil
}

func seedLeaky(executor dataset.Executor, _ string) error {
	leaky, ok := executor.(*leakyExecutor)
	if !ok {
		return errors.New("wrong executor")
	}
	leaky.rows += 3 // every tenant's rows land in the same pile
	return nil
}

// wideScanExecutor returns the right Value but reads every tenant's rows to
// get it, so the other tenant's volume reaches this tenant's row budget.
type wideScanExecutor struct {
	manifest dataset.Manifest
	tenants  int
}

func newWideScanExecutor(manifest dataset.Manifest) *wideScanExecutor {
	return &wideScanExecutor{manifest: manifest}
}

func (w *wideScanExecutor) Manifests() []dataset.Manifest {
	return []dataset.Manifest{w.manifest}
}

func (w *wideScanExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return dataset.Result{}, errNoTenantPredicate
	}
	if w.tenants == 0 {
		return dataset.Result{}, nil
	}
	return dataset.Result{Value: 3, RowsScanned: 3 * w.tenants}, nil
}

func seedWideScan(executor dataset.Executor, _ string) error {
	wide, ok := executor.(*wideScanExecutor)
	if !ok {
		return errors.New("wrong executor")
	}
	wide.tenants++
	return nil
}

// typedExecutor returns a chosen value for every numeric aggregate, so a test
// can present a wrong type.
type typedExecutor struct {
	manifest dataset.Manifest
	value    any
	rows     int
}

func newTypedExecutor(manifest dataset.Manifest, value any) *typedExecutor {
	return &typedExecutor{manifest: manifest, value: value}
}

func (ty *typedExecutor) Manifests() []dataset.Manifest {
	return []dataset.Manifest{ty.manifest}
}

func (ty *typedExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return dataset.Result{}, errNoTenantPredicate
	}
	if ty.rows == 0 {
		return dataset.Result{}, nil
	}
	switch query.Aggregate().Fn {
	case dataset.AggregateSum, dataset.AggregateAvg,
		dataset.AggregateMin, dataset.AggregateMax:
		return dataset.Result{Value: ty.value, RowsScanned: ty.rows}, nil
	case dataset.AggregateExists:
		return dataset.Result{Value: true, RowsScanned: ty.rows}, nil
	case dataset.AggregateLatest:
		return dataset.Result{Value: decimalx.FromInt(7), RowsScanned: ty.rows}, nil
	default:
		return dataset.Result{Value: ty.rows, RowsScanned: ty.rows}, nil
	}
}

func seedTyped(executor dataset.Executor, _ string) error {
	typed, ok := executor.(*typedExecutor)
	if !ok {
		return errors.New("wrong executor")
	}
	typed.rows += 3
	return nil
}

// countAsStringExecutor returns a row count as text.
type countAsStringExecutor struct {
	manifest dataset.Manifest
	rows     int
}

func newCountAsStringExecutor(manifest dataset.Manifest) *countAsStringExecutor {
	return &countAsStringExecutor{manifest: manifest}
}

func (c *countAsStringExecutor) Manifests() []dataset.Manifest {
	return []dataset.Manifest{c.manifest}
}

func (c *countAsStringExecutor) Run(
	ctx context.Context, query dataset.Query,
) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return dataset.Result{}, errNoTenantPredicate
	}
	if c.rows == 0 {
		return dataset.Result{}, nil
	}
	if query.Aggregate().Fn == dataset.AggregateCount {
		return dataset.Result{Value: "3", RowsScanned: c.rows}, nil
	}
	return dataset.Result{Value: decimalx.FromInt(int64(c.rows)), RowsScanned: c.rows}, nil
}

func seedCountAsString(executor dataset.Executor, _ string) error {
	counter, ok := executor.(*countAsStringExecutor)
	if !ok {
		return errors.New("wrong executor")
	}
	counter.rows += 3
	return nil
}

// countingExecutor records which aggregates the matrix asked for.
type countingExecutor struct {
	*fakeExecutor
	seen      int
	functions map[dataset.AggregateFn]bool
}

func newCountingExecutor(manifest dataset.Manifest) *countingExecutor {
	fake := newFakeExecutor(manifest)
	fake.add(tenantA, 3)
	return &countingExecutor{
		fakeExecutor: fake,
		functions:    map[dataset.AggregateFn]bool{},
	}
}

func (c *countingExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	c.seen++
	c.functions[query.Aggregate().Fn] = true
	return c.fakeExecutor.Run(ctx, query)
}

type silentExecutor struct{}

func (s *silentExecutor) Manifests() []dataset.Manifest { return nil }
func (s *silentExecutor) Run(context.Context, dataset.Query) (dataset.Result, error) {
	return dataset.Result{}, nil
}

type brokenManifestExecutor struct{ manifest dataset.Manifest }

func (b *brokenManifestExecutor) Manifests() []dataset.Manifest {
	broken := b.manifest
	broken.SourceService, broken.OwnerService = "", ""
	return []dataset.Manifest{broken}
}

func (b *brokenManifestExecutor) Run(context.Context, dataset.Query) (dataset.Result, error) {
	return dataset.Result{}, nil
}

type greedyExecutor struct{ manifest dataset.Manifest }

func (g *greedyExecutor) Manifests() []dataset.Manifest {
	return []dataset.Manifest{g.manifest}
}

func (g *greedyExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return dataset.Result{}, errNoTenantPredicate
	}
	return dataset.Result{Value: 1, RowsScanned: query.RowLimit() + 1}, nil
}

type negativeExecutor struct{ manifest dataset.Manifest }

func (n *negativeExecutor) Manifests() []dataset.Manifest {
	return []dataset.Manifest{n.manifest}
}

func (n *negativeExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return dataset.Result{}, errNoTenantPredicate
	}
	return dataset.Result{Value: 1, RowsScanned: -1}, nil
}

type stubbornExecutor struct{ manifest dataset.Manifest }

func (s *stubbornExecutor) Manifests() []dataset.Manifest {
	return []dataset.Manifest{s.manifest}
}

func (s *stubbornExecutor) Run(_ context.Context, query dataset.Query) (dataset.Result, error) {
	if _, scoped := query.TenantPredicate(); !scoped {
		return dataset.Result{}, errNoTenantPredicate
	}
	return dataset.Result{Value: 1, RowsScanned: 1}, nil
}

type driftingExecutor struct {
	manifest dataset.Manifest
	calls    int
}

func (d *driftingExecutor) Manifests() []dataset.Manifest {
	return []dataset.Manifest{d.manifest}
}

func (d *driftingExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return dataset.Result{}, errNoTenantPredicate
	}
	d.calls++
	return dataset.Result{Value: d.calls, RowsScanned: 1}, nil
}

type refusingExecutor struct{ manifest dataset.Manifest }

func (r *refusingExecutor) Manifests() []dataset.Manifest {
	return []dataset.Manifest{r.manifest}
}

func (r *refusingExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return dataset.Result{}, errNoTenantPredicate
	}
	return dataset.Result{}, errors.New("refused")
}

// recordingTB captures failures and logs instead of failing the surrounding
// test, so a test can assert that the suite rejects something.
type recordingTB struct {
	testing.TB
	failed  bool
	message string
	logged  string
}

func (r *recordingTB) Error(args ...any) {
	r.failed = true
	r.message += joinArgs(args)
}

func (r *recordingTB) Fatal(args ...any) {
	r.failed = true
	r.message += joinArgs(args)
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.failed = true
	r.message += format + joinArgs(args)
}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.failed = true
	r.message += format + joinArgs(args)
}

func (r *recordingTB) Log(args ...any) { r.logged += joinArgs(args) }

func (r *recordingTB) Logf(format string, args ...any) {
	r.logged += format + joinArgs(args)
}

func (r *recordingTB) Helper() {}

func joinArgs(args []any) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if text, ok := arg.(string); ok {
			parts = append(parts, text)
		}
	}
	return " " + strings.Join(parts, " ")
}
