package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
)

const discordToken = "Xa1b2C3d4E5f6G7h8I9j0KlMnOpQrStUvWxYz-AbCdEf_123456"

// saveForm is a valid detail-form save of the test server's printer watch
// with the given Notify box.
func saveForm(notify string) url.Values {
	return url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"},
		"tthreshold": {"10"}, "confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"}, "notify": {notify}}
}

// receiver records what a notify target was sent.
type receiver struct {
	mu    sync.Mutex
	got   []string
	srv   *httptest.Server
	start time.Time
}

func newReceiver(t *testing.T) *receiver {
	rc := &receiver{start: time.Now()}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.got = append(rc.got, r.URL.Path+" "+string(b))
		rc.mu.Unlock()
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *receiver) bodies() []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]string(nil), rc.got...)
}

// A03: Send test notification posts the unsaved box to every URL in it and
// says line by line whether it got there. Nothing is saved.
func TestTestNotifySendsTheUnsavedBoxAndSavesNothing(t *testing.T) {
	s, cfgPath := newTestServer(t)
	h := s.Handler()
	before, _ := os.ReadFile(cfgPath)
	rc := newReceiver(t)
	host := strings.TrimPrefix(rc.srv.URL, "http://")

	start := time.Now()
	resp, body := postForm(t, h, "/watch/printer/test-notify", url.Values{"notify": {"generic+" + rc.srv.URL + "/hook?template=json\n\n"}})
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("status %d; %s", resp.StatusCode, body)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("a reachable webhook took %v", d)
	}
	for _, want := range []string{`<p id="notify-test-title" class="notify-test-title">Test sent</p>`, `<li class="notify-row is-ok">`, `>✓</span>`,
		`<span class="mono">generic&#43;http://` + host + `</span>`, "Sent. Check that it arrived.", "Nothing was saved. Save to keep these URLs."} {
		if !strings.Contains(body, want) {
			t.Errorf("result lacks %q:\n%s", want, body)
		}
	}
	got := rc.bodies()
	if len(got) != 1 {
		t.Fatalf("receiver got %v", got)
	}
	var msg struct{ Title, Message string }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got[0], "/hook ")), &msg); err != nil ||
		msg.Title != "watchglass: printer (test)" || msg.Message != "Test from watchglass: printer can reach you." {
		t.Errorf("receiver got %q (%v)", got[0], err)
	}
	if after, _ := os.ReadFile(cfgPath); !bytes.Equal(before, after) {
		t.Error("a test send must not write the config file")
	}
	if w, _ := s.findWatch("printer"); len(w.Notify) != 0 {
		t.Errorf("a test send must not change the watch, notify = %v", w.Notify)
	}

	// Several lines: each is its own row, the plain-http server behind a
	// generic:// URL gets the https hint, a 404 names the path problem,
	// and a web address gets its rewrite (masked in the text, whole only in
	// the button that puts it back).
	box := strings.Join([]string{
		"generic+" + rc.srv.URL + "/hook",
		"generic://" + host + "/hook",
		"ntfy+" + rc.srv.URL + "/missing-topic",
		"https://discord.com/api/webhooks/123456789012345678/" + discordToken,
	}, "\n")
	_, body = postForm(t, h, "/watch/printer/test-notify", url.Values{"notify": {box}})
	for _, want := range []string{
		"Test reached 1 of 4",
		`<span class="notify-line">Line 2</span> <span class="mono">generic://` + host + `</span>`,
		"Speaks plain http, not https: use generic&#43;http://… instead.",
		"Answered HTTP 404: check the topic and the server address.",
		"That&#39;s a Discord webhook link.",
		`Suggested: <code class="notify-fix-url">discord://•••@123456789012345678</code> <button`,
		// The plain-http line gets its one obvious rewrite too.
		`data-line="2" data-fix="generic&#43;http://` + host + `/hook"`,
		`data-line="4" data-fix="discord://` + discordToken + `@123456789012345678"`,
		"Save to keep the URLs that worked.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("result lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "missing-topic") {
		t.Error("the result names a line by scheme://host, never its path")
	}
	if n := strings.Count(body, discordToken); n != 1 {
		t.Errorf("the token appears %d times, want only in the Use button's data-fix", n)
	}
	if after, _ := os.ReadFile(cfgPath); !bytes.Equal(before, after) {
		t.Error("a test send must not write the config file")
	}

	// A pasted ntfy web address is answered with the ntfy:// form, unsent.
	_, body = postForm(t, h, "/watch/printer/test-notify", url.Values{"notify": {"https://ntfy.sh/x"}})
	if !strings.Contains(body, "Test not delivered") || !strings.Contains(body, `Suggested: <code class="notify-fix-url">ntfy://ntfy.sh/x</code> <button`) {
		t.Errorf("https://ntfy.sh/x:\n%s", body)
	}

	for _, c := range []struct {
		notify string
		code   int
		want   string
	}{
		{"", 400, "add a notification URL first"},
		{"  \n ", 400, "add a notification URL first"},
		{strings.Repeat("ntfy://ntfy.sh/x\n", testNotifyMax+1), 400, "Test at most 10 URLs"},
	} {
		resp, body := postForm(t, h, "/watch/printer/test-notify", url.Values{"notify": {c.notify}})
		if resp.StatusCode != c.code || !strings.Contains(body, c.want) || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
			t.Errorf("notify %q: %d %q", c.notify, resp.StatusCode, body)
		}
	}
	if resp, _ := postForm(t, h, "/watch/nope/test-notify", url.Values{"notify": {"ntfy://ntfy.sh/x"}}); resp.StatusCode != 404 {
		t.Errorf("unknown watch: %d", resp.StatusCode)
	}
	// Cross-site pages can't make watchglass post anywhere.
	resp, _ = postFormWithHeaders(t, h, "/watch/printer/test-notify", url.Values{"notify": {"generic+" + rc.srv.URL + "/hook"}},
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site test send: %d, want 403", resp.StatusCode)
	}
}

// A04: a notify URL watchglass can't send to is refused before anything
// is written, with the reason and the likely rewrite, and the watch keeps
// running as it was.
func TestSaveRefusesUnusableNotifyURLs(t *testing.T) {
	for _, c := range []struct{ in, reason, shown, fix string }{
		{"https://ntfy.sh/x", "That&#39;s the ntfy web address.", "ntfy://ntfy.sh/x", "ntfy://ntfy.sh/x"},
		{"https://discord.com/api/webhooks/123456789012345678/" + discordToken, "That&#39;s a Discord webhook link.",
			"discord://•••@123456789012345678", "discord://" + discordToken + "@123456789012345678"},
		// html/template writes "+" as &#43;.
		{"http://127.0.0.1:1/x", "A plain web address isn&#39;t a notification service. To post to a webhook, use generic&#43;http://.",
			"generic&#43;http://127.0.0.1:1/x?template=json", "generic&#43;http://127.0.0.1:1/x?template=json"},
		{"ntfy://ntfy.sh/", "No topic: add a topic name after the slash, for example ntfy://ntfy.sh/my-topic.", "", ""},
	} {
		s, cfgPath := newTestServer(t)
		h := s.Handler()
		if err := s.sup.Start(context.Background(), s.Watches()[0]); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(cfgPath)
		st, _ := os.Stat(cfgPath)
		resp, body := postForm(t, h, "/watch/printer/save", saveForm("ntfy://ntfy.sh/fine\n"+c.in))
		if resp.StatusCode != 400 {
			t.Errorf("%s: status %d, want 400", c.in, resp.StatusCode)
		}
		if after, _ := os.ReadFile(cfgPath); !bytes.Equal(before, after) {
			t.Errorf("%s: config.yaml changed", c.in)
		}
		if st2, _ := os.Stat(cfgPath); !st2.ModTime().Equal(st.ModTime()) {
			t.Errorf("%s: config.yaml was touched", c.in)
		}
		if !s.isRunning("printer") {
			t.Errorf("%s: the watch stopped", c.in)
		}
		for _, want := range []string{
			`<ul id="err-notify" class="notify-problems">`,
			`<li class="field-error">Line 2: ` + c.reason,
			`<a href="#f-notify">Notify line 2: ` + c.reason,
			`aria-invalid="true" aria-describedby="err-notify notify-help"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: page lacks %q", c.in, want)
			}
		}
		if c.fix != "" {
			if want := `Suggested: <code class="notify-fix-url">` + c.shown + `</code> <button type="button" class="link-button notify-use" data-line="2" data-fix="` + c.fix + `">Use this</button>`; !strings.Contains(body, want) {
				t.Errorf("%s: page lacks the suggestion %q", c.in, want)
			}
		} else if strings.Contains(body, "notify-use") {
			t.Errorf("%s: no rewrite to offer, but the page offers one", c.in)
		}
	}
}

// blockSource never delivers a frame (it waits for the watch to stop), so
// a running watch adds nothing to the registry behind the test's back.
type blockSource struct{}

func (blockSource) Grab(ctx context.Context) (image.Image, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A05: the Live panel and the list say whether alerts get out, and a
// token in a notify URL never reaches either.
func TestDeliveryStatusOnLiveAndIndex(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	s.sup.NewSource = func(config.Watch) (source.Source, error) { return blockSource{}, nil }

	// No URLs: fires only show here.
	_, live := get(t, h, "/watch/printer/live")
	if !strings.Contains(live, `data-delivery="none"`) || !strings.Contains(live, `<p class="delivery delivery-none">No notification URLs: fires are only shown here. <a href="#f-notify">Add one under Notify</a>.</p>`) {
		t.Errorf("no-URL note missing:\n%s", live)
	}

	if resp, body := postForm(t, h, "/watch/printer/save", saveForm("discord://"+discordToken+"@123456789012345678")); resp.StatusCode != 303 {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	t0 := time.Date(2026, 9, 24, 14, 2, 5, 0, time.Local)
	s.reg.Add("printer", state.Sample{TS: t0, Reading: "PRINT COMPLETE", Fired: true})
	s.reg.SetSending("printer", t0)
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, `<span class="tag tag-fired">fired · sending</span>`) || !strings.Contains(live, `Sending the alert from <time class="mono"`) {
		t.Errorf("sending state:\n%s", live)
	}

	s.reg.SetDelivery("printer", state.Delivery{TS: t0, Kind: "fired",
		Err: `line 1 of 1 (discord://123456789012345678): failed to send: Post "https://discord.com": status code: 401`})
	_, live = get(t, h, "/watch/printer/live")
	for _, want := range []string{
		`data-delivery="failed"`,
		`<span class="tag tag-fired">fired · not delivered</span>`,
		`<p class="delivery delivery-failed">Last alert at <time class="mono" datetime="` + isoTime(t0) + `">14:02:05</time> could not be delivered: discord://123456789012345678 turned the request down (HTTP 401): check the token or password in the URL. Use <a href="#notify-test">Send test notification</a> to check the URL.</p>`,
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live lacks %q:\n%s", want, live)
		}
	}
	_, index := get(t, h, "/")
	if !regexp.MustCompile(`<span class="fired-ago">fired [^<]+</span>|<span class="tag tag-fired">fired</span>`).MatchString(index) ||
		!strings.Contains(index, `<span class="alerts-failing" title="Last alert at 14:02:05 could not be delivered: discord://123456789012345678 turned the request down (HTTP 401): check the token or password in the URL.">alerts failing</span>`) {
		t.Errorf("index lacks the marker:\n%s", index)
	}
	_, detail := get(t, h, "/watch/printer")
	for page, body := range map[string]string{"live": live, "index": index} {
		if strings.Contains(body, discordToken) {
			t.Errorf("%s shows the Discord token", page)
		}
	}
	if n := strings.Count(detail, discordToken); n != 1 {
		t.Errorf("the detail page holds the token %d times, want once (in the Notify box being edited)", n)
	}

	// The next alert goes through: both clear.
	t1 := t0.Add(time.Minute)
	s.reg.Add("printer", state.Sample{TS: t1, Reading: "PRINT COMPLETE", Fired: true})
	s.reg.SetDelivery("printer", state.Delivery{TS: t1, Kind: "fired", OK: true})
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, `fired · sent</span>`) || !strings.Contains(live, `<p class="delivery delivery-sent"><span class="delivery-mark" aria-hidden="true">✓</span>Last alert sent at`) || strings.Contains(live, "delivery-failed") {
		t.Errorf("after a good send:\n%s", live)
	}
	if _, index = get(t, h, "/"); strings.Contains(index, "alerts failing") {
		t.Error("the marker outlived a good send")
	}

	// A failure, then new URLs saved: the failure was about the old ones.
	s.reg.SetDelivery("printer", state.Delivery{TS: t1.Add(time.Minute), Kind: "down", Err: "status 500"})
	if _, index = get(t, h, "/"); !strings.Contains(index, "Last alert (stream down) at") {
		t.Errorf("a failed down alert should mark the row:\n%s", index)
	}
	if resp, _ := postForm(t, h, "/watch/printer/save", saveForm("ntfy://ntfy.sh/other")); resp.StatusCode != 303 {
		t.Fatal("save")
	}
	if _, ok := s.reg.GetDelivery("printer"); ok {
		t.Error("changing the URLs should forget the old delivery")
	}

	// The same when the save is written but the restart fails: the watch
	// is stopped with the new list, and the old list's failure isn't about it.
	s.reg.SetDelivery("printer", state.Delivery{TS: t1.Add(2 * time.Minute), Kind: "fired", Err: "status 404"})
	s.sup.NewSource = func(w config.Watch) (source.Source, error) { return nil, errors.New("camera went away") }
	if resp, body := postForm(t, h, "/watch/printer/save", saveForm("ntfy://ntfy.sh/third")); resp.StatusCode != 500 {
		t.Fatalf("save with a failing restart = %d:\n%s", resp.StatusCode, body)
	}
	if _, ok := s.reg.GetDelivery("printer"); ok {
		t.Error("a save whose restart failed kept the old URLs' delivery failure")
	}
}

func TestDeliveryText(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{"line 1 of 1 (ntfy+http://10.0.0.2:81): ntfy: status 404", "ntfy+http://10.0.0.2:81 answered HTTP 404: check the topic and the server address."},
		{`line 2 of 3 (generic://h:9): generic: sending HTTP request: Post "https://h:9": http: server gave HTTP response to HTTPS client`,
			"Line 2 (generic://h:9) speaks plain http, not https: use generic+http://… instead."},
		{`line 1 of 2 (ntfy://h): ntfy: Post "https://h": tls: first record does not look like a TLS handshake; line 2 of 2 (discord://1): failed: status code: 503`,
			"Line 1 (ntfy://h) speaks plain http, not https: use ntfy+http://… instead. Line 2 (discord://1) had a server error (HTTP 503)."},
		{`line 1 of 1 (generic+http://nas.local): Post "http://nas.local": dial tcp: lookup nas.local: no such host`, "generic+http://nas.local couldn't be found (no such host)."},
		{`line 1 of 1 (generic+http://h): failed to send: timed out`, "generic+http://h didn't answer in time."},
		{"line 1 of 1 (telegram://telegram): status 429", "telegram://telegram is limiting how often it takes messages (HTTP 429)."},
		{"line 1 of 1 (x://h): weird: thing broke", "x://h failed: thing broke."},
		{"not sent: the alerts before it were still waiting to go out", "Wasn't sent: the alerts before it were still waiting to go out."},
		// A 404 means something different for each kind of service.
		{"line 1 of 1 (discord://1122): failed to send discord notification: response status code 404 Not Found",
			"discord://1122 answered HTTP 404: check that the webhook still exists and the token in the URL is right."},
		{"line 1 of 1 (generic+http://h:9): generic: status 404", "generic+http://h:9 answered HTTP 404: check the path after the address."},
		// Pushover quotes its status; its text starts "failed to" already.
		{`line 1 of 1 (pushover://u): failed to send notification to pushover device: "", response status "400 Bad Request"`,
			"pushover://u answered HTTP 400."},
		{"line 1 of 1 (x://h): failed to reach the service: EOF", "x://h failed to reach the service: EOF."},
	} {
		if got := deliveryText(c.raw); got != c.want {
			t.Errorf("deliveryText(%q)\n got %q\nwant %q", c.raw, got, c.want)
		}
	}
	if got := deliveryText(`line 1 of 1 (generic+http://127.0.0.1:9): Post "http://127.0.0.1:9": connection refused`); !strings.Contains(got, "refused the connection") || !strings.Contains(got, "127.0.0.1 is that container") {
		t.Errorf("a refused loopback target should carry the container hint: %q", got)
	}
	for _, raw := range []string{
		`line 1 of 1 (ntfy://127.0.0.1:1): ntfy: Post "https://127.0.0.1:1/topic": dial tcp 127.0.0.1:1: connectex: No connection could be made because the target machine actively refused it.`,
		`line 1 of 1 (generic+http://localhost:9): Post "http://localhost:9": connection refused`,
	} {
		if got := deliveryText(raw); strings.Contains(got, "?.") || strings.Contains(got, "..") || !strings.Contains(got, "on that port? If watchglass runs in Docker") {
			t.Errorf("the refused sentence and its hint run together: %q", got)
		}
	}
}
