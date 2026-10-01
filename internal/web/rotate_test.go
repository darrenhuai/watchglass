package web

import (
	"bytes"
	"encoding/base64"
	"html"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/imgproc"
	"github.com/darrenhuai/watchglass/internal/source"
)

var cropDataRe = regexp.MustCompile(`<img src="data:image/png;base64,([^"]+)"`)

// testCrop decodes the crop a Test answer shows.
func testCrop(t *testing.T, body string) image.Image {
	t.Helper()
	m := cropDataRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no crop in the Test answer:\n%s", body)
	}
	raw, err := base64.StdEncoding.DecodeString(html.UnescapeString(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// cornerFrame is 40 wide and 20 tall, dark, with a bright 2x2 block in its
// top-left corner: turned, the block says which way the crop went.
func cornerFrame() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			v := uint8(30)
			if x < 2 && y < 2 {
				v = 230
			}
			img.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return img
}

func bright(img image.Image, x, y int) bool {
	r, _, _, _ := img.At(img.Bounds().Min.X+x, img.Bounds().Min.Y+y).RGBA()
	return r>>8 > 128
}

// sidewaysFixture is the seven-segment fixture (it reads 23.5) the way a
// camera mounted a quarter turn would send it: turned 90 degrees
// counter-clockwise, so Rotate 90 puts it upright again.
func sidewaysFixture(t *testing.T) source.Source {
	t.Helper()
	f, err := os.Open("../ocr/testdata/sevenseg-23.5.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSource{img: imgproc.Rotate(imgproc.Crop(img, config.Region{W: 1, H: 1}), 270)}
}

// POST /test with pp_rotate turns the crop the answer shows: a quarter
// turn swaps its sides and moves the marked corner, a half turn keeps the
// size, 0 and a pixel_change test leave it as the camera sent it.
func TestTestRegionRotateTurnsTheCrop(t *testing.T) {
	s, _ := newTestServer(t)
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: cornerFrame()}, nil }
	base := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"ocr_changed"}, "engine": {"tesseract"}}
	for _, c := range []struct {
		rotate string
		size   image.Point
		corner image.Point // where the bright block's outer pixel ends up
	}{
		{"0", image.Pt(40, 20), image.Pt(0, 0)},
		{"90", image.Pt(20, 40), image.Pt(19, 0)},
		{"180", image.Pt(40, 20), image.Pt(39, 19)},
		{"270", image.Pt(20, 40), image.Pt(0, 39)},
	} {
		form := url.Values{"pp_rotate": {c.rotate}}
		for k, v := range base {
			form[k] = v
		}
		resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
		if resp.StatusCode != 200 {
			t.Fatalf("rotate %s: status = %d, body: %s", c.rotate, resp.StatusCode, body)
		}
		crop := testCrop(t, body)
		if got := crop.Bounds().Size(); got != c.size {
			t.Errorf("rotate %s: crop is %v, want %v", c.rotate, got, c.size)
			continue
		}
		if !bright(crop, c.corner.X, c.corner.Y) {
			t.Errorf("rotate %s: the marked corner should be at %v", c.rotate, c.corner)
		}
		lit := 0
		for _, p := range []image.Point{{}, {X: c.size.X - 1}, {Y: c.size.Y - 1}, {X: c.size.X - 1, Y: c.size.Y - 1}} {
			if bright(crop, p.X, p.Y) {
				lit++
			}
		}
		if lit != 1 {
			t.Errorf("rotate %s: %d corners are bright, want only the marked one", c.rotate, lit)
		}
	}

	// pixel_change compares the crop as the camera sends it; its Test shows
	// that crop even when the (hidden) Rotate control still submits a turn.
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"}, "pp_rotate": {"90"}}
	_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if crop := testCrop(t, body); crop.Bounds().Size() != image.Pt(40, 20) || !bright(crop, 0, 0) {
		t.Errorf("pixel_change test must not turn the crop: size %v", crop.Bounds().Size())
	}
}

// The reason Rotate exists: a seven-segment display that is sideways in
// the picture doesn't read, and with the right turn it reads like the
// upright one.
func TestTestRegionRotateReadsASidewaysDisplay(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	s.NewSource = func(w config.Watch) (source.Source, error) { return sidewaysFixture(t), nil }
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"numeric"}, "engine": {"sevenseg"}}
	_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if strings.Contains(body, "23.5") {
		t.Fatalf("the sideways fixture read 23.5 without a turn: the test proves nothing; body:\n%s", body)
	}
	form.Set("pp_rotate", "90")
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 200 || !strings.Contains(body, "23.5") {
		t.Errorf("status %d; with Rotate 90 the sideways display should read 23.5; body:\n%s", resp.StatusCode, body)
	}
	// The other quarter turn puts it upside down, which doesn't read 23.5.
	form.Set("pp_rotate", "270")
	if _, body := postForm(t, s.Handler(), "/watch/printer/test", form); strings.Contains(body, "23.5") {
		t.Errorf("Rotate 270 turns it the wrong way and must not read 23.5; body:\n%s", body)
	}
}

// A request that doesn't carry pp_rotate (curl, a page loaded before the
// control existed) reads with the watch's saved turn, the way one without
// engine reads with the saved engine.
func TestTestRegionRotateFallsBackToTheSavedTurn(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	s.NewSource = func(w config.Watch) (source.Source, error) { return sidewaysFixture(t), nil }
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "sevenseg"
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "numeric", Op: "gt", Threshold: 0}
	s.cfg.Watches[0].Preprocess.Rotate = 90
	s.mu.Unlock()
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}}
	if _, body := postForm(t, s.Handler(), "/watch/printer/test", form); !strings.Contains(body, "23.5") {
		t.Errorf("no pp_rotate: should use the watch's saved turn; body:\n%s", body)
	}
	form.Set("pp_rotate", "0")
	if _, body := postForm(t, s.Handler(), "/watch/printer/test", form); strings.Contains(body, "23.5") {
		t.Errorf("pp_rotate=0 in the form must override the saved turn; body:\n%s", body)
	}
}

// A saved pixel_change watch with a rotate left in its file (from when it
// read text) isn't turned by the runner, so its Test isn't either, also
// when the request carries no ttype (curl, an older client) and the saved
// type decides.
func TestTestRegionRotateSkipsASavedPixelChangeWatch(t *testing.T) {
	s, _ := newTestServer(t)
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: cornerFrame()}, nil }
	s.mu.Lock()
	if s.cfg.Watches[0].Trigger.Type != "pixel_change" {
		s.mu.Unlock()
		t.Fatalf("the test watch should be pixel_change, is %q", s.cfg.Watches[0].Trigger.Type)
	}
	s.cfg.Watches[0].Preprocess.Rotate = 90
	s.mu.Unlock()
	for _, form := range []url.Values{
		{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}},
		{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "pp_rotate": {"90"}},
		{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"}},
	} {
		_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
		if crop := testCrop(t, body); crop.Bounds().Size() != image.Pt(40, 20) || !bright(crop, 0, 0) {
			t.Errorf("form %v: a pixel_change watch's Test must show the crop unturned, got %v", form, crop.Bounds().Size())
		}
	}
	// The form's type still wins: switching the same watch to a reading
	// type in the form (unsaved) turns the crop.
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"ocr_changed"}, "engine": {"tesseract"}}
	_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if crop := testCrop(t, body); crop.Bounds().Size() != image.Pt(20, 40) {
		t.Errorf("ttype ocr_changed with the saved Rotate 90 should turn the crop, got %v", crop.Bounds().Size())
	}
}

// A turn that isn't one of the four is a 400 with a plain-text sentence
// naming them, from Test and from Save.
func TestRotateBadValueIsRefusedInPlainWords(t *testing.T) {
	s, cfgPath := newTestServer(t)
	h := s.Handler()
	for _, bad := range []string{"45", "-90", "360", "sideways", ""} {
		form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"ocr_changed"}, "pp_rotate": {bad}}
		resp, body := postForm(t, h, "/watch/printer/test", form)
		if resp.StatusCode != 400 {
			t.Errorf("test pp_rotate=%q: status = %d, want 400", bad, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("test pp_rotate=%q: Content-Type = %q, want text/plain (app.js shows it as text)", bad, ct)
		}
		if got := strings.TrimSpace(body); got != "Rotate must be 0, 90, 180 or 270 degrees clockwise" {
			t.Errorf("test pp_rotate=%q: body = %q", bad, got)
		}
	}

	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"ocr_match"}, "pattern": {"done"}, "tthreshold": {"0"}, "confirm": {"1"}, "cooldown": {"0"}, "interval": {"5s"},
		"pp_rotate": {"45"},
	}
	resp, body := postForm(t, h, "/watch/printer/save", form)
	if resp.StatusCode != 400 {
		t.Fatalf("save pp_rotate=45: status = %d, want 400", resp.StatusCode)
	}
	for _, want := range []string{
		`<p id="err-pp-rotate" class="field-error">Rotate must be 0, 90, 180 or 270 degrees clockwise.</p>`,
		`<a href="#f-rotate">Rotate must be 0, 90, 180 or 270 degrees clockwise.</a>`,
		`<details class="fold" open>`, // the section opens on its error
		`<select id="f-rotate" name="pp_rotate" class="control-medium" aria-invalid="true" aria-describedby="err-pp-rotate rotate-help">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rejected save lacks %q; body:\n%s", want, body)
		}
	}
	if raw, _ := os.ReadFile(cfgPath); strings.Contains(string(raw), "rotate") {
		t.Errorf("a refused turn was written:\n%s", raw)
	}

	// config.Validate's own wording (a hand-edited file, another client)
	// maps to the same field and sentence.
	fe := friendlyConfigError(&configErr{`watch "a": preprocess rotate must be 0, 90, 180 or 270 (degrees clockwise), got 45`})
	if fe.Field != "pp_rotate" || fe.Msg != "Rotate must be 0, 90, 180 or 270 degrees clockwise." {
		t.Errorf("friendlyConfigError = %+v", fe)
	}
}

type configErr struct{ s string }

func (e *configErr) Error() string { return e.s }

// The form renders the saved turn, saves it, and takes it out of the file
// again; a client that omits the field keeps what was saved.
func TestRotateFormRendersAndSavesTheTurn(t *testing.T) {
	s, cfgPath := newTestServer(t)
	h := s.Handler()

	_, body := get(t, h, "/watch/printer")
	for _, want := range []string{
		`<label for="f-rotate">Rotate</label>`,
		`<select id="f-rotate" name="pp_rotate" class="control-medium" aria-describedby="rotate-help">`,
		`<option value="0" selected>none</option>`,
		`<option value="90" >90° clockwise</option>`,
		`<option value="180" >180°</option>`,
		`<option value="270" >270° (90° counter-clockwise)</option>`,
		`<p id="rotate-help" class="field-hint">For a display that is sideways or upside down in the picture. Draw the box on the picture as it arrives; the crop is turned before it is read.</p>`,
		`<details class="fold">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page lacks %q; body:\n%s", want, body)
		}
	}
	// Rotate is the first row of the group: the steps are listed in the
	// order they run.
	if r, g := strings.Index(body, `id="f-rotate"`), strings.Index(body, `name="pp_grayscale"`); r < 0 || g < 0 || r > g {
		t.Errorf("Rotate should come before Grayscale (rotate at %d, grayscale at %d)", r, g)
	}

	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"ocr_match"}, "pattern": {"done"}, "tthreshold": {"0"}, "confirm": {"1"}, "cooldown": {"0"}, "interval": {"5s"},
		"pp_rotate": {"270"}, "pp_threshold": {"0"}, "pp_upscale": {"0"},
	}
	if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("save status = %d, body: %s", resp.StatusCode, body)
	}
	if raw, _ := os.ReadFile(cfgPath); !strings.Contains(string(raw), "rotate: 270") {
		t.Errorf("config file lacks rotate: 270:\n%s", raw)
	}
	if w, _ := s.findWatch("printer"); w.Preprocess.Rotate != 270 {
		t.Errorf("saved rotate = %d, want 270", w.Preprocess.Rotate)
	}
	_, body = get(t, h, "/watch/printer")
	for _, want := range []string{
		`<option value="270" selected>270° (90° counter-clockwise)</option>`,
		`<option value="0" >none</option>`,
		`<details class="fold" open>`, // a turned watch shows its Preprocess group
		`<span id="pp-summary" class="fold-note mono">rotate 270°</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page after save lacks %q; body:\n%s", want, body)
		}
	}

	// A client that doesn't send the field keeps the saved turn.
	form.Del("pp_rotate")
	if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("save without pp_rotate: status = %d, body: %s", resp.StatusCode, body)
	}
	if w, _ := s.findWatch("printer"); w.Preprocess.Rotate != 270 {
		t.Errorf("a save without pp_rotate changed the turn to %d", w.Preprocess.Rotate)
	}

	// Back to none: the key leaves the file.
	form.Set("pp_rotate", "0")
	if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("save rotate 0: status = %d, body: %s", resp.StatusCode, body)
	}
	if raw, _ := os.ReadFile(cfgPath); strings.Contains(string(raw), "rotate") {
		t.Errorf("rotate back at none must leave the file:\n%s", raw)
	}
}

func TestPreprocessSummaryNamesTheTurn(t *testing.T) {
	for _, c := range []struct {
		p    config.Preprocess
		want string
	}{
		{config.Preprocess{Rotate: 90}, "rotate 90°"},
		{config.Preprocess{Rotate: 180, Grayscale: true}, "rotate 180° · grayscale"},
		{config.Preprocess{Rotate: 270, Invert: true, Threshold: 128, Upscale: 2}, "rotate 270° · invert · binarize 128 · 2×"},
	} {
		if !preprocessSet(c.p) {
			t.Errorf("preprocessSet(%+v) = false: a turned watch must open the section", c.p)
		}
		if got := preprocessSummary(c.p); got != c.want {
			t.Errorf("preprocessSummary(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

// app.js treats Rotate like the other preprocess controls: a change marks
// a shown Test result stale, the folded summary names the turn in the
// server's words, the Test placeholder is sized for the turned crop, and
// pixel_change's help says the turn isn't used.
func TestRotateIsWiredInTheScriptAndStyles(t *testing.T) {
	js, err := readSourceLF("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`pp_rotate: 1, pp_grayscale: 1, pp_invert: 1, pp_threshold: 1, pp_upscale: 1 };`,
		`if (turn && turn.value !== "0") p.push("rotate " + turn.value + "°");`,
		`if ((turn === "90" || turn === "270") && testEngine() !== "") { var t = w; w = h; h = t; }`,
		`so Engine and Preprocess (Rotate included) aren't used.`,
	} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	css, err := readSourceLF("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".control-medium { max-width: 18rem; }") {
		t.Error("style.css lacks the .control-medium width the Rotate select uses")
	}
}
