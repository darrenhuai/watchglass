package source

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/demo"
)

// mjpegServer streams the demo printer frames as an endless
// multipart/x-mixed-replace response, the way ESP32-CAM, mjpg-streamer
// and IP Webcam do, and reports when the client hangs up.
func mjpegServer(t *testing.T, boundaryHeader, boundaryLine string, withLength bool) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	closed := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundaryHeader)
		fl := w.(http.Flusher)
		defer func() { closed <- struct{}{} }()
		for i := 0; ; i++ {
			frame, err := demo.FramePNG(demo.Printer, i%6)
			if err != nil {
				t.Error(err)
				return
			}
			head := "--" + boundaryLine + "\r\nContent-Type: image/png\r\n"
			if withLength {
				head += fmt.Sprintf("Content-Length: %d\r\n", len(frame))
			}
			if _, err := fmt.Fprintf(w, "%s\r\n", head); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := w.Write([]byte("\r\n")); err != nil {
				return
			}
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(150 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(ts.Close)
	return ts, closed
}

// A08: an MJPEG stream gives its first frame in well under a second, and
// the connection is closed after it rather than read until the timeout.
func TestGrabReadsTheFirstFrameOfAnMJPEGStream(t *testing.T) {
	cases := []struct {
		name         string
		header, line string
		withLength   bool
	}{
		{"boundary as declared", "frame", "frame", true},
		{"no Content-Length on the part", "frame", "frame", false},
		{"header boundary with leading dashes", "--myboundary", "myboundary", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts, closed := mjpegServer(t, c.header, c.line, c.withLength)
			start := time.Now()
			img, err := NewHTTPSnapshot(ts.URL + "/stream").Grab(context.Background())
			if err != nil {
				t.Fatalf("Grab: %v", err)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("first frame took %v, want under 1s", d)
			}
			if b := img.Bounds(); b.Dx() != 640 || b.Dy() != 360 {
				t.Errorf("frame is %v, want the 640x360 demo frame", b)
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Error("the server never saw the stream closed after the first frame")
			}
		})
	}
}

// A stream that stalls before its first frame ends at the grab's deadline
// like any slow camera.
func TestMJPEGStreamThatNeverSendsAFrameTimesOut(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=x")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer ts.Close()
	src := NewHTTPSnapshot(ts.URL)
	src.Timeout = 300 * time.Millisecond
	start := time.Now()
	if _, err := src.Grab(context.Background()); err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("err = %v, want a deadline", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

// digestServer answers like a Dahua/Amcrest snapshot.cgi: 401 with a
// Digest challenge, then the frame for a correct response. It checks the
// response with its own RFC 7616 arithmetic.
func digestServer(t *testing.T, algorithm, user, pass string, useTLS bool) (*httptest.Server, *int32) {
	t.Helper()
	var ok int32
	const realm, nonce, opaque = "Login to cam", "dcd98b7102dd2f0e8b11d0f600bfb0c093", "5ccc069c403ebaf9f0171e9517f40e41"
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		p := parseAuthParams(strings.TrimPrefix(authz, "Digest "))
		if strings.HasPrefix(authz, "Digest ") && p["username"] == user && p["realm"] == realm &&
			p["nonce"] == nonce && p["opaque"] == opaque && p["uri"] == r.URL.RequestURI() && p["qop"] == "auth" {
			var hf func() hash.Hash = md5.New
			if algorithm == "SHA-256" {
				hf = sha256.New
			}
			hx := func(s string) string {
				x := hf()
				x.Write([]byte(s))
				return hex.EncodeToString(x.Sum(nil))
			}
			want := hx(hx(user+":"+realm+":"+pass) + ":" + nonce + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + hx(r.Method+":"+r.URL.RequestURI()))
			if p["response"] == want && strings.EqualFold(p["algorithm"], algorithm) {
				atomic.AddInt32(&ok, 1)
				frame, _ := demo.FramePNG(demo.SevenSeg, 0)
				w.Header().Set("Content-Type", "image/png")
				w.Write(frame)
				return
			}
		}
		if algorithm == "SHA-256" {
			// Offered alongside MD5, as RFC 7616 servers do; the client
			// must pick SHA-256.
			w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", opaque="%s", algorithm=MD5`, realm, nonce, opaque))
		}
		w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", opaque="%s", algorithm=%s, stale=FALSE`, realm, nonce, opaque, algorithm))
		w.WriteHeader(http.StatusUnauthorized)
	})
	var ts *httptest.Server
	if useTLS {
		ts = httptest.NewTLSServer(h)
	} else {
		ts = httptest.NewServer(h)
	}
	t.Cleanup(ts.Close)
	return ts, &ok
}

func withCreds(raw, user, pass string) string {
	scheme, rest, _ := strings.Cut(raw, "://")
	return scheme + "://" + user + ":" + pass + "@" + rest
}

// A20: Digest auth from the URL's credentials, MD5 and SHA-256.
func TestGrabAnswersADigestChallenge(t *testing.T) {
	for _, alg := range []string{"MD5", "SHA-256"} {
		t.Run(alg, func(t *testing.T) {
			ts, ok := digestServer(t, alg, "admin", "s3cr:t", false)
			img, err := NewHTTPSnapshot(withCreds(ts.URL, "admin", "s3cr%3At") + "/cgi-bin/snapshot.cgi?channel=1").Grab(context.Background())
			if err != nil {
				t.Fatalf("Grab: %v", err)
			}
			if img == nil || atomic.LoadInt32(ok) != 1 {
				t.Fatalf("server accepted %d responses", atomic.LoadInt32(ok))
			}
			// A wrong password is still a 401 after the one retry: the
			// camera turning the login down, not a loop.
			_, err = NewHTTPSnapshot(withCreds(ts.URL, "admin", "wrongpw") + "/cgi-bin/snapshot.cgi").Grab(context.Background())
			if err == nil || !strings.Contains(err.Error(), "status 401") {
				t.Errorf("wrong password: err = %v, want status 401", err)
			}
			if strings.Contains(fmt.Sprint(err), "wrongpw") {
				t.Errorf("the error shows the password: %v", err)
			}
			// No credentials in the URL: nothing to answer with.
			if _, err := NewHTTPSnapshot(ts.URL + "/x").Grab(context.Background()); err == nil || !strings.Contains(err.Error(), "status 401") {
				t.Errorf("no credentials: err = %v", err)
			}
		})
	}
}

// A20: a self-signed HTTPS camera with Digest auth works with tls_insecure
// and, without it, fails saying what to set. The flag stays with its
// watch: other sources to the same server, built without it, still verify.
func TestSelfSignedCameraNeedsTLSInsecure(t *testing.T) {
	ts, ok := digestServer(t, "MD5", "user", "campw", true)
	target := withCreds(ts.URL, "user", "campw") + "/snap"

	secure, err := For(config.Watch{Name: "a", Source: target})
	if err != nil {
		t.Fatal(err)
	}
	_, err = secure.Grab(context.Background())
	if !errors.Is(err, ErrCertNotTrusted) || !strings.Contains(err.Error(), "certificate not trusted: set tls_insecure: true for this camera") {
		t.Fatalf("without tls_insecure: err = %v", err)
	}
	if strings.Contains(err.Error(), "campw") {
		t.Errorf("the error shows the password: %v", err)
	}

	insecure, err := For(config.Watch{Name: "b", Source: target, TLSInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insecure.Grab(context.Background()); err != nil {
		t.Fatalf("with tls_insecure: %v", err)
	}
	if atomic.LoadInt32(ok) != 1 {
		t.Errorf("digest over TLS: server accepted %d", atomic.LoadInt32(ok))
	}
	// Built after the insecure one and polled after it: still refused.
	again, _ := For(config.Watch{Name: "c", Source: target})
	if _, err := again.Grab(context.Background()); !errors.Is(err, ErrCertNotTrusted) {
		t.Errorf("a watch without tls_insecure must still verify: %v", err)
	}
	if cfg := http.DefaultTransport.(*http.Transport).TLSClientConfig; cfg != nil && cfg.InsecureSkipVerify {
		t.Error("the default transport was changed")
	}
}

// A20: headers: go out with every request.
func TestGrabSendsTheWatchHeaders(t *testing.T) {
	var got http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		frame, _ := demo.FramePNG(demo.Printer, 0)
		w.Write(frame)
	}))
	defer ts.Close()
	src, err := For(config.Watch{Name: "h", Source: ts.URL, Headers: []string{"X-Api-Key: abc 123", "Accept: image/jpeg"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Grab(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Api-Key") != "abc 123" || got.Get("Accept") != "image/jpeg" {
		t.Errorf("headers = %v", got)
	}
}

func TestDigestChallengeParsingAndRFCExamples(t *testing.T) {
	p := parseAuthParams(`realm="a, \"b\"", nonce=xyz, qop="auth,auth-int",algorithm=SHA-256`)
	if p["realm"] != `a, "b"` || p["nonce"] != "xyz" || p["qop"] != "auth,auth-int" || p["algorithm"] != "SHA-256" {
		t.Errorf("params = %q", p)
	}
	if _, ok := pickDigest([]string{`Digest realm="r", nonce="n", qop="auth-int"`}); ok {
		t.Error("an auth-int-only challenge can't be answered for a GET")
	}
	if _, ok := pickDigest([]string{`Basic realm="r"`}); ok {
		t.Error("Basic is not Digest")
	}
	// RFC 7616 section 3.9.1: the responses are fixed by the inputs, so
	// this pins the hashing for both algorithms.
	c := digestChallenge{realm: "http-auth@example.org", nonce: "7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v",
		opaque: "FQhe/qaU925kfnzjCev0ciny7QMkPqMAFRtzCUYo5tdS", algorithm: "SHA-256", qopAuth: true}
	const cnonce = "f2/wE4q74E6zIJEtWaHKaf5wv/H5QzzpXusqGemxURZJ"
	auth := c.authorization("Mufasa", "Circle of Life", "GET", "/dir/index.html", cnonce, 1)
	if !strings.Contains(auth, `response="753927fa0e85d155564e2e272a28d1802ca10daf4496794697cf8db5856cb6c1"`) {
		t.Errorf("SHA-256 response differs from RFC 7616's example: %s", auth)
	}
	c.algorithm = "MD5"
	auth = c.authorization("Mufasa", "Circle of Life", "GET", "/dir/index.html", cnonce, 1)
	if !strings.Contains(auth, `response="8ca523f5e9506fed4657c9700eebdbec"`) {
		t.Errorf("MD5 response differs from RFC 7616's example: %s", auth)
	}
}

// A20 (fixer): once a camera has asked for Digest, later grabs answer it
// straight away. Go's client would otherwise send the URL's password as
// Basic auth, in clear text on plain http, before every Digest retry.
func TestDigestSourceStopsSendingBasic(t *testing.T) {
	const user, pass = "admin", "s3cret"
	inner, ok := digestServer(t, "MD5", user, pass, false)
	var mu sync.Mutex
	var seen []string
	var ncs []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		mu.Lock()
		scheme, _, _ := strings.Cut(a, " ")
		seen = append(seen, scheme)
		if scheme == "Digest" {
			ncs = append(ncs, parseAuthParams(strings.TrimPrefix(a, "Digest "))["nc"])
		}
		mu.Unlock()
		req, _ := http.NewRequest(r.Method, inner.URL+r.URL.RequestURI(), nil)
		req.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			w.Header()[k] = vs
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer ts.Close()

	// The first grab from the runner's source, the next two from new
	// sources, as the web UI builds one per snapshot refresh and Test.
	target := withCreds(ts.URL, user, pass) + "/cgi-bin/snapshot.cgi"
	for i := 0; i < 3; i++ {
		if _, err := NewHTTPSnapshot(target).Grab(context.Background()); err != nil {
			t.Fatalf("grab %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	// The first grab can't know the camera wants Digest; after its 401,
	// every request is Digest and each grab is a single request.
	want := []string{"Basic", "Digest", "Digest", "Digest"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("Authorization schemes = %v, want %v", seen, want)
	}
	if strings.Join(ncs, ",") != "00000001,00000002,00000003" {
		t.Errorf("nonce counts = %v, want 1, 2, 3", ncs)
	}
	if atomic.LoadInt32(ok) != 3 {
		t.Errorf("camera accepted %d logins, want 3", atomic.LoadInt32(ok))
	}
}

// A stale nonce (the camera rotated it) costs one 401 and a retry with
// the new challenge, still without Basic.
func TestDigestSourceRetriesAStaleNonce(t *testing.T) {
	const user, pass, realm = "admin", "pw", "cam"
	var nonce atomic.Value
	nonce.Store("n1")
	var basic, okCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		if strings.HasPrefix(a, "Basic ") {
			atomic.AddInt32(&basic, 1)
		}
		n := nonce.Load().(string)
		if p := parseAuthParams(strings.TrimPrefix(a, "Digest ")); strings.HasPrefix(a, "Digest ") && p["nonce"] == n {
			c := digestChallenge{realm: realm, nonce: n, algorithm: "MD5", qopAuth: true}
			nc, _ := strconv.ParseUint(p["nc"], 16, 32)
			if a == c.authorization(user, pass, "GET", r.URL.RequestURI(), p["cnonce"], uint32(nc)) {
				atomic.AddInt32(&okCount, 1)
				frame, _ := demo.FramePNG(demo.SevenSeg, 0)
				w.Write(frame)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", stale=TRUE`, realm, n))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()
	src := NewHTTPSnapshot(withCreds(ts.URL, user, pass) + "/snap")
	if _, err := src.Grab(context.Background()); err != nil {
		t.Fatal(err)
	}
	nonce.Store("n2")
	if _, err := src.Grab(context.Background()); err != nil {
		t.Fatalf("after the nonce changed: %v", err)
	}
	if b := atomic.LoadInt32(&basic); b != 1 {
		t.Errorf("Basic sent %d times, want only the first request", b)
	}
	if atomic.LoadInt32(&okCount) != 2 {
		t.Errorf("accepted %d", okCount)
	}
}

// Two watches on one camera account, one with a wrong password: the
// wrong one keeps its own Digest memory, so it can't reset the nonce
// count the working one is on (a camera that checks for replayed counts
// would refuse the repeats), and after its first grab it costs the
// account one failed login per poll, not two.
func TestDigestWrongPasswordKeepsToItself(t *testing.T) {
	const user, pass, realm, nonce = "admin", "s3cret", "cam", "fixednonce"
	var mu sync.Mutex
	goodNC := []string{}
	badRequests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		mu.Lock()
		if r.URL.Path == "/bad" {
			badRequests++
		}
		mu.Unlock()
		if p := parseAuthParams(strings.TrimPrefix(a, "Digest ")); strings.HasPrefix(a, "Digest ") && p["nonce"] == nonce {
			c := digestChallenge{realm: realm, nonce: nonce, algorithm: "MD5", qopAuth: true}
			nc, _ := strconv.ParseUint(p["nc"], 16, 32)
			if a == c.authorization(user, pass, "GET", r.URL.RequestURI(), p["cnonce"], uint32(nc)) {
				mu.Lock()
				goodNC = append(goodNC, p["nc"])
				mu.Unlock()
				frame, _ := demo.FramePNG(demo.SevenSeg, 0)
				w.Write(frame)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s"`, realm, nonce))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()
	good := withCreds(ts.URL, user, pass) + "/good"
	bad := withCreds(ts.URL, user, "wrong") + "/bad"
	for i := 0; i < 3; i++ {
		if _, err := NewHTTPSnapshot(good).Grab(context.Background()); err != nil {
			t.Fatalf("good grab %d: %v", i, err)
		}
		if _, err := NewHTTPSnapshot(bad).Grab(context.Background()); err == nil || !strings.Contains(err.Error(), "status 401") {
			t.Fatalf("bad grab %d: err = %v, want status 401", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(goodNC, ",") != "00000001,00000002,00000003" {
		t.Errorf("working watch's nonce counts = %v, want 1, 2, 3 with no repeats", goodNC)
	}
	// First grab: Basic, then the Digest retry. Each later grab: the
	// remembered Digest answer only, which the camera refuses with the
	// same nonce and no stale flag, so there is nothing to retry.
	if badRequests != 2+1+1 {
		t.Errorf("wrong-password watch made %d requests over 3 grabs, want 4", badRequests)
	}
}

// A camera that refuses a count but keeps the nonce says stale=true; that
// still gets the one retry (the password was right), with the count
// started over.
func TestDigestStaleSameNonceStillRetries(t *testing.T) {
	const user, pass, realm, nonce = "admin", "pw", "cam", "samenonce"
	var refusedOnce, okCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		stale := ""
		if p := parseAuthParams(strings.TrimPrefix(a, "Digest ")); strings.HasPrefix(a, "Digest ") && p["nonce"] == nonce {
			c := digestChallenge{realm: realm, nonce: nonce, algorithm: "MD5", qopAuth: true}
			nc, _ := strconv.ParseUint(p["nc"], 16, 32)
			if a == c.authorization(user, pass, "GET", r.URL.RequestURI(), p["cnonce"], uint32(nc)) {
				if nc == 1 || atomic.LoadInt32(&refusedOnce) == 1 {
					atomic.AddInt32(&okCount, 1)
					frame, _ := demo.FramePNG(demo.SevenSeg, 0)
					w.Write(frame)
					return
				}
				atomic.StoreInt32(&refusedOnce, 1)
				stale = ", stale=true"
			}
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s"%s`, realm, nonce, stale))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()
	src := NewHTTPSnapshot(withCreds(ts.URL, user, pass) + "/snap")
	for i := 0; i < 2; i++ {
		if _, err := src.Grab(context.Background()); err != nil {
			t.Fatalf("grab %d: %v", i, err)
		}
	}
	if atomic.LoadInt32(&okCount) != 2 {
		t.Errorf("accepted %d", okCount)
	}
}
