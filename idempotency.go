package ruleengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/internal/deepcopy"
	"github.com/arrowhr/ruleengine/rule"
)

const minimumIdempotencyRetention = 24 * time.Hour

// IdempotencyRequest identifies one money-moving operation. Tenant and Domain
// scope a client UUID, Fingerprint prevents reuse for different inputs, and
// ExpiresAt lets the backing store enforce its retention policy.
type IdempotencyRequest struct {
	Tenant      string
	Domain      string
	Key         string
	Fingerprint string
	RequestedAt time.Time
	ExpiresAt   time.Time
}

// IdempotencyResult is the immutable outcome retained for a money-moving
// operation. A single Evaluate call contains one decision; EvaluateAll can
// contain several and preserves which points were optional. Store adapters
// must preserve the complete Go values, including Decision fields excluded
// from its public JSON representation and concrete values inside Result.
type IdempotencyResult struct {
	Decisions []Decision `json:"decisions"`
	Optional  []bool     `json:"optional,omitempty"`
}

// IdempotencyStore atomically executes and retains the first operation for a
// scoped key. Implementations must coalesce concurrent identical requests,
// return ErrIdempotencyConflict for a different fingerprint, retain only a
// successful callback result, and treat an expired entry as absent.
//
// Production implementations must use storage shared by every service replica.
type IdempotencyStore interface {
	Do(
		ctx context.Context,
		request IdempotencyRequest,
		evaluate func(context.Context) (IdempotencyResult, error),
	) (IdempotencyResult, error)
}

// MemoryIdempotencyStore is a concurrency-safe process-local implementation.
// It is suitable for tests and single-process tools. Replicated production
// services must provide a shared durable implementation instead.
type MemoryIdempotencyStore struct {
	mu      sync.Mutex
	entries map[idempotencyScope]*memoryIdempotencyEntry
}

type idempotencyScope struct {
	tenant string
	domain string
	key    string
}

type memoryIdempotencyEntry struct {
	fingerprint string
	expiresAt   time.Time
	done        chan struct{}
	complete    bool
	result      IdempotencyResult
}

// NewMemoryIdempotencyStore constructs an empty process-local store.
func NewMemoryIdempotencyStore() *MemoryIdempotencyStore {
	return &MemoryIdempotencyStore{
		entries: make(map[idempotencyScope]*memoryIdempotencyEntry),
	}
}

// Do implements IdempotencyStore.
func (s *MemoryIdempotencyStore) Do(
	ctx context.Context,
	request IdempotencyRequest,
	evaluate func(context.Context) (IdempotencyResult, error),
) (result IdempotencyResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || request.Tenant == "" || request.Domain == "" || request.Key == "" ||
		request.Fingerprint == "" || request.RequestedAt.IsZero() || request.ExpiresAt.IsZero() ||
		!request.ExpiresAt.After(request.RequestedAt) || evaluate == nil {
		return IdempotencyResult{}, fmt.Errorf("%w: incomplete store request", ErrIdempotency)
	}
	scope := idempotencyScope{tenant: request.Tenant, domain: request.Domain, key: request.Key}
	for {
		if err := ctx.Err(); err != nil {
			return IdempotencyResult{}, err
		}
		s.mu.Lock()
		entry, found := s.entries[scope]
		if found && entry.complete && !entry.expiresAt.After(request.RequestedAt) {
			delete(s.entries, scope)
			entry, found = nil, false
		}
		if found {
			if entry.fingerprint != request.Fingerprint {
				s.mu.Unlock()
				return IdempotencyResult{}, ErrIdempotencyConflict
			}
			if entry.complete {
				result := cloneIdempotencyResult(entry.result)
				s.mu.Unlock()
				return result, nil
			}
			done := entry.done
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return IdempotencyResult{}, ctx.Err()
			}
		}

		entry = &memoryIdempotencyEntry{
			fingerprint: request.Fingerprint,
			expiresAt:   request.ExpiresAt,
			done:        make(chan struct{}),
		}
		s.entries[scope] = entry
		s.mu.Unlock()

		defer func() {
			if recovered := recover(); recovered != nil {
				s.failReservation(scope, entry)
				panic(recovered)
			}
		}()
		result, err = evaluate(ctx)
		if err != nil {
			s.failReservation(scope, entry)
			return IdempotencyResult{}, err
		}
		result = cloneIdempotencyResult(result)
		s.mu.Lock()
		entry.result = result
		entry.complete = true
		close(entry.done)
		s.mu.Unlock()
		return cloneIdempotencyResult(result), nil
	}
}

func (s *MemoryIdempotencyStore) failReservation(scope idempotencyScope, entry *memoryIdempotencyEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.entries[scope]; current == entry {
		delete(s.entries, scope)
		close(entry.done)
	}
}

func (e *Engine) idempotencyRetention() time.Duration {
	if e.config.IdempotencyRetention > 0 {
		return e.config.IdempotencyRetention
	}
	return minimumIdempotencyRetention
}

func (e *Engine) idempotent(
	ctx context.Context,
	request EvalRequest,
	points []Point,
	evaluate func(context.Context) (IdempotencyResult, error),
) (IdempotencyResult, error) {
	fingerprint, err := idempotencyFingerprint(request, points)
	if err != nil {
		return IdempotencyResult{}, &sanitizedOperationalError{
			message: "idempotency request fingerprint failed", cause: errors.Join(ErrIdempotency, err),
		}
	}
	requestedAt := e.options.clock.Now()
	stored, err := e.options.idempotencyStore.Do(ctx, IdempotencyRequest{
		Tenant: request.Tenant.ID, Domain: e.config.Domain,
		Key: request.IdempotencyKey, Fingerprint: fingerprint,
		RequestedAt: requestedAt, ExpiresAt: requestedAt.Add(e.idempotencyRetention()),
	}, func(callCtx context.Context) (IdempotencyResult, error) {
		result, evalErr := evaluate(callCtx)
		return cloneIdempotencyResult(result), evalErr
	})
	if err != nil {
		message := "idempotency operation failed"
		if errors.Is(err, ErrIdempotencyConflict) {
			message = "idempotency key was already used for a different request"
		}
		return IdempotencyResult{}, &sanitizedOperationalError{message: message, cause: err}
	}
	return cloneIdempotencyResult(stored), nil
}

func idempotencyFingerprint(request EvalRequest, points []Point) (string, error) {
	request.IdempotencyKey = ""
	request.CorrelationID = ""
	request.Traceparent = ""
	// The evaluated keys live in points for both Evaluate and EvaluateAll.
	// Omitting the envelope copy prevents an irrelevant routing-key difference
	// from changing the identity of an otherwise identical batch operation.
	request.Routing.RuleKey = ""
	payload := struct {
		Request EvalRequest `json:"request"`
		Points  []Point     `json:"points"`
	}{Request: request, Points: points}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			if !((character >= '0' && character <= '9') ||
				(character >= 'a' && character <= 'f') ||
				(character >= 'A' && character <= 'F')) {
				return false
			}
		}
	}
	// RFC 9562 UUIDs use versions 1-8 and the RFC variant (binary 10xx).
	version := value[14]
	if version < '1' || version > '8' {
		return false
	}
	variant := value[19]
	return variant == '8' || variant == '9' ||
		variant == 'a' || variant == 'b' ||
		variant == 'A' || variant == 'B'
}

func cloneIdempotencyResult(source IdempotencyResult) IdempotencyResult {
	result := IdempotencyResult{
		Decisions: make([]Decision, len(source.Decisions)),
		Optional:  append([]bool(nil), source.Optional...),
	}
	for index, decision := range source.Decisions {
		result.Decisions[index] = cloneDecision(decision)
	}
	return result
}

func cloneDecision(source Decision) Decision {
	result := source
	result.Result = deepcopy.Map(source.Result)
	result.ReasonCodes = append([]ReasonCode(nil), source.ReasonCodes...)
	if source.ResolvedAt != nil {
		resolved := *source.ResolvedAt
		result.ResolvedAt = &resolved
	}
	result.AnchorPath = append([]string(nil), source.AnchorPath...)
	result.SubjectPath = append([]hierarchy.PathEntry(nil), source.SubjectPath...)
	result.Evaluated = append([]Evaluated(nil), source.Evaluated...)
	result.FactsResolved = append([]FactResolved(nil), source.FactsResolved...)
	if source.ResolvedAttributes != nil {
		result.ResolvedAttributes = make(map[string]ResolvedAttribute, len(source.ResolvedAttributes))
		for name, attribute := range source.ResolvedAttributes {
			result.ResolvedAttributes[name] = attribute
		}
	}
	result.Obligations = append([]string(nil), source.Obligations...)
	if source.Tie != nil {
		tie := *source.Tie
		tie.Candidates = append([]rule.RuleKeyVersion(nil), source.Tie.Candidates...)
		result.Tie = &tie
	}
	result.Trace = cloneTrace(source.Trace)
	return result
}

func cloneTrace(source *engine.Trace) *engine.Trace {
	if source == nil {
		return nil
	}
	result := &engine.Trace{Rows: make([]engine.RowTrace, len(source.Rows))}
	for rowIndex, sourceRow := range source.Rows {
		row := sourceRow
		row.Conditions = append([]engine.ConditionTrace(nil), sourceRow.Conditions...)
		for conditionIndex := range row.Conditions {
			row.Conditions[conditionIndex].Actual = deepcopy.Value(row.Conditions[conditionIndex].Actual)
		}
		result.Rows[rowIndex] = row
	}
	return result
}
