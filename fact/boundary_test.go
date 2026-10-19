package fact_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/ruletest"
	"github.com/arrowhr/ruleengine/telemetry"
)

func TestNativeRegistryBoundaries(t *testing.T) {
	natives := fact.NewNativeRegistry()
	noop := func(context.Context, fact.Params) (any, error) { return 1, nil }

	if err := natives.Register("", noop); err == nil {
		t.Fatal("an unnamed native fact was registered")
	}
	if err := natives.Register("named", nil); err == nil {
		t.Fatal("a native fact with no function was registered")
	}
	if err := natives.Register("named", noop); err != nil {
		t.Fatal(err)
	}
	if !natives.Has("named") || natives.Has("other") {
		t.Fatal("Has disagrees with what was registered")
	}
	if references := natives.References(); len(references) != 1 || references[0] != "named" {
		t.Fatalf("References() = %v", references)
	}

	// The zero value and a nil registry must answer rather than panic: a
	// service that registers nothing is a valid configuration, and a decision
	// must fail closed rather than take the process down.
	var zero fact.NativeRegistry
	if zero.Has("named") {
		t.Fatal("the zero registry claims a registration")
	}
	if err := zero.Register("named", noop); err != nil {
		t.Fatalf("the zero registry refused a registration: %v", err)
	}
	var absent *fact.NativeRegistry
	if absent.Has("named") || absent.References() != nil {
		t.Fatal("a nil registry claims a registration")
	}
	if err := absent.Register("named", noop); err == nil {
		t.Fatal("a nil registry accepted a registration")
	}
}

func TestFaultFormattingAndCode(t *testing.T) {
	var nilFault *fact.Fault
	if nilFault.Error() != "<nil>" || nilFault.Unwrap() != nil {
		t.Fatal("a nil fault should be printable")
	}
	cause := errors.New("because")
	fault := &fact.Fault{Code: fact.ReasonTimeout, Field: "countConsecutiveLate", Err: cause}
	message := fault.Error()
	for _, want := range []string{fact.ReasonTimeout, "countConsecutiveLate", "because"} {
		if !strings.Contains(message, want) {
			t.Fatalf("Error() = %q, missing %q", message, want)
		}
	}
	if !errors.Is(fault, cause) || !errors.Is(fault.Unwrap(), cause) {
		t.Fatal("Unwrap did not expose the cause")
	}
	if bare := (&fact.Fault{Code: fact.ReasonUnresolvable}).Error(); bare != fact.ReasonUnresolvable {
		t.Fatalf("Error() = %q", bare)
	}

	// Code reads a reason code out of either package's fault, and says nothing
	// about an error that did not come from fact resolution.
	if got := fact.Code(fault); got != fact.ReasonTimeout {
		t.Fatalf("Code() = %q", got)
	}
	compileFault := &dataset.Fault{Code: dataset.ReasonWindowTooWide}
	if got := fact.Code(compileFault); got != dataset.ReasonWindowTooWide {
		t.Fatalf("Code() = %q for a compile fault", got)
	}
	if got := fact.Code(errors.New("unrelated")); got != "" {
		t.Fatalf("Code() = %q for an unrelated error, want empty", got)
	}
	if got := fact.Code(nil); got != "" {
		t.Fatalf("Code(nil) = %q", got)
	}
}

func TestResolverExposesItsBudget(t *testing.T) {
	fields := mustRegistry(t, countField("one", 1))
	resolver := mustResolver(t, fields, nil, nil, nil)
	if resolver.Budget() != ruletest.Budget() {
		t.Fatalf("Budget() = %+v", resolver.Budget())
	}
	// A nil metrics sink is substituted rather than refused, so a consumer
	// that supplies none still works.
	if _, err := fact.NewResolver(fields, nil, nil, nil, ruletest.Budget(), nil); err != nil {
		t.Fatal(err)
	}
}

// TestTotalTimeoutFailsClosed covers the stage-wide deadline, which bounds a
// whole request rather than one fetch: several facts that are each fast enough
// must still not run the decision past its budget.
func TestTotalTimeoutFailsClosed(t *testing.T) {
	manifests := map[string]dataset.Manifest{"attendance.days": mustManifest(t, "attendance.days")}
	executor, err := ruletest.NewInMemoryExecutor(manifests["attendance.days"])
	if err != nil {
		t.Fatal(err)
	}
	// Each fetch is comfortably inside the per-fact timeout; three of them are
	// not inside the stage timeout.
	executor.SetDelay(25 * time.Millisecond)
	budget := ruletest.Budget()
	budget.PerFactTimeout = 50 * time.Millisecond
	budget.TotalFactTimeout = 60 * time.Millisecond
	fields := mustRegistry(t, countField("one", 1), countField("two", 2), countField("three", 3))
	resolver, err := fact.NewResolver(fields, manifests, executor, nil, budget, telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), []string{"one", "two", "three"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonTimeout)
}

// TestCallerCancellationIsHonoured covers a caller giving up: the request's
// context, not only the library's own deadline, ends the fetch.
func TestCallerCancellationIsHonoured(t *testing.T) {
	resolver, executor := attendanceResolver(t)
	addAttendance(t, executor, []bool{true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := resolver.Resolve(ctx, []string{"countConsecutiveLate"}, nil, params(), nil)
	if err == nil {
		t.Fatal("a cancelled request still resolved facts")
	}
	if code := fact.Code(err); code != fact.ReasonUnresolvable && code != fact.ReasonTimeout {
		t.Fatalf("reason code = %q", code)
	}
}

// TestNilContextIsTolerated pins that a caller without a context gets a
// decision rather than a panic.
func TestNilContextIsTolerated(t *testing.T) {
	fields := mustRegistry(t, registry.FieldDefinition{
		Name: "given", Domain: "leave", DataType: registry.DataInteger,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactPassed},
	})
	resolver := mustResolver(t, fields, nil, nil, nil)
	resolution, err := resolver.Resolve(nil, []string{"given"}, map[string]any{"given": 3}, params(), nil) //nolint:staticcheck // Deliberately checking the nil-context path.
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Values["given"] != 3 {
		t.Fatalf("values = %v", resolution.Values)
	}
}

// TestRowLimitBreachAcrossSeveralFacts covers the request-wide row accounting:
// each fact stays inside the per-query limit but together they exceed it.
func TestRowLimitBreachAcrossSeveralFacts(t *testing.T) {
	manifests := map[string]dataset.Manifest{"attendance.days": mustManifest(t, "attendance.days")}
	executor, err := ruletest.NewInMemoryExecutor(manifests["attendance.days"])
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 6; index++ {
		if err := executor.Add("attendance.days", ruletest.Row{
			dataset.TenantColumn: tenant, "employeeId": "E1001",
			"workDate": validAt.AddDate(0, 0, -index-1).Format(time.DateOnly), "isLate": true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	budget := ruletest.Budget()
	budget.MaxRowsScanned = 8 // each query scans 6, so two of them exceed it
	fields := mustRegistry(t, countField("one", 10), countField("two", 20))
	resolver, err := fact.NewResolver(fields, manifests, executor, nil, budget, telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), []string{"one", "two"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonBudgetExceeded)
	if !strings.Contains(err.Error(), "in total") {
		t.Fatalf("the error should attribute the breach to the request total: %v", err)
	}
}

// TestDeclarationShapeFailures covers the resolver's defense for a declaration
// with no kind. Kind-specific incomplete recipes are rejected earlier by the
// field registry and are covered by registry tests.
func TestDeclarationShapeFailures(t *testing.T) {
	tests := []struct {
		name   string
		source registry.FactSource
		code   string
	}{
		// A kind outside the closed set is refused by the registry before a
		// resolver ever sees it, so the resolver's own check for one is
		// defence in depth rather than a reachable path.
		{"no kind at all", registry.FactSource{}, fact.ReasonUnresolvable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := mustRegistry(t, registry.FieldDefinition{
				Name: "broken", Domain: "leave", DataType: registry.DataInteger,
				OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
				FactSource: test.source,
			})
			resolver := mustResolver(t, fields, nil, nil, fact.NewNativeRegistry())
			_, err := resolver.Resolve(context.Background(), []string{"broken"}, nil, params(), nil)
			assertCode(t, err, test.code)
		})
	}
}

// TestNativeFailureAndEmptyValue covers a native function that errors and one
// that returns nothing, including the onEmpty fallback.
func TestNativeFailureAndEmptyValue(t *testing.T) {
	natives := fact.NewNativeRegistry()
	if err := natives.Register("boom", func(context.Context, fact.Params) (any, error) {
		return nil, errors.New("the store is down")
	}); err != nil {
		t.Fatal(err)
	}
	if err := natives.Register("empty", func(context.Context, fact.Params) (any, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}

	fields := mustRegistry(t,
		nativeField("failing", "boom", nil),
		nativeField("emptyNoFallback", "empty", nil),
		nativeField("emptyWithFallback", "empty", 0),
	)
	resolver := mustResolver(t, fields, nil, nil, natives)

	_, err := resolver.Resolve(context.Background(), []string{"failing"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonUnresolvable)
	if !strings.Contains(err.Error(), "the store is down") {
		t.Fatalf("the cause should be wrapped: %v", err)
	}

	_, err = resolver.Resolve(context.Background(), []string{"emptyNoFallback"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonUnresolvable)

	resolution, err := resolver.Resolve(context.Background(), []string{"emptyWithFallback"}, nil, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Values["emptyWithFallback"] != 0 {
		t.Fatalf("values = %v, want the declared onEmpty", resolution.Values)
	}
}

// TestDerivedOnEmptyAndTypeChecking covers a derived value that comes back
// nil, and one that is the wrong type for its field.
func TestDerivedOnEmptyAndTypeChecking(t *testing.T) {
	// coalesce over a passed field that is absent cannot happen — a passed
	// field must be passed — so the nil path is reached through a dataset
	// dependency with its own onEmpty of nil.
	fields := mustRegistry(t,
		registry.FieldDefinition{
			Name: "joinDate", Domain: "leave", DataType: registry.DataDate,
			OwnerService: "employee-service", Status: "active", Role: registry.RoleInput,
			FactSource: registry.FactSource{Kind: registry.FactPassed},
		},
		registry.FieldDefinition{
			// A date-typed field computed by arithmetic is a declaration
			// mistake: the expression yields a number.
			Name: "wrongType", Domain: "leave", DataType: registry.DataDate,
			OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
			FactSource: registry.FactSource{Kind: registry.FactDerived, Expression: "monthsBetween($joinDate, $validAt)"},
		},
	)
	resolver := mustResolver(t, fields, nil, nil, nil)
	_, err := resolver.Resolve(context.Background(), []string{"wrongType"},
		map[string]any{"joinDate": "2025-06-15"}, params(), nil)
	assertCode(t, err, fact.ReasonFieldTypeMismatch)
}

// TestDatasetValueCoercionFailure covers an executor returning a value the
// field's declared type cannot accept. The decision fails rather than carrying
// a value nothing downstream can use.
func TestDatasetValueCoercionFailure(t *testing.T) {
	manifests := map[string]dataset.Manifest{"attendance.days": mustManifest(t, "attendance.days")}
	executor, err := ruletest.NewInMemoryExecutor(manifests["attendance.days"])
	if err != nil {
		t.Fatal(err)
	}
	// A boolean-typed field over a row-count aggregate: the count is an
	// integer, so the declared type cannot accept it.
	fields := mustRegistry(t, registry.FieldDefinition{
		Name: "mistyped", Domain: "attendance", DataType: registry.DataBoolean,
		OwnerService: "attendance-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{
			Kind: registry.FactDataset, Dataset: "attendance.days",
			Window:    &registry.FactWindow{Column: "workDate", RelativeTo: dataset.TokenValidAt, LastDays: 7},
			Aggregate: &registry.FactAggregate{Fn: registry.AggregateCount},
			OnEmpty:   0,
		},
	})
	resolver, err := fact.NewResolver(fields, manifests, executor, nil, ruletest.Budget(), telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), []string{"mistyped"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonFieldTypeMismatch)
}

// TestCachedEmptyResultUsesOnEmpty covers the de-duplication cache holding a
// no-rows answer: the second point must get the declared onEmpty too, not a
// nil that looks like a different outcome.
func TestCachedEmptyResultUsesOnEmpty(t *testing.T) {
	resolver, _ := leaveResolver(t)
	session := resolver.NewSession()
	for point := 0; point < 2; point++ {
		resolution, err := session.Resolve(context.Background(),
			[]string{"casualLeaveTakenThisYear"}, nil, params(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if resolution.Values["casualLeaveTakenThisYear"] != "0" {
			t.Fatalf("point %d got %#v, want the declared onEmpty", point, resolution.Values["casualLeaveTakenThisYear"])
		}
	}
}

func TestNativeEmptyErrorAndLateReturnFailClosed(t *testing.T) {
	newNativeResolver := func(t *testing.T, source registry.FactSource, function fact.Func, budget fact.Budget) *fact.Resolver {
		t.Helper()
		fields := mustRegistry(t, registry.FieldDefinition{
			Name: "nativeValue", Domain: "leave", DataType: registry.DataInteger,
			OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
			FactSource: source,
		})
		natives := fact.NewNativeRegistry()
		if err := natives.Register(source.NativeRef, function); err != nil {
			t.Fatal(err)
		}
		resolver, err := fact.NewResolver(fields, nil, nil, natives, budget, telemetry.Nop())
		if err != nil {
			t.Fatal(err)
		}
		return resolver
	}

	budget := ruletest.Budget()
	for _, test := range []struct {
		name     string
		source   registry.FactSource
		function fact.Func
		want     any
		code     string
	}{
		{
			name: "declared fallback", source: registry.FactSource{Kind: registry.FactNative, NativeRef: "native.empty", OnEmpty: 7},
			function: func(context.Context, fact.Params) (any, error) { return nil, nil }, want: 7,
		},
		{
			name: "missing fallback", source: registry.FactSource{Kind: registry.FactNative, NativeRef: "native.missing"},
			function: func(context.Context, fact.Params) (any, error) { return nil, nil }, code: fact.ReasonUnresolvable,
		},
		{
			name: "builder error", source: registry.FactSource{Kind: registry.FactNative, NativeRef: "native.error"},
			function: func(context.Context, fact.Params) (any, error) { return nil, errors.New("lookup failed") }, code: fact.ReasonUnresolvable,
		},
		{
			name: "value after deadline", source: registry.FactSource{Kind: registry.FactNative, NativeRef: "native.late"},
			function: func(ctx context.Context, _ fact.Params) (any, error) { <-ctx.Done(); return 1, nil }, code: fact.ReasonTimeout,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			caseBudget := budget
			if test.name == "value after deadline" {
				caseBudget.PerFactTimeout = time.Millisecond
			}
			resolver := newNativeResolver(t, test.source, test.function, caseBudget)
			result, err := resolver.Resolve(context.Background(), []string{"nativeValue"}, nil, params(), nil)
			if test.code != "" {
				assertCode(t, err, test.code)
				return
			}
			if err != nil || result.Values["nativeValue"] != test.want {
				t.Fatalf("Resolve() = %+v, %v; want %v", result, err, test.want)
			}
		})
	}
}

type scriptedExecutor struct {
	manifest dataset.Manifest
	result   dataset.Result
	err      error
	late     bool
}

func (e scriptedExecutor) Manifests() []dataset.Manifest { return []dataset.Manifest{e.manifest} }

func (e scriptedExecutor) Run(ctx context.Context, _ dataset.Query) (dataset.Result, error) {
	if e.late {
		<-ctx.Done()
		return e.result, nil
	}
	return e.result, e.err
}

func TestDatasetExecutorMisbehaviorFailsClosed(t *testing.T) {
	manifest := mustManifest(t, "attendance.days")
	fields := mustRegistry(t, countField("one", 1))
	budget := ruletest.Budget()
	for _, test := range []struct {
		name     string
		executor scriptedExecutor
		code     string
	}{
		{
			name: "reports too many rows",
			executor: scriptedExecutor{manifest: manifest, result: dataset.Result{
				Value: 1, RowsScanned: budget.MaxRowsScanned + 1,
			}},
			code: fact.ReasonBudgetExceeded,
		},
		{
			name:     "returns an error",
			executor: scriptedExecutor{manifest: manifest, err: errors.New("database unavailable")},
			code:     fact.ReasonUnresolvable,
		},
		{
			name:     "returns after deadline",
			executor: scriptedExecutor{manifest: manifest, result: dataset.Result{Value: 1}, late: true},
			code:     fact.ReasonTimeout,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			caseBudget := budget
			if test.executor.late {
				caseBudget.PerFactTimeout = time.Millisecond
			}
			resolver, err := fact.NewResolver(fields, map[string]dataset.Manifest{manifest.DatasetID: manifest},
				test.executor, nil, caseBudget, telemetry.Nop())
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.Resolve(context.Background(), []string{"one"}, nil, params(), nil)
			assertCode(t, err, test.code)
		})
	}
}

// TestDeprecatedDatasetNotesTheDecision covers a dataset that still answers
// while it retires, so a migration can be driven from the audit trail.
func TestDeprecatedDatasetNotesTheDecision(t *testing.T) {
	manifest := mustManifest(t, "attendance.days")
	manifest.Status = dataset.Deprecated
	executor, err := ruletest.NewInMemoryExecutor(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Add("attendance.days", ruletest.Row{
		dataset.TenantColumn: tenant, "employeeId": "E1001",
		"workDate": validAt.AddDate(0, 0, -1).Format(time.DateOnly), "isLate": true,
	}); err != nil {
		t.Fatal(err)
	}
	fields := mustRegistry(t, countField("one", 7))
	resolver, err := fact.NewResolver(fields,
		map[string]dataset.Manifest{"attendance.days": manifest}, executor, nil, ruletest.Budget(), telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolver.Resolve(context.Background(), []string{"one"}, nil, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Notes) != 1 || resolution.Notes[0] != dataset.ReasonDatasetDeprecated {
		t.Fatalf("notes = %v, want %s", resolution.Notes, dataset.ReasonDatasetDeprecated)
	}
}

// TestFactsAreOrderedByField pins that the audit record is stable: two runs of
// the same request must produce the same explanation, in the same order.
func TestFactsAreOrderedByField(t *testing.T) {
	fields := mustRegistry(t, countField("zebra", 1), countField("alpha", 2), countField("middle", 3))
	manifests := map[string]dataset.Manifest{"attendance.days": mustManifest(t, "attendance.days")}
	executor, err := ruletest.NewInMemoryExecutor(manifests["attendance.days"])
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := fact.NewResolver(fields, manifests, executor, nil, ruletest.Budget(), telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolver.Resolve(context.Background(),
		[]string{"zebra", "middle", "alpha"}, nil, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "middle", "zebra"}
	if len(resolution.Facts) != len(want) {
		t.Fatalf("facts = %+v", resolution.Facts)
	}
	for index, name := range want {
		if resolution.Facts[index].Field != name {
			t.Fatalf("facts[%d] = %q, want %q", index, resolution.Facts[index].Field, name)
		}
	}
}

func nativeField(name, reference string, onEmpty any) registry.FieldDefinition {
	return registry.FieldDefinition{
		Name: name, Domain: "leave", DataType: registry.DataInteger,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactNative, NativeRef: reference, OnEmpty: onEmpty},
	}
}

// TestNotesAreDeduplicatedAndSorted pins that a decision carries each reason
// code once: two deprecated fields are one FIELD_DEPRECATED on the decision,
// not two, and the order is stable for the audit record.
func TestNotesAreDeduplicatedAndSorted(t *testing.T) {
	fields := mustRegistry(t,
		deprecatedPassedField("oldOne"),
		deprecatedPassedField("oldTwo"),
	)
	resolver := mustResolver(t, fields, nil, nil, nil)
	resolution, err := resolver.Resolve(context.Background(), []string{"oldOne", "oldTwo"},
		map[string]any{"oldOne": 1, "oldTwo": 2}, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Notes) != 1 || resolution.Notes[0] != fact.ReasonFieldDeprecated {
		t.Fatalf("notes = %v, want exactly one %s", resolution.Notes, fact.ReasonFieldDeprecated)
	}
}

// TestDerivedOverADatasetDependency pins that a formula can be written over a
// dataset fact without the author ordering anything: the dependency is
// resolved depth-first when the expression is evaluated.
func TestDerivedOverADatasetDependency(t *testing.T) {
	manifests := map[string]dataset.Manifest{"attendance.days": mustManifest(t, "attendance.days")}
	executor, err := ruletest.NewInMemoryExecutor(manifests["attendance.days"])
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if err := executor.Add("attendance.days", ruletest.Row{
			dataset.TenantColumn: tenant, "employeeId": "E1001",
			"workDate": validAt.AddDate(0, 0, -index-1).Format(time.DateOnly), "isLate": true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	fields := mustRegistry(t,
		countField("lateDays", 7),
		registry.FieldDefinition{
			Name: "lateDaysPlusOne", Domain: "attendance", DataType: registry.DataInteger,
			OwnerService: "attendance-service", Status: "active", Role: registry.RoleInput,
			FactSource: registry.FactSource{Kind: registry.FactDerived, Expression: "$lateDays + 1"},
		},
	)
	resolver, err := fact.NewResolver(fields, manifests, executor, nil, ruletest.Budget(), telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolver.Resolve(context.Background(), []string{"lateDaysPlusOne"}, nil, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := decimalText(resolution.Values["lateDaysPlusOne"]); got != "4" {
		t.Fatalf("lateDaysPlusOne = %s, want 4", got)
	}
	// The dependency is recorded too, so the audit record explains the whole
	// chain rather than only the field the rule asked for.
	if len(resolution.Facts) != 2 {
		t.Fatalf("facts = %+v, want the derived field and its dependency", resolution.Facts)
	}
}

func deprecatedPassedField(name string) registry.FieldDefinition {
	return registry.FieldDefinition{
		Name: name, Domain: "leave", DataType: registry.DataInteger,
		OwnerService: "leave-service", Status: "deprecated", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactPassed},
	}
}
