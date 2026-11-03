package trigger

import (
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
)

// A sevenseg watch with letters on reads fault codes and status words (E4,
// End, OL). For a numeric watch a reading with a letter in it is no number
// at all, exactly like one with a '?' (see unread): it counts for nothing,
// changes nothing and never reaches Home Assistant's Value. The text types
// see the letters.

// The codes a letter can touch: never a number for a numeric watch.
var letterReadings = []string{"E4", "F28", "End", "OL", "0L", "2b.5", "H1", "A1", "Err", "E", "1E", "4E4", "-E4", "PA55"}

func TestLetterReadingIsNoNumber(t *testing.T) {
	for _, s := range letterReadings {
		// Every one of them holds a number below 1000 for the default pattern.
		e := sevenseg(t, config.Trigger{Type: "numeric", Op: "lt", Threshold: 1000, Confirm: 2})
		for i := 0; i < 4; i++ {
			if ev := e.ObserveText(s); ev.Fired || ev.Pending != 0 || ev.HasValue || ev.HasSettled {
				t.Errorf("%q, reading %d: %+v; it must count for nothing", s, i, ev)
			}
		}
		if st := e.State(); st.HasStable {
			t.Errorf("%q settled the watch: %+v", s, st)
		}

		// Nor does one count as "not met": a watch that fired on 52 stays
		// fired through a code and doesn't fire again when 52 comes back.
		e = sevenseg(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 40, Confirm: 2})
		if got := feed(e, "52", "52"); !got[1] {
			t.Fatalf("52 twice should fire")
		}
		for _, f := range feed(e, s, s, s, "52", "52", "52") {
			if f {
				t.Errorf("%q between clean readings of a steady display fired again", s)
			}
		}
		if st := e.State(); st.Stable != condMet {
			t.Errorf("%q changed the settled state: %+v", s, st)
		}

		// And a count in progress carries on across one.
		e = sevenseg(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 40, Confirm: 2})
		if got := feed(e, "52", s, "52"); got[1] || !got[2] {
			t.Errorf("52, %q, 52: fired %v, want the second 52 to confirm", s, got)
		}
	}

	// A text engine's E4 holds a 4.
	e, _ := mk(t, config.Trigger{Type: "numeric", Op: "lt", Threshold: 10, Confirm: 2})
	if got := feed(e, "E4", "E4"); !got[1] {
		t.Error("tesseract's E4 holds 4 and should still fire")
	}
}

// The boiler recipe's two watches on one display: the temperature watch
// skips the code and the fault watch catches it, and with the code
// showing between temperatures neither alerts twice.
func TestBoilerFaultCodeFiresTheMatchWatchOnly(t *testing.T) {
	temp := sevenseg(t, config.Trigger{Type: "numeric", Pattern: `^([0-9]{1,3})$`, Op: "lt", Threshold: 40, Confirm: 2})
	fault := sevenseg(t, config.Trigger{Type: "ocr_match", Pattern: `^E[0-9]+$`, Confirm: 2})
	seq := []string{"52", "52", "E4", "E4", "E4", "52", "52", "F3", "F3", "E4", "E4"}
	var tf, ff []bool
	for _, s := range seq {
		tf = append(tf, temp.ObserveText(s).Fired)
		ff = append(ff, fault.ObserveText(s).Fired)
	}
	for i, f := range tf {
		if f {
			t.Errorf("the temperature watch fired on reading %d %q", i, seq[i])
		}
	}
	// E4 twice fires once; F3 doesn't match and counts as the code going
	// (not skipped: a letter is text to ocr_match), so E4 fires again.
	want := []bool{false, false, false, true, false, false, false, false, false, false, true}
	for i := range want {
		if ff[i] != want[i] {
			t.Errorf("fault watch, reading %d %q: fired %v, want %v (all: %v)", i, seq[i], ff[i], want[i], ff)
		}
	}
}

// Home Assistant's Value of a display at 24 that shows E4 in between is 24
// and never 4 (or 24 with the code's digits mixed in).
func TestValueNeverTakesANumberOutOfALetterGroup(t *testing.T) {
	e := sevenseg(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 100, Confirm: 2})
	seen := false
	for i, in := range []string{"E4", "E4", "24", "24", "E4", "24", "2b.5", "24", "End", "E4", "24", "OL"} {
		ev := e.ObserveText(in)
		if !ev.HasValue {
			continue
		}
		seen = true
		if ev.Value != 24 || ev.Settled != "24" {
			t.Errorf("reading %d %q: Value %v settled %q, want 24 from \"24\"", i, in, ev.Value, ev.Settled)
		}
	}
	if !seen {
		t.Error("three clean readings of 24 should fill the window")
	}
}

// Test this region judges a letter reading the way the running watch does.
func TestCheckReadWithLetters(t *testing.T) {
	num := config.Trigger{Type: "numeric", Op: "lt", Threshold: 40, Confirm: 2}
	fault := config.Trigger{Type: "ocr_match", Pattern: `^E[0-9]+$`, Confirm: 2}
	changed := config.Trigger{Type: "ocr_changed", Confirm: 2}
	for _, c := range []struct {
		name         string
		cfg          config.Trigger
		engine, read string
		met, skipped bool
		detail       string
	}{
		{"numeric E4", num, "sevenseg", "E4", false, true, "A letter was read, so this is a code or a word, not a number: this reading is skipped and the watch keeps its last state"},
		{"numeric End", num, "sevenseg", "End", false, true, "A letter was read, so this is a code or a word, not a number: this reading is skipped and the watch keeps its last state"},
		{"numeric E?", num, "sevenseg", "E?", false, true, "A letter was read, so this is a code or a word, not a number: this reading is skipped and the watch keeps its last state"},
		{"numeric ?5", num, "sevenseg", "?5", false, true, "A digit wasn't read for sure, so this reading is skipped and the watch keeps its last state"},
		{"numeric 52", num, "sevenseg", "52", false, false, "52 is not below 40"},
		{"fault E4", fault, "sevenseg", "E4", true, false, "The text matches the pattern"},
		{"fault F3", fault, "sevenseg", "F3", false, false, "The text doesn't match the pattern"},
		{"fault 52", fault, "sevenseg", "52", false, false, "The text doesn't match the pattern"},
		{"text engine E4", num, "tesseract", "E4", true, false, "4 is below 40"},
	} {
		got, err := CheckEngine(c.cfg, c.engine, c.read)
		if err != nil {
			t.Fatal(err)
		}
		if got.Met != c.met || got.Skipped != c.skipped || got.Detail != c.detail || !got.Evaluable {
			t.Errorf("%s: got %+v, want met=%v skipped=%v %q", c.name, got, c.met, c.skipped, c.detail)
		}
	}
	// ocr_changed sees End as a change from 0:04 and settles on it.
	e := sevenseg(t, changed)
	got := feed(e, "0:04", "0:04", "End", "End")
	if !got[3] || got[2] {
		t.Errorf("0:04, 0:04, End, End: fired %v, want the second End to fire", got)
	}
	if ev := e.ObserveText("End"); !ev.HasSettled || ev.Settled != "End" {
		t.Errorf("End should settle as the reading: %+v", ev)
	}
}
