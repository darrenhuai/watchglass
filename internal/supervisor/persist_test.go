package supervisor

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// waitUntil polls cond with room for a loaded machine.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A watch that has fired is restarted three ways while its display stays
// the same: Save & restart watch (same supervisor), watchglass itself
// restarting (a new supervisor and an empty registry on the same history
// database), and deleted then created again. Only the last is a new watch
// that fires again.
func TestRestartsDoNotRepeatAnAlert(t *testing.T) {
	store, err := history.Open(filepath.Join(t.TempDir(), "wg.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	img := fixtureImage(t) // a seven-segment display showing 23.5
	var fires, events atomic.Int32
	newSupervisor := func(reg *state.Registry) *Supervisor {
		s := New(store, reg, ocr.Engines{}, t.Logf)
		s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: img}, nil }
		s.OnEvent = func(watch string, ev trigger.Event, png []byte) {
			if ev.Fired {
				fires.Add(1)
			}
			events.Add(1)
		}
		return s
	}
	// morePolls waits until the watch has read the display n more times.
	morePolls := func(n int32) {
		t.Helper()
		from := events.Load()
		waitUntil(t, "more readings", func() bool { return events.Load() >= from+n })
	}
	w := testWatch("scale")
	w.Interval = config.Duration(20 * time.Millisecond)
	w.Engine = "sevenseg"
	w.Trigger = config.Trigger{Type: "numeric", Pattern: "([0-9.]+)", Op: "gt", Threshold: 20, Confirm: 2,
		Cooldown: config.Duration(10 * time.Minute)}
	ctx := context.Background()

	reg := state.New(5)
	s := newSupervisor(reg)
	if err := s.Start(ctx, w); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the first fire", func() bool { return fires.Load() == 1 })
	firedAt, ok := reg.LastFired("scale")
	if !ok {
		t.Fatal("the registry doesn't know the watch fired")
	}

	// Save & restart watch with nothing about the question changed.
	edited := w
	edited.HealthAfter = 5
	edited.Trigger.Cooldown = config.Duration(20 * time.Minute)
	if err := s.Restart(ctx, edited); err != nil {
		t.Fatal(err)
	}
	morePolls(6)
	if n := fires.Load(); n != 1 {
		t.Fatalf("%d fires after Save & restart watch, want still 1", n)
	}

	// watchglass stops and starts again: nothing in memory survives.
	s.StopAll()
	reg2 := state.New(5)
	s2 := newSupervisor(reg2)
	defer s2.StopAll()
	s2.ForgetExcept([]config.Watch{w}) // start-up: the watch is still in the config
	if err := s2.Start(ctx, w); err != nil {
		t.Fatal(err)
	}
	// Known at once, before the first poll: the watch list can say "fired
	// N ago" although this process never saw the fire.
	got, ok := reg2.LastFired("scale")
	if !ok {
		t.Fatal("after a restart the registry doesn't know the watch has fired")
	}
	if d := got.Sub(firedAt); d < -time.Second || d > time.Second {
		t.Errorf("fire time after the restart = %v, want the original %v", got, firedAt)
	}
	morePolls(6)
	if n := fires.Load(); n != 1 {
		t.Fatalf("%d fires after watchglass restarted, want still 1", n)
	}
	if latest, _ := reg2.Latest("scale"); latest.Fired || latest.Pending != 0 {
		t.Errorf("latest sample after the restart = %+v, want a plain reading", latest)
	}

	// Deleted, then created again under the same name: a new watch.
	s2.Stop("scale")
	s2.ForgetExcept(nil)
	if _, ok, err := store.LoadTriggerState("scale"); ok || err != nil {
		t.Fatalf("trigger state after the watch was deleted: ok=%v err=%v, want none", ok, err)
	}
	if err := s2.Start(ctx, w); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the re-created watch to fire", func() bool { return fires.Load() == 2 })
}

// Without a history store there is nothing to forget and nothing to seed.
func TestForgetExceptWithoutAStore(t *testing.T) {
	reg := state.New(5)
	s := newSup(reg)
	s.ForgetExcept(nil)
	if err := s.Start(context.Background(), testWatch("a")); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll()
	if _, ok := reg.LastFired("a"); ok {
		t.Error("a watch with no store and no fire has a last-fired time")
	}
}
