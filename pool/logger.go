package pool

import (
	"context"

	"github.com/arrowhr/ruleengine/audit"
)

// NoCloseLogger wraps a decision logger so that closing an engine does not
// close it. Engine.Close closes its decision logger, so a logger shared by
// every tenant's engine must be wrapped, or evicting one tenant would shut the
// logger down for all of them.
//
// The service owns a shared logger: it closes it once, after Pool.Close has
// returned. If the wrapped logger is an audit.DurableDecisionLogger the result
// is one too, so RequireDurableAudit keeps working.
func NoCloseLogger(logger audit.DecisionLogger) audit.DecisionLogger {
	if logger == nil {
		return audit.Nop()
	}
	if durable, ok := logger.(audit.DurableDecisionLogger); ok {
		return noCloseDurable{noCloseLogger: noCloseLogger{logger: durable}, durable: durable}
	}
	return noCloseLogger{logger: logger}
}

type noCloseLogger struct{ logger audit.DecisionLogger }

func (n noCloseLogger) Log(record audit.Record) { n.logger.Log(record) }

type noCloseDurable struct {
	noCloseLogger
	durable audit.DurableDecisionLogger
}

func (n noCloseDurable) LogDurably(ctx context.Context, record audit.Record) error {
	return n.durable.LogDurably(ctx, record)
}
