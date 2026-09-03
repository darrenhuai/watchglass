package hass

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"watchglass/internal/config"
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
