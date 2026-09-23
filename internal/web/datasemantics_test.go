package web

import (
	"context"
	"image"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
)

// A watch name reaches a URL escaped everywhere (links, form actions, the
// snapshot src and every redirect), so names with '%', '\', spaces, '|' or
// non-ASCII open, save and delete like any other. "." and ".." can't be
// addressed at all and are refused up front.
func TestOddWatchNamesStayAddressable(t *testing.T) {
	for _, name := range []string{"pct 100%", "a%20b", `bs2\x`, "sp ace", "ünï", "a|b", "a..b"} {
		t.Run(name, func(t *testing.T) {
			s, cfgPath := newTestServer(t)
			s.BasePath = "/wg"
			h := s.Handler()
			esc := url.PathEscape(name)

			resp, body := postForm(t, h, "/watch/new", url.Values{"name": {name}, "source": {"http://cam2/snap.jpg"}})
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("create status = %d; body:\n%s", resp.StatusCode, body)
			}
			loc := resp.Header.Get("Location")
			if loc != "/wg/watch/"+esc {
				t.Fatalf("create Location = %q, want %q", loc, "/wg/watch/"+esc)
			}
			// The router sees the path without the base path, as behind a proxy.
			detailPath := strings.TrimPrefix(loc, "/wg")
			dresp, detail := getWithCookies(t, h, detailPath, []*http.Cookie{flashCookieFrom(t, resp)})
			if dresp.StatusCode != 200 {
				t.Fatalf("GET %s = %d", detailPath, dresp.StatusCode)
			}
			for _, want := range []string{
				`src="/wg/watch/` + esc + `/snapshot"`,
				`action="/wg/watch/` + esc + `/save"`,
				// The flash names the watch whole, '|' included.
				`Created <strong class="mono">` + htmlEscape(name) + `</strong>.`,
			} {
				if !strings.Contains(detail, want) {
					t.Errorf("detail missing %s", want)
				}
			}
			for _, sub := range []string{"/snapshot", "/live"} {
				if r, _ := get(t, h, detailPath+sub); r.StatusCode != 200 {
					t.Errorf("GET %s%s = %d", detailPath, sub, r.StatusCode)
				}
			}

			form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"},
				"tthreshold": {"10"}, "confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"}}
			resp, body = postForm(t, h, detailPath+"/save", form)
			if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != loc {
				t.Errorf("save: status %d Location %q, want 303 %q; body:\n%s", resp.StatusCode, resp.Header.Get("Location"), loc, body)
			}

			_, index := get(t, h, "/")
			for _, want := range []string{`href="/wg/watch/` + esc + `"`, `action="/wg/watch/` + esc + `/delete"`} {
				if !strings.Contains(index, want) {
					t.Errorf("index missing %s", want)
				}
			}
			if resp, _ := postForm(t, h, detailPath+"/delete", url.Values{}); resp.StatusCode != http.StatusSeeOther {
				t.Errorf("delete status = %d", resp.StatusCode)
			}
			got, err := config.Load(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Watches) != 1 {
				t.Errorf("watch should be gone after delete, have %d watches", len(got.Watches))
			}
		})
	}

	s, cfgPath := newTestServer(t)
	for _, name := range []string{".", ".."} {
		resp, body := postForm(t, s.Handler(), "/watch/new", url.Values{"name": {name}, "source": {"http://cam2/snap.jpg"}})
		if resp.StatusCode != 400 || !strings.Contains(body, `<p id="err-name" class="field-error">A name can&#39;t be just`) {
			t.Errorf("name %q: status %d, want 400 with the error under Name; body:\n%s", name, resp.StatusCode, body)
		}
	}
	if got, _ := config.Load(cfgPath); len(got.Watches) != 1 {
		t.Errorf("rejected names must not be written, have %d watches", len(got.Watches))
	}
}

// htmlEscape is html/template's escaping of plain text for the few
// characters the names above use.
func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(s)
}

// A restart failure's "Back to" link must lead back to the same watch.
func TestRestartFailureBackLinkIsEscaped(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	if resp, body := postForm(t, h, "/watch/new", url.Values{"name": {"pct 100%"}, "source": {"http://cam2/snap.jpg"}}); resp.StatusCode != 303 {
		t.Fatalf("create = %d; %s", resp.StatusCode, body)
	}
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"},
		"tthreshold": {"10"}, "confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"}, "notify": {"nope://x"}}
	resp, body := postForm(t, h, "/watch/pct%20100%25/save", form)
	if resp.StatusCode != 500 || !strings.Contains(body, `href="/watch/pct%20100%25"`) {
		t.Errorf("restart failure page should link back to the escaped watch URL; status %d body:\n%s", resp.StatusCode, body)
	}
}

func TestViewReading(t *testing.T) {
	cases := []struct {
		engine, reading string
		want            readingView
	}{
		{"sevenseg", "23.5", readingView{Runs: []readingRun{{Text: "23.5"}}}},
		{"sevenseg", "?", readingView{Unreadable: true, Placeholder: "No digits read"}},
		{"sevenseg", "?.?", readingView{Unreadable: true, Placeholder: "No digits read"}},
		{"sevenseg", "?4?", readingView{Partial: true, Runs: []readingRun{{"?", true}, {"4", false}, {"?", true}}}},
		{"sevenseg", "1??.5", readingView{Partial: true, Runs: []readingRun{{"1", false}, {"??", true}, {".5", false}}}},
		{"", "Ready?", readingView{Runs: []readingRun{{Text: "Ready?"}}}},
		{"rapidocr", "?", readingView{Runs: []readingRun{{Text: "?"}}}},
		{"rapidocr", "", readingView{Placeholder: "Nothing read"}},
	}
	for _, c := range cases {
		got := viewReading(c.engine, c.reading)
		if got.Partial != c.want.Partial || got.Unreadable != c.want.Unreadable || got.Placeholder != c.want.Placeholder || len(got.Runs) != len(c.want.Runs) {
			t.Errorf("viewReading(%q, %q) = %+v, want %+v", c.engine, c.reading, got, c.want)
			continue
		}
		for i := range got.Runs {
			if got.Runs[i] != c.want.Runs[i] {
				t.Errorf("viewReading(%q, %q) run %d = %+v, want %+v", c.engine, c.reading, i, got.Runs[i], c.want.Runs[i])
			}
		}
	}
}

// startBlocked makes printer count as running without it adding samples.
func startBlocked(t *testing.T, s *Server, engine string) {
	t.Helper()
	s.mu.Lock()
	s.cfg.Watches[0].Engine = engine
	s.mu.Unlock()
	s.sup.NewSource = func(w config.Watch) (source.Source, error) { return blockingSource{}, nil }
	wc, _ := s.findWatch("printer")
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.sup.Stop("printer") })
}

// The seven-segment decoder's '?' is a digit it couldn't read: a reading of
// nothing but '?' is named in words with a half-lit LED, a partial one
// marks the unread digits, and neither looks like a clean reading. Text
// engines' question marks are left alone.
func TestUnreadableSevenSegReadings(t *testing.T) {
	s, _ := newTestServer(t)
	startBlocked(t, s, "sevenseg")
	h := s.Handler()

	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "?", PNG: pngBytes(t)})
	_, index := get(t, h, "/")
	row := index[strings.Index(index, `data-label="Last reading"`):]
	if !strings.Contains(row, "led-unsure") || strings.Contains(row, "led-green") || !strings.Contains(row, `<span class="reading-none" title="The seven-segment decoder couldn't read any digit.`) || !strings.Contains(row, ">No digits read</span>") {
		t.Errorf("an all-? reading should be named, with a half-lit LED; row:\n%s", row)
	}
	_, live := get(t, h, "/watch/printer/live")
	if !strings.Contains(live, "led-unsure") || strings.Contains(live, "led-green") ||
		!strings.Contains(live, `<strong class="readout-value reading-none">No digits read</strong>`) ||
		!strings.Contains(live, `class="readout-hint">The seven-segment decoder couldn't read any digit.`) ||
		!strings.Contains(live, `<figcaption class="caption-none" title="No digits read"><span class="cap-body"><span class="cap-text">No digits read</span></span></figcaption>`) {
		t.Errorf("live readout should name an unreadable reading and say what to do; body:\n%s", live)
	}

	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "?4?", PNG: pngBytes(t)})
	unknown := `<span class="digit-unknown" title="Digit not read">?</span>`
	marked := unknown + "4" + unknown + `<span class="sr-only"> (some digits not read)</span>`
	_, index = get(t, h, "/")
	if row := index[strings.Index(index, `data-label="Last reading"`):]; !strings.Contains(row, `<span class="mono">`+marked+`</span>`) || !strings.Contains(row, "led-green") {
		t.Errorf("a partial reading keeps its digits, marks the unread ones; row:\n%s", row)
	}
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, `<strong class="readout-value mono">`+marked+`</strong>`) || !strings.Contains(live, "marks a digit the seven-segment decoder couldn't read") {
		t.Errorf("live readout should mark unread digits and explain the mark; body:\n%s", live)
	}

	// Another engine: '?' is text.
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "rapidocr"
	s.mu.Unlock()
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "Ready?", PNG: pngBytes(t)})
	_, index = get(t, h, "/")
	_, live = get(t, h, "/watch/printer/live")
	if strings.Contains(index+live, "digit-unknown") || strings.Contains(index+live, "led-unsure") || !strings.Contains(live, `<strong class="readout-value mono">Ready?</strong>`) {
		t.Errorf("a text engine's question mark is just text; live:\n%s", live)
	}
}

// Durations read the way people write them in the table and the form.
func TestDurationsShowWithoutTrailingZeroUnits(t *testing.T) {
	s, _ := newTestServer(t)
	s.mu.Lock()
	s.cfg.Watches[0].Interval = config.Duration(time.Minute)
	s.cfg.Watches[0].MaxInterval = config.Duration(time.Hour)
	s.cfg.Watches[0].Trigger.Cooldown = config.Duration(5 * time.Minute)
	s.mu.Unlock()
	_, index := get(t, s.Handler(), "/")
	if !strings.Contains(index, `data-label="Interval">1m</td>`) {
		t.Errorf("interval column should read 1m; body:\n%s", index)
	}
	_, detail := get(t, s.Handler(), "/watch/printer")
	for _, want := range []string{`name="cooldown" value="5m" placeholder="e.g. 30s, 5m"`, `name="interval" value="1m" placeholder="e.g. 30s, 5m"`, `name="max_interval" value="1h"`} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %s", want)
		}
	}
	if strings.Contains(index+detail, "m0s") {
		t.Error("no duration should print a trailing 0s")
	}
}

func TestConfidencePct(t *testing.T) {
	for c, want := range map[float64]int{99.996: 99, 100: 100, 59.9: 59, 60: 60, 30: 30, 0: 0, -1: 0} {
		if got := confidencePct(c); got != want {
			t.Errorf("confidencePct(%v) = %d, want %d", c, got, want)
		}
	}
}

type lowConfEngine struct{}

func (lowConfEngine) Recognize(ctx context.Context, img image.Image) (string, error) {
	return "?4", nil
}
func (lowConfEngine) RecognizeWords(ctx context.Context, img image.Image) (string, []ocr.Word, error) {
	return "?4", []ocr.Word{{Text: "?", Conf: 30}, {Text: "4", Conf: 59.9}}, nil
}

// Confidences are whole percents rounded down, labelled once, and a low one
// is marked in words for screen readers rather than with a "!" glyph.
func TestTestResultConfidenceChips(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	s.engines.RapidOCR = fakeRapid{}
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"ocr_match"}, "pattern": {"x"}, "engine": {"rapidocr"}}
	_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, `PRINTER-01<span class="conf">99%</span>`) || strings.Contains(body, "(99.") || strings.Contains(body, "100%") {
		t.Errorf("chips should show floor percents; body:\n%s", body)
	}
	if !strings.Contains(body, "<dt>Confidence</dt>") || strings.Contains(body, `title="confidence`) {
		t.Errorf("confidence should be labelled in the page, not only in a title; body:\n%s", body)
	}

	s.engines.RapidOCR = lowConfEngine{}
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	low := `<span class="word-chip conf-low"><span class="sr-only">Low confidence: </span>`
	if n := strings.Count(body, low); n != 2 {
		t.Errorf("59.9 and 30 are both under the line (as 59%% and 30%%), want 2 low chips, got %d; body:\n%s", n, body)
	}
	if !strings.Contains(body, `4<span class="conf">59%</span>`) {
		t.Errorf("59.9 shows as 59%%; body:\n%s", body)
	}
}

// The test result is one framed panel: a heading, what read it and when,
// a way to clear it, a stale notice for app.js to reveal, and the verdict
// before the evidence.
func TestTestResultPanelHeadAndVerdict(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	base := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "confirm": {"2"}}
	with := func(kv ...string) url.Values {
		v := url.Values{}
		for k, vs := range base {
			v[k] = vs
		}
		for i := 0; i < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}

	_, body := postForm(t, h, "/watch/printer/test", with("ttype", "ocr_match", "pattern", "(?i)print complete"))
	for _, want := range []string{
		`<section class="test-panel" aria-labelledby="test-title">`,
		`<h2 id="test-title" class="test-title">Test result</h2>`,
		`<p class="test-meta mono"><span>tesseract</span>`,
		`<time datetime="`,
		`data-dismiss-test aria-label="Clear test result"`,
		`<p class="test-stale">`,
		`<p class="verdict verdict-met">`,
		`<strong class="verdict-title">Condition met</strong> <span class="verdict-detail">The text matches the pattern. The watch fires after 2 readings like this in a row, unless it was already met or Cooldown is running.</span>`,
		`<dt>Read</dt>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ocr_match met: missing %s; body:\n%s", want, body)
		}
	}
	if strings.Index(body, "verdict-met") > strings.Index(body, "crop-frame") {
		t.Error("the verdict should come before the crop")
	}

	_, body = postForm(t, h, "/watch/printer/test", with("ttype", "ocr_match", "pattern", "^idle$"))
	if !strings.Contains(body, `<p class="verdict verdict-unmet">`) || !strings.Contains(body, "Condition not met</strong> <span class=\"verdict-detail\">The text doesn&#39;t match the pattern.</span>") {
		t.Errorf("ocr_match not met; body:\n%s", body)
	}
	_, body = postForm(t, h, "/watch/printer/test", with("ttype", "numeric", "op", "gt", "tthreshold", "5"))
	if !strings.Contains(body, "No number found in the reading.") {
		t.Errorf("numeric without a number; body:\n%s", body)
	}
	_, body = postForm(t, h, "/watch/printer/test", with("ttype", "numeric", "op", "gt", "tthreshold", "five"))
	if !strings.Contains(body, `verdict-invalid`) || !strings.Contains(body, "Threshold must be a number.") {
		t.Errorf("an unparseable threshold can't be checked; body:\n%s", body)
	}
	_, body = postForm(t, h, "/watch/printer/test", with("ttype", "ocr_match", "pattern", "("))
	if !strings.Contains(body, `verdict-invalid`) || !strings.Contains(body, "Pattern isn&#39;t a valid regular expression. A &#34;(&#34; is never closed.</span>") || !strings.Contains(body, "led-error") {
		t.Errorf("a bad pattern is reported, not evaluated; body:\n%s", body)
	}

	// pixel_change: no engine runs, nothing is "read", and one frame has
	// nothing to compare with.
	_, body = postForm(t, h, "/watch/printer/test", with("ttype", "pixel_change", "tthreshold", "10"))
	if !strings.Contains(body, `<p class="test-meta mono"><time datetime="`) || strings.Contains(body, "<dt>Read</dt>") ||
		!strings.Contains(body, `verdict-info`) || !strings.Contains(body, "Nothing to compare yet") || strings.Contains(body, "tesseract") {
		t.Errorf("pixel_change test: time only, no reading, an info verdict; body:\n%s", body)
	}
}

// A sevenseg test with undecoded digits marks them and explains the '?'
// once; the same text from a text engine is just text.
func TestTestResultExplainsUndecodedDigits(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	s.engines.SevenSeg = lowConfEngine{}
	s.engines.RapidOCR = lowConfEngine{}
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"numeric"}, "op": {"gt"}, "tthreshold": {"24"}, "pattern": {"([0-9.]+)"}, "engine": {"sevenseg"}}
	_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	for _, want := range []string{
		`<span class="mono test-reading"><span class="digit-unknown" title="Digit not read">?</span>4<span class="sr-only"> (some digits not read)</span></span>`,
		`marks a digit the seven-segment decoder couldn't read`,
		`<p class="test-meta mono"><span>sevenseg</span>`,
		// "?4" of what may be 24.5 isn't "not met": it's a partial read.
		`<p class="verdict verdict-info">`,
		`<strong class="verdict-title">Partial reading</strong> <span class="verdict-detail">Some digits weren&#39;t read, so this only checked part of the number: 4 is not above 24.</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sevenseg partial read: missing %s; body:\n%s", want, body)
		}
	}
	if n := strings.Count(body, "seven-segment decoder couldn"); n != 1 {
		t.Errorf("explain the mark once, got %d; body:\n%s", n, body)
	}

	if strings.Contains(body, "verdict-unmet") || strings.Contains(body, "verdict-met") {
		t.Errorf("a partial read must not get a met/unmet LED; body:\n%s", body)
	}

	// The same caveat for a pattern checked against a partial read.
	form.Set("ttype", "ocr_match")
	form.Set("pattern", "4")
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, `verdict-info`) || !strings.Contains(body, "only checked part of the number: the text matches the pattern.") {
		t.Errorf("ocr_match on a partial read; body:\n%s", body)
	}

	// A text engine's '?' is just text, so its verdict stands.
	form.Set("ttype", "numeric")
	form.Set("pattern", "([0-9.]+)")
	form.Set("engine", "rapidocr")
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if strings.Contains(body, `class="mono test-reading"><span class="digit-unknown"`) || strings.Contains(body, "seven-segment") {
		t.Errorf("rapidocr's '?' is text, not an undecoded digit; body:\n%s", body)
	}
	if !strings.Contains(body, `<p class="verdict verdict-unmet">`) || !strings.Contains(body, "4 is not above 24.") || strings.Contains(body, "Partial reading") {
		t.Errorf("rapidocr's reading is judged as read; body:\n%s", body)
	}

	// No digit read at all: "no number found, not met" would claim the
	// display is below the threshold; it says nothing was read instead.
	s.engines.SevenSeg = readsAs("?.?")
	form.Set("engine", "sevenseg")
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, `<p class="verdict verdict-info">`) || !strings.Contains(body, `<strong class="verdict-title">No digits read</strong>`) || strings.Contains(body, "No number found") {
		t.Errorf("an unreadable sevenseg test has no verdict; body:\n%s", body)
	}
	// ...but a text engine that read nothing numeric is a plain "not met".
	s.engines.RapidOCR = readsAs("Ready?")
	form.Set("engine", "rapidocr")
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, `<p class="verdict verdict-unmet">`) || !strings.Contains(body, "No number found in the reading.") {
		t.Errorf("rapidocr without a number; body:\n%s", body)
	}
}

// readsAs is an engine that always reads the same text.
type readsAs string

func (r readsAs) Recognize(ctx context.Context, img image.Image) (string, error) {
	return string(r), nil
}
