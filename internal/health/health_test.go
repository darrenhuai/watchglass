package health

import (
	"errors"
	"strings"
	"testing"
)

func TestFailuresBelowThresholdAreSilent(t *testing.T) {
	tr := New(3)
	for i := 0; i < 2; i++ {
		if _, changed := tr.Failure(errors.New("boom")); changed {
			t.Fatalf("failure %d should not transition yet", i+1)
		}
	}
}

func TestThresholdFiresOnceThenStaysQuiet(t *testing.T) {
	tr := New(2)
	tr.Failure(errors.New("boom"))
	ev, changed := tr.Failure(errors.New("connection refused"))
	if !changed {
		t.Fatal("reaching threshold should transition to down")
	}
	if ev.State != "down" {
		t.Errorf("state = %q, want down", ev.State)
	}
	if !strings.Contains(ev.Message, "connection refused") {
		t.Errorf("message should carry the last error, got %q", ev.Message)
	}
	for i := 0; i < 5; i++ {
		if _, changed := tr.Failure(errors.New("boom")); changed {
			t.Fatal("already-down tracker must not re-fire")
		}
	}
}

func TestRecoveryFiresOnce(t *testing.T) {
	tr := New(1)
	if _, changed := tr.Failure(errors.New("boom")); !changed {
		t.Fatal("threshold 1 should transition on first failure")
	}
	ev, changed := tr.Success()
	if !changed {
		t.Fatal("success after down should transition to healthy")
	}
	if ev.State != "healthy" {
		t.Errorf("state = %q, want healthy", ev.State)
	}
	if _, changed := tr.Success(); changed {
		t.Error("already-healthy tracker must not re-fire")
	}
}

func TestSuccessResetsFailureRun(t *testing.T) {
	tr := New(3)
	tr.Failure(errors.New("a"))
	tr.Failure(errors.New("b"))
	tr.Success() // resets the run; no transition (never went down)
	tr.Failure(errors.New("c"))
	tr.Failure(errors.New("d"))
	if _, changed := tr.Failure(errors.New("e")); !changed {
		t.Error("a fresh run of 3 failures should transition to down")
	}
}

// A tracker seeded down (a restart carrying over a previous run's verdict)
// must stay silent on further failures — it is already down, nothing new
// to report — and emit exactly one "healthy" on the first success, so
// recovery is reported at the moment the source actually answers.
func TestSeedDownRecoversOnFirstSuccess(t *testing.T) {
	tr := New(3)
	tr.SeedDown()
	for i := 0; i < 5; i++ {
		if _, changed := tr.Failure(errors.New("still dead")); changed {
			t.Fatalf("failure %d after SeedDown must not re-report down", i+1)
		}
	}
	ev, changed := tr.Success()
	if !changed || ev.State != "healthy" {
		t.Fatalf("first success after SeedDown = (%+v, %v), want a healthy transition", ev, changed)
	}
	if _, changed := tr.Success(); changed {
		t.Error("second success must not re-fire")
	}
	// And the tracker is fully live again afterwards: a fresh run of
	// threshold failures transitions to down.
	tr.Failure(errors.New("a"))
	tr.Failure(errors.New("b"))
	if _, changed := tr.Failure(errors.New("c")); !changed {
		t.Error("threshold failures after recovery should transition to down again")
	}
}

func TestNewClampsThreshold(t *testing.T) {
	for _, n := range []int{0, -5} {
		tr := New(n)
		if _, changed := tr.Failure(errors.New("boom")); !changed {
			t.Errorf("New(%d) should clamp threshold to 1", n)
		}
	}
}
