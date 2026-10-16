package testengine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

var _ engine.Engine = (*Engine)(nil)

func TestGoldenMinimumService(t *testing.T) {
	type golden struct {
		Name            string         `json:"name"`
		Logic           rule.RuleLogic `json:"logic"`
		DeclaredInputs  []string       `json:"declaredInputs"`
		DeclaredOutputs []string       `json:"declaredOutputs"`
		Input           engine.Input   `json:"input"`
		Expected        engine.Output  `json:"expected"`
	}
	file, err := os.Open(filepath.Join("..", "..", "testdata", "golden", "leave", "eligibility-min-service.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var fixture golden
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	fieldRegistry, err := registry.NewRegistry([]registry.FieldDefinition{
		{Name: "serviceMonths", Domain: "leave", DataType: registry.DataInteger, OwnerService: "employee", Status: "active", Role: registry.RoleInput},
		{Name: "eligible", Domain: "leave", DataType: registry.DataBoolean, OwnerService: "leave", Status: "active", Role: registry.RoleOutput},
		{Name: "reasonCode", Domain: "leave", DataType: registry.DataString, OwnerService: "leave", Status: "active", Role: registry.RoleOutput},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fieldRegistry.ValidateInputs("leave", fixture.DeclaredInputs, fixture.Input.Facts); err != nil {
		t.Fatal(err)
	}
	output, err := New().Evaluate(context.Background(), fixture.Logic, fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	if err := fieldRegistry.ValidateOutputs(fixture.DeclaredOutputs, output.Result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(output.Result, fixture.Expected.Result) || !reflect.DeepEqual(output.MatchedRows, fixture.Expected.MatchedRows) {
		t.Fatalf("golden %q mismatch\nwant %#v\n got %#v", fixture.Name, fixture.Expected, output)
	}
	if output.Trace == nil || len(output.Trace.Rows) != 1 || !output.Trace.Rows[0].Matched {
		t.Fatalf("golden trace = %#v", output.Trace)
	}
}

func TestComparisonOperators(t *testing.T) {
	tests := []struct {
		name       string
		actual     any
		expression string
		facts      map[string]any
		want       bool
	}{
		{"equal", "A", "== A", nil, true},
		{"not equal", "A", "!= B", nil, true},
		{"greater", -1, "> -2", nil, true},
		{"greater equal boundary", json.Number("1.20"), ">= 1.2", nil, true},
		{"less", 2, "< 3", nil, true},
		{"less equal boundary", 3, "<= 3", nil, true},
		{"in", "B", "in [A, B, C]", nil, true},
		{"not in", "D", "notIn [A,B,C]", nil, true},
		{"between lower", 1, "between 1 and 5", nil, true},
		{"between upper", 5, "between 1 and 5", nil, true},
		{"field reference", 6, ">= $minimum", map[string]any{"minimum": 6}, true},
		{"set field reference", "A", "in $allowed", map[string]any{"allowed": []string{"A", "B"}}, true},
		{"quoted string", "Development team", `== "Development team"`, nil, true},
		{"single quoted string", "Development team", "== 'Development team'", nil, true},
		{"boolean", true, "== true", nil, true},
		{"false result", 2, "> 3", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := map[string]any{"value": test.actual}
			for key, value := range test.facts {
				facts[key] = value
			}
			logic := rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{
				{ID: "match", When: map[string]string{"value": test.expression}, Then: map[string]any{"matched": true}},
				{ID: "fallback", When: map[string]string{}, Then: map[string]any{"matched": false}},
			}}
			output, err := New().Evaluate(context.Background(), logic, engine.Input{Facts: facts})
			if err != nil {
				t.Fatal(err)
			}
			if got := output.Result["matched"]; got != test.want {
				t.Fatalf("result = %v, want %v", got, test.want)
			}
		})
	}
}

func TestOutputFieldReferenceAndIsolation(t *testing.T) {
	authored := map[string]any{"employee": "$employeeId", "nested": map[string]any{"items": []any{"original"}}}
	logic := rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{ID: "row", Then: authored}}}
	output, err := New().Evaluate(context.Background(), logic, engine.Input{Facts: map[string]any{"employeeId": "E1"}})
	if err != nil {
		t.Fatal(err)
	}
	if output.Result["employee"] != "E1" {
		t.Fatalf("field reference = %#v", output.Result)
	}
	output.Result["nested"].(map[string]any)["items"].([]any)[0] = "changed"
	if authored["nested"].(map[string]any)["items"].([]any)[0] != "original" {
		t.Fatal("engine output aliases authored values")
	}
}

func TestTraceConditionOrderAndShortCircuit(t *testing.T) {
	logic := rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{
		{ID: "miss", When: map[string]string{"zeta": "== no", "alpha": ">= 1"}, Then: map[string]any{"result": "miss"}},
		{ID: "hit", When: map[string]string{}, Then: map[string]any{"result": "hit"}},
		{ID: "unreached", When: map[string]string{"missing": "== value"}, Then: map[string]any{"result": "bad"}},
	}}
	output, err := New().Evaluate(context.Background(), logic, engine.Input{Facts: map[string]any{"zeta": "yes", "alpha": 1}, Trace: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Trace.Rows) != 2 || output.Trace.Rows[0].Conditions[0].Field != "alpha" || output.Trace.Rows[0].Conditions[1].Field != "zeta" || output.Result["result"] != "hit" {
		t.Fatalf("output = %#v", output)
	}
}

func TestCapabilitiesAreHonestAndIndependent(t *testing.T) {
	implementation := New()
	if implementation.Name() != "testengine" {
		t.Fatalf("Name() = %q", implementation.Name())
	}
	capabilities := implementation.Capabilities()
	if !reflect.DeepEqual(capabilities.HitPolicies, []rule.HitPolicy{rule.HitFirst, rule.HitPriority, rule.HitCollect}) ||
		!capabilities.FieldRefs || !capabilities.Arithmetic || !capabilities.Trace || !capabilities.Decimal {
		t.Fatalf("Capabilities() = %#v", capabilities)
	}
	capabilities.Operators[0] = "mutated"
	if New().Capabilities().Operators[0] == "mutated" {
		t.Fatal("Capabilities returned mutable shared state")
	}
}

func TestEvaluationErrors(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name  string
		ctx   context.Context
		logic rule.RuleLogic
		input engine.Input
		match string
	}{
		{"unsupported policy", context.Background(), rule.RuleLogic{HitPolicy: rule.HitPolicy("unique")}, engine.Input{}, "hit policy"},
		{"cancelled", cancelled, rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{ID: "row"}}}, engine.Input{}, "context canceled"},
		{"missing condition fact", context.Background(), oneCondition("missing", "== x"), engine.Input{}, "unavailable"},
		{"bad expression", context.Background(), oneCondition("value", "contains x"), engine.Input{Facts: map[string]any{"value": "x"}}, "unsupported expression"},
		{"empty operand", context.Background(), oneCondition("value", "=="), engine.Input{Facts: map[string]any{"value": "x"}}, "operand is empty"},
		{"missing reference", context.Background(), oneCondition("value", "== $missing"), engine.Input{Facts: map[string]any{"value": "x"}}, "referenced fact"},
		{"empty reference", context.Background(), oneCondition("value", "== $"), engine.Input{Facts: map[string]any{"value": "x"}}, "reference is empty"},
		{"inverted between", context.Background(), oneCondition("value", "between 5 and 1"), engine.Input{Facts: map[string]any{"value": 3}}, "inverted"},
		{"bad between", context.Background(), oneCondition("value", "between 1"), engine.Input{Facts: map[string]any{"value": 3}}, "two operands"},
		{"bad set", context.Background(), oneCondition("value", "in A,B"), engine.Input{Facts: map[string]any{"value": "A"}}, "bracketed"},
		{"set reference not array", context.Background(), oneCondition("value", "in $allowed"), engine.Input{Facts: map[string]any{"value": "A", "allowed": "A"}}, "not an array"},
		{"incomparable", context.Background(), oneCondition("value", "> true"), engine.Input{Facts: map[string]any{"value": false}}, "not comparable"},
		{"missing output reference", context.Background(), rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{ID: "row", Then: map[string]any{"out": "$missing"}}}}, engine.Input{}, "output"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New().Evaluate(test.ctx, test.logic, test.input)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Evaluate() error = %v, want substring %q", err, test.match)
			}
		})
	}
	if _, err := New().Evaluate(nil, rule.RuleLogic{HitPolicy: rule.HitFirst}, engine.Input{}); err != nil {
		t.Fatalf("nil context empty evaluation error = %v", err)
	}
}

func TestTimeComparisonAndNoMatch(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	logic := oneCondition("when", "> $minimum")
	output, err := New().Evaluate(context.Background(), logic, engine.Input{Facts: map[string]any{"when": now, "minimum": now.Add(-time.Hour)}})
	if err != nil || len(output.MatchedRows) != 1 {
		t.Fatalf("time comparison = %#v, %v", output, err)
	}
	logic = oneCondition("value", "> 10")
	output, err = New().Evaluate(context.Background(), logic, engine.Input{Facts: map[string]any{"value": 1}})
	if err != nil || len(output.Result) != 0 || output.Trace != nil {
		t.Fatalf("no-match output = %#v, %v", output, err)
	}
}

func TestMalformedQuotedOperand(t *testing.T) {
	_, err := New().Evaluate(context.Background(), oneCondition("value", `== "\q"`), engine.Input{Facts: map[string]any{"value": "x"}})
	if err == nil {
		t.Fatal("malformed quoted operand succeeded")
	}
}

func TestContextCancellationErrorIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().Evaluate(ctx, rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{ID: "row"}}}, engine.Input{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func oneCondition(field, expression string) rule.RuleLogic {
	return rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{{ID: "row", When: map[string]string{field: expression}, Then: map[string]any{"matched": true}}}}
}
