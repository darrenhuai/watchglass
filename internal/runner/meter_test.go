package runner

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
)

func meterWatch(confirm int, maxRise float64) config.Watch {
	w := textWatch(config.Trigger{Type: "value", Confirm: confirm})
	w.Notify = nil
	w.Unit, w.DeviceClass, w.MaxRise = "kWh", "energy", maxRise
	return w
}

func (l *logs) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, sub) {
			n++
		}
	}
	return n
}

// V1.1/V1.5: a value watch records the number it published with each
// reading, and nothing for a reading that published none. It never fires.
func TestValueWatchRecordsItsValue(t *testing.T) {
	store := openStore(t)
	g := newRig(t, store)
	w := textWatch(config.Trigger{Type: "value", Confirm: 1})
	r := g.start(w)
	for _, text := range []string{"1234.5", "Err", "1234.6"} {
		if ev := g.show(r, text, 1); ev.Fired {
			t.Fatalf("%q fired", text)
		}
	}
	if g.sent() != 0 {
		t.Fatalf("a value watch sent %d alerts", g.sent())
	}
	rows, err := store.ReadingsPage(w.Name, 0, time.Time{}, time.Time{}, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows %+v err %v", rows, err)
	}
	if !rows[0].HasValue || rows[0].Value != 1234.5 || rows[1].HasValue || rows[2].Value != 1234.6 {
		t.Errorf("rows = %+v", rows)
	}
	if st := r.Value(); !st.HasValue || st.Value != 1234.6 || st.Counter {
		t.Errorf("Value() = %+v", st)
	}
}

// V1.3: a counter holds a lower reading back (not published, not recorded
// as the value, logged once per run), the page's accept releases it, and
// both the last value and the accept survive a restart.
func TestCounterHoldsLowerAndAcceptSurvivesRestart(t *testing.T) {
	store := openStore(t)
	g := newRig(t, store)
	w := meterWatch(1, 0)
	r := g.start(w)
	if ev := g.show(r, "1240.1", 1); !ev.HasValue || ev.Value != 1240.1 {
		t.Fatalf("first reading: %+v", ev)
	}
	for i := 0; i < 3; i++ {
		if ev := g.show(r, "1234.5", 1); ev.HasValue || ev.Held == nil || !ev.Held.Lower {
			t.Fatalf("lower reading %d: %+v", i, ev)
		}
	}
	if n := g.logs.count("held back 1234.5: it is lower than the last value 1240.1"); n != 1 {
		t.Errorf("logged %d times, want once per run: %v", n, g.logs.lines)
	}
	st := r.Value()
	if st.Value != 1240.1 || st.Held == nil || !st.Held.Lower || st.Held.Value != 1234.5 || st.Last != 1240.1 {
		t.Fatalf("Value() = %+v", st)
	}
	rows, _ := store.ReadingsPage(w.Name, 0, time.Time{}, time.Time{}, 10)
	for _, row := range rows[1:] {
		if row.HasValue {
			t.Fatalf("a held reading was recorded with value %v", row.Value)
		}
	}

	// A restart keeps the last value: the lower reading is still held.
	r = g.start(w)
	if st := r.Value(); !st.HasLast || st.Last != 1240.1 {
		t.Fatalf("after restart Value() = %+v", st)
	}
	if ev := g.show(r, "1234.5", 1); ev.HasValue {
		t.Fatalf("after restart the lower reading was published: %+v", ev)
	}

	if st := r.Value(); !st.Restored {
		t.Errorf("after restart the last value is the one kept: %+v", st)
	}
	if err := r.AcceptLower(1300); !errors.Is(err, ErrNotLower) {
		t.Errorf("AcceptLower(1300) = %v, want ErrNotLower", err)
	}
	if err := r.AcceptLower(1234.5); err != nil {
		t.Fatalf("AcceptLower: %v", err)
	}
	if st := r.Value(); !st.HasAccepting || st.Accepting != 1234.5 {
		t.Errorf("Value() after accept = %+v", st)
	}
	// Restart before the next poll: the accept is kept.
	r = g.start(w)
	if st := r.Value(); !st.HasAccepting {
		t.Fatalf("the accept didn't survive the restart: %+v", st)
	}
	if ev := g.show(r, "1234.5", 1); !ev.HasValue || ev.Value != 1234.5 {
		t.Fatalf("after accept: %+v", ev)
	}
	if st := r.Value(); st.HasAccepting || st.Held != nil || st.Last != 1234.5 || st.Restored {
		t.Errorf("Value() after the accept was taken = %+v", st)
	}
	// And the new last value is what the next restart judges by.
	r = g.start(w)
	if ev := g.show(r, "1234.0", 1); ev.HasValue {
		t.Fatalf("1234.0 after accepting 1234.5: %+v", ev)
	}
	if ev := g.show(r, "1234.6", 1); !ev.HasValue {
		t.Fatalf("1234.6: %+v", ev)
	}
}

// V1.4: one high misread is held and logged; a real jump is published once
// RiseConfirm readings agree.
func TestCounterRiseLimitInRunner(t *testing.T) {
	store := openStore(t)
	g := newRig(t, store)
	r := g.start(meterWatch(1, 5))
	g.show(r, "1234.5", 1)
	if ev := g.show(r, "9234.5", 1); ev.HasValue || ev.Held == nil || ev.Held.Lower || ev.Held.Need != 2 {
		t.Fatalf("misread: %+v", ev)
	}
	if !g.logs.has("held back 9234.5: it is more than max_rise 5 above the last value 1234.5") {
		t.Errorf("no log line for the jump: %v", g.logs.lines)
	}
	if ev := g.show(r, "1234.6", 1); !ev.HasValue || ev.Value != 1234.6 {
		t.Fatalf("back to normal: %+v", ev)
	}
	if st := r.Value(); st.Held != nil {
		t.Errorf("the hold should end with a published value: %+v", st)
	}
	g.show(r, "1290.0", 1)
	if ev := g.show(r, "1290.0", 1); !ev.HasValue || ev.Value != 1290 {
		t.Fatalf("a jump read twice: %+v", ev)
	}
}

// An Accept from a page shown while a lower misread was held, pressed
// after the display came back up, must not move the counter down: a later
// misread between the two values would then reach Home Assistant as a
// drop. Before, AcceptLower(1234.5) was taken here and 1236.0 published.
func TestStaleAcceptLowerIsRefused(t *testing.T) {
	store := openStore(t)
	g := newRig(t, store)
	w := meterWatch(1, 0)
	r := g.start(w)
	g.show(r, "1240.1", 1)
	g.show(r, "1234.5", 1) // a misread lower: held, the page offers Accept
	g.show(r, "1240.2", 1) // the display is back up
	if err := r.AcceptLower(1234.5); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("AcceptLower on a stale page = %v, want ErrNotHeld", err)
	}
	if ev := g.show(r, "1236.0", 1); ev.HasValue || ev.Held == nil || !ev.Held.Lower {
		t.Fatalf("a later misread lower reading: %+v", ev)
	}

	// Accepted while held, but posted while the frame that shows the
	// display back up was being read: the next poll drops it, and a
	// restart doesn't bring it back.
	hook := &hookDisplay{display: g.disp}
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, hook, g.notifier, store, g.logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	g.show(r, "1236.0", 1) // held again
	hook.during = func() {
		if err := r.AcceptLower(1236.0); err != nil {
			t.Errorf("AcceptLower while held: %v", err)
		}
	}
	g.show(r, "1240.3", 1)
	if st := r.Value(); !st.HasAccepting {
		t.Fatalf("the accept should wait for the next poll: %+v", st)
	}
	if ev := g.show(r, "1237.0", 1); ev.HasValue || ev.Held == nil {
		t.Fatalf("the stale accept was taken: %+v", ev)
	}
	if st := r.Value(); st.HasAccepting || st.Last != 1240.3 {
		t.Fatalf("Value() = %+v", st)
	}
	if !g.logs.has("1236 was accepted, but the display is back at the counter's value 1240.3") {
		t.Errorf("no log line for the dropped accept: %v", g.logs.lines)
	}
	r = g.start(w)
	if st := r.Value(); st.HasAccepting || st.Last != 1240.3 {
		t.Fatalf("after a restart: %+v", st)
	}
}

// A lower value accepted after a restart, with no reading at or above it
// yet, is the counter's last value but not the one kept from before the
// restart: the page mustn't call it that (Restored).
func TestAcceptedAfterRestartIsNotRestored(t *testing.T) {
	store := openStore(t)
	g := newRig(t, store)
	w := meterWatch(1, 0)
	r := g.start(w)
	g.show(r, "1240.1", 1)
	r = g.start(w)
	g.show(r, "1234.5", 1)
	if err := r.AcceptLower(1234.5); err != nil {
		t.Fatal(err)
	}
	g.show(r, "", 1) // the accept is taken; nothing is read
	if st := r.Value(); st.HasValue || !st.HasLast || st.Last != 1234.5 || st.Restored {
		t.Fatalf("Value() = %+v", st)
	}
}

// hookDisplay is display with a function run once inside the next read,
// as a page's POST can land while a frame is being read.
type hookDisplay struct {
	*display
	during func()
}

func (d *hookDisplay) Recognize(ctx context.Context, img image.Image) (string, error) {
	if f := d.during; f != nil {
		d.during = nil
		f()
	}
	return d.display.Recognize(ctx, img)
}

func TestAcceptLowerOnAPlainValueWatch(t *testing.T) {
	g := newRig(t, openStore(t))
	r := g.start(textWatch(config.Trigger{Type: "value", Confirm: 1}))
	if err := r.AcceptLower(1); !errors.Is(err, ErrNotCounter) {
		t.Errorf("AcceptLower = %v, want ErrNotCounter", err)
	}
}

// A watch that stops being a counter forgets its last value, so turning
// it back on later doesn't hold the meter against a stale number.
func TestCounterStateGoesWhenNoLongerACounter(t *testing.T) {
	store := openStore(t)
	g := newRig(t, store)
	w := meterWatch(1, 0)
	r := g.start(w)
	g.show(r, "500", 1)
	if _, ok, _ := store.LoadCounter(w.Name); !ok {
		t.Fatal("counter state not saved")
	}
	w.Unit, w.DeviceClass = "", ""
	g.start(w)
	if _, ok, _ := store.LoadCounter(w.Name); ok {
		t.Error("counter state kept for a watch that isn't a counter")
	}
}
