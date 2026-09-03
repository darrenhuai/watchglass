package hass

import "watchglass/internal/config"

// syncWorker is the single goroutine that ever calls Sync after startup: it
// serializes discovery-config publishes so two overlapping SyncAsync callers
// (e.g. a startup Sync racing a web-triggered config save) can never
// interleave writes to p.slugs/p.skipped. It exits when quit is closed.
func (p *Publisher) syncWorker() {
	for {
		select {
		case w := <-p.syncCh:
			p.Sync(w)
		case <-p.quit:
			return
		}
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
