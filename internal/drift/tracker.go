package drift

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"math"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/source"
)

// The numbers below come from round 6's photos and T2's emulated cameras
// (see docs/configuration.md, "When the camera or the screen moves").
const (
	// learnReadings is how many clean readings in a row a watch makes
	// before its reference is taken.
	learnReadings = 3
	// checkEvery is the least time between two checks. A check costs a
	// few milliseconds; most polls skip it.
	checkEvery = 5 * time.Second
	// settleChecks trusted checks after the reference is taken set where
	// "not moved" is (the median of their shifts): a camera that shakes a
	// little was caught somewhere in its shake when the reference was
	// taken. settleGiveUp checks without them and the surroundings are
	// taken to be too plain, or too changeable, to go by.
	settleChecks = 5
	settleGiveUp = 15
	// Whether the picture moved is decided from the median of the settle
	// checks, the rule the false-move counts below were measured with.
	// How far it moved is measured from a finer "not moved": the mean of
	// the settle checks near that median, and then of the steady checks
	// after them that are near it, up to baseChecks checks in all (about
	// a minute). It stops there, so a picture that creeps slowly isn't
	// followed. A move's offset is the mean of all the checks that agree
	// with it, not of the window that reported it: on a camera that shakes
	// by a pixel each frame, a few checks put it 2 to 3 px off, and "Move
	// the box with it" would leave the box that far off for good.
	baseChecks = 12
	// window checks in a row, all lined up well (trustMin) and within
	// agree pixels of their median, decide a move: when that median is at
	// least the move threshold from where the picture was. The threshold
	// is moveMin pixels, or shakeTimes the camera's own shake when that is
	// more: T2's cameras shake by 1-3 px from frame to frame, and a move
	// has to stand out from that. The shake is the median jitter of the
	// last shakeMemory steady checks, each check's distance from the one
	// before it over the square root of 2 (for a camera that shakes at
	// random, the same as its distance from where it shakes about). It
	// goes by how far the picture jumps between checks, not by how far it
	// is from where it was, so a picture that creeps, or gets nudged a
	// little at a time, doesn't raise the threshold as it goes: it is
	// reported once it has gone far enough. maxMove caps the threshold.
	window      = 4
	moveMin     = 4.5
	maxMove     = 9.0
	agree       = 3.0
	shakeTimes  = 4.0
	shakeMemory = 30
	// trustMin is the score a check needs for its shift to count, and
	// findMin the score under which the reference isn't in the picture
	// at all. Shifted, turned, re-exposed photos all scored 0.66 or more;
	// a covered lens and another scene 0.31 or less.
	trustMin = 0.6
	findMin  = 0.35
	// lostAfter is how long, and lostChecks how many checks, the
	// reference must stay missing, with no run of readRun clean readings
	// meanwhile, before the page says so: a screen that shows something
	// else for a few minutes (a menu, a boot screen) while its box still
	// reads is not a camera pointed elsewhere. One stray clean reading
	// doesn't count: the seven-segment decoder reads a lone "7" in a
	// covered lens's noise now and then.
	lostAfter  = 5 * time.Minute
	lostChecks = 5
	readRun    = 3
)

// State is where a watch's position check stands.
type State string

const (
	// Off: the watch has check_moves: false.
	Off State = "off"
	// Learning: waiting for clean readings, or settling a new reference.
	Learning State = "learning"
	// Steady: the picture is where it was.
	Steady State = "steady"
	// Moved: the picture around the box has moved by DX, DY.
	Moved State = "moved"
	// Lost: the picture around the box isn't in the frame any more.
	Lost State = "lost"
	// Unusable: the area around the box is too plain, or changes too
	// much, to notice a move by.
	Unusable State = "unusable"
)

// Status is what the page shows about a watch's position.
type Status struct {
	State State
	// DX, DY: Moved, how far the picture moved, in frame pixels (right
	// and down are positive). FrameW, FrameH: the frame size they are in.
	DX, DY         int
	FrameW, FrameH int
	// FX, FY: Moved, the same offset unrounded, for moving the box.
	FX, FY float64
	// Since: Moved or Lost, the first check that saw it.
	Since time.Time
}

// Read is what a poll's reading was like, for the check: a clean one
// (text with no unsure digit), a bad one (nothing read, or unsure), or
// none (pixel_change watches read nothing).
type Read int

const (
	ReadClean Read = iota
	ReadBad
	ReadNone
)

// Saved is what the history database keeps of a watch's check, so a
// restart neither forgets where the picture was nor says a move again.
type Saved struct {
	Key string
	Ref Reference
	// BaseX, BaseY: where the picture rests, for deciding a move (the
	// median of the settle checks). MeanX, MeanY: the same, measured
	// finer, for the offset; both 0 in a row saved before they were kept,
	// and BaseX, BaseY then stand in.
	BaseX, BaseY float64
	MeanX, MeanY float64
	State        State
	DX, DY       float64
	Since        time.Time
	// Shake is the camera's own shake (Tracker.threshold), in pixels.
	Shake float64
	// MovedSince: Lost after it had moved, when the move was first seen;
	// DX, DY are then the move's offset. When the picture is back in view
	// still moved, the move isn't reported again.
	MovedSince time.Time
}

// Store keeps Saved per watch (history.Store does).
type Store interface {
	LoadDrift(watch string) (Saved, bool, error)
	SaveDrift(watch string, s Saved) error
}

// Key identifies what a watch's reference is a picture of: its camera
// (without the login, as the runner's fingerprint has it) and its box. A
// save that changes either takes a new reference.
func Key(w config.Watch) string {
	src := w.Source
	if u, err := url.Parse(src); err == nil && source.StripLogin(u) {
		src = u.String()
	}
	r := w.Region
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\n%g %g %g %g", src, r.X, r.Y, r.W, r.H)))
	return hex.EncodeToString(sum[:12])
}

// Enabled says whether a watch checks its position: unless it says
// check_moves: false.
func Enabled(w config.Watch) bool { return w.CheckMoves == nil || *w.CheckMoves }

// Board is every watch's position status, for the web UI, and the
// requests the UI makes of the running checks. It outlives the trackers:
// a Save & restart makes a new tracker, which picks up a request made
// just before it.
type Board struct {
	mu sync.Mutex
	m  map[string]*slot
}

type slot struct {
	status Status
	retake bool
}

func NewBoard() *Board { return &Board{m: map[string]*slot{}} }

func (b *Board) get(name string) *slot {
	s, ok := b.m[name]
	if !ok {
		s = &slot{status: Status{State: Learning}}
		b.m[name] = s
	}
	return s
}

// Status is watch name's position status, or false if it has none yet.
func (b *Board) Status(name string) (Status, bool) {
	if b == nil {
		return Status{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.m[name]
	if !ok {
		return Status{}, false
	}
	return s.status, true
}

// Retake asks watch name's check to take a new reference from its next
// frame, whatever its readings: the box was just moved with the picture,
// or someone said the new position is fine.
func (b *Board) Retake(name string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.get(name)
	s.retake = true
	s.status = Status{State: Learning}
}

// Set puts st on the board as a check would: for tests, and anything
// else that learns a watch's position. The watch's own check replaces it
// at its next news.
func (b *Board) Set(name string, st Status) {
	if b != nil {
		b.set(name, st)
	}
}

// Forget drops a deleted watch.
func (b *Board) Forget(name string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, name)
}

func (b *Board) set(name string, st Status) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.get(name).status = st
}

func (b *Board) takeRetake(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.get(name)
	r := s.retake
	s.retake = false
	return r
}

// Change is a tracker's news from one poll: the picture moved, or was
// lost (an alert may go out), or it is steady again.
type Change struct {
	State  State
	Status Status
}

// Tracker checks one watch's position. It runs on the watch's poll
// goroutine only.
type Tracker struct {
	name, key string
	region    config.Region
	board     *Board
	store     Store
	logf      func(string, ...any)

	ref          *Reference
	restX, restY float64    // "not moved", for deciding a move: the settle median
	baseX, baseY float64    // "not moved", for measuring a move: the finer mean
	baseSum      [2]float64 // the shifts baseX, baseY are the mean of
	baseN        int
	mvSum        [2]float64 // Moved: the shifts the offset is the mean of
	mvN          int
	before       Status       // Lost: what it was before (Steady or Moved)
	settle       [][2]float64 // trusted shifts while settling
	settled      int          // checks while settling
	clean        int          // clean readings in a row, while learning
	last         time.Time    // last check
	lastClean    time.Time    // last run of readRun clean readings
	cleanRun     int          // clean readings in a row
	win          []check
	lostSince    time.Time
	lostN        int
	shake        []float64 // jitter of the last steady checks
	prevX, prevY float64   // the last trusted shift, for the jitter
	hasPrev      bool
	st           Status
}

type check struct {
	x, y float64
	at   time.Time
}

// Tracker makes the tracker for a watch, restoring what the store kept
// when it is about the same camera and box. It returns nil, and the board
// says Off, when the watch doesn't check its position.
func (b *Board) Tracker(w config.Watch, store Store, logf func(string, ...any)) *Tracker {
	if b == nil {
		return nil
	}
	if !Enabled(w) {
		b.set(w.Name, Status{State: Off})
		return nil
	}
	t := &Tracker{name: w.Name, key: Key(w), region: w.Region, board: b, store: store, logf: logf,
		st: Status{State: Learning}}
	b.mu.Lock()
	retake := b.get(w.Name).retake
	b.mu.Unlock()
	if store != nil && !retake {
		sv, ok, err := store.LoadDrift(w.Name)
		switch {
		case err != nil:
			logf("watch %s: couldn't read where its picture was, so it learns it again: %v", w.Name, err)
		case ok && sv.Key == t.key && len(sv.Ref.Gray) == sv.Ref.W*sv.Ref.H && sv.Ref.W > 0 &&
			sv.State != Learning && sv.State != Unusable:
			// A reference that hadn't settled (one just retaken), or
			// one that was too plain, is learned again.
			ref := sv.Ref
			t.ref = &ref
			t.restX, t.restY = sv.BaseX, sv.BaseY
			t.baseX, t.baseY = sv.MeanX, sv.MeanY
			if sv.MeanX == 0 && sv.MeanY == 0 {
				t.baseX, t.baseY = sv.BaseX, sv.BaseY
			}
			t.baseN = baseChecks
			moved := Status{State: Moved, DX: int(math.Round(sv.DX)), DY: int(math.Round(sv.DY)), FX: sv.DX, FY: sv.DY,
				FrameW: ref.FrameW, FrameH: ref.FrameH, Since: sv.Since}
			switch sv.State {
			case Moved:
				t.st = moved
				t.startMove(sv.DX+t.baseX, sv.DY+t.baseY, window)
			case Lost:
				t.st = Status{State: Lost, FrameW: ref.FrameW, FrameH: ref.FrameH, Since: sv.Since}
				t.lostSince = sv.Since
				t.before = Status{State: Steady, FrameW: ref.FrameW, FrameH: ref.FrameH}
				if !sv.MovedSince.IsZero() {
					moved.Since = sv.MovedSince
					t.before = moved
					t.startMove(sv.DX+t.baseX, sv.DY+t.baseY, window)
				}
			default:
				t.st = Status{State: Steady, FrameW: ref.FrameW, FrameH: ref.FrameH}
			}
			if sv.Shake > 0 {
				t.shake = []float64{sv.Shake}
			}
		}
	}
	b.set(w.Name, t.st)
	return t
}

// Status is the tracker's current status.
func (t *Tracker) Status() Status { return t.st }

// Observe takes one poll's frame and what its reading was like, and
// checks the position when a check is due. It returns the news, if any.
func (t *Tracker) Observe(img image.Image, at time.Time, rd Read) (Change, bool) {
	fw, fh := img.Bounds().Dx(), img.Bounds().Dy()
	if rd == ReadClean {
		if t.cleanRun++; t.cleanRun >= readRun {
			t.lastClean = at
		}
	} else {
		t.cleanRun = 0
	}
	if t.board.takeRetake(t.name) {
		t.take(img, at, fw, fh)
		return Change{}, false
	}
	// Too plain, or too little around the box: not checked until the
	// watch is saved or restarted (a new Tracker), or a retake is asked
	// for. Learning again would only say so again.
	if t.st.State == Unusable {
		return Change{}, false
	}
	if t.ref == nil {
		if rd == ReadBad {
			t.clean = 0
			return Change{}, false
		}
		if t.clean++; t.clean >= learnReadings {
			t.take(img, at, fw, fh)
		}
		return Change{}, false
	}
	if fw != t.ref.FrameW || fh != t.ref.FrameH {
		t.logf("watch %s: the camera's picture changed size (%dx%d to %dx%d), so watchglass learns where things are again", t.name, t.ref.FrameW, t.ref.FrameH, fw, fh)
		t.reset()
		return t.publish(Status{State: Learning}, false)
	}
	if at.Sub(t.last) < checkEvery {
		return Change{}, false
	}
	t.last = at
	return t.judge(measure(t.ref, img, t.baseX, t.baseY), at)
}

// reset forgets the reference: the next clean readings take a new one.
func (t *Tracker) reset() {
	t.ref, t.settle, t.settled, t.clean, t.win = nil, nil, 0, 0, nil
	t.hasPrev = false
	t.baseSum, t.baseN, t.mvSum, t.mvN, t.before = [2]float64{}, 0, [2]float64{}, 0, Status{}
	t.lostSince, t.lostN = time.Time{}, 0
}

// take makes a new reference from img. Where "not moved" is gets
// settled over the next checks.
//
// What was saved is overwritten at once, so a restart before the new
// reference settles doesn't bring back a move or a lost picture that
// was dealt with: it learns again.
func (t *Tracker) take(img image.Image, at time.Time, fw, fh int) {
	old := t.ref
	t.reset()
	box := BoxRect(fw, fh, t.region.X, t.region.Y, t.region.W, t.region.H)
	t.ref = Take(img, box)
	t.last = at
	if t.ref == nil {
		t.publish(Status{State: Unusable}, false)
		t.saveRef(old)
		t.logf("watch %s: its box leaves too little of the picture around it to notice a move by", t.name)
		return
	}
	t.publish(Status{State: Learning, FrameW: fw, FrameH: fh}, false)
	t.save()
}

func (t *Tracker) judge(r Result, at time.Time) (Change, bool) {
	found := r.Found && r.Local >= findMin
	trusted := r.Found && r.Local >= trustMin
	fw, fh := t.ref.FrameW, t.ref.FrameH
	if t.st.State == Learning {
		t.settled++
		if trusted {
			t.settle = append(t.settle, [2]float64{r.DX, r.DY})
		}
		if len(t.settle) >= settleChecks {
			mx, my := median(t.settle)
			t.baseSum, t.baseN = [2]float64{}, 0
			for _, p := range t.settle {
				if math.Hypot(p[0]-mx, p[1]-my) <= agree {
					t.baseSum[0], t.baseSum[1], t.baseN = t.baseSum[0]+p[0], t.baseSum[1]+p[1], t.baseN+1
				}
			}
			if t.baseN == 0 {
				// Scattered so that none is near their median: the
				// median it is.
				t.baseSum, t.baseN = [2]float64{mx, my}, 1
			}
			t.baseX, t.baseY = t.baseSum[0]/float64(t.baseN), t.baseSum[1]/float64(t.baseN)
			t.restX, t.restY = mx, my
			if math.Hypot(mx, my) > maxMove {
				// Shake doesn't put the picture this far from the frame
				// the reference was taken from: it moved since. That
				// frame read well, so it is where "not moved" is, and
				// the move is reported as any other.
				t.baseX, t.baseY, t.baseN = 0, 0, baseChecks
				t.restX, t.restY = 0, 0
			}
			t.shake = t.shake[:0]
			for i := 1; i < len(t.settle); i++ {
				p, q := t.settle[i-1], t.settle[i]
				t.shake = append(t.shake, math.Hypot(q[0]-p[0], q[1]-p[1])/math.Sqrt2)
			}
			last := t.settle[len(t.settle)-1]
			t.prevX, t.prevY, t.hasPrev = last[0], last[1], true
			t.settle = nil
			t.logf("watch %s: took a picture of the area around its box, to notice if the camera or the screen moves", t.name)
			ch, news := t.publish(Status{State: Steady, FrameW: fw, FrameH: fh}, false)
			t.save()
			return ch, news
		}
		if t.settled >= settleGiveUp {
			t.logf("watch %s: the picture around its box is too plain, or changes too much, to notice a move by; it isn't checked until the watch is saved or restarted", t.name)
			ch, news := t.publish(Status{State: Unusable}, false)
			t.save()
			t.ref = nil
			return ch, news
		}
		return Change{}, false
	}
	if !found {
		t.win = t.win[:0]
		if t.lostSince.IsZero() {
			t.lostSince, t.lostN = at, 0
		}
		t.lostN++
		if t.st.State != Lost && t.lostN >= lostChecks && at.Sub(t.lostSince) >= lostAfter && t.lastClean.Before(t.lostSince) {
			st := Status{State: Lost, FrameW: fw, FrameH: fh, Since: t.lostSince}
			t.before = t.st
			t.logf("watch %s: the picture around its box hasn't been in the frame since %s: is the camera covered or pointed elsewhere?", t.name, t.lostSince.Format("15:04:05"))
			t.st = st
			t.save()
			return t.publish(st, true)
		}
		return Change{}, false
	}
	if t.st.State != Lost {
		t.lostSince, t.lostN = time.Time{}, 0
	}
	if !trusted {
		t.win = t.win[:0]
		return Change{}, false
	}
	if t.st.State == Lost {
		// Back as it was before: a move it had then is still the same
		// move, not news, unless the checks from here on say otherwise
		// (back where it was, or moved again).
		t.lostSince, t.lostN = time.Time{}, 0
		back := t.before
		if back.State != Moved {
			back = Status{State: Steady, FrameW: fw, FrameH: fh}
			t.logf("watch %s: the picture around its box is back in view", t.name)
		} else {
			t.logf("watch %s: the picture around its box is back in view; it had moved about %s before, and the warning stays until the checks say where it is now", t.name, Offset(back.DX, back.DY))
		}
		t.before = Status{}
		t.st = back
		t.save()
		t.publish(t.st, false)
	}
	if t.st.State == Steady && t.hasPrev {
		t.shake = append(t.shake, math.Hypot(r.DX-t.prevX, r.DY-t.prevY)/math.Sqrt2)
		if len(t.shake) > shakeMemory {
			t.shake = t.shake[1:]
		}
	}
	t.prevX, t.prevY, t.hasPrev = r.DX, r.DY, true
	limit := t.threshold()
	if t.st.State == Steady && t.baseN < baseChecks && math.Hypot(r.DX-t.baseX, r.DY-t.baseY) < limit/2 {
		t.baseSum[0], t.baseSum[1], t.baseN = t.baseSum[0]+r.DX, t.baseSum[1]+r.DY, t.baseN+1
		t.baseX, t.baseY = t.baseSum[0]/float64(t.baseN), t.baseSum[1]/float64(t.baseN)
	}
	t.win = append(t.win, check{x: r.DX, y: r.DY, at: at})
	if len(t.win) > window {
		t.win = t.win[1:]
	}
	if len(t.win) < window {
		return Change{}, false
	}
	pts := make([][2]float64, len(t.win))
	for i, c := range t.win {
		pts[i] = [2]float64{c.x, c.y}
	}
	wx, wy := median(pts)
	for _, c := range t.win {
		if math.Hypot(c.x-wx, c.y-wy) > agree {
			return Change{}, false
		}
	}
	mag := math.Hypot(wx-t.restX, wy-t.restY)
	switch t.st.State {
	case Steady:
		if mag < limit {
			return Change{}, false
		}
		since := t.startWindow(wx, wy, t.baseX, t.baseY)
		st := t.moved(Status{State: Moved, FrameW: fw, FrameH: fh, Since: since})
		t.logf("watch %s: the picture around its box moved about %s since %s; readings may be wrong until the box is moved", t.name, Offset(st.DX, st.DY), st.Since.Format("15:04:05"))
		t.st = st
		t.save()
		return t.publish(st, true)
	case Moved:
		if mag < limit/2 {
			t.logf("watch %s: the picture around its box is back where it was", t.name)
			t.st = Status{State: Steady, FrameW: fw, FrameH: fh}
			t.save()
			return t.publish(t.st, false)
		}
		// The last checks are off the mean by more than the shake
		// explains: the picture moved on (knocks, creep), so the mean
		// starts again from them. More than agree is a move of its own
		// and gets a log line. The window's mean, not its median: on a
		// camera that shakes by a pixel, the median of four checks is a
		// pixel off on both axes one check in twenty.
		var sx, sy float64
		for _, c := range t.win {
			sx, sy = sx+c.x, sy+c.y
		}
		n := float64(len(t.win))
		if d := math.Hypot(sx/n-t.baseX-t.st.FX, sy/n-t.baseY-t.st.FY); d > limit/3 {
			t.startWindow(wx, wy, t.baseX+t.st.FX, t.baseY+t.st.FY)
			st := t.moved(t.st)
			if d > agree {
				t.logf("watch %s: the picture around its box has moved again, about %s from where it was", t.name, Offset(st.DX, st.DY))
			}
			t.st = st
			t.save()
			t.publish(st, false)
			return Change{}, false
		}
		// The same move, measured once more: the offset is the mean of
		// every check of it so far.
		if math.Hypot(r.DX-t.baseX-t.st.FX, r.DY-t.baseY-t.st.FY) <= agree {
			t.mvSum[0], t.mvSum[1], t.mvN = t.mvSum[0]+r.DX, t.mvSum[1]+r.DY, t.mvN+1
		}
		st := t.moved(t.st)
		if st.DX != t.st.DX || st.DY != t.st.DY {
			t.st = st
			t.save()
		}
		t.publish(st, false)
	}
	return Change{}, false
}

// startMove starts a move's mean at the shift x, y, as n checks.
func (t *Tracker) startMove(x, y float64, n int) {
	t.mvSum, t.mvN = [2]float64{x * float64(n), y * float64(n)}, n
}

// startWindow starts a move's mean with the checks of the window that saw
// it, wx, wy being the window's median and fromX, fromY where the picture
// was before, and returns when the first of them was made. A check nearer
// where the picture was than where it went was made before the move: on a
// camera that shakes, a small move's window can take in the check before
// it, which is still within agree pixels of the others, and that check
// alone put the first report of a 5 px move 2 px short now and then.
func (t *Tracker) startWindow(wx, wy, fromX, fromY float64) time.Time {
	t.mvSum, t.mvN = [2]float64{}, 0
	var since time.Time
	for _, c := range t.win {
		if math.Hypot(c.x-fromX, c.y-fromY) < math.Hypot(c.x-wx, c.y-wy) {
			continue
		}
		if t.mvN == 0 {
			since = c.at
		}
		t.mvSum[0], t.mvSum[1], t.mvN = t.mvSum[0]+c.x, t.mvSum[1]+c.y, t.mvN+1
	}
	if t.mvN == 0 {
		// Each check nearer where it was than the window's median: the
		// median it is.
		t.mvSum, t.mvN, since = [2]float64{wx, wy}, 1, t.win[0].at
	}
	return since
}

// moved is st with the move's offset: the mean of its checks, from
// where "not moved" is.
func (t *Tracker) moved(st Status) Status {
	st.FX = t.mvSum[0]/float64(t.mvN) - t.baseX
	st.FY = t.mvSum[1]/float64(t.mvN) - t.baseY
	st.DX, st.DY = int(math.Round(st.FX)), int(math.Round(st.FY))
	return st
}

// threshold is how far the picture must move to count: moveMin, or more
// for a camera that shakes.
func (t *Tracker) threshold() float64 {
	if len(t.shake) == 0 {
		return moveMin
	}
	return math.Min(maxMove, math.Max(moveMin, shakeTimes*med(append([]float64(nil), t.shake...))))
}

// publish puts st on the board; with news, it is returned as a Change.
func (t *Tracker) publish(st Status, news bool) (Change, bool) {
	t.st = st
	t.board.set(t.name, st)
	return Change{State: st.State, Status: st}, news
}

func (t *Tracker) save() { t.saveRef(t.ref) }

// saveRef saves the check's state with ref as its reference; with none,
// there is nothing to save.
func (t *Tracker) saveRef(ref *Reference) {
	if t.store == nil || ref == nil {
		return
	}
	var shake float64
	if len(t.shake) > 0 {
		shake = med(append([]float64(nil), t.shake...))
	}
	sv := Saved{Key: t.key, Ref: *ref, BaseX: t.restX, BaseY: t.restY, MeanX: t.baseX, MeanY: t.baseY,
		State: t.st.State, Since: t.st.Since, Shake: shake}
	switch {
	case t.st.State == Moved:
		sv.DX, sv.DY = t.st.FX, t.st.FY
	case t.st.State == Lost && t.before.State == Moved:
		sv.DX, sv.DY, sv.MovedSince = t.before.FX, t.before.FY, t.before.Since
	}
	err := t.store.SaveDrift(t.name, sv)
	if err != nil {
		t.logf("watch %s: save where its picture is: %v", t.name, err)
	}
}

// median is the median of each coordinate.
func median(p [][2]float64) (float64, float64) {
	xs, ys := make([]float64, len(p)), make([]float64, len(p))
	for i, q := range p {
		xs[i], ys[i] = q[0], q[1]
	}
	return med(xs), med(ys)
}

func med(v []float64) float64 {
	sort.Float64s(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

// Offset says a shift in words: "14 px right and 3 px down", "5 px left".
func Offset(dx, dy int) string {
	var parts []string
	switch {
	case dx > 0:
		parts = append(parts, fmt.Sprintf("%d px right", dx))
	case dx < 0:
		parts = append(parts, fmt.Sprintf("%d px left", -dx))
	}
	switch {
	case dy > 0:
		parts = append(parts, fmt.Sprintf("%d px down", dy))
	case dy < 0:
		parts = append(parts, fmt.Sprintf("%d px up", -dy))
	}
	switch len(parts) {
	case 0:
		return "less than a pixel"
	case 1:
		return parts[0]
	}
	return parts[0] + " and " + parts[1]
}

// Shifted is region moved by st's offset (unrounded, when the check
// measured it), kept inside the frame: the box moved with the picture.
func Shifted(r config.Region, st Status) config.Region {
	if st.FrameW <= 0 || st.FrameH <= 0 {
		return r
	}
	dx, dy := st.FX, st.FY
	if dx == 0 && dy == 0 {
		dx, dy = float64(st.DX), float64(st.DY)
	}
	round := func(f float64) float64 { return math.Round(f*1e4) / 1e4 }
	r.X = round(r.X + dx/float64(st.FrameW))
	r.Y = round(r.Y + dy/float64(st.FrameH))
	r.X = math.Max(0, math.Min(r.X, 1-r.W))
	r.Y = math.Max(0, math.Min(r.Y, 1-r.H))
	return r
}
