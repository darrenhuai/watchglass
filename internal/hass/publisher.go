package hass

import (
	"watchglass/internal/health"
	"watchglass/internal/trigger"
)

// OnEvent publishes one tick's outcome: the reading always (retained, so a
// restarted HA picks up the last value), and on a fire additionally the
// motion pulse and the retained snapshot crop.
func (p *Publisher) OnEvent(watch string, ev trigger.Event, png []byte) {
	slug, ok := p.slugs[watch]
	if !ok {
		return // unknown to the last Sync, or slug-collision skipped
	}
	base := p.cfg.BaseTopic + "/" + slug
	p.publish(base+"/reading", true, []byte(ev.Reading))
	if !ev.Fired {
		return
	}
	p.publish(base+"/motion", false, []byte("ON"))
	if len(png) > 0 {
		p.publish(base+"/snapshot", true, png)
	}
}

// OnHealth publishes stream up/down transitions on the health topic.
func (p *Publisher) OnHealth(watch string, hev health.Event) {
	slug, ok := p.slugs[watch]
	if !ok {
		return
	}
	payload := "online"
	if hev.State == "down" {
		payload = "offline"
	}
	p.publish(p.cfg.BaseTopic+"/"+slug+"/health", true, []byte(payload))
}
