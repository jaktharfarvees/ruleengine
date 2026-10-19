package testengine

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/internal/decimalx"
	"github.com/arrowhr/ruleengine/internal/expr"
	"github.com/arrowhr/ruleengine/rule"
)

const notInOperator = "notIn"

var parseOperators = []string{notInOperator, "between", "in", ">=", "<=", "!=", "==", ">", "<"}

// Engine is the minimal reference implementation of engine.Engine.
type Engine struct{}

// New constructs a reference engine.
func New() *Engine { return &Engine{} }

// Name returns the stable implementation name.
func (*Engine) Name() string { return "testengine" }

// Capabilities reports exactly the features implemented by Engine.
func (*Engine) Capabilities() engine.Capabilities {
	operators := append([]string(nil), parseOperators...)
	sort.Strings(operators)
	return engine.Capabilities{
		HitPolicies: []rule.HitPolicy{rule.HitFirst, rule.HitPriority, rule.HitCollect},
		Operators:   operators,
		FieldRefs:   true,
		Arithmetic:  true,
		Trace:       true,
		Decimal:     true,
	}
}

// Evaluate applies first-hit decision-table semantics without I/O, clock
// access, logging, or mutation of the authored logic and input maps.
func (*Engine) Evaluate(ctx context.Context, logic rule.RuleLogic, input engine.Input) (engine.Output, error) {
	if logic.HitPolicy != rule.HitFirst && logic.HitPolicy != rule.HitPriority && logic.HitPolicy != rule.HitCollect {
		return engine.Output{}, fmt.Errorf("testengine does not support hit policy %q", logic.HitPolicy)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	output := engine.Output{Result: make(map[string]any)}
	if input.Trace {
		output.Trace = &engine.Trace{Rows: make([]engine.RowTrace, 0, len(logic.Rows))}
	}
	matchedRows := make([]int, 0, len(logic.Rows))
	for index, row := range logic.Rows {
		if err := ctx.Err(); err != nil {
			return engine.Output{}, err
		}
		matched, rowTrace, err := evaluateRow(row, input.Facts, input.Trace)
		if err != nil {
			return engine.Output{}, fmt.Errorf("row %q: %w", row.ID, err)
		}
		if output.Trace != nil {
			output.Trace.Rows = append(output.Trace.Rows, rowTrace)
		}
		if !matched {
			continue
		}
		matchedRows = append(matchedRows, index)
		if logic.HitPolicy == rule.HitFirst {
			break
		}
	}
	if len(matchedRows) == 0 {
		return output, nil
	}
	selected := matchedRows
	if logic.HitPolicy == rule.HitFirst {
		selected = matchedRows[:1]
	} else if logic.HitPolicy == rule.HitPriority {
		winner := matchedRows[0]
		for _, index := range matchedRows[1:] {
			if logic.Rows[index].Priority > logic.Rows[winner].Priority {
				winner = index
			}
		}
		selected = []int{winner}
	}
	for _, index := range selected {
		row := logic.Rows[index]
		result, err := resolveOutputs(row.Then, input.Facts)
		if err != nil {
			return engine.Output{}, fmt.Errorf("row %q: %w", row.ID, err)
		}
		for key, value := range result {
			output.Result[key] = value
		}
		output.MatchedRows = append(output.MatchedRows, row.ID)
	}
	return output, nil
}

func evaluateRow(row rule.DecisionRow, facts map[string]any, trace bool) (bool, engine.RowTrace, error) {
	rowTrace := engine.RowTrace{RowID: row.ID, Matched: true}
	fields := make([]string, 0, len(row.When))
	for field := range row.When {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	if trace {
		rowTrace.Conditions = make([]engine.ConditionTrace, 0, len(fields))
	}
	for _, field := range fields {
		actual, exists := facts[field]
		if !exists {
			return false, rowTrace, fmt.Errorf("fact %q is unavailable", field)
		}
		expression := row.When[field]
		matched, err := evaluateCondition(actual, expression, facts)
		if err != nil {
			return false, rowTrace, fmt.Errorf("condition %q: %w", field, err)
		}
		if trace {
			rowTrace.Conditions = append(rowTrace.Conditions, engine.ConditionTrace{
				Field: field, Expr: expression, Actual: clone(actual), Matched: matched,
			})
		}
		if !matched {
			rowTrace.Matched = false
			return false, rowTrace, nil
		}
	}
	return true, rowTrace, nil
}

func evaluateCondition(actual any, expression string, facts map[string]any) (bool, error) {
	expression = strings.TrimSpace(expression)
	operator, operand, ok := splitOperator(expression)
	if !ok {
		return false, fmt.Errorf("unsupported expression %q", expression)
	}
	switch operator {
	case "in", notInOperator:
		values, err := resolveSet(operand, facts)
		if err != nil {
			return false, err
		}
		matched := false
		for _, value := range values {
			if equal(actual, value) {
				matched = true
				break
			}
		}
		if operator == notInOperator {
			return !matched, nil
		}
		return matched, nil
	case "between":
		parts := strings.SplitN(operand, " and ", 2)
		if len(parts) != 2 {
			return false, fmt.Errorf("between requires two operands")
		}
		lower, err := resolveOperand(parts[0], facts)
		if err != nil {
			return false, err
		}
		upper, err := resolveOperand(parts[1], facts)
		if err != nil {
			return false, err
		}
		bounds, ordered := compare(lower, upper)
		if !ordered || bounds > 0 {
			return false, fmt.Errorf("between range is inverted or incomparable")
		}
		low, lowOK := compare(actual, lower)
		high, highOK := compare(actual, upper)
		return lowOK && highOK && low >= 0 && high <= 0, nil
	default:
		expected, err := resolveOperand(operand, facts)
		if err != nil {
			return false, err
		}
		if operator == "==" {
			return equal(actual, expected), nil
		}
		if operator == "!=" {
			return !equal(actual, expected), nil
		}
		comparison, ordered := compare(actual, expected)
		if !ordered {
			return false, fmt.Errorf("values are not comparable")
		}
		switch operator {
		case ">":
			return comparison > 0, nil
		case ">=":
			return comparison >= 0, nil
		case "<":
			return comparison < 0, nil
		case "<=":
			return comparison <= 0, nil
		}
	}
	return false, fmt.Errorf("unsupported operator %q", operator)
}

func splitOperator(expression string) (operator, operand string, ok bool) {
	for _, candidate := range parseOperators {
		if expression == candidate {
			return candidate, "", true
		}
		prefix := candidate + " "
		if strings.HasPrefix(expression, prefix) {
			return candidate, strings.TrimSpace(strings.TrimPrefix(expression, prefix)), true
		}
	}
	return "", "", false
}

func resolveSet(operand string, facts map[string]any) ([]any, error) {
	if strings.HasPrefix(operand, "$") {
		value, err := resolveOperand(operand, facts)
		if err != nil {
			return nil, err
		}
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() || (reflected.Kind() != reflect.Array && reflected.Kind() != reflect.Slice) {
			return nil, fmt.Errorf("field reference %q is not an array", operand)
		}
		values := make([]any, reflected.Len())
		for index := range values {
			values[index] = reflected.Index(index).Interface()
		}
		return values, nil
	}
	if len(operand) < 2 || operand[0] != '[' || operand[len(operand)-1] != ']' {
		return nil, fmt.Errorf("set requires bracketed values")
	}
	contents := strings.TrimSpace(operand[1 : len(operand)-1])
	if contents == "" {
		return []any{}, nil
	}
	parts := strings.Split(contents, ",")
	values := make([]any, len(parts))
	for index, part := range parts {
		value, err := resolveOperand(part, facts)
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return values, nil
}

func resolveOperand(operand string, facts map[string]any) (any, error) {
	operand = strings.TrimSpace(operand)
	if operand == "" {
		return nil, fmt.Errorf("operand is empty")
	}
	if strings.HasPrefix(operand, "$") {
		field := strings.TrimPrefix(operand, "$")
		if field == "" {
			return nil, fmt.Errorf("field reference is empty")
		}
		value, exists := facts[field]
		if !exists {
			return nil, fmt.Errorf("referenced fact %q is unavailable", field)
		}
		return value, nil
	}
	if len(operand) >= 2 && ((operand[0] == '"' && operand[len(operand)-1] == '"') || (operand[0] == '\'' && operand[len(operand)-1] == '\'')) {
		if operand[0] == '\'' {
			return operand[1 : len(operand)-1], nil
		}
		value, err := strconv.Unquote(operand)
		if err != nil {
			return nil, fmt.Errorf("invalid quoted operand")
		}
		return value, nil
	}
	if operand == "true" {
		return true, nil
	}
	if operand == "false" {
		return false, nil
	}
	if number, err := decimalx.Parse(operand); err == nil {
		return number, nil
	}
	return operand, nil
}

func equal(left, right any) bool {
	if comparison, ok := compare(left, right); ok {
		return comparison == 0
	}
	return reflect.DeepEqual(left, right)
}

func compare(left, right any) (int, bool) {
	leftDecimal, leftNumeric := decimalx.FromValue(left)
	rightDecimal, rightNumeric := decimalx.FromValue(right)
	if leftNumeric || rightNumeric {
		if !leftNumeric || !rightNumeric {
			return 0, false
		}
		return leftDecimal.Cmp(rightDecimal), true
	}
	leftTime, leftIsTime := left.(time.Time)
	rightTime, rightIsTime := right.(time.Time)
	if leftIsTime || rightIsTime {
		if !leftIsTime || !rightIsTime {
			return 0, false
		}
		switch {
		case leftTime.Before(rightTime):
			return -1, true
		case leftTime.After(rightTime):
			return 1, true
		default:
			return 0, true
		}
	}
	leftString, leftIsString := left.(string)
	rightString, rightIsString := right.(string)
	if leftIsString || rightIsString {
		if !leftIsString || !rightIsString {
			return 0, false
		}
		return strings.Compare(leftString, rightString), true
	}
	return 0, false
}

func resolveOutputs(authored map[string]any, facts map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(authored))
	keys := make([]string, 0, len(authored))
	for key := range authored {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := authored[key]
		if source, ok := value.(string); ok && looksLikeOutputExpression(source) {
			parsed, err := expr.Parse(source)
			if err != nil {
				return nil, fmt.Errorf("output %q: %w", key, err)
			}
			resolved, err := parsed.Eval(facts)
			if err != nil {
				return nil, fmt.Errorf("output %q: %w", key, err)
			}
			direct := strings.TrimSpace(source) == "$"+singleReference(parsed.References())
			if !direct {
				if currency, ok := facts["currency"].(string); ok {
					if decimal, exact := decimalx.FromValue(resolved); exact {
						resolved, err = decimalx.QuantizeCurrencyStrict(decimal, currency)
						if err != nil {
							return nil, fmt.Errorf("output %q: %w", key, err)
						}
					}
				}
			}
			result[key] = clone(resolved)
			continue
		}
		result[key] = clone(value)
	}
	return result, nil
}

func looksLikeOutputExpression(source string) bool {
	trimmed := strings.TrimSpace(source)
	if strings.Contains(trimmed, "$") || strings.HasPrefix(trimmed, "(") {
		return true
	}
	for _, function := range expr.FunctionNames() {
		if strings.HasPrefix(trimmed, function+"(") {
			return true
		}
	}
	for _, operator := range expr.OperatorSymbols() {
		if strings.Contains(trimmed, " "+operator+" ") {
			return true
		}
	}
	return false
}

func singleReference(references []string) string {
	if len(references) == 1 {
		return references[0]
	}
	return ""
}

func clone(value any) any {
	if value == nil {
		return nil
	}
	return cloneReflect(reflect.ValueOf(value)).Interface()
}

func cloneReflect(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(cloneReflect(value.Elem()))
		return cloned
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Elem().Type())
		cloned.Elem().Set(cloneReflect(value.Elem()))
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			cloned.Index(index).Set(cloneReflect(value.Index(index)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			cloned.Index(index).Set(cloneReflect(value.Index(index)))
		}
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			cloned.SetMapIndex(iterator.Key(), cloneReflect(iterator.Value()))
		}
		return cloned
	default:
		return value
	}
}
