package ruleengine

import (
	"time"

	"github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/resolve"
	"github.com/arrowhr/ruleengine/rule"
)

// Decision is an explainable evaluation outcome. A business refusal is
// represented by Failed and ReasonCodes rather than a Go error.
type Decision struct {
	DecisionID   string         `json:"decisionId"`
	RuleKey      rule.RuleKey   `json:"ruleKey,omitempty"`
	RuleVersion  string         `json:"ruleVersion,omitempty"`
	DecisionType string         `json:"decisionType,omitempty"`
	Result       map[string]any `json:"result,omitempty"`
	ReasonCodes  []ReasonCode   `json:"reasonCodes"`
	// ResolvedAt retains detailed in-process precedence for Go callers.
	ResolvedAt     *hierarchy.DerivedPrecedence `json:"-"`
	ResolvedAtNode string                       `json:"resolvedAt,omitempty"`
	AnchorPath     []string                     `json:"anchorPath,omitempty"`
	// SubjectPath is the deprecated Go alias for AnchorPath.
	SubjectPath         []hierarchy.PathEntry        `json:"-"`
	Evaluated           []Evaluated                  `json:"evaluated,omitempty"`
	FactsResolved       []FactResolved               `json:"factsResolved,omitempty"`
	ResolvedAttributes  map[string]ResolvedAttribute `json:"resolvedAttributes,omitempty"`
	Obligations         []string                     `json:"obligations,omitempty"`
	Failed              bool                         `json:"failed"`
	Tie                 *Tie                         `json:"tie,omitempty"`
	AsOf                time.Time                    `json:"asOf"`
	ValidAt             time.Time                    `json:"validAt"`
	RunID               string                       `json:"runId,omitempty"`
	HierarchySnapshotID string                       `json:"hierarchySnapshotId,omitempty"`
	CorrelationID       string                       `json:"correlationId,omitempty"`
	Traceparent         string                       `json:"traceparent,omitempty"`
	Trace               *engine.Trace                `json:"trace,omitempty"`
	LibVersion          string                       `json:"libVersion"`
}

// Evaluated explains how one candidate participated in resolution.
type Evaluated struct {
	RuleKey        rule.RuleKey         `json:"ruleKey"`
	RuleVersion    string               `json:"ruleVersion"`
	AnchorNodeID   string               `json:"anchorNodeId,omitempty"`
	Anchor         rule.Anchor          `json:"-"`
	Order          int                  `json:"order"`
	Depth          int                  `json:"depth"`
	OnPath         bool                 `json:"onPath"`
	FiltersMatched bool                 `json:"filtersMatched"`
	Winner         bool                 `json:"winner"`
	EliminatedBy   resolve.EliminatedBy `json:"eliminatedBy,omitempty"`
	Reason         string               `json:"reason,omitempty"`
}

// FactResolved explains one fact used by evaluation without recording its value.
type FactResolved struct {
	Field             string    `json:"field"`
	Kind              fact.Kind `json:"kind"`
	FactSourceVersion string    `json:"factSourceVersion,omitempty"`
	Dataset           string    `json:"dataset,omitempty"`
	RowsScanned       int       `json:"rowsScanned,omitempty"`
	ElapsedMS         int       `json:"elapsedMs,omitempty"`
	Cached            bool      `json:"cached"`
	FromRequest       bool      `json:"fromRequest"`
}

// Tie describes candidates that remain indistinguishable after every tie-break.
type Tie struct {
	Candidates []rule.RuleKeyVersion `json:"candidates"`
	Order      int                   `json:"order"`
	Depth      int                   `json:"depth"`
	Message    string                `json:"message"`
}

// ResolvedAttribute records a hierarchy-derived value and its source node.
type ResolvedAttribute struct {
	Value        string `json:"value"`
	SourceNodeID string `json:"sourceNodeId"`
}
