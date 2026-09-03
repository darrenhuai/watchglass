package hass

import "watchglass/internal/config"

// syncWorker is the single goroutine that ever calls Sync after startup, and
// the only goroutine that ever calls Publish on the underlying MQTT client:
// every publish — a full discovery resync from SyncAsync, or one event's
// publish batch from enqueue — funnels through this one loop, so nothing a
// watch's own poll goroutine does ever blocks on network I/O, and Sync's
// writes to p.slugs/p.skipped can never interleave with each other. It
// exits when quit is closed.
func (p *Publisher) syncWorker() {
	for {
		select {
		case w := <-p.syncCh:
			p.Sync(w)
		case job := <-p.jobs:
			job()
		case <-p.quit:
			return
		}
	}
}

// enqueue queues one publish-batch job for the background worker and
// returns immediately — it never blocks the caller, even when the worker
// is stuck mid-Sync or mid-publish against a dead broker. When the queue
// is already full (32 outstanding jobs), the new job is dropped and
// logged rather than made to wait: see the jobs field doc on Publisher for
// why a drop is safe.
func (p *Publisher) enqueue(watch string, job func()) {
	select {
	case p.jobs <- job:
	default:
		p.logf("mqtt: publish queue full, dropping event for %s", watch)
	}
}

// SyncAsync queues watches for the background worker to Sync and returns
// immediately — it never blocks, even if the broker is unreachable and a
// prior Sync is stuck inside a slow Publish. Only the most recently queued
// watch list survives: SyncAsync drops a still-pending (not yet picked up
// by the worker) entry in favor of the new one, so a burst of config saves
// collapses to one Sync of the final state rather than queuing up stale
// ones behind it.
func (p *Publisher) SyncAsync(watches []config.Watch) {
	for {
		select {
		case p.syncCh <- watches:
			return
		default:
			select {
			case <-p.syncCh:
			default:
			}
		}
	}
}
