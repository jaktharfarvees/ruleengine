package fact_test

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
	"github.com/arrowhr/ruleengine/telemetry"
)

// validAt is the business time every fact test resolves at. The attendance and
// leave rows below are positioned relative to it.
var validAt = time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC)

const tenant = "RWB"

func params() fact.Params {
	return fact.Params{
		Tenant: tenant, EmployeeID: "E1001", NodeID: "BR001",
		AsOf: validAt, ValidAt: validAt,
	}
}

// TestFT01StreakOverADataset covers FT01: countConsecutiveLate must return the
// run of late days ending at validAt, and zero when the most recent day is not
// late. Zero is the case that matters: a streak that counts every late day in
// the window instead of the consecutive ones would silently penalise someone.
func TestFT01StreakOverADataset(t *testing.T) {
	tests := []struct {
		name string
		late []bool // most recent first
		want int
	}{
		{"three consecutive late days", []bool{true, true, true, false, true}, 3},
		{"most recent day is not late", []bool{false, true, true, true}, 0},
		{"one late day", []bool{true, false}, 1},
		{"never late", []bool{false, false}, 0},
		{"every day late", []bool{true, true}, 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver, executor := attendanceResolver(t)
			addAttendance(t, executor, test.late)
			resolution := mustResolve(t, resolver, []string{"countConsecutiveLate"}, nil)
			if got := resolution.Values["countConsecutiveLate"]; got != test.want {
				t.Fatalf("countConsecutiveLate = %v, want %d", got, test.want)
			}
			if resolution.Facts[0].Kind != fact.KindDataset || resolution.Facts[0].Dataset != "attendance.days" {
				t.Fatalf("fact record = %+v", resolution.Facts[0])
			}
		})
	}
}

// TestFT02SumWithAWindow covers FT02: a windowed sum must add only the rows
// inside the resolved window. A leave day from last year must not count
// against this year's entitlement.
func TestFT02SumWithAWindow(t *testing.T) {
	resolver, executor := leaveResolver(t)
	// Inside the 365-day window.
	addLeave(t, executor, validAt.AddDate(0, -1, 0), "CASUAL", "APPROVED", "2")
	addLeave(t, executor, validAt.AddDate(0, -6, 0), "CASUAL", "APPROVED", "1.5")
	// Outside it, by two days.
	addLeave(t, executor, validAt.AddDate(0, 0, -367), "CASUAL", "APPROVED", "4")
	// Inside, but the wrong leave type and the wrong status.
	addLeave(t, executor, validAt.AddDate(0, -2, 0), "ANNUAL", "APPROVED", "8")
	addLeave(t, executor, validAt.AddDate(0, -2, 0), "CASUAL", "PENDING", "8")

	resolution := mustResolve(t, resolver, []string{"casualLeaveTakenThisYear"}, nil)
	if got := decimalText(resolution.Values["casualLeaveTakenThisYear"]); got != "3.5" {
		t.Fatalf("casualLeaveTakenThisYear = %s, want 3.5", got)
	}
}

// TestFT03OnEmptyApplies covers FT03: a subject with no rows gets the declared
// onEmpty, not a nil and not a guessed zero. The distinction matters because a
// guessed zero reads as "took no leave" when the truth is "we do not know".
func TestFT03OnEmptyApplies(t *testing.T) {
	resolver, _ := leaveResolver(t)
	resolution := mustResolve(t, resolver, []string{"casualLeaveTakenThisYear"}, nil)
	if got := resolution.Values["casualLeaveTakenThisYear"]; got != "0" {
		t.Fatalf("casualLeaveTakenThisYear = %#v, want the declared onEmpty \"0\"", got)
	}

	// A field with no onEmpty fails closed rather than inventing a value.
	fields := mustRegistry(t, registry.FieldDefinition{
		Name: "noFallback", Domain: "leave", DataType: registry.DataDecimal,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{
			Kind: registry.FactDataset, Dataset: "leave.balances",
			Window:    &registry.FactWindow{Column: "asOfDate", RelativeTo: dataset.TokenValidAt, LastDays: 30},
			Aggregate: &registry.FactAggregate{Fn: registry.AggregateLatest, On: "balanceDays"},
		},
	})
	executor, err := ruletest.NewInMemoryExecutor(mustManifest(t, "leave.balances"))
	if err != nil {
		t.Fatal(err)
	}
	resolver = mustResolver(t, fields, map[string]dataset.Manifest{
		"leave.balances": mustManifest(t, "leave.balances"),
	}, executor, nil)
	_, err = resolver.Resolve(context.Background(), []string{"noFallback"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonUnresolvable)
}

// TestFT04NonFilterableColumnRefused covers FT04. workedHours is deliberately
// not filterable: the attendance service has promised no index for it, so a
// filter on it would be a table scan in production.
func TestFT04NonFilterableColumnRefused(t *testing.T) {
	_, _, err := compileAttendance(t, registry.FactSource{
		Kind: registry.FactDataset, Dataset: "attendance.days",
		Filters:   []rule.FilterCondition{{Attribute: "workedHours", Op: rule.FilterEquals, Value: "8"}},
		Aggregate: &registry.FactAggregate{Fn: registry.AggregateCount},
	})
	assertCompileCode(t, err, dataset.ReasonColumnNotAllowed, "workedHours")
}

// TestFT05UnsupportedAggregateRefused covers FT05. payroll.overtime
// deliberately omits streak, so asking for one is refused rather than faked by
// scanning the dataset's whole history.
func TestFT05UnsupportedAggregateRefused(t *testing.T) {
	manifest := mustManifest(t, "payroll.overtime")
	_, _, err := dataset.Compile(registry.FactSource{
		Kind: registry.FactDataset, Dataset: "payroll.overtime",
		Window:    &registry.FactWindow{Column: "periodStart", RelativeTo: dataset.TokenValidAt, LastDays: 30},
		Aggregate: &registry.FactAggregate{Fn: registry.AggregateStreak, On: "otHours"},
	}, manifest, compileParams())
	assertCompileCode(t, err, dataset.ReasonAggregateUnsupported, "")
	if !strings.Contains(err.Error(), "payroll.overtime") || !strings.Contains(err.Error(), "streak") {
		t.Fatalf("error should name the dataset and the aggregate: %v", err)
	}
}

// TestFT06OverWideWindowRefused covers FT06, including that the message names
// both numbers so an author can fix it without reading documentation.
func TestFT06OverWideWindowRefused(t *testing.T) {
	_, _, err := compileAttendance(t, registry.FactSource{
		Kind: registry.FactDataset, Dataset: "attendance.days",
		Window:    &registry.FactWindow{Column: "workDate", RelativeTo: dataset.TokenValidAt, LastDays: 400},
		Aggregate: &registry.FactAggregate{Fn: registry.AggregateCount},
	})
	assertCompileCode(t, err, dataset.ReasonWindowTooWide, "workDate")
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "365") {
		t.Fatalf("error should name both 400 and 365: %v", err)
	}

	// A zero or negative window is refused too: an unbounded streak is not a
	// streak, it is the whole table.
	for _, lastDays := range []int{0, -1} {
		_, _, err := compileAttendance(t, registry.FactSource{
			Kind: registry.FactDataset, Dataset: "attendance.days",
			Window:    &registry.FactWindow{Column: "workDate", RelativeTo: dataset.TokenValidAt, LastDays: lastDays},
			Aggregate: &registry.FactAggregate{Fn: registry.AggregateCount},
		})
		assertCompileCode(t, err, dataset.ReasonWindowTooWide, "workDate")
	}
}

// TestFT07InjectionCorpusIsRefused covers FT07. The corpus is committed under
// testdata/security so that a change to the guard has to face every string in
// it again.
func TestFT07InjectionCorpusIsRefused(t *testing.T) {
	corpus := loadInjectionCorpus(t)
	if len(corpus) < 20 {
		t.Fatalf("injection corpus holds %d entries, want at least 20", len(corpus))
	}
	manifest := mustManifest(t, "attendance.days")
	for _, entry := range corpus {
		t.Run(entry, func(t *testing.T) {
			_, _, err := dataset.Compile(registry.FactSource{
				Kind: registry.FactDataset, Dataset: "attendance.days",
				Filters:   []rule.FilterCondition{{Attribute: "shiftCode", Op: rule.FilterEquals, Value: entry}},
				Aggregate: &registry.FactAggregate{Fn: registry.AggregateCount},
			}, manifest, compileParams())
			if err == nil {
				t.Fatalf("compiled a query from %q", entry)
			}
			if code := fact.Code(err); code != dataset.ReasonRawQueryRejected {
				t.Fatalf("%q was refused as %s, want %s: %v", entry, code, dataset.ReasonRawQueryRejected, err)
			}
		})
	}
}

// TestFT08TenantPredicateAlwaysInjected covers FT08: every compiled query
// carries exactly one injected tenant predicate, and an author filtering on
// the tenant column is an error rather than something to merge.
func TestFT08TenantPredicateAlwaysInjected(t *testing.T) {
	sources := []registry.FactSource{
		{Kind: registry.FactDataset, Dataset: "attendance.days",
			Aggregate: &registry.FactAggregate{Fn: registry.AggregateCount}},
		{Kind: registry.FactDataset, Dataset: "attendance.days",
			Filters:   []rule.FilterCondition{{Attribute: "employeeId", Op: rule.FilterEquals, Value: dataset.TokenEmployeeID}},
			Window:    &registry.FactWindow{Column: "workDate", RelativeTo: dataset.TokenValidAt, LastDays: 7},
			Aggregate: &registry.FactAggregate{Fn: registry.AggregateStreak, On: "isLate"}},
	}
	for _, source := range sources {
		query, _, err := compileAttendance(t, source)
		if err != nil {
			t.Fatal(err)
		}
		injected := 0
		for _, predicate := range query.Predicates() {
			if predicate.Injected {
				injected++
				if predicate.Column != dataset.TenantColumn || len(predicate.Values) != 1 || predicate.Values[0] != tenant {
					t.Fatalf("injected predicate = %+v", predicate)
				}
			}
		}
		if injected != 1 {
			t.Fatalf("query carries %d injected predicates, want exactly 1", injected)
		}
		if _, present := query.TenantPredicate(); !present {
			t.Fatal("TenantPredicate reported none on a compiled query")
		}
	}

	// An authored tenant filter is refused outright.
	_, _, err := compileAttendance(t, registry.FactSource{
		Kind: registry.FactDataset, Dataset: "attendance.days",
		Filters:   []rule.FilterCondition{{Attribute: dataset.TenantColumn, Op: rule.FilterEquals, Value: "OTHER"}},
		Aggregate: &registry.FactAggregate{Fn: registry.AggregateCount},
	})
	assertCompileCode(t, err, dataset.ReasonColumnNotAllowed, dataset.TenantColumn)

	// A tenant's rows must not reach another tenant, which is the behaviour the
	// predicate exists to produce.
	resolver, executor := attendanceResolver(t)
	addAttendance(t, executor, []bool{true, true})
	if err := executor.Add("attendance.days", ruletest.Row{
		dataset.TenantColumn: "EARROW", "employeeId": "E1001",
		"workDate": validAt.AddDate(0, 0, -1).Format(time.DateOnly), "isLate": true,
	}); err != nil {
		t.Fatal(err)
	}
	resolution := mustResolve(t, resolver, []string{"countConsecutiveLate"}, nil)
	if got := resolution.Values["countConsecutiveLate"]; got != 2 {
		t.Fatalf("countConsecutiveLate = %v, want 2; another tenant's row leaked in", got)
	}
}

// TestFT09DerivedCycleDetected covers FT09: a cycle is reported, not recursed
// into. A stack overflow during a payroll run would take the process down.
func TestFT09DerivedCycleDetected(t *testing.T) {
	fields := mustRegistry(t,
		derivedField("alpha", "$beta + 1"),
		derivedField("beta", "$alpha + 1"),
	)
	resolver := mustResolver(t, fields, nil, nil, nil)
	_, err := resolver.Resolve(context.Background(), []string{"alpha"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonDerivedCycle)
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("the error should name both fields in the loop: %v", err)
	}

	// A field referring to itself is the same failure.
	fields = mustRegistry(t, derivedField("solo", "$solo + 1"))
	resolver = mustResolver(t, fields, nil, nil, nil)
	_, err = resolver.Resolve(context.Background(), []string{"solo"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonDerivedCycle)
}

// TestFT10DerivedGrammarIsClosed covers FT10. The grammar admits numbers,
// dates, registered field references, arithmetic and an allowlisted function,
// and nothing else.
func TestFT10DerivedGrammarIsClosed(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		code       string
	}{
		{"unregistered identifier", "$notAField + 1", fact.ReasonFieldNotRegistered},
		{"unlisted function", "sqrt($joinDate)", fact.ReasonUnresolvable},
		// A derived field is arithmetic over numbers and dates. A quoted
		// literal is refused by the same guard that refuses query syntax,
		// which is a louder and more accurate answer than a type mismatch
		// three steps later.
		{"string literal", `"ANNUAL"`, dataset.ReasonRawQueryRejected},
		{"single-quoted literal", `'ANNUAL'`, dataset.ReasonRawQueryRejected},
		{"statement smuggling", "1; DROP TABLE x", dataset.ReasonRawQueryRejected},
		{"empty expression", "   ", fact.ReasonUnresolvable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := mustRegistry(t,
				derivedField("computed", test.expression),
				registry.FieldDefinition{
					Name: "joinDate", Domain: "leave", DataType: registry.DataDate,
					OwnerService: "employee-service", Status: "active", Role: registry.RoleInput,
					FactSource: registry.FactSource{Kind: registry.FactPassed},
				},
			)
			resolver := mustResolver(t, fields, nil, nil, nil)
			_, err := resolver.Resolve(context.Background(), []string{"computed"}, nil, params(), nil)
			assertCode(t, err, test.code)
		})
	}

	// The allowlisted shape works, over a passed dependency.
	fields := mustRegistry(t,
		registry.FieldDefinition{
			Name: "joinDate", Domain: "leave", DataType: registry.DataDate,
			OwnerService: "employee-service", Status: "active", Role: registry.RoleInput,
			FactSource: registry.FactSource{Kind: registry.FactPassed},
		},
		registry.FieldDefinition{
			Name: "serviceMonths", Domain: "leave", DataType: registry.DataInteger,
			OwnerService: "employee-service", Status: "active", Role: registry.RoleInput,
			FactSource: registry.FactSource{Kind: registry.FactDerived, Expression: "monthsBetween($joinDate, $validAt)"},
		},
	)
	resolver := mustResolver(t, fields, nil, nil, nil)
	resolution, err := resolver.Resolve(context.Background(), []string{"serviceMonths"},
		map[string]any{"joinDate": "2025-06-15"}, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := decimalText(resolution.Values["serviceMonths"]); got != "12" {
		t.Fatalf("serviceMonths = %s, want 12", got)
	}
}

// TestFT11BudgetExceededFailsClosed covers FT11: exceeding the dataset-fact
// budget fails the whole decision and returns no partial fact set.
func TestFT11BudgetExceededFailsClosed(t *testing.T) {
	fields := mustRegistry(t, countField("one", 1), countField("two", 2), countField("three", 3))
	manifests := map[string]dataset.Manifest{"attendance.days": mustManifest(t, "attendance.days")}
	executor, err := ruletest.NewInMemoryExecutor(manifests["attendance.days"])
	if err != nil {
		t.Fatal(err)
	}
	budget := ruletest.Budget()
	budget.MaxDatasetFacts = 2
	resolver, err := fact.NewResolver(fields, manifests, executor, nil, budget, telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolver.Resolve(context.Background(), []string{"one", "two", "three"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonBudgetExceeded)
	if resolution.Values != nil {
		t.Fatalf("a budget breach returned a partial fact set: %v", resolution.Values)
	}

	// A row-limit breach is the same failure. The executor reports more rows
	// than the limit, so the decision fails rather than trusting the value.
	budget = ruletest.Budget()
	budget.MaxRowsScanned = 1
	resolver, err = fact.NewResolver(fields, manifests, executor, nil, budget, telemetry.Nop())
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
	_, err = resolver.Resolve(context.Background(), []string{"one"}, nil, params(), nil)
	if err == nil {
		t.Fatal("a query over more rows than the limit was allowed")
	}
}

// TestFT12TimeoutFailsClosed covers FT12: an executor that sleeps past the
// per-fact timeout yields FACT_TIMEOUT, and the context it was given is
// cancelled rather than left running.
func TestFT12TimeoutFailsClosed(t *testing.T) {
	fields := mustRegistry(t, countField("one", 1))
	manifests := map[string]dataset.Manifest{"attendance.days": mustManifest(t, "attendance.days")}
	executor, err := ruletest.NewInMemoryExecutor(manifests["attendance.days"])
	if err != nil {
		t.Fatal(err)
	}
	executor.SetDelay(200 * time.Millisecond)
	budget := ruletest.Budget()
	budget.PerFactTimeout = 20 * time.Millisecond
	resolver, err := fact.NewResolver(fields, manifests, executor, nil, budget, telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), []string{"one"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonTimeout)

	// A native fact handed a cancelled context sees it.
	natives := fact.NewNativeRegistry()
	var observed error
	if err := natives.Register("slow", func(ctx context.Context, _ fact.Params) (any, error) {
		<-ctx.Done()
		observed = ctx.Err()
		return nil, ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	fields = mustRegistry(t, registry.FieldDefinition{
		Name: "slowFact", Domain: "leave", DataType: registry.DataInteger,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactNative, NativeRef: "slow"},
	})
	resolver, err = fact.NewResolver(fields, nil, nil, natives, budget, telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), []string{"slowFact"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonTimeout)
	if !errors.Is(observed, context.DeadlineExceeded) {
		t.Fatalf("the native function observed %v, want a deadline", observed)
	}
}

// TestFT13DeDuplicationAcrossPoints covers FT13: several decision points
// sharing a fact execute the query once, and the reuse is recorded.
func TestFT13DeDuplicationAcrossPoints(t *testing.T) {
	resolver, executor := attendanceResolver(t)
	addAttendance(t, executor, []bool{true, true, false})
	counting := &countingExecutor{Executor: executor}
	resolver = mustResolver(t, mustRegistry(t, countField("one", 1), countField("two", 2)),
		map[string]dataset.Manifest{"attendance.days": mustManifest(t, "attendance.days")}, counting, nil)

	// One session, four points, two distinct facts between them.
	session := resolver.NewSession()
	for point := 0; point < 4; point++ {
		resolution, err := session.Resolve(context.Background(), []string{"one", "two"}, nil, params(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if point > 0 {
			for _, resolved := range resolution.Facts {
				if !resolved.Cached {
					t.Fatalf("point %d refetched %q instead of reusing it", point, resolved.Field)
				}
			}
		}
	}
	if counting.calls != 2 {
		t.Fatalf("executor ran %d queries for two distinct facts across four points, want 2", counting.calls)
	}
}

// TestFT14RequestSuppliedFactIsNotFetched covers FT14.
func TestFT14RequestSuppliedFactIsNotFetched(t *testing.T) {
	resolver, executor := attendanceResolver(t)
	counting := &countingExecutor{Executor: executor}
	resolver = mustResolver(t, mustRegistry(t, countField("one", 1)),
		map[string]dataset.Manifest{"attendance.days": mustManifest(t, "attendance.days")}, counting, nil)
	resolution, err := resolver.Resolve(context.Background(), []string{"one"},
		map[string]any{"one": 7}, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolution.Values["one"]; got != 7 {
		t.Fatalf("one = %v, want the supplied 7", got)
	}
	if !resolution.Facts[0].FromRequest {
		t.Fatal("a supplied fact was not recorded as coming from the request")
	}
	if counting.calls != 0 {
		t.Fatalf("executor was called %d times for a supplied fact", counting.calls)
	}

	// A supplied value of the wrong type is a caller bug worth failing on.
	_, err = resolver.Resolve(context.Background(), []string{"one"},
		map[string]any{"one": "seven"}, params(), nil)
	assertCode(t, err, fact.ReasonFieldTypeMismatch)
}

// TestFT15PinsAreHonoured covers FT15: a pinned fact-source version is
// recorded even when the registry declares a newer one, so a replay can tell
// which recipe produced a figure.
func TestFT15PinsAreHonoured(t *testing.T) {
	resolver, executor := attendanceResolver(t)
	addAttendance(t, executor, []bool{true})
	resolution := mustResolve(t, resolver, []string{"countConsecutiveLate"},
		map[string]string{"countConsecutiveLate": "0.9.0"})
	if got := resolution.Facts[0].FactSourceVersion; got != "0.9.0" {
		t.Fatalf("fact source version = %q, want the pinned 0.9.0", got)
	}

	// Without a pin the registry's own version is recorded.
	resolution = mustResolve(t, resolver, []string{"countConsecutiveLate"}, nil)
	if got := resolution.Facts[0].FactSourceVersion; got != "1" {
		t.Fatalf("fact source version = %q, want the declared 1", got)
	}
}

// TestFT16FingerprintStability covers FT16: filter order does not change a
// query's identity, and any semantic difference does.
func TestFT16FingerprintStability(t *testing.T) {
	employee := rule.FilterCondition{Attribute: "employeeId", Op: rule.FilterEquals, Value: dataset.TokenEmployeeID}
	shift := rule.FilterCondition{Attribute: "shiftCode", Op: rule.FilterEquals, Value: "MORNING"}
	base := registry.FactSource{
		Kind: registry.FactDataset, Dataset: "attendance.days",
		Filters:   []rule.FilterCondition{employee, shift},
		Window:    &registry.FactWindow{Column: "workDate", RelativeTo: dataset.TokenValidAt, LastDays: 30},
		Aggregate: &registry.FactAggregate{Fn: registry.AggregateCount},
	}
	swapped := base
	swapped.Filters = []rule.FilterCondition{shift, employee}

	first, _, err := compileAttendance(t, base)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := compileAttendance(t, swapped)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Fatal("filter order changed the fingerprint; the same question must have one identity")
	}

	differences := map[string]func(*registry.FactSource){
		"a different window":    func(s *registry.FactSource) { s.Window.LastDays = 29 },
		"a different aggregate": func(s *registry.FactSource) { s.Aggregate.Fn = registry.AggregateExists },
		"a different filter":    func(s *registry.FactSource) { s.Filters[1].Value = "EVENING" },
		"an extra filter": func(s *registry.FactSource) {
			s.Filters = append(s.Filters, rule.FilterCondition{Attribute: "isLate", Op: rule.FilterEquals, Value: true})
		},
		"a different window column": func(s *registry.FactSource) { s.Window.Column = "workDate" },
	}
	seen := map[string]string{first.Fingerprint(): "baseline"}
	for name, change := range differences {
		variant := cloneSource(base)
		change(&variant)
		query, _, err := compileAttendance(t, variant)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		fingerprint := query.Fingerprint()
		if previous, clash := seen[fingerprint]; clash && name != "a different window column" {
			t.Fatalf("%s shares a fingerprint with %s", name, previous)
		}
		seen[fingerprint] = name
	}

	// A different tenant is a different query, which is what keeps the
	// de-duplication cache from serving one tenant's fact to another.
	manifest := mustManifest(t, "attendance.days")
	other := compileParams()
	other.Tenant = "EARROW"
	elsewhere, _, err := dataset.Compile(base, manifest, other)
	if err != nil {
		t.Fatal(err)
	}
	if elsewhere.Fingerprint() == first.Fingerprint() {
		t.Fatal("two tenants share a query fingerprint")
	}
}

// TestPassedFactMustBePassed pins the rule that a passed fact which was not
// passed is a caller bug rather than an empty result, so onEmpty is
// deliberately not substituted for it.
func TestPassedFactMustBePassed(t *testing.T) {
	fields := mustRegistry(t, registry.FieldDefinition{
		Name: "joinDate", Domain: "leave", DataType: registry.DataDate,
		OwnerService: "employee-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactPassed, OnEmpty: "1970-01-01"},
	})
	resolver := mustResolver(t, fields, nil, nil, nil)
	_, err := resolver.Resolve(context.Background(), []string{"joinDate"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonUnresolvable)
	if !strings.Contains(err.Error(), "passed") {
		t.Fatalf("the error should say the field is declared as passed: %v", err)
	}
}

// TestUnregisteredAndDeprecatedFields pins that an unregistered field is fatal
// and never null, while a deprecated one resolves with a note.
func TestUnregisteredAndDeprecatedFields(t *testing.T) {
	resolver, _ := leaveResolver(t)
	_, err := resolver.Resolve(context.Background(), []string{"notAField"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonFieldNotRegistered)

	fields := mustRegistry(t, registry.FieldDefinition{
		Name: "oldField", Domain: "leave", DataType: registry.DataInteger,
		OwnerService: "leave-service", Status: "deprecated", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactPassed},
	})
	resolver = mustResolver(t, fields, nil, nil, nil)
	resolution, err := resolver.Resolve(context.Background(), []string{"oldField"},
		map[string]any{"oldField": 1}, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Notes) != 1 || resolution.Notes[0] != fact.ReasonFieldDeprecated {
		t.Fatalf("notes = %v, want %s", resolution.Notes, fact.ReasonFieldDeprecated)
	}
}

// TestUnresolvableExampleFixture pins the committed fixture whose whole purpose
// is to give FACT_UNRESOLVABLE a shared example.
func TestUnresolvableExampleFixture(t *testing.T) {
	resolver, _ := leaveResolver(t)
	_, err := resolver.Resolve(context.Background(), []string{"unresolvableExample"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonUnresolvable)
}

// TestNativeFactResolution pins the native kind end to end, including that a
// missing registration names the owning service's omission.
func TestNativeFactResolution(t *testing.T) {
	natives := fact.NewNativeRegistry()
	if err := natives.Register("leave.accruedBalanceToDate", func(_ context.Context, p fact.Params) (any, error) {
		if p.Tenant != tenant || p.EmployeeID != "E1001" || !p.ValidAt.Equal(validAt) {
			return nil, errors.New("params were not passed through")
		}
		return "12.5", nil
	}); err != nil {
		t.Fatal(err)
	}
	fields := mustRegistry(t, registry.FieldDefinition{
		Name: "accruedBalanceToDate", Domain: "leave", DataType: registry.DataDecimal,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactNative, NativeRef: "leave.accruedBalanceToDate"},
	})
	resolver := mustResolver(t, fields, nil, nil, natives)
	resolution, err := resolver.Resolve(context.Background(), []string{"accruedBalanceToDate"}, nil, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolution.Values["accruedBalanceToDate"]; got != "12.5" {
		t.Fatalf("accruedBalanceToDate = %v", got)
	}

	// Registering twice is a wiring mistake, not a replacement.
	if err := natives.Register("leave.accruedBalanceToDate", func(context.Context, fact.Params) (any, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("a native fact was registered twice")
	}

	// A field naming an unregistered function says whose job it is.
	resolver = mustResolver(t, fields, nil, nil, fact.NewNativeRegistry())
	_, err = resolver.Resolve(context.Background(), []string{"accruedBalanceToDate"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonUnresolvable)
	if !strings.Contains(err.Error(), "owning service") {
		t.Fatalf("the error should name the owning service's omission: %v", err)
	}
}

// TestBudgetMustBeConfigured pins that an unset budget is a configuration
// error rather than a silent default.
func TestBudgetMustBeConfigured(t *testing.T) {
	fields := mustRegistry(t, countField("one", 1))
	if _, err := fact.NewResolver(fields, nil, nil, nil, fact.Budget{}, nil); err == nil {
		t.Fatal("an unset budget was accepted")
	}
	tests := []struct {
		name   string
		budget fact.Budget
	}{
		{"no dataset facts", fact.Budget{PerFactTimeout: time.Second, TotalFactTimeout: time.Second, MaxRowsScanned: 1}},
		{"no per-fact timeout", fact.Budget{MaxDatasetFacts: 1, TotalFactTimeout: time.Second, MaxRowsScanned: 1}},
		{"no total timeout", fact.Budget{MaxDatasetFacts: 1, PerFactTimeout: time.Second, MaxRowsScanned: 1}},
		{"no row limit", fact.Budget{MaxDatasetFacts: 1, PerFactTimeout: time.Second, TotalFactTimeout: time.Second}},
		{"total shorter than per-fact", fact.Budget{MaxDatasetFacts: 1, PerFactTimeout: 2 * time.Second, TotalFactTimeout: time.Second, MaxRowsScanned: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.budget.Validate(); err == nil {
				t.Fatal("an incomplete budget validated")
			}
		})
	}
	if err := ruletest.Budget().Validate(); err != nil {
		t.Fatalf("the shared test budget is invalid: %v", err)
	}
}

// TestResolverRejectsInconsistentManifests pins that a resolver cannot be
// built over a manifest that is invalid or keyed under the wrong name.
func TestResolverRejectsInconsistentManifests(t *testing.T) {
	fields := mustRegistry(t, countField("one", 1))
	good := mustManifest(t, "attendance.days")
	if _, err := fact.NewResolver(fields, map[string]dataset.Manifest{"wrong.key": good},
		nil, nil, ruletest.Budget(), nil); err == nil {
		t.Fatal("a miskeyed manifest was accepted")
	}
	broken := good
	broken.OwnerService, broken.SourceService = "", ""
	if _, err := fact.NewResolver(fields, map[string]dataset.Manifest{"attendance.days": broken},
		nil, nil, ruletest.Budget(), nil); err == nil {
		t.Fatal("an invalid manifest was accepted")
	}
	if _, err := fact.NewResolver(nil, nil, nil, nil, ruletest.Budget(), nil); err == nil {
		t.Fatal("a resolver was built with no registry")
	}
}

// TestDatasetFactNeedsAnExecutor pins that a dataset field without an executor
// fails closed rather than silently resolving to nothing.
func TestDatasetFactNeedsAnExecutor(t *testing.T) {
	fields := mustRegistry(t, countField("one", 1))
	resolver := mustResolver(t, fields,
		map[string]dataset.Manifest{"attendance.days": mustManifest(t, "attendance.days")}, nil, nil)
	_, err := resolver.Resolve(context.Background(), []string{"one"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonUnresolvable)

	// An unpublished dataset is a different, equally loud failure.
	resolver = mustResolver(t, fields, nil, nil, nil)
	_, err = resolver.Resolve(context.Background(), []string{"one"}, nil, params(), nil)
	assertCode(t, err, fact.ReasonDatasetNotPublished)
}

// TestExplainReadsAsASentence pins the authoring-preview text, which is what
// an author who cannot read a decision table checks instead.
func TestExplainReadsAsASentence(t *testing.T) {
	query, _, err := compileAttendance(t, registry.FactSource{
		Kind: registry.FactDataset, Dataset: "attendance.days",
		Filters:   []rule.FilterCondition{{Attribute: "employeeId", Op: rule.FilterEquals, Value: dataset.TokenEmployeeID}},
		Window:    &registry.FactWindow{Column: "workDate", RelativeTo: dataset.TokenValidAt, LastDays: 30},
		Aggregate: &registry.FactAggregate{Fn: registry.AggregateStreak, On: "isLate", Unit: "days"},
	})
	if err != nil {
		t.Fatal(err)
	}
	explanation := query.Explain()
	for _, want := range []string{"consecutive", "attendance.days", "employeeId is E1001", "workDate", "scoped to this tenant"} {
		if !strings.Contains(explanation, want) {
			t.Fatalf("Explain() = %q, missing %q", explanation, want)
		}
	}
	// The injected predicate is described as tenant scoping rather than listed
	// as one of the author's filters, because the author did not write it.
	if strings.Contains(explanation, dataset.TenantColumn) {
		t.Fatalf("Explain() exposes the tenant column as an authored filter: %q", explanation)
	}
}

// TestInMemoryExecutorPassesConformance runs the published suite against the
// reference executor, so the suite itself is known to be satisfiable.
func TestInMemoryExecutorPassesConformance(t *testing.T) {
	for _, name := range []string{"attendance.days", "leave.applications", "leave.balances", "payroll.overtime"} {
		manifest := mustManifest(t, name)
		t.Run(name, func(t *testing.T) {
			dataset.ConformanceSuite(t, func() dataset.Executor {
				executor, err := ruletest.NewInMemoryExecutor(manifest)
				if err != nil {
					t.Fatal(err)
				}
				return executor
			}, manifest)
		})
	}
}

// TestConformanceSuiteCatchesAnUnsafeExecutor is the test of the test: an
// executor that ignores the tenant predicate must fail the suite. Without
// this, the suite could pass everything and prove nothing.
func TestConformanceSuiteCatchesAnUnsafeExecutor(t *testing.T) {
	manifest := mustManifest(t, "attendance.days")
	recorder := &recordingTB{TB: t}
	dataset.ConformanceSuite(recorder, func() dataset.Executor {
		return &unsafeExecutor{manifest: manifest}
	}, manifest)
	if !recorder.failed {
		t.Fatal("the conformance suite passed an executor that ignores the tenant predicate")
	}
	if !strings.Contains(recorder.message, "tenant predicate") {
		t.Fatalf("the failure should name the tenant predicate: %q", recorder.message)
	}
}

// --- helpers ---

func attendanceResolver(t *testing.T) (*fact.Resolver, *ruletest.InMemoryExecutor) {
	t.Helper()
	manifest := mustManifest(t, "attendance.days")
	executor, err := ruletest.NewInMemoryExecutor(manifest)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := ruletest.LoadRegistry("attendance")
	if err != nil {
		t.Fatal(err)
	}
	return mustResolver(t, fields, map[string]dataset.Manifest{"attendance.days": manifest}, executor, nil), executor
}

func leaveResolver(t *testing.T) (*fact.Resolver, *ruletest.InMemoryExecutor) {
	t.Helper()
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
	return mustResolver(t, fields, manifests, executor, nil), executor
}

func mustResolver(
	t *testing.T, fields *registry.Registry, manifests map[string]dataset.Manifest,
	executor dataset.Executor, natives *fact.NativeRegistry,
) *fact.Resolver {
	t.Helper()
	resolver, err := fact.NewResolver(fields, manifests, executor, natives, ruletest.Budget(), telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func mustRegistry(t *testing.T, fields ...registry.FieldDefinition) *registry.Registry {
	t.Helper()
	built, err := registry.NewRegistry(fields)
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func mustManifest(t *testing.T, name string) dataset.Manifest {
	t.Helper()
	manifest, err := ruletest.LoadManifest(name)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func mustResolve(t *testing.T, resolver *fact.Resolver, fields []string, pins map[string]string) fact.Resolution {
	t.Helper()
	resolution, err := resolver.Resolve(context.Background(), fields, nil, params(), pins)
	if err != nil {
		t.Fatal(err)
	}
	return resolution
}

func compileParams() dataset.Params {
	return dataset.Params{
		Tenant: tenant, EmployeeID: "E1001", NodeID: "BR001",
		AsOf: validAt, ValidAt: validAt, RowLimit: 500,
	}
}

func compileAttendance(t *testing.T, source registry.FactSource) (dataset.Query, []string, error) {
	t.Helper()
	return dataset.Compile(source, mustManifest(t, "attendance.days"), compileParams())
}

func cloneSource(source registry.FactSource) registry.FactSource {
	copied := source
	copied.Filters = append([]rule.FilterCondition(nil), source.Filters...)
	if source.Window != nil {
		window := *source.Window
		copied.Window = &window
	}
	if source.Aggregate != nil {
		aggregate := *source.Aggregate
		copied.Aggregate = &aggregate
	}
	return copied
}

// addAttendance writes one row per entry, most recent first, ending the day
// before validAt so every row sits inside a 30-day window.
func addAttendance(t *testing.T, executor *ruletest.InMemoryExecutor, late []bool) {
	t.Helper()
	for index, isLate := range late {
		minutes := 0
		if isLate {
			minutes = 30 + index
		}
		if err := executor.Add("attendance.days", ruletest.Row{
			dataset.TenantColumn: tenant,
			"employeeId":         "E1001",
			"workDate":           validAt.AddDate(0, 0, -index-1).Format(time.DateOnly),
			"isLate":             isLate,
			"lateMinutes":        minutes,
			"isAbsent":           false,
			"workedHours":        "8",
			"shiftCode":          "MORNING",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func addLeave(t *testing.T, executor *ruletest.InMemoryExecutor, start time.Time, leaveType, status, days string) {
	t.Helper()
	if err := executor.Add("leave.applications", ruletest.Row{
		dataset.TenantColumn: tenant,
		"employeeId":         "E1001",
		"leaveType":          leaveType,
		"status":             status,
		"startDate":          start.Format(time.DateOnly),
		"endDate":            start.Format(time.DateOnly),
		"days":               days,
		"isHalfDay":          false,
	}); err != nil {
		t.Fatal(err)
	}
}

func derivedField(name, expression string) registry.FieldDefinition {
	return registry.FieldDefinition{
		Name: name, Domain: "leave", DataType: registry.DataDecimal,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactDerived, Expression: expression},
	}
}

// countField builds a dataset field whose query differs from its siblings only
// by its window, so several distinct facts can share one dataset.
func countField(name string, lastDays int) registry.FieldDefinition {
	return registry.FieldDefinition{
		Name: name, Domain: "attendance", DataType: registry.DataInteger,
		OwnerService: "attendance-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{
			Kind: registry.FactDataset, Dataset: "attendance.days",
			Window:    &registry.FactWindow{Column: "workDate", RelativeTo: dataset.TokenValidAt, LastDays: lastDays},
			Aggregate: &registry.FactAggregate{Fn: registry.AggregateCount},
			OnEmpty:   0,
		},
	}
}

func decimalText(value any) string {
	type stringer interface{ String() string }
	if text, ok := value.(stringer); ok {
		return text.String()
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %s failure, got none", want)
	}
	if got := fact.Code(err); got != want {
		t.Fatalf("reason code = %q, want %q: %v", got, want, err)
	}
}

func assertCompileCode(t *testing.T, err error, want, column string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %s failure, got none", want)
	}
	var fault *dataset.Fault
	if !errors.As(err, &fault) {
		t.Fatalf("error is not a dataset fault: %v", err)
	}
	if fault.Code != want {
		t.Fatalf("reason code = %q, want %q: %v", fault.Code, want, err)
	}
	if column != "" && fault.Column != column {
		t.Fatalf("fault column = %q, want %q", fault.Column, column)
	}
}

func loadInjectionCorpus(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "testdata", "security", "injection-corpus.txt")
	file, err := os.Open(path) //nolint:gosec // A repository-owned fixture path.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	var corpus []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		corpus = append(corpus, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return corpus
}

// countingExecutor counts how many queries actually reach storage, which is
// how de-duplication is proved rather than assumed.
type countingExecutor struct {
	dataset.Executor
	calls int
}

func (c *countingExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	c.calls++
	return c.Executor.Run(ctx, query)
}

// unsafeExecutor is the mistake the conformance suite exists to catch: it
// answers any query, whether or not the tenant predicate is there.
type unsafeExecutor struct{ manifest dataset.Manifest }

func (u *unsafeExecutor) Manifests() []dataset.Manifest { return []dataset.Manifest{u.manifest} }

func (u *unsafeExecutor) Run(ctx context.Context, _ dataset.Query) (dataset.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}
	return dataset.Result{Value: 0, RowsScanned: 0}, nil
}

// recordingTB captures a failure instead of failing the surrounding test, so a
// test can assert that the conformance suite rejects something.
type recordingTB struct {
	testing.TB
	failed  bool
	message string
}

func (r *recordingTB) Error(args ...any) {
	r.failed = true
	r.message += fmtArgs(args)
}

func (r *recordingTB) Fatal(args ...any) {
	r.failed = true
	r.message += fmtArgs(args)
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.failed = true
	r.message += format + fmtArgs(args)
}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.failed = true
	r.message += format + fmtArgs(args)
}

func fmtArgs(args []any) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if text, ok := arg.(string); ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}
