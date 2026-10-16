package telemetry

import "context"

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
