package hierarchy

import (
	"fmt"
	"math"
	"strings"

	"github.com/arrowhr/ruleengine/rule"
)

// EmployeeExceptionOrder is reserved for employee anchors and can never be a
// real hierarchy-type order.
const EmployeeExceptionOrder = math.MaxInt32

// DerivePrecedence computes read-only precedence from an authored anchor.
func (s *Snapshot) DerivePrecedence(tenant string, anchor rule.Anchor) (DerivedPrecedence, error) {
	switch anchor.Kind {
	case rule.AnchorEmployee:
		if anchor.EmployeeID == "" {
			return DerivedPrecedence{}, fmt.Errorf("employee anchor has no employee identifier")
		}
		return DerivedPrecedence{
			Order: EmployeeExceptionOrder, Depth: EmployeeExceptionOrder,
			IsEmployeeException: true,
		}, nil
	case rule.AnchorNode:
		node, exists := s.Node(tenant, anchor.NodeID)
		if !exists {
			return DerivedPrecedence{}, fmt.Errorf("tenant %q anchor node %q is unknown", tenant, anchor.NodeID)
		}
		if !strings.EqualFold(node.Status, "ACTIVE") {
			return DerivedPrecedence{}, fmt.Errorf("tenant %q anchor node %q is inactive", tenant, anchor.NodeID)
		}
		hierarchyType, exists := s.Type(tenant, node.TypeID)
		if !exists {
			return DerivedPrecedence{}, fmt.Errorf("tenant %q anchor type %q is unknown", tenant, node.TypeID)
		}
		path, err := s.Path(tenant, node.NodeID)
		if err != nil {
			return DerivedPrecedence{}, err
		}
		return DerivedPrecedence{
			Order: hierarchyType.Order, Depth: path[len(path)-1].Depth,
			TypeID: hierarchyType.TypeID, TypeName: hierarchyType.TypeName,
			NodeName: node.Name, NodeID: node.NodeID,
		}, nil
	default:
		return DerivedPrecedence{}, fmt.Errorf("unsupported anchor kind %q", anchor.Kind)
	}
}
