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
			}
		}(i)
	}
	wg.Wait()
	if len(r.Recent("w")) != 5 {
		t.Errorf("len = %d, want 5", len(r.Recent("w")))
	}
}
