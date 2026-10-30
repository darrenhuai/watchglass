package history

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// V1.5: readings come back out with the value each one published, page by
// page, oldest first, inside a time range.
func TestReadingsPageWithValues(t *testing.T) {
	s, _ := openTemp(t)
	base := time.Unix(1700000000, 0)
	for i := 0; i < 25; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		var err error
		if i%5 == 4 {
			err = s.Record("meter", ts, "?234.5", false) // nothing published
		} else {
			err = s.RecordValue("meter", ts, "1234.5", 1234.5+float64(i), i == 7)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordValue("other", base, "1", 1, false); err != nil {
		t.Fatal(err)
	}
	var all []Row
	var after int64
	for {
		page, err := s.ReadingsPage("meter", after, time.Time{}, time.Time{}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 10 {
			t.Fatalf("page of %d, limit 10", len(page))
		}
		all = append(all, page...)
		after = page[len(page)-1].ID
	}
	if len(all) != 25 {
		t.Fatalf("got %d rows, want 25", len(all))
	}
	for i, r := range all {
		if !r.TS.Equal(base.Add(time.Duration(i) * time.Minute)) {
			t.Fatalf("row %d at %v: not oldest first", i, r.TS)
		}
		if i%5 == 4 {
			if r.HasValue {
				t.Errorf("row %d: a reading with no value came back with %v", i, r.Value)
			}
			continue
		}
		if !r.HasValue || r.Value != 1234.5+float64(i) || r.Fired != (i == 7) {
			t.Errorf("row %d = %+v", i, r)
		}
	}
	// from is inclusive, to exclusive.
	page, err := s.ReadingsPage("meter", 0, base.Add(10*time.Minute), base.Add(13*time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 3 || !page[0].TS.Equal(base.Add(10*time.Minute)) {
		t.Errorf("range: %+v", page)
	}
}

// A v0.1.12 database has no value column: Open adds it, the old rows read
// without a value, and new ones keep theirs.
func TestOpenAddsTheValueColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE readings (id INTEGER PRIMARY KEY AUTOINCREMENT, watch TEXT NOT NULL, ts INTEGER NOT NULL, reading TEXT NOT NULL, fired INTEGER NOT NULL)`,
		`INSERT INTO readings (watch, ts, reading, fired) VALUES ('meter', 1700000000, '1234.5', 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RecordValue("meter", time.Unix(1700000060, 0), "1234.6", 1234.6, false); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ReadingsPage("meter", 0, time.Time{}, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].HasValue || !rows[1].HasValue || rows[1].Value != 1234.6 {
		t.Errorf("rows = %+v", rows)
	}
}

// A read-only database from before the column still reads, with no values.
func TestReadOnlyDatabaseReadsWithoutValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE readings (id INTEGER PRIMARY KEY AUTOINCREMENT, watch TEXT NOT NULL, ts INTEGER NOT NULL, reading TEXT NOT NULL, fired INTEGER NOT NULL)`,
		`CREATE INDEX idx_readings_watch_id ON readings(watch, id)`,
		`CREATE INDEX idx_readings_ts ON readings(ts)`,
		`INSERT INTO readings (watch, ts, reading, fired) VALUES ('meter', 1700000000, '1234.5', 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open("file:" + filepath.ToSlash(path) + "?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.ReadingsPage("meter", 0, time.Time{}, time.Time{}, 10)
	if err != nil || len(rows) != 1 || rows[0].HasValue {
		t.Errorf("rows = %+v err = %v", rows, err)
	}
	if err := s.RecordValue("meter", time.Now(), "1", 1, false); err != nil {
		t.Errorf("RecordValue on a read-only database: %v", err)
	}
	if _, ok, err := s.LoadCounter("meter"); ok || err != nil {
		t.Errorf("LoadCounter: ok=%v err=%v", ok, err)
	}
}

// V1.3: a counter's last value and an accepted lower value outlive the
// store; taking the accept makes it the last value.
func TestCounterStateRoundTrip(t *testing.T) {
	s, path := openTemp(t)
	if _, ok, err := s.LoadCounter("meter"); ok || err != nil {
		t.Fatalf("nothing saved: ok=%v err=%v", ok, err)
	}
	if err := s.SaveCounterLast("meter", 1240.1); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCounterAccept("meter", 12.5); err != nil {
		t.Fatal(err)
	}
	// A later last value doesn't drop the accept that waits.
	if err := s.SaveCounterLast("meter", 1240.2); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	c, ok, err := s2.LoadCounter("meter")
	if err != nil || !ok || c.Last != 1240.2 || !c.HasAccept || c.Accept != 12.5 {
		t.Fatalf("after reopen: %+v ok=%v err=%v", c, ok, err)
	}
	if err := s2.TakeCounterAccept("meter", 99, 99); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := s2.LoadCounter("meter"); !c.HasAccept || c.Last != 1240.2 {
		t.Fatalf("taking another value changed it: %+v", c)
	}
	if err := s2.TakeCounterAccept("meter", 12.5, 12.5); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := s2.LoadCounter("meter"); c.HasAccept || c.Last != 12.5 {
		t.Fatalf("after take: %+v", c)
	}
	if _, err := s2.KeepTriggerState([]string{"other"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s2.LoadCounter("meter"); ok {
		t.Error("a deleted watch's counter state should go with it")
	}
	if err := s2.SaveCounterLast("meter", 1); err != nil {
		t.Fatal(err)
	}
	if err := s2.DropCounter("meter"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s2.LoadCounter("meter"); ok {
		t.Error("DropCounter left the row")
	}
}
