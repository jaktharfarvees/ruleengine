package hierarchy

import "sync/atomic"

// Cache atomically publishes immutable hierarchy snapshots. Its zero value is
// ready for use, and reads never acquire a lock.
type Cache struct {
	snapshot atomic.Pointer[Snapshot]
}

// Load returns the current immutable snapshot without locking.
func (c *Cache) Load() *Snapshot { return c.snapshot.Load() }

// Swap publishes snapshot atomically. Passing nil clears the cache.
func (c *Cache) Swap(snapshot *Snapshot) { c.snapshot.Store(snapshot) }
