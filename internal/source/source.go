// Package source provides frame sources. Tier A: plain HTTP snapshot URLs.
package source

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // register decoders: most cameras serve JPEG
	_ "image/png"
	"net/http"
	"time"
)

// Source produces one frame per call. Implementations must respect ctx.
type Source interface {
	Grab(ctx context.Context) (image.Image, error)
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
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: decode: %w", h.URL, err)
	}
	return img, nil
}
