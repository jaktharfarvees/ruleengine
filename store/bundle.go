package store

import (
	"context"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

// Bundle is one coherent set of artifacts returned by a Loader. Cache builds
// and validates a complete immutable Snapshot before publishing any part of it.
type Bundle struct {
	Rules     []rule.RuleDefinition      `json:"rules"`
	Fields    []registry.FieldDefinition `json:"fields"`
	Manifests []dataset.Manifest         `json:"manifests"`
	Hierarchy HierarchyBundle            `json:"hierarchy"`
	Cursor    string                     `json:"cursor,omitempty"`
	LoadedAt  time.Time                  `json:"loadedAt,omitempty"`
}

// HierarchyBundle is the serializable source form of an immutable hierarchy
// snapshot. Types and nodes are validated together with the rest of a Bundle.
type HierarchyBundle struct {
	Types []hierarchy.Type `json:"types"`
	Nodes []hierarchy.Node `json:"nodes"`
}

// Loader obtains a complete coherent bundle. Implementations may read files,
// call the Rule Service, or provide deterministic fixtures.
type Loader interface {
	LoadAll(context.Context) (Bundle, error)
}

// ChangesLoader obtains the coherent state visible after cursor. The returned
// Bundle is a complete replacement snapshot, not a patch, so publication stays
// atomic even when the remote endpoint itself is changes-since based.
type ChangesLoader interface {
	LoadChanges(context.Context, string) (Bundle, error)
}
