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

	"watchglass/internal/config"
	"watchglass/internal/ocr"
	"watchglass/internal/source"
	"watchglass/internal/state"
	"watchglass/internal/supervisor"
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
	for _, want := range []string{"id=\"stage\"", "id=\"overlay\"", "name=\"ttype\"", "name=\"pp_threshold\"", "/static/app.js"} {
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
