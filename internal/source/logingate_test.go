package source

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/demo"
)

// fakeClock stands in for the login gate's clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func gateClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	logins.mu.Lock()
	old := logins.now
	logins.now = c.now
	logins.mu.Unlock()
	t.Cleanup(func() {
		logins.mu.Lock()
		logins.now = old
		logins.mu.Unlock()
	})
	return c
}

// refusingCamera turns every request down the way mode says until accept
// is set, then serves a frame. It counts requests per password tried.
type refusingCamera struct {
	*httptest.Server
	accept   atomic.Bool
	mu       sync.Mutex
	requests int
	byPass   map[string]int
}

func newRefusingCamera(t *testing.T, mode string) *refusingCamera {
	t.Helper()
	c := &refusingCamera{byPass: map[string]int{}}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, _ := r.BasicAuth()
		c.mu.Lock()
		c.requests++
		c.byPass[pass]++
		c.mu.Unlock()
		if c.accept.Load() {
			frame, _ := demo.FramePNG(demo.Printer, 0)
			w.Header().Set("Content-Type", "image/png")
			w.Write(frame)
			return
		}
		switch mode {
		case "forbid":
			w.WriteHeader(http.StatusForbidden)
		case "digest":
			// A challenge it then refuses whatever the answer.
			w.Header().Set("WWW-Authenticate", `Digest realm="cam", qop="auth", nonce="n-b2", opaque="o"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.Header().Set("WWW-Authenticate", `Basic realm="cam"`)
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *refusingCamera) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

// B2: a refused login is not tried again until its wait is over. Each grab
// inside the wait fails at once with the refusal, without a request; the
// wait grows with each refusal in a row and stays at the last step; an
// accepted login ends it for good.
func TestRefusedLoginWaitsBeforeAskingAgain(t *testing.T) {
	cases := []struct {
		mode    string
		status  int
		perAsk  int // requests one ask costs the camera
		firstAs int // the first ask (Basic, then the Digest answer)
	}{
		{"basic", 401, 1, 1},
		{"digest", 401, 1, 2},
		{"forbid", 403, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			clock := gateClock(t)
			cam := newRefusingCamera(t, c.mode)
			const pw = "hunter2-b2"
			target := withCreds(cam.URL, "admin", pw) + "/snap.jpg"
			shown := strings.Replace(target, pw, "xxxxx", 1)
			wantText := fmt.Sprintf("snapshot %s: status %d", shown, c.status)

			_, err := NewHTTPSnapshot(target).Grab(context.Background())
			var lre *LoginRefusedError
			if !errors.Is(err, ErrLoginRefused) || !errors.As(err, &lre) || lre.Status != c.status || lre.Skipped {
				t.Fatalf("first grab: err = %#v, want a LoginRefusedError with status %d", err, c.status)
			}
			// The text the UI already knows (errtext.go's loginRefused).
			if err.Error() != wantText {
				t.Errorf("first grab says %q, want %q", err, wantText)
			}
			if got := cam.count(); got != c.firstAs {
				t.Fatalf("first grab made %d requests, want %d", got, c.firstAs)
			}

			want := c.firstAs
			for step, wait := range []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute,
				5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
				// The poll loop, the snapshot and its refresh, from new
				// sources each time, all inside the wait: nothing is sent.
				for i := 0; i < 5; i++ {
					clock.add(wait / 6)
					_, err := NewHTTPSnapshot(target).Grab(context.Background())
					if !errors.As(err, &lre) || !lre.Skipped || lre.Status != c.status {
						t.Fatalf("step %d grab %d inside the wait: err = %v, want the remembered refusal", step, i, err)
					}
					if !strings.HasPrefix(err.Error(), wantText+" (not asked: ") {
						t.Errorf("step %d: skipped grab says %q", step, err)
					}
					if strings.Contains(err.Error(), pw) {
						t.Fatalf("the error shows the password: %v", err)
					}
					// errtext.go reads "refused" as a refused connection.
					if strings.Contains(strings.ToLower(err.Error()), "refused") {
						t.Fatalf("a skipped grab reads like a refused connection: %v", err)
					}
				}
				if got := cam.count(); got != want {
					t.Fatalf("step %d: %d requests inside a %v wait, want none (total %d)", step, got-want, wait, want)
				}
				if left, ok := LoginRetryIn(shown); !ok || left != wait-5*(wait/6) {
					t.Errorf("step %d: LoginRetryIn = %v, %v, want %v", step, left, ok, wait-5*(wait/6))
				}
				clock.add(wait - 5*(wait/6))
				if _, err := NewHTTPSnapshot(target).Grab(context.Background()); !errors.As(err, &lre) || lre.Skipped {
					t.Fatalf("step %d: the grab after the %v wait: err = %v, want a real refusal", step, wait, err)
				}
				want += c.perAsk
				if got := cam.count(); got != want {
					t.Fatalf("step %d: the grab after the %v wait made %d requests, want %d", step, wait, got-(want-c.perAsk), c.perAsk)
				}
			}

			// The camera takes the login now: the next grab after the wait
			// gets the frame and the wait is gone.
			cam.accept.Store(true)
			clock.add(30 * time.Minute)
			if _, err := NewHTTPSnapshot(target).Grab(context.Background()); err != nil {
				t.Fatalf("accepted: %v", err)
			}
			if _, ok := LoginRetryIn(shown); ok {
				t.Error("an accepted login left a wait behind")
			}
			// Refused again later: asked at once, and the wait starts over
			// at its first step.
			cam.accept.Store(false)
			before := cam.count()
			if _, err := NewHTTPSnapshot(target).Grab(context.Background()); !errors.As(err, &lre) || lre.Skipped {
				t.Fatalf("after an accepted login: err = %v, want a real refusal", err)
			}
			if cam.count() == before {
				t.Fatal("after an accepted login the camera wasn't asked")
			}
			if left, ok := LoginRetryIn(shown); !ok || left != 10*time.Second {
				t.Errorf("wait after a fresh refusal = %v, %v, want 10s", left, ok)
			}
		})
	}
}

// B2: the wait belongs to the login. A different password (the user fixed
// the URL), user or camera is asked at once; the same login from another
// source is not.
func TestLoginWaitIsPerLogin(t *testing.T) {
	gateClock(t)
	cam := newRefusingCamera(t, "basic")
	other := newRefusingCamera(t, "basic")
	wrong := withCreds(cam.URL, "admin", "wrong-b2") + "/snap.jpg"
	if _, err := NewHTTPSnapshot(wrong).Grab(context.Background()); !errors.Is(err, ErrLoginRefused) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewHTTPSnapshot(wrong).Grab(context.Background()); !errors.Is(err, ErrLoginRefused) {
		t.Fatalf("err = %v", err)
	}
	if n := cam.byPass["wrong-b2"]; n != 1 {
		t.Fatalf("wrong password asked %d times, want 1", n)
	}
	// Another path on the same camera with the same login waits too: it is
	// the account that would be locked.
	if _, err := NewHTTPSnapshot(withCreds(cam.URL, "admin", "wrong-b2") + "/other.jpg").Grab(context.Background()); !errors.Is(err, ErrLoginRefused) {
		t.Fatalf("err = %v", err)
	}
	if n := cam.byPass["wrong-b2"]; n != 1 {
		t.Errorf("same login on another path asked the camera (%d)", n)
	}

	// Fixed password: tried at once and taken.
	cam.accept.Store(true)
	if _, err := NewHTTPSnapshot(withCreds(cam.URL, "admin", "right-b2") + "/snap.jpg").Grab(context.Background()); err != nil {
		t.Fatalf("fixed password: %v", err)
	}
	cam.accept.Store(false)
	// Another user, and the same login on another camera: asked at once.
	for _, target := range []string{withCreds(cam.URL, "viewer", "wrong-b2") + "/snap.jpg", withCreds(other.URL, "admin", "wrong-b2") + "/snap.jpg"} {
		before := cam.count() + other.count()
		NewHTTPSnapshot(target).Grab(context.Background()) //nolint:errcheck // refused; the count is what matters
		if cam.count()+other.count() != before+1 {
			t.Errorf("%s was not asked", strings.Replace(target, "wrong-b2", "xxxxx", 1))
		}
	}
	// A header that carries a key is part of the login too.
	keyed := &HTTPSnapshot{URL: cam.URL + "/snap.jpg", Client: &http.Client{}, Headers: http.Header{"X-Api-Key": {"k1"}}}
	keyed.Grab(context.Background()) //nolint:errcheck
	before := cam.count()
	keyed.Grab(context.Background()) //nolint:errcheck
	if cam.count() != before {
		t.Error("same key inside the wait asked the camera")
	}
	keyed.Headers = http.Header{"X-Api-Key": {"k2"}}
	keyed.Grab(context.Background()) //nolint:errcheck
	if cam.count() != before+1 {
		t.Error("a new key wasn't tried at once")
	}
}

// B2: the page names the camera and user, not the password. After a second
// wrong password replaces the first, the wait it shows is the new one's.
func TestRetryInFollowsTheLatestPassword(t *testing.T) {
	clock := gateClock(t)
	cam := newRefusingCamera(t, "basic")
	first := withCreds(cam.URL, "admin", "typo-1") + "/snap.jpg"
	for _, step := range []time.Duration{10 * time.Second, 30 * time.Second, 0} {
		NewHTTPSnapshot(first).Grab(context.Background()) //nolint:errcheck
		clock.add(step)
	}
	shown := withCreds(cam.URL, "admin", "xxxxx") + "/snap.jpg"
	if left, _ := LoginRetryIn(shown); left != 2*time.Minute {
		t.Fatalf("first password's wait = %v, want 2m", left)
	}
	clock.add(time.Second)
	NewHTTPSnapshot(withCreds(cam.URL, "admin", "typo-2") + "/snap.jpg").Grab(context.Background()) //nolint:errcheck
	if left, ok := LoginRetryIn(shown); !ok || left != 10*time.Second {
		t.Errorf("after a new wrong password the page would say %v, %v, want its 10s", left, ok)
	}
	if cam.byPass["typo-2"] != 1 {
		t.Errorf("the new password was asked %d times", cam.byPass["typo-2"])
	}
}

// B2: a 403 for one path (a channel this user may not see) waits for that
// path only; the same login keeps working on the others.
func TestForbiddenPathDoesNotBlockTheOthers(t *testing.T) {
	gateClock(t)
	var allowed, denied atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ch2.jpg" {
			denied.Add(1)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		allowed.Add(1)
		frame, _ := demo.FramePNG(demo.Printer, 0)
		w.Write(frame)
	}))
	defer ts.Close()
	base := withCreds(ts.URL, "admin", "pw-b2")
	for i := 0; i < 3; i++ {
		if _, err := NewHTTPSnapshot(base + "/ch2.jpg").Grab(context.Background()); !errors.Is(err, ErrLoginRefused) {
			t.Fatalf("ch2: %v", err)
		}
		if _, err := NewHTTPSnapshot(base + "/ch1.jpg").Grab(context.Background()); err != nil {
			t.Fatalf("ch1 after ch2's 403: %v", err)
		}
	}
	if denied.Load() != 1 || allowed.Load() != 3 {
		t.Errorf("ch2 asked %d times (want 1), ch1 %d (want 3)", denied.Load(), allowed.Load())
	}
}

// B2: Test this region is a person asking. It asks the camera inside the
// wait; a refusal doesn't make the wait longer, and an accepted login ends
// it for the poll loop too.
func TestForcedGrabAsksInsideTheWait(t *testing.T) {
	clock := gateClock(t)
	cam := newRefusingCamera(t, "basic")
	target := withCreds(cam.URL, "admin", "pw-b2") + "/snap.jpg"
	shown := strings.Replace(target, "pw-b2", "xxxxx", 1)
	for i := 0; i < 2; i++ { // two refusals: a 30 s wait
		NewHTTPSnapshot(target).Grab(context.Background()) //nolint:errcheck
		clock.add(10 * time.Second)
	}
	clock.add(-10 * time.Second)
	if n := cam.count(); n != 2 {
		t.Fatalf("setup made %d requests", n)
	}
	clock.add(5 * time.Second)
	_, err := NewHTTPSnapshot(target).Grab(Forced(context.Background()))
	var lre *LoginRefusedError
	if !errors.As(err, &lre) || lre.Skipped || cam.count() != 3 {
		t.Fatalf("forced grab inside the wait: err = %v, requests = %d, want a real refusal and a request", err, cam.count())
	}
	if lre.Refusals != 2 {
		t.Errorf("a refusal inside the wait counted: %d in a row, want 2", lre.Refusals)
	}
	if left, _ := LoginRetryIn(shown); left != 30*time.Second {
		t.Errorf("wait after a refused Test = %v, want the 30 s step from now", left)
	}
	cam.accept.Store(true)
	clock.add(time.Second)
	if _, err := NewHTTPSnapshot(target).Grab(Forced(context.Background())); err != nil {
		t.Fatalf("forced grab once the camera takes the login: %v", err)
	}
	// The poll loop's next grab asks at once.
	if _, err := NewHTTPSnapshot(target).Grab(context.Background()); err != nil {
		t.Fatalf("poll after a successful Test: %v", err)
	}
	if cam.count() != 5 {
		t.Errorf("requests = %d, want 5", cam.count())
	}
}

// B2: after the wait, one grab asks; the others that arrive while it is out
// keep failing with the refusal instead of piling onto the camera.
func TestOnlyOneGrabAsksAfterTheWait(t *testing.T) {
	clock := gateClock(t)
	release := make(chan struct{})
	var requests atomic.Int32
	var holding atomic.Bool
	arrived := make(chan struct{}, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if holding.Load() {
			arrived <- struct{}{}
			<-release
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()
	target := withCreds(ts.URL, "admin", "pw-b2") + "/snap.jpg"
	NewHTTPSnapshot(target).Grab(context.Background()) //nolint:errcheck
	clock.add(10 * time.Second)
	holding.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := NewHTTPSnapshot(target).Grab(context.Background())
		done <- err
	}()
	<-arrived
	for i := 0; i < 3; i++ {
		_, err := NewHTTPSnapshot(target).Grab(context.Background())
		var lre *LoginRefusedError
		if !errors.As(err, &lre) || !lre.Skipped {
			t.Errorf("grab while another is asking: err = %v, want the refusal", err)
		}
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrLoginRefused) {
		t.Errorf("asking grab: %v", err)
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	if left, _ := LoginRetryIn(strings.Replace(target, "pw-b2", "xxxxx", 1)); left != 30*time.Second {
		t.Errorf("wait = %v, want 30s", left)
	}
}

// B2: a camera that is merely down is polled as before: every grab asks.
func TestCameraThatIsDownIsNotWaitedFor(t *testing.T) {
	gateClock(t)
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	target := withCreds(ts.URL, "admin", "pw-b2") + "/snap.jpg"
	for i := 0; i < 5; i++ {
		_, err := NewHTTPSnapshot(target).Grab(context.Background())
		if err == nil || errors.Is(err, ErrLoginRefused) || !strings.Contains(err.Error(), "status 500") {
			t.Fatalf("grab %d: err = %v, want status 500", i, err)
		}
	}
	if n := requests.Load(); n != 5 {
		t.Errorf("500: %d requests over 5 grabs, want 5", n)
	}
	// Nothing listening at all.
	ts.Close()
	for i := 0; i < 3; i++ {
		_, err := NewHTTPSnapshot(target).Grab(context.Background())
		if err == nil || errors.Is(err, ErrLoginRefused) {
			t.Fatalf("closed: err = %v", err)
		}
	}
	if _, ok := LoginRetryIn(ts.URL); ok {
		t.Error("a camera that is down got a login wait")
	}
}

// B2: an RTSP camera that turns the login down (ffmpeg's own words) waits
// the same way, and ffmpeg's errors don't show the password it prints.
func TestFFmpegRefusedLoginWaits(t *testing.T) {
	clock := gateClock(t)
	const pw = "s3cret-b2"
	input := "rtsp://admin:" + pw + "@cam-b2.invalid:554/Streaming/101"
	runs := 0
	refuse := func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		runs++
		// What ffmpeg 8 prints for a refused DESCRIBE.
		return nil, errors.New("exit status 0xcecfcb08: [in#0 @ 000001f693b494c0] method DESCRIBE failed: 401 (Unauthorized)\n" +
			"[in#0 @ 000001f693ae4f00] Error opening input: Server returned 401 Unauthorized (authorization failed)\n" +
			"Error opening input file " + input + ".\n" +
			"Error opening input files: Server returned 401 Unauthorized (authorization failed)")
	}
	grab := func(ctx context.Context, in string, run ffmpegRunFunc) error {
		f, err := NewFFmpeg(in)
		if err != nil {
			t.Fatal(err)
		}
		f.run = run
		_, err = f.Grab(ctx)
		return err
	}
	err := grab(context.Background(), input, refuse)
	var lre *LoginRefusedError
	if !errors.As(err, &lre) || lre.Status != 401 || lre.Skipped || runs != 1 {
		t.Fatalf("first grab: err = %v, runs = %d", err, runs)
	}
	if strings.Contains(err.Error(), pw) || !strings.Contains(err.Error(), "rtsp://admin:xxxxx@cam-b2.invalid:554/Streaming/101") ||
		!strings.Contains(err.Error(), "401 Unauthorized") {
		t.Errorf("first grab says %q", err)
	}
	for i := 0; i < 5; i++ {
		clock.add(time.Second)
		err := grab(context.Background(), input, refuse)
		if !errors.As(err, &lre) || !lre.Skipped || strings.Contains(err.Error(), pw) ||
			!strings.Contains(err.Error(), "ffmpeg: rtsp://admin:xxxxx@cam-b2.invalid:554/Streaming/101: 401 Unauthorized (not asked") {
			t.Fatalf("grab inside the wait: %v", err)
		}
	}
	if runs != 1 {
		t.Fatalf("ffmpeg ran %d times inside the wait", runs-1)
	}
	if left, ok := LoginRetryIn("rtsp://admin:xxxxx@cam-b2.invalid:554/Streaming/101"); !ok || left != 5*time.Second {
		t.Errorf("LoginRetryIn = %v, %v", left, ok)
	}
	clock.add(5 * time.Second)
	grab(context.Background(), input, refuse) //nolint:errcheck
	if runs != 2 {
		t.Fatalf("after the wait ffmpeg ran %d times, want once", runs-1)
	}
	// Test this region asks anyway.
	grab(Forced(context.Background()), input, refuse) //nolint:errcheck
	if runs != 3 {
		t.Errorf("a forced grab didn't run ffmpeg")
	}
	// A fixed password runs at once, and its frame clears nothing else's
	// wait but its own.
	ok := func(ctx context.Context, bin string, args ...string) ([]byte, error) { return pngOf(t, 4, 4), nil }
	if err := grab(context.Background(), "rtsp://admin:fixed-b2@cam-b2.invalid:554/Streaming/101", ok); err != nil {
		t.Fatalf("fixed password: %v", err)
	}
	if _, waiting := LoginRetryIn("rtsp://admin:xxxxx@cam-b2.invalid:554/Streaming/101"); !waiting {
		t.Error("the wrong password's wait is gone")
	}

	// 403 the same way.
	forbidden := 0
	forbid := func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		forbidden++
		return nil, errors.New("exit status 1: rtsp://cam-b2.invalid/x: Server returned 403 Forbidden (access denied)")
	}
	for i := 0; i < 3; i++ {
		err := grab(context.Background(), "rtsp://cam-b2.invalid/x", forbid)
		if !errors.As(err, &lre) || lre.Status != 403 {
			t.Fatalf("403: err = %v", err)
		}
	}
	if forbidden != 1 {
		t.Errorf("403: ffmpeg ran %d times over 3 grabs, want 1", forbidden)
	}
}

// B2: ffmpeg failing for any other reason runs every time, as before, and
// its error doesn't show the password either; a device has no login.
func TestFFmpegOtherFailuresAreNotWaitedFor(t *testing.T) {
	gateClock(t)
	const pw = "s3cret-b2"
	runs := 0
	f, err := NewFFmpeg("rtsp://admin:" + pw + "@cam-b2-down.invalid/live")
	if err != nil {
		t.Fatal(err)
	}
	f.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		runs++
		return nil, fmt.Errorf("exit status 1: [tcp @ 0000] Connection to tcp://cam-b2-down.invalid:554 failed: Connection refused\n"+
			"Error opening input file rtsp://admin:%s@cam-b2-down.invalid/live.: %w", pw, context.DeadlineExceeded)
	}
	for i := 0; i < 4; i++ {
		_, err := f.Grab(context.Background())
		if err == nil || errors.Is(err, ErrLoginRefused) || strings.Contains(err.Error(), pw) {
			t.Fatalf("grab %d: err = %v", i, err)
		}
		if !strings.Contains(err.Error(), "rtsp://admin:xxxxx@cam-b2-down.invalid/live") || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("grab %d: err = %v, want the URL masked and the cause kept", i, err)
		}
	}
	if runs != 4 {
		t.Errorf("ffmpeg ran %d times over 4 grabs, want 4", runs)
	}
	dev, _ := NewFFmpeg("v4l2:/dev/video-b2")
	if dev.camera != nil {
		t.Errorf("a capture device got a login wait key: %v", dev.camera)
	}
}

// B2: a gate key never carries the password, only a keyed fingerprint of
// it, so debug output of the map can't leak one.
func TestGateKeysHideThePassword(t *testing.T) {
	const pw = "hunter2"
	k1, p1, label, ok := gateKeys(mustURL(t, "http://admin:"+pw+"@cam.local/snap.jpg"), nil)
	k2, _, _, _ := gateKeys(mustURL(t, "http://admin:other@cam.local/snap.jpg"), nil)
	k3, _, _, _ := gateKeys(mustURL(t, "http://admin:"+pw+"@cam.local/other.jpg"), nil)
	if !ok || strings.Contains(k1+p1+label, pw) || label != "http://admin@cam.local" {
		t.Errorf("keys = %q %q %q", k1, p1, label)
	}
	if k1 == k2 {
		t.Error("two passwords share a key")
	}
	if k1 != k3 {
		t.Error("one login on two paths got two keys")
	}
	if got := RedactText("x rtsp://u:p%40ss@h/s and http://a:b@c/d", "rtsp://u:p%40ss@h/s"); got != "x rtsp://u:xxxxx@h/s and http://a:xxxxx@c/d" {
		t.Errorf("RedactText = %q", got)
	}
}

// B2 fixer: a camera that takes the login in the query string (Reolink's
// ?user=&password=) is a login too. The password is masked in the grab's
// error, in the skipped grab's text and in ffmpeg's output, and the wait
// is found by the URL as the message shows it.
func TestQueryStringPasswordIsMasked(t *testing.T) {
	for in, want := range map[string]string{
		"http://cam/cgi-bin/api.cgi?cmd=Snap&channel=0&user=admin&password=s3cret": "http://cam/cgi-bin/api.cgi?cmd=Snap&channel=0&user=admin&password=xxxxx",
		"http://cam/cgi?usr=admin&PWD=s3cret&x=1":                                  "http://cam/cgi?usr=admin&PWD=xxxxx&x=1",
		"http://u:p@cam/snap.jpg?pass=s3cret":                                      "http://u:xxxxx@cam/snap.jpg?pass=xxxxx",
		"rtsp://cam:554/live?token=abc123":                                         "rtsp://cam:554/live?token=xxxxx",
		"http://cam/snap.jpg?channel=1&password":                                   "http://cam/snap.jpg?channel=1&password",
		"http://cam/snap.jpg?channel=1":                                            "http://cam/snap.jpg?channel=1",
		"http://user:pw@cam/snap.jpg":                                              "http://user:xxxxx@cam/snap.jpg",
	} {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
	for raw, want := range map[string]bool{
		"http://cam/snap.jpg":                      false,
		"http://admin@cam/snap.jpg":                true,
		"http://cam/cgi?user=admin&password=xxxxx": true,
		"http://cam/cgi?channel=1&Password=x":      true,
		"http://cam/cgi?channel=1":                 false,
	} {
		if got := LoginInURL(mustURL(t, raw)); got != want {
			t.Errorf("LoginInURL(%q) = %v, want %v", raw, got, want)
		}
	}
	if got := RedactText("Error opening input file http://cam/x?user=a&password=s3cret&ch=1."); got != "Error opening input file http://cam/x?user=a&password=xxxxx&ch=1." {
		t.Errorf("RedactText = %q", got)
	}

	gateClock(t)
	cam := newRefusingCamera(t, "basic")
	const pw = "QSECRET-b2"
	target := cam.URL + "/cgi-bin/api.cgi?cmd=Snap&channel=0&user=admin&password=" + pw
	shown := strings.Replace(target, pw, "xxxxx", 1)
	_, err := NewHTTPSnapshot(target).Grab(context.Background())
	if !errors.Is(err, ErrLoginRefused) || err.Error() != "snapshot "+shown+": status 401" {
		t.Fatalf("first grab: err = %v, want the refusal with the password masked", err)
	}
	_, err = NewHTTPSnapshot(target).Grab(context.Background())
	var lre *LoginRefusedError
	if !errors.As(err, &lre) || !lre.Skipped || strings.Contains(err.Error(), pw) {
		t.Fatalf("grab inside the wait: err = %v", err)
	}
	if left, ok := LoginRetryIn(shown); !ok || left != 10*time.Second {
		t.Errorf("LoginRetryIn by the masked URL = %v, %v, want 10s", left, ok)
	}

	// ffmpeg reading an MJPEG URL with the same style of login prints it
	// whole in its errors.
	f, err := NewFFmpeg("ffmpeg:-i " + target)
	if err != nil {
		t.Fatal(err)
	}
	f.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return nil, errors.New("exit status 1: Error opening input file " + target + ".: Connection refused")
	}
	if _, err := f.Grab(context.Background()); err == nil || strings.Contains(err.Error(), pw) || !strings.Contains(err.Error(), shown) {
		t.Errorf("ffmpeg error = %v, want the query password masked", err)
	}
}

// B2 fixer: after the wait, the one grab that asks may not reach the
// camera at all (it is off, or the connection drops). That says nothing
// about the login, so the ask is handed back: the next grab asks, and a
// camera that is back and takes the login is taken at once instead of
// every grab failing with the old refusal until someone presses Test.
func TestProbeThatCannotReachTheCameraHandsTheGateBack(t *testing.T) {
	clock := gateClock(t)
	var drop, accept atomic.Bool
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case drop.Load():
			// The camera goes away mid-request: a transport error, no
			// status at all.
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
		case accept.Load():
			frame, _ := demo.FramePNG(demo.Printer, 0)
			w.Header().Set("Content-Type", "image/png")
			w.Write(frame)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer ts.Close()
	target := withCreds(ts.URL, "admin", "pw-b2") + "/snap.jpg"
	shown := withCreds(ts.URL, "admin", "xxxxx") + "/snap.jpg"
	if _, err := NewHTTPSnapshot(target).Grab(context.Background()); !errors.Is(err, ErrLoginRefused) {
		t.Fatalf("first grab: %v", err)
	}
	clock.add(10 * time.Second)
	drop.Store(true)
	if _, err := NewHTTPSnapshot(target).Grab(context.Background()); err == nil || errors.Is(err, ErrLoginRefused) {
		t.Fatalf("the ask with the camera gone: err = %v, want a transport error", err)
	}
	if left, ok := LoginRetryIn(shown); !ok || left != 0 {
		t.Errorf("after the failed ask LoginRetryIn = %v, %v, want 0 (the next grab asks)", left, ok)
	}
	drop.Store(false)
	accept.Store(true)
	clock.add(time.Second)
	before := requests.Load()
	if _, err := NewHTTPSnapshot(target).Grab(context.Background()); err != nil {
		t.Fatalf("camera back and taking the login: %v (the ask was never handed back)", err)
	}
	if requests.Load() != before+1 {
		t.Errorf("the camera wasn't asked once it was back")
	}
	if _, ok := LoginRetryIn(shown); ok {
		t.Error("an accepted login left a wait behind")
	}

	// The same for ffmpeg: a run that fails without a word about the login.
	runs := 0
	var answer error
	f, err := NewFFmpeg("rtsp://admin:pw-b2@cam-b2-probe.invalid:554/live")
	if err != nil {
		t.Fatal(err)
	}
	f.run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		runs++
		if answer != nil {
			return nil, answer
		}
		return pngOf(t, 4, 4), nil
	}
	answer = errors.New("exit status 1: method DESCRIBE failed: 401 (Unauthorized)")
	if _, err := f.Grab(context.Background()); !errors.Is(err, ErrLoginRefused) {
		t.Fatalf("ffmpeg first grab: %v", err)
	}
	clock.add(10 * time.Second)
	answer = fmt.Errorf("exit status 1: Connection to tcp://cam-b2-probe.invalid:554 failed: %w", context.DeadlineExceeded)
	if _, err := f.Grab(context.Background()); err == nil || errors.Is(err, ErrLoginRefused) {
		t.Fatalf("ffmpeg ask with the camera gone: err = %v, want a transport error", err)
	}
	answer = nil
	clock.add(time.Second)
	if _, err := f.Grab(context.Background()); err != nil {
		t.Fatalf("ffmpeg once the camera is back: %v (the ask was never handed back)", err)
	}
	if runs != 3 {
		t.Errorf("ffmpeg ran %d times, want 3", runs)
	}
}

// B2 fixer 2: a grab that gets no answer at all (the camera is off, times
// out, or fails its certificate check) fails with the http client's error,
// which quotes the request URL with only a user:password@ masked. A login
// in the query string must be masked there too, and the error must still
// say what went wrong underneath (a context deadline, ErrCertNotTrusted).
func TestQueryStringPasswordIsMaskedInTransportErrors(t *testing.T) {
	gateClock(t)
	const pw = "TSECRET-b2"
	closed := newListener(t)
	for name, raw := range map[string]string{
		"query":    "http://" + closed + "/cgi?user=admin&password=" + pw,
		"token":    "http://" + closed + "/snap.jpg?token=" + pw,
		"userinfo": "http://admin:" + pw + "@" + closed + "/snap.jpg",
	} {
		_, err := NewHTTPSnapshot(raw).Grab(context.Background())
		if err == nil {
			t.Fatalf("%s: no error from a closed port", name)
		}
		shown := RedactURL(raw)
		if strings.Contains(err.Error(), pw) || !strings.HasPrefix(err.Error(), "snapshot "+shown+": Get ") {
			t.Errorf("%s, connection refused: err = %v, want the password masked after %q", name, err, "snapshot "+shown)
		}
		var lre *LoginRefusedError
		if errors.Is(err, ErrLoginRefused) || errors.As(err, &lre) {
			t.Errorf("%s: a transport error counts as a refused login: %v", name, err)
		}
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	h := NewHTTPSnapshot(slow.URL + "/cgi?user=admin&password=" + pw)
	h.Timeout = 200 * time.Millisecond
	_, err := h.Grab(context.Background())
	if err == nil {
		t.Fatal("timeout: no error")
	}
	if strings.Contains(err.Error(), pw) || !strings.Contains(err.Error(), "password=xxxxx") {
		t.Errorf("timeout: err = %v, want the password masked", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: err = %v, want it to still unwrap to the context deadline", err)
	}

	tlsCam := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer tlsCam.Close()
	_, err = NewHTTPSnapshot(tlsCam.URL + "/snap.jpg?user=admin&password=" + pw).Grab(context.Background())
	if err == nil {
		t.Fatal("self-signed certificate: no error")
	}
	if strings.Contains(err.Error(), pw) || !strings.Contains(err.Error(), "password=xxxxx") {
		t.Errorf("certificate: err = %v, want the password masked", err)
	}
	if !errors.Is(err, ErrCertNotTrusted) || !strings.Contains(err.Error(), ErrCertNotTrusted.Error()) {
		t.Errorf("certificate: err = %v, want ErrCertNotTrusted", err)
	}
}

// newListener is the address of a port nothing listens on any more.
func newListener(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// StripLogin takes out what LoginInURL counts as a login and nothing
// else: the runner's Fingerprint goes by it, so a new password or token
// is the same camera and a new channel is not.
func TestStripLogin(t *testing.T) {
	for raw, want := range map[string]string{
		"http://cam/snap.jpg":                                "http://cam/snap.jpg",
		"http://admin:pw@cam/snap.jpg":                       "http://cam/snap.jpg",
		"http://cam/cgi?cmd=Snap&user=admin&password=x&ch=1": "http://cam/cgi?cmd=Snap&ch=1",
		"http://cam/cgi?channel=1&Password=x":                "http://cam/cgi?channel=1",
		"http://cam/snap.jpg?token=abc":                      "http://cam/snap.jpg",
		"http://cam/snap.jpg?apikey=abc&api_key=d&q=1":       "http://cam/snap.jpg?q=1",
		"rtsp://admin:pw@cam:554/stream1":                    "rtsp://cam:554/stream1",
	} {
		u := mustURL(t, raw)
		changed := StripLogin(u)
		if got := u.String(); got != want {
			t.Errorf("StripLogin(%q) left %q, want %q", raw, got, want)
		}
		if changed != (raw != want) {
			t.Errorf("StripLogin(%q) reported changed=%v", raw, changed)
		}
		if LoginInURL(u) {
			t.Errorf("StripLogin(%q) left a login behind: %q", raw, u.String())
		}
	}
	if StripLogin(nil) {
		t.Error("StripLogin(nil) reported a change")
	}
}
