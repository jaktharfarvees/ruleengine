package ruleengine

import (
	"io"
	"log/slog"

	"github.com/arrowhr/ruleengine/audit"
	"github.com/arrowhr/ruleengine/contextbuilder"
	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/engine/decisiontable"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/internal/clock"
	"github.com/arrowhr/ruleengine/manifest"
	"github.com/arrowhr/ruleengine/store"
	"github.com/arrowhr/ruleengine/telemetry"
)

// options holds everything a consumer may supply. It is unexported so that
// adding a collaborator later is not a breaking change.
type options struct {
	engine         engine.Engine
	loader         store.Loader
	changesLoader  store.ChangesLoader
	executor       dataset.Executor
	natives        *fact.NativeRegistry
	contextBuilder contextbuilder.ContextBuilder
	decisionLogger audit.DecisionLogger
	manifestStore  manifest.Store
	metrics        telemetry.Metrics
	tracer         telemetry.Tracer
	logger         *slog.Logger
	clock          clock.Clock
}

// Option configures an Engine.
type Option func(*options)

// defaultOptions are the choices a consumer gets for supplying nothing: a
// working engine that decides, records nothing, measures nothing and reaches
// for no data it was not given.
func defaultOptions() *options {
	return &options{
		engine:         decisiontable.New(),
		natives:        fact.NewNativeRegistry(),
		decisionLogger: audit.Nop(),
		metrics:        telemetry.Nop(),
		tracer:         telemetry.NopTracer(),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:          clock.System{},
	}
}

// WithEngine replaces the decision engine. The default is the production
// decision-table engine; the reference engine is useful for conformance.
func WithEngine(value engine.Engine) Option {
	return func(o *options) {
		if value != nil {
			o.engine = value
		}
	}
}

// WithLoader supplies where rules come from. Without one, a service must call
// ApplyBundle itself, which is what a test or a pinned batch run does.
//
// A loader that also implements store.ChangesLoader is used for background
// reconciliation as well.
func WithLoader(value store.Loader) Option {
	return func(o *options) {
		if value == nil {
			return
		}
		o.loader = value
		if changes, ok := value.(store.ChangesLoader); ok {
			o.changesLoader = changes
		}
	}
}

// WithExecutor supplies the service's dataset executor, which is what makes
// dataset-sourced fields resolvable.
func WithExecutor(value dataset.Executor) Option {
	return func(o *options) {
		if value != nil {
			o.executor = value
		}
	}
}

// WithNativeFacts supplies the functions this service registers for
// native-sourced fields.
func WithNativeFacts(value *fact.NativeRegistry) Option {
	return func(o *options) {
		if value != nil {
			o.natives = value
		}
	}
}

// WithContextBuilder supplies a builder for facts the service computes itself.
func WithContextBuilder(value contextbuilder.ContextBuilder) Option {
	return func(o *options) {
		if value != nil {
			o.contextBuilder = value
		}
	}
}

// WithDecisionLogger supplies the audit trail. The default discards records,
// so a production service must set this.
func WithDecisionLogger(value audit.DecisionLogger) Option {
	return func(o *options) {
		if value != nil {
			o.decisionLogger = value
		}
	}
}

// WithManifestStore supplies run manifests, which batch runs need to pin
// versions. A service that runs no batches needs none.
func WithManifestStore(value manifest.Store) Option {
	return func(o *options) {
		if value != nil {
			o.manifestStore = value
		}
	}
}

// WithMetrics supplies a metrics sink. The default is a no-op that allocates
// nothing.
func WithMetrics(value telemetry.Metrics) Option {
	return func(o *options) {
		if value != nil {
			o.metrics = value
		}
	}
}

// WithTracer supplies a tracer. The default is a no-op.
func WithTracer(value telemetry.Tracer) Option {
	return func(o *options) {
		if value != nil {
			o.tracer = value
		}
	}
}

// WithLogger supplies the logger for operational messages. The default
// discards them, because a library should not write to a service's output
// uninvited. Nothing here logs a fact value or an identifier.
func WithLogger(value *slog.Logger) Option {
	return func(o *options) {
		if value != nil {
			o.logger = value
		}
	}
}

// WithClock supplies lifecycle time, for deterministic tests. Evaluation
// business time always arrives on the request and is never read from a clock.
func WithClock(value clock.Clock) Option {
	return func(o *options) {
		if value != nil {
			o.clock = value
		}
	}
}
