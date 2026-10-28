package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/darrenhuai/watchglass/internal/notify"
)

// An alert that a notify URL didn't take is tried there again, with a
// growing wait between tries, for as long as what it says is still so
// (the condition still holds, the camera is still down) and no newer
// alert of its kind has replaced it. URLs that took it are not sent it
// twice. A failure that won't fix itself (HTTP 404, a refused token) is
// not tried again. All of it runs on the watch's one sender goroutine
// (sendLoop), which keeps at most one fire alert and one stream alert
// (down or recovered), so a service that is down for hours piles nothing
// up, and a camera that blips while a fire alert waits doesn't push that
// alert out. A stop ends the tries; the restart re-send (persist.go)
// takes over a fire alert, since it was never marked delivered, and sends
// it only to the URLs that hadn't taken it.

// retryDelays is the wait before each new try: 10 s after the first
// failure, then 20 s, then every 30 s up to two and a half minutes after
// the alert, so a service that is back by then gets it within 30 s; then
// 1 min, 2 min, 5 min, and every 5 min from there.
var retryDelays = []time.Duration{
	10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
	time.Minute, 2 * time.Minute, 5 * time.Minute,
}

// retryDelay is the wait after the n-th failed try (n >= 1).
func retryDelay(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > len(retryDelays) {
		n = len(retryDelays)
	}
	return retryDelays[n-1]
}

// triesText counts tries: "1 try", "3 tries".
func triesText(n int) string {
	if n == 1 {
		return "1 try"
	}
	return fmt.Sprintf("%d tries", n)
}

// waitText is a wait in the words the log and the page use: "10 s",
// "30 s", "1 min", "2 min", "1 min 40 s", "2 h 5 min".
func waitText(d time.Duration) string {
	d = d.Round(time.Second)
	if d < 0 {
		d = 0
	}
	h, m, s := int(d/time.Hour), int(d%time.Hour/time.Minute), int(d%time.Minute/time.Second)
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%d h %d min", h, m)
	case h > 0:
		return fmt.Sprintf("%d h", h)
	case m > 0 && s > 0:
		return fmt.Sprintf("%d min %d s", m, s)
	case m > 0:
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d s", s)
}

// clock is the time the sender goroutine reads and waits on. Tests swap
// in internal/fakeclock.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// condMet is trigger.State.Stable's value while an ocr_match or numeric
// watch's condition holds (the trigger package's cond:true key).
const condMet = "cond:true"

// condition is what the sender checks before trying an alert again:
// whether a fire's condition still holds and whether the stream is down.
type condition struct {
	fire bool
	down bool
}

// noteCondition records the watch's condition after a poll, for the
// sender, and wakes it when the condition changed, so an alert whose
// reason is gone is let go at once rather than at its next try. Runs on
// the poll goroutine (and once from New).
func (r *Runner) noteCondition() {
	c := condition{fire: r.fireHolds(), down: r.isDown}
	r.condMu.Lock()
	changed := c != r.cond
	r.cond = c
	r.condMu.Unlock()
	if changed {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// fireHolds is whether a fired alert is still about what the display
// shows: for ocr_match and numeric, the condition the trigger settled on.
// ocr_changed and pixel_change fire on a change, which is news until it
// is told (or a newer change replaces it), so theirs always holds.
func (r *Runner) fireHolds() bool {
	switch r.watch.Trigger.Type {
	case "ocr_match", "numeric":
		return r.eval.State().Stable == condMet
	}
	return true
}

// condHolds is whether an alert of kind still has its reason.
func (r *Runner) condHolds(kind string) bool {
	r.condMu.Lock()
	defer r.condMu.Unlock()
	switch kind {
	case "down":
		return r.cond.down
	case "recovered":
		return !r.cond.down
	}
	return r.cond.fire
}

// owed is one notify URL that hasn't taken the current alert yet.
type owed struct {
	tries int       // tries so far at the current alert
	next  time.Time // when the next one is due
	cause string    // the last failure, scrubbed
}

// slot is one alert on its way out and, per notify URL, where it stands.
// A sender has two: one for fires, one for the stream's down and
// recovered alerts. A newer alert replaces only the one in its own slot.
type slot struct {
	cur    alert
	has    bool           // cur is set
	tries  int            // the most tries any URL has made at cur
	owed   map[int]*owed  // URLs that still owe cur, by index
	failed map[int]string // URLs that won't take cur, with the cause, by index
	taken  map[int]bool   // URLs that took cur (before a restart too: alert.skip)
	// dropped is the URLs let off cur before they took it, and why is
	// the reason: the condition cleared, the camera came back.
	dropped map[int]*owed
	why     string
	sentAt  time.Time // when the last URL took it
}

// retrying is whether a URL is still to be tried again at cur.
func (sl *slot) retrying() bool { return sl.has && len(sl.owed) > 0 }

// done is whether every URL took cur.
func (sl *slot) done() bool {
	return len(sl.owed) == 0 && len(sl.failed) == 0 && len(sl.dropped) == 0
}

// sender is one watch's alerts on their way out: the newest fire alert
// and the newest stream alert, and per notify URL whether it still owes
// each. Only the sender goroutine touches it (deliver, for a Tick driven
// without Run, uses one of its own that never tries twice).
type sender struct {
	r      *Runner
	lines  notify.LineSender // nil for a plain Notifier
	labels []string          // one per notify URL; a plain Notifier is one line labelled ""
	keys   []string          // urlKey of each notify URL, when the lines are the watch's URLs
	// stopping is set when no new try may start: the watch is stopping,
	// or there is no sender goroutine to make it.
	stopping     bool
	fire, stream slot
}

func newSender(r *Runner) *sender {
	s := &sender{r: r}
	if ls, ok := r.notifier.(notify.LineSender); ok {
		s.lines, s.labels = ls, ls.Lines()
	}
	if len(s.labels) == 0 {
		s.labels = []string{""}
	}
	if s.lines != nil && len(r.watch.Notify) == len(s.labels) {
		for _, u := range r.watch.Notify {
			s.keys = append(s.keys, urlKey(u))
		}
	}
	return s
}

// urlKey names a notify URL in the saved trigger state without keeping
// the URL, which may carry a token: the first 16 hex digits of its
// SHA-256.
func urlKey(u string) string {
	sum := sha256.Sum256([]byte(u))
	return hex.EncodeToString(sum[:8])
}

// slotFor is the slot an alert of kind goes in.
func (s *sender) slotFor(kind string) *slot {
	if kind == "fired" {
		return &s.fire
	}
	return &s.stream
}

// other is the slot that isn't sl.
func (s *sender) other(sl *slot) *slot {
	if sl == &s.fire {
		return &s.stream
	}
	return &s.fire
}

// name is what the log and the page call line.
func (s *sender) name(line int) string {
	if l := s.labels[line]; l != "" {
		return l
	}
	return "the notify URL"
}

// part is a line's failure as the delivery record carries it: "line 2 of
// 3 (ntfy://ntfy.sh): ntfy: status 503", or the cause alone when the
// notifier has no lines of its own.
func (s *sender) part(line int, cause string) string {
	if s.lines == nil {
		return cause
	}
	return fmt.Sprintf("line %d of %d (%s): %s", line+1, len(s.labels), s.labels[line], cause)
}

// take starts on a new alert: every URL is tried at once, except those
// that took it before a restart (alert.skip). A URL still owed the alert
// in the same slot before is let off it: the newer alert is what the
// watch has to say now.
func (s *sender) take(ctx context.Context, a alert) {
	sl := s.slotFor(a.kind)
	for _, line := range lineList(sl.owed) {
		s.r.logf("watch %s: notify (%s): a newer alert replaced the one %s hadn't taken after %s",
			s.r.watch.Name, sl.cur.kind, s.name(line), triesText(sl.owed[line].tries))
	}
	*sl = slot{cur: a, has: true, owed: map[int]*owed{}, failed: map[int]string{}, taken: map[int]bool{}, dropped: map[int]*owed{}}
	var todo []int
	for i := range s.labels {
		if i < len(s.keys) && a.skip[s.keys[i]] {
			sl.taken[i] = true
			continue
		}
		todo = append(todo, i)
	}
	if len(sl.taken) > 0 {
		s.r.logf("watch %s: notify (%s): %d of %d notify URLs took this alert before the restart, so it goes to the rest only",
			s.r.watch.Name, a.kind, len(sl.taken), len(s.labels))
	}
	if len(todo) == 0 {
		sl.sentAt = s.r.clock.Now()
		s.report(sl)
		return
	}
	s.attempt(ctx, sl, todo)
}

// attempt tries sl's alert at lines, all at once, and sorts each outcome:
// taken, to be tried again, or given up on. Then it reports (report). A
// try cut short because the watch is stopping (ctx ended) is not a try:
// it says nothing about the service, and the stop logs what is left.
func (s *sender) attempt(ctx context.Context, sl *slot, lines []int) {
	errs := make([]error, len(lines))
	var wg sync.WaitGroup
	for k, line := range lines {
		wg.Add(1)
		go func(k, line int) {
			defer wg.Done()
			errs[k] = s.r.sendLine(ctx, sl.cur, line)
		}(k, line)
	}
	wg.Wait()
	now := s.r.clock.Now()
	name, kind := s.r.watch.Name, sl.cur.kind
	for k, line := range lines {
		err := errs[k]
		if err != nil && ctx.Err() != nil {
			continue
		}
		before := sl.owed[line]
		tries := 1
		if before != nil {
			tries = before.tries + 1
		}
		if tries > sl.tries {
			sl.tries = tries
		}
		if err == nil {
			delete(sl.owed, line)
			sl.taken[line] = true
			if tries > 1 {
				s.r.logf("watch %s: notify (%s): %s took the alert after %s, %s after it was raised",
					name, kind, s.name(line), triesText(tries), waitText(time.Since(sl.cur.ts)))
			}
			continue
		}
		cause := notify.Scrub(err.Error(), s.r.watch.Notify)
		var crashed *sendCrash
		if !s.stopping && !errors.As(err, &crashed) && notify.TransientAt(s.labels[line], cause) {
			d := retryDelay(tries)
			sl.owed[line] = &owed{tries: tries, next: now.Add(d), cause: cause}
			// One line per change of state, not one per try: the first
			// failure, and a failure with a new cause (refused, then 503).
			if before == nil || notify.ShortCauseAt(s.labels[line], before.cause) != notify.ShortCauseAt(s.labels[line], cause) {
				s.r.logf("watch %s: notify (%s): %s; trying again in %s", name, kind, notify.Describe(s.labels[line], cause), waitText(d))
			}
			continue
		}
		delete(sl.owed, line)
		sl.failed[line] = cause
		s.r.logf("watch %s: notify (%s): %s", name, kind, s.part(line, cause))
	}
	if sl.done() {
		sl.sentAt = now
	}
	s.report(sl)
}

// retry tries each alert again at every URL whose wait is over, unless
// its reason is gone, which lets every URL off it (prune).
func (s *sender) retry(ctx context.Context) {
	for _, sl := range []*slot{&s.fire, &s.stream} {
		if !sl.retrying() {
			continue
		}
		if !s.r.condHolds(sl.cur.kind) {
			s.prune()
			continue
		}
		now := s.r.clock.Now()
		var due []int
		for _, line := range lineList(sl.owed) {
			if !sl.owed[line].next.After(now) {
				due = append(due, line)
			}
		}
		if len(due) > 0 {
			s.attempt(ctx, sl, due)
		}
	}
}

// prune lets every URL off an alert whose reason is gone: the condition
// cleared, or the stream came back (or went down again), before the
// service did.
func (s *sender) prune() {
	for _, sl := range []*slot{&s.fire, &s.stream} {
		if !sl.retrying() || s.r.condHolds(sl.cur.kind) {
			continue
		}
		why := "the condition cleared"
		switch sl.cur.kind {
		case "down":
			why = "the camera came back"
		case "recovered":
			why = "the camera went down again"
		}
		for _, line := range lineList(sl.owed) {
			s.r.logf("watch %s: notify (%s): %s before %s took the alert (%s); it is dropped",
				s.r.watch.Name, sl.cur.kind, why, s.name(line), triesText(sl.owed[line].tries))
			sl.dropped[line] = sl.owed[line]
		}
		sl.why = why
		sl.owed = map[int]*owed{}
		s.report(sl)
	}
}

// stop ends the tries: the watch is stopping. What is still owed is
// logged. A match or number alert is left to the restart re-send, which
// finds it not marked delivered; a change alert and a stream alert are
// dropped (the restarted watch takes what it sees as its starting point,
// and the tracker starts from its saved verdict and sends no second
// one). No report: the run it belongs to is over, and the supervisor
// marks the record.
func (s *sender) stop() {
	s.stopping = true
	resent := s.r.watch.Trigger.Type == "ocr_match" || s.r.watch.Trigger.Type == "numeric"
	for _, sl := range []*slot{&s.fire, &s.stream} {
		then := "it goes out again after the restart if the condition still holds"
		if sl.cur.kind != "fired" || !resent {
			then = "it is dropped"
		}
		for _, line := range lineList(sl.owed) {
			s.r.logf("watch %s: notify (%s): stopping before %s took the alert from %s (%s); %s",
				s.r.watch.Name, sl.cur.kind, s.name(line), sl.cur.ts.Format("15:04:05"), triesText(sl.owed[line].tries), then)
		}
		sl.owed = map[int]*owed{}
	}
}

// nextDue is when the earliest try is due, if any is.
func (s *sender) nextDue() (time.Time, bool) {
	var next time.Time
	for _, sl := range []*slot{&s.fire, &s.stream} {
		for _, o := range sl.owed {
			if next.IsZero() || o.next.Before(next) {
				next = o.next
			}
		}
	}
	return next, !next.IsZero()
}

// lineList is the lines of m in list order.
func lineList[V any](m map[int]V) []int {
	lines := make([]int, 0, len(m))
	for line := range m {
		lines = append(lines, line)
	}
	sort.Ints(lines)
	return lines
}

// report settles sl's alert where it stands (delivered: marked so for
// the restart re-send; partly taken: which URLs took it, for the same)
// and tells OnDelivery. The page has room for one alert, so while the
// other slot's alert is still being tried and this one is not, it hears
// about the other last: that is the one that still needs watching. When
// both are being tried, it hears about the newer last. sl's own report
// always goes first: it settles sl's alert, the registry's "sending"
// mark for it included.
func (s *sender) report(sl *slot) {
	switch {
	case !sl.has:
		return
	case sl.done():
		s.r.markDelivered(sl.cur.fired)
	case sl.cur.kind == "fired" && len(sl.taken) > 0 && len(sl.owed) > 0:
		var keys []string
		for _, line := range lineList(sl.taken) {
			if line < len(s.keys) {
				keys = append(keys, s.keys[line])
			}
		}
		s.r.markTaken(sl.cur.fired, keys)
	}
	s.r.report(s.delivery(sl))
	if o := s.other(sl); o.retrying() && (!sl.retrying() || o.cur.ts.After(sl.cur.ts)) {
		s.r.report(s.delivery(o))
	}
}

// delivery is where sl's alert stands, as OnDelivery hears it: delivered
// (every URL took it), still being tried (Retrying), let go (Dropped) or
// not delivered (Err). Each failed line is capped at errMax runes, so a
// long first error can't push the second line's cause out of the record.
// A line still being tried, or let go, carries its cause in a few words
// (notify.ShortCauseAt): the page shows it in a status line, and the full
// text of a refused connection on Windows alone is longer than the cap.
func (s *sender) delivery(sl *slot) Delivery {
	d := Delivery{TS: sl.cur.ts, Kind: sl.cur.kind, Tries: sl.tries}
	if sl.done() {
		d.OK, d.SentAt = true, sl.sentAt
		return d
	}
	var failed []string
	for _, line := range lineList(sl.failed) {
		failed = append(failed, clipRunes(s.part(line, sl.failed[line])))
	}
	d.Err = strings.Join(failed, "; ")
	waiting := sl.owed
	if len(sl.dropped) > 0 {
		waiting, d.Dropped = sl.dropped, sl.why
	}
	var parts []string
	for _, line := range lineList(waiting) {
		o := waiting[line]
		parts = append(parts, clipRunes(s.part(line, notify.ShortCauseAt(s.labels[line], o.cause))))
		if d.Dropped == "" && (d.NextTry.IsZero() || o.next.Before(d.NextTry)) {
			d.NextTry = o.next
		}
	}
	d.Retrying = strings.Join(parts, "; ")
	return d
}

func clipRunes(p string) string {
	if utf8.RuneCountInString(p) > errMax {
		return string([]rune(p)[:errMax-1]) + "…"
	}
	return p
}

// sendCrash is a panic in the notifier, recovered by sendLine: a bug,
// not an outage, so the alert is not tried again over it.
type sendCrash struct{ msg string }

func (e *sendCrash) Error() string { return e.msg }

// sendLine hands one alert to one notify URL (every URL at once for a
// notifier without lines of its own). A panic in it (the notification
// library meeting an answer it didn't expect) fails this try only: the
// sender goroutine would otherwise take the whole process down with it.
// The panic value is scrubbed before it is logged, like every error from
// the notifier: shoutrrr's carry the service URL, token and all.
func (r *Runner) sendLine(ctx context.Context, a alert, line int) (err error) {
	defer func() {
		if v := recover(); v != nil {
			r.logf("watch %s: sending the %s alert crashed; this is a bug, please report it: %s\n%s", r.watch.Name, a.kind, notify.Scrub(fmt.Sprint(v), r.watch.Notify), debug.Stack())
			err = &sendCrash{fmt.Sprintf("sending crashed; this is a bug in watchglass, please report it (%v)", v)}
		}
	}()
	if ls, ok := r.notifier.(notify.LineSender); ok {
		return ls.SendLine(ctx, line, a.title, a.body, a.png)
	}
	if is, ok := r.notifier.(notify.ImageSender); ok && a.png != nil {
		return is.SendImage(ctx, a.title, a.body, a.png)
	}
	return r.notifier.Send(ctx, a.title, a.body)
}

// sendLoop sends queued alerts in order until Run closes box, and tries
// again at the URLs that didn't take one while its reason holds. First
// tries are cut loose from ctx: an alert raised just before the watch
// was stopped (a Save & restart) still goes out, bounded by the services'
// own timeouts. Later tries are not: a stop ends them (sender.stop), and
// after it only what was already queued is tried, once.
//
// A panic anywhere in a delivery (the notifier has its own recover in
// sendLine; this covers the store and OnDelivery after it) is logged and
// fails nothing but that alert's bookkeeping: this goroutine would
// otherwise take the whole process down.
func (r *Runner) sendLoop(ctx context.Context, box <-chan alert) {
	s := newSender(r)
	sendCtx := context.WithoutCancel(ctx)
	// One timer per due time, kept across turns of the loop: a new one
	// only when the earliest try moved. A service down for hours would
	// otherwise leave a timer behind at every wake.
	var (
		due   <-chan time.Time
		dueAt time.Time
	)
	for {
		if next, ok := s.nextDue(); !ok {
			due, dueAt = nil, time.Time{}
		} else if !next.Equal(dueAt) {
			due, dueAt = r.clock.After(next.Sub(r.clock.Now())), next
		}
		select {
		case a, ok := <-box:
			if !ok {
				s.stop()
				return
			}
			r.contained(a.kind, func() { s.take(sendCtx, a) })
		case <-r.wake:
			r.contained("waiting", s.prune)
		case <-due:
			due, dueAt = nil, time.Time{}
			r.contained("waiting", func() { s.retry(ctx) })
		case <-ctx.Done():
			s.stop()
			for a := range box {
				r.contained(a.kind, func() { s.take(sendCtx, a) })
			}
			return
		}
	}
}

// contained runs one step of the sender under Contain and logs a crash
// in it, scrubbed.
func (r *Runner) contained(kind string, fn func()) {
	err := Contain(func() error {
		fn()
		return nil
	})
	var ce *CrashError
	if errors.As(err, &ce) {
		r.logf("watch %s: after sending the %s alert, watchglass crashed; this is a bug, please report it with the log: %s\n%s",
			r.watch.Name, kind, notify.Scrub(ce.Detail(), r.watch.Notify), ce.Stack)
	}
}

// deliver sends one alert, once, and reports the outcome: for a Tick
// driven without Run, where there is no sender goroutine to try again.
func (r *Runner) deliver(ctx context.Context, a alert) {
	s := newSender(r)
	s.stopping = true
	s.take(ctx, a)
}
