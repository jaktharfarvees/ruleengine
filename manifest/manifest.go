package manifest

import (
	"fmt"
	"time"

	"github.com/arrowhr/ruleengine/rule"
)

// Status is the lifecycle of a batch run manifest.
type Status string

const (
	// StatusOpen means evaluations may join the run.
	StatusOpen Status = "open"
	// StatusClosed means the run accepts no further lifecycle changes. Its pins
	// remain available indefinitely for replay.
	StatusClosed Status = "closed"
)

// RunManifest freezes rule and fact-source versions for one tenant and
// business time. The maps are immutable after insertion into Store.
type RunManifest struct {
	RunID               string            `json:"runId"`
	Tenant              string            `json:"tenant"`
	ValidAt             time.Time         `json:"validAt"`
	RulePins            map[string]string `json:"rulePins"`
	FactPins            map[string]string `json:"factPins"`
	HierarchySnapshotID string            `json:"hierarchySnapshotId"`
	Status              Status            `json:"status"`
	OpenedAt            time.Time         `json:"openedAt"`
	ClosedAt            *time.Time        `json:"closedAt,omitempty"`
}

// Validate checks identity, lifecycle, and pin completeness.
func (m RunManifest) Validate() error {
	if m.RunID == "" || m.Tenant == "" {
		return fmt.Errorf("runId and tenant are required")
	}
	if m.ValidAt.IsZero() || m.OpenedAt.IsZero() {
		return fmt.Errorf("validAt and openedAt are required")
	}
	if m.RulePins == nil || m.FactPins == nil || m.HierarchySnapshotID == "" {
		return fmt.Errorf("rulePins, factPins, and hierarchySnapshotId are required")
	}
	for rawKey, version := range m.RulePins {
		if _, err := rule.RuleKey(rawKey).Parse(); err != nil {
			return fmt.Errorf("rule pin %q: %w", rawKey, err)
		}
		if version == "" {
			return fmt.Errorf("rule pin %q has an empty version", rawKey)
		}
	}
	for field, version := range m.FactPins {
		if field == "" || version == "" {
			return fmt.Errorf("fact pin field and version are required")
		}
	}
	switch m.Status {
	case StatusOpen:
		if m.ClosedAt != nil {
			return fmt.Errorf("open manifest must not have closedAt")
		}
	case StatusClosed:
		if m.ClosedAt == nil || m.ClosedAt.Before(m.OpenedAt) {
			return fmt.Errorf("closed manifest requires closedAt at or after openedAt")
		}
	default:
		return fmt.Errorf("unsupported manifest status %q", m.Status)
	}
	return nil
}

// RulePin returns the frozen version for key.
func (m RunManifest) RulePin(key rule.RuleKey) (string, bool) {
	version, ok := m.RulePins[key.String()]
	return version, ok
}

// FactPin returns the frozen source version for field.
func (m RunManifest) FactPin(field string) (string, bool) {
	version, ok := m.FactPins[field]
	return version, ok
}

func clone(manifest RunManifest) RunManifest {
	manifest.RulePins = cloneMap(manifest.RulePins)
	manifest.FactPins = cloneMap(manifest.FactPins)
	if manifest.ClosedAt != nil {
		closedAt := *manifest.ClosedAt
		manifest.ClosedAt = &closedAt
	}
	return manifest
}

func cloneMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
