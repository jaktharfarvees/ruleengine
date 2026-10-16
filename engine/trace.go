package engine

// Trace explains every evaluated decision-table row.
type Trace struct {
	Rows []RowTrace `json:"rows"`
}

// RowTrace explains one evaluated row.
type RowTrace struct {
	RowID      string           `json:"rowId"`
	Matched    bool             `json:"matched"`
	Conditions []ConditionTrace `json:"conditions"`
}

// ConditionTrace explains one evaluated condition.
type ConditionTrace struct {
	Field   string `json:"field"`
	Expr    string `json:"expr"`
	Actual  any    `json:"actual"`
	Matched bool   `json:"matched"`
}
