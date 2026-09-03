package hass

import (
	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// OnEvent publishes one tick's outcome: the reading always (retained, so a
// restarted HA picks up the last value), and on a fire additionally the
// motion pulse and the retained snapshot crop. Called from the watch's own
// poll goroutine, it only resolves the slug and hands the whole batch to
// the background worker as one job — see enqueue — so the actual network
// I/O never runs on, or blocks, the caller. The batch is one closure
// specifically so the worker executes reading, then motion, then snapshot
// in order for this event, never interleaved with another event's publishes
// for the same watch.
func (p *Publisher) OnEvent(watch string, ev trigger.Event, png []byte) {
	p.mu.RLock()
	slug, ok := p.slugs[watch]
	p.mu.RUnlock()
	if !ok {
		return // unknown to the last Sync, or slug-collision skipped
	}
	base := p.cfg.BaseTopic + "/" + slug
	reading := []byte(ev.Reading)
	p.enqueue(watch, func() {
		p.publish(base+"/reading", true, reading)
		if !ev.Fired {
			return
		}
		p.publish(base+"/motion", false, []byte("ON"))
		if len(png) > 0 {
			p.publish(base+"/snapshot", true, png)
		}
	})
}

// OnHealth publishes stream up/down transitions on the health topic. Like
// OnEvent, it only resolves the slug on the caller's goroutine and queues
// the actual publish for the background worker.
func (p *Publisher) OnHealth(watch string, hev health.Event) {
	p.mu.RLock()
	slug, ok := p.slugs[watch]
	p.mu.RUnlock()
	if !ok {
		return
	}
	payload := "online"
	if hev.State == "down" {
		payload = "offline"
	}
	topic := p.cfg.BaseTopic + "/" + slug + "/health"
	p.enqueue(watch, func() {
		p.publish(topic, true, []byte(payload))
	})
}
