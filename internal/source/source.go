// Package source provides frame sources. Tier A: plain HTTP snapshot URLs.
package source

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // register decoders: most cameras serve JPEG
	_ "image/png"
	"io"
	"net/http"
	"time"
)

// Source produces one frame per call. Implementations must respect ctx.
type Source interface {
	Grab(ctx context.Context) (image.Image, error)
}

const (
	// maxImageBytes caps the encoded image body read from any source
	// before decoding — a hard limit against a misbehaving/spoofed camera
	// streaming an unbounded response.
	maxImageBytes = 32 << 20 // 32 MiB

	// maxImagePixels caps width*height before the real pixel buffer is
	// allocated. image.Decode allocates its full pixel buffer up front
	// from the header-declared dimensions, so a tiny, well-formed file
	// that just declares an enormous width/height (a "decompression/decode
	// bomb") forces a multi-gigabyte allocation before any real pixel data
	// is read — fatal OOM on Pi-class targets, and recover() cannot catch
	// a runtime OOM. Checking image.DecodeConfig's cheap header-only parse
	// against this cap before calling image.Decode closes that hole.
	maxImagePixels = 16 << 20 // 16 megapixels
)

// decodeImage decodes r as an image, guarding against both a too-large body
// and a dimension bomb. Every Source that decodes attacker-reachable bytes
// (an HTTP snapshot response, ffmpeg's stdout) must go through this instead
// of calling image.Decode directly.
func decodeImage(r io.Reader) (image.Image, error) {
	limited := io.LimitReader(r, maxImageBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read image body: %w", err)
	}
	if len(buf) > maxImageBytes {
		return nil, fmt.Errorf("image body exceeds %d byte cap", maxImageBytes)
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("decode image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("image has non-positive dimensions %dx%d", cfg.Width, cfg.Height)
	}
	// int64 multiplication: cfg.Width/Height come straight from an
	// attacker-controlled header, and a 32-bit int (Pi-class ARM builds)
	// would silently overflow computing width*height directly.
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, fmt.Errorf("image dimensions %dx%d exceed %d pixel cap", cfg.Width, cfg.Height, maxImagePixels)
	}

	img, _, err := image.Decode(bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return img, nil
}

type HTTPSnapshot struct {
	URL    string
	Client *http.Client
}

func NewHTTPSnapshot(url string) *HTTPSnapshot {
	return &HTTPSnapshot{URL: url, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (h *HTTPSnapshot) Grab(ctx context.Context) (image.Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", h.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snapshot %s: status %d", h.URL, resp.StatusCode)
	}
	img, err := decodeImage(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: decode: %w", h.URL, err)
	}
	return img, nil
}
