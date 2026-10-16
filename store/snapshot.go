package store

import (
	"fmt"
	"sort"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

type catalogKey struct {
	tenant  string
	domain  string
	ruleSet string
	name    string
}

type familyKey struct {
	tenant string
	key    rule.RuleKey
}

type versionKey struct {
	tenant  string
	key     rule.RuleKey
	version string
}

// Stats describes the contents and freshness of a published snapshot.
type Stats struct {
	Rules          int       `json:"rules"`
	RuleFamilies   int       `json:"ruleFamilies"`
	CatalogEntries int       `json:"catalogEntries"`
	Fields         int       `json:"fields"`
	Manifests      int       `json:"manifests"`
	HierarchyTypes int       `json:"hierarchyTypes"`
	HierarchyNodes int       `json:"hierarchyNodes"`
	Cursor         string    `json:"cursor,omitempty"`
	LoadedAt       time.Time `json:"loadedAt"`
}

// Snapshot is a validated immutable view safe for concurrent readers. Slices
// and pointers returned by its lookup methods are immutable views owned by the
// snapshot and must not be modified by callers.
type Snapshot struct {
	byVersion map[versionKey][]*rule.RuleDefinition
	versions  map[familyKey][]*rule.RuleDefinition
	catalog   map[catalogKey][]catalogEntry
	registry  *registry.Registry
	manifests map[string]dataset.Manifest
	hierarchy *hierarchy.Snapshot
	stats     Stats
}

func newSnapshot(bundle Bundle, loadedAt time.Time) (*Snapshot, error) {
	fields := append([]registry.FieldDefinition(nil), bundle.Fields...)
	fieldRegistry, err := registry.NewRegistry(fields)
	if err != nil {
		return nil, fmt.Errorf("build field registry: %w", err)
	}
	hierarchySnapshot, err := hierarchy.NewSnapshot(
		append([]hierarchy.Type(nil), bundle.Hierarchy.Types...),
		append([]hierarchy.Node(nil), bundle.Hierarchy.Nodes...),
	)
	if err != nil {
		return nil, fmt.Errorf("build hierarchy snapshot: %w", err)
	}
	snapshot := &Snapshot{
		byVersion: make(map[versionKey][]*rule.RuleDefinition, len(bundle.Rules)),
		versions:  make(map[familyKey][]*rule.RuleDefinition),
		catalog:   make(map[catalogKey][]catalogEntry),
		registry:  fieldRegistry,
		manifests: make(map[string]dataset.Manifest, len(bundle.Manifests)),
		hierarchy: hierarchySnapshot,
		stats: Stats{
			Rules: len(bundle.Rules), Fields: len(bundle.Fields), Manifests: len(bundle.Manifests),
			HierarchyTypes: len(bundle.Hierarchy.Types), HierarchyNodes: len(bundle.Hierarchy.Nodes),
			Cursor: bundle.Cursor, LoadedAt: loadedAt,
		},
	}
	parsedVersions := make(map[versionKey]semanticVersion, len(bundle.Rules))
	for index := range bundle.Rules {
		definition := cloneRule(bundle.Rules[index])
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("validate rule at index %d: %w", index, err)
		}
		parsed, err := parseSemanticVersion(definition.Version)
		if err != nil {
			return nil, fmt.Errorf("validate rule %q version: %w", definition.RuleKey, err)
		}
		identity := scopeIdentity(definition.Scope)
		exact := versionKey{tenant: definition.Tenant, key: definition.RuleKey, version: definition.Version}
		for _, existing := range snapshot.byVersion[exact] {
			if scopeIdentity(existing.Scope) == identity {
				return nil, fmt.Errorf("duplicate rule variant tenant=%q key=%q version=%q", definition.Tenant, definition.RuleKey, definition.Version)
			}
		}
		stored := new(rule.RuleDefinition)
		*stored = definition
		snapshot.byVersion[exact] = append(snapshot.byVersion[exact], stored)
		parsedVersions[exact] = parsed
		family := familyKey{tenant: definition.Tenant, key: definition.RuleKey}
		snapshot.versions[family] = append(snapshot.versions[family], stored)
		catalog := catalogKey{tenant: definition.Tenant, domain: definition.Domain, ruleSet: definition.RuleSet, name: definition.Name}
		snapshot.catalog[catalog] = append(snapshot.catalog[catalog],
			catalogEntry{definition: stored, scope: identity, version: parsed})
	}
	for family, definitions := range snapshot.versions {
		sort.SliceStable(definitions, func(i, j int) bool {
			left := parsedVersions[versionKey{tenant: family.tenant, key: family.key, version: definitions[i].Version}]
			right := parsedVersions[versionKey{tenant: family.tenant, key: family.key, version: definitions[j].Version}]
			return compareSemanticVersions(left, right) > 0
		})
	}
	for _, entries := range snapshot.catalog {
		sortCatalogBucket(entries)
	}
	for index := range bundle.Manifests {
		manifest := cloneManifest(bundle.Manifests[index])
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("validate dataset manifest at index %d: %w", index, err)
		}
		if _, duplicate := snapshot.manifests[manifest.Dataset]; duplicate {
			return nil, fmt.Errorf("duplicate dataset manifest %q", manifest.Dataset)
		}
		snapshot.manifests[manifest.Dataset] = manifest
	}
	snapshot.stats.RuleFamilies = len(snapshot.versions)
	snapshot.stats.CatalogEntries = len(snapshot.catalog)
	return snapshot, nil
}

// Candidates returns the rules resolution should choose between: one per
// distinct scope in the catalog bucket, each at its latest version that is
// evaluable and effective at at. Including name in the key prevents
// rule-set-wide scans.
//
// Collapsing versions here is what keeps a version bump from becoming a
// precedence tie; see collapse.
func (s *Snapshot) Candidates(tenant, domain, ruleSet, name string, at time.Time) []*rule.RuleDefinition {
	if s == nil || tenant == "" || domain == "" || ruleSet == "" || name == "" || at.IsZero() {
		return nil
	}
	return collapse(s.catalog[catalogKey{tenant: tenant, domain: domain, ruleSet: ruleSet, name: name}], at)
}

// candidatesForKey is Candidates addressed by rule key, for version selection.
func (s *Snapshot) candidatesForKey(tenant string, key rule.RuleKey, at time.Time) []*rule.RuleDefinition {
	family := key.Family()
	return s.Candidates(tenant, family.Domain, family.RuleSet, family.Name, at)
}

// Versions returns every cached version for a tenant and rule key in newest
// semantic-version order, whatever its status. It is an index lookup for
// diagnostics and version selection, not a selection in itself.
func (s *Snapshot) Versions(tenant string, key rule.RuleKey) []*rule.RuleDefinition {
	if s == nil {
		return nil
	}
	return append([]*rule.RuleDefinition(nil), s.versions[familyKey{tenant: tenant, key: key}]...)
}

// Version returns every anchored variant of one exact cached rule version.
//
// This is a raw index lookup and applies no selection policy: it returns a
// draft or retired version if one is cached. Selecting a version for an
// evaluation goes through VersionResolver, which refuses a version that is not
// evaluable even when a pin names it.
func (s *Snapshot) Version(tenant string, key rule.RuleKey, version string) []*rule.RuleDefinition {
	if s == nil {
		return nil
	}
	return append([]*rule.RuleDefinition(nil), s.byVersion[versionKey{tenant: tenant, key: key, version: version}]...)
}

// Registry returns the immutable field registry in this snapshot.
func (s *Snapshot) Registry() *registry.Registry {
	if s == nil {
		return nil
	}
	return s.registry
}

// Manifest returns an independent copy of a dataset manifest.
func (s *Snapshot) Manifest(name string) (dataset.Manifest, bool) {
	if s == nil {
		return dataset.Manifest{}, false
	}
	manifest, ok := s.manifests[name]
	return cloneManifest(manifest), ok
}

// Manifests returns an independent copy of every published dataset manifest,
// keyed by dataset.
//
// Enumerating them is what a service needs to build a fact resolver, which
// takes the whole set rather than looking them up one at a time: the
// declaration names the dataset, so the resolver must already hold it.
func (s *Snapshot) Manifests() map[string]dataset.Manifest {
	if s == nil {
		return map[string]dataset.Manifest{}
	}
	manifests := make(map[string]dataset.Manifest, len(s.manifests))
	for name, manifest := range s.manifests {
		manifests[name] = cloneManifest(manifest)
	}
	return manifests
}

// Hierarchy returns the immutable hierarchy snapshot published with the rules.
func (s *Snapshot) Hierarchy() *hierarchy.Snapshot {
	if s == nil {
		return nil
	}
	return s.hierarchy
}

// Stats returns snapshot counts and freshness metadata.
func (s *Snapshot) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return s.stats
}
