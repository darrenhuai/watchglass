package hass

import (
	"strconv"

	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// OnEvent publishes what one tick changed. The reading entity gets the
// settled reading (trigger.Event.Settled: the same text for Confirm
// readings in a row), never a raw frame, so OCR noise doesn't flip it
// every tick; a numeric watch's value sensor gets its filtered number
// (Event.Value). Both are retained and only published when they change
// (setState). A fire adds the motion pulse and the retained snapshot crop.
// A reading also means the stream is up: the first one seeds the health
// topic "online", since the stream's health only reports changes (down,
// and up again) and HA's connectivity sensor would otherwise stay unknown
// until the first of those.
//
// Called from the watch's own poll goroutine, it only resolves the slug and
// hands the whole batch to the background worker as one job — see enqueue
// — so the network I/O and the bookkeeping never run on, or block, the
// caller. The batch is one closure specifically so the worker executes
// reading, value, motion, then snapshot in order for this event, never
// interleaved with another event's publishes for the same watch.
func (p *Publisher) OnEvent(watch string, ev trigger.Event, png []byte) {
	p.mu.RLock()
	slug, ok := p.slugs[watch]
	healthKnown := p.healthKnown[slug]
	p.mu.RUnlock()
	if !ok {
		return // unknown to the last Sync, or slug-collision skipped
	}
	if healthKnown && !ev.HasSettled && !ev.HasValue && !ev.Fired {
		return // nothing HA would see changed
	}
	p.enqueue(watch, func() {
		// A Sync between the enqueue and now may have renamed or removed
		// the watch; its old slug's topics were cleared and must stay so.
		if p.slugs[watch] != slug {
			return
		}
		if _, ok := p.want[slug]["health"]; !ok {
			p.setState(slug, "health", []byte("online"))
		}
		if ev.HasSettled {
			p.setState(slug, "reading", []byte(ev.Settled))
		}
		if ev.HasValue {
			p.setState(slug, "value", []byte(strconv.FormatFloat(ev.Value, 'f', -1, 64)))
		}
		if !ev.Fired {
			return
		}
		p.publish(p.cfg.BaseTopic+"/"+slug+"/motion", false, []byte("ON"))
		if len(png) > 0 {
			p.setState(slug, "snapshot", png)
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
	p.enqueue(watch, func() {
		if p.slugs[watch] != slug {
			return
		}
		p.setState(slug, "health", []byte(payload))
	})
}
