// Package runner drives one watch: grab -> crop -> evaluate -> record -> notify.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/imgproc"
	"github.com/darrenhuai/watchglass/internal/notify"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/trigger"
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
	// outcome, the RAW crop (before preprocessing) and the time the tick
	// stamps on it (a fire's alert carries the same time, see Delivery.TS).
	// The web UI uses it to feed the live readout; keep it fast — it runs on
	// the poll goroutine.
	OnReading func(ev trigger.Event, crop image.Image, at time.Time)

	// OnHealth, when set, is called on every stream health transition, after
	// it is logged and before the notifier is attempted. Keep it fast — it
	// runs on the poll goroutine.
	OnHealth func(hev health.Event)

	// OnDelivery, when set, hears how each alert (a fire, a stream going
	// down or recovering) went out: Pending when it is handed to the
	// sender, then OK or Err when the send finishes, or Skipped at once
	// when the watch has no notify URLs. Under Run the finished report comes
	// from the sender goroutine, not the poll goroutine. Keep it fast.
	OnDelivery func(d Delivery)

	// outbox feeds the sender goroutine Run starts, so a slow or dead
	// notification service never holds up the next poll. nil outside Run
	// (Tick driven directly, as the tests do): alerts are then sent inline.
	outbox chan alert
}

// Delivery is one report on an alert's way out (see OnDelivery).
type Delivery struct {
	TS      time.Time // when the alert was raised; a fire's is its reading's time
	Kind    string    // "fired", "down" or "recovered"
	Pending bool      // handed to the sender; the outcome follows
	OK      bool      // every notify URL took it
	Skipped bool      // no notify URLs: nothing to send
	Err     string    // the failure, scrubbed of credentials (notify.Scrub), at most errMax runes per failed line
}

// errMax caps a delivery error kept for display.
const errMax = 200

// outboxSize is how many alerts may wait behind one that is being sent
// before new ones are dropped (and reported as not delivered).
const outboxSize = 16

// alert is one notification waiting to go out.
type alert struct {
	ts          time.Time
	kind        string
	title, body string
	png         []byte
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

// SeedDown starts the health tracker in the down state. The supervisor
// calls it before Run when the watch's previous incarnation was down, so a
// restart neither wipes that verdict nor re-reports it: the first poll that
// produces a reading emits the one real "healthy" transition. Must be
// called before Run.
func (r *Runner) SeedDown() {
	r.health.SeedDown()
}

// Run polls until ctx is cancelled. Errors are logged, never fatal: a
// watcher that dies on one bad frame is worse than no watcher. Alerts go out
// from a goroutine of their own (sendLoop), so a webhook that takes its
// full 10-15 s to time out delays nothing but itself. Stop doesn't wait for
// that goroutine: it finishes what was queued and exits.
func (r *Runner) Run(ctx context.Context) {
	if r.notifier != nil {
		r.outbox = make(chan alert, outboxSize)
		go r.sendLoop(ctx, r.outbox)
		defer func() {
			close(r.outbox)
			r.outbox = nil
		}()
	}
	interval := r.baseIvl
	var lastReading string
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		ev, err := r.Tick(ctx)
		if err != nil && ctx.Err() == nil {
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
	at := time.Now()
	img, err := r.src.Grab(ctx)
	if err != nil {
		return trigger.Event{}, r.pollFailed(ctx, fmt.Errorf("grab: %w", err))
	}
	crop := imgproc.Crop(img, r.watch.Region)

	var ev trigger.Event
	if r.watch.Trigger.Type == "pixel_change" {
		r.pollSucceeded(ctx)
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
			// A frame that arrives but can't be read is still a poll with
			// no reading: it counts toward the health threshold exactly like
			// a failed grab, so an engine that fails on every tick (tesseract
			// crashing, removed after boot, bad tessdata) surfaces as down
			// instead of as a healthy watch with "no data yet" forever.
			return trigger.Event{}, r.pollFailed(ctx, fmt.Errorf("ocr: %w", err))
		}
		r.pollSucceeded(ctx)
		ev = r.eval.ObserveText(text)
	}

	if r.store != nil {
		if err := r.store.Record(r.watch.Name, time.Now(), ev.Reading, ev.Fired); err != nil {
			r.logf("watch %s: history: %v", r.watch.Name, err)
		}
	}
	if r.OnReading != nil {
		r.OnReading(ev, crop, at)
	}
	if ev.Fired {
		// The body names the watch too: a plain webhook (generic without
		// ?template=json) gets only the body, and "pattern matched — PRINT
		// COMPLETE" alone doesn't say which printer.
		a := alert{ts: at, kind: "fired", title: fmt.Sprintf("watchglass: %s", r.watch.Name),
			body: fmt.Sprintf("%s: %s — %s", r.watch.Name, ev.Reason, ev.Reading)}
		if _, ok := r.notifier.(notify.ImageSender); ok {
			var buf bytes.Buffer
			if err := png.Encode(&buf, crop); err != nil {
				r.logf("watch %s: encode crop for notification: %v", r.watch.Name, err)
			} else {
				a.png = buf.Bytes()
			}
		}
		r.raise(ctx, a)
	}
	return ev, nil
}

// raise sends an alert, or says why it isn't sent. Under Run it only queues
// it: the send happens on the sender goroutine (sendLoop).
func (r *Runner) raise(ctx context.Context, a alert) {
	if r.notifier == nil {
		r.report(Delivery{TS: a.ts, Kind: a.kind, Skipped: true})
		return
	}
	if r.outbox == nil {
		r.deliver(ctx, a)
		return
	}
	r.report(Delivery{TS: a.ts, Kind: a.kind, Pending: true})
	select {
	case r.outbox <- a:
	default:
		r.logf("watch %s: notify: %d alerts are still waiting to be sent, so this %s alert was dropped", r.watch.Name, outboxSize, a.kind)
		r.report(Delivery{TS: a.ts, Kind: a.kind, Err: "not sent: the alerts before it were still waiting to go out"})
	}
}

// sendLoop sends queued alerts in order until Run closes box. Sends are
// cut loose from ctx: an alert raised just before the watch was stopped
// (a Save & restart) still goes out, bounded by the services' own timeouts.
func (r *Runner) sendLoop(ctx context.Context, box <-chan alert) {
	sendCtx := context.WithoutCancel(ctx)
	for a := range box {
		r.deliver(sendCtx, a)
	}
}

// deliver sends one alert and reports the outcome. A failure is logged and
// reported with every URL cut down to scheme://host and every credential
// masked (notify.Scrub): the report is shown in the web UI.
func (r *Runner) deliver(ctx context.Context, a alert) {
	var err error
	if is, ok := r.notifier.(notify.ImageSender); ok && a.png != nil {
		err = is.SendImage(ctx, a.title, a.body, a.png)
	} else {
		err = r.notifier.Send(ctx, a.title, a.body)
	}
	d := Delivery{TS: a.ts, Kind: a.kind, OK: err == nil}
	if err != nil {
		msg := notify.Scrub(err.Error(), r.watch.Notify)
		r.logf("watch %s: notify (%s): %s", r.watch.Name, a.kind, msg)
		// Capped per failed line, so a long first error can't push the
		// second line's cause (its status code) out of the record.
		parts := strings.Split(msg, "; ")
		for i, p := range parts {
			if utf8.RuneCountInString(p) > errMax {
				parts[i] = string([]rune(p)[:errMax-1]) + "…"
			}
		}
		d.Err = strings.Join(parts, "; ")
	}
	r.report(d)
}

func (r *Runner) report(d Delivery) {
	if r.OnDelivery != nil {
		r.OnDelivery(d)
	}
}

// pollFailed records a poll that produced no reading with the health
// tracker and hands err back unchanged for Tick to return. A poll that
// failed only because ctx was cancelled (Stop arriving while a grab or OCR
// pass is in flight) says nothing about the source, so it is not counted:
// it must never manufacture a Down transition — and the notification, MQTT
// "offline", and registry verdict that fan out from one — on the way out.
func (r *Runner) pollFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}
	if hev, changed := r.health.Failure(err); changed {
		r.notifyHealth(ctx, hev)
	}
	return err
}

// pollSucceeded records a poll that produced a reading.
func (r *Runner) pollSucceeded(ctx context.Context) {
	if hev, changed := r.health.Success(); changed {
		r.notifyHealth(ctx, hev)
	}
}

// notifyHealth reports a stream up/down transition. Failures to notify are
// logged and reported (OnDelivery), never fatal — the watch keeps polling
// regardless.
func (r *Runner) notifyHealth(ctx context.Context, hev health.Event) {
	r.logf("watch %s: %s", r.watch.Name, hev.Message)
	if r.OnHealth != nil {
		r.OnHealth(hev)
	}
	kind := "recovered"
	if hev.State == "down" {
		kind = "down"
	}
	r.raise(ctx, alert{ts: time.Now(), kind: kind,
		title: fmt.Sprintf("watchglass: %s (%s)", r.watch.Name, hev.State),
		body:  fmt.Sprintf("%s: %s", r.watch.Name, hev.Message)})
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
