package supervisor

import (
	"fmt"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/runner"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// gateEngine hands r the engine its gate is read with, when the watch's
// gate names one other than the watch's own (config.Gate.Engine). A gate
// engine this box can't run stops the watch from starting, as the watch's
// own engine does.
func (s *Supervisor) gateEngine(w config.Watch, r *runner.Runner) error {
	if w.Gate == nil || sameEngine(w.GateEngine(), w.Engine) {
		return nil
	}
	e, err := s.engines.For(w.GateEngine())
	if err != nil {
		return fmt.Errorf("watch %q gate: %w", w.Name, err)
	}
	r.SetGateEngine(e)
	return nil
}

// sameEngine reports whether two engine names read the same way; "" is
// tesseract.
func sameEngine(a, b string) bool {
	norm := func(s string) string {
		if s == "" {
			return "tesseract"
		}
		return s
	}
	return norm(a) == norm(b)
}

// Gate is the running watch's gate status (runner.GateStatus), or false
// when it isn't running.
func (s *Supervisor) Gate(name string) (runner.GateStatus, bool) {
	s.mu.Lock()
	h, ok := s.running[name]
	s.mu.Unlock()
	if !ok {
		return runner.GateStatus{}, false
	}
	return h.r.Gate(), true
}

// gateClosedHook passes a gated watch's closed polls to onEvent as the
// zero Event with no crop. Nothing in it changed, so the Home Assistant
// publisher publishes nothing for it but the health seed it sends with a
// watch's first reading: a gate that has never opened (a box off the
// code, a pattern that doesn't match) still shows the watch online in HA,
// as a blank display does. The registry (Live, the dashboard) doesn't
// hear it: those read the gate's status (Gate).
func gateClosedHook(name string, r *runner.Runner, onEvent func(string, trigger.Event, []byte)) {
	if onEvent == nil {
		return
	}
	r.SetOnGateClosed(func(time.Time) { onEvent(name, trigger.Event{}, nil) })
}
