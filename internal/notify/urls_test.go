package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	discordToken = "Xa1b2C3d4E5f6G7h8I9j0KlMnOpQrStUvWxYz-AbCdEf_123456"
	slackA       = "T01ABCDEF2"
	slackB       = "B02GHIJKL3"
	slackC       = "abcdefghijklmnopqrstuvwx"
)

func TestSuggestRewrites(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://ntfy.sh/my-printer", "ntfy://ntfy.sh/my-printer"},
		{"http://ntfy.sh/my-printer", "ntfy://ntfy.sh/my-printer"},
		{"ntfy.sh/my-printer", "ntfy://ntfy.sh/my-printer"},
		{"https://discord.com/api/webhooks/123456789012345678/" + discordToken, "discord://" + discordToken + "@123456789012345678"},
		{"https://discordapp.com/api/webhooks/123456789012345678/" + discordToken + "?thread_id=42", "discord://" + discordToken + "@123456789012345678?thread_id=42"},
		{"https://hooks.slack.com/services/" + slackA + "/" + slackB + "/" + slackC, "slack://hook:" + slackA + "-" + slackB + "-" + slackC + "@webhook"},
		{"http://127.0.0.1:1/x", "generic+http://127.0.0.1:1/x?template=json"},
		{"https://example.com/hook?a=1", "generic+https://example.com/hook?a=1&template=json"},
		{"http://192.168.1.10:9000/hook?template=json", "generic+http://192.168.1.10:9000/hook?template=json"},
		{"192.168.1.10:8081/topic", "generic+http://192.168.1.10:8081/topic?template=json"},
		{"localhost:9000/hook", "generic+http://localhost:9000/hook?template=json"},
	}
	for _, c := range cases {
		got, ok := Suggest(c.in)
		if !ok || got != c.want {
			t.Errorf("Suggest(%q) = %q, %v; want %q", c.in, got, ok, c.want)
			continue
		}
		// Every rewrite is a URL the watch can actually start with.
		if p := Check(got); p != nil {
			t.Errorf("Suggest(%q) = %q, which Check refuses: %s", c.in, got, p.Reason)
		}
		if _, err := NewShoutrrr([]string{got}); err != nil {
			t.Errorf("NewShoutrrr(%q): %v", got, err)
		}
	}
	for _, in := range []string{"ntfy://ntfy.sh/x", "discord://tok@123", "generic+http://h/x", "nope://x", "printer", "",
		// Not web addresses: an email address must not become a webhook
		// POST to gmail.com with the address as its login.
		"me@gmail.com", "mailto:foo@bar.com", "tel:+15551234", "user:pw@192.168.1.10:9000/hook"} {
		if got, ok := Suggest(in); ok {
			t.Errorf("Suggest(%q) = %q, want no suggestion", in, got)
		}
	}
}

func TestCheckExplainsEachBadURL(t *testing.T) {
	cases := []struct{ in, reason, fix string }{
		{"https://ntfy.sh/x", "That's the ntfy web address", "ntfy://ntfy.sh/x"},
		{"https://ntfy.sh/", "without a topic", ""},
		{"https://discord.com/api/webhooks/123456789012345678/" + discordToken, "Discord webhook link", "discord://" + discordToken + "@123456789012345678"},
		{"http://127.0.0.1:1/x", "use generic+http://. If it's your own ntfy server, use ntfy+http://", "generic+http://127.0.0.1:1/x?template=json"},
		{"ntfy://ntfy.sh/", "No topic: add a topic name after the slash", ""},
		{"ntfy://ntfy.sh", "No topic", ""},
		{"ntfy+http://192.168.1.10:8081/", "No topic", ""},
		{"ntfy:///topic", "No server", ""},
		{"nope://x", `"nope" isn't a notification service`, ""},
		{"discord://@123", "The discord URL doesn't work: token missing from config URL.", ""},
		{"generic+http://", "Put the server's address after generic+http://", ""},
		{"just words", "It needs a service at the front", ""},
		// The token is the part shoutrrr complains about; masked, it said
		// "The telegram URL doesn't work: •••:." Say what a token looks like.
		{"telegram://12345abc@telegram?chats=@x", "bot token isn't in the right form. Use the token @BotFather gave you", ""},
		{"telegram://BOT_TOKEN@telegram?chats=@channel", "bot token isn't in the right form", ""},
		// An email address is not a web address to post to.
		{"me@gmail.com", "That's an email address. For alerts by email, use the smtp:// form", ""},
		{"mailto:me@example.com", "That's an email address.", ""},
	}
	for _, c := range cases {
		p := Check(c.in)
		if p == nil {
			t.Errorf("Check(%q) = nil, want a problem", c.in)
			continue
		}
		if !strings.Contains(p.Reason, c.reason) || p.Fix != c.fix {
			t.Errorf("Check(%q) = {%q, %q}, want reason containing %q and fix %q", c.in, p.Reason, p.Fix, c.reason, c.fix)
		}
		if strings.Contains(p.Reason, discordToken) || strings.Contains(p.Reason, mask) {
			t.Errorf("Check(%q) reason leaks the token or reads as a mask: %q", c.in, p.Reason)
		}
		// The watch's own start refuses exactly these too.
		if _, err := NewShoutrrr([]string{c.in}); err == nil {
			t.Errorf("NewShoutrrr(%q) accepted a URL Check refuses", c.in)
		}
	}
	for _, ok := range []string{"ntfy://ntfy.sh/x", "ntfy+http://192.168.1.10:8081/topic", "generic+http://127.0.0.1:9/hook?template=json",
		"discord://" + discordToken + "@123456789012345678", "telegram://123456:ABCdef@telegram?chats=@channel"} {
		if p := Check(ok); p != nil {
			t.Errorf("Check(%q) = %q, want nil", ok, p.Reason)
		}
	}
}

func TestLabelRedactScrubKeepCredentialsOut(t *testing.T) {
	u := "discord://" + discordToken + "@123456789012345678"
	if got := Label(u); got != "discord://123456789012345678" {
		t.Errorf("Label = %q", got)
	}
	if got := Label("ntfy+http://user:pw@10.0.0.2:81/secret-topic"); got != "ntfy+http://10.0.0.2:81" {
		t.Errorf("Label = %q", got)
	}
	if got := Label("ntfy.sh/secret-topic"); got != "ntfy.sh" {
		t.Errorf("Label of a scheme-less URL = %q", got)
	}
	// url.Parse reads "localhost:" as a scheme; it's a host and port.
	for in, want := range map[string]string{"localhost:9000/hook": "localhost:9000", "192.168.1.10:9000/x": "192.168.1.10:9000",
		"mailto:me@example.com": "mailto:", "tel:+15551234": "tel:"} {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Redact(u); got != "discord://•••@123456789012345678" {
		t.Errorf("Redact = %q", got)
	}
	if got := Redact("generic+https://h/hook?template=json&@Authorization=Bearer%20abc&token=zzz"); got != "generic+https://h/hook?template=json&@Authorization=•••&token=•••" {
		t.Errorf("Redact query = %q", got)
	}
	raw := `generic: sending HTTP request: Post "https://discord.com/api/webhooks/123456789012345678/` + discordToken + `": dial tcp: lookup discord.com: no such host`
	got := Scrub(raw, []string{"https://discord.com/api/webhooks/123456789012345678/" + discordToken})
	if strings.Contains(got, discordToken) || !strings.Contains(got, `Post "https://discord.com"`) || !strings.Contains(got, "no such host") {
		t.Errorf("Scrub = %q", got)
	}
	// A credential outside any URL is masked too.
	if got := Scrub("token "+discordToken+" refused", []string{u}); strings.Contains(got, discordToken) {
		t.Errorf("Scrub left a bare token: %q", got)
	}
	if got := Scrub("status 404", nil); got != "status 404" {
		t.Errorf("Scrub changed a URL-less message: %q", got)
	}
	// Slack's rewrite has the fixed username "hook"; masking it turned
	// every "webhook" into "web•••". The token is still masked.
	slack, _ := Suggest("https://hooks.slack.com/services/" + slackPlaceholder)
	if got, want := Scrub("line 1 of 1 (slack://webhook): Post to webhook failed: status 404 (T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX)", []string{slack}),
		"line 1 of 1 (slack://webhook): Post to webhook failed: status 404 (•••)"; got != want {
		t.Errorf("Scrub of a Slack error\n got %q\nwant %q", got, want)
	}
	// Pushover's fixed username is 8 letters, as long as a short token.
	push := "pushover://shoutrrr:azGDORePK8gMaC0QOYAMyEEuzJnyUi@uQiRzpo4DXghDmr9QzzfQu27cmVRsG/"
	if got, want := Scrub("line 1 of 1 (pushover://x): shoutrrr: status 400 azGDORePK8gMaC0QOYAMyEEuzJnyUi", []string{push}),
		"line 1 of 1 (pushover://x): shoutrrr: status 400 •••"; got != want {
		t.Errorf("Scrub of a Pushover error\n got %q\nwant %q", got, want)
	}
	// A short login name isn't a credential, and a host is never masked,
	// even when the password is part of it; the password itself still is.
	gen := "generic+http://admin:nas@nas.local/hook"
	if got, want := Scrub(`line 1 of 1 (generic+http://nas.local): Post "http://admin:nas@nas.local/hook": 401 for admin, password nas`, []string{gen}),
		`line 1 of 1 (generic+http://nas.local): Post "http://nas.local": 401 for admin, password •••`; got != want {
		t.Errorf("Scrub kept the host?\n got %q\nwant %q", got, want)
	}
}

// Each line of a list succeeds or fails on its own, and a failure names
// its line and host, never the topic or the rest of the URL.
func TestSendReportsFailuresPerLine(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	n, err := NewShoutrrr([]string{
		"generic+" + srv.URL + "/hook",
		"ntfy+" + srv.URL + "/missing-secret-topic",
		"generic://" + host + "/tls-mismatch",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = n.Send(context.Background(), "t", "b")
	var se *SendError
	if !errors.As(err, &se) {
		t.Fatalf("Send = %v, want a *SendError", err)
	}
	if se.Total != 3 || len(se.Failures) != 2 || se.Failures[0].Line != 2 || se.Failures[1].Line != 3 {
		t.Fatalf("failures = %+v", se)
	}
	msg := err.Error()
	for _, want := range []string{"line 2 of 3 (ntfy+http://" + host + "): ntfy: status 404", "line 3 of 3 (generic://" + host + "): ", "HTTP response to HTTPS client"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "secret-topic") || strings.Contains(msg, "tls-mismatch") {
		t.Errorf("error leaks a path: %q", msg)
	}
}

func TestNewShoutrrrNamesTheLineNotTheURL(t *testing.T) {
	_, err := NewShoutrrr([]string{"ntfy://ntfy.sh/ok", "https://discord.com/api/webhooks/1/" + discordToken})
	if err == nil {
		t.Fatal("want an error")
	}
	if msg := err.Error(); !strings.HasPrefix(msg, "line 2 (https://discord.com): That's a Discord webhook link.") || strings.Contains(msg, discordToken) {
		t.Errorf("error = %q", msg)
	}
	if _, err := NewShoutrrr([]string{"ntfy://ntfy.sh/"}); err == nil || !strings.Contains(err.Error(), "No topic") {
		t.Errorf("empty ntfy topic: err = %v", err)
	}
}

// slackPlaceholder is the shape of Slack's documented example webhook path,
// built at run time so secret scanners don't read a webhook URL in the source.
var slackPlaceholder = "T00000000" + "/" + "B00000000" + "/" + strings.Repeat("X", 24)
