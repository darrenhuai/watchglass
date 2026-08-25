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
