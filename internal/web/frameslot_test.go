package web

import (
	"context"
	"image"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/source"
)

// gateSource is a camera that answers only when the test lets it: every
// Grab counts itself in grabbing and waits for release (or the request
// to go away).
type gateSource struct {
	img      image.Image
	grabbing atomic.Int32
	release  chan struct{}
}

func (g *gateSource) Grab(ctx context.Context) (image.Image, error) {
	g.grabbing.Add(1)
	defer g.grabbing.Add(-1)
	select {
	case <-g.release:
		return g.img, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Only frameSlots Tests (or snapshots) hold a frame at once: the one
// after that is refused with a 503 saying so instead of decoding another
// frame, and once a slot frees up Tests are answered again.
func TestConcurrentTestsAreCapped(t *testing.T) {
	old := frameSlotWait
	frameSlotWait = 50 * time.Millisecond
	t.Cleanup(func() { frameSlotWait = old })

	s, _ := newTestServer(t)
	gate := &gateSource{img: testImage(), release: make(chan struct{})}
	s.NewSource = func(w config.Watch) (source.Source, error) { return gate, nil }
	h := s.Handler()
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}}

	var wg sync.WaitGroup
	codes := make([]int, frameSlots)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, _ := postForm(t, h, "/watch/printer/test", form)
			codes[i] = resp.StatusCode
		}(i)
	}
	waitFor(t, "every slot to be taken", func() bool { return int(gate.grabbing.Load()) == frameSlots })

	resp, body := postForm(t, h, "/watch/printer/test", form)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.HasPrefix(body, busyTestText) {
		t.Errorf("Test %d past the cap: status %d, body %q; want 503 %q", frameSlots+1, resp.StatusCode, body, busyTestText)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("the refusal is %q, want text/plain: app.js renders it as text", ct)
	}
	resp, body = get(t, h, "/watch/printer/snapshot")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.HasPrefix(body, busySnapshotText) {
		t.Errorf("snapshot past the cap: status %d, body %q; want 503 %q", resp.StatusCode, body, busySnapshotText)
	}
	if n := gate.grabbing.Load(); int(n) != frameSlots {
		t.Errorf("%d grabs in flight after the refusals, want %d: a refused request must not reach the camera", n, frameSlots)
	}

	close(gate.release)
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("Test %d inside the cap: status %d, want 200", i+1, c)
		}
	}
	// The slots came back.
	resp, body = postForm(t, h, "/watch/printer/test", form)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Test after the burst: status %d, body %q; want 200", resp.StatusCode, body)
	}
	resp, _ = get(t, h, "/watch/printer/snapshot")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("snapshot after the burst: status %d, want 200", resp.StatusCode)
	}
}

// The pixel_change Test keeps the first frame's crop, not the frame,
// across its wait: the size note still names both frames' sizes.
func TestPixelTestKeepsTheCropNotTheFrame(t *testing.T) {
	src := &seqSource{frames: []image.Image{flat(40, 20, 100), flat(80, 40, 100)}}
	_, _, _, body := postPixelTest(t, pixelServer(t, src))
	if !strings.Contains(body, "different sizes (40×20, then 80×40)") {
		t.Errorf("the size note should name the first frame's size from its bounds, got:\n%s", body)
	}
}
