package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNopMetricsAcceptEveryCall(t *testing.T) {
	metrics := Nop()
	counter := metrics.Counter(MetricEvalTotal, Label{Key: LabelDomain, Value: "leave"})
	counter.Inc()
	counter.Add(2)
	histogram := metrics.Histogram(MetricFactFetchDuration, Label{Key: LabelField, Value: "serviceMonths"})
	histogram.Observe(1.5)
	if metrics.Counter("") == nil || metrics.Histogram("") == nil {
		t.Fatal("no-op metrics returned a nil instrument")
	}
}

// TestNopMetricsDoNotAllocate pins the promise that a consumer supplying no
// metrics pays nothing: the library looks an instrument up per decision, so an
// allocation here would be an allocation on every decision.
func TestNopMetricsDoNotAllocate(t *testing.T) {
	metrics := Nop()
	allocations := testing.AllocsPerRun(1000, func() {
		metrics.Counter(MetricEvalTotal, Label{Key: LabelDomain, Value: "leave"}).Inc()
		metrics.Histogram(MetricFactRowsScanned, Label{Key: LabelField, Value: "otHours"}).Observe(3)
	})
	if allocations != 0 {
		t.Fatalf("no-op metrics allocated %.2f times per call, want zero", allocations)
	}
}

func TestNopTracerReturnsUsableContextAndSpan(t *testing.T) {
	tracer := NopTracer()
	ctx, span := tracer.Start(context.Background(), SpanFacts)
	if ctx == nil || span == nil {
		t.Fatal("tracer returned a nil context or span")
	}
	span.SetAttr(LabelKind, "dataset")
	span.RecordError(errors.New("boom"))
	span.End()
	// A nil context must not panic: a caller deep in a pipeline may not have
	// one, and losing a trace is never worth losing a decision.
	nilCtx, nilSpan := tracer.Start(nil, SpanEvaluate) //nolint:staticcheck // Deliberately checking the nil-context path.
	if nilCtx == nil || nilSpan == nil {
		t.Fatal("tracer did not substitute a context")
	}
	nilSpan.End()
}

func TestNopTracerDoesNotAllocate(t *testing.T) {
	tracer := NopTracer()
	ctx := context.Background()
	allocations := testing.AllocsPerRun(1000, func() {
		_, span := tracer.Start(ctx, SpanResolve)
		span.End()
	})
	if allocations != 0 {
		t.Fatalf("no-op tracer allocated %.2f times per call, want zero", allocations)
	}
}

func TestNamesAreDistinctAndWellFormed(t *testing.T) {
	names := []string{
		MetricEvalDuration, MetricEvalTotal, MetricEvalErrors, MetricCandidatesConsidered,
		MetricPrecedenceTie, MetricCrossTenantDropped, MetricFactFetchDuration,
		MetricFactRowsScanned, MetricFactBudgetExceeded, MetricFactCacheHits,
		MetricCacheEntries, MetricCacheStaleSeconds, MetricHierarchyStaleSeconds,
		MetricAuditDropped,
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, " .-") || strings.ToLower(name) != name {
			t.Errorf("metric name %q is not lower_snake_case", name)
		}
		if _, duplicate := seen[name]; duplicate {
			t.Errorf("metric name %q is defined twice", name)
		}
		seen[name] = struct{}{}
	}
	if got := SpanFactKind("dataset"); got != "fact.dataset" {
		t.Fatalf("SpanFactKind(\"dataset\") = %q, want fact.dataset", got)
	}
}
