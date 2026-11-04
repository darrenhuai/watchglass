package web

import (
	"context"
	"image"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/drift"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/supervisor"
)

// stillSource never sends a frame, so the watch's own check never
// replaces the status a test puts on the board.
type stillSource struct{}

func (stillSource) Grab(ctx context.Context) (image.Image, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

const commentedConfig = `# my cameras
watches:
  - name: clock # the kitchen clock
    source: http://cam.invalid/snap.jpg
    interval: 2s
    # the digits
    region:
      x: 0.35 # left edge
      y: 0.4
      w: 0.34
      h: 0.15
    trigger:
      type: ocr_changed
    notify: []
`

func newMoveServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(commentedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	engines := ocr.Engines{Tesseract: fakeDetailed{}}
	reg := state.New(5)
	sup := supervisor.New(nil, reg, engines, t.Logf)
	sup.NewSource = func(w config.Watch) (source.Source, error) { return stillSource{}, nil }
	t.Cleanup(sup.StopAll)
	s, err := New(cfgPath, cfg, sup, reg, engines, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background(), cfg.Watches[0]); err != nil {
		t.Fatal(err)
	}
	return s, cfgPath
}

var movedStatus = drift.Status{State: drift.Moved, DX: 16, DY: -8, FrameW: 640, FrameH: 480,
	Since: time.Date(2026, 10, 28, 14, 2, 0, 0, time.UTC)}

func TestMovedWarningOnLiveAndDashboard(t *testing.T) {
	s, _ := newMoveServer(t)
	s.sup.Drift.Set("clock", movedStatus)
	_, body := get(t, s.Handler(), "/watch/clock/live")
	for _, want := range []string{
		`The picture around this box moved about 16 px right and 8 px up since <time class="mono" datetime="2026-10-28T14:02:00`,
		`. Readings may be wrong until the box is moved.`,
		`<form method="post" action="/watch/clock/move-box"><input type="hidden" name="dx" value="16"><input type="hidden" name="dy" value="-8"><button type="submit" class="btn btn-primary">Move the box with it</button></form>`,
		`<form method="post" action="/watch/clock/keep-box"><button type="submit" class="btn btn-outline">The new position is fine</button></form>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("live fragment lacks %q; body:\n%s", want, body)
		}
	}
	// The warning is its own block, ahead of .live-status: app.js swaps
	// .live-status on every reading, and the buttons lost their focus.
	warn, liveDrift, status := strings.Index(body, `class="drift-warn"`), strings.Index(body, `<div class="live-drift">`), strings.Index(body, `<div class="live-status"`)
	if liveDrift < 0 || !(liveDrift < warn && warn < status) {
		t.Errorf("the warning isn't in .live-drift ahead of .live-status (at %d, %d, %d); body:\n%s", liveDrift, warn, status, body)
	}
	_, body = get(t, s.Handler(), "/")
	if !strings.Contains(body, `title="The picture around this box moved about 16 px right and 8 px up since 14:02. Readings may be wrong until the box is moved.">camera moved</span>`) {
		t.Errorf("dashboard lacks the moved marker; body:\n%s", body)
	}

	s.sup.Drift.Set("clock", drift.Status{State: drift.Lost, Since: movedStatus.Since})
	_, body = get(t, s.Handler(), "/watch/clock/live")
	if !strings.Contains(body, "hasn't been in view since") || strings.Contains(body, "move-box") || !strings.Contains(body, "The new picture is fine") {
		t.Errorf("lost: live fragment; body:\n%s", body)
	}
	// A covered lens isn't a moved camera on the dashboard either.
	_, body = get(t, s.Handler(), "/")
	if !strings.Contains(body, `title="The picture around this box hasn&#39;t been in view since 14:02. Is the camera covered or pointed elsewhere? Readings may be wrong until it is back.">not in view</span>`) || strings.Contains(body, "camera moved") {
		t.Errorf("lost: dashboard marker; body:\n%s", body)
	}

	// Too plain to go by: a note on Live, no warning, no marker.
	s.sup.Drift.Set("clock", drift.Status{State: drift.Unusable})
	_, body = get(t, s.Handler(), "/watch/clock/live")
	if !strings.Contains(body, `<p class="drift-note">watchglass can't tell from the area around this box whether the camera moves: it is too plain, or changes too much.</p>`) || strings.Contains(body, "drift-warn") {
		t.Errorf("unusable: live fragment; body:\n%s", body)
	}
	if _, body = get(t, s.Handler(), "/"); strings.Contains(body, "drift-mark") {
		t.Errorf("unusable: dashboard has a marker; body:\n%s", body)
	}

	s.sup.Drift.Set("clock", drift.Status{State: drift.Steady})
	_, body = get(t, s.Handler(), "/watch/clock/live")
	if strings.Contains(body, "drift-") {
		t.Errorf("steady: live fragment says something about the position; body:\n%s", body)
	}
}

func TestMoveTheBoxWithItShiftsTheRegionAndKeepsComments(t *testing.T) {
	s, cfgPath := newMoveServer(t)
	s.sup.Drift.Set("clock", movedStatus)
	resp, body := postForm(t, s.Handler(), "/watch/clock/move-box", url.Values{"dx": {"16"}, "dy": {"-8"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d; body: %s", resp.StatusCode, body)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# my cameras", "# the kitchen clock", "# the digits", "x: 0.375 # left edge", "y: 0.3833"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config lacks %q:\n%s", want, raw)
		}
	}
	w, _ := s.findWatch("clock")
	if w.Region != (config.Region{X: 0.375, Y: 0.3833, W: 0.34, H: 0.15}) {
		t.Errorf("region = %+v", w.Region)
	}
	if got := s.sup.Running(); len(got) != 1 {
		t.Errorf("running = %v, want the watch restarted", got)
	}
	// The restarted watch takes a new reference: its check is learning.
	if st, _ := s.sup.Drift.Status("clock"); st.State != drift.Learning {
		t.Errorf("board = %+v, want learning", st)
	}
	_, page := getWithCookies(t, s.Handler(), "/watch/clock", resp.Cookies())
	if !strings.Contains(page, "Moved the box 16 px right and 8 px up, saved it to config.yaml and restarted the watch.") {
		t.Errorf("no confirmation on the page:\n%s", page)
	}
}

// The check refines the offset while the picture stays moved: a page
// that showed 15, -8 when the check now says 16, -8 is the same move, and
// the box moves by the check's unrounded offset.
func TestMoveTheBoxUsesTheRefinedOffset(t *testing.T) {
	s, _ := newMoveServer(t)
	st := movedStatus
	st.FX, st.FY = 16.4, -7.7
	s.sup.Drift.Set("clock", st)
	resp, body := postForm(t, s.Handler(), "/watch/clock/move-box", url.Values{"dx": {"15"}, "dy": {"-8"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d; body: %s", resp.StatusCode, body)
	}
	// 0.35 + 16.4/640 and 0.4 - 7.7/480, to 4 places.
	if w, _ := s.findWatch("clock"); w.Region != (config.Region{X: 0.3756, Y: 0.384, W: 0.34, H: 0.15}) {
		t.Errorf("region = %+v", w.Region)
	}
}

// "Move the box with it" pressed while a Save is being applied: it waits
// for the Save, then reads the check's status, which the Save's restart
// set to learning (a new box), and moves nothing. It used to read the
// status before waiting, and then moved the Save's new box by the old
// box's offset.
func TestMoveTheBoxWaitsForASaveInProgress(t *testing.T) {
	s, cfgPath := newMoveServer(t)
	before, _ := os.ReadFile(cfgPath)
	s.sup.Drift.Set("clock", movedStatus)
	s.applyMu.Lock() // a Save being applied
	done := make(chan *http.Response, 1)
	go func() {
		resp, _ := postForm(t, s.Handler(), "/watch/clock/move-box", url.Values{"dx": {"16"}, "dy": {"-8"}})
		done <- resp
	}()
	// Wait for the request to be waiting on the Save: its goroutine is
	// in moveBox, blocked on a mutex.
	blocked := false
	for deadline := time.Now().Add(10 * time.Second); !blocked && time.Now().Before(deadline); {
		runtime.Gosched()
		buf := make([]byte, 1<<20)
		for _, g := range strings.Split(string(buf[:runtime.Stack(buf, true)]), "\n\n") {
			if strings.Contains(g, "(*Server).moveBox") && strings.Contains(g, "sync.Mutex.Lock") {
				blocked = true
			}
		}
	}
	// The Save restarted the watch with a new box: its check is learning.
	s.sup.Drift.Set("clock", drift.Status{State: drift.Learning})
	s.applyMu.Unlock()
	if !blocked {
		t.Fatal("move-box never waited for the Save")
	}
	resp := <-done
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Errorf("the box was moved by an offset from before the Save:\n%s", after)
	}
	if _, page := getWithCookies(t, s.Handler(), "/watch/clock", resp.Cookies()); strings.Contains(page, "Moved the box") {
		t.Errorf("the page says the box was moved")
	}
}

func TestMoveTheBoxRefusesAnOffsetThePageDidntShow(t *testing.T) {
	s, cfgPath := newMoveServer(t)
	before, _ := os.ReadFile(cfgPath)
	s.sup.Drift.Set("clock", movedStatus)
	resp, _ := postForm(t, s.Handler(), "/watch/clock/move-box", url.Values{"dx": {"10"}, "dy": {"-8"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if _, page := getWithCookies(t, s.Handler(), "/watch/clock", resp.Cookies()); !strings.Contains(page, "The picture moved again after this page showed it, so the box wasn't moved.") {
		t.Errorf("no explanation on the page")
	}
	// Not moved at all: nothing to move.
	s.sup.Drift.Set("clock", drift.Status{State: drift.Steady})
	postForm(t, s.Handler(), "/watch/clock/move-box", url.Values{"dx": {"16"}, "dy": {"-8"}})
	after, _ := os.ReadFile(cfgPath)
	if string(after) != string(before) {
		t.Errorf("config changed:\n%s", after)
	}
}

func TestNewPositionIsFineRetakesWithoutMoving(t *testing.T) {
	s, cfgPath := newMoveServer(t)
	before, _ := os.ReadFile(cfgPath)
	s.sup.Drift.Set("clock", movedStatus)
	resp, _ := postForm(t, s.Handler(), "/watch/clock/keep-box", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	after, _ := os.ReadFile(cfgPath)
	if string(after) != string(before) {
		t.Errorf("config changed:\n%s", after)
	}
	if st, _ := s.sup.Drift.Status("clock"); st.State != drift.Learning {
		t.Errorf("board = %+v, want learning (a new reference from the next frame)", st)
	}
	if _, page := getWithCookies(t, s.Handler(), "/watch/clock", resp.Cookies()); !strings.Contains(page, "The box stays where it is.") {
		t.Errorf("no confirmation on the page")
	}
}

func TestMoveActionsRefuseCrossSitePOSTs(t *testing.T) {
	s, cfgPath := newMoveServer(t)
	before, _ := os.ReadFile(cfgPath)
	s.sup.Drift.Set("clock", movedStatus)
	for _, path := range []string{"/watch/clock/move-box", "/watch/clock/keep-box"} {
		resp, _ := postFormWithHeaders(t, s.Handler(), path, url.Values{"dx": {"16"}, "dy": {"-8"}},
			map[string]string{"Sec-Fetch-Site": "cross-site"})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s cross-site: status = %d, want 403", path, resp.StatusCode)
		}
	}
	after, _ := os.ReadFile(cfgPath)
	if string(after) != string(before) {
		t.Errorf("config changed:\n%s", after)
	}
	if st, _ := s.sup.Drift.Status("clock"); st.State != drift.Moved {
		t.Errorf("board = %+v, want still moved", st)
	}
}

// A move alert's delivery line says what the alert was about and what
// lets it go, on Live and on the list's marker, rather than reading as a
// fire alert's.
func TestMoveAlertDeliveryLineSaysItIsAboutTheCamera(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	s.sup.NewSource = func(config.Watch) (source.Source, error) { return blockSource{}, nil }
	if resp, body := postForm(t, h, "/watch/printer/save", saveForm("generic+http://h/hook")); resp.StatusCode != 303 {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	esc := strings.NewReplacer("'", "&#39;", "+", "&#43;").Replace
	t0 := time.Date(2026, 9, 24, 17, 50, 49, 0, time.Local)
	at := `<time class="mono" datetime="` + isoTime(t0) + `">17:50:49</time>`
	s.reg.Add("printer", state.Sample{TS: t0.Add(-time.Second), Reading: "Printing 87%"})

	s.reg.SetDelivery("printer", state.Delivery{TS: t0, Kind: "moved", Tries: 1, NextTry: time.Now().Add(30 * time.Second),
		Retrying: "line 1 of 1 (generic+http://h): HTTP 503"})
	_, live := get(t, h, "/watch/printer/live")
	if want := esc(`The alert (camera moved or covered) from ` + at + ` is kept until it gets through or the picture around the box is back or the box is moved or kept (1 try so far).`); !strings.Contains(live, want) {
		t.Errorf("retrying: live lacks %q:\n%s", want, live)
	}
	if _, index := get(t, h, "/"); !strings.Contains(index, "The alert (camera moved or covered) from 17:50:49 is not delivered yet") {
		t.Errorf("retrying: index marker doesn't name the move alert:\n%s", index)
	}

	s.reg.SetDelivery("printer", state.Delivery{TS: t0, Kind: "moved", Err: "line 1 of 1 (generic+http://h): HTTP 503"})
	if _, live = get(t, h, "/watch/printer/live"); !strings.Contains(live, `Last alert (camera moved or covered) at `+at+` could not be delivered`) {
		t.Errorf("failed: live:\n%s", live)
	}

	s.reg.SetDelivery("printer", state.Delivery{TS: t0, Kind: "moved", OK: true, Tries: 1})
	if _, live = get(t, h, "/watch/printer/live"); !strings.Contains(live, `Last alert (camera moved or covered) sent at `+at+`.`) {
		t.Errorf("sent: live:\n%s", live)
	}
}

// A deleted watch leaves nothing on the board: a new watch of the same
// name doesn't start with its warning.
func TestDeleteForgetsThePosition(t *testing.T) {
	s, _ := newMoveServer(t)
	s.sup.Drift.Set("clock", movedStatus)
	if resp, body := postForm(t, s.Handler(), "/watch/clock/delete", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	if st, ok := s.sup.Drift.Status("clock"); ok {
		t.Errorf("board still has %+v for the deleted watch", st)
	}
}
