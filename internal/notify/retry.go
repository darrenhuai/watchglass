package notify

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Whether a failed delivery is worth another try, and the short words for
// it. The runner retries an alert at a notify URL that didn't take it
// (internal/runner/retry.go) and the web UI says why it is waiting; both
// read the failure's text, which is all the notification libraries give.

// statusRe finds the HTTP status code in a failure's text, in the forms
// the senders use: ntfy's "status 503", shoutrrr's "status code: 503" and
// "unexpected status code: 503 Service Unavailable", and a bare "503
// Service Unavailable".
var statusRe = regexp.MustCompile(`(?i)(?:status(?: code)?:?\s+"?|\bHTTP )(\d{3})\b|\b([45]\d\d) [A-Z][a-z]+`)

// smtpRe finds a mail server's reply in a failure's text: the code at the
// start of a part ("error authenticating: 535 5.7.8 Username and Password
// not accepted", "421 Service not available"). enhancedRe is the
// enhanced status code ("5.7.8") that marks a reply as a mail server's
// even when the URL isn't known.
var (
	smtpRe     = regexp.MustCompile(`(?:^|: )([2-5]\d\d)[ -]\S`)
	enhancedRe = regexp.MustCompile(`\b[2-5]\d\d[ -][245]\.\d{1,3}\.\d{1,3}\b`)
	eofRe      = regexp.MustCompile(`\beof\b`)
)

// StatusCode is the HTTP status code a failure's text carries, if any.
func StatusCode(msg string) (int, bool) {
	if enhancedRe.MatchString(msg) {
		return 0, false // a mail server's reply, not HTTP
	}
	m := statusRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	s := m[1]
	if s == "" {
		s = m[2]
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// smtpReply finds a mail server's answer in msg: the reply code and where
// its text starts. It looks for one when label (scheme://host) is an smtp
// URL or the text carries an enhanced status code. HTTP's reading of the
// same three digits would be wrong for it: a mail server's 5xx is final
// and its 4xx means "try again later".
func smtpReply(label, msg string) (code, at int, ok bool) {
	if !isSMTP(label) && !enhancedRe.MatchString(msg) {
		return 0, 0, false
	}
	m := smtpRe.FindStringSubmatchIndex(msg)
	if m == nil {
		return 0, 0, false
	}
	n, err := strconv.Atoi(msg[m[2]:m[3]])
	return n, m[2], err == nil
}

func isSMTP(label string) bool {
	u, err := url.Parse(label)
	return err == nil && strings.EqualFold(u.Scheme, "smtp")
}

// unreachable is whether msg says the service couldn't be reached at all,
// as against reached and said no: the connection was refused, reset or
// dropped, it timed out, the name didn't resolve, there was no route. The
// words are Go's and the operating systems' own, and ShortCause's.
func unreachable(msg string) bool {
	low := strings.ToLower(msg)
	for _, w := range []string{
		"refused", "no connection could be made",
		"connection reset", "forcibly closed", "connection aborted", "was aborted",
		"connection dropped", "broken pipe",
		"timed out", "timeout", "deadline exceeded", "did not properly respond",
		"no such host", "name resolution", "name or service not known", "server misbehaving",
		"unreachable", "no route to host", "network is down",
	} {
		if strings.Contains(low, w) {
			return true
		}
	}
	return eofRe.MatchString(low)
}

// busy is whether a service that answered said "not now" in words rather
// than with a status code (Telegram and Discord give only the words).
func busy(msg string) bool {
	low := strings.ToLower(msg)
	for _, w := range []string{
		"too many requests", "rate limit", "retry after", "try again later",
		"service unavailable", "bad gateway", "gateway timeout", "internal server error",
	} {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// Transient reports whether a delivery failure may fix itself, so the
// alert should be tried again (TransientAt, with no URL known).
func Transient(msg string) bool { return TransientAt("", msg) }

// TransientAt reports whether a delivery failure at label (scheme://host)
// may fix itself, so the alert should be tried again: the service
// couldn't be reached (the connection was refused, reset or dropped, it
// timed out, the name didn't resolve), it answered with a server error
// (HTTP 5xx), 429 (too many requests) or 408, it said it was busy in
// words, or a mail server answered 4xx. Anything else is not tried again:
// any other 4xx is the request's own fault (a wrong token, a topic that
// doesn't exist), a mail server's 5xx is its final word (a refused
// password, an unknown address), and so is an answer without a code that
// isn't about being busy (Telegram's "Unauthorized", Slack's
// "invalid_token"). A TLS certificate watchglass doesn't trust and a
// server that speaks plain http on an https URL stay wrong too.
func TransientAt(label, msg string) bool {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "certificate"), strings.Contains(low, "x509"),
		strings.Contains(low, "gave http response to https client"),
		strings.Contains(low, "first record does not look like a tls handshake"):
		return false
	}
	if code, _, ok := smtpReply(label, msg); ok {
		return code >= 400 && code < 500
	}
	if code, ok := StatusCode(msg); ok {
		return code >= 500 || code == 429 || code == 408
	}
	return unreachable(msg) || busy(msg)
}

// ShortCause is a failure in a few words, for a status line that must
// stay short (ShortCauseAt, with no URL known).
func ShortCause(msg string) string { return ShortCauseAt("", msg) }

// ShortCauseAt is a failure at label in a few words, for a status line
// that must stay short: "connection refused", "timed out", "no such
// host", "HTTP 503", "rate limited", or a mail server's reply ("421 4.7.0
// Try again later"). It never holds more of a URL than msg already does,
// and msg is expected scrubbed (Scrub).
func ShortCauseAt(label, msg string) string {
	low := strings.ToLower(msg)
	if _, at, ok := smtpReply(label, msg); ok {
		return clipCause(msg[at:])
	}
	if code, ok := StatusCode(msg); ok {
		return "HTTP " + strconv.Itoa(code)
	}
	switch {
	case strings.Contains(low, "refused"), strings.Contains(low, "no connection could be made"):
		return "connection refused"
	case strings.Contains(low, "no such host"), strings.Contains(low, "name resolution"),
		strings.Contains(low, "name or service not known"), strings.Contains(low, "server misbehaving"):
		return "no such host"
	case strings.Contains(low, "timed out"), strings.Contains(low, "deadline exceeded"),
		strings.Contains(low, "timeout"), strings.Contains(low, "did not properly respond"):
		return "timed out"
	case strings.Contains(low, "connection reset"), strings.Contains(low, "forcibly closed"),
		strings.Contains(low, "connection aborted"), strings.Contains(low, "was aborted"):
		return "connection reset"
	case eofRe.MatchString(low), strings.Contains(low, "broken pipe"), strings.Contains(low, "connection dropped"):
		return "connection dropped"
	case strings.Contains(low, "unreachable"), strings.Contains(low, "no route to host"), strings.Contains(low, "network is down"):
		return "network unreachable"
	case strings.Contains(low, "certificate"), strings.Contains(low, "x509"):
		return "untrusted certificate"
	case strings.Contains(low, "too many requests"), strings.Contains(low, "rate limit"), strings.Contains(low, "retry after"):
		return "rate limited"
	}
	// Whatever the library said, cut to its last part and to one line.
	cause := msg
	if i := strings.LastIndex(cause, ": "); i >= 0 {
		cause = cause[i+2:]
	}
	return clipCause(cause)
}

// clipCause is the first line of cause, at most 60 runes of it.
func clipCause(cause string) string {
	cause = strings.TrimSpace(strings.SplitN(cause, "\n", 2)[0])
	if r := []rune(cause); len(r) > 60 {
		cause = string(r[:59]) + "…"
	}
	return cause
}

// Describe says in one clause why label (scheme://host) didn't take an
// alert: "couldn't reach ntfy+http://10.0.0.2:81 (connection refused)"
// when the service couldn't be reached, "ntfy+http://10.0.0.2:81
// answered HTTP 503" when it answered with a status code, and
// "telegram://api.telegram.org answered: rate limited" when it answered
// in words. A label of "" stands for the only URL and the clause names
// no one. msg may be the whole failure or ShortCauseAt's words for it.
func Describe(label, msg string) string {
	cause := ShortCauseAt(label, msg)
	who := label
	if who == "" {
		who = "the service"
	}
	if _, _, ok := smtpReply(label, msg); !ok {
		if _, ok := StatusCode(msg); ok {
			return who + " answered " + cause
		}
		if unreachable(msg) {
			return "couldn't reach " + who + " (" + cause + ")"
		}
	}
	return who + " answered: " + cause
}
