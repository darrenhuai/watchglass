package trigger

import (
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
)

// restart is what a watch restart does to an evaluator: a new one for the
// same trigger, given the old one's State, on the same clock.
func restart(t *testing.T, cfg config.Trigger, old *Evaluator, now *time.Time) *Evaluator {
	t.Helper()
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.Now = func() time.Time { return *now }
	e.Restore(old.State())
	return e
}

// A condition that fired, and still holds after a restart, is not news: the
// restored evaluator stays quiet on it, keeps the same State, and fires as
// normal once the display has left the condition and come back.
func TestRestoredStateDoesNotFireAgainOnTheSameDisplay(t *testing.T) {
	cooldown := config.Duration(10 * time.Minute)
	cases := []struct {
		name   string
		cfg    config.Trigger
		before []string // readings up to and including the one that fires
		held   string   // what the display still shows after the restart
		away   string   // a reading that leaves the condition
		back   string   // and one that meets it again
	}{
		{"ocr_match", config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2, Cooldown: cooldown},
			[]string{"PRINTING 80%", "PRINTING 90%", "PRINT COMPLETE", "PRINT COMPLETE"}, "PRINT COMPLETE", "PRINTING 5%", "PRINT COMPLETE"},
		{"numeric", config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Confirm: 2, Cooldown: cooldown},
			[]string{"24.1", "24.3", "25.3", "25.4"}, "25.3", "23.0", "26.1"},
		{"ocr_changed", config.Trigger{Type: "ocr_changed", Confirm: 2, Cooldown: cooldown},
			[]string{"IDLE", "IDLE", "PRINT COMPLETE", "PRINT COMPLETE"}, "PRINT COMPLETE", "", "IDLE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, now := mk(t, c.cfg)
			fired := feed(e, c.before...)
			for i, f := range fired {
				if f != (i == len(fired)-1) {
					t.Fatalf("before the restart: fired = %v, want only the last reading to fire", fired)
				}
			}
			firedAt := *now
			saved := e.State()
			if !saved.LastFired.Equal(firedAt) || !saved.HasStable {
				t.Fatalf("State after the fire = %+v, want LastFired %v and a settled state", saved, firedAt)
			}

			*now = now.Add(time.Minute)
			e2 := restart(t, c.cfg, e, now)
			for i := 0; i < 5; i++ {
				ev := e2.ObserveText(c.held)
				if ev.Fired || ev.Pending != 0 {
					t.Fatalf("reading %d after the restart: %+v, want no fire and nothing pending: the display hasn't changed", i+1, ev)
				}
			}
			if got := e2.State(); !got.Equal(saved) {
				t.Errorf("State after the restart = %+v, want it unchanged: %+v", got, saved)
			}

			// The display leaves the condition and comes back while the
			// cooldown from the fire before the restart is still running:
			// the alert is held until the same moment it would have been.
			if c.away != "" {
				feed(e2, c.away, c.away)
			}
			ends := firedAt.Add(time.Duration(cooldown))
			var ev Event
			for i := 0; i < 2; i++ {
				ev = e2.ObserveText(c.back)
			}
			if ev.Fired || ev.Pending != 2 || !ev.CooldownEnds.Equal(ends) {
				t.Fatalf("back in the condition inside the cooldown: %+v, want it held until %v, the end of the cooldown that started before the restart", ev, ends)
			}
			*now = ends.Add(time.Second)
			if ev := e2.ObserveText(c.back); !ev.Fired {
				t.Errorf("after the cooldown: %+v, want the fire", ev)
			}
		})
	}
}

// Without Restore the same readings fire, as they always have: that is the
// behaviour a watch whose state no longer applies falls back to.
func TestFreshEvaluatorStillFiresFromNothing(t *testing.T) {
	e, _ := mk(t, config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2})
	if got := feed(e, "PRINT COMPLETE", "PRINT COMPLETE"); !got[1] {
		t.Errorf("fired = %v, want the second reading to fire on a fresh evaluator", got)
	}
	fresh, _ := mk(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 1, Confirm: 1})
	if st := fresh.State(); !st.Equal(State{}) {
		t.Errorf("State of an evaluator that has read nothing = %+v, want the zero State", st)
	}
}

// An edge that the cooldown was holding back when the watch stopped still
// goes out after the restart, when the cooldown ends.
func TestRestoredStateKeepsAHeldAlert(t *testing.T) {
	cfg := config.Trigger{Type: "ocr_match", Pattern: "(?i)error", Confirm: 1, Cooldown: config.Duration(15 * time.Minute)}
	e, now := mk(t, cfg)
	feed(e, "OK")
	if !e.ObserveText("ERROR: jam").Fired {
		t.Fatal("first error should fire")
	}
	firedAt := *now
	*now = now.Add(2 * time.Minute)
	feed(e, "OK")
	*now = now.Add(3 * time.Minute)
	if ev := e.ObserveText("ERROR: jam again"); ev.Fired || ev.Pending != 1 {
		t.Fatalf("re-error inside the cooldown: %+v, want it held", ev)
	}

	e2 := restart(t, cfg, e, now)
	if ev := e2.ObserveText("ERROR: jam again"); ev.Fired || ev.Pending != 1 || !ev.CooldownEnds.Equal(firedAt.Add(15*time.Minute)) {
		t.Fatalf("after the restart, inside the cooldown: %+v, want it still held until %v", ev, firedAt.Add(15*time.Minute))
	}
	*now = firedAt.Add(16 * time.Minute)
	if !e2.ObserveText("ERROR: jam again").Fired {
		t.Error("the held error must fire at the first reading after the cooldown, restart or not")
	}
}

// pixel_change has no settled state to keep, only its cooldown.
func TestRestoredPixelCooldown(t *testing.T) {
	cfg := config.Trigger{Type: "pixel_change", Threshold: 10, Cooldown: config.Duration(10 * time.Minute)}
	e, now := mk(t, cfg)
	if !e.ObservePixel(50).Fired {
		t.Fatal("above threshold should fire")
	}
	if st := e.State(); st.HasStable || !st.LastFired.Equal(*now) {
		t.Fatalf("pixel State = %+v, want only the fire time", st)
	}
	*now = now.Add(time.Minute)
	e2 := restart(t, cfg, e, now)
	if e2.ObservePixel(50).Fired {
		t.Error("a change inside the cooldown that started before the restart must not fire")
	}
	*now = now.Add(10 * time.Minute)
	if !e2.ObservePixel(50).Fired {
		t.Error("after the cooldown it should fire")
	}
}

// Restore keeps only what the trigger type could have produced itself; the
// fire time always applies.
func TestRestoreDropsAStateTheTypeCannotHave(t *testing.T) {
	at := time.Unix(900, 0)
	for _, c := range []struct {
		name string
		cfg  config.Trigger
		in   State
		want State
	}{
		{"ocr_match takes a condition", config.Trigger{Type: "ocr_match", Pattern: "x"},
			State{LastFired: at, Stable: "cond:false", HasStable: true}, State{LastFired: at, Stable: "cond:false", HasStable: true}},
		{"ocr_match refuses a text", config.Trigger{Type: "ocr_match", Pattern: "x"},
			State{LastFired: at, Stable: "PRINT COMPLETE", HasStable: true}, State{LastFired: at}},
		{"numeric refuses a text", config.Trigger{Type: "numeric", Op: "gt"},
			State{LastFired: at, Stable: "25.3", HasStable: true}, State{LastFired: at}},
		{"ocr_changed takes any text", config.Trigger{Type: "ocr_changed"},
			State{Stable: "cond:true", HasStable: true}, State{Stable: "cond:true", HasStable: true}},
		{"ocr_changed takes the empty text", config.Trigger{Type: "ocr_changed"},
			State{Stable: "", HasStable: true}, State{Stable: "", HasStable: true}},
		{"pixel_change has none", config.Trigger{Type: "pixel_change", Threshold: 5},
			State{LastFired: at, Stable: "cond:true", HasStable: true}, State{LastFired: at}},
		{"a text without HasStable is not a state", config.Trigger{Type: "ocr_changed"},
			State{LastFired: at, Stable: "left over"}, State{LastFired: at}},
	} {
		e, _ := mk(t, c.cfg)
		e.Restore(c.in)
		if got := e.State(); got != c.want {
			t.Errorf("%s: State after Restore(%+v) = %+v, want %+v", c.name, c.in, got, c.want)
		}
	}

	// A restored "condition not met" arms a numeric watch exactly like
	// having read it: the next confirmed crossing fires.
	e, _ := mk(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Confirm: 1})
	e.Restore(State{Stable: "cond:false", HasStable: true})
	if !e.ObserveText("26").Fired {
		t.Error("crossing after a restored below-threshold state should fire")
	}
	// Restore replaces what was there, it doesn't merge.
	e.Restore(State{})
	if got := e.State(); got != (State{}) {
		t.Errorf("State after Restore(zero) = %+v, want zero", got)
	}
}

// A fire time read back from storage compares equal to the one that was
// written, though it has lost its monotonic reading and its location.
func TestStateEqualComparesInstants(t *testing.T) {
	now := time.Now()
	a := State{LastFired: now, Stable: "x", HasStable: true}
	b := State{LastFired: time.Unix(0, now.UnixNano()).UTC(), Stable: "x", HasStable: true}
	if !a.Equal(b) {
		t.Errorf("%+v and %+v are the same state", a, b)
	}
	for _, other := range []State{
		{LastFired: now.Add(time.Nanosecond), Stable: "x", HasStable: true},
		{LastFired: now, Stable: "y", HasStable: true},
		{LastFired: now, Stable: "x"},
	} {
		if a.Equal(other) {
			t.Errorf("%+v and %+v differ", a, other)
		}
	}
}
