// Package runner drives one watch: grab -> crop -> evaluate -> record -> notify.
package runner

import (
	"context"
	"fmt"
	"image"
	"time"

	"watchglass/internal/config"
	"watchglass/internal/health"
	"watchglass/internal/history"
	"watchglass/internal/imgproc"
	"watchglass/internal/notify"
	"watchglass/internal/ocr"
	"watchglass/internal/source"
	"watchglass/internal/trigger"
)

// diffTolerance absorbs camera sensor noise in pixel_change watches.
const diffTolerance = 32

type Runner struct {
	watch    config.Watch
	src      source.Source
	engine   ocr.Engine
	notifier notify.Notifier
	store    *history.Store
	eval     *trigger.Evaluator
	prev     *image.RGBA
	logf     func(string, ...any)

	health  *health.Tracker
	baseIvl time.Duration
	maxIvl  time.Duration

	// OnReading, when set, is called once per completed Tick with the trigger
	// outcome and the RAW crop (before preprocessing). The web UI uses it to
	// feed the live readout; keep it fast — it runs on the poll goroutine.
	OnReading func(ev trigger.Event, crop image.Image)
}

func New(w config.Watch, src source.Source, engine ocr.Engine, notifier notify.Notifier,
	store *history.Store, logf func(string, ...any)) (*Runner, error) {
	eval, err := trigger.New(w.Trigger)
	if err != nil {
		return nil, fmt.Errorf("watch %q: %w", w.Name, err)
	}
	if w.Trigger.Type != "pixel_change" && engine == nil {
		return nil, fmt.Errorf("watch %q: trigger %q requires an OCR engine", w.Name, w.Trigger.Type)
	}
	base := time.Duration(w.Interval)
	if base <= 0 {
		logf("watch %s: invalid interval %v, defaulting to %v", w.Name, base, 5*time.Second)
		base = 5 * time.Second
	}
	return &Runner{watch: w, src: src, engine: engine, notifier: notifier,
		store: store, eval: eval, logf: logf,
		health: health.New(w.HealthAfter), baseIvl: base, maxIvl: time.Duration(w.MaxInterval)}, nil
}

// Run polls until ctx is cancelled. Errors are logged, never fatal: a
// watcher that dies on one bad frame is worse than no watcher.
func (r *Runner) Run(ctx context.Context) {
	interval := r.baseIvl
	var lastReading string
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		ev, err := r.Tick(ctx)
		if err != nil {
			r.logf("watch %s: %v", r.watch.Name, err)
		}
		changed := tickChanged(r.watch.Trigger.Type, ev, err, lastReading)
		lastReading = ev.Reading
		interval = NextInterval(r.baseIvl, r.maxIvl, interval, changed)

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(interval)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

// Tick performs one poll cycle and returns the trigger event it produced
// (the zero Event when the poll produced no evaluation, e.g. the pixel
// baseline frame). Exported so tests can drive it deterministically.
func (r *Runner) Tick(ctx context.Context) (trigger.Event, error) {
	img, err := r.src.Grab(ctx)
	if err != nil {
		if hev, changed := r.health.Failure(err); changed {
			r.notifyHealth(ctx, hev)
		}
		return trigger.Event{}, fmt.Errorf("grab: %w", err)
	}
	if hev, changed := r.health.Success(); changed {
		r.notifyHealth(ctx, hev)
	}
	crop := imgproc.Crop(img, r.watch.Region)

	var ev trigger.Event
	if r.watch.Trigger.Type == "pixel_change" {
		if r.prev == nil {
			r.prev = crop
			return trigger.Event{}, nil // first frame is the baseline
		}
		pct := imgproc.PercentChanged(r.prev, crop, diffTolerance)
		r.prev = crop
		ev = r.eval.ObservePixel(pct)
	} else {
		prepped := imgproc.Apply(crop, r.watch.Preprocess)
		text, err := r.engine.Recognize(ctx, prepped)
		if err != nil {
			return trigger.Event{}, fmt.Errorf("ocr: %w", err)
		}
		ev = r.eval.ObserveText(text)
	}

	if r.store != nil {
		if err := r.store.Record(r.watch.Name, time.Now(), ev.Reading, ev.Fired); err != nil {
			r.logf("watch %s: history: %v", r.watch.Name, err)
		}
	}
	if r.OnReading != nil {
		r.OnReading(ev, crop)
	}
	if ev.Fired && r.notifier != nil {
		title := fmt.Sprintf("watchglass: %s", r.watch.Name)
		body := fmt.Sprintf("%s — %s", ev.Reason, ev.Reading)
		if err := r.notifier.Send(ctx, title, body); err != nil {
			return ev, fmt.Errorf("notify: %w", err)
		}
	}
	return ev, nil
}

// notifyHealth reports a stream up/down transition. Failures to notify are
// logged, never fatal — the watch keeps polling regardless.
func (r *Runner) notifyHealth(ctx context.Context, hev health.Event) {
	r.logf("watch %s: %s", r.watch.Name, hev.Message)
	if r.notifier == nil {
		return
	}
	title := fmt.Sprintf("watchglass: %s (%s)", r.watch.Name, hev.State)
	if err := r.notifier.Send(ctx, title, hev.Message); err != nil {
		r.logf("watch %s: notify health: %v", r.watch.Name, err)
	}
}

// NextInterval computes the next poll gap. With max unset (or not above
// base) polling is fixed at base. Otherwise the gap doubles while nothing
// changes, capped at max, and snaps back to base the moment something does.
//
// Note that backing off stretches confirm-N semantics in wall-clock time: a
// watch needing 3 consecutive readings takes proportionally longer to
// confirm while backed off. That is the intended trade and it is why
// adaptive polling is opt-in per watch.
func NextInterval(base, max, current time.Duration, changed bool) time.Duration {
	if changed || max <= base {
		return base
	}
	if current < base {
		current = base
	}
	next := current * 2
	if next > max {
		next = max
	}
	return next
}

// tickChanged decides whether a completed Tick should reset polling to its
// base interval: any grab/OCR error, a fired trigger, or (for non-pixel
// triggers) a reading that differs from the previous one.
//
// A failed tick always counts as "changed" regardless of trigger type. This
// is deliberate: for pixel_change watches, ev is the zero Event on error (no
// Reading to compare), so without this an ffmpeg/HTTP grab failure would
// never interrupt the backoff and a dying camera backed off at max_interval
// would only be health-detected at multiples of max_interval instead of
// health_after×interval. Resetting to base on every failure keeps both
// detection and recovery prompt. This is cheap: a grab against a dead
// endpoint fails fast (ffmpeg's own timeout, or an HTTP dial/read error) and
// spawns nothing persistent, so base-rate polling while a stream is down
// costs no more than base-rate polling while it's healthy.
func tickChanged(triggerType string, ev trigger.Event, err error, lastReading string) bool {
	if err != nil {
		return true
	}
	if ev.Fired {
		return true
	}
	return triggerType != "pixel_change" && ev.Reading != lastReading
}
