package trigger

import (
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
)

func mk(t *testing.T, cfg config.Trigger) (*Evaluator, *time.Time) {
	t.Helper()
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Unix(1000, 0)
	e.Now = func() time.Time { return now }
	return e, &now
}

func feed(e *Evaluator, readings ...string) []bool {
	var fired []bool
	for _, r := range readings {
		fired = append(fired, e.ObserveText(r).Fired)
	}
	return fired
}

func TestConfirmSuppressesFlicker(t *testing.T) {
	e, _ := mk(t, config.Trigger{Type: "ocr_changed", Confirm: 2})
	// A A (baseline stable) -> glare flicker B once -> back to A -> C C (real change)
	got := feed(e, "A", "A", "B", "A", "A", "C", "C")
	want := []bool{false, false, false, false, false, false, true}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reading %d: fired=%v, want %v", i, got[i], want[i])
		}
	}
}

func TestOCRMatchFiresOnceOnEdge(t *testing.T) {
	e, _ := mk(t, config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 1})
	got := feed(e, "Printing 87%", "PRINT COMPLETE", "PRINT COMPLETE", "Print Complete")
	// fires on first match transition; identical stable reading doesn't re-fire;
	// "Print Complete" is a new stable reading but previous one also matched -> no edge.
	want := []bool{false, true, false, false}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reading %d: fired=%v, want %v", i, got[i], want[i])
		}
	}
}

func TestNumericCrossing(t *testing.T) {
	e, _ := mk(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 200, Confirm: 1})
	got := feed(e, "180", "250", "260", "150", "250")
	// fires entering >200; stays true 250->260 without re-firing; re-arms below; fires again.
	want := []bool{false, true, false, false, true}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reading %d: fired=%v, want %v", i, got[i], want[i])
		}
	}
}

func TestNumericCaptureGroup(t *testing.T) {
	e, _ := mk(t, config.Trigger{Type: "numeric", Op: "lt", Threshold: 60, Confirm: 1, Pattern: `Temp: (\d+)C`})
	got := feed(e, "Temp: 80C", "Temp: 45C")
	want := []bool{false, true}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reading %d: fired=%v, want %v", i, got[i], want[i])
		}
	}
}

func TestCooldownSuppressesRefire(t *testing.T) {
	e, now := mk(t, config.Trigger{Type: "ocr_changed", Confirm: 1, Cooldown: config.Duration(10 * time.Minute)})
	feed(e, "A") // baseline
	if !e.ObserveText("B").Fired {
		t.Fatal("first change should fire")
	}
	if e.ObserveText("C").Fired {
		t.Error("change within cooldown must not fire")
	}
	*now = now.Add(11 * time.Minute)
	if !e.ObserveText("D").Fired {
		t.Error("change after cooldown should fire")
	}
}

func TestPixelThresholdAndCooldown(t *testing.T) {
	e, now := mk(t, config.Trigger{Type: "pixel_change", Threshold: 10, Cooldown: config.Duration(time.Minute)})
	if e.ObservePixel(5).Fired {
		t.Error("below threshold must not fire")
	}
	if !e.ObservePixel(20).Fired {
		t.Error("above threshold should fire")
	}
	if e.ObservePixel(50).Fired {
		t.Error("within cooldown must not fire")
	}
	*now = now.Add(2 * time.Minute)
	if !e.ObservePixel(50).Fired {
		t.Error("after cooldown should fire")
	}
}

// TestCooldownDelaysPersistingErrorRatherThanDropping is the verified repro
// for the standing-jam bug: an ocr_match error fires, clears, then re-errors
// INSIDE the cooldown window and stays on screen past expiry. The suppressed
// edge must not be silently absorbed into e.stable — it must fire at the
// first post-expiry observation.
func TestCooldownDelaysPersistingErrorRatherThanDropping(t *testing.T) {
	e, now := mk(t, config.Trigger{Type: "ocr_match", Pattern: "(?i)error", Confirm: 1, Cooldown: config.Duration(15 * time.Minute)})
	feed(e, "OK") // baseline
	if !e.ObserveText("ERROR: jam").Fired {
		t.Fatal("first error should fire")
	}
	*now = now.Add(2 * time.Minute)
	if e.ObserveText("OK").Fired {
		t.Fatal("clearing must not fire")
	}
	*now = now.Add(3 * time.Minute) // t+5m, still within 15m cooldown
	if e.ObserveText("ERROR: jam again").Fired {
		t.Fatal("re-error within cooldown must not fire yet")
	}
	// Reading persists, unchanged, well past cooldown expiry (t+5m -> t+16m).
	*now = now.Add(11 * time.Minute)
	if !e.ObserveText("ERROR: jam again").Fired {
		t.Error("persisting error must fire at first post-expiry observation")
	}
}

// TestNumericCooldownDelaysPersistingCrossing mirrors the ocr_match repro
// for numeric triggers: a threshold re-cross happens inside cooldown and the
// crossed state persists past expiry, so it must fire once the window ends.
func TestNumericCooldownDelaysPersistingCrossing(t *testing.T) {
	e, now := mk(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 200, Confirm: 1, Cooldown: config.Duration(15 * time.Minute)})
	feed(e, "150") // baseline, below threshold
	if !e.ObserveText("250").Fired {
		t.Fatal("first crossing should fire")
	}
	*now = now.Add(2 * time.Minute)
	if e.ObserveText("150").Fired {
		t.Fatal("dropping back below threshold must not fire")
	}
	*now = now.Add(3 * time.Minute) // t+5m, still within cooldown
	if e.ObserveText("260").Fired {
		t.Fatal("re-crossing within cooldown must not fire yet")
	}
	*now = now.Add(11 * time.Minute) // t+16m, cooldown expired
	if !e.ObserveText("260").Fired {
		t.Error("persisting crossing must fire at first post-expiry observation")
	}
}

// TestCooldownTransientNeverFires checks that a state which reverts back
// before the cooldown window ends is correctly resolved and never fires —
// only a PERSISTING suppressed edge should fire at expiry.
func TestCooldownTransientNeverFires(t *testing.T) {
	e, now := mk(t, config.Trigger{Type: "ocr_match", Pattern: "(?i)error", Confirm: 1, Cooldown: config.Duration(15 * time.Minute)})
	feed(e, "OK")
	if !e.ObserveText("ERROR: jam").Fired {
		t.Fatal("first error should fire")
	}
	*now = now.Add(5 * time.Minute)
	if e.ObserveText("ERROR: jam again").Fired {
		t.Fatal("re-error within cooldown must not fire yet")
	}
	*now = now.Add(2 * time.Minute) // t+7m, still within cooldown
	if e.ObserveText("OK").Fired {
		t.Fatal("reverting to OK must not fire")
	}
	*now = now.Add(9 * time.Minute) // t+16m, cooldown expired, state stayed OK
	if e.ObserveText("OK").Fired {
		t.Error("resolved transient must never fire, even after cooldown expiry")
	}
}

// TestOCRChangedCooldownDelaysAbsorbedChange covers ocr_changed: a change
// that lands inside cooldown and persists past expiry must fire once,
// instead of being silently absorbed as the new stable value.
func TestOCRChangedCooldownDelaysAbsorbedChange(t *testing.T) {
	e, now := mk(t, config.Trigger{Type: "ocr_changed", Confirm: 1, Cooldown: config.Duration(15 * time.Minute)})
	feed(e, "A")
	if !e.ObserveText("B").Fired {
		t.Fatal("first change should fire")
	}
	*now = now.Add(5 * time.Minute)
	if e.ObserveText("C").Fired {
		t.Fatal("change within cooldown must not fire yet")
	}
	*now = now.Add(11 * time.Minute) // t+16m, cooldown expired, reading persists at "C"
	if !e.ObserveText("C").Fired {
		t.Error("persisting change must fire at first post-expiry observation")
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	bad := []config.Trigger{
		{Type: "ocr_match"},                            // missing pattern
		{Type: "ocr_match", Pattern: "("},              // invalid regex
		{Type: "numeric", Op: "between", Threshold: 1}, // bad op
		{Type: "banana"},                               // unknown type
	}
	for i, cfg := range bad {
		if _, err := New(cfg); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestCheckEvaluatesOneReading(t *testing.T) {
	cases := []struct {
		name      string
		cfg       config.Trigger
		reading   string
		evaluable bool
		met       bool
		detail    string
	}{
		{"match met", config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete"}, "PRINT COMPLETE", true, true, "The text matches the pattern"},
		{"match not met", config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete"}, "PRINTING 12%", true, false, "The text doesn't match the pattern"},
		{"gt met", config.Trigger{Type: "numeric", Op: "gt", Threshold: 20}, "23.5", true, true, "23.5 is above 20"},
		{"gt not met", config.Trigger{Type: "numeric", Op: "gt", Threshold: 24, Pattern: "([0-9.]+)"}, "?4?", true, false, "4 is not above 24"},
		{"lt met", config.Trigger{Type: "numeric", Op: "lt", Threshold: -2.5}, "temp -3", true, true, "-3 is below -2.5"},
		{"no number", config.Trigger{Type: "numeric", Op: "gt", Threshold: 1}, "?", true, false, "No number found in the reading"},
		{"changed", config.Trigger{Type: "ocr_changed"}, "A", false, false, "Fires when the text changes from one stable reading to another"},
		{"pixel", config.Trigger{Type: "pixel_change", Threshold: 10}, "", false, false, "Pixel change compares each frame with the one before, so one test has nothing to compare"},
	}
	for _, c := range cases {
		got, err := Check(c.cfg, c.reading)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Evaluable != c.evaluable || got.Met != c.met || got.Detail != c.detail {
			t.Errorf("%s: got %+v, want evaluable=%v met=%v detail=%q", c.name, got, c.evaluable, c.met, c.detail)
		}
	}
	if _, err := Check(config.Trigger{Type: "ocr_match", Pattern: "("}, "x"); err == nil {
		t.Error("an invalid pattern must be reported, not evaluated")
	}
}

// A06: Confirm on a numeric trigger counts readings on the same side of the
// threshold, not identical texts, so a climbing or jittering value confirms.
func TestNumericConfirmCountsTheCondition(t *testing.T) {
	cfg := config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Confirm: 3}
	cases := []struct {
		name     string
		readings []string
		want     []bool
	}{
		{"climbing value fires on the third", []string{"25.1", "25.2", "25.3"}, []bool{false, false, true}},
		{"a dip below breaks the run", []string{"25.3", "24.9", "25.3"}, []bool{false, false, false}},
		{"a dip then three above fires", []string{"25.3", "24.9", "25.3", "25.4", "25.5"}, []bool{false, false, false, false, true}},
		{"garbage resets the count", []string{"25.1", "25.2", "--", "25.3", "25.4"}, []bool{false, false, false, false, false}},
		{"garbage then a full run fires", []string{"25.1", "--", "25.2", "25.3", "25.4"}, []bool{false, false, false, false, true}},
		{"stays above: fires once", []string{"26", "27", "28", "29", "30", "31"}, []bool{false, false, true, false, false, false}},
	}
	for _, c := range cases {
		e, _ := mk(t, cfg)
		got := feed(e, c.readings...)
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("%s: reading %d (%s): fired=%v, want %v", c.name, i, c.readings[i], got[i], c.want[i])
			}
		}
	}
}

// A below-threshold run is confirmed the same way, so the trigger re-arms on
// a jittering fall and fires again on the next climb.
func TestNumericRearmsOnAJitteringFall(t *testing.T) {
	e, _ := mk(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Confirm: 2})
	got := feed(e, "25.5", "25.6", "24.1", "23.9", "25.2", "25.8")
	want := []bool{false, true, false, false, false, true}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reading %d: fired=%v, want %v", i, got[i], want[i])
		}
	}
}

// Two OCR spellings of the same match are one condition, so they confirm
// together instead of resetting each other.
func TestOCRMatchConfirmCountsTheCondition(t *testing.T) {
	e, _ := mk(t, config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 2})
	got := feed(e, "PRINTING 90%", "PRINT COMPLETE", "PRINT COMPLETE.", "Print Complete")
	want := []bool{false, false, true, false}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reading %d: fired=%v, want %v", i, got[i], want[i])
		}
	}
}

// The cooldown still delays, not drops, a numeric crossing confirmed on the
// condition.
func TestNumericConditionConfirmStillDelaysInCooldown(t *testing.T) {
	e, now := mk(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Confirm: 2, Cooldown: config.Duration(time.Minute)})
	if got := feed(e, "25.1", "25.2"); !got[1] {
		t.Fatalf("first crossing should fire: %v", got)
	}
	feed(e, "24.0", "24.1") // re-armed, inside the cooldown
	if got := feed(e, "25.5", "25.6", "25.7"); got[0] || got[1] || got[2] {
		t.Fatalf("a crossing inside the cooldown fired: %v", got)
	}
	*now = now.Add(2 * time.Minute)
	if !e.ObserveText("25.9").Fired {
		t.Fatal("a crossing still holding when the cooldown ends should fire then")
	}
}

// A16: a reading on its way to firing reports how far along Confirm it is;
// the reading that fires, and readings that wouldn't fire, report nothing.
func TestPendingReportsConfirmProgress(t *testing.T) {
	type pn struct{ pending, need int }
	check := func(name string, cfg config.Trigger, readings []string, want []pn, fired int) {
		t.Helper()
		e, _ := mk(t, cfg)
		for i, r := range readings {
			ev := e.ObserveText(r)
			if got := (pn{ev.Pending, ev.Need}); got != want[i] {
				t.Errorf("%s: reading %d (%q): pending/need=%v, want %v", name, i, r, got, want[i])
			}
			if ev.Fired != (i == fired) {
				t.Errorf("%s: reading %d (%q): fired=%v", name, i, r, ev.Fired)
			}
		}
	}
	check("ocr_match", config.Trigger{Type: "ocr_match", Pattern: "(?i)complete", Confirm: 3},
		[]string{"PRINTING", "PRINT COMPLETE", "PRINT COMPLETE", "PRINT COMPLETE", "PRINT COMPLETE"},
		[]pn{{}, {1, 3}, {2, 3}, {}, {}}, 3)
	check("numeric", config.Trigger{Type: "numeric", Op: "gt", Threshold: 25, Confirm: 2},
		[]string{"24.0", "25.1", "25.2", "25.3"},
		[]pn{{}, {1, 2}, {}, {}}, 2)
	check("ocr_changed", config.Trigger{Type: "ocr_changed", Confirm: 2},
		[]string{"A", "A", "B", "B"},
		[]pn{{}, {}, {1, 2}, {}}, 3)
	check("confirm 1 has no progress", config.Trigger{Type: "ocr_match", Pattern: "x", Confirm: 1},
		[]string{"x"}, []pn{{}}, 0)
}

// A16 (fixer): progress made while Cooldown runs says so. The count goes
// on ("1 of 3", "2 of 3") with the time the cooldown ends, and the
// reading that completes it is held, not dropped silently: Pending
// reaches Need and CooldownEnds says until when. The first confirmed
// reading after the cooldown fires.
func TestConfirmProgressDuringCooldownSaysItIsHeld(t *testing.T) {
	e, now := mk(t, config.Trigger{Type: "ocr_match", Pattern: "(?i)complete", Confirm: 3, Cooldown: config.Duration(5 * time.Minute)})
	for _, r := range []string{"PRINT COMPLETE", "PRINT COMPLETE", "PRINT COMPLETE"} {
		e.ObserveText(r)
	}
	if e.lastFired.IsZero() {
		t.Fatal("setup: the first run should fire")
	}
	ends := e.lastFired.Add(5 * time.Minute)
	for i := 0; i < 3; i++ {
		*now = now.Add(time.Second)
		if ev := e.ObserveText("PRINTING"); ev.Pending != 0 || !ev.CooldownEnds.IsZero() {
			t.Fatalf("an unmet reading has no progress: %+v", ev)
		}
	}
	type step struct {
		pending, need int
		held          bool
	}
	want := []step{{1, 3, true}, {2, 3, true}, {3, 3, true}, {3, 3, true}}
	for i, w := range want {
		*now = now.Add(time.Second)
		ev := e.ObserveText("PRINT COMPLETE")
		got := step{ev.Pending, ev.Need, ev.CooldownEnds.Equal(ends)}
		if ev.Fired || got != w {
			t.Errorf("reading %d in cooldown: fired=%v %+v, want %+v (cooldown ends %v, got %v)", i, ev.Fired, got, w, ends, ev.CooldownEnds)
		}
	}
	*now = ends.Add(time.Second)
	if ev := e.ObserveText("PRINT COMPLETE"); !ev.Fired || ev.Pending != 0 || !ev.CooldownEnds.IsZero() {
		t.Errorf("the first reading after the cooldown should fire: %+v", ev)
	}
	// Outside a cooldown, progress carries no end time.
	e2, _ := mk(t, config.Trigger{Type: "ocr_match", Pattern: "x", Confirm: 2, Cooldown: config.Duration(time.Minute)})
	if ev := e2.ObserveText("x"); ev.Pending != 1 || !ev.CooldownEnds.IsZero() {
		t.Errorf("no cooldown running: %+v", ev)
	}
}
