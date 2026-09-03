package hass

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"watchglass/internal/config"
	"watchglass/internal/health"
	"watchglass/internal/trigger"
)

func syncedPublisher(t *testing.T) (*Publisher, *lockedFakeClient) {
	t.Helper()
	fc := &lockedFakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{testWatch("printer")}) // direct call: synchronous on this goroutine
	fc.reset()
	return p, fc
}

func TestOnEventPublishesReading(t *testing.T) {
	p, fc := syncedPublisher(t)
	defer p.Close()

	p.OnEvent("printer", trigger.Event{Reading: "Printing 87%"}, nil)
	pollFor(t, 2*time.Second, func() bool {
		for _, pb := range fc.snapshot() {
			if pb.topic == "watchglass/printer/reading" {
				return true
			}
		}
		return false
	})

	r := fc.find(t, "watchglass/printer/reading")
	if r.payload != "Printing 87%" || !r.retain {
		t.Errorf("reading pub = %+v", r)
	}
	// The reading publish is the only (and therefore last) call in a
	// non-fired event's batch, so seeing it above means the whole job
	// closure already finished running — this check is not racy.
	for _, pb := range fc.snapshot() {
		if pb.topic == "watchglass/printer/motion" || pb.topic == "watchglass/printer/snapshot" {
			t.Errorf("non-fired event must not publish %s", pb.topic)
		}
	}
}

func TestOnEventFiredPublishesMotionAndSnapshot(t *testing.T) {
	p, fc := syncedPublisher(t)
	defer p.Close()

	png := []byte{0x89, 'P', 'N', 'G'}
	p.OnEvent("printer", trigger.Event{Reading: "PRINT COMPLETE", Fired: true}, png)
	pollFor(t, 2*time.Second, func() bool {
		for _, pb := range fc.snapshot() {
			if pb.topic == "watchglass/printer/snapshot" {
				return true
			}
		}
		return false
	})

	m := fc.find(t, "watchglass/printer/motion")
	if m.payload != "ON" || m.retain {
		t.Errorf("motion pub = %+v (must be ON, not retained)", m)
	}
	s := fc.find(t, "watchglass/printer/snapshot")
	if s.payload != string(png) || !s.retain {
		t.Errorf("snapshot pub = %+v", s)
	}
}

func TestOnEventFiredNilPNGSkipsSnapshot(t *testing.T) {
	p, fc := syncedPublisher(t)
	defer p.Close()

	p.OnEvent("printer", trigger.Event{Reading: "x", Fired: true}, nil)
	pollFor(t, 2*time.Second, func() bool {
		for _, pb := range fc.snapshot() {
			if pb.topic == "watchglass/printer/motion" {
				return true
			}
		}
		return false
	})
	// motion is the last publish in this batch (nil png skips snapshot), so
	// the job closure has finished running by the time motion is visible.
	for _, pb := range fc.snapshot() {
		if pb.topic == "watchglass/printer/snapshot" {
			t.Error("nil png must not publish a snapshot")
		}
	}
}

func TestOnHealthMapsStates(t *testing.T) {
	p, fc := syncedPublisher(t)
	defer p.Close()

	p.OnHealth("printer", health.Event{State: "down", Message: "boom"})
	pollFor(t, 2*time.Second, func() bool {
		for _, pb := range fc.snapshot() {
			if pb.topic == "watchglass/printer/health" {
				return true
			}
		}
		return false
	})
	if h := fc.find(t, "watchglass/printer/health"); h.payload != "offline" || !h.retain {
		t.Errorf("health pub = %+v", h)
	}

	fc.reset()
	p.OnHealth("printer", health.Event{State: "healthy"})
	pollFor(t, 2*time.Second, func() bool {
		for _, pb := range fc.snapshot() {
			if pb.topic == "watchglass/printer/health" {
				return true
			}
		}
		return false
	})
	if fc.find(t, "watchglass/printer/health").payload != "online" {
		t.Error("healthy must map to online")
	}
}

func TestEventsIgnoreUnknownAndSkippedWatches(t *testing.T) {
	p, fc := syncedPublisher(t)
	defer p.Close()

	// Neither call resolves a slug, so neither ever reaches the job queue —
	// nothing to wait for, the assertion below is immediately deterministic.
	p.OnEvent("ghost", trigger.Event{Reading: "x"}, nil)
	p.OnHealth("ghost", health.Event{State: "down"})
	if pubs := fc.snapshot(); len(pubs) != 0 {
		t.Errorf("unknown watch must publish nothing, got %v", pubs)
	}
}

// TestOnEventNeverBlocks proves OnEvent returns immediately even while the
// worker is busy inside a slow Publish — the whole point of routing event
// publishes through the job queue instead of calling Publish inline on the
// poll goroutine.
func TestOnEventNeverBlocks(t *testing.T) {
	fc := &lockedFakeClient{delay: 100 * time.Millisecond}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	defer p.Close()
	p.Sync([]config.Watch{testWatch("printer")})
	fc.reset()

	for i := 0; i < 10; i++ {
		start := time.Now()
		p.OnEvent("printer", trigger.Event{Reading: fmt.Sprintf("r%d", i)}, nil)
		if elapsed := time.Since(start); elapsed >= 10*time.Millisecond {
			t.Errorf("OnEvent call %d took %v, want <10ms", i, elapsed)
		}
	}
}

// TestOnEventBatchOrderPreserved proves a single fired event's reading,
// motion, and snapshot publishes land in order, even though they now run
// on the worker goroutine rather than inline — because they're enqueued as
// one closure, the worker can't interleave another event's publishes
// between them.
func TestOnEventBatchOrderPreserved(t *testing.T) {
	fc := &lockedFakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	defer p.Close()
	p.Sync([]config.Watch{testWatch("printer")})
	fc.reset()

	png := []byte{0x89, 'P', 'N', 'G'}
	p.OnEvent("printer", trigger.Event{Reading: "PRINT COMPLETE", Fired: true}, png)

	pollFor(t, 2*time.Second, func() bool { return len(fc.snapshot()) >= 3 })

	pubs := fc.snapshot()
	if len(pubs) != 3 {
		t.Fatalf("want exactly 3 publishes for one fired event, got %d: %+v", len(pubs), pubs)
	}
	wantOrder := []string{
		"watchglass/printer/reading",
		"watchglass/printer/motion",
		"watchglass/printer/snapshot",
	}
	for i, want := range wantOrder {
		if pubs[i].topic != want {
			t.Errorf("publish %d topic = %q, want %q (full order: %+v)", i, pubs[i].topic, want, pubs)
		}
	}
}

// TestPublishQueueOverflowDrops proves that once the 32-deep job queue is
// full, enqueue drops the newest job (logging it) instead of blocking the
// caller — the fixed capacity is a hard ceiling on outstanding publishes.
func TestPublishQueueOverflowDrops(t *testing.T) {
	fc := &lockedFakeClient{delay: 50 * time.Millisecond}
	var logged []string
	p := NewPublisher(fc, mqttCfg(), func(f string, a ...any) {
		logged = append(logged, fmt.Sprintf(f, a...))
	})
	defer p.Close()
	p.Sync([]config.Watch{testWatch("printer")})
	fc.reset()
	logged = nil

	const burst = 50 // capacity is 32; this must overflow it
	start := time.Now()
	for i := 0; i < burst; i++ {
		p.OnEvent("printer", trigger.Event{Reading: fmt.Sprintf("r%d", i)}, nil)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("%d OnEvent calls took %v, want well under the 50ms publish delay (never block)", burst, elapsed)
	}

	drops := 0
	for _, l := range logged {
		if strings.Contains(l, "queue full") {
			drops++
		}
	}
	if drops == 0 {
		t.Error("expected at least one dropped-job log line when the queue overflows")
	}
}
