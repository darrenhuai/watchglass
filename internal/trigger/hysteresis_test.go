package trigger

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/ocr"
)

// Round 6's two-hour recording (FEAT5/T2 full2) read a console's "Kernel
// panic" line through tesseract with a garbled frame now and then, and
// twice two in a row: "Kernel! panic", "Kernel pamic". At Confirm 2 those
// left the condition, and the same alert went out again once the cooldown
// ended. The readings here are the recorded ones.
var (
	panicLine  = "[ 1.209350] ---[ end Kernel panic - not syncing: VFS: Unable to mount root f"
	panicReads = []string{
		"L 1.209350} ---| end Kernel! panic - not syncing: VFS: Unable to mount root T",
		"L 1.209350} ---| end Kernel pamic - not syncing: VFS: Unable to mount root rT",
		"tL 1,209350) ---| end Kerne! panic - not syncing: VFS: Unable to mount root f",
	}
	promptLine = "root@db01:~# uptime"
)

func fires(got []bool) int {
	n := 0
	for _, f := range got {
		if f {
			n++
		}
	}
	return n
}

// A met condition is left only after twice Confirm readings in a row that
// clearly don't meet it: one misread, or two or three in a row, never
// re-arm the watch, on text or on digits.
func TestMisreadsDoNotRearmAMetCondition(t *testing.T) {
	t.Run("console, one misread between good readings", func(t *testing.T) {
		e, _ := mk(t, config.Trigger{Type: "ocr_match", Pattern: "(?i)kernel panic", Confirm: 1})
		got := feed(e, panicLine, panicReads[0], panicLine, panicReads[1], panicLine)
		if got[0] != true || fires(got) != 1 {
			t.Errorf("Confirm 1: fired %v, want only the first reading", got)
		}
	})
	t.Run("console, the recorded runs of two misreads", func(t *testing.T) {
		e, _ := mk(t, config.Trigger{Type: "ocr_match", Pattern: "(?i)kernel panic", Confirm: 2})
		seq := []string{panicLine, panicLine}
		for i := 0; i < 4; i++ {
			seq = append(seq, panicReads[0], panicReads[1], panicLine, panicLine, panicReads[2], panicLine, panicLine)
		}
		got := feed(e, seq...)
		if !got[1] || fires(got) != 1 {
			t.Errorf("fired %v, want one alert for a line that never left the screen", got)
		}
		if st := e.State(); !st.HasStable || st.Stable != condMet {
			t.Errorf("the misreads changed the settled state: %+v", st)
		}
	})
	t.Run("boiler fault code, three bare ? readings", func(t *testing.T) {
		// T2's boiler-fault watch (pattern ^\?[0-9]+$, Confirm 3) read its
		// E4 as "?", "?", "?" once and alerted again after the cooldown.
		e := sevenseg(t, config.Trigger{Type: "ocr_match", Pattern: `^\?[0-9]+$`, Confirm: 3})
		got := feed(e, "?4", "?4", "?4", "?", "?", "?", "?4", "?4", "?4", "", "", "?4", "?4", "?4")
		if !got[2] || fires(got) != 1 {
			t.Errorf("fired %v, want one alert: a ? the pattern doesn't match, or a blank, says nothing", got)
		}
	})
	t.Run("counter, a digit dropped without a ?", func(t *testing.T) {
		// T2's counter read 1284 as "284" (no '?') now and then.
		e := sevenseg(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 1000, Confirm: 2})
		got := feed(e, "1284", "1284", "284", "284", "1284", "1284", "284", "1284", "284", "284", "284", "1284", "1284")
		if !got[1] || fires(got) != 1 {
			t.Errorf("fired %v, want one alert for a counter that never fell", got)
		}
	})
	t.Run("panel text read as nothing", func(t *testing.T) {
		// T2's heat-pump-fault watch (tesseract) read "" and "." of a
		// panel showing E04 LOW FLOW, up to three frames in a row. A
		// text engine's blank counts as not meeting the pattern (a status
		// line that clears reads blank), but three is short of the four
		// it takes to leave at Confirm 2.
		e, _ := mk(t, config.Trigger{Type: "ocr_match", Pattern: "E0[0-9]", Confirm: 2})
		got := feed(e, "E04 LOW FLOW", "E04 LOW FLOW", ".", "", "E04 LOW FLOW", "", "", "", "E04 LOW FLOW", "E04 LOW FLOW")
		if !got[1] || fires(got) != 1 {
			t.Errorf("fired %v, want one alert", got)
		}
		cond, err := CheckEngine(config.Trigger{Type: "ocr_match", Pattern: "E0[0-9]", Confirm: 2}, "tesseract", "")
		if err != nil || cond.Skipped || cond.Met {
			t.Errorf("Test on a blank text reading: %+v, %v; want it not met, not skipped", cond, err)
		}
	})
}

// Leaving takes more readings, not more time: a display that really leaves
// the condition and comes back still fires again, at once without a
// cooldown, and when the cooldown ends with one.
func TestRealLeaveAndReturnFiresAgain(t *testing.T) {
	t.Run("console without cooldown", func(t *testing.T) {
		e, _ := mk(t, config.Trigger{Type: "ocr_match", Pattern: "(?i)kernel panic", Confirm: 2})
		got := feed(e, panicLine, panicLine, promptLine, promptLine, promptLine, promptLine, panicLine, panicLine)
		if !got[1] || !got[7] || fires(got) != 2 {
			t.Errorf("fired %v, want the second panic to alert again after four prompt readings", got)
		}
	})
	t.Run("console with cooldown", func(t *testing.T) {
		e, now := mk(t, config.Trigger{Type: "ocr_match", Pattern: "(?i)kernel panic", Confirm: 2, Cooldown: config.Duration(4 * time.Minute)})
		feed(e, panicLine, panicLine)
		firedAt := *now
		*now = now.Add(time.Minute)
		feed(e, promptLine, promptLine, promptLine, promptLine)
		*now = now.Add(time.Minute)
		if ev := e.ObserveText(panicLine); ev.Fired {
			t.Fatal("one reading isn't Confirm")
		}
		if ev := e.ObserveText(panicLine); ev.Fired || ev.Pending != 2 || !ev.CooldownEnds.Equal(firedAt.Add(4*time.Minute)) {
			t.Fatalf("the second panic inside the cooldown: %+v, want it held until the cooldown ends", ev)
		}
		*now = firedAt.Add(4*time.Minute + time.Second)
		if ev := e.ObserveText(panicLine); !ev.Fired {
			t.Errorf("after the cooldown: %+v, want the second alert", ev)
		}
	})
	t.Run("a status line that clears to blank", func(t *testing.T) {
		// A text engine's blank is the line with nothing on it: the fault
		// cleared. Five minutes of it, and the fault coming back after
		// the cooldown, is a second fault.
		e, now := mk(t, config.Trigger{Type: "ocr_match", Pattern: "ERROR", Confirm: 2, Cooldown: config.Duration(time.Minute)})
		if got := feed(e, "ERROR", "ERROR"); !got[1] {
			t.Fatal("the first error should fire")
		}
		for i := 0; i < 30; i++ {
			*now = now.Add(10 * time.Second)
			if ev := e.ObserveText(""); ev.Fired {
				t.Fatalf("a blank reading fired: %+v", ev)
			}
		}
		if got := feed(e, "ERROR", "ERROR"); !got[1] {
			t.Errorf("fired %v, want the error to alert again after the line was blank for five minutes", got)
		}
	})
	t.Run("digits", func(t *testing.T) {
		e := sevenseg(t, config.Trigger{Type: "numeric", Op: "gt", Threshold: 1000, Confirm: 2})
		got := feed(e, "1284", "1284", "984", "984", "984", "984", "1284", "1284")
		if !got[1] || !got[7] || fires(got) != 2 {
			t.Errorf("fired %v, want the counter to alert again after four readings below", got)
		}
		// One reading short of leaving, the condition is kept.
		got = feed(e, "984", "984", "984", "1284", "1284")
		if fires(got) != 0 {
			t.Errorf("fired %v, want nothing: three readings below are not twice Confirm", got)
		}
	})
}

// A restart in a met condition keeps the hysteresis: the misreads that
// follow don't re-arm the restored watch, and a leave that committed
// before the restart is still a leave after it.
func TestRestartKeepsTheHysteresis(t *testing.T) {
	cfg := config.Trigger{Type: "ocr_match", Pattern: "(?i)kernel panic", Confirm: 2}
	e, now := mk(t, cfg)
	feed(e, panicLine, panicLine)
	e2 := restart(t, cfg, e, now)
	if got := feed(e2, panicReads[0], panicReads[1], panicLine, panicLine, panicReads[2], panicLine); fires(got) != 0 {
		t.Errorf("fired %v after the restart, want nothing: the line never left the screen", got)
	}
	feed(e2, promptLine, promptLine, promptLine, promptLine)
	if st := e2.State(); !st.HasStable || st.Stable != condUnmet {
		t.Fatalf("four prompt readings should have left the condition: %+v", st)
	}
	e3 := restart(t, cfg, e2, now)
	if got := feed(e3, panicLine, panicLine); !got[1] {
		t.Errorf("fired %v, want the panic after the restart to alert: the screen had left it", got)
	}
}

// The decoder reads the last 0 of a clock's 5:50 as an 8 at 11% (its middle
// bar nearly lit): a digit that may be another digit. It comes back as '?'
// in the text, so a watch skips the reading instead of acting on 58, while
// the Word keeps the 8 and its confidence for Test this region.
func TestUnsureDigitReadsAsAQuestionMark(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "partial", "casio-5-50.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	text, words, err := ocr.NewSevenSeg().RecognizeWords(context.Background(), img)
	if err != nil {
		t.Fatal(err)
	}
	if text != "5:5?" {
		t.Errorf("read %q, want 5:5? (the fixture's README says the last digit reads as an 8 at 11%%)", text)
	}
	if len(words) != 4 || words[3].Text != "8" || words[3].Conf >= 40 {
		t.Errorf("words %+v, want the fourth to keep the 8 the decoder saw, below 40%%", words)
	}
	cfg := config.Trigger{Type: "numeric", Pattern: `:([0-9]+)$`, Op: "gt", Threshold: 55, Confirm: 1}
	e := sevenseg(t, cfg)
	if ev := e.ObserveText(text); ev.Fired || ev.HasValue {
		t.Errorf("the watch acted on the unsure digit: %+v", ev)
	}
	if cond, err := CheckEngine(cfg, "sevenseg", text); err != nil || !cond.Skipped {
		t.Errorf("Test on %q: %+v, %v; want it skipped", text, cond, err)
	}
}

// A '?' a pattern is written to catch is a glyph that spells no digit:
// the "E" of the boiler recipe's E4 reads "?4". A digit the decoder saw
// and wasn't sure of also reads '?', and the pattern must not catch that:
// a 52 °C whose 5 has a third of its lower-left bar lit by glare reads
// "?2" (the 5 at 11%, which may be a 6), and that is a temperature, not a
// fault. The watch and Test skip such a reading whatever the pattern says.
// glare-52.png is that display, drawn (see the README beside it).
func TestUnsureDigitIsNotAFaultCode(t *testing.T) {
	fault := config.Trigger{Type: "ocr_match", Pattern: `^\?[0-9]+$`, Confirm: 3}
	f, err := os.Open(filepath.Join("testdata", "partial", "glare-52.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	text, words, err := ocr.NewSevenSeg().RecognizeWords(context.Background(), img)
	if err != nil {
		t.Fatal(err)
	}
	if text != "?2" || len(words) != 2 || words[0].Text != "5" || words[0].Conf >= ocr.UnsureDigit {
		t.Fatalf("read %q %+v, want ?2 with the 5 kept in its Word below %d%%", text, words, ocr.UnsureDigit)
	}
	unsure := ocr.HasUnsureDigit(text, words)
	if !unsure {
		t.Fatal("HasUnsureDigit says the ? is a glyph that spells no digit")
	}
	e := sevenseg(t, fault)
	for i := 0; i < 6; i++ {
		if ev := e.ObserveRead(text, unsure); ev.Fired || ev.HasSettled || !ev.Unsure {
			t.Fatalf("reading %d of the glared 52: %+v, want it skipped", i+1, ev)
		}
	}
	if cond, err := CheckRead(fault, "sevenseg", text, unsure); err != nil || !cond.Skipped || cond.Met {
		t.Errorf("Test on the glared 52: %+v, %v; want it skipped", cond, err)
	}

	// A '?' that is a glyph spelling no digit is the fault code.
	if ocr.HasUnsureDigit("?4", []ocr.Word{{Text: "?", Conf: 30}, {Text: "4", Conf: 100}}) {
		t.Error("an E4's ? counted as an unsure digit")
	}
	got := []bool{}
	for i := 0; i < 3; i++ {
		got = append(got, e.ObserveRead("?4", false).Fired)
	}
	if !got[2] {
		t.Errorf("fired %v, want the E4 to fire", got)
	}
}

// An ocr_changed watch on the seven-segment decoder: a reading with a '?'
// that fits the text the watch settled on (same length, every digit it did
// read the same) is that display read partly, not a change, so a steady
// 22.5 read as ?2.5 on a shaky frame never fires "text changed". A reading
// that doesn't fit still does: the washer's End reads as a bare '?' after
// 0:04, and a display that went dark reads as nothing.
func TestPartlyReadDisplayIsNotAChange(t *testing.T) {
	e := sevenseg(t, config.Trigger{Type: "ocr_changed", Confirm: 2})
	if got := feed(e, "22.5", "22.5", "?2.5", "?2.5", "22.5", "22.5", "?2.5", "?2.5", "2?.?", "22.5"); fires(got) != 0 {
		t.Errorf("fired %v, want nothing: the display never changed", got)
	}
	if st := e.State(); st.Stable != "22.5" {
		t.Errorf("the partial readings moved the settled text: %+v", st)
	}
	if got := feed(e, "23.5", "23.5"); !got[1] {
		t.Errorf("fired %v, want the change to 23.5 to fire", got)
	}
	if got := feed(e, "?3.5", "?3.5"); fires(got) != 0 {
		t.Errorf("fired %v, want nothing: ?3.5 fits 23.5", got)
	}

	// A real change the camera first caught partly fires once: ?3.5
	// doesn't fit 22.5, so it fires, and the whole 23.5 after it is that
	// same change, which takes over the settled text without firing.
	e = sevenseg(t, config.Trigger{Type: "ocr_changed", Confirm: 2})
	if got := feed(e, "22.5", "22.5", "?3.5", "?3.5", "23.5", "23.5", "23.5"); !got[3] || fires(got) != 1 {
		t.Errorf("fired %v, want one alert for one change read partly first", got)
	}
	if st := e.State(); st.Stable != "23.5" {
		t.Errorf("the whole reading should have taken over the settled text: %+v", st)
	}
	// A watch that starts on a partial reading moves on to the whole one
	// without firing: the display never changed.
	e = sevenseg(t, config.Trigger{Type: "ocr_changed", Confirm: 2})
	if got := feed(e, "?2.5", "?2.5", "22.5", "22.5", "22.5"); fires(got) != 0 {
		t.Errorf("fired %v, want nothing: 22.5 is the ?2.5 the watch started on, read whole", got)
	}
	if st := e.State(); st.Stable != "22.5" {
		t.Errorf("the whole reading should have taken over the settled text: %+v", st)
	}
	// Two partial readings of one display fit each other too, and a real
	// change after them still fires, once.
	e = sevenseg(t, config.Trigger{Type: "ocr_changed", Confirm: 2})
	if got := feed(e, "?2.5", "?2.5", "2?.5", "2?.5", "22.5", "22.5", "23.5", "23.5"); fires(got) != 1 || !got[7] {
		t.Errorf("fired %v, want only the change to 23.5", got)
	}

	washer := sevenseg(t, config.Trigger{Type: "ocr_changed", Confirm: 2})
	if got := feed(washer, "0:04", "0:04", "?:04", "?", "?"); fires(got) != 1 || !got[4] {
		t.Errorf("fired %v, want End (a bare ?) to fire once and ?:04 not at all", got)
	}
	dark := sevenseg(t, config.Trigger{Type: "ocr_changed", Confirm: 2})
	if got := feed(dark, "52", "52", "", ""); !got[3] {
		t.Errorf("fired %v, want a display that went dark to fire", got)
	}
	// Only the seven-segment decoder's '?' means a digit it didn't read.
	text, _ := mk(t, config.Trigger{Type: "ocr_changed", Confirm: 2})
	if got := feed(text, "22.5", "22.5", "?2.5", "?2.5"); !got[3] {
		t.Errorf("fired %v, want a text engine's ?2.5 to count as new text", got)
	}
}

// settledSeq feeds the readings and returns what each one settled, "-"
// where it settled nothing.
func settledSeq(e *Evaluator, readings ...string) []string {
	var out []string
	for _, r := range readings {
		ev := e.ObserveText(r)
		if ev.HasSettled {
			out = append(out, ev.Settled)
		} else {
			out = append(out, "-")
		}
	}
	return out
}

// Home Assistant's Reading is the text once it has held. A '?' reading the
// watch skips (ocr_match, the pattern doesn't match it) or takes as its
// stable text read partly (ocr_changed) says nothing about the display, so
// it neither settles nor breaks the run of the text that did: the Reading
// keeps the last text read whole. A '?' the pattern catches on purpose,
// and a text engine's '?', settle as any text.
func TestPartlyReadDoesNotReachTheReading(t *testing.T) {
	e := sevenseg(t, config.Trigger{Type: "ocr_changed", Confirm: 2})
	got := settledSeq(e, "22.5", "22.5", "??.?", "??.?", "2?.5", "22.5")
	want := []string{"-", "22.5", "-", "-", "-", "22.5"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("ocr_changed settled %q, want %q: the partial readings settle nothing and don't break 22.5's run", got, want)
	}

	m := sevenseg(t, config.Trigger{Type: "ocr_match", Pattern: `^\?[0-9]+$`, Confirm: 3})
	got = settledSeq(m, "52", "52", "52", "?4", "?4", "?4", "?", "?", "?", "?", "52", "52", "52")
	want = []string{"-", "-", "52", "-", "-", "?4", "-", "-", "-", "-", "-", "-", "52"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("ocr_match settled %q, want %q: ?4 is what the pattern catches, a bare ? is skipped", got, want)
	}

	tx, _ := mk(t, config.Trigger{Type: "ocr_match", Pattern: "E0[0-9]", Confirm: 2})
	if got := settledSeq(tx, "?", "?"); got[1] != "?" {
		t.Errorf("a text engine's ? settled %q, want it to settle like any text", got)
	}
}

// A seven-segment display that goes dark and comes back showing the same
// thing within a few minutes is one event: the blank readings are skipped
// and the condition kept. One that has shown nothing readable for longer
// than StaleAfter (the runner sets it to the restart window) may have been
// through anything: the watch forgets what it settled on, and the reading
// is news when the display comes back, held by a running cooldown as any
// fire is. A restart doesn't stop that clock: the window counts from the
// restore. (A text engine's blank is a reading of an empty line, which
// leaves the condition the usual way; see TestRealLeaveAndReturnFiresAgain.)
func TestALongDarkSpellForgetsTheCondition(t *testing.T) {
	const window = 15 * time.Minute
	// dark feeds n readings of r, step apart, none of which may fire.
	dark := func(t *testing.T, e *Evaluator, now *time.Time, n int, step time.Duration, r string) {
		t.Helper()
		for i := 0; i < n; i++ {
			*now = now.Add(step)
			if ev := e.ObserveText(r); ev.Fired {
				t.Fatalf("a %q reading fired: %+v", r, ev)
			}
		}
	}
	done := config.Trigger{Type: "ocr_match", Pattern: "^0:00$", Confirm: 2}
	t.Run("an appliance's 0:00 after 40 minutes dark", func(t *testing.T) {
		e, now := mk(t, done)
		e.SevenSeg, e.StaleAfter = true, window
		if got := feed(e, "0:00", "0:00"); !got[1] {
			t.Fatal("the first 0:00 should fire")
		}
		dark(t, e, now, 20, 2*time.Minute, "")
		if st := e.State(); st.HasStable {
			t.Errorf("40 minutes dark should have forgotten the condition: %+v", st)
		}
		if got := feed(e, "0:00", "0:00"); !got[1] {
			t.Errorf("fired %v, want 0:00 to be news after 40 minutes dark", got)
		}
	})
	for _, c := range []struct {
		name   string
		before int // dark readings, 2 minutes apart, before the restart
	}{{"a restart in the dark spell", 5}, {"a restart, then the dark spell", 0}} {
		t.Run(c.name, func(t *testing.T) {
			e, now := mk(t, done)
			e.SevenSeg, e.StaleAfter = true, window
			feed(e, "0:00", "0:00")
			dark(t, e, now, c.before, 2*time.Minute, "")
			saved, at := e.State(), *now
			restore := func() *Evaluator {
				*now = at
				e2, err := New(done)
				if err != nil {
					t.Fatal(err)
				}
				e2.Now = func() time.Time { return *now }
				e2.SevenSeg, e2.StaleAfter = true, window
				e2.Restore(saved)
				return e2
			}
			short := restore()
			dark(t, short, now, 6, 2*time.Minute, "")
			if got := feed(short, "0:00", "0:00"); fires(got) != 0 {
				t.Errorf("fired %v, want nothing: dark for less than the window since the restart", got)
			}
			long := restore()
			dark(t, long, now, 20, 2*time.Minute, "")
			if st := long.State(); st.HasStable {
				t.Errorf("40 minutes dark after the restart should have forgotten the condition: %+v", st)
			}
			if got := feed(long, "0:00", "0:00"); !got[1] {
				t.Errorf("fired %v, want 0:00 to be news after 40 minutes dark", got)
			}
		})
	}
	t.Run("a short dark spell keeps the condition", func(t *testing.T) {
		e, now := mk(t, done)
		e.SevenSeg, e.StaleAfter = true, window
		feed(e, "0:00", "0:00")
		dark(t, e, now, 6, 2*time.Second, "")
		dark(t, e, now, 1, 14*time.Minute, "")
		if got := feed(e, "0:00", "0:00", "0:00"); fires(got) != 0 {
			t.Errorf("fired %v, want nothing: the display was dark for less than the window", got)
		}
		if st := e.State(); st.Stable != condMet {
			t.Errorf("the dark spell changed the settled state: %+v", st)
		}
	})
	t.Run("a running cooldown still holds the repeat", func(t *testing.T) {
		cd := done
		cd.Cooldown = config.Duration(2 * time.Hour)
		e, now := mk(t, cd)
		e.SevenSeg, e.StaleAfter = true, window
		feed(e, "0:00", "0:00")
		firedAt := *now
		dark(t, e, now, 20, 2*time.Minute, "")
		feed(e, "0:00")
		if ev := e.ObserveText("0:00"); ev.Fired || ev.Pending != 2 || !ev.CooldownEnds.Equal(firedAt.Add(2*time.Hour)) {
			t.Errorf("inside the cooldown: %+v, want the repeat held until it ends", ev)
		}
		*now = firedAt.Add(2*time.Hour + time.Second)
		if ev := e.ObserveText("0:00"); !ev.Fired {
			t.Errorf("after the cooldown: %+v, want the held repeat to fire", ev)
		}
	})
	t.Run("without a window the condition is kept for good", func(t *testing.T) {
		e, now := mk(t, done)
		e.SevenSeg = true
		feed(e, "0:00", "0:00")
		dark(t, e, now, 20, 2*time.Minute, "")
		if got := feed(e, "0:00", "0:00"); fires(got) != 0 {
			t.Errorf("fired %v, want nothing: StaleAfter is zero", got)
		}
	})
}
