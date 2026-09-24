package web

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/supervisor"
)

type fakeSource struct{ img image.Image }

func (f *fakeSource) Grab(ctx context.Context) (image.Image, error) { return f.img, nil }

type fakeDetailed struct{}

func (fakeDetailed) Recognize(ctx context.Context, img image.Image) (string, error) {
	return "PRINT COMPLETE", nil
}
func (fakeDetailed) RecognizeWords(ctx context.Context, img image.Image) (string, []ocr.Word, error) {
	return "PRINT COMPLETE", []ocr.Word{{Text: "PRINT", Conf: 91.5}, {Text: "COMPLETE", Conf: 84}}, nil
}

func testImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{R: 90, G: 90, B: 90, A: 255})
		}
	}
	return img
}

// newTestServer builds a Server over a real temp config file, a real
// supervisor (with fake sources), and a fake OCR engine.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	return newTestServerWith(t, ocr.Engines{Tesseract: fakeDetailed{}})
}

// newTestServerWith is newTestServer with the engine set spelled out, for
// the supervisor as well as the server (a save restarts the watch through
// the supervisor, so both must agree on what is installed).
func newTestServerWith(t *testing.T, engines ocr.Engines) (*Server, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{Watches: []config.Watch{{
		Name:     "printer",
		Source:   "http://unused.invalid/snap.jpg",
		Interval: config.Duration(time.Second),
		Region:   config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger:  config.Trigger{Type: "pixel_change", Threshold: 10},
	}}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	reg := state.New(5)
	sup := supervisor.New(nil, reg, engines, func(string, ...any) {})
	sup.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, engines, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	return s, cfgPath
}

func get(t *testing.T, h http.Handler, path string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestIndexListsWatches(t *testing.T) {
	s, _ := newTestServer(t)
	resp, body := get(t, s.Handler(), "/")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "printer") {
		t.Errorf("index does not list watch name; body:\n%s", body)
	}
}

func TestSnapshotReturnsPNG(t *testing.T) {
	s, _ := newTestServer(t)
	resp, body := get(t, s.Handler(), "/watch/printer/snapshot")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	img, err := png.Decode(strings.NewReader(body))
	if err != nil {
		t.Fatalf("body is not PNG: %v", err)
	}
	if img.Bounds().Dx() != 40 {
		t.Errorf("bounds = %v", img.Bounds())
	}
}

func TestSnapshotUnknownWatch404(t *testing.T) {
	s, _ := newTestServer(t)
	resp, _ := get(t, s.Handler(), "/watch/nope/snapshot")
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestLiveFragmentShowsLatestReading(t *testing.T) {
	s, _ := newTestServer(t)
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "42.0% changed", Fired: true, PNG: pngBytes(t)})
	resp, body := get(t, s.Handler(), "/watch/printer/live")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "42.0% changed") {
		t.Errorf("live fragment missing reading; body:\n%s", body)
	}
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Errorf("live fragment missing inline crop image")
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b strings.Builder
	if err := png.Encode(&b, testImage()); err != nil {
		t.Fatal(err)
	}
	return []byte(b.String())
}

var _ = os.ReadFile // silence unused import until Task 9 uses os

func postForm(t *testing.T, h http.Handler, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestDetailRendersEditor(t *testing.T) {
	s, _ := newTestServer(t)
	resp, body := get(t, s.Handler(), "/watch/printer")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, want := range []string{"id=\"stage\"", "id=\"overlay\"", "name=\"ttype\"", "name=\"pp_threshold\"", "/static/app.js",
		// app.js's poll targets: the readout and strip containers (not live
		// regions: see TestLiveAnnouncesChangesNotTicks), the sr-only region
		// that says what changed, the poll's notice, and the status pill.
		"id=\"live\"", `<div id="live-status">`, `<p id="live-announce" class="sr-only" aria-live="polite" aria-atomic="true"></p>`,
		`<p id="live-notice" class="live-notice" hidden></p>`, "id=\"live-strip\"", "id=\"status-pill\""} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %s", want)
		}
	}
}

func TestDetailUnknown404(t *testing.T) {
	s, _ := newTestServer(t)
	resp, _ := get(t, s.Handler(), "/watch/nope")
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestTestRegionReturnsWordsFragment(t *testing.T) {
	s, _ := newTestServer(t)
	form := url.Values{
		"x": {"0.1"}, "y": {"0.1"}, "w": {"0.5"}, "h": {"0.3"},
		"pp_grayscale": {"on"}, "pp_threshold": {"128"}, "pp_upscale": {"2"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	// Confidence is a whole percent (rounded down), not the engine's float.
	for _, want := range []string{"PRINT COMPLETE", `<span class="conf">91%</span>`, "data:image/png;base64,"} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment missing %q; body:\n%s", want, body)
		}
	}
}

func TestTestRegionRejectsBadRegion(t *testing.T) {
	s, _ := newTestServer(t)
	form := url.Values{"x": {"0.8"}, "y": {"0"}, "w": {"0.5"}, "h": {"1"}}
	resp, _ := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestTestRegionWithoutEngineShowsCropOnly(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}}
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "data:image/png;base64,") || !strings.Contains(body, "tesseract") {
		t.Errorf("engine-less fragment should show crop + tesseract hint; body:\n%s", body)
	}
}

// should_fix 3: pixel_change never uses OCR, so the tesseract-missing note
// (which reads as an error) must not appear on the most common first test a
// new user runs against a pixel_change watch.
func TestTestRegionPixelChangeSuppressesTesseractNote(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"}}
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if strings.Contains(body, "tesseract") {
		t.Errorf("pixel_change test must not show the tesseract-missing note; body:\n%s", body)
	}
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Errorf("crop should still render; body:\n%s", body)
	}
}

// should_fix 3 (still applies): an OCR trigger type with no engine keeps
// showing the note, matching pre-fix behavior — only pixel_change is exempt.
func TestTestRegionOCRTypeKeepsTesseractNote(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"ocr_match"}}
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "tesseract") {
		t.Errorf("ocr_match test without an engine should still show the tesseract note; body:\n%s", body)
	}
}

func TestSavePersistsAndRestarts(t *testing.T) {
	s, cfgPath := newTestServer(t)
	form := url.Values{
		"x": {"0.25"}, "y": {"0.25"}, "w": {"0.5"}, "h": {"0.25"},
		"ttype": {"ocr_match"}, "pattern": {"(?i)done"}, "op": {""},
		"tthreshold": {"0"}, "confirm": {"2"}, "cooldown": {"10m"}, "interval": {"5s"},
		"pp_grayscale": {"on"}, "pp_threshold": {"128"}, "pp_upscale": {"2"},
		"notify": {"ntfy://ntfy.sh/topic\n"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 303 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	w := got.Watches[0]
	if w.Region.X != 0.25 || w.Region.H != 0.25 {
		t.Errorf("region not saved: %+v", w.Region)
	}
	if w.Trigger.Type != "ocr_match" || w.Trigger.Pattern != "(?i)done" || w.Trigger.Confirm != 2 {
		t.Errorf("trigger not saved: %+v", w.Trigger)
	}
	if time.Duration(w.Trigger.Cooldown) != 10*time.Minute {
		t.Errorf("cooldown = %v", time.Duration(w.Trigger.Cooldown))
	}
	if !w.Preprocess.Grayscale || w.Preprocess.Threshold != 128 || w.Preprocess.Upscale != 2 {
		t.Errorf("preprocess not saved: %+v", w.Preprocess)
	}
	if len(w.Notify) != 1 || w.Notify[0] != "ntfy://ntfy.sh/topic" {
		t.Errorf("notify not saved: %v", w.Notify)
	}
	if running := s.sup.Running(); len(running) != 1 || running[0] != "printer" {
		t.Errorf("watch not running after save: %v", running)
	}
}

func TestSaveInvalidRejected(t *testing.T) {
	s, cfgPath := newTestServer(t)
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"ocr_match"}, "pattern": {""}, // ocr_match without pattern is invalid at runner level...
		"tthreshold": {"0"}, "confirm": {"1"}, "cooldown": {"0s"}, "interval": {"0.5s"},
	}
	resp, _ := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 400 {
		t.Errorf("sub-second interval: status = %d, want 400", resp.StatusCode)
	}
	got, _ := config.Load(cfgPath)
	if got.Watches[0].Trigger.Type != "pixel_change" {
		t.Error("invalid save must not modify the config file")
	}
}

func TestCreateAndDelete(t *testing.T) {
	s, cfgPath := newTestServer(t)
	resp, body := postForm(t, s.Handler(), "/watch/new", url.Values{
		"name": {"oven"}, "source": {"http://cam2/snap.jpg"},
	})
	if resp.StatusCode != 303 {
		t.Fatalf("create status = %d, body: %s", resp.StatusCode, body)
	}
	got, _ := config.Load(cfgPath)
	if len(got.Watches) != 2 {
		t.Fatalf("watches = %d, want 2", len(got.Watches))
	}
	if running := s.sup.Running(); len(running) != 1 || running[0] != "oven" {
		t.Errorf("new watch not started: %v", running)
	}
	resp, _ = postForm(t, s.Handler(), "/watch/oven/delete", url.Values{})
	if resp.StatusCode != 303 {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	got, _ = config.Load(cfgPath)
	if len(got.Watches) != 1 {
		t.Errorf("watches after delete = %d, want 1", len(got.Watches))
	}
	if running := s.sup.Running(); len(running) != 0 {
		t.Errorf("deleted watch still running: %v", running)
	}
}

func TestCreateEmptyFFmpegArgsRejected(t *testing.T) {
	s, cfgPath := newTestServer(t)
	resp, _ := postForm(t, s.Handler(), "/watch/new", url.Values{
		"name": {"badcam"}, "source": {"ffmpeg:"},
	})
	if resp.StatusCode != 400 {
		t.Errorf("empty ffmpeg args create: status = %d, want 400", resp.StatusCode)
	}
	got, _ := config.Load(cfgPath)
	if len(got.Watches) != 1 {
		t.Errorf("watches after rejected create = %d, want 1 (unchanged)", len(got.Watches))
	}
}

func TestCreateDuplicateNameRejected(t *testing.T) {
	s, _ := newTestServer(t)
	resp, _ := postForm(t, s.Handler(), "/watch/new", url.Values{
		"name": {"printer"}, "source": {"http://x/snap.jpg"},
	})
	if resp.StatusCode != 400 {
		t.Errorf("duplicate create: status = %d, want 400", resp.StatusCode)
	}
}

func TestSaveIOErrorReturns500(t *testing.T) {
	s, _ := newTestServer(t)
	s.cfgPath = filepath.Join(t.TempDir(), "missing", "config.yaml")
	form := url.Values{
		"x": {"0.25"}, "y": {"0.25"}, "w": {"0.5"}, "h": {"0.25"},
		"ttype": {"ocr_match"}, "pattern": {"(?i)done"}, "op": {""},
		"tthreshold": {"0"}, "confirm": {"2"}, "cooldown": {"10m"}, "interval": {"5s"},
		"pp_grayscale": {"on"}, "pp_threshold": {"128"}, "pp_upscale": {"2"},
		"notify": {"ntfy://ntfy.sh/topic\n"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 500 {
		t.Errorf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}
}

func TestRemoveIOErrorLeavesWatchRunning(t *testing.T) {
	s, _ := newTestServer(t)
	wc, ok := s.findWatch("printer")
	if !ok {
		t.Fatal("printer watch missing from test config")
	}
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatalf("start printer: %v", err)
	}
	s.cfgPath = filepath.Join(t.TempDir(), "missing", "config.yaml")
	resp, body := postForm(t, s.Handler(), "/watch/printer/delete", url.Values{})
	if resp.StatusCode != 500 {
		t.Errorf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}
	if running := s.sup.Running(); len(running) != 1 || running[0] != "printer" {
		t.Errorf("watch orphan-stopped after failed delete: running = %v, want [printer]", running)
	}
	// must_fix 3 applies to delete too: the rejection renders the app's own
	// error page with a link back, never a bare text/plain http.Error dump.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html error page", ct)
	}
	for _, want := range []string{"<title>Couldn&#39;t delete printer · watchglass</title>", "<h1>Couldn&#39;t delete printer</h1>",
		"keeps running", "&larr; All watches", `href="/"`, "Technical detail", "config save failed"} {
		if !strings.Contains(body, want) {
			t.Errorf("delete error page missing %q; body:\n%s", want, body)
		}
	}
}

func TestSnapshotBadSourceReturns400(t *testing.T) {
	s, _ := newTestServer(t)
	s.NewSource = func(w config.Watch) (source.Source, error) {
		return nil, errors.New("unsupported source")
	}
	resp, _ := get(t, s.Handler(), "/watch/printer/snapshot")
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestMutateConfigPreservesMQTTBlock(t *testing.T) {
	s, cfgPath := newTestServer(t)
	s.mu.Lock()
	s.cfg.MQTT = &config.MQTT{Broker: "tcp://broker:1883"}
	s.mu.Unlock()
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"pixel_change"}, "tthreshold": {"10"},
		"confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 303 {
		t.Fatalf("save status = %d, body: %s", resp.StatusCode, body)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.MQTT == nil || got.MQTT.Broker != "tcp://broker:1883" {
		t.Errorf("mqtt block dropped by UI save: %+v", got.MQTT)
	}
}

func TestOnConfigChangedFiresOnMutations(t *testing.T) {
	s, _ := newTestServer(t)
	var calls [][]string
	s.OnConfigChanged = func(watches []config.Watch) {
		var names []string
		for _, w := range watches {
			names = append(names, w.Name)
		}
		calls = append(calls, names)
	}
	resp, _ := postForm(t, s.Handler(), "/watch/new", url.Values{
		"name": {"oven"}, "source": {"http://cam2/snap.jpg"},
	})
	if resp.StatusCode != 303 {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	resp, _ = postForm(t, s.Handler(), "/watch/oven/delete", url.Values{})
	if resp.StatusCode != 303 {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	if len(calls) != 2 {
		t.Fatalf("OnConfigChanged calls = %d, want 2 (create, delete)", len(calls))
	}
	if len(calls[0]) != 2 || len(calls[1]) != 1 {
		t.Errorf("watch lists = %v", calls)
	}
}

// TestWatchesSnapshotAccessor exercises Watches(), the accessor the MQTT
// on-connect resync uses to read the live watch list instead of a stale
// boot-time capture (c3ad308's regression: reconnect syncs replayed the
// snapshot taken at startup, resurrecting deleted watches' discovery configs
// and wiping ones added or renamed after boot).
func TestWatchesSnapshotAccessor(t *testing.T) {
	s, _ := newTestServer(t)
	got := s.Watches()
	if len(got) != 1 || got[0].Name != "printer" || got[0].Trigger.Threshold != 10 {
		t.Fatalf("Watches() = %+v, want [printer] threshold 10", got)
	}

	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"pixel_change"}, "tthreshold": {"42"},
		"confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 303 {
		t.Fatalf("save status = %d, body: %s", resp.StatusCode, body)
	}
	after := s.Watches()
	if len(after) != 1 || after[0].Trigger.Threshold != 42 {
		t.Fatalf("Watches() after save = %+v, want threshold 42", after)
	}

	// The returned slice must be a copy: mutating it must not reach back into
	// the server's own state (the whole point is that a caller — the MQTT
	// on-connect closure — reads a safe-to-use snapshot, not a live alias).
	after[0].Name = "mutated"
	again := s.Watches()
	if again[0].Name != "printer" {
		t.Errorf("Watches() leaked a live reference; mutating the returned slice changed internal state: %+v", again)
	}
}

func TestAuthRejectsAndAccepts(t *testing.T) {
	s, _ := newTestServer(t)
	s.mu.Lock()
	s.cfg.Auth = &config.Auth{Username: "admin", Password: "hunter2"}
	s.mu.Unlock()
	h := s.Handler()

	resp, _ := get(t, h, "/")
	if resp.StatusCode != 401 {
		t.Fatalf("no credentials: status = %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Basic") {
		t.Errorf("WWW-Authenticate = %q", got)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Result().StatusCode != 401 {
		t.Errorf("wrong password: status = %d, want 401", rec.Result().StatusCode)
	}

	req = httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "hunter2")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Result().StatusCode != 200 {
		t.Errorf("correct credentials: status = %d, want 200", rec.Result().StatusCode)
	}
}

func TestNoAuthBlockMeansOpen(t *testing.T) {
	s, _ := newTestServer(t)
	resp, _ := get(t, s.Handler(), "/")
	if resp.StatusCode != 200 {
		t.Errorf("nil auth must not gate: status = %d", resp.StatusCode)
	}
}

func TestSaveRestartsWithCanonicalDefaults(t *testing.T) {
	s, _ := newTestServer(t)
	var restarted []config.Watch
	// Intercept at the supervisor's source factory: record the watch each
	// (re)start builds a source for.
	s.sup.NewSource = func(w config.Watch) (source.Source, error) {
		restarted = append(restarted, w)
		return &fakeSource{img: testImage()}, nil
	}
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"pixel_change"}, "tthreshold": {"10"},
		"confirm": {""}, "cooldown": {"0s"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 303 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	if len(restarted) == 0 {
		t.Fatal("restart never reached the source factory")
	}
	got := restarted[len(restarted)-1]
	if got.Trigger.Confirm != 3 {
		t.Errorf("running watch Confirm = %d, want Validate's default 3 (canonical config)", got.Trigger.Confirm)
	}
}

func TestSaveMaxIntervalAndHealthAfter(t *testing.T) {
	s, cfgPath := newTestServer(t)
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"pixel_change"}, "tthreshold": {"10"},
		"confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
		"max_interval": {"1m"}, "health_after": {"5"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 303 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	got, _ := config.Load(cfgPath)
	w := got.Watches[0]
	if time.Duration(w.MaxInterval) != time.Minute || w.HealthAfter != 5 {
		t.Errorf("max_interval=%v health_after=%d", time.Duration(w.MaxInterval), w.HealthAfter)
	}
	// Empty max_interval means off (zero).
	form.Set("max_interval", "")
	form.Set("health_after", "")
	if resp, _ := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("empty optional fields rejected: %d", resp.StatusCode)
	}
	got, _ = config.Load(cfgPath)
	if got.Watches[0].MaxInterval != 0 {
		t.Errorf("empty max_interval should clear it, got %v", got.Watches[0].MaxInterval)
	}
	// Empty health_after saves as 0, but config.Load runs Validate() on
	// every load, which replaces a zero HealthAfter with the default (3) -
	// so the reloaded watch should read back 3, not 0.
	if got.Watches[0].HealthAfter != 3 {
		t.Errorf("empty health_after should reload as the Validate default 3, got %d", got.Watches[0].HealthAfter)
	}
}

func TestAuthCoversStaticAndAPI(t *testing.T) {
	s, _ := newTestServer(t)
	s.mu.Lock()
	s.cfg.Auth = &config.Auth{Username: "u", Password: "p"}
	s.mu.Unlock()
	h := s.Handler()
	for _, path := range []string{"/static/app.js", "/watch/printer/snapshot", "/watch/printer/live"} {
		resp, _ := get(t, h, path)
		if resp.StatusCode != 401 {
			t.Errorf("%s: status = %d, want 401 (auth must cover everything)", path, resp.StatusCode)
		}
	}
}

func TestBasePathPrefixesLinksAndRedirects(t *testing.T) {
	s, _ := newTestServer(t)
	s.BasePath = "/wg"
	h := s.Handler()

	_, body := get(t, h, "/")
	for _, want := range []string{`href="/wg/watch/printer"`, `action="/wg/watch/new"`} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %s", want)
		}
	}
	_, body = get(t, h, "/watch/printer")
	for _, want := range []string{`src="/wg/static/app.js"`, `data-base="/wg"`, `action="/wg/watch/printer/save"`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %s", want)
		}
	}
	resp, _ := postForm(t, h, "/watch/new", url.Values{
		"name": {"oven"}, "source": {"http://cam2/snap.jpg"},
	})
	if resp.StatusCode != 303 {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/wg/watch/oven" {
		t.Errorf("redirect Location = %q, want /wg/watch/oven", loc)
	}
}

func TestEmptyBasePathUnchanged(t *testing.T) {
	s, _ := newTestServer(t)
	_, body := get(t, s.Handler(), "/")
	if !strings.Contains(body, `href="/watch/printer"`) {
		t.Error("empty base path must leave URLs bare")
	}
}

// postFormWithHeaders is postForm plus the ability to set extra headers
// (Sec-Fetch-Site, Origin, ...) for the cross-origin-protection tests.
func postFormWithHeaders(t *testing.T, h http.Handler, path string, form url.Values, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// Bug 5: the four mutation POST routes had no CSRF protection. A
// same-origin browser POST or a non-browser client (curl, no fetch
// metadata headers) must still work; a cross-site browser POST must not.
func TestCrossOriginProtectionBlocksCrossSitePOST(t *testing.T) {
	s, cfgPath := newTestServer(t)
	resp, body := postFormWithHeaders(t, s.Handler(), "/watch/printer/delete", url.Values{},
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST: status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Watches) != 1 {
		t.Errorf("cross-site POST must not have taken effect: watches = %d, want 1", len(got.Watches))
	}
}

func TestCrossOriginProtectionAllowsSameOriginPOST(t *testing.T) {
	s, _ := newTestServer(t)
	resp, body := postFormWithHeaders(t, s.Handler(), "/watch/printer/delete", url.Values{},
		map[string]string{"Sec-Fetch-Site": "same-origin"})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("same-origin POST: status = %d, want 303; body: %s", resp.StatusCode, body)
	}
}

func TestCrossOriginProtectionAllowsNoFetchMetadataPOST(t *testing.T) {
	s, _ := newTestServer(t)
	// No Sec-Fetch-Site and no Origin header at all: curl and other
	// non-browser clients must keep working.
	resp, body := postFormWithHeaders(t, s.Handler(), "/watch/printer/delete", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("no-fetch-metadata POST: status = %d, want 303; body: %s", resp.StatusCode, body)
	}
}

// Bug 6 (web-level): a name with a path-breaking character must be
// rejected at /watch/new, not just at the config layer.
func TestCreateRejectsUnroutableName(t *testing.T) {
	s, cfgPath := newTestServer(t)
	resp, _ := postForm(t, s.Handler(), "/watch/new", url.Values{
		"name": {"kitchen/oven"}, "source": {"http://cam2/snap.jpg"},
	})
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	got, _ := config.Load(cfgPath)
	if len(got.Watches) != 1 {
		t.Errorf("watches after rejected create = %d, want 1 (unchanged)", len(got.Watches))
	}
}

// Minor: parseRegion must reject NaN/Inf the same way it rejects
// out-of-bounds values — strconv.ParseFloat happily parses "NaN" and "Inf"
// as valid floats, and NaN in particular sails through every plain
// comparison (<, <=, >), so the existing bounds check alone lets it by.
// must_fix 1 case 1: a watch that config.yaml still lists but that isn't
// actually running (e.g. a failed restart) must render as stopped on the
// dashboard, never as a stale green "healthy" reading.
func TestIndexShowsStoppedWhenNotRunning(t *testing.T) {
	s, _ := newTestServer(t)
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "42% changed", Fired: false, PNG: pngBytes(t)})
	s.sup.Stop("printer") // newTestServer doesn't actually start it, but be explicit/robust
	_, body := get(t, s.Handler(), "/")
	// Isolate the watch row from the rest of the page so the LED checks
	// below only see this watch's own indicator.
	rowStart := strings.Index(body, `data-label="Last reading"`)
	if rowStart == -1 {
		t.Fatalf("Last reading cell not found; body:\n%s", body)
	}
	row := body[rowStart:]
	if !strings.Contains(row, "led-stopped") || !strings.Contains(row, "stopped") {
		t.Errorf("stopped watch with a stale reading must show stopped, not the stale reading; row:\n%s", row)
	}
	if strings.Contains(row, "led-green") || strings.Contains(row, "42% changed") {
		t.Errorf("stopped watch must not show a healthy LED or the stale reading; row:\n%s", row)
	}
}

// must_fix 1 case 2: a running watch whose source has been failing must
// show a distinct error indicator with the failure text, not the same
// neutral "no data yet" a brand-new watch shows.
func TestIndexShowsErrorWhenHealthDown(t *testing.T) {
	s, _ := newTestServer(t)
	if err := s.sup.Start(context.Background(), config.Watch{
		Name: "printer", Source: "http://x/snap.jpg", Interval: config.Duration(time.Second),
		Region: config.Region{X: 0, Y: 0, W: 1, H: 1}, Trigger: config.Trigger{Type: "pixel_change", Threshold: 10},
	}); err == nil {
		t.Cleanup(func() { s.sup.Stop("printer") })
	}
	s.reg.SetHealth("printer", state.Health{Down: true, Message: "stream unreachable after 3 consecutive failures: dial tcp: connection refused"})
	_, body := get(t, s.Handler(), "/")
	if !strings.Contains(body, "led-error") {
		t.Errorf("running-but-erroring watch must show the error LED; body:\n%s", body)
	}
	if !strings.Contains(body, "connection refused") {
		t.Errorf("dashboard must surface the health error text, not stay neutral; body:\n%s", body)
	}
	if strings.Contains(body, "No readings yet") {
		t.Errorf("erroring watch must not read the same as a brand-new one; body:\n%s", body)
	}
}

// The detail page's status pill must show the same three states, plus the
// error message, and the Live panel fragment must carry a stale-since badge.
func TestDetailAndLiveShowErrorStatus(t *testing.T) {
	s, _ := newTestServer(t)
	s.sup.Start(context.Background(), config.Watch{
		Name: "printer", Source: "http://x/snap.jpg", Interval: config.Duration(time.Second),
		Region: config.Region{X: 0, Y: 0, W: 1, H: 1}, Trigger: config.Trigger{Type: "pixel_change", Threshold: 10},
	})
	t.Cleanup(func() { s.sup.Stop("printer") })
	s.reg.SetHealth("printer", state.Health{Down: true, Message: "stream unreachable: refused", Since: time.Now()})

	_, body := get(t, s.Handler(), "/watch/printer")
	// The header leads with the summary and keeps the full chain one
	// disclosure away, once.
	for _, want := range []string{"status-error", `<p class="status-summary" title="Connection refused">Connection refused</p>`,
		`<p class="tech-raw mono">stream unreachable: refused</p>`, "<title>[error] printer · watchglass</title>"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q; body:\n%s", want, body)
		}
	}
	if n := strings.Count(body, "stream unreachable: refused"); n != 1 {
		t.Errorf("the raw error chain should appear once on the detail page, got %d", n)
	}

	_, body = get(t, s.Handler(), "/watch/printer/live")
	if !strings.Contains(body, `<p class="stale-badge">Failing since`) ||
		!strings.Contains(body, `data-summary="Connection refused"`) || !strings.Contains(body, `data-message="stream unreachable: refused"`) {
		t.Errorf("live fragment missing the failing-since badge or the summary/message for the header; body:\n%s", body)
	}
	// The message is shown once, under the page title (#status-detail); the
	// badge only dates the failure instead of repeating it.
	badge := body[strings.Index(body, `<p class="stale-badge">`):]
	if badge = badge[:strings.Index(badge, "</p>")]; strings.Contains(badge, "refused") {
		t.Errorf("stale badge repeats the error message: %q", badge)
	}
	if strings.Contains(body, "stale since") {
		t.Errorf("a watch with no readings has nothing stale; body:\n%s", body)
	}
	if !strings.Contains(body, `data-hint=""`) {
		t.Errorf("a refusal with no host to judge has no hint; body:\n%s", body)
	}
	_, body = get(t, s.Handler(), "/watch/printer")
	if !strings.Contains(body, `<p class="status-hint" hidden></p>`) {
		t.Errorf("the hint line should be there, empty and hidden; body:\n%s", body)
	}

	// A camera at 127.0.0.1 that refuses: the header says what that means
	// inside Docker or WSL, under the sentence, and the Live poll carries
	// the same advice for app.js to keep in step.
	t.Setenv("WATCHGLASS_IN_CONTAINER", "")
	s.reg.SetHealth("printer", state.Health{Down: true, Since: time.Now(),
		Message: `no reading for 3 consecutive polls: grab: snapshot http://127.0.0.1:8102/snapshot.jpg: Get "http://127.0.0.1:8102/snapshot.jpg": dial tcp 127.0.0.1:8102: connect: connection refused`})
	hint := "If watchglass runs in Docker or WSL, 127.0.0.1 is that container, not your computer. Use the host&#39;s LAN IP or host.docker.internal."
	_, body = get(t, s.Handler(), "/watch/printer")
	if !strings.Contains(body, `<p class="status-summary" title="Connection refused by 127.0.0.1:8102">Connection refused by 127.0.0.1:8102</p>`) ||
		!strings.Contains(body, `<p class="status-hint">`+hint+`</p>`) {
		t.Errorf("detail header should show the loopback hint under the summary; body:\n%s", body)
	}
	_, body = get(t, s.Handler(), "/watch/printer/live")
	if !strings.Contains(body, `data-hint="`+hint+`"`) {
		t.Errorf("live fragment should carry the hint; body:\n%s", body)
	}
	js, err := assets.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"updateStatus(ds.state, ds.summary, ds.message, ds.hint);", `querySelector(".status-hint")`, "hintEl.hidden = !hint;"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js should keep the hint in step with the poll: missing %q", want)
		}
	}
}

// must_fix 3: server-side rejections on create/save must render the app's
// normal page chrome (brand header + a link back), never a bare
// http.Error() text body.
func TestCreateRejectionRendersAppChrome(t *testing.T) {
	s, _ := newTestServer(t)
	resp, body := postForm(t, s.Handler(), "/watch/new", url.Values{
		"name": {"printer"}, "source": {"http://x/snap.jpg"}, // duplicate name
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	// A rejected create is the index again: the add form keeps what was
	// typed and the problem sits under the field it concerns.
	for _, want := range []string{"<title>Watches · watchglass</title>", `class="watch-table"`,
		`value="printer"`, `value="http://x/snap.jpg"`,
		`aria-describedby="err-name hint-name" aria-invalid="true" autofocus`,
		`<p id="err-name" class="field-error">A watch named &#34;printer&#34; already exists.</p>`} {
		if !strings.Contains(body, want) {
			t.Errorf("rejected create missing %q; body:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"<pre>", "duplicate watch name", `err-source`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("rejected create should not contain %q; body:\n%s", unwanted, body)
		}
	}
}

func TestCreateRejectionMapsEachFieldAndKeepsValues(t *testing.T) {
	s, cfgPath := newTestServer(t)
	cases := []struct {
		name, source, field, want string
	}{
		{"", "http://x/a.jpg", "name", "Enter a name for the watch."},
		{"cam", "  ", "source", "Enter the camera&#39;s source URL."},
		{"kitchen/oven", "http://x/a.jpg", "name", "Names can&#39;t contain /, ?, # or control characters."},
		{"cam", "ftp://cam/x", "source", "That source isn&#39;t supported. It must start with http://, https://, rtsp://, rtsps://, v4l2:, dshow: or ffmpeg:."},
		{"cam", "ffmpeg:", "source", "An ffmpeg: source needs its input arguments"},
	}
	for _, c := range cases {
		resp, body := postForm(t, s.Handler(), "/watch/new", url.Values{"name": {c.name}, "source": {c.source}})
		if resp.StatusCode != 400 {
			t.Errorf("%q/%q: status = %d, want 400", c.name, c.source, resp.StatusCode)
		}
		if !strings.Contains(body, `<p id="err-`+c.field+`" class="field-error">`+c.want) {
			t.Errorf("%q/%q: want %s error %q; body:\n%s", c.name, c.source, c.field, c.want, body)
		}
	}
	if got, _ := config.Load(cfgPath); len(got.Watches) != 1 {
		t.Errorf("rejected creates must not write: watches = %d, want 1", len(got.Watches))
	}
}

// A bad name and a bad source are reported together: the user fixes both
// in one pass instead of learning about the source once the name is fine.
// Focus still lands on the first field with a problem.
func TestCreateRejectionReportsBothFields(t *testing.T) {
	s, cfgPath := newTestServer(t)
	cases := []struct{ name, source, wantName, wantSource string }{
		{"   ", "notaurl", "Enter a name for the watch.", "That source isn&#39;t supported."},
		{"kitchen/oven", "ffmpeg:", "Names can&#39;t contain /, ?, # or control characters.", "An ffmpeg: source needs its input arguments"},
		{"printer", "http://", "A watch named &#34;printer&#34; already exists.", "The source needs an address after the scheme"},
		{"..", "", "A name can&#39;t be just &#34;.&#34; or &#34;..&#34;", "Enter the camera&#39;s source URL."},
	}
	for _, c := range cases {
		resp, body := postForm(t, s.Handler(), "/watch/new", url.Values{"name": {c.name}, "source": {c.source}})
		if resp.StatusCode != 400 {
			t.Errorf("%q/%q: status = %d, want 400", c.name, c.source, resp.StatusCode)
		}
		if !strings.Contains(body, `<p id="err-name" class="field-error">`+c.wantName) {
			t.Errorf("%q/%q: want name error %q; body:\n%s", c.name, c.source, c.wantName, body)
		}
		if !strings.Contains(body, `<p id="err-source" class="field-error">`+c.wantSource) {
			t.Errorf("%q/%q: want source error %q; body:\n%s", c.name, c.source, c.wantSource, body)
		}
		if !strings.Contains(body, `aria-describedby="err-name hint-name" aria-invalid="true" autofocus`) ||
			!strings.Contains(body, `aria-describedby="err-source hint-source" aria-invalid="true">`) {
			t.Errorf("%q/%q: focus should land on the name field only; body:\n%s", c.name, c.source, body)
		}
	}
	if got, _ := config.Load(cfgPath); len(got.Watches) != 1 {
		t.Errorf("rejected creates must not write: watches = %d, want 1", len(got.Watches))
	}
}

// A rejected save is the detail form again, with what was submitted still in
// it and the problem marked on its field; one typo never costs every edit.
func TestSaveRejectionRerendersFormWithFieldErrors(t *testing.T) {
	s, cfgPath := newTestServer(t)
	form := url.Values{
		"x": {"0.1"}, "y": {"0.2"}, "w": {"0.3"}, "h": {"0.4"},
		"ttype": {"ocr_match"}, "pattern": {"("}, // invalid regex
		"tthreshold": {"0"}, "confirm": {"2"}, "cooldown": {"7s"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`id="watchform"`, `action="/watch/printer/save"`, "&larr; All watches",
		`<div id="form-errors" class="form-alert" role="alert" tabindex="-1" autofocus>`,
		"Not saved. One field needs a fix:", `<a href="#f-pattern">Pattern isn&#39;t a valid regular expression. A &#34;(&#34; is never closed.</a>`,
		`name="pattern" value="("`, `aria-describedby="err-pattern"`, `<p id="err-pattern" class="field-error">`,
		`name="cooldown" value="7s"`, `name="x" value="0.1"`, `<option value="ocr_match" selected`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rejected save missing %q; body:\n%s", want, body)
		}
	}
	if got, _ := config.Load(cfgPath); got.Watches[0].Trigger.Type != "pixel_change" {
		t.Error("a rejected save must not modify the config file")
	}
}

// Every unparseable field is reported at once, verbatim, in form order.
func TestSaveRejectionReportsEveryParseError(t *testing.T) {
	s, _ := newTestServer(t)
	form := url.Values{
		"x": {"0.8"}, "y": {"0"}, "w": {"0.5"}, "h": {"1"}, // off the right edge
		"ttype": {"pixel_change"}, "tthreshold": {"10"}, "confirm": {"1"},
		"cooldown": {"soon"}, "interval": {"abc"}, "max_interval": {"later"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	wants := []string{
		`<a href="#stage">Region must fit inside the frame`,
		`<a href="#f-cooldown">Cooldown &#34;soon&#34; isn&#39;t a duration.`,
		`<a href="#f-interval">Interval &#34;abc&#34; isn&#39;t a duration. Use a number with a unit, for example 5s, 1m30s or 2h.`,
		`<a href="#f-maxinterval">Max interval &#34;later&#34; isn&#39;t a duration.`,
	}
	last := -1
	for _, want := range wants {
		i := strings.Index(body, want)
		if i < 0 {
			t.Errorf("rejected save missing %q; body:\n%s", want, body)
			continue
		}
		if i < last {
			t.Errorf("%q is out of form order", want)
		}
		last = i
	}
	for _, want := range []string{"4 fields need a fix", `name="interval" value="abc"`, `name="max_interval" value="later"`, `id="err-region"`} {
		if !strings.Contains(body, want) {
			t.Errorf("rejected save missing %q", want)
		}
	}
}

// A config.Validate rejection (the form parsed, the values don't hold)
// lands on the right field too, with the config package's wording
// translated.
func TestSaveValidateRejectionMarksField(t *testing.T) {
	s, _ := newTestServer(t)
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"pixel_change"}, "tthreshold": {"10"}, "confirm": {"1"}, "interval": {"0.5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body, `<p id="err-interval" class="field-error">Interval must be at least 1s, for example 5s or 1m30s.</p>`) ||
		!strings.Contains(body, `name="interval" value="0.5s"`) || strings.Contains(body, "watch &#34;printer&#34;") {
		t.Errorf("sub-second interval should be marked on Interval in plain words; body:\n%s", body)
	}
}

// must_fix 1 case 1 + must_fix 3: a restart failure (e.g. the trigger type
// requires an OCR engine watchglass doesn't have) must render the styled
// error page AND leave the watch stopped, not a still-running stale state.
func TestSaveRestartFailureStopsWatchAndRendersErrorPage(t *testing.T) {
	// newTestServer's supervisor is built with a fixed fake OCR engine, so
	// this needs its own server whose supervisor genuinely has none —
	// otherwise Restart would succeed and there'd be nothing to test.
	// The file already says ocr_match (a hand edit, or tesseract gone since):
	// the save changes neither type nor engine, so it is not refused up
	// front (see TestSaveRefusesTypeThatCannotRunHere) and reaches Restart.
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{Watches: []config.Watch{{
		Name: "printer", Source: "http://unused.invalid/snap.jpg",
		Interval: config.Duration(time.Second), Region: config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger: config.Trigger{Type: "ocr_match", Pattern: "(?i)done"},
	}}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	reg := state.New(5)
	sup := supervisor.New(nil, reg, ocr.Engines{}, func(string, ...any) {}) // no tesseract
	sup.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, ocr.Engines{}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	// It was running as pixel_change when the file was edited underneath it.
	running := cfg.Watches[0]
	running.Trigger = config.Trigger{Type: "pixel_change", Threshold: 10}
	if err := sup.Start(context.Background(), running); err != nil {
		t.Fatalf("start printer: %v", err)
	}

	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"ocr_match"}, "pattern": {"(?i)done"},
		"tthreshold": {"0"}, "confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{"<h1>Saved, but the watch didn&#39;t restart</h1>", "printer is stopped until you fix this and save again",
		`<span class="error-reason-label">Reason</span> This trigger type reads text with tesseract, and tesseract isn&#39;t installed.`,
		"&larr; Back to printer", `href="/watch/printer"`, "Technical detail"} {
		if !strings.Contains(body, want) {
			t.Errorf("restart-failure page missing %q; body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Back to your edits") {
		t.Error("the settings were saved, so there are no unsaved edits to go back to")
	}
	if running := s.sup.Running(); len(running) != 0 {
		t.Errorf("watch should be stopped after a failed restart, got running = %v", running)
	}
	_, indexBody := get(t, s.Handler(), "/")
	rowStart := strings.Index(indexBody, `data-label="Last reading"`)
	if rowStart == -1 {
		t.Fatalf("Last reading cell not found; body:\n%s", indexBody)
	}
	row := indexBody[rowStart:]
	if !strings.Contains(row, "stopped") || strings.Contains(row, "led-green") {
		t.Errorf("dashboard must show the watch stopped after a failed restart, not a healthy LED; row:\n%s", row)
	}
}

// should_fix 2: with no OCR engine configured, the OCR-only trigger types
// must be disabled in the dropdown so a config known to fail restart can't
// be written in the first place.
func TestDetailDisablesOCRTypesWithoutEngine(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	_, body := get(t, s.Handler(), "/watch/printer")
	for _, want := range []string{`value="ocr_match" `, `value="ocr_changed" `, `value="numeric" `} {
		idx := strings.Index(body, want)
		if idx == -1 {
			t.Fatalf("option %q not found; body:\n%s", want, body)
		}
		// disabled should appear on the same <option> line, shortly after.
		line := body[idx : idx+120]
		if !strings.Contains(line, "disabled") {
			t.Errorf("option %q should be disabled without an OCR engine: %q", want, line)
		}
	}
}

func TestDetailEnablesOCRTypesWithEngine(t *testing.T) {
	s, _ := newTestServer(t)
	_, body := get(t, s.Handler(), "/watch/printer")
	idx := strings.Index(body, `value="ocr_match" `)
	if idx == -1 {
		t.Fatalf("ocr_match option not found; body:\n%s", body)
	}
	line := body[idx : idx+120]
	if strings.Contains(line, "disabled") {
		t.Errorf("ocr_match must not be disabled when an OCR engine is available: %q", line)
	}
}

// With no OCR engine, the watch's CURRENT type must stay enabled even when
// it is an OCR type: a disabled option is dropped from the form's entry
// list, so the save would carry no ttype and be rejected as `unknown
// trigger type ""` — every other field of the watch unsaveable. The other
// OCR types remain disabled.
func TestDetailKeepsCurrentOCRTypeEnabledWithoutEngine(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	s.mu.Lock()
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "ocr_match", Pattern: "(?i)done"}
	s.mu.Unlock()
	_, body := get(t, s.Handler(), "/watch/printer")
	optionLine := func(value string) string {
		idx := strings.Index(body, `value="`+value+`" `)
		if idx == -1 {
			t.Fatalf("option %q not found; body:\n%s", value, body)
		}
		end := strings.Index(body[idx:], "</option>")
		if end == -1 {
			t.Fatalf("option %q not closed; body:\n%s", value, body)
		}
		return body[idx : idx+end]
	}
	cur := optionLine("ocr_match")
	if !strings.Contains(cur, "selected") || strings.Contains(cur, "disabled") {
		t.Errorf("current type must be selected and NOT disabled: %q", cur)
	}
	// The lock suffix belongs to the locked options: the selected one is
	// explained by the engine note and the blocked styling instead, so the
	// closed select never reads "(needs tesseract)".
	if strings.Contains(cur, "needs tesseract") {
		t.Errorf("current type must not carry the lock suffix: %q", cur)
	}
	for _, other := range []string{"ocr_changed", "numeric"} {
		if line := optionLine(other); !strings.Contains(line, "disabled") || !strings.Contains(line, "(needs tesseract)") {
			t.Errorf("option %q should stay disabled and say why without an OCR engine: %q", other, line)
		}
	}
	for _, want := range []string{`<select id="f-ttype" name="ttype" class="needs-engine"`, `<select id="f-engine" name="engine" class="needs-engine"`,
		`<p id="engine-note" class="field-hint engine-note is-warn">Tesseract isn&#39;t on PATH, so this trigger can&#39;t run. Switch Engine to sevenseg (the seven-segment decoder) for digit displays, or install tesseract.</p>`} {
		if !strings.Contains(body, want) {
			t.Errorf("a blocked selection should be marked and explained; missing %q in body:\n%s", want, body)
		}
	}
	// And the save round-trips: editing only Cooldown keeps ttype intact.
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"ocr_match"}, "pattern": {"(?i)done"},
		"tthreshold": {"0"}, "confirm": {"1"}, "cooldown": {"45s"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if strings.Contains(body, `unknown trigger type ""`) {
		t.Errorf("save was rejected for a missing ttype; status %d body:\n%s", resp.StatusCode, body)
	}
}

// Hidden Pattern/Op controls still submit; for types that never read them
// the values must not be persisted (invisible in the UI, impossible to
// clear, and a later hand edit of the type would trip over them).
func TestSaveDropsPatternAndOpForTypesThatIgnoreThem(t *testing.T) {
	s, cfgPath := newTestServer(t)
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"pixel_change"}, "pattern": {"(?i)stale[unclosed"}, "op": {"gt"},
		"tthreshold": {"10"}, "confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 303 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if tr := got.Watches[0].Trigger; tr.Pattern != "" || tr.Op != "" {
		t.Errorf("pixel_change persisted hidden pattern/op: %+v", tr)
	}
	form.Set("ttype", "ocr_match")
	form.Set("pattern", "(?i)done")
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("ocr_match save status = %d, body: %s", resp.StatusCode, body)
	}
	got, _ = config.Load(cfgPath)
	if tr := got.Watches[0].Trigger; tr.Pattern != "(?i)done" || tr.Op != "" || tr.Threshold != 0 {
		t.Errorf("ocr_match should keep pattern and drop op and the hidden threshold: %+v", tr)
	}
	form.Set("ttype", "ocr_changed")
	form.Set("tthreshold", "7")
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("ocr_changed save status = %d, body: %s", resp.StatusCode, body)
	}
	got, _ = config.Load(cfgPath)
	if tr := got.Watches[0].Trigger; tr.Pattern != "" || tr.Op != "" || tr.Threshold != 0 {
		t.Errorf("ocr_changed reads none of pattern/op/threshold: %+v", tr)
	}
}

// A change of Type or Engine into a state this box can't run (a text
// type on tesseract without tesseract) is refused before anything is
// written: saving it would only stop the watch. The error sits under Type
// and the form comes back with the blocked selection marked. A watch
// already in that state still saves its other fields (the current type is
// never disabled), and switching to an engine that runs is fine.
func TestSaveRefusesTypeThatCannotRunHere(t *testing.T) {
	s, cfgPath := newTestServer(t)
	s.engines.Tesseract = nil
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"ocr_match"}, "pattern": {"(?i)done"}, "engine": {"tesseract"},
		"tthreshold": {"0"}, "confirm": {"1"}, "cooldown": {"9s"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	want := `<p id="err-ttype" class="field-error">ocr_match reads text with tesseract, and tesseract isn&#39;t installed on this box, so saving this would stop the watch. Nothing was saved: switch Engine to sevenseg, or install tesseract.</p>`
	if !strings.Contains(body, want) || !strings.Contains(body, `<select id="f-ttype" name="ttype" class="needs-engine" aria-invalid="true" aria-describedby="err-ttype ttype-help">`) {
		t.Errorf("refused save should explain under Type and mark the selects; body:\n%s", body)
	}
	if got, _ := config.Load(cfgPath); got.Watches[0].Trigger.Type != "pixel_change" || time.Duration(got.Watches[0].Trigger.Cooldown) == 9*time.Second {
		t.Errorf("a refused save must write nothing: %+v", got.Watches[0].Trigger)
	}
	// The page shows the submitted values, which differ from the file: the
	// Save bar starts on "Unsaved changes" (app.js keeps it there and asks
	// before the page is left), and the warn note under Engine stays out of
	// the way while the error under Type says the same thing.
	for _, want := range []string{
		`<span id="dirty-note" class="dirty-note"><span class="led led-amber"`,
		`<p class="save-note" hidden title="`,
		`<p id="engine-note" class="field-hint engine-note is-warn" hidden>Tesseract isn&#39;t on PATH, so this trigger can&#39;t run.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("refused save page missing %q; body:\n%s", want, body)
		}
	}
	// The same change onto an engine that runs here goes through.
	form.Set("engine", "sevenseg")
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Errorf("ocr_match on sevenseg should save: status %d, body: %s", resp.StatusCode, body)
	}
	// Back onto tesseract is an engine change into the blocked state.
	form.Set("engine", "tesseract")
	if resp, _ := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 400 {
		t.Errorf("switching a text watch onto a missing engine should be refused: status %d", resp.StatusCode)
	}
	// With rapidocr present the message offers it.
	s.engines.RapidOCR = fakeRapid{}
	if _, body := postForm(t, s.Handler(), "/watch/printer/save", form); !strings.Contains(body, "switch Engine to sevenseg or rapidocr, or install tesseract.") {
		t.Errorf("message should offer rapidocr when it is installed; body:\n%s", body)
	}
	// A rapidocr watch without rapidocr: the rapidocr wording.
	s.engines.RapidOCR = nil
	form.Set("engine", "rapidocr")
	if _, body := postForm(t, s.Handler(), "/watch/printer/save", form); !strings.Contains(body, `<p id="err-ttype" class="field-error">ocr_match reads text with rapidocr, and no Python with the rapidocr package was found, so saving this would stop the watch. Nothing was saved: run pip install rapidocr onnxruntime, or switch Engine.</p>`) {
		t.Errorf("rapidocr wording missing; body:\n%s", body)
	}
	// pixel_change never needs an engine, whatever Engine says.
	form.Set("ttype", "pixel_change")
	form.Set("engine", "tesseract")
	form.Set("tthreshold", "10")
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Errorf("pixel_change on a missing engine still saves: status %d, body: %s", resp.StatusCode, body)
	}
}

// The engine note and the trigger labels, at the source: the copy app.js
// mirrors (engineNoteFor / data-label) and the pill title on the index.
func TestEngineNoteAndTriggerLabels(t *testing.T) {
	w := func(typ, engine string) config.Watch {
		return config.Watch{Engine: engine, Trigger: config.Trigger{Type: typ}}
	}
	cases := []struct {
		name        string
		w           config.Watch
		tess, rapid bool
		want        engineNote
	}{
		{"pixel on tesseract, all present: row hidden", w("pixel_change", ""), true, true, engineNote{}},
		{"text on tesseract, present: nothing to say", w("ocr_match", ""), true, false, engineNote{}},
		{"text on sevenseg: nothing to say", w("numeric", "sevenseg"), false, false, engineNote{}},
		{"pixel on sevenseg without tesseract: hint", w("pixel_change", "sevenseg"), false, false,
			engineNote{Text: "Not used by pixel_change. It only matters if Type becomes a text trigger.", Show: true}},
		{"text on tesseract, missing", w("ocr_changed", "tesseract"), false, false,
			engineNote{Text: "Tesseract isn't on PATH, so this trigger can't run. Switch Engine to sevenseg (the seven-segment decoder) for digit displays, or install tesseract.", Warn: true, Show: true}},
		{"text on tesseract, missing, rapidocr here", w("ocr_changed", ""), false, true,
			engineNote{Text: "Tesseract isn't on PATH, so this trigger can't run. Switch Engine to rapidocr for printed text or sevenseg (the seven-segment decoder) for digit displays, or install tesseract.", Warn: true, Show: true}},
		{"pixel on tesseract, missing", w("pixel_change", ""), false, false,
			engineNote{Text: "Not used by pixel_change. Tesseract isn't on PATH, so the text triggers are locked while Engine is tesseract: switch to sevenseg (the seven-segment decoder) first, or install tesseract.", Show: true}},
		{"pixel on tesseract, missing, rapidocr here", w("pixel_change", ""), false, true,
			engineNote{Text: "Not used by pixel_change. Tesseract isn't on PATH, so the text triggers are locked while Engine is tesseract: switch to rapidocr or sevenseg (the seven-segment decoder) first, or install tesseract.", Show: true}},
		{"text on rapidocr, missing", w("numeric", "rapidocr"), true, false,
			engineNote{Text: "rapidocr isn't available on this box (Python with the rapidocr package). Install it with pip install rapidocr onnxruntime, or switch Engine.", Warn: true, Show: true}},
		{"pixel on rapidocr, missing", w("pixel_change", "rapidocr"), true, false,
			engineNote{Text: "Not used by pixel_change. rapidocr isn't available on this box, so the text triggers are locked on this engine: pip install rapidocr onnxruntime, or switch Engine.", Show: true}},
		{"text on rapidocr, present, no tesseract: nothing to say", w("ocr_match", "rapidocr"), false, true, engineNote{}},
	}
	for _, c := range cases {
		if got := engineNoteFor(c.w, c.tess, c.rapid); got != c.want {
			t.Errorf("%s:\n got  %+v\n want %+v", c.name, got, c.want)
		}
	}
	for typ, want := range map[string]string{"pixel_change": "pixels change", "ocr_match": "text matches pattern", "ocr_changed": "text changes", "numeric": "number vs threshold", "bogus": ""} {
		if got := triggerLabel(typ); got != want {
			t.Errorf("triggerLabel(%q) = %q, want %q", typ, got, want)
		}
	}
	// The Type options carry "id — label" as text and data-label (app.js
	// rebuilds the text from it), and the index pill titles the id.
	s, _ := newTestServer(t)
	_, body := get(t, s.Handler(), "/watch/printer")
	for _, want := range []string{
		`data-label="pixel_change — pixels change">pixel_change — pixels change</option>`,
		`data-label="ocr_match — text matches pattern"`, `>ocr_match — text matches pattern</option>`,
		`data-label="numeric — number vs threshold"`, `>numeric — number vs threshold</option>`,
		`<label for="f-op">Compare</label>`, `<option value="" selected>choose…</option>`, `<option value="gt" >above (gt)</option>`, `<option value="lt" >below (lt)</option>`,
		`<label for="f-confirm">Confirm after</label>`, `<label for="f-health">Down after</label>`,
		`<p id="confirm-help" class="field-hint">`, `<p id="cooldown-help" class="field-hint">`, `<p id="interval-help" class="field-hint">`, `<p id="max-interval-help" class="field-hint">`, `<p id="health-help" class="field-hint">`,
		`<div class="field-row" id="row-threshold">`, `aria-describedby="threshold-help"`, `<p id="threshold-help" class="field-hint"></p>`,
		`<div class="field-row" id="row-pattern">`,
		`<span id="dirty-note" class="dirty-note" hidden>`,
		`placeholder="ntfy://ntfy.sh/my-topic&#10;telegram://token@telegram?chats=@channel"`,
		`<input type="text" name="x" value="0" hidden tabindex="-1" aria-hidden="true">`,
		`<input type="text" name="w" value="1" hidden tabindex="-1" aria-hidden="true">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	for _, gone := range []string{`type="hidden"`, `title="pixel_change: percent`, `title="Consecutive`, "field-row-wide", ">-</option>", "Health after", ">gt</option>"} {
		if strings.Contains(body, gone) {
			t.Errorf("detail page still contains %q", gone)
		}
	}
	_, index := get(t, s.Handler(), "/")
	if !strings.Contains(index, `<span class="pill mono" title="pixels change">pixel_change</span>`) {
		t.Errorf("index pill should title the id with its label; body:\n%s", index)
	}
}

// The Live panel is the third place a stopped watch must not look healthy:
// the polled fragment carries a stopped marker, no green LED, and the state
// (data-state) app.js uses to keep the header pill in step.
func TestLiveFragmentConveysStopped(t *testing.T) {
	s, _ := newTestServer(t)
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "0.0% changed", PNG: pngBytes(t)})
	if running := s.sup.Running(); len(running) != 0 {
		t.Fatalf("precondition: expected no running watches, got %v", running)
	}
	_, live := get(t, s.Handler(), "/watch/printer/live")
	if !strings.Contains(live, "stopped") || !strings.Contains(live, `data-state="stopped"`) {
		t.Errorf("stopped watch's live fragment carries no stopped marker; body:\n%s", live)
	}
	if strings.Contains(live, "led-green") {
		t.Errorf("stopped watch's live fragment still renders led-green; body:\n%s", live)
	}
	if !strings.Contains(live, "0.0% changed") {
		t.Errorf("old readings should still be shown (marked as pre-stop); body:\n%s", live)
	}
	// A running watch reports its state the same way, so the pill can flip
	// back without a reload.
	wc, _ := s.findWatch("printer")
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.sup.Stop("printer") })
	_, live = get(t, s.Handler(), "/watch/printer/live")
	if !strings.Contains(live, `data-state="running"`) || strings.Contains(live, "stopped") {
		t.Errorf("running watch's live fragment should carry data-state running and no stopped marker; body:\n%s", live)
	}
}

// The stale badge dates staleness from the last real frame, not from the
// moment the failure threshold tripped, and the readout LED follows the
// verdict rather than staying green over stale readings.
func TestLiveFragmentStaleSinceUsesLastFrame(t *testing.T) {
	s, _ := newTestServer(t)
	// A source that never answers until Stop: the watch counts as running
	// without ever adding samples or verdicts of its own under the test.
	s.sup.NewSource = func(w config.Watch) (source.Source, error) { return blockingSource{}, nil }
	wc, _ := s.findWatch("printer")
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.sup.Stop("printer") })
	frameTS := time.Date(2026, 9, 11, 0, 53, 26, 0, time.Local)
	s.reg.Add("printer", state.Sample{TS: frameTS, Reading: "0.0% changed", PNG: pngBytes(t)})
	s.reg.SetHealth("printer", state.Health{Down: true, Message: "no reading for 2 consecutive polls: grab: refused", Since: frameTS.Add(4 * time.Second)})
	_, live := get(t, s.Handler(), "/watch/printer/live")
	// The server's zone is the no-script text; datetime (UTC) is what app.js
	// shows in the viewer's zone.
	if !strings.Contains(live, `Stale — last good reading <time class="mono" datetime="`+frameTS.UTC().Format(time.RFC3339)+`">00:53:26</time>`) {
		t.Errorf("stale badge should date from the last frame (00:53:26); body:\n%s", live)
	}
	if strings.Contains(live, "led-green") || !strings.Contains(live, "led-error") {
		t.Errorf("readout LED should follow the error verdict; body:\n%s", live)
	}
	// With no frame at all, fall back to the detection time.
	s.reg.Drop("printer")
	s.reg.SetHealth("printer", state.Health{Down: true, Message: "no reading: refused", Since: frameTS.Add(4 * time.Second)})
	_, live = get(t, s.Handler(), "/watch/printer/live")
	if !strings.Contains(live, `Failing since <time class="mono" datetime="`+frameTS.Add(4*time.Second).UTC().Format(time.RFC3339)+`">00:53:30</time>`) || strings.Contains(live, "Stale") {
		t.Errorf("with no readings nothing is stale: the badge should say since when it has been failing; body:\n%s", live)
	}
}

type blockingSource struct{}

func (blockingSource) Grab(ctx context.Context) (image.Image, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type ocrFailEngine struct{}

func (ocrFailEngine) Recognize(ctx context.Context, img image.Image) (string, error) {
	return "", errors.New("tesseract: exit status 1: Error opening data file tessdata/eng.traineddata")
}

// An OCR watch whose grab works but whose OCR fails on every tick must read
// as error on the dashboard and the detail pill, not as running / "no data
// yet" forever.
func TestDashboardShowsErrorWhenOCRFailsEveryTick(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	wc := config.Watch{
		Name: "printer", Source: "http://unused.invalid/snap.jpg",
		Interval: config.Duration(30 * time.Millisecond), HealthAfter: 2,
		Region:  config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger: config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 1},
	}
	cfg := &config.Config{Watches: []config.Watch{wc}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	reg := state.New(5)
	sup := supervisor.New(nil, reg, ocr.Engines{Tesseract: ocrFailEngine{}}, func(string, ...any) {})
	sup.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, ocr.Engines{Tesseract: ocrFailEngine{}}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h, ok := reg.GetHealth("printer"); ok && h.Down {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, body := get(t, s.Handler(), "/")
	rowStart := strings.Index(body, `data-label="Last reading"`)
	if rowStart == -1 {
		t.Fatalf("Last reading cell not found; body:\n%s", body)
	}
	row := body[rowStart:]
	if strings.Contains(row, "no data yet") || !strings.Contains(row, "led-error") || !strings.Contains(row, "ocr: tesseract") {
		t.Errorf("dashboard should show the OCR failure as an error; row:\n%s", row[:400])
	}
	_, detail := get(t, s.Handler(), "/watch/printer")
	if !strings.Contains(detail, "status-error") {
		t.Errorf("detail pill should be status-error; body:\n%s", detail)
	}
}

func TestTestRegionRejectsNonFiniteFloats(t *testing.T) {
	s, _ := newTestServer(t)
	for _, bad := range []url.Values{
		{"x": {"NaN"}, "y": {"0"}, "w": {"1"}, "h": {"1"}},
		{"x": {"0"}, "y": {"NaN"}, "w": {"1"}, "h": {"1"}},
		{"x": {"0"}, "y": {"0"}, "w": {"Inf"}, "h": {"1"}},
		{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"-Inf"}},
	} {
		resp, body := postForm(t, s.Handler(), "/watch/printer/test", bad)
		if resp.StatusCode != 400 {
			t.Errorf("region %v: status = %d, want 400; body: %s", bad, resp.StatusCode, body)
		}
	}
}

func fixtureSource(t *testing.T) source.Source {
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
	return &fakeSource{img: img}
}

// optionLine returns the <option value="v" ...> ... </option> markup for
// the select option with that value.
func optionLine(t *testing.T, body, value string) string {
	t.Helper()
	idx := strings.Index(body, `value="`+value+`"`)
	if idx == -1 {
		t.Fatalf("option %q not found; body:\n%s", value, body)
	}
	end := strings.Index(body[idx:], "</option>")
	if end == -1 {
		t.Fatalf("option %q not closed; body:\n%s", value, body)
	}
	return body[idx : idx+end]
}

func TestDetailRendersEngineSelect(t *testing.T) {
	s, _ := newTestServer(t)
	_, body := get(t, s.Handler(), "/watch/printer")
	for _, want := range []string{`id="f-engine"`, `name="engine"`, `id="row-engine"`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %s", want)
		}
	}
	if line := optionLine(t, body, "tesseract"); !strings.Contains(line, "selected") {
		t.Errorf("tesseract must be selected for the default engine: %q", line)
	}
	if line := optionLine(t, body, "sevenseg"); strings.Contains(line, "selected") {
		t.Errorf("sevenseg must not be selected for the default engine: %q", line)
	} else if !strings.Contains(line, "seven-segment") {
		t.Errorf("sevenseg option should say what it is: %q", line)
	}

	s.mu.Lock()
	s.cfg.Watches[0].Engine = "sevenseg"
	s.mu.Unlock()
	_, body = get(t, s.Handler(), "/watch/printer")
	if line := optionLine(t, body, "sevenseg"); !strings.Contains(line, "selected") {
		t.Errorf("saved sevenseg engine must render selected: %q", line)
	}
	if line := optionLine(t, body, "tesseract"); strings.Contains(line, "selected") {
		t.Errorf("tesseract must not be selected when sevenseg is saved: %q", line)
	}
}

// Without tesseract the OCR trigger types are only locked while the engine
// is tesseract: a sevenseg watch can switch between them freely, and the
// note tells the user the decoder is the way around a missing tesseract.
func TestDetailSevenSegUnlocksOCRTypesWithoutTesseract(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	_, body := get(t, s.Handler(), "/watch/printer")
	if !strings.Contains(body, "seven-segment decoder") {
		t.Errorf("tesseract-missing note should mention the seven-segment decoder; body:\n%s", body)
	}
	if line := optionLine(t, body, "ocr_match"); !strings.Contains(line, "disabled") {
		t.Errorf("ocr_match should stay locked for a tesseract watch without tesseract: %q", line)
	}
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "sevenseg"
	s.mu.Unlock()
	_, body = get(t, s.Handler(), "/watch/printer")
	for _, typ := range []string{"ocr_match", "ocr_changed", "numeric"} {
		if line := optionLine(t, body, typ); strings.Contains(line, "disabled") || strings.Contains(line, "needs tesseract") {
			t.Errorf("%s must be usable with the sevenseg engine and no tesseract: %q", typ, line)
		}
	}
}

func TestSaveRoundTripsEngine(t *testing.T) {
	s, cfgPath := newTestServer(t)
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"numeric"}, "pattern": {"([0-9.]+)"}, "op": {"gt"}, "engine": {"sevenseg"},
		"tthreshold": {"25"}, "confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 303 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Watches[0].Engine != "sevenseg" {
		t.Errorf("engine = %q after save, want sevenseg", got.Watches[0].Engine)
	}
	if running := s.sup.Running(); len(running) != 1 {
		t.Errorf("watch not running after save: %v", running)
	}
	// Back to tesseract: the default is spelled by omission, so the file
	// loses the key rather than gaining engine: tesseract.
	form.Set("engine", "tesseract")
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	got, err = config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Watches[0].Engine != "" {
		t.Errorf("engine = %q after switching back, want the default (empty)", got.Watches[0].Engine)
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "engine") {
		t.Errorf("default engine should not be written to the file:\n%s", raw)
	}
	// An unknown engine is rejected before anything is persisted.
	form.Set("engine", "bogus")
	if resp, _ := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 400 {
		t.Errorf("bogus engine: status = %d, want 400", resp.StatusCode)
	}
	// rapidocr passes through the form untouched and is written as-is.
	s, cfgPath = newTestServerWith(t, ocr.Engines{Tesseract: fakeDetailed{}, RapidOCR: fakeRapid{}})
	form.Set("engine", "rapidocr")
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("rapidocr: status = %d, body: %s", resp.StatusCode, body)
	}
	got, err = config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Watches[0].Engine != "rapidocr" {
		t.Errorf("engine = %q after save, want rapidocr", got.Watches[0].Engine)
	}
	if running := s.sup.Running(); len(running) != 1 {
		t.Errorf("watch not running after save: %v", running)
	}
}

// Test this region with the sevenseg engine shows the decoder's digits and
// one confidence chip per glyph — without ever touching tesseract.
func TestTestRegionSevenSegDecodesDigits(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	s.NewSource = func(w config.Watch) (source.Source, error) { return fixtureSource(t), nil }
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"numeric"}, "engine": {"sevenseg"}}
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "23.5") {
		t.Errorf("fragment should show the decoded reading; body:\n%s", body)
	}
	if strings.Contains(body, "tesseract") {
		t.Errorf("sevenseg test must not mention tesseract; body:\n%s", body)
	}
	if n := strings.Count(body, "word-chip"); n != 4 {
		t.Errorf("want 4 glyph chips (2 3 . 5), got %d; body:\n%s", n, body)
	}
	if strings.Contains(body, "conf-low") {
		t.Errorf("a clean fixture should decode with every glyph above the 60 line; body:\n%s", body)
	}
}

// A request that does not say which engine (older clients, curl) uses the
// watch's configured one; the form's choice wins over the saved one so the
// user can try the decoder before saving.
func TestTestRegionEngineFallsBackToWatchConfig(t *testing.T) {
	s, _ := newTestServer(t)
	s.NewSource = func(w config.Watch) (source.Source, error) { return fixtureSource(t), nil }
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "sevenseg"
	s.mu.Unlock()
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}}
	_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, "23.5") {
		t.Errorf("no engine field: should use the watch's sevenseg engine; body:\n%s", body)
	}
	form.Set("engine", "tesseract")
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, "PRINT COMPLETE") || strings.Contains(body, "23.5") {
		t.Errorf("engine=tesseract in the form must override the saved sevenseg; body:\n%s", body)
	}
}

func TestTestRegionTesseractNoteMentionsSevenSeg(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"ocr_match"}, "engine": {"tesseract"}}
	_, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if !strings.Contains(body, "tesseract isn") || !strings.Contains(body, "try Engine: sevenseg") {
		t.Errorf("note should say tesseract is missing and point a digit display at sevenseg; body:\n%s", body)
	}
	form.Set("engine", "bogus")
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 200 || !strings.Contains(body, "bogus") {
		t.Errorf("unknown engine should be reported in the fragment, status %d; body:\n%s", resp.StatusCode, body)
	}
}

// fakeRapid stands in for the RapidOCR engine: one Word per recognized
// line, confidences already scaled to 0-100 (see ocr.RapidOCR).
type fakeRapid struct{}

func (fakeRapid) Recognize(ctx context.Context, img image.Image) (string, error) {
	return "PRINTER-01 PRINTING 79%", nil
}
func (fakeRapid) RecognizeWords(ctx context.Context, img image.Image) (string, []ocr.Word, error) {
	return "PRINTER-01 PRINTING 79%", []ocr.Word{{Text: "PRINTER-01", Conf: 99.994}, {Text: "PRINTING", Conf: 99.995}, {Text: "79%", Conf: 99.676}}, nil
}

func TestDetailRendersRapidOCROption(t *testing.T) {
	s, _ := newTestServer(t)
	_, body := get(t, s.Handler(), "/watch/printer")
	line := optionLine(t, body, "rapidocr")
	if strings.Contains(line, "selected") {
		t.Errorf("rapidocr must not be selected for the default engine: %q", line)
	}
	if !strings.HasSuffix(line, ">rapidocr — text (not installed)") {
		t.Errorf("rapidocr option should say what it is and that it is missing: %q", line)
	}
	if !strings.Contains(body, `data-rapidocr="0"`) {
		t.Errorf("engine select should carry data-rapidocr=0 when the engine is absent; body:\n%s", body)
	}
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "rapidocr"
	s.mu.Unlock()
	s.engines.RapidOCR = fakeRapid{}
	_, body = get(t, s.Handler(), "/watch/printer")
	if line := optionLine(t, body, "rapidocr"); !strings.Contains(line, "selected") {
		t.Errorf("saved rapidocr engine must render selected: %q", line)
	}
	for _, other := range []string{"tesseract", "sevenseg"} {
		if line := optionLine(t, body, other); strings.Contains(line, "selected") {
			t.Errorf("%s must not be selected when rapidocr is saved: %q", other, line)
		}
	}
	if !strings.Contains(body, `data-rapidocr="1"`) {
		t.Errorf("engine select should carry data-rapidocr=1 when the engine is present; body:\n%s", body)
	}
}

// A rapidocr watch on a box without a rapidocr Python locks the OCR types
// it isn't currently on, annotated with the engine that is missing — not
// tesseract, which is present here — and the note says how to install it.
func TestDetailRapidOCRWatchLocksTypesWithoutRapidOCR(t *testing.T) {
	s, _ := newTestServer(t)
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "rapidocr"
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "ocr_match", Pattern: "(?i)done"}
	s.mu.Unlock()
	_, body := get(t, s.Handler(), "/watch/printer")
	cur := optionLine(t, body, "ocr_match")
	if !strings.Contains(cur, "selected") || strings.Contains(cur, "disabled") || strings.Contains(cur, "needs ") {
		t.Errorf("current type must be selected, NOT disabled and carry no lock suffix: %q", cur)
	}
	for _, other := range []string{"ocr_changed", "numeric"} {
		line := optionLine(t, body, other)
		if !strings.Contains(line, "disabled") || !strings.Contains(line, "(needs rapidocr)") {
			t.Errorf("%s should be locked and say it needs rapidocr: %q", other, line)
		}
		if strings.Contains(line, "needs tesseract") || !strings.Contains(line, `data-needs-rapidocr="1"`) || strings.Contains(line, "data-needs-tesseract") {
			t.Errorf("%s: tesseract is present, only the rapidocr lock applies: %q", other, line)
		}
	}
	// One note, in its warn state, naming the missing engine and the fix.
	if !strings.Contains(body, `<p id="engine-note" class="field-hint engine-note is-warn">rapidocr isn&#39;t available on this box (Python with the rapidocr package). Install it with pip install rapidocr onnxruntime, or switch Engine.</p>`) {
		t.Errorf("note should say rapidocr is missing and how to install it; body:\n%s", body)
	}
	if strings.Contains(body, "Tesseract isn&#39;t on PATH") || strings.Count(body, `id="engine-note"`) != 1 {
		t.Errorf("tesseract is present; only the rapidocr note renders; body:\n%s", body)
	}
	// The saved type round-trips like the tesseract case.
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"ocr_match"}, "pattern": {"(?i)done"}, "engine": {"rapidocr"},
		"tthreshold": {"0"}, "confirm": {"1"}, "cooldown": {"45s"}, "interval": {"5s"},
	}
	if resp, body := postForm(t, s.Handler(), "/watch/printer/save", form); strings.Contains(body, `unknown trigger type ""`) {
		t.Errorf("save was rejected for a missing ttype; status %d body:\n%s", resp.StatusCode, body)
	}
	// A watch on another engine gets no rapidocr note at all.
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "sevenseg"
	s.mu.Unlock()
	_, body = get(t, s.Handler(), "/watch/printer")
	if strings.Contains(body, "rapidocr isn&#39;t available") || !strings.Contains(body, `<p id="engine-note" class="field-hint engine-note" hidden></p>`) {
		t.Errorf("a sevenseg watch must not carry the rapidocr note; body:\n%s", body)
	}
	for _, typ := range []string{"ocr_match", "ocr_changed", "numeric"} {
		if line := optionLine(t, body, typ); strings.Contains(line, "disabled") || strings.Contains(line, "needs ") {
			t.Errorf("%s must be usable with the sevenseg engine: %q", typ, line)
		}
	}
	if strings.Contains(body, "needs-engine") {
		t.Errorf("a sevenseg watch is not blocked; body:\n%s", body)
	}
}

// With rapidocr present the OCR types are open for a rapidocr watch even
// when tesseract is missing; app.js gets both facts to re-derive the lock.
func TestDetailRapidOCRWatchUnlockedWithRapidOCRAndNoTesseract(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	s.engines.RapidOCR = fakeRapid{}
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "rapidocr"
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "ocr_match", Pattern: "(?i)done"}
	s.mu.Unlock()
	_, body := get(t, s.Handler(), "/watch/printer")
	for _, typ := range []string{"ocr_match", "ocr_changed", "numeric"} {
		line := optionLine(t, body, typ)
		if strings.Contains(line, "disabled") || strings.Contains(line, "needs ") {
			t.Errorf("%s must be usable with rapidocr present: %q", typ, line)
		}
		if !strings.Contains(line, `data-needs-tesseract="1"`) || strings.Contains(line, "data-needs-rapidocr") {
			t.Errorf("%s should flag only the absent tesseract for app.js: %q", typ, line)
		}
	}
	if !strings.Contains(body, `data-tesseract="0"`) || !strings.Contains(body, `data-rapidocr="1"`) {
		t.Errorf("engine select should carry both availability flags; body:\n%s", body)
	}
	// The watch reads with rapidocr, which is here, so no engine note: the
	// tesseract option's "(not installed)" suffix says all there is to say.
	if strings.Contains(body, "isn&#39;t available") || strings.Contains(body, "isn&#39;t on PATH") || strings.Contains(body, "needs-engine") {
		t.Errorf("a runnable rapidocr watch must carry no engine note or blocked styling; body:\n%s", body)
	}
	if !strings.Contains(body, `<p id="engine-note" class="field-hint engine-note" hidden></p>`) {
		t.Errorf("the note element should render hidden for app.js; body:\n%s", body)
	}
	if line := optionLine(t, body, "tesseract"); !strings.HasSuffix(line, ">tesseract — text (not installed)") {
		t.Errorf("the tesseract option should say it is not installed: %q", line)
	}
	if line := optionLine(t, body, "rapidocr"); !strings.HasSuffix(line, ">rapidocr — text, harder cases") {
		t.Errorf("the rapidocr option should not claim to be missing: %q", line)
	}
	// A pixel_change watch on rapidocr shows the Engine row (tesseract is
	// missing, so the row is the way to the text types) with the hint that
	// pixel_change doesn't use it, and no warning.
	s.mu.Lock()
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "pixel_change", Threshold: 5}
	s.mu.Unlock()
	_, body = get(t, s.Handler(), "/watch/printer")
	if strings.Contains(body, "seven-segment decoder") {
		t.Errorf("a pixel_change rapidocr watch's OCR types would use rapidocr, not the seven-segment decoder; body:\n%s", body)
	}
	if !strings.Contains(body, `<p id="engine-note" class="field-hint engine-note">Not used by pixel_change. It only matters if Type becomes a text trigger.</p>`) {
		t.Errorf("note should say pixel_change does not use the engine; body:\n%s", body)
	}
	for _, typ := range []string{"ocr_match", "ocr_changed", "numeric"} {
		if line := optionLine(t, body, typ); strings.Contains(line, "disabled") || strings.Contains(line, "needs ") {
			t.Errorf("%s must be usable with rapidocr present: %q", typ, line)
		}
	}
}

// A saved rapidocr + pixel_change watch on a box with tesseract but without
// rapidocr locks all three OCR types (none is current). Its only way out is
// the Engine select, so the page carries the flags app.js reads to keep the
// Engine row visible for it (data-tesseract=1 alone would hide the row).
func TestDetailRapidOCRPixelChangeWatchLocksTypesWithoutRapidOCR(t *testing.T) {
	s, _ := newTestServer(t)
	s.mu.Lock()
	s.cfg.Watches[0].Engine = "rapidocr"
	s.cfg.Watches[0].Trigger = config.Trigger{Type: "pixel_change", Threshold: 5}
	s.mu.Unlock()
	_, body := get(t, s.Handler(), "/watch/printer")
	if line := optionLine(t, body, "pixel_change"); !strings.Contains(line, "selected") || strings.Contains(line, "disabled") {
		t.Errorf("pixel_change must be selected and enabled: %q", line)
	}
	for _, typ := range []string{"ocr_match", "ocr_changed", "numeric"} {
		line := optionLine(t, body, typ)
		if !strings.Contains(line, "disabled") || !strings.Contains(line, "(needs rapidocr)") || !strings.Contains(line, `data-needs-rapidocr="1"`) {
			t.Errorf("%s should be locked and say it needs rapidocr: %q", typ, line)
		}
	}
	if !strings.Contains(body, `data-tesseract="1"`) || !strings.Contains(body, `data-rapidocr="0"`) {
		t.Errorf("engine select must carry both flags so app.js keeps the Engine row reachable; body:\n%s", body)
	}
	// Not a warning (pixel_change runs), but the note says what the row is for.
	if !strings.Contains(body, `<p id="engine-note" class="field-hint engine-note">Not used by pixel_change. rapidocr isn&#39;t available on this box, so the text triggers are locked on this engine: pip install rapidocr onnxruntime, or switch Engine.</p>`) {
		t.Errorf("note should point at the Engine select without warning; body:\n%s", body)
	}
	if strings.Contains(body, "isn&#39;t on PATH") || strings.Contains(body, "needs-engine") {
		t.Errorf("tesseract is present and pixel_change runs: no tesseract note, nothing blocked; body:\n%s", body)
	}
}

// A rapidocr watch on a box with neither engine: the lock is rapidocr's, so
// the tesseract note must not claim the types unlock once tesseract is
// installed — only the rapidocr note's advice is true for this watch.
func TestDetailRapidOCRWatchWithoutEitherEngine(t *testing.T) {
	s, _ := newTestServerWith(t, ocr.Engines{})
	for _, trig := range []config.Trigger{{Type: "pixel_change", Threshold: 5}, {Type: "ocr_match", Pattern: "(?i)done"}} {
		s.mu.Lock()
		s.cfg.Watches[0].Engine = "rapidocr"
		s.cfg.Watches[0].Trigger = trig
		s.mu.Unlock()
		_, body := get(t, s.Handler(), "/watch/printer")
		for _, wrong := range []string{"until it's installed", "switch Engine to sevenseg instead", "seven-segment decoder", "isn&#39;t on PATH"} {
			if strings.Contains(body, wrong) {
				t.Errorf("%s: tesseract note claims %q for a rapidocr-locked watch; body:\n%s", trig.Type, wrong, body)
			}
		}
		for _, want := range []string{"rapidocr isn&#39;t available on this box", "pip install rapidocr onnxruntime", ">tesseract — text (not installed)</option>", ">rapidocr — text (not installed)</option>"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: body should contain %q; body:\n%s", trig.Type, want, body)
			}
		}
		if strings.Count(body, `id="engine-note"`) != 1 {
			t.Errorf("%s: exactly one engine note expected; body:\n%s", trig.Type, body)
		}
		for _, typ := range []string{"ocr_match", "ocr_changed", "numeric"} {
			line := optionLine(t, body, typ)
			if typ == trig.Type {
				if strings.Contains(line, "needs ") {
					t.Errorf("%s: the selected type carries no lock suffix: %q", trig.Type, line)
				}
			} else if !strings.Contains(line, "(needs rapidocr)") || strings.Contains(line, "needs tesseract") {
				t.Errorf("%s: %s should be annotated with rapidocr, the watch's own missing engine: %q", trig.Type, typ, line)
			}
			if typ != trig.Type && !strings.Contains(line, "disabled") {
				t.Errorf("%s: %s should be locked: %q", trig.Type, typ, line)
			}
			if !strings.Contains(line, `data-needs-tesseract="1"`) || !strings.Contains(line, `data-needs-rapidocr="1"`) {
				t.Errorf("%s: %s should flag both absent engines for app.js: %q", trig.Type, typ, line)
			}
		}
	}
}

func TestTestRegionRapidOCRNoteWhenMissing(t *testing.T) {
	s, _ := newTestServer(t)
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"ocr_match"}, "engine": {"rapidocr"}}
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	// The apostrophe is HTML-escaped in the fragment; match around it.
	if !strings.Contains(body, "rapidocr isn") || !strings.Contains(body, "pip install rapidocr onnxruntime") {
		t.Errorf("note should say rapidocr is missing and how to install it; body:\n%s", body)
	}
	if strings.Contains(body, "tesseract isn") {
		t.Errorf("the tesseract note is the wrong note here; body:\n%s", body)
	}
	form.Set("ttype", "pixel_change")
	_, body = postForm(t, s.Handler(), "/watch/printer/test", form)
	if strings.Contains(body, "rapidocr isn") {
		t.Errorf("pixel_change never reads OCR; the note is noise there; body:\n%s", body)
	}
}

// Test this region with rapidocr shows each recognized line as a chip.
func TestTestRegionRapidOCRShowsLines(t *testing.T) {
	s, _ := newTestServer(t)
	s.engines.Tesseract = nil
	s.engines.RapidOCR = fakeRapid{}
	s.NewSource = func(w config.Watch) (source.Source, error) { return fixtureSource(t), nil }
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"ocr_match"}, "engine": {"rapidocr"}}
	resp, body := postForm(t, s.Handler(), "/watch/printer/test", form)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{"PRINTER-01", "PRINTING", "79%"} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment should show line %q; body:\n%s", want, body)
		}
	}
	if n := strings.Count(body, "word-chip"); n != 3 {
		t.Errorf("want 3 line chips, got %d; body:\n%s", n, body)
	}
	if strings.Contains(body, "available") || strings.Contains(body, "not on PATH") {
		t.Errorf("no availability note when the engine ran; body:\n%s", body)
	}
}

// "Fired" is shown as a visible word next to the reading on the dashboard,
// in the Live readout and on the filmstrip caption, never by colour alone
// (amber and the running mint have the same luminance).
func TestFiredIsAVisibleTag(t *testing.T) {
	s, _ := newTestServer(t)
	s.sup.NewSource = func(w config.Watch) (source.Source, error) { return blockingSource{}, nil }
	wc, _ := s.findWatch("printer")
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.sup.Stop("printer") })
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "PRINT COMPLETE", Fired: true, PNG: pngBytes(t)})

	const tag = `<span class="tag tag-fired">fired</span>`
	_, body := get(t, s.Handler(), "/")
	row := body[strings.Index(body, `data-label="Last reading"`):]
	// Reading and tag share one wrapping box (so the tag follows the value
	// instead of claiming its own column), with a real space between them.
	if !strings.Contains(row, "led-amber") || !strings.Contains(row, `<span class="reading-body"><span class="mono fired">PRINT COMPLETE</span> `+tag+`</span>`) {
		t.Errorf("dashboard row should pair the fired reading with a visible tag in one reading-body; row:\n%s", row)
	}
	_, live := get(t, s.Handler(), "/watch/printer/live")
	if n := strings.Count(live, tag); n != 2 {
		t.Errorf("live fragment should tag the readout and the fired strip frame (2), got %d; body:\n%s", n, live)
	}
	// Without a separator the caption's text reads "firedPRINT COMPLETE".
	if !strings.Contains(live, tag+` <span class="cap-body"><span class="cap-text">PRINT COMPLETE</span></span></figcaption>`) {
		t.Errorf("strip caption should separate the fired tag from the reading with a space; body:\n%s", live)
	}
	if strings.Contains(body+live, `<span class="sr-only">, fired</span>`) {
		t.Error("fired should no longer be screen-reader-only text")
	}

	if strings.Contains(row, "fired-ago") {
		t.Errorf("the fired reading carries the tag, not a \"fired … ago\" note; row:\n%s", row)
	}

	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "PRINTING 12%", PNG: pngBytes(t)})
	_, body = get(t, s.Handler(), "/")
	row = body[strings.Index(body, `data-label="Last reading"`):]
	if strings.Contains(row, "tag-fired") || !strings.Contains(row, "led-green") {
		t.Errorf("a reading that did not fire must carry no fired tag; row:\n%s", row)
	}
	// The fire itself isn't forgotten: the fired sample was latest for one
	// interval, less than the list's poll period, so the row says when it
	// was, after the current reading.
	if !strings.Contains(row, `<span class="mono">PRINTING 12%</span> <span class="fired-ago">fired just now</span></span>`) {
		t.Errorf("a watch that fired before its latest reading should say so on the dashboard; row:\n%s", row)
	}
	// Other rows never fired and say nothing.
	if n := strings.Count(body, "fired-ago"); n != 1 {
		t.Errorf("only the watch that fired gets the note, got %d; body:\n%s", n, body)
	}
}

func TestAgoText(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{0, "just now"},
		{4 * time.Second, "just now"},
		{12 * time.Second, "12 s ago"},
		{59 * time.Second, "59 s ago"},
		{time.Minute, "1 min ago"},
		{59*time.Minute + 59*time.Second, "59 min ago"},
		{time.Hour, "1 h ago"},
		{47 * time.Hour, "47 h ago"},
		{48 * time.Hour, "2 days ago"},
	} {
		if got := agoText(c.d); got != c.want {
			t.Errorf("agoText(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// A source error on the dashboard keeps red for the signal (LED + "error"
// token) and renders the message as quiet body text with the full text
// kept in title, instead of a red paragraph.
func TestIndexErrorMessageIsQuietWithFullTextInTitle(t *testing.T) {
	s, _ := newTestServer(t)
	s.sup.NewSource = func(w config.Watch) (source.Source, error) { return blockingSource{}, nil }
	wc, _ := s.findWatch("printer")
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.sup.Stop("printer") })
	msg := "no reading for 2 consecutive polls: grab: refused"
	s.reg.SetHealth("printer", state.Health{Down: true, Message: msg, Since: time.Now()})
	_, body := get(t, s.Handler(), "/")
	row := body[strings.Index(body, `data-label="Last reading"`):]
	if !strings.Contains(body, `<span class="reading-wrap reading-wrap-error">`) {
		t.Errorf("error cell should carry the reading-wrap-error modifier (mobile card layout); body:\n%s", body)
	}
	// The cell shows the summary; the full chain stays in title.
	for _, want := range []string{`<span class="tag tag-error">error</span>`, `class="reading-error-msg" title="` + msg + `">Connection refused<`} {
		if !strings.Contains(row, want) {
			t.Errorf("error row missing %q; row:\n%s", want, row)
		}
	}
	if strings.Contains(body, "conf-low") {
		t.Errorf("conf-low is for OCR confidence only, not error text; body:\n%s", body)
	}
	_, detail := get(t, s.Handler(), "/watch/printer")
	if !strings.Contains(detail, `<div id="status-detail" class="status-detail">`) || strings.Contains(detail, "conf-low") {
		t.Errorf("detail status line should be quiet mono text, not conf-low; body:\n%s", detail)
	}
}

// Stopped is its own neutral state on all three views, never error red.
func TestStoppedUsesNeutralStateEverywhere(t *testing.T) {
	s, _ := newTestServer(t)
	s.reg.Add("printer", state.Sample{TS: time.Now(), Reading: "0.0% changed", PNG: pngBytes(t)})
	_, index := get(t, s.Handler(), "/")
	_, detail := get(t, s.Handler(), "/watch/printer")
	_, live := get(t, s.Handler(), "/watch/printer/live")
	if row := index[strings.Index(index, `data-label="Last reading"`):]; !strings.Contains(row, `<span class="tag">Stopped</span>`) {
		t.Errorf("dashboard should show a neutral stopped tag; row:\n%s", row)
	}
	if !strings.Contains(detail, "status-stopped") || !strings.Contains(detail, "led-stopped") {
		t.Errorf("detail pill should be the stopped state with its own LED; body:\n%s", detail)
	}
	if !strings.Contains(live, `<p class="stale-badge stale-badge-stopped"><span class="led led-stopped" aria-hidden="true"></span>Not polling`) {
		t.Errorf("live fragment should use the neutral stopped badge and LED; body:\n%s", live)
	}
	for name, b := range map[string]string{"index": index, "detail": detail, "live": live} {
		if strings.Contains(b, "led-red") || strings.Contains(b, "led-error") {
			t.Errorf("%s: a stopped watch must not use a red LED; body:\n%s", name, b)
		}
	}
}

// The page chrome carries no status LED of its own (a light that never
// changes would dilute every real one), and Save is a real <button>.
func TestChromeHasNoStaticStatusLEDAndSaveIsAButton(t *testing.T) {
	s, _ := newTestServer(t)
	for _, path := range []string{"/", "/watch/printer"} {
		_, body := get(t, s.Handler(), path)
		head := body[:strings.Index(body, `<main id="main"`)]
		if strings.Contains(head, "led") || strings.Contains(head, "local instrument") {
			t.Errorf("%s: topbar should carry no status LED; head:\n%s", path, head)
		}
	}
	_, detail := get(t, s.Handler(), "/watch/printer")
	if !strings.Contains(detail, `<button type="submit" class="btn btn-primary">Save &amp; restart watch</button>`) {
		t.Errorf("Save should be a <button> so it shares every button state; body:\n%s", detail)
	}
}
