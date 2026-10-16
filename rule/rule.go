package rule

import "time"

// RuleDefinition is an authored, versioned decision rule.
type RuleDefinition struct {
	RuleKey          RuleKey    `json:"ruleKey"`
	Domain           string     `json:"domain"`
	RuleSet          string     `json:"ruleSet"`
	Name             string     `json:"name"`
	Description      string     `json:"description,omitempty"`
	Version          string     `json:"version"`
	ChangeType       ChangeType `json:"changeType"`
	Status           Status     `json:"status"`
	Tenant           string     `json:"tenant"`
	Scope            Scope      `json:"scope"`
	TieBreakPriority *int       `json:"tieBreakPriority,omitempty"`
	// ConflictPolicy is carried for the Rule Service and is not consulted by
	// resolution. Two rules at the same anchor are separated by filter
	// specificity, then by TieBreakPriority, and otherwise fail closed; see
	// docs/adr/0004-fail-closed-on-tie.md. Setting this field changes nothing
	// in the library today, which docs/OPEN_QUESTIONS.md records as a gap.
	ConflictPolicy  string    `json:"conflictPolicy,omitempty"`
	Effective       Effective `json:"effective"`
	DeclaredInputs  []string  `json:"declaredInputs"`
	DeclaredOutputs []string  `json:"declaredOutputs"`
	DecisionType    string    `json:"decisionType"`
	Logic           RuleLogic `json:"logic"`
	Guards          []string  `json:"guards,omitempty"`
	EngineRef       EngineRef `json:"engineRef"`
	Metadata        Metadata  `json:"metadata"`
}

// Effective is a half-open validity interval.
type Effective struct {
	ValidFrom time.Time  `json:"validFrom"`
	ValidTo   *time.Time `json:"validTo,omitempty"`
}

// Contains reports whether t belongs to [ValidFrom, ValidTo).
func (e Effective) Contains(t time.Time) bool {
	return !t.Before(e.ValidFrom) && (e.ValidTo == nil || t.Before(*e.ValidTo))
}

// EngineRef selects an evaluation implementation.
type EngineRef struct {
	Engine string `json:"engine"`
}

// Metadata records rule ownership, authorship, and approval data.
type Metadata struct {
	Owner           string     `json:"owner"`
	Tags            []string   `json:"tags,omitempty"`
	CreatedBy       string     `json:"createdBy"`
	CreatedAt       time.Time  `json:"createdAt"`
	ChangeNote      string     `json:"changeNote,omitempty"`
	ApprovedBy      string     `json:"approvedBy,omitempty"`
	ApprovedAt      *time.Time `json:"approvedAt,omitempty"`
	TemplateID      string     `json:"templateId,omitempty"`
	TemplateVersion string     `json:"templateVersion,omitempty"`
}
