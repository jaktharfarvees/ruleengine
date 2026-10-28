// Package datasettest checks a service's dataset.Executor against the
// contract the rule engine relies on.
//
// It lives in its own package so that the shipped dataset package carries no
// dependency on testing: a service that imports dataset for evaluation does
// not link a test framework into its binary.
//
// A service team runs the suite from its own test package:
//
//	func TestExecutorConformance(t *testing.T) {
//	    datasettest.ConformanceSuite(t,
//	        func() dataset.Executor { return newExecutor(testDB(t)) },
//	        manifestUnderTest,
//	        datasettest.WithTenantSeeder(seedRowsFor),
//	    )
//	}
//
// The suite checks the properties the library cannot enforce from the outside:
// that the Executor declares the manifest it serves, refuses a query carrying
// no injected tenant predicate, actually isolates one tenant's rows from
// another's, stays inside the row limit, honours cancellation, answers
// deterministically, and returns the documented Go type for each aggregate.
package datasettest
