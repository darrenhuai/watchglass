package trigger

import (
	"context"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/ocr"
)

// readFixture decodes one of testdata/partial's crops (see its README) with
// the seven-segment decoder, as a running sevenseg watch reads its box.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "partial", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	text, err := ocr.NewSevenSeg().Recognize(context.Background(), img)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// sevenseg is mk for a watch on the seven-segment decoder.
func sevenseg(t *testing.T, cfg config.Trigger) *Evaluator {
	t.Helper()
	e, _ := mk(t, cfg)
	e.SevenSeg = true
	return e
}

// Round 6's three real photos, each a display well above the watch's
// threshold that the decoder reads with a digit missing. The default
// pattern finds a number beside the '?' (0 in "??.0", 5.5 in "?5.5", 3 in
// "?.3") that is below the threshold, and each one alerted. A numeric
// watch must skip those readings: no alert, no progress towards one and
// no number for Home Assistant.
func TestPartlyReadPhotosNeverFireANumericWatch(t *testing.T) {
	for _, c := range []struct {
		file, reads string
		display     float64
		cfg         config.Trigger
	}{
		{"car-71-0.png", "??.0", 71.0, config.Trigger{Type: "numeric", Op: "lt", Threshold: 50, Confirm: 2}},
		{"wall-15-5.png", "?5.5", 15.5, config.Trigger{Type: "numeric", Op: "lt", Threshold: 10, Confirm: 2}},
		{"hama-22-3.png", "?.3", 22.3, config.Trigger{Type: "numeric", Op: "lt", Threshold: 10, Confirm: 2}},
	} {
		t.Run(c.file, func(t *testing.T) {
			got := readFixture(t, c.file)
			if got != c.reads {
				// The fixture is only a partial case while the decoder
				// misses that digit; testdata/partial/README.md lists it.
				t.Fatalf("the decoder reads %q, the fixture's README says %q: pick a crop that is still read partly", got, c.reads)
			}
			e := sevenseg(t, c.cfg)
			for i := 0; i < 10; i++ {
				ev := e.ObserveText(got)
				if ev.Fired || ev.Pending != 0 {
					t.Fatalf("reading %d %q of a display at %v: fired=%v pending=%d (%s)", i, got, c.display, ev.Fired, ev.Pending, ev.Reason)
				}
				if ev.HasValue || ev.HasSettled {
					t.Fatalf("reading %d %q of a display at %v reached Home Assistant: value %v settled %q", i, got, c.display, ev.Value, ev.Settled)
				}
			}
			if s := e.State(); s.HasStable {
				t.Errorf("a partly read display settled the watch: %+v", s)
			}
		})
	}
}

// Every reading with a '?' in its number, wherever in the number the '?'
// sits, and every reading with a '?' anywhere else on the display, is no
// number at all for a sevenseg watch: it never counts towards Confirm on
// either side and never resets a count in progress.
func TestPartlyReadNumberIsNoNumber(t *testing.T) {
	partial := []string{"??.0", "?5.5", "12?4", "?.3", "126?", "?84", "1?8?", "-?", "?", "?.?", "??", "?:04", "22.5?", "?22.5"}
	for _, p := range partial {
		// Below 1000 is what every one of them would parse to.
		e := sevenseg(t, config.Trigger{Type: "numeric", Op: "lt", Threshold: 1000, Confirm: 2})
		for i := 0; i < 4; i++ {
			if ev := e.ObserveText(p); ev.Fired || ev.Pending != 0 || ev.HasValue || ev.HasSettled {
				t.Errorf("%q, reading %d: %+v; it must count for nothing", p, i, ev)
			}
		}

		// Nor do they count as "not met": a watch that fired on 1284 stays
		// fired through them and doesn't fire again when 1284 comes back.
		e = sevenseg(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 1000, Confirm: 2})
		if got := feed(e, "1284", "1284"); !got[1] {
			t.Fatalf("1284 twice should fire")
		}
		for _, f := range feed(e, p, p, p, "1284", "1284", "1284") {
			if f {
				t.Errorf("%q between clean readings of a steady display fired again", p)
			}
		}
		if s := e.State(); s.Stable != condMet {
			t.Errorf("%q changed the settled state: %+v", p, s)
		}

		// And a count in progress carries on across one.
		e = sevenseg(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 1000, Confirm: 2})
		if got := feed(e, "1284", p, "1284"); got[1] || !got[2] {
			t.Errorf("1284, %q, 1284: fired %v, want the second 1284 to confirm", p, got)
		}
	}

	// A text engine's '?' is text: "?5.5" has 5.5 in it.
	e, _ := mk(t, config.Trigger{Type: "numeric", Op: "lt", Threshold: 10, Confirm: 2})
	if got := feed(e, "?5.5", "?5.5"); !got[1] {
		t.Error("tesseract's ?5.5 holds 5.5 and should still fire")
	}
}

// The Home Assistant Value of a display at 71.0 read as "??.0" was 0. It
// is never a number the display doesn't show.
func TestValueNeverTakesAPartlyReadNumber(t *testing.T) {
	e := sevenseg(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 100, Confirm: 2})
	seen := false
	for i, in := range []string{"??.0", "??.0", "??.0", "71.0", "??.0", "71.0", "?1.0", "71.0", "??.0", "??.0", "??.0", "71.0"} {
		ev := e.ObserveText(in)
		if !ev.HasValue {
			continue
		}
		seen = true
		if ev.Value != 71 || ev.Settled != "71.0" {
			t.Errorf("reading %d %q: Value %v settled %q, want 71 from \"71.0\"", i, in, ev.Value, ev.Settled)
		}
	}
	if !seen {
		t.Error("three clean readings of 71.0 should fill the window")
	}
}

// Round 6's jitter run: a steady Vaillant display at 22.5 (above 20) read
// whole, then a little zoomed (what a box 6% smaller sees when the camera
// shakes) with its first digit missed, alternating two and two. It sent 6
// alerts in 25 s; one display crossing once is one alert.
func TestSteadyDisplayReadPartlyAlertsOnce(t *testing.T) {
	a, b := readFixture(t, "vaillant-22-5-a.png"), readFixture(t, "vaillant-22-5-b.png")
	if a != "22.5" || b != "?2.5" {
		t.Fatalf("the decoder reads %q and %q, the fixture's README says 22.5 and ?2.5", a, b)
	}
	e := sevenseg(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 20, Confirm: 2})
	var seq []string
	for i := 0; i < 6; i++ {
		seq = append(seq, a, a, b, b)
	}
	seq = append(seq, a)
	fires := 0
	for _, f := range feed(e, seq...) {
		if f {
			fires++
		}
	}
	if fires != 1 {
		t.Errorf("%d alerts for one crossing, want 1", fires)
	}

	// A real crossing still alerts again: the display falls below 20 and
	// comes back, partial readings in between.
	if got := feed(e, "19.5", b, "19.5", "?9.5", a, b, a); !got[6] {
		t.Errorf("falling to 19.5 and back to 22.5 should alert again: %v", got)
	}

	// T2's counter, a steady 1284 above 1000, read with a digit missed now
	// and then. It sent 9 duplicate alerts in two hours.
	c := sevenseg(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 1000, Confirm: 2})
	fires = 0
	for _, f := range feed(c, "1284", "1284", "1284", "12?4", "126?", "1284", "?84", "?84", "?84", "1284",
		"2??", "1284", "1?8?", "1284", "1?94", "1?84", "1284", "12?4", "12??", "1284", "12?", "12?", "12?", "1284") {
		if f {
			fires++
		}
	}
	if fires != 1 {
		t.Errorf("counter: %d alerts for one crossing, want 1", fires)
	}
}

// Cooldown still applies to a crossing confirmed across partial readings.
func TestPartlyReadNumberKeepsCooldown(t *testing.T) {
	e, now := mk(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 20, Confirm: 2, Cooldown: config.Duration(time.Minute)})
	e.SevenSeg = true
	if got := feed(e, "22.5", "?2.5", "22.5"); !got[2] {
		t.Fatalf("first crossing should fire: %v", got)
	}
	*now = now.Add(10 * time.Second)
	feed(e, "19.5", "19.5")
	if got := feed(e, "22.5", "?2.5", "22.5"); got[0] || got[1] || got[2] {
		t.Errorf("a second crossing inside Cooldown fired: %v", got)
	}
	*now = now.Add(time.Minute)
	if got := feed(e, "?2.5", "22.5"); got[0] || !got[1] {
		t.Errorf("the held crossing should fire on the first clean reading after Cooldown: %v", got)
	}
}

// CheckEngine (the web UI's Test) judges a reading the way a running watch
// with that engine does.
func TestCheckEngineAgreesWithTheWatch(t *testing.T) {
	washer := config.Trigger{Type: "ocr_match", Pattern: `^(0:00|\?+)$`, Confirm: 2}
	boiler := config.Trigger{Type: "ocr_match", Pattern: `^\?[0-9]+$`, Confirm: 3}
	num := config.Trigger{Type: "numeric", Op: "lt", Threshold: 10, Confirm: 2}
	for _, c := range []struct {
		name          string
		cfg           config.Trigger
		engine, read  string
		met, skipped  bool
		detail        string
		watchFiresNow bool // fed Confirm times to a fresh watch
	}{
		{"washer End", washer, "sevenseg", "?", true, false, "The text matches the pattern", true},
		{"boiler E4", boiler, "sevenseg", "?4", true, false, "The text matches the pattern", true},
		{"boiler 35", boiler, "sevenseg", "35", false, false, "The text doesn't match the pattern", false},
		{"partial number", num, "sevenseg", "?5.5", false, true, "A digit wasn't read, so this reading is skipped and the watch keeps its last state", false},
		{"no digit", num, "sevenseg", "?.?", false, true, "A digit wasn't read, so this reading is skipped and the watch keeps its last state", false},
		{"clean number", num, "sevenseg", "5.5", true, false, "5.5 is below 10", true},
		{"text engine", num, "tesseract", "?5.5", true, false, "5.5 is below 10", true},
	} {
		got, err := CheckEngine(c.cfg, c.engine, c.read)
		if err != nil {
			t.Fatal(err)
		}
		if got.Met != c.met || got.Skipped != c.skipped || got.Detail != c.detail || !got.Evaluable {
			t.Errorf("%s: got %+v, want met=%v skipped=%v %q", c.name, got, c.met, c.skipped, c.detail)
		}
		e, _ := mk(t, c.cfg)
		e.SevenSeg = c.engine == "sevenseg"
		fired := false
		for i := 0; i < c.cfg.Confirm; i++ {
			fired = e.ObserveText(c.read).Fired
		}
		if fired != c.watchFiresNow {
			t.Errorf("%s: the watch fired=%v on %d readings of %q, Test said met=%v", c.name, fired, c.cfg.Confirm, c.read, got.Met)
		}
	}
}

// A skipped reading keeps the count in progress (above), and the Live tile
// for it says so: "1 of 3 needed to fire" doesn't vanish on the partial
// frame and come back as "2 of 3", and a crossing held by Cooldown stays
// held on it.
func TestSkippedReadingKeepsTheProgressShown(t *testing.T) {
	e, now := mk(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 20, Confirm: 3, Cooldown: config.Duration(time.Minute)})
	e.SevenSeg = true
	if ev := e.ObserveText("?2.5"); ev.Pending != 0 {
		t.Errorf("a skipped first reading has nothing in progress, got %d of %d", ev.Pending, ev.Need)
	}
	e.ObserveText("22.5")
	for i, want := range []int{1, 2} {
		ev := e.ObserveText("?2.5")
		if ev.Pending != want || ev.Need != 3 || ev.Fired {
			t.Errorf("skipped reading %d: %d of %d (fired %v), want %d of 3 carried", i, ev.Pending, ev.Need, ev.Fired, want)
		}
		if i == 0 {
			e.ObserveText("22.5")
		}
	}
	if !e.ObserveText("22.5").Fired {
		t.Fatal("the third clean reading should fire")
	}
	if ev := e.ObserveText("?2.5"); ev.Pending != 0 {
		t.Errorf("after the fire nothing is in progress, got %d of %d", ev.Pending, ev.Need)
	}
	*now = now.Add(10 * time.Second)
	feed(e, "19.5", "19.5", "19.5", "22.5", "22.5", "22.5")
	ev := e.ObserveText("?2.5")
	if ev.Pending != 3 || ev.Need != 3 || ev.CooldownEnds.IsZero() {
		t.Errorf("a crossing held by Cooldown should stay held on a skipped reading: %d of %d, ends %v", ev.Pending, ev.Need, ev.CooldownEnds)
	}
}

// A frame the decoder finds no digits on (a display caught dark between
// refreshes, a glare) reads as "" and says nothing about the number, so a
// numeric watch skips it like a '?' reading: 22.5, a blank, 22.5 is two
// readings in a row above 20, not a count started over. A text engine's
// blank reading still resets the count: it read the box and found nothing
// to say the condition holds.
func TestBlankSevenSegReadingIsSkipped(t *testing.T) {
	cfg := config.Trigger{Type: "numeric", Op: "gt", Threshold: 20, Confirm: 2}
	e := sevenseg(t, cfg)
	var pending []int
	var fired []bool
	for _, in := range []string{"22.5", "", "22.5"} {
		ev := e.ObserveText(in)
		pending = append(pending, ev.Pending)
		fired = append(fired, ev.Fired)
		if ev.HasSettled || ev.HasValue {
			t.Errorf("%q settled %q", in, ev.Settled)
		}
	}
	if pending[0] != 1 || pending[1] != 1 || !fired[2] {
		t.Errorf("sevenseg 22.5, '', 22.5: pending %v fired %v; want the blank to keep the count and the third reading to fire", pending, fired)
	}
	if got := feed(e, "", "", ""); got[0] || got[1] || got[2] {
		t.Errorf("blank frames fired: %v", got)
	}
	if st := e.State(); !st.HasStable || st.Stable != condMet {
		t.Errorf("blank frames changed the settled state: %+v", st)
	}
	if got := feed(e, "19.0", "", "19.0", "", "22.5", "22.5"); !got[5] {
		t.Errorf("after a blank-interrupted run below, a clean run above should fire again: %v", got)
	}

	cond, err := CheckEngine(cfg, "sevenseg", "")
	if err != nil {
		t.Fatal(err)
	}
	if !cond.Skipped || cond.Met || cond.Detail != "Nothing was read, so this reading is skipped and the watch keeps its last state" {
		t.Errorf("Test on a blank sevenseg reading: %+v", cond)
	}

	text, _ := mk(t, cfg)
	if got := feed(text, "22.5", "", "22.5"); got[2] {
		t.Errorf("a text engine's blank reading should still reset the count: %v", got)
	}
	cond, _ = CheckEngine(cfg, "tesseract", "")
	if cond.Skipped {
		t.Errorf("a text engine's blank reading isn't skipped: %+v", cond)
	}
}
