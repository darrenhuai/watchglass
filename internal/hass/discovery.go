package hass

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"watchglass/internal/config"
)

// Publisher owns everything watchglass says over MQTT: retained Home
// Assistant discovery configs synced to the watch list, and per-event state.
type Publisher struct {
	c    Client
	cfg  config.MQTT
	logf func(string, ...any)

	// mu guards slugs/skipped. Sync runs only on the background worker
	// (see syncqueue.go) and writes both maps under Lock; OnEvent/OnHealth
	// run on each watch's own poll goroutine and resolve a watch's slug
	// under RLock. Every access to either map must go through mu — there
	// is no other synchronization between the worker goroutine and the
	// poll goroutines.
	mu      sync.RWMutex
	slugs   map[string]string
	skipped map[string]bool

	// syncCh feeds the background worker a full discovery resync (see
	// SyncAsync); buffered to 1 so only the latest pending resync survives
	// a caller that never blocks.
	syncCh chan []config.Watch
	// jobs feeds the background worker one publish batch per event (see
	// enqueue, syncqueue.go); buffered to 32 as a hard ceiling on
	// outstanding publishes — and the PNGs some of them carry — when the
	// broker is slow or down. Past that, enqueue drops the newest job
	// rather than block the poll goroutine that called OnEvent/OnHealth;
	// state topics are retained and republished next tick, so a drop just
	// means HA shows last tick's value a little longer.
	jobs chan func()
	quit chan struct{}
	// done is closed by syncWorker right before it returns, once it has
	// drained whatever was left in jobs/syncCh after quit fired (see
	// drainRemaining, syncqueue.go). Close waits on it, bounded by
	// closeDrainBudget, instead of assuming the worker is finished the
	// instant quit is closed.
	done chan struct{}
}

func NewPublisher(c Client, cfg config.MQTT, logf func(string, ...any)) *Publisher {
	p := &Publisher{
		c: c, cfg: cfg, logf: logf,
		slugs: map[string]string{}, skipped: map[string]bool{},
		syncCh: make(chan []config.Watch, 1),
		jobs:   make(chan func(), 32),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go p.syncWorker()
	return p
}

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

func (p *Publisher) entities(w config.Watch, slug string) []discoveryEntity {
	base := p.cfg.BaseTopic + "/" + slug
	device := map[string]any{
		"identifiers":  []string{"watchglass_" + slug},
		"name":         "watchglass " + w.Name,
		"manufacturer": "watchglass",
	}
	common := func(object string) map[string]any {
		return map[string]any{
			"name":               w.Name + " " + object,
			"unique_id":          fmt.Sprintf("watchglass_%s_%s", slug, object),
			"availability_topic": p.cfg.BaseTopic + "/status",
			"device":             device,
		}
	}
	reading := common("reading")
	reading["state_topic"] = base + "/reading"

	healthCfg := common("health")
	healthCfg["state_topic"] = base + "/health"
	healthCfg["device_class"] = "connectivity"
	healthCfg["payload_on"] = "online"
	healthCfg["payload_off"] = "offline"

	motion := common("motion")
	motion["state_topic"] = base + "/motion"
	motion["device_class"] = "motion"
	motion["payload_on"] = "ON"
	motion["off_delay"] = 30

	camera := common("snapshot")
	camera["topic"] = base + "/snapshot"

	return []discoveryEntity{
		{"sensor", "reading", reading},
		{"binary_sensor", "health", healthCfg},
		{"binary_sensor", "motion", motion},
		{"camera", "snapshot", camera},
	}
}

func (p *Publisher) discoveryTopic(component, slug, object string) string {
	return fmt.Sprintf("%s/%s/watchglass-%s/%s/config", p.cfg.DiscoveryPrefix, component, slug, object)
}

// Sync reconciles retained discovery configs with the given watch list:
// present watches get their four entity configs published, vanished watches
// get theirs cleared. Slug collisions keep the first watch and skip later
// ones loudly.
func (p *Publisher) Sync(watches []config.Watch) {
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
		for _, e := range p.entities(w, slug) {
			raw, err := json.Marshal(e.payload)
			if err != nil {
				p.logf("mqtt: marshal discovery for %q: %v", w.Name, err)
				continue
			}
			p.publish(p.discoveryTopic(e.component, slug, e.object), true, raw)
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
	components := []struct{ component, object string }{
		{"sensor", "reading"}, {"binary_sensor", "health"},
		{"binary_sensor", "motion"}, {"camera", "snapshot"},
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
		if claimed[slug] {
			continue
		}
		for _, c := range components {
			p.publish(p.discoveryTopic(c.component, slug, c.object), true, nil)
		}
		// Clear retained state topics for the removed watch. Motion is not
		// retained and must not be cleared.
		for _, st := range []string{"reading", "health", "snapshot"} {
			p.publish(p.cfg.BaseTopic+"/"+slug+"/"+st, true, nil)
		}
	}
}

// publish is fire-and-forget: MQTT failures are logged, never propagated.
func (p *Publisher) publish(topic string, retain bool, payload []byte) {
	if err := p.c.Publish(topic, 1, retain, payload); err != nil {
		p.logf("mqtt: publish %s: %v", topic, err)
	}
}
