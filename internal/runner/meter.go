package runner

import (
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/darrenhuai/watchglass/internal/trigger"
)

// A watch with a value (numeric, value) publishes a number; a counter's
// number only goes up (trigger/counter.go). This file keeps what the web
// UI shows about that number (ValueStatus), a counter's last value in the
// history database across restarts, and the accept action for a lower
// value on a replaced meter.

// ValueStatus is the number a watch last published and, for a counter,
// what it is holding back, for the watch page and the dashboard.
type ValueStatus struct {
	// Value is the last number published, at At (HasValue).
	Value    float64
	HasValue bool
	At       time.Time
	// Counter: the watch's number is a counter. Last is the value a lower
	// or jumped reading is judged against (HasLast): the last one
	// published, restored or accepted.
	Counter bool
	Last    float64
	HasLast bool
	// Restored: Last is the value kept from before the restart, and
	// nothing has been published or accepted since.
	Restored bool
	// Held is the reading held back now, nil when none is, and HeldSince
	// when this run of holds began.
	Held      *trigger.Hold
	HeldSince time.Time
	// Accepting is a lower value accepted on the watch page that the next
	// reading takes (HasAccepting).
	Accepting    float64
	HasAccepting bool
}

// meter is a Runner's value bookkeeping. status and pending are shared
// with the web UI's goroutines (Value, AcceptLower) under mu; savedLast is
// the poll goroutine's own.
type meter struct {
	mu      sync.Mutex
	status  ValueStatus
	pending *float64
	// restored: pending was saved before a restart, and was checked
	// against the hold it was accepted for then.
	restored bool
	// savedLast is the counter's last value as the store has it.
	savedLast    float64
	hasSavedLast bool
}

// ErrNotLower is AcceptLower's answer to a value that isn't below the
// counter's last value.
var ErrNotLower = errors.New("that value isn't lower than the last one")

// ErrNotHeld is AcceptLower's answer when the counter isn't holding a
// lower value back now: the page was shown before the display came back
// up, and taking the value would let a later misread lower one through.
var ErrNotHeld = errors.New("the counter isn't holding a lower value back")

// ErrNotCounter is AcceptLower's answer on a watch that isn't a counter.
var ErrNotCounter = errors.New("this watch isn't a counter")

// restoreCounter turns the counter gate on for a counter watch and puts
// back its last value and any accepted value still waiting. A watch that
// is no longer a counter forgets what it kept as one.
func (r *Runner) restoreCounter() {
	w := r.watch
	if !w.IsCounter() {
		if r.store != nil {
			if _, ok, err := r.store.LoadCounter(w.Name); err == nil && ok {
				if err := r.store.DropCounter(w.Name); err != nil {
					r.logf("watch %s: forget its counter state: %v", w.Name, err)
				}
			}
		}
		return
	}
	r.eval.SetCounter(w.MaxRise, w.RiseConfirm())
	r.meter.status.Counter = true
	if r.store == nil {
		return
	}
	c, ok, err := r.store.LoadCounter(w.Name)
	if err != nil {
		r.logf("watch %s: couldn't read its counter's last value, so the first value read is taken as it: %v", w.Name, err)
		return
	}
	if !ok {
		return
	}
	r.eval.RestoreCounter(c.Last)
	r.meter.savedLast, r.meter.hasSavedLast = c.Last, true
	r.meter.status.Last, r.meter.status.HasLast = c.Last, true
	r.meter.status.Restored = true
	if c.HasAccept {
		v := c.Accept
		r.meter.pending, r.meter.restored = &v, true
		r.meter.status.Accepting, r.meter.status.HasAccepting = v, true
	}
}

// AcceptLower accepts v, a value lower than the counter's last one, as
// its new last value (the meter was replaced or reset): the next reading
// at v or above is published. It is saved first, so a restart before
// that reading still takes it. Once the counter has a last value, it
// takes v only while it is holding a lower value back (ErrNotHeld): a
// page shown before the display came back up must not move the counter
// down. Safe to call from any goroutine.
func (r *Runner) AcceptLower(v float64) error {
	r.meter.mu.Lock()
	defer r.meter.mu.Unlock()
	st := r.meter.status
	if !st.Counter {
		return ErrNotCounter
	}
	if st.HasLast && !(v < st.Last) {
		return ErrNotLower
	}
	if st.HasLast && (st.Held == nil || !st.Held.Lower) {
		return ErrNotHeld
	}
	if r.store != nil {
		if err := r.store.SaveCounterAccept(r.watch.Name, v); err != nil {
			return err
		}
	}
	r.meter.pending, r.meter.restored = &v, false
	r.meter.status.Accepting, r.meter.status.HasAccepting = v, true
	return nil
}

// Value is the watch's ValueStatus now. Safe to call from any goroutine.
func (r *Runner) Value() ValueStatus {
	r.meter.mu.Lock()
	defer r.meter.mu.Unlock()
	st := r.meter.status
	if st.Held != nil {
		h := *st.Held
		st.Held = &h
	}
	return st
}

// takeAccept hands the evaluator a lower value accepted since the last
// poll. It runs on the poll goroutine, before the frame is read.
func (r *Runner) takeAccept() {
	r.meter.mu.Lock()
	p, restored := r.meter.pending, r.meter.restored
	// The reading after the accept (it can have been read while the
	// page posted) may have shown the display back up.
	stale := r.meter.status.Held == nil || !r.meter.status.Held.Lower
	r.meter.pending, r.meter.restored = nil, false
	r.meter.mu.Unlock()
	if p == nil {
		return
	}
	v := *p
	if _, has := r.eval.CounterLast(); has && stale && !restored {
		r.dropAccept(v)
		return
	}
	took := r.eval.AcceptLower(v)
	if _, has := r.eval.CounterLast(); !has {
		// Nothing published yet, before or since the restart: the
		// accepted value is where the counter starts.
		r.eval.RestoreCounter(v)
		took = true
	}
	last, _ := r.eval.CounterLast()
	if took {
		r.logf("watch %s: %s accepted as the counter's new value; readings from it up are published", r.watch.Name, num(v))
	} else {
		r.logf("watch %s: %s was accepted, but the counter is at %s already, so it carries on from there", r.watch.Name, num(v), num(last))
	}
	if r.store != nil {
		if err := r.store.TakeCounterAccept(r.watch.Name, v, last); err != nil {
			r.logf("watch %s: save counter: %v", r.watch.Name, err)
		} else {
			r.meter.savedLast, r.meter.hasSavedLast = last, true
		}
	}
	r.meter.mu.Lock()
	r.meter.status.HasAccepting = false
	r.meter.status.Last, r.meter.status.HasLast = last, true
	r.meter.status.Restored = r.meter.status.Restored && !took
	r.meter.status.Held, r.meter.status.HeldSince = nil, time.Time{}
	r.meter.mu.Unlock()
}

// dropAccept forgets v, accepted on the page while a lower value was
// held, because the display has come back up since: the counter keeps its
// last value, and the saved accept is cleared.
func (r *Runner) dropAccept(v float64) {
	last, _ := r.eval.CounterLast()
	r.logf("watch %s: %s was accepted, but the display is back at the counter's value %s, so the counter carries on from there", r.watch.Name, num(v), num(last))
	if r.store != nil {
		if err := r.store.TakeCounterAccept(r.watch.Name, v, last); err != nil {
			r.logf("watch %s: save counter: %v", r.watch.Name, err)
		} else {
			r.meter.savedLast, r.meter.hasSavedLast = last, true
		}
	}
	r.meter.mu.Lock()
	r.meter.status.HasAccepting = false
	r.meter.mu.Unlock()
}

// noteValue records what one reading did to the watch's number: the value
// it published, or the hold it was stopped by (logged once per run of
// holds of one kind), and a counter's new last value, saved when it
// moved. It runs on the poll goroutine.
func (r *Runner) noteValue(ev trigger.Event, at time.Time) {
	last, hasLast := r.eval.CounterLast()
	r.meter.mu.Lock()
	st := &r.meter.status
	newHold := ev.Held != nil && (st.Held == nil || st.Held.Lower != ev.Held.Lower)
	if ev.HasValue {
		st.Value, st.HasValue, st.At = ev.Value, true, at
		st.Held, st.HeldSince = nil, time.Time{}
		st.Restored = false
	}
	if ev.Held != nil {
		if newHold {
			st.HeldSince = at
		}
		h := *ev.Held
		st.Held = &h
	}
	st.Last, st.HasLast = last, hasLast
	r.meter.mu.Unlock()
	if newHold {
		r.logHold(*ev.Held)
	}
	if r.store != nil && hasLast && (!r.meter.hasSavedLast || last != r.meter.savedLast) {
		if err := r.store.SaveCounterLast(r.watch.Name, last); err != nil {
			r.logf("watch %s: save counter: %v", r.watch.Name, err)
		} else {
			r.meter.savedLast, r.meter.hasSavedLast = last, true
		}
	}
}

func (r *Runner) logHold(h trigger.Hold) {
	if h.Lower {
		r.logf("watch %s: held back %s: it is lower than the last value %s, and a counter never goes down. If the meter was replaced, accept the new value on the watch page", r.watch.Name, num(h.Value), num(h.Last))
		return
	}
	r.logf("watch %s: held back %s: it is more than max_rise %s above the last value %s. It is published once %d readings in a row agree", r.watch.Name, num(h.Value), num(h.MaxRise), num(h.Last), h.Need)
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
