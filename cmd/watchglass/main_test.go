package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/supervisor"
)

type fakeStartSource struct{}

func (fakeStartSource) Grab(ctx context.Context) (image.Image, error) {
	return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil
}

// logCollector is a concurrency-safe sink for the logf callback: each
// started watch's poll loop runs on its own goroutine and may log (e.g. a
// source error) concurrently with the test reading back what startWatches
// itself logged.
type logCollector struct {
	mu   sync.Mutex
	msgs []string
}

func (c *logCollector) logf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, fmt.Sprintf(format, args...))
}

func (c *logCollector) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

// TestStartWatchesSurvivesPerWatchFailure is Bug 1b: a boot-time
// sup.Start failure for one watch must not abort startWatches (and, by
// extension, must not exit the whole daemon per run()'s use of it) — the
// remaining watches must still start, and the web UI stays reachable as
// the recovery path for whatever's wrong with the failed watch's config.
//
// config.Validate (Bug 1a) now closes off the config-shaped ways Start can
// fail after Validate passed, so this test reaches a Start-only failure
// the way supervisor.Start itself defines one: starting the same watch
// name twice ("watch %q already running"), which needs no fixture beyond
// two config.Watch values sharing a Name — Validate is never consulted
// here since the watch slice is built directly, matching how a
// hand-edited config.yaml (or a future config-shaped Start failure this
// test doesn't anticipate) could still reach this loop.
func TestStartWatchesSurvivesPerWatchFailure(t *testing.T) {
	reg := state.New(5)
	sup := supervisor.New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	sup.NewSource = func(w config.Watch) (source.Source, error) { return fakeStartSource{}, nil }
	t.Cleanup(sup.StopAll)

	watchTemplate := config.Watch{
		Source:   "http://unused.invalid/snap.jpg",
		Interval: config.Duration(time.Minute),
		Region:   config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger:  config.Trigger{Type: "pixel_change", Threshold: 10},
	}
	a := watchTemplate
	a.Name = "a"
	dup := watchTemplate
	dup.Name = "a" // triggers "watch %q already running" at Start, not Validate
	b := watchTemplate
	b.Name = "b"
	watches := []config.Watch{a, dup, b}

	logs := &logCollector{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	startWatches(ctx, sup, watches, logs.logf)

	running := sup.Running()
	sort.Strings(running)
	if len(running) != 2 || running[0] != "a" || running[1] != "b" {
		t.Fatalf("running = %v, want [a b] (the duplicate's failure must not have stopped watch b from starting)", running)
	}

	found := false
	for _, m := range logs.snapshot() {
		if strings.Contains(m, "ERROR") && strings.Contains(m, `"a"`) &&
			strings.Contains(m, "failed to start") && strings.Contains(m, "already running") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an ERROR log naming the failed watch and its cause; got: %v", logs.snapshot())
	}
}

type stubEngine struct{}

func (stubEngine) Recognize(ctx context.Context, img image.Image) (string, error) { return "", nil }

// The boot check refuses a config whose OCR watches have no engine to run
// on — tesseract missing and the watch not on the built-in decoder — and
// accepts sevenseg watches and pixel_change watches regardless.
func TestCheckEngines(t *testing.T) {
	px := config.Watch{Name: "px", Trigger: config.Trigger{Type: "pixel_change", Threshold: 10}}
	lcd := config.Watch{Name: "lcd", Trigger: config.Trigger{Type: "ocr_match", Pattern: "x"}}
	scale := config.Watch{Name: "scale", Engine: "sevenseg", Trigger: config.Trigger{Type: "numeric", Op: "gt"}}
	explicit := config.Watch{Name: "explicit", Engine: "tesseract", Trigger: config.Trigger{Type: "ocr_changed"}}

	noTess := ocr.Engines{SevenSeg: ocr.NewSevenSeg()}
	if err := checkEngines([]config.Watch{px, scale}, noTess); err != nil {
		t.Errorf("pixel_change + sevenseg without tesseract: %v", err)
	}
	err := checkEngines([]config.Watch{px, scale, lcd}, noTess)
	if err == nil {
		t.Fatal("ocr_match without tesseract must be refused")
	}
	if !strings.Contains(err.Error(), `watch "lcd" needs OCR but tesseract wasn't found`) {
		t.Errorf("error = %q, want the watch named and the tesseract wording", err)
	}
	if err := checkEngines([]config.Watch{explicit}, noTess); err == nil {
		t.Error("engine: tesseract spelled out must still be refused without tesseract")
	}

	withTess := ocr.Engines{Tesseract: stubEngine{}, SevenSeg: ocr.NewSevenSeg()}
	if err := checkEngines([]config.Watch{px, scale, lcd, explicit}, withTess); err != nil {
		t.Errorf("with tesseract every watch is fine: %v", err)
	}
	if err := checkEngines(nil, ocr.Engines{}); err != nil {
		t.Errorf("no watches: %v", err)
	}

	// rapidocr watches need the detected Python engine the same way
	// tesseract watches need the binary; the error wraps ErrNoRapidOCR so
	// the message says how to install it.
	rapid := config.Watch{Name: "lcd-hard", Engine: "rapidocr", Trigger: config.Trigger{Type: "ocr_match", Pattern: "x"}}
	err = checkEngines([]config.Watch{px, scale, rapid}, noTess)
	if err == nil {
		t.Fatal("engine: rapidocr without a detected Python must be refused")
	}
	if !errors.Is(err, ocr.ErrNoRapidOCR) {
		t.Errorf("error = %v, want ErrNoRapidOCR wrapped", err)
	}
	if !strings.Contains(err.Error(), `watch "lcd-hard" needs the rapidocr engine`) {
		t.Errorf("error = %q, want the watch named and the rapidocr wording", err)
	}
	withRapid := ocr.Engines{RapidOCR: stubEngine{}, SevenSeg: ocr.NewSevenSeg()}
	if err := checkEngines([]config.Watch{px, scale, rapid}, withRapid); err != nil {
		t.Errorf("with rapidocr detected the watch is fine: %v", err)
	}
	if err := checkEngines([]config.Watch{lcd}, withRapid); err == nil {
		t.Error("rapidocr present must not stand in for a missing tesseract")
	}
}

// After a save, create or delete in the web UI, the trigger state of every
// watch that is no longer in the list is dropped, and the MQTT publisher
// (when there is one) is handed the same list.
func TestConfigChangedForgetsDeletedWatches(t *testing.T) {
	store, err := history.Open(filepath.Join(t.TempDir(), "wg.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, name := range []string{"kept", "deleted"} {
		if err := store.SaveTriggerState(name, history.TriggerState{Fingerprint: "f", Stable: "cond:true", HasStable: true}); err != nil {
			t.Fatal(err)
		}
	}
	sup := supervisor.New(store, state.New(5), ocr.Engines{}, func(string, ...any) {})
	list := []config.Watch{{Name: "kept"}, {Name: "new"}}

	var published []config.Watch
	configChanged(sup, func(ws []config.Watch) { published = ws })(list)
	if _, ok, _ := store.LoadTriggerState("deleted"); ok {
		t.Error("a deleted watch's trigger state is still saved")
	}
	if _, ok, _ := store.LoadTriggerState("kept"); !ok {
		t.Error("a watch still in the list lost its trigger state")
	}
	if len(published) != 2 {
		t.Errorf("the MQTT publisher was handed %d watches, want the 2 in the list", len(published))
	}

	// Without MQTT there is nobody to hand the list to.
	configChanged(sup, nil)(nil)
	if _, ok, _ := store.LoadTriggerState("kept"); ok {
		t.Error("an empty list should leave no trigger state")
	}
}

// At boot, a watch deleted or renamed in config.yaml while watchglass was
// off loses its saved trigger state, unless config.yaml was only just
// created empty (a wrong -config path next to the real -db), which must not
// wipe every watch's state.
func TestBootWatchesForgetsWatchesNoLongerConfigured(t *testing.T) {
	store, err := history.Open(filepath.Join(t.TempDir(), "wg.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, name := range []string{"kept", "gone"} {
		if err := store.SaveTriggerState(name, history.TriggerState{Fingerprint: "f", Stable: "cond:true", HasStable: true}); err != nil {
			t.Fatal(err)
		}
	}
	sup := supervisor.New(store, state.New(5), ocr.Engines{}, func(string, ...any) {})
	sup.NewSource = func(w config.Watch) (source.Source, error) { return fakeStartSource{}, nil }
	t.Cleanup(sup.StopAll)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logs := &logCollector{}

	bootWatches(ctx, sup, nil, true, logs.logf)
	for _, name := range []string{"kept", "gone"} {
		if _, ok, _ := store.LoadTriggerState(name); !ok {
			t.Errorf("booting on a just-created empty config dropped %q's trigger state", name)
		}
	}

	kept := config.Watch{
		Name: "kept", Source: "http://unused.invalid/snap.jpg", Interval: config.Duration(time.Minute),
		Region:  config.Region{W: 1, H: 1},
		Trigger: config.Trigger{Type: "pixel_change", Threshold: 10},
	}
	bootWatches(ctx, sup, []config.Watch{kept}, false, logs.logf)
	if _, ok, _ := store.LoadTriggerState("gone"); ok {
		t.Error("a watch no longer in config.yaml kept its trigger state over a boot")
	}
	if _, ok, _ := store.LoadTriggerState("kept"); !ok {
		t.Error("a configured watch lost its trigger state at boot")
	}
	if running := sup.Running(); len(running) != 1 || running[0] != "kept" {
		t.Errorf("running = %v, want [kept]", running)
	}
}
