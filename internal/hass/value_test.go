package hass

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

func numericWatch(name, unit, class string) config.Watch {
	w := testWatch(name)
	w.Trigger = config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Confirm: 2}
	w.Unit, w.DeviceClass = unit, class
	return w
}

// A17 golden: the value entity's discovery config is exactly what HA's
// MQTT sensor schema takes for a graphable number, and the entity names
// are no longer doubled with the device name.
func TestValueDiscoveryGolden(t *testing.T) {
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{numericWatch("demo-scale", "kg", "weight")})

	got := fc.find(t, "homeassistant/sensor/watchglass-demo-scale/value/config")
	if !got.retain {
		t.Error("discovery config must be retained")
	}
	const want = `{"availability_topic":"watchglass/status",` +
		`"device":{"identifiers":["watchglass_demo-scale"],"manufacturer":"watchglass","name":"watchglass demo-scale"},` +
		`"device_class":"weight","name":"Value","state_class":"measurement",` +
		`"state_topic":"watchglass/demo-scale/value","unique_id":"watchglass_demo-scale_value","unit_of_measurement":"kg"}`
	if got.payload != want {
		t.Errorf("value config\n got %s\nwant %s", got.payload, want)
	}
	const wantReading = `{"availability_topic":"watchglass/status",` +
		`"device":{"identifiers":["watchglass_demo-scale"],"manufacturer":"watchglass","name":"watchglass demo-scale"},` +
		`"name":"Reading","state_topic":"watchglass/demo-scale/reading","unique_id":"watchglass_demo-scale_reading"}`
	if r := fc.find(t, "homeassistant/sensor/watchglass-demo-scale/reading/config"); r.payload != wantReading {
		t.Errorf("reading config\n got %s\nwant %s", r.payload, wantReading)
	}

	// No unit or class: still a measurement, with neither key.
	fc.pubs = nil
	p.Sync([]config.Watch{numericWatch("demo-scale", "", "")})
	var m map[string]any
	if err := json.Unmarshal([]byte(fc.find(t, "homeassistant/sensor/watchglass-demo-scale/value/config").payload), &m); err != nil {
		t.Fatal(err)
	}
	if m["state_class"] != "measurement" || m["unit_of_measurement"] != nil || m["device_class"] != nil {
		t.Errorf("bare numeric value config = %v", m)
	}
}

// A17: a watch that isn't numeric has no value entity, and one that stops
// being numeric loses it (config and last number cleared).
func TestValueEntityOnlyForNumeric(t *testing.T) {
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{numericWatch("scale", "°C", "temperature")})
	fc.pubs = nil
	p.Sync([]config.Watch{testWatch("scale")}) // now pixel_change
	if c := fc.find(t, "homeassistant/sensor/watchglass-scale/value/config"); c.payload != "" || !c.retain {
		t.Errorf("value config must be cleared, got %+v", c)
	}
	if v := fc.find(t, "watchglass/scale/value"); v.payload != "" || !v.retain {
		t.Errorf("last value must be cleared, got %+v", v)
	}
	// Once cleared it isn't cleared again on every sync.
	fc.pubs = nil
	p.Sync([]config.Watch{testWatch("scale")})
	for _, pb := range fc.pubs {
		if strings.Contains(pb.topic, "/value") {
			t.Errorf("cleared again: %+v", pb)
		}
	}
}

func waitFor(t *testing.T, fc *lockedFakeClient, topic string) pub {
	t.Helper()
	var got pub
	pollFor(t, 2*time.Second, func() bool {
		for _, pb := range fc.snapshot() {
			if pb.topic == topic {
				got = pb
			}
		}
		return got.topic != ""
	})
	return got
}

// A17 publish-on-confirm: only a settled reading reaches the reading
// entity, the value topic carries a bare number, and a state that holds
// is published once, not every tick.
func TestPublishesOnlySettledChanges(t *testing.T) {
	fc := &lockedFakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	defer p.Close()
	p.Sync([]config.Watch{numericWatch("scale", "kg", "weight")})
	fc.reset()

	// Raw readings that haven't settled publish no reading or value; the
	// first one only says the stream is up (health seeding, below).
	p.OnEvent("scale", trigger.Event{Reading: "88.8"}, nil)
	p.OnEvent("scale", trigger.Event{Reading: "No digits"}, nil)
	settled := trigger.Event{Reading: "25.3", Settled: "25.3", HasSettled: true, Value: 25.3, HasValue: true}
	for i := 0; i < 5; i++ {
		p.OnEvent("scale", settled, nil)
	}
	p.OnEvent("scale", trigger.Event{Reading: "25.0", Settled: "25.0", HasSettled: true, Value: 25, HasValue: true}, nil)
	pollFor(t, 2*time.Second, func() bool { return len(fc.snapshot()) >= 5 })
	time.Sleep(50 * time.Millisecond) // let any stray publish land
	var got []string
	for _, pb := range fc.snapshot() {
		got = append(got, pb.topic+"="+pb.payload)
		if !pb.retain {
			t.Errorf("state must be retained: %+v", pb)
		}
	}
	want := []string{
		"watchglass/scale/health=online",
		"watchglass/scale/reading=25.3", "watchglass/scale/value=25.3",
		"watchglass/scale/reading=25.0", "watchglass/scale/value=25",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("publishes\n got %v\nwant %v", got, want)
	}
}

// The broker being away costs nothing (no publish is attempted, so no
// timeouts pile up on the worker), and the next connect republishes the
// discovery configs and the latest state: nothing that changed while it
// was away is lost, and a broker that lost its retained messages is healed.
func TestReconnectResyncsDiscoveryAndState(t *testing.T) {
	fc := &lockedFakeClient{down: true}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	defer p.Close()
	p.SyncAsync([]config.Watch{numericWatch("scale", "kg", "weight")})
	pollFor(t, 2*time.Second, func() bool {
		p.mu.RLock()
		defer p.mu.RUnlock()
		return p.slugs["scale"] != ""
	})
	p.OnEvent("scale", trigger.Event{Settled: "24.1", HasSettled: true, Value: 24.1, HasValue: true}, nil)
	p.OnHealth("scale", health.Event{State: "down"})
	p.OnEvent("scale", trigger.Event{Settled: "25.3", HasSettled: true, Value: 25.3, HasValue: true, Fired: true}, []byte("PNG"))
	time.Sleep(100 * time.Millisecond)
	if pubs := fc.snapshot(); len(pubs) != 0 {
		t.Fatalf("nothing may be published while the broker is away, got %v", pubs)
	}

	fc.setDown(false)
	p.Reconnected()
	waitFor(t, fc, "watchglass/scale/snapshot")
	for topic, want := range map[string]string{
		"watchglass/scale/reading":  "25.3",
		"watchglass/scale/value":    "25.3",
		"watchglass/scale/health":   "offline",
		"watchglass/scale/snapshot": "PNG",
	} {
		if got := waitFor(t, fc, topic); got.payload != want || !got.retain {
			t.Errorf("%s = %+v, want %q retained", topic, got, want)
		}
	}
	waitFor(t, fc, "homeassistant/sensor/watchglass-scale/value/config")
	for _, pb := range fc.snapshot() {
		if pb.topic == "watchglass/scale/motion" {
			t.Error("a motion pulse from while the broker was away must not be replayed")
		}
	}

	// A second connect (the broker restarted and lost everything) sends it
	// all again, though nothing changed.
	fc.reset()
	p.Reconnected()
	if got := waitFor(t, fc, "watchglass/scale/value"); got.payload != "25.3" {
		t.Errorf("resync value = %+v", got)
	}
}

// A watch deleted while the broker is away has its topics cleared on the
// next connect.
func TestRemovalWhileAwayIsClearedOnConnect(t *testing.T) {
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{testWatch("a"), testWatch("b")})
	fc.down = true
	fc.pubs = nil
	p.Sync([]config.Watch{testWatch("a")})
	if len(fc.pubs) != 0 {
		t.Fatalf("published while away: %v", fc.pubs)
	}
	fc.down = false
	p.resync()
	if c := fc.find(t, "homeassistant/sensor/watchglass-b/reading/config"); c.payload != "" {
		t.Errorf("b's config must be cleared on connect, got %+v", c)
	}
	if c := fc.find(t, "homeassistant/sensor/watchglass-a/reading/config"); c.payload == "" {
		t.Error("a's config must be republished on connect")
	}
}

// A18: every failed attempt updates Status with a reason a person can act
// on; the log gets the first failure at once and then at most one line a
// minute; a connect resets that.
func TestTrackerStatusAndRateLimitedLog(t *testing.T) {
	var lines []string
	tr := newTracker("tcp://user:secret@127.0.0.1:18884", func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	tr.now = func() time.Time { return now }
	if st, err := tr.status(); st != StateConnecting || err != nil {
		t.Errorf("before the first attempt: %q %v", st, err)
	}
	refused := fmt.Errorf("%w : %w", packets.ErrorNetworkError,
		&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)})
	for i := 0; i < 20; i++ { // 20 retries, 5 s apart: 95 s
		tr.failed(refused)
		now = now.Add(5 * time.Second)
	}
	st, err := tr.status()
	if st != StateDown || err == nil || err.Error() != "connection refused" {
		t.Errorf("status = %q %v", st, err)
	}
	if len(lines) != 2 {
		t.Errorf("want 2 log lines in 95 s of retries, got %d: %q", len(lines), lines)
	}
	if len(lines) > 0 && !strings.HasPrefix(lines[0], "mqtt: can't connect to tcp://user:xxxxx@127.0.0.1:18884: connection refused (retrying)") {
		t.Errorf("line = %q", lines[0])
	}
	for _, l := range lines {
		if strings.Contains(l, "secret") {
			t.Errorf("the broker password leaked: %q", l)
		}
	}
	tr.connected()
	if st, err := tr.status(); st != StateConnected || err != nil {
		t.Errorf("after connect: %q %v", st, err)
	}
	lines = nil
	tr.lost(errors.New("EOF"))
	tr.failed(refused) // the first failure of a new outage logs at once
	if len(lines) != 2 || !strings.Contains(lines[1], "connection refused") {
		t.Errorf("after a lost connection: %q", lines)
	}
}

func TestConnReason(t *testing.T) {
	winRefused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", syscall.Errno(10061))}
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w : %w", packets.ErrorNetworkError, winRefused), "connection refused"},
		{packets.ErrorRefusedBadUsernameOrPassword, "wrong username or password"},
		{packets.ErrorRefusedNotAuthorised, "not authorised (check the username and password)"},
		{&net.DNSError{Err: "no such host", Name: "broker.lan", IsNotFound: true}, "no such host"},
		{&net.OpError{Op: "dial", Err: timeoutErr{}}, "no answer (timed out)"},
		{fmt.Errorf("%w : %w", packets.ErrorNetworkError, errors.New("something odd")), "something odd"},
	} {
		if got := connReason(c.err); got != c.want {
			t.Errorf("connReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	ce := &ConnError{Reason: "connection refused", Err: fmt.Errorf("%w : %w", packets.ErrorNetworkError, errors.New("dial tcp 127.0.0.1:1: actively refused it."))}
	if got := ce.Detail(); got != "dial tcp 127.0.0.1:1: actively refused it" {
		t.Errorf("Detail = %q", got)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// A18: paho's connection notifications reach the tracker, so a broker on
// a dead port shows up in Status and the log instead of silence.
func TestBuildOptionsReportsFailedAttempts(t *testing.T) {
	var lines []string
	tr := newTracker("tcp://127.0.0.1:1", func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
	opts := buildOptions(mqttCfg(), tr, nil)
	if opts.OnConnectionNotification == nil {
		t.Fatal("no connection notification handler")
	}
	if opts.MaxReconnectInterval != maxReconnectEvery {
		t.Errorf("max reconnect interval = %v, want %v", opts.MaxReconnectInterval, maxReconnectEvery)
	}
	opts.OnConnectionNotification(nil, mqtt.ConnectionNotificationFailed{Reason: fmt.Errorf("%w : %w", packets.ErrorNetworkError,
		&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)})})
	if st, err := tr.status(); st != StateDown || err == nil || err.Error() != "connection refused" {
		t.Errorf("status after a failed attempt = %q %v", st, err)
	}
	if len(lines) != 1 {
		t.Errorf("log = %q", lines)
	}
	opts.OnConnect(&spyPahoClient{})
	if st, _ := tr.status(); st != StateConnected {
		t.Errorf("status after connect = %q", st)
	}
	opts.OnConnectionNotification(nil, mqtt.ConnectionNotificationLost{Reason: errors.New("EOF")})
	if st, err := tr.status(); st != StateDown || err == nil || !strings.HasPrefix(err.Error(), "connection lost") {
		t.Errorf("status after a lost connection = %q %v", st, err)
	}
}

// The stream's health only reports changes, so HA's Health entity would
// stay unknown until the first down. The first reading seeds "online"
// once; a later down wins over it, a reconnect resyncs it, and a watch
// removed and added again is seeded again.
func TestFirstReadingSeedsHealthOnline(t *testing.T) {
	fc := &lockedFakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	defer p.Close()
	p.Sync([]config.Watch{testWatch("cam")})
	fc.reset()

	healthPubs := func() []string {
		var out []string
		for _, pb := range fc.snapshot() {
			if pb.topic == "watchglass/cam/health" {
				out = append(out, pb.payload)
			}
		}
		return out
	}
	for i := 0; i < 3; i++ {
		p.OnEvent("cam", trigger.Event{Reading: fmt.Sprintf("frame %d", i)}, nil)
	}
	if got := waitFor(t, fc, "watchglass/cam/health"); got.payload != "online" || !got.retain {
		t.Fatalf("first reading: health = %+v, want online retained", got)
	}
	p.OnHealth("cam", health.Event{State: "down"})
	p.OnEvent("cam", trigger.Event{Reading: "late"}, nil)
	pollFor(t, 2*time.Second, func() bool { return len(healthPubs()) >= 2 })
	time.Sleep(50 * time.Millisecond)
	if got := strings.Join(healthPubs(), " "); got != "online offline" {
		t.Errorf("health publishes = %q, want seeded once, then the down", got)
	}

	fc.reset()
	p.Reconnected()
	if got := waitFor(t, fc, "watchglass/cam/health"); got.payload != "offline" {
		t.Errorf("resync health = %+v, want offline", got)
	}

	// Removed and added again: its topics were cleared, so it seeds again.
	p.SyncAsync(nil)
	pollFor(t, 2*time.Second, func() bool {
		p.mu.RLock()
		defer p.mu.RUnlock()
		return p.slugs["cam"] == "" && !p.healthKnown["cam"]
	})
	p.SyncAsync([]config.Watch{testWatch("cam")})
	pollFor(t, 2*time.Second, func() bool {
		p.mu.RLock()
		defer p.mu.RUnlock()
		return p.slugs["cam"] != ""
	})
	fc.reset()
	p.OnEvent("cam", trigger.Event{Reading: "back"}, nil)
	if got := waitFor(t, fc, "watchglass/cam/health"); got.payload != "online" {
		t.Errorf("re-added watch: health = %+v, want online", got)
	}
}
