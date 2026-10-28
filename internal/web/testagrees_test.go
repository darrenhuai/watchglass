package web

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/runner"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
)

func recipeSample(t *testing.T, name string) image.Image {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "docs", "recipes", "samples", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// Round 6 found Test and the running watch disagreeing on the recipes'
// own samples: the washer's End screen reads "?" and its watch fired while
// Test said "No digits read" with no verdict; the boiler's E4 reads "?4"
// and its fault watch fired while Test said "Partial reading". Test this
// region must show what the watch acts on: each sample is read through
// the Test route and by a real runner with the same settings, and the
// verdict and the fire must agree.
func TestTestAgreesWithTheRunningWatch(t *testing.T) {
	washer := config.Region{X: 0.44375, Y: 0.3222, W: 0.30625, H: 0.2822}
	boiler := config.Region{X: 0.3875, Y: 0.3111, W: 0.225, H: 0.3111}
	for _, c := range []struct {
		name, sample string
		region       config.Region
		trig         config.Trigger
		read         string // what the Test page's Read line shows
		state, title string
		note         string // the note under the facts, "" for none
	}{
		{"washer-finished End", "washer-end.jpg", washer, config.Trigger{Type: "ocr_match", Pattern: `^(0:00|\?+)$`, Confirm: 2},
			`<span class="digit-unknown" title="Digit not read">?</span><span class="sr-only"> (no digits read)</span></span> <span class="reading-sub" aria-hidden="true">no digits read</span>`,
			"met", "Condition met", wantMatchedNote},
		{"washer-almost-done End", "washer-end.jpg", washer, config.Trigger{Type: "numeric", Pattern: `^0:([0-9]{2})$`, Op: "lt", Threshold: 5, Confirm: 2},
			`<span class="digit-unknown" title="Digit not read">?</span><span class="sr-only"> (no digits read)</span></span>`,
			"skipped", "Reading skipped", "couldn't make a digit of anything here. Tighten the region around the digits or adjust Binarize."},
		{"boiler-fault E4", "boiler-seven-segment-fault.jpg", boiler, config.Trigger{Type: "ocr_match", Pattern: `^\?[0-9]+$`, Confirm: 3},
			`<span class="digit-unknown" title="Digit not read">?</span>4<span class="sr-only"> (some digits not read)</span></span>`,
			"met", "Condition met", wantMatchedNote},
		{"boiler-temp E4", "boiler-seven-segment-fault.jpg", boiler, config.Trigger{Type: "numeric", Pattern: `^([0-9]{1,3})$`, Op: "lt", Threshold: 40, Confirm: 3},
			`<span class="digit-unknown" title="Digit not read">?</span>4<span class="sr-only"> (some digits not read)</span></span>`,
			"skipped", "Reading skipped", "marks a digit the seven-segment decoder couldn't read. Tighten the region around the digits or adjust Binarize."},
		{"boiler-temp 52", "boiler-seven-segment-temp.jpg", boiler, config.Trigger{Type: "numeric", Pattern: `^([0-9]{1,3})$`, Op: "lt", Threshold: 40, Confirm: 3},
			`52</span>`, "unmet", "Condition not met", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			img := recipeSample(t, c.sample)
			s, _ := newTestServerWith(t, ocr.Engines{SevenSeg: ocr.NewSevenSeg()})
			s.NewSource = func(config.Watch) (source.Source, error) { return &fakeSource{img: img}, nil }
			f := func(v float64) string { return fmt.Sprint(v) }
			form := url.Values{"x": {f(c.region.X)}, "y": {f(c.region.Y)}, "w": {f(c.region.W)}, "h": {f(c.region.H)},
				"engine": {"sevenseg"}, "ttype": {c.trig.Type}, "pattern": {c.trig.Pattern}, "op": {c.trig.Op},
				"tthreshold": {f(c.trig.Threshold)}, "confirm": {fmt.Sprint(c.trig.Confirm)}, "cooldown": {"0s"}}
			_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
			if !strings.Contains(body, `<span class="mono test-reading">`+c.read) {
				t.Errorf("Read should show %s; body:\n%s", c.read, body)
			}
			if !strings.Contains(body, `<p class="verdict verdict-`+c.state+`">`) || !strings.Contains(body, `<strong class="verdict-title">`+c.title+`</strong>`) {
				t.Errorf("verdict should be %s %q; body:\n%s", c.state, c.title, body)
			}
			// The '?' a recipe's pattern catches on purpose is the box
			// working, not a box to fix: no "Tighten the region" there.
			if c.note == "" && strings.Contains(body, `class="test-note"`) || c.note != "" && !strings.Contains(body, c.note) {
				t.Errorf("the note under the facts should say %q; body:\n%s", c.note, body)
			}
			if c.state == "met" && strings.Contains(body, "Tighten the region around the digits or adjust Binarize.") {
				t.Errorf("a pattern that matches the ? shouldn't be told to fix the region; body:\n%s", body)
			}

			w := config.Watch{Name: "w", Source: "unused", Engine: "sevenseg", Region: c.region, Trigger: c.trig}
			r, err := runner.New(w, &fakeSource{img: img}, ocr.NewSevenSeg(), nil, nil, t.Logf)
			if err != nil {
				t.Fatal(err)
			}
			fired, pending := false, 0
			for i := 0; i < c.trig.Confirm; i++ {
				ev, err := r.Tick(context.Background())
				if err != nil {
					t.Fatal(err)
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

// wantMatchedNote is what Test and Live say under a reading whose '?' the
// watch's pattern matches.
const wantMatchedNote = "The pattern matches the ? itself, so the watch acts on this reading. That is expected when the display shows a letter here. If it shows a number, tighten the region around the digits or adjust Binarize."

// The dashboard and Live say what the running watch does with a '?'
// reading, as Test does: a numeric watch skips it (a half-lit LED and
// "skipped", not a green LED beside "??.0"), and a pattern that catches
// the '?' on purpose isn't told to fix the region.
func TestDashboardAndLiveSayWhatTheWatchDoesWithAQuestionMark(t *testing.T) {
	s, _ := newTestServer(t)
	startBlocked(t, s, "sevenseg")
	h := s.Handler()
	setTrig := func(trig config.Trigger) {
		s.mu.Lock()
		s.cfg.Watches[0].Trigger = trig
		s.mu.Unlock()
	}

	setTrig(config.Trigger{Type: "numeric", Op: "gt", Threshold: 50, Confirm: 2})
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "??.0", PNG: pngBytes(t)})
	_, index := get(t, h, "/")
	row := index[strings.Index(index, `data-label="Last reading"`):]
	if !strings.Contains(row, "led-unsure") || strings.Contains(row, "led-green") ||
		!strings.Contains(row, `<span class="mono"><span class="digit-unknown" title="Digit not read">??</span>.0<span class="sr-only"> (some digits not read)</span></span> <span class="reading-sub" title="A digit wasn&#39;t read, so the watch skips this reading and keeps its last state.">skipped</span>`) {
		t.Errorf("a numeric watch's partial reading should say the watch skips it; row:\n%s", row)
	}
	_, live := get(t, h, "/watch/printer/live")
	if !strings.Contains(live, "led-unsure") || strings.Contains(live, "led-green") ||
		!strings.Contains(live, "marks a digit the seven-segment decoder couldn't read, so the watch skips this reading and keeps its last state. Tighten the region") ||
		!strings.Contains(live, `<figcaption title="??.0 (skipped)">`) {
		t.Errorf("Live should say the watch skips a partial reading; body:\n%s", live)
	}

	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "?", PNG: pngBytes(t)})
	_, index = get(t, h, "/")
	row = index[strings.Index(index, `data-label="Last reading"`):]
	if !strings.Contains(row, `<span class="reading-sub" title="A digit wasn&#39;t read, so the watch skips this reading and keeps its last state."><span aria-hidden="true">no digits read, </span>skipped</span>`) {
		t.Errorf("an all-? reading on a numeric watch is unread and skipped; row:\n%s", row)
	}
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, "The seven-segment decoder couldn't read any digit, so the watch skips this reading and keeps its last state.") {
		t.Errorf("Live should say the watch skips an all-? reading; body:\n%s", live)
	}

	// The washer recipe: End reads "?", and the pattern catches it.
	setTrig(config.Trigger{Type: "ocr_match", Pattern: `^(0:00|\?+)$`, Confirm: 2})
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "?", Fired: true, PNG: pngBytes(t)})
	_, index = get(t, h, "/")
	row = index[strings.Index(index, `data-label="Last reading"`):]
	if !strings.Contains(row, `<span class="reading-sub" aria-hidden="true" title="`+wantMatchedNote+`">no digits read</span>`) || strings.Contains(row, "skipped") {
		t.Errorf("a ? the pattern matches shouldn't be called a box to fix; row:\n%s", row)
	}
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, `<p class="readout-hint">`+wantMatchedNote+`</p>`) || strings.Contains(live, "couldn't read any digit. Tighten") {
		t.Errorf("Live should say the pattern matches the ?; body:\n%s", live)
	}
	// Before it fires (Confirm 2), the matched '?' keeps a healthy LED.
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "?", PNG: pngBytes(t)})
	_, index = get(t, h, "/")
	_, live = get(t, h, "/watch/printer/live")
	if row = index[strings.Index(index, `data-label="Last reading"`):]; !strings.Contains(row, "led-green") || strings.Contains(row, "led-unsure") ||
		!strings.Contains(live, `<span class="led led-green"`) || strings.Contains(live, "led-unsure") {
		t.Errorf("a ? the pattern matches is the box working, not an unsure reading; row:\n%s\nlive:\n%s", row, live)
	}
	// A '?' the pattern doesn't match is still a box to look at.
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "?4", PNG: pngBytes(t)})
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, "marks a digit the seven-segment decoder couldn't read. Tighten the region") || strings.Contains(live, wantMatchedNote) {
		t.Errorf("a ? the pattern doesn't match keeps the advice; body:\n%s", live)
	}
}

// A skipped reading carries the count in progress (trigger.Evaluator.skipped),
// and the Live page says so: not "Above 24: 1 of 3 readings in a row needed
// to fire." right under the hint that this reading was skipped, as if it
// had counted, but that the count stays where it was.
func TestLiveProgressOnASkippedReadingSaysTheCountStays(t *testing.T) {
	s, _ := newTestServer(t)
	startBlocked(t, s, "sevenseg")
	h := s.Handler()
	s.mu.Lock()
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "numeric", Op: "gt", Threshold: 24, Confirm: 3, Cooldown: config.Duration(time.Minute)}
	s.mu.Unlock()
	t0 := time.Unix(1700000000, 0)
	png := pngBytes(t)
	s.reg.Add("printer", state.Sample{TS: t0, Reading: "25.0", PNG: png, Pending: 1, Need: 3})
	s.reg.Add("printer", state.Sample{TS: t0.Add(2 * time.Second), Reading: "?5.0", PNG: png, Pending: 1, Need: 3})
	_, live := get(t, h, "/watch/printer/live")
	for _, want := range []string{
		`<span class="progress-text">This reading was skipped, so the count stays at 1 of 3 readings in a row above 24.</span>`,
		`title="?5.0 (skipped) (still 1 of 3 needed to fire)"`,
		`title="25.0 (1 of 3 needed to fire)"`,
		`<span class="tile-progress">still 1 of 3<span class="sr-only"> needed to fire</span></span>`,
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live missing %q; body:\n%s", want, live)
		}
	}
	if strings.Contains(live, "Above 24: 1 of 3") {
		t.Error("a skipped reading shouldn't be told as if it counted")
	}

	ends := t0.Add(time.Minute)
	s.reg.Add("printer", state.Sample{TS: t0.Add(4 * time.Second), Reading: "?5.1", PNG: png, Pending: 1, Need: 3, CooldownEnds: ends})
	_, live = get(t, h, "/watch/printer/live")
	if want := `<span class="progress-text">This reading was skipped, so the count stays at 1 of 3 readings in a row above 24. Cooldown holds any alert until <time class="mono" datetime="` + isoTime(ends) + `">`; !strings.Contains(live, want) {
		t.Errorf("live missing %q; body:\n%s", want, live)
	}
	if !strings.Contains(live, `title="?5.1 (skipped) (still 1 of 3 readings in a row, held by Cooldown)"`) {
		t.Errorf("tile should say the held count stays; body:\n%s", live)
	}
	s.reg.Add("printer", state.Sample{TS: t0.Add(6 * time.Second), Reading: "?5.2", PNG: png, Pending: 3, Need: 3, CooldownEnds: ends})
	_, live = get(t, h, "/watch/printer/live")
	if want := `<span class="progress-text">This reading was skipped; above 24 stays confirmed. Cooldown holds the alert until <time class="mono" datetime="` + isoTime(ends) + `">`; !strings.Contains(live, want) {
		t.Errorf("live missing %q; body:\n%s", want, live)
	}
	if !strings.Contains(live, `title="?5.2 (skipped) (still held by Cooldown)"`) {
		t.Errorf("tile should say the hold stays; body:\n%s", live)
	}

	// A clean reading that counts is told as before.
	s.reg.Add("printer", state.Sample{TS: t0.Add(8 * time.Second), Reading: "25.0", PNG: png, Pending: 2, Need: 3})
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, `<span class="progress-text">Above 24: 2 of 3 readings in a row needed to fire.</span>`) || strings.Contains(live, `title="25.0 (still`) {
		t.Errorf("a counted reading keeps the plain line; body:\n%s", live)
	}
}

// A frame with no digit on it reads as "" from the seven-segment decoder,
// and a numeric watch skips it like a '?' reading (trigger.Evaluator.unread):
// the count in progress stands. Test, the watch list and Live say so, the
// same way they do for a '?'; a text engine's blank reading is still just
// "Nothing read".
func TestNumericWatchSkipsABlankSevenSegReading(t *testing.T) {
	num := config.Trigger{Type: "numeric", Op: "gt", Threshold: 24, Confirm: 3}
	if st, title, detail := Verdict(num, "sevenseg", ""); st != "skipped" || title != "Reading skipped" ||
		detail != "Nothing was read, so this reading is skipped and the watch keeps its last state." {
		t.Errorf("Test on a blank sevenseg reading: %s %q %q", st, title, detail)
	}
	if st, _, _ := Verdict(num, "tesseract", ""); st != "unmet" {
		t.Errorf("Test on a blank tesseract reading should stay %q, got %q", "unmet", st)
	}

	s, _ := newTestServer(t)
	startBlocked(t, s, "sevenseg")
	h := s.Handler()
	s.mu.Lock()
	s.cfg.Watches[0].Trigger = num
	s.mu.Unlock()
	png := pngBytes(t)
	t0 := time.Unix(1700000000, 0)
	s.reg.Add("printer", state.Sample{TS: t0, Reading: "25.0", PNG: png, Pending: 1, Need: 3})
	s.reg.Add("printer", state.Sample{TS: t0.Add(2 * time.Second), Reading: "", PNG: png, Pending: 1, Need: 3})
	_, index := get(t, h, "/")
	row := index[strings.Index(index, `data-label="Last reading"`):]
	if !strings.Contains(row, "led-unsure") || strings.Contains(row, "led-green") ||
		!strings.Contains(row, `<span class="reading-none">Nothing read</span> <span class="reading-sub" title="`+blankSkippedNote+`">skipped</span>`) {
		t.Errorf("the row should say a numeric watch skips a blank reading; row:\n%s", row)
	}
	_, live := get(t, h, "/watch/printer/live")
	for _, want := range []string{
		`<p class="readout-hint">` + blankSkippedNote + `</p>`,
		`<span class="progress-text">This reading was skipped, so the count stays at 1 of 3 readings in a row above 24.</span>`,
		`title="Nothing read (skipped) (still 1 of 3 needed to fire)"`,
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live missing %q; body:\n%s", want, live)
		}
	}
	if !strings.Contains(live, "led-unsure") || strings.Contains(live, `<span class="led led-green"`) || strings.Contains(live, "Above 24: 1 of 3") {
		t.Errorf("Live should mark a blank reading unsure and not as if it counted; body:\n%s", live)
	}

	// ocr_match judges the blank as text (it may match ^$ on purpose), so
	// nothing is called skipped.
	s.mu.Lock()
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "ocr_match", Pattern: `^[0-9]+$`, Confirm: 2}
	s.mu.Unlock()
	_, index = get(t, h, "/")
	row = index[strings.Index(index, `data-label="Last reading"`):]
	if strings.Contains(row, "skipped") || !strings.Contains(row, "led-green") {
		t.Errorf("an ocr_match watch's blank reading isn't skipped; row:\n%s", row)
	}
	if _, live = get(t, h, "/watch/printer/live"); strings.Contains(live, "skipped") {
		t.Errorf("Live shouldn't call an ocr_match watch's blank reading skipped; body:\n%s", live)
	}
}
