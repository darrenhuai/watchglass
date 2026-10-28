package fakeclock

import (
	"testing"
	"time"
)

func TestAfterFiresWhenAddReachesIt(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := New(t0)
	ch := c.After(10 * time.Second)
	select {
	case <-ch:
		t.Fatal("fired before the clock moved")
	default:
	}
	c.Add(9 * time.Second)
	select {
	case <-ch:
		t.Fatal("fired a second early")
	default:
	}
	c.Add(time.Second)
	select {
	case at := <-ch:
		if !at.Equal(t0.Add(10 * time.Second)) {
			t.Errorf("fired at %v", at)
		}
	case <-time.After(time.Second):
		t.Fatal("didn't fire when its time came")
	}
	if c.Waiting() != 0 {
		t.Errorf("%d still waiting", c.Waiting())
	}
	// A wait that is already over fires at once, and the clock can be
	// moved past several waits in one Add.
	select {
	case <-c.After(0):
	case <-time.After(time.Second):
		t.Fatal("After(0) didn't fire")
	}
	a, b := c.After(time.Minute), c.After(2*time.Minute)
	c.Add(time.Hour)
	for i, ch := range []<-chan time.Time{a, b} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("wait %d didn't fire", i)
		}
	}
}
