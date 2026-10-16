package dataset

import (
	"fmt"
	"time"

	"github.com/arrowhr/ruleengine/registry"
)

// AggregateFn identifies a closed dataset aggregation operation. It is defined
// in the registry package, with the fact-source declaration that names it, and
// aliased here because a caller reads it as a dataset concept.
type AggregateFn = registry.AggregateFn

// The closed aggregate set, re-exported so a caller reads these as dataset
// concepts. They are the same values the registry declares.
const (
	// AggregateCount counts matching rows.
	AggregateCount = registry.AggregateCount
	// AggregateSum adds the values of the aggregate column.
	AggregateSum = registry.AggregateSum
	// AggregateMax is the largest value of the aggregate column.
	AggregateMax = registry.AggregateMax
	// AggregateMin is the smallest value of the aggregate column.
	AggregateMin = registry.AggregateMin
	// AggregateAvg is the mean of the aggregate column.
	AggregateAvg = registry.AggregateAvg
	// AggregateStreak counts the consecutive most-recent rows satisfying the
	// aggregate column, stopping at the first that does not.
	AggregateStreak = registry.AggregateStreak
	// AggregateLatest is the aggregate column of the most recent row.
	AggregateLatest = registry.AggregateLatest
	// AggregateExists reports whether any row matches.
	AggregateExists = registry.AggregateExists
)

// TenantColumn is the column every tenant-scoped dataset is required to carry
// and that Compile always injects a predicate on.
//
// It is deliberately absent from every manifest. An author therefore cannot
// select it, filter on it, or aggregate over it, and cannot be the reason a
// query crosses a tenant boundary: tenant scoping is the library's to set, not
// the author's (guardrail G9).
const TenantColumn = "tenantId"

// Published is the manifest status a dataset must hold to be queried at all.
const Published = "published"

// Deprecated is a status that still resolves but records a reason code, so a
// dataset can be retired without breaking a decision on the day it is marked.
const Deprecated = "deprecated"

// Column is one column an author may build a field from. The three booleans
// are permissions, not descriptions: a column that is not Filterable cannot be
// filtered on however sensible that would be, because the owning service has
// not promised an index for it.
type Column struct {
	Name         string            `json:"name"`
	Type         registry.DataType `json:"type"`
	Filterable   bool              `json:"filterable"`
	Aggregatable bool              `json:"aggregatable"`
	Windowable   bool              `json:"windowable"`
	Values       []string          `json:"values,omitempty"`
	PII          bool              `json:"pii"`
	Description  string            `json:"description,omitempty"`
}

// Manifest is a read-only description of one of a service's own data shapes:
// which columns exist, how each may be used, and the safety limits. It is
// authored in the owning service's repository and published like any other
// artifact; this package parses and enforces it but never creates one.
type Manifest struct {
	Dataset       string        `json:"dataset"`
	OwnerService  string        `json:"ownerService"`
	Domain        string        `json:"domain"`
	Grain         string        `json:"grain"`
	Description   string        `json:"description,omitempty"`
	TenantScoped  bool          `json:"tenantScoped"`
	Status        string        `json:"status"`
	Version       string        `json:"version"`
	PublishedAt   time.Time     `json:"publishedAt"`
	MaxWindowDays int           `json:"maxWindowDays"`
	RowLimit      int           `json:"rowLimit,omitempty"`
	Aggregates    []AggregateFn `json:"aggregates"`
	Columns       []Column      `json:"columns"`
}

// Validate checks a manifest is usable before any field is compiled against
// it. A manifest is published rarely and read on every decision, so every
// check that can happen here rather than per evaluation belongs here.
func (m Manifest) Validate() error {
	if m.Dataset == "" {
		return fmt.Errorf("manifest dataset identifier is required")
	}
	if m.OwnerService == "" || m.Domain == "" {
		return fmt.Errorf("manifest %q requires an owner service and a domain", m.Dataset)
	}
	// The grain states what one row means. Without it an aggregate is
	// uninterpretable: "sum of days" over an unknown grain is not a number
	// anybody can act on.
	if m.Grain == "" {
		return fmt.Errorf("manifest %q requires a grain stating what one row represents", m.Dataset)
	}
	// A dataset that is not tenant-scoped cannot be made safe by an injected
	// predicate, so it is refused outright rather than queried carefully.
	if !m.TenantScoped {
		return fmt.Errorf("manifest %q must be tenant scoped", m.Dataset)
	}
	if m.Version == "" {
		return fmt.Errorf("manifest %q requires a version", m.Dataset)
	}
	if m.Status == "" {
		return fmt.Errorf("manifest %q requires a status", m.Dataset)
	}
	if m.MaxWindowDays <= 0 {
		return fmt.Errorf("manifest %q requires a positive maxWindowDays", m.Dataset)
	}
	if m.RowLimit < 0 {
		return fmt.Errorf("manifest %q rowLimit must not be negative", m.Dataset)
	}
	if len(m.Aggregates) == 0 {
		return fmt.Errorf("manifest %q offers no aggregates", m.Dataset)
	}
	seenAggregate := make(map[AggregateFn]struct{}, len(m.Aggregates))
	for _, aggregate := range m.Aggregates {
		if !aggregate.Valid() {
			return fmt.Errorf("manifest %q offers unknown aggregate %q", m.Dataset, aggregate)
		}
		if _, duplicate := seenAggregate[aggregate]; duplicate {
			return fmt.Errorf("manifest %q lists aggregate %q twice", m.Dataset, aggregate)
		}
		seenAggregate[aggregate] = struct{}{}
	}
	if len(m.Columns) == 0 {
		return fmt.Errorf("manifest %q declares no columns", m.Dataset)
	}
	seenColumn := make(map[string]struct{}, len(m.Columns))
	for index, column := range m.Columns {
		if column.Name == "" {
			return fmt.Errorf("manifest %q column %d has no name", m.Dataset, index)
		}
		if _, duplicate := seenColumn[column.Name]; duplicate {
			return fmt.Errorf("manifest %q declares column %q twice", m.Dataset, column.Name)
		}
		seenColumn[column.Name] = struct{}{}
		if column.Name == TenantColumn {
			return fmt.Errorf("manifest %q must not declare the implicit %q column", m.Dataset, TenantColumn)
		}
		if !column.Type.Valid() {
			return fmt.Errorf("manifest %q column %q has unknown type %q", m.Dataset, column.Name, column.Type)
		}
		if column.Type == registry.DataEnum && len(column.Values) == 0 {
			return fmt.Errorf("manifest %q enum column %q declares no values", m.Dataset, column.Name)
		}
		if column.Type != registry.DataEnum && len(column.Values) > 0 {
			return fmt.Errorf("manifest %q column %q declares values but is not an enum", m.Dataset, column.Name)
		}
		if column.Windowable && !windowableType(column.Type) {
			return fmt.Errorf("manifest %q column %q is windowable but is %q, not a date or datetime", m.Dataset, column.Name, column.Type)
		}
	}
	return nil
}

// Column returns a declared column by name.
func (m Manifest) Column(name string) (Column, bool) {
	for _, column := range m.Columns {
		if column.Name == name {
			return column, true
		}
	}
	return Column{}, false
}

// Offers reports whether the dataset offers an aggregate.
func (m Manifest) Offers(aggregate AggregateFn) bool {
	for _, offered := range m.Aggregates {
		if offered == aggregate {
			return true
		}
	}
	return false
}

// Queryable reports whether the dataset may be queried, and whether doing so
// should record a deprecation reason code.
func (m Manifest) Queryable() (ok, deprecated bool) {
	switch m.Status {
	case Published:
		return true, false
	case Deprecated:
		return true, true
	default:
		return false, false
	}
}

func windowableType(dataType registry.DataType) bool {
	return dataType == registry.DataDate || dataType == registry.DataDateTime
}
