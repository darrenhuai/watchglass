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

// A2: the retrying line names what else ends the tries. A match or
// number watch's alert is let go when its condition clears; a change
// watch (pixel_change, ocr_changed) has no condition, so its alert is
// kept until a newer change replaces it, and the line must not name a
// condition that doesn't exist.
func TestRetryingLineNamesWhatEndsTheTries(t *testing.T) {
	esc := strings.NewReplacer("'", "&#39;", "+", "&#43;").Replace
	for _, c := range []struct {
		ttype, pattern, want string
	}{
		{"ocr_match", "(?i)print complete", "is kept until it gets through or the condition clears (1 try so far)."},
		{"numeric", "([0-9.]+)", "is kept until it gets through or the condition clears (1 try so far)."},
		{"ocr_changed", "", "is kept until it gets through or a newer change replaces it (1 try so far)."},
		{"pixel_change", "", "is kept until it gets through or a newer change replaces it (1 try so far)."},
	} {
		t.Run(c.ttype, func(t *testing.T) {
			s, _ := newTestServer(t)
			h := s.Handler()
			s.sup.NewSource = func(config.Watch) (source.Source, error) { return blockSource{}, nil }
			form := saveForm("generic+http://h/hook?template=json")
			form.Set("ttype", c.ttype)
			form.Set("engine", "sevenseg")
			if c.pattern != "" {
				form.Set("pattern", c.pattern)
			}
			if c.ttype == "numeric" {
				form.Set("op", "gt")
			}
			if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != 303 {
				t.Fatalf("save: %d %s", resp.StatusCode, body)
			}
			t0 := time.Date(2026, 9, 24, 14, 2, 5, 0, time.Local)
			s.reg.Add("printer", state.Sample{TS: t0, Reading: "PRINT COMPLETE", Fired: true})
			s.reg.SetDelivery("printer", state.Delivery{TS: t0, Kind: "fired", Tries: 1, NextTry: time.Now().Add(30 * time.Second),
				Retrying: "line 1 of 1 (generic+http://h): HTTP 503"})
			_, live := get(t, h, "/watch/printer/live")
			if !strings.Contains(live, esc(c.want)) {
				t.Errorf("live lacks %q:\n%s", c.want, live)
			}
		})
	}
}

// A2: while a notify URL that didn't take the alert is being tried
// again, the Live panel says why and when, the list marks the watch
// "retrying alert", and once it is through the line counts the tries.
func TestDeliveryRetryingOnLiveAndIndex(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	s.sup.NewSource = func(config.Watch) (source.Source, error) { return blockSource{}, nil }
	if resp, body := postForm(t, h, "/watch/printer/save", saveForm("ntfy+http://10.0.0.2:81/topic\ngeneric+http://h/hook?template=json")); resp.StatusCode != 303 {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	t0 := time.Date(2026, 9, 24, 14, 2, 5, 0, time.Local)
	s.reg.Add("printer", state.Sample{TS: t0, Reading: "PRINT COMPLETE", Fired: true})
	s.reg.SetSending("printer", t0)
	s.reg.SetDelivery("printer", state.Delivery{TS: t0, Kind: "fired", Tries: 2, NextTry: time.Now().Add(30 * time.Second),
		Retrying: "line 1 of 2 (ntfy+http://10.0.0.2:81): connection refused; line 2 of 2 (generic+http://h): HTTP 503"})
	// html/template writes ' and + as entities.
	esc := strings.NewReplacer("'", "&#39;", "+", "&#43;").Replace
	_, live := get(t, h, "/watch/printer/live")
	for _, want := range []string{
		`data-delivery="retrying"`,
		`<span class="tag tag-fired">fired · retrying</span>`,
		esc(`<p class="delivery delivery-retrying">Couldn't reach line 1 (ntfy+http://10.0.0.2:81) (connection refused), line 2 (generic+http://h) answered HTTP 503; trying again in 30 s. The alert from <time class="mono" datetime="` + isoTime(t0) + `">14:02:05</time> is kept until it gets through or a newer change replaces it (2 tries so far).</p>`),
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live lacks %q:\n%s", want, live)
		}
	}
	if strings.Contains(live, "Sending the alert") || strings.Contains(live, "could not be delivered") {
		t.Errorf("a retry is neither sending nor failed:\n%s", live)
	}
	_, index := get(t, h, "/")
	if !strings.Contains(index, esc(`<span class="alerts-failing alerts-retrying" title="Couldn't reach line 1 (ntfy+http://10.0.0.2:81) (connection refused), line 2 (generic+http://h) answered HTTP 503; trying again in 30 s. The alert from 14:02:05 is not delivered yet (2 tries so far).">retrying alert</span>`)) {
		t.Errorf("index lacks the marker:\n%s", index)
	}

	// One line took it, one was given up on, one is still being tried:
	// the line says both, and the wait counts down to "now".
	s.reg.SetDelivery("printer", state.Delivery{TS: t0, Kind: "fired", Tries: 3, NextTry: time.Now().Add(-time.Second),
		Retrying: "line 2 of 2 (generic+http://h): HTTP 503", Err: "line 1 of 2 (ntfy+http://10.0.0.2:81): ntfy: status 404"})
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, esc(`Line 2 (generic+http://h) answered HTTP 503; trying again now. The alert from <time class="mono" datetime="`+isoTime(t0)+`">14:02:05</time> is kept until it gets through or a newer change replaces it (3 tries so far). Line 1 (ntfy+http://10.0.0.2:81) answered HTTP 404: check the topic and the server address.</p>`)) {
		t.Errorf("live with a line given up on:\n%s", live)
	}

	// A down alert is kept while the camera is down.
	s.reg.SetDelivery("printer", state.Delivery{TS: t0.Add(time.Second), Kind: "down", Tries: 1, NextTry: time.Now().Add(100 * time.Second),
		Retrying: "line 1 of 2 (ntfy+http://10.0.0.2:81): timed out; line 2 of 2 (generic+http://h): timed out"})
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, esc(`Couldn't reach line 1 (ntfy+http://10.0.0.2:81) (timed out), couldn't reach line 2 (generic+http://h) (timed out); trying again in 1 min 40 s. The alert (stream down) from <time class="mono" datetime="`+isoTime(t0.Add(time.Second))+`">14:02:06</time> is kept until it gets through or the camera comes back (1 try so far).</p>`)) {
		t.Errorf("live with a down alert waiting:\n%s", live)
	}

	// Through on the third try: sent, and the line says when it went out
	// (not when it was raised) and after how many tries.
	t1 := t0.Add(time.Minute)
	s.reg.Add("printer", state.Sample{TS: t1, Reading: "PRINT COMPLETE", Fired: true})
	s.reg.SetDelivery("printer", state.Delivery{TS: t1, Kind: "fired", OK: true, Tries: 3, SentAt: t1.Add(100 * time.Second)})
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, `fired · sent</span>`) || strings.Contains(live, "delivery-retrying") ||
		!strings.Contains(live, `Last alert, from <time class="mono" datetime="`+isoTime(t1)+`">14:03:05</time>, went out at <time class="mono" datetime="`+isoTime(t1.Add(100*time.Second))+`">14:04:45</time> after 3 tries.</p>`) {
		t.Errorf("after the retry got through:\n%s", live)
	}
	if _, index = get(t, h, "/"); strings.Contains(index, "retrying alert") || strings.Contains(index, "alerts failing") {
		t.Error("the marker outlived the delivery")
	}
	// A first-try send doesn't count tries.
	s.reg.SetDelivery("printer", state.Delivery{TS: t1, Kind: "fired", OK: true, Tries: 1})
	if _, live = get(t, h, "/watch/printer/live"); !strings.Contains(live, `14:03:05</time>.</p>`) || strings.Contains(live, "tries") {
		t.Errorf("a first-try send:\n%s", live)
	}
}

func TestRetryText(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{"line 1 of 1 (ntfy+http://10.0.0.2:81): connection refused", "Couldn't reach ntfy+http://10.0.0.2:81 (connection refused)"},
		{"line 1 of 1 (ntfy+http://10.0.0.2:81): HTTP 503", "ntfy+http://10.0.0.2:81 answered HTTP 503"},
		{"line 1 of 3 (ntfy://ntfy.sh): HTTP 429; line 3 of 3 (generic+http://h): no such host",
			"Line 1 (ntfy://ntfy.sh) answered HTTP 429, couldn't reach line 3 (generic+http://h) (no such host)"},
		{"status 502", "The service answered HTTP 502"},
		{"dial tcp: connection refused", "Couldn't reach the service (connection refused)"},
	} {
		if got := retryText(c.raw); got != c.want {
			t.Errorf("retryText(%q)\n got %q\nwant %q", c.raw, got, c.want)
		}
	}
}

// An alert let go before a URL took it (the condition cleared first)
// says so in its own words, names the URL once, with its cause and the
// tries, and gives no advice to check the URL. The list marks the watch.
func TestDeliveryDropped(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	s.sup.NewSource = func(config.Watch) (source.Source, error) { return blockSource{}, nil }
	if resp, body := postForm(t, h, "/watch/printer/save", saveForm("generic+http://127.0.0.1:8726/hook?template=json")); resp.StatusCode != 303 {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	t0 := time.Date(2026, 9, 24, 14, 2, 5, 0, time.Local)
	s.reg.Add("printer", state.Sample{TS: t0, Reading: "PRINT COMPLETE", Fired: true})
	s.reg.SetDelivery("printer", state.Delivery{TS: t0, Kind: "fired", Tries: 1, Dropped: "the condition cleared",
		Retrying: "line 1 of 1 (generic+http://127.0.0.1:8726): connection refused"})
	_, live := get(t, h, "/watch/printer/live")
	want := `<p class="delivery delivery-failed">Last alert at <time class="mono" datetime="` + isoTime(t0) + `">14:02:05</time> was not sent: the condition cleared before generic&#43;http://127.0.0.1:8726 (connection refused) took it, after 1 try.</p>`
	if !strings.Contains(live, want) || strings.Contains(live, "Send test notification") || strings.Contains(live, "delivery-retrying") {
		t.Errorf("live lacks %q:\n%s", want, live)
	}
	if !strings.Contains(live, `data-delivery="dropped"`) || !strings.Contains(live, "fired · not delivered") {
		t.Errorf("live: delivery state or fired tag:\n%s", live)
	}
	_, index := get(t, h, "/")
	if !strings.Contains(index, `title="Last alert at 14:02:05 was not sent: the condition cleared before generic&#43;http://127.0.0.1:8726 (connection refused) took it, after 1 try.">alerts failing</span>`) {
		t.Errorf("index lacks the marker:\n%s", index)
	}
}

// A2: Save & restart while an alert is still being tried. Nothing tries
// it any more, so the page stops saying "trying again" and the list stops
// saying "retrying alert"; for a fire of a watch with a condition it says
// the alert goes out again if the condition still holds (the restarted
// watch's re-send).
func TestSaveAndRestartEndsTheRetryingState(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	s.sup.NewSource = func(config.Watch) (source.Source, error) { return blockSource{}, nil }
	form := saveForm("ntfy+http://127.0.0.1:8723/topic")
	form.Set("ttype", "numeric")
	form.Set("engine", "sevenseg")
	form.Set("op", "gt")
	form.Set("tthreshold", "1000")
	form.Set("pattern", "([0-9.]+)")
	if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	t0 := time.Date(2026, 9, 24, 21, 1, 42, 0, time.Local)
	s.reg.Add("printer", state.Sample{TS: t0, Reading: "PRINT COMPLETE", Fired: true})
	s.reg.SetDelivery("printer", state.Delivery{TS: t0, Kind: "fired", Tries: 1, NextTry: time.Now().Add(10 * time.Second),
		Retrying: "line 1 of 1 (ntfy+http://127.0.0.1:8723): connection refused"})
	if _, live := get(t, h, "/watch/printer/live"); !strings.Contains(live, "trying again in") {
		t.Fatalf("before the save: no retrying line:\n%s", live)
	}
	// The same URLs, another interval: a plain Save & restart.
	form.Set("interval", "7s")
	if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	_, live := get(t, h, "/watch/printer/live")
	if strings.Contains(live, "trying again") || strings.Contains(live, "delivery-retrying") || strings.Contains(live, "is kept until") {
		t.Errorf("after Save & restart the page still says the alert is being tried:\n%s", live)
	}
	want := `was not sent: the watch was restarted before ntfy&#43;http://127.0.0.1:8723 (connection refused) took it, after 1 try. If the condition still holds, it goes out again.</p>`
	if !strings.Contains(live, want) {
		t.Errorf("live lacks %q:\n%s", want, live)
	}
	if _, index := get(t, h, "/"); strings.Contains(index, "retrying alert") {
		t.Errorf("the list still says retrying alert:\n%s", index)
	}

	// A pixel_change watch starts from what it sees after a restart: no
	// promise that the alert goes out again.
	s.reg.SetDelivery("printer", state.Delivery{TS: t0.Add(time.Minute), Kind: "fired", Tries: 2, NextTry: time.Now().Add(10 * time.Second),
		Retrying: "line 1 of 1 (ntfy+http://127.0.0.1:8723): connection refused"})
	form = saveForm("ntfy+http://127.0.0.1:8723/topic")
	if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	_, live = get(t, h, "/watch/printer/live")
	if !strings.Contains(live, "the watch was restarted before ntfy&#43;http://127.0.0.1:8723 (connection refused) took it, after 2 tries.</p>") || strings.Contains(live, "goes out again") {
		t.Errorf("pixel_change after Save & restart:\n%s", live)
	}
}

func TestDroppedText(t *testing.T) {
	for _, c := range []struct {
		why, raw string
		tries    int
		want     string
	}{
		{"the condition cleared", "line 1 of 1 (generic+http://h): HTTP 503", 1, "the condition cleared before generic+http://h (HTTP 503) took it, after 1 try."},
		{"the camera came back", "line 1 of 3 (ntfy://ntfy.sh): timed out; line 3 of 3 (generic+http://h): connection refused", 4,
			"the camera came back before ntfy://ntfy.sh (timed out) and generic+http://h (connection refused) took it, after 4 tries."},
		{"the watch was restarted", "connection refused", 2, "the watch was restarted before the notify URL (connection refused) took it, after 2 tries."},
	} {
		if got := droppedText(c.why, c.raw, c.tries); got != c.want {
			t.Errorf("droppedText(%q, %q)\n got %q\nwant %q", c.why, c.raw, got, c.want)
		}
	}
}
