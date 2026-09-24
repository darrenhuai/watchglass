package runner

import (
	"context"
	"errors"
	"image"
	"image/color"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// --- fakes ---

type fakeSource struct {
	imgs []image.Image
	i    int
}

func (f *fakeSource) Grab(ctx context.Context) (image.Image, error) {
	img := f.imgs[f.i%len(f.imgs)]
	f.i++
	return img, nil
}

type fakeOCR struct {
	texts []string
	i     int
}

func (f *fakeOCR) Recognize(ctx context.Context, img image.Image) (string, error) {
	t := f.texts[f.i%len(f.texts)]
	f.i++
	return t, nil
}

type fakeNotifier struct{ sent []string }

func (f *fakeNotifier) Send(ctx context.Context, title, body string) error {
	f.sent = append(f.sent, title+" | "+body)
	return nil
}

func flat(w, h int, v uint8) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return img
}

func watchCfg(triggerCfg config.Trigger) config.Watch {
	return config.Watch{
		Name:    "test-watch",
		Source:  "fake",
		Region:  config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger: triggerCfg,
	}
}

func TestOCRWatchFiresAndNotifies(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	ocrEngine := &fakeOCR{texts: []string{"Printing 87%", "PRINT COMPLETE"}}
	notifier := &fakeNotifier{}
	r, err := New(
		watchCfg(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 1}),
		src, ocrEngine, notifier, nil, t.Logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("no fire expected yet, got %v", notifier.sent)
	}
	if _, err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 notification, got %v", notifier.sent)
	}
	// A14: the body names the watch, so a plain webhook (which gets only
	// the body) still says which one fired.
	if want := "watchglass: test-watch | test-watch: pattern matched — PRINT COMPLETE"; notifier.sent[0] != want {
		t.Errorf("notification = %q, want %q", notifier.sent[0], want)
	}
}

func TestPixelWatchBaselinesThenFires(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{
		flat(10, 10, 0),   // tick 1: baseline, never fires
		flat(10, 10, 0),   // tick 2: unchanged
		flat(10, 10, 255), // tick 3: full change -> fire
	}}
	notifier := &fakeNotifier{}
	r, err := New(
		watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10}),
		src, nil, notifier, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := r.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected exactly 1 notification, got %d: %v", len(notifier.sent), notifier.sent)
	}
}

func TestRunToleratesZeroIntervalAndCancels(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 0)}}
	r, err := New(
		watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10}),
		src, nil, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	// watchCfg leaves Interval at its zero value; Run must default it
	// instead of panicking in time.NewTicker.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

func TestOnReadingHookObservesTicks(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	ocrEngine := &fakeOCR{texts: []string{"A", "B"}}
	r, err := New(
		watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1}),
		src, ocrEngine, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	type call struct {
		reading string
		fired   bool
	}
	var calls []call
	r.OnReading = func(ev trigger.Event, crop image.Image, at time.Time) {
		if crop == nil {
			t.Error("hook received nil crop")
		}
		calls = append(calls, call{ev.Reading, ev.Fired})
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	want := []call{{"A", false}, {"B", true}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

func TestPreprocessAppliedBeforeOCR(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 200)}}
	var seen image.Image
	capture := &captureOCR{onImg: func(img image.Image) { seen = img }}
	w := watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1})
	w.Preprocess = config.Preprocess{Invert: true}
	r, err := New(w, src, capture, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen == nil {
		t.Fatal("OCR engine never called")
	}
	rr, _, _, _ := seen.At(0, 0).RGBA()
	if got := uint8(rr >> 8); got != 55 {
		t.Errorf("OCR saw pixel %d, want 55 (inverted 200)", got)
	}
}

type captureOCR struct{ onImg func(image.Image) }

func (c *captureOCR) Recognize(ctx context.Context, img image.Image) (string, error) {
	c.onImg(img)
	return "x", nil
}

func TestTickOrdersStoreThenHookThenNotify(t *testing.T) {
	// Create a real store
	store, err := history.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Set up sources and engine
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	ocrEngine := &fakeOCR{texts: []string{"GO"}}
	notifier := &fakeNotifier{}

	// Create runner with watch that fires on first tick
	r, err := New(
		watchCfg(config.Trigger{Type: "ocr_match", Pattern: "GO", Confirm: 1}),
		src, ocrEngine, notifier, store, t.Logf)
	if err != nil {
		t.Fatal(err)
	}

	var hookRan bool
	const watchName = "test-watch"
	r.OnReading = func(ev trigger.Event, crop image.Image, at time.Time) {
		// Assert: store already holds exactly 1 reading (record-before-hook)
		readings, err := store.LastN(watchName, 5)
		if err != nil {
			t.Errorf("hook: store.LastN failed: %v", err)
			return
		}
		if len(readings) != 1 {
			t.Errorf("hook: store has %d readings, want 1", len(readings))
			return
		}

		// Assert: notifier sent is still empty (hook-before-notify)
		if len(notifier.sent) != 0 {
			t.Errorf("hook: notifier already sent %d messages, want 0", len(notifier.sent))
			return
		}

		hookRan = true
	}

	ctx := context.Background()
	if _, err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// After tick: assert hook ran once and notifier sent 1 notification
	if !hookRan {
		t.Error("hook did not run")
	}
	if len(notifier.sent) != 1 {
		t.Errorf("notifier sent %d messages, want 1", len(notifier.sent))
	}
}

func TestNextIntervalDisabledWithoutMax(t *testing.T) {
	base := 5 * time.Second
	for _, max := range []time.Duration{0, base, -time.Second} {
		if got := NextInterval(base, max, base, false); got != base {
			t.Errorf("max=%v: got %v, want %v (adaptive off)", max, got, base)
		}
	}
}

func TestNextIntervalBacksOffAndCaps(t *testing.T) {
	base, max := 5*time.Second, 20*time.Second
	got := NextInterval(base, max, base, false)
	if got != 10*time.Second {
		t.Errorf("first backoff = %v, want 10s", got)
	}
	got = NextInterval(base, max, got, false)
	if got != 20*time.Second {
		t.Errorf("second backoff = %v, want 20s", got)
	}
	got = NextInterval(base, max, got, false)
	if got != 20*time.Second {
		t.Errorf("capped backoff = %v, want 20s", got)
	}
}

func TestNextIntervalResetsOnChange(t *testing.T) {
	base, max := 5*time.Second, 60*time.Second
	if got := NextInterval(base, max, 40*time.Second, true); got != base {
		t.Errorf("changed: got %v, want %v", got, base)
	}
}

func TestNextIntervalFloorsAtBase(t *testing.T) {
	base, max := 5*time.Second, 60*time.Second
	if got := NextInterval(base, max, time.Second, false); got != 10*time.Second {
		t.Errorf("current below base: got %v, want 10s", got)
	}
}

func TestTickChanged(t *testing.T) {
	cases := []struct {
		name        string
		triggerType string
		ev          trigger.Event
		err         error
		lastReading string
		want        bool
	}{
		{
			name:        "pixel with grab error resets",
			triggerType: "pixel_change",
			ev:          trigger.Event{},
			err:         errors.New("grab failed"),
			lastReading: "",
			want:        true,
		},
		{
			name:        "pixel with no change does not reset",
			triggerType: "pixel_change",
			ev:          trigger.Event{},
			err:         nil,
			lastReading: "",
			want:        false,
		},
		{
			name:        "pixel fired resets",
			triggerType: "pixel_change",
			ev:          trigger.Event{Fired: true},
			err:         nil,
			lastReading: "",
			want:        true,
		},
		{
			name:        "ocr reading changed resets",
			triggerType: "ocr_changed",
			ev:          trigger.Event{Reading: "B"},
			err:         nil,
			lastReading: "A",
			want:        true,
		},
		{
			name:        "ocr same reading does not reset",
			triggerType: "ocr_changed",
			ev:          trigger.Event{Reading: "A"},
			err:         nil,
			lastReading: "A",
			want:        false,
		},
		{
			name:        "ocr error resets regardless of reading",
			triggerType: "ocr_changed",
			ev:          trigger.Event{Reading: ""},
			err:         errors.New("ocr failed"),
			lastReading: "A",
			want:        true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tickChanged(tc.triggerType, tc.ev, tc.err, tc.lastReading); got != tc.want {
				t.Errorf("tickChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHealthNotifiesOnDownAndRecovery(t *testing.T) {
	failing := &flakySource{fail: true}
	notifier := &fakeNotifier{}
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.HealthAfter = 2
	r, err := New(w, failing, nil, notifier, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := r.Tick(ctx); err == nil {
		t.Fatal("expected grab error")
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("one failure must not notify, got %v", notifier.sent)
	}
	if _, err := r.Tick(ctx); err == nil {
		t.Fatal("expected grab error")
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("threshold reached: want 1 notification, got %v", notifier.sent)
	}
	if !strings.Contains(notifier.sent[0], "watchglass: test-watch (down) | test-watch: no reading for 2 consecutive polls") || !strings.Contains(notifier.sent[0], "connection refused") {
		t.Errorf("down notification = %q", notifier.sent[0])
	}
	if _, err := r.Tick(ctx); err == nil {
		t.Fatal("expected grab error")
	}
	if len(notifier.sent) != 1 {
		t.Errorf("already-down must not re-notify, got %v", notifier.sent)
	}
	failing.fail = false
	if _, err := r.Tick(ctx); err != nil {
		t.Fatalf("recovered grab: %v", err)
	}
	if len(notifier.sent) != 2 {
		t.Fatalf("recovery should notify, got %v", notifier.sent)
	}
	if notifier.sent[1] != "watchglass: test-watch (healthy) | test-watch: stream recovered" {
		t.Errorf("recovery notification = %q", notifier.sent[1])
	}
}

func TestTickReturnsEvaluatedEvent(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	engine := &fakeOCR{texts: []string{"Printing", "PRINT COMPLETE"}}
	r, err := New(
		watchCfg(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 1}),
		src, engine, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := r.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Reading != "Printing" || ev.Fired {
		t.Errorf("first tick event = %+v", ev)
	}
	ev, err = r.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ev.Fired || ev.Reading != "PRINT COMPLETE" {
		t.Errorf("second tick event = %+v, want fired", ev)
	}
}

// flakySource fails on demand so health transitions can be driven.
type flakySource struct {
	fail bool
}

func (f *flakySource) Grab(ctx context.Context) (image.Image, error) {
	if f.fail {
		return nil, errors.New("connection refused")
	}
	return flat(10, 10, 128), nil
}

type imageNotifier struct {
	fakeNotifier
	images int
}

func (n *imageNotifier) SendImage(ctx context.Context, title, body string, png []byte) error {
	if len(png) == 0 {
		return errors.New("empty png")
	}
	n.images++
	return nil
}

func TestFiredEventUsesImageSender(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	engine := &fakeOCR{texts: []string{"PRINT COMPLETE"}}
	n := &imageNotifier{}
	r, err := New(
		watchCfg(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 1}),
		src, engine, n, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n.images != 1 {
		t.Errorf("SendImage calls = %d, want 1", n.images)
	}
	if len(n.sent) != 0 {
		t.Errorf("plain Send should not be used when ImageSender available, got %v", n.sent)
	}
}

// blockingSource models a grab in flight when Stop fires: it returns
// ctx.Err() the moment ctx is cancelled.
type blockingSource struct{}

func (blockingSource) Grab(ctx context.Context) (image.Image, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Stop cancelling a grab mid-flight is not a verdict on the stream: with
// health_after=1 it used to trip a Down transition ("context canceled") on
// the way out, fanning out to the notifier, the registry, and the MQTT
// health topic.
func TestCancelledPollIsNotAHealthFailure(t *testing.T) {
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.HealthAfter = 1
	notifier := &fakeNotifier{}
	r, err := New(w, blockingSource{}, nil, notifier, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var events []health.Event
	r.OnHealth = func(hev health.Event) { events = append(events, hev) }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Tick(ctx); err == nil {
		t.Fatal("cancelled grab should still return an error to the caller")
	}
	if len(events) != 0 || len(notifier.sent) != 0 {
		t.Errorf("cancelled grab tripped health: events=%+v sent=%v", events, notifier.sent)
	}
}

type failingOCR struct{ fail bool }

func (f *failingOCR) Recognize(ctx context.Context, img image.Image) (string, error) {
	if f.fail {
		return "", errors.New("tesseract: exit status 1")
	}
	return "PRINT COMPLETE", nil
}

// A frame that arrives but can't be read is a poll with no reading: it must
// count toward the health threshold like a failed grab, so an engine failing
// on every tick surfaces as down (and recovers) instead of reading as a
// healthy watch that never produces data.
func TestOCRFailureCountsTowardHealth(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	engine := &failingOCR{fail: true}
	w := watchCfg(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 1})
	w.HealthAfter = 2
	r, err := New(w, src, engine, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var events []health.Event
	r.OnHealth = func(hev health.Event) { events = append(events, hev) }
	ctx := context.Background()
	r.Tick(ctx)
	if len(events) != 0 {
		t.Fatalf("one OCR failure must not transition, got %+v", events)
	}
	r.Tick(ctx)
	if len(events) != 1 || events[0].State != "down" || !strings.Contains(events[0].Message, "ocr: tesseract") {
		t.Fatalf("second OCR failure should transition to down quoting the OCR error, got %+v", events)
	}
	r.Tick(ctx)
	if len(events) != 1 {
		t.Fatalf("already-down must not re-report, got %+v", events)
	}
	engine.fail = false
	if _, err := r.Tick(ctx); err != nil {
		t.Fatalf("recovered tick: %v", err)
	}
	if len(events) != 2 || events[1].State != "healthy" {
		t.Errorf("first readable frame should transition back to healthy, got %+v", events)
	}
}

// A runner seeded down (restart of a watch that was down) reports exactly
// one "healthy" — on the first poll that produces a reading — and nothing
// while the source is still failing.
func TestSeedDownReportsRecoveryOnFirstReading(t *testing.T) {
	failing := &flakySource{fail: true}
	notifier := &fakeNotifier{}
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.HealthAfter = 2
	r, err := New(w, failing, nil, notifier, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	r.SeedDown()
	var events []health.Event
	r.OnHealth = func(hev health.Event) { events = append(events, hev) }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		r.Tick(ctx)
	}
	if len(events) != 0 || len(notifier.sent) != 0 {
		t.Fatalf("still-dead source after SeedDown must stay silent, got events=%+v sent=%v", events, notifier.sent)
	}
	failing.fail = false
	r.Tick(ctx)
	if len(events) != 1 || events[0].State != "healthy" {
		t.Fatalf("first reading after SeedDown should emit healthy, got %+v", events)
	}
	if len(notifier.sent) != 1 || !strings.Contains(notifier.sent[0], "recovered") {
		t.Errorf("recovery should notify once, got %v", notifier.sent)
	}
}

func TestOnHealthHookFires(t *testing.T) {
	failing := &flakySource{fail: true}
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.HealthAfter = 1
	r, err := New(w, failing, nil, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var events []health.Event
	r.OnHealth = func(hev health.Event) { events = append(events, hev) }
	ctx := context.Background()
	r.Tick(ctx) // failure 1 -> down
	failing.fail = false
	r.Tick(ctx) // success -> healthy
	if len(events) != 2 || events[0].State != "down" || events[1].State != "healthy" {
		t.Errorf("health hook events = %+v", events)
	}
}

// errNotifier fails every send with err while err is set.
type errNotifier struct {
	fakeNotifier
	err error
}

func (n *errNotifier) Send(ctx context.Context, title, body string) error {
	if n.err != nil {
		return n.err
	}
	return n.fakeNotifier.Send(ctx, title, body)
}

// A05: every alert's outcome is reported, a failure with its credentials
// scrubbed out, and the fire's report carries the time its reading got.
func TestDeliveryIsReportedPerAlert(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	engine := &fakeOCR{texts: []string{"PRINT COMPLETE", "idle"}}
	const token = "Xa1b2C3d4E5f6G7h8I9j0KlMnOpQr"
	w := watchCfg(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 1})
	w.Notify = []string{"discord://" + token + "@123456789"}
	n := &errNotifier{err: errors.New(`line 1 of 1 (discord://123456789): Post "https://discord.com/api/webhooks/123456789/` + token + `": 401 Unauthorized` + strings.Repeat(" padding", 40))}
	r, err := New(w, src, engine, n, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var readAt time.Time
	r.OnReading = func(ev trigger.Event, crop image.Image, at time.Time) {
		if ev.Fired {
			readAt = at
		}
	}
	var got []Delivery
	r.OnDelivery = func(d Delivery) { got = append(got, d) }
	ctx := context.Background()
	if _, err := r.Tick(ctx); err != nil {
		t.Fatalf("a failed send is reported, not returned: %v", err)
	}
	if len(got) != 1 || got[0].OK || got[0].Kind != "fired" || !got[0].TS.Equal(readAt) || readAt.IsZero() {
		t.Fatalf("reports = %+v (reading at %v)", got, readAt)
	}
	if strings.Contains(got[0].Err, token) || !strings.Contains(got[0].Err, "401 Unauthorized") {
		t.Errorf("Err = %q, want the cause without the token", got[0].Err)
	}
	if n := len([]rune(got[0].Err)); n > errMax {
		t.Errorf("Err is %d runes, want at most %d", n, errMax)
	}
	// It stops firing (idle), fires again, and this time it goes through.
	n.err = nil
	r.Tick(ctx)
	engine.texts = []string{"PRINT COMPLETE"}
	r.Tick(ctx)
	if len(got) != 2 || !got[1].OK || got[1].Err != "" {
		t.Errorf("after the fix: reports = %+v", got)
	}
}

// With no notify URLs a fire is reported as skipped, and nothing is sent.
func TestDeliveryWithoutNotifierIsSkipped(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	r, err := New(watchCfg(config.Trigger{Type: "ocr_match", Pattern: "GO", Confirm: 1}),
		src, &fakeOCR{texts: []string{"GO"}}, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var got []Delivery
	r.OnDelivery = func(d Delivery) { got = append(got, d) }
	r.Tick(context.Background())
	if len(got) != 1 || !got[0].Skipped || got[0].OK || got[0].Err != "" {
		t.Errorf("reports = %+v, want one skipped", got)
	}
}

// Health alerts are reported too, as "down" and "recovered".
func TestHealthAlertsAreReported(t *testing.T) {
	failing := &flakySource{fail: true}
	n := &errNotifier{err: errors.New("status 500")}
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.HealthAfter = 1
	r, err := New(w, failing, nil, n, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var got []Delivery
	r.OnDelivery = func(d Delivery) { got = append(got, d) }
	r.Tick(context.Background())
	n.err = nil
	failing.fail = false
	r.Tick(context.Background())
	if len(got) != 2 || got[0].Kind != "down" || got[0].OK || got[0].Err != "status 500" || got[1].Kind != "recovered" || !got[1].OK {
		t.Errorf("reports = %+v", got)
	}
}

// blockingNotifier holds every send until release is closed.
type blockingNotifier struct {
	release chan struct{}
	calls   chan string
}

func (b *blockingNotifier) Send(ctx context.Context, title, body string) error {
	select {
	case b.calls <- body:
	default:
	}
	<-b.release
	return nil
}

// A notification service that hangs never holds up polling: under Run the
// send happens on its own goroutine, the poll loop keeps its interval, and
// the report goes Pending first, then OK once the send returns.
func TestSlowNotifierDoesNotDelayPolling(t *testing.T) {
	src := &countingSource{}
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10, Confirm: 1})
	w.Interval = config.Duration(20 * time.Millisecond)
	n := &blockingNotifier{release: make(chan struct{}), calls: make(chan string, 64)}
	r, err := New(w, src, nil, n, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	reports := make(chan Delivery, 256)
	r.OnDelivery = func(d Delivery) { reports <- d }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	select {
	case <-n.calls:
	case <-time.After(3 * time.Second):
		t.Fatal("no alert was ever sent")
	}
	before := src.grabs()
	time.Sleep(300 * time.Millisecond) // the first send is still hanging
	if after := src.grabs(); after-before < 5 {
		t.Errorf("polling stalled behind a hanging send: %d grabs in 300ms", after-before)
	}
	select {
	case first := <-reports:
		if !first.Pending {
			t.Errorf("first report = %+v, want Pending", first)
		}
	case <-time.After(time.Second):
		t.Error("no Pending report while the send hangs")
	}
	close(n.release)
	deadline := time.After(3 * time.Second)
	for ok := false; !ok; {
		select {
		case d := <-reports:
			ok = d.OK
		case <-deadline:
			t.Fatal("no OK report after the send returned")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

// countingSource alternates black and white frames, so a pixel_change
// watch fires on every poll, and counts its grabs.
type countingSource struct {
	mu sync.Mutex
	n  int
}

func (c *countingSource) Grab(ctx context.Context) (image.Image, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return flat(4, 4, uint8(255*(c.n%2))), nil
}

func (c *countingSource) grabs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
