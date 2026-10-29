// Package untenanted lets the dataset conformance suite build the one query
// dataset.Compile never produces: a query with no injected tenant predicate.
//
// It lives under dataset/internal so that only packages inside dataset (in
// practice dataset/datasettest) can import it. A query carrying no tenant
// predicate is precisely what the injected predicate exists to prevent, and
// handing one to a real Executor asks it for every tenant's rows, so the
// capability is deliberately not part of the public dataset API.
package untenanted

// Strip returns a copy of a dataset.Query with its injected tenant predicate
// removed. Package dataset installs it at init, because the query's fields are
// unexported there and this package cannot import dataset without a cycle.
// The argument and result are dataset.Query values.
var Strip func(query any) any
