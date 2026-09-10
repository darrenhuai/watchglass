package source

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os/exec"
	"strings"
	"time"
)

// defaultFFmpegTimeout bounds one frame grab. A hung ffmpeg against a dead
// camera is the most likely reliability failure in this design, so every
// spawn is both context-bound and force-killed after WaitDelay.
const defaultFFmpegTimeout = 20 * time.Second

// ffmpegRunFunc runs the binary and returns its stdout. Injectable so unit
// tests never spawn a real process.
type ffmpegRunFunc func(ctx context.Context, bin string, args ...string) ([]byte, error)

// FFmpeg grabs a single frame by spawning ffmpeg once per poll and letting
// it exit. There is no persistent decoder: between polls this source costs
// nothing, which is the project's headline resource claim.
//
// ffmpeg is invoked as a SUBPROCESS and is never linked or redistributed,
// which keeps watchglass clear of ffmpeg's (L)GPL linking obligations.
type FFmpeg struct {
	Bin     string
	Timeout time.Duration

	inputArgs []string // format/transport flags plus -i <input>
	run       ffmpegRunFunc
}

// NewFFmpeg builds a frame source for an rtsp://, rtsps://, v4l2:, dshow:,
// or ffmpeg: source string. Other schemes are rejected — use the HTTP
// snapshot source for plain image URLs.
func NewFFmpeg(input string) (*FFmpeg, error) {
	args, err := ffmpegInputArgs(input)
	if err != nil {
		return nil, err
	}
	return &FFmpeg{
		Bin:       "ffmpeg",
		Timeout:   defaultFFmpegTimeout,
		inputArgs: args,
		run:       runFFmpeg,
	}, nil
}

func ffmpegInputArgs(input string) ([]string, error) {
	switch {
	case strings.HasPrefix(input, "rtsp://"), strings.HasPrefix(input, "rtsps://"):
		// TCP transport: UDP silently drops frames on congested networks.
		return []string{"-rtsp_transport", "tcp", "-i", input}, nil
	case strings.HasPrefix(input, "v4l2:"):
		return []string{"-f", "v4l2", "-i", strings.TrimPrefix(input, "v4l2:")}, nil
	case strings.HasPrefix(input, "dshow:"):
		return []string{"-f", "dshow", "-i", strings.TrimPrefix(input, "dshow:")}, nil
	case strings.HasPrefix(input, "ffmpeg:"):
		// Duplicated in config.SourceKind (config cannot import source
		// without an import cycle) — keep both checks in sync.
		raw := strings.TrimSpace(strings.TrimPrefix(input, "ffmpeg:"))
		if raw == "" {
			return nil, fmt.Errorf("ffmpeg: source has no arguments")
		}
		return strings.Fields(raw), nil
	}
	return nil, fmt.Errorf("source %q is not an ffmpeg input "+
		"(expected rtsp:// rtsps:// v4l2: dshow: or ffmpeg:)", input)
}

func (f *FFmpeg) Grab(ctx context.Context) (image.Image, error) {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = defaultFFmpegTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := make([]string, 0, len(f.inputArgs)+9)
	args = append(args, "-nostdin", "-loglevel", "error")
	args = append(args, f.inputArgs...)
	args = append(args, "-frames:v", "1", "-f", "image2", "-c:v", "png", "-")

	out, err := f.run(ctx, f.Bin, args...)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	img, err := decodeImage(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("ffmpeg: decode frame: %w", err)
	}
	return img, nil
}

func runFFmpeg(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	// WaitDelay force-kills an ffmpeg that ignores context cancellation
	// instead of leaking the process and blocking the poll goroutine.
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}
