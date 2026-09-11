// Package state holds an in-memory ring buffer of recent readings per watch,
// feeding the web UI's live readout and history strip. Persistence stays in
// the history package; this is display state only.
package state

import (
	"sync"
	"time"
)

// Sample is one observed reading with its rendered crop.
//
// The PNG field holds a shared backing array: do not modify the returned bytes.
// Returned by Recent and Latest, the Sample struct itself is copied, but PNG's
// backing array remains shared with the registry. Callers must treat PNG as
// read-only to avoid corrupting shared state.
type Sample struct {
	TS      time.Time
	Reading string
	Fired   bool
	PNG     []byte
}

// Health is a watch's most recent stream health verdict, mirrored from
// health.Tracker's edge-triggered up/down transitions (see
// supervisor.Supervisor.Start's r.OnHealth wiring). The zero value (Down:
// false) reads as healthy — indistinguishable from "never checked yet",
// which is intentional: a brand-new watch with no Health entry at all
// should also read as healthy rather than erroring, see Registry.GetHealth.
type Health struct {
	Down    bool
	Message string
	Since   time.Time // when this Down/healthy state began
}

type Registry struct {
	mu     sync.Mutex
	n      int
	buf    map[string][]Sample // newest first
	health map[string]Health
}

func New(n int) *Registry {
	return &Registry{n: n, buf: map[string][]Sample{}, health: map[string]Health{}}
}

func (r *Registry) Add(watch string, s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := append([]Sample{s}, r.buf[watch]...)
	if len(list) > r.n {
		list = list[:r.n]
	}
	r.buf[watch] = list
}

// Recent returns a copy of the newest-first samples for watch.
// The returned Sample structs are copied, but their PNG backing arrays are
// shared with the registry; treat PNG as read-only to avoid data corruption.
func (r *Registry) Recent(watch string) []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Sample(nil), r.buf[watch]...)
}

// Latest returns the most recent sample for watch, or false if none exist.
// The returned Sample struct is copied, but its PNG backing array is shared
// with the registry; treat PNG as read-only to avoid data corruption.
func (r *Registry) Latest(watch string) (Sample, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.buf[watch]
	if len(l) == 0 {
		return Sample{}, false
	}
	return l[0], true
}

// Drop forgets a watch's samples (used when a watch is deleted).
func (r *Registry) Drop(watch string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.buf, watch)
	delete(r.health, watch)
}

// SetHealth records watch's current stream health verdict, overwriting
// whatever was there before. Called from supervisor.Supervisor.Start's
// r.OnHealth closure on every edge-triggered transition, and reset to the
// zero value (healthy) each time a watch (re)starts, since a fresh
// health.Tracker always begins in the "not down" state.
func (r *Registry) SetHealth(watch string, h Health) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.health[watch] = h
}

// GetHealth returns watch's last recorded health verdict, or false if none
// has ever been recorded (which reads as healthy to callers, same as an
// explicit Health{Down: false}).
func (r *Registry) GetHealth(watch string) (Health, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.health[watch]
	return h, ok
}
