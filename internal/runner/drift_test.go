package runner

import (
	"context"
	"image"
	"image/draw"
	_ "image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/drift"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// The position check's own photo (internal/drift/testdata): a clock radio
// showing 9:17. The camera sees a 600x440 window of it; a camera that
// moved sees the window moved the other way.
func clockView(t *testing.T, dx, dy int) image.Image {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "drift", "testdata", "clock-sony-917.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	out := image.NewRGBA(image.Rect(0, 0, 600, 440))
	draw.Draw(out, out.Bounds(), img, image.Pt(20-dx, 20-dy), draw.Src)
	return out
}

func clockWatch(alert bool) config.Watch {
	w := watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1})
	w.Name = "clock"
	w.Region = config.Region{X: 0.35, Y: 0.4023, W: 0.3417, H: 0.1523}
	w.MoveAlert = alert
	w.Notify = []string{"generic+http://127.0.0.1:9/unused"}
	return w
}

// observe feeds the runner's hook n frames, one check apart, with a clean
// reading, from *at on.
func observe(r *Runner, img image.Image, at *time.Time, n int) {
	for i := 0; i < n; i++ {
		*at = at.Add(5 * time.Second)
		r.observeDrift(context.Background(), img, *at, driftRead(r.watch, trigger.Event{Reading: "9:17"}))
	}
}

func TestMoveAlertGoesOutOncePerMove(t *testing.T) {
	for _, alert := range []bool{true, false} {
		n := &fakeNotifier{}
		w := clockWatch(alert)
		r, err := New(w, &fakeSource{imgs: []image.Image{clockView(t, 0, 0)}}, &fakeOCR{texts: []string{"9:17"}}, n, nil, t.Logf)
		if err != nil {
			t.Fatal(err)
		}
		board := drift.NewBoard()
		r.Drift = board.Tracker(w, nil, t.Logf)
		at := time.Date(2026, 10, 28, 14, 0, 0, 0, time.Local)
		observe(r, clockView(t, 0, 0), &at, 12)
		if st, _ := board.Status("clock"); st.State != drift.Steady || len(n.sent) != 0 {
			t.Fatalf("alert=%v: before the move: %+v, sent %q", alert, st, n.sent)
		}
		observe(r, clockView(t, 10, 4), &at, 10)
		if !r.isMoved {
			t.Errorf("alert=%v: isMoved is false after the move", alert)
		}
		if !alert {
			if len(n.sent) != 0 {
				t.Errorf("move_alert off, sent %q", n.sent)
			}
			continue
		}
		if len(n.sent) != 1 {
			t.Fatalf("sent %d alerts, want 1: %q", len(n.sent), n.sent)
		}
		for _, want := range []string{"watchglass: clock (camera moved)", "the picture around the box moved about 10 px right and 4 px down since 14:01", "Readings may be wrong until the box is moved."} {
			if !strings.Contains(n.sent[0], want) {
				t.Errorf("alert %q lacks %q", n.sent[0], want)
			}
		}
	}
}

// TestTickFeedsThePositionCheck: every poll's whole frame reaches the
// check, which takes its reference after three clean readings.
func TestTickFeedsThePositionCheck(t *testing.T) {
	w := clockWatch(false)
	r, err := New(w, &fakeSource{imgs: []image.Image{clockView(t, 0, 0)}}, &fakeOCR{texts: []string{"9:17"}}, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	board := drift.NewBoard()
	r.Drift = board.Tracker(w, nil, t.Logf)
	for i := 0; i < 3; i++ {
		if _, err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := board.Status("clock"); st.FrameW != 600 || st.FrameH != 440 {
		t.Errorf("after 3 clean polls the check has no reference: %+v", st)
	}
}

// A move alert waits in its own slot: a fire alert still being tried
// isn't pushed out by it, and it is let go once the picture is back.
func TestMoveAlertRetriedWhileMovedAndDroppedWhenBack(t *testing.T) {
	sk := newSink(t)
	sk.status.Store(503)
	w := retryWatch(sk.url())
	// A change alert holds until it is told, whatever the display shows.
	w.Trigger = config.Trigger{Type: "ocr_changed", Confirm: 1}
	w.MoveAlert = true
	logs := &logBook{}
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, &fakeOCR{texts: []string{"GO"}}, mustShoutrrr(t, sk.url()), nil, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	var reports []Delivery
	r.OnDelivery = func(d Delivery) { reports = append(reports, d) }
	ctx := context.Background()
	s := newSender(r)
	r.isMoved = true
	r.noteCondition()
	s.take(ctx, alert{ts: time.Now(), kind: "fired", title: "fire", body: "fire"})
	s.take(ctx, alert{ts: time.Now(), kind: "moved", title: "watchglass: test-watch (camera moved)", body: "moved"})
	if !s.fire.retrying() || !s.move.retrying() || s.fire.cur.kind != "fired" {
		t.Fatalf("fire slot %+v, move slot %+v: want both waiting", s.fire.cur, s.move.cur)
	}
	if got := reports[len(reports)-1]; got.Kind != "moved" && got.Kind != "fired" {
		t.Errorf("last report = %+v", got)
	}
	// The picture is back: the move alert is let go, the fire alert kept.
	r.isMoved = false
	r.noteCondition()
	s.prune()
	if s.move.retrying() || s.move.why != "the picture around the box is back, or the box was moved" {
		t.Errorf("move slot after the picture came back: retrying %v, why %q", s.move.retrying(), s.move.why)
	}
	if !s.fire.retrying() {
		t.Error("the fire alert was let go with the move alert")
	}
	if !strings.Contains(strings.Join(logs.all(), "\n"), "notify (moved): the picture around the box is back, or the box was moved before") {
		t.Errorf("log:\n%s", strings.Join(logs.all(), "\n"))
	}
}

// covered is a frame from a camera whose lens is covered: dark, with a
// little sensor noise.
func covered(seed int64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 600, 440))
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < len(img.Pix); i += 4 {
		v := uint8(6 + rng.Intn(5))
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 255
	}
	return img
}

// On a covered lens the seven-segment decoder reads "?" (it saw a digit
// cell but couldn't read it) or nothing, and now and then a lone digit
// in the noise. A '?' reading is not a clean one, and one stray digit is
// not the box reading well, so after 5 minutes the page says the picture
// around the box isn't in view. Counted as clean, they kept telling the
// check the box still read well, and the lens was never reported.
func TestCoveredLensOnASevenSegWatchIsReported(t *testing.T) {
	w := clockWatch(true)
	w.Engine = "sevenseg"
	n := &fakeNotifier{}
	r, err := New(w, &fakeSource{imgs: []image.Image{clockView(t, 0, 0)}}, &fakeOCR{texts: []string{"9:17"}}, n, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	board := drift.NewBoard()
	r.Drift = board.Tracker(w, nil, t.Logf)
	at := time.Date(2026, 10, 28, 14, 0, 0, 0, time.Local)
	observe(r, clockView(t, 0, 0), &at, 12)
	if st, _ := board.Status("clock"); st.State != drift.Steady {
		t.Fatalf("before the lens was covered: %+v", st)
	}
	// Now and then the decoder reads a lone digit in the noise.
	readings := []string{"?", "", "?", "1?:?4", "?.?", "7"}
	for i := 0; i < 70; i++ {
		at = at.Add(5 * time.Second)
		r.observeDrift(context.Background(), covered(int64(i)), at, driftRead(r.watch, trigger.Event{Reading: readings[i%len(readings)]}))
	}
	if st, _ := board.Status("clock"); st.State != drift.Lost {
		t.Errorf("lens covered for %v with readings %q: status %+v, want lost", 70*5*time.Second, readings, st)
	}
	if len(n.sent) != 1 || !strings.Contains(n.sent[0], "(box not in view)") {
		t.Errorf("sent %q, want one box-not-in-view alert", n.sent)
	}
}

// A sevenseg watch whose box sits half off the digits reads "1?:?4": not
// a reading to take the reference by.
func TestPartialSevenSegReadingsDontTakeAReference(t *testing.T) {
	for _, tc := range []struct {
		engine, reading string
		learns          bool
	}{
		{"sevenseg", "1?:?4", false},
		{"sevenseg", "?", false},
		{"sevenseg", "12:34", true},
		// Printed text: a question mark is just a question mark.
		{"tesseract", "OK?", true},
	} {
		w := clockWatch(false)
		w.Engine = tc.engine
		r, err := New(w, &fakeSource{imgs: []image.Image{clockView(t, 0, 0)}}, &fakeOCR{texts: []string{tc.reading}}, nil, nil, t.Logf)
		if err != nil {
			t.Fatal(err)
		}
		board := drift.NewBoard()
		r.Drift = board.Tracker(w, nil, t.Logf)
		at := time.Date(2026, 10, 28, 14, 0, 0, 0, time.Local)
		for i := 0; i < 6; i++ {
			at = at.Add(5 * time.Second)
			r.observeDrift(context.Background(), clockView(t, 0, 0), at, driftRead(r.watch, trigger.Event{Reading: tc.reading}))
		}
		st, _ := board.Status("clock")
		if learned := st.FrameW != 0; learned != tc.learns {
			t.Errorf("%s reading %q: took a reference %v, want %v (%+v)", tc.engine, tc.reading, learned, tc.learns, st)
		}
	}
}
