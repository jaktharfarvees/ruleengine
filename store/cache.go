package store

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/hierarchy"
	internalclock "github.com/arrowhr/ruleengine/internal/clock"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/telemetry"
)

// Clock supplies lifecycle time for cache freshness. Evaluation business time
// is always passed explicitly and never read from this clock.
type Clock interface{ Now() time.Time }

// Cache atomically publishes immutable store snapshots. Readers never acquire
// a mutex and a failed reload never replaces the last valid snapshot.
type Cache struct {
	snapshot    atomic.Pointer[Snapshot]
	lastSuccess atomic.Int64
	metrics     telemetry.Metrics
	clock       Clock
}

// Option configures a Cache.
type Option func(*Cache)

// WithMetrics reports cache size and staleness through metrics.
func WithMetrics(metrics telemetry.Metrics) Option {
	return func(cache *Cache) {
		if metrics != nil {
			cache.metrics = metrics
		}
	}
}

// WithClock supplies lifecycle time for deterministic tests.
func WithClock(value Clock) Option {
	return func(cache *Cache) {
		if value != nil {
			cache.clock = value
		}
	}
}

// NewCache constructs an empty cache.
func NewCache(options ...Option) *Cache {
	cache := &Cache{metrics: telemetry.Nop(), clock: internalclock.System{}}
	for _, option := range options {
		option(cache)
	}
	return cache
}

// Load returns the currently published snapshot.
func (c *Cache) Load() *Snapshot {
	if c == nil {
		return nil
	}
	return c.snapshot.Load()
}

// Replace validates bundle completely and atomically publishes it. The
// previous snapshot remains live if validation fails.
func (c *Cache) Replace(bundle Bundle) error {
	if c == nil {
		return fmt.Errorf("store cache is nil")
	}
	loadedAt := bundle.LoadedAt
	if loadedAt.IsZero() {
		loadedAt = c.clock.Now()
	}
	snapshot, err := newSnapshot(bundle, loadedAt)
	if err != nil {
		c.observeStaleness()
		return err
	}
	c.snapshot.Store(snapshot)
	c.lastSuccess.Store(loadedAt.UnixNano())
	c.observeEntries(snapshot.stats)
	c.metrics.Histogram(telemetry.MetricCacheStaleSeconds).Observe(0)
	c.metrics.Histogram(telemetry.MetricHierarchyStaleSeconds).Observe(0)
	return nil
}

// Reload loads a complete bundle and publishes it only when both loading and
// validation succeed.
func (c *Cache) Reload(ctx context.Context, loader Loader) error {
	if loader == nil {
		return fmt.Errorf("store loader is nil")
	}
	bundle, err := loader.LoadAll(ctx)
	if err != nil {
		c.observeStaleness()
		return fmt.Errorf("load store bundle: %w", err)
	}
	return c.Replace(bundle)
}

// Reconcile loads the coherent state after the current cursor and publishes it
// with the same all-or-nothing guarantee as Reload.
func (c *Cache) Reconcile(ctx context.Context, loader ChangesLoader) error {
	if loader == nil {
		return fmt.Errorf("store changes loader is nil")
	}
	cursor := ""
	if current := c.Load(); current != nil {
		cursor = current.stats.Cursor
	}
	bundle, err := loader.LoadChanges(ctx, cursor)
	if err != nil {
		c.observeStaleness()
		return fmt.Errorf("load store changes after %q: %w", cursor, err)
	}
	return c.Replace(bundle)
}

// Candidates delegates to the current snapshot.
func (c *Cache) Candidates(tenant, domain, ruleSet, name string, at time.Time) []*rule.RuleDefinition {
	return c.Load().Candidates(tenant, domain, ruleSet, name, at)
}

// Versions delegates to the current snapshot.
func (c *Cache) Versions(tenant string, key rule.RuleKey) []*rule.RuleDefinition {
	return c.Load().Versions(tenant, key)
}

// Version delegates to the current snapshot.
func (c *Cache) Version(tenant string, key rule.RuleKey, version string) []*rule.RuleDefinition {
	return c.Load().Version(tenant, key, version)
}

// Registry returns the current immutable registry.
func (c *Cache) Registry() *registry.Registry { return c.Load().Registry() }

// Manifest returns a current dataset manifest copy.
func (c *Cache) Manifest(name string) (dataset.Manifest, bool) { return c.Load().Manifest(name) }

// Manifests returns a copy of every current dataset manifest, keyed by
// dataset, ready to hand to a fact resolver.
func (c *Cache) Manifests() map[string]dataset.Manifest { return c.Load().Manifests() }

// Hierarchy returns the current immutable hierarchy snapshot.
func (c *Cache) Hierarchy() *hierarchy.Snapshot { return c.Load().Hierarchy() }

// Stats returns the current snapshot statistics.
func (c *Cache) Stats() Stats { return c.Load().Stats() }

// StaleSeconds returns seconds since the last successful publication. An empty
// cache is stale from the caller's perspective and returns zero until a clock
// interval can be measured.
func (c *Cache) StaleSeconds() float64 {
	if c == nil {
		return 0
	}
	last := c.lastSuccess.Load()
	if last == 0 {
		return 0
	}
	duration := c.clock.Now().Sub(time.Unix(0, last)).Seconds()
	if duration < 0 {
		return 0
	}
	return duration
}

func (c *Cache) observeStaleness() {
	if c != nil {
		staleSeconds := c.StaleSeconds()
		c.metrics.Histogram(telemetry.MetricCacheStaleSeconds).Observe(staleSeconds)
		c.metrics.Histogram(telemetry.MetricHierarchyStaleSeconds).Observe(staleSeconds)
	}
}

func (c *Cache) observeEntries(stats Stats) {
	values := []struct {
		kind  string
		count int
	}{{"rules", stats.Rules}, {"fields", stats.Fields}, {"datasets", stats.Manifests}, {"hierarchy_nodes", stats.HierarchyNodes}}
	for _, value := range values {
		c.metrics.Histogram(telemetry.MetricCacheEntries, telemetry.Label{Key: telemetry.LabelKind, Value: value.kind}).Observe(float64(value.count))
	}
}
