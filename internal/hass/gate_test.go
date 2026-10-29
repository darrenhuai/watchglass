package hass

import (
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/trigger"
)

// A gated watch's poll whose gate was closed reaches OnEvent as the zero
// Event (supervisor gateClosedHook). It seeds the health topic "online",
// as a first reading does, and publishes nothing else: no reading, no
// value, no motion.
func TestZeroEventSeedsHealthOnly(t *testing.T) {
	p, fc := syncedPublisher(t)
	defer p.Close()
	p.OnEvent("printer", trigger.Event{}, nil)
	pollFor(t, 2*time.Second, func() bool { return len(fc.snapshot()) >= 1 })
	pubs := fc.snapshot()
	if len(pubs) != 1 || pubs[0].topic != "watchglass/printer/health" || pubs[0].payload != "online" || !pubs[0].retain {
		t.Fatalf("zero event published %+v; want only health online, retained", pubs)
	}
	// Health is known now: another closed poll publishes nothing.
	fc.reset()
	p.OnEvent("printer", trigger.Event{}, nil)
	p.OnEvent("printer", trigger.Event{HasSettled: true, Settled: "x"}, nil)
	pollFor(t, 2*time.Second, func() bool { return len(fc.snapshot()) >= 1 })
	if pubs := fc.snapshot(); len(pubs) != 1 || pubs[0].topic != "watchglass/printer/reading" {
		t.Errorf("after health was seeded: %+v; want only the later reading", pubs)
	}
}
