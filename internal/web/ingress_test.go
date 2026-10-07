package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
)

// Home Assistant ingress (ingress.go): the Supervisor strips
// /api/hassio_ingress/<token> from the path and says so in X-Ingress-Path,
// so every link watchglass writes must put it back, and only for requests
// that really came through the Supervisor.

const (
	ingressPeer   = "192.0.2.1"
	ingressToken  = "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6a7b8c9d0e1f2a3b4c5d6a7b8c9d0e1f2"
	ingressPrefix = "/api/hassio_ingress/" + ingressToken
)

// ingressServer is newTestServer with ingress mode on and ingressPeer as
// the Supervisor's address.
func ingressServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, cfgPath := newTestServer(t)
	s.Ingress = true
	s.IngressFrom = netip.MustParseAddr(ingressPeer)
	return s, cfgPath
}

// doReq sends one request from peer (an ip:port) with the given headers;
// form, when non-nil, makes it a form POST.
func doReq(t *testing.T, h http.Handler, method, path string, form url.Values, peer string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.RemoteAddr = peer
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// viaIngress is doReq from the Supervisor's address with the header set.
func viaIngress(t *testing.T, h http.Handler, method, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	return doReq(t, h, method, path, form, ingressPeer+":40000", map[string]string{IngressHeader: ingressPrefix})
}

// pathAttrs is every href, src, action, data-src and data-base value on
// the page that is a path (starts with "/"). Anchors and the external
// docs links are not watchglass's own URLs.
var pathAttrs = regexp.MustCompile(`(?:href|src|action|data-src|data-base)="(/[^"]*)"`)

// wantAllPrefixed fails for any path attribute on body that doesn't start
// with prefix: the sweep behind "every place a link is produced".
func wantAllPrefixed(t *testing.T, page, body, prefix string) {
	t.Helper()
	m := pathAttrs.FindAllStringSubmatch(body, -1)
	if len(m) == 0 {
		t.Fatalf("%s: no path attributes found; body:\n%s", page, body)
	}
	for _, mm := range m {
		if !strings.HasPrefix(mm[1], prefix+"/") && mm[1] != prefix {
			t.Errorf("%s: %s doesn't carry the prefix %s", page, mm[0], prefix)
		}
	}
}

func flashPath(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == flashCookie {
			return c.Path
		}
	}
	return "(no flash cookie)"
}

func TestIngressPathShape(t *testing.T) {
	valid := []string{
		ingressPrefix,
		"/api/hassio_ingress/abc",
		"/api/hassio_ingress/A-Z_09",
		"/api/hassio_ingress/" + strings.Repeat("f", ingressTokenMax),
	}
	for _, v := range valid {
		if !validIngressPath(v) {
			t.Errorf("validIngressPath(%q) = false, want true", v)
		}
	}
	invalid := []string{
		"",
		"/",
		"/api/hassio_ingress",
		"/api/hassio_ingress/",
		"/api/hassio_ingress/abc/",
		"/api/hassio_ingress/abc/..",
		"/api/hassio_ingress/..",
		"/api/hassio_ingress/abc/../..",
		`/api/hassio_ingress/abc"onmouseover="alert(1)`,
		"/api/hassio_ingress/abc'",
		"//evil.example/api/hassio_ingress/abc",
		"/api/hassio_ingress/abc//evil.example",
		"javascript:alert(1)",
		"http://evil.example/api/hassio_ingress/abc",
		"/api/hassio_ingress/abc\r\nSet-Cookie: x=y",
		"/api/hassio_ingress/abc\n",
		"/api/hassio_ingress/abc?x=1",
		"/api/hassio_ingress/abc#f",
		"/api/hassio_ingress/ab c",
		"/api/hassio_ingress/abc%2F..",
		"/api/hassio_ingress/abc;x",
		"/API/hassio_ingress/abc",
		" /api/hassio_ingress/abc",
		"/api/hassio_ingress/abc ",
		"/api/hassio_ingress/" + strings.Repeat("f", ingressTokenMax+1),
		"/api/hassio_ingress/é",
		"/api/hassio_ingress/abc\x00",
	}
	for _, v := range invalid {
		if validIngressPath(v) {
			t.Errorf("validIngressPath(%q) = true, want false", v)
		}
	}
}

func TestIngressPrefixesEveryLink(t *testing.T) {
	s, _ := ingressServer(t)
	s.MQTTStatus = func() (string, error) { return "connected", nil }
	h := s.Handler()

	// Index: static assets, brand link, index.js's data-src, the add form,
	// the watch link and its delete form, the topbar line.
	resp, body := viaIngress(t, h, "GET", "/", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("index: status = %d", resp.StatusCode)
	}
	for _, want := range []string{
		`href="` + ingressPrefix + `/static/favicon.svg"`,
		`href="` + ingressPrefix + `/static/style.css"`,
		`src="` + ingressPrefix + `/static/topbar.js"`,
		`src="` + ingressPrefix + `/static/index.js"`,
		`class="brand" href="` + ingressPrefix + `/"`,
		`data-src="` + ingressPrefix + `/"`,
		`data-src="` + ingressPrefix + `/ha-status"`,
		`action="` + ingressPrefix + `/watch/new"`,
		`href="` + ingressPrefix + `/watch/printer"`,
		`action="` + ingressPrefix + `/watch/printer/delete"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %s", want)
		}
	}
	wantAllPrefixed(t, "index", body, ingressPrefix)

	// Detail: back link, app.js and the fetch base it reads, the snapshot
	// img, the save form.
	_, body = viaIngress(t, h, "GET", "/watch/printer", nil)
	for _, want := range []string{
		`href="` + ingressPrefix + `/"`,
		`src="` + ingressPrefix + `/static/app.js"`,
		`data-base="` + ingressPrefix + `"`,
		`src="` + ingressPrefix + `/watch/printer/snapshot"`,
		`action="` + ingressPrefix + `/watch/printer/save"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %s", want)
		}
	}
	wantAllPrefixed(t, "detail", body, ingressPrefix)

	// The topbar fragment topbar.js polls.
	_, body = viaIngress(t, h, "GET", "/ha-status", nil)
	if !strings.Contains(body, `data-src="`+ingressPrefix+`/ha-status"`) {
		t.Errorf("ha-status fragment lacks the prefix: %s", body)
	}

	// The favicon probe.
	resp, _ = viaIngress(t, h, "GET", "/favicon.ico", nil)
	if loc := resp.Header.Get("Location"); resp.StatusCode != 301 || loc != ingressPrefix+"/static/favicon.svg" {
		t.Errorf("favicon: %d %q", resp.StatusCode, loc)
	}

	// Create, save and delete: the redirect and the flash cookie's Path.
	resp, _ = viaIngress(t, h, "POST", "/watch/new", url.Values{"name": {"oven"}, "source": {"http://cam2/snap.jpg"}})
	if loc := resp.Header.Get("Location"); resp.StatusCode != 303 || loc != ingressPrefix+"/watch/oven" {
		t.Errorf("create: %d %q", resp.StatusCode, loc)
	}
	if p := flashPath(resp); p != ingressPrefix+"/" {
		t.Errorf("create flash cookie Path = %q", p)
	}
	saveForm := url.Values{
		"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
		"ttype": {"pixel_change"}, "tthreshold": {"10"},
		"confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
	}
	resp, _ = viaIngress(t, h, "POST", "/watch/printer/save", saveForm)
	if loc := resp.Header.Get("Location"); resp.StatusCode != 303 || loc != ingressPrefix+"/watch/printer" {
		t.Errorf("save: %d %q", resp.StatusCode, loc)
	}
	if p := flashPath(resp); p != ingressPrefix+"/" {
		t.Errorf("save flash cookie Path = %q", p)
	}
	// The page that consumes the flash clears the cookie on the same Path.
	resp, body = doReq(t, h, "GET", "/watch/printer", nil, ingressPeer+":40001",
		map[string]string{IngressHeader: ingressPrefix, "Cookie": resp.Cookies()[0].String()})
	if p := flashPath(resp); p != ingressPrefix+"/" {
		t.Errorf("flash clearing cookie Path = %q", p)
	}
	if !strings.Contains(body, "Saved to") {
		t.Errorf("flash not shown on the ingress page")
	}
	resp, _ = viaIngress(t, h, "POST", "/watch/oven/delete", nil)
	if loc := resp.Header.Get("Location"); resp.StatusCode != 303 || loc != ingressPrefix+"/" {
		t.Errorf("delete: %d %q", resp.StatusCode, loc)
	}
	if p := flashPath(resp); p != ingressPrefix+"/" {
		t.Errorf("delete flash cookie Path = %q", p)
	}

	// The error page (a delete whose config can't be written).
	s.cfgPath = filepath.Join(t.TempDir(), "missing", "config.yaml")
	resp, body = viaIngress(t, h, "POST", "/watch/printer/delete", nil)
	if resp.StatusCode != 500 || !strings.Contains(body, "Couldn&#39;t delete printer") {
		t.Fatalf("error page: %d; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `href="`+ingressPrefix+`/">`) {
		t.Errorf("error page back link lacks the prefix")
	}
	wantAllPrefixed(t, "error page", body, ingressPrefix)
}

// Without a header, or with one outside the documented shape, the answer is
// byte for byte what it is today.
func TestIngressIgnoresAMissingOrBadHeader(t *testing.T) {
	s, _ := ingressServer(t)
	h := s.Handler()
	peer := ingressPeer + ":40000"
	_, plainIndex := doReq(t, h, "GET", "/", nil, peer, nil)
	_, plainDetail := doReq(t, h, "GET", "/watch/printer", nil, peer, nil)
	if !strings.Contains(plainIndex, `href="/watch/printer"`) || !strings.Contains(plainDetail, `data-base=""`) {
		t.Fatal("no header: links must be bare")
	}
	bad := []string{
		"",
		"/api/hassio_ingress/",
		"/api/hassio_ingress/abc/",
		`/api/hassio_ingress/abc"`,
		"//evil.example/api/hassio_ingress/abc",
		"javascript:alert(1)",
		"/api/hassio_ingress/abc\r\nX-Injected: 1",
		"/api/hassio_ingress/../../static",
		"/other/" + ingressToken,
	}
	// Pages first (they must not change at all), then the redirects: a
	// save restarts the watch, after which the pages legitimately differ
	// from the ones captured above.
	for _, v := range bad {
		hdr := map[string]string{IngressHeader: v}
		if _, body := doReq(t, h, "GET", "/", nil, peer, hdr); body != plainIndex {
			t.Errorf("header %q: index differs from the no-header page", v)
		}
		if _, body := doReq(t, h, "GET", "/watch/printer", nil, peer, hdr); body != plainDetail {
			t.Errorf("header %q: detail differs from the no-header page", v)
		}
		resp, _ := doReq(t, h, "GET", "/favicon.ico", nil, peer, hdr)
		if loc := resp.Header.Get("Location"); loc != "/static/favicon.svg" {
			t.Errorf("header %q: favicon Location = %q", v, loc)
		}
	}
	// Two values, one of them valid: not the documented one-header shape.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = peer
	req.Header.Add(IngressHeader, ingressPrefix)
	req.Header.Add(IngressHeader, "/api/hassio_ingress/other")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if b, _ := io.ReadAll(rec.Result().Body); string(b) != plainIndex {
		t.Error("two X-Ingress-Path values: index differs from the no-header page")
	}
	for _, v := range bad {
		resp, _ := doReq(t, h, "POST", "/watch/printer/save", url.Values{
			"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
			"ttype": {"pixel_change"}, "tthreshold": {"10"},
			"confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
		}, peer, map[string]string{IngressHeader: v})
		if loc := resp.Header.Get("Location"); resp.StatusCode != 303 || loc != "/watch/printer" {
			t.Errorf("header %q: save redirect %d %q", v, resp.StatusCode, loc)
		}
		if p := flashPath(resp); p != "/" {
			t.Errorf("header %q: flash cookie Path = %q", v, p)
		}
	}
}

// Ingress mode off (the default): the header does nothing, from anywhere,
// with or without -base-path.
func TestIngressOffIgnoresTheHeader(t *testing.T) {
	s, _ := newTestServer(t)
	s.IngressFrom = netip.MustParseAddr(ingressPeer) // set, but Ingress is false
	h := s.Handler()
	for _, peer := range []string{ingressPeer + ":40000", "127.0.0.1:50000"} {
		_, body := doReq(t, h, "GET", "/", nil, peer, map[string]string{IngressHeader: ingressPrefix})
		if !strings.Contains(body, `href="/watch/printer"`) || strings.Contains(body, ingressToken) {
			t.Errorf("ingress off, peer %s: the header changed the page", peer)
		}
		resp, _ := doReq(t, h, "POST", "/watch/printer/save", url.Values{
			"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"},
			"ttype": {"pixel_change"}, "tthreshold": {"10"},
			"confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"},
		}, peer, map[string]string{IngressHeader: ingressPrefix})
		if loc := resp.Header.Get("Location"); loc != "/watch/printer" || flashPath(resp) != "/" {
			t.Errorf("ingress off, peer %s: save redirect %q, cookie Path %q", peer, loc, flashPath(resp))
		}
	}

	// -base-path with ingress off: exactly as before.
	s.BasePath = "/wg"
	_, body := doReq(t, h, "GET", "/watch/printer", nil, ingressPeer+":40000", map[string]string{IngressHeader: ingressPrefix})
	for _, want := range []string{`src="/wg/static/app.js"`, `data-base="/wg"`, `action="/wg/watch/printer/save"`} {
		if !strings.Contains(body, want) {
			t.Errorf("-base-path with ingress off: detail missing %s", want)
		}
	}
	wantAllPrefixed(t, "-base-path detail", body, "/wg")

	// -base-path with ingress on: direct requests keep the base path, the
	// Supervisor's get the ingress prefix instead.
	s.Ingress = true
	_, body = doReq(t, h, "GET", "/watch/printer", nil, "10.0.0.9:50000", map[string]string{IngressHeader: ingressPrefix})
	wantAllPrefixed(t, "-base-path direct request in ingress mode", body, "/wg")
	_, body = viaIngress(t, h, "GET", "/watch/printer", nil)
	wantAllPrefixed(t, "-base-path ingress request", body, ingressPrefix)
	if strings.Contains(body, `"/wg`) {
		t.Error("an ingress request must not also carry -base-path")
	}
}

// Only the Supervisor's address makes an ingress request, and only an
// ingress request skips basic auth.
func TestIngressTrustsOnlyTheSupervisorAddress(t *testing.T) {
	s, _ := ingressServer(t)
	s.mu.Lock()
	s.cfg.Auth = &config.Auth{Username: "admin", Password: "hunter2"}
	s.mu.Unlock()
	h := s.Handler()

	// Through the Supervisor: no login prompt, prefixed links.
	resp, body := viaIngress(t, h, "GET", "/", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("ingress request with an auth block: status = %d, want 200 (Home Assistant already logged the user in)", resp.StatusCode)
	}
	wantAllPrefixed(t, "ingress index", body, ingressPrefix)
	for _, path := range []string{"/static/app.js", "/watch/printer/snapshot", "/watch/printer/live", "/ha-status"} {
		if resp, _ := viaIngress(t, h, "GET", path, nil); resp.StatusCode != 200 {
			t.Errorf("ingress %s: status = %d, want 200", path, resp.StatusCode)
		}
	}

	// The same header from anywhere else: a direct request, auth applies,
	// links bare.
	for _, peer := range []string{"10.0.0.9:50000", "127.0.0.1:50000", "[::1]:50000", "[2001:db8::1]:50000"} {
		resp, _ := doReq(t, h, "GET", "/", nil, peer, map[string]string{IngressHeader: ingressPrefix})
		if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Basic") {
			t.Errorf("peer %s with the header: status = %d, want 401 with a Basic challenge", peer, resp.StatusCode)
		}
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = peer
		req.Header.Set(IngressHeader, ingressPrefix)
		req.SetBasicAuth("admin", "hunter2")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Result().Body)
		if rec.Result().StatusCode != 200 {
			t.Errorf("peer %s with credentials: status = %d, want 200", peer, rec.Result().StatusCode)
		}
		if strings.Contains(string(b), ingressToken) {
			t.Errorf("peer %s: a direct request must not get the ingress prefix", peer)
		}
	}

	// The Supervisor's address without the header is a direct request too.
	resp, _ = doReq(t, h, "GET", "/", nil, ingressPeer+":40000", nil)
	if resp.StatusCode != 401 {
		t.Errorf("Supervisor address without the header: status = %d, want 401", resp.StatusCode)
	}

	// IPv4-mapped IPv6 is the same address.
	resp, _ = doReq(t, h, "GET", "/", nil, "[::ffff:"+ingressPeer+"]:40000", map[string]string{IngressHeader: ingressPrefix})
	if resp.StatusCode != 200 {
		t.Errorf("IPv4-mapped Supervisor address: status = %d, want 200", resp.StatusCode)
	}

	// No IngressFrom at all: nothing is an ingress request.
	s.IngressFrom = netip.Addr{}
	resp, _ = viaIngress(t, h, "GET", "/", nil)
	if resp.StatusCode != 401 {
		t.Errorf("zero IngressFrom: status = %d, want 401", resp.StatusCode)
	}
}

// Through the proxy the page and its forms share Home Assistant's origin:
// the browser's Sec-Fetch-Site and Origin/Host pass through unchanged, so
// cross-origin protection must keep its verdicts.
func TestIngressKeepsCrossOriginProtection(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"same-origin fetch metadata", map[string]string{"Sec-Fetch-Site": "same-origin"}, 303},
		{"none (typed URL)", map[string]string{"Sec-Fetch-Site": "none"}, 303},
		{"cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-site", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"old browser, Origin matches HA's Host", map[string]string{"Origin": "http://homeassistant.local:8123", "Host": "homeassistant.local:8123"}, 303},
		{"old browser, Origin elsewhere", map[string]string{"Origin": "http://evil.example", "Host": "homeassistant.local:8123"}, 403},
		{"no fetch metadata, no Origin (curl)", nil, 303},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, cfgPath := ingressServer(t)
			h := s.Handler()
			hdr := map[string]string{IngressHeader: ingressPrefix}
			for k, v := range c.headers {
				hdr[k] = v
			}
			req := httptest.NewRequest("POST", "/watch/printer/delete", strings.NewReader(""))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.RemoteAddr = ingressPeer + ":40000"
			for k, v := range hdr {
				if k == "Host" {
					req.Host = v
					continue
				}
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			resp := rec.Result()
			if resp.StatusCode != c.want {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, c.want, b)
			}
			got := s.Watches()
			if c.want == 303 && len(got) != 0 {
				t.Errorf("the delete didn't take: %d watches", len(got))
			}
			if c.want == 403 && len(got) != 1 {
				t.Errorf("a refused POST took effect: %d watches (config %s)", len(got), cfgPath)
			}
			if c.want == 303 {
				if loc := resp.Header.Get("Location"); loc != ingressPrefix+"/" {
					t.Errorf("Location = %q", loc)
				}
			}
		})
	}
}

// A header from the Supervisor's address that isn't the documented shape
// is said once in the log, with the value quoted, and never changes a link.
func TestIngressSaysOnceWhenTheHeaderIsNotTheDocumentedShape(t *testing.T) {
	s, _ := ingressServer(t)
	var lines []string
	s.logf = func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) }
	h := s.Handler()
	odd := "/api/hassio_ingress/abc/\r\n"
	for i := 0; i < 3; i++ {
		_, body := doReq(t, h, "GET", "/", nil, ingressPeer+":40000", map[string]string{IngressHeader: odd})
		if !strings.Contains(body, `href="/watch/printer"`) {
			t.Fatal("an undocumented header must leave links bare")
		}
	}
	var hits []string
	for _, l := range lines {
		if strings.Contains(l, "ignoring X-Ingress-Path") {
			hits = append(hits, l)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("log lines about the header = %d, want 1: %q", len(hits), hits)
	}
	if !strings.Contains(hits[0], `"/api/hassio_ingress/abc/\r\n"`) || strings.Contains(hits[0], "\r") {
		t.Errorf("the value must be quoted with its control characters escaped: %q", hits[0])
	}
}

// Two header values are refused as a whole (TestIngressIgnoresAMissingOrBadHeader)
// and, like an undocumented value, the log says so once per start, with
// the count: a Supervisor that sent two would otherwise be invisible.
func TestIngressSaysOnceWhenTheHeaderComesMoreThanOnce(t *testing.T) {
	s, _ := ingressServer(t)
	var lines []string
	s.logf = func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) }
	h := s.Handler()
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = ingressPeer + ":40000"
		req.Header.Add(IngressHeader, ingressPrefix)
		req.Header.Add(IngressHeader, ingressPrefix)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if body, _ := io.ReadAll(rec.Result().Body); !strings.Contains(string(body), `href="/watch/printer"`) {
			t.Fatal("two header values must leave links bare")
		}
	}
	var hits []string
	for _, l := range lines {
		if strings.Contains(l, "ignoring X-Ingress-Path") {
			hits = append(hits, l)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("log lines about the header = %d, want 1: %q", len(hits), hits)
	}
	if !strings.Contains(hits[0], "2 values") {
		t.Errorf("the line must say how many values came: %q", hits[0])
	}
}

// app.js and index.js take their fetch base from the page, never from
// window.location, so the prefix the server put in data-base/data-src is
// the one they use.
func TestIngressFetchBaseComesFromThePage(t *testing.T) {
	src := func(name string) string {
		b, err := readSourceLF(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	app := src("static/app.js")
	if !strings.Contains(app, `stage.dataset.base || ""`) {
		t.Error("app.js must read its fetch base from #stage's data-base")
	}
	for _, want := range []string{
		`base + "/watch/" + encodeURIComponent(name) + "/snapshot"`,
		`base + "/watch/" + encodeURIComponent(name) + "/live"`,
		`base + "/watch/" + encodeURIComponent(name) + "/test"`,
		`base + "/watch/" + encodeURIComponent(name) + "/test-notify"`,
		`back.href = base + "/"`,
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js must build %s from the page's base", want)
		}
	}
	if strings.Contains(app, "location.pathname") || strings.Contains(app, "location.href") {
		t.Error("app.js must not derive URLs from window.location")
	}
	index := src("static/index.js")
	if !strings.Contains(index, `getAttribute("data-src")`) {
		t.Error("index.js must poll the URL the page gave it in data-src")
	}
	topbar := src("static/topbar.js")
	if !strings.Contains(topbar, `getAttribute("data-src")`) {
		t.Error("topbar.js must poll the URL the page gave it in data-src")
	}
}
