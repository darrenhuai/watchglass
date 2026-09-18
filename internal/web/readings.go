package web

import (
	"math"
	"strings"
	"time"
)

// readingRun is a stretch of a reading shown one way: Unknown runs are the
// seven-segment decoder's '?', a digit it saw but couldn't read.
type readingRun struct {
	Text    string
	Unknown bool
}

// readingView is a reading prepared for display. A bare '?' next to a
// healthy LED read as a template bug, so undecoded digits are marked and an
// all-'?' or empty reading is named in words (Placeholder) instead.
type readingView struct {
	Runs []readingRun
	// Partial: some digits were read and some weren't.
	Partial bool
	// Unreadable: the decoder found digits but read none of them.
	Unreadable bool
	// Placeholder replaces the reading when there is nothing to show.
	Placeholder string
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
	// read at all, what's left ("?", "?.?", "-?") says nothing.
	if !strings.ContainsAny(reading, "0123456789") {
		return readingView{Unreadable: true, Placeholder: "No digits read"}
	}
	v := readingView{Partial: true}
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
