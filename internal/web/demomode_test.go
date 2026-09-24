package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/source"
)

// -demo sets Server.DemoDir, and every page then says where the demo's
// edits go and that a restart resets them. Without it, no page does.
func TestDemoBannerOnlyInDemoMode(t *testing.T) {
	s, _ := newTestServer(t)
	for _, p := range []string{"/", "/watch/printer"} {
		if _, body := get(t, s.Handler(), p); strings.Contains(body, "demo-banner") || strings.Contains(body, "Demo mode") {
			t.Errorf("%s: no banner outside demo mode; body:\n%s", p, body)
		}
	}

	s.DemoDir = `C:\Users\me\AppData\Local\Temp\watchglass-demo`
	want := `<div class="demo-banner" role="note">
  <p class="demo-banner-text"><strong>Demo mode:</strong> changes are kept in <span class="mono">C:\Users\me\AppData\Local\Temp\watchglass-demo</span> and reset on restart. To get alerts on your phone, paste your ntfy URL into a watch's Notify box and save.</p>
</div>`
	for _, p := range []string{"/", "/watch/printer"} {
		_, body := get(t, s.Handler(), p)
		body = strings.ReplaceAll(body, "\r\n", "\n")
		if !strings.Contains(body, want) {
			t.Errorf("%s: banner missing; body:\n%s", p, body)
		}
		// Between the topbar and main, so it spans the page like the bar.
		if i, j, k := strings.Index(body, "</header>"), strings.Index(body, "demo-banner"), strings.Index(body, `<main id="main"`); !(i < j && j < k) {
			t.Errorf("%s: banner should sit between the topbar and main", p)
		}
	}
}

// Every response says it is watchglass, a 401 from auth and a 404 included,
// so `watchglass -healthcheck` and a double-clicked second copy can tell
// this server from another program holding the port.
func TestEveryResponseCarriesTheIdentityHeader(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	for _, p := range []string{"/", "/watch/printer", "/no-such-page", "/static/style.css"} {
		if resp, _ := get(t, h, p); resp.Header.Get(IdentityHeader) != "1" {
			t.Errorf("%s: %s = %q, want 1", p, IdentityHeader, resp.Header.Get(IdentityHeader))
		}
	}
	s.mu.Lock()
	s.cfg.Auth = &config.Auth{Username: "u", Password: "p"}
	s.mu.Unlock()
	resp, _ := get(t, h, "/")
	if resp.StatusCode != 401 || resp.Header.Get(IdentityHeader) != "1" {
		t.Errorf("auth: status %d, %s = %q; want 401 with the header", resp.StatusCode, IdentityHeader, resp.Header.Get(IdentityHeader))
	}
}

// The empty state offers the built-in demo camera to anyone without one: a
// second, secondary form that creates demo-printer through the ordinary
// create route, which lands on its detail page like any other watch.
func TestEmptyStateOffersADemoWatch(t *testing.T) {
	s, _ := newTestServer(t)
	s.NewSource = source.For // the real factory: demo:printer needs nothing installed
	h := s.Handler()
	if resp, _ := postForm(t, h, "/watch/printer/delete", url.Values{}); resp.StatusCode != 303 {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	_, body := get(t, h, "/")
	offer := `<form class="demo-offer" method="post" action="/watch/new">
    <input type="hidden" name="name" value="demo-printer">
    <input type="hidden" name="source" value="demo:printer">
    <p class="demo-offer-text">No camera? Try it on the 3D printer screen built into watchglass.</p>
    <button type="submit" class="btn btn-outline">Add a demo watch</button>
  </form>`
	if !strings.Contains(strings.ReplaceAll(body, "\r\n", "\n"), offer) {
		t.Fatalf("empty state should offer a demo watch; body:\n%s", body)
	}

	resp, _ := postForm(t, h, "/watch/new", url.Values{"name": {"demo-printer"}, "source": {"demo:printer"}})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/watch/demo-printer" {
		t.Fatalf("demo create: status %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	w, ok := s.findWatch("demo-printer")
	if !ok || w.Source != "demo:printer" {
		t.Fatalf("demo-printer not saved as a demo:printer watch: %+v", w)
	}
	// The snapshot is the built-in camera's frame, no network involved.
	resp, png := get(t, h, "/watch/demo-printer/snapshot")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(png, "\x89PNG") {
		t.Errorf("demo snapshot: status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// With watches, the offer is gone: it belongs to the first run only.
	if _, body = get(t, h, "/"); strings.Contains(body, "demo-offer") {
		t.Errorf("the demo offer is for the empty state only; body:\n%s", body)
	}

	// index.js gives it the same busy state as Create.
	js, err := assets.ReadFile("static/index.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`document.querySelector(".demo-offer")`, `busyOnSubmit(demoForm, demoBtn, "Adding…")`, "unbusy(demoBtn, demoLabel)"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("index.js missing %q", want)
		}
	}
}

// A demo source other than the two scenes is refused at create, in words
// that name the two that exist.
func TestUnknownDemoSourceIsRefused(t *testing.T) {
	s, _ := newTestServer(t)
	resp, body := postForm(t, s.Handler(), "/watch/new", url.Values{"name": {"d"}, "source": {"demo:camera"}})
	if resp.StatusCode != 400 || !strings.Contains(body, "A demo source is demo:printer or demo:sevenseg.") {
		t.Errorf("status %d; body:\n%s", resp.StatusCode, body)
	}
	if _, err := config.SourceKind("demo:sevenseg"); err != nil {
		t.Errorf("demo:sevenseg should be a valid source: %v", err)
	}
}
