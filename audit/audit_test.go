package audit

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/telemetry"
)

// recordingSink collects batches so a test can assert what was written.
type recordingSink struct {
	mu      sync.Mutex
	batches [][]Record
	err     error
	block   chan struct{}
}

func (s *recordingSink) Write(ctx context.Context, batch []Record) error {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.batches = append(s.batches, append([]Record(nil), batch...))
	return nil
}

func (s *recordingSink) written() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []Record
	for _, batch := range s.batches {
		all = append(all, batch...)
	}
	return all
}

func TestAsyncWritesEveryRecord(t *testing.T) {
	sink := &recordingSink{}
	logger, err := NewAsync(sink, 32, telemetry.Nop(), WithBatchSize(4), WithFlushInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10; index++ {
		logger.Log(Record{DecisionID: string(rune('a' + index))})
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(sink.written()); got != 10 {
		t.Fatalf("wrote %d records, want 10", got)
	}
	if logger.Dropped() != 0 {
		t.Fatalf("dropped %d records with room to spare", logger.Dropped())
	}
	// A record carries when it was written even if the caller did not say.
	for _, record := range sink.written() {
		if record.RecordedAt.IsZero() {
			t.Fatal("a record was written with no RecordedAt")
		}
	}
}

// TestLogNeverBlocksADecision is why this type exists. A decision has already
// been made by the time Log is called, so a full buffer has to cost the
// record, not the decision.
func TestLogNeverBlocksADecision(t *testing.T) {
	blocked := make(chan struct{})
	sink := &recordingSink{block: blocked}
	logger, err := NewAsync(sink, 2, telemetry.Nop(), WithBatchSize(1), WithFlushInterval(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	// The writer is stuck in the sink, so the buffer fills and then overflows.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; index < 500; index++ {
			logger.Log(Record{DecisionID: "d"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Log blocked while the sink was stuck; a decision would have been held up")
	}
	if logger.Dropped() == 0 {
		t.Fatal("a full buffer dropped nothing, so Log must have blocked")
	}
	close(blocked)
	_ = logger.Close()
}

// TestDropsAreCountedNotSilent pins the only signal that a record was lost.
func TestDropsAreCountedNotSilent(t *testing.T) {
	counter := &countingMetrics{}
	blocked := make(chan struct{})
	logger, err := NewAsync(&recordingSink{block: blocked}, 1, counter,
		WithBatchSize(1), WithFlushInterval(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		logger.Log(Record{DecisionID: "d"})
	}
	if logger.Dropped() == 0 || counter.value(telemetry.MetricAuditDropped) == 0 {
		t.Fatalf("dropped %d records but the counter read %v",
			logger.Dropped(), counter.value(telemetry.MetricAuditDropped))
	}
	close(blocked)
	_ = logger.Close()
}

// TestSinkFailureIsCountedRatherThanRetried pins the choice not to retry: a
// retry loop behind a sink that is down would grow without bound, and the drop
// counter already tells an operator records are being lost.
func TestSinkFailureIsCountedRatherThanRetried(t *testing.T) {
	counter := &countingMetrics{}
	sink := &recordingSink{err: errors.New("audit store unavailable")}
	logger, err := NewAsync(sink, 8, counter, WithBatchSize(2), WithFlushInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		logger.Log(Record{DecisionID: "d"})
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if logger.Dropped() != 4 {
		t.Fatalf("dropped %d records, want all 4 the failing sink refused", logger.Dropped())
	}
	if counter.value(telemetry.MetricAuditDropped) != 4 {
		t.Fatalf("counter read %v, want 4", counter.value(telemetry.MetricAuditDropped))
	}
}

func TestCloseIsIdempotentAndBoundsItsWait(t *testing.T) {
	sink := &recordingSink{}
	logger, err := NewAsync(sink, 4, telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	logger.Log(Record{DecisionID: "a"})
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("a second Close returned %v", err)
	}

	// A sink that will not drain must not hold shutdown open for ever.
	blocked := make(chan struct{})
	defer close(blocked)
	stuck, err := NewAsync(&recordingSink{block: blocked}, 4, telemetry.Nop(),
		WithBatchSize(1), WithDrainTimeout(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	stuck.Log(Record{DecisionID: "b"})
	start := time.Now()
	if err := stuck.Close(); err == nil {
		t.Fatal("Close hid a sink that never drained")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close waited %s for a stuck sink", elapsed)
	}
}

func TestAsyncConstructionIsChecked(t *testing.T) {
	if _, err := NewAsync(nil, 4, telemetry.Nop()); err == nil {
		t.Fatal("a logger was built with no sink")
	}
	for _, size := range []int{0, -1} {
		if _, err := NewAsync(&recordingSink{}, size, telemetry.Nop()); err == nil {
			t.Fatalf("a logger was built with buffer size %d", size)
		}
	}
	// A nil metrics sink is substituted, and a batch larger than the buffer is
	// capped rather than deadlocking.
	logger, err := NewAsync(&recordingSink{}, 2, nil, WithBatchSize(100),
		WithFlushInterval(0), WithDrainTimeout(0))
	if err != nil {
		t.Fatal(err)
	}
	logger.Log(Record{DecisionID: "a"})
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	// A nil logger is inert rather than a panic, so a consumer that failed to
	// construct one still gets decisions.
	var absent *Async
	absent.Log(Record{})
	if absent.Dropped() != 0 || absent.Close() != nil {
		t.Fatal("a nil logger did not behave inertly")
	}
}

func TestNopLoggerAcceptsEverything(t *testing.T) {
	logger := Nop()
	logger.Log(Record{DecisionID: "a"})
	allocations := testing.AllocsPerRun(100, func() { logger.Log(Record{}) })
	if allocations != 0 {
		t.Fatalf("the no-op logger allocated %.1f times per record", allocations)
	}
}

func TestSlogSinkWritesOneLinePerRecord(t *testing.T) {
	var lines int
	handler := slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		lines++
		return len(p), nil
	}), nil)
	sink := NewSlogSink(slog.New(handler))
	batch := []Record{
		{DecisionID: "a", Tenant: "RWB", Failed: false},
		{DecisionID: "b", Tenant: "RWB", Failed: true, ReasonCodes: []string{"NO_RULE_RESOLVED"}},
	}
	if err := sink.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if lines != 2 {
		t.Fatalf("wrote %d lines for 2 records", lines)
	}

	// A cancelled context stops the write rather than finishing it.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sink.Write(cancelled, batch); err == nil {
		t.Fatal("a cancelled write was completed")
	}
	// A nil logger falls back to the default rather than panicking.
	if NewSlogSink(nil) == nil {
		t.Fatal("NewSlogSink(nil) returned nothing")
	}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

// countingMetrics records counter totals by name.
type countingMetrics struct {
	mu     sync.Mutex
	totals map[string]float64
}

func (m *countingMetrics) Counter(name string, _ ...telemetry.Label) telemetry.Counter {
	return &countingCounter{metrics: m, name: name}
}

func (m *countingMetrics) Histogram(string, ...telemetry.Label) telemetry.Histogram {
	return nopHistogram{}
}

func (m *countingMetrics) value(name string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totals[name]
}

type countingCounter struct {
	metrics *countingMetrics
	name    string
}

func (c *countingCounter) Inc() { c.Add(1) }

func (c *countingCounter) Add(delta float64) {
	c.metrics.mu.Lock()
	defer c.metrics.mu.Unlock()
	if c.metrics.totals == nil {
		c.metrics.totals = map[string]float64{}
	}
	c.metrics.totals[c.name] += delta
}

type nopHistogram struct{}

func (nopHistogram) Observe(float64) {}
