package expr

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arrowhr/ruleengine/internal/decimalx"
)

const maxExpressionLength = 4096

var arithmeticOperatorSymbols = []string{"+", "-", "*", "/"}

// Expression is an immutable parsed expression safe for concurrent use.
type Expression struct {
	root       node
	references []string
}

// Parse validates and parses source using the closed expression grammar.
func Parse(source string) (*Expression, error) {
	if len(source) > maxExpressionLength {
		return nil, fmt.Errorf("expression exceeds %d bytes", maxExpressionLength)
	}
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("expression is empty")
	}
	tokens, err := lex(source)
	if err != nil {
		return nil, err
	}
	p := parser{tokens: tokens, references: make(map[string]struct{})}
	root, err := p.parseExpression(0)
	if err != nil {
		return nil, err
	}
	if tok := p.peek(); tok.kind != tokenEOF {
		return nil, fmt.Errorf("unexpected %q at byte %d", tok.text, tok.pos)
	}
	references := make([]string, 0, len(p.references))
	for name := range p.references {
		references = append(references, name)
	}
	sort.Strings(references)
	return &Expression{root: root, references: references}, nil
}

// Literal constructs an expression that always evaluates to value.
func Literal(value any) *Expression { return &Expression{root: literalNode{value: value}} }

// OperatorSymbols returns the arithmetic operators implemented by the parser.
func OperatorSymbols() []string { return append([]string(nil), arithmeticOperatorSymbols...) }

// References returns a sorted copy of the field names used by the expression.
func (e *Expression) References() []string {
	if e == nil {
		return nil
	}
	return append([]string(nil), e.references...)
}

// Constant reports the value when the expression consists of one literal.
func (e *Expression) Constant() (any, bool) {
	if e == nil {
		return nil, false
	}
	literal, ok := e.root.(literalNode)
	return literal.value, ok
}

type parser struct {
	tokens     []token
	index      int
	depth      int
	references map[string]struct{}
}

func (p *parser) parseExpression(minPrecedence int) (node, error) {
	if p.depth >= 64 {
		return nil, fmt.Errorf("expression nesting exceeds 64 levels")
	}
	p.depth++
	defer func() { p.depth-- }()

	left, err := p.parsePrefix()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		precedence := binaryPrecedence(tok.kind)
		if precedence < minPrecedence {
			break
		}
		p.advance()
		right, err := p.parseExpression(precedence + 1)
		if err != nil {
			return nil, err
		}
		left = binaryNode{operator: tok.kind, left: left, right: right}
	}
	return left, nil
}

func (p *parser) parsePrefix() (node, error) {
	tok := p.advance()
	switch tok.kind {
	case tokenNumber:
		value, err := decimalx.Parse(tok.text)
		if err != nil {
			return nil, fmt.Errorf("number at byte %d: %w", tok.pos, err)
		}
		return literalNode{value: value}, nil
	case tokenString:
		return literalNode{value: tok.text}, nil
	case tokenField:
		p.references[tok.text] = struct{}{}
		return variableNode{name: tok.text}, nil
	case tokenIdentifier:
		switch tok.text {
		case "true":
			return literalNode{value: true}, nil
		case "false":
			return literalNode{value: false}, nil
		case "null":
			return literalNode{}, nil
		}
		if p.peek().kind == tokenLeftParen {
			return p.parseCall(tok)
		}
		p.references[tok.text] = struct{}{}
		return variableNode{name: tok.text}, nil
	case tokenMinus, tokenPlus:
		operand, err := p.parseExpression(30)
		if err != nil {
			return nil, err
		}
		return unaryNode{operator: tok.kind, operand: operand}, nil
	case tokenLeftParen:
		value, err := p.parseExpression(0)
		if err != nil {
			return nil, err
		}
		if closing := p.advance(); closing.kind != tokenRightParen {
			return nil, fmt.Errorf("expected ) at byte %d", closing.pos)
		}
		return value, nil
	case tokenEOF:
		return nil, fmt.Errorf("unexpected end of expression")
	default:
		return nil, fmt.Errorf("unexpected %q at byte %d", tok.text, tok.pos)
	}
}

func (p *parser) parseCall(name token) (node, error) {
	if _, allowed := functionTable[name.text]; !allowed {
		return nil, fmt.Errorf("unknown function %q at byte %d", name.text, name.pos)
	}
	p.advance() // left parenthesis
	arguments := make([]node, 0, 4)
	if p.peek().kind != tokenRightParen {
		for {
			argument, err := p.parseExpression(0)
			if err != nil {
				return nil, err
			}
			arguments = append(arguments, argument)
			if p.peek().kind != tokenComma {
				break
			}
			p.advance()
		}
	}
	if closing := p.advance(); closing.kind != tokenRightParen {
		return nil, fmt.Errorf("expected ) after %s arguments at byte %d", name.text, closing.pos)
	}
	return callNode{name: name.text, arguments: arguments}, nil
}

func (p *parser) peek() token {
	if p.index >= len(p.tokens) {
		return token{kind: tokenEOF}
	}
	return p.tokens[p.index]
}

func (p *parser) advance() token {
	tok := p.peek()
	if p.index < len(p.tokens) {
		p.index++
	}
	return tok
}

func binaryPrecedence(kind tokenKind) int {
	switch kind {
	case tokenPlus, tokenMinus:
		return 10
	case tokenStar, tokenSlash:
		return 20
	default:
		return -1
	}
}
