package hierarchy

import (
	"fmt"
	"sort"
	"strings"
)

type key struct {
	tenant string
	id     string
}

// Snapshot is an immutable hierarchy with every node path precomputed.
// Snapshot is safe for concurrent readers.
type Snapshot struct {
	nodes    map[key]Node
	types    map[key]Type
	paths    map[key][]PathEntry
	maxOrder map[string]int
	counts   map[string]int
}

// NewSnapshot validates types and nodes and precomputes deepest-first paths.
func NewSnapshot(types []Type, nodes []Node) (*Snapshot, error) {
	snapshot := &Snapshot{
		nodes:    make(map[key]Node, len(nodes)),
		types:    make(map[key]Type, len(types)),
		paths:    make(map[key][]PathEntry, len(nodes)),
		maxOrder: make(map[string]int),
		counts:   make(map[string]int),
	}
	orders := make(map[string]map[int]string)
	for index, hierarchyType := range types {
		if hierarchyType.Tenant == "" || hierarchyType.TypeID == "" || hierarchyType.TypeName == "" || hierarchyType.Order < 0 {
			return nil, fmt.Errorf("hierarchy type at index %d is invalid", index)
		}
		typeKey := key{tenant: hierarchyType.Tenant, id: hierarchyType.TypeID}
		if _, exists := snapshot.types[typeKey]; exists {
			return nil, fmt.Errorf("tenant %q has duplicate hierarchy type %q", hierarchyType.Tenant, hierarchyType.TypeID)
		}
		if orders[hierarchyType.Tenant] == nil {
			orders[hierarchyType.Tenant] = make(map[int]string)
		}
		if existing, exists := orders[hierarchyType.Tenant][hierarchyType.Order]; exists {
			return nil, fmt.Errorf("tenant %q hierarchy types %q and %q share order %d", hierarchyType.Tenant, existing, hierarchyType.TypeID, hierarchyType.Order)
		}
		orders[hierarchyType.Tenant][hierarchyType.Order] = hierarchyType.TypeID
		snapshot.types[typeKey] = hierarchyType
		if current, exists := snapshot.maxOrder[hierarchyType.Tenant]; !exists || hierarchyType.Order > current {
			snapshot.maxOrder[hierarchyType.Tenant] = hierarchyType.Order
		}
	}

	roots := make(map[string][]string)
	for index, node := range nodes {
		if node.Tenant == "" || node.NodeID == "" || node.TypeID == "" || node.Name == "" || node.Status == "" {
			return nil, fmt.Errorf("hierarchy node at index %d is invalid", index)
		}
		nodeKey := key{tenant: node.Tenant, id: node.NodeID}
		if _, exists := snapshot.nodes[nodeKey]; exists {
			return nil, fmt.Errorf("tenant %q has duplicate hierarchy node %q", node.Tenant, node.NodeID)
		}
		if _, exists := snapshot.types[key{tenant: node.Tenant, id: node.TypeID}]; !exists {
			return nil, fmt.Errorf("tenant %q node %q references unknown hierarchy type %q", node.Tenant, node.NodeID, node.TypeID)
		}
		snapshot.nodes[nodeKey] = node
		snapshot.counts[node.Tenant]++
		if node.ParentNodeID == "" {
			roots[node.Tenant] = append(roots[node.Tenant], node.NodeID)
		}
	}

	for _, node := range snapshot.nodes {
		if node.ParentNodeID == "" {
			continue
		}
		if _, exists := snapshot.nodes[key{tenant: node.Tenant, id: node.ParentNodeID}]; !exists {
			return nil, fmt.Errorf("tenant %q node %q references missing parent %q", node.Tenant, node.NodeID, node.ParentNodeID)
		}
	}
	if err := snapshot.validateAcyclic(); err != nil {
		return nil, err
	}
	for tenant := range snapshot.counts {
		if len(roots[tenant]) != 1 {
			sort.Strings(roots[tenant])
			return nil, fmt.Errorf("tenant %q must have exactly one root; found %d (%s)", tenant, len(roots[tenant]), strings.Join(roots[tenant], ", "))
		}
	}
	for nodeKey := range snapshot.nodes {
		snapshot.paths[nodeKey] = snapshot.computePath(nodeKey)
	}
	return snapshot, nil
}

func (s *Snapshot) validateAcyclic() error {
	state := make(map[key]uint8, len(s.nodes))
	for start := range s.nodes {
		if state[start] == 2 {
			continue
		}
		chain := make([]key, 0, 8)
		positions := make(map[key]int)
		current := start
		for {
			if state[current] == 2 {
				break
			}
			if position, exists := positions[current]; exists {
				cycle := make([]string, 0, len(chain)-position+1)
				for _, item := range chain[position:] {
					cycle = append(cycle, item.id)
				}
				cycle = append(cycle, current.id)
				return fmt.Errorf("tenant %q hierarchy cycle: %s", current.tenant, strings.Join(cycle, " -> "))
			}
			positions[current] = len(chain)
			chain = append(chain, current)
			node := s.nodes[current]
			if node.ParentNodeID == "" {
				break
			}
			current = key{tenant: node.Tenant, id: node.ParentNodeID}
		}
		for _, item := range chain {
			state[item] = 2
		}
	}
	return nil
}

func (s *Snapshot) computePath(nodeKey key) []PathEntry {
	lineage := make([]Node, 0, 8)
	current := s.nodes[nodeKey]
	for {
		lineage = append(lineage, current)
		if current.ParentNodeID == "" {
			break
		}
		current = s.nodes[key{tenant: current.Tenant, id: current.ParentNodeID}]
	}
	path := make([]PathEntry, len(lineage))
	for index, node := range lineage {
		hierarchyType := s.types[key{tenant: node.Tenant, id: node.TypeID}]
		path[index] = PathEntry{
			NodeID: node.NodeID, Name: node.Name, TypeID: node.TypeID,
			TypeName: hierarchyType.TypeName, Order: hierarchyType.Order,
			Depth: len(lineage) - index - 1,
		}
	}
	return path
}

// Empty reports whether the snapshot has no nodes for tenant. An empty tenant
// checks the entire snapshot.
func (s *Snapshot) Empty(tenant string) bool {
	if s == nil {
		return true
	}
	if tenant == "" {
		return len(s.nodes) == 0
	}
	return s.counts[tenant] == 0
}

// Path returns the precomputed deepest-first path for a node without allocating.
// The returned slice is an immutable view and must not be modified by callers.
func (s *Snapshot) Path(tenant, nodeID string) ([]PathEntry, error) {
	if s == nil {
		return nil, fmt.Errorf("hierarchy snapshot is unavailable")
	}
	result, exists := s.paths[key{tenant: tenant, id: nodeID}]
	if !exists {
		return nil, fmt.Errorf("tenant %q hierarchy node %q is unknown", tenant, nodeID)
	}
	return result, nil
}

// Node returns a hierarchy node by tenant and identifier.
func (s *Snapshot) Node(tenant, nodeID string) (Node, bool) {
	if s == nil {
		return Node{}, false
	}
	node, exists := s.nodes[key{tenant: tenant, id: nodeID}]
	return node, exists
}

// Nodes returns every node belonging to tenant, ordered by identifier.
//
// Enumeration exists for authoring rather than evaluation: scoping a node
// picker to an author's granted subtrees needs the tenant's nodes, while
// resolution only ever walks one subject's path. The slice is a copy.
func (s *Snapshot) Nodes(tenant string) []Node {
	if s == nil || tenant == "" {
		return nil
	}
	count := s.counts[tenant]
	if count == 0 {
		// A tenant with no nodes answers nil rather than an empty slice, so a
		// caller cannot tell "no nodes" from "unknown tenant" by the type and
		// then act on the difference.
		return nil
	}
	nodes := make([]Node, 0, count)
	for identity, node := range s.nodes {
		if identity.tenant == tenant {
			nodes = append(nodes, node)
		}
	}
	sort.Slice(nodes, func(left, right int) bool { return nodes[left].NodeID < nodes[right].NodeID })
	return nodes
}

// Type returns a hierarchy type by tenant and identifier.
func (s *Snapshot) Type(tenant, typeID string) (Type, bool) {
	if s == nil {
		return Type{}, false
	}
	hierarchyType, exists := s.types[key{tenant: tenant, id: typeID}]
	return hierarchyType, exists
}

// MaxOrder returns the largest hierarchy-type order for tenant.
func (s *Snapshot) MaxOrder(tenant string) int {
	if s == nil {
		return 0
	}
	return s.maxOrder[tenant]
}
