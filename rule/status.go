package rule

// Status is the publication lifecycle status of a rule.
type Status string

const (
	// StatusDraft is not evaluable.
	StatusDraft Status = "draft"
	// StatusPendingApproval is not evaluable.
	StatusPendingApproval Status = "pending-approval"
	// StatusActive is evaluable.
	StatusActive Status = "active"
	// StatusScheduled is evaluable within its effective interval.
	StatusScheduled Status = "scheduled"
	// StatusDeprecated is not selected for new evaluations.
	StatusDeprecated Status = "deprecated"
	// StatusRetired is not evaluable.
	StatusRetired Status = "retired"
)

// Evaluable reports whether s may participate in resolution.
func (s Status) Evaluable() bool { return s == StatusActive || s == StatusScheduled }

// ChangeType declares the compatibility of a rule change.
type ChangeType string

const (
	// ChangeMinor is a compatible rule change.
	ChangeMinor ChangeType = "MINOR"
	// ChangeMajor is an incompatible rule change.
	ChangeMajor ChangeType = "MAJOR"
)
