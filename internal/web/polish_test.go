package web

import (
	"context"
	"crypto/tls"
	"fmt"
	"image"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/runner"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/supervisor"
)

// E1: the small things the v0.1.9 review left open.

// A region that covers no whole pixel of the frame (only reachable by
// posting fractions the stage can't draw) is a 400 that says so, for the
// OCR Test and the pixel_change Test alike, not a bare 500 from the PNG
// encoder.
func TestTestOfARegionUnderOnePixelIsA400(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	for _, ttype := range []string{"ocr_match", "pixel_change"} {
		form := url.Values{"x": {"0.5"}, "y": {"0.5"}, "w": {"0.0001"}, "h": {"0.0001"}, "ttype": {ttype},
			"pattern": {"x"}, "tthreshold": {"10"}, "pp_rotate": {"90"}}
		resp, body := postForm(t, h, "/watch/printer/test", form)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400; body %q", ttype, resp.StatusCode, body)
		}
		if want := "The region is smaller than one pixel of the 40×30 frame. Drag a bigger box."; !strings.HasPrefix(body, want) {
			t.Errorf("%s: body %q, want %q", ttype, body, want)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s: Content-Type %q, want text/plain (app.js shows it as text)", ttype, ct)
		}
	}
	// One pixel is enough.
	form := url.Values{"x": {"0.5"}, "y": {"0.5"}, "w": {"0.025"}, "h": {"0.034"}, "ttype": {"pixel_change"}, "tthreshold": {"10"}}
	useFakeClock(t)
	if resp, body := postForm(t, h, "/watch/printer/test", form); resp.StatusCode != http.StatusOK {
		t.Errorf("a one-pixel region: status %d, body %q", resp.StatusCode, body)
	}
}

// refusingCam is a camera that turns every login down, counting requests.
func refusingCam(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	n := new(int)
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*n++
		w.Header().Set("WWW-Authenticate", `Basic realm="cam"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(cam.Close)
	return cam, n
}

// A Test the camera turns down says what that press did: it asked the
// camera just now. It doesn't tell the person to press Test, which they
// just did. A second press inside source.ForcedTryGap doesn't ask the
// camera and says when Test will. The snapshot (nobody pressed anything)
// keeps errHint's words.
func TestRefusedLoginTestSaysWhatThePressDid(t *testing.T) {
	cam, requests := refusingCam(t)
	target := "http://admin:pw-e1-web@" + strings.TrimPrefix(cam.URL, "http://") + "/snap.png"
	s, _ := newTestServer(t)
	s.NewSource = func(w config.Watch) (source.Source, error) {
		w.Source = target
		return source.For(w)
	}
	h := s.Handler()
	test := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"}, "tthreshold": {"10"}}

	resp, body := postForm(t, h, "/watch/printer/test", test)
	first, _, _ := strings.Cut(body, "\n")
	if resp.StatusCode != http.StatusBadGateway || *requests != 1 {
		t.Fatalf("first Test: %d, %d requests: %s", resp.StatusCode, *requests, body)
	}
	asked := regexp.MustCompile(`^127\.0\.0\.1:\d+ turned down the login\. Trying again in (10|9)s\. This Test asked the camera just now\. Check the user and password in the source URL; between its own tries the watch waits, so that wrong tries don't lock the account\.$`)
	if !asked.MatchString(first) {
		t.Errorf("first Test says %q", first)
	}
	for _, bad := range []string{"Test this region", "Pressing Test", "pw-e1-web"} {
		if strings.Contains(first, bad) {
			t.Errorf("first Test's summary has %q: %q", bad, first)
		}
	}

	resp, body = postForm(t, h, "/watch/printer/test", test)
	first, _, _ = strings.Cut(body, "\n")
	if resp.StatusCode != http.StatusBadGateway || *requests != 1 {
		t.Fatalf("second Test at once: %d, %d requests, want the camera left alone: %s", resp.StatusCode, *requests, body)
	}
	// One clock only (the Test's), not the poll loop's "Trying again in".
	throttled := regexp.MustCompile(`^127\.0\.0\.1:\d+ turned down the login\. This Test didn't ask the camera: a Test asked it a moment ago, and Test waits 10s between tries so it can't lock the account\. Press it again in (10|9)s\.$`)
	if !throttled.MatchString(first) {
		t.Errorf("second Test says %q", first)
	}

	// The page's snapshot: nobody pressed Test, so the hint can mention it.
	_, body = get(t, h, "/watch/printer/snapshot")
	if !strings.Contains(body, "Pressing Test this region asks the camera even during the wait, at most once every 10s,") {
		t.Errorf("snapshot body %q", body)
	}
}

// The throttled Test's two numbers come from two clocks: how long ago the
// last Test asked (whole seconds, rounded down) and how long until the
// next may (rounded up), with the gap itself between them. Away from the
// first second the three differ, so each must be the right one.
func TestThrottledTestTextNumbers(t *testing.T) {
	for _, c := range []struct {
		ago, in   time.Duration
		agoT, inT string
	}{
		{3400 * time.Millisecond, 6600 * time.Millisecond, "3s ago", "7s"},
		{8 * time.Second, 2 * time.Second, "8s ago", "2s"},
		{200 * time.Millisecond, 9800 * time.Millisecond, "a moment ago", "10s"},
	} {
		err := fmt.Errorf("grab: %w", &source.LoginRefusedError{Status: 401, Skipped: true, Throttled: true, TriedAgo: c.ago, RetryIn: c.in})
		want := "The camera turned down the login. This Test didn't ask the camera: a Test asked it " + c.agoT +
			", and Test waits 10s between tries so it can't lock the account. Press it again in " + c.inT + "."
		if got := testGrabText(err); got != want {
			t.Errorf("TriedAgo %v, RetryIn %v:\n got %q\nwant %q", c.ago, c.in, got, want)
		}
	}
}

// wordsAs is a detailed engine that always reads the same text and words.
type wordsAs struct {
	text  string
	words []ocr.Word
}

func (e wordsAs) Recognize(ctx context.Context, img image.Image) (string, error) { return e.text, nil }
func (e wordsAs) RecognizeWords(ctx context.Context, img image.Image) (string, []ocr.Word, error) {
	return e.text, e.words, nil
}

// "No digits read" comes without a confidence chip: a glyph the decoder
// couldn't make a digit of is a "?" chip, which beside that heading reads
// as a digit after all. A partial read keeps its chips.
func TestNoDigitsReadHasNoConfidenceChips(t *testing.T) {
	s, _ := newTestServerWith(t, ocr.Engines{Tesseract: fakeDetailed{}, SevenSeg: wordsAs{"?", []ocr.Word{{Text: "?", Conf: 11}}}})
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "engine": {"sevenseg"}, "ttype": {"numeric"},
		"pattern": {"([0-9.]+)"}, "op": {"gt"}, "tthreshold": {"1"}}
	_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, `<span class="reading-none">No digits read</span>`) {
		t.Fatalf("no placeholder; body:\n%s", body)
	}
	if strings.Contains(body, "word-chip") || strings.Contains(body, "<dt>Confidence</dt>") {
		t.Errorf("a confidence chip beside \"No digits read\"; body:\n%s", body)
	}
	if !strings.Contains(body, "couldn't make a digit of anything here") {
		t.Errorf("the note that says what to do is gone; body:\n%s", body)
	}

	s.engines.SevenSeg = wordsAs{"2?", []ocr.Word{{Text: "2", Conf: 97}, {Text: "?", Conf: 30}}}
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if strings.Count(body, `class="word-chip`) != 2 {
		t.Errorf("a partial read should keep its two chips; body:\n%s", body)
	}
}

// The flash cookie is Secure when the browser came over https: a TLS
// connection, or an ingress request whose X-Forwarded-Proto (which Home
// Assistant sets) says https. The header means nothing on a direct
// request, where anyone can send it, and plain http keeps the cookie
// usable.
func TestFlashCookieIsSecureOverHTTPS(t *testing.T) {
	s, _ := ingressServer(t)
	h := s.Handler()
	secure := func(resp *http.Response) string {
		for _, c := range resp.Cookies() {
			if c.Name == flashCookie {
				if c.Secure {
					return "Secure"
				}
				return "not Secure"
			}
		}
		return "no flash cookie"
	}
	n := 0
	create := func(peer string, headers map[string]string, tlsConn bool) *http.Response {
		n++
		form := url.Values{"name": {"e1-flash-" + string(rune('a'+n))}, "source": {"http://unused.invalid/s.jpg"}}
		req := httptest.NewRequest("POST", "/watch/new", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = peer
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if tlsConn {
			req.TLS = &tls.ConnectionState{}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}
	sup := ingressPeer + ":40000"
	for _, c := range []struct {
		what    string
		peer    string
		headers map[string]string
		tls     bool
		want    string
	}{
		{"ingress, https", sup, map[string]string{IngressHeader: ingressPrefix, "X-Forwarded-Proto": "https"}, false, "Secure"},
		{"ingress, HTTPS behind another proxy", sup, map[string]string{IngressHeader: ingressPrefix, "X-Forwarded-Proto": "HTTPS, http"}, false, "Secure"},
		{"ingress, http", sup, map[string]string{IngressHeader: ingressPrefix, "X-Forwarded-Proto": "http"}, false, "not Secure"},
		{"ingress, no header", sup, map[string]string{IngressHeader: ingressPrefix}, false, "not Secure"},
		{"direct, header from anyone", "198.51.100.7:5000", map[string]string{"X-Forwarded-Proto": "https"}, false, "not Secure"},
		{"direct, plain http", "198.51.100.7:5000", nil, false, "not Secure"},
		{"direct, TLS", "198.51.100.7:5000", nil, true, "Secure"},
	} {
		resp := create(c.peer, c.headers, c.tls)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s: create answered %d", c.what, resp.StatusCode)
		}
		if got := secure(resp); got != c.want {
			t.Errorf("%s: flash cookie %s, want %s", c.what, got, c.want)
		}
	}
	// Reading it back clears it with the same attribute (a browser keeps a
	// Secure cookie that an insecure Set-Cookie tries to clear).
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = sup
	req.Header.Set(IngressHeader, ingressPrefix)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.AddCookie(&http.Cookie{Name: flashCookie, Value: url.QueryEscape("deleted|x")})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := secure(rec.Result()); got != "Secure" {
		t.Errorf("the clearing cookie through https ingress is %s", got)
	}
}

// restartedServer is a test server whose supervisor keeps trigger state in
// a real history database that already holds a fire of the printer watch
// from before a restart (delivered says whether its alert went out), and
// starts the watch the way main does after a restart.
func restartedServer(t *testing.T, fired time.Time, delivered bool) *Server {
	t.Helper()
	store, err := history.Open(filepath.Join(t.TempDir(), "wg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	w := config.Watch{
		Name:     "printer",
		Source:   "http://unused.invalid/snap.jpg",
		Interval: config.Duration(time.Hour),
		Region:   config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger:  config.Trigger{Type: "pixel_change", Threshold: 10},
		Notify:   []string{"generic+http://127.0.0.1:9/" + "hook"},
	}
	if err := store.SaveTriggerState("printer", history.TriggerState{Fingerprint: runner.Fingerprint(w), LastFired: fired, Delivered: delivered}); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{Watches: []config.Watch{w}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	reg := state.New(5)
	engines := ocr.Engines{Tesseract: fakeDetailed{}}
	sup := supervisor.New(store, reg, engines, func(string, ...any) {})
	sup.NewSource = func(config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, engines, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	s.NewSource = sup.NewSource
	if err := sup.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	return s
}

// After a restart the Live panel still says when the last alert went out
// (or that the last fire's alert may not have), from the trigger state the
// history database kept, until the watch raises an alert again. A fire
// from another day carries its date.
func TestLiveSaysTheLastAlertFromBeforeARestart(t *testing.T) {
	fired := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	if fired.Day() != time.Now().Day() {
		fired = time.Now().Truncate(time.Second) // just after midnight
	}
	s := restartedServer(t, fired, true)
	_, live := get(t, s.Handler(), "/watch/printer/live")
	want := `<p class="delivery delivery-restored"><span class="delivery-mark" aria-hidden="true">✓</span>Last alert sent at <time class="mono" datetime="` +
		fired.UTC().Format(time.RFC3339) + `">` + fired.Format("15:04:05") + `</time>, before watchglass restarted.</p>`
	if !strings.Contains(live, want) {
		t.Errorf("Live panel after a restart: want %s; fragment:\n%s", want, live)
	}
	if !strings.Contains(live, `data-delivery="restored"`) {
		t.Errorf("data-delivery should say restored; fragment:\n%s", live)
	}

	// The watch fires again: the line is about that alert from then on.
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "12.5% changed", Fired: true, PNG: []byte("png")})
	if _, live = get(t, s.Handler(), "/watch/printer/live"); strings.Contains(live, "before watchglass restarted") {
		t.Errorf("the restored line outlived a new fire; fragment:\n%s", live)
	}

	old := time.Date(2026, 3, 4, 20, 43, 43, 0, time.Local)
	s = restartedServer(t, old, false)
	_, live = get(t, s.Handler(), "/watch/printer/live")
	want = `<p class="delivery delivery-restored">Last fired at <time class="mono" datetime="` + old.UTC().Format(time.RFC3339) +
		`">Mar 4, 20:43:43</time>, before watchglass restarted. That alert may not have gone out, so it goes out again if the condition still holds.</p>`
	if !strings.Contains(live, want) {
		t.Errorf("an undelivered fire from another day: want %s; fragment:\n%s", want, live)
	}
	// The list says when it fired, whatever the row's state.
	_, index := get(t, s.Handler(), "/")
	if !strings.Contains(index, `<span class="fired-ago">fired `) {
		t.Errorf("the list should still say when the watch fired; body:\n%s", index)
	}
}

// E1: a Save that changes the notify URLs forgets the delivery record of
// the old list but keeps the restored line: it is about the fire before
// the restart, which the new list doesn't change. It used to vanish on
// that Save and come back on the next Save that left notify alone.
func TestRestoredLineSurvivesANotifySave(t *testing.T) {
	fired := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	if fired.Day() != time.Now().Day() {
		fired = time.Now().Truncate(time.Second)
	}
	s := restartedServer(t, fired, true)
	h := s.Handler()
	form := saveForm("generic+http://127.0.0.1:9/" + "other")
	for i, what := range []string{"a Save that changes notify", "a later Save that leaves it alone"} {
		if i == 1 {
			form.Set("interval", "7s")
		}
		if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s: %d %s", what, resp.StatusCode, body)
		}
		if _, live := get(t, h, "/watch/printer/live"); !strings.Contains(live, "Last alert sent at") ||
			!strings.Contains(live, "before watchglass restarted.") {
			t.Errorf("after %s the restored line is gone; fragment:\n%s", what, live)
		}
	}
}

// A stopped watch (its last Save & restart failed) keeps "fired N ago" on
// the list too, beside the Stopped tag.
func TestStoppedRowSaysWhenItFired(t *testing.T) {
	s, _ := newTestServer(t)
	s.reg.SeedFired("printer", time.Now().Add(-3*time.Minute))
	_, body := get(t, s.Handler(), "/")
	if !strings.Contains(body, `<span class="tag">Stopped</span><span class="fired-ago">fired 3 min ago</span>`) {
		t.Errorf("a stopped watch should still say when it fired; body:\n%s", body)
	}
}

// A Save that changes what the watch reads (region, preprocess including
// rotate, engine, type) drops the strip's old crops, so an unturned crop
// never sits beside a turned one. A Save that changes something else
// (interval, notify, cooldown) keeps them, and so does a pixel_change
// watch's Save of preprocess, which pixel_change doesn't read: its crops
// are the same picture as before.
func TestSaveThatChangesWhatIsReadDropsTheStrip(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	wc, _ := s.findWatch("printer")
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	seed := func() {
		for i := 0; i < 3; i++ {
			s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "OLD", PNG: []byte("png")})
		}
	}
	samples := func() int {
		n := 0
		for _, smp := range s.reg.Recent("printer") {
			if smp.Reading == "OLD" {
				n++
			}
		}
		return n
	}
	seed()
	form := saveForm("")
	form.Set("interval", "7s")
	if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	if n := samples(); n != 3 {
		t.Errorf("a Save of the interval only dropped the strip: %d old samples left, want 3", n)
	}
	for _, c := range []struct {
		field, value string
		drops        bool
	}{
		// printer is a pixel_change watch until the ttype Save.
		{"pp_rotate", "90", false}, {"pp_upscale", "3", false}, {"pp_threshold", "128", false},
		{"x", "0.5", true}, {"ttype", "ocr_changed", true},
		{"pp_rotate", "180", true}, {"pp_upscale", "2", true}, {"engine", "sevenseg", true},
	} {
		if c.drops {
			seed()
		}
		before := samples()
		form.Set(c.field, c.value)
		if c.field == "x" {
			form.Set("w", "0.5")
		}
		if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("save %s=%s: %d %s", c.field, c.value, resp.StatusCode, body)
		}
		switch n := samples(); {
		case c.drops && n != 0:
			t.Errorf("a Save of %s=%s left %d old samples in the strip", c.field, c.value, n)
		case !c.drops && (before == 0 || n != before):
			t.Errorf("a pixel_change Save of %s=%s dropped the strip: %d old samples of %d left", c.field, c.value, n, before)
		}
	}
	if _, ok := s.reg.LastFired("printer"); ok {
		t.Error("LastFired appeared from nowhere")
	}
}

// Test this region is disabled while a test runs and enabled again on
// every outcome (setTesting(false) runs for a result, an error, the 503,
// no answer and a back/forward-cache restore), staying off for a deleted
// watch; focus goes back to it when disabling it dropped focus on <body>.
// The live check (E1/after, probe.py) presses it for real.
func TestTestButtonIsDisabledWhileATestRuns(t *testing.T) {
	js, err := readSourceLF("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{
		"testHadFocus = document.activeElement === testBtn;\n      testBtn.setAttribute(\"aria-busy\", \"true\");\n      testBtn.disabled = true;",
		"testBtn.removeAttribute(\"aria-busy\");\n      testBtn.disabled = retired;",
		"if (testHadFocus && lost && !retired) testBtn.focus({ preventScroll: true });",
		// Every way a test ends goes through setTesting(false).
		"}).then(function () {\n      setTesting(false);\n      revealTestResult();",
		"if (testing) setTesting(false);",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	for _, stale := range []string{"The button stays focusable (aria-busy, not\n  // disabled", "As with\n  // Test, the button stays focusable"} {
		if strings.Contains(src, stale) {
			t.Errorf("app.js still says Test is never disabled: %q", stale)
		}
	}
}

// A portrait frame no longer makes the stage as tall as the picture: the
// img is capped at 60% of the viewport height with its width left auto,
// so the picture keeps its proportions, and the canvas is still sized from
// the img's rendered box (the screenshots at 375 and 1440 are in E1/after).
func TestStageImageHasAMaximumHeight(t *testing.T) {
	css, err := readSourceLF("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "#stage img { display: block; max-width: 100%; max-height: 60vh; max-height: 60svh; -webkit-user-drag: none; }") {
		t.Error("style.css: #stage img should carry max-height 60vh/60svh beside max-width 100%")
	}
	js, _ := readSourceLF("static/app.js")
	if !strings.Contains(string(js), "var w = img.clientWidth, h = img.clientHeight, d = window.devicePixelRatio || 1;") {
		t.Error("app.js: the canvas must be sized from the img's rendered box for the cap to keep the overlay in place")
	}
}
