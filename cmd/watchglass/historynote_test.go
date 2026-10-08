package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/history"
)

// E1: a history database that already has every table but can't be
// written gets one line at start, which says readings aren't recorded
// either; a writable one gets none.
func TestHistoryNoteForAReadOnlyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg.db")
	rw, err := history.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := historyNote(rw); got != "" {
		t.Errorf("writable database: %q, want no line", got)
	}
	rw.Close()

	ro, err := history.Open("file:" + filepath.ToSlash(path) + "?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got := historyNote(ro)
	if !strings.HasPrefix(got, "history: the database can't be written, so readings aren't recorded and what each watch knows isn't kept across restarts (a restart can repeat an alert): ") ||
		!strings.Contains(got, "readonly") {
		t.Errorf("read-only database: %q", got)
	}
}
