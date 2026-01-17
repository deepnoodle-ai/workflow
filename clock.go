package workflow

import (
	"sync"
	"time"
)

// Clock abstracts time operations for testability.
// Use RealClock for production and FakeClock for testing.
type Clock interface {
	// Now returns the current time.
	Now() time.Time

	// After returns a channel that receives the current time after duration d.
	After(d time.Duration) <-chan time.Time
}

// RealClock implements Clock using the standard time package.
type RealClock struct{}

// NewRealClock creates a new RealClock.
func NewRealClock() *RealClock {
	return &RealClock{}
}

// Now returns the current time.
func (c *RealClock) Now() time.Time {
	return time.Now()
}

// After returns a channel that receives the current time after duration d.
func (c *RealClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// FakeClock is a controllable clock for testing.
// Time only advances when Advance() is called.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []clockWaiter
}

type clockWaiter struct {
	deadline time.Time
	ch       chan time.Time
}

// NewFakeClock creates a new FakeClock starting at the given time.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{
		now: start,
	}
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After returns a channel that receives the time after duration d.
// The channel only receives when Advance() moves time past the deadline.
func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	ch := make(chan time.Time, 1)
	deadline := c.now.Add(d)

	// If already past deadline, send immediately
	if !deadline.After(c.now) {
		ch <- c.now
		return ch
	}

	c.waiters = append(c.waiters, clockWaiter{
		deadline: deadline,
		ch:       ch,
	})

	return ch
}

// Advance moves the fake clock forward by duration d, triggering any waiters
// whose deadlines have passed.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)

	// Trigger waiters whose deadlines have passed
	remaining := make([]clockWaiter, 0, len(c.waiters))
	for _, w := range c.waiters {
		if !w.deadline.After(c.now) {
			// Deadline passed, send time and don't keep in waiters
			w.ch <- c.now
		} else {
			remaining = append(remaining, w)
		}
	}
	c.waiters = remaining
}

// Set sets the fake clock to a specific time, triggering any waiters
// whose deadlines have passed.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = t

	// Trigger waiters whose deadlines have passed
	remaining := make([]clockWaiter, 0, len(c.waiters))
	for _, w := range c.waiters {
		if !w.deadline.After(c.now) {
			w.ch <- c.now
		} else {
			remaining = append(remaining, w)
		}
	}
	c.waiters = remaining
}

// WaiterCount returns the number of pending After() calls waiting for time to advance.
// Useful for testing to ensure timers are set up before advancing.
func (c *FakeClock) WaiterCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}
