package web

import (
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

// A19: a watch as Create left it offers the three presets above Engine,
// and Engine comes before Type (always shown); a configured watch doesn't
// get the presets.
func TestFreshWatchOffersPresetsAndEngineComesFirst(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	if resp, body := postForm(t, h, "/watch/new", url.Values{"name": {"cam"}, "source": {"demo:printer"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	_, body := get(t, h, "/watch/cam")
	for _, want := range []string{
		`<div class="presets" role="group" aria-labelledby="presets-title">`,
		`<p id="presets-title" class="presets-title">What are you watching?</p>`,
		`data-preset="text" aria-pressed="false"><span class="preset-name">Status text</span>`,
		`data-preset="digits" aria-pressed="false"><span class="preset-name">Digit display</span>`,
		`data-preset="change" aria-pressed="false"><span class="preset-name">Any change</span>`,
		`<p id="preset-note" class="field-hint preset-note" aria-live="polite">Fills in the fields below. Nothing is saved until you press Save.</p>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fresh watch page missing %q", want)
		}
	}
	presets, engine, ttype := strings.Index(body, `class="presets"`), strings.Index(body, `id="row-engine"`), strings.Index(body, `id="f-ttype"`)
	if !(presets < engine && engine < ttype) {
		t.Errorf("order presets %d < Engine row %d < Type %d broken", presets, engine, ttype)
	}
	if row := body[engine : strings.Index(body[engine:], ">")+engine]; strings.Contains(row, "hidden") {
		t.Errorf("the Engine row must always show: %s", row)
	}
	// A pixel_change watch (Create's default) sets the row aside; a text
	// watch doesn't.
	if !strings.Contains(body, `<div class="field-row is-unused" id="row-engine">`) {
		t.Error("a pixel_change watch should set the Engine row aside (is-unused)")
	}
	s.mu.Lock()
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "ocr_match", Pattern: "DONE"}
	s.mu.Unlock()
	if _, pb := get(t, h, "/watch/printer"); !strings.Contains(pb, `id="row-engine"`) || strings.Contains(pb, `is-unused" id="row-engine"`) {
		t.Error("an ocr_match watch uses Engine: its row is not set aside")
	}
	// A configured watch (the test server's printer: threshold 10) has none.
	if _, body := get(t, h, "/watch/printer"); strings.Contains(body, `class="presets"`) {
		t.Error("a configured watch should not offer presets")
	}
	// No engine that reads text: Status text is offered but disabled.
	s2, _ := newTestServerWith(t, ocr.Engines{})
	postForm(t, s2.Handler(), "/watch/new", url.Values{"name": {"cam"}, "source": {"demo:printer"}})
	_, body = get(t, s2.Handler(), "/watch/cam")
	if !strings.Contains(body, `data-preset="text" aria-pressed="false" disabled title="Reading text needs tesseract, and it isn't installed">`) ||
		!strings.Contains(body, "Needs tesseract (not installed)") {
		t.Error("Status text should be disabled and say why without a text engine")
	}
	// app.js fills the fields through their events; the preset values are
	// the spec's, and every preset sets all six trigger fields (the page's
	// starting value where it names none), so what an earlier preset left
	// behind never carries over.
	js, err := readSourceLF("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`ttype: "ocr_match", pattern: "(?i)complete|done|error", confirm: "2"`,
		`engine: "sevenseg", ttype: "numeric", confirm: "2", op: "gt"`,
		`engine: "tesseract", ttype: "pixel_change", tthreshold: "20"`,
		`var PRESET_FIELDS = ["engine", "ttype", "pattern", "confirm", "tthreshold", "op"];`,
		`setField(name, p[name] !== undefined ? p[name] : presetBase[name]);`,
		`f.dispatchEvent(new Event("change", { bubbles: true }));`,
		`if (!btn || btn.disabled || retired) return;`,
		`if (rowEngine) rowEngine.classList.toggle("is-unused", pixel);`,
	} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	if strings.Contains(string(js), "rowEngine.hidden") {
		t.Error("the Engine row is never hidden any more")
	}
}

func TestIsFresh(t *testing.T) {
	fresh := config.Watch{Trigger: config.Trigger{Type: "pixel_change", Threshold: 25, Cooldown: config.Duration(5 * time.Minute), Confirm: 3}}
	if !isFresh(fresh) {
		t.Fatal("Create's defaults should be fresh")
	}
	for name, mod := range map[string]func(*config.Watch){
		"threshold":  func(w *config.Watch) { w.Trigger.Threshold = 20 },
		"type":       func(w *config.Watch) { w.Trigger.Type = "ocr_match" },
		"engine":     func(w *config.Watch) { w.Engine = "sevenseg" },
		"cooldown":   func(w *config.Watch) { w.Trigger.Cooldown = 0 },
		"confirm":    func(w *config.Watch) { w.Trigger.Confirm = 2 },
		"preprocess": func(w *config.Watch) { w.Preprocess.Grayscale = true },
	} {
		w := fresh
		mod(&w)
		if isFresh(w) {
			t.Errorf("changed %s but still fresh", name)
		}
	}
}

// A06/A19: Confirm's hint says what it counts for the type, and Pattern's
// says how to write one; app.js carries the same sentences.
func TestConfirmAndPatternHelpPerType(t *testing.T) {
	s, _ := newTestServer(t)
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "sevenseg"
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Confirm: 2}
	s.mu.Unlock()
	_, body := get(t, s.Handler(), "/watch/printer")
	for _, want := range []string{
		`<div class="field-row" id="row-confirm">`,
		`<p id="confirm-help" class="field-hint">Readings in a row that must be on the same side of the threshold. Blank means 3.</p>`,
		`placeholder="e.g. (?i)print complete"`,
		`aria-describedby="pattern-help"`,
		`<p id="pattern-help" class="field-hint">Leave it empty to use the first number read.</p>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("numeric watch page missing %q", want)
		}
	}
	js, err := readSourceLF("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	jsText := strings.ReplaceAll(string(js), `\\`, `\`)
	for typ, text := range confirmHelps {
		if !strings.Contains(jsText, typ+`: "`+text+`"`) {
			t.Errorf("app.js CONFIRM_HELP for %s differs from confirmHelps: %q", typ, text)
		}
	}
	for typ, text := range patternHelps {
		if !strings.Contains(jsText, typ+`: "`+text+`"`) {
			t.Errorf("app.js PATTERN_HELP for %s differs from patternHelps: %q", typ, text)
		}
	}
	if got := patternHelp("ocr_match"); got != `Plain words work; <span class="mono">(?i)</span> ignores case; <span class="mono">a|b</span> matches either.` {
		t.Errorf("ocr_match pattern hint = %q", got)
	}
	if !strings.Contains(string(js), `if (rowConfirm) rowConfirm.hidden = t === "pixel_change";`) {
		t.Error("app.js should hide Confirm for pixel_change, which doesn't use it")
	}
}

// A16: a reading on its way to firing shows how far along Confirm it is,
// in the readout and on its strip frame, and each step is its own frame.
func TestLiveShowsConfirmProgress(t *testing.T) {
	s, _ := newTestServer(t)
	s.mu.Lock()
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "ocr_match", Pattern: "(?i)complete", Confirm: 3}
	s.mu.Unlock()
	t0 := time.Unix(1700000000, 0)
	png := []byte("png")
	s.reg.Add("printer", state.Sample{TS: t0, Reading: "PRINTING 90%", PNG: png})
	s.reg.Add("printer", state.Sample{TS: t0.Add(2 * time.Second), Reading: "PRINT COMPLETE", PNG: png, Pending: 1, Need: 3})
	s.reg.Add("printer", state.Sample{TS: t0.Add(4 * time.Second), Reading: "PRINT COMPLETE", PNG: png, Pending: 2, Need: 3})
	_, body := get(t, s.Handler(), "/watch/printer/live")
	for _, want := range []string{
		`<p class="readout-progress"><span class="progress-steps" aria-hidden="true"><span class="step is-on"></span><span class="step is-on"></span><span class="step"></span></span><span class="progress-text">Matches the pattern: 2 of 3 readings in a row needed to fire.</span></p>`,
		`<span class="tile-progress">2 of 3<span class="sr-only"> needed to fire</span></span>`,
		`<span class="tile-progress">1 of 3<span class="sr-only"> needed to fire</span></span>`,
		`title="PRINT COMPLETE (2 of 3 needed to fire)"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("live fragment missing %q; body:\n%s", want, body)
		}
	}
	// The fire itself carries no progress line.
	s.reg.Add("printer", state.Sample{TS: t0.Add(6 * time.Second), Reading: "PRINT COMPLETE", PNG: png, Fired: true})
	if _, body := get(t, s.Handler(), "/watch/printer/live"); strings.Contains(body, "readout-progress") {
		t.Error("a fired reading shows no confirm progress")
	}
	if n := len(collapseSamples(s.reg.Recent("printer"))); n != 4 {
		t.Errorf("tiles = %d, want 4: steps towards Confirm are separate frames", n)
	}
	num := config.Trigger{Type: "numeric", Op: "lt", Threshold: 4.5}
	if p := confirmProgress(num, state.Sample{Pending: 1, Need: 2}); p == nil || p.Text != "Below 4.5: 1 of 2 readings in a row needed to fire." || len(p.Steps) != 2 {
		t.Errorf("numeric progress = %+v", p)
	}
	if p := confirmProgress(num, state.Sample{Pending: 3, Need: 20}); p == nil || p.Steps != nil {
		t.Errorf("a long Confirm is told in words only: %+v", p)
	}
	if confirmProgress(num, state.Sample{}) != nil {
		t.Error("no progress on a reading that isn't pending")
	}
}

// A20: the Certificate checkbox shows for https sources, saves as
// tls_insecure only when the page showed it, applies to Test before a
// save, and the header says so while it's on.
func TestCertificateCheckboxAndChip(t *testing.T) {
	s, cfgPath := newTestServer(t)
	h := s.Handler()
	if _, body := get(t, h, "/watch/printer"); strings.Contains(body, `id="f-tls"`) {
		t.Error("an http:// source has no certificate to skip")
	}
	s.mu.Lock()
	s.cfg.Watches[0].Source = "https://user:pw@192.168.1.20/snap"
	s.mu.Unlock()
	_, body := get(t, h, "/watch/printer")
	for _, want := range []string{
		`<label class="checkbox"><input type="checkbox" id="f-tls" name="tls_insecure" aria-describedby="tls-help"> Don't check it</label>`,
		`<input type="hidden" name="tls_shown" value="1">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("https watch page missing %q", want)
		}
	}
	if strings.Contains(body, "source-flag") {
		t.Error("no chip while the certificate is checked")
	}

	// Test uses the box as it is on the form.
	var tested config.Watch
	s.NewSource = func(w config.Watch) (source.Source, error) {
		tested = w
		return &fakeSource{img: testImage()}, nil
	}
	test := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"}, "tthreshold": {"10"}, "tls_shown": {"1"}, "tls_insecure": {"on"}}
	postForm(t, h, "/watch/printer/test", test)
	if !tested.TLSInsecure {
		t.Error("Test should try the camera with the ticked box")
	}

	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"pixel_change"}, "tthreshold": {"10"}, "confirm": {"3"}, "cooldown": {"0s"}, "interval": {"5s"},
		"tls_shown": {"1"}, "tls_insecure": {"on"},
	}
	if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	if got, _ := config.Load(cfgPath); !got.Watches[0].TLSInsecure {
		t.Fatal("ticked box should save tls_insecure: true")
	}
	_, body = get(t, h, "/watch/printer")
	if !strings.Contains(body, `certificate not checked</span>`) || !strings.Contains(body, `name="tls_insecure" aria-describedby="tls-help" checked>`) {
		t.Error("the header chip and the ticked box should show while tls_insecure is on")
	}
	// A client that never showed the box keeps the setting.
	form.Del("tls_shown")
	form.Del("tls_insecure")
	postForm(t, h, "/watch/printer/save", form)
	if got, _ := config.Load(cfgPath); !got.Watches[0].TLSInsecure {
		t.Error("a save without the box on the page must keep tls_insecure")
	}
	// Unticked on the page: off.
	form.Set("tls_shown", "1")
	postForm(t, h, "/watch/printer/save", form)
	if got, _ := config.Load(cfgPath); got.Watches[0].TLSInsecure {
		t.Error("an unticked box should turn tls_insecure off")
	}
}

func TestCertificateErrorIsExplained(t *testing.T) {
	msg := "grab: snapshot https://user:xxxxx@192.168.1.20/snap: " + source.ErrCertNotTrusted.Error() + ` (Get "https://192.168.1.20/snap": tls: failed to verify certificate: x509: certificate signed by unknown authority)`
	if got := summarizeErr(msg); got != "192.168.1.20's certificate isn't trusted" {
		t.Errorf("summary = %q", got)
	}
	if got := errHint(msg); !strings.Contains(got, "tick Certificate: Don't check it") {
		t.Errorf("hint = %q", got)
	}
	if got := summarizeErr(msg); strings.Contains(got, "user") {
		t.Errorf("summary shows credentials: %q", got)
	}
}

// The detail header shows the source without its password: the page has
// no field that edits the source, and the header is what gets screenshot.
func TestDetailHeaderHidesTheCameraPassword(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	s.mu.Lock()
	s.cfg.Watches[0].Source = "https://admin:pw-9f3kq@192.168.1.20/cgi-bin/snapshot.cgi?channel=1"
	s.cfg.Watches[0].TLSInsecure = true
	s.mu.Unlock()
	_, body := get(t, h, "/watch/printer")
	if strings.Contains(body, "pw-9f3kq") {
		t.Error("the detail page shows the camera password")
	}
	if !strings.Contains(body, `<p class="source-line muted mono">https://admin:xxxxx@192.168.1.20/cgi-bin/snapshot.cgi?channel=1 <span class="source-flag"`) {
		t.Error("the header should show the source with the password masked")
	}
}

func TestRedactSource(t *testing.T) {
	for in, want := range map[string]string{
		"http://cam/snap.jpg":                            "http://cam/snap.jpg",
		"http://admin:s3cret@cam/snap.jpg":               "http://admin:xxxxx@cam/snap.jpg",
		"http://admin@cam/snap.jpg":                      "http://admin@cam/snap.jpg",
		"rtsp://u:p%40ss@10.0.0.9:554/stream1":           "rtsp://u:xxxxx@10.0.0.9:554/stream1",
		"ffmpeg:-rtsp_transport tcp -i rtsp://u:pw@h/s1": "ffmpeg:-rtsp_transport tcp -i rtsp://u:xxxxx@h/s1",
		"demo:printer":                                   "demo:printer",
		"v4l2:/dev/video0":                               "v4l2:/dev/video0",
		// A raw "@" (or ":") in the password: Go splits at the last "@".
		"http://admin:p@ss@127.0.0.1:18286/snap":               "http://admin:xxxxx@127.0.0.1:18286/snap",
		"rtsp://u:a:b@c@10.0.0.9:554/s?x=1@2":                  "rtsp://u:xxxxx@10.0.0.9:554/s?x=1@2",
		"ffmpeg:-i rtsp://u:p@x@h/s1 -i http://v:q@k/y@2x.jpg": "ffmpeg:-i rtsp://u:xxxxx@h/s1 -i http://v:xxxxx@k/y@2x.jpg",
		"http://cam:8080/snap@2x.jpg":                          "http://cam:8080/snap@2x.jpg",
		"http://cam/cgi?user=a:b@c":                            "http://cam/cgi?user=a:b@c",
	} {
		if got := redactSource(in); got != want {
			t.Errorf("redactSource(%q) = %q, want %q", in, got, want)
		}
		// For a plain URL, the page must agree with the grab errors,
		// which use net/url.
		if u, err := url.Parse(in); err == nil && u.Scheme != "" && u.Host != "" {
			if got := redactSource(in); got != u.Redacted() {
				t.Errorf("redactSource(%q) = %q, url.Redacted = %q", in, got, u.Redacted())
			}
		}
	}
}

// A16 (fixer): while Cooldown runs, the progress line says the alert is
// held and until when, and the reading that completes Confirm reads
// "held", not a silent 3 of 3 that never fires.
func TestLiveConfirmProgressDuringCooldown(t *testing.T) {
	s, _ := newTestServer(t)
	s.mu.Lock()
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "ocr_match", Pattern: "(?i)complete", Confirm: 3, Cooldown: config.Duration(5 * time.Minute)}
	s.mu.Unlock()
	t0 := time.Date(2026, 9, 24, 14, 0, 0, 0, time.Local)
	ends := t0.Add(4 * time.Minute)
	png := []byte("png")
	s.reg.Add("printer", state.Sample{TS: t0, Reading: "PRINT COMPLETE", PNG: png, Pending: 2, Need: 3, CooldownEnds: ends})
	_, body := get(t, s.Handler(), "/watch/printer/live")
	want := `<span class="progress-text">Matches the pattern: 2 of 3 readings in a row. Cooldown holds any alert until <time class="mono" datetime="` + isoTime(ends) + `">14:04:00</time>.</span>`
	if !strings.Contains(body, want) {
		t.Errorf("live fragment missing %q; body:\n%s", want, body)
	}
	s.reg.Add("printer", state.Sample{TS: t0.Add(2 * time.Second), Reading: "PRINT COMPLETE", PNG: png, Pending: 3, Need: 3, CooldownEnds: ends})
	_, body = get(t, s.Handler(), "/watch/printer/live")
	for _, want := range []string{
		`<span class="progress-steps" aria-hidden="true"><span class="step is-on"></span><span class="step is-on"></span><span class="step is-on"></span></span><span class="progress-text">Matches the pattern. Cooldown holds the alert until <time class="mono" datetime="` + isoTime(ends) + `">14:04:00</time>; it goes out then if this still holds.</span>`,
		`<span class="tile-progress">held<span class="sr-only"> by Cooldown</span></span>`,
		`title="PRINT COMPLETE (held by Cooldown)"`,
		// The counting tiles inside the cooldown say it too: the Live
		// line says any alert is held, so "needed to fire" would be wrong.
		`<span class="tile-progress">2 of 3<span class="sr-only"> readings in a row,</span><span aria-hidden="true"> ·</span> held<span class="sr-only"> by Cooldown</span></span>`,
		`title="PRINT COMPLETE (2 of 3 readings in a row, held by Cooldown)"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("live fragment missing %q; body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "needed to fire") {
		t.Errorf("a tile inside the cooldown says it is needed to fire; body:\n%s", body)
	}
	// Confirm 1: nothing to count, but a held reading still says so.
	if pv := confirmProgress(config.Trigger{Type: "ocr_match"}, state.Sample{Pending: 1, Need: 1, CooldownEnds: ends}); pv == nil || pv.Until != ends || pv.Steps != nil {
		t.Errorf("confirm 1 held: %+v", pv)
	}
	if pv := confirmProgress(config.Trigger{Type: "ocr_match"}, state.Sample{Pending: 1, Need: 1}); pv != nil {
		t.Errorf("confirm 1 without a cooldown has no progress: %+v", pv)
	}
}
