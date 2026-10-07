package runner

import (
	"context"
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/notify"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// display is a screen whose text the test changes between polls.
type display struct{ text string }

func (d *display) Recognize(ctx context.Context, img image.Image) (string, error) {
	return d.text, nil
}

// screen is a camera whose picture the test changes between polls.
type screen struct{ img image.Image }

func (s *screen) Grab(ctx context.Context) (image.Image, error) { return s.img, nil }

// logs collects a runner's log lines.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logs) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

func openStore(t *testing.T) *history.Store {
	t.Helper()
	store, err := history.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// rig is one watch across restarts: the same store, display and notifier,
// and a clock the test moves. start makes the next runner the way the
// supervisor does on boot or on Save & restart watch.
type rig struct {
	t        *testing.T
	store    *history.Store
	disp     *display
	notifier *fakeNotifier
	now      time.Time
	logs     *logs
}

func newRig(t *testing.T, store *history.Store) *rig {
	return &rig{t: t, store: store, disp: &display{}, notifier: &fakeNotifier{}, now: time.Now(), logs: &logs{}}
}

func (g *rig) start(w config.Watch) *Runner {
	g.t.Helper()
	return g.startWith(w, g.notifier)
}

// startWith is start with another notifier; nil is a watch with no notify
// URLs.
func (g *rig) startWith(w config.Watch, n notify.Notifier) *Runner {
	g.t.Helper()
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, g.disp, n, g.store, g.logs.logf)
	if err != nil {
		g.t.Fatalf("New: %v", err)
	}
	r.eval.Now = func() time.Time { return g.now }
	return r
}

// show puts text on the display and polls n times, returning the last event.
func (g *rig) show(r *Runner, text string, n int) trigger.Event {
	g.t.Helper()
	g.disp.text = text
	var ev trigger.Event
	for i := 0; i < n; i++ {
		var err error
		if ev, err = r.Tick(context.Background()); err != nil {
			g.t.Fatalf("Tick: %v", err)
		}
	}
	return ev
}

func (g *rig) sent() int { return len(g.notifier.sent) }

const tenMin = config.Duration(10 * time.Minute)

func textWatch(tr config.Trigger) config.Watch {
	w := watchCfg(tr)
	w.Source = "http://camera.invalid/snap.jpg"
	w.Interval = config.Duration(2 * time.Second)
	w.Notify = []string{"generic://example.invalid/hook"}
	return w
}

// The three text triggers, one restart each: the watch fires, stops, and
// starts again on a display that still shows the same thing. Nothing fires
// a second time, the cooldown still ends when it was going to, and a real
// change afterwards fires as normal.
func TestRestartDoesNotRepeatAnAlert(t *testing.T) {
	cases := []struct {
		name         string
		tr           config.Trigger
		before, held string // the display before the fire, and the reading that fires and stays
		away, back   string // then it leaves the condition and comes back
	}{
		{"ocr_match", config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2, Cooldown: tenMin},
			"PRINTING 90%", "PRINT COMPLETE", "PRINTING 4%", "PRINT COMPLETE"},
		{"numeric", config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Confirm: 2, Cooldown: tenMin},
			"24.9", "25.3", "24.0", "26.5"},
		{"ocr_changed", config.Trigger{Type: "ocr_changed", Confirm: 2, Cooldown: tenMin},
			"IDLE", "PRINT COMPLETE", "PRINT COMPLETE", "IDLE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, openStore(t))
			w := textWatch(c.tr)

			r1 := g.start(w)
			g.show(r1, c.before, 2)
			if ev := g.show(r1, c.held, 2); !ev.Fired || g.sent() != 1 {
				t.Fatalf("first run: last event %+v, %d alerts; want it to fire once", ev, g.sent())
			}
			firedAt := g.now
			ends := firedAt.Add(time.Duration(tenMin))

			// Stopped, then started again a minute later: boot, an update,
			// or Save & restart watch with nothing about the question changed.
			g.now = g.now.Add(time.Minute)
			r2 := g.start(w)
			for i := 0; i < 5; i++ {
				if ev := g.show(r2, c.held, 1); ev.Fired || ev.Pending != 0 {
					t.Fatalf("poll %d after the restart: %+v, want no fire and nothing building up", i+1, ev)
				}
			}
			if g.sent() != 1 {
				t.Fatalf("%d alerts after the restart, want still 1: %v", g.sent(), g.notifier.sent)
			}
			if got := r2.eval.State().LastFired; !got.Equal(firedAt) {
				t.Errorf("fire time after the restart = %v, want %v", got, firedAt)
			}
			if got, ok := r2.RestoredFire(); !ok || !got.Equal(firedAt) {
				t.Errorf("RestoredFire = %v, %v; want %v, true", got, ok, firedAt)
			}

			// The display changes for real. Inside the cooldown the alert is
			// held until the moment the cooldown was always going to end.
			g.show(r2, c.away, 2)
			if ev := g.show(r2, c.back, 2); ev.Fired || ev.Pending != 2 || !ev.CooldownEnds.Equal(ends) {
				t.Fatalf("a real change inside the cooldown: %+v, want it held until %v", ev, ends)
			}
			g.now = ends.Add(time.Second)
			if ev := g.show(r2, c.back, 1); !ev.Fired || g.sent() != 2 {
				t.Fatalf("after the cooldown: %+v, %d alerts; want the second alert", ev, g.sent())
			}

			// And that second fire is remembered in turn.
			g.now = g.now.Add(time.Minute)
			r3 := g.start(w)
			if ev := g.show(r3, c.back, 3); ev.Fired || g.sent() != 2 {
				t.Errorf("third run: %+v, %d alerts; want none new", ev, g.sent())
			}
		})
	}
}

// pixel_change keeps no settled state, only its cooldown: a restart inside
// it doesn't fire on a change, and the cooldown ends when it was going to.
func TestRestartKeepsPixelCooldown(t *testing.T) {
	store := openStore(t)
	notifier := &fakeNotifier{}
	now := time.Now()
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10, Cooldown: tenMin})
	cam := &screen{}
	start := func() *Runner {
		r, err := New(w, cam, nil, notifier, store, t.Logf)
		if err != nil {
			t.Fatal(err)
		}
		r.eval.Now = func() time.Time { return now }
		return r
	}
	flip := func(r *Runner, v uint8) trigger.Event {
		cam.img = flat(10, 10, v)
		ev, err := r.Tick(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}

	r1 := start()
	flip(r1, 0) // baseline
	if ev := flip(r1, 255); !ev.Fired || len(notifier.sent) != 1 {
		t.Fatalf("first run: %+v, %d alerts; want one fire", ev, len(notifier.sent))
	}
	firedAt := now

	now = now.Add(time.Minute)
	r2 := start()
	flip(r2, 0) // baseline again: a new runner has no previous frame
	if ev := flip(r2, 255); ev.Fired {
		t.Fatalf("a change one minute into a ten-minute cooldown fired after the restart: %+v", ev)
	}
	if got := r2.eval.State().LastFired; !got.Equal(firedAt) {
		t.Errorf("fire time after the restart = %v, want %v", got, firedAt)
	}
	now = firedAt.Add(time.Duration(tenMin) + time.Second)
	if ev := flip(r2, 0); !ev.Fired || len(notifier.sent) != 2 {
		t.Errorf("a change after the cooldown: %+v, %d alerts; want it to fire", ev, len(notifier.sent))
	}
}

// Editing what the watch looks at or looks for is a new question: the
// watch starts fresh and fires on a condition that already holds. Editing
// anything else is not, and stays quiet.
func TestRestartAfterAnEdit(t *testing.T) {
	base := textWatch(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2, Cooldown: tenMin})
	cases := []struct {
		name  string
		edit  func(w *config.Watch)
		fresh bool
	}{
		{"pattern, still matching", func(w *config.Watch) { w.Trigger.Pattern = "(?i)complete" }, true},
		{"region", func(w *config.Watch) { w.Region.W = 0.5 }, true},
		{"preprocess", func(w *config.Watch) { w.Preprocess.Invert = true }, true},
		{"source", func(w *config.Watch) { w.Source = "http://other-camera.invalid/snap.jpg" }, true},
		{"notify URLs", func(w *config.Watch) { w.Notify = []string{"generic://example.invalid/other-topic"} }, false},
		{"interval, confirm and cooldown", func(w *config.Watch) {
			w.Interval = config.Duration(30 * time.Second)
			w.Trigger.Confirm = 3
			w.Trigger.Cooldown = config.Duration(time.Hour)
		}, false},
		{"nothing", func(w *config.Watch) {}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, openStore(t))
			r1 := g.start(base)
			if ev := g.show(r1, "PRINT COMPLETE", 2); !ev.Fired {
				t.Fatalf("first run: %+v, want a fire", ev)
			}
			edited := base
			c.edit(&edited)
			g.now = g.now.Add(time.Minute)
			r2 := g.start(edited)
			g.show(r2, "PRINT COMPLETE", 4)
			want := 1
			if c.fresh {
				want = 2
			}
			if g.sent() != want {
				t.Errorf("%d alerts after restarting with the edit, want %d: %v", g.sent(), want, g.notifier.sent)
			}
			if c.fresh && !g.logs.has("starts fresh") {
				t.Errorf("no log line says the watch started fresh: %q", g.logs.lines)
			}
		})
	}

	// An edit forgets the old state for good: undoing it later doesn't
	// bring back what the watch knew under the first pattern.
	g := newRig(t, openStore(t))
	g.show(g.start(base), "PRINT COMPLETE", 2)
	edited := base
	edited.Trigger.Pattern = "(?i)error"
	g.show(g.start(edited), "PRINT COMPLETE", 1) // no match, nothing settled yet
	if row, ok, err := g.store.LoadTriggerState(base.Name); err != nil || !ok || row.HasStable || !row.LastFired.IsZero() || row.Fingerprint != Fingerprint(edited) {
		t.Errorf("saved state after the edit = %+v (ok=%v err=%v), want an empty one under the new fingerprint", row, ok, err)
	}
	if ev := g.show(g.start(base), "PRINT COMPLETE", 2); !ev.Fired {
		t.Errorf("back on the first pattern: %+v, want a fresh start that fires", ev)
	}

	// `upscale: 1` is off, and Save writes it back as no preprocess at all:
	// the same question, so the Save stays quiet.
	g = newRig(t, openStore(t))
	one := base
	one.Preprocess = config.Preprocess{Grayscale: true, Upscale: 1}
	if ev := g.show(g.start(one), "PRINT COMPLETE", 2); !ev.Fired {
		t.Fatalf("first run with upscale 1: %+v, want a fire", ev)
	}
	saved := one
	saved.Preprocess.Upscale = 0
	g.now = g.now.Add(time.Minute)
	g.show(g.start(saved), "PRINT COMPLETE", 4)
	if g.sent() != 1 {
		t.Errorf("%d alerts after a Save turned upscale 1 into 0, want 1: %v", g.sent(), g.notifier.sent)
	}
}

// saveOld writes what a run that fired (and sent its alert) and then
// stopped `ago` ago leaves in the database: the state, and a last reading
// that old.
func saveOld(t *testing.T, store *history.Store, w config.Watch, ago time.Duration) time.Time {
	t.Helper()
	at := time.Now().Add(-ago)
	if err := store.Record(w.Name, at, "PRINT COMPLETE", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTriggerState(w.Name, history.TriggerState{
		Fingerprint: Fingerprint(w), LastFired: at, Stable: "cond:true", HasStable: true, Delivered: true}); err != nil {
		t.Fatal(err)
	}
	return at
}

// State is only trusted while it is recent: the watch must have read its
// region within the last 15 minutes, or twice its poll gap if that is
// longer. Older than that, a condition that holds when watchglass comes
// back is reported as new. The cooldown doesn't stretch that (the display
// may have been through a whole new event meanwhile); it still holds the
// new alert back until it ends, as it would have without the restart.
func TestRestartWithStaleState(t *testing.T) {
	match := config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2}
	hour := func(w *config.Watch) { w.Trigger.Cooldown = config.Duration(time.Hour) }
	const (
		quiet = "carries on" // the settled state is restored: nothing to report
		fires = "fires"      // fresh, and no cooldown is running
		held  = "held"       // fresh, and held until the restored cooldown ends
	)
	cases := []struct {
		name string
		edit func(w *config.Watch)
		ago  time.Duration
		want string
	}{
		{"5 minutes off", func(w *config.Watch) {}, 5 * time.Minute, quiet},
		{"20 minutes off", func(w *config.Watch) {}, 20 * time.Minute, fires},
		{"a day off", func(w *config.Watch) {}, 24 * time.Hour, fires},
		{"5 minutes off, cooldown 1h", hour, 5 * time.Minute, quiet},
		{"20 minutes off, cooldown 1h", hour, 20 * time.Minute, held},
		{"3 hours off, cooldown 6h", func(w *config.Watch) { w.Trigger.Cooldown = config.Duration(6 * time.Hour) }, 3 * time.Hour, held},
		{"2 hours off, cooldown 1h", hour, 2 * time.Hour, fires},
		{"20 minutes off, polls every 15 minutes", func(w *config.Watch) { w.Interval = config.Duration(15 * time.Minute) }, 20 * time.Minute, quiet},
		{"20 minutes off, backs off to 15 minutes", func(w *config.Watch) { w.MaxInterval = config.Duration(15 * time.Minute) }, 20 * time.Minute, quiet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := textWatch(match)
			c.edit(&w)
			g := newRig(t, openStore(t))
			firedAt := saveOld(t, g.store, w, c.ago)
			r := g.start(w)
			ev := g.show(r, "PRINT COMPLETE", 2)
			got := quiet
			switch {
			case ev.Fired:
				got = fires
			case ev.Pending == ev.Need && ev.Need > 0:
				got = held
			}
			if got != c.want {
				t.Fatalf("after the restart the watch %s (%+v), want it %s", got, ev, c.want)
			}
			if c.want == fires {
				return
			}
			if got, ok := r.RestoredFire(); !ok || !got.Equal(firedAt) {
				t.Errorf("RestoredFire = %v, %v; want %v, true", got, ok, firedAt)
			}
			if c.want == held {
				// It goes out when the cooldown that was running ends, not
				// before and not never.
				if end := firedAt.Add(time.Duration(w.Trigger.Cooldown)); !ev.CooldownEnds.Equal(end) {
					t.Errorf("held until %v, want the original cooldown end %v", ev.CooldownEnds, end)
				}
				g.now = ev.CooldownEnds.Add(time.Second)
				if ev := g.show(r, "PRINT COMPLETE", 1); !ev.Fired {
					t.Errorf("after the cooldown: %+v, want the held alert to fire", ev)
				}
			}
		})
	}

	// Stale state is forgotten when it is found, not merely skipped: a
	// second restart a moment later (when the watch HAS just polled) must
	// not bring it back. The fire time stays, for the cooldown.
	w := textWatch(match)
	g := newRig(t, openStore(t))
	firedAt := saveOld(t, g.store, w, 24*time.Hour)
	r := g.start(w)
	if !g.logs.has("counts as new") {
		t.Errorf("no log line explains the fresh start: %q", g.logs.lines)
	}
	row, ok, err := g.store.LoadTriggerState(w.Name)
	if err != nil || !ok || row.HasStable || !row.LastFired.Equal(firedAt) {
		t.Errorf("saved state once found stale = %+v (ok=%v err=%v), want the settled state gone and the fire time kept", row, ok, err)
	}
	g.show(r, "PRINTING 12%", 1) // one poll, nothing settled; then it restarts again
	if ev := g.show(g.start(w), "PRINT COMPLETE", 2); !ev.Fired {
		t.Errorf("second restart: %+v, want the fresh start to still be fresh", ev)
	}

	// State with no reading to date it by (the readings were pruned or
	// never written) is not trusted either.
	w2 := textWatch(match)
	g2 := newRig(t, openStore(t))
	if err := g2.store.SaveTriggerState(w2.Name, history.TriggerState{Fingerprint: Fingerprint(w2), Stable: "cond:true", HasStable: true}); err != nil {
		t.Fatal(err)
	}
	if ev := g2.show(g2.start(w2), "PRINT COMPLETE", 2); !ev.Fired {
		t.Errorf("state with no reading to date it: %+v, want a fresh start", ev)
	}
}

// failingNotifier refuses every alert, as a notify URL with a typo does.
type failingNotifier struct{ tries int }

func (f *failingNotifier) Send(ctx context.Context, title, body string) error {
	f.tries++
	return errors.New("generic+http://127.0.0.1:9 answered HTTP 404")
}

// An alert that never went out was not "already sent". The user sees it
// failed, fixes the notify URL and presses Save & restart watch: a
// condition that still holds fires once, to the new URL, without waiting
// out the cooldown of a fire nobody heard about. Once that one is sent the
// next restart is quiet again.
func TestRestartResendsAnAlertThatWasNotSent(t *testing.T) {
	g := newRig(t, openStore(t))
	w := textWatch(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2, Cooldown: tenMin})
	bad := &failingNotifier{}
	if ev := g.show(g.startWith(w, bad), "PRINT COMPLETE", 2); !ev.Fired || bad.tries != 1 {
		t.Fatalf("first run: %+v after %d sends; want a fire whose send fails", ev, bad.tries)
	}
	firedAt := g.now

	fixed := w
	fixed.Notify = []string{"generic://example.invalid/fixed"}
	g.now = g.now.Add(time.Minute)
	r2 := g.start(fixed)
	if !g.logs.has("may not have gone out") {
		t.Errorf("no log line says why the watch starts fresh: %q", g.logs.lines)
	}
	if got, ok := r2.RestoredFire(); !ok || !got.Equal(firedAt) {
		t.Errorf("RestoredFire = %v, %v; want %v: the fire happened, the watch list should still say so", got, ok, firedAt)
	}
	if ev := g.show(r2, "PRINT COMPLETE", 2); !ev.Fired || g.sent() != 1 {
		t.Fatalf("after fixing the notify URL: %+v, %d alerts at the new URL; want the alert sent once", ev, g.sent())
	}

	g.now = g.now.Add(time.Minute)
	g.show(g.start(fixed), "PRINT COMPLETE", 4)
	if g.sent() != 1 || bad.tries != 1 {
		t.Errorf("a restart after the alert went out sent it again: %d at the new URL, %d at the old", g.sent(), bad.tries)
	}
}

// A fire that was decided and saved but never sent, because watchglass
// stopped (or crashed) first, is saved as not delivered: the next start
// sends it.
func TestRestartSendsAFireThatStoppedBeforeItsSend(t *testing.T) {
	g := newRig(t, openStore(t))
	w := textWatch(config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Confirm: 2, Cooldown: tenMin})
	at := time.Now().Add(-30 * time.Second)
	if err := g.store.Record(w.Name, at, "25.3", true); err != nil {
		t.Fatal(err)
	}
	if err := g.store.SaveTriggerState(w.Name, history.TriggerState{
		Fingerprint: Fingerprint(w), LastFired: at, Stable: "cond:true", HasStable: true}); err != nil {
		t.Fatal(err)
	}
	if ev := g.show(g.start(w), "25.4", 2); !ev.Fired || g.sent() != 1 {
		t.Errorf("start after a fire that was never sent: %+v, %d alerts; want it sent now", ev, g.sent())
	}
}

// A watch with no notify URLs has nowhere to send an alert, so its fire is
// done, and a restart doesn't repeat it (Home Assistant would see it twice).
func TestRestartWithoutNotifyURLsDoesNotRepeat(t *testing.T) {
	g := newRig(t, openStore(t))
	w := textWatch(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2, Cooldown: tenMin})
	w.Notify = nil
	if ev := g.show(g.startWith(w, nil), "PRINT COMPLETE", 2); !ev.Fired {
		t.Fatalf("first run: %+v, want a fire", ev)
	}
	g.now = g.now.Add(time.Minute)
	for i := 0; i < 2; i++ {
		r := g.startWith(w, nil)
		for j := 0; j < 3; j++ {
			if ev := g.show(r, "PRINT COMPLETE", 1); ev.Fired || ev.Pending != 0 {
				t.Fatalf("restart %d with no notify URLs, reading %d: %+v, want nothing to report", i+1, j+1, ev)
			}
		}
	}
}

// A fire time saved while the clock was ahead would hold every alert back
// until that time plus the cooldown, on every restart. It is dropped.
func TestRestartDropsAFireTimeInTheFuture(t *testing.T) {
	g := newRig(t, openStore(t))
	w := textWatch(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2, Cooldown: tenMin})
	at := time.Now()
	if err := g.store.Record(w.Name, at, "PRINT COMPLETE", true); err != nil {
		t.Fatal(err)
	}
	if err := g.store.SaveTriggerState(w.Name, history.TriggerState{Fingerprint: Fingerprint(w),
		LastFired: at.Add(2 * time.Hour), Stable: "cond:true", HasStable: true, Delivered: true}); err != nil {
		t.Fatal(err)
	}
	r := g.start(w)
	if !g.logs.has("in the future") {
		t.Errorf("no log line about the future fire time: %q", g.logs.lines)
	}
	if ev := g.show(r, "PRINT COMPLETE", 2); ev.Fired {
		t.Fatalf("the settled state should still carry on: %+v", ev)
	}
	g.show(r, "PRINTING 5%", 2)
	if ev := g.show(r, "PRINT COMPLETE", 2); !ev.Fired {
		t.Errorf("next job: %+v, want a fire, not one held until the wrong clock's time", ev)
	}
}

// Without a history store nothing is remembered, as before, and nothing
// breaks.
func TestRestartWithoutAStoreStartsFresh(t *testing.T) {
	g := newRig(t, nil)
	w := textWatch(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2, Cooldown: tenMin})
	if ev := g.show(g.start(w), "PRINT COMPLETE", 2); !ev.Fired {
		t.Fatalf("first run: %+v, want a fire", ev)
	}
	r2 := g.start(w)
	if _, ok := r2.RestoredFire(); ok {
		t.Error("RestoredFire with no store should be false")
	}
	if ev := g.show(r2, "PRINT COMPLETE", 2); !ev.Fired || g.sent() != 2 {
		t.Errorf("second run with no store: %+v, %d alerts; want it to fire again, as it always has", ev, g.sent())
	}
	if len(g.logs.lines) != 0 {
		t.Errorf("a watch with no store logged: %q", g.logs.lines)
	}
}

// The state is written when it changes, not on every poll: after the store
// is closed, polls that change nothing don't try to write it (they would
// log the failure), and the next change does.
func TestStateIsWrittenOnlyWhenItChanges(t *testing.T) {
	store := openStore(t)
	g := newRig(t, store)
	w := textWatch(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2})
	r := g.start(w)
	g.show(r, "PRINT COMPLETE", 2)
	row, ok, err := store.LoadTriggerState(w.Name)
	if err != nil || !ok || !row.HasStable || row.Stable != "cond:true" || !row.LastFired.Equal(g.now) || row.Fingerprint != Fingerprint(w) {
		t.Fatalf("saved state after the fire = %+v (ok=%v err=%v)", row, ok, err)
	}
	store.Close()
	g.show(r, "PRINT COMPLETE", 5)
	if g.logs.has("save trigger state") {
		t.Fatalf("polls that changed nothing tried to write the state: %q", g.logs.lines)
	}
	g.show(r, "PRINTING 3%", 2) // the condition ends: a new settled state
	if !g.logs.has("save trigger state") {
		t.Errorf("a new settled state was not written (expected the closed store to refuse it): %q", g.logs.lines)
	}
}

func TestFingerprint(t *testing.T) {
	base := config.Watch{
		Name: "printer", Source: "http://127.0.0.1:8102/snapshot.jpg", Interval: config.Duration(2 * time.Second),
		Region:  config.Region{X: 0.0312, Y: 0.4167, W: 0.9375, H: 0.2222},
		Trigger: config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2, Cooldown: tenMin},
		Notify:  []string{"generic://example.invalid/hook"},
	}
	fp := Fingerprint(base)
	same := map[string]func(w *config.Watch){
		"name":         func(w *config.Watch) { w.Name = "other" },
		"interval":     func(w *config.Watch) { w.Interval = config.Duration(time.Minute) },
		"max_interval": func(w *config.Watch) { w.MaxInterval = config.Duration(time.Minute) },
		"health_after": func(w *config.Watch) { w.HealthAfter = 9 },
		"notify":       func(w *config.Watch) { w.Notify = nil },
		"headers":      func(w *config.Watch) { w.Headers = []string{"X-Key: 1"} },
		"tls_insecure": func(w *config.Watch) { w.TLSInsecure = true },
		"unit":         func(w *config.Watch) { w.Unit, w.DeviceClass = "°C", "temperature" },
		"confirm":      func(w *config.Watch) { w.Trigger.Confirm = 5 },
		"cooldown":     func(w *config.Watch) { w.Trigger.Cooldown = 0 },
		"engine spelled out": func(w *config.Watch) {
			w.Engine = "tesseract" // "" means tesseract
		},
		"camera login": func(w *config.Watch) { w.Source = "http://admin:hunter2@127.0.0.1:8102/snapshot.jpg" },
		"camera login in the query": func(w *config.Watch) {
			w.Source = "http://127.0.0.1:8102/snapshot.jpg?user=admin&password=hunter2"
		},
		"camera login in the query, other names": func(w *config.Watch) {
			w.Source = "http://127.0.0.1:8102/snapshot.jpg?usr=admin&PWD=hunter2"
		},
		// A token is the login too (source.LoginInURL): rotating it is a
		// new password, not a new camera.
		"camera token in the query": func(w *config.Watch) {
			w.Source = "http://127.0.0.1:8102/snapshot.jpg?token=abc123"
		},
		"camera api key in the query": func(w *config.Watch) {
			w.Source = "http://127.0.0.1:8102/snapshot.jpg?apikey=abc123&api_key=def"
		},
		// ocr_match never reads them.
		"op":        func(w *config.Watch) { w.Trigger.Op = "lt" },
		"threshold": func(w *config.Watch) { w.Trigger.Threshold = 3 },
		// The docs call upscale 0 or 1 off, and the web form writes 1 back
		// as 0, so a Save with nothing changed must not start it fresh.
		"upscale 1 is off": func(w *config.Watch) { w.Preprocess.Upscale = 1 },
	}
	for name, edit := range same {
		w := base
		edit(&w)
		if got := Fingerprint(w); got != fp {
			t.Errorf("changing %s changed the fingerprint; it is not part of the question", name)
		}
	}
	differs := map[string]func(w *config.Watch){
		"source":     func(w *config.Watch) { w.Source = "http://127.0.0.1:8103/snapshot.jpg" },
		"region":     func(w *config.Watch) { w.Region.X = 0.04 },
		"engine":     func(w *config.Watch) { w.Engine = "rapidocr" },
		"preprocess": func(w *config.Watch) { w.Preprocess.Threshold = 128 },
		"upscale":    func(w *config.Watch) { w.Preprocess.Upscale = 2 },
		"grayscale":  func(w *config.Watch) { w.Preprocess.Grayscale = true },
		"rotate":     func(w *config.Watch) { w.Preprocess.Rotate = 90 },
		"rotate 270": func(w *config.Watch) { w.Preprocess.Rotate = 270 },
		"type":       func(w *config.Watch) { w.Trigger.Type = "ocr_changed" },
		"pattern":    func(w *config.Watch) { w.Trigger.Pattern = "(?i)complete" },
		"numeric op": func(w *config.Watch) {
			w.Trigger = config.Trigger{Type: "numeric", Op: "lt", Threshold: 25}
		},
		"numeric threshold": func(w *config.Watch) {
			w.Trigger = config.Trigger{Type: "numeric", Op: "gt", Threshold: 26}
		},
		"numeric pattern": func(w *config.Watch) {
			w.Trigger = config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Pattern: `(\d+)C`}
		},
		"numeric": func(w *config.Watch) { // what the three above are compared with
			w.Trigger = config.Trigger{Type: "numeric", Op: "gt", Threshold: 25}
		},
		"pixel threshold": func(w *config.Watch) {
			w.Trigger = config.Trigger{Type: "pixel_change", Threshold: 7}
		},
	}
	seen := map[string]string{fp: "the base watch"}
	for name, edit := range differs {
		w := base
		edit(&w)
		got := Fingerprint(w)
		if other, dup := seen[got]; dup {
			t.Errorf("changing %s gave the same fingerprint as %s; it is part of the question", name, other)
		}
		seen[got] = name
	}

	// Fields a trigger type never reads, left behind by another type in the
	// web form, are not part of its question.
	for _, c := range []struct {
		name string
		tr   config.Trigger
		edit func(w *config.Watch)
	}{
		{"pixel_change engine", config.Trigger{Type: "pixel_change", Threshold: 5}, func(w *config.Watch) { w.Engine = "sevenseg" }},
		{"pixel_change preprocess", config.Trigger{Type: "pixel_change", Threshold: 5}, func(w *config.Watch) { w.Preprocess.Invert = true }},
		{"pixel_change rotate", config.Trigger{Type: "pixel_change", Threshold: 5}, func(w *config.Watch) { w.Preprocess.Rotate = 90 }},
		{"pixel_change pattern", config.Trigger{Type: "pixel_change", Threshold: 5}, func(w *config.Watch) { w.Trigger.Pattern = "x" }},
		{"ocr_changed pattern", config.Trigger{Type: "ocr_changed"}, func(w *config.Watch) { w.Trigger.Pattern = "x" }},
		{"ocr_changed threshold", config.Trigger{Type: "ocr_changed"}, func(w *config.Watch) { w.Trigger.Threshold = 9 }},
	} {
		w := base
		w.Trigger = c.tr
		before := Fingerprint(w)
		c.edit(&w)
		if Fingerprint(w) != before {
			t.Errorf("%s changed the fingerprint; that type never reads it", c.name)
		}
	}
	// Binarizing makes the image gray whether Grayscale is ticked or not
	// (imgproc.Apply), so ticking it next to a binarize level asks nothing new.
	w := base
	w.Preprocess = config.Preprocess{Threshold: 128}
	gray := w
	gray.Preprocess.Grayscale = true
	if Fingerprint(w) != Fingerprint(gray) {
		t.Error("Grayscale next to a binarize level changed the fingerprint; binarizing already grays the image")
	}

	// A turned crop is a different question for every type that turns it
	// (numeric too: the fingerprint is of what the decoder is shown).
	for _, typ := range []string{"ocr_match", "ocr_changed", "numeric"} {
		w := base
		w.Trigger.Type = typ
		if typ == "numeric" {
			w.Trigger = config.Trigger{Type: "numeric", Op: "gt", Threshold: 25}
		}
		turned := w
		turned.Preprocess.Rotate = 90
		other := w
		other.Preprocess.Rotate = 270
		if Fingerprint(w) == Fingerprint(turned) || Fingerprint(turned) == Fingerprint(other) {
			t.Errorf("%s: rotate 0, 90 and 270 must all give different fingerprints", typ)
		}
	}

	// The password is dropped, the rest of the address is not.
	w = base
	w.Source = "rtsp://admin:pw@10.0.0.5:554/stream1"
	other := base
	other.Source = "rtsp://admin:pw@10.0.0.6:554/stream1"
	if Fingerprint(w) == Fingerprint(other) {
		t.Error("two cameras with the same login got the same fingerprint")
	}
	// So is a login in the query; the rest of the query is not.
	w.Source = "http://10.0.0.5/cgi-bin/api.cgi?cmd=Snap&channel=0&user=admin&password=a"
	other.Source = "http://10.0.0.5/cgi-bin/api.cgi?cmd=Snap&channel=1&user=admin&password=a"
	if Fingerprint(w) == Fingerprint(other) {
		t.Error("two channels of a camera with the login in the query got the same fingerprint")
	}
	other.Source = "http://10.0.0.5/cgi-bin/api.cgi?cmd=Snap&channel=0&user=admin&password=b"
	if Fingerprint(w) != Fingerprint(other) {
		t.Error("a new password in the query changed the fingerprint")
	}

	// Pinned: this is what the first release with saved trigger state
	// wrote for this watch. If it changes (a new config.Watch field without
	// omitempty, a different YAML form), every installed watch loses its
	// saved state on upgrade and repeats its last alert once. Decide that
	// on purpose, then update the hash.
	const pinned = "dbaf2da4fdeee6d649532cc6eabd1868f48a9c1fad1c33feab23b6c6331114ee"
	if fp != pinned {
		t.Errorf("Fingerprint(base) = %s, want the pinned %s", fp, pinned)
	}
}

func TestRestoreWindow(t *testing.T) {
	d := func(x time.Duration) config.Duration { return config.Duration(x) }
	for _, c := range []struct {
		interval, maxInterval, cooldown config.Duration
		want                            time.Duration
	}{
		{d(2 * time.Second), 0, 0, 15 * time.Minute},
		{d(2 * time.Second), d(time.Minute), d(5 * time.Minute), 15 * time.Minute},
		{d(2 * time.Second), 0, d(6 * time.Hour), 15 * time.Minute}, // the cooldown doesn't count
		{d(10 * time.Minute), 0, 0, 20 * time.Minute},
		{d(time.Minute), d(30 * time.Minute), d(20 * time.Minute), time.Hour},
	} {
		w := config.Watch{Interval: c.interval, MaxInterval: c.maxInterval, Trigger: config.Trigger{Cooldown: c.cooldown}}
		if got := restoreWindow(w); got != c.want {
			t.Errorf("restoreWindow(interval %v, max %v, cooldown %v) = %v, want %v", c.interval, c.maxInterval, c.cooldown, got, c.want)
		}
	}
}
