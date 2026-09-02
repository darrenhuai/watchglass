package hass

import (
	"encoding/json"
	"fmt"

	"watchglass/internal/config"
)

// Publisher owns everything watchglass says over MQTT: retained Home
// Assistant discovery configs synced to the watch list, and per-event state.
type Publisher struct {
	c    Client
	cfg  config.MQTT
	logf func(string, ...any)

	// slugs maps watch name -> slug for every watch the last Sync accepted.
	slugs map[string]string
	// skipped holds watch names dropped by slug collision; event methods
	// must ignore them so two colliding watches never interleave state.
	skipped map[string]bool
}

func NewPublisher(c Client, cfg config.MQTT, logf func(string, ...any)) *Publisher {
	return &Publisher{c: c, cfg: cfg, logf: logf, slugs: map[string]string{}, skipped: map[string]bool{}}
}

func (p *Publisher) Close() { p.c.Close() }

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
	for name, slug := range p.slugs {
		if still, ok := nextSlugs[name]; ok && still == slug {
			continue
		}
		if claimed[slug] {
			continue
		}
		for _, c := range components {
			p.publish(p.discoveryTopic(c.component, slug, c.object), true, nil)
		}
	}
	p.slugs = nextSlugs
	p.skipped = nextSkipped
}

// publish is fire-and-forget: MQTT failures are logged, never propagated.
func (p *Publisher) publish(topic string, retain bool, payload []byte) {
	if err := p.c.Publish(topic, 1, retain, payload); err != nil {
		p.logf("mqtt: publish %s: %v", topic, err)
	}
}
