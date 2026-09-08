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
