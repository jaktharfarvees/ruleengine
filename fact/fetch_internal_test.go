package fact

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/telemetry"
)

// The registry refuses each declaration below before a resolver can see it,
// so these tests call the fetchers directly. The checks are defence in depth:
// a registry change that let one through must still fail the decision closed
// rather than reach for a nameless function or dataset.

func TestNativeFactWithoutAFunctionFailsClosed(t *testing.T) {
	value, resolved, err := newTestResolution(t).fetchNative(context.Background(), "nameless",
		registry.FactSource{Kind: registry.FactNative})
	assertFault(t, value, resolved, err, ReasonUnresolvable, "names no function")
}

func TestDatasetFactWithoutADatasetFailsClosed(t *testing.T) {
	value, resolved, err := newTestResolution(t).fetchDataset(context.Background(), "nameless",
		registry.FieldDefinition{}, registry.FactSource{Kind: registry.FactDataset})
	assertFault(t, value, resolved, err, ReasonUnresolvable, "names no dataset")
}

func TestUnsupportedFactKindFailsClosed(t *testing.T) {
	value, resolved, err := newTestResolution(t).fetch(context.Background(), "odd",
		registry.FieldDefinition{}, registry.FactSource{Kind: registry.FactKind("spreadsheet")})
	assertFault(t, value, resolved, err, ReasonUnresolvable, `unsupported fact kind "spreadsheet"`)
}

func newTestResolution(t *testing.T) *resolution {
	t.Helper()
	fields, err := registry.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(fields, nil, nil, NewNativeRegistry(), Budget{
		MaxDatasetFacts: 4, PerFactTimeout: time.Second, TotalFactTimeout: time.Second, MaxRowsScanned: 100,
	}, telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	return &resolution{
		session: resolver.NewSession(),
		values:  map[string]any{},
		facts:   map[string]Resolved{},
		active:  map[string]struct{}{},
	}
}

func assertFault(t *testing.T, value any, resolved Resolved, err error, code, message string) {
	t.Helper()
	if Code(err) != code {
		t.Fatalf("reason code = %q, want %q: %v", Code(err), code, err)
	}
	if !strings.Contains(err.Error(), message) {
		t.Fatalf("error %q does not mention %q", err, message)
	}
	if value != nil || resolved != (Resolved{}) {
		t.Fatalf("a failed fetch returned value %#v and record %+v", value, resolved)
	}
}
