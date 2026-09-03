package hass

import (
	"sync"
	"testing"
	"time"
)

// lockedFakeClient is a concurrency-safe stand-in for fakeClient: with the
// background worker now the sole publisher (both for Sync and for
// OnEvent/OnHealth's queued jobs), tests observe pubs from a different
// goroutine than the one doing the publishing, so plain fakeClient's
// unsynchronized slice would race.
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

func (f *lockedFakeClient) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pubs = nil
}

func (f *lockedFakeClient) find(t *testing.T, topic string) pub {
	t.Helper()
	for _, p := range f.snapshot() {
		if p.topic == topic {
			return p
		}
	}
	t.Fatalf("no publish to %q; got %v", topic, f.snapshot())
	return pub{}
}

// pollFor polls cond every 5ms until it returns true or timeout elapses,
// failing the test on timeout. Used wherever a background worker goroutine
// needs time to drain a queued publish before a test can observe it.
func pollFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
