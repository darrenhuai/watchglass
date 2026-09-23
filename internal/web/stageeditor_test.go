package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
)

// The editor rounds each edge of a dragged region to four decimals, so a
// region dragged to the frame's edge can post x+w = 1.0001. Save and Test
// take that as the edge (config.Region.Clamp) and the file gets a region
// that sums to exactly 1; a region really outside the frame is still
// refused with the readable message.
func TestSaveAcceptsRegionARoundingStepPastTheEdge(t *testing.T) {
	s, cfgPath := newTestServer(t)
	form := url.Values{
		"x": {"0.0063"}, "y": {"0.0556"}, "w": {"0.9938"}, "h": {"0.9445"},
		"ttype": {"pixel_change"}, "tthreshold": {"10"}, "confirm": {"1"}, "cooldown": {"0"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 303 {
		t.Fatalf("edge region: status = %d, want 303; body:\n%s", resp.StatusCode, body)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	r := got.Watches[0].Region
	if r.X != 0.0063 || r.Y != 0.0556 || r.X+r.W > 1 || r.Y+r.H > 1 || r.W < 0.9936 || r.H < 0.9443 {
		t.Errorf("saved region should be clamped onto the edge, got %+v", r)
	}
	resp, _ = postForm(t, s.Handler(), "/watch/printer/test", url.Values{"x": {"0.0063"}, "y": {"0"}, "w": {"0.9938"}, "h": {"1"}})
	if resp.StatusCode == 400 {
		t.Errorf("Test should accept the same edge region; status %d", resp.StatusCode)
	}
	form.Set("w", "0.9940") // two steps over: outside the frame
	resp, body = postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 400 || !strings.Contains(body, `<a href="#stage">Region must fit inside the frame`) {
		t.Errorf("a region two steps past the edge should still be refused; status %d body:\n%s", resp.StatusCode, body)
	}
}

// The stage and region editor: the name wraps rather than widening the
// page, the snapshot placeholder fills the frame, the canvas is a keyboard
// editor with a live readout, the manual fields are one row of named
// fractions, touch screens get the Edit region toggle, and app.js carries
// the drag rules (main button and primary pointer only, a start threshold,
// a minimum size, grid-snapped edges, handle hit-testing, DPR scaling).
func TestStageEditorMarkupStylesAndScript(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	_, body := get(t, h, "/watch/printer")
	for _, want := range []string{
		`<img id="snap" src="/watch/printer/snapshot" alt="Latest frame from printer">`,
		`<canvas id="overlay" tabindex="0" role="application" aria-label="Region editor. Arrow keys move the region, Shift+Arrow resizes it, Alt for fine steps." aria-describedby="region-readout"></canvas>`,
		`<div id="snap-error" class="snap-error" role="status" hidden></div>`,
		`<span id="region-readout" class="sr-only" aria-live="polite"></span>`,
		`<p id="region-hint" class="hint muted">Drag on the image to select the region to watch, or set it manually below.</p>`,
		`<div class="region-btns">`,
		`<button type="button" id="region-edit" class="btn btn-outline region-edit" aria-pressed="false">Edit region</button>`,
		`<details class="region-manual" id="region-manual">`,
		`<p class="region-manual-hint">Fractions of the frame from its top-left corner, 0 to 1 (0.5 = halfway). Arrow keys step by 0.01.</p>`,
		`<div class="region-manual-grid">`,
		`<label for="m-x">Left</label>`, `<label for="m-y">Top</label>`, `<label for="m-w">Width</label>`, `<label for="m-h">Height</label>`,
		`<input id="m-x" type="number" step="any" min="0" max="1" inputmode="decimal" placeholder="0–1" aria-describedby="region-manual-error">`,
		`<input id="m-y" type="number" step="any" min="0" max="1" inputmode="decimal" placeholder="0–1" aria-describedby="region-manual-error">`,
		`<input id="m-w" type="number" step="any" min="0" max="1" inputmode="decimal" placeholder="0–1" aria-describedby="region-manual-error">`,
		`<input id="m-h" type="number" step="any" min="0" max="1" inputmode="decimal" placeholder="0–1" aria-describedby="region-manual-error">`,
		// A polite live region that is always rendered, so the reason a
		// value is refused is announced, not only painted red.
		`<p id="region-manual-error" class="field-error" aria-live="polite"></p>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	for _, gone := range []string{`alt="snapshot"`, `class="field-error" hidden></p>`, `X (0–1)`, `W (0–1)`, `step="0.0001"`, `<canvas id="overlay"></canvas>`} {
		if strings.Contains(body, gone) {
			t.Errorf("detail page still contains %q", gone)
		}
	}
	// The manual fields stay outside the form (no name), so a bad value can
	// never be posted; the four region inputs the form posts are separate.
	form := body[at(body, `<form id="watchform"`):]
	if strings.Contains(form, `id="m-x"`) || !strings.Contains(form, `name="x"`) {
		t.Errorf("manual fields must sit outside the form and the region inputs inside it")
	}

	_, css := get(t, h, "/static/style.css")
	for _, rule := range []string{
		".detail-title-row h1 { margin: 0; font-family: var(--font-mono); min-width: 0; overflow-wrap: anywhere; }",
		"#stage:has(> #snap-error:not([hidden])) { display: block; }",
		"#stage canvas { position: absolute; left: 0; top: 0; cursor: crosshair; touch-action: pan-y pinch-zoom; }",
		"#stage.is-editing canvas { touch-action: none; }",
		"#stage canvas:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }",
		".stage-frame:has(#stage.is-editing) {",
		"@media (any-pointer: coarse) { .region-edit { display: inline-flex; } }",
		"@keyframes led-pulse",
		".snap-error.is-down strong { color: var(--red); }",
		".snap-error.is-slow strong { color: var(--amber); }",
		".region-manual-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr));",
		".region-manual-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }",
		// The slate's text stays copyable inside the selection-locked frame.
		".snap-error { user-select: text; -webkit-user-select: text; }",
		// Edit region, Test (every shared .btn since W8, see
		// TestSharedButtonsAreTouchTargets) and the manual disclosure are
		// 44px touch targets wherever a coarse pointer exists.
		"details.region-manual > summary { min-height: 44px; padding-block: 0; }",
		"#region-manual-error:empty::before { display: none; }",
	} {
		if !strings.Contains(css, rule) {
			t.Errorf("style.css missing %q", rule)
		}
	}
	snap := css[strings.Index(css, ".snap-error {"):]
	snap = snap[:strings.Index(snap, "}")]
	frame := css[strings.Index(css, ".stage-frame {"):]
	frame = frame[:strings.Index(frame, "}")]
	for _, d := range []string{"width: 100%;", "aspect-ratio: 16 / 9;"} {
		if !strings.Contains(snap, d) {
			t.Errorf(".snap-error rule missing %q:\n%s", d, snap)
		}
	}
	for _, d := range []string{"text-align: center;", "user-select: none;"} {
		if !strings.Contains(frame, d) {
			t.Errorf(".stage-frame rule missing %q:\n%s", d, frame)
		}
	}
	if strings.Contains(css, "cursor: crosshair; touch-action: none; }") || strings.Contains(css, ".region-manual .field-row { grid-template-columns: 60px 1fr; }") {
		t.Errorf("the old always-none touch-action / 60px label grid should be gone")
	}

	_, js := get(t, h, "/static/app.js")
	for _, want := range []string{
		"if (drag || !e.isPrimary || e.button !== 0) return;",
		`if (e.pointerType === "touch" && !editing()) return;`,
		"var DRAG_START_PX = 6, MIN_REGION_PX = 4;",
		"function hitTest(px, py, tol)",
		// A near-full region has nowhere to move and no exterior to start
		// on; a press inside it draws a new one, as for the full frame.
		"function roomless(r)", `if (isFull(r) || roomless(r)) return "new";`,
		"if (manualErr.textContent !== text) manualErr.textContent = text;",
		"function toGrid(v)", "function setRegion(x, y, w, h)",
		"window.devicePixelRatio || 1", "ctx.setTransform(canvas.width / cssW, 0, 0, canvas.height / cssH, 0, 0);",
		`getPropertyValue("--accent")`,
		"function manualProblems()", "Left + Width can't exceed 1", "function manualGate(e)",
		`"No frame to draw on. Set the region manually below."`, `"Whole frame selected. "`,
		"function renderSnapResult()", `renderSnap("is-checking", "led-amber", "No frame yet");`,
		`canvas.addEventListener("keydown"`, `canvas.addEventListener("pointercancel", function (e) { endDrag(e, true); });`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	for _, gone := range []string{`"rgba(127, 212, 168, 0.12)"`, "Math.abs(x1 - drag.x0).toFixed(4)", `el("strong", null, "No image from the camera")`, "manualErr.hidden"} {
		if strings.Contains(js, gone) {
			t.Errorf("app.js still contains %q", gone)
		}
	}
}
