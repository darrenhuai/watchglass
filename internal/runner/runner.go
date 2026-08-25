// Package runner drives one watch: grab -> crop -> evaluate -> record -> notify.
package runner

import (
	"context"
	"fmt"
	"image"
	"time"

	"watchglass/internal/config"
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
	return &Runner{watch: w, src: src, engine: engine, notifier: notifier,
		store: store, eval: eval, logf: logf}, nil
}

// Run polls until ctx is cancelled. Errors are logged, never fatal:
// a watcher that dies on one bad frame is worse than no watcher.
func (r *Runner) Run(ctx context.Context) {
	interval := time.Duration(r.watch.Interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.Tick(ctx); err != nil {
			r.logf("watch %s: %v", r.watch.Name, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick performs one poll cycle. Exported so tests can drive it deterministically.
func (r *Runner) Tick(ctx context.Context) error {
	img, err := r.src.Grab(ctx)
	if err != nil {
		return fmt.Errorf("grab: %w", err)
	}
	crop := imgproc.Crop(img, r.watch.Region)

	var ev trigger.Event
	if r.watch.Trigger.Type == "pixel_change" {
		if r.prev == nil {
			r.prev = crop
			return nil // first frame is the baseline
		}
		pct := imgproc.PercentChanged(r.prev, crop, diffTolerance)
		r.prev = crop
		ev = r.eval.ObservePixel(pct)
	} else {
		text, err := r.engine.Recognize(ctx, crop)
		if err != nil {
			return fmt.Errorf("ocr: %w", err)
		}
		ev = r.eval.ObserveText(text)
	}

	if r.store != nil {
		if err := r.store.Record(r.watch.Name, time.Now(), ev.Reading, ev.Fired); err != nil {
			r.logf("watch %s: history: %v", r.watch.Name, err)
		}
	}
	if ev.Fired && r.notifier != nil {
		title := fmt.Sprintf("watchglass: %s", r.watch.Name)
		body := fmt.Sprintf("%s — %s", ev.Reason, ev.Reading)
		if err := r.notifier.Send(ctx, title, body); err != nil {
			return fmt.Errorf("notify: %w", err)
		}
	}
	return nil
}
