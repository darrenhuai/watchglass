package web

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
)

// index of the first occurrence, or -1: for asserting document order.
func at(body, s string) int { return strings.Index(body, s) }

// The detail page's composition: the stage is the hero and everything that
// acts on the region (the drag hint, Test, manual entry, the result) sits
// with it; the form has its own heading and four headed sections in order,
// with the Save bar holding only Save and the note on where the save goes.
func TestDetailCompositionSectionsAndTestBesideStage(t *testing.T) {
	s, _ := newTestServer(t)
	_, body := get(t, s.Handler(), "/watch/printer")

	// Test is a region action: it lives under the stage, before the manual
	// fields and the result box, and outside the form.
	order := []string{
		`<div class="stage-frame">`,
		`<div class="region-actions">`,
		`<button type="button" id="testbtn" class="btn btn-outline">Test this region</button>`,
		`<details class="region-manual" id="region-manual">`,
		`<div id="test-result"`,
		`<h2 id="live-heading">Live</h2>`,
		`<h2 id="config-heading" class="col-heading">Configuration</h2>`,
		`<form id="watchform" class="panel" method="post" action="/watch/printer/save" aria-labelledby="config-heading">`,
		`<legend>Trigger</legend>`,
		`name="cooldown"`,
		`<legend>Polling</legend>`,
		`name="interval"`,
		`name="max_interval"`,
		`name="health_after"`,
		`<fieldset id="fs-preprocess" class="fs-fold" aria-labelledby="pp-label">`,
		`<span id="pp-label" class="fold-title">Preprocess</span>`,
		`name="pp_threshold"`,
		`<legend>Notify <span class="legend-note">one URL per line · <a href="https://shoutrrr.nickfedor.com/latest/services/overview/" target="_blank" rel="noopener">supported services</a></span></legend>`,
		`<div class="form-actions">`,
		`<p class="save-note" title="Merged into config.yaml: comments, key order and quoting are kept. See the README for the few things YAML can't round-trip."><span class="save-note-file">Writes to <code>config.yaml</code></span><span class="save-note-more">· comments kept</span></p>`,
		`<button type="submit" class="btn btn-primary">Save &amp; restart watch</button>`,
	}
	last := -1
	for _, want := range order {
		i := at(body, want)
		if i < 0 {
			t.Errorf("detail page missing %q", want)
			continue
		}
		if i < last {
			t.Errorf("%q is out of order (expected after the previous item)", want)
		}
		last = i
	}
	// The interval/health fields left the Trigger section: they come after
	// the Polling legend, and Cooldown stays before it.
	if p := at(body, `<legend>Polling</legend>`); p < 0 || at(body, `name="interval"`) < p || at(body, `name="cooldown"`) > p {
		t.Errorf("Interval belongs under Polling and Cooldown under Trigger")
	}
	// Test is not inside the form, and the form-actions bar holds no Test.
	form := body[at(body, `<form id="watchform"`):]
	if strings.Contains(form, `id="testbtn"`) {
		t.Errorf("Test this region must sit beside the stage, not in the form")
	}
	if strings.Count(body, `id="testbtn"`) != 1 {
		t.Errorf("exactly one Test button expected")
	}
	// The old two-line YAML caveat above Save is gone.
	for _, gone := range []string{"save-warning", "blank lines may be collapsed", "Saving merges this watch"} {
		if strings.Contains(body, gone) {
			t.Errorf("detail page still contains %q", gone)
		}
	}
	// Numbers and durations are short controls; text and long selects are not.
	for _, short := range []string{"f-threshold", "f-confirm", "f-cooldown", "f-interval", "f-maxinterval", "f-health"} {
		if !strings.Contains(body, `<input class="control-short" id="`+short+`"`) {
			t.Errorf("%s should be a short control", short)
		}
	}
	for _, sel := range []string{`<select id="f-op" name="op" class="control-short"`, `<select id="f-upscale" name="pp_upscale" class="control-short"`} {
		if !strings.Contains(body, sel) {
			t.Errorf("missing short select %q", sel)
		}
	}
	for _, full := range []string{`<select id="f-ttype" name="ttype" aria-describedby="ttype-help">`, `<select id="f-engine" name="engine" aria-describedby="engine-note" data-`, `<input id="f-pattern" name="pattern"`} {
		if !strings.Contains(body, full) {
			t.Errorf("%q should keep the full width (no control-short)", full)
		}
	}
	// One heading outline: name, Live, Configuration.
	if strings.Count(body, "<h2") != 2 || !strings.Contains(body, `aria-labelledby="config-heading"`) {
		t.Errorf("expected exactly the Live and Configuration h2s, with the form labelled by the latter")
	}
}

// Preprocess folds away at its defaults and opens whenever a setting is
// on, or a rejected save names one of its fields, so nothing configured is
// hidden. The readout beside the title says what the section holds.
func TestPreprocessFoldOpensWhenSetOrRejected(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	closed := `<details class="fold">`
	open := `<details class="fold" open>`

	_, body := get(t, h, "/watch/printer")
	if !strings.Contains(body, closed) || strings.Contains(body, open) {
		t.Errorf("preprocess at defaults should render closed; body:\n%s", body)
	}
	if !strings.Contains(body, `<span id="pp-summary" class="fold-note mono">off</span>`) {
		t.Errorf("closed section should read off; body:\n%s", body)
	}

	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"ocr_match"}, "pattern": {"done"}, "tthreshold": {"0"}, "confirm": {"1"}, "cooldown": {"0"}, "interval": {"5s"},
		"pp_grayscale": {"on"}, "pp_threshold": {"128"}, "pp_upscale": {"2"},
	}
	if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("save status = %d, body: %s", resp.StatusCode, body)
	}
	_, body = get(t, h, "/watch/printer")
	if !strings.Contains(body, open) {
		t.Errorf("tuned preprocess should render open; body:\n%s", body)
	}
	if !strings.Contains(body, `<span id="pp-summary" class="fold-note mono">grayscale · binarize 128 · 2×</span>`) {
		t.Errorf("readout should list the settings that are on; body:\n%s", body)
	}

	// Back to defaults, but a rejected upscale value: the section must show
	// the field error, so it opens.
	form.Set("pp_grayscale", "")
	form.Del("pp_grayscale")
	form.Set("pp_threshold", "0")
	form.Set("pp_upscale", "9")
	resp, body := postForm(t, h, "/watch/printer/save", form)
	if resp.StatusCode != 400 {
		t.Fatalf("bad upscale status = %d, body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, open) || !strings.Contains(body, `id="err-pp-upscale"`) {
		t.Errorf("a rejected preprocess field must open the section with its error; body:\n%s", body)
	}
}

func TestPreprocessSummary(t *testing.T) {
	cases := []struct {
		p    config.Preprocess
		set  bool
		want string
	}{
		{config.Preprocess{}, false, "off"},
		{config.Preprocess{Upscale: 1}, false, "off"},
		{config.Preprocess{Grayscale: true}, true, "grayscale"},
		{config.Preprocess{Invert: true, Threshold: 200}, true, "invert · binarize 200"},
		{config.Preprocess{Grayscale: true, Invert: true, Threshold: 128, Upscale: 3}, true, "grayscale · invert · binarize 128 · 3×"},
		{config.Preprocess{Upscale: 4}, true, "4×"},
	}
	for _, c := range cases {
		if got := preprocessSet(c.p); got != c.set {
			t.Errorf("preprocessSet(%+v) = %v, want %v", c.p, got, c.set)
		}
		if got := preprocessSummary(c.p); got != c.want {
			t.Errorf("preprocessSummary(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

// The header's error sentence is clamped to two lines, so its title must
// carry the whole of it, and app.js must keep that title in step.
func TestStatusSummaryTitleCarriesTheSentence(t *testing.T) {
	s, _ := newTestServer(t)
	_, body := get(t, s.Handler(), "/watch/printer")
	if !strings.Contains(body, `<p class="status-summary" title="`) {
		t.Errorf("status summary should carry a title; body:\n%s", body)
	}
	_, js := get(t, s.Handler(), "/static/app.js")
	if !strings.Contains(js, "sum.title = summary") {
		t.Errorf("app.js should update the summary's title with its text")
	}
	for _, want := range []string{"revealTestResult", "scrollIntoView", "ResizeObserver", `addEventListener("scroll", onStageScroll`, `getPropertyValue("--stage-gap")`, `dataset.pin = mode`, `getElementById("pp-summary")`} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
}

// The Save bar's note keeps to one line beside Save whatever the config
// file is called: the file half can shrink and truncate (min-width: 0 on
// the flex item, an ellipsis on the wrapper), the "comments kept" half
// is a separate item that the clipped flex row drops when it does not
// fit, and the stage column never flips to static (that jump is gone).
func TestSaveNoteWrapsFileAndCaveatSeparately(t *testing.T) {
	s, _ := newTestServer(t)
	s.cfgPath = filepath.Join(t.TempDir(), "home-assistant-watchglass-config.yaml")
	h := s.Handler()
	_, body := get(t, h, "/watch/printer")
	want := `<span class="save-note-file">Writes to <code>home-assistant-watchglass-config.yaml</code></span><span class="save-note-more">· comments kept</span></p>`
	if !strings.Contains(body, want) {
		t.Errorf("save note should wrap the file name and the caveat separately; body:\n%s", body)
	}
	_, css := get(t, h, "/static/style.css")
	for _, rule := range []string{".save-note-file {", "text-overflow: ellipsis;", "max-height: 1.4em;", "--stage-gap: var(--space-4); position: sticky; top: var(--stage-gap);"} {
		if !strings.Contains(css, rule) {
			t.Errorf("style.css missing %q", rule)
		}
	}
	noteRule := css[strings.Index(css, ".save-note {"):]
	noteRule = noteRule[:strings.Index(noteRule, "}")]
	for _, decl := range []string{"display: flex;", "flex-wrap: wrap;", "min-width: 0;", "overflow: hidden;"} {
		if !strings.Contains(noteRule, decl) {
			t.Errorf(".save-note rule missing %q:\n%s", decl, noteRule)
		}
	}
	// A row's hint sits on the control's edge like its error, so the amber
	// and red dots line up; the label column is sized to the widest label
	// so the Type/Engine option text fits the control at the 920px floor.
	hintRule := css[strings.Index(css, ".field-row > .field-hint {"):]
	hintRule = hintRule[:strings.Index(hintRule, "}")]
	if !strings.Contains(hintRule, "grid-column: 2;") {
		t.Errorf(".field-row > .field-hint should share the control column:\n%s", hintRule)
	}
	for _, rule := range []string{"--field-label-w: 5.5rem;", ".col-stage  { flex: 1 1 440px;", ".col-config { flex: 1 1 400px;", ".field-row > .field-hint { grid-column: 1; }"} {
		if !strings.Contains(css, rule) {
			t.Errorf("style.css missing %q", rule)
		}
	}
	_, js := get(t, h, "/static/app.js")
	if strings.Contains(css, "is-tall") || strings.Contains(js, "is-tall") {
		t.Errorf("the stage column must not switch between sticky and static any more")
	}
}
