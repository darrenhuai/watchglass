package web

import (
	"bytes"
	"context"
	"errors"
	"image"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/state"
)

// slowEngine blocks until its context gives up, like a cold rapidocr
// start on a loaded box.
type slowEngine struct{}

func (slowEngine) Recognize(ctx context.Context, img image.Image) (string, error) {
	<-ctx.Done()
	return "", errors.New("signal: killed")
}

// brokenEngine fails at once, the way a crashed interpreter does.
type brokenEngine struct{}

func (brokenEngine) Recognize(ctx context.Context, img image.Image) (string, error) {
	return "", errors.New("rapidocr: exit status 1: ModuleNotFoundError: onnxruntime")
}

// A Test's read has its own budget, sized per engine and separate from the
// grab's, and running out of it is explained in words with the raw error
// one click away.
func TestTestReadTimeoutIsExplained(t *testing.T) {
	if testReadBudget("rapidocr") <= grabTimeout || testReadBudget("tesseract") <= grabTimeout {
		t.Errorf("subprocess engines need more than the grab's %s to read: rapidocr %s, tesseract %s",
			grabTimeout, testReadBudget("rapidocr"), testReadBudget("tesseract"))
	}
	if testReadBudget("") != testReadBudget("tesseract") {
		t.Error(`an unnamed engine is tesseract and gets its budget`)
	}
	if got := readFailureNote("rapidocr", 45*time.Second, true); !strings.HasPrefix(got, "rapidocr didn't finish reading within 45 seconds,") {
		t.Errorf("readFailureNote = %q", got)
	}
	saved := testReadBudgets["rapidocr"]
	testReadBudgets["rapidocr"] = 50 * time.Millisecond
	t.Cleanup(func() { testReadBudgets["rapidocr"] = saved })

	s, _ := newTestServer(t)
	s.engines.RapidOCR = slowEngine{}
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"ocr_match"}, "engine": {"rapidocr"}, "pattern": {"x"}}
	start := time.Now()
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the read budget was not applied (took %s)", time.Since(start))
	}
	for _, want := range []string{
		`rapidocr didn&#39;t finish reading within `,
		` seconds, so the test stopped. The box may be busy (the watch reads with rapidocr too). Try again in a moment.`,
		`<details class="tech-detail test-note-detail">`,
		`<p class="tech-raw mono">signal: killed</p>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("timed-out read should say %q; body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Read failed") || strings.Contains(body, "deadline exceeded</span>") {
		t.Errorf("the note must not lead with Go's wording; body:\n%s", body)
	}

	s.engines.RapidOCR = brokenEngine{}
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, `<span>rapidocr couldn&#39;t read the region.</span>`) ||
		!strings.Contains(body, "ModuleNotFoundError: onnxruntime</p>") || strings.Contains(body, "finish reading within") {
		t.Errorf("a failed read is a sentence plus its raw error; body:\n%s", body)
	}
}

func TestCollapseSamples(t *testing.T) {
	now := time.Now()
	at := func(s int) time.Time { return now.Add(-time.Duration(s) * time.Second) }
	in := []state.Sample{
		{TS: at(0), Reading: "?"},
		{TS: at(1), Reading: "?"},
		{TS: at(2), Reading: "?", Fired: true},
		{TS: at(3), Reading: "12"},
		{TS: at(4), Reading: "?"},
		{TS: at(5), Reading: "?"},
	}
	got := collapseSamples(in)
	want := []struct {
		reading string
		fired   bool
		count   int
		ts      time.Time
	}{{"?", false, 2, at(0)}, {"?", true, 1, at(2)}, {"12", false, 1, at(3)}, {"?", false, 2, at(4)}}
	if len(got) != len(want) {
		t.Fatalf("got %d tiles, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Reading != w.reading || g.Fired != w.fired || g.Count != w.count || !g.TS.Equal(w.ts) {
			t.Errorf("tile %d = %q fired=%v ×%d at %v, want %q fired=%v ×%d at %v (the newest of its run)",
				i, g.Reading, g.Fired, g.Count, g.TS, w.reading, w.fired, w.count, w.ts)
		}
	}
	if collapseSamples(nil) != nil {
		t.Error("no samples, no tiles")
	}
}

// The filmstrip is a named, focusable scroll region whose frames are keyed
// for app.js's reconciliation; a run of equal readings is one frame with a
// count, and captions carry their full text in a title.
func TestLiveStripIsAKeyedScrollRegion(t *testing.T) {
	s, _ := newTestServer(t)
	now := time.Now()
	s.reg.Add("printer", state.Sample{TS: now.Add(-3 * time.Second), Reading: "3.0% changed", PNG: pngBytes(t)})
	s.reg.Add("printer", state.Sample{TS: now.Add(-2 * time.Second), Reading: "0.0% changed", PNG: pngBytes(t)})
	s.reg.Add("printer", state.Sample{TS: now.Add(-time.Second), Reading: "0.0% changed", PNG: pngBytes(t)})
	s.reg.Add("printer", state.Sample{TS: now, Reading: "0.0% changed", PNG: pngBytes(t)})
	_, live := get(t, s.Handler(), "/watch/printer/live")
	for _, want := range []string{
		`<div class="strip" tabindex="0" role="region" aria-label="Recent readings, newest first">`,
		`<figure data-key="` + itoa(now.UnixNano()) + `-3">`,
		`<figcaption title="0.0% changed (3 readings in a row)"><span class="cap-body"><span class="cap-text">0.0% changed</span><span class="tile-count" aria-hidden="true">&times;3</span><span class="sr-only">, 3 readings in a row</span></span></figcaption>`,
		`<figcaption title="3.0% changed"><span class="cap-body"><span class="cap-text">3.0% changed</span></span></figcaption>`,
		`alt=""`,
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live strip missing %q; body:\n%s", want, live)
		}
	}
	if n := strings.Count(live, "<figure "); n != 2 {
		t.Errorf("4 samples in 2 runs should be 2 frames, got %d", n)
	}
	if strings.Contains(live, `alt="crop"`) {
		t.Error(`every crop used to be announced as "crop"; the caption is the name`)
	}
	// The readout still shows the newest sample; the fragment says whether
	// it fired and when, for app.js's announcement.
	if !strings.Contains(live, `data-fired="false" data-ts="`+itoa(now.UnixNano())+`"`) ||
		!strings.Contains(live, `<span class="readout-label">Latest reading</span>`) {
		t.Errorf("live status should carry data-fired/data-ts and the readout label; body:\n%s", live)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// An unchanged Live fragment goes back as a bodiless 304: the poll runs
// every 2 s and the fragment carries every crop as base64.
func TestLiveFragmentRevalidatesWithETag(t *testing.T) {
	s, _ := newTestServer(t)
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "1.0% changed", PNG: pngBytes(t)})
	h := s.Handler()
	resp, body := get(t, h, "/watch/printer/live")
	etag := resp.Header.Get("ETag")
	if resp.StatusCode != 200 || etag == "" || !strings.HasPrefix(etag, `"`) || body == "" {
		t.Fatalf("first GET: status %d, ETag %q", resp.StatusCode, etag)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache (always revalidate)", cc)
	}
	cond := func(inm string) (*http.Response, string) {
		req := httptest.NewRequest("GET", "/watch/printer/live", nil)
		req.Header.Set("If-None-Match", inm)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result(), rec.Body.String()
	}
	for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		if r, b := cond(inm); r.StatusCode != http.StatusNotModified || b != "" {
			t.Errorf("If-None-Match %s: status %d, %d body bytes; want an empty 304", inm, r.StatusCode, len(b))
		}
	}
	if r, _ := cond(`"stale"`); r.StatusCode != 200 {
		t.Errorf("a different tag must get the fragment, got %d", r.StatusCode)
	}
	s.reg.Add("printer", state.Sample{TS: time.Now().Add(time.Second), Reading: "2.0% changed", PNG: pngBytes(t)})
	if r, b := cond(etag); r.StatusCode != 200 || !strings.Contains(b, "2.0% changed") || r.Header.Get("ETag") == etag {
		t.Errorf("a new reading must change the tag and send the fragment: status %d", r.StatusCode)
	}
}

// The readout is not a live region any more (its timestamp changes every
// reading); app.js announces state changes and new fires through
// #live-announce, polls with one request in flight, sleeps in a hidden
// tab, and turns failures into a notice instead of swallowing them.
func TestLiveAnnouncesChangesNotTicks(t *testing.T) {
	s, _ := newTestServer(t)
	_, body := get(t, s.Handler(), "/watch/printer")
	if strings.Contains(body, `id="live-status" aria-live`) || strings.Contains(body, `<div id="live-status" aria-`) {
		t.Errorf("#live-status must not be a live region; body:\n%s", body)
	}
	js, err := readSourceLF("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{
		"function announceLive(ds, reading)",
		`if (ds.state !== heard.state)`,
		`announce("Fired: "`,
		`if (document.visibilityState === "hidden") return;`,
		`if (livePending || liveStopped) return;`,
		`if (resp.status === 404) { discard(); return { kind: "gone" }; }`,
		`if (resp.redirected || ct.indexOf("text/html") !== 0)`,
		`if (!status) { liveFailed("session"); return; }`,
		`if (kind === "fail" && ++liveFails < 3) return;`,
		`if (etag && etag === liveEtag && !liveProblem)`,
		"function syncStrip(next)",
		`figure[data-key]`,
		`liveStrip.classList.toggle("has-more"`,
		`ro.classList.add("tick-fired")`,
		// At the start the strip is put back at the start after an insert:
		// Chromium re-snaps to the frame it last snapped to, which pushed
		// the newest frame off to the left on every reading.
		`var atStart = cur.scrollLeft <= 2;`,
		`if (!atStart) {`,
		`if (cur.scrollLeft !== 0) cur.scrollLeft = 0;`,
		// ...and snapping is off while it is there, because the re-snap also
		// happens later, when the new frame's image loads. Reaching for the
		// strip turns it back on.
		`pinStrip(cur, atStart);`,
		`["wheel", "pointerdown", "touchstart", "keydown"].forEach(function (type) {`,
		`liveStrip.addEventListener(type, unpinStrip, { capture: true, passive: true });`,
		// A run that grew by one equal reading is not a new frame arriving.
		`tileSays(added[0]) !== wasFirst`,
		// The session case keeps polling (a good answer clears it), so it
		// says paused, not stopped.
		`"Live updates paused: something other than watchglass answered`,
		`"Live updates paused. Your login may have expired."`,
		// A deleted watch switches off what would only 404.
		"function retire()",
		"setTitleState(\"deleted\");\n      retire();",
		`saveBtn.disabled = retired;`,
		`if (retired) return "This watch no longer exists, so its region can't be edited or tested.";`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	for _, gone := range []string{
		`.catch(function () {});`,
		`setInterval(poll, 2000)`,
		`var statusHTML = status ? status.innerHTML : html;`,
		`liveStrip.innerHTML = stripHTML`,
		`if (cur.scrollLeft > 0) {`,
		"Live updates stopped",
	} {
		if strings.Contains(src, gone) {
			t.Errorf("app.js still has %q", gone)
		}
	}
	css, err := readSourceLF("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		".status-offline {",
		"#live-strip.has-more::after {",
		"  width: 0;\n  min-width: 100%;\n  margin-top: 6px;", // the caption doesn't size the frame
		"  flex-wrap: wrap;\n  justify-content: center;",      // a fired caption wraps under its tag when narrow
		".cap-body { display: flex;",
		"  -webkit-line-clamp: 2;",
		".detail-grid.is-gone .stage-frame {",
		".readout-value {\n  order: 3;\n  flex: 1 1 100%;",
		"@keyframes readout-fire",
		"scroll-snap-type: x proximity;",
		".strip.at-start { scroll-snap-type: none; }",
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("style.css missing %q", want)
		}
	}
	if strings.Contains(string(css), ".strip::after") || strings.Contains(string(css), ".readout-ts { margin-left: 0; }") {
		t.Error("the scrolling fade and the phone-only timestamp override should be gone")
	}
}

// Test this region shows it is working and takes one press at a time.
func TestTestButtonHasABusyState(t *testing.T) {
	js, err := readSourceLF("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{
		"function setTesting(on)",
		`if (testing || manualGate()) return;`,
		`testBtn.textContent = on ? "Testing…" : testLabel;`,
		`testBox.setAttribute("aria-busy", on ? "true" : "false");`,
		"function testPending()",
		"if (testing) setTesting(false);", // pageshow resync
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	// The result is revealed after the busy state is gone.
	if i, j := strings.Index(src, "setTesting(false);\n      revealTestResult();"), strings.Index(src, "function setTesting(on)"); i < 0 || j < 0 {
		t.Error("revealTestResult must run right after setTesting(false)")
	}
	css, _ := readSourceLF("static/style.css")
	for _, want := range []string{`.btn[aria-busy="true"]::before {`, `#test-result[aria-busy="true"] > :not(.test-pending)`, ".crop-skeleton {"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("style.css missing %q", want)
		}
	}
}

// Every shared button is a 44px target wherever a coarse pointer exists.
func TestSharedButtonsAreTouchTargets(t *testing.T) {
	css, err := readSourceLF("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "@media (any-pointer: coarse) {\n  .btn { min-height: 44px; }\n}") {
		t.Error("style.css should give .btn a 44px min-height under any-pointer: coarse")
	}
	if strings.Contains(string(css), ".region-btns .btn { height: auto; min-height: 44px; }") {
		t.Error("the region-only touch rule is folded into the shared one")
	}
}

// A watch deleted while its page is open answers the poll 404, which the
// page turns into "This watch no longer exists".
func TestLiveOfGoneWatchIs404(t *testing.T) {
	s, _ := newTestServer(t)
	if resp, _ := get(t, s.Handler(), "/watch/nope/live"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// readSourceLF reads a static source file with CRLF folded to LF, so the
// multi-line snippets below match on a Windows checkout that converted line
// endings (.gitattributes pins these files to LF, but an older clone may not
// have been renormalized).
func readSourceLF(name string) ([]byte, error) {
	b, err := os.ReadFile(name)
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), err
}
