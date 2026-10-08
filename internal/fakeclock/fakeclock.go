// Package fakeclock is a clock tests move by hand, for code that reads the
// time through a func() time.Time it lets a test replace (the login gate's
// clock, the pixel_change Test's). Only tests import it.
package fakeclock

import (
	"sync"
	"time"
)

// Clock is a time that only Add moves. It is safe for concurrent use.
type Clock struct {
	mu sync.Mutex
	t  time.Time
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

// Add moves the clock by d (backwards for a negative d).
func (c *Clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
