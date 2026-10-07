package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/demo"
	"github.com/darrenhuai/watchglass/internal/source"
)

// B2: a camera that turns the login down. The detail page's snapshot (and
// its refresh) waits like the poll loop does and says when the next try
// is; Test this region asks the camera anyway, and once the camera takes
// the login the wait is over for the snapshot too.
func TestRefusedLoginWaitShownAndTestAsksAnyway(t *testing.T) {
	var requests atomic.Int32
	var accept atomic.Bool
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if accept.Load() {
			frame, _ := demo.FramePNG(demo.Printer, 0)
			w.Header().Set("Content-Type", "image/png")
			w.Write(frame)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="cam"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer cam.Close()
	const pw = "pw-b2-web"
	target := "http://admin:" + pw + "@" + strings.TrimPrefix(cam.URL, "http://") + "/snap.png"

	s, _ := newTestServer(t)
	s.NewSource = func(w config.Watch) (source.Source, error) {
		w.Source = target
		return source.For(w)
	}
	h := s.Handler()
	waitRe := regexp.MustCompile(`^127\.0\.0\.1:\d+ turned down the login\. Trying again in (\d+)s\. watchglass waits between tries so that repeated wrong tries don't lock the account\. Check the user and password in the source URL\. Test this region tries again straight away\.\n`)

	resp, body := get(t, h, "/watch/printer/snapshot")
	if resp.StatusCode != http.StatusBadGateway || requests.Load() != 1 {
		t.Fatalf("first snapshot: %d, %d requests: %s", resp.StatusCode, requests.Load(), body)
	}
	if m := waitRe.FindStringSubmatch(body); m == nil || (m[1] != "10" && m[1] != "9") {
		t.Errorf("first snapshot body = %q, want the wait in words", body)
	}
	for i := 0; i < 3; i++ {
		resp, body = get(t, h, "/watch/printer/snapshot")
		if resp.StatusCode != http.StatusBadGateway || !waitRe.MatchString(body) {
			t.Fatalf("snapshot inside the wait: %d %q", resp.StatusCode, body)
		}
		if strings.Contains(body, pw) {
			t.Fatalf("the password is in the answer: %q", body)
		}
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("snapshots inside the wait asked the camera %d times", n-1)
	}

	test := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"}, "tthreshold": {"10"}}
	resp, body = postForm(t, h, "/watch/printer/test", test)
	if resp.StatusCode != http.StatusBadGateway || requests.Load() != 2 {
		t.Fatalf("Test inside the wait: %d, %d requests, want it to ask the camera: %s", resp.StatusCode, requests.Load(), body)
	}
	if !strings.Contains(body, "turned down the login") || strings.Contains(body, pw) {
		t.Errorf("refused Test says %q", body)
	}

	// A pixel_change Test compares two frames, so a Test the camera
	// accepts costs two requests.
	accept.Store(true)
	if resp, body = postForm(t, h, "/watch/printer/test", test); resp.StatusCode != http.StatusOK || requests.Load() != 4 {
		t.Fatalf("Test once the camera takes the login: %d, %d requests: %s", resp.StatusCode, requests.Load(), body)
	}
	if resp, _ = get(t, h, "/watch/printer/snapshot"); resp.StatusCode != http.StatusOK || requests.Load() != 5 {
		t.Errorf("snapshot after a successful Test: %d, %d requests, want the frame at once", resp.StatusCode, requests.Load())
	}
}

// B2: the error words without a running wait are what they were, and a
// wait shows in the summary and the hint for any message naming that
// camera and user, including an ffmpeg one and one recorded when the
// watch went down.
func TestLoginRefusedWordsWithAndWithoutAWait(t *testing.T) {
	plain := "grab: snapshot http://admin:xxxxx@192.0.2.7/snap.jpg: status 401"
	if got := summarizeErr(plain); got != "192.0.2.7 turned down the login" {
		t.Errorf("summary without a wait = %q", got)
	}
	if got := errHint(plain); got != "Check the user and password in the source URL. Some cameras lock the account for a while after a few wrong tries." {
		t.Errorf("hint without a wait = %q", got)
	}

	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer cam.Close()
	host := strings.TrimPrefix(cam.URL, "http://")
	if _, err := source.NewHTTPSnapshot("http://viewer:pw-b2@" + host + "/a.jpg").Grab(t.Context()); err == nil {
		t.Fatal("want a refusal")
	}
	for _, msg := range []string{
		"no reading for 3 consecutive polls: grab: snapshot http://viewer:xxxxx@" + host + "/b.jpg: status 401 (not asked: the camera turned this login down a moment ago)",
		"grab: snapshot http://viewer:xxxxx@" + host + "/a.jpg: status 403",
		// ffmpeg reading the same camera's MJPEG URL, skipped inside the wait.
		"grab: ffmpeg: http://viewer:xxxxx@" + host + "/a.jpg: 401 Unauthorized (not asked: the camera turned this login down a moment ago)",
	} {
		sum := summarizeErr(msg)
		if !regexp.MustCompile(`^` + regexp.QuoteMeta(host) + ` turned down the login\. Trying again in (10|9)s$`).MatchString(sum) {
			t.Errorf("summary of %q = %q", msg, sum)
		}
		if h := errHint(msg); !strings.HasPrefix(h, "watchglass waits between tries") {
			t.Errorf("hint of %q = %q", msg, h)
		}
	}
	// Another user on that camera has no wait running.
	if got := summarizeErr("grab: snapshot http://admin:xxxxx@" + host + "/a.jpg: status 401"); got != host+" turned down the login" {
		t.Errorf("another user's summary = %q", got)
	}
}

// B2 fixer: a login in the query string (Reolink's ?user=&password=) is a
// login too: the hint says to check it rather than to put one in the URL,
// the wait shows for it, and the page's source line masks it.
func TestQueryStringLoginIsALogin(t *testing.T) {
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer cam.Close()
	host := strings.TrimPrefix(cam.URL, "http://")
	const pw = "QSECRET-b2-web"
	if _, err := source.NewHTTPSnapshot("http://" + host + "/q.png?user=viewer&password=" + pw).Grab(t.Context()); err == nil {
		t.Fatal("want a refusal")
	}
	msg := "grab: snapshot http://" + host + "/q.png?user=viewer&password=xxxxx: status 401"
	if sum := summarizeErr(msg); !regexp.MustCompile(`^` + regexp.QuoteMeta(host) + ` turned down the login\. Trying again in (10|9)s$`).MatchString(sum) {
		t.Errorf("summary = %q", sum)
	}
	if h := errHint(msg); !strings.HasPrefix(h, "watchglass waits between tries") {
		t.Errorf("hint during the wait = %q", h)
	}
	// Without a wait running (another camera), the same style of login
	// gets the "check it" hint, not "put one in the URL".
	plain := "grab: snapshot http://192.0.2.9/cgi-bin/api.cgi?cmd=Snap&user=admin&password=xxxxx: status 401"
	if got := errHint(plain); got != "Check the user and password in the source URL. Some cameras lock the account for a while after a few wrong tries." {
		t.Errorf("hint without a wait = %q", got)
	}
	for in, want := range map[string]string{
		"http://cam/cgi-bin/api.cgi?cmd=Snap&user=admin&password=s3cret": "http://cam/cgi-bin/api.cgi?cmd=Snap&user=admin&password=xxxxx",
		"ffmpeg:-i http://a:b@cam/x?pwd=s3cret -f mjpeg":                 "ffmpeg:-i http://a:xxxxx@cam/x?pwd=xxxxx -f mjpeg",
		"http://cam/cgi?user=a:b@c":                                      "http://cam/cgi?user=a:b@c",
	} {
		if got := redactSource(in); got != want {
			t.Errorf("redactSource(%q) = %q, want %q", in, got, want)
		}
	}
}
