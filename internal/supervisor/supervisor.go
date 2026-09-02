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
	"watchglass/internal/health"
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
	// NewSource builds a frame source for a watch. An unsupported source
	// scheme is a configuration error that must surface at Start rather
	// than silently failing on the first tick. Tests inject fakes.
	NewSource func(w config.Watch) (source.Source, error)

	// OnEvent, when set, receives every tick's outcome for every watch,
	// with the crop already PNG-encoded. Nil-safe; read at Start time.
	OnEvent func(watch string, ev trigger.Event, png []byte)
	// OnHealth, when set, receives every stream health transition.
	OnHealth func(watch string, hev health.Event)

	mu      sync.Mutex
	running map[string]*handle
	store   *history.Store
	reg     *state.Registry
	engine  ocr.Engine
	logf    func(string, ...any)
}

func New(store *history.Store, reg *state.Registry, engine ocr.Engine, logf func(string, ...any)) *Supervisor {
	return &Supervisor{
		NewSource: source.For,
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
	src, err := s.NewSource(w)
	if err != nil {
		return fmt.Errorf("watch %q: source: %w", w.Name, err)
	}
	r, err := runner.New(w, src, s.engine, notifier, s.store, s.logf)
	if err != nil {
		return err
	}
	name := w.Name
	// Captured under s.mu so a data race with a later field write is the
	// caller's misuse, matching NewSource's semantics.
	onEvent := s.OnEvent
	onHealth := s.OnHealth
	r.OnReading = func(ev trigger.Event, crop image.Image) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, crop); err != nil {
			s.logf("watch %s: encode sample: %v", name, err)
			return
		}
		s.reg.Add(name, state.Sample{TS: time.Now(), Reading: ev.Reading, Fired: ev.Fired, PNG: buf.Bytes()})
		// Reuse the same encoding for the hook; buf.Bytes() is read-only from
		// here on, matching the registry's PNG read-only convention.
		if onEvent != nil {
			onEvent(name, ev, buf.Bytes())
		}
	}
	if onHealth != nil {
		r.OnHealth = func(hev health.Event) { onHealth(name, hev) }
	}
	wctx, cancel := context.WithCancel(ctx)
	h := &handle{cancel: cancel, done: make(chan struct{})}
	s.running[name] = h
	go func() {
		r.Run(wctx)
		// Self-remove so Running() reflects reality even when the parent
		// ctx was cancelled externally (not via Stop). The identity check
		// (cur == h) guards against removing a different watch that was
		// later started under the same name (Stop->Start, or a StopAll
		// map swap followed by a fresh Start).
		s.mu.Lock()
		if cur, ok := s.running[name]; ok && cur == h {
			delete(s.running, name)
		}
		s.mu.Unlock()
		close(h.done)
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

// StopAll cancels every watch and waits for all goroutines to exit. It
// joins only the handles it snapshots, so a Start that races in during
// shutdown (landing in the fresh map left behind for it) can never make
// StopAll block: StopAll depends solely on its own snapshot.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	hs := s.running
	s.running = map[string]*handle{}
	s.mu.Unlock()
	for _, h := range hs {
		h.cancel()
	}
	for _, h := range hs {
		<-h.done
	}
}
