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

type Registry struct {
	mu  sync.Mutex
	n   int
	buf map[string][]Sample // newest first
}

func New(n int) *Registry {
	return &Registry{n: n, buf: map[string][]Sample{}}
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
}
