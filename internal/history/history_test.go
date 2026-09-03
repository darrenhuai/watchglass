package history

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRecordAndLastN(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	base := time.Unix(1000, 0)
	for i, r := range []string{"Printing 10%", "Printing 50%", "PRINT COMPLETE"} {
		if err := s.Record("printer", base.Add(time.Duration(i)*time.Minute), r, i == 2); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}
	if err := s.Record("other-watch", base, "unrelated", false); err != nil {
		t.Fatal(err)
	}

	got, err := s.LastN("printer", 2)
	if err != nil {
		t.Fatalf("LastN: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Reading != "PRINT COMPLETE" || !got[0].Fired {
		t.Errorf("newest = %+v, want PRINT COMPLETE fired", got[0])
	}
	if got[1].Reading != "Printing 50%" || got[1].Fired {
		t.Errorf("second = %+v", got[1])
	}
}

func TestLastNEmptyWatch(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.LastN("nope", 5)
	if err != nil {
		t.Fatalf("LastN: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestPruneDeletesOnlyOldReadings(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.Record("w", old, "old", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Record("w", fresh, "fresh", true); err != nil {
		t.Fatal(err)
	}
	n, err := s.Prune(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Errorf("pruned %d, want 3", n)
	}
	got, err := s.LastN("w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Reading != "fresh" {
		t.Errorf("surviving rows = %+v", got)
	}
}

func TestPruneEmptyStoreNoError(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, err := s.Prune(time.Now())
	if err != nil || n != 0 {
		t.Errorf("empty prune: n=%d err=%v", n, err)
	}
}

func TestOpenCreatesIndex(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var name string
	err = s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_readings_watch_id'`).Scan(&name)
	if err != nil {
		t.Fatalf("index not found: %v", err)
	}
}
