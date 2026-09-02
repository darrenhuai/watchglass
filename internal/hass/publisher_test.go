package hass

import (
	"testing"

	"watchglass/internal/config"
	"watchglass/internal/health"
	"watchglass/internal/trigger"
)

func syncedPublisher(t *testing.T) (*Publisher, *fakeClient) {
	t.Helper()
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{testWatch("printer")})
	fc.pubs = nil
	return p, fc
}

func TestOnEventPublishesReading(t *testing.T) {
	p, fc := syncedPublisher(t)
	p.OnEvent("printer", trigger.Event{Reading: "Printing 87%"}, nil)
	r := fc.find(t, "watchglass/printer/reading")
	if r.payload != "Printing 87%" || !r.retain {
		t.Errorf("reading pub = %+v", r)
	}
	for _, pb := range fc.pubs {
		if pb.topic == "watchglass/printer/motion" || pb.topic == "watchglass/printer/snapshot" {
			t.Errorf("non-fired event must not publish %s", pb.topic)
		}
	}
}

func TestOnEventFiredPublishesMotionAndSnapshot(t *testing.T) {
	p, fc := syncedPublisher(t)
	png := []byte{0x89, 'P', 'N', 'G'}
	p.OnEvent("printer", trigger.Event{Reading: "PRINT COMPLETE", Fired: true}, png)
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
	p.OnEvent("printer", trigger.Event{Reading: "x", Fired: true}, nil)
	fc.find(t, "watchglass/printer/motion")
	for _, pb := range fc.pubs {
		if pb.topic == "watchglass/printer/snapshot" {
			t.Error("nil png must not publish a snapshot")
		}
	}
}

func TestOnHealthMapsStates(t *testing.T) {
	p, fc := syncedPublisher(t)
	p.OnHealth("printer", health.Event{State: "down", Message: "boom"})
	h := fc.find(t, "watchglass/printer/health")
	if h.payload != "offline" || !h.retain {
		t.Errorf("health pub = %+v", h)
	}
	fc.pubs = nil
	p.OnHealth("printer", health.Event{State: "healthy"})
	if fc.find(t, "watchglass/printer/health").payload != "online" {
		t.Error("healthy must map to online")
	}
}

func TestEventsIgnoreUnknownAndSkippedWatches(t *testing.T) {
	p, fc := syncedPublisher(t)
	p.OnEvent("ghost", trigger.Event{Reading: "x"}, nil)
	p.OnHealth("ghost", health.Event{State: "down"})
	if len(fc.pubs) != 0 {
		t.Errorf("unknown watch must publish nothing, got %v", fc.pubs)
	}
}
