package audit

import (
	"context"
	"fmt"
	"time"
)

// Outbox durably appends one decision record. Implementations should commit
// before returning and deliver appended records to their final audit store at
// least once. DecisionID is the idempotency key for downstream delivery.
type Outbox interface {
	Append(ctx context.Context, record Record) error
}

// OutboxLogger adapts a durable local or transactional outbox to the decision
// logger contract.
type OutboxLogger struct{ outbox Outbox }

// NewOutboxLogger builds a durable logger. The outbox remains owned by the
// consumer because its lifecycle is normally shared with the service store.
func NewOutboxLogger(outbox Outbox) (*OutboxLogger, error) {
	if outbox == nil {
		return nil, fmt.Errorf("audit outbox is required")
	}
	return &OutboxLogger{outbox: outbox}, nil
}

// Log satisfies DecisionLogger for compatibility. Engine uses LogDurably when
// it sees this type, so failures are returned from Evaluate.
func (l *OutboxLogger) Log(record Record) {
	_ = l.LogDurably(context.Background(), record)
}

// LogDurably appends record before reporting success.
func (l *OutboxLogger) LogDurably(ctx context.Context, record Record) error {
	if l == nil || l.outbox == nil {
		return fmt.Errorf("audit outbox logger is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if record.RecordedAt.IsZero() {
		record.RecordedAt = time.Now().UTC()
	}
	if err := l.outbox.Append(ctx, record); err != nil {
		return fmt.Errorf("append audit record %q: %w", record.DecisionID, err)
	}
	return nil
}
