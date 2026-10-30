package ruleengine

import "github.com/arrowhr/ruleengine/rule"

// SnapshotVersions returns a deep copy of every version the engine's current
// snapshot holds for key, so an external test can compare the snapshot before
// and after decisions without being able to change it.
func SnapshotVersions(e *Engine, key rule.RuleKey) []*rule.RuleDefinition {
	return e.cache.Load().Versions(e.config.Tenant, key)
}
