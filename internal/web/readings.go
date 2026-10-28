package web

import (
	"math"
	"strings"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// readingRun is a stretch of a reading shown one way: Unknown runs are the
// seven-segment decoder's '?', a digit it saw but couldn't read.
type readingRun struct {
	Text    string
	Unknown bool
}

// readingView is a reading prepared for display. A bare '?' next to a
// healthy LED read as a template bug, so undecoded digits are marked, and
// an all-'?' reading is marked too and explained in words beside it. It
// is still shown: a watch can act on it (the washer recipe's pattern
// matches "?", which is how its End screen reads), and the page must show
// what the watch acted on. An empty reading is named instead (Placeholder).
type readingView struct {
	Runs []readingRun
	// Partial: some digits were read and some weren't.
	Partial bool
	// Unreadable: the decoder found digits but read none of them. Runs
	// still hold the reading ("?", "?.?"); callers add the explanation.
	Unreadable bool
	// Placeholder replaces the reading when there is nothing to show.
	Placeholder string
	// Set by viewReadingFor only, for a reading with a '?' (Partial or
	// Unreadable) or none at all (Placeholder): Skipped when the watch's
	// numeric trigger skips it, and Matched when the watch's ocr_match
	// pattern matches a '?' reading, '?' and all. A Matched '?' is what the
	// pattern was written to catch (the washer's End, the boiler's E4), so
	// the page doesn't tell the user to fix the region; a Skipped one is a
	// reading the watch ignores, so the page says that instead of showing
	// it beside a healthy green LED.
	Skipped bool
	Matched bool
}

// matchedNote is what Test and Live say under a '?' reading the watch's
// pattern matches: the box is doing what the recipe asks, so it isn't
// told to tighten the region unless the display shows a number there.
const matchedNote = "The pattern matches the ? itself, so the watch acts on this reading. That is expected when the display shows a letter here. If it shows a number, tighten the region around the digits or adjust Binarize."

// skippedNote is the dashboard's hover text on "skipped", a numeric
// watch's '?' reading; blankSkippedNote the same for a blank reading, and
// Live's hint on it.
const (
	skippedNote      = "A digit wasn't read for sure, so the watch skips this reading and keeps its last state."
	blankSkippedNote = "Nothing was read, so the watch skips this reading and keeps its last state."
)

// viewReadingFor is viewReading for a watch with trigger trig: it also
// says what the running watch does with a '?' or blank reading (Skipped,
// Matched), judged by trigger.CheckRead as the runner judges it. unsure
// says a '?' is a digit the decoder wasn't sure of (ocr.HasUnsureDigit),
// which the watch skips whatever its pattern says.
func viewReadingFor(trig config.Trigger, engine, reading string, unsure bool) readingView {
	v := viewReading(engine, reading)
	if !v.Partial && !v.Unreadable && reading != "" {
		return v
	}
	cond, err := trigger.CheckRead(trig, engine, reading, unsure)
	if err != nil {
		return v
	}
	v.Skipped = cond.Skipped
	v.Matched = reading != "" && trig.Type == "ocr_match" && cond.Met
	return v
}

// viewReading splits reading for display. Only the sevenseg engine's '?'
// means "not decoded": tesseract and rapidocr read printed text, where a
// question mark is just a question mark.
func viewReading(engine, reading string) readingView {
	if reading == "" {
		return readingView{Placeholder: "Nothing read"}
	}
	if engine != "sevenseg" || !strings.Contains(reading, "?") {
		return readingView{Runs: []readingRun{{Text: reading}}}
	}
	// The decoder's alphabet is digits, '-', '.', ':' and '?'; with no digit
	// read at all ("?", "?.?", "-?") it is unreadable rather than partial.
	v := readingView{Partial: true}
	if !strings.ContainsAny(reading, "0123456789") {
		v = readingView{Unreadable: true}
	}
	for len(reading) > 0 {
		unknown := reading[0] == '?'
		n := strings.IndexFunc(reading, func(r rune) bool { return (r == '?') != unknown })
		if n < 0 {
			n = len(reading)
		}
		v.Runs = append(v.Runs, readingRun{Text: reading[:n], Unknown: unknown})
		reading = reading[n:]
	}
	return v
}

// lowConfidence is the line under which a test chip is marked low.
const lowConfidence = 60.0

// confidencePct is an engine confidence (0-100) as a whole percent,
// rounded down: 99.996 never shows as an overstated 100, and 59.9 shows as
// 59, on the same side of the low-confidence line as its marker.
func confidencePct(c float64) int {
	if math.IsNaN(c) || c < 0 {
		return 0
	}
	return int(math.Floor(c))
}

// isoTime is t for a <time datetime> attribute; app.js rewrites the text
// in the viewer's own time zone from it. The server's zone (often UTC in a
// container) stays as the no-script fallback text.
func isoTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
