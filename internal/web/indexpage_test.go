package web

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The watch list refreshes itself in the background (static/index.js
// re-fetches the page every few seconds with the poll header). That fetch
// must never eat the one-shot confirmation cookie a redirect just set: the
// flash belongs to the page load the user navigated to.
func TestIndexPollDoesNotConsumeFlash(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	resp, _ := postForm(t, h, "/watch/printer/delete", url.Values{})
	c := flashCookieFrom(t, resp)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(pollHeader, "1")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	pollResp := rec.Result()
	raw, _ := io.ReadAll(pollResp.Body)
	body := string(raw)
	if strings.Contains(body, `class="flash"`) {
		t.Errorf("a background poll must not render the flash; body:\n%s", body)
	}
	for _, sc := range pollResp.Cookies() {
		if sc.Name == flashCookie {
			t.Errorf("a background poll must not clear the flash cookie, got %+v", sc)
		}
	}
	// The real page load that follows still gets it, exactly once.
	resp, body = getWithCookies(t, h, "/", []*http.Cookie{c})
	if !strings.Contains(body, `Deleted <strong class="mono">printer</strong>`) {
		t.Errorf("the navigation after the poll should still confirm the delete; body:\n%s", body)
	}
	if cleared := flashCookieFrom(t, resp); cleared.MaxAge >= 0 {
		t.Errorf("showing the flash must clear the cookie, got %+v", cleared)
	}
}

// Each row names its watch for index.js (data-name), keeps table semantics
// under the phone card layout (explicit roles), and its Delete says which
// watch it deletes and what deleting does, in the styled dialog and in the
// no-script confirm() alike.
func TestIndexRowsNameTheirWatchAndDeleteSaysWhatItDoes(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	_, body := get(t, h, "/")
	for _, want := range []string{
		`<table class="watch-table" id="watch-rows" role="table" data-src="/">`,
		`<thead role="rowgroup">`, `<tbody role="rowgroup">`,
		`<th role="columnheader" class="col-trigger">Trigger</th>`,
		`<tr role="row" data-name="printer">`,
		`<td role="cell" class="cell-name" data-label="Name">`,
		`<td role="cell" class="col-trigger" data-label="Trigger">`,
		`<td role="cell" class="cell-reading" data-label="Last reading">`,
		`<button type="submit" class="btn btn-ghost btn-danger">Delete<span class="sr-only"> watch printer</span></button>`,
		`class="delete-form" onsubmit="return confirm('Delete watch printer? It stops now, is removed from config.yaml, and its reading history is discarded.')"`,
		`<dialog class="dialog" id="confirm-delete" aria-labelledby="confirm-delete-title" aria-describedby="confirm-delete-text">`,
		`It stops now, is removed from config.yaml, and its reading history is discarded. This can't be undone.`,
		`<button type="submit" value="delete" class="btn btn-danger-solid">Delete watch</button>`,
		`<script src="/static/index.js" defer></script>`,
		`<a class="skip-link" href="#main">Skip to content</a>`,
		`<main id="main" tabindex="-1">`,
		`<a class="btn btn-outline" href="#add-heading" data-jump-add>Add watch</a>`,
		`<h2 id="add-heading" tabindex="-1">Add a watch</h2>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %s\nbody:\n%s", want, body)
		}
	}
	// A name with a quote can't break out of the confirm() string or the
	// data-name attribute.
	resp, _ := postForm(t, h, "/watch/new", url.Values{"name": {`it's "odd"`}, "source": {"http://cam2/snap.jpg"}})
	if resp.StatusCode != 303 {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	_, body = get(t, h, "/")
	if !strings.Contains(body, `data-name="it&#39;s &#34;odd&#34;"`) {
		t.Errorf("data-name must be attribute-escaped; body:\n%s", body)
	}
	// html/template writes the quotes as ' and " inside the JS
	// string (spelled out here through a rune, so the source stays free of
	// escape sequences).
	bs := string(rune(92))
	wantJS := "confirm('Delete watch it" + bs + "u0027s " + bs + "u0022odd" + bs + "u0022? It stops now"
	if !strings.Contains(body, wantJS) {
		t.Errorf("the confirm() copy must be JS-string-escaped (want %s); body:\n%s", wantJS, body)
	}
	// The script is served from the embedded assets and speaks the same
	// poll header the server checks.
	resp, js := get(t, h, "/static/index.js")
	if resp.StatusCode != 200 || !strings.Contains(js, `"`+pollHeader+`"`) {
		t.Errorf("/static/index.js: status %d, mentions %s: %v", resp.StatusCode, pollHeader, strings.Contains(js, pollHeader))
	}
}

// The count chip says what it counts, and disappears with the list: the
// empty page is the add form with the three steps in front of it, rendered
// once (ids stay unique), and without the delete dialog or the poll notice.
func TestIndexCountChipAndEmptyState(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	_, body := get(t, h, "/")
	if !strings.Contains(body, `<span class="count-chip mono" data-count>1 watch</span>`) {
		t.Errorf("one watch should read '1 watch'; body:\n%s", body)
	}
	if strings.Contains(body, "total") || strings.Contains(body, "Camera-fed") {
		t.Error("the old 'N total' chip / tagline copy is still rendered")
	}
	if resp, _ := postForm(t, h, "/watch/new", url.Values{"name": {"oven"}, "source": {"http://cam2/snap.jpg"}}); resp.StatusCode != 303 {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	if _, body = get(t, h, "/"); !strings.Contains(body, `data-count>2 watches</span>`) {
		t.Errorf("two watches should read '2 watches'; body:\n%s", body)
	}
	for _, n := range []string{"printer", "oven"} {
		if resp, _ := postForm(t, h, "/watch/"+n+"/delete", url.Values{}); resp.StatusCode != 303 {
			t.Fatalf("delete %s status = %d", n, resp.StatusCode)
		}
	}
	_, body = get(t, h, "/")
	for _, want := range []string{
		// data-src: index.js polls from the empty page too, so the first
		// watch created elsewhere shows up without a manual reload.
		`<section class="panel empty-state" aria-labelledby="add-heading" data-src="/">`,
		`<h2 id="add-heading" class="empty-title" tabindex="-1">Add your first watch</h2>`,
		`<ol class="empty-steps">`,
		`<form class="form-inline-add" method="post" action="/watch/new" aria-labelledby="add-heading">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("empty index missing %s\nbody:\n%s", want, body)
		}
	}
	for _, gone := range []string{"count-chip", "data-jump-add", "confirm-delete", "index-stale", "watch-table", "No watches yet."} {
		if strings.Contains(body, gone) {
			t.Errorf("empty index should not contain %q; body:\n%s", gone, body)
		}
	}
	if n := strings.Count(body, `id="new-name"`); n != 1 {
		t.Errorf("the add form must render exactly once, got %d name inputs", n)
	}
	// A rejected create on the empty page keeps the same shape.
	_, body = postForm(t, h, "/watch/new", url.Values{"name": {"a/b"}, "source": {"http://cam2/snap.jpg"}})
	if !strings.Contains(body, `class="panel empty-state"`) || strings.Count(body, `id="new-name"`) != 1 || !strings.Contains(body, `id="err-name"`) {
		t.Errorf("a rejected create on the empty page should re-render the empty state with the error; body:\n%s", body)
	}
}

// index.js keeps the rendered rows in place and only re-orders when the
// server's order changed. The template leaves whitespace text between the
// <tr>s, so the "is this row already where it belongs" check must compare
// element siblings: comparing plain siblings moved every row on the first
// poll, which dropped the focus a keyboard user had in the list.
func TestIndexRowsAreSeparatedByWhitespaceAndScriptOrdersByElement(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	if resp, _ := postForm(t, h, "/watch/new", url.Values{"name": {"oven"}, "source": {"http://cam2/snap.jpg"}}); resp.StatusCode != 303 {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	_, body := get(t, h, "/")
	tbody := body[strings.Index(body, "<tbody"):strings.Index(body, "</tbody>")]
	if !regexp.MustCompile(`</tr>\s+<tr `).MatchString(tbody) {
		t.Fatalf("expected whitespace between rendered rows (the case the script must handle); tbody:\n%s", tbody)
	}
	_, js := get(t, h, "/static/index.js")
	if !strings.Contains(js, "prev.nextElementSibling") || !strings.Contains(js, "tbody.rows[0]") {
		t.Error("index.js must place rows against element siblings (nextElementSibling / rows[0])")
	}
	if strings.Contains(js, "prev.nextSibling") || strings.Contains(js, "tbody.firstChild") {
		t.Error("index.js compares against text nodes again (nextSibling / firstChild), which moves every row on the first poll")
	}
	// Both branches that bring a row into the table strip the no-script
	// confirm() through the one helper, so a swapped-in row can't ask twice.
	if strings.Count(js, "adoptRow(") < 3 {
		t.Error("index.js should adopt rendered, added and replaced rows through adoptRow")
	}
}

// The add form's `pattern` attributes let the browser flag a bad name or
// source before the round trip. They are only useful if they agree with
// the server, so this compiles them (they are written RE2-compatible, no
// lookarounds) and checks each verdict against a real POST.
func TestAddFormPatternsAgreeWithServer(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	_, body := get(t, h, "/")
	namePat := patternOf(t, body, "new-name")
	srcPat := patternOf(t, body, "new-source")

	names := []struct {
		name string
		ok   bool
	}{
		{"printer-lcd", true}, {"x", true}, {"..." /* three dots: not a dot segment */, true},
		{".hidden", true}, {"a..b", true}, {". .", true}, {"-", true},
		{"pct 100%", true}, {`bs2\x`, true}, {"ünï", true}, {"a%20b", true}, {"a|b", true},
		{"  padded  ", true}, // trimmed by the client's \s* and by create()
		{"a/b", false}, {"a?b", false}, {"a#b", false}, {"/", false}, {"?", false},
		{".", false}, {"..", false}, {" . ", false}, {"a\tb", false}, {"a\nb", false},
		{"   ", false}, {"", false},
	}
	for _, c := range names {
		client := namePat.MatchString(c.name)
		resp, _ := postForm(t, h, "/watch/new", url.Values{"name": {c.name}, "source": {"http://cam2/snap.jpg"}})
		server := resp.StatusCode == 303
		if client != c.ok || server != c.ok {
			t.Errorf("name %q: client accepts %v, server accepts %v, want %v", c.name, client, server, c.ok)
		}
	}

	// Every source scheme config.SourceKind accepts, and nothing else. The
	// server is the reference for the http cases; the rest are compared to
	// SourceKind's own list, because whether an rtsp source starts here
	// depends on ffmpeg being installed.
	for _, ok := range []string{"http://cam/snap.jpg", "https://cam/snap.jpg", "rtsp://cam/stream", "rtsps://cam/stream",
		"v4l2:/dev/video0", "dshow:video=Cam", "ffmpeg:-i x", "ffmpeg: -i x", "  http://cam/snap.jpg  ",
		"demo:printer", "demo:sevenseg", "demo: printer", " demo:sevenseg "} {
		if !srcPat.MatchString(ok) {
			t.Errorf("source pattern rejects %q, which the server accepts", ok)
		}
	}
	// The names here are plain counters: a source with "/" in a name would
	// be rejected for the name, not the source.
	n := 0
	for _, bad := range []string{"notaurl", "cam/snap.jpg", "ftp://cam/x", "http://", "http://   ", "rtsp://", "v4l2:", "ffmpeg:", "ffmpeg:  ", "http:/cam", "", "  ",
		"demo:", "demo:  ", "demo:camera", "demo:printerx", "demo:printer sevenseg"} {
		if srcPat.MatchString(bad) {
			t.Errorf("source pattern accepts %q, which the server rejects", bad)
		}
		n++
		resp, _ := postForm(t, h, "/watch/new", url.Values{"name": {"src-" + strings.Repeat("x", n)}, "source": {bad}})
		if resp.StatusCode != 400 {
			t.Errorf("server accepted source %q (status %d)", bad, resp.StatusCode)
		}
	}
	// The demo cameras need nothing installed, so the server is the
	// reference for them too.
	for _, ok := range []string{"http://cam/snap.jpg", "https://cam/snap.jpg", "demo:printer", "demo: sevenseg"} {
		n++
		resp, _ := postForm(t, h, "/watch/new", url.Values{"name": {"src-" + strings.Repeat("x", n)}, "source": {ok}})
		if resp.StatusCode != 303 {
			t.Errorf("server rejected source %q (status %d)", ok, resp.StatusCode)
		}
	}
}

// patternOf pulls an input's pattern attribute out of rendered HTML and
// compiles it the way a browser does: anchored at both ends.
func patternOf(t *testing.T, body, id string) *regexp.Regexp {
	t.Helper()
	i := strings.Index(body, `id="`+id+`"`)
	if i < 0 {
		t.Fatalf("no input #%s in body:\n%s", id, body)
	}
	tag := body[i:]
	tag = tag[:strings.Index(tag, ">")]
	m := regexp.MustCompile(` pattern="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		t.Fatalf("input #%s has no pattern: %s", id, tag)
	}
	re, err := regexp.Compile(`^(?:` + html.UnescapeString(m[1]) + `)$`)
	if err != nil {
		t.Fatalf("input #%s pattern does not compile as RE2 (%v): %s", id, err, m[1])
	}
	return re
}
