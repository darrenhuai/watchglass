package config

import (
	"fmt"
	"regexp"
)

// Gate is an optional second box on a watch, for a display that cycles
// through several values that look alike (a meter showing 1.8.0, then
// 2.8.0, each with a kWh total): the box around the code that says which
// value is on show. Every poll reads the gate from the same frame as the
// value. When the gate's reading matches Pattern, the value is read and
// used as usual. When it doesn't (another phase, a blank frame between
// phases, a flash), the poll is a skip: nothing is published, settled or
// recorded, and it counts toward neither Confirm nor the stream's health.
type Gate struct {
	Region Region `yaml:"region"`
	// Pattern is a regular expression the gate's reading must match for
	// the poll to count. Anchor it (^2$) when one code is part of another
	// (2 and 12).
	Pattern string `yaml:"pattern"`
	// Engine reads the gate; "" reads it with the watch's engine.
	Engine string `yaml:"engine,omitempty"`
	// Preprocess prepares the gate's crop; nil uses the watch's.
	Preprocess *Preprocess `yaml:"preprocess,omitempty"`
}

// GateEngine is the engine the watch's gate is read with: the gate's own,
// or the watch's. "" is tesseract, as for Watch.Engine.
func (w Watch) GateEngine() string {
	if w.Gate != nil && w.Gate.Engine != "" {
		return w.Gate.Engine
	}
	return w.Engine
}

// GatePreprocess is how the gate's crop is prepared: the gate's own
// preprocess block, or the watch's.
func (w Watch) GatePreprocess() Preprocess {
	if w.Gate != nil && w.Gate.Preprocess != nil {
		return *w.Gate.Preprocess
	}
	return w.Preprocess
}

// checkPreprocess validates one preprocess block; what names it ("preprocess",
// "gate preprocess") starts the message.
func checkPreprocess(p Preprocess, what string) error {
	if p.Threshold < 0 || p.Threshold > 255 {
		return fmt.Errorf("%s threshold must be 0-255", what)
	}
	if p.Upscale < 0 || p.Upscale > 4 {
		return fmt.Errorf("%s upscale must be 0-4", what)
	}
	if !ValidRotate(p.Rotate) {
		return fmt.Errorf("%s rotate must be 0, 90, 180 or 270 (degrees clockwise), got %d", what, p.Rotate)
	}
	return nil
}

// checkGate validates the watch's gate and puts its clamped region back.
// The gate is copied first: Validate runs on copies of a config whose
// watches share the gate's pointer with the live one (the web UI's
// mutateConfig, Save's read-back check), and must not change it.
func checkGate(w *Watch) error {
	if w.Gate == nil {
		return nil
	}
	g := *w.Gate
	if w.Trigger.Type == "pixel_change" {
		return fmt.Errorf("gate only applies to watches that read text or digits, not pixel_change")
	}
	r := g.Region
	if nonFinite(r.X) || nonFinite(r.Y) || nonFinite(r.W) || nonFinite(r.H) {
		return fmt.Errorf("gate region must be finite")
	}
	r = r.Clamp()
	if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > 1 || r.Y+r.H > 1 {
		return fmt.Errorf("gate region must be normalized 0-1 with positive size")
	}
	g.Region = r
	if g.Pattern == "" {
		return fmt.Errorf("gate needs a pattern: what the code box reads when the value you want is on show")
	}
	if _, err := regexp.Compile(g.Pattern); err != nil {
		return fmt.Errorf("gate pattern: %w", err)
	}
	if !validEngines[g.Engine] {
		return fmt.Errorf("unknown gate engine %q (expected tesseract, sevenseg or rapidocr, or leave it out to use the watch's)", g.Engine)
	}
	if g.Preprocess != nil {
		p := *g.Preprocess
		if err := checkPreprocess(p, "gate preprocess"); err != nil {
			return err
		}
		g.Preprocess = &p
	}
	w.Gate = &g
	return nil
}
