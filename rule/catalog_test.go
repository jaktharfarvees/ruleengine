package rule

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDefaultOnlyCatalogHasValidWireShape(t *testing.T) {
	catalog := RuleCatalog{
		CatalogManifestID: "defaults-v1", Tenant: "T1",
		Defaults: []DefaultPolicy{{
			Domain: "leave", RuleSet: "eligibility", Name: "baseline",
			DecisionType: "leave-eligibility", Result: map[string]any{"eligible": false},
			ReasonCode: "TENANT_DEFAULT", AuthorizedBy: "author-1", AuthorizedAt: time.Now(),
		}},
	}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	entries, ok := wire["entries"].([]any)
	if !ok || len(entries) != 0 {
		t.Fatalf("entries wire value = %#v, want an empty array", wire["entries"])
	}
}

func TestCatalogRejectsIncompleteAndDuplicateDefaults(t *testing.T) {
	now := time.Now()
	valid := DefaultPolicy{Domain: "leave", RuleSet: "eligibility", Name: "baseline", DecisionType: "leave-eligibility", Result: map[string]any{"eligible": false}, ReasonCode: "DEFAULT", AuthorizedBy: "author", AuthorizedAt: now}
	for _, catalog := range []RuleCatalog{
		{},
		{CatalogManifestID: "c", Tenant: "T1"},
		{CatalogManifestID: "c", Tenant: "T1", Defaults: []DefaultPolicy{{}}},
		{CatalogManifestID: "c", Tenant: "T1", Defaults: []DefaultPolicy{valid, valid}},
	} {
		if err := catalog.Validate(); err == nil {
			t.Fatalf("invalid catalog was accepted: %+v", catalog)
		}
	}
}
