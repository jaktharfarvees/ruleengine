package ruletest

import (
	"sync"
	"time"
)

// FakeClock is a clock a test moves by hand.
//
// It satisfies every clock the library accepts, because each is declared as
// the same single method where it is consumed. Use it to drive cache
// staleness, audit timestamps or a reconcile interval without sleeping, which
// keeps a test fast and keeps it from failing on a slow machine.
//
// Evaluation business time never comes from a clock: it arrives on the request
// as AsOf and ValidAt. So a FakeClock changes a decision's lifecycle, never
// its outcome.
type FakeClock struct {
	mu      sync.RWMutex
	current time.Time
}

// NewFakeClock starts a clock at a given instant. A zero instant starts at a
// fixed, readable date rather than the epoch, so a failure message shows a
// time a reader recognises.
func NewFakeClock(start time.Time) *FakeClock {
	if start.IsZero() {
		start = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	return &FakeClock{current: start}
}

// Now returns the current instant. It is safe to call while another goroutine
// advances the clock.
func (c *FakeClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current
}

// Advance moves the clock forward and returns the new instant. A negative step
// moves it back, which is occasionally what a test of a retroactive run needs.
func (c *FakeClock) Advance(step time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(step)
	return c.current
}

// Set moves the clock to an instant and returns it.
func (c *FakeClock) Set(instant time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = instant
	return c.current
}
