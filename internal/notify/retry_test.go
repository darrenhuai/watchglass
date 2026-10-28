package notify

import "testing"

func TestTransient(t *testing.T) {
	for _, c := range []struct {
		msg  string
		want bool
	}{
		{`ntfy: Put "http://127.0.0.1:8669": dial tcp 127.0.0.1:8669: connectex: No connection could be made because the target machine actively refused it.`, true},
		{`generic: failed to send notification to generic webhook: sending HTTP request: Post "http://h": dial tcp: connection refused`, true},
		{`Post "http://nas.local": dial tcp: lookup nas.local: no such host`, true},
		{`failed to send: context deadline exceeded (Client.Timeout exceeded while awaiting headers)`, true},
		{"ntfy: status 503", true},
		{"ntfy: status 502", true},
		{"ntfy: status 429", true},
		{"ntfy: status 408", true},
		{"failed: status code: 500", true},
		{"got unexpected HTTP status: 503 Service Unavailable", true},
		{"read tcp: wsarecv: An existing connection was forcibly closed by the remote host.", true},
		{"unexpected EOF", true},
		{"ntfy: status 404", false},
		{"ntfy: status 401", false},
		{"ntfy: status 403", false},
		{"failed: status code: 400", false},
		{"got unexpected HTTP status: 401 Unauthorized", false},
		{`Post "https://h": tls: failed to verify certificate: x509: certificate signed by unknown authority`, false},
		{`Post "https://h": http: server gave HTTP response to HTTPS client`, false},
		// Answers with no status code: retried only when they say "busy".
		// Telegram's error is the API's description alone, Slack's its
		// error code, and a refused token is the same tomorrow.
		{"failed to send telegram notification: Unauthorized", false},
		{"failed to send telegram notification: Bad Request: chat not found", false},
		{"failed to send slack notification: api response failure: invalid_token", false},
		{"failed to send telegram notification: Too Many Requests: retry after 35", true},
		{"failed to send discord notification: rate limited by Discord", true},
		{"failed to send telegram notification: Bad Gateway", true},
		{"Message exceeds the max length", false},
		{`Post "http://h/x": context canceled`, false},
		{"dial tcp 10.0.0.2:8080: i/o timeout", true},
		{"dial tcp: lookup ntfy.lan on 127.0.0.53:53: server misbehaving", true},
		{"dial tcp 10.0.0.2:80: connect: no route to host", true},
		// A mail server: 4xx is "later", 5xx is final, whatever HTTP says.
		{"failed to send notification to smtp: error authenticating: 535 5.7.8 Username and Password not accepted", false},
		{"error sending message to recipient \"a@b\": 550 5.1.1 User unknown", false},
		{"smtp: 421 4.7.0 Try again later", true},
		{"error connecting to server: dial tcp 10.0.0.9:587: connect: connection refused", true},
	} {
		if got := Transient(c.msg); got != c.want {
			t.Errorf("Transient(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// With the URL known to be smtp, a reply without an enhanced code is
// still read as a mail server's: "535 Authentication failed" is final,
// though HTTP would retry a 535, and "421 Service not available" is
// "later", though HTTP would give up on a 421.
func TestTransientAtSMTP(t *testing.T) {
	const mail = "smtp://smtp.example.com:587"
	for _, c := range []struct {
		msg   string
		want  bool
		short string
	}{
		{"error authenticating: 535 Authentication failed", false, "535 Authentication failed"},
		{"error sending message to recipient: 554 Transaction failed", false, "554 Transaction failed"},
		{"error enabling StartTLS: 421 Service not available", true, "421 Service not available"},
		{"error connecting to server: dial tcp 10.0.0.9:587: i/o timeout", true, "timed out"},
	} {
		if got := TransientAt(mail, c.msg); got != c.want {
			t.Errorf("TransientAt(smtp, %q) = %v, want %v", c.msg, got, c.want)
		}
		if got := ShortCauseAt(mail, c.msg); got != c.short {
			t.Errorf("ShortCauseAt(smtp, %q) = %q, want %q", c.msg, got, c.short)
		}
	}
	if got := Describe(mail, "421 Service not available"); got != mail+" answered: 421 Service not available" {
		t.Errorf("Describe = %q", got)
	}
	if !TransientAt("ntfy+http://h", "status 503") || TransientAt("ntfy+http://h", "status 421") {
		t.Error("an HTTP URL's codes are read the HTTP way")
	}
}

func TestShortCauseAndDescribe(t *testing.T) {
	for _, c := range []struct{ msg, short, describe string }{
		{`ntfy: Put "http://127.0.0.1:8669": dial tcp 127.0.0.1:8669: connectex: No connection could be made because the target machine actively refused it.`,
			"connection refused", "couldn't reach ntfy+http://127.0.0.1:8669 (connection refused)"},
		{"ntfy: status 503", "HTTP 503", "ntfy+http://127.0.0.1:8669 answered HTTP 503"},
		{"got unexpected HTTP status: 429 Too Many Requests", "HTTP 429", "ntfy+http://127.0.0.1:8669 answered HTTP 429"},
		{`Post "http://nas.local": dial tcp: lookup nas.local: no such host`, "no such host", "couldn't reach ntfy+http://127.0.0.1:8669 (no such host)"},
		{"failed to send: timed out", "timed out", "couldn't reach ntfy+http://127.0.0.1:8669 (timed out)"},
		// An answer in words is an answer: the service was reached.
		{"something the library said: with a part at the end", "with a part at the end", "ntfy+http://127.0.0.1:8669 answered: with a part at the end"},
		{"failed to send telegram notification: Too Many Requests: retry after 35", "rate limited", "ntfy+http://127.0.0.1:8669 answered: rate limited"},
		{"read tcp 10.0.0.2:51000->10.0.0.9:80: wsarecv: An established connection was aborted by the software in your host machine.", "connection reset", "couldn't reach ntfy+http://127.0.0.1:8669 (connection reset)"},
	} {
		if got := ShortCause(c.msg); got != c.short {
			t.Errorf("ShortCause(%q) = %q, want %q", c.msg, got, c.short)
		}
		if got := Describe("ntfy+http://127.0.0.1:8669", c.msg); got != c.describe {
			t.Errorf("Describe(%q) = %q, want %q", c.msg, got, c.describe)
		}
	}
	if got := Describe("", "ntfy: status 503"); got != "the service answered HTTP 503" {
		t.Errorf("Describe with no label = %q", got)
	}
	if got := Describe("", "dial tcp: connection refused"); got != "couldn't reach the service (connection refused)" {
		t.Errorf("Describe with no label = %q", got)
	}
}

// A real per-line send: Lines names each URL, SendLine reaches only the
// one asked for, and its error carries no more of the URL than Scrub
// leaves.
func TestShoutrrrLines(t *testing.T) {
	s, err := NewShoutrrr([]string{"ntfy+http://127.0.0.1:1/secret-topic", "generic+http://127.0.0.1:1/hook?token=abcdefghijklmnop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Lines(); len(got) != 2 || got[0] != "ntfy+http://127.0.0.1:1" || got[1] != "generic+http://127.0.0.1:1" {
		t.Errorf("Lines = %q", got)
	}
	for line := 0; line < 2; line++ {
		err := s.SendLine(t.Context(), line, "t", "b", nil)
		if err == nil {
			t.Fatalf("line %d: sent to port 1?", line)
		}
		if msg := err.Error(); !Transient(msg) {
			t.Errorf("line %d: %q should be worth another try", line, msg)
		} else if contains(msg, "secret-topic") || contains(msg, "abcdefghijklmnop") {
			t.Errorf("line %d: the error carries the secret: %q", line, msg)
		}
	}
	if err := s.SendLine(t.Context(), 2, "t", "b", nil); err == nil {
		t.Error("line 3 of 2 sent?")
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
