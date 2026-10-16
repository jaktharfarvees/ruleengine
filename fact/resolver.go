package fact

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/telemetry"
)

// The reason codes fact resolution can produce. This package does not import
// the root package (INV1), so it names them as strings and the pipeline maps
// them onto the typed catalogue.
const (
	// ReasonFieldNotRegistered is returned for a field absent from the
	// registry. It is always fatal: an unregistered field is never null.
	ReasonFieldNotRegistered = "FIELD_NOT_REGISTERED"
	// ReasonFieldDeprecated is a non-fatal note that a deprecated field was
	// used.
	ReasonFieldDeprecated = "FIELD_DEPRECATED"
	// ReasonFieldTypeMismatch is returned when a value does not match the
	// field's declared type.
	ReasonFieldTypeMismatch = "FIELD_TYPE_MISMATCH"
	// ReasonUnresolvable is returned when a declared input has no usable fact
	// source, or a passed fact was not supplied.
	ReasonUnresolvable = "FACT_UNRESOLVABLE"
	// ReasonBudgetExceeded is returned when the per-request dataset-fact
	// budget or row limit is exceeded.
	ReasonBudgetExceeded = "FACT_BUDGET_EXCEEDED"
	// ReasonTimeout is returned when a fetch exceeds its timeout.
	ReasonTimeout = "FACT_TIMEOUT"
	// ReasonDerivedCycle is returned when derived expressions form a cycle.
	ReasonDerivedCycle = "FACT_DERIVED_CYCLE"
	// ReasonDatasetNotPublished is returned when a dataset is unknown or not
	// in a queryable status.
	ReasonDatasetNotPublished = dataset.ReasonDatasetNotPublished
)

// Fault is a resolution failure carrying the reason code the decision must
// fail closed with, so the pipeline never infers one from an error string.
type Fault struct {
	Code  string
	Field string
	Err   error
}

// Error returns a message free of any fact value.
func (f *Fault) Error() string {
	if f == nil {
		return "<nil>"
	}
	message := f.Code
	if f.Field != "" {
		message += fmt.Sprintf(" field=%q", f.Field)
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

// Code returns the reason code a resolution error carries, or an empty string
// if the error did not come from this package.
func Code(err error) string {
	var fault *Fault
	if errors.As(err, &fault) {
		return fault.Code
	}
	var compileFault *dataset.Fault
	if errors.As(err, &compileFault) {
		return compileFault.Code
	}
	return ""
}

// Resolution is the outcome of resolving one set of fields.
type Resolution struct {
	// Values holds every requested field, keyed by name.
	Values map[string]any
	// Facts explains how each one was obtained, ordered by field name so two
	// runs of the same request produce the same audit record.
	Facts []Resolved
	// Notes are the non-fatal reason codes the decision should carry, such as
	// a deprecated field or dataset.
	Notes []string
}

// Resolver turns a service's declared fields into values.
//
// It is the only part of the library that performs I/O, and every fetch is
// bounded by a budget and a timeout (INV3). Exceeding either fails the whole
// decision closed: there is no partial fact set, because a missing fact
// silently becomes a different decision, and a held decision is recoverable
// where a wrong payroll figure is not.
//
// A Resolver is safe for concurrent use and is normally built once per
// service. Per-request state lives in a Session.
type Resolver struct {
	fields    *registry.Registry
	manifests map[string]dataset.Manifest
	executor  dataset.Executor
	natives   *NativeRegistry
	budget    Budget
	metrics   telemetry.Metrics
}

// NewResolver builds a resolver. The budget must be valid: an unset budget is
// a configuration error, not a default.
func NewResolver(
	fields *registry.Registry,
	manifests map[string]dataset.Manifest,
	executor dataset.Executor,
	natives *NativeRegistry,
	budget Budget,
	metrics telemetry.Metrics,
) (*Resolver, error) {
	if fields == nil {
		return nil, fmt.Errorf("fact resolver requires a field registry")
	}
	if err := budget.Validate(); err != nil {
		return nil, err
	}
	if metrics == nil {
		metrics = telemetry.Nop()
	}
	copied := make(map[string]dataset.Manifest, len(manifests))
	for name, manifest := range manifests {
		manifest = manifest.Canonical()
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("dataset manifest %q: %w", name, err)
		}
		if manifest.Dataset != name {
			return nil, fmt.Errorf("dataset manifest keyed %q declares %q", name, manifest.Dataset)
		}
		copied[name] = manifest
	}
	return &Resolver{
		fields: fields, manifests: copied, executor: executor,
		natives: natives, budget: budget, metrics: metrics,
	}, nil
}

// Budget returns the configured budget.
func (r *Resolver) Budget() Budget { return r.budget }

// Session is one request's fact-fetch state: its budget spend and its
// de-duplication cache.
//
// De-duplication spans the whole request, not one decision point. A service
// evaluating four checks for the same employee shares the facts between them,
// so a fact needed by three checks is fetched once. That is mandatory rather
// than an optimisation: the budget is a request budget, and fetching the same
// fact three times would both exhaust it and risk three different answers.
type Session struct {
	resolver *Resolver
	spend    spend
	cache    map[string]cached
	deadline time.Time
}

type cached struct {
	value       any
	rowsScanned int
}

// NewSession opens a per-request session. The caller closes nothing; the
// session is garbage once the request ends.
func (r *Resolver) NewSession() *Session {
	return &Session{
		resolver: r,
		spend:    spend{budget: r.budget},
		cache:    make(map[string]cached),
	}
}

// Resolve resolves one decision point's fields in a session of their own. Use
// NewSession when a request evaluates several decision points, so that they
// share both the budget and the fetched facts.
func (r *Resolver) Resolve(
	ctx context.Context, fields []string, have map[string]any, params Params, pins map[string]string,
) (Resolution, error) {
	return r.NewSession().Resolve(ctx, fields, have, params, pins)
}

// Resolve resolves the named fields, returning every value the winning rule
// declared it needs.
//
// have is what the caller already supplied, normally the request context. A
// field present there is taken as given and recorded with FromRequest set,
// and nothing is fetched for it.
func (s *Session) Resolve(
	ctx context.Context, fields []string, have map[string]any, params Params, pins map[string]string,
) (Resolution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// The total timeout bounds the whole stage across every decision point in
	// the request, so it is set once per session rather than per call.
	if s.deadline.IsZero() {
		s.deadline = time.Now().Add(s.resolver.budget.TotalFactTimeout)
	}
	ctx, cancel := context.WithDeadline(ctx, s.deadline)
	defer cancel()

	state := &resolution{
		session: s, params: params, pins: pins,
		values: make(map[string]any, len(fields)),
		facts:  make(map[string]Resolved, len(fields)),
		have:   have,
		active: make(map[string]struct{}, len(fields)),
	}
	for _, field := range fields {
		if _, err := state.resolve(ctx, field); err != nil {
			return Resolution{}, err
		}
	}
	return state.result(), nil
}

// resolution is one Resolve call's working state. It is not shared between
// goroutines: facts are fetched in order so that a budget breach is
// attributable and a replay is reproducible.
type resolution struct {
	session *Session
	params  Params
	pins    map[string]string
	values  map[string]any
	facts   map[string]Resolved
	notes   []string
	have    map[string]any
	// active is the derived-dependency stack, used to detect a cycle.
	active map[string]struct{}
}

func (s *resolution) result() Resolution {
	names := make([]string, 0, len(s.facts))
	for name := range s.facts {
		names = append(names, name)
	}
	sort.Strings(names)
	facts := make([]Resolved, 0, len(names))
	for _, name := range names {
		facts = append(facts, s.facts[name])
	}
	return Resolution{Values: s.values, Facts: facts, Notes: dedupe(s.notes)}
}

func (s *resolution) note(code string) { s.notes = append(s.notes, code) }

// resolve returns one field's value, fetching it if this is the first request
// for it, and recursing through derived dependencies.
func (s *resolution) resolve(ctx context.Context, field string) (any, error) {
	if value, done := s.values[field]; done {
		return value, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, s.contextFault(field, err)
	}

	definition, registered := s.session.resolver.fields.Field(field)
	if !registered {
		// An unregistered field is always fatal and is never treated as null.
		// Treating it as null is how a condition quietly stops applying.
		return nil, &Fault{Code: ReasonFieldNotRegistered, Field: field,
			Err: fmt.Errorf("field is absent from the registry")}
	}
	if definition.Status == "deprecated" {
		s.note(ReasonFieldDeprecated)
	}

	// A value the caller supplied is taken as given. It is still type-checked,
	// because a caller sending the wrong type is a bug worth failing on, but
	// nothing is fetched and no budget is spent.
	if raw, supplied := s.have[field]; supplied {
		if !definition.DataType.Accepts(raw, definition.AllowedValues) {
			return nil, &Fault{Code: ReasonFieldTypeMismatch, Field: field,
				Err: fmt.Errorf("supplied value is not a valid %s", definition.DataType)}
		}
		s.values[field] = raw
		s.facts[field] = Resolved{Field: field, Kind: definition.FactSource.Kind, FromRequest: true}
		return raw, nil
	}

	source := definition.FactSource
	if !source.Kind.Valid() {
		return nil, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("field declares no usable fact source")}
	}

	started := time.Now()
	value, resolved, err := s.fetch(ctx, field, definition, source)
	if err != nil {
		return nil, err
	}
	resolved.Field = field
	resolved.Kind = source.Kind
	resolved.FactSourceVersion = s.sourceVersion(field, definition)
	resolved.ElapsedMS = int(time.Since(started).Milliseconds())

	// The value is type-checked against the field's declared type whatever its
	// kind, so a dataset, a formula and a service function cannot disagree
	// about what a field is.
	if !definition.DataType.Accepts(value, definition.AllowedValues) {
		return nil, &Fault{Code: ReasonFieldTypeMismatch, Field: field,
			Err: fmt.Errorf("resolved value is not a valid %s", definition.DataType)}
	}

	s.session.resolver.metrics.Histogram(telemetry.MetricFactFetchDuration,
		telemetry.Label{Key: telemetry.LabelField, Value: field},
		telemetry.Label{Key: telemetry.LabelKind, Value: source.Kind.String()},
	).Observe(float64(resolved.ElapsedMS))

	s.values[field] = value
	s.facts[field] = resolved
	return value, nil
}

func (s *resolution) fetch(
	ctx context.Context, field string, definition registry.FieldDefinition, source registry.FactSource,
) (any, Resolved, error) {
	switch source.Kind {
	case registry.FactPassed:
		// A passed fact that was not passed is a caller bug, not an empty
		// result, so OnEmpty is deliberately not substituted here.
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("field is declared as passed but the request supplied no value")}
	case registry.FactDerived:
		return s.fetchDerived(ctx, field, source)
	case registry.FactNative:
		return s.fetchNative(ctx, field, source)
	case registry.FactDataset:
		return s.fetchDataset(ctx, field, definition, source)
	default:
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("unsupported fact kind %q", source.Kind)}
	}
}

func (s *resolution) contextFault(field string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Fault{Code: ReasonTimeout, Field: field,
			Err: fmt.Errorf("the request's total fact timeout elapsed: %w", err)}
	}
	return &Fault{Code: ReasonUnresolvable, Field: field, Err: err}
}

// sourceVersion returns the fact-source version in force, honouring a pin. A
// pin makes a payroll replay use the recipe that produced the original figure,
// not today's.
func (s *resolution) sourceVersion(field string, definition registry.FieldDefinition) string {
	if pinned, ok := s.pins[field]; ok && pinned != "" {
		return pinned
	}
	if definition.FactSource.Version != "" {
		return definition.FactSource.Version
	}
	return definition.FactSourceVersion
}

func dedupe(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, done := seen[value]; done {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
