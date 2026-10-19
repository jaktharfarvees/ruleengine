package dataset

import (
	"fmt"
	"time"

	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

// Params are the request values Compile may substitute. They arrive from the
// pipeline, never from an authored rule, which is why a token can never widen
// a query beyond the subject being decided.
type Params struct {
	Tenant     string
	EmployeeID string
	NodeID     string
	AsOf       time.Time
	ValidAt    time.Time
	// RowLimit is the cap the fact budget allows for one query. Compile
	// refuses to produce an unbounded query, so this must be positive.
	RowLimit int
}

// Fault is a compile failure carrying the reason code the decision must fail
// closed with. Compile returns one for every refusal so the pipeline never has
// to infer a reason code from an error string.
type Fault struct {
	Code    string
	Dataset string
	Column  string
	Err     error
}

// Error returns a message free of any fact value.
func (f *Fault) Error() string {
	if f == nil {
		return "<nil>"
	}
	message := f.Code
	if f.Dataset != "" {
		message += fmt.Sprintf(" dataset=%q", f.Dataset)
	}
	if f.Column != "" {
		message += fmt.Sprintf(" column=%q", f.Column)
	}
	if f.Err != nil {
		message += ": " + f.Err.Error()
	}
	return message
}

// Unwrap exposes the cause.
func (f *Fault) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Err
}

// The reason codes Compile can produce. They mirror the library's catalogue;
// this package does not import the root package (INV1), so it names them as
// strings and the pipeline maps them to typed codes.
const (
	// ReasonDatasetNotPublished is returned when the dataset is unknown or not
	// in a queryable status.
	ReasonDatasetNotPublished = "DATASET_NOT_PUBLISHED"
	// ReasonDatasetDeprecated is a non-fatal note that the dataset is retiring.
	ReasonDatasetDeprecated = "DATASET_DEPRECATED"
	// ReasonRawQueryRejected is returned when an author string contains query
	// or script syntax. It is meant to alert.
	ReasonRawQueryRejected = "FACT_RAW_QUERY_REJECTED"
	// ReasonColumnNotAllowed is returned when a column is used in a way the
	// manifest does not permit.
	ReasonColumnNotAllowed = "FACT_COLUMN_NOT_ALLOWED"
	// ReasonAggregateUnsupported is returned when the dataset does not offer
	// the aggregate.
	ReasonAggregateUnsupported = "FACT_AGGREGATE_UNSUPPORTED"
	// ReasonWindowTooWide is returned when the window exceeds the dataset
	// maximum.
	ReasonWindowTooWide = "FACT_WINDOW_TOO_WIDE"
	// ReasonUnresolvable is returned when the declaration itself is unusable.
	ReasonUnresolvable = "FACT_UNRESOLVABLE"
	// ReasonPIIColumnInFilter is a non-fatal note that a filter used a personal
	// column other than the subject key.
	ReasonPIIColumnInFilter = "PII_COLUMN_IN_FILTER"
)

// Compile turns a declared dataset fact source into a validated Query, and
// returns the non-fatal reason codes the decision should carry.
//
// Guardrails, in the order they are applied:
//
//	G1  the dataset is published (deprecated is allowed, with a reason code)
//	G2  no author string contains query or script syntax
//	G3  every filter column exists and is filterable
//	G4  the window column is windowable and the aggregate column aggregatable
//	G5  the window is positive and within the dataset maximum
//	G6  the aggregate is one the dataset offers
//	G7  filter values match the column type, and the enum value set
//	G8  only the four substitution tokens are honoured
//	G9  the tenant predicate is injected, and an author cannot write one
//	G10 a row limit is always set
//	G11 a relative window is resolved to absolute times
//	G12 a personal column used as a filter is noted unless it is the subject key
func Compile(source registry.FactSource, manifest Manifest, params Params) (Query, []string, error) {
	source = source.Canonical()
	manifest = manifest.Canonical()
	if source.Kind != registry.FactDataset {
		return Query{}, nil, &Fault{Code: ReasonUnresolvable,
			Err: fmt.Errorf("fact source kind %q is not a dataset source", source.Kind)}
	}
	if err := manifest.Validate(); err != nil {
		return Query{}, nil, &Fault{Code: ReasonDatasetNotPublished, Dataset: manifest.Dataset, Err: err}
	}
	if source.Dataset != manifest.Dataset {
		return Query{}, nil, &Fault{Code: ReasonDatasetNotPublished, Dataset: source.Dataset,
			Err: fmt.Errorf("declared dataset does not match the manifest %q", manifest.Dataset)}
	}
	if params.Tenant == "" {
		return Query{}, nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset,
			Err: fmt.Errorf("a tenant is required to scope a dataset query")}
	}

	var notes []string

	// G1. A dataset that is not published cannot be queried at all; one that
	// is deprecated still answers, but the decision records that it is going
	// away so a migration can be driven from the audit trail.
	queryable, deprecated := manifest.Queryable()
	if !queryable {
		return Query{}, nil, &Fault{Code: ReasonDatasetNotPublished, Dataset: manifest.Dataset,
			Err: fmt.Errorf("status %q is not queryable", manifest.Status)}
	}
	if deprecated {
		notes = append(notes, ReasonDatasetDeprecated)
	}

	// G10. The budget decides how many rows one fact may scan, and a manifest
	// may cap its own dataset lower. They are different controls: the budget
	// protects the decision, the cap protects the store.
	rowLimit := params.RowLimit
	if rowLimit <= 0 {
		return Query{}, nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset,
			Err: fmt.Errorf("a positive row limit is required; there is no unbounded query")}
	}
	if manifest.RowLimit > 0 && manifest.RowLimit < rowLimit {
		rowLimit = manifest.RowLimit
	}

	predicates, filterNotes, err := compileFilters(source.Filters, manifest, params)
	if err != nil {
		return Query{}, nil, err
	}
	notes = append(notes, filterNotes...)

	// G9. The tenant predicate is the library's, always, and is appended after
	// the author's filters so it is visible last in every explanation.
	predicates = append(predicates, Predicate{
		Column:   TenantColumn,
		Op:       rule.FilterEquals,
		Values:   []any{params.Tenant},
		Injected: true,
	})

	window, err := compileWindow(source.Window, manifest, params)
	if err != nil {
		return Query{}, nil, err
	}

	aggregate, err := compileAggregate(source.Aggregate, manifest)
	if err != nil {
		return Query{}, nil, err
	}

	// streak and latest are defined by row order, and row order over an
	// unbounded set is whatever the store happens to return. Requiring a
	// window makes both deterministic, which the replay contract needs: the
	// same request re-run for a payroll correction has to produce the same
	// fact, not the same fact most of the time.
	if window == nil && orderDependent(aggregate.Fn) {
		return Query{}, nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset,
			Err: fmt.Errorf("the %q aggregate depends on row order and requires a window", aggregate.Fn)}
	}

	return Query{
		dataset:        manifest.Dataset,
		datasetVersion: manifest.Version,
		predicates:     predicates,
		window:         window,
		aggregate:      aggregate,
		rowLimit:       rowLimit,
	}, notes, nil
}

func compileFilters(filters []rule.FilterCondition, manifest Manifest, params Params) ([]Predicate, []string, error) {
	predicates := make([]Predicate, 0, len(filters)+1)
	var notes []string
	for index, filter := range filters {
		where := fmt.Sprintf("filter %d", index)

		// G2 and G8 on the column name itself: a column is author-controlled
		// text like any value.
		if err := rejectQuerySyntax(where+" column", filter.Attribute); err != nil {
			return nil, nil, &Fault{Code: ReasonRawQueryRejected, Dataset: manifest.Dataset, Err: err}
		}

		// G9. An author filtering on the tenant column is an error, not
		// something to merge with the injected predicate: the author must not
		// control tenant scoping, and a conflicting pair would be ambiguous.
		if filter.Attribute == TenantColumn {
			return nil, nil, &Fault{Code: ReasonColumnNotAllowed, Dataset: manifest.Dataset, Column: filter.Attribute,
				Err: fmt.Errorf("tenant scoping is injected by the library and must not be authored")}
		}

		// G3. The column exists and the owning service permits filtering it.
		column, declared := manifest.Column(filter.Attribute)
		if !declared {
			return nil, nil, &Fault{Code: ReasonColumnNotAllowed, Dataset: manifest.Dataset, Column: filter.Attribute,
				Err: fmt.Errorf("column is not declared by the manifest")}
		}
		if !column.Filterable {
			return nil, nil, &Fault{Code: ReasonColumnNotAllowed, Dataset: manifest.Dataset, Column: column.Name,
				Err: fmt.Errorf("column is not filterable")}
		}

		values, err := compileValues(where, filter, column, manifest, params)
		if err != nil {
			return nil, nil, err
		}

		// G12. A personal column has to be filterable — the subject key is one
		// — but any other personal column in a filter is worth recording.
		if column.PII && !isSubjectKey(filter, params) {
			notes = append(notes, ReasonPIIColumnInFilter)
		}

		predicates = append(predicates, Predicate{Column: column.Name, Op: filter.Op, Values: values})
	}
	return predicates, notes, nil
}

// compileValues normalises one filter's operand into the scalar list a
// Predicate carries, substituting tokens and type-checking against the column.
func compileValues(where string, filter rule.FilterCondition, column Column, manifest Manifest, params Params) ([]any, error) {
	var wantCount int
	switch filter.Op {
	case rule.FilterEquals:
		wantCount = 1
	case rule.FilterIn, rule.FilterNotIn:
		wantCount = -1
	case rule.FilterRange:
		wantCount = 2
	case rule.FilterExists, rule.FilterNotExists:
		if filter.Value != nil {
			return nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset, Column: column.Name,
				Err: fmt.Errorf("%s uses an existence operator and must carry no value", where)}
		}
		return nil, nil
	default:
		return nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset, Column: column.Name,
			Err: fmt.Errorf("%s uses unsupported operator %q", where, filter.Op)}
	}

	raw, err := operands(where, filter.Value, manifest, column)
	if err != nil {
		return nil, err
	}
	if wantCount > 0 && len(raw) != wantCount {
		return nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset, Column: column.Name,
			Err: fmt.Errorf("%s with operator %q requires %d values, got %d", where, filter.Op, wantCount, len(raw))}
	}
	if wantCount == -1 && len(raw) == 0 {
		return nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset, Column: column.Name,
			Err: fmt.Errorf("%s with operator %q requires a non-empty set", where, filter.Op)}
	}

	values := make([]any, 0, len(raw))
	for _, operand := range raw {
		// G2 and G8 on every author-controlled string value.
		if text, isText := operand.(string); isText {
			if err := rejectQuerySyntax(where+" value", text); err != nil {
				return nil, &Fault{Code: ReasonRawQueryRejected, Dataset: manifest.Dataset, Column: column.Name, Err: err}
			}
			substituted, wasToken, err := substitute(text, params)
			if err != nil {
				return nil, &Fault{Code: ReasonRawQueryRejected, Dataset: manifest.Dataset, Column: column.Name, Err: err}
			}
			if wasToken {
				values = append(values, substituted)
				continue
			}
		}
		// G7. The value must be a usable representation of the column's type,
		// and within the enum's value set when the column is an enum. This is
		// the same check that will type-check the resulting fact, so a filter
		// cannot pass here and fail there.
		if !column.Type.Accepts(operand, column.Values) {
			return nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset, Column: column.Name,
				Err: fmt.Errorf("%s value is not a valid %s for this column", where, column.Type)}
		}
		values = append(values, operand)
	}
	return values, nil
}

// operands flattens a filter value into a scalar list without reflection on
// the common shapes an authored JSON filter produces.
func operands(where string, value any, manifest Manifest, column Column) ([]any, error) {
	switch typed := value.(type) {
	case nil:
		return nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset, Column: column.Name,
			Err: fmt.Errorf("%s carries no value", where)}
	case []any:
		return typed, nil
	case []string:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = item
		}
		return out, nil
	default:
		return []any{value}, nil
	}
}

// substitute replaces a whole-value token from Params. A token is only ever a
// complete value: "$subject.employeeId" is substituted, "E-$subject.employeeId"
// is refused, because concatenating request data into an authored string is the
// shape every injection takes.
func substitute(text string, params Params) (any, bool, error) {
	switch text {
	case TokenEmployeeID:
		if params.EmployeeID == "" {
			return nil, false, fmt.Errorf("%s was used but the request carries no employee identifier", TokenEmployeeID)
		}
		return params.EmployeeID, true, nil
	case TokenNodeID:
		if params.NodeID == "" {
			return nil, false, fmt.Errorf("%s was used but the request carries no node identifier", TokenNodeID)
		}
		return params.NodeID, true, nil
	case TokenAsOf:
		return params.AsOf, true, nil
	case TokenValidAt:
		return params.ValidAt, true, nil
	}
	// Anything else is plain text. rejectQuerySyntax ran first and has already
	// proved it carries no dollar sign, so there is no token left to miss.
	return text, false, nil
}

func compileWindow(window *registry.FactWindow, manifest Manifest, params Params) (*WindowSpec, error) {
	if window == nil {
		return nil, nil
	}
	if err := rejectQuerySyntax("window column", window.Column); err != nil {
		return nil, &Fault{Code: ReasonRawQueryRejected, Dataset: manifest.Dataset, Err: err}
	}
	// A column-pair window is not expressible as a single bounded range here;
	// it is refused rather than silently narrowed to one of the two columns.
	if window.FromColumn != "" || window.ToColumn != "" {
		return nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset,
			Err: fmt.Errorf("a from/to column window is not supported yet; declare a lastDays window instead")}
	}

	// G4. The window column must exist and be one the owning service has
	// promised to range-scan.
	column, declared := manifest.Column(window.Column)
	if !declared {
		return nil, &Fault{Code: ReasonColumnNotAllowed, Dataset: manifest.Dataset, Column: window.Column,
			Err: fmt.Errorf("window column is not declared by the manifest")}
	}
	if !column.Windowable {
		return nil, &Fault{Code: ReasonColumnNotAllowed, Dataset: manifest.Dataset, Column: column.Name,
			Err: fmt.Errorf("column is not windowable")}
	}

	// G5. A window must be positive and within the dataset's maximum. The
	// message names both numbers so an author can fix it without reading docs.
	if window.LastDays <= 0 {
		return nil, &Fault{Code: ReasonWindowTooWide, Dataset: manifest.Dataset, Column: column.Name,
			Err: fmt.Errorf("lastDays must be positive, got %d", window.LastDays)}
	}
	if window.LastDays > manifest.MaxWindowDays {
		return nil, &Fault{Code: ReasonWindowTooWide, Dataset: manifest.Dataset, Column: column.Name,
			Err: fmt.Errorf("lastDays %d exceeds the dataset maximum of %d", window.LastDays, manifest.MaxWindowDays)}
	}

	// G11. RelativeTo is resolved here, so the Executor never interprets a
	// token and never reads a clock: the same request replayed next year
	// produces the same absolute window.
	var anchor time.Time
	switch window.RelativeTo {
	case TokenValidAt, "":
		anchor = params.ValidAt
	case TokenAsOf:
		anchor = params.AsOf
	default:
		return nil, &Fault{Code: ReasonRawQueryRejected, Dataset: manifest.Dataset, Column: column.Name,
			Err: fmt.Errorf("window relativeTo must be %s or %s, got %q", TokenAsOf, TokenValidAt, window.RelativeTo)}
	}
	if anchor.IsZero() {
		return nil, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset, Column: column.Name,
			Err: fmt.Errorf("window is relative to %q but the request carries no such time", window.RelativeTo)}
	}

	// The window ends at the anchor and is exclusive at the upper bound, so a
	// day is counted once however the store stores its timestamps.
	to := anchor.UTC()
	from := to.AddDate(0, 0, -window.LastDays)
	return &WindowSpec{Column: column.Name, From: from, To: to}, nil
}

func compileAggregate(aggregate *registry.FactAggregate, manifest Manifest) (AggregateSpec, error) {
	if aggregate == nil {
		return AggregateSpec{}, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset,
			Err: fmt.Errorf("a dataset fact source must declare an aggregate")}
	}
	if !aggregate.Fn.Valid() {
		return AggregateSpec{}, &Fault{Code: ReasonAggregateUnsupported, Dataset: manifest.Dataset,
			Err: fmt.Errorf("unknown aggregate %q", aggregate.Fn)}
	}

	// G6. The dataset decides which aggregates it can answer. A store without
	// an ordered index cannot answer a streak, and refusing is better than
	// scanning its whole history to fake one.
	if !manifest.Offers(aggregate.Fn) {
		return AggregateSpec{}, &Fault{Code: ReasonAggregateUnsupported, Dataset: manifest.Dataset,
			Err: fmt.Errorf("dataset %q does not offer the %q aggregate", manifest.Dataset, aggregate.Fn)}
	}

	if err := rejectQuerySyntax("aggregate column", aggregate.On); err != nil {
		return AggregateSpec{}, &Fault{Code: ReasonRawQueryRejected, Dataset: manifest.Dataset, Err: err}
	}
	if err := rejectQuerySyntax("aggregate unit", aggregate.Unit); err != nil {
		return AggregateSpec{}, &Fault{Code: ReasonRawQueryRejected, Dataset: manifest.Dataset, Err: err}
	}

	if !aggregate.Fn.NeedsColumn() {
		if aggregate.On != "" {
			return AggregateSpec{}, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset, Column: aggregate.On,
				Err: fmt.Errorf("the %q aggregate is about rows and must not name a column", aggregate.Fn)}
		}
		return AggregateSpec{Fn: aggregate.Fn, Unit: aggregate.Unit}, nil
	}

	// G4. The aggregate column must exist and be one the service permits
	// aggregating.
	if aggregate.On == "" {
		return AggregateSpec{}, &Fault{Code: ReasonUnresolvable, Dataset: manifest.Dataset,
			Err: fmt.Errorf("the %q aggregate requires a column", aggregate.Fn)}
	}
	column, declared := manifest.Column(aggregate.On)
	if !declared {
		return AggregateSpec{}, &Fault{Code: ReasonColumnNotAllowed, Dataset: manifest.Dataset, Column: aggregate.On,
			Err: fmt.Errorf("aggregate column is not declared by the manifest")}
	}
	if !column.Aggregatable {
		return AggregateSpec{}, &Fault{Code: ReasonColumnNotAllowed, Dataset: manifest.Dataset, Column: column.Name,
			Err: fmt.Errorf("column is not aggregatable")}
	}
	return AggregateSpec{Fn: aggregate.Fn, On: column.Name, Unit: aggregate.Unit}, nil
}

// isSubjectKey reports whether a filter is the subject-identity filter, which
// is the one personal filter every per-employee fact needs.
func isSubjectKey(filter rule.FilterCondition, params Params) bool {
	if filter.Op != rule.FilterEquals {
		return false
	}
	text, ok := filter.Value.(string)
	if !ok {
		return false
	}
	return text == TokenEmployeeID || (params.EmployeeID != "" && text == params.EmployeeID)
}

// orderDependent reports whether an aggregate's answer depends on the order of
// the rows it sees, and therefore on a window to order them by.
func orderDependent(aggregate AggregateFn) bool {
	return aggregate == AggregateStreak || aggregate == AggregateLatest
}
