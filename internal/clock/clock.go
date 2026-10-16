// Package clock supplies injectable time without introducing mutable globals.
package clock

import "time"

// Clock returns the current time for non-evaluation lifecycle operations.
type Clock interface{ Now() time.Time }

// System is the production wall clock.
type System struct{}

// Now returns the current wall-clock time.
func (System) Now() time.Time { return time.Now() }

// Fixed is a deterministic clock for tests.
type Fixed struct{ Time time.Time }

// Now returns the configured fixed time.
func (f Fixed) Now() time.Time { return f.Time }
