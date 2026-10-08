package hass

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
)

// Publisher owns everything watchglass says over MQTT: retained Home
// Assistant discovery configs synced to the watch list, and per-watch
// state.
//
// State topics are kept as desired state: every retained topic's latest
// payload (reading, value, health, snapshot) is remembered per watch, and
// only a change is published. A reading that holds therefore costs nothing
// on the broker or in HA's recorder, and on every (re)connect the whole
// desired state is published again (resync), so a broker that restarted
// without persistence, or state that changed while it was away, is healed
// at once instead of on the next change.
type Publisher struct {
	c    Client
	cfg  config.MQTT
	logf func(string, ...any)

	// mu guards slugs/skipped. Sync runs only on the background worker
	// (see syncqueue.go) and writes both maps under Lock; OnEvent/OnHealth
	// run on each watch's own poll goroutine and resolve a watch's slug
	// under RLock. The worker, as the only writer, reads them unlocked.
	mu      sync.RWMutex
	slugs   map[string]string
	skipped map[string]bool
	// healthKnown says a slug has a health state in want, so OnEvent can
	// tell without a job whether a reading still has to seed it. The
	// worker writes it (under Lock) wherever want's health changes.
	healthKnown map[string]bool

	// Worker-owned: only ever touched on the worker goroutine (Sync, the
	// queued jobs and resync), or by a test calling Sync directly with no
	// worker traffic in flight.
	//
	// watches is the list the last Sync saw, which resync republishes.
	// want is each slug's desired retained state, by topic suffix; sent is
	// what the broker has been sent since the last connect. valueCfg says
	// whether a slug's value entity config is published (true) or cleared
	// (false); a slug not in it may have one left from an earlier run.
	// pendingClear holds vanished slugs whose topics couldn't be cleared
	// while the broker was away. listed says Sync has been given a watch
	// list (at startup the first connect can come before it), so the stray
	// sweep knows which slugs are current. swept holds the slugs the sweep
	// has cleared since the last connect: paho hands over the retained
	// configs on several goroutines, so one slug's can still be arriving
	// after its clear, and it must not be cleared (and logged) twice.
	watches      []config.Watch
	want         map[string]map[string][]byte
	sent         map[string]map[string][]byte
	valueCfg     map[string]bool
	pendingClear map[string]bool
	listed       bool
	swept        map[string]bool
	// lastCfg is each slug's discovery configs (topic to JSON) as last
	// built, kept until the slug is cleared, so a clear Home Assistant may
	// have missed can be replayed with the config it needs (see cleared).
	lastCfg map[string]map[string][]byte
	// cleared are the slugs this run cleared while it couldn't tell that
	// Home Assistant was listening, with their last configs. HA only drops
	// an entity when it receives the empty config, an empty retained
	// message leaves nothing on the broker for it to find later, and an HA
	// that restarted ignores an empty config for an entity it hasn't seen
	// a config for since. So each time HA announces itself (haCh,
	// replayClears) every one gets its config again, not retained, and then
	// the empty one: HA discovers the entity it restored and removes it.
	cleared map[string]map[string][]byte
	// haUp says Home Assistant's birth message arrived on this connection
	// and no "offline" since, so a clear sent now reaches it.
	haUp bool

	// Owner records: each watchglass keeps a retained list of the slugs it
	// publishes on <base_topic>/.owners/<client_id>. The sweep leaves alone
	// every slug another client's record lists, so two watchglass sharing
	// a base_topic don't remove each other's devices. followed says this
	// connection's subscriptions were made; sweepOK that both the configs
	// and the records can be read; nothing is swept or recorded before
	// sweepAt, which gives the broker's retained records time to arrive
	// (paho delivers them on separate goroutines, in no fixed order with
	// the configs). ownersSent is the record sent on this connection.
	followed    bool
	sweepOK     bool
	sweepAt     time.Time
	sweepSettle time.Duration
	ownersSent  []byte
	warned      map[string]bool
	dupWarned   bool
	// run tells this process's owner record from one an earlier run, or
	// another process with the same client_id, wrote.
	run string

	// strays are slugs the broker holds retained discovery configs for,
	// published by this watchglass (same status topic), with those
	// configs, seen since the last sweep; strayCh tells the worker there
	// are some. They are filled on paho's goroutines (onRetainedConfig)
	// and emptied by the worker (sweepStrays), which clears every one no
	// watch claims and no other watchglass owns: the devices of watches
	// renamed or removed while watchglass wasn't running, which nothing
	// else would ever remove from Home Assistant. owners are the other
	// clients' records by topic key; ownSent says this process has
	// published its own record, and dupClient that another process with
	// the same client_id overwrote it since.
	strayMu   sync.Mutex
	strays    map[string]map[string][]byte
	owners    map[string]ownerRecord
	ownSent   bool
	dupClient bool
	strayCh   chan struct{}

	// haState is the last status Home Assistant announced ("online" or
	// "offline"; empty until it says one), for Status. haCh tells the
	// worker a live one arrived; buffered to 1.
	haMu    sync.Mutex
	haState string
	haCh    chan struct{}

	// syncCh feeds the background worker a full discovery resync (see
	// SyncAsync); buffered to 1 so only the latest pending resync survives
	// a caller that never blocks.
	syncCh chan []config.Watch
	// connCh tells the worker the broker (re)connected (Reconnected);
	// buffered to 1, so a burst of connects is one resync.
	connCh chan struct{}
	// jobs feeds the background worker one publish batch per event (see
	// enqueue, syncqueue.go); buffered to 32 as a hard ceiling on
	// outstanding publishes — and the PNGs some of them carry — when the
	// broker is slow. Past that, enqueue drops the newest job rather than
	// block the poll goroutine that called OnEvent/OnHealth. Every settled
	// reading carries the watch's whole current reading and value, so a
	// drop just means HA gets them a reading later.
	jobs chan func()
	quit chan struct{}
	// done is closed by syncWorker right before it returns, once it has
	// drained whatever was left in jobs/syncCh after quit fired (see
	// drainRemaining, syncqueue.go). Close waits on it, bounded by
	// closeDrainBudget, instead of assuming the worker is finished the
	// instant quit is closed.
	done chan struct{}
}

// NewPublisher starts a publisher on an existing client. Start is the
// one-call version that also connects.
func NewPublisher(c Client, cfg config.MQTT, logf func(string, ...any)) *Publisher {
	p := newPublisher(cfg, logf)
	p.c = c
	go p.syncWorker()
	return p
}

func newPublisher(cfg config.MQTT, logf func(string, ...any)) *Publisher {
	return &Publisher{
		cfg: cfg, logf: logf,
		slugs: map[string]string{}, skipped: map[string]bool{}, healthKnown: map[string]bool{},
		want: map[string]map[string][]byte{}, sent: map[string]map[string][]byte{},
		valueCfg: map[string]bool{}, pendingClear: map[string]bool{}, swept: map[string]bool{},
		lastCfg: map[string]map[string][]byte{}, cleared: map[string]map[string][]byte{},
		sweepSettle: straySettle, warned: map[string]bool{}, run: newRunID(),
		strays:  map[string]map[string][]byte{},
		owners:  map[string]ownerRecord{},
		strayCh: make(chan struct{}, 1),
		haCh:    make(chan struct{}, 1),
		syncCh:  make(chan []config.Watch, 1),
		connCh:  make(chan struct{}, 1),
		jobs:    make(chan func(), 32),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Start connects to the broker in the background and returns the
// publisher at once; a dead broker never blocks watchglass. Every
// established connection, the first and each reconnect, triggers a resync
// of discovery and state. The publisher exists before the connection is
// started, so a broker that answers at once can't connect before there is
// anyone to tell: the old wiring created the publisher after Connect, and
// with a broker on the same host the first sync was skipped, so nothing
// reached HA until a config save.
func Start(cfg config.MQTT, logf func(string, ...any)) (*Publisher, error) {
	p := newPublisher(cfg, logf)
	c, err := Connect(cfg, logf, p.Reconnected)
	if err != nil {
		return nil, err
	}
	p.c = c
	go p.syncWorker()
	return p, nil
}

// Reconnected tells the worker the broker connection is (again) up. It
// never blocks: it runs on paho's callback goroutine.
func (p *Publisher) Reconnected() {
	select {
	case p.connCh <- struct{}{}:
	default:
	}
}

// Status is the connection state for the web UI (see Client.Status).
// With the broker connected it is StateHAOffline once Home Assistant has
// said "offline" on its status topic and hasn't come back: the broker
// being up says nothing about HA. HA doesn't retain that message, so a
// watchglass started while HA is down says connected until HA's next word.
func (p *Publisher) Status() (string, error) {
	state, err := p.c.Status()
	if state != StateConnected {
		return state, err
	}
	p.haMu.Lock()
	ha := p.haState
	p.haMu.Unlock()
	if ha == "offline" {
		return StateHAOffline, &ConnError{Reason: "it went offline; the MQTT broker is up", Err: errHAOffline}
	}
	return state, err
}

var errHAOffline = errors.New(`Home Assistant sent "offline" on its status topic`)

// closeDrainBudget bounds how long Close waits for the worker to drain
// already-queued jobs and syncs before giving up and shutting down anyway.
const closeDrainBudget = 2 * time.Second

// Close stops the background worker and closes the MQTT client. It is
// called once, at shutdown, so idempotence is not required.
//
// Close closes quit, then waits (up to closeDrainBudget) for the worker to
// confirm — via done — that it has actually stopped, rather than closing
// the underlying client the instant quit is closed. That distinction is the
// fix: quit merely wakes the worker's select in syncWorker, which treats
// jobs/syncCh/quit as equally ready and could just as easily pick the quit
// case over a job enqueued moments earlier (Go picks pseudo-randomly among
// ready cases) — so on quit the worker doesn't return immediately, it first
// drains whatever is left (drainRemaining, syncqueue.go) and only then
// closes done. Waiting for that signal, instead of assuming it's instant,
// is what actually keeps the promise (see cmd/watchglass/main.go) that
// watches drain their last publishes before MQTT announces offline — the
// job and the sync stay on the single worker goroutine throughout, so
// there's never a second goroutine racing it for the same channels.
//
// If the worker is stuck (e.g. a job retrying against a dead broker),
// closeDrainBudget still bounds how long Close itself waits: past that, it
// gives up on the worker and closes the client anyway rather than hang
// shutdown forever. The worker goroutine may keep running in the
// background after that — acceptable at process shutdown.
func (p *Publisher) Close() {
	close(p.quit)
	select {
	case <-p.done:
	case <-time.After(closeDrainBudget):
	}
	p.c.Close()
}

type discoveryEntity struct {
	component string
	object    string
	payload   map[string]any
}

// entities are a watch's discovery configs. HA names each entity after its
// device plus the entity's own name, so the entity names are just the
// part ("Reading"): "watchglass printer Reading", not the old doubled
// "watchglass printer printer reading". Entity IDs come from unique_id and
// don't change. A numeric watch also gets its number as a sensor HA can
// graph (value): state_class measurement, with the watch's unit and device
// class when it has them.
func (p *Publisher) entities(w config.Watch, slug string) []discoveryEntity {
	base := p.cfg.BaseTopic + "/" + slug
	device := map[string]any{
		"identifiers":  []string{"watchglass_" + slug},
		"name":         "watchglass " + w.Name,
		"manufacturer": "watchglass",
	}
	common := func(object, name string) map[string]any {
		return map[string]any{
			"name":               name,
			"unique_id":          fmt.Sprintf("watchglass_%s_%s", slug, object),
			"availability_topic": p.cfg.BaseTopic + "/status",
			"device":             device,
		}
	}
	reading := common("reading", "Reading")
	reading["state_topic"] = base + "/reading"

	healthCfg := common("health", "Health")
	healthCfg["state_topic"] = base + "/health"
	healthCfg["device_class"] = "connectivity"
	healthCfg["payload_on"] = "online"
	healthCfg["payload_off"] = "offline"

	motion := common("motion", "Motion")
	motion["state_topic"] = base + "/motion"
	motion["device_class"] = "motion"
	motion["payload_on"] = "ON"
	motion["off_delay"] = 30

	camera := common("snapshot", "Snapshot")
	camera["topic"] = base + "/snapshot"

	out := []discoveryEntity{
		{"sensor", "reading", reading},
		{"binary_sensor", "health", healthCfg},
		{"binary_sensor", "motion", motion},
		{"camera", "snapshot", camera},
	}
	if w.Trigger.Type == "numeric" {
		value := common("value", "Value")
		value["state_topic"] = base + "/value"
		value["state_class"] = "measurement"
		if w.Unit != "" {
			value["unit_of_measurement"] = config.NormalizeUnit(w.Unit)
		}
		if w.DeviceClass != "" {
			value["device_class"] = w.DeviceClass
		}
		out = append(out, discoveryEntity{"sensor", "value", value})
	}
	return out
}

func (p *Publisher) discoveryTopic(component, slug, object string) string {
	return fmt.Sprintf("%s/%s/watchglass-%s/%s/config", p.cfg.DiscoveryPrefix, component, slug, object)
}

// stateTopics are the retained state topics, in the order a resync
// publishes them. Motion is a pulse, not state: it is never retained.
var stateTopics = []string{"reading", "value", "health", "snapshot"}

// allEntities are every discovery config a slug can have, for clearing.
var allEntities = []struct{ component, object string }{
	{"sensor", "reading"}, {"binary_sensor", "health"},
	{"binary_sensor", "motion"}, {"camera", "snapshot"}, {"sensor", "value"},
}

// Sync reconciles retained discovery configs with the given watch list:
// present watches get their entity configs published, vanished watches
// get theirs (and their retained state) cleared. Slug collisions keep the
// first watch and skip later ones loudly. While the broker is away the
// new list is only recorded (slugs still resolve, so state keeps being
// tracked), and the next connect publishes it (resync). Configs an
// earlier run left on the broker for watches that are gone now are cleared
// too, once the broker has shown them (sweepStrays).
func (p *Publisher) Sync(watches []config.Watch) {
	p.listed = true
	p.sync(watches)
	p.sweepStrays()
}

func (p *Publisher) sync(watches []config.Watch) {
	p.watches = watches
	connected := p.c.Connected()
	nextSlugs := map[string]string{}
	nextSkipped := map[string]bool{}
	taken := map[string]string{} // slug -> first owner name
	for _, w := range watches {
		slug := Slug(w.Name)
		if slug == "" {
			p.logf("mqtt: watch %q has no usable slug; skipping", w.Name)
			nextSkipped[w.Name] = true
			continue
		}
		if owner, clash := taken[slug]; clash {
			p.logf("mqtt: watch %q collides with %q on slug %q; skipping", w.Name, owner, slug)
			nextSkipped[w.Name] = true
			continue
		}
		taken[slug] = w.Name
		nextSlugs[w.Name] = slug
		p.lastCfg[slug] = p.configPayloads(w, slug)
		if connected {
			p.publishDiscovery(w, slug)
		}
	}
	// Clear configs for watches that vanished (or lost their slug). A slug
	// still claimed by any current watch must never be cleared, even if the
	// watch *name* that claims it changed (e.g. a same-slug rename, or a
	// collision winner changing identity between syncs) — otherwise the
	// clear loop would wipe out the fresh configs just published above for
	// whichever watch now owns that slug.
	claimed := map[string]bool{}
	for _, slug := range nextSlugs {
		claimed[slug] = true
	}
	// Swap in the new maps now, under lock, so OnEvent/OnHealth on other
	// goroutines never observe a half-updated p.slugs. oldSlugs is kept
	// only as a local snapshot for the clear loop below, which needs the
	// *previous* mapping to know what vanished — it deliberately runs
	// after the swap, outside the lock, since it does slow publish I/O.
	p.mu.Lock()
	oldSlugs := p.slugs
	p.slugs = nextSlugs
	p.skipped = nextSkipped
	p.mu.Unlock()

	for name, slug := range oldSlugs {
		if still, ok := nextSlugs[name]; ok && still == slug {
			continue
		}
		if !claimed[slug] {
			p.pendingClear[slug] = true
			delete(p.want, slug)
			delete(p.sent, slug)
			p.mu.Lock()
			delete(p.healthKnown, slug)
			p.mu.Unlock()
		}
	}
	p.flushPendingClear(claimed, connected)
}

// flushPendingClear clears every pending slug no current watch claims.
// A clear Home Assistant may not have taken (its birth message hasn't come
// on this connection) is remembered with the slug's configs for
// replayClears.
func (p *Publisher) flushPendingClear(claimed map[string]bool, connected bool) {
	for slug := range p.pendingClear {
		if claimed[slug] {
			delete(p.pendingClear, slug) // back again: its configs are fresh
			continue
		}
		if connected && p.clearSlug(slug) {
			delete(p.pendingClear, slug)
			delete(p.valueCfg, slug)
			if p.haUp {
				delete(p.cleared, slug)
			} else {
				p.cleared[slug] = p.lastCfg[slug]
			}
			delete(p.lastCfg, slug)
		}
	}
}

// claimedSlugs are the slugs the current watches publish under.
func (p *Publisher) claimedSlugs() map[string]bool {
	claimed := map[string]bool{}
	for _, slug := range p.slugs {
		claimed[slug] = true
	}
	return claimed
}

// configPayloads are a watch's discovery configs by topic, as
// publishDiscovery sends them.
func (p *Publisher) configPayloads(w config.Watch, slug string) map[string][]byte {
	out := map[string][]byte{}
	for _, e := range p.entities(w, slug) {
		if raw, err := json.Marshal(e.payload); err == nil {
			out[p.discoveryTopic(e.component, slug, e.object)] = raw
		}
	}
	return out
}

// haStatusTopics are where Home Assistant announces itself: "online" each
// time its MQTT integration (re)connects, after it has subscribed to
// discovery, and "offline" when it stops (its last will if it dies). HA's
// default birth topic is homeassistant/status whatever the discovery
// prefix; with another prefix, <prefix>/status is followed too, for an HA
// whose birth topic was moved with it.
func (p *Publisher) haStatusTopics() []string {
	topics := []string{"homeassistant/status"}
	if p.cfg.DiscoveryPrefix != "homeassistant" {
		topics = append(topics, p.cfg.DiscoveryPrefix+"/status")
	}
	return topics
}

// onHAStatus runs on paho's goroutines. It records the status for Status
// and wakes the worker for a live one. A retained one (HA doesn't retain
// its status unless told to) may be stale, say after a broker restart, so
// it never counts as HA being there to receive a clear.
func (p *Publisher) onHAStatus(_ string, payload []byte, retained bool) {
	s := string(payload)
	if s != "online" && s != "offline" {
		return
	}
	p.haMu.Lock()
	p.haState = s
	p.haMu.Unlock()
	if retained {
		return
	}
	select {
	case p.haCh <- struct{}{}:
	default:
	}
}

// haChanged runs on the worker after a live status from Home Assistant.
func (p *Publisher) haChanged() {
	p.haMu.Lock()
	s := p.haState
	p.haMu.Unlock()
	p.haUp = s == "online"
	if p.haUp {
		p.replayClears()
	}
}

// replayClears runs on the worker when Home Assistant comes online: every
// remembered clear that no watch has claimed since is sent again, as the
// entity's config (not retained) followed by the empty one. An HA that
// was only away from the broker drops the device on the empty config
// alone, but one that restarted ignores an empty config for an entity it
// hasn't been sent a config for since it started, and keeps the device
// it restored from its registry for good. The config makes HA take the
// entity up again, and the empty one then removes it. Each clear is
// replayed once; an HA that had already dropped the device just adds and
// removes it again.
func (p *Publisher) replayClears() {
	if len(p.cleared) == 0 || !p.c.Connected() {
		return
	}
	claimed := p.claimedSlugs()
	for slug, cfgs := range p.cleared {
		if claimed[slug] {
			delete(p.cleared, slug)
			continue
		}
		ok := true
		for _, c := range allEntities {
			topic := p.discoveryTopic(c.component, slug, c.object)
			if raw := cfgs[topic]; len(raw) > 0 {
				ok = p.publish(topic, false, raw) && ok
			}
			ok = p.publish(topic, true, nil) && ok
		}
		if ok {
			delete(p.cleared, slug)
		}
	}
}

// strayFilter is the subscription that shows the broker's retained
// discovery configs: every one, since a single-level wildcard can't ask
// for the "watchglass-" node IDs only. onRetainedConfig picks ours out.
func (p *Publisher) strayFilter() string {
	return p.cfg.DiscoveryPrefix + "/+/+/+/config"
}

// onRetainedConfig runs on paho's goroutines for each message on
// strayFilter. Only retained messages count (the broker's copy, sent when
// the subscription is made; a live one is a config being published right
// now, our own included), and only a config written for this base_topic:
// node ID "watchglass-<slug>", a unique_id for that slug and this
// instance's status topic, so a second watchglass with its own base_topic
// sharing the broker is left alone. One sharing the base_topic passes this
// check; its owner record keeps the sweep off its slugs. It only records
// the slug and its config; the worker decides.
func (p *Publisher) onRetainedConfig(topic string, payload []byte, retained bool) {
	if !retained || len(payload) == 0 {
		return
	}
	rest, ok := strings.CutPrefix(topic, p.cfg.DiscoveryPrefix+"/")
	if !ok {
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || parts[3] != "config" {
		return
	}
	slug, ok := strings.CutPrefix(parts[1], "watchglass-")
	if !ok || slug == "" {
		return
	}
	var cfg struct {
		UniqueID          string `json:"unique_id"`
		AvailabilityTopic string `json:"availability_topic"`
	}
	if json.Unmarshal(payload, &cfg) != nil ||
		cfg.AvailabilityTopic != p.cfg.BaseTopic+"/status" ||
		!strings.HasPrefix(cfg.UniqueID, "watchglass_"+slug+"_") {
		return
	}
	p.strayMu.Lock()
	if p.strays[slug] == nil {
		p.strays[slug] = map[string][]byte{}
	}
	p.strays[slug][topic] = bytes.Clone(payload)
	p.strayMu.Unlock()
	p.wakeSweep()
}

// wakeSweep asks the worker to sweep; it never blocks.
func (p *Publisher) wakeSweep() {
	select {
	case p.strayCh <- struct{}{}:
	default:
	}
}

// straySettle is how long after subscribing the sweep waits before it
// clears anything, so every retained owner record has arrived.
const straySettle = 2 * time.Second

// ownersLevel is the topic level under base_topic that holds the owner
// records. A dot can't appear in a slug, so no watch's topics collide.
const ownersLevel = ".owners"

// ownerRecord is a watchglass's retained list of the slugs it publishes.
type ownerRecord struct {
	ClientID string   `json:"client_id"`
	Run      string   `json:"run"`
	Slugs    []string `json:"slugs"`
}

// ownerKey is a client_id made safe for one topic level.
func ownerKey(clientID string) string {
	key := strings.Map(func(r rune) rune {
		if r == '/' || r == '+' || r == '#' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, clientID)
	if key == "" {
		return "_"
	}
	return key
}

func (p *Publisher) ownersTopic() string {
	return p.cfg.BaseTopic + "/" + ownersLevel + "/" + ownerKey(p.cfg.ClientID)
}

func (p *Publisher) ownersFilter() string {
	return p.cfg.BaseTopic + "/" + ownersLevel + "/+"
}

func newRunID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// onOwnerRecord runs on paho's goroutines for each owner record under this
// base_topic. Another client's is kept (an empty one deletes it). This
// client's own is only checked: once this process has published its
// record, one with another run ID means a second process connects with
// the same client_id. The broker then keeps disconnecting one for the
// other, and each would sweep the other's devices on every reconnect.
func (p *Publisher) onOwnerRecord(topic string, payload []byte, _ bool) {
	key, ok := strings.CutPrefix(topic, p.cfg.BaseTopic+"/"+ownersLevel+"/")
	if !ok || key == "" || strings.Contains(key, "/") {
		return
	}
	var rec ownerRecord
	if len(payload) > 0 && json.Unmarshal(payload, &rec) != nil {
		return
	}
	p.strayMu.Lock()
	switch {
	case key == ownerKey(p.cfg.ClientID):
		if len(payload) > 0 && p.ownSent && rec.Run != p.run {
			p.dupClient = true
		}
	case len(payload) == 0:
		delete(p.owners, key)
	default:
		p.owners[key] = rec
	}
	p.strayMu.Unlock()
	p.wakeSweep()
}

// sweepStrays runs on the worker. Every slug the broker showed configs for
// that no current watch claims and no other watchglass's record lists is
// cleared like a watch removed in this run (pendingClear), so HA drops its
// device. A record that lists one of this watchglass's own slugs is its
// own under an old client_id: it is deleted and protects nothing
// (retireOwner). Then this watchglass's own record is published. Nothing happens
// until Sync has had a watch list (at startup the first connect can come
// before it, and clearing then would wipe every watch's device for a
// moment) and the records have had time to arrive (sweepAt).
func (p *Publisher) sweepStrays() {
	if !p.listed || !p.followed || !p.c.Connected() || time.Now().Before(p.sweepAt) {
		return
	}
	p.strayMu.Lock()
	found := p.strays
	p.strays = map[string]map[string][]byte{}
	others := make(map[string]ownerRecord, len(p.owners))
	for k, rec := range p.owners {
		others[k] = rec
	}
	dup := p.dupClient
	p.strayMu.Unlock()

	claimed := p.claimedSlugs()
	elsewhere := map[string]bool{}
	for key, rec := range others {
		if listsAny(rec.Slugs, claimed) {
			p.retireOwner(key, rec)
			continue
		}
		if !p.warned[key] {
			p.warned[key] = true
			p.logf("mqtt: another watchglass (client_id %q) also uses base_topic %q. Its devices are left alone, but when either one stops, Home Assistant shows both as unavailable. Give each watchglass its own base_topic. If that one no longer runs, delete the retained message on %s",
				rec.ClientID, p.cfg.BaseTopic, p.cfg.BaseTopic+"/"+ownersLevel+"/"+key)
		}
		for _, slug := range rec.Slugs {
			elsewhere[slug] = true
		}
	}
	switch {
	case dup:
		if !p.dupWarned {
			p.dupWarned = true
			p.logf("mqtt: another watchglass connects with the same client_id %q, so the broker keeps dropping one of them. Until a restart, this one won't remove any old devices from Home Assistant. Give each watchglass its own client_id and base_topic", p.cfg.ClientID)
		}
	case p.sweepOK:
		for slug, cfgs := range found {
			if claimed[slug] || elsewhere[slug] || p.pendingClear[slug] || p.swept[slug] {
				continue
			}
			p.swept[slug] = true
			p.logf("mqtt: clearing the Home Assistant device watchglass-%s left by an earlier run: no watch has that name now", slug)
			if len(p.lastCfg[slug]) == 0 {
				p.lastCfg[slug] = cfgs
			}
			p.pendingClear[slug] = true
		}
		p.flushPendingClear(claimed, true)
	}
	p.publishOwners(claimed)
}

// publishOwners sends this watchglass's owner record when it changed on
// this connection.
func (p *Publisher) publishOwners(claimed map[string]bool) {
	rec := ownerRecord{ClientID: p.cfg.ClientID, Run: p.run, Slugs: []string{}}
	for slug := range claimed {
		rec.Slugs = append(rec.Slugs, slug)
	}
	sort.Strings(rec.Slugs)
	raw, err := json.Marshal(rec)
	if err != nil || bytes.Equal(raw, p.ownersSent) {
		return
	}
	if p.publish(p.ownersTopic(), true, raw) {
		p.ownersSent = raw
		p.strayMu.Lock()
		p.ownSent = true
		p.strayMu.Unlock()
	}
}

// listsAny says whether slugs holds a slug in claimed.
func listsAny(slugs []string, claimed map[string]bool) bool {
	for _, slug := range slugs {
		if claimed[slug] {
			return true
		}
	}
	return false
}

// retireOwner deletes another client_id's record that lists a slug this
// watchglass publishes. Two watchglass running on one base_topic with the
// same slug write the same topics and fight over one device anyway, so
// such a record is this install's own from before its client_id was
// changed. Kept, it would shield the devices of watches renamed or removed
// since, and its warning would come back on every start. The sweep treats
// its other slugs like any earlier run's.
func (p *Publisher) retireOwner(key string, rec ownerRecord) {
	topic := p.cfg.BaseTopic + "/" + ownersLevel + "/" + key
	p.logf("mqtt: the watch list on %s (client_id %q) has watches this watchglass publishes, so it is taken as this watchglass's own from before its client_id changed, and deleted", topic, rec.ClientID)
	if p.publish(topic, true, nil) {
		// The broker echoes the empty record back (onOwnerRecord), but
		// later, and a sweep before that must not take it up again.
		p.strayMu.Lock()
		delete(p.owners, key)
		p.strayMu.Unlock()
	}
}

// publishDiscovery sends one watch's entity configs. A watch that isn't
// numeric has no value entity, so one left by an earlier type (in this run
// or, once per run, possibly an earlier one) is cleared with its number.
func (p *Publisher) publishDiscovery(w config.Watch, slug string) {
	for _, e := range p.entities(w, slug) {
		raw, err := json.Marshal(e.payload)
		if err != nil {
			p.logf("mqtt: marshal discovery for %q: %v", w.Name, err)
			continue
		}
		if p.publish(p.discoveryTopic(e.component, slug, e.object), true, raw) && e.object == "value" {
			p.valueCfg[slug] = true
		}
	}
	if w.Trigger.Type == "numeric" {
		return
	}
	if published, known := p.valueCfg[slug]; known && !published {
		return
	}
	if p.publish(p.discoveryTopic("sensor", slug, "value"), true, nil) &&
		p.publish(p.cfg.BaseTopic+"/"+slug+"/value", true, nil) {
		p.valueCfg[slug] = false
		delete(p.want[slug], "value")
		delete(p.sent[slug], "value")
	}
}

// clearSlug empties a vanished watch's discovery configs and retained
// state topics; true when every clear was sent.
func (p *Publisher) clearSlug(slug string) bool {
	ok := true
	for _, c := range allEntities {
		ok = p.publish(p.discoveryTopic(c.component, slug, c.object), true, nil) && ok
	}
	// Motion is not retained and must not be cleared.
	for _, st := range stateTopics {
		ok = p.publish(p.cfg.BaseTopic+"/"+slug+"/"+st, true, nil) && ok
	}
	return ok
}

// resync runs on every established connection: the broker may have lost
// its retained messages, and nothing was published while it was away, so
// discovery and every watch's desired state go out again.
//
// It then subscribes to the owner records and the retained discovery
// configs, so ones an earlier run left for watches that are gone now get
// swept (onOwnerRecord, onRetainedConfig, sweepStrays), and to Home
// Assistant's status, so clears HA missed are sent again when it comes
// online (onHAStatus). The subscriptions are made on every connect,
// because a clean session drops them, and each one shows the retained
// messages again. Whether HA is there is unknown until it says so: if the
// broker restarted, HA is reconnecting too.
func (p *Publisher) resync() {
	p.sent = map[string]map[string][]byte{}
	p.haUp = false
	p.sync(p.watches)
	for _, slug := range p.slugs {
		for _, key := range stateTopics {
			if payload, ok := p.want[slug][key]; ok {
				p.setState(slug, key, payload)
			}
		}
	}
	p.swept = map[string]bool{}
	p.ownersSent = nil
	p.followed, p.sweepOK = false, false
	if s, ok := p.c.(subscriber); ok && p.c.Connected() {
		p.followed = true
		errOwners := s.Subscribe(p.ownersFilter(), p.onOwnerRecord)
		errConfigs := s.Subscribe(p.strayFilter(), p.onRetainedConfig)
		if err := errors.Join(errOwners, errConfigs); err != nil {
			p.logf("mqtt: can't read back discovery configs, so devices of watches removed while watchglass was stopped stay in Home Assistant: %v", err)
		}
		p.sweepOK = errOwners == nil && errConfigs == nil
		for _, topic := range p.haStatusTopics() {
			if err := s.Subscribe(topic, p.onHAStatus); err != nil {
				p.logf("mqtt: can't follow Home Assistant's status on %s: %v", topic, err)
			}
		}
		p.sweepAt = time.Now().Add(p.sweepSettle)
		time.AfterFunc(p.sweepSettle, p.wakeSweep)
	}
	p.sweepStrays()
}

// setState records payload as slug's desired retained state for key and
// publishes it unless the broker already has exactly that.
func (p *Publisher) setState(slug, key string, payload []byte) {
	if p.want[slug] == nil {
		p.want[slug] = map[string][]byte{}
	}
	if _, had := p.want[slug][key]; !had && key == "health" {
		p.mu.Lock()
		p.healthKnown[slug] = true
		p.mu.Unlock()
	}
	p.want[slug][key] = payload
	if sent, ok := p.sent[slug][key]; ok && bytes.Equal(sent, payload) {
		return
	}
	if p.publish(p.cfg.BaseTopic+"/"+slug+"/"+key, true, payload) {
		if p.sent[slug] == nil {
			p.sent[slug] = map[string][]byte{}
		}
		p.sent[slug][key] = payload
	}
}

// publish is fire-and-forget: MQTT failures are logged, never propagated.
// It reports whether the broker took the message. Nothing is tried while
// the broker is away: the next connect republishes what matters (resync).
func (p *Publisher) publish(topic string, retain bool, payload []byte) bool {
	if !p.c.Connected() {
		return false
	}
	if err := p.c.Publish(topic, 1, retain, payload); err != nil {
		p.logf("mqtt: publish %s: %v", topic, err)
		return false
	}
	return true
}
