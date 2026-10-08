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

// A fire from before watchglass restarted is carried over as a time only:
// no sample appears, a newer fire wins, and an older seed never moves the
// time back.
func TestSeedFiredCarriesAFireOverARestart(t *testing.T) {
	r := New(3)
	r.SeedFired("w", time.Unix(100, 0))
	if got, ok := r.LastFired("w"); !ok || !got.Equal(time.Unix(100, 0)) {
		t.Errorf("LastFired after SeedFired = %v, %v; want t=100", got, ok)
	}
	if _, ok := r.Latest("w"); ok || len(r.Recent("w")) != 0 {
		t.Error("SeedFired must not add a sample")
	}
	r.Add("w", Sample{TS: time.Unix(200, 0), Reading: "x", Fired: true})
	r.SeedFired("w", time.Unix(150, 0)) // a Save & restart re-seeds the older, saved time
	if got, _ := r.LastFired("w"); !got.Equal(time.Unix(200, 0)) {
		t.Errorf("LastFired = %v, want the newer fire at t=200 to stand", got)
	}
	r.Drop("w")
	if _, ok := r.LastFired("w"); ok {
		t.Error("Drop should forget a seeded fire too")
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

// A failed delivery stays on record until a later one finishes, a late
// result for an older alert never replaces a newer one, and Drop and
// ClearDelivery forget it.
func TestDeliveryRecord(t *testing.T) {
	r := New(3)
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if _, ok := r.GetDelivery("w"); ok {
		t.Fatal("a new watch has no delivery")
	}
	r.SetSending("w", t0)
	if ts, ok := r.Sending("w"); !ok || !ts.Equal(t0) {
		t.Fatalf("Sending = %v, %v", ts, ok)
	}
	r.SetDelivery("w", Delivery{TS: t0, Kind: "fired", Err: "HTTP 404"})
	if _, ok := r.Sending("w"); ok {
		t.Error("finishing the alert must clear its sending mark")
	}
	// The next alert starts: the failure is still the record.
	t1 := t0.Add(time.Minute)
	r.SetSending("w", t1)
	if d, _ := r.GetDelivery("w"); d.OK || d.Err != "HTTP 404" {
		t.Errorf("record while the next alert sends = %+v", d)
	}
	r.SetDelivery("w", Delivery{TS: t1, Kind: "fired", OK: true})
	// A straggler for the older alert changes nothing.
	r.SetDelivery("w", Delivery{TS: t0, Kind: "fired", Err: "late"})
	if d, _ := r.GetDelivery("w"); !d.OK || !d.TS.Equal(t1) {
		t.Errorf("record = %+v, want the newer success", d)
	}
	if _, ok := r.GetDelivery("other"); ok {
		t.Error("delivery leaked to another watch")
	}
	r.ClearDelivery("w")
	if _, ok := r.GetDelivery("w"); ok {
		t.Error("ClearDelivery kept the record")
	}
	r.SetSending("w", t1)
	r.SetDelivery("w", Delivery{TS: t1, Err: "x"})
	r.Drop("w")
	if _, ok := r.GetDelivery("w"); ok {
		t.Error("Drop kept the delivery")
	}
	if _, ok := r.Sending("w"); ok {
		t.Error("Drop kept the sending mark")
	}
}

// E1: the restored record of the last fire before a restart is kept until
// the watch fires again, and is ignored for a fire this run already saw (a
// Save & restart restores the fire it just made). Drop forgets it;
// ClearDelivery (a Save that changes the notify URLs) keeps it, since the
// Start right after would seed it again anyway; DropSamples forgets the
// readings only.
func TestSeedRestoredLastsUntilTheNextFire(t *testing.T) {
	r := New(5)
	r.SeedRestored("w", time.Unix(100, 0), true)
	if got, ok := r.RestoredFire("w"); !ok || !got.TS.Equal(time.Unix(100, 0)) || !got.Sent {
		t.Fatalf("RestoredFire = %+v, %v", got, ok)
	}
	r.Add("w", Sample{TS: time.Unix(110, 0), Reading: "a"})
	if _, ok := r.RestoredFire("w"); !ok {
		t.Error("a reading that isn't a fire dropped the record")
	}
	r.Add("w", Sample{TS: time.Unix(120, 0), Reading: "b", Fired: true})
	if _, ok := r.RestoredFire("w"); ok {
		t.Error("a new fire should replace the restored record")
	}
	r.SeedRestored("w", time.Unix(120, 0), false) // a Save & restart restoring that same fire
	if _, ok := r.RestoredFire("w"); ok {
		t.Error("a fire this run saw came back as restored")
	}

	r.SeedRestored("v", time.Unix(100, 0), false)
	r.ClearDelivery("v")
	if got, ok := r.RestoredFire("v"); !ok || !got.TS.Equal(time.Unix(100, 0)) {
		t.Errorf("ClearDelivery dropped the restored record: %+v, %v", got, ok)
	}
	r.SeedRestored("v", time.Unix(100, 0), false)
	r.Drop("v")
	if _, ok := r.RestoredFire("v"); ok {
		t.Error("Drop kept the restored record")
	}

	r.Add("u", Sample{TS: time.Unix(1, 0), Reading: "x", Fired: true})
	r.DropSamples("u")
	if len(r.Recent("u")) != 0 {
		t.Error("DropSamples kept samples")
	}
	if _, ok := r.LastFired("u"); !ok {
		t.Error("DropSamples forgot when the watch fired")
	}
}
