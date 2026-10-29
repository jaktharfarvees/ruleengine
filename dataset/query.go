package dataset

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arrowhr/ruleengine/dataset/internal/untenanted"
	"github.com/arrowhr/ruleengine/rule"
)

// Predicate is one resolved filter on a query. Injected marks the tenant
// predicate, which Compile adds and an author cannot: an Executor is expected
// to assert it is present rather than trust that it is.
type Predicate struct {
	Column   string              `json:"column"`
	Op       rule.FilterOperator `json:"op"`
	Values   []any               `json:"values"`
	Injected bool                `json:"injected"`
}

// WindowSpec is a resolved time window. Compile turns a relative window into
// absolute bounds so that an Executor never interprets a token and never reads
// a clock; From is inclusive and To is exclusive.
type WindowSpec struct {
	Column string    `json:"column"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
}

// AggregateSpec is the operation to apply to the matching rows.
type AggregateSpec struct {
	Fn   AggregateFn `json:"fn"`
	On   string      `json:"on,omitempty"`
	Unit string      `json:"unit,omitempty"`
}

// Query is a validated, structured request for one fact.
//
// Every field is unexported and there is no exported constructor, so a Query
// cannot be built outside Compile and an Executor cannot receive one that
// skipped a guardrail (INV4). Accessors return copies of the mutable parts, so
// an Executor cannot alter a query another Point is about to reuse.
//
// A Query is not SQL and contains no SQL. Translating it into a storage
// dialect is the Executor's job.
type Query struct {
	dataset        string
	datasetVersion string
	predicates     []Predicate
	window         *WindowSpec
	aggregate      AggregateSpec
	rowLimit       int
}

// Dataset returns the dataset identifier to query.
func (q Query) Dataset() string { return q.dataset }

// DatasetVersion returns the manifest version the query was compiled against.
// An Executor that caches a translation should key it on this.
func (q Query) DatasetVersion() string { return q.datasetVersion }

// Predicates returns the filters to apply, including the injected tenant
// predicate. The slice is a copy; the values inside are scalars.
func (q Query) Predicates() []Predicate {
	out := make([]Predicate, len(q.predicates))
	for index, predicate := range q.predicates {
		out[index] = predicate
		out[index].Values = append([]any(nil), predicate.Values...)
	}
	return out
}

// TenantPredicate returns the injected tenant predicate. It is always present
// on a compiled query, so the second result being false means the query did
// not come from Compile.
func (q Query) TenantPredicate() (Predicate, bool) {
	for _, predicate := range q.predicates {
		if predicate.Injected {
			return predicate, true
		}
	}
	return Predicate{}, false
}

// untenantedCopy returns a copy of query with its injected tenant predicate
// removed.
//
// It exists for exactly one purpose: the conformance suite in
// dataset/datasettest must be able to present an Executor with the one query
// Compile will never produce, to check that the Executor refuses it rather
// than assuming the predicate is there. It is reachable only through
// dataset/internal/untenanted, so nothing outside dataset can build such a
// query: it is precisely what the injected predicate exists to prevent, and
// handing one to a real Executor asks it for every tenant's rows.
func untenantedCopy(query Query) Query {
	kept := make([]Predicate, 0, len(query.predicates))
	for _, predicate := range query.predicates {
		if !predicate.Injected {
			kept = append(kept, predicate)
		}
	}
	query.predicates = kept
	return query
}

func init() {
	untenanted.Strip = func(query any) any {
		typed, _ := query.(Query)
		return untenantedCopy(typed)
	}
}

// Window returns the resolved time window, if the fact declared one.
func (q Query) Window() (WindowSpec, bool) {
	if q.window == nil {
		return WindowSpec{}, false
	}
	return *q.window, true
}

// Aggregate returns the operation to apply to the matching rows.
func (q Query) Aggregate() AggregateSpec { return q.aggregate }

// RowLimit returns the maximum rows the Executor may scan. It is always
// positive: there is no unbounded query (guardrail G10).
func (q Query) RowLimit() int { return q.rowLimit }

// Fingerprint returns a stable identity for the query.
//
// Two fact sources that differ only in the order of their filters produce the
// same fingerprint, because they are the same question; any difference that
// could change the answer produces a different one. The pipeline uses it to
// fetch a shared fact once per request rather than once per decision point.
func (q Query) Fingerprint() string {
	var builder strings.Builder
	builder.Grow(192)
	builder.WriteString(q.dataset)
	builder.WriteByte('\x1f')
	builder.WriteString(q.datasetVersion)
	builder.WriteByte('\x1f')
	// Predicates are sorted so that authored order cannot change the identity.
	ordered := make([]string, 0, len(q.predicates))
	for _, predicate := range q.predicates {
		var one strings.Builder
		one.WriteString(predicate.Column)
		one.WriteByte('=')
		one.WriteString(string(predicate.Op))
		one.WriteByte('(')
		for valueIndex, value := range predicate.Values {
			if valueIndex > 0 {
				one.WriteByte(',')
			}
			one.WriteString(scalarKey(value))
		}
		one.WriteByte(')')
		if predicate.Injected {
			one.WriteString("!injected")
		}
		ordered = append(ordered, one.String())
	}
	sort.Strings(ordered)
	for _, predicate := range ordered {
		builder.WriteString(predicate)
		builder.WriteByte('\x1e')
	}
	builder.WriteByte('\x1f')
	if q.window != nil {
		builder.WriteString(q.window.Column)
		builder.WriteByte(':')
		builder.WriteString(q.window.From.UTC().Format(time.RFC3339Nano))
		builder.WriteByte(':')
		builder.WriteString(q.window.To.UTC().Format(time.RFC3339Nano))
	}
	builder.WriteByte('\x1f')
	builder.WriteString(string(q.aggregate.Fn))
	builder.WriteByte(':')
	builder.WriteString(q.aggregate.On)
	builder.WriteByte(':')
	builder.WriteString(q.aggregate.Unit)
	builder.WriteByte('\x1f')
	builder.WriteString(strconv.Itoa(q.rowLimit))
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

// Explain returns a plain-language sentence describing the query, for the
// authoring UI's preview and for a support engineer reading an audit record.
// An author who cannot read a decision table can still check this sentence.
func (q Query) Explain() string {
	var builder strings.Builder
	builder.WriteString(describeAggregate(q.aggregate))
	builder.WriteString(" from ")
	builder.WriteString(q.dataset)
	authored := make([]string, 0, len(q.predicates))
	for _, predicate := range q.predicates {
		if predicate.Injected {
			continue
		}
		authored = append(authored, describePredicate(predicate))
	}
	if len(authored) > 0 {
		builder.WriteString(" where ")
		builder.WriteString(strings.Join(authored, " and "))
	}
	if q.window != nil {
		builder.WriteString(", for ")
		builder.WriteString(q.window.Column)
		builder.WriteString(" from ")
		builder.WriteString(q.window.From.UTC().Format(time.DateOnly))
		builder.WriteString(" up to ")
		builder.WriteString(q.window.To.UTC().Format(time.DateOnly))
	}
	builder.WriteString(", scoped to this tenant, at most ")
	builder.WriteString(strconv.Itoa(q.rowLimit))
	builder.WriteString(" rows")
	return builder.String()
}

func describeAggregate(aggregate AggregateSpec) string {
	unit := ""
	if aggregate.Unit != "" {
		unit = " " + aggregate.Unit
	}
	switch aggregate.Fn {
	case AggregateCount:
		return "The number of rows"
	case AggregateExists:
		return "Whether any row exists"
	case AggregateStreak:
		return fmt.Sprintf("The number of consecutive most-recent rows where %s holds", aggregate.On)
	case AggregateLatest:
		return fmt.Sprintf("The most recent %s%s", aggregate.On, unit)
	default:
		return fmt.Sprintf("The %s of %s%s", aggregate.Fn, aggregate.On, unit)
	}
}

func describePredicate(predicate Predicate) string {
	values := make([]string, len(predicate.Values))
	for index, value := range predicate.Values {
		values[index] = scalarKey(value)
	}
	switch predicate.Op {
	case rule.FilterEquals:
		return fmt.Sprintf("%s is %s", predicate.Column, values[0])
	case rule.FilterIn:
		return fmt.Sprintf("%s is one of %s", predicate.Column, strings.Join(values, ", "))
	case rule.FilterNotIn:
		return fmt.Sprintf("%s is none of %s", predicate.Column, strings.Join(values, ", "))
	case rule.FilterRange:
		return fmt.Sprintf("%s is between %s and %s", predicate.Column, values[0], values[1])
	case rule.FilterExists:
		return fmt.Sprintf("%s is present", predicate.Column)
	case rule.FilterNotExists:
		return fmt.Sprintf("%s is absent", predicate.Column)
	default:
		return fmt.Sprintf("%s %s %s", predicate.Column, predicate.Op, strings.Join(values, ", "))
	}
}

// scalarKey renders a predicate value as text, stably. A time is rendered in
// UTC so that the same instant in two zones is one value, not two.
func scalarKey(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(typed)
	}
}
