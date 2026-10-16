package decimalx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Decimal is an immutable exact decimal value. Its zero value is numeric zero.
type Decimal struct {
	value *big.Rat
	scale int32
}

// Parse parses an ordinary or exponent-form decimal without binary floating
// point conversion.
func Parse(text string) (Decimal, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Decimal{}, fmt.Errorf("decimal is empty")
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return Decimal{}, fmt.Errorf("invalid decimal %q", text)
	}
	return Decimal{value: value, scale: decimalScale(text)}, nil
}

// MustParse parses text and panics when it is invalid. It is intended for
// package constants and tests, not untrusted input.
func MustParse(text string) Decimal {
	value, err := Parse(text)
	if err != nil {
		panic(err)
	}
	return value
}

// FromInt constructs an integer decimal.
func FromInt(value int64) Decimal {
	return Decimal{value: new(big.Rat).SetInt64(value)}
}

// FromValue converts supported exact Go and JSON scalar representations.
// Binary floating-point values are deliberately rejected.
func FromValue(value any) (Decimal, bool) {
	switch typed := value.(type) {
	case Decimal:
		return typed, true
	case *Decimal:
		if typed == nil {
			return Decimal{}, false
		}
		return *typed, true
	case json.Number:
		parsed, err := Parse(string(typed))
		return parsed, err == nil
	case string:
		parsed, err := Parse(typed)
		return parsed, err == nil
	case int:
		return FromInt(int64(typed)), true
	case int8:
		return FromInt(int64(typed)), true
	case int16:
		return FromInt(int64(typed)), true
	case int32:
		return FromInt(int64(typed)), true
	case int64:
		return FromInt(typed), true
	case uint:
		return fromUint(uint64(typed))
	case uint8:
		return fromUint(uint64(typed))
	case uint16:
		return fromUint(uint64(typed))
	case uint32:
		return fromUint(uint64(typed))
	case uint64:
		return fromUint(typed)
	default:
		return Decimal{}, false
	}
}

func fromUint(value uint64) (Decimal, bool) {
	integer := new(big.Int).SetUint64(value)
	return Decimal{value: new(big.Rat).SetInt(integer)}, true
}

// Add returns d+other exactly.
func (d Decimal) Add(other Decimal) Decimal {
	return Decimal{value: new(big.Rat).Add(d.rat(), other.rat()), scale: max(d.scale, other.scale)}
}

// Sub returns d-other exactly.
func (d Decimal) Sub(other Decimal) Decimal {
	return Decimal{value: new(big.Rat).Sub(d.rat(), other.rat()), scale: max(d.scale, other.scale)}
}

// Mul returns d*other exactly.
func (d Decimal) Mul(other Decimal) Decimal {
	return Decimal{value: new(big.Rat).Mul(d.rat(), other.rat()), scale: d.scale + other.scale}
}

// Div returns d/other rounded to scale decimal places.
func (d Decimal) Div(other Decimal, scale int32) (Decimal, error) {
	if other.Sign() == 0 {
		return Decimal{}, fmt.Errorf("decimal division by zero")
	}
	quotient := Decimal{value: new(big.Rat).Quo(d.rat(), other.rat()), scale: scale}
	return quotient.Quantize(scale), nil
}

// Cmp compares d and other.
func (d Decimal) Cmp(other Decimal) int { return d.rat().Cmp(other.rat()) }

// Sign returns -1, 0, or 1 according to the sign of d.
func (d Decimal) Sign() int { return d.rat().Sign() }

// IsInteger reports whether d has no fractional component.
func (d Decimal) IsInteger() bool { return d.rat().Denom().Cmp(big.NewInt(1)) == 0 }

// Scale returns the decimal scale retained by d.
func (d Decimal) Scale() int32 { return d.scale }

// Quantize rounds d to scale decimal places.
func (d Decimal) Quantize(scale int32) Decimal {
	if scale < 0 {
		scale = 0
	}
	text := d.StringFixed(scale)
	value, _ := new(big.Rat).SetString(text)
	return Decimal{value: value, scale: scale}
}

// StringFixed formats d at exactly scale decimal places.
func (d Decimal) StringFixed(scale int32) string {
	if scale < 0 {
		scale = 0
	}
	return d.rat().FloatString(int(scale))
}

// String formats d without insignificant trailing fractional zeros.
func (d Decimal) String() string {
	text := d.StringFixed(d.scale)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	if text == "-0" {
		return "0"
	}
	return text
}

// MarshalJSON preserves the exact decimal as a JSON string.
func (d Decimal) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON accepts either a JSON number or a quoted decimal.
func (d *Decimal) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("decimal receiver is nil")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return fmt.Errorf("decimal JSON is empty")
	}
	text := string(data)
	if data[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return fmt.Errorf("decode decimal: %w", err)
		}
	}
	parsed, err := Parse(text)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// CurrencyScale returns the ISO 4217 minor-unit scale used by currency. Most
// currencies use two digits; known zero- and three-minor-unit currencies are
// handled explicitly.
func CurrencyScale(currency string) int32 {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	case "CLF", "UYW":
		return 4
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "UYI", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	default:
		return 2
	}
}

// QuantizeCurrency rounds d to the declared minor-unit scale for currency.
func QuantizeCurrency(d Decimal, currency string) Decimal {
	return d.Quantize(CurrencyScale(currency))
}

func (d Decimal) rat() *big.Rat {
	if d.value == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(d.value)
}

func decimalScale(text string) int32 {
	text = strings.TrimPrefix(strings.TrimPrefix(text, "+"), "-")
	exponent := int64(0)
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		parsed, err := strconv.ParseInt(text[index+1:], 10, 32)
		if err == nil {
			exponent = parsed
		}
		text = text[:index]
	}
	fraction := int64(0)
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		fraction = int64(len(text) - dot - 1)
	}
	scale := fraction - exponent
	if scale < 0 {
		return 0
	}
	return int32(scale)
}
