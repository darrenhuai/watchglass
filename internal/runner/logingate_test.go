package runner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/demo"
	"github.com/darrenhuai/watchglass/internal/source"
)

// countingCamera answers status to every request until accept is set, then
// serves a frame.
func countingCamera(t *testing.T, status int) (*httptest.Server, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	var requests atomic.Int32
	var accept atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if accept.Load() {
			frame, _ := demo.FramePNG(demo.Printer, 0)
			w.Header().Set("Content-Type", "image/png")
			w.Write(frame)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(ts.Close)
	return ts, &requests, &accept
}

// B2: a watch whose camera turns the login down still goes down after
// health_after polls and says so once, while the polls inside the login
// wait cost the camera nothing. A Test that gets in (the password fixed on
// the camera) ends the wait, and the next poll recovers.
func TestRefusedLoginGoesDownOnceWithoutAskingEachPoll(t *testing.T) {
	ts, requests, accept := countingCamera(t, http.StatusUnauthorized)
	const pw = "pw-b2-runner"
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.Source = "http://admin:" + pw + "@" + strings.TrimPrefix(ts.URL, "http://") + "/snap.png"
	w.HealthAfter = 3
	src, err := source.For(w)
	if err != nil {
		t.Fatal(err)
	}
	notifier := &fakeNotifier{}
	r, err := New(w, src, nil, notifier, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := r.Tick(ctx); !errors.Is(err, source.ErrLoginRefused) {
			t.Fatalf("poll %d: err = %v, want the login refusal", i, err)
		}
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("10 polls asked the camera %d times, want 1", n)
	}
	if len(notifier.sent) != 1 || !strings.Contains(notifier.sent[0], "(down)") ||
		!strings.Contains(notifier.sent[0], "no reading for 3 consecutive polls") || !strings.Contains(notifier.sent[0], "status 401") {
		t.Fatalf("alerts = %q, want one down alert", notifier.sent)
	}
	if strings.Contains(strings.Join(notifier.sent, "\n"), pw) {
		t.Error("the down alert shows the password")
	}

	accept.Store(true)
	if _, err := src.Grab(source.Forced(ctx)); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if _, err := r.Tick(ctx); err != nil {
		t.Fatalf("poll after the Test: %v", err)
	}
	if len(notifier.sent) != 2 || !strings.Contains(notifier.sent[1], "(healthy)") {
		t.Errorf("alerts = %q, want down then recovered", notifier.sent)
	}
	if n := requests.Load(); n != 3 {
		t.Errorf("requests = %d, want 3 (the refusal, the Test, the poll)", n)
	}
}

// B2: a camera that is merely broken is asked on every poll, as before.
func TestBrokenCameraIsAskedEveryPoll(t *testing.T) {
	ts, requests, _ := countingCamera(t, http.StatusInternalServerError)
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.Source = "http://admin:pw@" + strings.TrimPrefix(ts.URL, "http://") + "/snap.png"
	w.HealthAfter = 3
	src, _ := source.For(w)
	notifier := &fakeNotifier{}
	r, err := New(w, src, nil, notifier, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := r.Tick(context.Background()); err == nil || errors.Is(err, source.ErrLoginRefused) {
			t.Fatalf("poll %d: err = %v", i, err)
		}
	}
	if n := requests.Load(); n != 6 {
		t.Errorf("6 polls asked %d times, want 6", n)
	}
	if len(notifier.sent) != 1 || !strings.Contains(notifier.sent[0], "status 500") {
		t.Errorf("alerts = %q, want one down alert", notifier.sent)
	}
}
