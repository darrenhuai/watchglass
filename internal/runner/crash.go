package runner

import (
	"errors"
	"fmt"
	"runtime/debug"
	"time"
	"unicode/utf8"
)

// Step is where in a poll a panic was caught. Each step has its own
// sentence for the person who sees it, because what they should report
// differs: a frame that crashed the reader is worth attaching, a frame
// that was read fine before a consumer crashed is not.
type Step string

const (
	// StepGrab: in the source, fetching the frame. There is no frame.
	StepGrab Step = "grab"
	// StepRead: the crop, the preprocessing, the engine or the trigger
	// evaluation. The frame is what to report.
	StepRead Step = "read"
	// StepAfter: in a consumer of the reading (OnReading: the Live panel,
	// the Home Assistant publisher). The reading was made and recorded.
	StepAfter Step = "after"
	// StepPoll: anywhere else in a Tick (the history store, the saved
	// state, raising an alert, a health callback), caught by Run.
	StepPoll Step = "poll"
)

// Steps lists every step, for code that looks for a crash sentence in a
// message (the web UI's summaries).
var Steps = []Step{StepGrab, StepRead, StepAfter, StepPoll}

// Sentence is the plain sentence for a crash at this step, the words the
// dashboard and the watch's page show.
func (s Step) Sentence() string {
	switch s {
	case StepGrab:
		return "watchglass crashed while fetching this watch's frame; this is a bug, please report it with the log"
	case StepAfter:
		return "watchglass crashed after reading this frame (the reading is fine); this is a bug, please report it with the log"
	case StepPoll:
		return "watchglass crashed while handling this watch's poll; this is a bug, please report it with the log"
	}
	return ErrCrashed.Error()
}

// ErrCrashed is in the chain of every CrashError (errors.Is), whatever
// the step; its text is the read step's sentence. A panic in the engine,
// the preprocessing or the trigger code is a bug in watchglass, never
// something the frame or the settings did wrong, and the person who sees
// it can only report it.
var ErrCrashed = errors.New("the reader crashed on this frame; this is a bug, please report it with the frame")

// CrashError is a panic caught at a watch's boundary (Contain).
type CrashError struct {
	Value any    // what was passed to panic
	Stack []byte // the panicking goroutine's stack, for the log
	// Step is where the panic was caught, set by whoever caught it
	// (crashed, or the Test route). Unset reads as StepRead.
	Step Step
	// First is when this run of crashes began: the first crash since the
	// watch last completed a poll without one (crashed sets it). A watch
	// that crashes on every poll has been failing since First.
	First time.Time
	// reported: the runner has logged it and told OnCrash (crashed).
	reported bool
}

// panicMax caps how much of the panic value an error message carries.
const panicMax = 200

// Detail is the panic value as text, capped at panicMax runes: the only
// part of the message that is new once the sentence has been read.
func (e *CrashError) Detail() string {
	v := fmt.Sprint(e.Value)
	if utf8.RuneCountInString(v) > panicMax {
		v = string([]rune(v)[:panicMax-1]) + "…"
	}
	return v
}

func (e *CrashError) Error() string {
	return fmt.Sprintf("%s (%s)", e.Step.Sentence(), e.Detail())
}

func (e *CrashError) Unwrap() error { return ErrCrashed }

// Contain runs fn and turns a panic in it into a *CrashError, so one bad
// frame fails one read instead of the whole process: an unrecovered panic
// in any watch's goroutine ends every watch with it, and a saved watch
// that panics on its first frame would do that again on every start.
func Contain(fn func() error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = &CrashError{Value: v, Stack: debug.Stack()}
		}
	}()
	return fn()
}

// crashRun is a watch's current run of crashes: polls that crashed with
// no clean poll between them (endTick). It decides how long the watch
// has been failing (first) and which stacks have been logged.
type crashRun struct {
	first time.Time
	// stacks: the panic values whose stack this run has logged. A stack is
	// logged once per distinct panic, not once per run: a watch that
	// runs for weeks and meets a second bug should log that one too.
	stacks map[string]bool
	// thisTick: the poll under way has crashed (endTick keeps the run).
	thisTick bool
}

// crashStacksMax caps the distinct stacks one run logs, for a panic
// value that changes every time (a counter, an address).
const crashStacksMax = 8

// crashed logs a contained panic and tells OnCrash. The stack goes to
// the log the first time this run of crashes meets that panic value, and
// a one-line message every time after.
func (r *Runner) crashed(ce *CrashError, step Step) {
	ce.Step = step
	ce.reported = true
	if r.crashes.first.IsZero() {
		r.crashes.first = time.Now()
		r.crashes.stacks = map[string]bool{}
	}
	r.crashes.thisTick = true
	ce.First = r.crashes.first
	if key := ce.Detail(); !r.crashes.stacks[key] && len(r.crashes.stacks) < crashStacksMax {
		r.crashes.stacks[key] = true
		r.logf("watch %s: %v\n%s", r.watch.Name, ce, ce.Stack)
	} else {
		r.logf("watch %s: %v", r.watch.Name, ce)
	}
	if r.OnCrash != nil {
		r.OnCrash(ce)
	}
}

// endTick closes the poll: one that crashed nowhere ends the run of
// crashes, so the next crash starts a new run (its own First, its stack
// logged again). Tick calls it on every return, and Run after a crash
// that escaped Tick.
func (r *Runner) endTick() {
	if !r.crashes.thisTick {
		r.crashes = crashRun{}
	}
	r.crashes.thisTick = false
}
