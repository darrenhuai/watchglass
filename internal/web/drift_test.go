package web

import (
	"context"
	"image"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	_, body = get(t, s.Handler(), "/")
	if !strings.Contains(body, `title="The picture around this box moved about 16 px right and 8 px up since 14:02. Readings may be wrong until the box is moved.">camera moved</span>`) {
		t.Errorf("dashboard lacks the moved marker; body:\n%s", body)
	}

	s.sup.Drift.Set("clock", drift.Status{State: drift.Lost, Since: movedStatus.Since})
	_, body = get(t, s.Handler(), "/watch/clock/live")
	if !strings.Contains(body, "hasn't been in view since") || strings.Contains(body, "move-box") || !strings.Contains(body, "The new picture is fine") {
		t.Errorf("lost: live fragment; body:\n%s", body)
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
