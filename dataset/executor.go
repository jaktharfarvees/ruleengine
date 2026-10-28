package dataset

import "context"

// Result is what an Executor returns for one query.
//
// RowsScanned is not optional bookkeeping. The fact budget checks it after the
// fact as well as capping it in the query, because an executor that reports
// scanning more rows than the limit is misbehaving and the decision fails
// closed rather than trusting the value it returned.
//
// # The type of Value
//
// Value is nil when no row matched, whatever the aggregate. That is how an
// Executor says "nothing", and it is why returning a zero instead is wrong:
// the caller cannot tell an absent balance from a balance of zero, and the
// field's declared onEmpty is the author's answer to that question.
//
// When a row did match, Value must carry one of these:
//
//	count, streak    a Go integer (int, int64, …). A count of rows is a whole
//	                 number of rows.
//	exists           bool.
//	latest           the cell itself, satisfying its own column's declared
//	                 dataType in the manifest.
//	sum, avg,        an exactly representable number: a string, a json.Number,
//	min, max         a Go integer, or the library's own decimal.
//
// Binary floating point is never acceptable, for any aggregate. A float64
// cannot represent most decimal money values exactly, the engine refuses to
// compare one, and strict boundary validation rejects it for a field typed
// decimal, number or money. Return "1234.56" rather than 1234.56.
//
// The library's decimal type is internal, so an Executor outside this module
// cannot name it. That is deliberate and costs nothing: a decimal string is
// accepted everywhere the decimal itself is, and is the normal shape to read
// out of a database driver.
//
// dataset/datasettest.ConformanceSuite checks all of this against a real
// implementation.
type Result struct {
	// Value is the aggregate's result, or nil when no row matched. See the
	// type table on Result.
	Value any
	// RowsScanned is how many rows the executor examined, matching or not.
	//
	// Report it honestly. It is charged to the request's MaxRowsScanned
	// budget, so an executor that scans every tenant's rows and filters in
	// memory spends one tenant's budget on another's data.
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
// A Query names logical things: a dataset identifier and the column names the
// manifest declares. Mapping those to physical storage — a table, a
// collection, a column whose real name differs — is the implementation's own
// business, and the library neither records nor needs that mapping. The one
// name the library fixes is the tenant column, dataset.TenantColumn
// ("tenantId"); a store that spells it differently maps it like any other.
//
// An implementation must:
//   - assert the injected tenant predicate is present and apply it, refusing
//     the query if it is absent, rather than assuming the library added it;
//   - apply every predicate, the window and the row limit, and report
//     RowsScanned honestly;
//   - honour context cancellation and the deadline the library sets;
//   - return a nil Value when nothing matched, rather than a zero that the
//     caller cannot distinguish from a real result, and otherwise the type
//     the Result documentation requires for that aggregate;
//   - never read a clock: the window is already absolute.
//
// Run dataset/datasettest.ConformanceSuite against an implementation to check
// these.
type Executor interface {
	// Manifests returns the published manifests this executor can serve.
	Manifests() []Manifest
	// Run executes a compiled query.
	Run(ctx context.Context, query Query) (Result, error)
}
