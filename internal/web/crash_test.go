package web

import (
	"context"
	"fmt"
	"html"
	"image"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/runner"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/supervisor"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// crashText is what a person reads when the reader panicked.
const crashText = "The reader crashed on this frame; this is a bug, please report it with the frame"

// panicEngine panics on every frame, as the seven-segment decoder did on
// an angled clock photo with Binarize set.
type panicEngine struct{}

func (panicEngine) Recognize(ctx context.Context, img image.Image) (string, error) {
	var counts []int
	i := -1
	return fmt.Sprint(counts[i]), nil
}

func (e panicEngine) RecognizeWords(ctx context.Context, img image.Image) (string, []ocr.Word, error) {
	text, err := e.Recognize(ctx, img)
	return text, nil, err
}

// logCapture keeps what a server logs.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logCapture) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// The Test route answers a panicking read with a result that says so, and
// the crop to report it with, where it used to drop the connection.
func TestTestRouteAnswersWhenTheReaderCrashes(t *testing.T) {
	s, _ := newTestServerWith(t, ocr.Engines{Tesseract: panicEngine{}, SevenSeg: panicEngine{}})
	var logs logCapture
	s.logf = logs.logf
	for _, engine := range []string{"tesseract", "sevenseg"} {
		form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "engine": {engine},
			"ttype": {"numeric"}, "op": {"gt"}, "tthreshold": {"5"}, "pp_threshold": {"128"}}
		resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s: status %d %s, want a result page; body:\n%s", engine, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
		page := html.UnescapeString(body)
		if !strings.Contains(page, crashText) {
			t.Errorf("%s: the result doesn't say the reader crashed; body:\n%s", engine, body)
		}
		if !strings.Contains(page, "index out of range [-1]") {
			t.Errorf("%s: the technical detail doesn't name the panic; body:\n%s", engine, body)
		}
		// The detail is the panic alone: the sentence is already on the page.
		if d := techDetail(page); !strings.HasPrefix(d, "runtime error: index out of range [-1]") || strings.Contains(d, "crashed") {
			t.Errorf("%s: technical detail = %q, want the panic value alone", engine, d)
		}
		if !strings.Contains(body, "data:image/png;base64,") || !strings.Contains(body, "as the camera sent it") {
			t.Errorf("%s: no crop to report the bug with; body:\n%s", engine, body)
		}
		if strings.Contains(body, "verdict-") {
			t.Errorf("%s: a crashed read has no verdict; body:\n%s", engine, body)
		}
	}
	if !strings.Contains(logs.text(), "[running]") || !strings.Contains(logs.text(), "panicEngine") {
		t.Errorf("the stack wasn't logged:\n%s", logs.text())
	}
}

// A running watch whose frames crash the reader shows as an error on the
// dashboard and on its page, in plain words, before (and whether or not)
// its health goes down.
func TestCrashedWatchShowsAsError(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	wc := config.Watch{
		Name: "printer", Source: "http://unused.invalid/snap.jpg",
		Interval: config.Duration(30 * time.Millisecond), HealthAfter: 1000,
		Region:  config.Region{X: 0, Y: 0, W: 1, H: 1},
		Engine:  "sevenseg",
		Trigger: config.Trigger{Type: "numeric", Op: "gt", Threshold: 5, Confirm: 1},
	}
	cfg := &config.Config{Watches: []config.Watch{wc}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	engines := ocr.Engines{SevenSeg: panicEngine{}}
	reg := state.New(5)
	var logs logCapture
	sup := supervisor.New(nil, reg, engines, logs.logf)
	sup.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, engines, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.GetCrash("printer"); ok && strings.Count(logs.text(), "reader crashed") >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h, ok := reg.GetHealth("printer"); ok && h.Down {
		t.Fatal("health went down; this test is about the crash showing before that")
	}
	_, body := get(t, s.Handler(), "/")
	rowStart := strings.Index(body, `data-label="Last reading"`)
	if rowStart == -1 {
		t.Fatalf("Last reading cell not found; body:\n%s", body)
	}
	row := html.UnescapeString(body[rowStart:])
	if !strings.Contains(row, "led-error") || !strings.Contains(row, crashText) {
		t.Errorf("dashboard should show the crash as an error; row:\n%s", row[:min(600, len(row))])
	}
	_, detail := get(t, s.Handler(), "/watch/printer")
	if !strings.Contains(detail, "status-error") || !strings.Contains(html.UnescapeString(detail), crashText) {
		t.Errorf("detail page should show the crash as an error; body:\n%s", detail)
	}
	_, live := get(t, s.Handler(), "/watch/printer/live")
	if !strings.Contains(live, `data-state="error"`) || !strings.Contains(html.UnescapeString(live), crashText) {
		t.Errorf("live panel should carry the crash; body:\n%s", live)
	}
	if !s.isRunning("printer") {
		t.Error("the watch stopped")
	}
	if n := strings.Count(logs.text(), "[running]"); n != 1 {
		t.Errorf("stack logged %d times, want once:\n%s", n, logs.text())
	}
}

// Crashes that go on until health goes down are one failure: "Failing
// since" stays at the first crash instead of jumping to the down verdict.
func TestCrashThenDownKeepsTheFirstSince(t *testing.T) {
	first := time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name        string
		crash, down time.Time
		want        time.Time
	}{
		{"crashes, then down", first, first.Add(4 * time.Second), first},
		{"down from before, then a crash", first.Add(4 * time.Second), first, first},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := state.New(5)
			reg.SetCrash("printer", state.Crash{Message: crashText, Since: tc.crash})
			reg.SetHealth("printer", state.Health{Down: true, Message: "no reading for 3 consecutive polls", Since: tc.down})
			st := (&Server{reg: reg}).statusFor("printer", true)
			if st.State != "error" || !st.Since.Equal(tc.want) || st.Message != "no reading for 3 consecutive polls" {
				t.Fatalf("status = %+v, want error since %v with the down message", st, tc.want)
			}
		})
	}
}

func TestSummarizeCrash(t *testing.T) {
	for _, msg := range []string{
		"the reader crashed on this frame; this is a bug, please report it with the frame (runtime error: index out of range [-1])",
		"no reading for 3 consecutive polls: the reader crashed on this frame; this is a bug, please report it with the frame (runtime error: index out of range [-1])",
	} {
		if got := summarizeErr(msg); got != crashText {
			t.Errorf("summarizeErr(%q) = %q, want %q", msg, got, crashText)
		}
	}
	// Every step's sentence, bare and inside a health message, is the
	// summary, with the panic value left out.
	for _, st := range runner.Steps {
		ce := &runner.CrashError{Value: "assignment to entry in nil map", Step: st}
		for _, msg := range []string{ce.Error(), "no reading for 2 consecutive polls: grab: " + ce.Error()} {
			if got, want := summarizeErr(msg), upperFirst(st.Sentence()); got != want {
				t.Errorf("summarizeErr(%q) = %q, want %q", msg, got, want)
			}
		}
	}
}

// techDetail is the text under Technical detail in a rendered result.
func techDetail(page string) string {
	i := strings.Index(page, `<p class="tech-raw mono">`)
	if i < 0 {
		return ""
	}
	rest := page[i+len(`<p class="tech-raw mono">`):]
	if j := strings.Index(rest, "</p>"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// grabPanicSource panics in Grab, as a parser meeting a stream it didn't
// expect would.
type grabPanicSource struct{}

func (grabPanicSource) Grab(ctx context.Context) (image.Image, error) {
	var m map[string]int
	m["frame"]++ // assignment to entry in nil map
	return nil, nil
}

// The Test route answers a source that panics in Grab with words that
// name the source, no crop (there is no frame), and the stack in the log,
// where net/http would have dropped the connection.
func TestTestRouteAnswersWhenTheSourceCrashes(t *testing.T) {
	s, _ := newTestServerWith(t, ocr.Engines{Tesseract: fakeDetailed{}, SevenSeg: ocr.NewSevenSeg()})
	s.NewSource = func(w config.Watch) (source.Source, error) { return grabPanicSource{}, nil }
	var logs logCapture
	s.logf = logs.logf
	for _, ttype := range []string{"ocr_changed", "pixel_change"} {
		form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "engine": {"sevenseg"}, "ttype": {ttype}, "tthreshold": {"10"}}
		resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s: status %d %s, want a result page; body:\n%s", ttype, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
		page := html.UnescapeString(body)
		if !strings.Contains(page, upperFirst(runner.StepGrab.Sentence())) || strings.Contains(page, crashText) {
			t.Errorf("%s: the result doesn't blame the source; body:\n%s", ttype, body)
		}
		if d := techDetail(page); !strings.HasPrefix(d, "assignment to entry in nil map") {
			t.Errorf("%s: technical detail = %q, want the panic value", ttype, d)
		}
		if strings.Contains(body, "<figure") || strings.Contains(body, "data:image/png") || strings.Contains(body, "verdict-") {
			t.Errorf("%s: a crash before any frame has no crop and no verdict; body:\n%s", ttype, body)
		}
	}
	if !strings.Contains(logs.text(), "[running]") || !strings.Contains(logs.text(), "grabPanicSource") {
		t.Errorf("the stack wasn't logged:\n%s", logs.text())
	}
}

// A consumer of the reading (OnEvent: the Home Assistant publisher) that
// panics on every poll shows as an error that says the reading is fine,
// next to that reading, with a "since" that holds still across polls:
// the reading clears the crash and the crash is set again every poll.
func TestConsumerCrashShowsNextToTheReading(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	wc := config.Watch{
		Name: "printer", Source: "http://unused.invalid/snap.jpg",
		Interval: config.Duration(30 * time.Millisecond), HealthAfter: 1000,
		Region:  config.Region{X: 0, Y: 0, W: 1, H: 1},
		Engine:  "tesseract",
		Trigger: config.Trigger{Type: "ocr_changed", Confirm: 1},
	}
	cfg := &config.Config{Watches: []config.Watch{wc}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	engines := ocr.Engines{Tesseract: fakeDetailed{}}
	reg := state.New(5)
	var logs logCapture
	sup := supervisor.New(nil, reg, engines, logs.logf)
	sup.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	sup.OnEvent = func(watch string, ev trigger.Event, png []byte) { panic("ha publisher broke") }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, engines, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(logs.text(), "watchglass crashed after reading") >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c1, ok := reg.GetCrash("printer")
	if !ok {
		t.Fatalf("no crash recorded; log:\n%s", logs.text())
	}
	time.Sleep(200 * time.Millisecond)
	c2, ok := reg.GetCrash("printer")
	if !ok || !c2.Since.Equal(c1.Since) {
		t.Errorf("the crash's Since moved from %v to %v (%v) across polls", c1.Since, c2.Since, ok)
	}
	if h, ok := reg.GetHealth("printer"); ok && h.Down {
		t.Error("health went down: the watch reads on every poll")
	}
	after := upperFirst(runner.StepAfter.Sentence())
	_, body := get(t, s.Handler(), "/")
	i := strings.Index(body, `data-label="Last reading"`)
	if i < 0 {
		t.Fatalf("Last reading cell not found; body:\n%s", body)
	}
	row := html.UnescapeString(body[i:])
	row = row[:min(700, len(row))]
	if !strings.Contains(row, "led-error") || !strings.Contains(row, after) || strings.Contains(row, crashText) {
		t.Errorf("dashboard should say the crash came after the reading; row:\n%s", row)
	}
	_, live := get(t, s.Handler(), "/watch/printer/live")
	page := html.UnescapeString(live)
	if !strings.Contains(live, `data-state="error"`) || !strings.Contains(page, after) || !strings.Contains(page, "PRINT COMPLETE") {
		t.Errorf("live panel should carry the crash and the reading; body:\n%s", live)
	}
	if n := strings.Count(logs.text(), "[running]"); n != 1 {
		t.Errorf("stack logged %d times for one panic, want once", n)
	}
}
