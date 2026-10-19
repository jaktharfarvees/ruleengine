package telemetry

import (
	"context"
	"errors"
)

// Propagation is the vendor-neutral request trace context supplied to a
// Tracer adapter. Traceparent is the inbound W3C traceparent value;
// CorrelationID is the application's cross-service correlation identifier.
type Propagation struct {
	Traceparent   string
	CorrelationID string
}

type propagationContextKey struct{}

// ContextWithPropagation returns a context carrying inbound trace metadata.
// A Tracer adapter reads it when starting the root evaluation span and maps
// Traceparent into its vendor's remote-parent representation.
func ContextWithPropagation(ctx context.Context, propagation Propagation) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if propagation.Traceparent == "" && propagation.CorrelationID == "" {
		return ctx
	}
	return context.WithValue(ctx, propagationContextKey{}, propagation)
}

// PropagationFromContext returns inbound trace metadata when the rule engine
// placed it on ctx.
func PropagationFromContext(ctx context.Context) (Propagation, bool) {
	if ctx == nil {
		return Propagation{}, false
	}
	propagation, found := ctx.Value(propagationContextKey{}).(Propagation)
	return propagation, found
}

// Span is one unit of traced work. Every span the library opens is closed on
// every path, including a fail-closed path, and records the reason code that
// ended it.
type Span interface {
	// End closes the span. Calling End more than once is a caller error and
	// implementations should tolerate it rather than panic.
	End()
	// SetAttr records one attribute. An attribute must not carry personal data.
	SetAttr(key string, value any)
	// RecordError marks the span as failed and records the cause.
	RecordError(err error)
}

// Tracer starts spans. Implementations must be safe for concurrent use.
type Tracer interface {
	// Start opens a span and returns a context carrying it.
	Start(ctx context.Context, name string) (context.Context, Span)
}

// RecordFailure attaches a stable, non-PII classification and marks a span as
// failed. Callers must pass a reason code or another bounded classification,
// never an uncontrolled dependency error string.
func RecordFailure(span Span, classification string) {
	if span == nil || classification == "" {
		return
	}
	span.SetAttr(AttrReasonCode, classification)
	span.RecordError(errors.New(classification))
}

// NopTracer returns a Tracer that records nothing and allocates nothing.
func NopTracer() Tracer { return nopTracer{} }

type nopTracer struct{}

func (nopTracer) Start(ctx context.Context, _ string) (context.Context, Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx, nopSpan{}
}

type nopSpan struct{}

func (nopSpan) End() {}

func (nopSpan) SetAttr(string, any) {}

func (nopSpan) RecordError(error) {}
