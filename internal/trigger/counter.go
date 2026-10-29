package trigger

import "math"

// A counter is a number that only goes up: the kWh on an electricity
// meter, the m³ on a water meter. Home Assistant sums it (state_class
// total_increasing) and takes any drop as a new meter, so one misread
// frame that reaches it either adds a meter's worth of energy to the
// Energy dashboard (a top digit read 8 for 3) or starts a fake new cycle
// (a drop). The gate below stands between the filtered Value and
// everything that publishes it.
//
// Never down: a value lower than the last one published is held back.
// It is not published and not recorded as the value until the display
// shows the last value or more again, or someone accepts it on the watch
// page (a replaced meter, AcceptLower).
//
// Rise limit: with MaxRise set, a value more than MaxRise above the last
// one is held until need readings in a row, as read, agree on the jump
// (agrees), and then published. The count is of readings, not of
// filtered values: the median keeps a burst of misreads for as many
// readings as the burst was long, so counting its output would let a
// burst of confirm misreads through. need is the whole median window
// (config.Watch.RiseConfirm), so a jump has to be read on every reading
// the Value is taken from, where an ordinary change only needs most of
// them. A misread top digit is gone again by the next reading; a real
// jump (the camera was off for a day) stays and gets through. So does a
// meter that climbs faster than MaxRise on every reading (a limit set a
// little low, a long poll in heavy use): it keeps a pace, and a reading
// that carries on at that pace agrees too.
//
// The pace only ever comes from readings already trusted (paceOf): a
// step between two readings within the limit, or a step that agreed. The
// reading that starts a jump never widens its own window, so a second
// misread higher than the first doesn't agree with it just because the
// first one was a big step.

// Hold says why a counter's value was held back, for the log and the
// watch page.
type Hold struct {
	// Value is the value held back, Last the last one published.
	Value float64
	Last  float64
	// Lower: Value is below Last (never down). Otherwise it rose more than
	// MaxRise, and Count of Need readings in a row have agreed on it.
	Lower bool
	Count int
	Need  int
	// MaxRise is the limit the jump went over (not Lower).
	MaxRise float64
}

type counterGate struct {
	maxRise float64
	need    int
	last    float64
	has     bool
	// jump is the latest reading of a rise over maxRise being confirmed,
	// and run how many readings in a row have agreed on it (observe).
	jump float64
	run  int
	// jumpStep is how far jump rose from the reading before it, and
	// jumpPaced whether that step can be taken as the meter's pace: the
	// reading before was trusted, or jump itself agreed with the run.
	jumpStep  float64
	jumpPaced bool
	// prev is the latest reading, and prevOK whether it was trusted:
	// within the limit and not below the last value, or agreeing with a
	// run. A step is only a pace when it starts at a trusted reading.
	prev   float64
	prevOK bool
	// paces are the latest trusted steps (addPace), the pace the meter
	// has been keeping. The largest of them counts (paceOf), so a meter
	// that alternates small and large steps keeps its large one.
	paces [4]float64
	np    int
}

// observe counts v, one number as read (before the median), toward a
// jump: run grows while readings over the limit agree with each other
// (agrees) and starts again at anything else.
func (c *counterGate) observe(v float64) {
	step, fromOK := v-c.prev, c.prevOK
	if !c.has || c.maxRise <= 0 || v-c.last <= c.maxRise {
		c.run = 0
		ok := !c.has || v >= c.last
		if ok && fromOK && c.has {
			c.addPace(step)
		}
		c.prev, c.prevOK = v, ok
		return
	}
	agreed := c.run > 0 && c.agrees(v)
	if agreed {
		c.run++
		c.addPace(step)
	} else {
		c.run = 1
	}
	c.jump, c.jumpStep, c.jumpPaced = v, step, fromOK || agreed
	c.prev, c.prevOK = v, agreed
}

func (c *counterGate) addPace(step float64) {
	c.paces[c.np%len(c.paces)] = math.Max(step, 0)
	c.np++
}

// paceOf is the meter's pace: the largest of its latest trusted steps.
func (c *counterGate) paceOf() float64 {
	p := 0.0
	for _, s := range c.paces {
		p = math.Max(p, s)
	}
	return p
}

// agrees reports whether v, read after jump, carries on the same jump:
// it is within maxRise of jump (the display shows the same number); or
// it is above jump by no more than maxRise plus twice the meter's pace
// (paceOf); or it rose from jump by about as much as jump rose from the
// trusted reading before it (a meter that went over the limit at its
// first step and keeps that pace). A misread top digit is none of these:
// the next reading drops back to the real number, and a second, different
// misread doesn't repeat the first one's step.
func (c *counterGate) agrees(v float64) bool {
	d := v - c.jump
	if math.Abs(d) <= c.maxRise {
		return true
	}
	if d < 0 {
		return false
	}
	return d <= c.maxRise+2*c.paceOf() ||
		(c.jumpPaced && math.Abs(d-c.jumpStep) <= c.maxRise)
}

// holds reports whether v, read now and not yet observed, is a number
// the gate doesn't trust yet: lower than the last value, or over the rise
// limit with fewer than need readings, v among them, agreeing on it. A
// numeric watch with a counter skips such a reading for its condition,
// so an alert never fires on a misread the Value holds back.
func (c *counterGate) holds(v float64) bool {
	if !c.has {
		return false
	}
	if v < c.last {
		return true
	}
	if c.maxRise <= 0 || v-c.last <= c.maxRise {
		return false
	}
	run := 1
	if c.run > 0 && c.agrees(v) {
		run = c.run + 1
	}
	return run < c.need
}

// SetCounter makes the Value a counter: never down, and, with maxRise
// above 0, held on a rise larger than that until need readings in a row
// agree. Call it before the first reading; it keeps the last value of an
// earlier SetCounter or RestoreCounter.
func (e *Evaluator) SetCounter(maxRise float64, need int) {
	if need < 1 {
		need = 1
	}
	if e.counter == nil {
		e.counter = &counterGate{}
	}
	e.counter.maxRise, e.counter.need = maxRise, need
}

// RestoreCounter puts back the last value a counter published, kept
// across a restart, so a lower value read after it is still held back.
// It does nothing on a watch that isn't a counter.
func (e *Evaluator) RestoreCounter(last float64) {
	if e.counter == nil {
		return
	}
	e.counter.last, e.counter.has = last, true
	e.counter.run = 0
}

// CounterLast is the last value the counter published (or was restored
// or accepted), false before there is one or on a watch that isn't a
// counter.
func (e *Evaluator) CounterLast() (float64, bool) {
	if e.counter == nil || !e.counter.has {
		return 0, false
	}
	return e.counter.last, true
}

// AcceptLower takes v as the counter's last value although it is lower
// than the one published: the meter was replaced or reset. Values from v
// up are published from the next reading on. It reports false, changing
// nothing, when v isn't below the last value (a page shown before the
// display came back up) or the watch isn't a counter.
func (e *Evaluator) AcceptLower(v float64) bool {
	c := e.counter
	if c == nil || !c.has || !(v < c.last) {
		return false
	}
	c.last, c.run = v, 0
	return true
}

// check passes v, the filtered value, (nil) when it may be published, and
// moves the last value to it; otherwise it says why it is held.
func (c *counterGate) check(v float64) *Hold {
	if !c.has {
		c.last, c.has, c.run = v, true, 0
		return nil
	}
	// run is observe's: the median lags the readings, so a value passed
	// here while the readings already show a jump leaves the count alone.
	if v < c.last {
		return &Hold{Value: v, Last: c.last, Lower: true}
	}
	if c.maxRise <= 0 || v-c.last <= c.maxRise || c.run >= c.need {
		c.last = v
		return nil
	}
	return &Hold{Value: v, Last: c.last, Count: c.run, Need: c.need, MaxRise: c.maxRise}
}
