package hass

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	// while the broker was away.
	watches      []config.Watch
	want         map[string]map[string][]byte
	sent         map[string]map[string][]byte
	valueCfg     map[string]bool
	pendingClear map[string]bool

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
		valueCfg: map[string]bool{}, pendingClear: map[string]bool{},
		syncCh: make(chan []config.Watch, 1),
		connCh: make(chan struct{}, 1),
		jobs:   make(chan func(), 32),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
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

// Status is the broker connection state, for the web UI (see
// Client.Status).
func (p *Publisher) Status() (string, error) { return p.c.Status() }

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
// tracked), and the next connect publishes it (resync).
func (p *Publisher) Sync(watches []config.Watch) {
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
	for slug := range p.pendingClear {
		if claimed[slug] {
			delete(p.pendingClear, slug) // back again: its configs are fresh
			continue
		}
		if connected && p.clearSlug(slug) {
			delete(p.pendingClear, slug)
			delete(p.valueCfg, slug)
		}
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
func (p *Publisher) resync() {
	p.sent = map[string]map[string][]byte{}
	p.Sync(p.watches)
	for _, slug := range p.slugs {
		for _, key := range stateTopics {
			if payload, ok := p.want[slug][key]; ok {
				p.setState(slug, key, payload)
			}
		}
	}
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
