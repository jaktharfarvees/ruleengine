package resolve

import (
	"time"

	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/rule"
)

// Input contains the subject and request data used by pure rule resolution.
type Input struct {
	Tenant     string         `json:"tenant"`
	Family     rule.Family    `json:"family"`
	NodeID     string         `json:"nodeId"`
	EmployeeID string         `json:"employeeId"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Context    map[string]any `json:"context,omitempty"`
	ValidAt    time.Time      `json:"validAt"`
}

// Candidate records how one authored rule progressed through resolution.
type Candidate struct {
	Rule           *rule.RuleDefinition        `json:"rule,omitempty"`
	Precedence     hierarchy.DerivedPrecedence `json:"precedence"`
	OnPath         bool                        `json:"onPath"`
	FiltersMatched bool                        `json:"filtersMatched"`
	FailedOn       *rule.FilterCondition       `json:"failedOn,omitempty"`
	EliminatedBy   EliminatedBy                `json:"eliminatedBy,omitempty"`
	Winner         bool                        `json:"winner"`
	Reason         string                      `json:"reason,omitempty"`
}

// Result is the deterministic output of rule resolution.
type Result struct {
	Path       []hierarchy.PathEntry `json:"path"`
	Candidates []Candidate           `json:"candidates"`
	Winner     *Candidate            `json:"winner,omitempty"`
	Tie        *TieInfo              `json:"tie,omitempty"`
	FailClosed bool                  `json:"failClosed"`
	ReasonCode string                `json:"reasonCode"`
}

// TieInfo describes candidates that remain tied after every tie-break.
type TieInfo struct {
	Candidates []rule.RuleKeyVersion `json:"candidates"`
	Order      int                   `json:"order"`
	Depth      int                   `json:"depth"`
	Message    string                `json:"message"`
}
