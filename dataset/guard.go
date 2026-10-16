package dataset

import (
	"fmt"
	"strings"
)

// The four tokens Compile substitutes from Params. Any other dollar-prefixed
// text in an author-controlled string is refused: an unknown token means the
// author is reaching for something the grammar does not offer, and guessing
// what they meant is how an injection becomes a feature.
const (
	// TokenEmployeeID substitutes the subject's employee identifier.
	TokenEmployeeID = "$subject.employeeId"
	// TokenNodeID substitutes the subject's hierarchy node identifier.
	TokenNodeID = "$subject.nodeId"
	// TokenAsOf substitutes the decision time.
	TokenAsOf = "$asOf"
	// TokenValidAt substitutes the business-validity time.
	TokenValidAt = "$validAt"
)

// allowedTokens is the closed substitution set, longest first so that
// scanning matches "$subject.employeeId" before any shorter prefix.
var allowedTokens = []string{TokenEmployeeID, TokenNodeID, TokenValidAt, TokenAsOf}

// bannedSequences are substrings that never appear in a legitimate authored
// value and are the building blocks of statement injection and comment
// smuggling.
// Quote and equals characters are included deliberately. A filterable column
// is an identifier, a code, a date, a number or a boolean — the owning service
// promised an index for it — so a filter value is never free text and never
// needs a quote or a comparison operator inside it. That closes the
// boolean-tautology family (' OR 'a'='a, 1 OR 1=1) which carries no keyword to
// match on. A tenant that genuinely needs to filter on free text gets a
// normalised column for it rather than a relaxed guard.
var bannedSequences = []string{";", "--", "/*", "*/", "`", "'", `"`, "=", "\x00"}

// bannedWords are refused as whole words, case-insensitively. The list is
// deliberately broader than SQL keywords a particular store would need,
// because the library does not know which dialect an executor will translate
// into and a value is never legitimately one of these.
var bannedWords = map[string]struct{}{
	"select": {}, "insert": {}, "update": {}, "delete": {}, "drop": {},
	"union": {}, "exec": {}, "where": {}, "from": {}, "join": {},
}

// rejectQuerySyntax refuses, rather than sanitises, anything in an
// author-controlled string that looks like query or script syntax.
//
// Refusing is the whole point. A sanitiser has to be right about every dialect
// an executor might use; a refusal only has to be right that a legitimate
// filter value is never a SQL fragment. The reason code is
// FACT_RAW_QUERY_REJECTED, and it is meant to alert, because the authoring UI
// should have made such a value impossible to submit.
func rejectQuerySyntax(where, value string) error {
	if err := rejectQueryText(where, value); err != nil {
		return err
	}
	return checkTokens(where, value)
}

// RejectQuerySyntax is rejectQuerySyntax for callers outside this package. The
// fact package applies it to a dataset declaration before compiling it.
func RejectQuerySyntax(where, value string) error { return rejectQuerySyntax(where, value) }

// RejectExpressionText applies the sequence and word checks but not the
// substitution-token check, for text whose own grammar already restricts
// dollar-prefixed names.
//
// A derived expression writes a field reference as $name, so the four-token
// rule does not apply to it. Its real guard is stronger than a keyword list:
// the expression grammar is closed, admitting only numbers, dates, field
// references, arithmetic and an allowlisted function, and every reference must
// resolve to a registered field. These checks are defence in depth.
func RejectExpressionText(where, value string) error { return rejectQueryText(where, value) }

func rejectQueryText(where, value string) error {
	if value == "" {
		return nil
	}
	lowered := strings.ToLower(value)
	for _, sequence := range bannedSequences {
		if strings.Contains(lowered, sequence) {
			return fmt.Errorf("%s contains the sequence %q, which is refused as query or script syntax", where, sequence)
		}
	}
	for _, word := range words(lowered) {
		if _, banned := bannedWords[word]; banned {
			return fmt.Errorf("%s contains the word %q, which is refused as query or script syntax", where, word)
		}
	}
	return nil
}

// checkTokens allows a dollar sign only when the entire value is one of the
// four substitution tokens.
//
// Both halves of that rule matter. An unknown token means the author is
// reaching for something the grammar does not offer, and guessing what they
// meant is how an injection becomes a feature. A known token inside a larger
// string — "E-$subject.employeeId" — is request data concatenated into an
// authored string, which is the shape every injection takes, so it is refused
// rather than substituted and left with a fragment around it.
func checkTokens(where, value string) error {
	if !strings.ContainsRune(value, '$') {
		return nil
	}
	if _, whole := matchWholeToken(value); whole {
		return nil
	}
	return fmt.Errorf("%s uses a dollar sign outside a whole substitution token; a value is either plain text or exactly one of %s",
		where, strings.Join(allowedTokens, ", "))
}

// matchWholeToken reports the token when value is exactly one of them.
func matchWholeToken(value string) (string, bool) {
	for _, token := range allowedTokens {
		if value == token {
			return token, true
		}
	}
	return "", false
}

// matchToken reports the allowed token that begins text, if any. A token must
// end at a boundary: "$asOfX" is not "$asOf" followed by junk, it is unknown.
func matchToken(text string) (string, bool) {
	for _, token := range allowedTokens {
		if !strings.HasPrefix(text, token) {
			continue
		}
		rest := text[len(token):]
		if rest == "" || !isTokenRune(rest[0]) {
			return token, true
		}
	}
	return "", false
}

func isTokenRune(character byte) bool {
	switch {
	case character >= 'a' && character <= 'z':
		return true
	case character >= 'A' && character <= 'Z':
		return true
	case character >= '0' && character <= '9':
		return true
	case character == '_' || character == '.':
		return true
	default:
		return false
	}
}

// words splits on everything that is not a letter or digit, so that a banned
// word is matched as a word and cannot be hidden by punctuation or by
// unusual whitespace. "Information" does not contain "from"; "un/ion" is
// already refused by the comment sequence check.
func words(lowered string) []string {
	return strings.FieldsFunc(lowered, func(character rune) bool {
		switch {
		case character >= 'a' && character <= 'z':
			return false
		case character >= '0' && character <= '9':
			return false
		default:
			return true
		}
	})
}
