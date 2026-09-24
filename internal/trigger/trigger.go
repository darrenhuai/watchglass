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
	// with no number, or (SevenSeg) a partly read one, don't count.
	// HasValue is false until the window has filled, and on every other
	// type.
	Value    float64
	HasValue bool
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
	// '?' is a digit it couldn't read rather than text: a number touching
	// one is only partly read and stays out of Value. (The condition still
	// judges what was read, as the web UI's Test does.)
	SevenSeg bool
}

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
// count rather than counting as "below".
func (e *Evaluator) ObserveText(s string) Event {
	ev := e.observeText(s)
	if e.cfg.Type == "numeric" {
		e.settleNumber(&ev, s)
	} else {
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
	v, partial, ok := e.extractPartial(s)
	if !ok || (partial && e.SevenSeg) {
		if !ok && s != "" && !(e.SevenSeg && strings.Contains(s, "?")) {
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

// observeText is ObserveText's confirm, edge and cooldown logic.
func (e *Evaluator) observeText(s string) Event {
	key, met := s, false
	switch e.cfg.Type {
	case "ocr_match":
		met = e.re.MatchString(s)
		key = condKey(met)
	case "numeric":
		v, ok := e.extract(s)
		if !ok {
			e.candidate, e.candCount = "", 0
			return Event{Reading: s}
		}
		met = (e.cfg.Op == "gt" && v > e.cfg.Threshold) ||
			(e.cfg.Op == "lt" && v < e.cfg.Threshold)
		key = condKey(met)
	}
	if e.candCount > 0 && key == e.candidate {
		e.candCount++
	} else {
		e.candidate = key
		e.candCount = 1
	}
	if e.candCount < e.cfg.Confirm {
		ev := Event{Reading: s}
		if e.edge(key, met) {
			ev.Pending, ev.Need = e.candCount, e.cfg.Confirm
			ev.CooldownEnds = e.cooldownEnds()
		}
		return ev
	}
	if e.hasStable && e.stable == key {
		return Event{Reading: s} // no transition
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
		return Event{Reading: s, Pending: e.cfg.Confirm, Need: e.cfg.Confirm, CooldownEnds: e.cooldownEnds()}
	}
	e.stable, e.hasStable = key, true
	if e.cfg.Type == "numeric" {
		e.numCond = met
	}
	if !wouldFire {
		return Event{Reading: s}
	}
	switch e.cfg.Type {
	case "ocr_changed":
		return e.fire(s, "text changed")
	case "ocr_match":
		return e.fire(s, "pattern matched")
	}
	v, _ := e.extract(s)
	return e.fire(s, fmt.Sprintf("value %v crossed threshold", v))
}

// edge reports whether a reading with candidate key (and, for the
// condition types, met) would fire once confirmed, cooldown aside: the
// text moved off a stable value (ocr_changed), or the condition turned
// true from false or from nothing yet (ocr_match, numeric).
func (e *Evaluator) edge(key string, met bool) bool {
	switch e.cfg.Type {
	case "ocr_changed":
		return e.hasStable && e.stable != key
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
	// Detail says why, in a short sentence without a trailing period.
	Detail string
}

// Check evaluates cfg's condition against one reading. The error is
// trigger.New's: an unknown type, a missing or invalid pattern, a bad op.
func Check(cfg config.Trigger, reading string) (Condition, error) {
	e, err := New(cfg)
	if err != nil {
		return Condition{}, err
	}
	switch cfg.Type {
	case "ocr_match":
		if e.re.MatchString(reading) {
			return Condition{Evaluable: true, Met: true, Detail: "The text matches the pattern"}, nil
		}
		return Condition{Evaluable: true, Detail: "The text doesn't match the pattern"}, nil
	case "numeric":
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
	return Condition{Detail: "Pixel change compares each frame with the one before, so one test has nothing to compare"}, nil
}

// extract finds the number in s: the pattern's first capture group when
// it matched something, else the whole match.
func (e *Evaluator) extract(s string) (float64, bool) {
	v, _, ok := e.extractPartial(s)
	return v, ok
}

// extractPartial is extract that also reports whether the number touches a
// '?': the seven-segment decoder's mark for a digit it saw but couldn't
// read, so "?4?" may well be 142 and "2?.5" isn't 2.
func (e *Evaluator) extractPartial(s string) (v float64, partial, ok bool) {
	loc := e.re.FindStringSubmatchIndex(s)
	if loc == nil {
		return 0, false, false
	}
	start, end := loc[0], loc[1]
	if len(loc) >= 4 && loc[2] >= 0 && loc[3] > loc[2] {
		start, end = loc[2], loc[3]
	}
	val := s[start:end]
	partial = strings.Contains(val, "?") || (start > 0 && s[start-1] == '?') || (end < len(s) && s[end] == '?')
	f, err := strconv.ParseFloat(val, 64)
	return f, partial, err == nil
}
