package runner

import (
	"context"
	"errors"
	"image"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/fakeclock"
	"github.com/darrenhuai/watchglass/internal/notify"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// A2: an alert decided while the notification service is down is tried
// again until it gets through, while its reason holds. These drive the
// sender goroutine with a fake clock and a real HTTP sink behind a
// generic+http:// notify URL, so the whole path (shoutrrr, the error
// text, Transient) is the one in production.

// sink is a webhook that answers with whatever status it is told to, and
// counts what it took. Its listener can be closed and reopened on the
// same port, for "connection refused, then up".
type sink struct {
	addr   string
	status atomic.Int32
	taken  atomic.Int32 // bodies answered 200
	hits   atomic.Int32 // every request
	mu     sync.Mutex
	srv    *http.Server
	bodies []string
}

func newSink(t *testing.T) *sink {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &sink{addr: l.Addr().String()}
	s.status.Store(200)
	s.serve(l)
	t.Cleanup(s.close)
	return s
}

func (s *sink) serve(l net.Listener) {
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		code := int(s.status.Load())
		if code == 200 {
			s.taken.Add(1)
			var b strings.Builder
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			b.Write(buf[:n])
			s.mu.Lock()
			s.bodies = append(s.bodies, b.String())
			s.mu.Unlock()
		}
		w.WriteHeader(code)
	})}
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()
	go srv.Serve(l)
}

// close stops listening: a connection is refused from now on.
func (s *sink) close() {
	s.mu.Lock()
	srv := s.srv
	s.srv = nil
	s.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}

// reopen listens again on the same port.
func (s *sink) reopen(t *testing.T) {
	t.Helper()
	var l net.Listener
	var err error
	for i := 0; i < 50; i++ {
		if l, err = net.Listen("tcp", s.addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("listen again on %s: %v", s.addr, err)
	}
	s.serve(l)
}

func (s *sink) url() string { return "generic+http://" + s.addr + "/hook?template=json" }

// display is a text the test changes under a running watch.
type liveDisplay struct {
	mu   sync.Mutex
	text string
}

func (d *liveDisplay) Recognize(ctx context.Context, img image.Image) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.text, nil
}

func (d *liveDisplay) show(s string) {
	d.mu.Lock()
	d.text = s
	d.mu.Unlock()
}

// retryRig is one watch under Run, with a fake clock on its sender, a
// real notifier at the sink, and every delivery report on a channel.
type retryRig struct {
	t        *testing.T
	r        *Runner
	disp     *liveDisplay
	clock    *fakeclock.Clock
	reports  chan Delivery
	readings chan string // every reading the watch makes, as far as the buffer holds
	logs     *logBook
	cancel   context.CancelFunc
	done     chan struct{}
}

func startRetryRig(t *testing.T, w config.Watch, n notify.Notifier) *retryRig {
	t.Helper()
	g := &retryRig{t: t, disp: &liveDisplay{text: "idle"}, clock: fakeclock.New(time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)),
		reports: make(chan Delivery, 256), readings: make(chan string, 256), logs: &logBook{}, done: make(chan struct{})}
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, g.disp, n, nil, g.logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	r.clock = g.clock
	r.OnDelivery = func(d Delivery) { g.reports <- d }
	r.OnReading = func(ev trigger.Event, _ image.Image, _ time.Time) {
		select {
		case g.readings <- ev.Reading:
		default:
		}
	}
	g.r = r
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	go func() {
		defer close(g.done)
		r.Run(ctx)
	}()
	t.Cleanup(g.stop)
	return g
}

func (g *retryRig) stop() {
	g.cancel()
	<-g.done
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !g.r.WaitSent(ctx) {
		g.t.Error("the sender goroutine kept running 5 s after the stop")
	}
}

func retryWatch(sinkURL string) config.Watch {
	w := watchCfg(config.Trigger{Type: "ocr_match", Pattern: "GO", Confirm: 1})
	w.Interval = config.Duration(10 * time.Millisecond)
	w.Notify = []string{sinkURL}
	return w
}

// next waits for the next finished report (Pending ones are skipped).
func (g *retryRig) next() Delivery {
	g.t.Helper()
	for {
		select {
		case d := <-g.reports:
			if d.Pending {
				continue
			}
			return d
		case <-time.After(5 * time.Second):
			g.t.Fatalf("no delivery report in 5 s; log:\n%s", strings.Join(g.logs.all(), "\n"))
		}
	}
}

// read waits until the watch has read text.
func (g *retryRig) read(text string) {
	g.t.Helper()
	for {
		select {
		case s := <-g.readings:
			if s == text {
				return
			}
		case <-time.After(5 * time.Second):
			g.t.Fatalf("the watch never read %q; log:\n%s", text, strings.Join(g.logs.all(), "\n"))
		}
	}
}

// quiet checks that no finished report arrives for a while.
func (g *retryRig) quiet(d time.Duration) {
	g.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case r := <-g.reports:
			if !r.Pending {
				g.t.Fatalf("unexpected report %+v", r)
			}
		case <-deadline:
			return
		}
	}
}

func wantRetrying(t *testing.T, d Delivery, tries int, wait time.Duration, now time.Time) {
	t.Helper()
	if d.OK || d.Retrying == "" || d.Tries != tries || !d.NextTry.Equal(now.Add(wait)) {
		t.Fatalf("report = %+v, want retrying, try %d, next in %v", d, tries, wait)
	}
}

// A 503, then a 200: the alert arrives once, after the first wait.
func TestRetryAfterServerError(t *testing.T) {
	sk := newSink(t)
	sk.status.Store(503)
	g := startRetryRig(t, retryWatch(sk.url()), mustShoutrrr(t, sk.url()))
	g.disp.show("GO")
	d := g.next()
	wantRetrying(t, d, 1, 10*time.Second, g.clock.Now())
	if !strings.Contains(d.Retrying, "503") || !strings.HasPrefix(d.Retrying, "line 1 of 1 (generic+http://") {
		t.Errorf("Retrying = %q", d.Retrying)
	}
	sk.status.Store(200)
	g.clock.Add(10 * time.Second)
	if d = g.next(); !d.OK || d.Tries != 2 || d.Retrying != "" || d.Err != "" {
		t.Fatalf("after the wait: %+v, want delivered on the 2nd try", d)
	}
	if !d.SentAt.Equal(g.clock.Now()) {
		t.Errorf("SentAt = %v, want the time of the try that got through (%v), not when it was raised", d.SentAt, g.clock.Now())
	}
	if sk.taken.Load() != 1 || sk.hits.Load() != 2 {
		t.Errorf("sink took %d (of %d requests), want 1 of 2", sk.taken.Load(), sk.hits.Load())
	}
	g.quiet(100 * time.Millisecond)
	if n := strings.Count(strings.Join(g.logs.all(), "\n"), "took the alert after 2 tries"); n != 1 {
		t.Errorf("success logged %d times:\n%s", n, strings.Join(g.logs.all(), "\n"))
	}
}

// Connection refused for 90 s, then up: tried after 10 s, 30 s, 60 s and
// 90 s, delivered once on the 5th try, 5 s after the service is back,
// and the log says so once, not once per try.
func TestRetryAfterConnectionRefused(t *testing.T) {
	sk := newSink(t)
	sk.close()
	g := startRetryRig(t, retryWatch(sk.url()), mustShoutrrr(t, sk.url()))
	g.disp.show("GO")
	t0 := g.clock.Now()
	d := g.next()
	wantRetrying(t, d, 1, 10*time.Second, t0)
	if !strings.Contains(strings.ToLower(d.Retrying), "refused") {
		t.Errorf("Retrying = %q, want the refused connection", d.Retrying)
	}
	g.clock.Add(10 * time.Second)
	wantRetrying(t, g.next(), 2, 20*time.Second, t0.Add(10*time.Second))
	g.clock.Add(20 * time.Second)
	wantRetrying(t, g.next(), 3, 30*time.Second, t0.Add(30*time.Second))
	g.clock.Add(30 * time.Second)
	wantRetrying(t, g.next(), 4, 30*time.Second, t0.Add(60*time.Second))
	// 85 s in: the service is back; the next try is due at 90 s.
	g.clock.Add(25 * time.Second)
	sk.reopen(t)
	g.quiet(50 * time.Millisecond)
	g.clock.Add(5 * time.Second)
	if d = g.next(); !d.OK || d.Tries != 5 {
		t.Fatalf("after the service came back: %+v, want delivered on the 5th try", d)
	}
	if sk.taken.Load() != 1 {
		t.Errorf("sink took %d, want exactly 1", sk.taken.Load())
	}
	all := strings.Join(g.logs.all(), "\n")
	if n := strings.Count(all, "trying again in"); n != 1 {
		t.Errorf("%d 'trying again' lines, want 1 (one per change of state, not per try):\n%s", n, all)
	}
	if !strings.Contains(all, "connection refused); trying again in 10 s") || !strings.Contains(all, "took the alert after 5 tries") {
		t.Errorf("log:\n%s", all)
	}
}

// The condition clears before the service is back: the tries stop, the
// alert is reported dropped, with why and at which URL, and the service
// never gets it.
func TestRetryStopsWhenTheConditionClears(t *testing.T) {
	sk := newSink(t)
	sk.status.Store(503)
	g := startRetryRig(t, retryWatch(sk.url()), mustShoutrrr(t, sk.url()))
	g.disp.show("GO")
	wantRetrying(t, g.next(), 1, 10*time.Second, g.clock.Now())
	g.disp.show("idle")
	d := g.next()
	if d.OK || d.Dropped != "the condition cleared" || d.Err != "" || !d.NextTry.IsZero() ||
		!strings.HasPrefix(d.Retrying, "line 1 of 1 (generic+http://") || !strings.HasSuffix(d.Retrying, "): HTTP 503") {
		t.Fatalf("after the condition cleared: %+v, want dropped, naming the URL and its 503", d)
	}
	sk.status.Store(200)
	g.clock.Add(10 * time.Minute)
	g.quiet(100 * time.Millisecond)
	if sk.taken.Load() != 0 || sk.hits.Load() != 1 {
		t.Errorf("sink: %d taken of %d requests, want 0 of 1", sk.taken.Load(), sk.hits.Load())
	}
	if all := strings.Join(g.logs.all(), "\n"); !strings.Contains(all, "the condition cleared before generic+http://") || !strings.Contains(all, "took the alert (1 try); it is dropped") {
		t.Errorf("log:\n%s", strings.Join(g.logs.all(), "\n"))
	}
}

// A 400 is the request's fault: not tried again, reported as before.
func TestNoRetryOnClientError(t *testing.T) {
	sk := newSink(t)
	sk.status.Store(400)
	g := startRetryRig(t, retryWatch(sk.url()), mustShoutrrr(t, sk.url()))
	g.disp.show("GO")
	d := g.next()
	if d.OK || d.Retrying != "" || d.Tries != 1 || !strings.Contains(d.Err, "400") || !strings.HasPrefix(d.Err, "line 1 of 1 (generic+http://") {
		t.Fatalf("report = %+v, want a plain failure naming the 400", d)
	}
	sk.status.Store(200)
	g.clock.Add(10 * time.Minute)
	g.quiet(100 * time.Millisecond)
	if sk.hits.Load() != 1 {
		t.Errorf("%d requests, want 1: a 400 is not tried again", sk.hits.Load())
	}
}

// A stop ends the tries: nothing is sent after it, even once the service
// is back, and the log says the restart will take it from here.
func TestRetryStopsOnShutdown(t *testing.T) {
	sk := newSink(t)
	sk.status.Store(503)
	g := startRetryRig(t, retryWatch(sk.url()), mustShoutrrr(t, sk.url()))
	g.disp.show("GO")
	wantRetrying(t, g.next(), 1, 10*time.Second, g.clock.Now())
	g.cancel()
	select {
	case <-g.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !g.r.WaitSent(ctx) {
		t.Fatal("the sender goroutine kept running after the stop")
	}
	sk.status.Store(200)
	g.clock.Add(10 * time.Minute)
	time.Sleep(100 * time.Millisecond)
	if sk.hits.Load() != 1 {
		t.Errorf("%d requests, want 1: no try after the stop", sk.hits.Load())
	}
	if all := strings.Join(g.logs.all(), "\n"); !strings.Contains(all, "stopping before generic+http://") ||
		!strings.Contains(all, "(1 try); it goes out again after the restart if the condition still holds") {
		t.Errorf("log:\n%s", all)
	}
}

// Two URLs, one of them down: the one that took the alert is not sent it
// again when the other is tried.
func TestRetryOnlyTheLineThatFailed(t *testing.T) {
	n := &lineFake{labels: []string{"ntfy+http://a", "generic+http://b"}, errs: map[int]error{1: errors.New("status 503")}}
	w := retryWatch("ntfy+http://a/t")
	w.Notify = []string{"ntfy+http://a/t", "generic+http://b/h"}
	g := startRetryRig(t, w, n)
	g.disp.show("GO")
	d := g.next()
	wantRetrying(t, d, 1, 10*time.Second, g.clock.Now())
	if d.Retrying != "line 2 of 2 (generic+http://b): HTTP 503" || d.Err != "" {
		t.Errorf("report = %+v", d)
	}
	n.set(1, nil)
	g.clock.Add(10 * time.Second)
	if d = g.next(); !d.OK || d.Tries != 2 {
		t.Fatalf("after the wait: %+v", d)
	}
	if a, b := n.count(0), n.count(1); a != 1 || b != 2 {
		t.Errorf("sends: line 1 %d, line 2 %d; want 1 and 2", a, b)
	}
}

// A newer alert replaces one still waiting: the old one is let go, the
// new one tried at once.
func TestNewerAlertReplacesTheWaitingOne(t *testing.T) {
	sk := newSink(t)
	sk.status.Store(503)
	w := retryWatch(sk.url())
	w.Trigger = config.Trigger{Type: "ocr_changed", Confirm: 1}
	g := startRetryRig(t, w, mustShoutrrr(t, sk.url()))
	g.disp.show("one")
	g.read("one") // the first settled text is the baseline; "one" fires nothing
	g.disp.show("two")
	d := g.next()
	wantRetrying(t, d, 1, 10*time.Second, g.clock.Now())
	first := d.TS
	sk.status.Store(200)
	g.disp.show("three")
	if d = g.next(); !d.OK || d.Tries != 1 || !d.TS.After(first) {
		t.Fatalf("the newer alert: %+v", d)
	}
	if !strings.Contains(strings.Join(g.logs.all(), "\n"), "a newer alert replaced the one generic+http://") {
		t.Errorf("log:\n%s", strings.Join(g.logs.all(), "\n"))
	}
	g.clock.Add(10 * time.Minute)
	g.quiet(100 * time.Millisecond)
	if sk.taken.Load() != 1 {
		t.Errorf("sink took %d, want 1 (the newer alert only)", sk.taken.Load())
	}
}

// A down alert is tried again while the stream is down, and let go when
// it comes back (the recovered alert then says what matters): either the
// wake finds the stream up first and drops it, or the recovered alert
// arrives first and replaces it. Both leave the service with the
// recovered alert only.
func TestDownAlertRetriedWhileDown(t *testing.T) {
	sk := newSink(t)
	sk.status.Store(503)
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.Interval = config.Duration(10 * time.Millisecond)
	w.HealthAfter = 1
	w.Notify = []string{sk.url()}
	src := &flakySource{fail: true}
	g := &retryRig{t: t, clock: fakeclock.New(time.Now()), reports: make(chan Delivery, 256), logs: &logBook{}, done: make(chan struct{})}
	r, err := New(w, src, nil, mustShoutrrr(t, sk.url()), nil, g.logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	r.clock = g.clock
	r.OnDelivery = func(d Delivery) { g.reports <- d }
	g.r = r
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	go func() {
		defer close(g.done)
		r.Run(ctx)
	}()
	t.Cleanup(g.stop)
	d := g.next()
	if d.Kind != "down" {
		t.Fatalf("first report = %+v, want the down alert", d)
	}
	wantRetrying(t, d, 1, 10*time.Second, g.clock.Now())
	g.clock.Add(10 * time.Second)
	wantRetrying(t, g.next(), 2, 20*time.Second, g.clock.Now())
	// The stream comes back: the down alert is dropped or replaced, the
	// recovered one tried.
	src.fail = false
	for d = g.next(); d.Kind != "recovered"; d = g.next() {
		if d.OK || d.Dropped != "the camera came back" || d.Err != "" {
			t.Errorf("down alert after recovery: %+v", d)
		}
	}
	if d.OK || d.Retrying == "" {
		t.Errorf("recovered alert: %+v, want it waiting for the service", d)
	}
	all := strings.Join(g.logs.all(), "\n")
	if !strings.Contains(all, "the camera came back before generic+http://") && !strings.Contains(all, "a newer alert replaced the one generic+http://") {
		t.Errorf("the down alert was neither dropped nor replaced:\n%s", all)
	}
	sk.status.Store(200)
	g.clock.Add(10 * time.Second)
	if d = g.next(); d.Kind != "recovered" || !d.OK {
		t.Fatalf("after the wait: %+v, want the recovered alert delivered", d)
	}
	g.clock.Add(10 * time.Minute)
	g.quiet(100 * time.Millisecond)
	s := sk.bodiesText()
	if sk.taken.Load() != 1 || !strings.Contains(s, "recovered") || strings.Contains(s, "no reading") {
		t.Errorf("sink took %d: %q; want the recovered alert only", sk.taken.Load(), s)
	}
}

func TestWaitText(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "10 s"}, {30 * time.Second, "30 s"}, {time.Minute, "1 min"}, {2 * time.Minute, "2 min"},
		{5 * time.Minute, "5 min"}, {100 * time.Second, "1 min 40 s"}, {0, "0 s"}, {-3 * time.Second, "0 s"},
		{2*time.Hour + 5*time.Minute + 3*time.Second, "2 h 5 min"}, {3 * time.Hour, "3 h"}, {1499 * time.Millisecond, "1 s"},
	} {
		if got := waitText(c.d); got != c.want {
			t.Errorf("waitText(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func (s *sink) bodiesText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.bodies, "\n")
}

// lineFake is a LineSender whose lines fail as told.
type lineFake struct {
	labels []string
	mu     sync.Mutex
	errs   map[int]error
	counts map[int]int
}

func (f *lineFake) Lines() []string { return f.labels }

func (f *lineFake) Send(ctx context.Context, title, body string) error {
	for i := range f.labels {
		if err := f.SendLine(ctx, i, title, body, nil); err != nil {
			return err
		}
	}
	return nil
}

func (f *lineFake) SendLine(ctx context.Context, line int, title, body string, png []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts == nil {
		f.counts = map[int]int{}
	}
	f.counts[line]++
	return f.errs[line]
}

func (f *lineFake) set(line int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.errs, line)
	} else {
		f.errs[line] = err
	}
}

func (f *lineFake) count(line int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[line]
}

func mustShoutrrr(t *testing.T, urls ...string) *notify.Shoutrrr {
	t.Helper()
	n, err := notify.NewShoutrrr(urls)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The restart re-send and the retry never deliver the same alert twice:
// an alert the retry got through is marked delivered, so the next start
// is quiet; one still waiting at the stop is not, so the next start sends
// it, once.
func TestRetryAndRestartResendNeverSendTwice(t *testing.T) {
	for _, c := range []struct {
		name          string
		upBeforeStop  bool
		wantTakenMore int32 // alerts the sink takes after the restart
	}{
		{"delivered by the retry, then a restart", true, 0},
		{"still waiting at the stop, then a restart", false, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			sk := newSink(t)
			sk.status.Store(503)
			store := openStore(t)
			w := retryWatch(sk.url())
			w.Trigger.Cooldown = tenMin
			g := &retryRig{t: t, disp: &liveDisplay{text: "GO"}, clock: fakeclock.New(time.Now()), reports: make(chan Delivery, 256), logs: &logBook{}, done: make(chan struct{})}
			r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, g.disp, mustShoutrrr(t, sk.url()), store, g.logs.logf)
			if err != nil {
				t.Fatal(err)
			}
			r.clock = g.clock
			r.OnDelivery = func(d Delivery) { g.reports <- d }
			g.r = r
			ctx, cancel := context.WithCancel(context.Background())
			g.cancel = cancel
			go func() {
				defer close(g.done)
				r.Run(ctx)
			}()
			wantRetrying(t, g.next(), 1, 10*time.Second, g.clock.Now())
			if c.upBeforeStop {
				sk.status.Store(200)
				g.clock.Add(10 * time.Second)
				if d := g.next(); !d.OK {
					t.Fatalf("retry: %+v", d)
				}
			}
			g.stop()
			sk.status.Store(200)
			before := sk.taken.Load()

			// The next start, on the same display, as the supervisor makes it.
			r2, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, g.disp, mustShoutrrr(t, sk.url()), store, g.logs.logf)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if _, err := r2.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if got := sk.taken.Load() - before; got != c.wantTakenMore {
				t.Errorf("the restart sent the alert %d times, want %d; log:\n%s", got, c.wantTakenMore, strings.Join(g.logs.all(), "\n"))
			}
			if sk.taken.Load() != 1 {
				t.Errorf("the sink took the alert %d times in all, want once", sk.taken.Load())
			}
		})
	}
}

// Tick without Run (no sender goroutine) still tries once and reports,
// as before: nothing waits on a clock nobody moves.
func TestTickWithoutRunTriesOnce(t *testing.T) {
	sk := newSink(t)
	sk.status.Store(503)
	w := retryWatch(sk.url())
	disp := &liveDisplay{text: "GO"}
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, disp, mustShoutrrr(t, sk.url()), nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var got []Delivery
	r.OnDelivery = func(d Delivery) { got = append(got, d) }
	ev, err := r.Tick(context.Background())
	if err != nil || !ev.Fired {
		t.Fatalf("Tick: %+v, %v", ev, err)
	}
	if len(got) != 1 || got[0].OK || got[0].Retrying != "" || !strings.Contains(got[0].Err, "503") {
		t.Errorf("reports = %+v, want one plain failure", got)
	}
}

// The schedule gets an alert out within 30 s of the service coming back,
// for any outage that ends in the first two and a half minutes after the
// alert (the acceptance's "back within 30 s", for the outages round 6
// measured), and settles at every 5 min after that.
func TestRetryScheduleIsWithin30sEarlyOn(t *testing.T) {
	var at []time.Duration // when each try happens, after the first
	var sum time.Duration
	for n := 1; sum < 3*time.Hour; n++ {
		sum += retryDelay(n)
		at = append(at, sum)
	}
	for back := time.Second; back <= 150*time.Second; back += time.Second {
		for _, a := range at {
			if a >= back {
				if a-back > 30*time.Second {
					t.Errorf("a service back %v after the alert gets it %v later", back, a-back)
				}
				break
			}
		}
	}
	for n, want := range map[int]time.Duration{1: 10 * time.Second, 2: 20 * time.Second, 3: 30 * time.Second, 6: 30 * time.Second,
		7: time.Minute, 8: 2 * time.Minute, 9: 5 * time.Minute, 10: 5 * time.Minute, 500: 5 * time.Minute} {
		if got := retryDelay(n); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", n, got, want)
		}
	}
}

// blipSource fails its grabs while fail is set.
type blipSource struct{ fail atomic.Bool }

func (s *blipSource) Grab(ctx context.Context) (image.Image, error) {
	if s.fail.Load() {
		return nil, errors.New("connection refused")
	}
	return flat(10, 10, 128), nil
}

// A fire alert waiting for a service that answers 503 survives a camera
// blip (down, then recovered): the stream alerts have a slot of their
// own, so once the service is back it gets the fire alert, once, as well
// as the recovered one. While the stream alerts come and go, the page
// keeps hearing that the fire alert is still being tried.
func TestFireAlertSurvivesACameraBlip(t *testing.T) {
	sk := newSink(t)
	sk.status.Store(503)
	w := retryWatch(sk.url())
	w.HealthAfter = 1
	src := &blipSource{}
	disp := &liveDisplay{text: "idle"}
	g := &retryRig{t: t, disp: disp, clock: fakeclock.New(time.Now()), reports: make(chan Delivery, 256), logs: &logBook{}, done: make(chan struct{})}
	r, err := New(w, src, disp, mustShoutrrr(t, sk.url()), nil, g.logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	r.clock = g.clock
	r.OnDelivery = func(d Delivery) { g.reports <- d }
	g.r = r
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	go func() {
		defer close(g.done)
		r.Run(ctx)
	}()
	t.Cleanup(g.stop)

	disp.show("GO")
	d := g.next()
	if d.Kind != "fired" || d.Retrying == "" {
		t.Fatalf("first report %+v, want the fire waiting", d)
	}
	src.fail.Store(true)
	for d = g.next(); d.Kind != "down"; d = g.next() {
	}
	src.fail.Store(false)
	for d = g.next(); d.Kind != "recovered"; d = g.next() {
	}
	sk.status.Store(200)
	for i := 0; i < 30 && sk.taken.Load() < 2; i++ {
		g.clock.Add(10 * time.Second)
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	body := sk.bodiesText()
	if n := strings.Count(body, "pattern matched"); n != 1 {
		t.Errorf("the fire alert reached the service %d times, want once; bodies:\n%s\nlog:\n%s", n, body, strings.Join(g.logs.all(), "\n"))
	}
	if !strings.Contains(body, "recovered") || strings.Contains(body, "(down)") {
		t.Errorf("stream alerts at the service: %q, want the recovered one only", body)
	}
	if all := strings.Join(g.logs.all(), "\n"); strings.Contains(all, "notify (fired): a newer alert replaced") {
		t.Errorf("a stream alert replaced the fire alert:\n%s", all)
	}
}

// While the fire alert is still being tried and a stream alert goes
// through, the last report the page gets is the fire alert's: that is the
// one that still needs watching. The stream alert's own report comes
// first, so the registry hears it settled (its "sending" mark goes), and
// the whole thing goes through the registry as the supervisor feeds it.
func TestPageKeepsTheAlertStillWaiting(t *testing.T) {
	n := &lineFake{labels: []string{"ntfy+http://a"}, errs: map[int]error{0: errors.New("status 503")}}
	w := retryWatch("ntfy+http://a/t")
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, &liveDisplay{text: "idle"}, n, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	clk := fakeclock.New(time.Now())
	r.clock = clk
	var got []Delivery
	reg := state.New(5)
	r.OnDelivery = func(d Delivery) {
		got = append(got, d)
		reg.SetDelivery("w", state.Delivery{TS: d.TS, Kind: d.Kind, OK: d.OK, Err: d.Err, Retrying: d.Retrying,
			NextTry: d.NextTry, Tries: d.Tries, Dropped: d.Dropped, SentAt: d.SentAt})
	}
	r.condMu.Lock()
	r.cond = condition{fire: true}
	r.condMu.Unlock()
	s := newSender(r)
	t0 := clk.Now()
	reg.SetSending("w", t0)
	s.take(context.Background(), alert{ts: t0, kind: "fired"})
	n.set(0, nil)
	t1 := t0.Add(time.Second)
	reg.SetSending("w", t1)
	s.take(context.Background(), alert{ts: t1, kind: "down"})
	if len(got) < 2 || got[len(got)-2].Kind != "down" || !got[len(got)-2].OK {
		t.Fatalf("reports %+v: the down alert's own report should come before the fire's", got)
	}
	last := got[len(got)-1]
	if last.Kind != "fired" || last.Retrying == "" || !last.TS.Equal(t0) {
		t.Fatalf("after the down alert went through: %+v, want the fire alert, still waiting", last)
	}
	if d, _ := reg.GetDelivery("w"); d.Kind != "fired" || d.Retrying == "" {
		t.Errorf("registry: %+v, want the fire alert waiting", d)
	}
	if _, sending := reg.Sending("w"); sending {
		t.Error("the down alert's sending mark outlived its delivery")
	}
	clk.Add(10 * time.Second)
	s.retry(context.Background())
	last = got[len(got)-1]
	if last.Kind != "fired" || !last.OK || last.Tries != 2 || !last.SentAt.Equal(clk.Now()) {
		t.Fatalf("after the fire alert went through: %+v", last)
	}
	if d, _ := reg.GetDelivery("w"); d.Kind != "fired" || !d.OK {
		t.Errorf("registry after the fire alert went through: %+v", d)
	}
}

// Two URLs: the first took the alert, the second answered 503, and then
// watchglass stopped. After the restart, with the condition still
// holding, the alert goes to the second URL only. If the watch sees the
// condition clear first, the next fire is a new alert, for both.
func TestRestartResendSkipsURLsThatTookIt(t *testing.T) {
	for _, c := range []struct {
		name       string
		clearFirst bool
		want0      int // sends to the first URL in all
	}{
		{"condition still holds", false, 1},
		{"condition cleared and came back", true, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			n := &lineFake{labels: []string{"ntfy+http://a", "generic+http://b"}, errs: map[int]error{1: errors.New("status 503")}}
			store := openStore(t)
			w := retryWatch("ntfy+http://a/t")
			w.Notify = []string{"ntfy+http://a/t", "generic+http://b/h"}
			w.Trigger.Cooldown = tenMin
			g := &retryRig{t: t, disp: &liveDisplay{text: "GO"}, clock: fakeclock.New(time.Now()), reports: make(chan Delivery, 256), logs: &logBook{}, done: make(chan struct{})}
			r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, g.disp, n, store, g.logs.logf)
			if err != nil {
				t.Fatal(err)
			}
			r.clock = g.clock
			r.OnDelivery = func(d Delivery) { g.reports <- d }
			g.r = r
			ctx, cancel := context.WithCancel(context.Background())
			g.cancel = cancel
			go func() {
				defer close(g.done)
				r.Run(ctx)
			}()
			wantRetrying(t, g.next(), 1, 10*time.Second, g.clock.Now())
			g.stop()
			n.set(1, nil)

			r2, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, g.disp, n, store, g.logs.logf)
			if err != nil {
				t.Fatal(err)
			}
			var got []Delivery
			r2.OnDelivery = func(d Delivery) { got = append(got, d) }
			if c.clearFirst {
				g.disp.show("idle")
				for i := 0; i < 2; i++ {
					r2.Tick(context.Background())
				}
				g.disp.show("GO")
			}
			for i := 0; i < 3; i++ {
				if _, err := r2.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if a, b := n.count(0), n.count(1); a != c.want0 || b != 2 {
				t.Errorf("sends: first URL %d, second %d; want %d and 2; log:\n%s", a, b, c.want0, strings.Join(g.logs.all(), "\n"))
			}
			if len(got) == 0 || !got[len(got)-1].OK {
				t.Errorf("reports after the restart: %+v, want the alert delivered", got)
			}
			// Delivered now: a third start sends nothing.
			r3, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, g.disp, n, store, g.logs.logf)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				r3.Tick(context.Background())
			}
			if a, b := n.count(0), n.count(1); a != c.want0 || b != 2 {
				t.Errorf("after a third start: first URL %d, second %d", a, b)
			}
		})
	}
}

// slowLine answers after a delay, or at once when ctx ends.
type slowLine struct {
	mu    sync.Mutex
	delay time.Duration
	err   error
}

func (f *slowLine) Lines() []string { return []string{"generic+http://slow"} }
func (f *slowLine) Send(ctx context.Context, title, body string) error {
	return f.SendLine(ctx, 0, title, body, nil)
}
func (f *slowLine) SendLine(ctx context.Context, line int, title, body string, png []byte) error {
	f.mu.Lock()
	d, err := f.delay, f.err
	f.mu.Unlock()
	select {
	case <-time.After(d):
		return err
	case <-ctx.Done():
		return errors.New(`Post "http://slow/h": ` + ctx.Err().Error())
	}
}

// A stop that lands while a retry is in flight ends it without counting
// it as a try or logging it as a failure to try again.
func TestStopDuringAnInFlightRetryIsNotATry(t *testing.T) {
	f := &slowLine{err: errors.New("status 503")}
	g := startRetryRig(t, retryWatch("generic+http://slow/h"), f)
	g.disp.show("GO")
	wantRetrying(t, g.next(), 1, 10*time.Second, g.clock.Now())
	f.mu.Lock()
	f.delay = 5 * time.Second
	f.mu.Unlock()
	g.clock.Add(10 * time.Second)
	time.Sleep(200 * time.Millisecond) // the retry is now in flight
	g.stop()
	all := strings.Join(g.logs.all(), "\n")
	if strings.Contains(all, "canceled") {
		t.Errorf("the stop was logged as a failure:\n%s", all)
	}
	if !strings.Contains(all, "(1 try); it goes out again after the restart") {
		t.Errorf("the stop line should count the one real try:\n%s", all)
	}
}

func TestTriesText(t *testing.T) {
	if triesText(1) != "1 try" || triesText(2) != "2 tries" || triesText(0) != "0 tries" {
		t.Errorf("triesText: %q %q %q", triesText(1), triesText(2), triesText(0))
	}
}

// A refused Telegram token answers in words only ("Unauthorized"): it is
// not tried again, and the record says so as a plain failure.
func TestNoRetryOnAnAnswerInWords(t *testing.T) {
	n := &lineFake{labels: []string{"telegram://api.telegram.org"}, errs: map[int]error{0: errors.New("failed to send telegram notification: Unauthorized")}}
	w := retryWatch("telegram://token@telegram?chats=1")
	g := startRetryRig(t, w, n)
	g.disp.show("GO")
	d := g.next()
	if d.OK || d.Retrying != "" || !strings.Contains(d.Err, "Unauthorized") {
		t.Fatalf("report = %+v, want a plain failure", d)
	}
	g.clock.Add(time.Hour)
	g.quiet(100 * time.Millisecond)
	if n.count(0) != 1 {
		t.Errorf("%d sends, want 1", n.count(0))
	}
}

// A change alert still waiting at a stop is dropped, and the stop line
// says so: the restarted watch takes what it sees as its starting point,
// so nothing sends it again.
func TestStopDropsAWaitingChangeAlert(t *testing.T) {
	sk := newSink(t)
	sk.status.Store(503)
	w := retryWatch(sk.url())
	w.Trigger = config.Trigger{Type: "ocr_changed", Confirm: 1}
	g := startRetryRig(t, w, mustShoutrrr(t, sk.url()))
	g.disp.show("one")
	g.read("one")
	g.disp.show("two")
	wantRetrying(t, g.next(), 1, 10*time.Second, g.clock.Now())
	g.stop()
	if all := strings.Join(g.logs.all(), "\n"); !strings.Contains(all, "(1 try); it is dropped") || strings.Contains(all, "goes out again") {
		t.Errorf("log:\n%s", all)
	}
}
