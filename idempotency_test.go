package ruleengine_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/audit"
	"github.com/arrowhr/ruleengine/rule"
)

const testIdempotencyKey = "8cf0d5ee-18e2-4d69-97b7-2e84f1ca9e91"

type countingDurableLogger struct {
	mu      sync.Mutex
	records []audit.Record
}

func (l *countingDurableLogger) Log(record audit.Record) {
	_ = l.LogDurably(context.Background(), record)
}

func (l *countingDurableLogger) LogDurably(_ context.Context, record audit.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, record)
	return nil
}

func (l *countingDurableLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.records)
}

func moneyConfig() ruleengine.Config {
	config := baseConfig()
	config.RequireDurableAudit = true
	config.RequireIdempotency = true
	return config
}

func TestMoneyMovingDecisionIsEvaluatedAndAuditedOnce(t *testing.T) {
	store := ruleengine.NewMemoryIdempotencyStore()
	logger := &countingDurableLogger{}
	instance := engineOver(t, moneyConfig(),
		ruleengine.WithIdempotencyStore(store),
		ruleengine.WithDecisionLogger(logger))
	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	request.IdempotencyKey = testIdempotencyKey

	const callers = 32
	start := make(chan struct{})
	decisions := make(chan ruleengine.Decision, callers)
	errorsFound := make(chan error, callers)
	var waiting sync.WaitGroup
	for worker := 0; worker < callers; worker++ {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			<-start
			decision, err := instance.Evaluate(context.Background(), request)
			if err != nil {
				errorsFound <- err
				return
			}
			decisions <- decision
		}()
	}
	close(start)
	waiting.Wait()
	close(decisions)
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}

	decisionID := ""
	var first ruleengine.Decision
	for decision := range decisions {
		if decisionID == "" {
			decisionID = decision.DecisionID
			first = decision
		}
		if decision.DecisionID != decisionID {
			t.Errorf("duplicate returned decision %q, want original %q", decision.DecisionID, decisionID)
		}
	}
	if decisionID == "" || logger.count() != 1 {
		t.Fatalf("decisionId = %q, audit writes = %d; want one completed operation", decisionID, logger.count())
	}

	// A caller cannot mutate the result retained under the key.
	first.Result["eligible"] = false
	retry := request
	retry.CorrelationID = "retry-correlation"
	replayed, err := instance.Evaluate(context.Background(), retry)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.DecisionID != decisionID || replayed.Result["eligible"] != true || replayed.CorrelationID != "corr-1" {
		t.Fatalf("replayed decision changed: %#v", replayed)
	}
	if logger.count() != 1 {
		t.Fatalf("retry produced %d audit records, want one", logger.count())
	}
}

func TestIdempotencyKeyReuseWithDifferentInputsIsRejected(t *testing.T) {
	logger := &countingDurableLogger{}
	instance := engineOver(t, moneyConfig(),
		ruleengine.WithIdempotencyStore(ruleengine.NewMemoryIdempotencyStore()),
		ruleengine.WithDecisionLogger(logger))
	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	request.IdempotencyKey = testIdempotencyKey
	if _, err := instance.Evaluate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Context["joinDate"] = "2025-05-01"
	if _, err := instance.Evaluate(context.Background(), request); !errors.Is(err, ruleengine.ErrIdempotencyConflict) {
		t.Fatalf("changed request error = %v, want ErrIdempotencyConflict", err)
	}
	if logger.count() != 1 {
		t.Fatalf("conflicting retry produced %d audit records, want one", logger.count())
	}
}

// TestIdempotencyFingerprintCoversRoutingAction pins that Routing.Action is
// part of the request's identity: the same key with a different action is a
// different operation, not a replay.
func TestIdempotencyFingerprintCoversRoutingAction(t *testing.T) {
	logger := &countingDurableLogger{}
	instance := engineOver(t, moneyConfig(),
		ruleengine.WithIdempotencyStore(ruleengine.NewMemoryIdempotencyStore()),
		ruleengine.WithDecisionLogger(logger))
	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	request.IdempotencyKey = testIdempotencyKey
	request.Routing.Action = "approve"
	if _, err := instance.Evaluate(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	// The identical request is a replay, so the conflict below can only come
	// from the action.
	if _, err := instance.Evaluate(context.Background(), request); err != nil {
		t.Fatalf("an identical retry failed: %v", err)
	}

	request.Routing.Action = "reject"
	if _, err := instance.Evaluate(context.Background(), request); !errors.Is(err, ruleengine.ErrIdempotencyConflict) {
		t.Fatalf("a different Routing.Action error = %v, want ErrIdempotencyConflict", err)
	}
	if logger.count() != 1 {
		t.Fatalf("produced %d audit records, want one", logger.count())
	}
}

func TestMoneyMovingDecisionRequiresConfiguredStoreAndUUID(t *testing.T) {
	config := moneyConfig()
	logger := &countingDurableLogger{}
	if _, err := ruleengine.New(config, ruleengine.WithDecisionLogger(logger)); !errors.Is(err, ruleengine.ErrConfig) {
		t.Fatalf("New without idempotency store = %v, want ErrConfig", err)
	}
	instance := engineOver(t, config,
		ruleengine.WithIdempotencyStore(ruleengine.NewMemoryIdempotencyStore()),
		ruleengine.WithDecisionLogger(logger))
	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	request.IdempotencyKey = "not-a-uuid"
	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Failed || !hasReason(decision, ruleengine.ReasonSchemaInvalid) {
		t.Fatalf("invalid idempotency key decision = %#v", decision)
	}
}

func TestEvaluateAllReplaysTheWholeStoredResult(t *testing.T) {
	logger := &countingDurableLogger{}
	instance := engineOver(t, moneyConfig(),
		ruleengine.WithIdempotencyStore(ruleengine.NewMemoryIdempotencyStore()),
		ruleengine.WithDecisionLogger(logger))
	key := rule.RuleKey("urn:arrowhr:rule:leave:eligibility:min-service:v1")
	points := []ruleengine.Point{{Key: key}, {Key: key, Optional: true}}
	request := leaveRequest(key.String(), map[string]any{"joinDate": "2026-05-01"})
	request.IdempotencyKey = "783a4c39-3a95-4c2e-85b4-a4dc614e90cd"

	first, err := instance.EvaluateAll(context.Background(), points, request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := instance.EvaluateAll(context.Background(), points, request)
	if err != nil {
		t.Fatal(err)
	}
	firstDecisions, replayedDecisions := first.All(), replayed.All()
	if len(firstDecisions) != 2 || len(replayedDecisions) != 2 ||
		firstDecisions[0].DecisionID != replayedDecisions[0].DecisionID ||
		firstDecisions[1].DecisionID != replayedDecisions[1].DecisionID {
		t.Fatalf("EvaluateAll replay differs: %#v then %#v", firstDecisions, replayedDecisions)
	}
	if logger.count() != 2 {
		t.Fatalf("EvaluateAll retry produced %d audit records, want two from the first call", logger.count())
	}
}

func TestMemoryIdempotencyStoreExpiresAndDoesNotRetainErrors(t *testing.T) {
	store := ruleengine.NewMemoryIdempotencyStore()
	requestedAt := time.Date(2026, time.October, 19, 0, 0, 0, 0, time.UTC)
	request := ruleengine.IdempotencyRequest{
		Tenant: "RWB", Domain: "payroll", Key: testIdempotencyKey,
		Fingerprint: "fingerprint", RequestedAt: requestedAt, ExpiresAt: requestedAt.Add(time.Hour),
	}
	var calls atomic.Int32
	callback := func(context.Context) (ruleengine.IdempotencyResult, error) {
		calls.Add(1)
		return ruleengine.IdempotencyResult{Decisions: []ruleengine.Decision{{DecisionID: "first"}}}, nil
	}
	if _, err := store.Do(context.Background(), request, callback); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Do(context.Background(), request, callback); err != nil {
		t.Fatal(err)
	}
	request.RequestedAt = request.ExpiresAt.Add(time.Second)
	request.ExpiresAt = request.RequestedAt.Add(time.Hour)
	if _, err := store.Do(context.Background(), request, callback); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("callback calls after replay and expiry = %d, want 2", calls.Load())
	}

	failing := ruleengine.IdempotencyRequest{
		Tenant: "RWB", Domain: "payroll", Key: "fbd7e643-d138-4206-827e-53d9d640468a",
		Fingerprint: "failure", RequestedAt: requestedAt, ExpiresAt: requestedAt.Add(time.Hour),
	}
	var attempts atomic.Int32
	fail := func(context.Context) (ruleengine.IdempotencyResult, error) {
		if attempts.Add(1) == 1 {
			return ruleengine.IdempotencyResult{}, errors.New("temporary")
		}
		return ruleengine.IdempotencyResult{Decisions: []ruleengine.Decision{{DecisionID: "retry"}}}, nil
	}
	if _, err := store.Do(context.Background(), failing, fail); err == nil {
		t.Fatal("callback failure was hidden")
	}
	if result, err := store.Do(context.Background(), failing, fail); err != nil || result.Decisions[0].DecisionID != "retry" {
		t.Fatalf("failed operation was retained: result %#v, error %v", result, err)
	}
}
