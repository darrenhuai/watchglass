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
	sup := supervisor.New(nil, reg, fakeDetailed{}, func(string, ...any) {})
	sup.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, fakeDetailed{}, t.Logf)
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
		// app.js's poll targets: the narrowed live region, the strip
		// container outside it, and the status pill it keeps in step.
		"id=\"live\"", "id=\"live-status\"", "aria-live=\"polite\"", "id=\"live-strip\"", "id=\"status-pill\""} {
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
	for _, want := range []string{"PRINT COMPLETE", "91.5", "data:image/png;base64,"} {
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
	s.engine = nil
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
	s.engine = nil
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
	s.engine = nil
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
	for _, want := range []string{"watchglass", "all watches", `href="/"`, "config save failed"} {
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
	// Isolate the watch row from the topbar's own always-on led-green
	// "local instrument" indicator (unrelated to any watch's status).
	rowStart := strings.Index(body, `data-label="Last reading"`)
	if rowStart == -1 {
		t.Fatalf("Last reading cell not found; body:\n%s", body)
	}
	row := body[rowStart:]
	if !strings.Contains(row, "led-red") || !strings.Contains(row, "stopped") {
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
	if strings.Contains(body, "no data yet") {
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
	if !strings.Contains(body, "status-error") || !strings.Contains(body, "stream unreachable: refused") {
		t.Errorf("detail status pill missing error state/message; body:\n%s", body)
	}

	_, body = get(t, s.Handler(), "/watch/printer/live")
	if !strings.Contains(body, "stale since") || !strings.Contains(body, "stream unreachable: refused") {
		t.Errorf("live fragment missing stale-since badge; body:\n%s", body)
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
	for _, want := range []string{"watchglass", "all watches", "duplicate watch name"} {
		if !strings.Contains(body, want) {
			t.Errorf("error page missing %q; body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<pre>") {
		t.Errorf("error page should not be a bare http.Error() dump; body:\n%s", body)
	}
}

func TestSaveRejectionRendersAppChromeWithBackLink(t *testing.T) {
	s, _ := newTestServer(t)
	form := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"ocr_match"}, "pattern": {"("}, // invalid regex
		"tthreshold": {"0"}, "confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{"watchglass", "back to printer", `href="/watch/printer"`} {
		if !strings.Contains(body, want) {
			t.Errorf("error page missing %q; body:\n%s", want, body)
		}
	}
}

// must_fix 1 case 1 + must_fix 3: a restart failure (e.g. the trigger type
// requires an OCR engine watchglass doesn't have) must render the styled
// error page AND leave the watch stopped, not a still-running stale state.
func TestSaveRestartFailureStopsWatchAndRendersErrorPage(t *testing.T) {
	// newTestServer's supervisor is built with a fixed fake OCR engine, so
	// this needs its own server whose supervisor genuinely has none —
	// otherwise Restart would succeed and there'd be nothing to test.
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{Watches: []config.Watch{{
		Name: "printer", Source: "http://unused.invalid/snap.jpg",
		Interval: config.Duration(time.Second), Region: config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger: config.Trigger{Type: "pixel_change", Threshold: 10},
	}}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	reg := state.New(5)
	sup := supervisor.New(nil, reg, nil, func(string, ...any) {}) // nil: no OCR engine
	sup.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	if err := sup.Start(context.Background(), cfg.Watches[0]); err != nil {
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
	if !strings.Contains(body, "restart failed") || !strings.Contains(body, "watchglass") {
		t.Errorf("error page missing restart-failure message or app chrome; body:\n%s", body)
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
	s.engine = nil
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
	s.engine = nil
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
	if !strings.Contains(cur, "needs tesseract") {
		t.Errorf("current type should still be annotated: %q", cur)
	}
	for _, other := range []string{"ocr_changed", "numeric"} {
		if line := optionLine(other); !strings.Contains(line, "disabled") {
			t.Errorf("option %q should stay disabled without an OCR engine: %q", other, line)
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
	if tr := got.Watches[0].Trigger; tr.Pattern != "(?i)done" || tr.Op != "" {
		t.Errorf("ocr_match should keep pattern and drop op: %+v", tr)
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
	if !strings.Contains(live, "stale since 00:53:26") {
		t.Errorf("stale badge should date from the last frame (00:53:26); body:\n%s", live)
	}
	if strings.Contains(live, "led-green") || !strings.Contains(live, "led-error") {
		t.Errorf("readout LED should follow the error verdict; body:\n%s", live)
	}
	// With no frame at all, fall back to the detection time.
	s.reg.Drop("printer")
	s.reg.SetHealth("printer", state.Health{Down: true, Message: "no reading: refused", Since: frameTS.Add(4 * time.Second)})
	_, live = get(t, s.Handler(), "/watch/printer/live")
	if !strings.Contains(live, "stale since 00:53:30") {
		t.Errorf("with no readings the badge should fall back to Since; body:\n%s", live)
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
	sup := supervisor.New(nil, reg, ocrFailEngine{}, func(string, ...any) {})
	sup.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, ocrFailEngine{}, t.Logf)
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
