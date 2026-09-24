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
	// Pending of Need: this reading would fire once Confirm readings in a
	// row agree, and this is the Pending-th (trigger.Event). 0 otherwise.
	Pending int
	Need    int
	// CooldownEnds: Cooldown holds the alert this progress leads to until
	// then (trigger.Event). Zero when no cooldown is running.
	CooldownEnds time.Time
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

// Delivery is how the last alert a watch raised went out: a fire, or a
// stream going down or recovering. TS is when the alert was raised (for a
// fire, the fired Sample's TS), so the page can tell which reading it
// belongs to. Err is already safe to show: no URL beyond scheme://host and
// no credential (notify.Scrub), at most 200 characters.
type Delivery struct {
	TS      time.Time
	Kind    string // "fired", "down" or "recovered"
	OK      bool   // every notify URL took it
	Skipped bool   // the watch has no notify URLs, so nothing was sent
	Err     string // why it wasn't delivered, when !OK && !Skipped
}

type Registry struct {
	mu     sync.Mutex
	n      int
	buf    map[string][]Sample // newest first
	health map[string]Health
	// delivery is each watch's newest finished (or skipped) delivery, and
	// sending the TS of an alert handed to the sender that hasn't finished.
	// They are kept apart so a failure stays on show until a later send
	// actually succeeds, not merely until the next one starts.
	delivery map[string]Delivery
	sending  map[string]time.Time
	// fired is when each watch last fired. buf only keeps the last n
	// samples, and with confirm/cooldown the fired sample is usually gone
	// a few readings later; the dashboard still needs to say it happened.
	fired map[string]time.Time
}

func New(n int) *Registry {
	return &Registry{n: n, buf: map[string][]Sample{}, health: map[string]Health{}, fired: map[string]time.Time{},
		delivery: map[string]Delivery{}, sending: map[string]time.Time{}}
}

func (r *Registry) Add(watch string, s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := append([]Sample{s}, r.buf[watch]...)
	if len(list) > r.n {
		list = list[:r.n]
	}
	r.buf[watch] = list
	if s.Fired && s.TS.After(r.fired[watch]) {
		r.fired[watch] = s.TS
	}
}

// LastFired returns when watch last fired (the TS of its newest fired
// sample since watchglass started), or false if it hasn't fired. Unlike
// Recent, it doesn't forget a fire once n newer readings have come in.
func (r *Registry) LastFired(watch string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.fired[watch]
	return t, ok
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
	delete(r.fired, watch)
	delete(r.delivery, watch)
	delete(r.sending, watch)
}

// SetSending notes that the alert raised at ts has been handed to the
// sender. SetDelivery for that alert (or a later one) clears it.
func (r *Registry) SetSending(watch string, ts time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ts.After(r.sending[watch]) {
		r.sending[watch] = ts
	}
}

// ClearSending forgets an alert in flight: a watch that restarts has none
// of its own yet, and its previous run's reports no longer count.
func (r *Registry) ClearSending(watch string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sending, watch)
}

// Sending is the TS of the newest alert still being sent, if any.
func (r *Registry) Sending(watch string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.sending[watch]
	return t, ok
}

// SetDelivery records how an alert went out. An older alert's result that
// arrives late never replaces a newer one's.
func (r *Registry) SetDelivery(watch string, d Delivery) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.delivery[watch]; !ok || !d.TS.Before(cur.TS) {
		r.delivery[watch] = d
	}
	if s, ok := r.sending[watch]; ok && !s.After(d.TS) {
		delete(r.sending, watch)
	}
}

// GetDelivery returns the watch's newest finished delivery, or false if it
// hasn't raised an alert since watchglass started (or since ClearDelivery).
func (r *Registry) GetDelivery(watch string) (Delivery, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.delivery[watch]
	return d, ok
}

// ClearDelivery forgets a watch's delivery record. A save that changes the
// notify URLs calls it: a failure to reach URLs that are no longer in the
// list says nothing about the new ones.
func (r *Registry) ClearDelivery(watch string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.delivery, watch)
	delete(r.sending, watch)
}

// SetHealth records watch's current stream health verdict, overwriting
// whatever was there before. Called from supervisor.Supervisor.Start's
// r.OnHealth closure on every edge-triggered transition. A restart never
// resets it: Start seeds the new runner's health.Tracker with a Down
// verdict found here, so the verdict (and its Since) survives until a
// poll actually produces a reading again.
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
