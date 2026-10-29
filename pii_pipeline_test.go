package ruleengine_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/audit"
	baseengine "github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/engine/decisiontable"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/store"
	"github.com/arrowhr/ruleengine/telemetry"
)

type piiTraceEngine struct {
	err error
}

func (piiTraceEngine) Name() string { return "pii-trace-test" }
func (piiTraceEngine) Capabilities() baseengine.Capabilities {
	return decisiontable.New().Capabilities()
}
func (e piiTraceEngine) Evaluate(context.Context, rule.RuleLogic, baseengine.Input) (baseengine.Output, error) {
	if e.err != nil {
		return baseengine.Output{}, e.err
	}
	return baseengine.Output{
		Result: map[string]any{"eligible": true, "reasonCode": "OK"},
		Trace: &baseengine.Trace{Rows: []baseengine.RowTrace{{
			RowID: "contains-pii", Matched: true,
			Conditions: []baseengine.ConditionTrace{{
				Field: "joinDate", Expr: `== "2099-12-31"`, Actual: "2099-12-31", Matched: true,
			}},
		}}},
	}, nil
}

type recordLogger struct {
	mu     sync.Mutex
	record audit.Record
}

func (l *recordLogger) Log(record audit.Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.record = record
}

func (l *recordLogger) last() audit.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.record
}

func TestPIIIsRedactedAtDecisionTraceAndAuditBoundaries(t *testing.T) {
	logger := &recordLogger{}
	config := baseConfig()
	config.TraceEnabled = true
	instance := engineOverBundle(t, config, func(bundle *store.Bundle) {
		retargetRules(bundle, piiTraceEngine{}.Name())
		for index := range bundle.Fields {
			switch bundle.Fields[index].Name {
			case "joinDate", "eligible":
				bundle.Fields[index].PII = true
			}
		}
	}, ruleengine.WithEngine(piiTraceEngine{}), ruleengine.WithDecisionLogger(logger))

	decision, err := instance.Evaluate(context.Background(), leaveRequest(
		"urn:arrowhr:rule:leave:eligibility:min-service:v1", map[string]any{"joinDate": "2099-12-31"}))
	if err != nil || decision.Failed {
		t.Fatalf("decision = %#v, error = %v", decision, err)
	}
	condition := decision.Trace.Rows[0].Conditions[0]
	if condition.Actual != "[REDACTED]" || condition.Expr != "[REDACTED]" {
		t.Fatalf("trace condition was not redacted: %#v", condition)
	}
	if decision.Result["eligible"] != true {
		t.Fatalf("caller result was changed by log redaction: %#v", decision.Result)
	}
	if got := logger.last().Result["eligible"]; got != "[REDACTED]" {
		t.Fatalf("audit result PII = %#v, want redaction marker", got)
	}
}

type recordingTracer struct {
	mu     sync.Mutex
	errors []string
}

func (t *recordingTracer) Start(ctx context.Context, _ string) (context.Context, telemetry.Span) {
	return ctx, recordingSpan{tracer: t}
}

type recordingSpan struct{ tracer *recordingTracer }

func (recordingSpan) End()                {}
func (recordingSpan) SetAttr(string, any) {}
func (s recordingSpan) RecordError(err error) {
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	s.tracer.errors = append(s.tracer.errors, err.Error())
}

// retargetRules points every rule at the stub engine a test installs, since a
// rule naming a different engine than the configured one is refused at load.
func retargetRules(bundle *store.Bundle, engineName string) {
	for index := range bundle.Rules {
		bundle.Rules[index].EngineRef.Engine = engineName
	}
}

func TestDependencyErrorTextIsNotCopiedIntoTelemetry(t *testing.T) {
	const secret = "person@example.com"
	tracer := &recordingTracer{}
	instance := engineOverBundle(t, baseConfig(),
		func(bundle *store.Bundle) { retargetRules(bundle, piiTraceEngine{}.Name()) },
		ruleengine.WithEngine(piiTraceEngine{err: errors.New(secret)}),
		ruleengine.WithTracer(tracer))

	decision, err := instance.Evaluate(context.Background(), leaveRequest(
		"urn:arrowhr:rule:leave:eligibility:min-service:v1", map[string]any{"joinDate": "2026-05-01"}))
	if err != nil || !decision.Failed {
		t.Fatalf("decision = %#v, error = %v", decision, err)
	}
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	if len(tracer.errors) == 0 {
		t.Fatal("engine failure was not recorded on a span")
	}
	for _, recorded := range tracer.errors {
		if strings.Contains(recorded, secret) {
			t.Fatalf("telemetry leaked dependency error text: %q", recorded)
		}
	}
}
