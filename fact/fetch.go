package fact

import (
	"context"
	"errors"
	"fmt"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/telemetry"
)

// fetchNative calls a function the owning service registered.
func (s *resolution) fetchNative(ctx context.Context, field string, source registry.FactSource) (any, Resolved, error) {
	source = source.Canonical()
	if source.NativeRef == "" {
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("field is declared as native but names no function")}
	}
	function, registered := s.session.resolver.natives.lookup(source.NativeRef)
	if !registered {
		// Naming the owning service's omission explicitly, because this is the
		// failure a service team hits on their first deployment and the error
		// should tell them what to do about it.
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("the owning service does not register native fact %q", source.NativeRef)}
	}

	// The function gets the per-fact timeout and Params, never the request
	// envelope: it must not be able to reach for data the field never declared.
	callCtx, cancel := context.WithTimeout(ctx, s.session.resolver.budget.PerFactTimeout)
	defer cancel()
	value, err := function(callCtx, s.params)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return nil, Resolved{}, s.timeoutFault(field, err)
		}
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("native fact %q failed: %w", source.NativeRef, err)}
	}
	if err := callCtx.Err(); err != nil && errors.Is(err, context.DeadlineExceeded) {
		// A function that returns a value after its deadline has produced a
		// value nobody can trust was computed from the right data.
		return nil, Resolved{}, s.timeoutFault(field, err)
	}
	if value == nil {
		if source.OnEmpty == nil {
			return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
				Err: fmt.Errorf("native fact %q returned no value and the field declares no onEmpty", source.NativeRef)}
		}
		value = source.OnEmpty
	}
	return value, Resolved{}, nil
}

// fetchDataset compiles the declaration into a query and runs it through the
// service's executor.
//
// Compilation applies every guardrail, so by the time the executor is called
// the dataset is published, the columns are permitted, the window is capped,
// the aggregate is offered, the values are typed and a tenant predicate is
// present. See docs/adr/0003-library-compiles-service-executes.md.
func (s *resolution) fetchDataset(
	ctx context.Context, field string, definition registry.FieldDefinition, source registry.FactSource,
) (any, Resolved, error) {
	source = source.Canonical()
	if source.Dataset == "" {
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("field is declared as a dataset fact but names no dataset")}
	}
	manifest, published := s.session.resolver.manifests[source.Dataset]
	if !published {
		return nil, Resolved{}, &Fault{Code: ReasonDatasetNotPublished, Field: field,
			Err: fmt.Errorf("dataset %q is not published to this service", source.Dataset)}
	}
	if s.session.resolver.executor == nil {
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("dataset %q needs an executor and the service supplied none", source.Dataset)}
	}

	query, notes, err := dataset.Compile(source, manifest, dataset.Params{
		Tenant:     s.params.Tenant,
		EmployeeID: s.params.EmployeeID,
		NodeID:     s.params.NodeID,
		AsOf:       s.params.AsOf,
		ValidAt:    s.params.ValidAt,
		RowLimit:   s.session.resolver.budget.MaxRowsScanned,
	})
	if err != nil {
		var fault *dataset.Fault
		if errors.As(err, &fault) {
			return nil, Resolved{}, &Fault{Code: fault.Code, Field: field, Err: err}
		}
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field, Err: err}
	}
	for _, note := range notes {
		s.note(note)
	}

	resolved := Resolved{Dataset: manifest.Dataset}
	fingerprint := query.Fingerprint()

	// Per-request de-duplication. Two decision points needing the same fact
	// ask the same question, so it is asked once; the second is recorded as
	// cached so an audit record still shows that the point used it.
	if hit, found := s.session.cache[fingerprint]; found {
		s.session.resolver.metrics.Counter(telemetry.MetricFactCacheHits,
			telemetry.Label{Key: telemetry.LabelField, Value: field}).Inc()
		resolved.Cached = true
		resolved.RowsScanned = hit.rowsScanned
		value := hit.value
		if value == nil {
			value = source.OnEmpty
		}
		if value == nil {
			return nil, Resolved{}, s.emptyFault(field, manifest.Dataset)
		}
		return value, resolved, nil
	}

	if err := s.session.spend.chargeQuery(); err != nil {
		s.budgetExceeded(definition.Domain)
		return nil, Resolved{}, &Fault{Code: ReasonBudgetExceeded, Field: field, Err: err}
	}

	runCtx, cancel := context.WithTimeout(ctx, s.session.resolver.budget.PerFactTimeout)
	defer cancel()
	result, err := s.session.resolver.executor.Run(runCtx, query)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, Resolved{}, s.timeoutFault(field, err)
		}
		return nil, Resolved{}, &Fault{Code: ReasonUnresolvable, Field: field,
			Err: fmt.Errorf("executor failed for dataset %q: %w", manifest.Dataset, err)}
	}
	if err := runCtx.Err(); err != nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, Resolved{}, s.timeoutFault(field, err)
	}

	resolved.RowsScanned = result.RowsScanned
	s.session.resolver.metrics.Histogram(telemetry.MetricFactRowsScanned,
		telemetry.Label{Key: telemetry.LabelField, Value: field}).Observe(float64(result.RowsScanned))

	// The row limit is both compiled into the query and checked here. An
	// executor reporting more rows than it was allowed is misbehaving, and the
	// decision fails closed rather than trusting the value it returned with it.
	if err := s.session.spend.chargeRows(field, result.RowsScanned); err != nil {
		s.budgetExceeded(definition.Domain)
		return nil, Resolved{}, &Fault{Code: ReasonBudgetExceeded, Field: field, Err: err}
	}

	s.session.cache[fingerprint] = cached{value: result.Value, rowsScanned: result.RowsScanned}

	value := result.Value
	if value == nil {
		// No row matched. The field's declared onEmpty is the author's answer
		// to that question; guessing a zero would be inventing a fact.
		value = source.OnEmpty
	}
	if value == nil {
		return nil, Resolved{}, s.emptyFault(field, manifest.Dataset)
	}
	return value, resolved, nil
}

func (s *resolution) emptyFault(field, datasetName string) error {
	return &Fault{Code: ReasonUnresolvable, Field: field,
		Err: fmt.Errorf("dataset %q matched no rows and the field declares no onEmpty", datasetName)}
}

func (s *resolution) timeoutFault(field string, cause error) error {
	return &Fault{Code: ReasonTimeout, Field: field,
		Err: fmt.Errorf("fetch exceeded the %s per-fact timeout: %w",
			s.session.resolver.budget.PerFactTimeout, cause)}
}

func (s *resolution) budgetExceeded(domain string) {
	s.session.resolver.metrics.Counter(telemetry.MetricFactBudgetExceeded,
		telemetry.Label{Key: telemetry.LabelDomain, Value: domain}).Inc()
}
