package supervisor

import (
	"errors"

	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/runner"
)

// ErrNotRunning is AcceptLower's answer for a watch that isn't running.
var ErrNotRunning = errors.New("the watch isn't running")

// Value is the running watch's published number and what its counter is
// holding back (runner.ValueStatus), or false when it isn't running.
func (s *Supervisor) Value(name string) (runner.ValueStatus, bool) {
	s.mu.Lock()
	h, ok := s.running[name]
	s.mu.Unlock()
	if !ok {
		return runner.ValueStatus{}, false
	}
	return h.r.Value(), true
}

// AcceptLower hands the running watch's counter a lower value to take as
// its new last value (runner.AcceptLower).
func (s *Supervisor) AcceptLower(name string, v float64) error {
	s.mu.Lock()
	h, ok := s.running[name]
	s.mu.Unlock()
	if !ok {
		return ErrNotRunning
	}
	return h.r.AcceptLower(v)
}

// History is the history database the watches record into, nil when
// there is none. The web UI reads readings out of it (history.csv).
func (s *Supervisor) History() *history.Store { return s.store }
