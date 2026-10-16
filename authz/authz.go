// Package authz answers one question: may this user author a rule here?
//
// Tenants author their own rules, scoped by hierarchy subtree grants (D18). A
// grant at a node covers that node and everything beneath it, and nothing
// above or beside it. The Rule Service enforces this when a rule is published
// and Rule Studio uses it to scope the node picker, so both reach the same
// answer from the same code rather than two implementations drifting apart.
//
// The library performs no authorisation at evaluation time: a published rule
// is already authorised, and re-checking it per decision would put an
// authorisation question on the payroll hot path. Nothing in this package is
// called from Evaluate.
package authz

import (
	"sort"
	"time"

	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/rule"
)

// GrantRole is what a grant permits its holder to do.
type GrantRole string

const (
	// RoleTenantAdmin may author, publish and administer anywhere in its
	// granted subtree, including managing other grants.
	RoleTenantAdmin GrantRole = "tenant-admin"
	// RolePublisher may publish a rule in its granted subtree, and author one.
	RolePublisher GrantRole = "rule-publisher"
	// RoleAuthor may draft a rule in its granted subtree but not publish it.
	RoleAuthor GrantRole = "rule-author"
	// RoleAuditor may read rules and decisions in its granted subtree.
	RoleAuditor GrantRole = "auditor"
)

// Valid reports whether r is one of the defined roles.
func (r GrantRole) Valid() bool {
	switch r {
	case RoleTenantAdmin, RolePublisher, RoleAuthor, RoleAuditor:
		return true
	default:
		return false
	}
}

// satisfies reports whether holding r is enough to act as need.
//
// The roles are ranked rather than equal: an admin can do a publisher's work
// and a publisher an author's. An auditor is outside that ladder — read access
// never implies write access, however senior the reader.
func (r GrantRole) satisfies(need GrantRole) bool {
	if r == need {
		return true
	}
	switch need {
	case RoleAuthor:
		return r == RolePublisher || r == RoleTenantAdmin
	case RolePublisher:
		return r == RoleTenantAdmin
	default:
		return false
	}
}

// RoleGrant gives one user a role over one node's subtree.
type RoleGrant struct {
	GrantID   string    `json:"grantId"`
	UserID    string    `json:"userId"`
	Tenant    string    `json:"tenant"`
	Role      GrantRole `json:"role"`
	NodeID    string    `json:"nodeId"`
	GrantedBy string    `json:"grantedBy"`
	GrantedAt time.Time `json:"grantedAt"`
}

// IsWithinAuthority reports whether grants permit authoring a rule at anchor.
//
// A node anchor is permitted when a granted node lies on the anchor's own path
// to the root: that is what makes a grant cover a subtree, because every node
// beneath the granted one has it as an ancestor. A grant deeper than the anchor
// does not permit it — authority flows downward, so an author granted one
// branch cannot write a rule that also governs its siblings.
//
// An employee anchor is an exception to a tenant's whole hierarchy and is
// permitted only to a tenant admin, because the employee's own node is not
// known here and an exception is the one rule that can override every node.
func IsWithinAuthority(
	anchor rule.Anchor, grants []RoleGrant, need GrantRole, snapshot *hierarchy.Snapshot, tenant string,
) bool {
	if tenant == "" || !need.Valid() || snapshot == nil {
		return false
	}
	switch anchor.Kind {
	case rule.AnchorEmployee:
		if anchor.EmployeeID == "" {
			return false
		}
		for _, grant := range grants {
			if grant.Tenant == tenant && grant.Role == RoleTenantAdmin && grant.Role.satisfies(need) {
				return true
			}
		}
		return false
	case rule.AnchorNode:
		if anchor.NodeID == "" {
			return false
		}
	default:
		return false
	}

	path, err := snapshot.Path(tenant, anchor.NodeID)
	if err != nil {
		// An anchor at a node this tenant does not have is refused rather than
		// allowed: an unknown node cannot be inside anybody's subtree.
		return false
	}
	for _, grant := range grants {
		if grant.Tenant != tenant || !grant.Role.satisfies(need) {
			continue
		}
		for _, entry := range path {
			if entry.NodeID == grant.NodeID {
				return true
			}
		}
	}
	return false
}

// EffectiveNodes returns every node a holder of grants may author at, sorted.
//
// This is what scopes Rule Studio's node picker. The full tenant tree is never
// sent to an author who may not see all of it, so the picker is built from
// this list server-side rather than filtered in the browser.
func EffectiveNodes(
	grants []RoleGrant, need GrantRole, snapshot *hierarchy.Snapshot, tenant string,
) []string {
	if tenant == "" || !need.Valid() || snapshot == nil {
		return nil
	}
	granted := make(map[string]struct{}, len(grants))
	for _, grant := range grants {
		if grant.Tenant == tenant && grant.Role.satisfies(need) && grant.NodeID != "" {
			granted[grant.NodeID] = struct{}{}
		}
	}
	if len(granted) == 0 {
		return nil
	}
	permitted := make(map[string]struct{}, len(granted))
	for _, node := range snapshot.Nodes(tenant) {
		path, err := snapshot.Path(tenant, node.NodeID)
		if err != nil {
			continue
		}
		for _, entry := range path {
			if _, isGranted := granted[entry.NodeID]; isGranted {
				permitted[node.NodeID] = struct{}{}
				break
			}
		}
	}
	nodes := make([]string, 0, len(permitted))
	for node := range permitted {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	return nodes
}
