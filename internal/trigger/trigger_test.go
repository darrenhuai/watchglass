package trigger

import (
	"testing"
	"time"

	"watchglass/internal/config"
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
