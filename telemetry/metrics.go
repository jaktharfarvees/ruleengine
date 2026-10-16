package telemetry

// Label is one metric dimension. No label may carry an employee identifier, a
// node identifier or any other personal data: that is both a privacy rule
// (INV8) and a cardinality rule, because a per-employee label multiplies every
// series by the headcount of every tenant.
type Label struct {
	Key   string
	Value string
}

// Counter is a monotonically increasing measurement.
type Counter interface {
	// Inc adds one.
	Inc()
	// Add adds delta, which must not be negative.
	Add(delta float64)
}

// Histogram is a distribution of observed values.
type Histogram interface {
	// Observe records one value.
	Observe(value float64)
}

// Metrics creates the instruments the library reports through. Implementations
// must be safe for concurrent use and must tolerate being called on a hot path:
// the library looks an instrument up per decision, not once at startup.
type Metrics interface {
	// Counter returns the counter for a name and label set.
	Counter(name string, labels ...Label) Counter
	// Histogram returns the histogram for a name and label set.
	Histogram(name string, labels ...Label) Histogram
}

// Nop returns a Metrics that discards every measurement. It is the default, so
// a consumer that supplies no metrics pays nothing beyond an interface call.
func Nop() Metrics { return nopMetrics{} }

type nopMetrics struct{}

func (nopMetrics) Counter(string, ...Label) Counter { return nopInstrument{} }

func (nopMetrics) Histogram(string, ...Label) Histogram { return nopInstrument{} }

// nopInstrument is an empty struct, so returning it as an interface value uses
// a package-level zero-size allocation rather than a new one per call.
type nopInstrument struct{}

func (nopInstrument) Inc() {}

func (nopInstrument) Add(float64) {}

func (nopInstrument) Observe(float64) {}
