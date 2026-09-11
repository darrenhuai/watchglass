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
