package expr

import (
	"fmt"

	"github.com/arrowhr/ruleengine/internal/decimalx"
)

const divisionScale int32 = 16

type node interface {
	evaluate(map[string]any) (any, error)
}

type literalNode struct{ value any }

func (n literalNode) evaluate(_ map[string]any) (any, error) { return n.value, nil }

type variableNode struct{ name string }

func (n variableNode) evaluate(facts map[string]any) (any, error) {
	value, exists := facts[n.name]
	if !exists {
		return nil, fmt.Errorf("missing field %q", n.name)
	}
	return value, nil
}

type unaryNode struct {
	operator tokenKind
	operand  node
}

func (n unaryNode) evaluate(facts map[string]any) (any, error) {
	value, err := n.operand.evaluate(facts)
	if err != nil {
		return nil, err
	}
	number, err := requireDecimal(value)
	if err != nil {
		return nil, err
	}
	if n.operator == tokenMinus {
		return decimalx.FromInt(0).Sub(number), nil
	}
	return number, nil
}

type binaryNode struct {
	operator    tokenKind
	left, right node
}

func (n binaryNode) evaluate(facts map[string]any) (any, error) {
	leftValue, err := n.left.evaluate(facts)
	if err != nil {
		return nil, err
	}
	rightValue, err := n.right.evaluate(facts)
	if err != nil {
		return nil, err
	}
	left, err := requireDecimal(leftValue)
	if err != nil {
		return nil, fmt.Errorf("left operand: %w", err)
	}
	right, err := requireDecimal(rightValue)
	if err != nil {
		return nil, fmt.Errorf("right operand: %w", err)
	}
	switch n.operator {
	case tokenPlus:
		return left.Add(right), nil
	case tokenMinus:
		return left.Sub(right), nil
	case tokenStar:
		return left.Mul(right), nil
	case tokenSlash:
		return left.Div(right, divisionScale)
	default:
		return nil, fmt.Errorf("unsupported arithmetic operator")
	}
}

type callNode struct {
	name      string
	arguments []node
}

func (n callNode) evaluate(facts map[string]any) (any, error) {
	arguments := make([]any, len(n.arguments))
	for index, argument := range n.arguments {
		value, err := argument.evaluate(facts)
		if err != nil {
			return nil, fmt.Errorf("%s argument %d: %w", n.name, index+1, err)
		}
		arguments[index] = value
	}
	return functionTable[n.name](arguments)
}

// Eval evaluates the expression only against facts supplied by the caller.
func (e *Expression) Eval(facts map[string]any) (any, error) {
	if e == nil || e.root == nil {
		return nil, fmt.Errorf("expression is nil")
	}
	if facts == nil {
		facts = map[string]any{}
	}
	return e.root.evaluate(facts)
}

func requireDecimal(value any) (decimalx.Decimal, error) {
	decimal, ok := decimalx.FromValue(value)
	if !ok {
		return decimalx.Decimal{}, fmt.Errorf("value %T is not an exact decimal", value)
	}
	return decimal, nil
}
