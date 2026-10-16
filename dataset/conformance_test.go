package dataset

import (
	"context"
	"strings"
	"testing"
)

// compliantExecutor is the smallest executor that honours the contract: it
// asserts the tenant predicate, honours cancellation, stays inside the row
// limit and answers deterministically.
type compliantExecutor struct {
	manifest Manifest
	rows     int
}

func (c *compliantExecutor) Manifests() []Manifest { return []Manifest{c.manifest} }

func (c *compliantExecutor) Run(ctx context.Context, query Query) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return Result{}, errNoTenantPredicate
	}
	scanned := c.rows
	if scanned > query.RowLimit() {
		scanned = query.RowLimit()
	}
	return Result{Value: scanned, RowsScanned: scanned}, nil
}

var errNoTenantPredicate = errTenant{}

type errTenant struct{}

func (errTenant) Error() string { return "query carries no injected tenant predicate" }

func TestConformanceSuiteAcceptsACompliantExecutor(t *testing.T) {
	manifest := validManifest()
	ConformanceSuite(t, func() Executor {
		return &compliantExecutor{manifest: manifest, rows: 3}
	}, manifest)
}

func TestConformanceSuiteCatchesEveryViolation(t *testing.T) {
	manifest := validManifest()
	tests := []struct {
		name     string
		executor func() Executor
		want     string
	}{
		{
			name:     "ignores the tenant predicate",
			executor: func() Executor { return &unscopedExecutor{manifest: manifest} },
			want:     "tenant predicate",
		},
		{
			name:     "does not declare the manifest",
			executor: func() Executor { return &silentExecutor{} },
			want:     "does not declare the manifest",
		},
		{
			name:     "declares an invalid manifest",
			executor: func() Executor { return &brokenManifestExecutor{manifest: manifest} },
			want:     "invalid manifest",
		},
		{
			name:     "exceeds the row limit",
			executor: func() Executor { return &greedyExecutor{manifest: manifest} },
			want:     "rows for a limit of",
		},
		{
			name:     "reports negative rows",
			executor: func() Executor { return &negativeExecutor{manifest: manifest} },
			want:     "rows scanned",
		},
		{
			name:     "ignores cancellation",
			executor: func() Executor { return &stubbornExecutor{manifest: manifest} },
			want:     "cancelled context",
		},
		{
			name:     "is not deterministic",
			executor: func() Executor { return &driftingExecutor{manifest: manifest} },
			want:     "not deterministic",
		},
		{
			name:     "refuses a valid query",
			executor: func() Executor { return &refusingExecutor{manifest: manifest} },
			want:     "refused a valid query",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingTB{TB: t}
			ConformanceSuite(recorder, test.executor, manifest)
			if !recorder.failed {
				t.Fatal("the suite accepted a non-compliant executor")
			}
			if !strings.Contains(recorder.message, test.want) {
				t.Fatalf("failure was %q, want it to mention %q", recorder.message, test.want)
			}
		})
	}
}

func TestConformanceSuiteRefusesBadInput(t *testing.T) {
	recorder := &recordingTB{TB: t}
	ConformanceSuite(recorder, nil, validManifest())
	if !recorder.failed || !strings.Contains(recorder.message, "constructor") {
		t.Fatalf("a nil constructor was accepted: %q", recorder.message)
	}

	broken := validManifest()
	broken.Grain = ""
	recorder = &recordingTB{TB: t}
	ConformanceSuite(recorder, func() Executor { return &compliantExecutor{manifest: broken} }, broken)
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
	conformanceChecks(recorder, func() Executor { return &unscopedExecutor{manifest: manifest} }, manifest)
	if !recorder.failed {
		t.Fatal("the inline checks accepted an unscoped executor")
	}
}

// TestConformanceQueryAdaptsToTheManifest covers the suite building a query
// for a dataset that offers no row aggregate and no windowable column.
func TestConformanceQueryAdaptsToTheManifest(t *testing.T) {
	manifest := validManifest()
	manifest.Aggregates = []AggregateFn{AggregateSum}
	manifest.Columns[1].Windowable = false // workDate
	query, ok := conformanceQuery(t, manifest)
	if !ok {
		t.Fatal("the suite could not build a query")
	}
	if query.Aggregate().Fn != AggregateSum || query.Aggregate().On == "" {
		t.Fatalf("aggregate = %+v, want a column sum", query.Aggregate())
	}
	if _, windowed := query.Window(); windowed {
		t.Fatal("the suite windowed a dataset with no windowable column")
	}

	// A dataset offering only a column aggregate but declaring no aggregatable
	// column is a contradiction the suite reports rather than panicking on.
	manifest = validManifest()
	manifest.Aggregates = []AggregateFn{AggregateSum}
	for index := range manifest.Columns {
		manifest.Columns[index].Aggregatable = false
	}
	recorder := &recordingTB{TB: t}
	if _, built := conformanceQuery(recorder, manifest); built {
		t.Fatal("the suite built a query from a contradictory manifest")
	}
	if !recorder.failed || !strings.Contains(recorder.message, "aggregatable") {
		t.Fatalf("the contradiction was not reported: %q", recorder.message)
	}
}

func TestPickAggregateFallsBackToWhatIsOffered(t *testing.T) {
	manifest := validManifest()
	manifest.Aggregates = []AggregateFn{AggregateAvg}
	if got := pickAggregate(manifest); got != AggregateAvg {
		t.Fatalf("pickAggregate = %q, want avg", got)
	}
	manifest.Aggregates = []AggregateFn{AggregateExists, AggregateCount}
	if got := pickAggregate(manifest); got != AggregateCount {
		t.Fatalf("pickAggregate = %q, want the row count it prefers", got)
	}
}

// --- non-compliant executors ---

type unscopedExecutor struct{ manifest Manifest }

func (u *unscopedExecutor) Manifests() []Manifest { return []Manifest{u.manifest} }
func (u *unscopedExecutor) Run(ctx context.Context, _ Query) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return Result{Value: 0, RowsScanned: 0}, nil
}

type silentExecutor struct{}

func (s *silentExecutor) Manifests() []Manifest { return nil }
func (s *silentExecutor) Run(context.Context, Query) (Result, error) {
	return Result{}, nil
}

type brokenManifestExecutor struct{ manifest Manifest }

func (b *brokenManifestExecutor) Manifests() []Manifest {
	broken := b.manifest
	broken.Grain = ""
	return []Manifest{broken}
}
func (b *brokenManifestExecutor) Run(context.Context, Query) (Result, error) {
	return Result{}, nil
}

type greedyExecutor struct{ manifest Manifest }

func (g *greedyExecutor) Manifests() []Manifest { return []Manifest{g.manifest} }
func (g *greedyExecutor) Run(ctx context.Context, query Query) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return Result{}, errNoTenantPredicate
	}
	return Result{Value: 1, RowsScanned: query.RowLimit() + 1}, nil
}

type negativeExecutor struct{ manifest Manifest }

func (n *negativeExecutor) Manifests() []Manifest { return []Manifest{n.manifest} }
func (n *negativeExecutor) Run(ctx context.Context, query Query) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return Result{}, errNoTenantPredicate
	}
	return Result{Value: 1, RowsScanned: -1}, nil
}

type stubbornExecutor struct{ manifest Manifest }

func (s *stubbornExecutor) Manifests() []Manifest { return []Manifest{s.manifest} }
func (s *stubbornExecutor) Run(_ context.Context, query Query) (Result, error) {
	if _, scoped := query.TenantPredicate(); !scoped {
		return Result{}, errNoTenantPredicate
	}
	return Result{Value: 1, RowsScanned: 1}, nil
}

type driftingExecutor struct {
	manifest Manifest
	calls    int
}

func (d *driftingExecutor) Manifests() []Manifest { return []Manifest{d.manifest} }
func (d *driftingExecutor) Run(ctx context.Context, query Query) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return Result{}, errNoTenantPredicate
	}
	d.calls++
	return Result{Value: d.calls, RowsScanned: 1}, nil
}

type refusingExecutor struct{ manifest Manifest }

func (r *refusingExecutor) Manifests() []Manifest { return []Manifest{r.manifest} }
func (r *refusingExecutor) Run(ctx context.Context, query Query) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if _, scoped := query.TenantPredicate(); !scoped {
		return Result{}, errNoTenantPredicate
	}
	return Result{}, errTenant{}
}

// recordingTB captures a failure instead of failing the surrounding test, so a
// test can assert that the suite rejects something.
type recordingTB struct {
	testing.TB
	failed  bool
	message string
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

func joinArgs(args []any) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if text, ok := arg.(string); ok {
			parts = append(parts, text)
		}
	}
	return " " + strings.Join(parts, " ")
}
