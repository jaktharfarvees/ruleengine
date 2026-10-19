package rule

import (
	"encoding/json"
	"time"
)

// RuleDefinition is an authored, versioned decision rule.
type RuleDefinition struct {
	RuleKey          RuleKey           `json:"ruleKey"`
	Domain           string            `json:"domain"`
	RuleSet          string            `json:"ruleSet"`
	Name             string            `json:"name"`
	Description      string            `json:"description,omitempty"`
	Version          string            `json:"version"`
	ChangeType       ChangeType        `json:"changeType"`
	Status           Status            `json:"status"`
	Tenant           string            `json:"tenant"`
	Anchor           Anchor            `json:"anchor"`
	AttributeFilters []FilterCondition `json:"attributeFilters,omitempty"`
	// Scope is retained as a source-compatibility bridge for Go callers that
	// constructed v1 values. It is never serialized. New code must use Anchor
	// and AttributeFilters, which are the v2 wire contract.
	Scope            Scope `json:"-"`
	TieBreakPriority *int  `json:"tieBreakPriority,omitempty"`
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

// UnmarshalJSON decodes the v2 wire contract and initializes the deprecated
// in-memory Scope alias for source-compatible Go consumers.
func (r *RuleDefinition) UnmarshalJSON(data []byte) error {
	type wire RuleDefinition
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = RuleDefinition(decoded)
	r.Scope = Scope{Anchor: r.EffectiveAnchor(), Filters: append([]FilterCondition(nil), r.AttributeFilters...)}
	return nil
}

// MarshalJSON emits only the v2 contract, while accepting legacy in-memory
// construction through EffectiveAnchor and EffectiveAttributeFilters.
func (r RuleDefinition) MarshalJSON() ([]byte, error) {
	r.Anchor = r.EffectiveAnchor()
	r.AttributeFilters = append([]FilterCondition(nil), r.EffectiveAttributeFilters()...)
	r.Scope = Scope{}
	type wire RuleDefinition
	return json.Marshal(wire(r))
}

// EffectiveAnchor returns the v2 anchor, or the deprecated in-memory scope
// anchor when a legacy Go caller constructed the value directly.
func (r RuleDefinition) EffectiveAnchor() Anchor {
	if r.Anchor.Kind != "" || r.Anchor.NodeID != "" {
		anchor := r.Anchor
		if anchor.Kind == "" && anchor.NodeID != "" {
			anchor.Kind = AnchorNode
		}
		return anchor
	}
	anchor := r.Scope.Anchor
	if anchor.Kind == "" && anchor.NodeID != "" {
		anchor.Kind = AnchorNode
	}
	return anchor
}

// EffectiveAttributeFilters returns the v2 filters, or the deprecated
// in-memory scope filters for source-compatible Go construction.
func (r RuleDefinition) EffectiveAttributeFilters() []FilterCondition {
	if r.AttributeFilters != nil {
		return r.AttributeFilters
	}
	return r.Scope.Filters
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
