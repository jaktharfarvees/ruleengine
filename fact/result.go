package fact

// Resolved explains how one fact was obtained, without recording its value.
//
// The value is deliberately absent: these records reach logs and metrics,
// where a fact value may be personal data (INV8). A value that must be kept
// for audit goes to the decision logger, which writes to an access-controlled
// store.
type Resolved struct {
	// Field is the registered field name.
	Field string `json:"field"`
	// Kind is how the value was obtained.
	Kind Kind `json:"kind"`
	// FactSourceVersion is the declaration version that produced it, so a
	// replay can tell which recipe was in force.
	FactSourceVersion string `json:"factSourceVersion,omitempty"`
	// Dataset is the dataset queried, for a dataset fact.
	Dataset string `json:"dataset,omitempty"`
	// RowsScanned is what the executor reported.
	RowsScanned int `json:"rowsScanned,omitempty"`
	// ElapsedMS is how long the fetch took.
	ElapsedMS int `json:"elapsedMs,omitempty"`
	// Cached records that the value was reused from an earlier identical query
	// in the same request rather than fetched again.
	Cached bool `json:"cached"`
	// FromRequest records that the caller supplied the value, so nothing was
	// fetched for it.
	FromRequest bool `json:"fromRequest"`
}
