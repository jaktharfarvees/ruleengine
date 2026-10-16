package ruletest

import (
	"fmt"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/store"
)

// defaultValidFrom is when fixture artifacts take effect. It is early enough
// that a test need not think about effective dating unless it is testing it.
var defaultValidFrom = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// FixtureBuilder assembles a store bundle for a test.
//
// Writing a rule by hand means twenty fields of which three matter, so a test
// that does it reads as noise and a reader cannot see which part is the point.
// This fills in everything a rule needs to be valid and lets a test state only
// what it is about.
//
// It is a builder rather than a set of helpers because the artifacts have to
// agree: a rule's declared fields must be in the registry and a dataset field
// must name a published manifest, or the bundle will not load.
type FixtureBuilder struct {
	tenant    string
	rules     []rule.RuleDefinition
	fields    []registry.FieldDefinition
	manifests []dataset.Manifest
	types     []hierarchy.Type
	nodes     []hierarchy.Node
	problems  []error
}

// NewFixtureBuilder starts an empty bundle for one tenant.
func NewFixtureBuilder(tenant string) *FixtureBuilder {
	return &FixtureBuilder{tenant: tenant}
}

// WithRWBHierarchy adopts the canonical RWB hierarchy: a group, four
// companies, four departments and four branches, with BR001 deliberately
// declaring a Sri Lankan timezone beneath the Canadian company.
func (b *FixtureBuilder) WithRWBHierarchy() *FixtureBuilder {
	fixture, err := LoadHierarchyFixture("rwb.json")
	if err != nil {
		b.problems = append(b.problems, err)
		return b
	}
	b.types, b.nodes = fixture.Types, fixture.Nodes
	return b
}

// WithHierarchy adopts a hierarchy built by HierarchyBuilder.
func (b *FixtureBuilder) WithHierarchy(builder *HierarchyBuilder) *FixtureBuilder {
	types, nodes := builder.Build()
	b.types, b.nodes = types, nodes
	return b
}

// WithRegistry adopts the committed field registry for one or more domains, so
// a test can use the real field contracts rather than inventing them.
func (b *FixtureBuilder) WithRegistry(domains ...string) *FixtureBuilder {
	fields, err := LoadRegistryFields(domains...)
	if err != nil {
		b.problems = append(b.problems, err)
		return b
	}
	b.fields = append(b.fields, fields...)
	return b
}

// WithManifests adopts committed dataset manifests by dataset name.
func (b *FixtureBuilder) WithManifests(names ...string) *FixtureBuilder {
	for _, name := range names {
		manifest, err := LoadManifest(name)
		if err != nil {
			b.problems = append(b.problems, err)
			continue
		}
		b.manifests = append(b.manifests, manifest)
	}
	return b
}

// AddField adds a field definition as given, for a test about the registry
// itself.
func (b *FixtureBuilder) AddField(field registry.FieldDefinition) *FixtureBuilder {
	b.fields = append(b.fields, field)
	return b
}

// AddPassedField adds an input the caller supplies on the request.
func (b *FixtureBuilder) AddPassedField(name string, dataType registry.DataType) *FixtureBuilder {
	return b.AddField(registry.FieldDefinition{
		Name: name, Domain: "test", DataType: dataType, OwnerService: "test-service",
		Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactPassed},
	})
}

// AddOutputField adds a field a rule may produce.
func (b *FixtureBuilder) AddOutputField(name string, dataType registry.DataType) *FixtureBuilder {
	return b.AddField(registry.FieldDefinition{
		Name: name, Domain: "test", DataType: dataType, OwnerService: "test-service",
		Status: "active", Role: registry.RoleOutput,
	})
}

// AddSubjectAttribute adds an attribute a scope filter may narrow on.
func (b *FixtureBuilder) AddSubjectAttribute(name string, allowed ...string) *FixtureBuilder {
	field := registry.FieldDefinition{
		Name: name, Domain: "test", DataType: registry.DataString, OwnerService: "test-service",
		Status: "active", Role: registry.RoleSubjectAttribute,
	}
	if len(allowed) > 0 {
		field.DataType = registry.DataEnum
		field.AllowedValues = allowed
	}
	return b.AddField(field)
}

// AddRule adds a rule as given, for a test that needs an unusual one.
func (b *FixtureBuilder) AddRule(definition rule.RuleDefinition) *FixtureBuilder {
	b.rules = append(b.rules, definition)
	return b
}

// Rule returns a builder for one rule in this bundle.
//
// The key is "<ruleSet>:<name>", which is how a decision point names a rule,
// and the version defaults to 1.0.0.
func (b *FixtureBuilder) Rule(domain, ruleSet, name string) *RuleBuilder {
	return &RuleBuilder{
		bundle: b,
		definition: rule.RuleDefinition{
			RuleKey: rule.RuleKey(fmt.Sprintf("urn:arrowhr:rule:%s:%s:%s:v1", domain, ruleSet, name)),
			Domain:  domain, RuleSet: ruleSet, Name: name,
			Version: "1.0.0", ChangeType: rule.ChangeMajor, Status: rule.StatusActive,
			Tenant:       b.tenant,
			Effective:    rule.Effective{ValidFrom: defaultValidFrom},
			DecisionType: domain + "-" + ruleSet,
			EngineRef:    rule.EngineRef{Engine: "decisiontable"},
			Metadata: rule.Metadata{
				Owner: "fixture", CreatedBy: "ruletest", CreatedAt: defaultValidFrom,
			},
		},
	}
}

// Build returns the bundle, or the first problem found while assembling it.
func (b *FixtureBuilder) Build() (store.Bundle, error) {
	if len(b.problems) > 0 {
		return store.Bundle{}, b.problems[0]
	}
	return store.Bundle{
		Rules: b.rules, Fields: b.fields, Manifests: b.manifests,
		Hierarchy: store.HierarchyBundle{Types: b.types, Nodes: b.nodes},
		LoadedAt:  defaultValidFrom,
	}, nil
}

// MustBuild returns the bundle and fails the test if it could not be built.
func (b *FixtureBuilder) MustBuild(t TestingT) store.Bundle {
	if helper, ok := t.(interface{ Helper() }); ok {
		helper.Helper()
	}
	bundle, err := b.Build()
	if err != nil {
		t.Fatalf("build fixture bundle: %v", err)
	}
	return bundle
}

// TestingT is the slice of testing.TB the builders need, declared here so a
// caller is not forced to pass a whole *testing.T.
type TestingT interface {
	Fatalf(format string, args ...any)
}

// RuleBuilder assembles one rule. Call Add to put it in the bundle.
type RuleBuilder struct {
	bundle     *FixtureBuilder
	definition rule.RuleDefinition
}

// Version sets the rule's version. Versions are immutable, so two versions of
// one rule are two definitions sharing a key.
func (r *RuleBuilder) Version(version string) *RuleBuilder {
	r.definition.Version = version
	return r
}

// Status sets the publication status. Only active and scheduled are evaluable.
func (r *RuleBuilder) Status(status rule.Status) *RuleBuilder {
	r.definition.Status = status
	return r
}

// AnchoredAt anchors the rule to a hierarchy node, which is what gives it its
// precedence: the deepest anchor on the subject's path wins.
func (r *RuleBuilder) AnchoredAt(nodeID string) *RuleBuilder {
	r.definition.Scope.Anchor = rule.Anchor{Kind: rule.AnchorNode, NodeID: nodeID}
	return r
}

// ForEmployee anchors the rule to one employee as an approved exception, which
// outranks every node anchor.
func (r *RuleBuilder) ForEmployee(employeeID, reason string) *RuleBuilder {
	r.definition.Scope.Anchor = rule.Anchor{
		Kind: rule.AnchorEmployee, EmployeeID: employeeID, Reason: reason,
	}
	return r
}

// FilteredBy narrows the rule within its anchor. A filter never raises a rule
// above a deeper anchor.
func (r *RuleBuilder) FilteredBy(attribute string, operator rule.FilterOperator, value any) *RuleBuilder {
	r.definition.Scope.Filters = append(r.definition.Scope.Filters,
		rule.FilterCondition{Attribute: attribute, Op: operator, Value: value})
	return r
}

// EffectiveFrom sets when the rule takes effect.
func (r *RuleBuilder) EffectiveFrom(from time.Time) *RuleBuilder {
	r.definition.Effective.ValidFrom = from
	return r
}

// EffectiveUntil sets when the rule stops applying.
func (r *RuleBuilder) EffectiveUntil(until time.Time) *RuleBuilder {
	r.definition.Effective.ValidTo = &until
	return r
}

// TieBreak sets an explicit priority, which separates two rules that are
// otherwise equally specific at one anchor. Lower wins.
func (r *RuleBuilder) TieBreak(priority int) *RuleBuilder {
	r.definition.TieBreakPriority = &priority
	return r
}

// Needs declares the rule's inputs. Only these are fetched, and each must be
// in the registry.
func (r *RuleBuilder) Needs(fields ...string) *RuleBuilder {
	r.definition.DeclaredInputs = append(r.definition.DeclaredInputs, fields...)
	return r
}

// Produces declares the rule's outputs. Every one must be present in the
// result or the decision fails its own contract.
func (r *RuleBuilder) Produces(fields ...string) *RuleBuilder {
	r.definition.DeclaredOutputs = append(r.definition.DeclaredOutputs, fields...)
	return r
}

// DecisionType sets the output contract the result satisfies.
func (r *RuleBuilder) DecisionType(name string) *RuleBuilder {
	r.definition.DecisionType = name
	return r
}

// HitPolicy sets how matching rows produce output.
func (r *RuleBuilder) HitPolicy(policy rule.HitPolicy) *RuleBuilder {
	r.definition.Logic.HitPolicy = policy
	return r
}

// Row adds a decision row. An empty when is the catch-all.
func (r *RuleBuilder) Row(id string, when map[string]string, then map[string]any) *RuleBuilder {
	r.definition.Logic.Rows = append(r.definition.Logic.Rows,
		rule.DecisionRow{ID: id, When: when, Then: then})
	return r
}

// Add validates the rule and puts it in the bundle, returning the bundle
// builder so calls chain.
func (r *RuleBuilder) Add() *FixtureBuilder {
	if r.definition.Logic.HitPolicy == "" {
		r.definition.Logic.HitPolicy = rule.HitFirst
	}
	if r.definition.Scope.Anchor.Kind == "" {
		r.bundle.problems = append(r.bundle.problems,
			fmt.Errorf("rule %q has no anchor; every rule is anchored to a node or an employee", r.definition.RuleKey))
		return r.bundle
	}
	// Validated here so the mistake is reported where it was made, not as an
	// opaque bundle failure later.
	if err := r.definition.Validate(); err != nil {
		r.bundle.problems = append(r.bundle.problems,
			fmt.Errorf("rule %q: %w", r.definition.RuleKey, err))
		return r.bundle
	}
	r.bundle.rules = append(r.bundle.rules, r.definition)
	return r.bundle
}

// HierarchyBuilder assembles a tenant's org hierarchy.
//
// A tenant's hierarchy is whatever the tenant defines, so a test about
// precedence needs to be able to state one in a few lines: the levels in
// order, then the nodes and their parents.
type HierarchyBuilder struct {
	tenant string
	types  []hierarchy.Type
	nodes  []hierarchy.Node
}

// NewHierarchyBuilder starts an empty hierarchy for one tenant.
func NewHierarchyBuilder(tenant string) *HierarchyBuilder {
	return &HierarchyBuilder{tenant: tenant}
}

// Level declares a hierarchy type. Order is the semantic depth, counting from
// zero at the root, and is what resolution ranks by when two anchors sit at
// the same depth.
func (h *HierarchyBuilder) Level(typeID, typeName string, order int) *HierarchyBuilder {
	h.types = append(h.types, hierarchy.Type{
		TypeID: typeID, TypeName: typeName, Order: order,
		Tenant: h.tenant, Status: "ACTIVE",
	})
	return h
}

// Root adds the tenant's root node.
func (h *HierarchyBuilder) Root(nodeID, typeID, name string) *HierarchyBuilder {
	return h.Node(nodeID, typeID, name, "")
}

// Node adds a node beneath a parent. An empty parent makes it a root.
func (h *HierarchyBuilder) Node(nodeID, typeID, name, parentNodeID string) *HierarchyBuilder {
	h.nodes = append(h.nodes, hierarchy.Node{
		NodeID: nodeID, Code: nodeID, Tenant: h.tenant, TypeID: typeID,
		Name: name, ParentNodeID: parentNodeID, Status: "ACTIVE",
	})
	return h
}

// Inactive marks the last added node inactive, which makes it unusable as a
// subject's node and as an anchor.
func (h *HierarchyBuilder) Inactive() *HierarchyBuilder {
	if len(h.nodes) > 0 {
		h.nodes[len(h.nodes)-1].Status = "INACTIVE"
	}
	return h
}

// Declares sets the locale attributes on the last added node. A subject
// resolves each one from the nearest ancestor that declares it, independently,
// so a branch may declare its own timezone while its country comes from above.
func (h *HierarchyBuilder) Declares(country, currency, locale, timezone string) *HierarchyBuilder {
	if len(h.nodes) == 0 {
		return h
	}
	node := &h.nodes[len(h.nodes)-1]
	node.Country, node.Currency, node.Locale, node.Timezone = country, currency, locale, timezone
	return h
}

// Build returns the types and nodes.
func (h *HierarchyBuilder) Build() ([]hierarchy.Type, []hierarchy.Node) {
	return h.types, h.nodes
}

// Snapshot validates the hierarchy into an immutable snapshot.
func (h *HierarchyBuilder) Snapshot() (*hierarchy.Snapshot, error) {
	return hierarchy.NewSnapshot(h.types, h.nodes)
}

// MustSnapshot validates the hierarchy and fails the test if it is invalid.
func (h *HierarchyBuilder) MustSnapshot(t TestingT) *hierarchy.Snapshot {
	if helper, ok := t.(interface{ Helper() }); ok {
		helper.Helper()
	}
	snapshot, err := h.Snapshot()
	if err != nil {
		t.Fatalf("build hierarchy: %v", err)
	}
	return snapshot
}
