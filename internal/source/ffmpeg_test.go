package source

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFFmpegRTSPArgsAndDecode(t *testing.T) {
	f, err := NewFFmpeg("rtsp://cam.local/stream1")
	if err != nil {
		t.Fatalf("NewFFmpeg: %v", err)
	}
	var gotBin string
	var gotArgs []string
	f.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		gotBin, gotArgs = bin, args
		return pngOf(t, 32, 24), nil
	}
	img, err := f.Grab(context.Background())
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 24 {
		t.Errorf("bounds = %v", img.Bounds())
	}
	if gotBin != "ffmpeg" {
		t.Errorf("bin = %q", gotBin)
	}
	want := []string{
		"-nostdin", "-loglevel", "error",
		"-rtsp_transport", "tcp",
		"-i", "rtsp://cam.local/stream1",
		"-frames:v", "1", "-f", "image2", "-c:v", "png", "-",
	}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args =\n %v\nwant\n %v", gotArgs, want)
	}
}

func TestFFmpegDeviceArgs(t *testing.T) {
	cases := []struct {
		input    string
		wantFmt  string
		wantName string
	}{
		{"v4l2:/dev/video0", "v4l2", "/dev/video0"},
		{"dshow:video=Integrated Cam", "dshow", "video=Integrated Cam"},
	}
	for _, c := range cases {
		f, err := NewFFmpeg(c.input)
		if err != nil {
			t.Fatalf("NewFFmpeg(%q): %v", c.input, err)
		}
		var gotArgs []string
		f.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
			gotArgs = args
			return pngOf(t, 8, 8), nil
		}
		if _, err := f.Grab(context.Background()); err != nil {
			t.Fatalf("Grab(%q): %v", c.input, err)
		}
		want := []string{
			"-nostdin", "-loglevel", "error",
			"-f", c.wantFmt, "-i", c.wantName,
			"-frames:v", "1", "-f", "image2", "-c:v", "png", "-",
		}
		if !reflect.DeepEqual(gotArgs, want) {
			t.Errorf("%q args =\n %v\nwant\n %v", c.input, gotArgs, want)
		}
	}
}

func TestFFmpegRawEscapeHatch(t *testing.T) {
	f, err := NewFFmpeg("ffmpeg:-f lavfi -i testsrc=size=16x16")
	if err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	f.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		gotArgs = args
		return pngOf(t, 16, 16), nil
	}
	if _, err := f.Grab(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=16x16",
		"-frames:v", "1", "-f", "image2", "-c:v", "png", "-",
	}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args =\n %v\nwant\n %v", gotArgs, want)
	}
}

func TestNewFFmpegRejectsUnknownInput(t *testing.T) {
	if _, err := NewFFmpeg("http://cam/snap.jpg"); err == nil {
		t.Error("http input should be rejected by NewFFmpeg")
	}
	if _, err := NewFFmpeg(""); err == nil {
		t.Error("empty input should be rejected")
	}
}

func TestFFmpegGrabWrapsRunError(t *testing.T) {
	f, _ := NewFFmpeg("rtsp://cam/x")
	f.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return nil, errors.New("exit status 1: Connection refused")
	}
	_, err := f.Grab(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Connection refused") {
		t.Errorf("error should surface ffmpeg stderr, got %q", err)
	}
}

func TestFFmpegGrabRejectsGarbageOutput(t *testing.T) {
	f, _ := NewFFmpeg("rtsp://cam/x")
	f.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return []byte("not an image"), nil
	}
	if _, err := f.Grab(context.Background()); err == nil {
		t.Fatal("expected decode error")
	}
}

// TestFFmpegGrabRejectsDimensionBomb mirrors TestGrabRejectsDimensionBomb:
// ffmpeg's stdout is just as attacker/misbehaving-source reachable as an
// HTTP snapshot body (a spoofed/broken camera feeding a crafted frame), so
// it must go through the same dimension guard before image.Decode.
func TestFFmpegGrabRejectsDimensionBomb(t *testing.T) {
	f, _ := NewFFmpeg("rtsp://cam/x")
	bomb := pngBomb(t, 40000, 40000)
	f.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return bomb, nil
	}
	_, err := f.Grab(context.Background())
	if err == nil {
		t.Fatal("expected error decoding dimension bomb")
	}
	if !strings.Contains(err.Error(), "40000") {
		t.Errorf("error should name the offending dimensions, got %q", err)
	}
}

func TestFFmpegGrabRejectsOversizedOutput(t *testing.T) {
	f, _ := NewFFmpeg("rtsp://cam/x")
	junk := bytes.Repeat([]byte{0xAB}, maxImageBytes+1024)
	f.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return junk, nil
	}
	_, err := f.Grab(context.Background())
	if err == nil {
		t.Fatal("expected error on oversized ffmpeg output")
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Errorf("error should mention the byte cap, got %q", err)
	}
}

// Integration: only runs where a real ffmpeg is installed. lavfi's testsrc
// needs no camera, so this exercises the real subprocess path end to end.
// A webcam can be opened by one ffmpeg at a time, so grabs of the same
// device input serialize even across separately built sources (the poll
// loop, the snapshot route and Test each build their own).
func TestFFmpegSerializesDeviceGrabs(t *testing.T) {
	const input = "dshow:video=Test Cam " + "serialize"
	var running, maxRunning atomic.Int32
	run := func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		n := running.Add(1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return pngOf(t, 4, 4), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		f, err := NewFFmpeg(input)
		if err != nil {
			t.Fatalf("NewFFmpeg: %v", err)
		}
		f.run = run
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Grab(context.Background()); err != nil {
				t.Errorf("Grab: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := maxRunning.Load(); got != 1 {
		t.Errorf("max concurrent ffmpeg runs on one device = %d, want 1", got)
	}
}

func TestFFmpegNetworkStreamsAreNotSerialized(t *testing.T) {
	f, err := NewFFmpeg("rtsp://cam.local/stream1")
	if err != nil {
		t.Fatalf("NewFFmpeg: %v", err)
	}
	if f.slot != nil {
		t.Error("rtsp source got a device slot; streams must not queue behind each other")
	}
}

func TestFFmpegDeviceWaitHonoursContext(t *testing.T) {
	const input = "v4l2:/dev/video" + "ctx"
	holder, err := NewFFmpeg(input)
	if err != nil {
		t.Fatalf("NewFFmpeg: %v", err)
	}
	release := make(chan struct{})
	holder.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		<-release
		return pngOf(t, 4, 4), nil
	}
	started := make(chan struct{})
	go func() {
		close(started)
		holder.Grab(context.Background()) //nolint:errcheck // the holder only occupies the device
	}()
	<-started
	time.Sleep(20 * time.Millisecond)

	waiter, _ := NewFFmpeg(input)
	waiter.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		t.Error("waiter ran ffmpeg while the device was held")
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = waiter.Grab(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiting grab error = %v, want context deadline", err)
	}
	close(release)
}

func TestFFmpegRealBinary(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	f, err := NewFFmpeg("ffmpeg:-f lavfi -i testsrc=size=64x48:duration=1")
	if err != nil {
		t.Fatal(err)
	}
	f.Timeout = 20 * time.Second
	img, err := f.Grab(context.Background())
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 48 {
		t.Errorf("bounds = %v, want 64x48", img.Bounds())
	}
}
