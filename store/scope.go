package store

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arrowhr/ruleengine/rule"
)

// catalogEntry is one authored rule in the catalog, carrying the two things
// selection needs beside the definition: which rule it is a version of, and
// how its version compares to the others.
type catalogEntry struct {
	definition *rule.RuleDefinition
	scope      string
	version    semanticVersion
}

// scopeIdentity answers "are these two definitions versions of the same rule?"
//
// A rule's identity for selection is its key plus its scope — the anchor it
// governs and the filters that narrow it. Two definitions sharing a scope are
// successive versions of one rule, and only the newest effective one may be a
// candidate. Two definitions with different scopes are different rules that
// resolution compares by anchor depth, so both must survive.
//
// Filters are canonicalised rather than compared in authored order: reordering
// a filter list changes nothing about which subjects a rule covers, so it must
// not create a second rule.
func scopeIdentity(scope rule.Scope) string {
	var builder strings.Builder
	builder.Grow(64)
	builder.WriteString(string(scope.Anchor.Kind))
	builder.WriteByte('\x1f')
	builder.WriteString(scope.Anchor.NodeID)
	builder.WriteByte('\x1f')
	builder.WriteString(scope.Anchor.EmployeeID)
	builder.WriteByte('\x1f')
	// Anchor.Reason is deliberately excluded: it is documentation for an
	// employee exception, not part of which subjects the rule covers.
	filters := make([]string, 0, len(scope.Filters))
	for _, filter := range scope.Filters {
		filters = append(filters, fmt.Sprintf("%s\x1d%s\x1d%v", filter.Attribute, filter.Op, filter.Value))
	}
	sort.Strings(filters)
	for _, filter := range filters {
		builder.WriteString(filter)
		builder.WriteByte('\x1e')
	}
	return builder.String()
}

// collapse selects the candidates for one catalog bucket at a business time.
//
// Entries arrive grouped by scope, newest version first within each group, so
// one pass takes the newest version of each rule that is evaluable and
// effective at at — which is the "latest version whose Effective contains
// validAt" the pipeline requires — and skips the older versions behind it.
//
// Collapsing here rather than in resolution is deliberate. Resolution ranks
// candidates by anchor depth and has no version tie-break, so handing it two
// versions of one rule would make a routine version bump an unresolved
// precedence tie and fail every decision closed.
func collapse(entries []catalogEntry, at time.Time) []*rule.RuleDefinition {
	if len(entries) == 0 {
		return nil
	}
	selected := make([]*rule.RuleDefinition, 0, len(entries))
	// The empty string cannot be a real scope identity, which always carries
	// at least an anchor kind, so it is a safe "nothing selected yet" marker.
	taken := ""
	for _, entry := range entries {
		if entry.scope == taken {
			continue
		}
		if !entry.definition.Status.Evaluable() || !entry.definition.Effective.Contains(at) {
			continue
		}
		selected = append(selected, entry.definition)
		taken = entry.scope
	}
	if len(selected) == 0 {
		return nil
	}
	return selected
}

// sortCatalogBucket groups a bucket by scope and orders each group newest
// version first, which is what lets collapse select in a single pass.
func sortCatalogBucket(entries []catalogEntry) {
	sort.SliceStable(entries, func(left, right int) bool {
		if entries[left].scope != entries[right].scope {
			return entries[left].scope < entries[right].scope
		}
		return compareSemanticVersions(entries[left].version, entries[right].version) > 0
	})
}
