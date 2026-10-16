package manifest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrNotFound means a run identifier has no stored manifest.
	ErrNotFound = errors.New("run manifest not found")
	// ErrAlreadyExists means a different manifest already owns a run identifier.
	ErrAlreadyExists = errors.New("run manifest already exists")
	// ErrAlreadyClosed means Close was requested with a different close time.
	ErrAlreadyClosed = errors.New("run manifest already closed")
)

// Store is the read-only contract version resolution depends on.
//
// It takes a context and returns an error because a run manifest is frozen by
// the Rule Service, so a consumer will read one over the network from inside a
// decision. Without the context such a read could not honour the decision's
// deadline; without the error, "this run has no manifest" would be
// indistinguishable from "the manifest store is unreachable", and those need
// opposite responses — the first fails the decision closed as
// PINNED_VERSION_UNAVAILABLE, the second is an infrastructure fault to retry
// or alert on.
type Store interface {
	Get(ctx context.Context, runID string) (RunManifest, bool, error)
}

// InMemoryStore is a concurrency-safe in-memory Store. Returned manifests are
// defensive copies, so pins cannot change after a run opens.
//
// It is the implementation a service uses until run manifests are served by the
// Rule Service, and the one the library's own tests run against.
type InMemoryStore struct {
	mu   sync.RWMutex
	runs map[string]RunManifest
}

// NewInMemoryStore constructs an empty in-memory store.
func NewInMemoryStore() *InMemoryStore { return &InMemoryStore{runs: make(map[string]RunManifest)} }

// Put validates and inserts a manifest. Repeating the identical insertion is
// idempotent; a different value under the same run ID is rejected.
func (s *InMemoryStore) Put(manifest RunManifest) error {
	if s == nil {
		return fmt.Errorf("manifest store is nil")
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("validate run manifest: %w", err)
	}
	manifest = clone(manifest)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == nil {
		s.runs = make(map[string]RunManifest)
	}
	if existing, found := s.runs[manifest.RunID]; found {
		if equal(existing, manifest) {
			return nil
		}
		return fmt.Errorf("%w: %q", ErrAlreadyExists, manifest.RunID)
	}
	s.runs[manifest.RunID] = manifest
	return nil
}

// Get returns a defensive copy of a run manifest. An in-memory read cannot
// fail, so the error is always nil; it exists for the remote implementations
// this interface is shaped for.
func (s *InMemoryStore) Get(ctx context.Context, runID string) (RunManifest, bool, error) {
	if s == nil {
		return RunManifest{}, false, fmt.Errorf("manifest store is nil")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return RunManifest{}, false, err
		}
	}
	s.mu.RLock()
	manifest, found := s.runs[runID]
	s.mu.RUnlock()
	return clone(manifest), found, nil
}

// Close marks a run closed without changing any pin. Repeating the same close
// is idempotent.
func (s *InMemoryStore) Close(runID string, closedAt time.Time) (RunManifest, error) {
	if s == nil {
		return RunManifest{}, fmt.Errorf("manifest store is nil")
	}
	if closedAt.IsZero() {
		return RunManifest{}, fmt.Errorf("closedAt is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest, found := s.runs[runID]
	if !found {
		return RunManifest{}, fmt.Errorf("%w: %q", ErrNotFound, runID)
	}
	if manifest.Status == StatusClosed {
		if manifest.ClosedAt != nil && manifest.ClosedAt.Equal(closedAt) {
			return clone(manifest), nil
		}
		return RunManifest{}, fmt.Errorf("%w: %q", ErrAlreadyClosed, runID)
	}
	manifest.Status = StatusClosed
	manifest.ClosedAt = &closedAt
	if err := manifest.Validate(); err != nil {
		return RunManifest{}, fmt.Errorf("close run manifest: %w", err)
	}
	s.runs[runID] = manifest
	return clone(manifest), nil
}

// Len returns the number of stored run manifests.
func (s *InMemoryStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.runs)
}

func equal(left, right RunManifest) bool {
	if left.RunID != right.RunID || left.Tenant != right.Tenant || !left.ValidAt.Equal(right.ValidAt) ||
		left.Status != right.Status || !left.OpenedAt.Equal(right.OpenedAt) || !equalTime(left.ClosedAt, right.ClosedAt) {
		return false
	}
	return equalMap(left.RulePins, right.RulePins) && equalMap(left.FactPins, right.FactPins)
}

func equalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func equalMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
