package engine

import (
	"context"

	"github.com/arrowhr/ruleengine/rule"
)

// Engine evaluates already-resolved rule logic without I/O or clock access.
//
// The logic passed to Evaluate shares its rows and maps with the published rule
// snapshot, which concurrent decisions read at the same time. An Engine must
// treat it as read-only and copy anything it returns in Output.
type Engine interface {
	Name() string
	Evaluate(ctx context.Context, logic rule.RuleLogic, input Input) (Output, error)
	Capabilities() Capabilities
}

// Input contains the facts available to rule logic.
type Input struct {
	Facts map[string]any `json:"facts"`
	Trace bool           `json:"trace"`
}

// Output contains the engine result and optional explanation trace.
type Output struct {
	Result      map[string]any `json:"result"`
	MatchedRows []string       `json:"matchedRows"`
	Trace       *Trace         `json:"trace,omitempty"`
}

// Capabilities truthfully describes the features supported by an Engine.
type Capabilities struct {
	HitPolicies []rule.HitPolicy `json:"hitPolicies"`
	Operators   []string         `json:"operators"`
	FieldRefs   bool             `json:"fieldRefs"`
	Arithmetic  bool             `json:"arithmetic"`
	Trace       bool             `json:"trace"`
	Decimal     bool             `json:"decimal"`
}
