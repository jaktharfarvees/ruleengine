package registry

import "github.com/arrowhr/ruleengine/rule"

// The declaration of where a field's value comes from lives here, with the
// field contract it belongs to, because plan section 4.4 makes `factSource` a
// FieldRegistry field. Resolving a declaration is a separate job and lives in
// the fact package, which re-exports these names as aliases. See
// docs/adr/0009-fact-source-declaration-lives-with-the-field.md.

// FactKind identifies how a fact value is obtained.
type FactKind string

const (
	// FactPassed arrives in the request context; nothing is fetched.
	FactPassed FactKind = "passed"
	// FactDerived is a formula over other already-resolved fields.
	FactDerived FactKind = "derived"
	// FactDataset is a compiled query over a published dataset.
	FactDataset FactKind = "dataset"
	// FactNative is a Go function the owning service registers.
	FactNative FactKind = "native"
)

// Valid reports whether k belongs to the closed fact-source kind set.
func (k FactKind) Valid() bool {
	switch k {
	case FactPassed, FactDerived, FactDataset, FactNative:
		return true
	default:
		return false
	}
}

// String returns the wire representation.
func (k FactKind) String() string { return string(k) }

// AggregateFn identifies a closed dataset aggregation operation.
type AggregateFn string

const (
	// AggregateCount counts matching rows.
	AggregateCount AggregateFn = "count"
	// AggregateSum adds the values of the aggregate column.
	AggregateSum AggregateFn = "sum"
	// AggregateMax is the largest value of the aggregate column.
	AggregateMax AggregateFn = "max"
	// AggregateMin is the smallest value of the aggregate column.
	AggregateMin AggregateFn = "min"
	// AggregateAvg is the mean of the aggregate column.
	AggregateAvg AggregateFn = "avg"
	// AggregateStreak counts rows consecutively satisfying the aggregate
	// column, counting back from the end of the window and stopping at the
	// first row that does not.
	AggregateStreak AggregateFn = "streak"
	// AggregateLatest is the aggregate column of the most recent row.
	AggregateLatest AggregateFn = "latest"
	// AggregateExists reports whether any row matches.
	AggregateExists AggregateFn = "exists"
)

// Valid reports whether f belongs to the closed aggregate set.
func (f AggregateFn) Valid() bool {
	switch f {
	case AggregateCount, AggregateSum, AggregateMax, AggregateMin,
		AggregateAvg, AggregateStreak, AggregateLatest, AggregateExists:
		return true
	default:
		return false
	}
}

// String returns the wire representation.
func (f AggregateFn) String() string { return string(f) }

// NeedsColumn reports whether f operates on a named column. count and exists
// are about rows rather than values, so they need none.
func (f AggregateFn) NeedsColumn() bool {
	return f != AggregateCount && f != AggregateExists
}

// FactSource declares how a registered field is resolved. Which fields apply
// depends on Kind: Expression for derived, NativeRef for native, and
// Dataset with Filters, Window and Aggregate for dataset.
type FactSource struct {
	Kind       FactKind               `json:"kind"`
	Dataset    string                 `json:"dataset,omitempty"`
	Filters    []rule.FilterCondition `json:"filters,omitempty"`
	Window     *FactWindow            `json:"window,omitempty"`
	Aggregate  *FactAggregate         `json:"aggregate,omitempty"`
	Expression string                 `json:"expression,omitempty"`
	NativeRef  string                 `json:"nativeRef,omitempty"`
	OnEmpty    any                    `json:"onEmpty,omitempty"`
	Notes      string                 `json:"notes,omitempty"`
	Version    string                 `json:"version,omitempty"`
}

// FactWindow describes a bounded fact-source time window. Either LastDays
// counting back from RelativeTo, or an explicit column pair on the row.
type FactWindow struct {
	Column     string `json:"column,omitempty"`
	RelativeTo string `json:"relativeTo,omitempty"`
	LastDays   int    `json:"lastDays,omitempty"`
	FromColumn string `json:"fromColumn,omitempty"`
	ToColumn   string `json:"toColumn,omitempty"`
}

// FactAggregate describes the operation applied to a dataset fact source.
type FactAggregate struct {
	Fn   AggregateFn `json:"fn"`
	On   string      `json:"on,omitempty"`
	Unit string      `json:"unit,omitempty"`
}
