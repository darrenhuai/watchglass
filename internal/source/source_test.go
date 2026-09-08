package source

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// pngBomb builds the bytes of a minimal, well-formed PNG whose IHDR chunk
// declares width x height without any of that many pixels actually being
// encoded — the "decode bomb" shape from the verified repro: a 59-byte PNG
// declaring 40000x40000 forces image.Decode to allocate ~6.1GB up front
// (width*height*4 bytes for RGBA) before it reads a single real pixel,
// which is a fatal, unrecoverable OOM on Pi-class targets.
//
// The IDAT chunk (an empty zlib stream) is required: image.Decode enforces
// PNG chunk ordering and rejects IHDR immediately followed by IEND before
// it ever gets to allocating pixels, which would hide the real bug behind
// an unrelated "chunk out of order" error. image.DecodeConfig — the guard's
// header-only read — never even looks past IHDR either way.
func pngBomb(t *testing.T, width, height uint32) []byte {
	t.Helper()
	chunk := func(typ string, data []byte) []byte {
		var buf bytes.Buffer
		lenBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(lenBuf, uint32(len(data)))
		buf.Write(lenBuf)
		buf.WriteString(typ)
		buf.Write(data)
		crcBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(crcBuf, crc32.ChecksumIEEE(append([]byte(typ), data...)))
		buf.Write(crcBuf)
		return buf.Bytes()
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // color type: truecolor + alpha (4 bytes/pixel)
	// compression, filter, interlace methods all 0.

	// A well-formed but empty zlib stream: enough to pass chunk-order
	// validation without encoding any real pixel data.
	idat := []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01}

	var buf bytes.Buffer
	buf.Write([]byte{137, 80, 78, 71, 13, 10, 26, 10}) // PNG signature
	buf.Write(chunk("IHDR", ihdr))
	buf.Write(chunk("IDAT", idat))
	buf.Write(chunk("IEND", nil))
	return buf.Bytes()
}

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

// TestGrabRejectsDimensionBomb is the verified repro: a tiny PNG declaring
// 40000x40000 must be rejected before image.Decode ever attempts the ~6.1GB
// pixel-buffer allocation that would OOM a Pi-class target.
func TestGrabRejectsDimensionBomb(t *testing.T) {
	bomb := pngBomb(t, 40000, 40000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bomb)
	}))
	defer ts.Close()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := NewHTTPSnapshot(ts.URL).Grab(context.Background())
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatal("expected error decoding dimension bomb")
	}
	if !strings.Contains(err.Error(), "40000") {
		t.Errorf("error should name the offending dimensions, got %q", err)
	}
	// A real 40000x40000 RGBA decode needs ~6.1GB; if the guard were
	// bypassed, TotalAlloc would jump by gigabytes. A generous 64MiB
	// budget catches a regression without being flaky under GC noise.
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
		t.Errorf("decode attempt allocated %d bytes, want < 64MiB (dimension guard bypassed?)", grew)
	}
}

func TestGrabRejectsOversizedBody(t *testing.T) {
	junk := bytes.Repeat([]byte{0xAB}, maxImageBytes+1024)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(junk)
	}))
	defer ts.Close()

	_, err := NewHTTPSnapshot(ts.URL).Grab(context.Background())
	if err == nil {
		t.Fatal("expected error on oversized body")
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Errorf("error should mention the byte cap, got %q", err)
	}
}
