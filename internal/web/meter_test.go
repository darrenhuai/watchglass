package web

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/runner"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/supervisor"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// newMeterServer is a server with one value watch, "meter" (an energy
// counter), and a history database behind its supervisor.
func newMeterServer(t *testing.T) (*Server, *history.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := history.Open(filepath.Join(dir, "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{Watches: []config.Watch{{
		Name: "meter", Source: "http://unused.invalid/snap.jpg", Interval: config.Duration(time.Second),
		Region: config.Region{W: 1, H: 1}, Engine: "sevenseg",
		Trigger: config.Trigger{Type: "value", Confirm: 3}, Unit: "kWh", DeviceClass: "energy",
	}}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	reg := state.New(5)
	engines := ocr.Engines{Tesseract: fakeDetailed{}, SevenSeg: ocr.NewSevenSeg()}
	sup := supervisor.New(store, reg, engines, func(string, ...any) {})
	sup.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: testImage()}, nil }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, engines, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	return s, store
}

// flushCounter is a ResponseRecorder that counts Flush calls.
type flushCounter struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushCounter) Flush() { f.flushes++; f.ResponseRecorder.Flush() }

func readCSV(t *testing.T, body string) [][]string {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v\n%s", err, body)
	}
	return recs
}

// V1.5: history.csv has a header, one row per reading, oldest first, with
// the time in ISO 8601 with its zone, the reading, the value it published
// and whether it fired. A cell a spreadsheet would run as a formula gets a
// ' in front; a negative number stays a number.
func TestHistoryCSV(t *testing.T) {
	s, store := newMeterServer(t)
	base := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	readings := []struct {
		text  string
		value float64
		has   bool
	}{
		{"1234.5", 1234.5, true},
		{`=HYPERLINK("http://evil.example","x")`, 0, false},
		{"+1+2", 0, false},
		{"-12.5", -12.5, true},
		{"@SUM(A1:A9)", 0, false},
		{"Err, 4", 0, false},
	}
	for i, r := range readings {
		ts := base.Add(time.Duration(i) * time.Minute)
		var err error
		if r.has {
			err = store.RecordValue("meter", ts, r.text, r.value, false)
		} else {
			err = store.Record("meter", ts, r.text, i == 5)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	resp, body := get(t, s.Handler(), "/watch/meter/history.csv")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="meter-history.csv"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	recs := readCSV(t, body)
	if len(recs) != 7 || strings.Join(recs[0], ",") != "time,reading,value,fired" {
		t.Fatalf("records = %q", recs)
	}
	zone := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(Z|[+-]\d\d:\d\d)$`)
	for i, rec := range recs[1:] {
		at, err := time.Parse(time.RFC3339, rec[0])
		if err != nil || !zone.MatchString(rec[0]) || !at.Equal(base.Add(time.Duration(i)*time.Minute)) {
			t.Errorf("row %d time %q: want ISO 8601 with a zone, at %v", i, rec[0], base.Add(time.Duration(i)*time.Minute))
		}
	}
	wantCells := [][3]string{
		{"1234.5", "1234.5", "false"},
		{`'=HYPERLINK("http://evil.example","x")`, "", "false"},
		{"'+1+2", "", "false"},
		{"-12.5", "-12.5", "false"},
		{"'@SUM(A1:A9)", "", "false"},
		{"Err, 4", "", "true"},
	}
	for i, want := range wantCells {
		got := recs[i+1][1:]
		if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Errorf("row %d = %q, want %q", i, got, want)
		}
	}
	// The bytes of the formula row, as a spreadsheet gets them.
	if !strings.Contains(body, `,"'=HYPERLINK(""http://evil.example"",""x"")",,false`) {
		t.Errorf("formula cell bytes:\n%s", body)
	}

	// A range: from inclusive, to exclusive; a to date takes in its day.
	_, body = get(t, s.Handler(), "/watch/meter/history.csv?from="+url.QueryEscape(base.Add(time.Minute).Format(time.RFC3339))+"&to="+url.QueryEscape(base.Add(3*time.Minute).Format(time.RFC3339)))
	if recs := readCSV(t, body); len(recs) != 3 || recs[1][1] != `'=HYPERLINK("http://evil.example","x")` {
		t.Errorf("range rows = %q", recs)
	}
	day := base.In(time.Local).Format("2006-01-02")
	_, body = get(t, s.Handler(), "/watch/meter/history.csv?from="+day+"&to="+day)
	if recs := readCSV(t, body); len(recs) != 7 {
		t.Errorf("a day's range: %d rows, want 7", len(recs))
	}
	// The time as the docs write it, typed into a URL: its + arrives as a
	// space. Before, that answered 400 with the very form that failed.
	_, body = get(t, s.Handler(), "/watch/meter/history.csv?from=2026-10-01T10:01:00+02:00&to=2026-10-01T10:03:00+02:00")
	if recs := readCSV(t, body); len(recs) != 3 || recs[1][1] != `'=HYPERLINK("http://evil.example","x")` {
		t.Errorf("a range with a + typed as it is: %q", recs)
	}
	if resp, body := get(t, s.Handler(), "/watch/meter/history.csv?from=yesterday"); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "isn't a date") {
		t.Errorf("bad from: %d %s", resp.StatusCode, body)
	}
	if resp, _ := get(t, s.Handler(), "/watch/nope/history.csv"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown watch: %d", resp.StatusCode)
	}
}

// history.csv is written page by page, each flushed before the next is
// read, so a long history never sits in memory at once.
func TestHistoryCSVStreams(t *testing.T) {
	s, store := newMeterServer(t)
	base := time.Unix(1700000000, 0)
	n := 3*csvPage + 7
	for i := 0; i < n; i++ {
		if err := store.RecordValue("meter", base.Add(time.Duration(i)*time.Second), strconv.Itoa(i), float64(i), false); err != nil {
			t.Fatal(err)
		}
	}
	rec := &flushCounter{ResponseRecorder: httptest.NewRecorder()}
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/watch/meter/history.csv", nil))
	recs := readCSV(t, rec.Body.String())
	if len(recs) != n+1 {
		t.Fatalf("%d rows, want %d", len(recs)-1, n)
	}
	for i, r := range recs[1:] {
		if r[2] != strconv.Itoa(i) {
			t.Fatalf("row %d value %q: out of order", i, r[2])
		}
	}
	if rec.flushes < 4 {
		t.Errorf("flushed %d times for %d rows; each page of %d should go out on its own", rec.flushes, n, csvPage)
	}
}

// A history that can't be read answers an error, not an empty 200 that
// looks like a watch with no readings.
func TestHistoryCSVReadError(t *testing.T) {
	s, store := newMeterServer(t)
	if err := store.RecordValue("meter", time.Unix(1700000000, 0), "1", 1, false); err != nil {
		t.Fatal(err)
	}
	store.Close()
	resp, body := get(t, s.Handler(), "/watch/meter/history.csv")
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, "couldn't be read") || resp.Header.Get("Content-Disposition") != "" {
		t.Fatalf("closed store: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Disposition"), body)
	}
}

// closeOnFlush closes the history store at the first flush, so the read
// of the next page fails with part of the file already sent.
type closeOnFlush struct {
	http.ResponseWriter
	store *history.Store
	once  bool
}

func (c *closeOnFlush) Flush() {
	c.ResponseWriter.(http.Flusher).Flush()
	if !c.once {
		c.once = true
		c.store.Close()
	}
}

// A read that fails after the first page cuts the download off, so the
// client sees an error, not a short file that looks complete.
func TestHistoryCSVFailsMidway(t *testing.T) {
	s, store := newMeterServer(t)
	old := csvPage
	csvPage = 5 // two pages without recording hundreds of readings
	t.Cleanup(func() { csvPage = old })
	base := time.Unix(1700000000, 0)
	for i := 0; i < csvPage+3; i++ {
		if err := store.RecordValue("meter", base.Add(time.Duration(i)*time.Second), strconv.Itoa(i), float64(i), false); err != nil {
			t.Fatal(err)
		}
	}
	h := s.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(&closeOnFlush{ResponseWriter: w, store: store}, r)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/watch/meter/history.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("the download ended cleanly after %d bytes; a cut-short file must fail", len(got))
	}
	if !strings.HasPrefix(string(got), "time,reading,value,fired\n") {
		t.Errorf("the first page should have gone out: %.80q", got)
	}
}

// Like every page: behind basic auth, through ingress, and linked from the watch page with
// the base path.
func TestHistoryCSVAuthAndLink(t *testing.T) {
	s, _ := newMeterServer(t)
	s.cfg.Auth = &config.Auth{Username: "u", Password: "p"}
	if resp, _ := get(t, s.Handler(), "/watch/meter/history.csv"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no credentials: %d, want 401", resp.StatusCode)
	}
	req := httptest.NewRequest("GET", "/watch/meter/history.csv", nil)
	req.SetBasicAuth("u", "p")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("with credentials: %d", rec.Code)
	}
	// Through Home Assistant's ingress, which has logged the user in: no
	// second login, and the link carries the ingress prefix.
	s.Ingress = true
	s.IngressFrom = netip.MustParseAddr(ingressPeer)
	if resp, body := viaIngress(t, s.Handler(), "GET", "/watch/meter/history.csv", nil); resp.StatusCode != 200 || !strings.HasPrefix(body, "time,reading,value,fired") {
		t.Errorf("through ingress: %d %q", resp.StatusCode, body)
	}
	if _, body := viaIngress(t, s.Handler(), "GET", "/watch/meter", nil); !strings.Contains(body, `<a href="`+ingressPrefix+`/watch/meter/history.csv" download>`) {
		t.Error("the watch page doesn't link the CSV under the ingress prefix")
	}
	s.Ingress = false
	s.cfg.Auth = nil
	s.BasePath = "/wg"
	_, body := get(t, s.Handler(), "/watch/meter")
	if !strings.Contains(body, `<a href="/wg/watch/meter/history.csv" download>Download readings (CSV)</a>`) {
		t.Error("the watch page doesn't link the CSV under the base path")
	}
}

// V1.3: the Live panel says what a counter holds back and offers the
// accept for a lower value; the accept posts through the CSRF check and
// reaches the runner.
func TestLiveShowsHeldValueAndAccepts(t *testing.T) {
	s, _ := newMeterServer(t)
	held := runner.ValueStatus{Value: 1240.1, HasValue: true, At: time.Unix(1700000000, 0), Counter: true, Last: 1240.1, HasLast: true,
		Held: &trigger.Hold{Value: 1234.5, Last: 1240.1, Lower: true}, HeldSince: time.Unix(1700000010, 0)}
	s.valueOf = func(string) (runner.ValueStatus, bool) { return held, true }
	var accepted []float64
	s.acceptOf = func(name string, v float64) error {
		accepted = append(accepted, v)
		return nil
	}
	_, body := get(t, s.Handler(), "/watch/meter/live")
	for _, want := range []string{
		`<span class="readout-label">Counter</span> <strong class="mono value-now">1240.1 kWh</strong>`,
		`<p class="value-held">Held back: 1234.5 kWh is lower than the last value 1240.1 kWh. A counter never goes down, so it isn&#39;t published.</p>`,
		`<form method="post" action="/watch/meter/accept-lower" class="accept-form">`,
		`<input type="hidden" name="value" value="1234.5">`,
		`Accept 1234.5 kWh as the new value`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("live fragment missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "No notification URLs") {
		t.Error("a value watch never fires, so it doesn't ask for notify URLs")
	}
	// The list shows the number of a running watch with a reading.
	wc, _ := s.findWatch("meter")
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	s.reg.Add("meter", state.Sample{TS: time.Unix(1700000020, 0), Reading: "1234.5", PNG: []byte("png")})
	_, index := get(t, s.Handler(), "/")
	if !strings.Contains(index, `<span class="tag tag-held" title="Held back: 1234.5 kWh is lower`) {
		t.Errorf("the list doesn't mark the held value:\n%s", index)
	}

	// A cross-site post is refused before it gets anywhere.
	resp, _ := postFormWithHeaders(t, s.Handler(), "/watch/meter/accept-lower", url.Values{"value": {"1234.5"}}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden || len(accepted) != 0 {
		t.Fatalf("cross-site accept: %d, accepted %v", resp.StatusCode, accepted)
	}
	resp, _ = postForm(t, s.Handler(), "/watch/meter/accept-lower", url.Values{"value": {"1234.5"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/watch/meter" || len(accepted) != 1 || accepted[0] != 1234.5 {
		t.Fatalf("accept: %d %q, accepted %v", resp.StatusCode, resp.Header.Get("Location"), accepted)
	}
	page := followFlash(t, s, resp)
	if !strings.Contains(page, "Accepted <strong class=\"mono\">1234.5 kWh</strong> as the counter's new value.") {
		t.Errorf("no confirmation after the accept:\n%s", page)
	}

	// After a restart the counter's last value is kept before anything is
	// published again: the panel says so instead of "no value yet".
	s.valueOf = func(string) (runner.ValueStatus, bool) {
		return runner.ValueStatus{Counter: true, Last: 3901, HasLast: true, Restored: true, Held: &trigger.Hold{Value: 13, Last: 3901, Lower: true}}, true
	}
	if _, body := get(t, s.Handler(), "/watch/meter/live"); !strings.Contains(body, `<strong class="mono value-now">3901 kWh</strong> <span class="muted">kept from before the restart</span>`) {
		t.Errorf("restored counter:\n%s", body)
	}
	// A lower value accepted since, that no reading has reached yet, was
	// never published: it isn't the one kept from before the restart.
	s.valueOf = func(string) (runner.ValueStatus, bool) {
		return runner.ValueStatus{Counter: true, Last: 13, HasLast: true}, true
	}
	if _, body := get(t, s.Handler(), "/watch/meter/live"); !strings.Contains(body, `<strong class="mono value-now">13 kWh</strong> <span class="muted">accepted, not published yet</span>`) ||
		strings.Contains(body, "kept from before the restart") {
		t.Errorf("accepted counter:\n%s", body)
	}

	s.acceptOf = func(string, float64) error { return runner.ErrNotHeld }
	resp, _ = postForm(t, s.Handler(), "/watch/meter/accept-lower", url.Values{"value": {"1234.5"}})
	if page := followFlash(t, s, resp); !strings.Contains(page, "The display is back at the counter&#39;s value, so there is nothing to accept.") {
		t.Errorf("a stale accept should say why:\n%s", page)
	}

	s.acceptOf = func(string, float64) error { return runner.ErrNotLower }
	resp, _ = postForm(t, s.Handler(), "/watch/meter/accept-lower", url.Values{"value": {"1234.5"}})
	if page := followFlash(t, s, resp); !strings.Contains(page, "wasn't accepted. It isn&#39;t lower than the counter&#39;s last value any more") {
		t.Errorf("a refused accept should say why:\n%s", page)
	}
	s.acceptOf = func(string, float64) error { return errors.New("disk full") }
	resp, _ = postForm(t, s.Handler(), "/watch/meter/accept-lower", url.Values{"value": {"x"}})
	if page := followFlash(t, s, resp); !strings.Contains(page, "That isn&#39;t a number.") {
		t.Errorf("a bad value should say so")
	}
}

// followFlash GETs the redirect target with the flash cookie resp set.
func followFlash(t *testing.T, s *Server, resp *http.Response) string {
	t.Helper()
	req := httptest.NewRequest("GET", resp.Header.Get("Location"), nil)
	for _, c := range resp.Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}

// V1.1 on the form: Type offers value; the Unit, Device class and Counter
// fields show for it; Counter and Max rise save; Test says what number a
// value watch would record.
func TestValueWatchForm(t *testing.T) {
	s, _ := newMeterServer(t)
	_, body := get(t, s.Handler(), "/watch/meter")
	for _, want := range []string{
		`<option value="value" selected`,
		`>value — records a number</option>`,
		`<fieldset id="fs-ha">`,
		`<fieldset id="fs-meter">`,
		`id="f-counter" name="counter" data-counter-classes="energy water gas volume" checked disabled`,
		`The energy class is always a counter.`,
		`<div class="field-row" id="row-maxrise">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("value watch page missing %q", want)
		}
	}
	form := url.Values{
		"ttype": {"value"}, "engine": {"sevenseg"}, "confirm": {"3"}, "interval": {"5s"},
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"unit": {"pulses"}, "device_class": {""}, "counter_shown": {"1"}, "counter": {"on"}, "max_rise": {"50"},
		"op": {"gt"}, "tthreshold": {"7"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/meter/save", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	w, _ := s.findWatch("meter")
	if !w.Counter || w.MaxRise != 50 || w.Unit != "pulses" || w.Trigger.Op != "" || w.Trigger.Threshold != 0 {
		t.Errorf("saved %+v", w)
	}
	// Max rise without Counter is refused under its field.
	form.Del("counter")
	resp, body = postForm(t, s.Handler(), "/watch/meter/save", form)
	if resp.StatusCode == http.StatusSeeOther || !strings.Contains(body, "Max rise only applies to a counter: tick Counter, or leave it empty.") {
		t.Errorf("max rise without counter: %d", resp.StatusCode)
	}
	if v := verdictFor(config.Trigger{Type: "value"}, "tesseract", "Total 0001234.5 kWh", false, nil); v.State != "met" || v.Title != "Value read" || !strings.HasPrefix(v.Detail, "The value is 1234.5.") {
		t.Errorf("Test verdict = %+v", v)
	}
	if v := verdictFor(config.Trigger{Type: "value"}, "sevenseg", "12?4.5", false, nil); v.State != "skipped" {
		t.Errorf("partial reading verdict = %+v", v)
	}
}
