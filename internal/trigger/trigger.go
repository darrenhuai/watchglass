// Package trigger is the pure state machine that turns a noisy stream of
// per-frame readings into rare, trustworthy fire events. No I/O lives here.
package trigger

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
)

type Event struct {
	Fired   bool
	Reading string
	Reason  string
}

type Evaluator struct {
	cfg       config.Trigger
	re        *regexp.Regexp
	stable    string
	hasStable bool
	candidate string
	candCount int
	numCond   bool
	lastFired time.Time
	// Now is the clock; tests replace it.
	Now func() time.Time
}

func New(cfg config.Trigger) (*Evaluator, error) {
	e := &Evaluator{cfg: cfg, Now: time.Now}
	if e.cfg.Confirm <= 0 {
		e.cfg.Confirm = 1
	}
	switch cfg.Type {
	case "ocr_match":
		if cfg.Pattern == "" {
			return nil, errors.New("ocr_match requires a pattern")
		}
		re, err := regexp.Compile(cfg.Pattern)
		if err != nil {
			return nil, fmt.Errorf("pattern: %w", err)
		}
		e.re = re
	case "numeric":
		if cfg.Op != "gt" && cfg.Op != "lt" {
			return nil, fmt.Errorf("numeric op must be gt or lt, got %q", cfg.Op)
		}
		pat := cfg.Pattern
		if pat == "" {
			pat = `[-+]?\d+(?:\.\d+)?`
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("pattern: %w", err)
		}
		e.re = re
	case "ocr_changed", "pixel_change":
	default:
		return nil, fmt.Errorf("unknown trigger type %q", cfg.Type)
	}
	return e, nil
}

func (e *Evaluator) coolingDown() bool {
	return e.cfg.Cooldown > 0 && !e.lastFired.IsZero() &&
		e.Now().Sub(e.lastFired) < time.Duration(e.cfg.Cooldown)
}

func (e *Evaluator) fire(reading, reason string) Event {
	e.lastFired = e.Now()
	return Event{Fired: true, Reading: reading, Reason: reason}
}

// ObserveText feeds one OCR reading through confirm + edge + cooldown logic.
func (e *Evaluator) ObserveText(s string) Event {
	if s == e.candidate {
		e.candCount++
	} else {
		e.candidate = s
		e.candCount = 1
	}
	if e.candCount < e.cfg.Confirm {
		return Event{Reading: s}
	}
	if e.hasStable && e.stable == s {
		return Event{Reading: s} // no transition
	}
	prev, prevHas := e.stable, e.hasStable
	e.stable, e.hasStable = s, true

	switch e.cfg.Type {
	case "ocr_changed":
		if prevHas && prev != s && !e.coolingDown() {
			return e.fire(s, "text changed")
		}
	case "ocr_match":
		if e.re.MatchString(s) && (!prevHas || !e.re.MatchString(prev)) && !e.coolingDown() {
			return e.fire(s, "pattern matched")
		}
	case "numeric":
		v, ok := e.extract(s)
		if !ok {
			return Event{Reading: s}
		}
		cond := (e.cfg.Op == "gt" && v > e.cfg.Threshold) ||
			(e.cfg.Op == "lt" && v < e.cfg.Threshold)
		was := e.numCond
		e.numCond = cond
		if cond && !was && !e.coolingDown() {
			return e.fire(s, fmt.Sprintf("value %v crossed threshold", v))
		}
	}
	return Event{Reading: s}
}

// ObservePixel feeds one pixel-diff percentage. Threshold + cooldown only.
func (e *Evaluator) ObservePixel(pct float64) Event {
	reading := fmt.Sprintf("%.1f%% changed", pct)
	if pct < e.cfg.Threshold || e.coolingDown() {
		return Event{Reading: reading}
	}
	return e.fire(reading, "pixel change")
}

func (e *Evaluator) extract(s string) (float64, bool) {
	m := e.re.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	val := m[0]
	if len(m) > 1 && m[1] != "" {
		val = m[1]
	}
	f, err := strconv.ParseFloat(val, 64)
	return f, err == nil
}
