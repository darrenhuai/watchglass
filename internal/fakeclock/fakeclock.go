// Package fakeclock is a clock tests move by hand, for code that reads the
// time through a func() time.Time it lets a test replace (the login gate's
// clock, the pixel_change Test's) or waits on it through After (the
// runner's alert retries). Only tests import it.
package fakeclock

import (
	"sync"
	"time"
)

// Clock is a time that only Add moves. It is safe for concurrent use.
type Clock struct {
	mu      sync.Mutex
	t       time.Time
	waiting []waiter
}

// waiter is an After that hasn't fired yet.
type waiter struct {
	at time.Time
	ch chan time.Time
}

// New returns a Clock that reads t.
func New(t time.Time) *Clock { return &Clock{t: t} }

// Now is the clock's time; pass the method value where a func() time.Time
// is wanted.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Add moves the clock by d (backwards for a negative d) and fires every
// After whose time has come.
func (c *Clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.fire()
	c.mu.Unlock()
}

// After is time.After on this clock: the channel receives the clock's
// time once Add has moved it d or more past now. A d of zero or less
// fires at once.
func (c *Clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiting = append(c.waiting, waiter{at: c.t.Add(d), ch: ch})
	c.fire()
	return ch
}

// Waiting is how many Afters have not fired yet.
func (c *Clock) Waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiting)
}

// fire sends to every waiter whose time has come. Called with mu held.
func (c *Clock) fire() {
	kept := c.waiting[:0]
	for _, w := range c.waiting {
		if w.at.After(c.t) {
			kept = append(kept, w)
			continue
		}
		w.ch <- c.t
	}
	c.waiting = kept
}
