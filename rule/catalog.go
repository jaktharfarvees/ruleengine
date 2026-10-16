package rule

import (
	"encoding/json"
	"fmt"
	"time"
)

// MarshalJSON preserves the required entries array for a catalog that only
// authorizes defaults.
func (c RuleCatalog) MarshalJSON() ([]byte, error) {
	if c.Entries == nil {
		c.Entries = []CatalogEntry{}
	}
	type wire RuleCatalog
	return json.Marshal(wire(c))
}

// RuleCatalog is the published, tenant-scoped index manifest used by the Rule
// Service and local loaders. Runtime snapshots build optimized maps from it.
type RuleCatalog struct {
	CatalogManifestID string          `json:"catalogManifestId"`
	Tenant            string          `json:"tenant"`
	Entries           []CatalogEntry  `json:"entries"`
	Defaults          []DefaultPolicy `json:"defaults,omitempty"`
}

// CatalogEntry identifies one loadable rule candidate without embedding its
// complete logic.
type CatalogEntry struct {
	RuleKey          RuleKey           `json:"ruleKey"`
	Domain           string            `json:"domain"`
	RuleSet          string            `json:"ruleSet"`
	AnchorNodeID     string            `json:"anchorNodeId"`
	AttributeFilters []FilterCondition `json:"attributeFilters,omitempty"`
	Effective        Effective         `json:"effective"`
	DecisionType     string            `json:"decisionType"`
	LoaderKey        string            `json:"loaderKey"`
}

// DefaultPolicy is an explicit tenant-author decision for a low-risk rule set.
// Absence means fail closed. Authorization is enforced by the Rule Service at
// publication; evaluation only consumes already-authorized catalog artifacts.
type DefaultPolicy struct {
	Domain       string         `json:"domain"`
	RuleSet      string         `json:"ruleSet"`
	Name         string         `json:"name"`
	DecisionType string         `json:"decisionType"`
	Result       map[string]any `json:"result"`
	ReasonCode   string         `json:"reasonCode"`
	AuthorizedBy string         `json:"authorizedBy"`
	AuthorizedAt time.Time      `json:"authorizedAt"`
}

// Validate checks the catalog's identity, entries, and explicit defaults.
func (c RuleCatalog) Validate() error {
	if c.CatalogManifestID == "" || c.Tenant == "" {
		return fmt.Errorf("catalogManifestId and tenant are required")
	}
	if len(c.Entries) == 0 && len(c.Defaults) == 0 {
		return fmt.Errorf("catalog requires entries or defaults")
	}
	for index, entry := range c.Entries {
		parts, err := entry.RuleKey.Parse()
		if err != nil {
			return fmt.Errorf("entry %d ruleKey: %w", index, err)
		}
		if entry.Domain == "" || entry.RuleSet == "" || entry.AnchorNodeID == "" ||
			entry.DecisionType == "" || entry.LoaderKey == "" || entry.Effective.ValidFrom.IsZero() {
			return fmt.Errorf("entry %d is incomplete", index)
		}
		if parts.Domain != entry.Domain || parts.RuleSet != entry.RuleSet {
			return fmt.Errorf("entry %d ruleKey family does not match domain/ruleSet", index)
		}
		for filterIndex, filter := range entry.AttributeFilters {
			if err := validateFilter(filter); err != nil {
				return fmt.Errorf("entry %d attributeFilters[%d]: %w", index, filterIndex, err)
			}
		}
	}
	seenDefaults := make(map[string]struct{}, len(c.Defaults))
	for index, policy := range c.Defaults {
		if policy.Domain == "" || policy.RuleSet == "" || policy.Name == "" ||
			policy.DecisionType == "" || len(policy.Result) == 0 || policy.ReasonCode == "" ||
			policy.AuthorizedBy == "" || policy.AuthorizedAt.IsZero() {
			return fmt.Errorf("default %d is incomplete", index)
		}
		key := policy.Domain + "\x1f" + policy.RuleSet + "\x1f" + policy.Name
		if _, duplicate := seenDefaults[key]; duplicate {
			return fmt.Errorf("duplicate default for %s/%s/%s", policy.Domain, policy.RuleSet, policy.Name)
		}
		seenDefaults[key] = struct{}{}
	}
	return nil
}
