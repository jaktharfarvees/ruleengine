package audit

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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
	// Wait for the failures to be counted before closing: a failure that lands
	// during the drain is reported by Close (see
	// TestCloseReportsRecordsLostWhileDraining), and this test is about the
	// counting.
	for deadline := time.Now().Add(5 * time.Second); logger.Dropped() < 4 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
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

// entrySink signals when a write begins and then blocks. With honourCtx it
// returns when the write's context ends; without it, only when released.
type entrySink struct {
	entered    chan struct{}
	release    chan struct{}
	honourCtx  bool
	enterOnce  sync.Once
	deadlineIn chan time.Duration
}

func newEntrySink(honourCtx bool) *entrySink {
	return &entrySink{
		entered: make(chan struct{}), release: make(chan struct{}),
		honourCtx: honourCtx, deadlineIn: make(chan time.Duration, 8),
	}
}

func (s *entrySink) Write(ctx context.Context, _ []Record) error {
	if deadline, ok := ctx.Deadline(); ok {
		select {
		case s.deadlineIn <- time.Until(deadline):
		default:
		}
	}
	s.enterOnce.Do(func() { close(s.entered) })
	if s.honourCtx {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	<-s.release
	return nil
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
	if got := len(sink.written()); got != 1 {
		t.Fatalf("a clean Close wrote %d records, want 1", got)
	}

	// A sink that will not drain must not hold shutdown open for ever, and
	// Close must say records were lost. Both a sink that honours its context
	// and one that ignores it are bounded.
	for _, honourCtx := range []bool{true, false} {
		stuck := newEntrySink(honourCtx)
		logger, err := NewAsync(stuck, 4, telemetry.Nop(),
			WithBatchSize(1), WithDrainTimeout(50*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		logger.Log(Record{DecisionID: "b"})
		<-stuck.entered // the record is in the sink, so the outcome no longer depends on scheduling
		start := time.Now()
		err = logger.Close()
		if err == nil {
			t.Fatalf("honourCtx=%v: Close hid a sink that never drained", honourCtx)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("honourCtx=%v: Close waited %s for a stuck sink", honourCtx, elapsed)
		}
		if again := logger.Close(); again == nil || again.Error() != err.Error() {
			t.Fatalf("honourCtx=%v: a second Close returned %v, want %v", honourCtx, again, err)
		}
		close(stuck.release)
	}
}

// TestCloseReportsRecordsLostWhileDraining pins that a sink failure during the
// drain is an error from Close, not only a counter.
func TestCloseReportsRecordsLostWhileDraining(t *testing.T) {
	sink := &recordingSink{err: errors.New("audit store unavailable")}
	logger, err := NewAsync(sink, 8, telemetry.Nop(), WithBatchSize(8), WithFlushInterval(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		logger.Log(Record{DecisionID: "d"})
	}
	err = logger.Close()
	if err == nil || !strings.Contains(err.Error(), "3 records") {
		t.Fatalf("Close returned %v, want an error naming the 3 lost records", err)
	}
	if logger.Dropped() != 3 {
		t.Fatalf("dropped %d, want 3", logger.Dropped())
	}
}

// TestDrainWriteGetsOnlyTheRemainingDeadline pins that a write's timeout comes
// from the drain deadline, not a fresh drainTimeout.
func TestDrainWriteGetsOnlyTheRemainingDeadline(t *testing.T) {
	const drain = 200 * time.Millisecond
	sink := newEntrySink(true)
	logger, err := NewAsync(sink, 4, telemetry.Nop(), WithBatchSize(1), WithDrainTimeout(drain))
	if err != nil {
		t.Fatal(err)
	}
	logger.Log(Record{DecisionID: "a"})
	<-sink.entered
	closeErr := make(chan error, 1)
	go func() { closeErr <- logger.Close() }()
	// The in-flight write is cut short at the drain deadline.
	select {
	case err := <-closeErr:
		if err == nil {
			t.Fatal("Close reported no loss though the write was cut short")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	if logger.Dropped() == 0 {
		// The cancelled write is counted by the writer goroutine; wait for it.
		<-logger.stopped
		if logger.Dropped() == 0 {
			t.Fatal("the cut-short record was not counted as dropped")
		}
	}
	close(sink.release)
}

// TestLogAfterCloseIsACountedDrop pins that logging into a closed logger is a
// drop, never a send on a closed channel. Run it with -race.
func TestLogAfterCloseIsACountedDrop(t *testing.T) {
	counter := &countingMetrics{}
	sink := &recordingSink{}
	logger, err := NewAsync(sink, 4, counter)
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logger.Log(Record{DecisionID: "late"})
	if logger.Dropped() != 1 || counter.value(telemetry.MetricAuditDropped) != 1 {
		t.Fatalf("dropped %d, counter %v; want a single counted drop",
			logger.Dropped(), counter.value(telemetry.MetricAuditDropped))
	}
	if len(sink.written()) != 0 {
		t.Fatal("a record logged after Close reached the sink")
	}

	// Concurrent Log racing Close must neither panic nor race.
	racing, err := NewAsync(&recordingSink{}, 4, telemetry.Nop())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := 0; index < 200; index++ {
				racing.Log(Record{DecisionID: "r"})
			}
		}()
	}
	_ = racing.Close()
	wg.Wait()
	racing.Log(Record{DecisionID: "after"})
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
	var output bytes.Buffer
	handler := slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		lines++
		return output.Write(p)
	}), nil)
	sink := NewSlogSink(slog.New(handler))
	batch := []Record{
		{
			DecisionID: "a", Tenant: "RWB", SubjectID: "secret-employee",
			CorrelationID: "corr-safe", InputsHash: "digest", Failed: false,
			Result:     map[string]any{"email": "person@example.com"},
			Candidates: []Candidate{{Reason: "name is Private Person"}},
			Inputs: &Inputs{
				Facts: map[string]any{"email": "audit-input@example.com"},
			},
		},
		{DecisionID: "b", Tenant: "RWB", Failed: true, ReasonCodes: []string{"NO_RULE_RESOLVED"}},
	}
	if err := sink.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if lines != 2 {
		t.Fatalf("wrote %d lines for 2 records", lines)
	}
	logOutput := output.String()
	for _, secret := range []string{"secret-employee", "person@example.com", "audit-input@example.com", "Private Person"} {
		if strings.Contains(logOutput, secret) {
			t.Fatalf("operational log leaked %q: %s", secret, logOutput)
		}
	}
	if !strings.Contains(logOutput, "decisionId=a") || !strings.Contains(logOutput, "correlationId=corr-safe") ||
		!strings.Contains(logOutput, "digest") {
		t.Fatalf("operational log omitted required safe context: %s", logOutput)
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

type memoryOutbox struct {
	records []Record
	err     error
}

func (o *memoryOutbox) Append(_ context.Context, record Record) error {
	if o.err != nil {
		return o.err
	}
	o.records = append(o.records, record)
	return nil
}

func TestOutboxLoggerIsDurableAndReportsFailure(t *testing.T) {
	if _, err := NewOutboxLogger(nil); err == nil {
		t.Fatal("nil outbox was accepted")
	}
	outbox := &memoryOutbox{}
	logger, err := NewOutboxLogger(outbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.LogDurably(context.Background(), Record{DecisionID: "d1"}); err != nil {
		t.Fatal(err)
	}
	if len(outbox.records) != 1 || outbox.records[0].RecordedAt.IsZero() {
		t.Fatalf("outbox records = %+v", outbox.records)
	}
	outbox.err = errors.New("disk full")
	if err := logger.LogDurably(context.Background(), Record{DecisionID: "d2"}); err == nil {
		t.Fatal("outbox failure was hidden")
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
