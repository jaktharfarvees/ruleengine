package dataset

import "context"

// Result is what an Executor returns for one query.
//
// RowsScanned is not optional bookkeeping. The fact budget checks it after the
// fact as well as capping it in the query, because an executor that reports
// scanning more rows than the limit is misbehaving and the decision fails
// closed rather than trusting the value it returned.
type Result struct {
	// Value is the aggregate's result, or nil when no row matched.
	Value any
	// RowsScanned is how many rows the executor examined.
	RowsScanned int
}

// Executor runs a compiled query against the owning service's own storage.
//
// Each service implements this for its own datasets. The library has already
// checked everything checkable: the dataset is published, the columns are
// permitted, the window is within the maximum, the aggregate is offered, the
// values are typed, and a tenant predicate is present. An implementation's job
// is to translate a Query into its storage dialect and run it.
//
// An implementation must:
//   - assert the injected tenant predicate is present and apply it, refusing
//     the query if it is absent, rather than assuming the library added it;
//   - apply every predicate, the window and the row limit, and report
//     RowsScanned honestly;
//   - honour context cancellation and the deadline the library sets;
//   - return a nil Value when nothing matched, rather than a zero that the
//     caller cannot distinguish from a real result;
//   - never read a clock: the window is already absolute.
//
// Run dataset.ConformanceSuite against an implementation to check these.
type Executor interface {
	// Manifests returns the published manifests this executor can serve.
	Manifests() []Manifest
	// Run executes a compiled query.
	Run(ctx context.Context, query Query) (Result, error)
}
