package web

import (
	"context"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/source"
)

// getWithCookies is get plus request cookies (the flash round trip).
func getWithCookies(t *testing.T, h http.Handler, path string, cookies []*http.Cookie) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func flashCookieFrom(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == flashCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in response; Set-Cookie: %v", flashCookie, resp.Header.Values("Set-Cookie"))
	return nil
}

// Save, create and delete each end in a redirect that carries a one-shot
// confirmation: shown once on the page it lands on, then cleared, and never
// part of the redirect URL.
func TestMutationsConfirmWithOneShotFlash(t *testing.T) {
	s, _ := newTestServer(t)
	s.BasePath = "/wg"
	h := s.Handler()

	resp, _ := postForm(t, h, "/watch/new", url.Values{"name": {"oven"}, "source": {"http://cam2/snap.jpg"}})
	if loc := resp.Header.Get("Location"); loc != "/wg/watch/oven" {
		t.Fatalf("create Location = %q, want the plain page URL", loc)
	}
	c := flashCookieFrom(t, resp)
	if c.Path != "/wg/" || !c.HttpOnly || c.MaxAge <= 0 {
		t.Errorf("flash cookie should be HttpOnly, short-lived and scoped to the base path: %+v", c)
	}
	resp, body := getWithCookies(t, h, "/watch/oven", []*http.Cookie{c})
	if !strings.Contains(body, `<p class="flash" role="status">`) || !strings.Contains(body, `Created <strong class="mono">oven</strong>.`) {
		t.Errorf("detail after create should confirm it; body:\n%s", body)
	}
	if cleared := flashCookieFrom(t, resp); cleared.MaxAge >= 0 {
		t.Errorf("showing the flash must clear the cookie, got %+v", cleared)
	}
	if _, body = get(t, h, "/watch/oven"); strings.Contains(body, `class="flash"`) {
		t.Error("a page without the cookie must not show a flash")
	}

	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"},
		"tthreshold": {"10"}, "confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"}}
	resp, _ = postForm(t, h, "/watch/oven/save", form)
	if resp.StatusCode != 303 {
		t.Fatalf("save status = %d", resp.StatusCode)
	}
	_, body = getWithCookies(t, h, "/watch/oven", []*http.Cookie{flashCookieFrom(t, resp)})
	if !strings.Contains(body, `Saved to config.yaml and restarted the watch <time class="flash-meta mono" datetime="`) {
		t.Errorf("detail after save should confirm the save and restart; body:\n%s", body)
	}

	resp, _ = postForm(t, h, "/watch/oven/delete", url.Values{})
	_, body = getWithCookies(t, h, "/", []*http.Cookie{flashCookieFrom(t, resp)})
	if !strings.Contains(body, `Deleted <strong class="mono">oven</strong>`) {
		t.Errorf("index after delete should confirm it; body:\n%s", body)
	}

	// A garbled or unknown cookie shows nothing; a name is always escaped.
	for _, v := range []string{"bogus", url.QueryEscape("pwned|x")} {
		if _, body = getWithCookies(t, h, "/", []*http.Cookie{{Name: flashCookie, Value: v}}); strings.Contains(body, `class="flash"`) {
			t.Errorf("cookie %q should not render a flash", v)
		}
	}
	_, body = getWithCookies(t, h, "/", []*http.Cookie{{Name: flashCookie, Value: url.QueryEscape("deleted|<script>alert(1)</script>")}})
	if strings.Contains(body, "<script>alert") || !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("flash subject must be escaped; body:\n%s", body)
	}
}

// Every page names itself in the tab, and the favicon is served.
func TestPageTitlesAndFavicon(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	for path, want := range map[string]string{
		"/":              "<title>Watches · watchglass</title>",
		"/watch/printer": "<title>[stopped] printer · watchglass</title>",
	} {
		_, body := get(t, h, path)
		if !strings.Contains(body, want) || !strings.Contains(body, `<link rel="icon" type="image/svg+xml" href="/static/favicon.svg">`) {
			t.Errorf("%s: want %s and the icon link; head:\n%s", path, want, body[:strings.Index(body, "<body>")])
		}
	}
	wc, _ := s.findWatch("printer")
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.sup.Stop("printer") })
	if _, body := get(t, h, "/watch/printer"); !strings.Contains(body, "<title>printer · watchglass</title>") {
		t.Errorf("a running watch's title carries no state prefix; body:\n%s", body)
	}
	if resp, body := get(t, h, "/static/favicon.svg"); resp.StatusCode != 200 || !strings.Contains(body, "<svg") {
		t.Errorf("favicon.svg: status %d", resp.StatusCode)
	}
	if resp, _ := get(t, h, "/favicon.ico"); resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/static/favicon.svg" {
		t.Errorf("/favicon.ico should redirect to the SVG, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

type failingSource struct{ err error }

func (f failingSource) Grab(ctx context.Context) (image.Image, error) { return nil, f.err }

// /test and /snapshot failures are text/plain, summary line first and the
// full chain after it. app.js relies on that contract: an error body is
// rendered as text, never as markup, because the chain echoes the source
// URL (a source like http://x/?a=<img onerror=...> used to execute).
func TestGrabErrorsArePlainTextSummaryThenDetail(t *testing.T) {
	s, _ := newTestServer(t)
	evil := `http://127.0.0.1:1/x?a=<img/src/onerror=alert(1)>`
	s.NewSource = func(w config.Watch) (source.Source, error) {
		return failingSource{fmt.Errorf("snapshot %s: Get %q: dial tcp 127.0.0.1:1: connect: connection refused", evil, evil)}, nil
	}
	region := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}}
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", region)
	if resp.StatusCode != http.StatusBadGateway || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("test grab failure: status %d, Content-Type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	// The summary line carries errHint's loopback advice after the
	// sentence: 127.0.0.1 refused, and inside Docker or WSL that address
	// is the container itself.
	if !strings.HasPrefix(body, "Connection refused by 127.0.0.1:1. If watchglass runs in Docker or WSL, ") ||
		!strings.Contains(body, "host.docker.internal.\nsnapshot http://127.0.0.1:1/x?a=<img") {
		t.Errorf("body should be summary, newline, raw chain; got %q", body)
	}
	if strings.Contains(body, "snapshot failed:") {
		t.Errorf("the redundant 'snapshot failed:' wrapper should be gone; got %q", body)
	}
	resp, body = get(t, s.Handler(), "/watch/printer/snapshot")
	if resp.StatusCode != http.StatusBadGateway || !strings.HasPrefix(body, "Connection refused by 127.0.0.1:1. If watchglass") {
		t.Errorf("snapshot failure: status %d body %q", resp.StatusCode, body)
	}

	resp, body = postForm(t, s.Handler(), "/watch/printer/test", url.Values{"x": {"-0.2"}, "y": {"0"}, "w": {"1.5"}, "h": {"1"}})
	if resp.StatusCode != 400 || strings.Contains(body, "{X:") || !strings.HasPrefix(body, "Region must fit inside the frame") {
		t.Errorf("bad region should explain itself without a struct dump; status %d body %q", resp.StatusCode, body)
	}

	js, err := assets.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	// The two places an answer is parsed as markup (Test this region and
	// Send test notification) each sit inside their ok+text/html branch,
	// which returns before that button's text-only error path.
	src := string(js)
	for _, errCall := range []string{`renderTestError(box, r.text.trim()`, `renderNotifyError(r.text.trim()`} {
		errPath := strings.Index(src, errCall)
		if errPath < 0 {
			t.Errorf("app.js lacks %s", errCall)
			continue
		}
		parse := strings.LastIndex(src[:errPath], "tpl.innerHTML = r.text;")
		guard := strings.LastIndex(src[:errPath], "if (r.ok && r.html) {")
		if guard < 0 || parse < guard || !strings.Contains(src[parse:errPath], "return;") {
			t.Errorf("app.js must only insert a successful text/html answer as markup (%s)", errCall)
		}
	}
	if strings.Count(src, "innerHTML = r.text") != 2 || strings.Contains(src, "el.innerHTML = html") {
		t.Error("app.js must only insert a successful text/html Test answer as markup")
	}
}

// Status fragments and labels share one voice: sentence case, no trailing
// period, and the same back-link wording on every page.
func TestStateWordingIsConsistent(t *testing.T) {
	s, _ := newTestServer(t)
	s.sup.NewSource = func(w config.Watch) (source.Source, error) { return blockingSource{}, nil }
	wc, _ := s.findWatch("printer")
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.sup.Stop("printer") })
	_, index := get(t, s.Handler(), "/")
	_, detail := get(t, s.Handler(), "/watch/printer")
	_, live := get(t, s.Handler(), "/watch/printer/live")
	for _, c := range []struct{ page, body, want string }{
		{"index", index, `<span class="reading-pending">No readings yet</span>`},
		{"live", live, `<p class="muted">No readings yet</p>`},
		{"detail", detail, `<p class="muted">Loading…</p>`},
		{"detail", detail, "&larr; All watches"},
		{"detail", detail, "> Grayscale</label>"},
		{"detail", detail, "> Invert</label>"},
	} {
		if !strings.Contains(c.body, c.want) {
			t.Errorf("%s missing %q", c.page, c.want)
		}
	}
	for _, old := range []string{"no data yet", "No readings yet.", "loading…", "all watches", "> grayscale", "stale since"} {
		if strings.Contains(index+detail+live, old) {
			t.Errorf("old wording %q still rendered", old)
		}
	}
}
