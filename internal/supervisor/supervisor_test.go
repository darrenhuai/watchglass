package supervisor

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

type fakeSource struct{ img image.Image }

func (f *fakeSource) Grab(ctx context.Context) (image.Image, error) { return f.img, nil }

// flakySource always errors on Grab — used to drive health.Tracker into its
// Down state deterministically, without a real unreachable network address.
type flakySource struct{}

func (flakySource) Grab(ctx context.Context) (image.Image, error) {
	return nil, errors.New("connection refused")
}

// countingSource errors until n successful Grabs remain to give, guarded by
// a mutex since Tick runs on the watch's own goroutine.
type countingSource struct {
	mu   sync.Mutex
	fail int
	img  image.Image
}

func (c *countingSource) Grab(ctx context.Context) (image.Image, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail > 0 {
		c.fail--
		return nil, errors.New("connection refused")
	}
	return c.img, nil
}

func testWatch(name string) config.Watch {
	return config.Watch{
		Name:     name,
		Source:   "http://unused.invalid/snap.jpg",
		Interval: config.Duration(time.Second),
		Region:   config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger:  config.Trigger{Type: "pixel_change", Threshold: 10},
	}
}

func flat() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 100, B: 100, A: 255})
		}
	}
	return img
}

func newSup(reg *state.Registry) *Supervisor {
	s := New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: flat()}, nil }
	return s
}

func waitForSample(t *testing.T, reg *state.Registry, watch string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Latest(watch); ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no sample for %q within deadline", watch)
}

func TestStartFeedsRegistryAndStopBlocks(t *testing.T) {
	reg := state.New(5)
	s := newSup(reg)
	if err := s.Start(context.Background(), testWatch("a")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Note: pixel watches baseline on the first tick (no sample); the second
	// tick arrives after ~1s interval.
	waitForSample(t, reg, "a")
	if got := s.Running(); len(got) != 1 || got[0] != "a" {
		t.Errorf("Running = %v", got)
	}
	s.Stop("a")
	if got := s.Running(); len(got) != 0 {
		t.Errorf("Running after Stop = %v", got)
	}
}

func TestStartDuplicateErrors(t *testing.T) {
	s := newSup(state.New(5))
	if err := s.Start(context.Background(), testWatch("a")); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll()
	if err := s.Start(context.Background(), testWatch("a")); err == nil {
		t.Error("duplicate Start should error")
	}
}

func TestRestartAndStopAll(t *testing.T) {
	reg := state.New(5)
	s := newSup(reg)
	if err := s.Start(context.Background(), testWatch("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Restart(context.Background(), testWatch("a")); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := s.Running(); len(got) != 1 {
		t.Errorf("Running after Restart = %v", got)
	}
	s.StopAll()
	if got := s.Running(); len(got) != 0 {
		t.Errorf("Running after StopAll = %v", got)
	}
}

func TestStartInvalidTriggerErrors(t *testing.T) {
	s := newSup(state.New(5))
	w := testWatch("bad")
	w.Trigger = config.Trigger{Type: "ocr_match", Pattern: "("}
	if err := s.Start(context.Background(), w); err == nil {
		t.Error("invalid trigger should error at Start")
	}
}

// TestExternalCancelRemovesFromRunning guards against Running() lying: when
// the parent ctx passed to Start is cancelled by something other than
// Stop/StopAll, the watch goroutine must still self-remove from the
// running set. It also checks that a subsequent Stop of the now-dead watch
// is a harmless, prompt no-op rather than blocking forever on a done
// channel nobody will ever close again.
func TestExternalCancelRemovesFromRunning(t *testing.T) {
	reg := state.New(5)
	s := newSup(reg)
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx, testWatch("a")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.Running()) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.Running(); len(got) != 0 {
		t.Fatalf("Running after external cancel = %v, want empty within deadline", got)
	}

	done := make(chan struct{})
	go func() {
		s.Stop("a")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Stop on an already-dead watch did not return promptly")
	}
}

func TestStartFailsOnBadSource(t *testing.T) {
	s := New(nil, state.New(5), ocr.Engines{}, func(string, ...any) {})
	// Default factory: an unsupported scheme must fail at Start.
	w := testWatch("bad")
	w.Source = "ftp://cam/x"
	if err := s.Start(context.Background(), w); err == nil {
		t.Error("expected Start to reject an unsupported source scheme")
	}
	if got := s.Running(); len(got) != 0 {
		t.Errorf("failed Start must not register a watch, got %v", got)
	}
}

// must_fix 1/4: OnHealth must mirror into the registry unconditionally
// (there is no OnHealth hook set on the Supervisor here at all — this is
// the "even with no MQTT publisher wired" case), so the web UI can derive
// its running/error/stopped status without any extra plumbing.
func TestHealthMirroredIntoRegistryOnFailure(t *testing.T) {
	reg := state.New(5)
	s := New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	s.NewSource = func(w config.Watch) (source.Source, error) { return flakySource{}, nil }
	w := testWatch("a")
	w.HealthAfter = 2
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h, ok := reg.GetHealth("a"); ok && h.Down {
			if h.Message == "" {
				t.Error("Down health event carries no message")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("registry never observed a Down health transition")
}

// A watch that recovers must flip the registry's verdict back to healthy —
// the Live panel's staleness badge (must_fix 4) and the dashboard/detail
// status (must_fix 1) both depend on this clearing automatically once the
// source comes back, with no user action required.
func TestHealthRecoversInRegistry(t *testing.T) {
	reg := state.New(5)
	s := New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	src := &countingSource{fail: 3, img: flat()}
	s.NewSource = func(w config.Watch) (source.Source, error) { return src, nil }
	w := testWatch("a")
	w.HealthAfter = 2
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h, ok := reg.GetHealth("a"); ok && h.Down {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h, ok := reg.GetHealth("a"); !ok || !h.Down {
		t.Fatalf("never observed Down before recovery: %+v ok=%v", h, ok)
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h, ok := reg.GetHealth("a"); ok && !h.Down {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("registry never observed recovery")
}

// A Down verdict left over from a previous run of the same-named watch is
// carried into the new runner and cleared by a REAL "healthy" transition on
// the first poll that produces a reading — not reset by Start itself. That
// transition is what every mirror hears: the registry (checked here), and
// the OnHealth hook, i.e. the MQTT publisher whose retained "offline" would
// otherwise stay wrong forever after a healthy Save & restart.
func TestStartHealsStaleDownThroughRealTransition(t *testing.T) {
	reg := state.New(5)
	s := New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	var mu sync.Mutex
	var hooked []string
	s.OnHealth = func(name string, hev health.Event) {
		mu.Lock()
		hooked = append(hooked, name+":"+hev.State)
		mu.Unlock()
	}
	reg.SetHealth("a", state.Health{Down: true, Message: "stale from a previous run"})
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: flat()}, nil }
	if err := s.Start(context.Background(), testWatch("a")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h, ok := reg.GetHealth("a"); ok && !h.Down {
			mu.Lock()
			defer mu.Unlock()
			if len(hooked) != 1 || hooked[0] != "a:healthy" {
				t.Errorf("OnHealth hook events = %v, want exactly [a:healthy]", hooked)
			}
			if h.Since.IsZero() {
				t.Error("healed verdict should carry the transition time")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stale Down verdict was never healed by the first successful poll")
}

// switchSource serves frames until dead is set, then errors forever.
type switchSource struct {
	dead atomic.Bool
	img  image.Image
}

func (s *switchSource) Grab(ctx context.Context) (image.Image, error) {
	if s.dead.Load() {
		return nil, errors.New("connection refused")
	}
	return s.img, nil
}

func waitHealth(t *testing.T, reg *state.Registry, name string, down bool) state.Health {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h, ok := reg.GetHealth(name); ok && h.Down == down {
			return h
		}
		time.Sleep(10 * time.Millisecond)
	}
	h, _ := reg.GetHealth(name)
	t.Fatalf("health never reached Down=%v: %+v", down, h)
	return h
}

// Restart (Save & restart) while the camera is still dead must not turn the
// watch green for health_after*interval, and "stale since" must not drift
// forward to the re-detection time while the last real frame hasn't moved.
func TestRestartKeepsDownVerdictWhileSourceStillDead(t *testing.T) {
	reg := state.New(5)
	s := New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	var mu sync.Mutex
	var hooked []health.Event
	s.OnHealth = func(name string, hev health.Event) {
		mu.Lock()
		hooked = append(hooked, hev)
		mu.Unlock()
	}
	src := &switchSource{img: flat()}
	s.NewSource = func(w config.Watch) (source.Source, error) { return src, nil }
	w := testWatch("cam")
	w.HealthAfter = 2
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll()
	waitForSample(t, reg, "cam")
	src.dead.Store(true)
	down := waitHealth(t, reg, "cam", true)

	if err := s.Restart(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if h, ok := reg.GetHealth("cam"); !ok || !h.Down {
		t.Fatalf("Restart wiped the Down verdict while the source is still dead: %+v", h)
	}
	// Give the restarted runner several failing polls (interval 1s, first
	// tick immediate) and confirm nothing about the verdict moved.
	time.Sleep(150 * time.Millisecond)
	if h, _ := reg.GetHealth("cam"); !h.Down || !h.Since.Equal(down.Since) || h.Message != down.Message {
		t.Errorf("verdict changed across Restart: before=%+v after=%+v", down, h)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hooked) != 1 || hooked[0].State != "down" {
		t.Errorf("OnHealth hook events = %+v, want exactly the one original down transition", hooked)
	}
}

// blockingSource blocks in Grab until ctx is cancelled — a snapshot in
// flight when Stop fires.
type blockingSource struct{}

func (blockingSource) Grab(ctx context.Context) (image.Image, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Stop cancelling a grab mid-flight must never manufacture a Down verdict
// (with health_after=1 it used to: "context canceled" hit the registry, the
// OnHealth hook — MQTT offline — and the notifier on every Save & restart).
func TestStopMidGrabDoesNotTripDown(t *testing.T) {
	reg := state.New(5)
	s := New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	var mu sync.Mutex
	var hooked []health.Event
	s.OnHealth = func(name string, hev health.Event) {
		mu.Lock()
		hooked = append(hooked, hev)
		mu.Unlock()
	}
	s.NewSource = func(w config.Watch) (source.Source, error) { return blockingSource{}, nil }
	w := testWatch("a")
	w.HealthAfter = 1
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	s.Stop("a")
	mu.Lock()
	defer mu.Unlock()
	if h, ok := reg.GetHealth("a"); ok && h.Down {
		t.Errorf("Stop tripped a Down verdict: %+v", h)
	}
	if len(hooked) != 0 {
		t.Errorf("Stop fired health events: %+v", hooked)
	}
}

// errEngine models an OCR engine that fails every call.
type errEngine struct{}

func (errEngine) Recognize(ctx context.Context, img image.Image) (string, error) {
	return "", errors.New("tesseract: exit status 1")
}

// Grab succeeding but OCR failing on every tick used to leave the registry
// with neither a sample nor a verdict — "running" / "no data yet" forever.
func TestOCRFailureSurfacesAsDownInRegistry(t *testing.T) {
	reg := state.New(5)
	s := New(nil, reg, ocr.Engines{Tesseract: errEngine{}}, func(string, ...any) {})
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: flat()}, nil }
	w := testWatch("a")
	w.Interval = config.Duration(30 * time.Millisecond)
	w.HealthAfter = 2
	w.Trigger = config.Trigger{Type: "ocr_match", Pattern: "x", Confirm: 1}
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll()
	h := waitHealth(t, reg, "a", true)
	if !strings.Contains(h.Message, "ocr: tesseract") {
		t.Errorf("Down message should quote the OCR error, got %q", h.Message)
	}
}

func TestSupervisorThreadsEventHook(t *testing.T) {
	reg := state.New(5)
	s := newSup(reg)
	type got struct {
		watch string
		png   bool
	}
	ch := make(chan got, 10)
	s.OnEvent = func(watch string, ev trigger.Event, png []byte) {
		ch <- got{watch: watch, png: len(png) > 0}
	}
	if err := s.Start(context.Background(), testWatch("a")); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll()
	select {
	case g := <-ch:
		if g.watch != "a" || !g.png {
			t.Errorf("event hook got %+v, want watch a with png", g)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event hook never fired")
	}
}

func fixtureImage(t *testing.T) image.Image {
	t.Helper()
	f, err := os.Open("../ocr/testdata/sevenseg-23.5.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// A sevenseg watch needs no tesseract: with an Engines that has none, a
// numeric watch on a seven-segment display starts, reads the digits and
// feeds the registry.
func TestStartSevenSegWatchWithoutTesseract(t *testing.T) {
	reg := state.New(5)
	s := New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: fixtureImage(t)}, nil }
	w := testWatch("scale")
	w.Interval = config.Duration(30 * time.Millisecond)
	w.Engine = "sevenseg"
	w.Trigger = config.Trigger{Type: "numeric", Pattern: "([0-9.]+)", Op: "gt", Threshold: 25, Confirm: 1}
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()
	waitForSample(t, reg, "scale")
	latest, _ := reg.Latest("scale")
	if latest.Reading != "23.5" {
		t.Errorf("reading = %q, want 23.5", latest.Reading)
	}
}

// The default engine is still tesseract, and its absence is still a Start
// error for OCR watches — with the wording main has always printed.
func TestStartOCRWatchWithoutTesseractErrors(t *testing.T) {
	s := newSup(state.New(5))
	w := testWatch("lcd")
	w.Trigger = config.Trigger{Type: "ocr_match", Pattern: "x"}
	err := s.Start(context.Background(), w)
	if err == nil {
		t.Fatal("expected an error without tesseract")
	}
	if !strings.Contains(err.Error(), `watch "lcd"`) || !strings.Contains(err.Error(), "tesseract is not on PATH") {
		t.Errorf("error = %q", err)
	}
	if len(s.Running()) != 0 {
		t.Errorf("running = %v", s.Running())
	}
}

// pixel_change never resolves an engine, so a bad engine name (which
// Validate would have rejected anyway) cannot stop it from starting.
func TestStartPixelChangeIgnoresEngine(t *testing.T) {
	s := newSup(state.New(5))
	w := testWatch("px")
	w.Engine = "sevenseg"
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.StopAll()
}

// flipSource alternates black and white frames: a pixel_change watch fires
// on every poll after the first.
type flipSource struct {
	mu sync.Mutex
	n  int
}

func (f *flipSource) Grab(ctx context.Context) (image.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	v := uint8(255 * (f.n % 2))
	for i := range img.Pix {
		img.Pix[i] = v
	}
	return img, nil
}

// A05: a fire's delivery outcome lands in the registry under the same time
// as the fired reading, so the Live panel can say which fire it was about.
func TestDeliveryIsMirroredIntoTheRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer srv.Close()
	reg := state.New(10)
	s := New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	s.NewSource = func(w config.Watch) (source.Source, error) { return &flipSource{}, nil }
	w := testWatch("a")
	w.Interval = config.Duration(50 * time.Millisecond)
	w.Notify = []string{"ntfy+" + srv.URL + "/secret-topic"}
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if d, ok := reg.GetDelivery("a"); ok {
			if d.OK || d.Kind != "fired" || !strings.Contains(d.Err, "status 404") || strings.Contains(d.Err, "secret-topic") {
				t.Fatalf("delivery = %+v", d)
			}
			fired := false
			for _, smp := range reg.Recent("a") {
				fired = fired || (smp.Fired && smp.TS.Equal(d.TS))
			}
			if last, _ := reg.LastFired("a"); !fired && last.Before(d.TS) {
				t.Errorf("no fired sample at the delivery's time %v", d.TS)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no delivery recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An alert in flight when a Save & restart fails must not leave the
// watch saying "Sending the alert from …" for as long as it stays
// stopped: the old run's final report is dropped (it's no longer the
// current run), so the mark has to go when that run is stopped.
func TestFailedRestartLeavesNoSendingMark(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	reg := state.New(10)
	s := New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	s.NewSource = func(w config.Watch) (source.Source, error) { return &flipSource{}, nil }
	w := testWatch("a")
	w.Interval = config.Duration(50 * time.Millisecond)
	w.Notify = []string{"ntfy+" + srv.URL + "/topic"}
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll()
	waitSending(t, reg)
	s.NewSource = func(w config.Watch) (source.Source, error) { return nil, errors.New("camera gone") }
	if err := s.Restart(context.Background(), w); err == nil {
		t.Fatal("restart with a broken source succeeded")
	}
	if ts, ok := reg.Sending("a"); ok {
		t.Fatalf("a stopped watch still has an alert in flight from %v", ts)
	}
	// A plain Stop clears it (with no Start after it to hide a miss)...
	s.NewSource = func(w config.Watch) (source.Source, error) { return &flipSource{}, nil }
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	waitSending(t, reg)
	s.Stop("a")
	if _, ok := reg.Sending("a"); ok {
		t.Fatal("Stop left the run's sending mark behind")
	}
	// ...and a Start drops one left from before it.
	reg.SetSending("a", time.Now())
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Sending("a"); ok {
		t.Fatal("Start kept a sending mark from before it")
	}
}

func waitSending(t *testing.T, reg *state.Registry) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := reg.Sending("a"); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no alert went in flight")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
