package fact_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/ruletest"
)

// TestDerivedEvaluationErrorFailsClosed covers a formula that parses but
// cannot be computed. A division by zero must hold the decision rather than
// carry an invented quotient into a payroll figure.
func TestDerivedEvaluationErrorFailsClosed(t *testing.T) {
	resolver := mustResolver(t, mustRegistry(t, derivedField("ratio", "10 / 0")), nil, nil, nil)
	resolution, err := resolver.Resolve(context.Background(), []string{"ratio"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonUnresolvable)
	assertNoValues(t, resolution)
	if !strings.Contains(err.Error(), "could not be evaluated") {
		t.Fatalf("the error should name the evaluation failure: %v", err)
	}
}

// TestDerivedNilResultHonoursOnEmpty covers a formula that evaluates to no
// value. Without an onEmpty the field fails closed; with one, the author's
// declared answer is used and nothing else.
func TestDerivedNilResultHonoursOnEmpty(t *testing.T) {
	nullField := func(name string, onEmpty any) registry.FieldDefinition {
		return registry.FieldDefinition{
			Name: name, Domain: "leave", DataType: registry.DataInteger,
			OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
			FactSource: registry.FactSource{Kind: registry.FactDerived, Expression: "coalesce(null)", OnEmpty: onEmpty},
		}
	}
	resolver := mustResolver(t, mustRegistry(t,
		nullField("noFallback", nil),
		nullField("withFallback", 7),
	), nil, nil, nil)

	resolution, err := resolver.Resolve(context.Background(), []string{"noFallback"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonUnresolvable)
	assertNoValues(t, resolution)
	if !strings.Contains(err.Error(), "produced no value") {
		t.Fatalf("the error should name the empty result: %v", err)
	}

	resolution, err = resolver.Resolve(context.Background(), []string{"withFallback"}, nil, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolution.Values["withFallback"]; got != 7 {
		t.Fatalf("withFallback = %#v, want the declared onEmpty 7", got)
	}
}

// TestCachedEmptyResultIsRecordedAsCached covers the second decision point of
// a request reading a no-rows answer from the de-duplication cache: it gets
// the declared onEmpty, the audit record shows the value was cached, and
// storage is not asked twice.
func TestCachedEmptyResultIsRecordedAsCached(t *testing.T) {
	manifests, err := ruletest.LoadManifests("leave.applications", "leave.balances")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := ruletest.NewInMemoryExecutor(manifests["leave.applications"], manifests["leave.balances"])
	if err != nil {
		t.Fatal(err)
	}
	fields, err := ruletest.LoadRegistry("leave")
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingExecutor{Executor: executor}
	session := mustResolver(t, fields, manifests, counting, nil).NewSession()

	for point := 0; point < 2; point++ {
		resolution, err := session.Resolve(context.Background(),
			[]string{"casualLeaveTakenThisYear"}, nil, params(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := resolution.Values["casualLeaveTakenThisYear"]; got != "0" {
			t.Fatalf("point %d got %#v, want the declared onEmpty", point, got)
		}
		if cached := resolution.Facts[0].Cached; cached != (point == 1) {
			t.Fatalf("point %d recorded Cached=%v", point, cached)
		}
	}
	if counting.calls != 1 {
		t.Fatalf("executor ran %d queries, want 1", counting.calls)
	}
}

// TestCachedEmptyResultWithoutOnEmptyFailsClosed covers the same cache hit for
// a field that declares no onEmpty. The cached no-rows answer must fail the
// second point exactly as it failed the first, not surface as a nil value.
func TestCachedEmptyResultWithoutOnEmptyFailsClosed(t *testing.T) {
	manifest := mustManifest(t, "leave.balances")
	executor, err := ruletest.NewInMemoryExecutor(manifest)
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingExecutor{Executor: executor}
	fields := mustRegistry(t, registry.FieldDefinition{
		Name: "noFallback", Domain: "leave", DataType: registry.DataDecimal,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{
			Kind: registry.FactDataset, Dataset: "leave.balances",
			Window:    &registry.FactWindow{Column: "asOfDate", RelativeTo: dataset.TokenValidAt, LastDays: 30},
			Aggregate: &registry.FactAggregate{Fn: registry.AggregateLatest, On: "balanceDays"},
		},
	})
	session := mustResolver(t, fields, map[string]dataset.Manifest{"leave.balances": manifest}, counting, nil).NewSession()

	for point := 0; point < 2; point++ {
		resolution, err := session.Resolve(context.Background(), []string{"noFallback"}, nil, params(), nil)
		assertCode(t, err, fact.ReasonUnresolvable)
		assertNoValues(t, resolution)
		if !strings.Contains(err.Error(), "matched no rows") {
			t.Fatalf("point %d: the error should name the empty result: %v", point, err)
		}
	}
	if counting.calls != 1 {
		t.Fatalf("executor ran %d queries, want the second point served from the cache", counting.calls)
	}
}

// TestContextEndedBeforeResolveFailsClosed covers a request whose context is
// already over when resolution starts. Nothing is resolved, not even a value
// the caller supplied, and the reason code says which way the context ended.
func TestContextEndedBeforeResolveFailsClosed(t *testing.T) {
	fields := mustRegistry(t, registry.FieldDefinition{
		Name: "given", Domain: "leave", DataType: registry.DataInteger,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactPassed},
	})
	resolver := mustResolver(t, fields, nil, nil, nil)
	have := map[string]any{"given": 3}

	expired, cancelExpired := context.WithDeadline(context.Background(), validAt)
	defer cancelExpired()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, test := range []struct {
		name  string
		ctx   context.Context
		code  string
		cause error
	}{
		{"deadline exceeded", expired, fact.ReasonTimeout, context.DeadlineExceeded},
		{"cancelled", cancelled, fact.ReasonUnresolvable, context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolution, err := resolver.Resolve(test.ctx, []string{"given"}, have, params(), nil)
			assertCode(t, err, test.code)
			assertNoValues(t, resolution)
			if !errors.Is(err, test.cause) {
				t.Fatalf("the cause should be wrapped: %v", err)
			}
		})
	}
}

func assertNoValues(t *testing.T, resolution fact.Resolution) {
	t.Helper()
	if resolution.Values != nil || resolution.Facts != nil {
		t.Fatalf("a failed resolution returned values: %+v", resolution)
	}
}
