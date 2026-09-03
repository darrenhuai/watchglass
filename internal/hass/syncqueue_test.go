package hass

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"watchglass/internal/config"
)

// lockedFakeClient is a concurrency-safe stand-in for fakeClient: the
// SyncAsync worker publishes from its own goroutine while a test reads
// pubs, so plain fakeClient's unsynchronized slice would race.
type lockedFakeClient struct {
	mu       sync.Mutex
	pubs     []pub
	delay    time.Duration
	isClosed bool
}

func (f *lockedFakeClient) Publish(topic string, qos byte, retain bool, payload []byte) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pubs = append(f.pubs, pub{topic: topic, retain: retain, payload: string(payload)})
	return nil
}

func (f *lockedFakeClient) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.isClosed = true
}

func (f *lockedFakeClient) snapshot() []pub {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]pub, len(f.pubs))
	copy(out, f.pubs)
	return out
}

func (f *lockedFakeClient) closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.isClosed
}

func TestSyncAsyncDeliversLatest(t *testing.T) {
	fc := &lockedFakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	defer p.Close()

	p.SyncAsync([]config.Watch{testWatch("a")})
	p.SyncAsync([]config.Watch{testWatch("b")})

	deadline := time.Now().Add(2 * time.Second)
	for {
		found := false
		for _, pb := range fc.snapshot() {
			if strings.Contains(pb.topic, "watchglass-b") && pb.payload != "" {
				found = true
				break
			}
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the latest sync (b) to be published; got %v", fc.snapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
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
