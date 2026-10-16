package dataset

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectQuerySyntaxRefusesTheCommittedCorpus(t *testing.T) {
	corpus := corpusEntries(t)
	if len(corpus) < 20 {
		t.Fatalf("corpus holds %d entries, want at least 20", len(corpus))
	}
	for _, entry := range corpus {
		if err := RejectQuerySyntax("value", entry); err == nil {
			t.Errorf("accepted %q", entry)
		}
	}
}

func TestRejectQuerySyntaxAcceptsLegitimateValues(t *testing.T) {
	// These are what a filterable column actually holds. A guard that refused
	// any of them would be unusable, and a team would turn it off.
	accepted := []string{
		"", "ANNUAL", "CASUAL", "APPROVED", "E1001", "BR001", "MORNING",
		"Development team", "Research and Development", "en-LK", "Asia/Colombo",
		"2026-06-15", "2026-06-15T10:30:00Z", "12.5", "-3", "true",
		"REST_DAY", "O_BRIEN", "shift.morning", "a/b",
		TokenEmployeeID, TokenNodeID, TokenAsOf, TokenValidAt,
	}
	for _, value := range accepted {
		if err := RejectQuerySyntax("value", value); err != nil {
			t.Errorf("refused the legitimate value %q: %v", value, err)
		}
	}
}

func TestRejectExpressionTextAllowsFieldReferences(t *testing.T) {
	// A derived expression writes a field reference as $name, so the
	// four-token rule must not apply to it.
	allowed := []string{
		"monthsBetween($joinDate, $validAt)",
		"$a + $b * 2",
		"round($rate, 2)",
		"coalesce($balance, 0)",
		"($x - $y) / 2",
	}
	for _, expression := range allowed {
		if err := RejectExpressionText("derived expression", expression); err != nil {
			t.Errorf("refused the legitimate expression %q: %v", expression, err)
		}
		// The same text as a filter value is refused, because there the
		// dollar sign can only be a substitution token.
		if err := RejectQuerySyntax("value", expression); err == nil {
			t.Errorf("accepted %q as a filter value", expression)
		}
	}

	// Query syntax is still refused in an expression.
	for _, expression := range []string{
		"1; DROP TABLE x", "$a -- comment", "/* x */ 1", "`a`", `"ANNUAL"`, "'ANNUAL'", "$a = 1",
	} {
		if err := RejectExpressionText("derived expression", expression); err == nil {
			t.Errorf("accepted %q as an expression", expression)
		}
	}
}

func TestTokenMatchingIsExact(t *testing.T) {
	tests := []struct {
		text  string
		token string
		ok    bool
	}{
		{TokenEmployeeID, TokenEmployeeID, true},
		{TokenNodeID, TokenNodeID, true},
		{TokenAsOf, TokenAsOf, true},
		{TokenValidAt, TokenValidAt, true},
		{TokenAsOf + " ", TokenAsOf, true},
		{TokenAsOf + "Yesterday", "", false},
		{TokenEmployeeID + "X", "", false},
		{TokenEmployeeID + ".more", "", false},
		{"$unknown", "", false},
		{"$", "", false},
		{"", "", false},
	}
	for _, test := range tests {
		token, ok := matchToken(test.text)
		if ok != test.ok || token != test.token {
			t.Errorf("matchToken(%q) = %q, %t; want %q, %t", test.text, token, ok, test.token, test.ok)
		}
	}
}

func TestIsTokenRune(t *testing.T) {
	for _, character := range []byte{'a', 'z', 'A', 'Z', '0', '9', '_', '.'} {
		if !isTokenRune(character) {
			t.Errorf("%q should continue a token", character)
		}
	}
	for _, character := range []byte{' ', '$', '-', '/', ';', '\t'} {
		if isTokenRune(character) {
			t.Errorf("%q should end a token", character)
		}
	}
}

func TestWordsSplitsOnEverythingElse(t *testing.T) {
	// "Information" must not count as containing "for", and a keyword hidden
	// behind punctuation or odd whitespace must still be found.
	if err := RejectQuerySyntax("value", "Information Technology"); err != nil {
		t.Fatalf("refused a legitimate department name: %v", err)
	}
	for _, value := range []string{"x\tfrom\ty", "x\nselect\ny", "x,delete,y", "x(drop)y"} {
		if err := RejectQuerySyntax("value", value); err == nil {
			t.Errorf("accepted hidden keyword in %q", value)
		}
	}
}

// FuzzRejectQuerySyntax asserts the guard's one hard promise: whatever it
// accepts carries no banned sequence and no unknown dollar token. A guard that
// panicked or let one through on an unusual input would be the one place in
// the library an injection could start.
func FuzzRejectQuerySyntax(f *testing.F) {
	for _, entry := range []string{
		"ANNUAL", "", TokenEmployeeID, "'; DROP TABLE x; --", "$unknown",
		"a=b", "Research and Development", "\x00", "€", "日本",
	} {
		f.Add(entry)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if RejectQuerySyntax("value", value) != nil {
			return
		}
		lowered := strings.ToLower(value)
		for _, sequence := range bannedSequences {
			if strings.Contains(lowered, sequence) {
				t.Fatalf("accepted %q, which contains %q", value, sequence)
			}
		}
		for _, word := range words(lowered) {
			if _, banned := bannedWords[word]; banned {
				t.Fatalf("accepted %q, which contains the word %q", value, word)
			}
		}
		for index := 0; index < len(value); index++ {
			if value[index] != '$' {
				continue
			}
			if _, ok := matchToken(value[index:]); !ok {
				t.Fatalf("accepted %q, which carries an unknown token at %d", value, index)
			}
		}
	})
}

func corpusEntries(t *testing.T) []string {
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
