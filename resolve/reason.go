package resolve

// EliminatedBy identifies the resolution stage that discarded a candidate.
type EliminatedBy string

const (
	// EliminatedNone means the candidate survived.
	EliminatedNone EliminatedBy = ""
	// EliminatedNotOnPath means the anchor does not cover the subject.
	EliminatedNotOnPath EliminatedBy = "not-on-path"
	// EliminatedFilters means a scope filter failed.
	EliminatedFilters EliminatedBy = "filters"
	// EliminatedEffectiveDate means the rule was not effective.
	EliminatedEffectiveDate EliminatedBy = "effective-date"
	// EliminatedStatus means the status was not evaluable.
	EliminatedStatus EliminatedBy = "status"
	// EliminatedCrossTenant means the candidate belonged to another tenant.
	EliminatedCrossTenant EliminatedBy = "cross-tenant"
	// EliminatedLowerOrder means a stronger candidate won.
	EliminatedLowerOrder EliminatedBy = "lower-order"
	// EliminatedTieBreak means explicit priority selected another rule.
	EliminatedTieBreak EliminatedBy = "tie-break"
)
