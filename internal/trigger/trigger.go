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

	// A cooldown DELAYS notification of a persisting new state rather than
	// dropping it: when a would-be transition lands inside the cooldown
	// window, e.stable/e.numCond are left untouched (instead of committing
	// the suppressed edge) so that, if the new state still holds once the
	// window ends, the next confirmed reading still sees prev as the old
	// (pre-edge) state and fires the transition then. If the state reverted
	// before expiry, the (non-edge) revert commits normally and nothing
	// fires — it resolved on its own.
	switch e.cfg.Type {
	case "ocr_changed":
		wouldFire := prevHas && prev != s
		if wouldFire && e.coolingDown() {
			return Event{Reading: s}
		}
		e.stable, e.hasStable = s, true
		if wouldFire {
			return e.fire(s, "text changed")
		}
	case "ocr_match":
		wouldFire := e.re.MatchString(s) && (!prevHas || !e.re.MatchString(prev))
		if wouldFire && e.coolingDown() {
			return Event{Reading: s}
		}
		e.stable, e.hasStable = s, true
		if wouldFire {
			return e.fire(s, "pattern matched")
		}
	case "numeric":
		v, ok := e.extract(s)
		if !ok {
			e.stable, e.hasStable = s, true
			return Event{Reading: s}
		}
		cond := (e.cfg.Op == "gt" && v > e.cfg.Threshold) ||
			(e.cfg.Op == "lt" && v < e.cfg.Threshold)
		wouldFire := cond && !e.numCond
		if wouldFire && e.coolingDown() {
			return Event{Reading: s}
		}
		e.stable, e.hasStable = s, true
		e.numCond = cond
		if wouldFire {
			return e.fire(s, fmt.Sprintf("value %v crossed threshold", v))
		}
	default:
		e.stable, e.hasStable = s, true
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

// Condition is what a single reading says about a trigger's condition, for
// a one-off check such as the web UI's "Test this region". It is stateless
// on purpose: whether a watch actually notifies also depends on the
// readings before this one (edges), Confirm and Cooldown, so callers must
// present Met as "the condition holds", never as "this will notify".
type Condition struct {
	// Evaluable is false for the types one reading can't decide:
	// pixel_change compares consecutive frames, and ocr_changed compares a
	// reading with the previous stable one.
	Evaluable bool
	Met       bool
	// Detail says why, in a short sentence without a trailing period.
	Detail string
}

// Check evaluates cfg's condition against one reading. The error is
// trigger.New's: an unknown type, a missing or invalid pattern, a bad op.
func Check(cfg config.Trigger, reading string) (Condition, error) {
	e, err := New(cfg)
	if err != nil {
		return Condition{}, err
	}
	switch cfg.Type {
	case "ocr_match":
		if e.re.MatchString(reading) {
			return Condition{Evaluable: true, Met: true, Detail: "The text matches the pattern"}, nil
		}
		return Condition{Evaluable: true, Detail: "The text doesn't match the pattern"}, nil
	case "numeric":
		v, ok := e.extract(reading)
		if !ok {
			return Condition{Evaluable: true, Detail: "No number found in the reading"}, nil
		}
		word := "above"
		met := v > cfg.Threshold
		if cfg.Op == "lt" {
			word, met = "below", v < cfg.Threshold
		}
		num := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
		if met {
			return Condition{Evaluable: true, Met: true, Detail: fmt.Sprintf("%s is %s %s", num(v), word, num(cfg.Threshold))}, nil
		}
		return Condition{Evaluable: true, Detail: fmt.Sprintf("%s is not %s %s", num(v), word, num(cfg.Threshold))}, nil
	case "ocr_changed":
		return Condition{Detail: "Fires when the text changes from one stable reading to another"}, nil
	}
	return Condition{Detail: "Pixel change compares each frame with the one before, so one test has nothing to compare"}, nil
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
