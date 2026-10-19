package manifest

import (
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

// Reader is the process-local, non-blocking lookup contract used during
// evaluation. Implementations must not perform network or database I/O. A
// service synchronizes manifests into local memory before evaluating a run.
//
// Missing data fails the decision closed as PINNED_VERSION_UNAVAILABLE; remote
// synchronization errors belong outside Evaluate and retain their own error
// handling, retry, and observability policy.
type Reader interface {
	Get(runID string) (RunManifest, bool)
}

// InMemoryStore is a concurrency-safe process-local Reader. Returned manifests are
// defensive copies, so pins cannot change after a run opens.
//
// Services populate it from run-open responses or distribution events before
// evaluation; the rule-engine hot path reads only this local copy.
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

// Get returns a defensive copy of a locally cached run manifest.
func (s *InMemoryStore) Get(runID string) (RunManifest, bool) {
	if s == nil {
		return RunManifest{}, false
	}
	s.mu.RLock()
	manifest, found := s.runs[runID]
	s.mu.RUnlock()
	return clone(manifest), found
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
		left.HierarchySnapshotID != right.HierarchySnapshotID || left.Status != right.Status ||
		!left.OpenedAt.Equal(right.OpenedAt) || !equalTime(left.ClosedAt, right.ClosedAt) {
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
