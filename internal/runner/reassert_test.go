package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/history"
)

// restoredDownRunner is a runner for a watch the history database kept as
// down, seeded the way the supervisor does on boot. heard lists every
// health verdict a consumer of the hook would see, transitions and the
// re-assertion alike, in order.
func restoredDownRunner(t *testing.T, src *flakySource, healthAfter int) (*Runner, *[]health.Event) {
	t.Helper()
	store := openStore(t)
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.HealthAfter = healthAfter
	w.Interval = config.Duration(time.Second)
	if err := store.SaveHealth(w.Name, history.HealthState{Down: true, Since: time.Now().Add(-time.Hour), Message: "no reading for 3 polls: connection refused"}); err != nil {
		t.Fatal(err)
	}
	r, err := New(w, src, nil, &fakeNotifier{}, store, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.RestoredDown(); !ok {
		t.Fatal("the saved down verdict wasn't restored")
	}
	heard := &[]health.Event{}
	r.OnHealth = func(hev health.Event) { *heard = append(*heard, hev) }
	r.SeedRestoredDown(func(hev health.Event) { *heard = append(*heard, hev) })
	return r, heard
}

// E1: the restored down verdict is heard again after health_after failed
// polls, when a tracker that started healthy would have gone down, and
// only once in the run.
func TestRestoredDownIsHeardAfterHealthAfterFailedPolls(t *testing.T) {
	src := &flakySource{fail: true}
	r, heard := restoredDownRunner(t, src, 3)
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		r.Tick(ctx)
		if len(*heard) != 0 {
			t.Fatalf("after %d failed polls the hook heard %+v, want nothing before health_after (3)", i, *heard)
		}
	}
	r.Tick(ctx)
	if len(*heard) != 1 || (*heard)[0].State != "down" || !strings.Contains((*heard)[0].Message, "connection refused") {
		t.Fatalf("after 3 failed polls the hook heard %+v, want the restored down verdict once", *heard)
	}
	for i := 0; i < 10; i++ {
		r.Tick(ctx)
	}
	if len(*heard) != 1 {
		t.Errorf("after 13 failed polls the hook heard %+v, want the one down verdict", *heard)
	}
}

// E1: a watch that reads on its first poll after the restart has
// recovered; the "healthy" transition says so, and the restored down
// verdict must not be heard later. Failures after that are counted by the
// tracker, which needs health_after in a row; scattered failures that add
// up to health_after must not send "down" (MQTT "offline") while the
// tracker says up.
func TestRestoredDownIsNotHeardAfterTheWatchReads(t *testing.T) {
	src := &flakySource{fail: false}
	r, heard := restoredDownRunner(t, src, 3)
	ctx := context.Background()
	r.Tick(ctx)
	if len(*heard) != 1 || (*heard)[0].State != "healthy" {
		t.Fatalf("first poll reads: the hook heard %+v, want one healthy", *heard)
	}
	// Two failures, a reading, two failures, a reading, ...: never
	// health_after (3) in a row, but 3 failures and more in all.
	for round := 0; round < 4; round++ {
		src.fail = true
		r.Tick(ctx)
		r.Tick(ctx)
		src.fail = false
		r.Tick(ctx)
	}
	for _, hev := range (*heard)[1:] {
		t.Errorf("after the recovery the hook heard %+v, want nothing (the tracker never went down)", hev)
	}
}
