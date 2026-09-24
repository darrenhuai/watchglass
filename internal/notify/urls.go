package notify

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicholas-fedor/shoutrrr"
)

// Notify URLs carry their credentials in plain sight: a Discord or
// Telegram token in the userinfo, an ntfy.sh topic (which is the password
// on a public server) or a webhook id in the path, API keys in the query.
// Anything watchglass says ABOUT a URL (a delivery error, a log line, the
// dashboard) names it by Label, scheme://host, and nothing more; Redact is
// for the few places that must show a whole URL (a suggested rewrite).

// mask stands in for a hidden credential.
const mask = "•••"

// Label is scheme://host of a notify URL: enough to tell the lines of a
// list apart, never a credential ("discord://1234567890" for a webhook,
// "ntfy://ntfy.sh" for a topic). A URL with no scheme is labelled by its
// host alone, and a "mailto:"-style one by its scheme.
func Label(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || hostPort(raw) {
		return bareHost(raw)
	}
	if u.Host == "" && u.Opaque != "" {
		return u.Scheme + ":"
	}
	return u.Scheme + "://" + u.Host
}

// hostPort is true for a scheme-less "host:port/…", which url.Parse reads
// as scheme "host" ("localhost:9000/hook" is not a localhost:// URL).
func hostPort(raw string) bool {
	if strings.Contains(raw, "://") {
		return false
	}
	_, rest, ok := strings.Cut(raw, ":")
	return ok && rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// bareHost is the host part of a scheme-less "host[:port]/path" string,
// without any userinfo.
func bareHost(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// safeQueryKeys are query parameters that configure a service rather than
// authenticate to it; Redact shows their values, and masks every other.
var safeQueryKeys = map[string]bool{
	"template": true, "titlekey": true, "messagekey": true, "title": true,
	"chats": true, "channel": true, "channels": true, "priority": true,
	"tags": true, "thread_id": true, "username": true, "requestmethod": true,
	"contenttype": true,
}

// Redact is raw with its credentials masked: the userinfo, and the values
// of query parameters that aren't plainly settings. The host and path stay,
// so a person can check a suggested rewrite against what they typed.
func Redact(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || hostPort(raw) {
		return bareHost(raw)
	}
	if u.Host == "" && u.Opaque != "" {
		return u.Scheme + ":"
	}
	var b strings.Builder
	b.WriteString(u.Scheme + "://")
	if u.User != nil {
		b.WriteString(mask + "@")
	}
	b.WriteString(u.Host)
	b.WriteString(u.EscapedPath())
	if u.RawQuery != "" {
		parts := strings.Split(u.RawQuery, "&")
		for i, p := range parts {
			k, _, found := strings.Cut(p, "=")
			key, _ := url.QueryUnescape(k)
			if found && !safeQueryKeys[strings.ToLower(key)] {
				parts[i] = k + "=" + mask
			}
		}
		b.WriteString("?" + strings.Join(parts, "&"))
	}
	return b.String()
}

// urlInText finds URLs quoted inside error text ("Post \"https://…\": …").
var urlInText = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>\[\]]+`)

// Scrub makes an error about notify URLs safe to show and log: every URL in
// msg is cut down to its Label, and anything that looks like a credential
// from urls (userinfo, long path segments, query values) is masked wherever
// it still appears in the rest of the text. The Labels themselves are left
// alone: scheme://host is what names a line everywhere else, and masking a
// password that happens to be part of the host ("admin" in admin.local)
// would only garble it. It never adds information, so an error that holds
// no URL comes back unchanged.
func Scrub(msg string, urls []string) string {
	var secs []string
	for _, u := range urls {
		secs = append(secs, secrets(u)...)
	}
	hide := func(s string) string {
		for _, x := range secs {
			s = strings.ReplaceAll(s, x, mask)
		}
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range urlInText.FindAllStringIndex(msg, -1) {
		m := msg[loc[0]:loc[1]]
		trail := ""
		for len(m) > 0 && strings.ContainsRune(".,;:)", rune(m[len(m)-1])) {
			trail = m[len(m)-1:] + trail
			m = m[:len(m)-1]
		}
		b.WriteString(hide(msg[last:loc[0]]))
		b.WriteString(Label(m) + trail)
		last = loc[1]
	}
	b.WriteString(hide(msg[last:]))
	return b.String()
}

// placeholderUsers are usernames a service's URL form requires as a fixed
// word (slack://hook:TOKEN@webhook, pushover://shoutrrr:TOKEN@USER). They
// aren't credentials, and masking them would garble ordinary words
// ("webhook" became "web•••").
var placeholderUsers = map[string]bool{"hook": true, "shoutrrr": true}

// secrets are the parts of raw that could be a credential and are long
// enough that masking them can't mangle ordinary words in a message.
func secrets(raw string) []string {
	var out []string
	add := func(s string, min int) {
		if utf8.RuneCountInString(s) >= min {
			out = append(out, s)
		}
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return strings.ContainsRune("/@:?&=#", r) }) {
			add(f, 8)
		}
		return out
	}
	if u.User != nil {
		// A username is only a credential when it's token-sized (Discord's
		// webhook token, a Telegram bot id); a short one is a login name
		// like "admin" or "user" and would mask ordinary words. A password
		// is always one.
		if name := u.User.Username(); !placeholderUsers[strings.ToLower(name)] {
			add(name, 8)
		}
		if p, ok := u.User.Password(); ok {
			add(p, 3)
		}
	}
	for _, seg := range strings.Split(u.Path, "/") {
		add(seg, 8)
	}
	for k, vs := range u.Query() {
		if safeQueryKeys[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			add(v, 6)
		}
	}
	return out
}

// Problem is why a notify URL can't be used, for the form: Reason is one or
// two sentences, Fix the URL it most likely meant ("" when there is no
// obvious one).
type Problem struct {
	Reason string
	Fix    string
}

// Check says what is wrong with one notify URL, or nil when watchglass can
// send to it. It is the same test the watch's start runs (NewShoutrrr), plus
// the rewrites Suggest knows, so a URL the form accepts is one the watch
// will start with.
func Check(raw string) *Problem {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &Problem{Reason: "The line is empty."}
	}
	if emailAddress(raw) {
		return &Problem{Reason: "That's an email address. For alerts by email, use the smtp:// form: " +
			"smtp://USER:PASSWORD@smtp.example.com:587/?from=you@example.com&to=you@example.com. The list of services has the details."}
	}
	switch s := suggest(raw); s.kind {
	case "ntfy":
		return &Problem{Reason: "That's the ntfy web address. watchglass sends to ntfy with the ntfy:// form.", Fix: s.url}
	case "ntfy-no-topic":
		return &Problem{Reason: "That's the ntfy web address without a topic. Use ntfy://ntfy.sh/ followed by your topic name."}
	case "discord":
		return &Problem{Reason: "That's a Discord webhook link. watchglass takes it as discord://TOKEN@WEBHOOK_ID.", Fix: s.url}
	case "slack":
		return &Problem{Reason: "That's a Slack webhook link. watchglass takes it as slack://hook:TOKEN@webhook.", Fix: s.url}
	case "webhook":
		return &Problem{Reason: "A plain web address isn't a notification service. To post to a webhook, use generic+" + s.scheme +
			"://. If it's your own ntfy server, use ntfy+" + s.scheme + ":// with the topic instead.", Fix: s.url}
	}
	if p := ntfyProblem(raw); p != "" {
		return &Problem{Reason: sentence(p)}
	}
	if ntfyTarget(raw) != "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return &Problem{Reason: "It isn't a valid URL."}
	}
	if u.Scheme == "" {
		return &Problem{Reason: "It needs a service at the front, such as ntfy:// or discord://."}
	}
	if strings.HasPrefix(strings.ToLower(u.Scheme), "generic") && u.Host == "" {
		return &Problem{Reason: "Put the server's address after " + u.Scheme + "://, for example " + u.Scheme + "://192.168.1.10:9000/hook."}
	}
	if _, err := shoutrrr.CreateSender(raw); err != nil {
		msg := initProblem(err, raw)
		if strings.HasPrefix(msg, `unknown service: `) {
			return &Problem{Reason: strings.TrimPrefix(msg, "unknown service: ") + " isn't a notification service watchglass knows. Check the spelling, or see the list of services."}
		}
		if strings.Contains(msg, "invalid telegram token") {
			return &Problem{Reason: "The Telegram bot token isn't in the right form. Use the token @BotFather gave you, which looks like " +
				"123456789:AAE…, as in telegram://123456789:AAE…@telegram?chats=@your_channel."}
		}
		// "discord: setting config URL: token missing from config URL": the
		// last part that is a phrase. A part without a space is usually the
		// value being complained about ("invalid telegram token: •••:"),
		// which says nothing once it's masked.
		service, rest, _ := strings.Cut(msg, ": ")
		parts := strings.Split(rest, ": ")
		reason := ""
		for i := len(parts) - 1; i >= 0; i-- {
			if p := strings.TrimSpace(strings.ReplaceAll(parts[i], mask, "")); strings.Contains(p, " ") {
				reason = p
				break
			}
		}
		if reason == "" {
			return &Problem{Reason: "The " + service + " URL isn't in a form watchglass can use. Check it against the list of services."}
		}
		return &Problem{Reason: "The " + service + " URL doesn't work: " + strings.TrimSuffix(reason, ".") + "."}
	}
	return nil
}

// emailAddress is true for "me@example.com" or "mailto:me@example.com":
// someone who wants alerts by email typed their address.
func emailAddress(raw string) bool {
	if strings.HasPrefix(strings.ToLower(raw), "mailto:") {
		return true
	}
	if strings.Contains(raw, "://") || strings.ContainsAny(raw, " /?#") {
		return false
	}
	local, domain, ok := strings.Cut(raw, "@")
	return ok && local != "" && !strings.Contains(local, ":") && strings.Contains(domain, ".") && !strings.ContainsAny(domain, "@:")
}

func sentence(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".")
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:] + "."
}

// Suggest returns the shoutrrr form of a notify URL someone pasted from a
// browser or a service's settings page, if there is an obvious one:
//
//	https://ntfy.sh/topic                          -> ntfy://ntfy.sh/topic
//	https://discord.com/api/webhooks/ID/TOKEN      -> discord://TOKEN@ID
//	https://hooks.slack.com/services/A/B/C         -> slack://hook:A-B-C@webhook
//	http(s)://host[:port]/path                     -> generic+http(s)://host[:port]/path?template=json
//
// A URL already in a service's form is left alone (false).
func Suggest(raw string) (string, bool) {
	s := suggest(strings.TrimSpace(raw))
	return s.url, s.url != ""
}

type suggestion struct {
	kind   string // "ntfy", "ntfy-no-topic", "discord", "slack", "webhook" or ""
	url    string
	scheme string // http or https, for "webhook"
}

var (
	discordHookPath = regexp.MustCompile(`^/api(?:/v\d+)?/webhooks/(\d+)/([^/]+)/?$`)
	slackHookPath   = regexp.MustCompile(`^/services/([^/]+)/([^/]+)/([^/]+)/?$`)
)

func suggest(raw string) suggestion {
	low := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"):
	case !strings.Contains(raw, "://") && looksLikeHost(raw):
		// "ntfy.sh/topic", "192.168.1.10:8081/topic": a web address with the
		// scheme left off. A LAN host is most likely plain http.
		if isLocalHost(bareHost(raw)) {
			raw = "http://" + raw
		} else {
			raw = "https://" + raw
		}
	default:
		return suggestion{}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return suggestion{}
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "ntfy.sh" || host == "www.ntfy.sh":
		topic := strings.Trim(u.EscapedPath(), "/")
		if topic == "" || strings.Contains(topic, "/") {
			return suggestion{kind: "ntfy-no-topic"}
		}
		out := "ntfy://ntfy.sh/" + topic
		if u.RawQuery != "" {
			out += "?" + u.RawQuery
		}
		return suggestion{kind: "ntfy", url: out}
	case host == "discord.com" || host == "discordapp.com" || strings.HasSuffix(host, ".discord.com"):
		m := discordHookPath.FindStringSubmatch(u.EscapedPath())
		if m == nil {
			return suggestion{}
		}
		out := "discord://" + m[2] + "@" + m[1]
		if t := u.Query().Get("thread_id"); t != "" {
			out += "?thread_id=" + url.QueryEscape(t)
		}
		return suggestion{kind: "discord", url: out}
	case host == "hooks.slack.com":
		m := slackHookPath.FindStringSubmatch(u.EscapedPath())
		if m == nil {
			return suggestion{}
		}
		return suggestion{kind: "slack", url: "slack://hook:" + m[1] + "-" + m[2] + "-" + m[3] + "@webhook"}
	}
	scheme := strings.ToLower(u.Scheme)
	out := "generic+" + scheme + "://"
	if u.User != nil {
		out += u.User.String() + "@"
	}
	out += u.Host + u.EscapedPath()
	q := u.RawQuery
	if u.Query().Get("template") == "" {
		if q != "" {
			q += "&"
		}
		q += "template=json"
	}
	return suggestion{kind: "webhook", url: out + "?" + q, scheme: scheme}
}

// looksLikeHost is true for "host.tld/…" or "host:port/…": a web address
// missing its scheme, not a stray word, an email address ("me@gmail.com")
// or another kind of URL ("mailto:…", "tel:…").
func looksLikeHost(s string) bool {
	if strings.ContainsAny(s, " \t") {
		return false
	}
	h := s
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if strings.Contains(h, "@") {
		return false
	}
	if _, rest, ok := strings.Cut(h, ":"); ok && !strings.HasPrefix(h, "[") && (rest == "" || rest[0] < '0' || rest[0] > '9') {
		return false
	}
	return strings.Contains(h, ".") || strings.Contains(h, ":") || strings.EqualFold(h, "localhost")
}

// isLocalHost is true for localhost and private or loopback IP addresses.
func isLocalHost(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
