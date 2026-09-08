package main

import (
	"context"
	"fmt"
	"image"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/supervisor"
)

type fakeStartSource struct{}

func (fakeStartSource) Grab(ctx context.Context) (image.Image, error) {
	return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil
}

// logCollector is a concurrency-safe sink for the logf callback: each
// started watch's poll loop runs on its own goroutine and may log (e.g. a
// source error) concurrently with the test reading back what startWatches
// itself logged.
type logCollector struct {
	mu   sync.Mutex
	msgs []string
}

func (c *logCollector) logf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, fmt.Sprintf(format, args...))
}

func (c *logCollector) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

// TestStartWatchesSurvivesPerWatchFailure is Bug 1b: a boot-time
// sup.Start failure for one watch must not abort startWatches (and, by
// extension, must not exit the whole daemon per run()'s use of it) — the
// remaining watches must still start, and the web UI stays reachable as
// the recovery path for whatever's wrong with the failed watch's config.
//
// config.Validate (Bug 1a) now closes off the config-shaped ways Start can
// fail after Validate passed, so this test reaches a Start-only failure
// the way supervisor.Start itself defines one: starting the same watch
// name twice ("watch %q already running"), which needs no fixture beyond
// two config.Watch values sharing a Name — Validate is never consulted
// here since the watch slice is built directly, matching how a
// hand-edited config.yaml (or a future config-shaped Start failure this
// test doesn't anticipate) could still reach this loop.
func TestStartWatchesSurvivesPerWatchFailure(t *testing.T) {
	reg := state.New(5)
	sup := supervisor.New(nil, reg, nil, func(string, ...any) {})
	sup.NewSource = func(w config.Watch) (source.Source, error) { return fakeStartSource{}, nil }
	t.Cleanup(sup.StopAll)

	watchTemplate := config.Watch{
		Source:   "http://unused.invalid/snap.jpg",
		Interval: config.Duration(time.Minute),
		Region:   config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger:  config.Trigger{Type: "pixel_change", Threshold: 10},
	}
	a := watchTemplate
	a.Name = "a"
	dup := watchTemplate
	dup.Name = "a" // triggers "watch %q already running" at Start, not Validate
	b := watchTemplate
	b.Name = "b"
	watches := []config.Watch{a, dup, b}

	logs := &logCollector{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	startWatches(ctx, sup, watches, logs.logf)

	running := sup.Running()
	sort.Strings(running)
	if len(running) != 2 || running[0] != "a" || running[1] != "b" {
		t.Fatalf("running = %v, want [a b] (the duplicate's failure must not have stopped watch b from starting)", running)
	}

	found := false
	for _, m := range logs.snapshot() {
		if strings.Contains(m, "ERROR") && strings.Contains(m, `"a"`) &&
			strings.Contains(m, "failed to start") && strings.Contains(m, "already running") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an ERROR log naming the failed watch and its cause; got: %v", logs.snapshot())
	}
}
