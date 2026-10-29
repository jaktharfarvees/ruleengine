package audit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arrowhr/ruleengine/telemetry"
)

// Async writes audit records on its own goroutine.
//
// It is deliberately lossy under pressure. A decision has already been made by
// the time Log is called, so blocking the caller to keep a record would trade a
// correct outcome for a complete audit trail — the wrong way round. Instead a
// full buffer drops the record and increments a counter, which is the only
// signal that anything was lost, so that counter is worth an alert.
type Async struct {
	records chan Record
	sink    Sink
	metrics telemetry.Metrics
	dropped atomic.Int64

	batchSize    int
	flushEvery   time.Duration
	drainTimeout time.Duration

	// mu orders Log against Close: Log sends while holding the read lock and
	// Close closes the channel under the write lock, so a send can never race
	// the close.
	mu     sync.RWMutex
	closed bool

	// ctx bounds every sink write. Close cancels it when the drain deadline
	// passes, so a write started late in the drain only gets the time that is
	// left rather than a fresh drainTimeout.
	ctx    context.Context
	cancel context.CancelFunc

	closeOnce sync.Once
	stopped   chan struct{}
	closeErr  error
}

// AsyncOption configures an Async logger.
type AsyncOption func(*Async)

// WithBatchSize sets how many records are written in one Sink call.
func WithBatchSize(size int) AsyncOption {
	return func(a *Async) {
		if size > 0 {
			a.batchSize = size
		}
	}
}

// WithFlushInterval sets how long a partial batch waits before being written,
// so a quiet service still records its decisions promptly.
func WithFlushInterval(interval time.Duration) AsyncOption {
	return func(a *Async) {
		if interval > 0 {
			a.flushEvery = interval
		}
	}
}

// WithDrainTimeout bounds how long Close waits for buffered records.
func WithDrainTimeout(timeout time.Duration) AsyncOption {
	return func(a *Async) {
		if timeout > 0 {
			a.drainTimeout = timeout
		}
	}
}

// NewAsync starts a logger that batches records to sink.
//
// bufferSize is how many records may wait to be written; beyond that they are
// dropped and counted. A sink that is slower than the decision rate therefore
// costs audit completeness rather than throughput, which is the trade this
// type exists to make.
func NewAsync(sink Sink, bufferSize int, metrics telemetry.Metrics, options ...AsyncOption) (*Async, error) {
	if sink == nil {
		return nil, fmt.Errorf("audit logger requires a sink")
	}
	if bufferSize <= 0 {
		return nil, fmt.Errorf("audit buffer size must be positive, got %d", bufferSize)
	}
	if metrics == nil {
		metrics = telemetry.Nop()
	}
	logger := &Async{
		records: make(chan Record, bufferSize),
		sink:    sink, metrics: metrics,
		batchSize: 64, flushEvery: time.Second, drainTimeout: 5 * time.Second,
		stopped: make(chan struct{}),
	}
	logger.ctx, logger.cancel = context.WithCancel(context.Background())
	for _, option := range options {
		option(logger)
	}
	if logger.batchSize > bufferSize {
		logger.batchSize = bufferSize
	}
	go logger.run()
	return logger, nil
}

// Log queues a record. It never blocks: if the buffer is full, or the logger
// has been closed, the record is dropped and counted.
func (a *Async) Log(record Record) {
	if a == nil {
		return
	}
	if record.RecordedAt.IsZero() {
		// Recorded here rather than by the sink, so a record carries when the
		// decision happened even if it waits in the buffer.
		record.RecordedAt = time.Now()
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.closed {
		select {
		case a.records <- record:
			return
		default:
		}
	}
	a.drop(1)
}

func (a *Async) drop(count int) {
	a.dropped.Add(int64(count))
	a.metrics.Counter(telemetry.MetricAuditDropped).Add(float64(count))
}

// Dropped returns how many records have been lost to a full buffer.
func (a *Async) Dropped() int64 {
	if a == nil {
		return 0
	}
	return a.dropped.Load()
}

// Close stops the writer and drains what is buffered, up to the drain timeout.
// It is safe to call more than once; every call returns the first call's
// result.
//
// It returns an error if any record was lost while draining, whether the sink
// failed, the drain deadline cut a write short, or the sink never returned.
// Records logged after Close are dropped and counted, but are not part of the
// drain.
func (a *Async) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		close(a.records)
		droppedBefore := a.dropped.Load()
		a.mu.Unlock()

		timer := time.NewTimer(a.drainTimeout)
		defer timer.Stop()
		select {
		case <-a.stopped:
			if lost := a.dropped.Load() - droppedBefore; lost > 0 {
				a.closeErr = fmt.Errorf("audit logger lost %d records while draining", lost)
			}
		case <-timer.C:
			// A sink that will not drain must not hold a service's shutdown
			// open indefinitely; cancelling cuts short any write that honours
			// its context, and the records still buffered are lost.
			a.cancel()
			a.closeErr = fmt.Errorf("audit logger did not drain within %s; buffered records were lost", a.drainTimeout)
		}
	})
	return a.closeErr
}

func (a *Async) run() {
	defer close(a.stopped)
	defer a.cancel()
	ticker := time.NewTicker(a.flushEvery)
	defer ticker.Stop()
	batch := make([]Record, 0, a.batchSize)
	for {
		select {
		case record, open := <-a.records:
			if !open {
				a.write(batch)
				return
			}
			batch = append(batch, record)
			if len(batch) >= a.batchSize {
				a.write(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				a.write(batch)
				batch = batch[:0]
			}
		}
	}
}

// write hands a batch to the sink. A sink failure is counted as a drop rather
// than retried: retrying from here would grow unboundedly behind a sink that
// is down, and the counter already tells an operator records are being lost.
func (a *Async) write(batch []Record) {
	if len(batch) == 0 {
		return
	}
	if a.ctx.Err() != nil {
		// The drain deadline has passed; the sink is not worth another call.
		a.drop(len(batch))
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, a.drainTimeout)
	defer cancel()
	if err := a.sink.Write(ctx, batch); err != nil {
		a.drop(len(batch))
	}
}
