package source

import (
	"testing"

	"watchglass/internal/config"
)

func TestForDispatchesByScheme(t *testing.T) {
	httpSrc, err := For(config.Watch{Name: "a", Source: "http://cam/snap.jpg"})
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	if _, ok := httpSrc.(*HTTPSnapshot); !ok {
		t.Errorf("http source = %T, want *HTTPSnapshot", httpSrc)
	}
	ffSrc, err := For(config.Watch{Name: "b", Source: "rtsp://cam/stream"})
	if err != nil {
		t.Fatalf("rtsp: %v", err)
	}
	if _, ok := ffSrc.(*FFmpeg); !ok {
		t.Errorf("rtsp source = %T, want *FFmpeg", ffSrc)
	}
}

func TestForRejectsUnknownScheme(t *testing.T) {
	if _, err := For(config.Watch{Name: "c", Source: "ftp://cam/x"}); err == nil {
		t.Error("expected error for unsupported scheme")
	}
}
