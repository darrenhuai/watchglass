package web

import (
	"context"
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
	sup.NewSource = func(w config.Watch) source.Source { return &fakeSource{img: testImage()} }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, fakeDetailed{}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	s.NewSource = func(w config.Watch) source.Source { return &fakeSource{img: testImage()} }
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
