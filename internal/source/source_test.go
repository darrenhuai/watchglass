package source

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func servePNG(t *testing.T, status int, img image.Image) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if img != nil {
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				t.Error(err)
			}
			w.Write(buf.Bytes())
		}
	}))
}

func TestGrabDecodesImage(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 48))
	src.Set(10, 10, color.RGBA{R: 255, A: 255})
	ts := servePNG(t, http.StatusOK, src)
	defer ts.Close()

	got, err := NewHTTPSnapshot(ts.URL).Grab(context.Background())
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if got.Bounds().Dx() != 64 || got.Bounds().Dy() != 48 {
		t.Errorf("bounds = %v", got.Bounds())
	}
}

func TestGrabRejectsNon200(t *testing.T) {
	ts := servePNG(t, http.StatusNotFound, nil)
	defer ts.Close()
	if _, err := NewHTTPSnapshot(ts.URL).Grab(context.Background()); err == nil {
		t.Fatal("expected error on 404")
	}
}
