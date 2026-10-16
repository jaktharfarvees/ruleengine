package rule

// Scope limits a rule to exactly one anchor and optional subject filters.
type Scope struct {
	Anchor  Anchor            `json:"anchor"`
	Filters []FilterCondition `json:"filters,omitempty"`
}

// Anchor identifies a hierarchy node or one employee exception.
type Anchor struct {
	Kind       AnchorKind `json:"kind,omitempty"`
	NodeID     string     `json:"nodeId,omitempty"`
	EmployeeID string     `json:"employeeId,omitempty"`
	Reason     string     `json:"reason,omitempty"`
}

// AnchorKind identifies the supported anchor forms.
type AnchorKind string

const (
	// AnchorNode anchors a rule to a hierarchy subtree.
	AnchorNode AnchorKind = "node"
	// AnchorEmployee anchors an explicit employee exception.
	AnchorEmployee AnchorKind = "employee"
)

// FilterCondition narrows an anchored rule by a subject attribute.
type FilterCondition struct {
	Attribute string         `json:"attribute"`
	Op        FilterOperator `json:"op"`
	Value     any            `json:"value,omitempty"`
}

// FilterOperator identifies a scope-filter operation.
type FilterOperator string

const (
	// FilterEquals requires equality.
	FilterEquals FilterOperator = "equals"
	// FilterIn requires membership in a set.
	FilterIn FilterOperator = "in"
	// FilterNotIn requires absence from a set.
	FilterNotIn FilterOperator = "notIn"
	// FilterRange requires inclusive range membership.
	FilterRange FilterOperator = "range"
	// FilterExists requires the attribute to be present.
	FilterExists FilterOperator = "exists"
	// FilterNotExists requires the attribute to be absent.
	FilterNotExists FilterOperator = "notExists"
)
