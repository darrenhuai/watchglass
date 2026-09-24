package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/state"
)

// grabTimeout bounds one camera grab for /snapshot and /test.
const grabTimeout = 10 * time.Second

// testReadBudgets is how long a Test gives the engine to read the crop,
// counted from the end of the grab. The subprocess engines start cold on
// every read (rapidocr imports its models each time: 3-5 s on a desktop,
// far more on a Pi or while the watch's own poll is reading at the same
// moment), so one shared 10 s for grab and read together turned a busy box
// into "context deadline exceeded". The built-in decoder is in-process and
// fast. A var so tests can shorten it.
var testReadBudgets = map[string]time.Duration{
	"sevenseg":  10 * time.Second,
	"tesseract": 30 * time.Second,
	"rapidocr":  45 * time.Second,
}

func testReadBudget(engine string) time.Duration {
	if d, ok := testReadBudgets[engine]; ok {
		return d
	}
	return testReadBudgets["tesseract"]
}

// readFailureNote turns a failed Test read into the sentence the result
// shows; the raw error goes behind "Technical detail". A read that ran out
// of time says so in words, with what to do about it, instead of Go's
// context wording. timedOut comes from the read's own context: tesseract
// reports a kill as "signal: killed", not as the context's error.
func readFailureNote(engine string, budget time.Duration, timedOut bool) string {
	if timedOut {
		return fmt.Sprintf("%s didn't finish reading within %d seconds, so the test stopped. The box may be busy (the watch reads with %s too). Try again in a moment.",
			engine, int(budget.Seconds()), engine)
	}
	return engine + " couldn't read the region."
}

// stripTile is one frame of the Live filmstrip: a run of consecutive
// samples that read the same (same text, same fired state, same step
// towards Confirm) shown once,
// with the newest crop and a count. Ten identical "?" or "0.0% changed"
// tiles in a row said nothing the first one didn't.
type stripTile struct {
	state.Sample
	Count int
}

// collapseSamples merges runs of consecutive equal readings, newest first
// (the order state.Registry.Recent returns).
func collapseSamples(recent []state.Sample) []stripTile {
	var tiles []stripTile
	for _, s := range recent {
		if n := len(tiles); n > 0 && tiles[n-1].Reading == s.Reading && tiles[n-1].Fired == s.Fired && tiles[n-1].Pending == s.Pending {
			tiles[n-1].Count++
			continue
		}
		tiles = append(tiles, stripTile{Sample: s, Count: 1})
	}
	return tiles
}

// renderCached renders like render, with an ETag over the rendered bytes:
// the Live panel polls every 2 s and its fragment carries every crop as
// base64 (100-200 KB), so an unchanged answer goes back as a bodiless 304.
// Hashing the bytes, not a hand-picked key, can never call changed content
// unchanged. no-cache makes the browser revalidate every time rather than
// reuse a copy on its own.
func (s *Server) renderCached(w http.ResponseWriter, r *http.Request, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logf("render %s: %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(buf.Bytes())
	etag := fmt.Sprintf(`"%x"`, sum[:12])
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// etagMatches compares If-None-Match loosely, as RFC 9110 asks for it: a
// list of tags, "*", and weak W/ tags (a compressing proxy weakens ours).
func etagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || strings.TrimPrefix(t, "W/") == etag {
			return true
		}
	}
	return false
}

// progressView is the line under the Live readout while the newest
// reading meets the trigger but Confirm isn't reached yet, so "PRINT
// COMPLETE" read once on a watch that needs three in a row says why it
// hasn't fired: "Matches the pattern: 1 of 3 readings in a row needed to
// fire." Steps draws the same count as dots (true = counted), only while
// there are few enough to read at a glance. While Cooldown runs, the line
// says the alert is held and until when (Until, a <time> between Text and
// After), so a count that reaches 3 of 3 without a fire isn't a mystery
// either.
type progressView struct {
	Text  string
	Steps []bool
	Until time.Time
	After string
}

// maxProgressDots is the most dots the readout draws; a longer Confirm is
// told in words only.
const maxProgressDots = 10

// confirmProgress is nil when the reading isn't on its way to a fire.
func confirmProgress(trig config.Trigger, s state.Sample) *progressView {
	cooling := !s.CooldownEnds.IsZero()
	if s.Pending <= 0 || s.Fired || (s.Need <= 1 && !cooling) {
		return nil
	}
	cond := "Condition met"
	switch trig.Type {
	case "ocr_match":
		cond = "Matches the pattern"
	case "ocr_changed":
		cond = "New text"
	case "numeric":
		word := "Above"
		if trig.Op == "lt" {
			word = "Below"
		}
		cond = word + " " + strconv.FormatFloat(trig.Threshold, 'f', -1, 64)
	}
	var pv *progressView
	switch {
	case cooling && s.Pending >= s.Need:
		// Confirmed, and held: the first confirmed reading after the
		// cooldown fires (trigger.go's delay-not-drop cooldown).
		pv = &progressView{Text: cond + ". Cooldown holds the alert until", Until: s.CooldownEnds, After: "; it goes out then if this still holds."}
	case cooling:
		pv = &progressView{Text: fmt.Sprintf("%s: %d of %d readings in a row. Cooldown holds any alert until", cond, s.Pending, s.Need), Until: s.CooldownEnds, After: "."}
	default:
		pv = &progressView{Text: fmt.Sprintf("%s: %d of %d readings in a row needed to fire.", cond, s.Pending, s.Need)}
	}
	if s.Need > 1 && s.Need <= maxProgressDots {
		pv.Steps = make([]bool, s.Need)
		for i := 0; i < s.Pending && i < s.Need; i++ {
			pv.Steps[i] = true
		}
	}
	return pv
}
