package trigger

import (
	"strconv"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
)

// V1.1: a value watch reads, settles and publishes its number with no op
// or threshold, and never fires. Before it, New refused the type.
func TestValueWatchPublishesWithoutThreshold(t *testing.T) {
	e, err := New(config.Trigger{Type: "value", Confirm: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.SevenSeg = true
	var got []float64
	for _, s := range []string{"1234.5", "1234.5", "?234.5", "", "1234.6", "1234.6", "1234.7"} {
		ev := e.ObserveText(s)
		if ev.Fired || ev.Pending != 0 {
			t.Fatalf("%q: a value watch never fires or counts toward one: %+v", s, ev)
		}
		if ev.HasValue {
			got = append(got, ev.Value)
		}
	}
	// Window 3 (2*confirm-1): the '?' and blank readings never count.
	want := []float64{1234.5, 1234.6, 1234.6}
	if len(got) != len(want) {
		t.Fatalf("values %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("values %v, want %v", got, want)
		}
	}
}

func TestValueWatchCheck(t *testing.T) {
	trig := config.Trigger{Type: "value", Pattern: `(\d+\.\d)`}
	c, err := CheckEngine(trig, "tesseract", "Total 01234.5 kWh")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Evaluable || !c.HasValue || c.Value != 1234.5 || c.Skipped {
		t.Errorf("Check = %+v", c)
	}
	c, _ = CheckEngine(trig, "sevenseg", "12?4.5")
	if !c.Skipped || c.HasValue {
		t.Errorf("a partial reading publishes nothing: %+v", c)
	}
	c, _ = CheckEngine(trig, "tesseract", "Err")
	if c.HasValue || c.Met {
		t.Errorf("no number: %+v", c)
	}
}

func counterValues(t *testing.T, e *Evaluator, readings []string) (vals []float64, holds []*Hold) {
	t.Helper()
	for _, s := range readings {
		ev := e.ObserveText(s)
		if ev.HasValue && ev.Held != nil {
			t.Fatalf("%q: a held value must not also publish: %+v", s, ev)
		}
		if ev.HasValue {
			vals = append(vals, ev.Value)
		}
		holds = append(holds, ev.Held)
	}
	return vals, holds
}

// V1.3: a counter never goes down. A lower reading is held, not
// published, until the display is back at the last value or the lower
// value is accepted (a replaced meter).
func TestCounterNeverDown(t *testing.T) {
	e, err := New(config.Trigger{Type: "value", Confirm: 1})
	if err != nil {
		t.Fatal(err)
	}
	e.SetCounter(0, 2)
	vals, holds := counterValues(t, e, []string{"1240.1", "1240.3", "1234.5", "1234.5", "1240.3", "1240.4"})
	if want := []float64{1240.1, 1240.3, 1240.3, 1240.4}; !equalFloats(vals, want) {
		t.Fatalf("published %v, want %v", vals, want)
	}
	h := holds[2]
	if h == nil || !h.Lower || h.Value != 1234.5 || h.Last != 1240.3 {
		t.Fatalf("hold = %+v", h)
	}
	// The meter is replaced: it reads 12.0 from now on.
	vals, holds = counterValues(t, e, []string{"12.0", "12.0"})
	if len(vals) != 0 || holds[1] == nil || !holds[1].Lower {
		t.Fatalf("lower values before accept: published %v holds %+v", vals, holds)
	}
	if e.AcceptLower(1300) {
		t.Fatal("a value above the last isn't a lower one to accept")
	}
	if !e.AcceptLower(12.0) {
		t.Fatal("AcceptLower(12.0) refused")
	}
	if last, ok := e.CounterLast(); !ok || last != 12.0 {
		t.Fatalf("CounterLast = %v %v", last, ok)
	}
	vals, _ = counterValues(t, e, []string{"12.0", "12.1"})
	if want := []float64{12.0, 12.1}; !equalFloats(vals, want) {
		t.Fatalf("after accept published %v, want %v", vals, want)
	}
}

// RestoreCounter carries the last value across a restart: the first
// lower reading after it is held, not taken as the new start.
func TestCounterRestoreHoldsLower(t *testing.T) {
	e, _ := New(config.Trigger{Type: "value", Confirm: 1})
	e.SetCounter(0, 2)
	e.RestoreCounter(500)
	vals, holds := counterValues(t, e, []string{"400", "501"})
	if want := []float64{501}; !equalFloats(vals, want) || holds[0] == nil || !holds[0].Lower {
		t.Fatalf("published %v holds %+v", vals, holds)
	}
}

// V1.4: one misread top digit is held; a real jump passes once the
// readings agree. Confirm 1 still needs two (config.Watch.RiseConfirm).
func TestCounterRiseLimit(t *testing.T) {
	e, _ := New(config.Trigger{Type: "value", Confirm: 1})
	e.SetCounter(5, 2)
	vals, holds := counterValues(t, e, []string{"1234.5", "1235.0", "9235.0", "1235.2", "1480.0", "1480.1", "1480.2"})
	if want := []float64{1234.5, 1235.0, 1235.2, 1480.1, 1480.2}; !equalFloats(vals, want) {
		t.Fatalf("published %v, want %v", vals, want)
	}
	if h := holds[2]; h == nil || h.Lower || h.Value != 9235 || h.Count != 1 || h.Need != 2 || h.MaxRise != 5 {
		t.Fatalf("misread hold = %+v", h)
	}
	if h := holds[4]; h == nil || h.Count != 1 {
		t.Fatalf("jump hold = %+v", h)
	}
}

// With confirm 3 the median already hides two misreads in a row; the rise
// limit also holds three, which win the median for three readings, since
// a jump needs all five readings of the window (RiseConfirm) to agree.
func TestCounterRiseLimitWithMedian(t *testing.T) {
	e, _ := New(config.Trigger{Type: "value", Confirm: 3})
	e.SetCounter(1, 5)
	readings := []string{"100", "100", "100", "100", "100", // window full: 100
		"900", "900", "900", // median 900 from the third: held 1
		"100", "100", "100"} // back: published 100 again
	vals, holds := counterValues(t, e, readings)
	for _, v := range vals {
		if v != 100 {
			t.Fatalf("published %v: the jump must not get through", vals)
		}
	}
	held := 0
	for _, h := range holds {
		if h != nil {
			held++
		}
	}
	if held == 0 {
		t.Fatal("the three misreads won the median; the rise limit should hold them")
	}
	// A real jump that stays.
	vals, _ = counterValues(t, e, []string{"150", "150", "150", "150", "150", "150"})
	if len(vals) == 0 || vals[len(vals)-1] != 150 {
		t.Fatalf("a jump that stays must get through: %v", vals)
	}
}

// A numeric watch with a counter gates its Value the same way and still
// judges each reading against its threshold.
func TestNumericCounterStillFires(t *testing.T) {
	e, _ := New(config.Trigger{Type: "numeric", Op: "gt", Threshold: 50, Confirm: 1})
	e.SetCounter(0, 2)
	if ev := e.ObserveText("40"); !ev.HasValue || ev.Value != 40 {
		t.Fatalf("first: %+v", ev)
	}
	ev := e.ObserveText("60")
	if !ev.Fired || ev.Value != 60 {
		t.Fatalf("crossing: %+v", ev)
	}
	if ev := e.ObserveText("30"); ev.HasValue || ev.Held == nil {
		t.Fatalf("lower: %+v", ev)
	}
}

// A meter that rises more than max_rise on every reading (the limit set
// a little low, a long poll in heavy use) still gets published: each
// reading is above the one before at about the same pace. Before the
// pace rule, every reading started the run again and 1 value of 200 got
// through. A misread top digit in the middle of the climb is still held.
func TestCounterRiseFasterThanLimit(t *testing.T) {
	for confirm := 1; confirm <= 3; confirm++ {
		e, _ := New(config.Trigger{Type: "value", Confirm: confirm})
		e.SetCounter(5, config.Watch{Trigger: config.Trigger{Confirm: confirm}}.RiseConfirm())
		var readings []string
		for i := 0; i < 200; i++ {
			v := 100 + 7*i
			if i == 120 {
				v += 900000 // a misread top digit
			}
			readings = append(readings, strconv.Itoa(v))
		}
		vals, _ := counterValues(t, e, readings)
		if len(vals) < 150 {
			t.Fatalf("confirm %d: %d of 200 readings published (last %v): a steady climb above max_rise must get through", confirm, len(vals), vals[len(vals)-1])
		}
		for i, v := range vals {
			if v > 100000 {
				t.Fatalf("confirm %d: the misread %v was published", confirm, v)
			}
			if i > 0 && v < vals[i-1] {
				t.Fatalf("confirm %d: went down %v -> %v", confirm, vals[i-1], v)
			}
		}
		if last := vals[len(vals)-1]; last < 1300 {
			t.Fatalf("confirm %d: last published %v, the meter is at 1493", confirm, last)
		}
	}
}

// A numeric watch with a counter doesn't fire on a reading its counter
// holds back: the page would call it a misread and the alert would call
// it real. Before, house-alarm fired on 904815. A real jump the counter
// lets through still fires.
func TestNumericCounterIgnoresHeldReadings(t *testing.T) {
	e, _ := New(config.Trigger{Type: "numeric", Op: "gt", Threshold: 4850, Confirm: 2})
	e.SetCounter(20, 3)
	for _, s := range []string{"4815", "4815", "4815", "904815", "904815", "4816", "4816", "4790", "4790", "4816"} {
		if ev := e.ObserveText(s); ev.Fired {
			t.Fatalf("%s fired: %+v", s, ev)
		}
	}
	fired := 0
	for _, s := range []string{"4900", "4900", "4900", "4900", "4900"} {
		ev := e.ObserveText(s)
		if ev.Fired {
			fired++
		}
	}
	if fired != 1 {
		t.Fatalf("a real jump over the threshold fired %d times, want 1", fired)
	}
}

// Two or more different misreads in a row, each higher than the one
// before, on a meter that is barely moving. The reading that starts a
// jump doesn't set the pace its successor is judged by: before, the first
// misread's step (800000) widened the window so the second agreed, and
// 904815 was published (and fired) at confirm 1 and 2.
func TestCounterRisingMisreadsDontAgree(t *testing.T) {
	mis := []string{"804815", "904815", "914815", "954815", "984815"}
	for confirm := 1; confirm <= 3; confirm++ {
		need := config.Watch{Trigger: config.Trigger{Confirm: confirm}}.RiseConfirm()
		e, _ := New(config.Trigger{Type: "numeric", Op: "gt", Threshold: 100000, Confirm: confirm})
		e.SetCounter(20, need)
		var rs []string
		for i := 0; i < 2*confirm+2; i++ {
			rs = append(rs, "4815")
		}
		rs = append(rs, mis[:need]...)
		rs = append(rs, "4816", "4816", "4816", "4816", "4816", "4816")
		for _, s := range rs {
			ev := e.ObserveText(s)
			if ev.HasValue && ev.Value > 100000 {
				t.Fatalf("confirm %d: misread %v published (readings %v)", confirm, ev.Value, rs)
			}
			if ev.Fired {
				t.Fatalf("confirm %d: fired on %s", confirm, s)
			}
		}
		if last, _ := e.CounterLast(); last != 4816 {
			t.Fatalf("confirm %d: last value %v, want 4816", confirm, last)
		}
	}
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
