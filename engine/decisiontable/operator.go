package decisiontable

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/arrowhr/ruleengine/internal/decimalx"
	"github.com/arrowhr/ruleengine/internal/expr"
)

type operatorKind string

const (
	opEqual        operatorKind = "=="
	opNotEqual     operatorKind = "!="
	opGreater      operatorKind = ">"
	opGreaterEqual operatorKind = ">="
	opLess         operatorKind = "<"
	opLessEqual    operatorKind = "<="
	opIn           operatorKind = "in"
	opNotIn        operatorKind = "notIn"
	opBetween      operatorKind = "between"
)

var operatorTable = map[string]operatorKind{
	"==": opEqual, "!=": opNotEqual, ">": opGreater, ">=": opGreaterEqual,
	"<": opLess, "<=": opLessEqual, "in": opIn, "notIn": opNotIn,
	"between": opBetween,
}

type compiledCondition struct {
	field      string
	source     string
	operator   operatorKind
	operands   []*expr.Expression
	dynamicSet *expr.Expression
}

func conditionOperatorNames() []string {
	names := make([]string, 0, len(operatorTable))
	for name := range operatorTable {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func parseCondition(field, source string) (compiledCondition, error) {
	trimmed := strings.TrimSpace(source)
	operator, operand, ok := splitConditionOperator(trimmed)
	if !ok {
		return compiledCondition{}, fmt.Errorf("unsupported condition %q", source)
	}
	compiled := compiledCondition{field: field, source: source, operator: operator}
	switch operator {
	case opBetween:
		parts, err := splitBetween(operand)
		if err != nil {
			return compiledCondition{}, err
		}
		for _, part := range parts {
			parsed, err := parseConditionOperand(part)
			if err != nil {
				return compiledCondition{}, err
			}
			compiled.operands = append(compiled.operands, parsed)
		}
		if lower, lowerOK := constantValue(compiled.operands[0]); lowerOK {
			if upper, upperOK := constantValue(compiled.operands[1]); upperOK {
				comparison, err := compareValues(lower, upper)
				if err != nil || comparison > 0 {
					return compiledCondition{}, fmt.Errorf("between range is inverted or incomparable")
				}
			}
		}
	case opIn, opNotIn:
		if strings.HasPrefix(strings.TrimSpace(operand), "$") {
			parsed, err := expr.Parse(strings.TrimSpace(operand))
			if err != nil {
				return compiledCondition{}, err
			}
			compiled.dynamicSet = parsed
			break
		}
		parts, err := splitSet(operand)
		if err != nil {
			return compiledCondition{}, err
		}
		for _, part := range parts {
			parsed, err := parseConditionOperand(part)
			if err != nil {
				return compiledCondition{}, err
			}
			compiled.operands = append(compiled.operands, parsed)
		}
	default:
		parsed, err := parseConditionOperand(operand)
		if err != nil {
			return compiledCondition{}, err
		}
		compiled.operands = []*expr.Expression{parsed}
	}
	return compiled, nil
}

func splitConditionOperator(source string) (operatorKind, string, bool) {
	names := make([]string, 0, len(operatorTable))
	for name := range operatorTable {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, name := range names {
		if source == name {
			return operatorTable[name], "", true
		}
		if strings.HasPrefix(source, name+" ") {
			return operatorTable[name], strings.TrimSpace(source[len(name):]), true
		}
	}
	return "", "", false
}

func parseConditionOperand(source string) (*expr.Expression, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, fmt.Errorf("condition operand is empty")
	}
	if strings.HasPrefix(source, "$") || isQuoted(source) || source == "true" || source == "false" || source == "null" {
		return expr.Parse(source)
	}
	if number, err := decimalx.Parse(source); err == nil {
		return expr.Literal(number), nil
	}
	return expr.Literal(source), nil
}

func isQuoted(source string) bool {
	if len(source) < 2 {
		return false
	}
	return source[0] == source[len(source)-1] && (source[0] == '\'' || source[0] == '"')
}

func splitBetween(source string) ([]string, error) {
	parts, err := splitTopLevel(source, " and ")
	if err != nil || len(parts) != 2 {
		return nil, fmt.Errorf("between requires two operands separated by and")
	}
	return parts, nil
}

func splitSet(source string) ([]string, error) {
	source = strings.TrimSpace(source)
	if len(source) < 2 || source[0] != '[' || source[len(source)-1] != ']' {
		return nil, fmt.Errorf("set requires bracketed values")
	}
	contents := strings.TrimSpace(source[1 : len(source)-1])
	if contents == "" {
		return nil, nil
	}
	parts, err := splitTopLevel(contents, ",")
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return nil, fmt.Errorf("set contains an empty value")
		}
	}
	return parts, nil
}

func splitTopLevel(source, separator string) ([]string, error) {
	var parts []string
	start, depth := 0, 0
	var quote byte
	escaped := false
	for index := 0; index < len(source); index++ {
		ch := source[index]
		if quote != 0 {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced parentheses")
			}
		}
		if depth == 0 && strings.HasPrefix(source[index:], separator) {
			parts = append(parts, strings.TrimSpace(source[start:index]))
			index += len(separator) - 1
			start = index + 1
		}
	}
	if quote != 0 || depth != 0 {
		return nil, fmt.Errorf("unterminated quote or parentheses")
	}
	parts = append(parts, strings.TrimSpace(source[start:]))
	return parts, nil
}

func constantValue(expression *expr.Expression) (any, bool) {
	if len(expression.References()) != 0 {
		return nil, false
	}
	value, err := expression.Eval(nil)
	return value, err == nil
}

func (condition compiledCondition) evaluate(actual any, facts map[string]any) (bool, error) {
	switch condition.operator {
	case opIn, opNotIn:
		values, err := condition.setValues(facts)
		if err != nil {
			return false, err
		}
		matched := false
		for _, value := range values {
			equal, err := equalValues(actual, value)
			if err != nil {
				return false, err
			}
			if equal {
				matched = true
				break
			}
		}
		if condition.operator == opNotIn {
			return !matched, nil
		}
		return matched, nil
	case opBetween:
		lower, err := condition.operands[0].Eval(facts)
		if err != nil {
			return false, err
		}
		upper, err := condition.operands[1].Eval(facts)
		if err != nil {
			return false, err
		}
		bounds, err := compareValues(lower, upper)
		if err != nil || bounds > 0 {
			return false, fmt.Errorf("between range is inverted or incomparable")
		}
		low, err := compareValues(actual, lower)
		if err != nil {
			return false, err
		}
		high, err := compareValues(actual, upper)
		return err == nil && low >= 0 && high <= 0, err
	default:
		expected, err := condition.operands[0].Eval(facts)
		if err != nil {
			return false, err
		}
		if condition.operator == opEqual || condition.operator == opNotEqual {
			equal, err := equalValues(actual, expected)
			if condition.operator == opNotEqual {
				equal = !equal
			}
			return equal, err
		}
		comparison, err := compareValues(actual, expected)
		if err != nil {
			return false, err
		}
		switch condition.operator {
		case opGreater:
			return comparison > 0, nil
		case opGreaterEqual:
			return comparison >= 0, nil
		case opLess:
			return comparison < 0, nil
		case opLessEqual:
			return comparison <= 0, nil
		}
	}
	return false, fmt.Errorf("unsupported operator %q", condition.operator)
}

func (condition compiledCondition) setValues(facts map[string]any) ([]any, error) {
	if condition.dynamicSet != nil {
		value, err := condition.dynamicSet.Eval(facts)
		if err != nil {
			return nil, err
		}
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() || reflected.Kind() != reflect.Array && reflected.Kind() != reflect.Slice {
			return nil, fmt.Errorf("set field reference is not an array")
		}
		values := make([]any, reflected.Len())
		for index := range values {
			values[index] = reflected.Index(index).Interface()
		}
		return values, nil
	}
	values := make([]any, len(condition.operands))
	for index, operand := range condition.operands {
		value, err := operand.Eval(facts)
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return values, nil
}

func equalValues(left, right any) (bool, error) {
	comparison, comparable, err := compare(left, right)
	if err != nil {
		return false, err
	}
	if comparable {
		return comparison == 0, nil
	}
	return reflect.DeepEqual(left, right), nil
}

func compareValues(left, right any) (int, error) {
	comparison, comparable, err := compare(left, right)
	if err != nil {
		return 0, err
	}
	if !comparable {
		return 0, fmt.Errorf("values %T and %T are not comparable", left, right)
	}
	return comparison, nil
}

func compare(left, right any) (int, bool, error) {
	if isFloat(left) || isFloat(right) {
		return 0, false, fmt.Errorf("binary floating-point values are not supported")
	}
	leftDecimal, leftNumeric := decimalx.FromValue(left)
	rightDecimal, rightNumeric := decimalx.FromValue(right)
	if leftNumeric || rightNumeric {
		if !leftNumeric || !rightNumeric {
			return 0, false, nil
		}
		return leftDecimal.Cmp(rightDecimal), true, nil
	}
	leftTime, leftIsTime := left.(time.Time)
	rightTime, rightIsTime := right.(time.Time)
	if leftIsTime || rightIsTime {
		if !leftIsTime || !rightIsTime {
			return 0, false, nil
		}
		switch {
		case leftTime.Before(rightTime):
			return -1, true, nil
		case leftTime.After(rightTime):
			return 1, true, nil
		default:
			return 0, true, nil
		}
	}
	leftString, leftIsString := left.(string)
	rightString, rightIsString := right.(string)
	if leftIsString || rightIsString {
		if !leftIsString || !rightIsString {
			return 0, false, nil
		}
		return strings.Compare(leftString, rightString), true, nil
	}
	return 0, false, nil
}

func isFloat(value any) bool {
	switch value.(type) {
	case float32, float64:
		return true
	default:
		return false
	}
}
