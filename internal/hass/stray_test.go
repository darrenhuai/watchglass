package hass

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// brokerFake is a lockedFakeClient that also keeps the broker's retained
// messages, the way mosquitto does: a retained publish stores the payload,
// an empty one deletes it, Subscribe hands every stored message its filter
// matches to the callback as retained, and every publish is forwarded live
// to the subscriptions it matches, the publisher's own included. Only
// pahoClient had Subscribe before, so these tests are the publisher's only
// view of reading the broker back.
type brokerFake struct {
	*lockedFakeClient
	rmu        sync.Mutex
	retained   map[string]string
	subscribed []string
	callbacks  map[string]func(topic string, payload []byte, retained bool)
	// late hands every retained message over a second time, a moment
	// later, the way paho's callback goroutines can still be delivering a
	// slug's configs after the worker has cleared it.
	late bool
	// slow holds back the retained messages of a filter for a while, the
	// way paho's goroutines can deliver one subscription's after another's.
	slow map[string]time.Duration
}

func newBrokerFake() *brokerFake {
	return &brokerFake{lockedFakeClient: &lockedFakeClient{}, retained: map[string]string{},
		callbacks: map[string]func(string, []byte, bool){}}
}

// topicMatches is MQTT's filter match, with + and #.
func topicMatches(filter, topic string) bool {
	f, t := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, level := range f {
		if level == "#" {
			return true
		}
		if i >= len(t) || level != "+" && level != t[i] {
			return false
		}
	}
	return len(f) == len(t)
}

func (b *brokerFake) Publish(topic string, qos byte, retain bool, payload []byte) error {
	if err := b.lockedFakeClient.Publish(topic, qos, retain, payload); err != nil {
		return err
	}
	b.rmu.Lock()
	if retain {
		if len(payload) == 0 {
			delete(b.retained, topic)
		} else {
			b.retained[topic] = string(payload)
		}
	}
	var fns []func(string, []byte, bool)
	for filter, fn := range b.callbacks {
		if topicMatches(filter, topic) {
			fns = append(fns, fn)
		}
	}
	b.rmu.Unlock()
	for _, fn := range fns {
		fn(topic, payload, false)
	}
	return nil
}

func (b *brokerFake) Subscribe(filter string, fn func(topic string, payload []byte, retained bool)) error {
	b.rmu.Lock()
	b.subscribed = append(b.subscribed, filter)
	b.callbacks[filter] = fn
	msgs := map[string]string{}
	for t, p := range b.retained {
		if topicMatches(filter, t) {
			msgs[t] = p
		}
	}
	delay := b.slow[filter]
	b.rmu.Unlock()
	deliver := func() {
		for t, p := range msgs {
			fn(t, []byte(p), true)
		}
	}
	if delay > 0 {
		go func() {
			time.Sleep(delay)
			deliver()
		}()
	} else {
		deliver()
	}
	if b.late {
		go func() {
			time.Sleep(20 * time.Millisecond)
			deliver()
		}()
	}
	return nil
}

// live hands a message to every subscription it matches, the way the
// broker forwards one published by another client while they stand.
func (b *brokerFake) live(topic, payload string) bool {
	b.rmu.Lock()
	var fns []func(string, []byte, bool)
	for filter, fn := range b.callbacks {
		if topicMatches(filter, topic) {
			fns = append(fns, fn)
		}
	}
	b.rmu.Unlock()
	for _, fn := range fns {
		fn(topic, []byte(payload), false)
	}
	return len(fns) > 0
}

func (b *brokerFake) has(topic string) bool {
	_, ok := b.get(topic)
	return ok
}

func (b *brokerFake) get(topic string) (string, bool) {
	b.rmu.Lock()
	defer b.rmu.Unlock()
	p, ok := b.retained[topic]
	return p, ok
}

func (b *brokerFake) subscribes() []string {
	b.rmu.Lock()
	defer b.rmu.Unlock()
	return append([]string(nil), b.subscribed...)
}

// sweeper is NewPublisher on a brokerFake with a short wait for the owner
// records (straySettle is 2 s), set before the first connect reaches the
// worker.
func sweeper(fc *brokerFake, cfg config.MQTT, logf func(string, ...any), settle time.Duration) *Publisher {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	p := NewPublisher(fc, cfg, logf)
	p.sweepSettle = settle
	return p
}

// logRecorder collects log lines from the worker and paho goroutines.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) logf(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
}

func (l *logRecorder) matching(sub string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range l.lines {
		if strings.Contains(line, sub) {
			out = append(out, line)
		}
	}
	return out
}

// seed stores what a run with these watches would have left on the broker:
// every entity config and a reading.
func (b *brokerFake) seed(cfg config.MQTT, watches ...config.Watch) {
	p := newPublisher(cfg, func(string, ...any) {})
	b.rmu.Lock()
	defer b.rmu.Unlock()
	for _, w := range watches {
		slug := Slug(w.Name)
		for _, e := range p.entities(w, slug) {
			raw, _ := json.Marshal(e.payload)
			b.retained[p.discoveryTopic(e.component, slug, e.object)] = string(raw)
		}
		b.retained[cfg.BaseTopic+"/"+slug+"/reading"] = "PRINT COMPLETE"
	}
}

// clearedTopics are the retained topics a run published empty.
func clearedTopics(pubs []pub) map[string]bool {
	out := map[string]bool{}
	for _, p := range pubs {
		if p.retain && p.payload == "" {
			out[p.topic] = true
		}
	}
	return out
}

// D1 (live check against Home Assistant 2026.10): a watch renamed in
// config.yaml, or removed, while watchglass was stopped left its old
// device in HA for good, still showing its last reading as online,
// because only a watch removed during a run had its retained configs
// cleared. On connect the publisher now reads the broker's retained
// discovery configs back and clears this instance's that no watch claims,
// and leaves everything else alone: other integrations' devices, another
// watchglass with its own base_topic, and the current watches.
func TestStrayDeviceFromEarlierRunIsCleared(t *testing.T) {
	cfg := mqttCfg()
	fc := newBrokerFake()
	fc.late = true
	fc.seed(cfg, testWatch("printer"), numericWatch("old scale", "kg", "weight"))
	other := cfg
	other.BaseTopic = "watchglass2"
	fc.seed(other, testWatch("garage"))
	fc.rmu.Lock()
	fc.retained["homeassistant/sensor/0x00158d0001/temperature/config"] = `{"unique_id":"0x00158d0001_temperature","availability_topic":"zigbee2mqtt/bridge/state"}`
	// Named like one of ours, but not written by watchglass.
	fc.retained["homeassistant/sensor/watchglass-lookalike/reading/config"] = `{"unique_id":"someone_else","availability_topic":"watchglass/status"}`
	fc.rmu.Unlock()

	logs := &logRecorder{}
	p := sweeper(fc, cfg, logs.logf, 30*time.Millisecond)
	defer p.Close()
	p.SyncAsync([]config.Watch{testWatch("printer")})
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool {
		return !fc.has("homeassistant/sensor/watchglass-old-scale/value/config")
	})
	time.Sleep(100 * time.Millisecond) // past the late delivery

	if subs := strings.Join(fc.subscribes(), " "); !strings.Contains(subs, "homeassistant/+/+/+/config") || !strings.Contains(subs, "watchglass/.owners/+") {
		t.Errorf("subscriptions = %v, want the retained discovery configs and the owner records", subs)
	}
	cleared := clearedTopics(fc.snapshot())
	for _, topic := range []string{
		"homeassistant/sensor/watchglass-old-scale/reading/config",
		"homeassistant/sensor/watchglass-old-scale/value/config",
		"homeassistant/binary_sensor/watchglass-old-scale/health/config",
		"homeassistant/binary_sensor/watchglass-old-scale/motion/config",
		"homeassistant/camera/watchglass-old-scale/snapshot/config",
		"watchglass/old-scale/reading",
	} {
		if !cleared[topic] || fc.has(topic) {
			t.Errorf("%s must be cleared, so HA drops the old device", topic)
		}
	}
	for _, topic := range []string{
		"homeassistant/sensor/watchglass-printer/reading/config",
		"homeassistant/sensor/watchglass-garage/reading/config",
		"homeassistant/sensor/0x00158d0001/temperature/config",
		"homeassistant/sensor/watchglass-lookalike/reading/config",
	} {
		if cleared[topic] || !fc.has(topic) {
			t.Errorf("%s must be left alone", topic)
		}
	}

	// One log line, for the device that went (though its configs came
	// twice), none for the current watch.
	clearing := logs.matching("clearing the Home Assistant device")
	if len(clearing) != 1 || !strings.Contains(clearing[0], "watchglass-old-scale") {
		t.Errorf("clearing log lines = %q, want one for watchglass-old-scale", clearing)
	}

	// The next connect sees nothing stray and clears nothing.
	fc.reset()
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool { return len(fc.subscribes()) >= 6 })
	time.Sleep(100 * time.Millisecond)
	if c := clearedTopics(fc.snapshot()); len(c) != 0 {
		t.Errorf("second connect cleared %v", c)
	}
}

// At startup the first connect can beat the first watch list to the
// worker. The retained configs of every current watch look stray then, and
// clearing them would make HA drop and re-add every device. The sweep
// waits for the list.
func TestStraySweepWaitsForTheWatchList(t *testing.T) {
	cfg := mqttCfg()
	fc := newBrokerFake()
	fc.seed(cfg, testWatch("printer"), testWatch("old"))

	p := sweeper(fc, cfg, nil, 30*time.Millisecond)
	defer p.Close()
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool { return len(fc.subscribes()) == 3 })
	time.Sleep(100 * time.Millisecond) // past the wait for owner records
	if c := clearedTopics(fc.snapshot()); len(c) != 0 {
		t.Fatalf("cleared %v before there was a watch list", c)
	}

	p.SyncAsync([]config.Watch{testWatch("printer")})
	pollFor(t, 2*time.Second, func() bool {
		return !fc.has("homeassistant/sensor/watchglass-old/reading/config")
	})
	time.Sleep(50 * time.Millisecond)
	if clearedTopics(fc.snapshot())["homeassistant/sensor/watchglass-printer/reading/config"] {
		t.Error("the current watch's config was cleared")
	}
	if !fc.has("homeassistant/sensor/watchglass-printer/reading/config") {
		t.Error("the current watch's config is gone from the broker")
	}
}

// D1: Home Assistant refuses a state longer than 255 characters ("exceeds
// the maximum allowed length (255)") and shows unknown instead, so a long
// reading is cut to fit, by characters, not bytes.
func TestReadingClippedToHAStateLimit(t *testing.T) {
	cases := map[string]struct {
		in, want string
	}{
		"short":       {"PRINT COMPLETE", "PRINT COMPLETE"},
		"exactly 255": {strings.Repeat("a", 255), strings.Repeat("a", 255)},
		"long ascii":  {strings.Repeat("a", 300), strings.Repeat("a", 254) + "…"},
		"long utf-8":  {strings.Repeat("é", 300), strings.Repeat("é", 254) + "…"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fc := &lockedFakeClient{}
			p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
			defer p.Close()
			p.SyncAsync([]config.Watch{testWatch("printer")})
			pollFor(t, 2*time.Second, func() bool {
				p.mu.RLock()
				defer p.mu.RUnlock()
				return p.slugs["printer"] != ""
			})
			p.OnEvent("printer", trigger.Event{Settled: c.in, HasSettled: true}, nil)
			got := waitFor(t, fc, "watchglass/printer/reading").payload
			if got != c.want {
				t.Errorf("reading = %q (%d chars), want %q", got, utf8.RuneCountInString(got), c.want)
			}
			if n := utf8.RuneCountInString(got); n > 255 {
				t.Errorf("reading has %d characters; HA keeps 255", n)
			}
		})
	}
}

// D1: a clear sent while Home Assistant was away from the broker never
// reaches it (the empty retained message leaves nothing for HA to find),
// so HA kept the device. Seen live: the broker came back, watchglass
// reconnected and cleared at once, HA's MQTT integration reconnected 12 s
// later and still showed the deleted watch. HA's birth message ("online"
// on homeassistant/status) makes watchglass send those clears again.
//
// D1 fixer: an HA that restarted (stopped while the watch was deleted, an
// update, a host reboot) ignores an empty config for an entity it hasn't
// been sent a config for since it started, and kept the device it restored
// from its registry. Seen live with HA 2026.10.0. So the replay sends each
// entity's config, not retained, before its empty one: HA takes the entity
// up again and then removes it. Each clear is replayed once, nothing goes
// out for a watch that came back, and a clear sent after HA's birth on
// this connection isn't kept.
func TestClearsAreSentAgainWhenHomeAssistantComesOnline(t *testing.T) {
	cfg := mqttCfg()
	fc := newBrokerFake()
	fc.seed(cfg, testWatch("printer"), testWatch("old"))
	p := sweeper(fc, cfg, nil, 30*time.Millisecond)
	defer p.Close()
	p.SyncAsync([]config.Watch{testWatch("printer"), testWatch("gone"), testWatch("back")})
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool {
		return !fc.has("homeassistant/sensor/watchglass-old/reading/config")
	})
	p.SyncAsync([]config.Watch{testWatch("printer")}) // gone and back deleted in this run
	pollFor(t, 2*time.Second, func() bool {
		return !fc.has("homeassistant/sensor/watchglass-gone/reading/config") &&
			!fc.has("homeassistant/sensor/watchglass-back/reading/config")
	})
	p.SyncAsync([]config.Watch{testWatch("printer"), testWatch("back")}) // and back returns
	pollFor(t, 2*time.Second, func() bool {
		return fc.has("homeassistant/sensor/watchglass-back/reading/config")
	})
	time.Sleep(50 * time.Millisecond)

	fc.reset()
	if !fc.live("homeassistant/status", "offline") {
		t.Fatal("no subscription to Home Assistant's status topic")
	}
	time.Sleep(50 * time.Millisecond)
	if pubs := fc.snapshot(); len(pubs) != 0 {
		t.Errorf("HA going offline sent %v", pubs)
	}
	fc.live("homeassistant/status", "online")
	pollFor(t, 2*time.Second, func() bool {
		c := clearedTopics(fc.snapshot())
		return c["homeassistant/camera/watchglass-old/snapshot/config"] && c["homeassistant/camera/watchglass-gone/snapshot/config"]
	})
	time.Sleep(50 * time.Millisecond)
	pubs := fc.snapshot()
	for _, slug := range []string{"old", "gone"} {
		for _, e := range allEntities {
			topic := "homeassistant/" + e.component + "/watchglass-" + slug + "/" + e.object + "/config"
			cfgAt, emptyAt := -1, -1
			for i, pb := range pubs {
				switch {
				case pb.topic != topic:
				case pb.payload == "" && pb.retain:
					emptyAt = i
				case !pb.retain && strings.Contains(pb.payload, `"unique_id":"watchglass_`+slug+`_`+e.object+`"`):
					cfgAt = i
				}
			}
			if emptyAt < 0 {
				t.Errorf("%s: empty config not sent again", topic)
			}
			if e.object == "value" {
				if cfgAt >= 0 {
					t.Errorf("%s: a config for an entity the watch never had", topic)
				}
				continue
			}
			if cfgAt < 0 || cfgAt > emptyAt {
				t.Errorf("%s: want the config (not retained) before the empty one; config at %d, empty at %d", topic, cfgAt, emptyAt)
			}
		}
	}
	for _, pb := range pubs {
		if strings.Contains(pb.topic, "watchglass-printer") || strings.Contains(pb.topic, "watchglass-back") {
			t.Errorf("replay touched a current watch: %+v", pb)
		}
	}
	if !fc.has("homeassistant/sensor/watchglass-back/reading/config") {
		t.Error("the returned watch's config is gone from the broker")
	}

	// Replayed once: the next birth sends nothing.
	fc.reset()
	fc.live("homeassistant/status", "online")
	time.Sleep(100 * time.Millisecond)
	if pubs := fc.snapshot(); len(pubs) != 0 {
		t.Errorf("second birth sent %v", pubs)
	}

	// HA's birth came on this connection, so a clear now reaches it at
	// once and isn't kept for the next birth.
	p.SyncAsync([]config.Watch{testWatch("printer")})
	pollFor(t, 2*time.Second, func() bool {
		return !fc.has("homeassistant/sensor/watchglass-back/reading/config")
	})
	time.Sleep(50 * time.Millisecond)
	fc.reset()
	fc.live("homeassistant/status", "online")
	time.Sleep(100 * time.Millisecond)
	for _, pb := range fc.snapshot() {
		if strings.Contains(pb.topic, "watchglass-back") {
			t.Errorf("a clear HA got at once was replayed: %+v", pb)
		}
	}
}

// D1 fixer (seen live): two watchglass with their own client_id but the
// same (default) base_topic removed each other's devices. The second one's
// sweep cleared all of the first one's at start, and restarting the first
// cleared the second's, because the configs both write pass the
// availability_topic and unique_id check. Each watchglass now keeps a
// retained owner record of its slugs on <base_topic>/.owners/<client_id>,
// and the sweep leaves alone every slug another one lists. The record can
// arrive after the configs (paho's goroutines), so the sweep waits for it.
func TestSecondInstanceOnTheSameBaseTopicKeepsItsDevices(t *testing.T) {
	cfg := mqttCfg()
	fc := newBrokerFake()
	fc.seed(cfg, testWatch("printer"), testWatch("old"), testWatch("room two"))
	fc.rmu.Lock()
	fc.retained["watchglass/.owners/watchglass-room2"] = `{"client_id":"watchglass-room2","run":"r2","slugs":["room-two"]}`
	fc.slow = map[string]time.Duration{"watchglass/.owners/+": 40 * time.Millisecond}
	fc.rmu.Unlock()
	logs := &logRecorder{}
	p := sweeper(fc, cfg, logs.logf, 200*time.Millisecond)
	defer p.Close()
	p.SyncAsync([]config.Watch{testWatch("printer")})
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool {
		return !fc.has("homeassistant/sensor/watchglass-old/reading/config")
	})
	pollFor(t, 2*time.Second, func() bool { return fc.has("watchglass/.owners/watchglass") })
	time.Sleep(50 * time.Millisecond)

	cleared := clearedTopics(fc.snapshot())
	for _, e := range allEntities[:4] {
		topic := "homeassistant/" + e.component + "/watchglass-room-two/" + e.object + "/config"
		if cleared[topic] || !fc.has(topic) {
			t.Errorf("%s belongs to the other watchglass and must be left alone", topic)
		}
	}
	if w := logs.matching(`another watchglass (client_id "watchglass-room2") also uses base_topic "watchglass"`); len(w) != 1 {
		t.Errorf("want one warning about the other watchglass; got %q", logs.matching("mqtt:"))
	}
	raw, _ := fc.get("watchglass/.owners/watchglass")
	var rec ownerRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil || rec.ClientID != "watchglass" || strings.Join(rec.Slugs, ",") != "printer" || rec.Run == "" {
		t.Errorf("own owner record = %s, want client_id watchglass with slugs [printer]", raw)
	}

	// A watch added in this run shows in the record.
	p.SyncAsync([]config.Watch{testWatch("printer"), testWatch("kitchen")})
	pollFor(t, 2*time.Second, func() bool {
		raw, _ := fc.get("watchglass/.owners/watchglass")
		return strings.Contains(raw, `"slugs":["kitchen","printer"]`)
	})
}

// The sweep only takes retained configs: a live one is being published
// right now, by this watchglass (its own echo) or by another one that just
// added a watch, and is never an earlier run's leftover.
func TestLiveConfigIsNotTakenForAStray(t *testing.T) {
	cfg := mqttCfg()
	fc := newBrokerFake()
	fc.seed(cfg, testWatch("printer"))
	p := sweeper(fc, cfg, nil, 30*time.Millisecond)
	defer p.Close()
	p.SyncAsync([]config.Watch{testWatch("printer")})
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool { return fc.has("watchglass/.owners/watchglass") })

	fc.reset()
	other := newPublisher(cfg, func(string, ...any) {})
	for topic, raw := range other.configPayloads(testWatch("visitor"), "visitor") {
		if !fc.live(topic, string(raw)) {
			t.Fatal("no subscription to the discovery configs")
		}
	}
	time.Sleep(150 * time.Millisecond)
	for _, pb := range fc.snapshot() {
		if strings.Contains(pb.topic, "watchglass-visitor") {
			t.Errorf("a live config was swept: %+v", pb)
		}
	}
}

// D1 fixer: with the same client_id the broker keeps dropping one
// watchglass for the other, and each reconnect would sweep the other's
// devices. Once a process has published its owner record, finding one
// with another run ID there means a second process uses the client_id: it
// warns once and stops sweeping.
func TestSameClientIDElsewhereStopsTheSweep(t *testing.T) {
	cfg := mqttCfg()
	fc := newBrokerFake()
	fc.seed(cfg, testWatch("printer"))
	logs := &logRecorder{}
	p := sweeper(fc, cfg, logs.logf, 30*time.Millisecond)
	defer p.Close()
	p.SyncAsync([]config.Watch{testWatch("printer")})
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool { return fc.has("watchglass/.owners/watchglass") })

	// The other process connects (dropping this one), writes its record
	// and its watch, and is dropped in turn.
	fc.seed(cfg, testWatch("intruder"))
	fc.rmu.Lock()
	fc.retained["watchglass/.owners/watchglass"] = `{"client_id":"watchglass","run":"someone-else","slugs":["intruder"]}`
	fc.rmu.Unlock()
	fc.reset()
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool { return len(logs.matching("same client_id")) > 0 })
	time.Sleep(100 * time.Millisecond)
	if !fc.has("homeassistant/sensor/watchglass-intruder/reading/config") {
		t.Error("the other process's device was swept")
	}
	if w := logs.matching(`same client_id "watchglass"`); len(w) != 1 {
		t.Errorf("want one warning; got %q", w)
	}
	if c := logs.matching("clearing the Home Assistant device"); len(c) != 0 {
		t.Errorf("swept after the clash: %q", c)
	}
}

// D1 fixer (seen live): with Home Assistant stopped and the broker up, the
// top bar said "Home Assistant: connected", because Status only knew the
// broker. HA says "online" and "offline" on homeassistant/status (its
// default birth topic, whatever the discovery prefix), and Status now
// reports StateHAOffline after an "offline". With a custom prefix,
// <prefix>/status is followed as well.
func TestStatusFollowsHomeAssistant(t *testing.T) {
	cfg := mqttCfg()
	cfg.DiscoveryPrefix = "custom"
	fc := newBrokerFake()
	p := sweeper(fc, cfg, nil, 30*time.Millisecond)
	defer p.Close()
	p.SyncAsync([]config.Watch{testWatch("printer")})
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool {
		subs := strings.Join(fc.subscribes(), " ")
		return strings.Contains(subs, "homeassistant/status") && strings.Contains(subs, "custom/status")
	})
	if state, err := p.Status(); state != StateConnected || err != nil {
		t.Errorf("before HA says anything: %q, %v; want connected", state, err)
	}
	for _, topic := range []string{"homeassistant/status", "custom/status"} {
		fc.live(topic, "offline")
		state, err := p.Status()
		var ce *ConnError
		if state != StateHAOffline || !errors.As(err, &ce) || ce.Reason != "it went offline; the MQTT broker is up" || ce.Detail() == "" {
			t.Errorf("%s offline: %q, %v; want %q with a reason", topic, state, err, StateHAOffline)
		}
		fc.live(topic, "online")
		if state, err := p.Status(); state != StateConnected || err != nil {
			t.Errorf("%s online: %q, %v; want connected", topic, state, err)
		}
	}
	fc.live("homeassistant/status", "offline")
	fc.setDown(true)
	if state, _ := p.Status(); state != StateDown {
		t.Errorf("broker down: %q; want %q, the broker comes first", state, StateDown)
	}
}

// D1 fixer 2 (seen live): an instance whose client_id was changed, as the
// docs advise for a second watchglass, took its own record under the old
// client_id for another watchglass. It warned about it on every start and
// never swept that record's slugs, so a watch renamed while it was stopped
// left its old device in HA, still online. A record that lists a slug this
// watchglass publishes is its own from before the change: it is deleted,
// with one log line, and protects nothing.
func TestOwnRecordUnderAnOldClientIDIsRetired(t *testing.T) {
	cfg := mqttCfg()
	fc := newBrokerFake()
	fc.seed(cfg, testWatch("printer"), testWatch("scale"))
	fc.rmu.Lock()
	fc.retained["watchglass/.owners/watchglass"] = `{"client_id":"watchglass","run":"old","slugs":["printer","scale"]}`
	fc.rmu.Unlock()
	cfg.ClientID = "watchglass-main"
	logs := &logRecorder{}
	p := sweeper(fc, cfg, logs.logf, 30*time.Millisecond)
	defer p.Close()
	p.SyncAsync([]config.Watch{testWatch("printer lcd"), testWatch("scale")}) // printer renamed while stopped
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool {
		return !fc.has("homeassistant/sensor/watchglass-printer/reading/config")
	})
	pollFor(t, 2*time.Second, func() bool { return fc.has("watchglass/.owners/watchglass-main") })
	time.Sleep(50 * time.Millisecond)

	if fc.has("watchglass/.owners/watchglass") {
		t.Error("the record under the old client_id is still on the broker")
	}
	cleared := clearedTopics(fc.snapshot())
	if !cleared["homeassistant/sensor/watchglass-printer/reading/config"] {
		t.Error("the renamed watch's old device wasn't cleared")
	}
	for _, topic := range []string{
		"homeassistant/sensor/watchglass-scale/reading/config",
		"homeassistant/sensor/watchglass-printer-lcd/reading/config",
	} {
		if cleared[topic] || !fc.has(topic) {
			t.Errorf("%s belongs to a current watch and must stay", topic)
		}
	}
	if w := logs.matching("another watchglass"); len(w) != 0 {
		t.Errorf("own old record taken for another watchglass: %q", w)
	}
	if r := logs.matching(`the watch list on watchglass/.owners/watchglass (client_id "watchglass")`); len(r) != 1 {
		t.Errorf("want one line about the old record; got %q", logs.matching("mqtt:"))
	}

	// The next start finds nothing to say.
	fc.reset()
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool { return fc.has("watchglass/.owners/watchglass-main") && len(fc.snapshot()) > 0 })
	time.Sleep(100 * time.Millisecond)
	if w := logs.matching("another watchglass"); len(w) != 0 {
		t.Errorf("warned after the old record was deleted: %q", w)
	}
	if c := clearedTopics(fc.snapshot()); len(c) != 0 {
		t.Errorf("second connect cleared %v", c)
	}
}

// D1 fixer 2: a clear made at the reconnect after a broker restart must be
// kept for HA's next birth even if HA's birth came on the connection
// before (HA is reconnecting to the restarted broker too, and may come
// back after watchglass). A retained "online" on HA's status topic, stale
// after a broker restart with persistence, doesn't count as HA being there.
func TestClearAtReconnectWaitsForHomeAssistantsNextBirth(t *testing.T) {
	cfg := mqttCfg()
	fc := newBrokerFake()
	p := sweeper(fc, cfg, nil, 30*time.Millisecond)
	defer p.Close()
	p.SyncAsync([]config.Watch{testWatch("printer"), testWatch("gone")})
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool { return fc.has("watchglass/.owners/watchglass") })
	if !fc.live("homeassistant/status", "online") {
		t.Fatal("no subscription to Home Assistant's status topic")
	}
	time.Sleep(50 * time.Millisecond)

	// The broker restarts; the watch is deleted while it is away.
	fc.setDown(true)
	p.SyncAsync([]config.Watch{testWatch("printer")})
	time.Sleep(50 * time.Millisecond)
	fc.rmu.Lock()
	fc.retained["homeassistant/status"] = "online"
	fc.rmu.Unlock()
	fc.setDown(false)
	fc.reset()
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool {
		return clearedTopics(fc.snapshot())["homeassistant/sensor/watchglass-gone/reading/config"]
	})
	time.Sleep(100 * time.Millisecond)

	fc.reset()
	fc.live("homeassistant/status", "online")
	pollFor(t, 2*time.Second, func() bool {
		return clearedTopics(fc.snapshot())["homeassistant/camera/watchglass-gone/snapshot/config"]
	})
	sentCfg := false
	for _, pb := range fc.snapshot() {
		if pb.topic == "homeassistant/sensor/watchglass-gone/reading/config" && !pb.retain && pb.payload != "" {
			sentCfg = true
		}
	}
	if !sentCfg {
		t.Error("HA's birth after the reconnect didn't get the deleted watch's config and clear")
	}
}

// D1 fixer 2: when another watchglass on the base_topic is gone for good,
// deleting its record (as the warning says) lets the sweep remove its
// devices on the next connect.
func TestDeletedOwnerRecordNoLongerProtects(t *testing.T) {
	cfg := mqttCfg()
	fc := newBrokerFake()
	fc.seed(cfg, testWatch("printer"), testWatch("room two"))
	fc.rmu.Lock()
	fc.retained["watchglass/.owners/watchglass-room2"] = `{"client_id":"watchglass-room2","run":"r2","slugs":["room-two"]}`
	fc.rmu.Unlock()
	p := sweeper(fc, cfg, nil, 30*time.Millisecond)
	defer p.Close()
	p.SyncAsync([]config.Watch{testWatch("printer")})
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool { return fc.has("watchglass/.owners/watchglass") })
	time.Sleep(50 * time.Millisecond)
	if !fc.has("homeassistant/sensor/watchglass-room-two/reading/config") {
		t.Fatal("the other watchglass's device was swept while its record stood")
	}

	if err := fc.Publish("watchglass/.owners/watchglass-room2", 1, true, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	p.Reconnected()
	pollFor(t, 2*time.Second, func() bool {
		return !fc.has("homeassistant/sensor/watchglass-room-two/reading/config")
	})
}
