package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"html"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/fakeclock"
	"github.com/darrenhuai/watchglass/internal/imgproc"
	"github.com/darrenhuai/watchglass/internal/source"
)

// Every pixel_change Test posted in this package would otherwise sleep its
// gap on the real clock (1 s for the test watch, about ten posts across the
// package): the wait is instant unless a test asks for a clock
// (useFakeClock) or the real one (TestRealPixelTestWait).
func TestMain(m *testing.M) {
	pixelTestWait = func(context.Context, time.Duration) error { return nil }
	os.Exit(m.Run())
}

// seqSource hands out its frames in order, one per Grab; a nil frame is a
// failed grab (with failErr). Each grab moves the fake clock on by grabTook.
type seqSource struct {
	mu       sync.Mutex
	frames   []image.Image
	failErr  error
	grabs    int
	clock    *pixelClock
	grabTook time.Duration
}

func (s *seqSource) Grab(ctx context.Context) (image.Image, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.grabs
	s.grabs++
	if s.clock != nil {
		s.clock.Add(s.grabTook)
	}
	if i >= len(s.frames) {
		i = len(s.frames) - 1
	}
	if s.frames[i] == nil {
		return nil, s.failErr
	}
	return s.frames[i], nil
}

// pixelClock is the pixel_change Test's clock in a test: it moves only
// when a grab takes time (seqSource.grabTook) or the Test waits, and it
// records how long each wait asked for.
type pixelClock struct {
	*fakeclock.Clock
	mu    sync.Mutex
	waits []time.Duration
}

// useFakeClock makes the pixel_change Test's wait instant: it records how
// long the Test asked to wait and moves the clock on by that much.
func useFakeClock(t *testing.T) *pixelClock {
	t.Helper()
	c := &pixelClock{Clock: fakeclock.New(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))}
	oldNow, oldWait := pixelTestNow, pixelTestWait
	pixelTestNow = c.Now
	pixelTestWait = func(ctx context.Context, d time.Duration) error {
		c.mu.Lock()
		c.waits = append(c.waits, d)
		c.mu.Unlock()
		c.Add(d)
		return nil
	}
	t.Cleanup(func() { pixelTestNow, pixelTestWait = oldNow, oldWait })
	return c
}

// flat is a w×h frame of one gray level.
func flat(w, h int, v uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 255
	}
	return img
}

// withColumns is base with the columns [x0, x1) set to gray level v.
func withColumns(base *image.RGBA, x0, x1 int, v uint8) *image.RGBA {
	img := image.NewRGBA(base.Bounds())
	copy(img.Pix, base.Pix)
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := x0; x < x1; x++ {
			img.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return img
}

func pixelServer(t *testing.T, src *seqSource) http.Handler {
	t.Helper()
	s, _ := newTestServer(t)
	s.NewSource = func(w config.Watch) (source.Source, error) { return src, nil }
	return s.Handler()
}

var verdictRe = regexp.MustCompile(`(?s)<p class="verdict verdict-(\w+)">.*?<strong class="verdict-title">(.*?)</strong> <span class="verdict-detail">(.*?)</span>`)

// pixelTest posts a pixel_change Test of the whole frame and returns the
// verdict's state, title and detail (unescaped) and the body.
func postPixelTest(t *testing.T, h http.Handler, kv ...string) (state, title, detail, body string) {
	t.Helper()
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"}, "tthreshold": {"20"}}
	for i := 0; i < len(kv); i += 2 {
		form.Set(kv[i], kv[i+1])
	}
	resp, body := postForm(t, h, "/watch/printer/test", form)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	m := verdictRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no verdict in:\n%s", body)
	}
	return m[1], html.UnescapeString(m[2]), html.UnescapeString(m[3]), body
}

// crops decodes the result's data: URLs.
func crops(t *testing.T, body string) []image.Image {
	t.Helper()
	var out []image.Image
	for _, m := range regexp.MustCompile(`data:image/png;base64,([A-Za-z0-9+/=]+)`).FindAllStringSubmatch(html.UnescapeString(body), -1) {
		raw, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, img)
	}
	return out
}

// Identical frames: 0.0% changed, would not fire, and the result says the
// camera adds no noise. Two grabs, the wait in between.
func TestPixelTestIdenticalFramesWouldNotFire(t *testing.T) {
	clock := useFakeClock(t)
	frame := flat(40, 30, 90)
	src := &seqSource{frames: []image.Image{frame, frame}}
	h := pixelServer(t, src)
	state, title, detail, body := postPixelTest(t, h)
	if state != "unmet" || title != "Would not fire" {
		t.Errorf("verdict %s %q, want unmet Would not fire", state, title)
	}
	for _, want := range []string{"Nothing changed between two frames 1.0 s apart (0.0% of the region)", "under the Threshold of 20%", "the camera adds no noise here"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q lacks %q", detail, want)
		}
	}
	if src.grabs != 2 {
		t.Errorf("grabs = %d, want 2", src.grabs)
	}
	if len(clock.waits) != 1 || clock.waits[0] != time.Second {
		t.Errorf("waits = %v, want [1s] (the test watch polls every 1s)", clock.waits)
	}
	if n := len(crops(t, body)); n != 2 {
		t.Errorf("%d crops, want the region in both frames", n)
	}
	for _, want := range []string{`<div class="crop-pair">`, `<figcaption>First frame</figcaption>`, `<figcaption>Second frame, 1.0 s later</figcaption>`, `alt="The region in the second frame, 1.0 s later"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(body), "nothing to compare") {
		t.Error("the old one-frame note is back")
	}
}

// A known share of the region changes: the result gives that percentage
// (as the Live panel rounds it) and the verdict on either side of the form's
// Threshold, which wins over the saved one (10 in the test config).
func TestPixelTestReportsTheShareThatChanged(t *testing.T) {
	useFakeClock(t)
	a := flat(40, 30, 0)
	b := withColumns(a, 0, 10, 255) // 10 of 40 columns: 25% of the frame
	cases := []struct {
		kv                   []string
		state, title, needle string
	}{
		{[]string{"tthreshold", "20"}, "met", "Would fire", "25.0% of the region changed between two frames 1.0 s apart, at or above the Threshold of 20%. If that was the change you want to catch, 20% catches it. If the screen didn't change, this is camera noise and the Threshold is inside it: raise it to about 75%."},
		{[]string{"tthreshold", "25"}, "met", "Would fire", "at or above the Threshold of 25%. If that was the change you want to catch, 25% catches it."},
		{[]string{"tthreshold", "30"}, "unmet", "Would not fire", "25.0% of the region changed between two frames 1.0 s apart, under the Threshold of 30%."},
		// The left half of the frame: 10 of its 20 columns changed.
		{[]string{"w", "0.5", "tthreshold", "40"}, "met", "Would fire", "50.0% of the region changed"},
		{[]string{"w", "0.5", "tthreshold", "60"}, "unmet", "Would not fire", "50.0% of the region changed"},
		// The right half: nothing in it changed.
		{[]string{"x", "0.5", "w", "0.5", "tthreshold", "1"}, "unmet", "Would not fire", "Nothing changed between two frames"},
	}
	for _, c := range cases {
		h := pixelServer(t, &seqSource{frames: []image.Image{a, b}})
		state, title, detail, _ := postPixelTest(t, h, c.kv...)
		if state != c.state || title != c.title || !strings.Contains(detail, c.needle) {
			t.Errorf("%v: got %s %q %q, want %s %q containing %q", c.kv, state, title, detail, c.state, c.title, c.needle)
		}
	}
}

// The comparison is the runner's: imgproc.PercentChanged with
// imgproc.NoiseTolerance, so a change of 32 gray levels or less is noise
// and one of 33 counts.
func TestPixelTestUsesTheRunnersTolerance(t *testing.T) {
	useFakeClock(t)
	a := flat(40, 30, 100)
	for _, c := range []struct {
		to   uint8
		want string
	}{
		{100 + imgproc.NoiseTolerance, "Nothing changed"},
		{100 + imgproc.NoiseTolerance + 1, "100.0% of the region changed"},
	} {
		h := pixelServer(t, &seqSource{frames: []image.Image{a, flat(40, 30, c.to)}})
		_, _, detail, _ := postPixelTest(t, h)
		if !strings.Contains(detail, c.want) {
			t.Errorf("gray 100 -> %d: %q, want %q", c.to, detail, c.want)
		}
	}
}

// The crops are the region as the camera sends it (the runner compares
// those): Preprocess and Rotate in the form don't touch them.
func TestPixelTestShowsTheCropsAsTheCameraSendsThem(t *testing.T) {
	useFakeClock(t)
	a := flat(40, 30, 10)
	a.Set(0, 0, color.RGBA{200, 10, 10, 255}) // a red corner to find
	b := withColumns(a, 30, 40, 250)
	h := pixelServer(t, &seqSource{frames: []image.Image{a, b}})
	_, _, _, body := postPixelTest(t, h, "pp_rotate", "90", "pp_invert", "on", "pp_grayscale", "on", "pp_threshold", "128", "pp_upscale", "3")
	got := crops(t, body)
	if len(got) != 2 {
		t.Fatalf("%d crops", len(got))
	}
	for i, want := range []image.Image{a, b} {
		g := got[i]
		if g.Bounds().Dx() != 40 || g.Bounds().Dy() != 30 {
			t.Fatalf("crop %d is %v, want the unturned, unscaled 40x30", i, g.Bounds())
		}
		for _, p := range []image.Point{{0, 0}, {5, 5}, {35, 20}} {
			r1, g1, b1, _ := g.At(p.X, p.Y).RGBA()
			r2, g2, b2, _ := want.At(p.X, p.Y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 {
				t.Errorf("crop %d at %v differs from the camera's frame", i, p)
			}
		}
	}
}

// The wait between the frames is the form's Interval (the saved one when
// the field is unusable), kept between 1 and 3 seconds, and the result
// gives the time the two frames actually came apart.
func TestPixelTestGapFollowsTheInterval(t *testing.T) {
	for _, c := range []struct {
		interval string
		wait     time.Duration
	}{
		{"", time.Second}, // saved: 1s
		{"2s", 2 * time.Second},
		{"1500ms", 1500 * time.Millisecond},
		{"1500000us", 1500 * time.Millisecond}, // the field's pattern allows ns, us and µs too
		{"10s", 3 * time.Second},
		{"1h", 3 * time.Second},
		{"500ms", time.Second}, // under 1s: the saved one
		{"soon", time.Second},
	} {
		clock := useFakeClock(t)
		frame := flat(40, 30, 0)
		src := &seqSource{frames: []image.Image{frame, frame}, clock: clock, grabTook: 300 * time.Millisecond}
		h := pixelServer(t, src)
		_, _, detail, body := postPixelTest(t, h, "interval", c.interval)
		if len(clock.waits) != 1 || clock.waits[0] != c.wait {
			t.Errorf("interval %q: waits %v, want %v", c.interval, clock.waits, c.wait)
		}
		// The second grab's 0.3 s counts: the frames are that much further apart.
		gap := gapText(c.wait + 300*time.Millisecond)
		if !strings.Contains(detail, "two frames "+gap+" apart") || !strings.Contains(body, "Second frame, "+gap+" later") {
			t.Errorf("interval %q: want %s apart; detail %q", c.interval, gap, detail)
		}
	}
	if got := pixelTestGap(0); got != time.Second {
		t.Errorf("pixelTestGap(0) = %v", got)
	}
}

// A second grab that fails is a plain-text error with the summary first,
// never markup, even when the error itself holds some.
func TestPixelTestSecondGrabFailureIsPlainText(t *testing.T) {
	useFakeClock(t)
	frame := flat(40, 30, 0)
	err := errors.New(`grab: Get "http://cam.local/<script>alert(1)</script>": dial tcp 10.0.0.9:80: connect: connection refused`)
	src := &seqSource{frames: []image.Image{frame, nil}, failErr: err}
	h := pixelServer(t, src)
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"}, "tthreshold": {"20"}}
	resp, body := postForm(t, h, "/watch/printer/test", form)
	if resp.StatusCode != 502 {
		t.Errorf("status %d, want 502", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type %q, want text/plain", ct)
	}
	first, rest, _ := strings.Cut(body, "\n")
	if !strings.HasPrefix(first, "The first frame arrived but the second didn't: connection refused by cam.local") {
		t.Errorf("summary line %q", first)
	}
	if !strings.Contains(rest, err.Error()) {
		t.Errorf("the full error should follow the summary; body %q", body)
	}
	if strings.Contains(body, "<section") || strings.Contains(body, "&lt;") {
		t.Errorf("an error body is plain text, not a fragment and not escaped markup: %q", body)
	}
	if src.grabs != 2 {
		t.Errorf("grabs = %d", src.grabs)
	}
}

// The verdict uses the Threshold in the form, unsaved and even unusable:
// a bad one still shows what was measured.
func TestPixelTestThresholdIsTheForms(t *testing.T) {
	useFakeClock(t)
	a := flat(40, 30, 0)
	b := withColumns(a, 0, 4, 255) // 10%
	for _, c := range []struct {
		thr, state, title, needle string
	}{
		{"10", "met", "Would fire", "at or above the Threshold of 10%"},
		{"10.5", "unmet", "Would not fire", "under the Threshold of 10.5%"},
		{"0", "invalid", "Can't check the trigger", "Threshold must be above 0: the percent of the region that has to change. 10.0% of the region changed"},
		{"", "invalid", "Can't check the trigger", "Threshold must be above 0"},
		{"lots", "invalid", "Can't check the trigger", "Threshold must be a number. 10.0% of the region changed"},
		{"NaN", "invalid", "Can't check the trigger", "Threshold must be a number."},
	} {
		h := pixelServer(t, &seqSource{frames: []image.Image{a, b}})
		state, title, detail, _ := postPixelTest(t, h, "tthreshold", c.thr)
		if state != c.state || title != c.title || !strings.Contains(detail, c.needle) {
			t.Errorf("threshold %q: got %s %q %q", c.thr, state, title, detail)
		}
	}
}

// What the number means when nothing moved on the screen: the floor and a
// safe Threshold, or a Threshold inside the noise. A met verdict leads with
// the reading the docs send the user to make (press Test while the screen
// changes) and keeps the noise reading as the alternative, so a skimming
// reader isn't told to raise the Threshold past the change they wanted.
func TestPixelTestNoiseAdvice(t *testing.T) {
	px := func(pct, thr float64) *pixelTest {
		return &pixelTest{Gap: "3.0 s", Changed: changedText(pct, thr), Threshold: strconv.FormatFloat(thr, 'f', -1, 64) + "%"}
	}
	for _, c := range []struct {
		pct, thr     float64
		state, wants string
	}{
		{2, 20, "unmet", "under the Threshold of 20%. If the screen didn't change, 2.0% is the camera's noise floor and 20% is well above it."},
		{2, 5, "unmet", "If the screen didn't change, 2.0% is the camera's noise floor and 5% is close to it: about 6% is safer."},
		{6, 5, "met", "at or above the Threshold of 5%. If that was the change you want to catch, 5% catches it. If the screen didn't change, this is camera noise and the Threshold is inside it: raise it to about 18%."},
		{40, 20, "met", "at or above the Threshold of 20%. If that was the change you want to catch, 20% catches it. If the screen didn't change, this is camera noise and the Threshold is inside it. Noise that large leaves little room for a Threshold: try a smaller region on a steadier part of the screen."},
		{100, 20, "met", "If that was the change you want to catch, 20% catches it. If the screen didn't change, this is camera noise and the Threshold is inside it. Noise that large leaves little room for a Threshold"},
		{40, 80, "unmet", "If the screen didn't change, 40.0% is camera noise, and noise that large leaves little room for a Threshold: try a smaller region on a steadier part of the screen."},
	} {
		v := pixelVerdict(c.pct, c.thr, px(c.pct, c.thr), nil)
		if v.State != c.state || !strings.Contains(v.Detail, c.wants) {
			t.Errorf("%v%% against %v: %s %q, want %s containing %q", c.pct, c.thr, v.State, v.Detail, c.state, c.wants)
		}
		if c.state == "met" {
			// The change-you-wanted reading comes before any advice to raise the Threshold.
			catches, raise := strings.Index(v.Detail, "catches it"), strings.Index(v.Detail, "If the screen didn't change")
			if catches < 0 || raise < 0 || catches > raise || strings.Contains(v.Detail, "fire by itself") {
				t.Errorf("%v%% against %v: a met verdict leads with the wanted change, got %q", c.pct, c.thr, v.Detail)
			}
		}
	}
}

// The default wait really sleeps, and stops early when the request is
// cancelled; a cancelled wait is a plain 503, not a result.
func TestRealPixelTestWait(t *testing.T) {
	start := time.Now()
	if err := realPixelTestWait(context.Background(), 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 30*time.Millisecond {
		t.Errorf("returned after %v, before the 30ms it was asked to wait", took)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	if err := realPixelTestWait(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled wait returned %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("a cancelled wait took %v", took)
	}

	old := pixelTestWait
	pixelTestWait = func(ctx context.Context, d time.Duration) error { return context.Canceled }
	t.Cleanup(func() { pixelTestWait = old })
	frame := flat(40, 30, 0)
	src := &seqSource{frames: []image.Image{frame, frame}}
	resp, body := postForm(t, pixelServer(t, src), "/watch/printer/test", url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"}, "tthreshold": {"20"}})
	if resp.StatusCode != 503 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || !strings.HasPrefix(body, "The test was cancelled before the second frame.") || strings.Contains(body, "<") {
		t.Errorf("cancelled test: status %d, Content-Type %q, body %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if src.grabs != 1 {
		t.Errorf("grabs = %d, want only the first", src.grabs)
	}
}

// The Interval field carries the saved interval in data-saved, so app.js
// can say the gap the server will use even after a refused Save re-rendered
// the field with the bad text in it (the server goes by the saved interval
// then).
func TestIntervalFieldCarriesTheSavedInterval(t *testing.T) {
	s, _ := newTestServer(t)
	_, body := get(t, s.Handler(), "/watch/printer")
	// The attribute sits on the Interval input (the only data-saved on the page).
	if !strings.Contains(body, `id="f-interval" name="interval" value="1s" placeholder`) || strings.Count(body, `data-saved="`) != 1 || !strings.Contains(body, `spellcheck="false" data-saved="1s"`) {
		t.Errorf("fresh page: body lacks the saved interval on the field:\n%s", body)
	}
	// "500ms" parses (the rejected page's Watch.Interval is then 500ms) but
	// config.Validate refuses it; "abc" doesn't parse at all. Either way the
	// field carries the interval in the file, the one a Test waits.
	for _, bad := range []string{"500ms", "abc"} {
		form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"}, "tthreshold": {"10"}, "confirm": {"1"}, "interval": {bad}}
		resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
		if resp.StatusCode != 400 {
			t.Fatalf("interval %q: status %d, want the save refused", bad, resp.StatusCode)
		}
		if !strings.Contains(body, `id="f-interval" name="interval" value="`+bad+`" placeholder`) || strings.Count(body, `data-saved="`) != 1 || !strings.Contains(body, `spellcheck="false" data-saved="1s"`) {
			t.Errorf("refused save with interval %q: the field shows the bad text but still carries the saved interval; body:\n%s", bad, body)
		}
	}
}

// The percentage is rounded as the Live panel rounds it, unless that
// rounding would contradict the verdict or turn a few pixels into nothing.
func TestChangedText(t *testing.T) {
	for _, c := range []struct {
		pct, thr float64
		want     string
	}{
		{25, 20, "25.0%"},
		{4.24, 20, "4.2%"},
		{0, 20, "0.0%"},
		{19.96, 20, "19.96%"},
		{19.9996, 20, "19.9996%"},
		{19.954321, 20, "19.95%"},
		{20, 20, "20.0%"},
		{0.004, 20, "0.004%"},
	} {
		if got := changedText(c.pct, c.thr); got != c.want {
			t.Errorf("changedText(%v, %v) = %q, want %q", c.pct, c.thr, got, c.want)
		}
	}
}

// Frames of different sizes: the watch counts everything as changed, and
// the result says why.
func TestPixelTestFramesOfDifferentSizes(t *testing.T) {
	useFakeClock(t)
	h := pixelServer(t, &seqSource{frames: []image.Image{flat(40, 30, 0), flat(80, 60, 0)}})
	_, title, detail, body := postPixelTest(t, h)
	if title != "Would fire" || !strings.Contains(detail, "100.0% of the region changed") ||
		!strings.Contains(html.UnescapeString(body), "The camera sent the two frames at different sizes (40×30, then 80×60)") {
		t.Errorf("got %q %q; body:\n%s", title, detail, body)
	}
}

// A Test without ttype (curl, an older client) on a saved pixel_change
// watch reads the crop with the engine as before and makes no claim about a
// change: the one-frame note is gone from there too.
func TestPixelTestWithoutTtypeHasNoOneFrameNote(t *testing.T) {
	s, _ := newTestServer(t)
	_, body := postForm(t, s.Handler(), "/watch/printer/test", url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}})
	if !strings.Contains(body, "PRINT COMPLETE") || strings.Contains(body, `class="verdict`) || strings.Contains(strings.ToLower(body), "nothing to compare") {
		t.Errorf("body:\n%s", body)
	}
}

// app.js says what the test is doing while it runs, with the same gap as
// the server, marks the result stale when Interval changes on a
// pixel_change watch, and style.css lays the two crops out.
func TestPixelTestPageWiring(t *testing.T) {
	js, err := readSourceLF("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := readSourceLF("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"function pixelGapMs()",
		"return Math.min(Math.max(ms, 1000), 3000);",
		// The same units the field's pattern and time.ParseDuration take, so "1500000us" isn't read as the 5 s fallback.
		`var DURATION_RE = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;`,
		`if (!DURATION_RE.test(s)) return 5000;`,
		`if (f && (!DURATION_RE.test(f.value.trim()) || ms < 1000)) ms = parseDuration((f.getAttribute("data-saved") || f.defaultValue).trim());`,
		`"Comparing two frames " + pixelGapText() + " apart…"`,
		`holder = el("div", "crop-pair");`,
		`(e.target.name === "interval" && testEngine() === "")`,
		"Test this region shows how much changes between two frames, so you can see the camera's own noise first.",
	} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	for _, want := range []string{
		".crop-pair {\n  display: flex;\n  flex-wrap: wrap;",
		".crop-pair > .crop-frame { flex: 1 1 13rem; min-width: 0; margin: 0; }",
		".test-panel.is-stale > .crop-pair,",
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("style.css lacks %q", want)
		}
	}
	if pixelTestMinGap != time.Second || pixelTestMaxGap != 3*time.Second {
		t.Error("app.js's pixelGapMs clamps to 1-3 s: keep the two in step")
	}
}
