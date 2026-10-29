package web

import (
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/runner"
	"github.com/darrenhuai/watchglass/internal/supervisor"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// The number a numeric or value watch publishes, on the pages: the value
// and what a counter is holding back (valueView), the readings as a CSV
// file (history.csv), and the accept action for a lower value after a
// meter was replaced (accept-lower).

// valueView is what the Live panel and the dashboard say about a watch's
// published number. nil for a watch without one or that isn't running.
type valueView struct {
	// Text is the last value published with its unit ("1234.5 kWh"), ""
	// before there is one.
	Text string
	At   time.Time
	// Held is set while the counter holds a reading back.
	Held *heldView
	// Accepting is a lower value accepted on the page that the next
	// reading takes, as text ("12.5"), or "".
	Accepting string
	// Counter: the watch's number is a counter.
	Counter bool
	// Kept: Text is a counter's last value from before the restart, not
	// yet published again in this run. Accepted: Text is a lower value
	// accepted on the page, not yet published (no reading has reached it).
	Kept     bool
	Accepted bool
}

// heldView is one held reading, in the page's words.
type heldView struct {
	Lower bool
	// Value is the reading held back as the accept form posts it, and
	// ValueText the same with the unit, for the button.
	Value     string
	ValueText string
	Text      string // the sentence
	Since     time.Time
}

func numText(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func withUnit(f float64, unit string) string {
	if unit == "" {
		return numText(f)
	}
	return numText(f) + " " + unit
}

// valueFor is the watch's valueView, from its running runner.
func (s *Server) valueFor(wc config.Watch) *valueView {
	if !config.HasValue(wc.Trigger.Type) {
		return nil
	}
	get := s.valueOf
	if get == nil {
		if s.sup == nil {
			return nil
		}
		get = s.sup.Value
	}
	st, ok := get(wc.Name)
	if !ok {
		return nil
	}
	return newValueView(st, wc.Unit)
}

func newValueView(st runner.ValueStatus, unit string) *valueView {
	v := &valueView{Counter: st.Counter, At: st.At}
	switch {
	case st.HasValue:
		v.Text = withUnit(st.Value, unit)
	case st.Counter && st.HasLast:
		v.Text = withUnit(st.Last, unit)
		v.Kept, v.Accepted = st.Restored, !st.Restored
	}
	if st.HasAccepting {
		v.Accepting = withUnit(st.Accepting, unit)
	}
	if h := st.Held; h != nil {
		v.Held = &heldView{Lower: h.Lower, Value: numText(h.Value), ValueText: withUnit(h.Value, unit), Since: st.HeldSince, Text: holdText(*h, unit)}
	}
	return v
}

// holdText says why a reading was held back, in a sentence.
func holdText(h trigger.Hold, unit string) string {
	if h.Lower {
		return fmt.Sprintf("Held back: %s is lower than the last value %s. A counter never goes down, so it isn't published.",
			withUnit(h.Value, unit), withUnit(h.Last, unit))
	}
	return fmt.Sprintf("Held back: %s is more than %s above the last value %s. It is published once %d readings in a row agree (%d so far).",
		withUnit(h.Value, unit), withUnit(h.MaxRise, unit), withUnit(h.Last, unit), h.Need, h.Count)
}

// acceptLower is POST /watch/{name}/accept-lower: take value, lower than
// the counter's last value, as its new one (a replaced or reset meter).
// It goes through the same CSRF check as every other POST (Handler).
func (s *Server) acceptLower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	wc, ok := s.findWatch(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := watchURL(s.base(r), name, "")
	raw := strings.TrimSpace(r.FormValue("value"))
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		s.setFlash(w, r, "acceptfail", raw, "That isn't a number.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	accept := s.acceptOf
	if accept == nil {
		accept = s.sup.AcceptLower
	}
	err = accept(name, v)
	switch {
	case err == nil:
		s.setFlash(w, r, "accepted", withUnit(v, wc.Unit), "")
	case errors.Is(err, supervisor.ErrNotRunning):
		s.setFlash(w, r, "acceptfail", withUnit(v, wc.Unit), "The watch isn't running. Start it with Save, then accept the value again.")
	case errors.Is(err, runner.ErrNotLower):
		s.setFlash(w, r, "acceptfail", withUnit(v, wc.Unit), "It isn't lower than the counter's last value any more, so there is nothing to accept.")
	case errors.Is(err, runner.ErrNotHeld):
		s.setFlash(w, r, "acceptfail", withUnit(v, wc.Unit), "The display is back at the counter's value, so there is nothing to accept.")
	case errors.Is(err, runner.ErrNotCounter):
		s.setFlash(w, r, "acceptfail", withUnit(v, wc.Unit), "This watch isn't a counter.")
	default:
		s.logf("watch %s: accept %s: %v", name, numText(v), err)
		s.setFlash(w, r, "acceptfail", withUnit(v, wc.Unit), "It couldn't be saved: "+err.Error())
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// csvPage is how many readings history.csv reads from the database at a
// time. Each page is written out (and flushed) before the next is read, so
// a long history never sits in memory at once, and the database's one
// connection is free between pages for the watches to record into.
var csvPage = 500 // a var so a test can make pages small

// historyCSV is GET /watch/{name}/history.csv: the watch's recorded
// readings, oldest first, as time (ISO 8601 with the zone), reading,
// value and fired. from and to take a date (2026-10-01) or a time
// (2026-10-01T08:00:00+02:00); from is inclusive, to exclusive, and a to
// date includes that whole day. Like every page it is behind basic auth
// and works under -base-path and ingress (Handler).
func (s *Server) historyCSV(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.findWatch(name); !ok {
		http.NotFound(w, r)
		return
	}
	db := s.sup.History()
	if db == nil {
		http.Error(w, "There is no history database, so there are no readings to export.", http.StatusServiceUnavailable)
		return
	}
	from, err := parseCSVTime(r.FormValue("from"), false)
	if err != nil {
		http.Error(w, "from: "+err.Error(), http.StatusBadRequest)
		return
	}
	to, err := parseCSVTime(r.FormValue("to"), true)
	if err != nil {
		http.Error(w, "to: "+err.Error(), http.StatusBadRequest)
		return
	}
	// The first page is read before anything is written, so a database
	// that can't be read gets an error page, not an empty file.
	rows, err := db.ReadingsPage(name, 0, from, to, csvPage)
	if err != nil {
		s.logf("watch %s: history.csv: %v", name, err)
		http.Error(w, "The readings couldn't be read from the history database: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+csvFileName(name)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := csv.NewWriter(w)
	flusher, _ := w.(http.Flusher)
	cw.Write([]string{"time", "reading", "value", "fired"})
	for {
		for _, row := range rows {
			value := ""
			if row.HasValue {
				value = numText(row.Value)
			}
			cw.Write([]string{row.TS.Format(time.RFC3339), inertCell(row.Reading), value, strconv.FormatBool(row.Fired)})
		}
		cw.Flush()
		if flusher != nil {
			flusher.Flush()
		}
		if len(rows) < csvPage || cw.Error() != nil || r.Context().Err() != nil {
			return
		}
		after := rows[len(rows)-1].ID
		if rows, err = db.ReadingsPage(name, after, from, to, csvPage); err != nil {
			// Part of the file is out with a 200: cutting the connection
			// is the only way left to tell the client it is incomplete,
			// so a cut-short file never looks like the whole history.
			s.logf("watch %s: history.csv: stopped after the reading with id %d: %v", name, after, err)
			panic(http.ErrAbortHandler)
		}
	}
}

// parseCSVTime reads a from or to: a date in the server's zone or an RFC
// 3339 time. A date given as to means the end of that day.
func parseCSVTime(v string, end bool) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		if end {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	// A + typed into a URL arrives as a space: 2026-10-01T08:00:00+02:00
	// as written in the docs comes in as "2026-10-01T08:00:00 02:00".
	if i := strings.LastIndexByte(v, ' '); i > 0 && strings.Contains(v[:i], "T") {
		if t, err := time.Parse(time.RFC3339, v[:i]+"+"+v[i+1:]); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q isn't a date like 2026-10-01 or a time like 2026-10-01T08:00:00+02:00", v)
}

// inertCell keeps a reading from being taken for a formula by a
// spreadsheet: one that starts with = + - @ (or a tab or carriage return,
// which some spreadsheets skip before looking) gets a ' in front, which
// spreadsheets show as text. A plain number such as -12.5 is left as it
// is: it can't be a formula, and it should stay a number.
func inertCell(s string) string {
	if s == "" || !strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return s
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil && !strings.ContainsAny(s, "xXpPnNiI_") {
		return s
	}
	return "'" + s
}

// csvFileName is the download's file name: the watch name with anything
// that isn't a plain letter, digit, dot, dash or underscore made a dash.
func csvFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x80 && (r == '.' || r == '-' || r == '_' || ('0' <= r && r <= '9') || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String() + "-history.csv"
}

// meterFromForm reads the Counter box and Max rise into w, a numeric or
// value watch, and returns their field errors. A client that sends
// neither (curl, a stale page) keeps base's: the page says it showed the
// box with counter_shown, since an unticked box sends nothing.
func meterFromForm(r *http.Request, w *config.Watch, base config.Watch) []fieldError {
	var errs []fieldError
	if r.FormValue("counter_shown") != "" {
		w.Counter = r.FormValue("counter") != ""
	} else {
		w.Counter = base.Counter
	}
	// energy, water, gas and volume are counters by their class; the
	// file needn't say so as well.
	if slices.Contains(config.CounterClasses, w.DeviceClass) {
		w.Counter = false
	}
	if _, ok := r.Form["max_rise"]; ok {
		w.MaxRise = 0
		if v := strings.TrimSpace(r.FormValue("max_rise")); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			switch {
			case err != nil || math.IsNaN(f) || math.IsInf(f, 0):
				errs = append(errs, fieldError{"max_rise", "Max rise must be a number, or empty for no limit."})
				w.MaxRise = base.MaxRise
			case f < 0:
				errs = append(errs, fieldError{"max_rise", "Max rise must be above 0, or empty for no limit."})
				w.MaxRise = base.MaxRise
			default:
				w.MaxRise = f
			}
		}
	}
	probe := *w
	probe.Unit = config.NormalizeUnit(probe.Unit)
	cfg := config.Config{Watches: []config.Watch{probe}}
	if err := cfg.Validate(); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "can't be a counter"):
			errs = append(errs, fieldError{"counter", upperFirst(w.DeviceClass) + " can't be a counter: Home Assistant keeps it as a measurement. Untick Counter, or pick another device class."})
		case strings.Contains(msg, "max_rise only applies to a counter"):
			errs = append(errs, fieldError{"max_rise", "Max rise only applies to a counter: tick Counter, or leave it empty."})
		}
	}
	return errs
}

// historyKept is how far back the readings go, for the CSV link.
func (s *Server) historyKept() string {
	s.mu.Lock()
	days := s.cfg.HistoryDays
	s.mu.Unlock()
	switch {
	case days < 0:
		return "since the first reading"
	case days == 0 || days == 30:
		return "for the last 30 days"
	case days == 1:
		return "for the last day"
	}
	return "for the last " + strconv.Itoa(days) + " days"
}
