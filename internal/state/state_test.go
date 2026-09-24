package state

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRingKeepsNewestN(t *testing.T) {
	r := New(3)
	for i := 1; i <= 5; i++ {
		r.Add("w", Sample{TS: time.Unix(int64(i), 0), Reading: fmt.Sprintf("r%d", i)})
	}
	got := r.Recent("w")
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Reading != "r5" || got[2].Reading != "r3" {
		t.Errorf("order wrong: %v", got)
	}
}

func TestLatest(t *testing.T) {
	r := New(3)
	if _, ok := r.Latest("none"); ok {
		t.Error("Latest on empty watch should be !ok")
	}
	r.Add("w", Sample{Reading: "a"})
	r.Add("w", Sample{Reading: "b"})
	s, ok := r.Latest("w")
	if !ok || s.Reading != "b" {
		t.Errorf("Latest = %+v ok=%v", s, ok)
	}
}

func TestDrop(t *testing.T) {
	r := New(3)
	r.Add("w", Sample{Reading: "a"})
	r.Drop("w")
	if len(r.Recent("w")) != 0 {
		t.Error("Drop did not clear samples")
	}
}

// must_fix 1/4: the web UI derives dashboard/detail/live status from
// Registry.GetHealth — a fresh registry must read as healthy (no entry at
// all), SetHealth must record a Down verdict, and Drop must forget it along
// with the watch's samples.
// A fire has to outlive the ring: with interval 2s and n 10 the fired
// sample is gone 20 s later, and the dashboard still says when it fired.
func TestLastFiredOutlivesTheRing(t *testing.T) {
	r := New(3)
	if _, ok := r.LastFired("w"); ok {
		t.Error("LastFired before any fire should be !ok")
	}
	r.Add("w", Sample{TS: time.Unix(1, 0), Reading: "a"})
	if _, ok := r.LastFired("w"); ok {
		t.Error("a reading that didn't fire must not count as a fire")
	}
	r.Add("w", Sample{TS: time.Unix(2, 0), Reading: "b", Fired: true})
	for i := 3; i <= 10; i++ {
		r.Add("w", Sample{TS: time.Unix(int64(i), 0), Reading: "c"})
	}
	if got, ok := r.LastFired("w"); !ok || !got.Equal(time.Unix(2, 0)) {
		t.Errorf("LastFired = %v, %v; want the fire at t=2 after it left the ring", got, ok)
	}
	r.Add("w", Sample{TS: time.Unix(11, 0), Reading: "d", Fired: true})
	if got, _ := r.LastFired("w"); !got.Equal(time.Unix(11, 0)) {
		t.Errorf("LastFired = %v, want the newer fire at t=11", got)
	}
	if _, ok := r.LastFired("other"); ok {
		t.Error("another watch's fire must not leak")
	}
	r.Drop("w")
	if _, ok := r.LastFired("w"); ok {
		t.Error("Drop should forget the last fire too")
	}
}

func TestHealthDefaultsToNoEntry(t *testing.T) {
	r := New(3)
	if h, ok := r.GetHealth("w"); ok {
		t.Errorf("GetHealth on untouched watch = %+v, ok=%v, want ok=false", h, ok)
	}
}

func TestSetHealthRoundTrips(t *testing.T) {
	r := New(3)
	ts := time.Unix(1000, 0)
	r.SetHealth("w", Health{Down: true, Message: "stream unreachable after 3 consecutive failures: dial tcp: refused", Since: ts})
	h, ok := r.GetHealth("w")
	if !ok || !h.Down || h.Message == "" || !h.Since.Equal(ts) {
		t.Errorf("GetHealth = %+v, ok=%v", h, ok)
	}
	// A later Success transition overwrites the Down verdict rather than
	// merging with it.
	r.SetHealth("w", Health{})
	h, ok = r.GetHealth("w")
	if !ok || h.Down {
		t.Errorf("SetHealth did not clear Down: %+v, ok=%v", h, ok)
	}
}

func TestDropClearsHealthToo(t *testing.T) {
	r := New(3)
	r.Add("w", Sample{Reading: "a"})
	r.SetHealth("w", Health{Down: true, Message: "down"})
	r.Drop("w")
	if len(r.Recent("w")) != 0 {
		t.Error("Drop did not clear samples")
	}
	if _, ok := r.GetHealth("w"); ok {
		t.Error("Drop did not clear health")
	}
}

func TestConcurrentAccess(t *testing.T) {
	r := New(5)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Add("w", Sample{Reading: "x"})
				r.Recent("w")
				r.Latest("w")
				r.SetHealth("w", Health{Down: j%2 == 0})
				r.GetHealth("w")
			}
		}(i)
	}
	wg.Wait()
	if len(r.Recent("w")) != 5 {
		t.Errorf("len = %d, want 5", len(r.Recent("w")))
	}
}
