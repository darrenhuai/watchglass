// Package demo is the fake camera built into the binary, so watchglass can
// be tried with no camera, no Python and no clone: `watchglass -demo`, or a
// watch whose source is demo:printer or demo:sevenseg.
//
// Each scene is six pre-rendered frames (the same PNGs examples/demo's
// fakecam.py serves). A scene plays a 40 second loop: frames 0-4 for 4
// seconds each, then the last frame held for 20 seconds, which is long
// enough for a watch polling every 2 seconds with confirm 2 to believe it
// and fire. The loop is a pure function of the time since the process
// started, so every watch and every snapshot request sees the same frame
// at the same moment, and a fresh start reaches the last frame 20 seconds
// in rather than wherever a wall-clock loop happens to be.
package demo

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"image"
	"image/png"
	"strings"
	"sync"
	"time"
)

//go:embed frames/printer/*.png frames/sevenseg/*.png
var frameFiles embed.FS

// Prefix starts every demo source: demo:printer, demo:sevenseg.
const Prefix = "demo:"

const (
	// Printer is a 3D printer's status LCD: "PRINTING 12%" up to
	// "PRINTING 94%", then "PRINT COMPLETE" in inverted colours.
	Printer = "printer"
	// SevenSeg is a red four-digit LED readout climbing 23.5, 23.7, 24.1,
	// 24.6, 25.0 and holding 25.3.
	SevenSeg = "sevenseg"
)

// Scenes lists the scene names in the order the UI mentions them.
var Scenes = []string{Printer, SevenSeg}

const (
	frameCount = 6
	step       = 4 * time.Second
	hold       = 20 * time.Second
	// Cycle is one full loop: five 4 s frames and the 20 s hold.
	Cycle = (frameCount-1)*step + hold
)

// Valid reports whether scene names a built-in scene.
func Valid(scene string) bool {
	for _, s := range Scenes {
		if s == scene {
			return true
		}
	}
	return false
}

// FrameIndex is the frame shown elapsed after the loop started.
func FrameIndex(elapsed time.Duration) int {
	if elapsed < 0 {
		elapsed = 0
	}
	t := elapsed % Cycle
	if i := int(t / step); i < frameCount-1 {
		return i
	}
	return frameCount - 1
}

// FramePNG is frame i of scene as the embedded PNG bytes.
func FramePNG(scene string, i int) ([]byte, error) {
	if !Valid(scene) {
		return nil, fmt.Errorf("unknown demo scene %q", scene)
	}
	if i < 0 || i >= frameCount {
		return nil, fmt.Errorf("demo frame %d out of range", i)
	}
	return frameFiles.ReadFile(fmt.Sprintf("frames/%s/frame_%d.png", scene, i))
}

type decoded struct {
	once   sync.Once
	frames []image.Image
	err    error
}

var cache = map[string]*decoded{Printer: {}, SevenSeg: {}}

// Frames is scene's frames, decoded once and shared. Callers must treat
// the images as read-only (imgproc.Crop copies before anything is drawn).
func Frames(scene string) ([]image.Image, error) {
	d, ok := cache[scene]
	if !ok {
		return nil, fmt.Errorf("unknown demo scene %q", scene)
	}
	d.once.Do(func() {
		for i := 0; i < frameCount; i++ {
			raw, err := FramePNG(scene, i)
			if err != nil {
				d.err = err
				return
			}
			img, err := png.Decode(bytes.NewReader(raw))
			if err != nil {
				d.err = fmt.Errorf("demo frame %s/%d: %w", scene, i, err)
				return
			}
			d.frames = append(d.frames, img)
		}
	})
	return d.frames, d.err
}

// epoch is when the loop started: process start, so a fresh `-demo` run
// shows the finished print about 20 seconds in.
var epoch = time.Now()

// Source is a demo camera. It satisfies source.Source.
type Source struct {
	scene string
	// Start is when the loop began (process start); Now is the clock.
	// Tests replace both.
	Start time.Time
	Now   func() time.Time
}

// NewSource returns the camera for a demo:<scene> source's scene name.
func NewSource(scene string) (*Source, error) {
	scene = strings.TrimSpace(scene)
	if !Valid(scene) {
		return nil, fmt.Errorf("demo source %q: expected %s", Prefix+scene, Names())
	}
	return &Source{scene: scene, Start: epoch, Now: time.Now}, nil
}

// Grab returns the frame the loop is showing now.
func (s *Source) Grab(ctx context.Context) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	frames, err := Frames(s.scene)
	if err != nil {
		return nil, err
	}
	return frames[FrameIndex(s.Now().Sub(s.Start))], nil
}

// Names spells the valid sources for an error message:
// "demo:printer or demo:sevenseg".
func Names() string {
	out := make([]string, len(Scenes))
	for i, s := range Scenes {
		out[i] = Prefix + s
	}
	return strings.Join(out, " or ")
}
