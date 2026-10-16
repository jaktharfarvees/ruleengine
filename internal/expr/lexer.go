package expr

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type tokenKind uint8

const (
	tokenEOF tokenKind = iota
	tokenNumber
	tokenString
	tokenIdentifier
	tokenField
	tokenPlus
	tokenMinus
	tokenStar
	tokenSlash
	tokenLeftParen
	tokenRightParen
	tokenComma
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

type lexer struct {
	input string
	pos   int
}

func lex(input string) ([]token, error) {
	l := lexer{input: input}
	tokens := make([]token, 0, 16)
	for {
		tok, err := l.next()
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
		if tok.kind == tokenEOF {
			return tokens, nil
		}
		if len(tokens) > 512 {
			return nil, fmt.Errorf("expression contains too many tokens")
		}
	}
}

func (l *lexer) next() (token, error) {
	for l.pos < len(l.input) && unicode.IsSpace(rune(l.input[l.pos])) {
		l.pos++
	}
	if l.pos >= len(l.input) {
		return token{kind: tokenEOF, pos: l.pos}, nil
	}
	start := l.pos
	ch := l.input[l.pos]
	l.pos++
	switch ch {
	case '+':
		return token{kind: tokenPlus, text: "+", pos: start}, nil
	case '-':
		return token{kind: tokenMinus, text: "-", pos: start}, nil
	case '*':
		return token{kind: tokenStar, text: "*", pos: start}, nil
	case '/':
		return token{kind: tokenSlash, text: "/", pos: start}, nil
	case '(':
		return token{kind: tokenLeftParen, text: "(", pos: start}, nil
	case ')':
		return token{kind: tokenRightParen, text: ")", pos: start}, nil
	case ',':
		return token{kind: tokenComma, text: ",", pos: start}, nil
	case '$':
		if l.pos >= len(l.input) || !isIdentStart(l.input[l.pos]) {
			return token{}, fmt.Errorf("expected field name after $ at byte %d", start)
		}
		nameStart := l.pos
		for l.pos < len(l.input) && isFieldPart(l.input[l.pos]) {
			l.pos++
		}
		return token{kind: tokenField, text: l.input[nameStart:l.pos], pos: start}, nil
	case '\'', '"':
		return l.stringToken(ch, start)
	}
	if isDigit(ch) || ch == '.' && l.pos < len(l.input) && isDigit(l.input[l.pos]) {
		l.pos = start
		return l.numberToken()
	}
	if isIdentStart(ch) {
		for l.pos < len(l.input) && isIdentPart(l.input[l.pos]) {
			l.pos++
		}
		return token{kind: tokenIdentifier, text: l.input[start:l.pos], pos: start}, nil
	}
	return token{}, fmt.Errorf("unexpected character %q at byte %d", ch, start)
}

func (l *lexer) numberToken() (token, error) {
	start := l.pos
	digits := false
	for l.pos < len(l.input) && isDigit(l.input[l.pos]) {
		l.pos++
		digits = true
	}
	if l.pos < len(l.input) && l.input[l.pos] == '.' {
		l.pos++
		for l.pos < len(l.input) && isDigit(l.input[l.pos]) {
			l.pos++
			digits = true
		}
	}
	if !digits {
		return token{}, fmt.Errorf("invalid number at byte %d", start)
	}
	if l.pos < len(l.input) && (l.input[l.pos] == 'e' || l.input[l.pos] == 'E') {
		l.pos++
		if l.pos < len(l.input) && (l.input[l.pos] == '+' || l.input[l.pos] == '-') {
			l.pos++
		}
		exponentStart := l.pos
		for l.pos < len(l.input) && isDigit(l.input[l.pos]) {
			l.pos++
		}
		if exponentStart == l.pos {
			return token{}, fmt.Errorf("invalid exponent at byte %d", start)
		}
	}
	return token{kind: tokenNumber, text: l.input[start:l.pos], pos: start}, nil
}

func (l *lexer) stringToken(quote byte, start int) (token, error) {
	var value strings.Builder
	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		l.pos++
		if ch == quote {
			return token{kind: tokenString, text: value.String(), pos: start}, nil
		}
		if ch != '\\' {
			value.WriteByte(ch)
			continue
		}
		if l.pos >= len(l.input) {
			break
		}
		escaped := l.input[l.pos]
		l.pos++
		switch escaped {
		case '\\', '\'', '"':
			value.WriteByte(escaped)
		case 'n':
			value.WriteByte('\n')
		case 'r':
			value.WriteByte('\r')
		case 't':
			value.WriteByte('\t')
		case 'u':
			if l.pos+4 > len(l.input) {
				return token{}, fmt.Errorf("short unicode escape at byte %d", l.pos-2)
			}
			parsed, err := strconv.ParseUint(l.input[l.pos:l.pos+4], 16, 16)
			if err != nil {
				return token{}, fmt.Errorf("invalid unicode escape at byte %d", l.pos-2)
			}
			value.WriteRune(rune(parsed))
			l.pos += 4
		default:
			return token{}, fmt.Errorf("invalid escape \\%c at byte %d", escaped, l.pos-2)
		}
	}
	return token{}, fmt.Errorf("unterminated string at byte %d", start)
}

func isDigit(ch byte) bool      { return ch >= '0' && ch <= '9' }
func isIdentStart(ch byte) bool { return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' }
func isIdentPart(ch byte) bool  { return isIdentStart(ch) || isDigit(ch) }
func isFieldPart(ch byte) bool  { return isIdentPart(ch) || ch == '.' }
