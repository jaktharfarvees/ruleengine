package pool

import (
	"fmt"
	"io"
	"log/slog"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/internal/clock"
	"github.com/arrowhr/ruleengine/telemetry"
)

const (
	defaultFailureBackoff  = 5 * time.Second
	defaultBuildTimeout    = 30 * time.Second
	defaultCloseTimeout    = 10 * time.Second
	defaultWarmConcurrency = 4
)

// config holds the pool's settings. It is unexported so that adding a setting
// later is not a breaking change.
type config struct {
	idleTTL         time.Duration
	maxTenants      int
	failureBackoff  time.Duration
	buildTimeout    time.Duration
	closeTimeout    time.Duration
	warmConcurrency int
	sweepInterval   time.Duration
	metrics         telemetry.Metrics
	logger          *slog.Logger
	clock           clock.Clock
	// newTicker supplies the janitor's ticks. Tests replace it so the janitor
	// runs without sleeping.
	newTicker func(time.Duration) (<-chan time.Time, func())
}

// Option configures a Pool.
type Option func(*config)

func defaultConfig() *config {
	return &config{
		failureBackoff:  defaultFailureBackoff,
		buildTimeout:    defaultBuildTimeout,
		closeTimeout:    defaultCloseTimeout,
		warmConcurrency: defaultWarmConcurrency,
		metrics:         telemetry.Nop(),
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:           clock.System{},
		newTicker: func(d time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(d)
			return ticker.C, ticker.Stop
		},
	}
}

func (c *config) validate() error {
	switch {
	case c.idleTTL < 0:
		return fmt.Errorf("%w: pool idle TTL must not be negative", ruleengine.ErrConfig)
	case c.maxTenants < 0:
		return fmt.Errorf("%w: pool max tenants must not be negative", ruleengine.ErrConfig)
	case c.failureBackoff < 0:
		return fmt.Errorf("%w: pool failure backoff must not be negative", ruleengine.ErrConfig)
	case c.buildTimeout < 0:
		return fmt.Errorf("%w: pool build timeout must not be negative", ruleengine.ErrConfig)
	case c.closeTimeout < 0:
		return fmt.Errorf("%w: pool close timeout must not be negative", ruleengine.ErrConfig)
	case c.warmConcurrency < 1:
		return fmt.Errorf("%w: pool warm concurrency must be at least 1", ruleengine.ErrConfig)
	case c.sweepInterval < 0:
		return fmt.Errorf("%w: pool sweep interval must not be negative", ruleengine.ErrConfig)
	}
	return nil
}

// WithIdleTTL evicts an engine that has not been requested for d. Zero, the
// default, never evicts for idleness.
func WithIdleTTL(d time.Duration) Option { return func(c *config) { c.idleTTL = d } }

// WithMaxTenants caps how many engines the pool holds. Building one more
// evicts the least recently used. Zero, the default, means no cap.
func WithMaxTenants(n int) Option { return func(c *config) { c.maxTenants = n } }

// WithFailureBackoff sets how long a failed build is remembered before the
// tenant may be built again. The default is five seconds; zero remembers
// nothing, so every request retries.
func WithFailureBackoff(d time.Duration) Option { return func(c *config) { c.failureBackoff = d } }

// WithBuildTimeout bounds one build: the factory, Reload and Ready together.
// The default is thirty seconds; zero means no limit beyond the pool closing.
func WithBuildTimeout(d time.Duration) Option { return func(c *config) { c.buildTimeout = d } }

// WithCloseTimeout bounds how long Close waits, in addition to its context.
// The default is ten seconds; zero leaves the context as the only limit.
func WithCloseTimeout(d time.Duration) Option { return func(c *config) { c.closeTimeout = d } }

// WithWarmConcurrency sets how many tenants Warm builds at once. The default
// is four.
func WithWarmConcurrency(n int) Option { return func(c *config) { c.warmConcurrency = n } }

// WithSweepInterval starts a background janitor that calls Sweep every d. The
// default, zero, starts no goroutine: idle engines are then removed lazily on
// the pool's next call, or by a service calling Sweep itself. Close stops the
// janitor.
func WithSweepInterval(d time.Duration) Option { return func(c *config) { c.sweepInterval = d } }

// WithMetrics supplies the metrics sink. The default discards measurements.
// No metric carries a tenant label.
func WithMetrics(value telemetry.Metrics) Option {
	return func(c *config) {
		if value != nil {
			c.metrics = value
		}
	}
}

// WithLogger supplies the logger for operational messages. The default
// discards them. Nothing here logs a tenant identifier.
func WithLogger(value *slog.Logger) Option {
	return func(c *config) {
		if value != nil {
			c.logger = value
		}
	}
}

// WithClock supplies lifecycle time for idle eviction and failure backoff, for
// deterministic tests.
func WithClock(value clock.Clock) Option {
	return func(c *config) {
		if value != nil {
			c.clock = value
		}
	}
}
