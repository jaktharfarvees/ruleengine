package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arrowhr/ruleengine/manifest"
	"github.com/arrowhr/ruleengine/rule"
)

var (
	// ErrCacheUnavailable means no validated snapshot has been published.
	ErrCacheUnavailable = errors.New("rule cache unavailable")
	// ErrRunManifestUnavailable means a runId did not resolve to a manifest.
	ErrRunManifestUnavailable = errors.New("run manifest unavailable")
	// ErrRunTenantMismatch means a manifest belongs to another tenant.
	ErrRunTenantMismatch = errors.New("run manifest tenant mismatch")
	// ErrPinnedVersionUnavailable means an explicit or manifest pin is absent
	// from the local snapshot. Callers may ask their Loader for it once before
	// surfacing this error, as required by the failure policy.
	ErrPinnedVersionUnavailable = errors.New("pinned version unavailable")
	// ErrEffectiveVersionUnavailable means no evaluable version covers the
	// requested business time.
	ErrEffectiveVersionUnavailable = errors.New("effective version unavailable")
)

// VersionRequest describes version selection for one already resolved rule
// family. Precedence is run manifest, explicit pin, then effective business
// time.
type VersionRequest struct {
	Tenant      string
	RuleKey     rule.RuleKey
	RunID       string
	ExplicitPin string
	EffectiveAt time.Time
}

// VersionResolver selects immutable versions from Cache and optional run
// manifests.
type VersionResolver struct {
	cache     *Cache
	manifests manifest.Store
}

// NewVersionResolver constructs a resolver. manifests may be nil when batch
// runs are not supported by the consumer.
func NewVersionResolver(cache *Cache, manifests manifest.Store) *VersionResolver {
	return &VersionResolver{cache: cache, manifests: manifests}
}

// Resolve applies manifest, explicit-pin, and effective-at precedence. It
// returns every anchored variant selected by that constraint; anchor
// resolution must see the whole slice so a baseline and employee exception at
// the same version remain distinguishable.
func (r *VersionResolver) Resolve(ctx context.Context, request VersionRequest) ([]*rule.RuleDefinition, error) {
	if r == nil || r.cache == nil || r.cache.Load() == nil {
		return nil, ErrCacheUnavailable
	}
	if request.Tenant == "" || request.RuleKey == "" {
		return nil, fmt.Errorf("version request requires tenant and rule key")
	}
	if _, err := request.RuleKey.Parse(); err != nil {
		return nil, fmt.Errorf("version request rule key: %w", err)
	}
	pin := request.ExplicitPin
	if request.RunID != "" {
		if r.manifests == nil {
			return nil, fmt.Errorf("%w: %q", ErrRunManifestUnavailable, request.RunID)
		}
		run, found, err := r.manifests.Get(ctx, request.RunID)
		if err != nil {
			// A store that cannot answer is an infrastructure fault, not a
			// missing pin: the caller should retry or alert rather than treat
			// the run as unpinned.
			return nil, fmt.Errorf("read run manifest %q: %w", request.RunID, err)
		}
		if !found {
			return nil, fmt.Errorf("%w: %q", ErrRunManifestUnavailable, request.RunID)
		}
		if run.Tenant != request.Tenant {
			return nil, fmt.Errorf("%w: run %q", ErrRunTenantMismatch, request.RunID)
		}
		var pinned bool
		pin, pinned = run.RulePin(request.RuleKey)
		if !pinned {
			return nil, fmt.Errorf("%w: run %q has no pin for %q", ErrPinnedVersionUnavailable, request.RunID, request.RuleKey)
		}
	}
	if pin != "" {
		// A pin names a version, not permission to use it. A draft or retired
		// version is never selected, even by an explicit pin or a run
		// manifest, because an unapproved rule must not be able to decide
		// someone's pay; that is a pin failure like any other.
		evaluable := make([]*rule.RuleDefinition, 0, 2)
		for _, definition := range r.cache.Version(request.Tenant, request.RuleKey, pin) {
			if definition.Status.Evaluable() {
				evaluable = append(evaluable, definition)
			}
		}
		if len(evaluable) == 0 {
			return nil, fmt.Errorf("%w: tenant=%q key=%q version=%q", ErrPinnedVersionUnavailable, request.Tenant, request.RuleKey, pin)
		}
		return evaluable, nil
	}
	if request.EffectiveAt.IsZero() {
		return nil, fmt.Errorf("version request requires effectiveAt when no pin is present")
	}
	// With no pin, each rule in the family contributes its latest version that
	// is effective at the requested time. Returning every effective version
	// instead would hand resolution two versions of one rule, which it reports
	// as an unresolved precedence tie.
	snapshot := r.cache.Load()
	if effective := snapshot.candidatesForKey(request.Tenant, request.RuleKey, request.EffectiveAt); len(effective) > 0 {
		return effective, nil
	}
	return nil, fmt.Errorf("%w: tenant=%q key=%q at=%s", ErrEffectiveVersionUnavailable, request.Tenant, request.RuleKey, request.EffectiveAt.Format(time.RFC3339))
}

// FactPin returns a run's frozen fact-source version after enforcing the same
// tenant boundary as rule resolution.
func (r *VersionResolver) FactPin(ctx context.Context, tenant, runID, field string) (string, bool, error) {
	if runID == "" || field == "" {
		return "", false, fmt.Errorf("fact pin requires runId and field")
	}
	if r == nil || r.manifests == nil {
		return "", false, fmt.Errorf("%w: %q", ErrRunManifestUnavailable, runID)
	}
	run, found, err := r.manifests.Get(ctx, runID)
	if err != nil {
		return "", false, fmt.Errorf("read run manifest %q: %w", runID, err)
	}
	if !found {
		return "", false, fmt.Errorf("%w: %q", ErrRunManifestUnavailable, runID)
	}
	if run.Tenant != tenant {
		return "", false, fmt.Errorf("%w: run %q", ErrRunTenantMismatch, runID)
	}
	version, found := run.FactPin(field)
	return version, found, nil
}
