package authz_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/authz"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
)

const tenant = "RWB"

func rwb(t *testing.T) *hierarchy.Snapshot {
	t.Helper()
	snapshot, err := ruletest.LoadRWB()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func grant(role authz.GrantRole, nodeID string) authz.RoleGrant {
	return authz.RoleGrant{
		GrantID: "g-" + nodeID, UserID: "u1", Tenant: tenant, Role: role, NodeID: nodeID,
		GrantedBy: "admin", GrantedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func nodeAnchor(nodeID string) rule.Anchor {
	return rule.Anchor{Kind: rule.AnchorNode, NodeID: nodeID}
}

// TestC002GrantCoversItsSubtreeOnly is the grant case from the plan. An author
// granted the Canadian company C002 may write rules for C002 and everything
// beneath it, and nowhere else: not at the group above it, and not in a
// sibling company's branch.
//
// The RWB fixture makes this worth checking, because BR001 sits under C002 but
// declares a Sri Lankan timezone — so subtree membership has to come from the
// hierarchy, not from anything that looks like the node's own attributes.
func TestC002GrantCoversItsSubtreeOnly(t *testing.T) {
	snapshot := rwb(t)
	grants := []authz.RoleGrant{grant(authz.RoleAuthor, "C002")}

	permitted := []string{
		"C002",  // the granted node itself
		"D002",  // Engineering, directly beneath it
		"D003",  // Finance, beneath it
		"BR001", // Colombo 03, two levels beneath it
		"BR002", // Kandy
		"BR003", // Galle, under D003
	}
	for _, nodeID := range permitted {
		if !authz.IsWithinAuthority(nodeAnchor(nodeID), grants, authz.RoleAuthor, snapshot, tenant) {
			t.Errorf("a C002 grant should permit anchoring at %s", nodeID)
		}
	}

	refused := []string{
		"G001",  // the group above the grant: authority flows downward only
		"C001",  // a sibling company
		"C003",  // another sibling
		"C004",  // another sibling
		"D001",  // Accounting, under the sibling C001
		"D004",  // Operations, under C004
		"BR004", // Colombo 07, under D004
	}
	for _, nodeID := range refused {
		if authz.IsWithinAuthority(nodeAnchor(nodeID), grants, authz.RoleAuthor, snapshot, tenant) {
			t.Errorf("a C002 grant must not permit anchoring at %s", nodeID)
		}
	}
}

// TestEffectiveNodesScopesTheNodePicker pins what Rule Studio sends an author:
// exactly their granted subtrees, so the full tenant tree never reaches
// somebody not permitted to see it.
func TestEffectiveNodesScopesTheNodePicker(t *testing.T) {
	snapshot := rwb(t)
	nodes := authz.EffectiveNodes([]authz.RoleGrant{grant(authz.RoleAuthor, "C002")}, authz.RoleAuthor, snapshot, tenant)
	want := []string{"BR001", "BR002", "BR003", "C002", "D002", "D003"}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("EffectiveNodes = %v, want %v", nodes, want)
	}

	// Two grants union rather than intersect.
	nodes = authz.EffectiveNodes([]authz.RoleGrant{
		grant(authz.RoleAuthor, "D002"), grant(authz.RoleAuthor, "C004"),
	}, authz.RoleAuthor, snapshot, tenant)
	want = []string{"BR001", "BR002", "BR004", "C004", "D002", "D004"}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("EffectiveNodes for two grants = %v, want %v", nodes, want)
	}

	// A grant at the root covers the whole tenant.
	nodes = authz.EffectiveNodes([]authz.RoleGrant{grant(authz.RoleTenantAdmin, "G001")}, authz.RoleAuthor, snapshot, tenant)
	if len(nodes) != len(snapshot.Nodes(tenant)) {
		t.Fatalf("a root grant covered %d of %d nodes", len(nodes), len(snapshot.Nodes(tenant)))
	}
}

// TestRolesAreRankedExceptAuditor pins that an admin can do a publisher's work
// and a publisher an author's, while read access never implies write access.
func TestRolesAreRankedExceptAuditor(t *testing.T) {
	snapshot := rwb(t)
	anchor := nodeAnchor("D002")
	tests := []struct {
		held, need authz.GrantRole
		want       bool
	}{
		{authz.RoleTenantAdmin, authz.RoleAuthor, true},
		{authz.RoleTenantAdmin, authz.RolePublisher, true},
		{authz.RoleTenantAdmin, authz.RoleTenantAdmin, true},
		{authz.RolePublisher, authz.RoleAuthor, true},
		{authz.RolePublisher, authz.RolePublisher, true},
		{authz.RolePublisher, authz.RoleTenantAdmin, false},
		{authz.RoleAuthor, authz.RoleAuthor, true},
		{authz.RoleAuthor, authz.RolePublisher, false},
		{authz.RoleAuthor, authz.RoleTenantAdmin, false},
		// An auditor reads; it never implies authority to write.
		{authz.RoleAuditor, authz.RoleAuditor, true},
		{authz.RoleAuditor, authz.RoleAuthor, false},
		{authz.RoleAuditor, authz.RolePublisher, false},
		{authz.RoleTenantAdmin, authz.RoleAuditor, false},
	}
	for _, test := range tests {
		got := authz.IsWithinAuthority(anchor, []authz.RoleGrant{grant(test.held, "D002")}, test.need, snapshot, tenant)
		if got != test.want {
			t.Errorf("holding %s, needing %s = %t, want %t", test.held, test.need, got, test.want)
		}
	}
}

// TestRetiredEmployeeAnchorHasNoAuthority ensures no role can revive the
// superseded employee-anchor model through the authorisation layer.
func TestRetiredEmployeeAnchorHasNoAuthority(t *testing.T) {
	snapshot := rwb(t)
	retired := rule.Anchor{Kind: rule.AnchorKind("employee")}
	for _, role := range []authz.GrantRole{authz.RoleTenantAdmin, authz.RolePublisher, authz.RoleAuthor, authz.RoleAuditor} {
		if authz.IsWithinAuthority(retired, []authz.RoleGrant{grant(role, "G001")},
			authz.RoleAuthor, snapshot, tenant) {
			t.Errorf("%s was allowed to author a retired employee anchor", role)
		}
	}
}

// TestAuthorityIsRefusedRatherThanAssumed pins the closed cases: a grant in
// another tenant, an unknown node, a malformed anchor or an unknown role all
// refuse. Authority is never inferred from an absence.
func TestAuthorityIsRefusedRatherThanAssumed(t *testing.T) {
	snapshot := rwb(t)
	admin := grant(authz.RoleTenantAdmin, "G001")

	tests := []struct {
		name     string
		anchor   rule.Anchor
		grants   []authz.RoleGrant
		need     authz.GrantRole
		snapshot *hierarchy.Snapshot
		tenant   string
	}{
		{"no grants", nodeAnchor("D002"), nil, authz.RoleAuthor, snapshot, tenant},
		{"another tenant's grant", nodeAnchor("D002"),
			[]authz.RoleGrant{{Tenant: "EARROW", Role: authz.RoleTenantAdmin, NodeID: "G001"}},
			authz.RoleAuthor, snapshot, tenant},
		{"unknown node", nodeAnchor("NOPE"), []authz.RoleGrant{admin}, authz.RoleAuthor, snapshot, tenant},
		{"empty node anchor", nodeAnchor(""), []authz.RoleGrant{admin}, authz.RoleAuthor, snapshot, tenant},
		{"unknown anchor kind", rule.Anchor{Kind: "region", NodeID: "D002"},
			[]authz.RoleGrant{admin}, authz.RoleAuthor, snapshot, tenant},
		{"unknown role needed", nodeAnchor("D002"), []authz.RoleGrant{admin}, "superuser", snapshot, tenant},
		{"grant with an unknown role", nodeAnchor("D002"),
			[]authz.RoleGrant{{Tenant: tenant, Role: "superuser", NodeID: "G001"}},
			authz.RoleAuthor, snapshot, tenant},
		{"no tenant", nodeAnchor("D002"), []authz.RoleGrant{admin}, authz.RoleAuthor, snapshot, ""},
		{"no hierarchy", nodeAnchor("D002"), []authz.RoleGrant{admin}, authz.RoleAuthor, nil, tenant},
		{"grant with no node", nodeAnchor("D002"),
			[]authz.RoleGrant{{Tenant: tenant, Role: authz.RoleTenantAdmin}}, authz.RoleAuthor, snapshot, tenant},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if authz.IsWithinAuthority(test.anchor, test.grants, test.need, test.snapshot, test.tenant) {
				t.Fatal("authority was granted where it should have been refused")
			}
		})
	}

	// EffectiveNodes answers about grants rather than about one anchor, so the
	// grant-level refusals apply to it too: it must return nothing rather than
	// everything, or a broken call would widen a node picker.
	for _, test := range tests {
		switch test.name {
		case "unknown node", "empty node anchor", "unknown anchor kind":
			// These describe the anchor, which EffectiveNodes never sees.
			continue
		}
		if nodes := authz.EffectiveNodes(test.grants, test.need, test.snapshot, test.tenant); len(nodes) != 0 {
			t.Errorf("%s: EffectiveNodes returned %v", test.name, nodes)
		}
	}
}

// TestGrantRoleValidity covers the closed role set.
func TestGrantRoleValidity(t *testing.T) {
	for _, role := range []authz.GrantRole{
		authz.RoleTenantAdmin, authz.RolePublisher, authz.RoleAuthor, authz.RoleAuditor,
	} {
		if !role.Valid() {
			t.Errorf("%q should be valid", role)
		}
	}
	if authz.GrantRole("superuser").Valid() || authz.GrantRole("").Valid() {
		t.Fatal("an unknown role was accepted")
	}
}

// TestNodesEnumerationIsScopedAndSorted pins the hierarchy enumeration that
// EffectiveNodes is built on: one tenant's nodes only, in a stable order.
func TestNodesEnumerationIsScopedAndSorted(t *testing.T) {
	snapshot, err := ruletest.LoadHierarchy("earrow-shallow.json")
	if err != nil {
		t.Fatal(err)
	}
	nodes := snapshot.Nodes("Earrow")
	if len(nodes) == 0 {
		t.Fatal("no nodes for EARROW")
	}
	for index, node := range nodes {
		if node.Tenant != "Earrow" {
			t.Fatalf("Nodes returned a node for %q", node.Tenant)
		}
		if index > 0 && nodes[index-1].NodeID >= node.NodeID {
			t.Fatalf("Nodes are not sorted: %q then %q", nodes[index-1].NodeID, node.NodeID)
		}
	}
	if snapshot.Nodes("RWB") != nil {
		t.Fatal("Nodes returned another tenant's nodes")
	}
	if snapshot.Nodes("") != nil {
		t.Fatal("Nodes answered an empty tenant")
	}
	var nilSnapshot *hierarchy.Snapshot
	if nilSnapshot.Nodes("Earrow") != nil {
		t.Fatal("a nil snapshot returned nodes")
	}
}
