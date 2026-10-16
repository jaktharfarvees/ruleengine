package hierarchy

// Type describes one tenant-defined hierarchy type and its semantic order.
type Type struct {
	TypeID      string `json:"typeId"`
	TypeName    string `json:"typeName"`
	Order       int    `json:"order"`
	Tenant      string `json:"tenant"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
}

// Node is one organization hierarchy node.
type Node struct {
	NodeID       string `json:"nodeId"`
	Code         string `json:"code,omitempty"`
	Tenant       string `json:"tenant"`
	TypeID       string `json:"typeId"`
	Name         string `json:"name"`
	ParentNodeID string `json:"parentNodeId,omitempty"`
	Status       string `json:"status"`
	Country      string `json:"country,omitempty"`
	Currency     string `json:"currency,omitempty"`
	Locale       string `json:"locale,omitempty"`
	Timezone     string `json:"timezone,omitempty"`
}

// PathEntry is one node on a root-to-leaf subject path.
type PathEntry struct {
	NodeID   string `json:"nodeId"`
	Name     string `json:"name"`
	TypeID   string `json:"typeId"`
	TypeName string `json:"typeName"`
	Order    int    `json:"order"`
	Depth    int    `json:"depth"`
}

// DerivedPrecedence is the read-only precedence obtained from an anchor.
type DerivedPrecedence struct {
	Order               int    `json:"order"`
	Depth               int    `json:"depth"`
	TypeID              string `json:"typeId"`
	TypeName            string `json:"typeName"`
	NodeName            string `json:"nodeName"`
	NodeID              string `json:"nodeId"`
	IsEmployeeException bool   `json:"isEmployeeException"`
}
