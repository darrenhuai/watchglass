package web

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/runner"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
)

// The Letters setting (config.Watch.Letters) on the detail page: shown
// for a sevenseg watch, saved with it, honoured by Test from the unsaved
// form, and a letter shows as its own chip with its confidence.

func TestDetailShowsLettersForSevenSegOnly(t *testing.T) {
	s, _ := newTestServer(t)
	_, body := get(t, s.Handler(), "/watch/printer")
	if !strings.Contains(body, `name="letters"`) {
		t.Fatalf("detail page has no letters checkbox; body:\n%s", body)
	}
	if !strings.Contains(body, `id="row-letters" hidden`) {
		t.Errorf("letters row should be hidden for a tesseract watch; body:\n%s", body)
	}
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "sevenseg"
	s.cfg.Watches[0].Letters = true
	s.mu.Unlock()
	_, body = get(t, s.Handler(), "/watch/printer")
	if strings.Contains(body, `id="row-letters" hidden`) {
		t.Errorf("letters row should show for a sevenseg watch; body:\n%s", body)
	}
	if !strings.Contains(body, `name="letters" aria-describedby="letters-help" checked`) {
		t.Errorf("saved letters: true should render checked; body:\n%s", body)
	}
}

func TestSaveRoundTripsLetters(t *testing.T) {
	s, cfgPath := newTestServer(t)
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"ocr_match"}, "pattern": {`^E[0-9]+$`}, "engine": {"sevenseg"}, "letters": {"on"},
		"confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
	}
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Watches[0].Letters || got.Watches[0].Engine != "sevenseg" {
		t.Errorf("after save: engine %q letters %v, want sevenseg with letters", got.Watches[0].Engine, got.Watches[0].Letters)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "letters: true") {
		t.Errorf("letters: true should be in the file:\n%s", raw)
	}
	// Unticked: the checkbox isn't sent, and the key goes.
	form.Del("letters")
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	got, _ = config.Load(cfgPath)
	if got.Watches[0].Letters {
		t.Error("letters should be off after a save without the box ticked")
	}
	raw, _ = os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "letters") {
		t.Errorf("letters off should not be written:\n%s", raw)
	}
	// Ticked but the engine switched to tesseract: dropped, not refused.
	form.Set("letters", "on")
	form.Set("engine", "tesseract")
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("tesseract with letters: status = %d, body: %s", resp.StatusCode, body)
	}
	got, _ = config.Load(cfgPath)
	if got.Watches[0].Letters || got.Watches[0].Engine != "" {
		t.Errorf("after switching to tesseract: engine %q letters %v", got.Watches[0].Engine, got.Watches[0].Letters)
	}
	// A client that omits the engine field keeps the saved letters.
	s.mu.Lock()
	s.cfg.Watches[0].Engine, s.cfg.Watches[0].Letters = "sevenseg", true
	s.mu.Unlock()
	form.Del("engine")
	form.Del("letters")
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	got, _ = config.Load(cfgPath)
	if !got.Watches[0].Letters {
		t.Error("a save without the engine field should keep the saved letters")
	}
}

// Test reads with the form's Letters, saved or not, and shows each letter
// as a chip with its confidence.
func TestTestRegionHonoursLettersFromTheForm(t *testing.T) {
	img := recipeSample(t, "boiler-seven-segment-fault.jpg")
	s, _ := newTestServerWith(t, ocr.Engines{SevenSeg: ocr.NewSevenSeg()})
	s.NewSource = func(config.Watch) (source.Source, error) { return &fakeSource{img: img}, nil }
	boiler := config.Region{X: 0.3875, Y: 0.3111, W: 0.225, H: 0.3111}
	f := func(v float64) string { return fmt.Sprint(v) }
	form := url.Values{"x": {f(boiler.X)}, "y": {f(boiler.Y)}, "w": {f(boiler.W)}, "h": {f(boiler.H)},
		"engine": {"sevenseg"}, "ttype": {"ocr_match"}, "pattern": {`^E[0-9]+$`}, "confirm": {"2"}, "cooldown": {"0s"}}
	_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, `<span class="mono test-reading"><span class="digit-unknown" title="Digit not read">?</span>4`) {
		t.Errorf("without letters the E should read ?; body:\n%s", body)
	}
	form.Set("letters", "on")
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, `<span class="mono test-reading">E4</span>`) {
		t.Errorf("with letters the form should read E4; body:\n%s", body)
	}
	if !strings.Contains(body, `<span class="word-chip conf-ok">E<span class="conf">100%</span></span>`) {
		t.Errorf("the E should have its own chip at 100%%; body:\n%s", body)
	}
	if !strings.Contains(body, `<strong class="verdict-title">Condition met</strong>`) {
		t.Errorf("E4 should meet ^E[0-9]+$; body:\n%s", body)
	}
	// A numeric watch on the same frame skips the code, and the note says
	// why without telling the user to fix the box.
	form.Set("ttype", "numeric")
	form.Set("pattern", `^([0-9]{1,3})$`)
	form.Set("op", "lt")
	form.Set("tthreshold", "40")
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, `<strong class="verdict-title">Reading skipped</strong>`) || !strings.Contains(body, "A letter was read, so this is a code or a word, not a number") {
		t.Errorf("a numeric Test on E4 should be skipped as a code; body:\n%s", body)
	}
	if strings.Contains(body, "Tighten the region") {
		t.Errorf("a code read whole is not a box to fix; body:\n%s", body)
	}
	// Saved letters, a request without the engine field (curl, an older
	// client): read with the saved setting.
	s.mu.Lock()
	s.cfg.Watches[0].Engine, s.cfg.Watches[0].Letters = "sevenseg", true
	s.mu.Unlock()
	form.Del("engine")
	form.Del("letters")
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, `<span class="mono test-reading">E4</span>`) {
		t.Errorf("no engine field: should read with the saved letters; body:\n%s", body)
	}
}

// Test and a running watch with letters agree on the recipes' samples.
func TestTestAgreesWithTheRunningWatchOnLetters(t *testing.T) {
	washer := config.Region{X: 0.43, Y: 0.3222, W: 0.36, H: 0.2822}
	boiler := config.Region{X: 0.3875, Y: 0.3111, W: 0.225, H: 0.3111}
	for _, c := range []struct {
		name, sample string
		region       config.Region
		trig         config.Trigger
		read         string
		state, title string
	}{
		{"washer-finished End", "washer-end.jpg", washer, config.Trigger{Type: "ocr_match", Pattern: `^(0:00|End)$`, Confirm: 2}, "End", "met", "Condition met"},
		{"washer-almost-done End", "washer-end.jpg", washer, config.Trigger{Type: "numeric", Pattern: `^0:([0-9]{2})$`, Op: "lt", Threshold: 5, Confirm: 2}, "End", "skipped", "Reading skipped"},
		{"boiler-fault E4", "boiler-seven-segment-fault.jpg", boiler, config.Trigger{Type: "ocr_match", Pattern: `^E[0-9]+$`, Confirm: 3}, "E4", "met", "Condition met"},
		{"boiler-temp E4", "boiler-seven-segment-fault.jpg", boiler, config.Trigger{Type: "numeric", Pattern: `^([0-9]{1,3})$`, Op: "lt", Threshold: 40, Confirm: 3}, "E4", "skipped", "Reading skipped"},
		{"boiler-temp 52", "boiler-seven-segment-temp.jpg", boiler, config.Trigger{Type: "numeric", Pattern: `^([0-9]{1,3})$`, Op: "lt", Threshold: 40, Confirm: 3}, "52", "unmet", "Condition not met"},
	} {
		t.Run(c.name, func(t *testing.T) {
			img := recipeSample(t, c.sample)
			s, _ := newTestServerWith(t, ocr.Engines{SevenSeg: ocr.NewSevenSeg()})
			s.NewSource = func(config.Watch) (source.Source, error) { return &fakeSource{img: img}, nil }
			f := func(v float64) string { return fmt.Sprint(v) }
			form := url.Values{"x": {f(c.region.X)}, "y": {f(c.region.Y)}, "w": {f(c.region.W)}, "h": {f(c.region.H)},
				"engine": {"sevenseg"}, "letters": {"on"}, "ttype": {c.trig.Type}, "pattern": {c.trig.Pattern}, "op": {c.trig.Op},
				"tthreshold": {f(c.trig.Threshold)}, "confirm": {fmt.Sprint(c.trig.Confirm)}, "cooldown": {"0s"}}
			_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
			if !strings.Contains(body, `<span class="mono test-reading">`+c.read+`</span>`) {
				t.Errorf("Read should show %s; body:\n%s", c.read, body)
			}
			if !strings.Contains(body, `<p class="verdict verdict-`+c.state+`">`) || !strings.Contains(body, `<strong class="verdict-title">`+c.title+`</strong>`) {
				t.Errorf("verdict should be %s %q; body:\n%s", c.state, c.title, body)
			}
			w := config.Watch{Name: "w", Source: "unused", Engine: "sevenseg", Letters: true, Region: c.region, Trigger: c.trig}
			eng, err := ocr.Engines{}.ForWatch(w.Engine, w.Letters)
			if err != nil {
				t.Fatal(err)
			}
			r, err := runner.New(w, &fakeSource{img: img}, eng, nil, nil, t.Logf)
			if err != nil {
				t.Fatal(err)
			}
			fired, pending := false, 0
			for i := 0; i < c.trig.Confirm; i++ {
				ev, err := r.Tick(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if ev.Reading != c.read {
					t.Errorf("the running watch read %q, Test showed %q", ev.Reading, c.read)
				}
				fired = fired || ev.Fired
				pending = max(pending, ev.Pending)
			}
			if fired != (c.state == "met") {
				t.Errorf("the running watch fired=%v after %d readings; Test says %q", fired, c.trig.Confirm, c.title)
			}
			if c.state == "skipped" && pending != 0 {
				t.Errorf("Test says the reading is skipped, but the watch counted it (%d of %d)", pending, c.trig.Confirm)
			}
		})
	}
}

// A numeric watch skips a reading with a letter in it (trigger.CheckRead),
// and the watch list and Live say so, as Test does: a half-lit LED,
// "skipped" and the letter's own reason, and a Confirm count that stays.
// Not a green LED beside E4 as if it were a healthy reading. An ocr_match
// watch whose pattern matches the code acts on it: plain text, green.
func TestNumericWatchShowsALetterReadingSkipped(t *testing.T) {
	s, _ := newTestServer(t)
	startBlocked(t, s, "sevenseg")
	h := s.Handler()
	s.mu.Lock()
	s.cfg.Watches[0].Letters = true
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "numeric", Op: "lt", Threshold: 40, Confirm: 3}
	s.mu.Unlock()
	png := pngBytes(t)
	t0 := time.Unix(1700000000, 0)
	s.reg.Add("printer", state.Sample{TS: t0, Reading: "35", PNG: png, Pending: 1, Need: 3})
	s.reg.Add("printer", state.Sample{TS: t0.Add(2 * time.Second), Reading: "E4", PNG: png, Pending: 1, Need: 3})

	_, index := get(t, h, "/")
	row := index[strings.Index(index, `data-label="Last reading"`):]
	if !strings.Contains(row, "led-unsure") || strings.Contains(row, "led-green") ||
		!strings.Contains(row, `<span class="reading-sub" title="`+letterSkippedNote+`">skipped</span>`) {
		t.Errorf("the row should say a numeric watch skips E4; row:\n%s", row)
	}
	_, live := get(t, h, "/watch/printer/live")
	for _, want := range []string{
		`<p class="readout-hint">` + letterSkippedNote + `</p>`,
		`<span class="progress-text">This reading was skipped, so the count stays at 1 of 3 readings in a row below 40.</span>`,
		`title="E4 (skipped) (still 1 of 3 needed to fire)"`,
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live missing %q; body:\n%s", want, live)
		}
	}
	if !strings.Contains(live, "led-unsure") || strings.Contains(live, `<span class="led led-green"`) || strings.Contains(live, "Below 40: 1 of 3") {
		t.Errorf("Live should mark E4 skipped and not as if it counted; body:\n%s", live)
	}
	if v := viewReadingFor(s.cfg.Watches[0].Trigger, "sevenseg", "End", false); !v.Skipped || !v.Lettered {
		t.Errorf("End on a numeric watch: %+v, want skipped", v)
	}

	// The fault watch: its pattern matches E4, which it acts on.
	s.mu.Lock()
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "ocr_match", Pattern: `^E[0-9]+$`, Confirm: 1}
	s.mu.Unlock()
	_, index = get(t, h, "/")
	row = index[strings.Index(index, `data-label="Last reading"`):]
	if strings.Contains(row, "skipped") || !strings.Contains(row, "led-green") {
		t.Errorf("an ocr_match watch acts on the E4 its pattern matches; row:\n%s", row)
	}
	if _, live = get(t, h, "/watch/printer/live"); strings.Contains(live, letterSkippedNote) || strings.Contains(live, matchedNote) {
		t.Errorf("Live should show E4 as plain text for the fault watch; body:\n%s", live)
	}
}

// pixel_change reads nothing, so the Letters row steps back like Engine's
// and a save drops letters: true.
func TestPixelChangeSetsLettersAside(t *testing.T) {
	s, cfgPath := newTestServer(t)
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "sevenseg"
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "pixel_change", Threshold: 10, Confirm: 1}
	s.mu.Unlock()
	if _, body := get(t, s.Handler(), "/watch/printer"); !strings.Contains(body, `class="field-row field-row-check is-unused" id="row-letters"`) {
		t.Errorf("the Letters row should be set aside for pixel_change; body:\n%s", body)
	}
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"pixel_change"}, "tthreshold": {"10"}, "engine": {"sevenseg"}, "letters": {"on"},
		"confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
	}
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Watches[0].Letters {
		t.Error("a pixel_change watch saved letters: true")
	}
}
