package ruleengine_test

import (
	"testing"

	ruleengine "github.com/arrowhr/ruleengine"
)

// TestEngineTenantReportsTheConfiguredTenant covers the accessor a pool uses
// to check that a factory built the engine it was asked for.
func TestEngineTenantReportsTheConfiguredTenant(t *testing.T) {
	instance := engineOver(t, baseConfig())
	if got := instance.Tenant(); got != tenant {
		t.Fatalf("Tenant() = %q, want %q", got, tenant)
	}
	var missing *ruleengine.Engine
	if got := missing.Tenant(); got != "" {
		t.Fatalf("nil engine Tenant() = %q, want empty", got)
	}
}
