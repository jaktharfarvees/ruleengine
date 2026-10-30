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

type defaultKey struct {
	tenant, domain, ruleSet, name string
}

type publishedHierarchy struct {
	id        string
	validFrom time.Time
	validTo   *time.Time
	snapshot  *hierarchy.Snapshot
	types     int
	nodes     int
}

func (p publishedHierarchy) contains(at time.Time) bool {
	return (p.validFrom.IsZero() || !at.Before(p.validFrom)) && (p.validTo == nil || at.Before(*p.validTo))
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
	byVersion   map[versionKey][]*rule.RuleDefinition
	versions    map[familyKey][]*rule.RuleDefinition
	catalog     map[catalogKey][]catalogEntry
	registry    *registry.Registry
	manifests   map[string]dataset.Manifest
	defaults    map[defaultKey]rule.DefaultPolicy
	hierarchy   *hierarchy.Snapshot
	hierarchies []publishedHierarchy
	stats       Stats
}

func newSnapshot(bundle Bundle, loadedAt time.Time) (*Snapshot, error) {
	fields := append([]registry.FieldDefinition(nil), bundle.Fields...)
	fieldRegistry, err := registry.NewRegistry(fields)
	if err != nil {
		return nil, fmt.Errorf("build field registry: %w", err)
	}
	hierarchyBundles := append([]HierarchyBundle(nil), bundle.Hierarchies...)
	if len(hierarchyBundles) == 0 {
		hierarchyBundles = []HierarchyBundle{bundle.Hierarchy}
	}
	snapshot := &Snapshot{
		byVersion: make(map[versionKey][]*rule.RuleDefinition, len(bundle.Rules)),
		versions:  make(map[familyKey][]*rule.RuleDefinition),
		catalog:   make(map[catalogKey][]catalogEntry),
		registry:  fieldRegistry,
		manifests: make(map[string]dataset.Manifest, len(bundle.Manifests)),
		defaults:  make(map[defaultKey]rule.DefaultPolicy),
		stats: Stats{
			Rules: len(bundle.Rules), Fields: len(bundle.Fields), Manifests: len(bundle.Manifests),
			Cursor: bundle.Cursor, LoadedAt: loadedAt,
		},
	}
	for index, source := range hierarchyBundles {
		if source.ValidTo != nil && (source.ValidFrom.IsZero() || !source.ValidTo.After(source.ValidFrom)) {
			return nil, fmt.Errorf("hierarchy snapshot %d has an invalid effective interval", index)
		}
		built, err := hierarchy.NewSnapshot(
			append([]hierarchy.Type(nil), source.Types...),
			append([]hierarchy.Node(nil), source.Nodes...),
		)
		if err != nil {
			return nil, fmt.Errorf("build hierarchy snapshot %d: %w", index, err)
		}
		id := source.SnapshotID
		if id == "" {
			id = "default"
		}
		for _, existing := range snapshot.hierarchies {
			if existing.id == id {
				return nil, fmt.Errorf("duplicate hierarchy snapshot id %q", id)
			}
		}
		snapshot.hierarchies = append(snapshot.hierarchies, publishedHierarchy{
			id: id, validFrom: source.ValidFrom, validTo: source.ValidTo, snapshot: built,
			types: len(source.Types), nodes: len(source.Nodes),
		})
	}
	sort.Slice(snapshot.hierarchies, func(left, right int) bool {
		return snapshot.hierarchies[left].validFrom.Before(snapshot.hierarchies[right].validFrom)
	})
	for index := 1; index < len(snapshot.hierarchies); index++ {
		previous := snapshot.hierarchies[index-1]
		current := snapshot.hierarchies[index]
		if current.validFrom.IsZero() || previous.validTo == nil || current.validFrom.Before(*previous.validTo) {
			return nil, fmt.Errorf("hierarchy snapshots %q and %q have overlapping effective intervals", previous.id, current.id)
		}
	}
	snapshot.hierarchy = snapshot.hierarchies[len(snapshot.hierarchies)-1].snapshot
	latestHierarchy := snapshot.hierarchies[len(snapshot.hierarchies)-1]
	snapshot.stats.HierarchyTypes = latestHierarchy.types
	snapshot.stats.HierarchyNodes = latestHierarchy.nodes
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
		identity := scopeIdentity(definition)
		exact := versionKey{tenant: definition.Tenant, key: definition.RuleKey, version: definition.Version}
		for _, existing := range snapshot.byVersion[exact] {
			if scopeIdentity(*existing) == identity {
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
	for index, catalog := range bundle.Catalogs {
		if err := catalog.Validate(); err != nil {
			return nil, fmt.Errorf("validate rule catalog at index %d: %w", index, err)
		}
		for _, policy := range catalog.Defaults {
			key := defaultKey{tenant: catalog.Tenant, domain: policy.Domain, ruleSet: policy.RuleSet, name: policy.Name}
			if _, duplicate := snapshot.defaults[key]; duplicate {
				return nil, fmt.Errorf("duplicate catalog default tenant=%q domain=%q ruleSet=%q name=%q", catalog.Tenant, policy.Domain, policy.RuleSet, policy.Name)
			}
			policy.Result = cloneAnyMap(policy.Result)
			snapshot.defaults[key] = policy
		}
	}
	for index := range bundle.Manifests {
		manifest := cloneManifest(bundle.Manifests[index]).Canonical()
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("validate dataset manifest at index %d: %w", index, err)
		}
		if _, duplicate := snapshot.manifests[manifest.DatasetID]; duplicate {
			return nil, fmt.Errorf("duplicate dataset manifest %q", manifest.DatasetID)
		}
		snapshot.manifests[manifest.DatasetID] = manifest
	}
	snapshot.stats.RuleFamilies = len(snapshot.versions)
	snapshot.stats.CatalogEntries = len(snapshot.catalog)
	return snapshot, nil
}

// DefaultPolicy returns the explicit tenant-authorized fallback for a rule
// family. Absence means evaluation must fail closed.
func (s *Snapshot) DefaultPolicy(tenant string, family rule.Family) (rule.DefaultPolicy, bool) {
	if s == nil {
		return rule.DefaultPolicy{}, false
	}
	policy, found := s.defaults[defaultKey{tenant: tenant, domain: family.Domain, ruleSet: family.RuleSet, name: family.Name}]
	if !found {
		return rule.DefaultPolicy{}, false
	}
	policy.Result = cloneAnyMap(policy.Result)
	return policy, true
}

// Candidates returns the rules resolution should choose between: one per
// distinct scope in the catalog bucket, each at its latest version that is
// evaluable and effective at at. Including name in the key prevents
// rule-set-wide scans.
//
// Collapsing versions here is what keeps a version bump from becoming a
// precedence tie; see collapse.
func (s *Snapshot) Candidates(tenant, domain, ruleSet, name string, at time.Time) []*rule.RuleDefinition {
	return cloneDefinitions(s.candidates(tenant, domain, ruleSet, name, at))
}

// candidates is Candidates without the deep copy, for version selection on the
// decision path. The slice is new but the rules are the snapshot's own, which
// newSnapshot copied from the bundle and nothing mutates afterwards; callers
// must only read them.
func (s *Snapshot) candidates(tenant, domain, ruleSet, name string, at time.Time) []*rule.RuleDefinition {
	if s == nil || tenant == "" || domain == "" || ruleSet == "" || name == "" || at.IsZero() {
		return nil
	}
	return collapse(s.catalog[catalogKey{tenant: tenant, domain: domain, ruleSet: ruleSet, name: name}], at)
}

// candidatesForKey is candidates addressed by rule key, for version selection.
func (s *Snapshot) candidatesForKey(tenant string, key rule.RuleKey, at time.Time) []*rule.RuleDefinition {
	family := key.Family()
	return s.candidates(tenant, family.Domain, family.RuleSet, family.Name, at)
}

// Versions returns every cached version for a tenant and rule key in newest
// semantic-version order, whatever its status. It is an index lookup for
// diagnostics and version selection, not a selection in itself.
func (s *Snapshot) Versions(tenant string, key rule.RuleKey) []*rule.RuleDefinition {
	if s == nil {
		return nil
	}
	return cloneDefinitions(s.versions[familyKey{tenant: tenant, key: key}])
}

// Version returns every anchored variant of one exact cached rule version.
//
// This is a raw index lookup and applies no selection policy: it returns a
// draft or retired version if one is cached. Selecting a version for an
// evaluation goes through VersionResolver, which refuses a version that is not
// evaluable even when a pin names it.
func (s *Snapshot) Version(tenant string, key rule.RuleKey, version string) []*rule.RuleDefinition {
	return cloneDefinitions(s.version(tenant, key, version))
}

// version is Version without the deep copy, for pinned version selection. Both
// the slice and the rules are the snapshot's own; callers must only read them.
func (s *Snapshot) version(tenant string, key rule.RuleKey, version string) []*rule.RuleDefinition {
	if s == nil {
		return nil
	}
	return s.byVersion[versionKey{tenant: tenant, key: key, version: version}]
}

func cloneDefinitions(source []*rule.RuleDefinition) []*rule.RuleDefinition {
	if len(source) == 0 {
		return nil
	}
	result := make([]*rule.RuleDefinition, len(source))
	for index, definition := range source {
		if definition == nil {
			continue
		}
		cloned := cloneRule(*definition)
		result[index] = &cloned
	}
	return result
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

// HierarchyAt returns the tenant hierarchy effective at business time. A
// non-empty snapshotID makes selection exact for replayable batch runs.
func (s *Snapshot) HierarchyAt(tenant string, at time.Time, snapshotID string) (*hierarchy.Snapshot, string, bool) {
	if s == nil || tenant == "" || at.IsZero() {
		return nil, "", false
	}
	for index := len(s.hierarchies) - 1; index >= 0; index-- {
		candidate := s.hierarchies[index]
		if snapshotID != "" && candidate.id != snapshotID {
			continue
		}
		if !candidate.contains(at) || candidate.snapshot.Empty(tenant) {
			continue
		}
		return candidate.snapshot, candidate.id, true
	}
	return nil, "", false
}

// Stats returns snapshot counts and freshness metadata.
func (s *Snapshot) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return s.stats
}
