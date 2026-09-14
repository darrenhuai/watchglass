// Package health turns a stream of per-poll outcomes into rare, edge-
// triggered up/down events. A poll "fails" whenever it produces no reading
// — the grab failed, or the frame arrived but OCR on it failed — so a watch
// is down exactly when it has stopped observing anything, whichever stage
// broke. A watcher that silently stopped watching is worse than no watcher
// — but a watcher that pages you every tick while a camera is unplugged is
// worse still, so only transitions are reported.
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

// SeedDown starts the tracker in the down state, as if it had already
// reported the transition. Used when a watch restarts while its previous
// incarnation was down: the verdict carries over unchanged, further
// failures stay silent (already down), and the first Success emits a real
// "healthy" transition — so recovery is reported exactly once, at the
// moment the source actually answers, no matter how many restarts happened
// in between.
func (t *Tracker) SeedDown() {
	t.down = true
	t.failures = t.threshold
}

// Failure records a failed poll. It returns an event with true only on the
// transition into the down state. err is whatever stopped this poll from
// producing a reading — a failed grab or a failed OCR pass — and is quoted
// in the message.
func (t *Tracker) Failure(err error) (Event, bool) {
	t.failures++
	if t.down || t.failures < t.threshold {
		return Event{}, false
	}
	t.down = true
	return Event{
		State: "down",
		Message: fmt.Sprintf("no reading for %d consecutive polls: %v",
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
