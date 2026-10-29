package runner

import (
	"context"
	"image"
	"image/color"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/fakeclock"
	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// panel is a fake cycling display: each frame's left half is the code
// box, its right half the value, and each half is one flat colour that
// stands for the text it shows. panelOCR reads a crop by its colour, so a
// watch whose boxes are the two halves reads both from the same frame,
// the way a camera and a real engine would.
type panel struct {
	mu    sync.Mutex
	texts map[color.RGBA]string
	next  uint8
}

func newPanel() *panel { return &panel{texts: map[color.RGBA]string{}} }

func (p *panel) colour(text string) color.RGBA {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c, t := range p.texts {
		if t == text {
			return c
		}
	}
	p.next++
	c := color.RGBA{R: p.next, G: 255 - p.next, B: 77, A: 255}
	p.texts[c] = text
	return c
}

// frame is a 20x10 frame showing code in the gate box and value beside it.
func (p *panel) frame(code, value string) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	left, right := p.colour(code), p.colour(value)
	for y := 0; y < 10; y++ {
		for x := 0; x < 20; x++ {
			if x < 10 {
				img.SetRGBA(x, y, left)
			} else {
				img.SetRGBA(x, y, right)
			}
		}
	}
	return img
}

// panelOCR reads a crop's text from its colour, and counts its reads.
type panelOCR struct {
	p     *panel
	mu    sync.Mutex
	reads []string
}

func (o *panelOCR) Recognize(ctx context.Context, img image.Image) (string, error) {
	b := img.Bounds()
	c := color.RGBAModel.Convert(img.At(b.Min.X, b.Min.Y)).(color.RGBA)
	o.p.mu.Lock()
	t := o.p.texts[c]
	o.p.mu.Unlock()
	o.mu.Lock()
	o.reads = append(o.reads, t)
	o.mu.Unlock()
	return t, nil
}

// frames serves whatever the test last put on the panel.
type frames struct{ img image.Image }

func (f *frames) Grab(ctx context.Context) (image.Image, error) { return f.img, nil }

// gateRig is a gated watch on a panel: the gate box is the left half, the
// value the right.
type gateRig struct {
	t      *testing.T
	p      *panel
	src    *frames
	eng    *panelOCR
	store  *history.Store
	clock  *fakeclock.Clock
	logs   *logs
	events []trigger.Event
	health []health.Event
	n      *fakeNotifier
}

func newGateRig(t *testing.T, store *history.Store) *gateRig {
	p := newPanel()
	return &gateRig{t: t, p: p, src: &frames{}, eng: &panelOCR{p: p}, store: store,
		clock: fakeclock.New(time.Date(2026, 10, 28, 9, 0, 0, 0, time.Local)), logs: &logs{}, n: &fakeNotifier{}}
}

func gatedWatch(tr config.Trigger, pattern string) config.Watch {
	w := textWatch(tr)
	w.Region = config.Region{X: 0.5, Y: 0, W: 0.5, H: 1}
	w.Gate = &config.Gate{Region: config.Region{X: 0, Y: 0, W: 0.5, H: 1}, Pattern: pattern}
	w.HealthAfter = 3
	return w
}

func (g *gateRig) start(w config.Watch) *Runner {
	g.t.Helper()
	r, err := New(w, g.src, g.eng, g.n, g.store, g.logs.logf)
	if err != nil {
		g.t.Fatalf("New: %v", err)
	}
	r.clock = g.clock
	r.eval.Now = g.clock.Now
	r.OnReading = func(ev trigger.Event, crop image.Image, at time.Time) { g.events = append(g.events, ev) }
	r.OnHealth = func(hev health.Event) { g.health = append(g.health, hev) }
	return r
}

// show puts code and value on the panel and polls once.
func (g *gateRig) show(r *Runner, code, value string) trigger.Event {
	g.t.Helper()
	g.src.img = g.p.frame(code, value)
	g.clock.Add(2 * time.Second)
	ev, err := r.Tick(context.Background())
	if err != nil {
		g.t.Fatalf("Tick(%q, %q): %v", code, value, err)
	}
	return ev
}

func (g *gateRig) rows(name string) []history.Row {
	g.t.Helper()
	rows, err := g.store.ReadingsPage(name, 0, time.Time{}, time.Time{}, 1000)
	if err != nil {
		g.t.Fatal(err)
	}
	return rows
}

// V2.1: with the gate open, the value is read and used as today: the
// number is published, recorded and handed to the Live panel and Home
// Assistant (OnReading).
func TestGateOpenPublishes(t *testing.T) {
	g := newGateRig(t, openStore(t))
	w := gatedWatch(config.Trigger{Type: "value", Confirm: 1}, `^1\.8\.0$`)
	r := g.start(w)
	ev := g.show(r, "1.8.0", "12345.6")
	if !ev.HasValue || ev.Value != 12345.6 || ev.Reading != "12345.6" {
		t.Fatalf("event = %+v", ev)
	}
	if len(g.events) != 1 || !g.events[0].HasValue {
		t.Errorf("OnReading heard %+v", g.events)
	}
	if rows := g.rows(w.Name); len(rows) != 1 || rows[0].Value != 12345.6 {
		t.Errorf("history = %+v", rows)
	}
	if st := r.Gate(); !st.Gated || !st.Open || !st.HasRead || st.ClosedPolls != 0 {
		t.Errorf("Gate() = %+v", st)
	}
	// Both boxes came from the one frame: the gate, then the value.
	if got := strings.Join(g.eng.reads, ","); got != "1.8.0,12345.6" {
		t.Errorf("engine read %s", got)
	}
}

// V2.1/V2.5: with the gate closed the poll is a skip: the value box isn't
// even read, nothing is published, recorded or shown, and the count
// towards Confirm is where it was, on both sides: entering the condition
// and leaving it (the 2x leave rule).
func TestGateClosedSkips(t *testing.T) {
	g := newGateRig(t, openStore(t))
	w := gatedWatch(config.Trigger{Type: "numeric", Op: "gt", Threshold: 50, Confirm: 3}, `^1\.8\.0$`)
	r := g.start(w)
	g.show(r, "1.8.0", "60")
	ev := g.show(r, "1.8.0", "61")
	if ev.Pending != 2 {
		t.Fatalf("pending = %d after two readings above, want 2", ev.Pending)
	}
	reads, rows, heard := len(g.eng.reads), len(g.rows(w.Name)), len(g.events)
	for i := 0; i < 5; i++ {
		// Another phase whose number is below the threshold, and one above:
		// neither may count.
		for _, v := range []string{"10", "70"} {
			if ev := g.show(r, "2.8.0", v); ev != (trigger.Event{}) {
				t.Fatalf("closed gate gave event %+v", ev)
			}
		}
	}
	if got := len(g.eng.reads) - reads; got != 10 {
		t.Errorf("%d reads on 10 closed polls; want the gate's 10 only", got)
	}
	if len(g.rows(w.Name)) != rows || len(g.events) != heard {
		t.Errorf("a closed gate recorded %d rows and published %d events", len(g.rows(w.Name))-rows, len(g.events)-heard)
	}
	if g.n.sent != nil {
		t.Fatalf("fired on a closed gate: %v", g.n.sent)
	}
	// The third reading above, after ten skips, is the third in a row.
	if ev := g.show(r, "1.8.0", "62"); !ev.Fired {
		t.Fatalf("third reading in a row didn't fire: %+v", ev)
	}
	// Leaving needs 2x Confirm = 6 readings below in a row; five below
	// with closed polls between them, some of the other phase above, is
	// still five.
	for i := 0; i < 5; i++ {
		g.show(r, "1.8.0", "40")
		g.show(r, "2.8.0", "99")
		g.show(r, "", "")
	}
	if st := r.eval.State(); st.Stable != condMet {
		t.Fatalf("left the condition after five readings below: %+v", st)
	}
	g.show(r, "1.8.0", "40")
	if st := r.eval.State(); st.Stable == condMet {
		t.Fatalf("six readings below in a row didn't leave: %+v", st)
	}
}

// V2.5: phases that alternate never mix: a counter gated on 1.8.0 only
// ever publishes the 1.8.0 phase's numbers, though the 2.8.0 phase shows
// a number in the same format, higher and climbing, and a blank frame
// sits between the phases. Every value, settled reading and history row
// is the 1.8.0 phase's, and the Value never goes down.
func TestGateKeepsPhasesApart(t *testing.T) {
	g := newGateRig(t, openStore(t))
	w := gatedWatch(config.Trigger{Type: "value", Confirm: 2}, `^1\.8\.0$`)
	w.Unit, w.DeviceClass = "kWh", "energy"
	r := g.start(w)
	imp := map[string]bool{}
	last := 0.0
	for i := 0; i < 40; i++ {
		in := 1000 + float64(i/4)
		inText := strconvF(in)
		imp[inText] = true
		g.show(r, "1.8.0", inText)
		g.show(r, "", "")
		g.show(r, "2.8.0", strconvF(5000+float64(i)))
		g.show(r, "", "")
	}
	published := 0
	for _, ev := range g.events {
		if ev.HasValue {
			published++
			if !imp[ev.Settled] || ev.Value < last {
				t.Fatalf("published %v (%q) after %v: not the 1.8.0 phase's", ev.Value, ev.Settled, last)
			}
			last = ev.Value
		}
		if !imp[ev.Reading] {
			t.Fatalf("Live/HA heard reading %q from another phase", ev.Reading)
		}
	}
	if published < 30 {
		t.Errorf("only %d of 40 readings published", published)
	}
	for _, row := range g.rows(w.Name) {
		if !imp[row.Reading] {
			t.Fatalf("history row %+v is another phase's", row)
		}
	}
	if st := r.Value(); st.Held != nil || st.Value != 1009 {
		t.Errorf("Value() = %+v, want 1009 and nothing held", st)
	}
}

func strconvF(f float64) string { return num(f) }

// V2.2/V2.5: a blank frame between phases is a skip, not a failed read: a
// gate that stays shut for more than health_after polls doesn't take the
// watch down, as a blank display doesn't. The status says how long it has
// been shut and what it last read, and says it is long, and logs it once,
// only after health_after polls and GateLongAfter.
func TestGateClosedIsNotDown(t *testing.T) {
	g := newGateRig(t, nil)
	w := gatedWatch(config.Trigger{Type: "value", Confirm: 1}, `^1\.8\.0$`)
	r := g.start(w)
	g.show(r, "1.8.0", "7")
	opened := g.clock.Now()
	g.show(r, "", "")
	first := g.clock.Now()
	for i := 0; i < 20; i++ {
		g.show(r, "", "")
	}
	if len(g.health) != 0 || r.isDown {
		t.Fatalf("blank frames took the watch down: %+v", g.health)
	}
	st := r.Gate()
	if st.Open || st.ClosedPolls != 21 || !st.ClosedSince.Equal(first) || !st.LastOpen.Equal(opened) || st.Saw != "" || st.Long {
		t.Fatalf("Gate() = %+v", st)
	}
	// 21 polls 2 s apart is 42 s: past health_after, short of GateLongAfter.
	for g.clock.Now().Sub(first) < GateLongAfter {
		g.show(r, "2.8.0", "9")
	}
	st = r.Gate()
	if !st.Long || st.Saw != "2.8.0" {
		t.Fatalf("Gate() = %+v after %v", st, g.clock.Now().Sub(first))
	}
	g.show(r, "2.8.0", "9")
	if n := g.logs.count("the gate hasn't opened for"); n != 1 {
		t.Errorf("logged the long-closed gate %d times", n)
	}
	if len(g.health) != 0 {
		t.Fatalf("health heard %+v", g.health)
	}
	g.show(r, "1.8.0", "7")
	if st := r.Gate(); !st.Open || st.Long || st.ClosedPolls != 0 || !g.logs.has("the gate is open again") {
		t.Errorf("Gate() = %+v after it opened", st)
	}
}

// V2.2: on a display that blanks between phases, what the gate last saw is
// the last code it read in this closed run, not the blank frame it read
// last: Live and the log say "last saw '2.8.0'", not "nothing". A run of
// blank frames only still saw nothing, and opening forgets it.
func TestGateRemembersTheLastCodeAcrossBlankFrames(t *testing.T) {
	g := newGateRig(t, nil)
	w := gatedWatch(config.Trigger{Type: "value", Confirm: 1}, `^1\.8\.0$`)
	r := g.start(w)
	g.show(r, "1.8.0", "7")
	first := g.clock.Now()
	for g.clock.Now().Sub(first) < GateLongAfter+10*time.Second {
		g.show(r, "2.8.0", "9")
		g.show(r, "", "")
		g.show(r, "", "")
		if st := r.Gate(); st.Saw != "2.8.0" {
			t.Fatalf("after a blank frame Gate().Saw = %q, want 2.8.0", st.Saw)
		}
	}
	if st := r.Gate(); !st.Long {
		t.Fatalf("Gate() = %+v, want long", st)
	}
	if !g.logs.has(`it last read "2.8.0"`) {
		t.Errorf("the long-closed log line doesn't name the code: %q", g.logs.lines)
	}
	g.show(r, "1.8.0", "7")
	g.show(r, "", "")
	if st := r.Gate(); st.Saw != "" {
		t.Errorf("a closed run of blank frames after the gate opened saw %q, want nothing", st.Saw)
	}
}

// A gate the engine can't read is a failed read, as a value it can't read
// is: it counts toward health.
func TestGateReadFailureCountsTowardHealth(t *testing.T) {
	g := newGateRig(t, nil)
	w := gatedWatch(config.Trigger{Type: "value", Confirm: 1}, `^1\.8\.0$`)
	r, err := New(w, g.src, &failingOCR{fail: true}, nil, nil, g.logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	r.OnHealth = func(hev health.Event) { g.health = append(g.health, hev) }
	g.src.img = g.p.frame("1.8.0", "1")
	for i := 0; i < 3; i++ {
		if _, err := r.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "gate") {
			t.Fatalf("Tick err = %v, want the gate's read error", err)
		}
	}
	if len(g.health) != 1 || g.health[0].State != "down" {
		t.Errorf("health = %+v", g.health)
	}
}

// A seven-segment '?' the decoder wasn't sure of never opens the gate,
// even when the pattern would match it: a half-lit 1.8.0 may be 1.6.0.
func TestGateOpens(t *testing.T) {
	re := regexp.MustCompile(`^1\.[0-9?]\.0$`)
	if !GateOpens(re, "1.8.0", false) || GateOpens(re, "1.?.0", true) || GateOpens(re, "2.8.0", false) || GateOpens(re, "", false) {
		t.Error("GateOpens disagrees")
	}
}

// The gate is read with its own engine when it names one (SetGateEngine),
// and with the watch's otherwise.
func TestGateUsesItsOwnEngine(t *testing.T) {
	g := newGateRig(t, nil)
	w := gatedWatch(config.Trigger{Type: "value", Confirm: 1}, `^A$`)
	r := g.start(w)
	codes := &panelOCR{p: g.p}
	r.SetGateEngine(codes)
	g.show(r, "A", "5")
	if strings.Join(codes.reads, ",") != "A" || strings.Join(g.eng.reads, ",") != "5" {
		t.Errorf("gate engine read %v, value engine %v", codes.reads, g.eng.reads)
	}
}

// V2.5: a gated watch's trigger state persists across a restart like any
// watch's: a met condition that still holds after the restart doesn't
// fire again, closed polls in between change nothing, and a counter's
// last value is kept. The gate is part of the question the saved state
// answers (Fingerprint): a new gate pattern starts the watch fresh.
func TestGatePersistsAcrossRestart(t *testing.T) {
	store := openStore(t)
	g := newGateRig(t, store)
	w := gatedWatch(config.Trigger{Type: "numeric", Op: "gt", Threshold: 50, Confirm: 2}, `^1\.8\.0$`)
	w.Counter = true
	r := g.start(w)
	g.show(r, "1.8.0", "60")
	g.show(r, "2.8.0", "900")
	if ev := g.show(r, "1.8.0", "61"); !ev.Fired {
		t.Fatalf("didn't fire: %+v", ev)
	}
	if ev := g.show(r, "1.8.0", "62"); !ev.HasValue || ev.Value != 61 {
		t.Fatalf("value not published: %+v", ev)
	}
	r2 := g.start(w)
	if st := r2.Value(); !st.HasLast || st.Last != 61 {
		t.Errorf("counter's last value after restart = %+v", st)
	}
	for i := 0; i < 4; i++ {
		g.show(r2, "2.8.0", "900")
		g.show(r2, "", "")
	}
	for i := 0; i < 3; i++ {
		if ev := g.show(r2, "1.8.0", "62"); ev.Fired {
			t.Fatalf("fired again after the restart: %+v", ev)
		}
	}
	if len(g.n.sent) != 1 {
		t.Errorf("sent %d alerts", len(g.n.sent))
	}
	moved := w
	gate := *w.Gate
	gate.Pattern = `^2\.8\.0$`
	moved.Gate = &gate
	if Fingerprint(moved) == Fingerprint(w) {
		t.Error("a new gate pattern keeps the fingerprint")
	}
	plain := w
	plain.Gate = nil
	if Fingerprint(plain) == Fingerprint(w) {
		t.Error("adding a gate keeps the fingerprint")
	}
	// The gate's engine spelled out as the watch's is the same question.
	same := w
	sg := *w.Gate
	sg.Engine = "tesseract"
	same.Gate = &sg
	if Fingerprint(same) != Fingerprint(w) {
		t.Error("gate engine tesseract and the watch's default tesseract fingerprint apart")
	}
}

// A watch without a gate reads its region and nothing else, as before.
func TestNoGateReadsOnlyTheValue(t *testing.T) {
	g := newGateRig(t, nil)
	w := gatedWatch(config.Trigger{Type: "value", Confirm: 1}, "x")
	w.Gate = nil
	r := g.start(w)
	ev := g.show(r, "2.8.0", "5")
	if !ev.HasValue || ev.Value != 5 || strings.Join(g.eng.reads, ",") != "5" {
		t.Fatalf("event %+v, reads %v", ev, g.eng.reads)
	}
	if st := r.Gate(); st.Gated {
		t.Errorf("Gate() = %+v on a watch without one", st)
	}
}

var _ ocr.Engine = (*panelOCR)(nil)

// A closed poll is handed to SetOnGateClosed's function (the supervisor
// passes it on, so Home Assistant hears the watch is polling), and an
// open one isn't: that goes to OnReading as before.
func TestGateClosedPollsReachTheHook(t *testing.T) {
	g := newGateRig(t, nil)
	r := g.start(gatedWatch(config.Trigger{Type: "value", Confirm: 1}, `^1\.8\.0$`))
	var closed []time.Time
	r.SetOnGateClosed(func(at time.Time) { closed = append(closed, at) })
	for i := 0; i < 3; i++ {
		g.show(r, "2.8.0", "9")
	}
	if len(closed) != 3 || len(g.events) != 0 {
		t.Fatalf("three closed polls: hook heard %d, OnReading %d", len(closed), len(g.events))
	}
	g.show(r, "1.8.0", "7")
	if len(closed) != 3 || len(g.events) != 1 {
		t.Fatalf("an open poll: hook heard %d in all, OnReading %d", len(closed), len(g.events))
	}
	// A panic in the hook is contained like one in OnReading.
	r.SetOnGateClosed(func(time.Time) { panic("boom") })
	g.show(r, "2.8.0", "9")
	g.show(r, "1.8.0", "8")
	if len(g.events) != 2 {
		t.Errorf("the poll after a crashing hook wasn't read: %d events", len(g.events))
	}
}

// A gate shut for longer than StaleAfter forgets the settled state, as a
// run of blank frames does: a condition that still holds when the gate
// opens again is reported again. A short closed spell, the normal cycle,
// keeps it.
func TestGateLongClosedForgetsLikeABlankDisplay(t *testing.T) {
	g := newGateRig(t, openStore(t))
	w := gatedWatch(config.Trigger{Type: "numeric", Op: "gt", Threshold: 50, Confirm: 2}, `^1\.8\.0$`)
	r := g.start(w)
	g.show(r, "1.8.0", "60")
	if ev := g.show(r, "1.8.0", "61"); !ev.Fired {
		t.Fatalf("didn't fire: %+v", ev)
	}
	// The normal cycle: other phases for a minute, then 1.8.0 again.
	for i := 0; i < 30; i++ {
		g.show(r, "2.8.0", "900")
	}
	for i := 0; i < 3; i++ {
		if ev := g.show(r, "1.8.0", "62"); ev.Fired {
			t.Fatalf("fired again after a minute of other phases: %+v", ev)
		}
	}
	// The display stopped on another phase for longer than StaleAfter.
	g.clock.Add(r.eval.StaleAfter)
	g.show(r, "2.8.0", "900")
	if st := r.eval.State(); st.HasStable {
		t.Fatalf("still settled after %v closed: %+v", r.eval.StaleAfter, st)
	}
	r2 := g.start(w)
	if st := r2.eval.State(); st.HasStable {
		t.Errorf("the forgotten state wasn't saved: a restart restores %+v", st)
	}
	g.show(r, "1.8.0", "63")
	if ev := g.show(r, "1.8.0", "64"); !ev.Fired {
		t.Fatalf("a held condition wasn't news after a long closed spell: %+v", ev)
	}
}

// max_interval backs off while the gated value stays the same, but a
// closed poll is the display moving on to another phase: it puts the
// watch back on its base interval, so the polls can't alias with the
// display's cycle and miss the wanted phase for minutes. A gate that
// never opens keeps the base interval.
func TestGateClosedPollsDontBackOff(t *testing.T) {
	g := newGateRig(t, nil)
	w := gatedWatch(config.Trigger{Type: "value", Confirm: 1}, `^1\.8\.0$`)
	w.Interval, w.MaxInterval = config.Duration(time.Second), config.Duration(20*time.Second)
	r := g.start(w)
	iv, last := r.baseIvl, ""
	poll := func(code, value string) time.Duration {
		t.Helper()
		g.src.img = g.p.frame(code, value)
		ev, err := r.Tick(context.Background())
		iv = NextInterval(r.baseIvl, r.maxIvl, iv, r.pollChanged(ev, err, last))
		last = ev.Reading
		return iv
	}
	for i := 0; i < 8; i++ {
		if got := poll("2.8.0", "900"); got != time.Second {
			t.Fatalf("closed poll %d: next poll in %v, want the base 1s", i+1, got)
		}
	}
	// Open on a steady value it backs off as any watch does...
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, d := range want {
		if got := poll("1.8.0", "7"); got != d {
			t.Fatalf("open poll %d: next in %v, want %v", i+1, got, d)
		}
	}
	// ...until the display moves on.
	if got := poll("", ""); got != time.Second {
		t.Fatalf("a blank frame between phases: next in %v, want 1s", got)
	}
	if got := poll("2.8.0", "900"); got != time.Second {
		t.Fatalf("the other phase: next in %v, want 1s", got)
	}
	// An ungated watch backs off on a steady display as before.
	plain := w
	plain.Gate = nil
	r = g.start(plain)
	iv, last = r.baseIvl, ""
	want = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 20 * time.Second}
	for i, d := range want {
		if got := poll("2.8.0", "900"); got != d {
			t.Fatalf("ungated poll %d: next in %v, want %v", i+1, got, d)
		}
	}
}
