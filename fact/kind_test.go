package fact_test

import (
	"testing"

	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/registry"
)

// TestKindAliasesMatchTheRegistry pins the alias relationship: a caller writing
// fact.KindDataset and a registry entry carrying registry.FactDataset must be
// the same value, or a declaration would resolve differently depending on which
// package named it.
func TestKindAliasesMatchTheRegistry(t *testing.T) {
	pairs := []struct {
		factKind     fact.Kind
		registryKind registry.FactKind
	}{
		{fact.KindPassed, registry.FactPassed},
		{fact.KindDerived, registry.FactDerived},
		{fact.KindDataset, registry.FactDataset},
		{fact.KindNative, registry.FactNative},
	}
	for _, pair := range pairs {
		if pair.factKind != pair.registryKind {
			t.Errorf("fact kind %q does not match registry kind %q", pair.factKind, pair.registryKind)
		}
		if !pair.factKind.Valid() {
			t.Errorf("%q should be valid", pair.factKind)
		}
	}
	if fact.Kind("remote").Valid() || fact.Kind("").Valid() {
		t.Fatal("unknown fact kind was accepted")
	}
	// The alias must be an alias, not a distinct named type: assigning one to
	// the other without a conversion is the property being asserted.
	var source fact.FactSource = registry.FactSource{Kind: registry.FactDataset}
	var back registry.FactSource = source
	if back.Kind != fact.KindDataset {
		t.Fatal("FactSource alias does not round-trip")
	}
}
