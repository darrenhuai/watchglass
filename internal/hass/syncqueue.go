package hass

import "github.com/darrenhuai/watchglass/internal/config"

// syncWorker is the single goroutine that ever calls Sync after startup, and
// the only goroutine that ever calls Publish on the underlying MQTT client:
// every publish — a full discovery resync from SyncAsync, or one event's
// publish batch from enqueue — funnels through this one loop, so nothing a
// watch's own poll goroutine does ever blocks on network I/O, and Sync's
// writes to p.slugs/p.skipped can never interleave with each other. It runs
// until quit is closed, then drains whatever is left (see drainRemaining)
// and signals done — see Close, which waits on done rather than closing the
// underlying client out from under a job this worker is still running.
func (p *Publisher) syncWorker() {
	defer close(p.done)
	for {
		select {
		case w := <-p.syncCh:
			p.Sync(w)
		case job := <-p.jobs:
			job()
		case <-p.quit:
			p.drainRemaining()
			return
		}
	}
}

// drainRemaining runs on the worker goroutine right after quit fires, before
// the worker returns. It exists because the select above treats jobs/syncCh/
// quit as equally ready: the moment Close closes quit, that select could
// just as easily pick the quit case over a job enqueued a moment earlier
// (Go picks pseudo-randomly among ready cases), which would otherwise drop
// that watch's last publish — breaking the promise (see
// cmd/watchglass/main.go) that watches drain their last publishes before
// MQTT announces offline. So instead of returning immediately, the worker
// makes one more pass: non-blocking, alternating a job and a pending sync,
// until both are empty. Everything here still runs on this one goroutine,
// so it never races or double-runs against the worker's own normal loop
// above — there's exactly one reader of jobs/syncCh for the Publisher's
// entire lifetime.
func (p *Publisher) drainRemaining() {
	for {
		drainedSync := false
		select {
		case w := <-p.syncCh:
			p.Sync(w)
			drainedSync = true
		default:
		}
		drainedJob := false
		select {
		case job := <-p.jobs:
			job()
			drainedJob = true
		default:
		}
		if !drainedSync && !drainedJob {
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
