// Package supervisor owns the lifecycle of watch runner goroutines so the
// web UI can start, stop, and restart watches after config edits.
package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"sort"
	"sync"
	"time"

	"watchglass/internal/config"
	"watchglass/internal/history"
	"watchglass/internal/notify"
	"watchglass/internal/ocr"
	"watchglass/internal/runner"
	"watchglass/internal/source"
	"watchglass/internal/state"
	"watchglass/internal/trigger"
)

type handle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type Supervisor struct {
	// NewSource builds a frame source for a watch. Tests inject fakes;
	// Plan 3 swaps in ffmpeg-tier sources here.
	NewSource func(w config.Watch) source.Source

	mu      sync.Mutex
	running map[string]*handle
	wg      sync.WaitGroup
	store   *history.Store
	reg     *state.Registry
	engine  ocr.Engine
	logf    func(string, ...any)
}

func New(store *history.Store, reg *state.Registry, engine ocr.Engine, logf func(string, ...any)) *Supervisor {
	return &Supervisor{
		NewSource: func(w config.Watch) source.Source { return source.NewHTTPSnapshot(w.Source) },
		running:   map[string]*handle{},
		store:     store,
		reg:       reg,
		engine:    engine,
		logf:      logf,
	}
}

// Start launches one watch's poll loop. It errors if the watch is already
// running or its configuration cannot build a runner.
func (s *Supervisor) Start(ctx context.Context, w config.Watch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[w.Name]; ok {
		return fmt.Errorf("watch %q already running", w.Name)
	}
	var notifier notify.Notifier
	if len(w.Notify) > 0 {
		n, err := notify.NewShoutrrr(w.Notify)
		if err != nil {
			return fmt.Errorf("watch %q: notify: %w", w.Name, err)
		}
		notifier = n
	}
	r, err := runner.New(w, s.NewSource(w), s.engine, notifier, s.store, s.logf)
	if err != nil {
		return err
	}
	name := w.Name
	r.OnReading = func(ev trigger.Event, crop image.Image) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, crop); err != nil {
			s.logf("watch %s: encode sample: %v", name, err)
			return
		}
		s.reg.Add(name, state.Sample{TS: time.Now(), Reading: ev.Reading, Fired: ev.Fired, PNG: buf.Bytes()})
	}
	wctx, cancel := context.WithCancel(ctx)
	h := &handle{cancel: cancel, done: make(chan struct{})}
	s.running[name] = h
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(h.done)
		r.Run(wctx)
	}()
	return nil
}

// Stop cancels a watch and blocks until its goroutine exits. Stopping an
// unknown watch is a no-op.
func (s *Supervisor) Stop(name string) {
	s.mu.Lock()
	h, ok := s.running[name]
	if ok {
		delete(s.running, name)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	h.cancel()
	<-h.done
}

func (s *Supervisor) Restart(ctx context.Context, w config.Watch) error {
	s.Stop(w.Name)
	return s.Start(ctx, w)
}

// Running returns the sorted names of currently-running watches.
func (s *Supervisor) Running() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.running))
	for n := range s.running {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// StopAll cancels every watch and waits for all goroutines to exit.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	hs := s.running
	s.running = map[string]*handle{}
	s.mu.Unlock()
	for _, h := range hs {
		h.cancel()
	}
	s.wg.Wait()
}
