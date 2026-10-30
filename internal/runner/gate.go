package runner

import (
	"context"
	"errors"
	"fmt"
	"image"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/imgproc"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// A watch with a gate (config.Gate) reads a second box from every frame it
// grabs, before the value: the code a cycling display shows next to the
// value it is showing (1.8.0, 2.8.0). The value is read only when the gate
// matches its pattern. A poll whose gate doesn't match is a skip: the
// trigger never sees it, so nothing is published, settled or recorded in
// the history, and it counts toward neither side of Confirm or the leave
// rule. The camera answered and the gate was read, so it isn't a failed
// poll either: a display that never shows the code keeps the watch
// healthy, as a blank display does, and the watch page says how long the
// gate has been closed instead (GateStatus, GateLongAfter).

// GateLongAfter is how long a gate must have stayed closed, besides
// health_after polls in a row, before the watch page and Live say so in
// so many words ("gate closed for 6 min"). A cycling display shows each
// phase every few seconds to a minute, so a gate shut for five minutes is
// a display that stopped cycling, a code that changed, or a gate box that
// slipped off the code.
const GateLongAfter = 5 * time.Minute

// GateReading is what the gate box said on one frame.
type GateReading struct {
	// Text is the gate's reading, Unsure the seven-segment decoder's flag
	// for a '?' it saw and wasn't sure of (ocr.HasUnsureDigit).
	Text   string
	Unsure bool
	// Open: the reading matches the gate's pattern, so the value is read.
	Open bool
	// Crop is the gate's box after its preprocessing, as the engine read it.
	Crop *image.RGBA
}

// GateOpens reports whether a gate reading opens the gate: the pattern
// matches it, and it isn't a reading with a digit the seven-segment
// decoder wasn't sure of (a half-lit 1.8.0 might be 1.6.0).
func GateOpens(re *regexp.Regexp, text string, unsure bool) bool {
	return !unsure && re.MatchString(text)
}

// ReadGate reads w's gate box from frame with eng, the engine
// w.GateEngine() names, and says whether it opens the gate. The runner,
// the web UI's Test and the docs' sample checks all read a gate through it.
func ReadGate(ctx context.Context, frame image.Image, w config.Watch, eng ocr.Engine, re *regexp.Regexp) (GateReading, error) {
	if w.Gate == nil {
		return GateReading{Open: true}, nil
	}
	pp := w.GatePreprocess()
	crop := imgproc.Rotate(imgproc.Crop(frame, w.Gate.Region), pp.Rotate)
	pp.Rotate = 0 // already turned
	prepped := imgproc.Apply(crop, pp)
	g := GateReading{Crop: prepped}
	var err error
	if d, ok := eng.(ocr.DetailedEngine); ok && w.GateEngine() == "sevenseg" {
		var words []ocr.Word
		g.Text, words, err = d.RecognizeWords(ctx, prepped)
		g.Unsure = ocr.HasUnsureDigit(g.Text, words)
	} else {
		g.Text, err = eng.Recognize(ctx, prepped)
	}
	if err != nil {
		return GateReading{}, fmt.Errorf("gate: %w", err)
	}
	g.Open = GateOpens(re, g.Text, g.Unsure)
	return g, nil
}

// GateStatus is what a gated watch's gate has been doing, for the watch
// page, Live and the dashboard. Gated is false for a watch without one.
type GateStatus struct {
	Gated bool
	// HasRead: the gate has been read at least once in this run.
	HasRead bool
	// Open is the last poll's gate.
	Open bool
	// LastOpen is the last poll the gate was open on, zero when none.
	LastOpen time.Time
	// While it is closed: Saw is the last thing the gate read in this
	// closed run, skipping blank frames (a display that blanks between
	// phases would otherwise mostly have "seen nothing"), empty when every
	// frame of the run was blank; ClosedSince is the first poll of this
	// closed run and ClosedPolls how many polls in a row it has been closed.
	Saw         string
	ClosedSince time.Time
	ClosedPolls int
	// Long: closed for health_after polls in a row and GateLongAfter.
	Long bool
}

// gate is a Runner's gate: its pattern, the engine that reads it when it
// isn't the watch's, and its status, which the web UI reads under mu.
type gate struct {
	re     *regexp.Regexp
	engine ocr.Engine
	mu     sync.Mutex
	status GateStatus
	// skipped: the last poll's gate was closed (Run's backoff, see
	// pollChanged). Only the poll goroutine touches it.
	skipped bool
	// onClosed hears every closed poll (SetOnGateClosed).
	onClosed func(at time.Time)
}

// setupGate compiles the watch's gate pattern (New calls it).
func (r *Runner) setupGate() error {
	if r.watch.Gate == nil {
		return nil
	}
	re, err := regexp.Compile(r.watch.Gate.Pattern)
	if err != nil {
		return fmt.Errorf("watch %q: gate pattern: %w", r.watch.Name, err)
	}
	r.gate.re = re
	r.gate.status.Gated = true
	return nil
}

// SetGateEngine gives the runner the engine its gate is read with when
// that isn't the watch's own (config.Watch.GateEngine). Call it before Run.
func (r *Runner) SetGateEngine(e ocr.Engine) { r.gate.engine = e }

// SetOnGateClosed sets a function called on the poll goroutine after every
// poll whose gate was closed, with the poll's time. Such a poll never
// reaches OnReading, so this is how a consumer that wants to know the
// watch is polling hears it: the Home Assistant publisher seeds the health
// topic "online" from it, as it does from a watch's first reading, so a
// gate that has never opened doesn't leave HA's connectivity sensor
// unknown. Keep it fast. Call it before Run.
func (r *Runner) SetOnGateClosed(fn func(at time.Time)) { r.gate.onClosed = fn }

// Gate is the watch's GateStatus now. Safe to call from any goroutine.
func (r *Runner) Gate() GateStatus {
	r.gate.mu.Lock()
	defer r.gate.mu.Unlock()
	return r.gate.status
}

// gateClosed reads the gate from img and reports whether it is closed, so
// the poll is skipped. It runs inside the poll's crash containment; a
// failed read is the poll's error, a failed read like any other.
func (r *Runner) gateClosed(ctx context.Context, img image.Image) (bool, error) {
	if r.watch.Gate == nil {
		return false, nil
	}
	r.gate.skipped = false
	eng := r.gate.engine
	if eng == nil {
		eng = r.engine
	}
	g, err := ReadGate(ctx, img, r.watch, eng, r.gate.re)
	if err != nil {
		return false, err
	}
	r.noteGate(g, r.clock.Now())
	r.gate.skipped = !g.Open
	return !g.Open, nil
}

// gateSkip finishes a poll whose gate was closed (tick calls it after the
// poll counted as a success). The trigger never sees the frame, but the
// time does pass for it: Idle forgets a settled state once nothing has
// been read for StaleAfter, as a run of blank frames does, so a condition
// that still holds when the gate opens after a long closed spell is news
// again. A forgotten state is saved. Then onClosed hears the poll.
func (r *Runner) gateSkip(at time.Time) {
	if r.eval.Idle() {
		r.saveState()
	}
	if r.gate.onClosed == nil {
		return
	}
	if err := Contain(func() error { r.gate.onClosed(at); return nil }); err != nil {
		var ce *CrashError
		if errors.As(err, &ce) {
			r.crashed(ce, StepAfter)
		}
	}
}

// pollChanged is Run's verdict on the poll just done for the backoff
// (max_interval, NextInterval): tickChanged, and on a gated watch, any
// poll whose gate was closed. The display moved on to another phase, which
// is the display changing; backing off across it would let the polls
// alias with the display's cycle and miss the wanted phase for minutes.
// So a gated watch backs off only while its gate stays open on the same
// value, and one whose gate never opens keeps to its base interval.
func (r *Runner) pollChanged(ev trigger.Event, err error, lastReading string) bool {
	return tickChanged(r.watch.Trigger.Type, ev, err, lastReading) || r.gate.skipped
}

// noteGate records one gate reading in the status, and logs a gate that
// has been closed long enough to say so, once, and its opening after that.
func (r *Runner) noteGate(g GateReading, at time.Time) {
	r.gate.mu.Lock()
	st := &r.gate.status
	wasLong := st.Long
	if g.Open {
		st.Open, st.LastOpen = true, at
		st.Saw, st.ClosedSince, st.ClosedPolls, st.Long = "", time.Time{}, 0, false
	} else {
		if st.ClosedPolls == 0 {
			st.ClosedSince = at
		}
		st.Open = false
		if strings.TrimSpace(g.Text) != "" {
			st.Saw = g.Text
		}
		st.ClosedPolls++
		n := r.watch.HealthAfter
		if n < 1 {
			n = 1
		}
		st.Long = st.ClosedPolls >= n && at.Sub(st.ClosedSince) >= GateLongAfter
	}
	st.HasRead = true
	long, saw, since := st.Long, st.Saw, st.ClosedSince
	r.gate.mu.Unlock()
	switch {
	case long && !wasLong:
		r.logf("watch %s: the gate hasn't opened for %s (it last read %q, and the gate pattern is %s), so the value isn't read. Check the gate box on the watch page", r.watch.Name, at.Sub(since).Round(time.Second), saw, r.watch.Gate.Pattern)
	case wasLong && g.Open:
		r.logf("watch %s: the gate is open again; the value is read", r.watch.Name)
	}
}
