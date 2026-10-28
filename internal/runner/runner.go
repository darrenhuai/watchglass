// Package runner drives one watch: grab -> crop -> evaluate -> record -> notify.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"sync"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/imgproc"
	"github.com/darrenhuai/watchglass/internal/notify"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// diffTolerance absorbs camera sensor noise in pixel_change watches. It is
// shared with the web UI's Test (imgproc.NoiseTolerance).
const diffTolerance = imgproc.NoiseTolerance

type Runner struct {
	watch    config.Watch
	src      source.Source
	engine   ocr.Engine
	notifier notify.Notifier
	store    *history.Store
	eval     *trigger.Evaluator
	prev     *image.RGBA
	logf     func(string, ...any)

	// fingerprint keys the saved trigger state to this watch's question,
	// saved is the state as the store has it, and restoredFire the fire
	// time New restored, if any (see persist.go).
	fingerprint  string
	saved        trigger.State
	restoredFire time.Time
	restoredSent bool
	// restoredDown is the down verdict New found saved, if any
	// (restoreHealth, RestoredDown).
	restoredDown *history.HealthState
	// reassert, set by SeedRestoredDown, hears the restored down verdict
	// again once reassertIn more polls have failed in a row.
	reassert   func()
	reassertIn int

	health  *health.Tracker
	baseIvl time.Duration
	maxIvl  time.Duration

	// OnReading, when set, is called once per completed Tick with the trigger
	// outcome, the crop and the time the tick stamps on it (a fire's alert
	// carries the same time, see Delivery.TS). The crop is the region in
	// the camera's own colours, before grayscale, invert, binarize and
	// upscale, but already turned by preprocess.rotate on a watch that
	// reads text: the same picture a fire attaches to its alert.
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

	// OnCrash, when set, hears every panic caught in this watch's poll
	// (see Contain), with where it was caught (CrashError.Step): in the
	// source, in the engine, the preprocessing or the trigger code (which
	// fails that one read), in a consumer of the reading, or, under Run,
	// anywhere else in a Tick. The web UI shows the watch in error until
	// OnCrashEnd. Keep it fast: it runs on the poll goroutine.
	OnCrash func(ce *CrashError)
	// OnCrashEnd, when set, hears that a run of crashes ended: a poll
	// finished with no crash anywhere in it. A consumer that crashes after
	// every reading never ends the run, so the watch stays in error
	// between its readings too.
	OnCrashEnd func()
	// crashes is the current run of crashes (crashed, endTick).
	crashes crashRun

	// outbox feeds the sender goroutine Run starts, so a slow or dead
	// notification service never holds up the next poll. nil outside Run
	// (Tick driven directly, as the tests do): alerts are then sent inline.
	outbox chan alert
	// sent is closed when the sender goroutine of the last Run has sent
	// everything queued to it (see WaitSent). nil if Run started none.
	sent chan struct{}

	// cond is the watch's condition as of its last poll, for the sender
	// to check before trying an alert again (retry.go); wake pokes the
	// sender when it changes, and clock is what the sender waits on.
	condMu sync.Mutex
	cond   condition
	wake   chan struct{}
	clock  clock
	// isDown mirrors the health tracker's verdict, for cond.
	isDown bool
	// resendTaken is the notify URLs (by urlKey) that took the last
	// fire's alert before the restart, when the rest hadn't and the
	// condition still held at the stop: the first fire after the restart
	// is that alert sent again, and goes to the rest only. It is let go
	// once the watch sees the condition clear first (Tick).
	resendTaken map[string]bool
}

// Delivery is one report on an alert's way out (see OnDelivery).
type Delivery struct {
	TS      time.Time // when the alert was raised; a fire's is its reading's time
	Kind    string    // "fired", "down" or "recovered"
	Pending bool      // handed to the sender; the outcome follows
	OK      bool      // every notify URL took it
	Skipped bool      // no notify URLs: nothing to send
	Err     string    // the failure at the URLs given up on, scrubbed of credentials (notify.Scrub), at most errMax runes per failed line
	SentAt  time.Time // OK: when the last URL took it
	// Retrying is the failure at the URLs that will be tried again, in
	// Err's form with each cause in a few words, NextTry when the next
	// try is due, and Tries how many tries the alert has had at the URL
	// that has had most. A report with Retrying set is not the last
	// word: OK, Err or Dropped follows, unless the watch stops first (see
	// retry.go).
	Retrying string
	NextTry  time.Time
	Tries    int
	// Dropped is why the alert was let go before the URLs in Retrying
	// took it: "the condition cleared", "the camera came back", "the
	// camera went down again". Nothing tries them any more.
	Dropped string
}

// DroppedByRestart is Delivery.Dropped for an alert still being tried
// when its watch was stopped (a Save & restart): the supervisor marks the
// last record so, since the stopped run no longer reports.
const DroppedByRestart = "the watch was restarted"

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
	fired       time.Time       // a fire's time as its trigger state has it (markDelivered)
	skip        map[string]bool // notify URLs (by urlKey) that took it before a restart
}

func New(w config.Watch, src source.Source, engine ocr.Engine, notifier notify.Notifier,
	store *history.Store, logf func(string, ...any)) (*Runner, error) {
	eval, err := trigger.New(w.Trigger)
	if err != nil {
		return nil, fmt.Errorf("watch %q: %w", w.Name, err)
	}
	eval.SevenSeg = w.Engine == "sevenseg"
	eval.StaleAfter = restoreWindow(w)
	if w.Trigger.Type != "pixel_change" && engine == nil {
		return nil, fmt.Errorf("watch %q: trigger %q requires an OCR engine", w.Name, w.Trigger.Type)
	}
	base := time.Duration(w.Interval)
	if base <= 0 {
		logf("watch %s: invalid interval %v, defaulting to %v", w.Name, base, 5*time.Second)
		base = 5 * time.Second
	}
	r := &Runner{watch: w, src: src, engine: engine, notifier: notifier,
		store: store, eval: eval, logf: logf,
		health: health.New(w.HealthAfter), baseIvl: base, maxIvl: time.Duration(w.MaxInterval),
		wake: make(chan struct{}, 1), clock: realClock{}}
	r.restoreState(time.Now())
	r.restoreHealth()
	r.noteCondition()
	return r, nil
}

// SeedDown starts the health tracker in the down state. The supervisor
// calls it before Run when the watch's previous incarnation was down, so a
// restart neither wipes that verdict nor re-reports it: the first poll that
// produces a reading emits the one real "healthy" transition. Must be
// called before Run.
func (r *Runner) SeedDown() {
	r.health.SeedDown()
	r.isDown = true
	r.noteCondition()
}

// SeedRestoredDown is SeedDown for the verdict the history database kept
// (RestoredDown), on the first start after watchglass itself restarted.
// No transition is made, so no second "down" alert goes out; but the
// consumers that only hear transitions (the MQTT publisher's retained
// health state, which a broker may not have kept) would otherwise never
// hear that the watch is down until it recovers. So once health_after
// polls have failed in a row in this run, which is when a tracker that
// started healthy would have gone down, reassert hears the down verdict
// once, with the restored message. A poll that reads first cancels it:
// the "healthy" transition says the rest. Must be called before Run.
func (r *Runner) SeedRestoredDown(reassert func(hev health.Event)) {
	r.health.SeedDown()
	r.isDown = true
	r.noteCondition()
	if reassert == nil || r.restoredDown == nil {
		return
	}
	n := r.watch.HealthAfter
	if n < 1 {
		n = 1
	}
	msg := r.restoredDown.Message
	r.reassertIn = n
	r.reassert = func() { reassert(health.Event{State: "down", Message: msg}) }
}

// Run polls until ctx is cancelled. Errors are logged, never fatal: a
// watcher that dies on one bad frame is worse than no watcher. Alerts go out
// from a goroutine of their own (sendLoop), so a webhook that takes its
// full 10-15 s to time out delays nothing but itself, and one that didn't
// take an alert is tried again while the alert's reason holds (retry.go).
// Stop doesn't wait for that goroutine: it finishes what was queued, drops
// the tries still due and exits (WaitSent waits for that).
func (r *Runner) Run(ctx context.Context) {
	if r.notifier != nil {
		r.outbox = make(chan alert, outboxSize)
		sent := make(chan struct{})
		r.sent = sent
		go func(box <-chan alert) {
			defer close(sent)
			r.sendLoop(ctx, box)
		}(r.outbox)
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
		var ev trigger.Event
		err := Contain(func() error {
			var err error
			ev, err = r.Tick(ctx)
			return err
		})
		var ce *CrashError
		switch {
		case errors.As(err, &ce):
			// A crash grabbing or reading the frame, or in OnReading,
			// Tick reports itself; the first two count as a failed poll.
			// One anywhere else in it (the history store, the saved
			// state, an alert, a health callback) is caught here, and
			// the next poll goes ahead all the same.
			if !ce.reported {
				r.crashed(ce, StepPoll)
				r.endTick()
			}
		case err != nil && ctx.Err() == nil:
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
	ev, err := r.tick(ctx)
	r.endTick()
	r.noteCondition()
	if r.resendTaken != nil {
		// The condition was seen clear before it fired again: the next
		// fire is news, for every URL.
		if st := r.eval.State(); st.HasStable && st.Stable != condMet {
			r.resendTaken = nil
		}
	}
	return ev, err
}

func (r *Runner) tick(ctx context.Context) (trigger.Event, error) {
	at := time.Now()
	var img image.Image
	err := Contain(func() error {
		var err error
		img, err = r.src.Grab(ctx)
		return err
	})
	if err != nil {
		// A panic in the source (a stream or snapshot its parser didn't
		// expect) is a failed grab too: it counts toward health, so a
		// camera that trips it on every poll still goes down and alerts.
		var ce *CrashError
		if errors.As(err, &ce) {
			r.crashed(ce, StepGrab)
		}
		return trigger.Event{}, r.pollFailed(ctx, fmt.Errorf("grab: %w", err))
	}
	var (
		ev       trigger.Event
		crop     *image.RGBA
		baseline bool
	)
	err = Contain(func() error {
		var err error
		ev, crop, baseline, err = r.read(ctx, img)
		return err
	})
	var ce *CrashError
	if errors.As(err, &ce) {
		// A panic reading the frame fails this read and nothing else: it
		// counts toward the health threshold like any read that produced
		// nothing, and the next poll goes ahead.
		r.crashed(ce, StepRead)
		return trigger.Event{}, r.pollFailed(ctx, ce)
	}
	if err != nil {
		// A frame that arrives but can't be read is still a poll with
		// no reading: it counts toward the health threshold exactly like
		// a failed grab, so an engine that fails on every tick (tesseract
		// crashing, removed after boot, bad tessdata) surfaces as down
		// instead of as a healthy watch with "no data yet" forever.
		return trigger.Event{}, r.pollFailed(ctx, fmt.Errorf("ocr: %w", err))
	}
	r.pollSucceeded(ctx)
	if baseline {
		return trigger.Event{}, nil // a pixel_change watch's first frame
	}

	if r.store != nil {
		if err := r.store.Record(r.watch.Name, time.Now(), ev.Reading, ev.Fired); err != nil {
			r.logf("watch %s: history: %v", r.watch.Name, err)
		}
	}
	// Before the alert is raised: a fire is remembered from the moment it
	// is decided, so a restart right after it can't send it twice.
	r.saveState()
	if r.OnReading != nil {
		// A panic in a consumer (the Live panel, the Home Assistant
		// publisher) is logged and shown, but the fire it decided still
		// goes out: the saved state already says it fired.
		if err := Contain(func() error { r.OnReading(ev, crop, at); return nil }); err != nil {
			var ce *CrashError
			if errors.As(err, &ce) {
				r.crashed(ce, StepAfter)
			}
		}
	}
	if ev.Fired {
		// The body names the watch too: a plain webhook (generic without
		// ?template=json) gets only the body, and "pattern matched — PRINT
		// COMPLETE" alone doesn't say which printer.
		a := alert{ts: at, kind: "fired", title: fmt.Sprintf("watchglass: %s", r.watch.Name),
			body:  fmt.Sprintf("%s: %s — %s", r.watch.Name, ev.Reason, ev.Reading),
			fired: r.eval.State().LastFired, skip: r.resendTaken}
		r.resendTaken = nil
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

// read turns one frame into the trigger's verdict: the crop, the
// preprocessing, the engine and the trigger evaluation, the code a
// strange frame can drive somewhere nobody tested (Tick contains a panic
// in it). baseline is a pixel_change watch's first frame, which has
// nothing to compare with. err is the engine's.
func (r *Runner) read(ctx context.Context, img image.Image) (ev trigger.Event, crop *image.RGBA, baseline bool, err error) {
	crop = imgproc.Crop(img, r.watch.Region)
	if r.watch.Trigger.Type == "pixel_change" {
		if r.prev == nil {
			r.prev = crop
			return trigger.Event{}, crop, true, nil
		}
		pct := imgproc.PercentChanged(r.prev, crop, diffTolerance)
		r.prev = crop
		return r.eval.ObservePixel(pct), crop, false, nil
	}
	// A sideways display is turned upright once, here, and the turned
	// crop is what goes everywhere from now on: to the engine (after the
	// rest of the preprocessing), to OnReading (the Live panel and Home
	// Assistant's camera) and into the alert. Someone reading an alert
	// on a phone wants the picture the right way up.
	pp := r.watch.Preprocess
	crop = imgproc.Rotate(crop, pp.Rotate)
	pp.Rotate = 0 // already turned
	prepped := imgproc.Apply(crop, pp)
	// The seven-segment decoder's Words say which '?' is a digit it
	// wasn't sure of, which no pattern may match (ObserveRead).
	var (
		text   string
		unsure bool
	)
	if d, ok := r.engine.(ocr.DetailedEngine); ok && r.watch.Engine == "sevenseg" {
		var words []ocr.Word
		text, words, err = d.RecognizeWords(ctx, prepped)
		unsure = ocr.HasUnsureDigit(text, words)
	} else {
		text, err = r.engine.Recognize(ctx, prepped)
	}
	if err != nil {
		return trigger.Event{}, crop, false, err
	}
	return r.eval.ObserveRead(text, unsure), crop, false, nil
}

// raise sends an alert, or says why it isn't sent. Under Run it only queues
// it: the send happens on the sender goroutine (sendLoop).
func (r *Runner) raise(ctx context.Context, a alert) {
	if r.notifier == nil {
		// Nothing to send it to: done, as far as a restart is concerned.
		r.markDelivered(a.fired)
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

// WaitSent waits until the sender goroutine of the Run that has returned
// has sent everything queued to it, or ctx ends, and reports whether it
// finished. The supervisor calls it when watchglass is shutting down, so an
// alert decided just before the stop goes out before the process exits.
// Call it only after Run has returned.
func (r *Runner) WaitSent(ctx context.Context) bool {
	if r.sent == nil {
		return true
	}
	select {
	case <-r.sent:
		return true
	case <-ctx.Done():
		return false
	}
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
	if r.reassert != nil {
		if r.reassertIn--; r.reassertIn <= 0 {
			fn := r.reassert
			r.reassert = nil
			fn()
		}
	}
	return err
}

// pollSucceeded records a poll that produced a reading.
func (r *Runner) pollSucceeded(ctx context.Context) {
	r.reassert = nil
	if hev, changed := r.health.Success(); changed {
		r.notifyHealth(ctx, hev)
	}
}

// notifyHealth reports a stream up/down transition. Failures to notify are
// logged and reported (OnDelivery), never fatal — the watch keeps polling
// regardless.
func (r *Runner) notifyHealth(ctx context.Context, hev health.Event) {
	r.logf("watch %s: %s", r.watch.Name, hev.Message)
	r.isDown = hev.State == "down"
	r.saveHealth(hev, time.Now())
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
