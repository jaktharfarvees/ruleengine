package decimalx

import (
	"encoding/json"
	"testing"
)

func TestExactArithmetic(t *testing.T) {
	left := MustParse("0.1")
	right := MustParse("0.2")
	if got := left.Add(right).String(); got != "0.3" {
		t.Fatalf("0.1 + 0.2 = %s", got)
	}
	if got := MustParse("2.50").Sub(MustParse("0.25")).StringFixed(2); got != "2.25" {
		t.Fatalf("subtract = %s", got)
	}
	if got := MustParse("1.20").Mul(MustParse("3")).StringFixed(2); got != "3.60" {
		t.Fatalf("multiply = %s", got)
	}
	quotient, err := MustParse("1").Div(MustParse("8"), 3)
	if err != nil || quotient.StringFixed(3) != "0.125" {
		t.Fatalf("divide = %s, %v", quotient.StringFixed(3), err)
	}
	if _, err := left.Div(Decimal{}, 2); err == nil {
		t.Fatal("division by zero succeeded")
	}
}

func TestQuantizeAndCurrencyScale(t *testing.T) {
	if got := QuantizeCurrency(MustParse("12.345"), "LKR").StringFixed(2); got != "12.34" {
		t.Fatalf("LKR quantization = %s", got)
	}
	if got := QuantizeCurrency(MustParse("12.5"), "JPY").StringFixed(0); got != "12" {
		t.Fatalf("JPY quantization = %s", got)
	}
	if got := QuantizeCurrency(MustParse("1.2345"), "KWD").StringFixed(3); got != "1.234" {
		t.Fatalf("KWD quantization = %s", got)
	}
	if CurrencyScale(" usd ") != 2 || CurrencyScale("KRW") != 0 || CurrencyScale("BHD") != 3 || CurrencyScale("CLF") != 4 {
		t.Fatal("unexpected currency scales")
	}
	if got := MustParse("12.355").Quantize(2).StringFixed(2); got != "12.36" {
		t.Fatalf("half-even odd tie = %s", got)
	}
	if got := MustParse("-12.345").Quantize(2).StringFixed(2); got != "-12.34" {
		t.Fatalf("negative half-even tie = %s", got)
	}
	if _, err := CurrencyScaleStrict("ZZZ"); err == nil {
		t.Fatal("unknown currency silently used a two-digit scale")
	}
	if got := MustParse("-0.000").String(); got != "0" {
		t.Fatalf("negative zero = %q", got)
	}
}

func TestParseScaleAndComparison(t *testing.T) {
	tests := []struct {
		text  string
		scale int32
		want  string
	}{
		{"42", 0, "42"},
		{"-1.250", 3, "-1.25"},
		{"1.2e2", 0, "120"},
		{"1.2e-2", 3, "0.012"},
	}
	for _, test := range tests {
		value, err := Parse(test.text)
		if err != nil || value.Scale() != test.scale || value.String() != test.want {
			t.Errorf("Parse(%q) = %q scale %d, %v", test.text, value.String(), value.Scale(), err)
		}
	}
	if _, err := Parse(""); err == nil {
		t.Fatal("empty decimal succeeded")
	}
	if _, err := Parse("not-a-number"); err == nil {
		t.Fatal("malformed decimal succeeded")
	}
	if MustParse("2").Cmp(MustParse("1.999")) <= 0 || MustParse("-1").Sign() >= 0 {
		t.Fatal("comparison or sign is incorrect")
	}
	if !MustParse("2.0").IsInteger() || MustParse("2.1").IsInteger() {
		t.Fatal("integer classification is incorrect")
	}
}

func TestFromValue(t *testing.T) {
	decimal := MustParse("3.5")
	tests := []any{decimal, &decimal, json.Number("3.5"), "3.5", int(3), int8(3), int16(3), int32(3), int64(3), uint(3), uint8(3), uint16(3), uint32(3), uint64(3)}
	for _, value := range tests {
		if _, ok := FromValue(value); !ok {
			t.Errorf("FromValue(%T) failed", value)
		}
	}
	if _, ok := FromValue((*Decimal)(nil)); ok {
		t.Fatal("nil decimal pointer succeeded")
	}
	if _, ok := FromValue(float64(3.5)); ok {
		t.Fatal("binary float succeeded")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	original := MustParse("123.450")
	data, err := json.Marshal(original)
	if err != nil || string(data) != `"123.45"` {
		t.Fatalf("Marshal() = %s, %v", data, err)
	}
	for _, data := range [][]byte{[]byte(`"123.450"`), []byte(`123.450`)} {
		var decoded Decimal
		if err := json.Unmarshal(data, &decoded); err != nil || decoded.Cmp(original) != 0 {
			t.Fatalf("Unmarshal(%s) = %s, %v", data, decoded.String(), err)
		}
	}
	var nilDecimal *Decimal
	if err := nilDecimal.UnmarshalJSON([]byte(`1`)); err == nil {
		t.Fatal("nil receiver succeeded")
	}
	for _, data := range [][]byte{nil, []byte(`"bad"`), []byte(`"unterminated`)} {
		var decoded Decimal
		if err := decoded.UnmarshalJSON(data); err == nil {
			t.Fatalf("invalid JSON %q succeeded", data)
		}
	}
}
