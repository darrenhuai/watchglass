package hass

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

func TestSyncAsyncDeliversLatest(t *testing.T) {
	fc := &lockedFakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	defer p.Close()

	p.SyncAsync([]config.Watch{testWatch("a")})
	p.SyncAsync([]config.Watch{testWatch("b")})

	pollFor(t, 2*time.Second, func() bool {
		for _, pb := range fc.snapshot() {
			if strings.Contains(pb.topic, "watchglass-b") && pb.payload != "" {
				return true
			}
		}
		return false
	})
}

func TestSyncAsyncNeverBlocks(t *testing.T) {
	fc := &lockedFakeClient{delay: 100 * time.Millisecond}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	defer p.Close()

	for i := 0; i < 10; i++ {
		watches := []config.Watch{testWatch(fmt.Sprintf("w%d", i))}
		start := time.Now()
		p.SyncAsync(watches)
		if elapsed := time.Since(start); elapsed >= 10*time.Millisecond {
			t.Errorf("SyncAsync call %d took %v, want <10ms", i, elapsed)
		}
	}
}

func TestCloseStopsWorkerPromptly(t *testing.T) {
	fc := &lockedFakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})

	done := make(chan struct{})
	go func() {
		p.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return promptly")
	}
	if !fc.closed() {
		t.Error("Close must close the underlying client")
	}
}

// TestCloseDrainsPendingJobs proves Close (discovery.go) actually rescues
// jobs that are still sitting in p.jobs when it's called, rather than
// letting syncWorker's select race them against quit and silently drop
// them: Close waits for the worker's done signal, which the worker only
// sends after drainRemaining (syncqueue.go) has run every job still queued.
// Every publish is asserted synchronously right after Close returns — no
// polling needed, since Close does not return until the worker confirms it
// drained (or the budget expires, which a fast fake publish never hits).
func TestCloseDrainsPendingJobs(t *testing.T) {
	fc := &lockedFakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{testWatch("printer")}) // direct call: synchronous
	fc.reset()

	const n = 5
	for i := 0; i < n; i++ {
		p.OnEvent("printer", trigger.Event{Reading: fmt.Sprintf("r%d", i)}, nil)
	}
	p.Close()

	pubs := fc.snapshot()
	got := map[string]bool{}
	for _, pb := range pubs {
		if pb.topic == "watchglass/printer/reading" {
			got[pb.payload] = true
		}
	}
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("r%d", i)
		if !got[want] {
			t.Errorf("reading %q never published; got %v", want, pubs)
		}
	}
	if !fc.closed() {
		t.Error("Close must close the underlying client")
	}
}
