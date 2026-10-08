package history

import (
	"context"
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

// E1: a database that already has trigger_state but can't be written
// (opened read only here; a read-only file or folder is the same) says so
// once, at Open: WriteErr and TriggerStateErr are set. Then nothing tries
// to write, so a poll costs no error line: Record, Prune and the trigger
// state writes return nil without writing, and what the database holds is
// still read.
func TestReadOnlyDatabaseWithTheTableSaysSoAtOpen(t *testing.T) {
	s, path := openTemp(t)
	fired := time.Unix(1700000000, 0)
	if err := s.SaveTriggerState("printer", TriggerState{Fingerprint: "f", LastFired: fired, Delivered: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("printer", fired, "PRINT COMPLETE", true); err != nil {
		t.Fatal(err)
	}
	if s.WriteErr() != nil || s.TriggerStateErr() != nil {
		t.Fatalf("a writable database: WriteErr %v, TriggerStateErr %v", s.WriteErr(), s.TriggerStateErr())
	}
	s.Close()

	ro, err := Open("file:" + filepath.ToSlash(path) + "?mode=ro")
	if err != nil {
		t.Fatalf("Open read only: %v", err)
	}
	defer ro.Close()
	if ro.WriteErr() == nil {
		t.Error("WriteErr = nil on a read-only database that has every table")
	}
	if ro.TriggerStateErr() == nil {
		t.Error("TriggerStateErr = nil on a read-only database that has the table: the start-up line about it never shows")
	}
	st, ok, err := ro.LoadTriggerState("printer")
	if err != nil || !ok || !st.LastFired.Equal(fired) || !st.Delivered {
		t.Errorf("Load: %+v ok=%v err=%v, want the saved row", st, ok, err)
	}
	for what, err := range map[string]error{
		"Record":        ro.Record("printer", time.Now(), "PRINTING", false),
		"Save":          ro.SaveTriggerState("printer", TriggerState{Fingerprint: "g", LastFired: time.Now()}),
		"MarkDelivered": ro.MarkDelivered("printer", fired),
	} {
		if err != nil {
			t.Errorf("%s: %v, want nothing written and no error", what, err)
		}
	}
	if _, err := ro.KeepTriggerState(nil); err != nil {
		t.Errorf("KeepTriggerState: %v", err)
	}
	if _, err := ro.Prune(time.Now()); err != nil {
		t.Errorf("Prune: %v", err)
	}
	if got, err := ro.LastN("printer", 5); err != nil || len(got) != 1 {
		t.Errorf("readings: %+v err=%v, want the one row, unchanged", got, err)
	}
	// The probe left nothing behind in a database it could write.
	rw, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	var n int
	if err := rw.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name LIKE '%probe%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("probe tables left: %d (err %v)", n, err)
	}
}

// E1: a watch's health verdict survives a reopen, a later one replaces
// it, and a database from before the table existed gets it at Open.
func TestHealthRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// v0.1.9's tables, without watch_health.
	if _, err := db.Exec(`CREATE TABLE trigger_state (watch TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, last_fired INTEGER NOT NULL,
		has_stable INTEGER NOT NULL, stable TEXT NOT NULL, delivered INTEGER NOT NULL DEFAULT 1, updated INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadHealth("cam"); ok || err != nil {
		t.Fatalf("nothing saved: ok=%v err=%v", ok, err)
	}
	since := time.Unix(1700000000, 123)
	if err := s.SaveHealth("cam", HealthState{Down: true, Since: since, Message: "no reading for 3 consecutive polls: refused"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h, ok, err := s.LoadHealth("cam")
	if err != nil || !ok || !h.Down || !h.Since.Equal(since) || h.Message != "no reading for 3 consecutive polls: refused" {
		t.Errorf("after a reopen: %+v ok=%v err=%v", h, ok, err)
	}
	if err := s.SaveHealth("cam", HealthState{Since: since.Add(time.Minute), Message: "stream recovered"}); err != nil {
		t.Fatal(err)
	}
	if h, _, _ := s.LoadHealth("cam"); h.Down || h.Message != "stream recovered" {
		t.Errorf("after recovery: %+v", h)
	}
}

// E1: a database another connection is writing while watchglass opens it
// (a DB browser, a backup, the previous instance still exiting) is not
// read only. Open's write probe meets SQLITE_BUSY there; WriteErr stays
// nil, and once the lock is gone readings and trigger state are written
// as usual, rather than nothing for the rest of the run.
func TestDatabaseLockedAtOpenIsStillWritten(t *testing.T) {
	s, path := openTemp(t)
	fired := time.Unix(1700000000, 0)
	if err := s.SaveTriggerState("printer", TriggerState{Fingerprint: "f", LastFired: fired, Delivered: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("printer", fired, "PRINT COMPLETE", true); err != nil {
		t.Fatal(err)
	}
	s.Close()

	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	lock, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("hold the write lock: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open while another connection writes: %v", err)
	}
	defer st.Close()
	if st.WriteErr() != nil || st.TriggerStateErr() != nil {
		t.Errorf("WriteErr %v, TriggerStateErr %v, want nil: a lock held by someone else isn't read only", st.WriteErr(), st.TriggerStateErr())
	}

	if _, err := lock.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	later := fired.Add(time.Minute)
	if err := st.Record("printer", later, "PRINTING", false); err != nil {
		t.Fatalf("Record after the lock is gone: %v", err)
	}
	if err := st.SaveTriggerState("printer", TriggerState{Fingerprint: "f", LastFired: later}); err != nil {
		t.Fatalf("Save after the lock is gone: %v", err)
	}
	if got, err := st.LastN("printer", 5); err != nil || len(got) != 2 {
		t.Errorf("readings after the lock is gone: %d (err %v), want 2", len(got), err)
	}
	if ts, ok, err := st.LoadTriggerState("printer"); err != nil || !ok || !ts.LastFired.Equal(later) {
		t.Errorf("trigger state after the lock is gone: %+v ok=%v err=%v, want the new fire", ts, ok, err)
	}
}

// E1: the first start after upgrading from v0.1.9 has a table to make
// (watch_health). If another connection is writing the database at that
// moment the CREATE meets its lock; that must not switch off keeping
// trigger state and stream health for the whole run. Writes fail with the
// lock while it is held, as any write meeting it does, and once it is gone
// the table is made and both are written and read back.
func TestUpgradeLockedAtOpenKeepsStateOnceTheLockIsGone(t *testing.T) {
	s, path := openTemp(t)
	fired := time.Unix(1700000000, 0)
	if err := s.SaveTriggerState("printer", TriggerState{Fingerprint: "f", LastFired: fired, Delivered: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE watch_health`); err != nil { // as v0.1.9 left it
		t.Fatal(err)
	}
	s.Close()

	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	lock, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("hold the write lock: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open while another connection writes: %v", err)
	}
	defer st.Close()
	if st.WriteErr() != nil || st.TriggerStateErr() != nil {
		t.Errorf("WriteErr %v, TriggerStateErr %v, want nil: a lock held by someone else isn't a reason to keep nothing", st.WriteErr(), st.TriggerStateErr())
	}
	if ts, ok, err := st.LoadTriggerState("printer"); err != nil || !ok || !ts.LastFired.Equal(fired) {
		t.Errorf("trigger state under the lock: %+v ok=%v err=%v, want the saved fire", ts, ok, err)
	}
	if _, ok, err := st.LoadHealth("printer"); ok || err != nil {
		t.Errorf("health under the lock: ok=%v err=%v, want none and no error", ok, err)
	}
	down := HealthState{Down: true, Since: fired, Message: "no reading for 3 polls"}
	if err := st.SaveHealth("printer", down); err == nil || !lockedByAnother(err) {
		t.Errorf("SaveHealth under the lock: %v, want the lock's error (the write didn't happen)", err)
	}

	if _, err := lock.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	later := fired.Add(time.Minute)
	if err := st.SaveTriggerState("printer", TriggerState{Fingerprint: "f", LastFired: later}); err != nil {
		t.Fatalf("SaveTriggerState after the lock is gone: %v", err)
	}
	if err := st.SaveHealth("printer", down); err != nil {
		t.Fatalf("SaveHealth after the lock is gone: %v", err)
	}
	if ts, ok, err := st.LoadTriggerState("printer"); err != nil || !ok || !ts.LastFired.Equal(later) {
		t.Errorf("trigger state after the lock is gone: %+v ok=%v err=%v, want the new fire", ts, ok, err)
	}
	if h, ok, err := st.LoadHealth("printer"); err != nil || !ok || !h.Down || h.Message != down.Message {
		t.Errorf("health after the lock is gone: %+v ok=%v err=%v, want the saved down verdict", h, ok, err)
	}
	if st.TriggerStateErr() != nil {
		t.Errorf("TriggerStateErr %v after the lock is gone", st.TriggerStateErr())
	}
}
