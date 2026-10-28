package runner

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// panicOCR panics on every frame, the way the seven-segment decoder did
// on an angled clock photo with Binarize set.
type panicOCR struct{ calls atomic.Int32 }

func (p *panicOCR) Recognize(ctx context.Context, img image.Image) (string, error) {
	p.calls.Add(1)
	var counts []int
	i := -1
	return fmt.Sprint(counts[i]), nil // index out of range [-1]
}

// logBook collects a runner's log lines; Run logs from two goroutines.
type logBook struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBook) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logBook) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func TestTickContainsEnginePanic(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	w := watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1})
	w.HealthAfter = 2
	var logs logBook
	r, err := New(w, src, &panicOCR{}, nil, nil, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	var crashes []*CrashError
	r.OnCrash = func(ce *CrashError) { crashes = append(crashes, ce) }
	var downs []health.Event
	r.OnHealth = func(hev health.Event) { downs = append(downs, hev) }
	var readings int
	r.OnReading = func(trigger.Event, image.Image, time.Time) { readings++ }

	for i := 1; i <= 3; i++ {
		_, err := r.Tick(context.Background())
		if !errors.Is(err, ErrCrashed) {
			t.Fatalf("tick %d: err = %v, want ErrCrashed", i, err)
		}
		if !strings.Contains(err.Error(), "index out of range [-1]") {
			t.Errorf("tick %d: the error doesn't say what panicked: %v", i, err)
		}
	}
	if len(crashes) != 3 {
		t.Fatalf("OnCrash heard %d crashes, want 3", len(crashes))
	}
	if len(crashes[0].Stack) == 0 || !strings.Contains(string(crashes[0].Stack), "panicOCR") {
		t.Errorf("the crash carries no stack through the engine:\n%s", crashes[0].Stack)
	}
	if readings != 0 {
		t.Errorf("a crashed read produced %d readings", readings)
	}
	// Like any read that produced nothing: down after health_after.
	if len(downs) != 1 || downs[0].State != "down" || !strings.Contains(downs[0].Message, ErrCrashed.Error()) {
		t.Errorf("health events = %+v, want one down saying the reader crashed", downs)
	}
	// The stack is logged once, not on every poll.
	stacks := 0
	for _, l := range logs.all() {
		if strings.Contains(l, "[running]") {
			stacks++
		}
	}
	if stacks != 1 {
		t.Errorf("stack logged %d times, want once:\n%s", stacks, strings.Join(logs.all(), "\n"))
	}
}

// brokenFrame panics when its pixels are read: a crash in the crop and
// preprocessing, before any engine, for either kind of watch.
type brokenFrame struct{}

func (brokenFrame) ColorModel() color.Model { return color.RGBAModel }
func (brokenFrame) Bounds() image.Rectangle { return image.Rect(0, 0, 10, 10) }
func (brokenFrame) At(x, y int) color.Color { panic("pixel out of reach") }

func TestTickContainsPanicOutsideTheEngine(t *testing.T) {
	for _, typ := range []string{"ocr_changed", "pixel_change"} {
		t.Run(typ, func(t *testing.T) {
			ocr := &fakeOCR{texts: []string{"A"}}
			r, err := New(watchCfg(config.Trigger{Type: typ, Confirm: 1, Threshold: 10}), &fakeSource{imgs: []image.Image{brokenFrame{}}}, ocr, nil, nil, t.Logf)
			if err != nil {
				t.Fatal(err)
			}
			crashes := 0
			r.OnCrash = func(*CrashError) { crashes++ }
			func() {
				defer func() {
					if v := recover(); v != nil {
						t.Fatalf("Tick let a panic through: %v", v)
					}
				}()
				if _, err := r.Tick(context.Background()); !errors.Is(err, ErrCrashed) || !strings.Contains(err.Error(), "pixel out of reach") {
					t.Fatalf("Tick: %v, want the crash", err)
				}
			}()
			if crashes != 1 || ocr.i != 0 {
				t.Errorf("OnCrash heard %d crashes, the engine read %d frames; want 1 and 0", crashes, ocr.i)
			}
		})
	}
}

// The process keeps going: one watch whose every frame crashes the
// reader, one beside it that reads, both under Run as the supervisor runs
// them. The second keeps producing readings; the first keeps polling and
// says each time that it crashed.
func TestRunKeepsOtherWatchesGoingWhenOneCrashes(t *testing.T) {
	newWatch := func(name string, engine interface {
		Recognize(context.Context, image.Image) (string, error)
	}, logf func(string, ...any)) *Runner {
		w := watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1})
		w.Name = name
		w.Interval = config.Duration(10 * time.Millisecond)
		r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, engine, nil, nil, logf)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	var logs logBook
	bad := newWatch("bad", &panicOCR{}, logs.logf)
	good := newWatch("good", &fakeOCR{texts: []string{"1", "2", "3"}}, t.Logf)
	var crashes, readings atomic.Int32
	bad.OnCrash = func(*CrashError) { crashes.Add(1) }
	good.OnReading = func(trigger.Event, image.Image, time.Time) { readings.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, r := range []*Runner{bad, good} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Run(ctx)
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for (crashes.Load() < 5 || readings.Load() < 5) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	if crashes.Load() < 5 || readings.Load() < 5 {
		t.Fatalf("after 5 s: %d crashes on the bad watch, %d readings on the good one; want 5 of each", crashes.Load(), readings.Load())
	}
	stacks := 0
	for _, l := range logs.all() {
		if strings.Contains(l, "[running]") {
			stacks++
		}
	}
	if stacks != 1 {
		t.Errorf("the bad watch logged its stack %d times, want once", stacks)
	}
}

// A panic in a callback Tick calls after the read (here OnReading, the
// web UI's feed) is caught by Run, and polling goes on.
func TestRunContainsPanicAfterTheRead(t *testing.T) {
	w := watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1})
	w.Interval = config.Duration(10 * time.Millisecond)
	var logs logBook
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, &fakeOCR{texts: []string{"1", "2"}}, nil, nil, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	var ticks, crashes atomic.Int32
	r.OnReading = func(trigger.Event, image.Image, time.Time) {
		ticks.Add(1)
		panic("feed broke")
	}
	r.OnCrash = func(*CrashError) { crashes.Add(1) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for ticks.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if ticks.Load() < 3 || crashes.Load() < 3 {
		t.Fatalf("%d polls, %d crashes heard; want polling to go on past the panics", ticks.Load(), crashes.Load())
	}
}

// panicSource panics in Grab, the way a parser meeting a stream or
// snapshot it didn't expect would.
type panicSource struct{}

func (panicSource) Grab(ctx context.Context) (image.Image, error) {
	var m map[string]int
	m["frame"]++ // assignment to entry in nil map
	return nil, nil
}

// A source that panics on every poll is a camera that never gives a
// frame: under Run, as the supervisor runs it, the watch goes down after
// health_after polls, so the down alert and Home Assistant hear of it.
func TestRunGrabPanicCountsTowardHealth(t *testing.T) {
	w := watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1})
	w.Interval = config.Duration(10 * time.Millisecond)
	w.HealthAfter = 2
	var logs logBook
	ocr := &fakeOCR{texts: []string{"1"}}
	r, err := New(w, panicSource{}, ocr, nil, nil, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	var crashes atomic.Int32
	r.OnCrash = func(*CrashError) { crashes.Add(1) }
	var mu sync.Mutex
	var events []health.Event
	r.OnHealth = func(hev health.Event) {
		mu.Lock()
		events = append(events, hev)
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for crashes.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if crashes.Load() < 4 {
		t.Fatalf("OnCrash heard %d crashes in 5 s, want 4 or more", crashes.Load())
	}
	if len(events) != 1 || events[0].State != "down" || !strings.Contains(events[0].Message, "grab: ") || !strings.Contains(events[0].Message, "nil map") {
		t.Fatalf("health events = %+v, want one down naming the crashed grab", events)
	}
	if ocr.i != 0 {
		t.Errorf("the engine read %d frames, want none", ocr.i)
	}
	logged := strings.Join(logs.all(), "\n")
	if !strings.Contains(logged, "watch test-watch: "+StepGrab.Sentence()) || strings.Count(logged, "[running]") != 1 {
		t.Errorf("want the crash logged with the grab sentence, its stack once:\n%s", logged)
	}
	// The words name the source, not the reader or a frame: there is none.
	if m := events[0].Message; strings.Contains(m, ErrCrashed.Error()) {
		t.Errorf("a grab crash is blamed on the reader: %q", m)
	}
}

// A panic in OnReading (the Live panel, the Home Assistant publisher)
// comes after the trigger decided and the saved state says it fired: the
// alert still goes out.
func TestPanicInOnReadingStillSendsTheAlert(t *testing.T) {
	notifier := &fakeNotifier{}
	r, err := New(watchCfg(config.Trigger{Type: "ocr_match", Pattern: "GO", Confirm: 1}),
		&fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, &fakeOCR{texts: []string{"GO"}}, notifier, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	r.OnReading = func(trigger.Event, image.Image, time.Time) { panic("feed broke") }
	crashes := 0
	r.OnCrash = func(*CrashError) { crashes++ }
	var ev trigger.Event
	func() {
		defer func() {
			if v := recover(); v != nil {
				t.Fatalf("Tick let the panic through: %v", v)
			}
		}()
		ev, err = r.Tick(context.Background())
	}()
	if err != nil || !ev.Fired {
		t.Fatalf("Tick: %+v, %v; want a fire and no error", ev, err)
	}
	if len(notifier.sent) != 1 || crashes != 1 {
		t.Fatalf("sent %q, %d crashes heard; want the alert sent and the crash heard once", notifier.sent, crashes)
	}
}

// seqOCR reads a string and panics with any other value, in order.
type seqOCR struct {
	steps []any
	n     int
}

func (s *seqOCR) Recognize(ctx context.Context, img image.Image) (string, error) {
	v := s.steps[s.n%len(s.steps)]
	s.n++
	if text, ok := v.(string); ok {
		return text, nil
	}
	panic(v)
}

// The stack is logged once per distinct panic in a run of crashes, and
// again once the watch has read in between: a watch that runs for weeks
// and meets a second bug logs that one's stack too, and a bug that comes
// back after a recovery is logged with its stack again. The same panic
// on consecutive polls stays one stack.
func TestCrashStackLoggedForEachNewPanic(t *testing.T) {
	w := watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1})
	w.HealthAfter = 100
	var logs logBook
	engine := &seqOCR{steps: []any{errors.New("bug A"), errors.New("bug A"), errors.New("bug B"), "1", errors.New("bug A")}}
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, engine, nil, nil, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	var firsts []time.Time
	r.OnCrash = func(ce *CrashError) { firsts = append(firsts, ce.First) }
	for i := 0; i < len(engine.steps); i++ {
		if i == len(engine.steps)-1 {
			time.Sleep(2 * time.Millisecond) // so the new run's First can differ
		}
		r.Tick(context.Background())
	}
	var withStack []string
	for _, l := range logs.all() {
		if strings.Contains(l, "[running]") {
			withStack = append(withStack, strings.SplitN(l, "\n", 2)[0])
		}
	}
	want := []string{"bug A", "bug B", "bug A"}
	if len(withStack) != len(want) {
		t.Fatalf("stacks logged for %q, want one each for %q:\n%s", withStack, want, strings.Join(logs.all(), "\n"))
	}
	for i, l := range withStack {
		if !strings.Contains(l, "("+want[i]+")") {
			t.Errorf("stack %d is for %q, want %q", i+1, l, want[i])
		}
	}
	// The first three crashes are one run, failing since the first; the
	// reading ended it, and the crash after it is a new run.
	if len(firsts) != 4 || !firsts[1].Equal(firsts[0]) || !firsts[2].Equal(firsts[0]) || !firsts[3].After(firsts[0]) {
		t.Errorf("First of each crash = %v; want the first three equal and the fourth later", firsts)
	}
}

// A consumer that crashes on every reading (OnReading: the Home Assistant
// publisher) is reported for what it is: the reading was made, and the
// watch has been failing since the first of those crashes, not since the
// newest one.
func TestConsumerCrashKeepsTheReadingAndItsFirstTime(t *testing.T) {
	w := watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1})
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, &fakeOCR{texts: []string{"1"}}, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	broken := func(trigger.Event, image.Image, time.Time) { panic("ha publisher broke") }
	r.OnReading = broken
	var crashes []*CrashError
	r.OnCrash = func(ce *CrashError) { crashes = append(crashes, ce) }
	ends := 0
	r.OnCrashEnd = func() { ends++ }
	for i := 0; i < 3; i++ {
		if ev, err := r.Tick(context.Background()); err != nil || ev.Reading != "1" {
			t.Fatalf("tick %d: %+v, %v; want the reading and no error", i+1, ev, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(crashes) != 3 {
		t.Fatalf("OnCrash heard %d crashes, want 3", len(crashes))
	}
	// Each poll made a reading and then crashed: none of them was clean,
	// so the watch stays in error between its readings.
	if ends != 0 {
		t.Errorf("OnCrashEnd heard %d ends while every poll crashed, want none", ends)
	}
	for i, ce := range crashes {
		if ce.Step != StepAfter || !strings.Contains(ce.Error(), StepAfter.Sentence()) || strings.Contains(ce.Error(), ErrCrashed.Error()) {
			t.Errorf("crash %d: step %q, %q; want the after-the-read sentence, not the reader's", i+1, ce.Step, ce)
		}
		if !ce.First.Equal(crashes[0].First) {
			t.Errorf("crash %d: First %v moved from %v", i+1, ce.First, crashes[0].First)
		}
	}
	// A poll whose consumer works ends the run; the next crash is a new one.
	r.OnReading = nil
	r.Tick(context.Background())
	if ends != 1 {
		t.Errorf("OnCrashEnd heard %d ends after a clean poll, want 1", ends)
	}
	r.Tick(context.Background())
	if ends != 1 {
		t.Errorf("OnCrashEnd heard %d ends; a second clean poll ends no run", ends)
	}
	time.Sleep(2 * time.Millisecond)
	r.OnReading = broken
	r.Tick(context.Background())
	if len(crashes) != 4 || !crashes[3].First.After(crashes[0].First) {
		t.Errorf("after a clean poll the crash should start a new run: First %v vs %v", crashes[3].First, crashes[0].First)
	}
}

// errOnceOCR fails its first read and reads after that.
type errOnceOCR struct{ n int }

func (f *errOnceOCR) Recognize(ctx context.Context, img image.Image) (string, error) {
	f.n++
	if f.n == 1 {
		return "", errors.New("tesseract exited 1")
	}
	return "1", nil
}

// A panic Run catches outside the read (here OnHealth, on the down and
// the recovered transitions) is not blamed on the reader or the frame:
// nothing about them was wrong.
func TestCrashOutsideTheReadIsNotBlamedOnTheFrame(t *testing.T) {
	w := watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1})
	w.Interval = config.Duration(10 * time.Millisecond)
	w.HealthAfter = 1
	var logs logBook
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, &errOnceOCR{}, nil, nil, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	r.OnHealth = func(health.Event) { panic("registry map broke") }
	var mu sync.Mutex
	var crashes []*CrashError
	r.OnCrash = func(ce *CrashError) {
		mu.Lock()
		crashes = append(crashes, ce)
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(crashes)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(crashes) < 2 {
		t.Fatalf("OnCrash heard %d crashes, want the down and the recovered transitions", len(crashes))
	}
	for _, ce := range crashes {
		if ce.Step != StepPoll || !strings.Contains(ce.Error(), StepPoll.Sentence()) || strings.Contains(ce.Error(), "frame") {
			t.Errorf("crash reads %q (step %q); want the poll sentence, nothing about a frame", ce, ce.Step)
		}
	}
	if n := strings.Count(strings.Join(logs.all(), "\n"), "[running]"); n != 1 {
		t.Errorf("stack logged %d times for one panic value, want once", n)
	}
}

// leakyNotifier panics with a value that carries its service URL, token
// and all, as shoutrrr's errors do.
type leakyNotifier struct{}

func (leakyNotifier) Send(ctx context.Context, title, body string) error {
	panic("router broke on ntfy://ntfy.example.net/topic?token=tk_notreal_4711")
}

// The recover in send scrubs the panic value like every error the
// notifier returns: a token in it never reaches the log.
func TestSendPanicValueIsScrubbed(t *testing.T) {
	w := watchCfg(config.Trigger{Type: "ocr_match", Pattern: "GO", Confirm: 1})
	w.Notify = []string{"ntfy://ntfy.example.net/topic?token=tk_notreal_4711"}
	var logs logBook
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, &fakeOCR{texts: []string{"GO"}}, leakyNotifier{}, nil, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	var errs []string
	r.OnDelivery = func(d Delivery) {
		if !d.Pending && !d.OK {
			errs = append(errs, d.Err)
		}
	}
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	logged := strings.Join(logs.all(), "\n")
	if !strings.Contains(logged, "sending the fired alert crashed") || !strings.Contains(logged, "[running]") {
		t.Fatalf("the send's panic wasn't logged with its stack:\n%s", logged)
	}
	if strings.Contains(logged, "tk_notreal_4711") || strings.Contains(logged, "/topic") {
		t.Errorf("the token reached the log:\n%s", logged)
	}
	if len(errs) != 1 || strings.Contains(errs[0], "tk_notreal_4711") || !strings.Contains(errs[0], "sending crashed") {
		t.Errorf("delivery errors = %q; want one, scrubbed", errs)
	}
}

// A panic after the send, on the sender goroutine (here OnDelivery, the
// supervisor's report), fails nothing but that alert's bookkeeping: it is
// logged with its stack, the goroutine lives, and the next alert still
// goes out.
func TestPanicAfterTheSendKeepsTheSenderGoing(t *testing.T) {
	w := watchCfg(config.Trigger{Type: "ocr_match", Pattern: "GO", Confirm: 1})
	w.Interval = config.Duration(10 * time.Millisecond)
	var logs logBook
	notifier := &fakeNotifier{}
	// Leaving a met condition takes twice Confirm readings, so "no" twice.
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, &fakeOCR{texts: []string{"GO", "no", "no"}}, notifier, nil, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	var finished atomic.Int32
	r.OnDelivery = func(d Delivery) {
		if d.Pending {
			return
		}
		if finished.Add(1) == 1 {
			panic("supervisor broke after the send")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for finished.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	r.WaitSent(context.Background())
	if finished.Load() < 2 || len(notifier.sent) < 2 {
		t.Fatalf("%d deliveries finished, %d alerts sent; want the sender to go on past the panic", finished.Load(), len(notifier.sent))
	}
	logged := strings.Join(logs.all(), "\n")
	if !strings.Contains(logged, "after sending the fired alert, watchglass crashed") || !strings.Contains(logged, "[running]") {
		t.Errorf("the panic after the send wasn't logged with its stack:\n%s", logged)
	}
}

// panicNotifier panics in the notification library.
type panicNotifier struct{}

func (panicNotifier) Send(ctx context.Context, title, body string) error { panic("router broke") }

// A panic sending an alert fails that alert, reported like any failed
// send, and the sender goroutine (and with it the process) lives on.
func TestSendPanicFailsTheAlertOnly(t *testing.T) {
	w := watchCfg(config.Trigger{Type: "ocr_match", Pattern: "GO", Confirm: 1})
	w.Interval = config.Duration(10 * time.Millisecond)
	// Leaving a met condition takes twice Confirm readings, so "no" twice.
	r, err := New(w, &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}, &fakeOCR{texts: []string{"GO", "no", "no"}}, panicNotifier{}, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var failed []string
	r.OnDelivery = func(d Delivery) {
		if !d.Pending && !d.OK {
			mu.Lock()
			failed = append(failed, d.Err)
			mu.Unlock()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(failed)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	r.WaitSent(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(failed) < 2 || !strings.Contains(failed[0], "sending crashed") {
		t.Fatalf("failed deliveries = %q, want two or more saying the send crashed", failed)
	}
}
