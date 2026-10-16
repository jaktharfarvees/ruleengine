package rule

// RuleLogic is an ordered decision table.
type RuleLogic struct {
	HitPolicy HitPolicy     `json:"hitPolicy"`
	Rows      []DecisionRow `json:"rows"`
}

// DecisionRow is one condition and output row.
type DecisionRow struct {
	ID       string            `json:"id"`
	When     map[string]string `json:"when"`
	Then     map[string]any    `json:"then"`
	Note     string            `json:"note,omitempty"`
	Priority int               `json:"priority,omitempty"`
}

// HitPolicy controls how matching rows produce output.
type HitPolicy string

const (
	// HitFirst uses the first matching row.
	HitFirst HitPolicy = "first"
	// HitPriority uses the highest-priority matching row.
	HitPriority HitPolicy = "priority"
	// HitCollect merges every matching row in authored order.
	HitCollect HitPolicy = "collect"
)
