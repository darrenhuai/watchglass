package history

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestTriggerStateRoundTrip(t *testing.T) {
	s, path := openTemp(t)
	if _, ok, err := s.LoadTriggerState("printer"); ok || err != nil {
		t.Fatalf("a watch with nothing saved: ok=%v err=%v, want false, nil", ok, err)
	}
	fired := time.Now()
	want := TriggerState{Fingerprint: "abc", LastFired: fired, Stable: "cond:true", HasStable: true}
	if err := s.SaveTriggerState("printer", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A second watch, never fired, whose settled text is the empty string.
	if err := s.SaveTriggerState("lcd", TriggerState{Fingerprint: "def", HasStable: true}); err != nil {
		t.Fatal(err)
	}

	// Closed and opened again, as a restart does.
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	got, ok, err := s2.LoadTriggerState("printer")
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if !got.LastFired.Equal(fired) {
		t.Errorf("LastFired = %v, want %v to the nanosecond: the cooldown ends when it was going to", got.LastFired, fired)
	}
	if got.Fingerprint != "abc" || got.Stable != "cond:true" || !got.HasStable {
		t.Errorf("loaded %+v, want %+v", got, want)
	}
	lcd, ok, err := s2.LoadTriggerState("lcd")
	if err != nil || !ok || !lcd.LastFired.IsZero() || !lcd.HasStable || lcd.Stable != "" {
		t.Errorf("lcd = %+v ok=%v err=%v, want never fired, settled on the empty text", lcd, ok, err)
	}

	// Saving again replaces the row.
	if err := s2.SaveTriggerState("printer", TriggerState{Fingerprint: "xyz"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s2.LoadTriggerState("printer")
	if got.Fingerprint != "xyz" || !got.LastFired.IsZero() || got.HasStable || got.Stable != "" {
		t.Errorf("after a second save: %+v, want only the new fingerprint", got)
	}
	var n int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM trigger_state`).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows = %d (err %v), want one per watch", n, err)
	}
}

func TestKeepTriggerStateDropsOnlyMissingWatches(t *testing.T) {
	s, _ := openTemp(t)
	for _, w := range []string{"a", "b", "c"} {
		if err := s.SaveTriggerState(w, TriggerState{Fingerprint: w}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.KeepTriggerState([]string{"a", "c", "never-saved"})
	if err != nil || n != 1 {
		t.Fatalf("Keep(a, c): n=%d err=%v, want 1 row dropped", n, err)
	}
	for w, want := range map[string]bool{"a": true, "b": false, "c": true} {
		if _, ok, _ := s.LoadTriggerState(w); ok != want {
			t.Errorf("state for %q present = %v, want %v", w, ok, want)
		}
	}
	// No watches left: nothing is kept.
	if n, err := s.KeepTriggerState(nil); err != nil || n != 2 {
		t.Errorf("Keep(none): n=%d err=%v, want the last 2 rows dropped", n, err)
	}
}

// The retention prune is for readings. A watch that fired 40 days ago under
// a 60-day cooldown still knows it.
func TestPruneLeavesTriggerStateAlone(t *testing.T) {
	s, _ := openTemp(t)
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := s.Record("w", old, "old", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTriggerState("w", TriggerState{Fingerprint: "f", LastFired: old, Stable: "old", HasStable: true}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Prune(time.Now()); err != nil || n != 1 {
		t.Fatalf("Prune: n=%d err=%v, want the one reading", n, err)
	}
	got, ok, err := s.LoadTriggerState("w")
	if err != nil || !ok || !got.LastFired.Equal(old) || got.Stable != "old" {
		t.Errorf("trigger state after Prune: %+v ok=%v err=%v, want it untouched", got, ok, err)
	}
}

func TestLastPolledIsTheNewestReading(t *testing.T) {
	s, _ := openTemp(t)
	if _, ok, err := s.LastPolled("w"); ok || err != nil {
		t.Fatalf("no readings: ok=%v err=%v, want false, nil", ok, err)
	}
	base := time.Unix(5000, 0)
	for i := 0; i < 3; i++ {
		if err := s.Record("w", base.Add(time.Duration(i)*time.Minute), "r", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Record("other", base.Add(time.Hour), "r", false); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LastPolled("w")
	if err != nil || !ok || !got.Equal(base.Add(2*time.Minute)) {
		t.Errorf("LastPolled = %v ok=%v err=%v, want %v", got, ok, err, base.Add(2*time.Minute))
	}
}

// A database written by a version without trigger_state opens as it is:
// the readings stay, and the new table appears.
func TestOpenUpgradesAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE readings (id INTEGER PRIMARY KEY AUTOINCREMENT, watch TEXT NOT NULL, ts INTEGER NOT NULL, reading TEXT NOT NULL, fired INTEGER NOT NULL)`,
		`CREATE INDEX idx_readings_watch_id ON readings(watch, id)`,
		`CREATE INDEX idx_readings_ts ON readings(ts)`,
		`INSERT INTO readings (watch, ts, reading, fired) VALUES ('printer', 1700000000, 'PRINT COMPLETE', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a v0.1.8 database: %v", err)
	}
	defer s.Close()
	got, err := s.LastN("printer", 5)
	if err != nil || len(got) != 1 || got[0].Reading != "PRINT COMPLETE" || !got[0].Fired {
		t.Errorf("readings after the upgrade: %+v err=%v, want the old row", got, err)
	}
	if _, ok, err := s.LoadTriggerState("printer"); ok || err != nil {
		t.Errorf("trigger state on an upgraded database: ok=%v err=%v, want none and no error", ok, err)
	}
	if err := s.SaveTriggerState("printer", TriggerState{Fingerprint: "f", HasStable: true, Stable: "cond:true"}); err != nil {
		t.Errorf("Save on an upgraded database: %v", err)
	}
}

// A fire starts out not delivered. MarkDelivered marks that fire, and saving
// a new settled state for the same fire keeps the mark; a new fire starts
// unmarked, and a late mark for the old one changes nothing.
func TestTriggerStateDelivered(t *testing.T) {
	s, _ := openTemp(t)
	save := func(st TriggerState) {
		t.Helper()
		if err := s.SaveTriggerState("printer", st); err != nil {
			t.Fatal(err)
		}
	}
	delivered := func() bool {
		t.Helper()
		st, ok, err := s.LoadTriggerState("printer")
		if err != nil || !ok {
			t.Fatalf("Load: ok=%v err=%v", ok, err)
		}
		return st.Delivered
	}
	mark := func(watch string, at time.Time) {
		t.Helper()
		if err := s.MarkDelivered(watch, at); err != nil {
			t.Fatal(err)
		}
	}
	t1 := time.Now()
	save(TriggerState{Fingerprint: "f", LastFired: t1, Stable: "cond:true", HasStable: true})
	if delivered() {
		t.Error("a fire just saved counts as delivered")
	}
	mark("printer", t1)
	if !delivered() {
		t.Error("MarkDelivered didn't mark the fire")
	}
	save(TriggerState{Fingerprint: "f", LastFired: t1, Stable: "cond:false", HasStable: true})
	if !delivered() {
		t.Error("a new settled state for the same fire undid the mark")
	}
	t2 := t1.Add(time.Minute)
	save(TriggerState{Fingerprint: "f", LastFired: t2, Stable: "cond:true", HasStable: true})
	if delivered() {
		t.Error("a new fire kept the old fire's mark")
	}
	mark("printer", t1)
	if delivered() {
		t.Error("a late mark for the old fire marked the new one")
	}
	mark("never-saved", t2)
	save(TriggerState{Fingerprint: "f", LastFired: t2, Delivered: true})
	if !delivered() {
		t.Error("Delivered: true wasn't saved")
	}
}

// The trigger_state table's first version had no delivered column. It gets
// one, and its rows count as delivered, as that version treated them.
func TestOpenAddsTheDeliveredColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE trigger_state (watch TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, last_fired INTEGER NOT NULL, has_stable INTEGER NOT NULL, stable TEXT NOT NULL, updated INTEGER NOT NULL)`,
		`INSERT INTO trigger_state VALUES ('printer', 'f', 1700000000000000000, 1, 'cond:true', 1700000000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.TriggerStateErr(); err != nil {
		t.Fatalf("TriggerStateErr: %v", err)
	}
	st, ok, err := s.LoadTriggerState("printer")
	if err != nil || !ok || !st.Delivered || st.Stable != "cond:true" {
		t.Errorf("old row = %+v ok=%v err=%v, want it kept and counted as delivered", st, ok, err)
	}
	if err := s.SaveTriggerState("printer", TriggerState{Fingerprint: "f", LastFired: time.Now()}); err != nil {
		t.Errorf("Save after adding the column: %v", err)
	}
}

// A database that can only be read still opens, as it did before there was
// trigger state: readings are there to show, and the trigger state methods
// do nothing instead of failing.
func TestReadOnlyDatabaseRunsWithoutTriggerState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE readings (id INTEGER PRIMARY KEY AUTOINCREMENT, watch TEXT NOT NULL, ts INTEGER NOT NULL, reading TEXT NOT NULL, fired INTEGER NOT NULL)`,
		`CREATE INDEX idx_readings_watch_id ON readings(watch, id)`,
		`CREATE INDEX idx_readings_ts ON readings(ts)`,
		`INSERT INTO readings (watch, ts, reading, fired) VALUES ('printer', 1700000000, 'PRINT COMPLETE', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open("file:" + filepath.ToSlash(path) + "?mode=ro")
	if err != nil {
		t.Fatalf("Open on a read-only database: %v", err)
	}
	defer s.Close()
	if s.TriggerStateErr() == nil {
		t.Error("TriggerStateErr = nil on a database that can't take the table")
	}
	if got, err := s.LastN("printer", 5); err != nil || len(got) != 1 {
		t.Errorf("readings: %+v err=%v, want the old row", got, err)
	}
	if _, ok, err := s.LoadTriggerState("printer"); ok || err != nil {
		t.Errorf("Load: ok=%v err=%v, want none and no error", ok, err)
	}
	if err := s.SaveTriggerState("printer", TriggerState{Fingerprint: "f", LastFired: time.Now()}); err != nil {
		t.Errorf("Save: %v, want nothing written and no error", err)
	}
	if err := s.MarkDelivered("printer", time.Now()); err != nil {
		t.Errorf("MarkDelivered: %v", err)
	}
	if _, err := s.KeepTriggerState(nil); err != nil {
		t.Errorf("KeepTriggerState: %v", err)
	}
}
