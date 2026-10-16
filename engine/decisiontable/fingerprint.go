package decisiontable

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	"github.com/arrowhr/ruleengine/rule"
)

// Authored logic is identified by content because Engine.Evaluate receives a
// RuleLogic value rather than a rule identity. The fingerprint therefore walks
// the table directly instead of encoding it: evaluation is on the hot path of
// every payroll record, so it must not pay for JSON encoding or reflection
// over values whose types are already known.
const (
	fingerprintSeed      = 14695981039346656037
	fingerprintLowPrime  = 1099511628211
	fingerprintHighPrime = 0x9E3779B97F4A7C15
	fingerprintSeparator = 0x1f
)

// tableKey is a 128-bit content identity for one authored decision table. Two
// independent accumulators are kept so an in-process cache collision, which
// would serve the wrong compiled table, stays beyond practical reach.
type tableKey struct{ low, high uint64 }

type fingerprint struct {
	key  tableKey
	keys []string
}

func newFingerprint() *fingerprint {
	return &fingerprint{key: tableKey{low: fingerprintSeed, high: fingerprintSeed}}
}

func (f *fingerprint) writeByte(value byte) {
	f.key.low = (f.key.low ^ uint64(value)) * fingerprintLowPrime
	f.key.high = (f.key.high ^ uint64(value)) * fingerprintHighPrime
	f.key.high ^= f.key.high >> 29
}

// writeString appends value followed by a separator, so that writing "ab" then
// "c" never produces the same key as writing "a" then "bc".
func (f *fingerprint) writeString(value string) {
	for index := 0; index < len(value); index++ {
		f.writeByte(value[index])
	}
	f.writeByte(fingerprintSeparator)
}

func (f *fingerprint) writeInt(value int) {
	var scratch [20]byte
	for _, digit := range strconv.AppendInt(scratch[:0], int64(value), 10) {
		f.writeByte(digit)
	}
	f.writeByte(fingerprintSeparator)
}

// sortedKeys reuses one buffer across every row of a table so that hashing a
// hundred-row table allocates once rather than once per row.
func (f *fingerprint) sortedKeys(count int) []string {
	if cap(f.keys) < count {
		f.keys = make([]string, 0, count)
	}
	return f.keys[:0]
}

// logicFingerprint returns a stable content identity for authored logic. It is
// deterministic for a given process: map keys are sorted before hashing, and
// the Go type of every output value contributes, so logic that differs only in
// whether a number arrived as an int or a json.Number compiles separately.
func logicFingerprint(logic rule.RuleLogic) tableKey {
	state := newFingerprint()
	state.writeString(string(logic.HitPolicy))
	state.writeInt(len(logic.Rows))
	for _, row := range logic.Rows {
		state.writeString(row.ID)
		state.writeInt(row.Priority)
		state.writeString(row.Note)
		state.writeInt(len(row.When))
		keys := state.sortedKeys(len(row.When))
		for field := range row.When {
			keys = append(keys, field)
		}
		sort.Strings(keys)
		for _, field := range keys {
			state.writeString(field)
			state.writeString(row.When[field])
		}
		state.keys = keys
		state.writeInt(len(row.Then))
		keys = state.sortedKeys(len(row.Then))
		for name := range row.Then {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			state.writeString(name)
			state.writeValue(row.Then[name])
		}
		state.keys = keys
	}
	return state.key
}

// writeValue hashes an authored output value together with a tag for its Go
// type. The tag is what makes the cache type-sensitive without reflection for
// any type the boundary can actually produce.
func (f *fingerprint) writeValue(value any) {
	switch typed := value.(type) {
	case nil:
		f.writeString("nil")
	case bool:
		f.writeString("bool")
		f.writeString(strconv.FormatBool(typed))
	case string:
		f.writeString("string")
		f.writeString(typed)
	case json.Number:
		f.writeString("number")
		f.writeString(string(typed))
	case int:
		f.writeString("int")
		f.writeInt(typed)
	case int32:
		f.writeString("int32")
		f.writeInt(int(typed))
	case int64:
		f.writeString("int64")
		f.writeString(strconv.FormatInt(typed, 10))
	case float32:
		f.writeString("float32")
		f.writeString(strconv.FormatFloat(float64(typed), 'g', -1, 32))
	case float64:
		f.writeString("float64")
		f.writeString(strconv.FormatFloat(typed, 'g', -1, 64))
	case []any:
		f.writeString("slice")
		f.writeInt(len(typed))
		for _, item := range typed {
			f.writeValue(item)
		}
	case map[string]any:
		f.writeString("map")
		f.writeInt(len(typed))
		nested := make([]string, 0, len(typed))
		for key := range typed {
			nested = append(nested, key)
		}
		sort.Strings(nested)
		for _, key := range nested {
			f.writeString(key)
			f.writeValue(typed[key])
		}
	default:
		// Unreachable for JSON-decoded rules; kept so an unexpected type can
		// never silently share a cache entry with a different one.
		f.writeString("other")
		f.writeString(reflect.TypeOf(value).String())
		f.writeString(fmt.Sprint(value))
	}
}
