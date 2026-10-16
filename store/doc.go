// Package store owns immutable, atomically published snapshots of the rule
// artifacts needed by an in-process evaluator.
//
// A snapshot deliberately stores authored rule logic rather than an
// engine-specific compiled representation. Compilation remains behind the
// engine swap point; coupling this package to decisiontable.Table would make
// selecting a different engine a store migration. The snapshot and index
// shape leave room for a generic compiled-program hook in the pipeline phase.
package store
