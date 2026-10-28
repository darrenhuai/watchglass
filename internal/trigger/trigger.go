// Package trigger is the pure state machine that turns a noisy stream of
// per-frame readings into rare, trustworthy fire events. No I/O lives here.
package trigger

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
)

type Event struct {
	Fired   bool
	Reading string
	Reason  string
	// Pending and Need are set on a reading that would fire once Confirm
	// is reached: Pending is how many readings in a row have said so,
	// this one included, and Need is Confirm. The Live panel shows them
	// as "1 of 3", so a condition that is read but never held long enough
	// isn't a silent mystery. Both are 0 on every other reading.
	Pending int
	Need    int
	// CooldownEnds is set with Pending while Cooldown is running: the
	// alert this progress leads to can't go out before then. A reading
	// with Pending == Need and CooldownEnds set is confirmed and held;
	// the first confirmed reading after CooldownEnds fires.
	CooldownEnds time.Time
	// Settled is the reading once it has held: the same text read Confirm
	// times in a row, repeated on every reading while it holds. For numeric
	// it is instead the text of the reading Value came from, so the two
	// always agree; a numeric reading with no number in it ("Err", "OFF")
	// settles like text, except the seven-segment decoder's undecoded '?'
	// readings, which say nothing. This is what Home Assistant's reading
	// entity shows, so one misread frame never reaches it. HasSettled is
	// false until a reading has settled.
	//
	// pixel_change has no misreads to filter, only a percentage that moves
	// a little on every frame, so its "N% changed" settles at once and is
	// then held (see pixelSettleEvery): it moves at once when the reading
	// crosses Threshold either way, and otherwise at most once a minute.
	Settled    string
	HasSettled bool
	// Value is a numeric watch's number with misreads filtered out: the
	// median of the last 2*Confirm-1 numbers read. A new level needs
	// Confirm readings (a majority) to take over, up to Confirm-1 bad
	// frames in a row never show, and a value that changes on every
	// reading (a power meter) is still followed, a reading or so behind;
	// the published number is always one that was actually read. Readings
	// with no number, and (SevenSeg) every reading with a '?' in it or no
	// digits at all, don't count.
	// HasValue is false until the window has filled, and on every other
	// type.
	Value    float64
	HasValue bool
	// Unsure is ObserveRead's unsure: a '?' in Reading is a digit the
	// decoder saw and wasn't sure of, which no pattern matches.
	Unsure bool
}

// numRead is one number a numeric watch read, with the text it came from.
type numRead struct {
	v    float64
	text string
}

// condition keys stand in for the reading as the Confirm candidate of the
// types that fire on a condition (ocr_match, numeric), so Confirm counts
// readings in a row that meet it, not identical texts: 25.1, 25.2, 25.3
// above 25 is three in a row, and so are two spellings of a match.
const (
	condMet   = "cond:true"
	condUnmet = "cond:false"
)

func condKey(met bool) string {
	if met {
		return condMet
	}
	return condUnmet
}

type Evaluator struct {
	cfg       config.Trigger
	re        *regexp.Regexp
	stable    string
	hasStable bool
	candidate string
	candCount int
	numCond   bool
	lastFired time.Time
	// lastText/textRun count identical readings in a row (Settled);
	// nums is the numeric window behind Value, oldest first.
	lastText string
	textRun  int
	nums     []numRead
	// pixText is pixel_change's held Settled reading (hasPix once there
	// is one), pixAbove the side of Threshold it was on and pixAt when it
	// was taken.
	pixText  string
	hasPix   bool
	pixAbove bool
	pixAt    time.Time
	// Now is the clock; tests replace it.
	Now func() time.Time
	// SevenSeg says the readings come from the seven-segment decoder, whose
	// '?' is a digit it couldn't read rather than text, and whose blank
	// reading is a frame with no digit on it. A numeric watch skips every
	// such reading (see unread): it counts toward Confirm in neither
	// direction, leaves the settled state alone and never reaches Value.
	// ocr_match still judges the text as read, '?' included, so a pattern
	// can catch a '?' on purpose ("End" reads as "?"); a '?' reading the
	// pattern doesn't match, or one with a digit the decoder wasn't sure
	// of (ObserveRead), is skipped the same way.
	SevenSeg bool
	// StaleAfter bounds how long a run of skipped readings may keep a
	// settled condition. A display that goes dark for a minute between
	// two frames of the same text is one event; one that has shown
	// nothing readable for longer than this may have been through
	// anything, so the watch forgets what it had settled on (the fire
	// time stays, so a running cooldown still holds) and a condition that
	// holds when the display comes back is reported again. The runner
	// sets it to the window a restart trusts a saved state for (15
	// minutes, or twice the slowest poll interval); zero never forgets.
	StaleAfter time.Duration
	// lastRead is when the last reading that wasn't skipped came in, or,
	// when none has since the start, a restore or a forget, the first
	// skipped one.
	lastRead time.Time
}

// leaveFactor is how many times Confirm it takes to leave a condition
// that has been met, against Confirm to enter it (hysteresis). A filmed
// screen that still shows the condition is misread now and then, two or
// three frames in a row on a bad night: round 6's two-hour recording has
// runs of two garbled "Kernel panic" lines and three bare '?' readings of
// a boiler's E4, each of which left the condition at Confirm 2 or 3 and
// sent the same alert again minutes later, once the cooldown ended. A
// display that really changed reads the new text on every frame, so
// leaving at twice Confirm costs a few seconds and nothing else.
const leaveFactor = 2

func New(cfg config.Trigger) (*Evaluator, error) {
	e := &Evaluator{cfg: cfg, Now: time.Now}
	if e.cfg.Confirm <= 0 {
		e.cfg.Confirm = 1
	}
	switch cfg.Type {
	case "ocr_match":
		if cfg.Pattern == "" {
			return nil, errors.New("ocr_match requires a pattern")
		}
		re, err := regexp.Compile(cfg.Pattern)
		if err != nil {
			return nil, fmt.Errorf("pattern: %w", err)
		}
		e.re = re
	case "numeric":
		if cfg.Op != "gt" && cfg.Op != "lt" {
			return nil, fmt.Errorf("numeric op must be gt or lt, got %q", cfg.Op)
		}
		pat := cfg.Pattern
		if pat == "" {
			pat = `[-+]?\d+(?:\.\d+)?`
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("pattern: %w", err)
		}
		e.re = re
	case "ocr_changed", "pixel_change":
	default:
		return nil, fmt.Errorf("unknown trigger type %q", cfg.Type)
	}
	return e, nil
}

// State is what an Evaluator has to remember for a restart not to look like
// news: when it last fired, which is where Cooldown counts from, and the
// settled state its edge logic compares the next confirmed reading with.
// Everything else it holds (the Confirm count in progress, the numeric
// window, Home Assistant's settled reading) rebuilds itself within a few
// readings and starts empty.
type State struct {
	// LastFired is zero for a watch that has never fired.
	LastFired time.Time
	// Stable is the last confirmed state, once there is one (HasStable):
	// the text itself for ocr_changed, and "cond:true" or "cond:false" for
	// ocr_match and numeric, which settle on whether the condition holds.
	// pixel_change has none.
	Stable    string
	HasStable bool
}

// Equal reports whether two states are the same, comparing the fire times
// as instants (a time read back from storage has lost its monotonic clock
// reading and location, which == would count as a difference).
func (s State) Equal(o State) bool {
	return s.LastFired.Equal(o.LastFired) && s.HasStable == o.HasStable && s.Stable == o.Stable
}

// State returns what Restore needs to carry on from here.
func (e *Evaluator) State() State {
	s := State{LastFired: e.lastFired}
	if e.hasStable {
		s.Stable, s.HasStable = e.stable, true
	}
	return s
}

// Restore puts back a State an earlier Evaluator for the same trigger
// exported, so the first readings after a restart are judged against what
// was already known: a condition that was met and still is doesn't fire
// again, and a cooldown that was running ends when it was going to. The
// caller decides whether s still applies (same trigger, recent enough); a
// settled state this trigger type could not have produced is dropped, the
// fire time is kept. Call it before the first reading. The saved state
// doesn't say when the display was last read, so StaleAfter counts from
// the first reading after the restore (see skipped): a dark spell that
// goes on after the restart forgets the state as one that began there
// would.
func (e *Evaluator) Restore(s State) {
	e.lastFired = s.LastFired
	e.lastRead = time.Time{}
	e.stable, e.hasStable, e.numCond = "", false, false
	if !s.HasStable {
		return
	}
	switch e.cfg.Type {
	case "ocr_changed":
	case "ocr_match", "numeric":
		if s.Stable != condMet && s.Stable != condUnmet {
			return
		}
	default: // pixel_change keeps no settled state
		return
	}
	e.stable, e.hasStable = s.Stable, true
	e.numCond = e.cfg.Type == "numeric" && s.Stable == condMet
}

func (e *Evaluator) coolingDown() bool {
	return e.cfg.Cooldown > 0 && !e.lastFired.IsZero() &&
		e.Now().Sub(e.lastFired) < time.Duration(e.cfg.Cooldown)
}

// cooldownEnds is when the running cooldown ends, or zero when none is.
func (e *Evaluator) cooldownEnds() time.Time {
	if !e.coolingDown() {
		return time.Time{}
	}
	return e.lastFired.Add(time.Duration(e.cfg.Cooldown))
}

func (e *Evaluator) fire(reading, reason string) Event {
	e.lastFired = e.Now()
	return Event{Fired: true, Reading: reading, Reason: reason}
}

// ObserveText feeds one OCR reading through confirm + edge + cooldown logic.
//
// Confirm counts consecutive readings with the same candidate key: the text
// itself for ocr_changed (it fires on the text settling on a new value), and
// whether the condition holds for ocr_match and numeric (condKey). A numeric
// reading with no number in it says nothing either way, so it resets the
// count rather than counting as "below". A reading that says nothing about
// the condition (unread: a seven-segment frame with nothing lit, or a
// digit the decoder couldn't read, unless the pattern matches it) is skipped
// outright: it neither counts nor resets, so "22.5, ?2.5, 22.5" is two
// readings in a row above 20, and so is "22.5, , 22.5", and a steady
// display read now whole, now partly or not at all, never looks like it
// left the condition and came back. Once the condition has been met it
// takes leaveFactor times Confirm readings in a row that don't meet it to
// leave (need), so a couple of misread frames don't re-arm the watch. A
// run of skipped readings longer than StaleAfter forgets the settled
// state instead (see skipped).
func (e *Evaluator) ObserveText(s string) Event {
	return e.ObserveRead(s, false)
}

// ObserveRead is ObserveText for a seven-segment reading that comes with
// what the decoder knew about its '?': unsure says one of them is a digit
// it saw and wasn't sure of (ocr.HasUnsureDigit), not a glyph that spells
// no digit. A pattern that catches a '?' is written for the second kind
// (a boiler's E4 reads "?4"), so ocr_match skips an unsure reading
// whatever the pattern says, as numeric does: a 52 whose 5 is half lit by
// glare reads "?2", and that is not a fault code.
func (e *Evaluator) ObserveRead(s string, unsure bool) Event {
	ev, quiet := e.observeText(s, unsure)
	ev.Unsure = unsure
	switch {
	case e.cfg.Type == "numeric":
		e.settleNumber(&ev, s)
	case quiet:
		// A '?' reading the watch skipped, or took as its stable text
		// read partly, says nothing about the display: it neither
		// settles nor breaks the run of the text that did, so Home
		// Assistant's Reading keeps the last text read whole.
	default:
		e.settleText(&ev, s)
	}
	return ev
}

// settleText marks ev Settled once s has been read Confirm times in a row.
func (e *Evaluator) settleText(ev *Event, s string) {
	if e.textRun > 0 && s == e.lastText {
		e.textRun++
	} else {
		e.lastText, e.textRun = s, 1
	}
	if e.textRun >= e.cfg.Confirm {
		ev.Settled, ev.HasSettled = s, true
	}
}

// settleNumber adds s's number to the window and, once the window is
// full, sets ev's Value and Settled from its median reading. A reading
// with no number is text the panel shows instead ("Err", "OFF"): it
// settles like any text and leaves the window alone. The seven-segment
// decoder's '?' readings are digits it couldn't read, not text, so they
// settle nothing.
func (e *Evaluator) settleNumber(ev *Event, s string) {
	if e.unread(s) {
		e.textRun = 0
		return
	}
	v, ok := e.extract(s)
	if !ok {
		if s != "" {
			e.settleText(ev, s)
		} else {
			e.textRun = 0
		}
		return
	}
	e.textRun = 0
	size := 2*e.cfg.Confirm - 1
	e.nums = append(e.nums, numRead{v, s})
	if len(e.nums) > size {
		e.nums = append(e.nums[:0], e.nums[len(e.nums)-size:]...)
	}
	if len(e.nums) < size {
		return
	}
	sorted := append([]numRead(nil), e.nums...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].v < sorted[j].v })
	mid := sorted[size/2]
	ev.Value, ev.HasValue = mid.v, true
	ev.Settled, ev.HasSettled = mid.text, true
}

// observeText is ObserveText's confirm, edge and cooldown logic. quiet
// reports a seven-segment '?' reading the watch skipped or took as its
// stable text read partly, which ObserveText then doesn't settle.
func (e *Evaluator) observeText(s string, unsure bool) (ev Event, quiet bool) {
	key, met := s, false
	partial := e.SevenSeg && strings.Contains(s, "?")
	switch e.cfg.Type {
	case "ocr_changed":
		if e.partlyRead(s) {
			key = e.stable
			quiet = true
		}
	case "ocr_match":
		met = !unsure && e.re.MatchString(s)
		if !met && (unsure || e.unread(s)) {
			return e.skipped(s), partial
		}
		key = condKey(met)
	case "numeric":
		if e.unread(s) {
			return e.skipped(s), partial
		}
		v, ok := e.extract(s)
		if !ok {
			e.candidate, e.candCount = "", 0
			e.lastRead = e.Now()
			return Event{Reading: s}, false
		}
		met = (e.cfg.Op == "gt" && v > e.cfg.Threshold) ||
			(e.cfg.Op == "lt" && v < e.cfg.Threshold)
		key = condKey(met)
	}
	e.lastRead = e.Now()
	if e.candCount > 0 && key == e.candidate {
		e.candCount++
	} else {
		e.candidate = key
		e.candCount = 1
	}
	if e.candCount < e.need(key) {
		ev := Event{Reading: s}
		if e.edge(key, met) {
			ev.Pending, ev.Need = e.candCount, e.cfg.Confirm
			ev.CooldownEnds = e.cooldownEnds()
		}
		return ev, quiet
	}
	if e.hasStable && e.stable == key {
		return Event{Reading: s}, quiet // no transition
	}

	// A cooldown DELAYS notification of a persisting new state rather than
	// dropping it: when a would-be transition lands inside the cooldown
	// window, e.stable/e.numCond are left untouched (instead of committing
	// the suppressed edge) so that, if the new state still holds once the
	// window ends, the next confirmed reading still sees the old
	// (pre-edge) state and fires the transition then. If the state reverted
	// before expiry, the (non-edge) revert commits normally and nothing
	// fires — it resolved on its own.
	wouldFire := e.edge(key, met)
	if wouldFire && e.coolingDown() {
		return Event{Reading: s, Pending: e.cfg.Confirm, Need: e.cfg.Confirm, CooldownEnds: e.cooldownEnds()}, quiet
	}
	e.stable, e.hasStable = key, true
	if e.cfg.Type == "numeric" {
		e.numCond = met
	}
	if !wouldFire {
		return Event{Reading: s}, quiet
	}
	switch e.cfg.Type {
	case "ocr_changed":
		return e.fire(s, "text changed"), false
	case "ocr_match":
		return e.fire(s, "pattern matched"), false
	}
	v, _ := e.extract(s)
	return e.fire(s, fmt.Sprintf("value %v crossed threshold", v)), false
}

// partlyRead reports whether s is the text an ocr_changed watch settled
// on, read partly: a seven-segment reading with a '?' in it that fits the
// stable text (fits). "?2.5" after "22.5" is the same display with a digit
// lost to a shake or a glare, not a change, and it counts as the stable
// text: a steady display read whole on one frame and partly on the next
// never fires "text changed". "?" after "0:04" (a washer's End) and a
// blank frame don't fit and still count as changes.
//
// The other way round, a whole reading that fits a partly read stable
// text ("22.5" after "?2.5", at start-up or after a change the camera
// first caught partly) is a new candidate that commits without firing
// (edge): the watch moves on to the text it can trust, and a change that
// read "?3.5" twice and then "23.5" fires once, not twice. A real change
// fires once the new text has been read whole Confirm times; a digit that
// never reads cleanly keeps the old text, the same cost a numeric watch
// pays for skipping every '?'. A fault code that shares its digits with
// the number it replaces ("E4" reads "?4", which fits "54") is the known
// cost; the boiler recipe's ocr_match watch catches that.
func (e *Evaluator) partlyRead(s string) bool {
	return e.hasStable && s != e.stable && strings.Contains(s, "?") && e.fits(s, e.stable)
}

// fits reports whether two seven-segment readings may be one display, one
// or both of them read partly: the same length, and at every position the
// same glyph or a '?' on either side.
func (e *Evaluator) fits(a, b string) bool {
	if !e.SevenSeg || len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] && a[i] != '?' && b[i] != '?' {
			return false
		}
	}
	return true
}

// need is how many readings in a row with candidate key it takes to
// believe it: Confirm, or leaveFactor times that to leave a condition
// that has been met and fired (hysteresis). A met edge the cooldown is
// holding hasn't committed, so readings against it need only Confirm, and
// the edge is dropped as before if they get there.
func (e *Evaluator) need(key string) int {
	if key == condUnmet && e.hasStable && e.stable == condMet {
		return leaveFactor * e.cfg.Confirm
	}
	return e.cfg.Confirm
}

// skipped is the Event for a reading the watch skips (unread). The count
// in progress is left as it is, and the event carries it, so the Live
// tile for the skipped frame still says "1 of 3 needed to fire" or "held
// by Cooldown" instead of dropping the line until the next clean reading.
//
// Once the watch has read nothing for StaleAfter, though, it starts
// fresh: the settled state, the count and the numeric window go, the fire
// time stays. A text that is still there when the display comes back is
// then news, held back by a running cooldown as any fire is. A run of
// skipped readings with no reading before it (after a restore) is timed
// from its first.
func (e *Evaluator) skipped(s string) Event {
	ev := Event{Reading: s}
	if e.lastRead.IsZero() {
		e.lastRead = e.Now()
	}
	if e.StaleAfter > 0 && e.Now().Sub(e.lastRead) > e.StaleAfter {
		e.stable, e.hasStable, e.numCond = "", false, false
		e.candidate, e.candCount = "", 0
		e.nums, e.lastText, e.textRun = nil, "", 0
		e.lastRead = time.Time{}
	}
	if e.candCount == 0 {
		return ev
	}
	met := e.candidate == condMet
	if !e.edge(e.candidate, met) {
		return ev
	}
	switch {
	case e.candCount < e.cfg.Confirm:
		ev.Pending, ev.Need = e.candCount, e.cfg.Confirm
		ev.CooldownEnds = e.cooldownEnds()
	case e.coolingDown():
		ev.Pending, ev.Need = e.cfg.Confirm, e.cfg.Confirm
		ev.CooldownEnds = e.cooldownEnds()
	}
	return ev
}

// edge reports whether a reading with candidate key (and, for the
// condition types, met) would fire once confirmed, cooldown aside: the
// text moved off a stable value (ocr_changed; a whole reading of a text
// the watch settled on partly isn't a move, see partlyRead), or the
// condition turned true from false or from nothing yet (ocr_match,
// numeric).
func (e *Evaluator) edge(key string, met bool) bool {
	switch e.cfg.Type {
	case "ocr_changed":
		return e.hasStable && e.stable != key && !e.fits(e.stable, key)
	case "ocr_match":
		return met && !(e.hasStable && e.stable == condMet)
	case "numeric":
		return met && !e.numCond
	}
	return false
}

// ObservePixel feeds one pixel-diff percentage. Threshold + cooldown only.
func (e *Evaluator) ObservePixel(pct float64) Event {
	reading := fmt.Sprintf("%.1f%% changed", pct)
	ev := Event{Reading: reading}
	if pct >= e.cfg.Threshold && !e.coolingDown() {
		ev = e.fire(reading, "pixel change")
	}
	e.settlePixel(&ev, pct, reading)
	return ev
}

// pixelSettleEvery is how often pixel_change's settled reading may follow
// the percentage while it stays on one side of Threshold. A camera's
// noise moves it by a fraction of a percent on every frame; published
// each time, that is a new Home Assistant state every few seconds that
// says nothing.
const pixelSettleEvery = time.Minute

// settlePixel sets ev's Settled for pixel_change: the first reading at
// once, then a new one when it crosses Threshold (a fire, or the change
// ending) or when pixelSettleEvery has passed.
func (e *Evaluator) settlePixel(ev *Event, pct float64, reading string) {
	above := pct >= e.cfg.Threshold
	now := e.Now()
	if !e.hasPix || above != e.pixAbove || (reading != e.pixText && now.Sub(e.pixAt) >= pixelSettleEvery) {
		e.pixText, e.hasPix, e.pixAbove, e.pixAt = reading, true, above, now
	}
	ev.Settled, ev.HasSettled = e.pixText, true
}

// Condition is what a single reading says about a trigger's condition, for
// a one-off check such as the web UI's "Test this region". It is stateless
// on purpose: whether a watch actually notifies also depends on the
// readings before this one (edges), Confirm and Cooldown, so callers must
// present Met as "the condition holds", never as "this will notify".
type Condition struct {
	// Evaluable is false for the types one reading can't decide:
	// pixel_change compares consecutive frames, and ocr_changed compares a
	// reading with the previous stable one.
	Evaluable bool
	Met       bool
	// Skipped is a reading the watch ignores (Met is false, and it doesn't
	// count as "not met" either): a seven-segment reading with nothing
	// lit or with a digit the decoder couldn't read that the pattern
	// (ocr_match) doesn't match, or one with a digit it wasn't sure of
	// (CheckRead).
	Skipped bool
	// Detail says why, in a short sentence without a trailing period.
	Detail string
}

// Check evaluates cfg's condition against one reading from a text engine
// (tesseract, rapidocr), where a '?' is just text. The error is
// trigger.New's: an unknown type, a missing or invalid pattern, a bad op.
func Check(cfg config.Trigger, reading string) (Condition, error) {
	return CheckEngine(cfg, "", reading)
}

// CheckEngine is Check for a reading from engine, judged exactly as a
// running watch with that engine judges it: on the seven-segment decoder a
// watch skips a blank reading and one with a '?' in it, unless its
// ocr_match pattern matches that (Skipped).
func CheckEngine(cfg config.Trigger, engine, reading string) (Condition, error) {
	return CheckRead(cfg, engine, reading, false)
}

// CheckRead is CheckEngine for a reading that comes with ObserveRead's
// unsure: an ocr_match pattern doesn't get to match a digit the decoder
// wasn't sure of, so the reading is skipped.
func CheckRead(cfg config.Trigger, engine, reading string, unsure bool) (Condition, error) {
	e, err := New(cfg)
	if err != nil {
		return Condition{}, err
	}
	e.SevenSeg = engine == "sevenseg"
	switch cfg.Type {
	case "ocr_match":
		if unsure {
			return Condition{Evaluable: true, Skipped: true, Detail: "A digit wasn't read for sure, so the pattern isn't checked: this reading is skipped and the watch keeps its last state"}, nil
		}
		if e.re.MatchString(reading) {
			return Condition{Evaluable: true, Met: true, Detail: "The text matches the pattern"}, nil
		}
		if e.unread(reading) {
			detail := "The pattern doesn't match it, and a digit wasn't read for sure, so this reading is skipped and the watch keeps its last state"
			if reading == "" {
				detail = "Nothing was read, so this reading is skipped and the watch keeps its last state"
			}
			return Condition{Evaluable: true, Skipped: true, Detail: detail}, nil
		}
		return Condition{Evaluable: true, Detail: "The text doesn't match the pattern"}, nil
	case "numeric":
		if e.unread(reading) {
			detail := "A digit wasn't read for sure, so this reading is skipped and the watch keeps its last state"
			if reading == "" {
				detail = "Nothing was read, so this reading is skipped and the watch keeps its last state"
			}
			return Condition{Evaluable: true, Skipped: true, Detail: detail}, nil
		}
		v, ok := e.extract(reading)
		if !ok {
			return Condition{Evaluable: true, Detail: "No number found in the reading"}, nil
		}
		word := "above"
		met := v > cfg.Threshold
		if cfg.Op == "lt" {
			word, met = "below", v < cfg.Threshold
		}
		num := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
		if met {
			return Condition{Evaluable: true, Met: true, Detail: fmt.Sprintf("%s is %s %s", num(v), word, num(cfg.Threshold))}, nil
		}
		return Condition{Evaluable: true, Detail: fmt.Sprintf("%s is not %s %s", num(v), word, num(cfg.Threshold))}, nil
	case "ocr_changed":
		return Condition{Detail: "Fires when the text changes from one stable reading to another"}, nil
	}
	return Condition{Detail: "Pixel change compares each frame with the one before, so a single reading has no verdict"}, nil
}

// extract finds the number in s: the pattern's first capture group when
// it matched something, else the whole match.
func (e *Evaluator) extract(s string) (float64, bool) {
	loc := e.re.FindStringSubmatchIndex(s)
	if loc == nil {
		return 0, false
	}
	start, end := loc[0], loc[1]
	if len(loc) >= 4 && loc[2] >= 0 && loc[3] > loc[2] {
		start, end = loc[2], loc[3]
	}
	f, err := strconv.ParseFloat(s[start:end], 64)
	return f, err == nil
}

// unread reports whether s is a reading that says nothing about the
// condition: nothing read at all (""), or a seven-segment reading with a
// digit the decoder saw but couldn't read (any '?' at all). A numeric
// watch skips it, and so does ocr_match when its pattern doesn't match
// the reading as it is (the caller checks that first: a pattern can catch
// a '?' or a blank on purpose).
//
// Not just a '?' inside or next to the number the pattern picks: the
// default pattern finds "0" in "??.0" (71.0 on the display), "5.5" in
// "?5.5" and "3" in "?.3", each a wrong number with the '?' a character or
// two away. The decoder's output has no spaces, so a '?' elsewhere is on
// the same display, and still changes what the number means: "?:04" may
// be 1:04 rather than 4 minutes. Test this region shows such a reading as
// skipped, so a box that always catches an unreadable glyph is noticed and
// tightened rather than read wrong.
//
// A blank seven-segment reading is a frame with no digit lit: a display
// caught dark between refreshes, a glare, a backlight that went off. It
// isn't "Err" or "OFF" (text a panel shows instead of a number, which does
// reset a numeric count): the display said nothing, so the count in
// progress stands, as it does across a '?'. A text engine's blank is
// different: a status line that clears when the fault does reads blank,
// and that is how the watch sees the fault end, so it counts as not
// meeting an ocr_match pattern. A frame tesseract makes nothing of now and
// then (round 6's heat-pump panel read "" and "." between frames of its
// fault code) can't re-arm the watch on its own, since leaving takes
// leaveFactor times Confirm readings in a row.
func (e *Evaluator) unread(s string) bool {
	return e.SevenSeg && (s == "" || strings.Contains(s, "?"))
}
