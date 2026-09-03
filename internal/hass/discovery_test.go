package hass

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"watchglass/internal/config"
)

type pub struct {
	topic   string
	retain  bool
	payload string
}

type fakeClient struct {
	pubs   []pub
	closed bool
}

func (f *fakeClient) Publish(topic string, qos byte, retain bool, payload []byte) error {
	f.pubs = append(f.pubs, pub{topic: topic, retain: retain, payload: string(payload)})
	return nil
}
func (f *fakeClient) Close() { f.closed = true }

func (f *fakeClient) find(t *testing.T, topic string) pub {
	t.Helper()
	for _, p := range f.pubs {
		if p.topic == topic {
			return p
		}
	}
	t.Fatalf("no publish to %q; got %v", topic, f.pubs)
	return pub{}
}

func testWatch(name string) config.Watch {
	return config.Watch{
		Name: name, Source: "http://x/s.jpg",
		Interval: config.Duration(5 * time.Second),
		Region:   config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger:  config.Trigger{Type: "pixel_change", Threshold: 10},
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"printer-lcd":     "printer-lcd",
		"Printer LCD #2":  "printer-lcd-2",
		"lab/instrument":  "lab-instrument",
		"  Weird   name ": "weird-name",
		"already_ok_123":  "already_ok_123",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSyncPublishesDiscoveryConfigs(t *testing.T) {
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{testWatch("Printer LCD")})

	cfgPub := fc.find(t, "homeassistant/sensor/watchglass-printer-lcd/reading/config")
	if !cfgPub.retain {
		t.Error("discovery config must be retained")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(cfgPub.payload), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["state_topic"] != "watchglass/printer-lcd/reading" {
		t.Errorf("state_topic = %v", payload["state_topic"])
	}
	if payload["unique_id"] != "watchglass_printer-lcd_reading" {
		t.Errorf("unique_id = %v", payload["unique_id"])
	}
	if payload["availability_topic"] != "watchglass/status" {
		t.Errorf("availability_topic = %v", payload["availability_topic"])
	}
	dev, ok := payload["device"].(map[string]any)
	if !ok {
		t.Fatal("missing device block")
	}
	if dev["manufacturer"] != "watchglass" {
		t.Errorf("device = %v", dev)
	}

	health := fc.find(t, "homeassistant/binary_sensor/watchglass-printer-lcd/health/config")
	if !strings.Contains(health.payload, `"device_class":"connectivity"`) {
		t.Errorf("health payload = %s", health.payload)
	}
	motion := fc.find(t, "homeassistant/binary_sensor/watchglass-printer-lcd/motion/config")
	if !strings.Contains(motion.payload, `"off_delay":30`) {
		t.Errorf("motion payload = %s", motion.payload)
	}
	camera := fc.find(t, "homeassistant/camera/watchglass-printer-lcd/snapshot/config")
	if !strings.Contains(camera.payload, `"topic":"watchglass/printer-lcd/snapshot"`) {
		t.Errorf("camera payload = %s", camera.payload)
	}
}

func TestSyncClearsRemovedWatches(t *testing.T) {
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{testWatch("a"), testWatch("b")})
	fc.pubs = nil
	p.Sync([]config.Watch{testWatch("a")})
	cleared := fc.find(t, "homeassistant/sensor/watchglass-b/reading/config")
	if cleared.payload != "" || !cleared.retain {
		t.Errorf("removed watch must get empty retained payload, got %+v", cleared)
	}
	for _, pb := range fc.pubs {
		if strings.Contains(pb.topic, "watchglass-a") && pb.payload == "" {
			t.Errorf("surviving watch was cleared: %+v", pb)
		}
	}
}

func TestSyncSkipsSlugCollisions(t *testing.T) {
	var logged []string
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(f string, a ...any) { logged = append(logged, f) })
	p.Sync([]config.Watch{testWatch("Printer LCD"), testWatch("printer lcd")})
	count := 0
	for _, pb := range fc.pubs {
		if strings.Contains(pb.topic, "printer-lcd") && pb.payload != "" {
			count++
		}
	}
	if count != 4 {
		t.Errorf("expected exactly 4 configs for the first watch, got %d", count)
	}
	if len(logged) == 0 {
		t.Error("collision must be logged")
	}
	if !p.skipped["printer lcd"] {
		t.Error("collided watch must be tracked as skipped for event publishing")
	}
}

func TestSyncSameSlugRenamePreservesConfigs(t *testing.T) {
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{testWatch("x")})
	fc.pubs = nil
	p.Sync([]config.Watch{testWatch("X")})

	cfgPub := fc.find(t, "homeassistant/sensor/watchglass-x/reading/config")
	if cfgPub.payload == "" {
		t.Errorf("renamed watch (same slug) must get a fresh config, got %+v", cfgPub)
	}
	for _, pb := range fc.pubs {
		if strings.Contains(pb.topic, "watchglass-x") && pb.payload == "" {
			t.Errorf("slug still claimed after rename must not be cleared: %+v", pb)
		}
	}
}

func TestSyncClearsRetainedStateTopicsOnRemoval(t *testing.T) {
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{testWatch("a"), testWatch("b")})
	fc.pubs = nil
	p.Sync([]config.Watch{testWatch("a")})
	for _, topic := range []string{
		"watchglass/b/reading", "watchglass/b/health", "watchglass/b/snapshot",
	} {
		cleared := fc.find(t, topic)
		if cleared.payload != "" || !cleared.retain {
			t.Errorf("%s: want empty retained clear, got %+v", topic, cleared)
		}
	}
	for _, pb := range fc.pubs {
		if pb.topic == "watchglass/b/motion" {
			t.Error("motion is not retained and must not be cleared")
		}
		if strings.HasPrefix(pb.topic, "watchglass/a/") && pb.payload == "" {
			t.Errorf("surviving watch state cleared: %+v", pb)
		}
	}
}
