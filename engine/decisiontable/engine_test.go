package decisiontable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	baseengine "github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/internal/decimalx"
	"github.com/arrowhr/ruleengine/rule"
)

var _ baseengine.Engine = (*Engine)(nil)

func TestAllConditionOperators(t *testing.T) {
	tests := []struct {
		name       string
		actual     any
		condition  string
		extraFacts map[string]any
		want       bool
	}{
		{"equal decimal precision", json.Number("1.20"), "== 1.2", nil, true},
		{"not equal", "A", "!= B", nil, true},
		{"greater negative", -1, "> -2", nil, true},
		{"greater equal boundary", 2, ">= 2", nil, true},
		{"less", 2, "< 3", nil, true},
		{"less equal boundary", 3, "<= 3", nil, true},
		{"in", "B", "in [A, B, C]", nil, true},
		{"not in", "D", "notIn [A,B,C]", nil, true},
		{"empty in", "A", "in []", nil, false},
		{"between lower inclusive", -2, "between -2 and 5", nil, true},
		{"between upper inclusive", 5, "between -2 and 5", nil, true},
		{"reference", 6, ">= $minimum", map[string]any{"minimum": 6}, true},
		{"set reference", "A", "in $allowed", map[string]any{"allowed": []string{"A", "B"}}, true},
		{"quoted separator", "a and b", `between "a and b" and "z"`, nil, true},
		{"boolean", true, "== true", nil, true},
		{"null", nil, "== null", nil, true},
		{"false result", 2, "> 3", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := map[string]any{"value": test.actual}
			for name, value := range test.extraFacts {
				facts[name] = value
			}
			output, err := New().Evaluate(context.Background(), matchingLogic(test.condition), baseengine.Input{Facts: facts})
			if err != nil {
				t.Fatal(err)
			}
			if got := output.Result["matched"]; got != test.want {
				t.Fatalf("matched = %v, want %v", got, test.want)
			}
		})
	}
}

func TestHitPolicies(t *testing.T) {
	rows := []rule.DecisionRow{
		{ID: "first-low", Priority: 1, When: map[string]string{"value": ">= 1"}, Then: map[string]any{"winner": "first", "shared": "early"}},
		{ID: "second-high", Priority: 5, When: map[string]string{"value": "<= 10"}, Then: map[string]any{"winner": "second", "extra": true, "shared": "late"}},
		{ID: "third-tie", Priority: 5, When: map[string]string{"value": "between 1 and 10"}, Then: map[string]any{"winner": "third"}},
	}
	tests := []struct {
		policy  rule.HitPolicy
		matches []string
		winner  string
		shared  string
		traces  int
	}{
		{rule.HitFirst, []string{"first-low"}, "first", "early", 1},
		{rule.HitPriority, []string{"second-high"}, "second", "late", 3},
		{rule.HitCollect, []string{"first-low", "second-high", "third-tie"}, "third", "late", 3},
	}
	for _, test := range tests {
		t.Run(string(test.policy), func(t *testing.T) {
			output, err := New().Evaluate(context.Background(), rule.RuleLogic{HitPolicy: test.policy, Rows: rows}, baseengine.Input{Facts: map[string]any{"value": 5}, Trace: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(output.MatchedRows, test.matches) || output.Result["winner"] != test.winner || output.Result["shared"] != test.shared {
				t.Fatalf("output = %#v", output)
			}
			if len(output.Trace.Rows) != test.traces {
				t.Fatalf("trace rows = %d, want %d", len(output.Trace.Rows), test.traces)
			}
		})
	}
}

func TestExpressionsFieldReferencesAndCurrency(t *testing.T) {
	logic := rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{
		ID: "calculate", When: map[string]string{"hours": "> $minimum"}, Then: map[string]any{
			"employee": "$employeeId",
			"gross":    "$hours * $rate + 0.1 + 0.2",
			"daily":    "round($monthly / 30, 2)",
			"service":  "monthsBetween(joined, $validAt)",
		},
	}}}
	facts := map[string]any{
		"hours": 10, "minimum": 5, "rate": json.Number("1.125"), "employeeId": "E-1",
		"monthly": 100, "joined": "2025-01-31", "validAt": "2026-03-30", "currency": "USD",
	}
	output, err := New().Evaluate(context.Background(), logic, baseengine.Input{Facts: facts})
	if err != nil {
		t.Fatal(err)
	}
	assertDecimal(t, output.Result["gross"], "11.55", 2)
	assertDecimal(t, output.Result["daily"], "3.33", 2)
	assertDecimal(t, output.Result["service"], "13", 2)
	if output.Result["employee"] != "E-1" {
		t.Fatalf("employee = %#v", output.Result["employee"])
	}

	precision := rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{ID: "exact", Then: map[string]any{"sum": "0.1 + 0.2"}}}}
	output, err = New().Evaluate(context.Background(), precision, baseengine.Input{})
	if err != nil {
		t.Fatal(err)
	}
	assertDecimal(t, output.Result["sum"], "0.3", 1)
}

func TestTraceIsDeterministicAndComplete(t *testing.T) {
	logic := rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{
		{ID: "miss", When: map[string]string{"zeta": "== yes", "alpha": ">= 1"}, Then: map[string]any{"result": "miss"}},
		{ID: "fallback", Then: map[string]any{"result": "hit"}},
		{ID: "unreached", Then: map[string]any{"result": "bad"}},
	}}
	output, err := New().Evaluate(context.Background(), logic, baseengine.Input{Facts: map[string]any{"zeta": "no", "alpha": 1}, Trace: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Trace.Rows) != 2 || output.Trace.Rows[0].Matched || !output.Trace.Rows[1].Matched {
		t.Fatalf("trace = %#v", output.Trace)
	}
	conditions := output.Trace.Rows[0].Conditions
	if len(conditions) != 2 || conditions[0].Field != "alpha" || conditions[1].Field != "zeta" || !conditions[0].Matched || conditions[1].Matched {
		t.Fatalf("conditions = %#v", conditions)
	}
	if output.Result["result"] != "hit" {
		t.Fatalf("result = %#v", output.Result)
	}
	withoutTrace, err := New().Evaluate(context.Background(), logic, baseengine.Input{Facts: map[string]any{"zeta": "no", "alpha": 1}})
	if err != nil || withoutTrace.Trace != nil {
		t.Fatalf("trace disabled = %#v, %v", withoutTrace, err)
	}
}

func TestCompileWarningsAndErrors(t *testing.T) {
	catchAll := rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{
		{ID: "always", Then: map[string]any{"value": 1}},
		{ID: "dead-one", When: map[string]string{"value": "> 1"}, Then: map[string]any{"value": 2}},
		{ID: "dead-two", Then: map[string]any{"value": 3}},
	}}
	_, warnings, err := Compile(catchAll)
	if err != nil || len(warnings) != 2 || warnings[0].Code != "unreachable-row" || warnings[0].RowID != "dead-one" {
		t.Fatalf("warnings = %#v, error = %v", warnings, err)
	}
	catchAll.HitPolicy = rule.HitCollect
	if _, warnings, err := Compile(catchAll); err != nil || len(warnings) != 0 {
		t.Fatalf("collect warnings = %#v, %v", warnings, err)
	}

	tests := []struct {
		name  string
		logic rule.RuleLogic
		match string
	}{
		{"policy", rule.RuleLogic{HitPolicy: "other", Rows: catchAll.Rows}, "hit policy"},
		{"no rows", rule.RuleLogic{HitPolicy: rule.HitFirst}, "no rows"},
		{"empty id", oneRow("", nil, map[string]any{"x": 1}), "empty id"},
		{"duplicate id", rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{ID: "r", Then: map[string]any{"x": 1}}, {ID: "r", Then: map[string]any{"x": 2}}}}, "duplicate"},
		{"no output", oneRow("r", nil, nil), "no outputs"},
		{"empty condition field", oneRow("r", map[string]string{"": "== 1"}, map[string]any{"x": 1}), "empty condition"},
		{"unsupported condition", oneRow("r", map[string]string{"x": "contains A"}, map[string]any{"x": 1}), "unsupported"},
		{"empty operand", oneRow("r", map[string]string{"x": "=="}, map[string]any{"x": 1}), "operand is empty"},
		{"inverted between", oneRow("r", map[string]string{"x": "between 5 and 1"}, map[string]any{"x": 1}), "inverted"},
		{"bad between", oneRow("r", map[string]string{"x": "between 1"}, map[string]any{"x": 1}), "two operands"},
		{"bad set", oneRow("r", map[string]string{"x": "in A,B"}, map[string]any{"x": 1}), "bracketed"},
		{"empty set item", oneRow("r", map[string]string{"x": "in [A,,B]"}, map[string]any{"x": 1}), "empty value"},
		{"empty output", oneRow("r", nil, map[string]any{"": 1}), "empty output"},
		{"bad expression", oneRow("r", nil, map[string]any{"x": "$value +"}), "unexpected end"},
		{"float output", oneRow("r", nil, map[string]any{"x": 0.1}), "floating-point"},
		{"nested float output", oneRow("r", nil, map[string]any{"x": []any{1, 0.1}}), "floating-point"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := Compile(test.logic); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Compile() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestEvaluationErrorsAndCancellation(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name  string
		ctx   context.Context
		logic rule.RuleLogic
		facts map[string]any
		match string
	}{
		{"missing condition fact", nil, oneRow("r", map[string]string{"missing": "== A"}, map[string]any{"x": 1}), nil, "unavailable"},
		{"missing condition reference", nil, oneRow("r", map[string]string{"x": "== $missing"}, map[string]any{"x": 1}), map[string]any{"x": 1}, "missing field"},
		{"missing output reference", nil, oneRow("r", nil, map[string]any{"x": "$missing"}), nil, "output"},
		{"dynamic inverted between", nil, oneRow("r", map[string]string{"x": "between $high and $low"}, map[string]any{"x": 1}), map[string]any{"x": 3, "high": 5, "low": 1}, "inverted"},
		{"dynamic set not array", nil, oneRow("r", map[string]string{"x": "in $allowed"}, map[string]any{"x": 1}), map[string]any{"x": "A", "allowed": "A"}, "not an array"},
		{"incomparable", nil, oneRow("r", map[string]string{"x": "> true"}, map[string]any{"x": 1}), map[string]any{"x": false}, "not comparable"},
		{"condition float", nil, oneRow("r", map[string]string{"x": "== 1.5"}, map[string]any{"x": 1}), map[string]any{"x": 1.5}, "floating-point"},
		{"output reference float", nil, oneRow("r", nil, map[string]any{"x": "$source"}), map[string]any{"source": 1.5}, "floating-point"},
		{"division zero", nil, oneRow("r", nil, map[string]any{"x": "$value / 0"}), map[string]any{"value": 1}, "zero"},
		{"cancelled", cancelled, oneRow("r", nil, map[string]any{"x": 1}), nil, "context canceled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New().Evaluate(test.ctx, test.logic, baseengine.Input{Facts: test.facts})
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Evaluate() error = %v, want %q", err, test.match)
			}
			if test.name == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("error identity = %v", err)
			}
		})
	}
	if _, err := (*Table)(nil).Evaluate(nil, baseengine.Input{}); err == nil {
		t.Fatal("nil table evaluated")
	}
}

func TestCacheConcurrencyAndTypeSensitiveFingerprint(t *testing.T) {
	implementation := New()
	logic := oneRow("r", nil, map[string]any{"value": "1"})
	const workers = 20
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			output, err := implementation.Evaluate(context.Background(), logic, baseengine.Input{})
			if err != nil || output.Result["value"] != "1" {
				errors <- fmt.Errorf("output %#v: %w", output, err)
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	entries := 0
	implementation.cache.Range(func(_, _ any) bool { entries++; return true })
	if entries != 1 {
		t.Fatalf("cache entries = %d, want 1", entries)
	}
	numeric := oneRow("r", nil, map[string]any{"value": json.Number("1")})
	output, err := implementation.Evaluate(context.Background(), numeric, baseengine.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := output.Result["value"].(json.Number); !ok {
		t.Fatalf("type-sensitive cache result = %T", output.Result["value"])
	}
}

func TestCapabilitiesAreDerivedAndDefensive(t *testing.T) {
	implementation := New()
	capabilities := implementation.Capabilities()
	if implementation.Name() != "decisiontable" || len(capabilities.HitPolicies) != len(hitPolicyTable) || len(capabilities.Operators) != len(operatorTable) || !capabilities.FieldRefs || !capabilities.Arithmetic || !capabilities.Trace || !capabilities.Decimal {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	for _, operator := range capabilities.Operators {
		if _, exists := operatorTable[operator]; !exists {
			t.Fatalf("advertised unknown operator %q", operator)
		}
	}
	capabilities.Operators[0] = "mutated"
	capabilities.HitPolicies[0] = "mutated"
	if New().Capabilities().Operators[0] == "mutated" || New().Capabilities().HitPolicies[0] == "mutated" {
		t.Fatal("capability slices alias shared state")
	}
}

func TestAuthoredValuesAndFactsAreNotMutated(t *testing.T) {
	authored := map[string]any{"nested": map[string]any{"items": []any{"original"}}, "copy": "$source"}
	facts := map[string]any{"source": map[string]any{"items": []any{"fact"}}}
	output, err := New().Evaluate(context.Background(), oneRow("r", nil, authored), baseengine.Input{Facts: facts})
	if err != nil {
		t.Fatal(err)
	}
	output.Result["nested"].(map[string]any)["items"].([]any)[0] = "changed"
	output.Result["copy"].(map[string]any)["items"].([]any)[0] = "changed"
	if authored["nested"].(map[string]any)["items"].([]any)[0] != "original" || facts["source"].(map[string]any)["items"].([]any)[0] != "fact" {
		t.Fatal("evaluation output aliases authored or input values")
	}
}

func TestDateComparison(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	logic := oneRow("r", map[string]string{"when": "> $minimum"}, map[string]any{"ok": true})
	output, err := New().Evaluate(context.Background(), logic, baseengine.Input{Facts: map[string]any{"when": now, "minimum": now.Add(-time.Hour)}})
	if err != nil || !reflect.DeepEqual(output.MatchedRows, []string{"r"}) {
		t.Fatalf("date output = %#v, %v", output, err)
	}
}

func TestInternalBoundaryPaths(t *testing.T) {
	// The clone and fingerprint walkers deliberately support nested authored
	// values without aliasing their mutable containers.
	value := 7
	var nilPointer *int
	var nilSlice []string
	var nilMap map[string]any
	containers := []any{
		nil, &value, nilPointer, []any{"x", []int{1, 2}}, nilSlice,
		[2]string{"a", "b"}, map[string]any{"b": &value, "a": []string{"x"}}, nilMap,
	}
	for _, container := range containers {
		cloned := clone(container)
		if container != nil && reflect.TypeOf(cloned) != reflect.TypeOf(container) {
			t.Fatalf("clone type = %T, want %T", cloned, container)
		}
	}
	var wrapped any = []string{"value"}
	clonedInterface := cloneReflect(reflect.ValueOf(&wrapped).Elem())
	if !reflect.DeepEqual(clonedInterface.Interface(), wrapped) {
		t.Fatalf("interface clone = %#v", clonedInterface.Interface())
	}
	if containsFloat(reflect.Value{}) || containsFloat(reflect.ValueOf(map[string]any{"safe": 1})) || !containsFloat(reflect.ValueOf(map[string]any{"nested": &[]float64{1.5}})) {
		t.Fatal("containsFloat recursion mismatch")
	}

	uncarriable := oneRow("r", nil, map[string]any{"function": func() {}})
	if _, err := New().Evaluate(context.Background(), uncarriable, baseengine.Input{}); err == nil {
		t.Fatal("engine accepted a function as an authored output")
	}
}

func TestFingerprintSeparatesContentAndType(t *testing.T) {
	tests := []struct {
		name  string
		logic rule.RuleLogic
	}{
		{"baseline", oneRow("r", map[string]string{"ab": "c"}, map[string]any{"v": 1})},
		{"condition boundary moved", oneRow("r", map[string]string{"a": "bc"}, map[string]any{"v": 1})},
		{"output type differs", oneRow("r", map[string]string{"ab": "c"}, map[string]any{"v": json.Number("1")})},
		{"output value differs", oneRow("r", map[string]string{"ab": "c"}, map[string]any{"v": 2})},
		{"row id differs", oneRow("s", map[string]string{"ab": "c"}, map[string]any{"v": 1})},
		{"nested value differs", oneRow("r", map[string]string{"ab": "c"}, map[string]any{"v": map[string]any{"n": []any{1, 2}}})},
	}
	seen := make(map[tableKey]string, len(tests))
	for _, test := range tests {
		key := logicFingerprint(test.logic)
		if previous, clash := seen[key]; clash {
			t.Fatalf("%q and %q share a fingerprint", test.name, previous)
		}
		seen[key] = test.name
		if again := logicFingerprint(test.logic); again != key {
			t.Fatalf("%q fingerprint is unstable: %v then %v", test.name, key, again)
		}
	}
	// Key order inside a row must not change the identity.
	left := oneRow("r", map[string]string{"a": "1", "b": "2"}, map[string]any{"x": 1, "y": 2})
	right := oneRow("r", map[string]string{"b": "2", "a": "1"}, map[string]any{"y": 2, "x": 1})
	if logicFingerprint(left) != logicFingerprint(right) {
		t.Fatal("authored map order changed the fingerprint")
	}
}

func TestFingerprintIsAllocationLight(t *testing.T) {
	logic := benchmarkLogic(100)
	allocations := testing.AllocsPerRun(200, func() { _ = logicFingerprint(logic) })
	if allocations > 2 {
		t.Fatalf("fingerprint allocated %.1f times per call for a 100-row table, want at most 2", allocations)
	}
}

func TestAdditionalEvaluationBoundaries(t *testing.T) {
	implementation := New()
	noMatch := oneRow("r", map[string]string{"value": "> 10"}, map[string]any{"x": 1})
	output, err := implementation.Evaluate(context.Background(), noMatch, baseengine.Input{Facts: map[string]any{"value": 1}})
	if err != nil || len(output.Result) != 0 || len(output.MatchedRows) != 0 {
		t.Fatalf("no match = %#v, %v", output, err)
	}

	for _, policy := range []rule.HitPolicy{rule.HitPriority, rule.HitCollect} {
		logic := rule.RuleLogic{HitPolicy: policy, Rows: []rule.DecisionRow{{ID: "r", Then: map[string]any{"x": "$missing"}}}}
		if _, err := implementation.Evaluate(context.Background(), logic, baseengine.Input{}); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("%s output error = %v", policy, err)
		}
	}

	table := &Table{hitPolicy: "invalid", rows: []compiledRow{{id: "r", outputs: map[string]compiledOutput{"x": {literal: 1}}}}}
	if output, err := table.Evaluate(nil, baseengine.Input{}); err != nil || len(output.MatchedRows) != 0 {
		t.Fatalf("invariant table output = %#v, %v", output, err)
	}
}

func TestAdditionalConditionBoundaries(t *testing.T) {
	compileErrors := []string{
		"in $", "in ['unterminated]", "in [(A]", "between true and false", `== "\q"`,
	}
	for _, source := range compileErrors {
		if _, err := parseCondition("value", source); err == nil {
			t.Errorf("parseCondition(%q) succeeded", source)
		}
	}
	quotedSet, err := parseCondition("value", `in ["A,B", C]`)
	if err != nil {
		t.Fatal(err)
	}
	if matched, err := quotedSet.evaluate("A,B", nil); err != nil || !matched {
		t.Fatalf("quoted set matched = %v, %v", matched, err)
	}
	missingSet, err := parseCondition("value", "in [A,$missing]")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missingSet.evaluate("Z", nil); err == nil {
		t.Fatal("missing static set reference succeeded")
	}
	missingLower, _ := parseCondition("value", "between $missing and 5")
	if _, err := missingLower.evaluate(3, nil); err == nil {
		t.Fatal("missing lower bound succeeded")
	}
	missingUpper, _ := parseCondition("value", "between 1 and $missing")
	if _, err := missingUpper.evaluate(3, nil); err == nil {
		t.Fatal("missing upper bound succeeded")
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if comparison, err := compareValues(now, now); err != nil || comparison != 0 {
		t.Fatalf("equal time comparison = %d, %v", comparison, err)
	}
	if comparison, err := compareValues(now, now.Add(time.Hour)); err != nil || comparison >= 0 {
		t.Fatalf("before time comparison = %d, %v", comparison, err)
	}
	if _, err := compareValues(now, "2026-01-01"); err == nil {
		t.Fatal("mixed time comparison succeeded")
	}
	if _, err := compareValues("A", 1); err == nil {
		t.Fatal("mixed string comparison succeeded")
	}
	if _, err := compareValues(true, false); err == nil {
		t.Fatal("boolean ordering succeeded")
	}
}

func matchingLogic(condition string) rule.RuleLogic {
	return rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{
		{ID: "condition", When: map[string]string{"value": condition}, Then: map[string]any{"matched": true}},
		{ID: "fallback", Then: map[string]any{"matched": false}},
	}}
}

func oneRow(id string, when map[string]string, then map[string]any) rule.RuleLogic {
	return rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{ID: id, When: when, Then: then}}}
}

func assertDecimal(t *testing.T, value any, want string, scale int32) {
	t.Helper()
	decimal, ok := value.(decimalx.Decimal)
	if !ok || decimal.String() != want || decimal.Scale() != scale {
		t.Fatalf("decimal = %#v (%T), want %s scale %d", value, value, want, scale)
	}
}

func TestFingerprintAndGuardCoverAllAuthoredShapes(t *testing.T) {
	// Every Go representation a decoded rule can carry must reach a distinct
	// fingerprint branch, so a shape the walker does not know cannot silently
	// share a compiled table with a different one.
	value := 7
	shapes := []any{
		nil, true, "text", json.Number("1.5"), 1, int32(2), int64(3),
		float32(1.5), float64(2.5), []any{1, "two", nil},
		map[string]any{"b": 1, "a": []any{2}}, &value, [2]string{"a", "b"},
	}
	seen := make(map[tableKey]int, len(shapes))
	for index, shape := range shapes {
		key := logicFingerprint(oneRow("r", nil, map[string]any{"v": shape}))
		if previous, clash := seen[key]; clash {
			t.Fatalf("shape %d (%T) collides with shape %d", index, shape, previous)
		}
		seen[key] = index
	}

	// A channel, a function and an unsafe pointer are not authored data, at
	// the top level or nested, and must be refused at load time.
	uncarriable := []any{
		func() {}, make(chan int), unsafe.Pointer(&value),
		[]any{func() {}}, map[string]any{"nested": make(chan int)},
		&[]any{func() {}},
	}
	for _, shape := range uncarriable {
		if _, _, err := Compile(oneRow("r", nil, map[string]any{"v": shape})); err == nil {
			t.Fatalf("Compile accepted %T as an authored output", shape)
		}
	}
	// A nil pointer and a nil map carry nothing and are therefore acceptable.
	var nilPointer *int
	var nilMap map[string]any
	for _, shape := range []any{nilPointer, nilMap} {
		if _, _, err := Compile(oneRow("r", nil, map[string]any{"v": shape})); err != nil {
			t.Fatalf("Compile refused %T: %v", shape, err)
		}
	}
}
