// Package health turns a stream of per-poll outcomes into rare, edge-
// triggered up/down events. A watcher that silently stopped watching is
// worse than no watcher — but a watcher that pages you every tick while a
// camera is unplugged is worse still, so only transitions are reported.
package health

import "fmt"

// Event describes a health transition. State is "down" or "healthy".
type Event struct {
	State   string
	Message string
}

type Tracker struct {
	threshold int
	failures  int
	down      bool
}

// New returns a tracker that reports "down" after threshold consecutive
// failures. A threshold below 1 is clamped to 1.
func New(threshold int) *Tracker {
	if threshold < 1 {
		threshold = 1
	}
	return &Tracker{threshold: threshold}
}

// Failure records a failed poll. It returns an event with true only on the
// transition into the down state.
func (t *Tracker) Failure(err error) (Event, bool) {
	t.failures++
	if t.down || t.failures < t.threshold {
		return Event{}, false
	}
	t.down = true
	return Event{
		State: "down",
		Message: fmt.Sprintf("stream unreachable after %d consecutive failures: %v",
			t.failures, err),
	}, true
}

// Success records a successful poll, clearing the current failure run. It
// returns an event with true only on the transition back to healthy.
func (t *Tracker) Success() (Event, bool) {
	t.failures = 0
	if !t.down {
		return Event{}, false
	}
	t.down = false
	return Event{State: "healthy", Message: "stream recovered"}, true
}
