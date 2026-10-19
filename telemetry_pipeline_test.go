package ruleengine_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/telemetry"
)

const testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func TestEvaluationEmitsCompleteMetricsAndPropagatesTraceContext(t *testing.T) {
	tracer := newTraceRecorder()
	metrics := newMetricRecorder()
	instance, _ := leaveEngine(t, ruleengine.WithTracer(tracer), ruleengine.WithMetrics(metrics))
	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	request.Traceparent = testTraceparent

	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Traceparent != testTraceparent || decision.CorrelationID != request.CorrelationID {
		t.Fatalf("decision propagation = %q, %q", decision.Traceparent, decision.CorrelationID)
	}

	root := tracer.one(t, telemetry.SpanEvaluate)
	if root.parent != "" {
		t.Fatalf("root parent = %q", root.parent)
	}
	if root.propagation.Traceparent != testTraceparent || root.propagation.CorrelationID != request.CorrelationID {
		t.Fatalf("root propagation = %+v", root.propagation)
	}
	for _, name := range []string{
		telemetry.SpanResolve, telemetry.SpanVersion, telemetry.SpanFacts,
		telemetry.SpanValidate, telemetry.SpanEngine, telemetry.SpanMap,
	} {
		span := tracer.one(t, name)
		if span.parent != root.id {
			t.Errorf("span %q parent = %q, want root %q", name, span.parent, root.id)
		}
		if !span.ended {
			t.Errorf("span %q was not ended", name)
		}
	}
	factSpan := tracer.one(t, telemetry.SpanFactKind("derived"))
	if factsSpan := tracer.one(t, telemetry.SpanFacts); factSpan.parent != factsSpan.id {
		t.Errorf("fact span parent = %q, want facts span %q", factSpan.parent, factsSpan.id)
	}
	if factSpan.attributes[telemetry.AttrField] != "serviceMonths" ||
		factSpan.attributes[telemetry.AttrFactKind] != "derived" {
		t.Errorf("fact span attributes = %#v", factSpan.attributes)
	}
	if !root.ended {
		t.Fatal("root evaluation span was not ended")
	}

	durations := metrics.observations(telemetry.MetricEvalDuration)
	if len(durations) != 1 || durations[0].value < 0 {
		t.Fatalf("evaluation duration observations = %#v", durations)
	}
	assertMetricLabels(t, durations[0].labels, map[string]string{
		telemetry.LabelDomain: "leave", telemetry.LabelRuleSet: "eligibility",
		telemetry.LabelName: "min-service", telemetry.LabelOutcome: "ok",
	})
	if got := metrics.observations(telemetry.MetricEvalTotal); len(got) != 1 || got[0].value != 1 {
		t.Fatalf("evaluation total observations = %#v", got)
	}
}

func TestFailClosedEvaluationClassifiesEveryEndingSpanAndErrorMetric(t *testing.T) {
	tracer := newTraceRecorder()
	metrics := newMetricRecorder()
	instance, _ := leaveEngine(t, ruleengine.WithTracer(tracer), ruleengine.WithMetrics(metrics))
	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1", nil)
	request.Tenant.ID = "wrong-tenant"
	request.Traceparent = testTraceparent

	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Failed {
		t.Fatal("cross-tenant request did not fail closed")
	}
	for _, name := range []string{telemetry.SpanEvaluate, telemetry.SpanResolve, telemetry.SpanMap} {
		span := tracer.one(t, name)
		if span.failure != ruleengine.ReasonSchemaInvalid.String() {
			t.Errorf("span %q failure = %q", name, span.failure)
		}
		if span.attributes[telemetry.AttrReasonCode] != ruleengine.ReasonSchemaInvalid.String() {
			t.Errorf("span %q reason attribute = %#v", name, span.attributes)
		}
	}
	errors := metrics.observations(telemetry.MetricEvalErrors)
	if len(errors) != 1 || errors[0].value != 1 {
		t.Fatalf("evaluation errors = %#v", errors)
	}
	assertMetricLabels(t, errors[0].labels, map[string]string{
		telemetry.LabelReason: ruleengine.ReasonSchemaInvalid.String(),
	})
}

type traceContextKey struct{}

type traceObservation struct {
	id          string
	name        string
	parent      string
	propagation telemetry.Propagation
	attributes  map[string]any
	failure     string
	ended       bool
}

type traceRecorder struct {
	mu    sync.Mutex
	spans []*traceObservation
}

func newTraceRecorder() *traceRecorder { return &traceRecorder{} }

func (r *traceRecorder) Start(ctx context.Context, name string) (context.Context, telemetry.Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	parent, _ := ctx.Value(traceContextKey{}).(string)
	propagation, _ := telemetry.PropagationFromContext(ctx)
	span := &traceObservation{
		id: fmt.Sprintf("span-%d", len(r.spans)+1), name: name, parent: parent,
		propagation: propagation, attributes: make(map[string]any),
	}
	r.spans = append(r.spans, span)
	return context.WithValue(ctx, traceContextKey{}, span.id), recordedTraceSpan{recorder: r, observation: span}
}

func (r *traceRecorder) one(t *testing.T, name string) traceObservation {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var matches []traceObservation
	for _, span := range r.spans {
		if span.name == name {
			matches = append(matches, *span)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("spans named %q = %d; all spans: %v", name, len(matches), r.names())
	}
	return matches[0]
}

func (r *traceRecorder) names() []string {
	names := make([]string, 0, len(r.spans))
	for _, span := range r.spans {
		names = append(names, span.name)
	}
	return names
}

type recordedTraceSpan struct {
	recorder    *traceRecorder
	observation *traceObservation
}

func (s recordedTraceSpan) End() {
	s.recorder.mu.Lock()
	s.observation.ended = true
	s.recorder.mu.Unlock()
}

func (s recordedTraceSpan) SetAttr(key string, value any) {
	s.recorder.mu.Lock()
	s.observation.attributes[key] = value
	s.recorder.mu.Unlock()
}

func (s recordedTraceSpan) RecordError(err error) {
	s.recorder.mu.Lock()
	if err != nil {
		s.observation.failure = err.Error()
	}
	s.recorder.mu.Unlock()
}

type metricObservation struct {
	name   string
	labels []telemetry.Label
	value  float64
}

type metricRecorder struct {
	mu           sync.Mutex
	measurements []metricObservation
}

func newMetricRecorder() *metricRecorder { return &metricRecorder{} }

func (r *metricRecorder) Counter(name string, labels ...telemetry.Label) telemetry.Counter {
	return recordedMetric{recorder: r, name: name, labels: slices.Clone(labels)}
}

func (r *metricRecorder) Histogram(name string, labels ...telemetry.Label) telemetry.Histogram {
	return recordedMetric{recorder: r, name: name, labels: slices.Clone(labels)}
}

func (r *metricRecorder) observations(name string) []metricObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []metricObservation
	for _, observation := range r.measurements {
		if observation.name == name {
			result = append(result, observation)
		}
	}
	return result
}

type recordedMetric struct {
	recorder *metricRecorder
	name     string
	labels   []telemetry.Label
}

func (m recordedMetric) Inc()              { m.Add(1) }
func (m recordedMetric) Add(value float64) { m.Observe(value) }
func (m recordedMetric) Observe(value float64) {
	m.recorder.mu.Lock()
	m.recorder.measurements = append(m.recorder.measurements, metricObservation{
		name: m.name, labels: slices.Clone(m.labels), value: value,
	})
	m.recorder.mu.Unlock()
}

func assertMetricLabels(t *testing.T, labels []telemetry.Label, want map[string]string) {
	t.Helper()
	got := make(map[string]string, len(labels))
	for _, label := range labels {
		got[label.Key] = label.Value
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("metric label %q = %q, want %q; all labels %#v", key, got[key], value, got)
		}
	}
}
