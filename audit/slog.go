package audit

import (
	"context"
	"log/slog"
)

// SlogSink writes audit records through a structured logger.
//
// It exists for development and for a service that has not yet pointed its
// audit trail at a store. It is not a production audit store: a log stream is
// usually not access-controlled the way audit records need, and these records
// carry identifiers. Point it at a handler you would be willing to show an
// auditor, or use a real Sink.
type SlogSink struct {
	logger *slog.Logger
	level  slog.Level
}

// NewSlogSink builds a sink over logger. A nil logger uses the default.
func NewSlogSink(logger *slog.Logger) *SlogSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogSink{logger: logger, level: slog.LevelInfo}
}

// Write logs one line per record.
func (s *SlogSink) Write(ctx context.Context, batch []Record) error {
	for _, record := range batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.logger.LogAttrs(ctx, s.level, "rule decision",
			slog.String("decisionId", record.DecisionID),
			slog.String("tenant", record.Tenant),
			slog.String("domain", record.Domain),
			slog.String("ruleKey", record.RuleKey),
			slog.String("ruleVersion", record.RuleVersion),
			slog.String("subjectId", record.SubjectID),
			slog.String("resolvedNodeId", record.ResolvedNodeID),
			slog.Bool("failed", record.Failed),
			slog.Any("reasonCodes", record.ReasonCodes),
			slog.Time("asOf", record.AsOf),
			slog.Time("validAt", record.ValidAt),
			slog.String("runId", record.RunID),
			slog.String("correlationId", record.CorrelationID),
		)
	}
	return nil
}
