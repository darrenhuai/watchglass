package source

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// A camera that turns the login down is asked again only after a wait, so a
// wrong password in a source URL doesn't cost the account a failed login on
// every poll and every snapshot refresh. Many cameras (Hikvision, Dahua,
// Reolink, Axis) lock the account after a handful of wrong tries, for half
// an hour or until someone unlocks it, and a watch polling every 2 s would
// keep that lock renewed for as long as it runs.
//
// The wait is shared by everything that grabs from the camera with the same
// login (the poll loop, the detail page's snapshot and its refresh, every
// watch on that camera), like the Digest memory in source.go: the web UI
// builds a new source for each request. It grows with each refusal in a row
// (loginWaits) and the first grab the camera accepts ends it. A different
// login is a different key, so fixing the password in the URL is tried at
// once. A grab made with Forced (Test this region: a person asking) asks the
// camera even inside the wait, but at most once every ForcedTryGap per
// login, so a Test button pressed again and again (or a script posting to
// the Test route) can't lock the account the wait is there to protect.
//
// The wait lives in memory and a restart starts without it, on purpose:
// restarting after fixing the password in config.yaml is the documented way
// to try it at once, and keeping a refusal across a restart would mean
// writing a fingerprint of the password to disk (the key's HMAC secret
// never leaves the process).

// ErrLoginRefused matches (errors.Is) every grab the camera turned down for
// its login: HTTP 401 or 403, or ffmpeg reporting either, and the grabs that
// failed without asking because of an earlier refusal.
var ErrLoginRefused = errors.New("the camera refused the login")

// LoginRefusedError is a grab that failed for the login (see ErrLoginRefused).
// Its text is the grab's error as it always read ("snapshot URL: status
// 401"), plus a note when the camera wasn't asked; the password is masked.
type LoginRefusedError struct {
	Status   int  // 401 or 403
	Skipped  bool // the camera wasn't asked: an earlier refusal's wait is running
	Refusals int  // refusals in a row so far
	// Throttled: a Forced grab that wasn't asked because another Forced
	// grab asked this login less than ForcedTryGap ago (Skipped is set too).
	// TriedAgo is how long ago that was, RetryIn how long until a Forced
	// grab asks again.
	Throttled         bool
	TriedAgo, RetryIn time.Duration
	msg               string
}

func (e *LoginRefusedError) Error() string { return e.msg }

// Is makes errors.Is(err, ErrLoginRefused) true.
func (e *LoginRefusedError) Is(target error) bool { return target == ErrLoginRefused }

// loginWaits are the waits after the 1st, 2nd, ... refusal in a row; the
// last one repeats. Half an hour at the end outlasts the 30-minute lock
// common on cameras, so watchglass alone doesn't keep an account locked.
var loginWaits = []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute,
	5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

func loginWait(refusals int) time.Duration {
	if refusals < 1 {
		refusals = 1
	}
	if refusals > len(loginWaits) {
		refusals = len(loginWaits)
	}
	return loginWaits[refusals-1]
}

// ForcedTryGap is the least time between two Forced grabs that ask the
// camera with a login it has turned down. A var so that tests in other
// packages can shorten it.
var ForcedTryGap = 10 * time.Second

// forgetAfter drops a refusal nobody has asked about for this long, so the
// map doesn't keep every password ever typed wrong.
const forgetAfter = 2 * time.Hour

type forcedKey struct{}

// Forced marks a grab as a person asking (Test this region): it asks the
// camera even while a refused login is waiting, and what the camera answers
// counts for everyone (an accepted login ends the wait at once).
func Forced(ctx context.Context) context.Context {
	return context.WithValue(ctx, forcedKey{}, true)
}

func isForced(ctx context.Context) bool {
	v, _ := ctx.Value(forcedKey{}).(bool)
	return v
}

// refusal is what a camera's last refusals of one login taught us.
type refusal struct {
	label   string // scheme://user@host, for LoginRetryIn
	status  int
	count   int
	last    time.Time // when the camera last refused
	until   time.Time // ask again from then
	probing bool      // a grab is asking the camera after the wait
	// forced is when a Forced grab last asked the camera with this login
	// (see ForcedTryGap).
	forced time.Time
}

type gate struct {
	mu   sync.Mutex
	now  func() time.Time
	byID map[string]*refusal
}

var logins = &gate{now: time.Now, byID: map[string]*refusal{}}

// gateSecret keys the password's fingerprint in a gate key. A plain hash of
// a short password can be reversed by trying candidates; an HMAC under a
// key that never leaves the process can't.
var gateSecret = func() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}()

// gateKeys returns the key for a refused login on u's camera (scheme, user,
// host and a fingerprint of the password, the query and the headers, which
// is where cameras take credentials), the narrower key a 403 is kept under
// (the same plus the path: a 403 can be one channel the user may not see
// while the login itself is fine for the others), and the label LoginRetryIn
// finds them by. ok is false when u names no network camera.
func gateKeys(u *url.URL, headers http.Header) (login, path, label string, ok bool) {
	if u == nil || u.Host == "" || u.Scheme == "" {
		return "", "", "", false
	}
	user := ""
	pass := ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	label = strings.ToLower(u.Scheme) + "://" + user + "@" + strings.ToLower(u.Host)
	mac := hmac.New(sha256.New, gateSecret)
	mac.Write([]byte(pass))
	mac.Write([]byte{0})
	mac.Write([]byte(u.RawQuery))
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		for _, v := range headers[k] {
			mac.Write([]byte{0})
			mac.Write([]byte(http.CanonicalHeaderKey(k) + ": " + v))
		}
	}
	login = label + "#" + hex.EncodeToString(mac.Sum(nil)[:8])
	return login, login + " " + u.EscapedPath(), label, true
}

// ticket is one grab's pass through the gate: finish tells the gate what
// the camera answered.
type ticket struct {
	g           *gate
	login, path string
	label       string
	probing     []*refusal // entries this grab claimed the after-wait ask of
	forced      bool       // a Forced grab: its refusal starts ForcedTryGap
}

// admit lets a grab of u's camera through, or returns the remembered
// refusal when the camera mustn't be asked yet, worded by describe (the
// grab's usual text for a refusal with that status). A nil ticket (and nil
// error) means there is nothing to gate.
func (g *gate) admit(ctx context.Context, u *url.URL, headers http.Header, describe func(status int) string) (*ticket, error) {
	login, path, label, ok := gateKeys(u, headers)
	if !ok {
		return nil, nil
	}
	forced := isForced(ctx)
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	t := &ticket{g: g, login: login, path: path, label: label, forced: forced}
	if forced {
		// A person asking goes through the wait, but not more often than
		// once every ForcedTryGap for a login the camera has turned down:
		// a press inside that gets the refusal back without a new try.
		var known []*refusal
		for _, id := range []string{login, path} {
			r := g.byID[id]
			if r == nil {
				continue
			}
			if ago := now.Sub(r.forced); !r.forced.IsZero() && ago >= 0 && ago < ForcedTryGap {
				return nil, &LoginRefusedError{Status: r.status, Skipped: true, Refusals: r.count,
					Throttled: true, TriedAgo: ago, RetryIn: ForcedTryGap - ago,
					msg: describe(r.status) + forcedNote(ago)}
			}
			known = append(known, r)
		}
		for _, r := range known {
			r.forced = now
		}
		return t, nil
	}
	for _, id := range []string{login, path} {
		r := g.byID[id]
		if r == nil {
			continue
		}
		// One grab at a time asks after the wait; the others carry on
		// failing with the refusal until it has an answer.
		if now.Before(r.until) || r.probing {
			for _, p := range t.probing {
				p.probing = false
			}
			return nil, &LoginRefusedError{Status: r.status, Skipped: true, Refusals: r.count,
				msg: describe(r.status) + skippedNote(r.count)}
		}
		r.probing = true
		t.probing = append(t.probing, r)
	}
	return t, nil
}

func skippedNote(n int) string {
	if n == 1 {
		return " (not asked: the camera turned this login down a moment ago)"
	}
	return fmt.Sprintf(" (not asked: the camera turned this login down %d times in a row)", n)
}

// forcedNote ends the text of a Forced grab that wasn't asked. It never
// says "refused": summaries read that as a connection refused.
func forcedNote(ago time.Duration) string {
	secs := int(ago / time.Second)
	if secs < 1 {
		return " (not asked: a Test asked the camera with this login a moment ago)"
	}
	return fmt.Sprintf(" (not asked: a Test asked the camera with this login %ds ago)", secs)
}

// refused records that the camera turned the login down with status (401
// or 403) and returns the error for the grab, msg being its usual text.
// It is safe on a nil ticket (nothing gated): it only builds the error.
func (t *ticket) refused(status int, msg string) error {
	if t == nil {
		return &LoginRefusedError{Status: status, msg: msg}
	}
	g := t.g
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	t.release()
	id := t.login
	if status == http.StatusForbidden {
		id = t.path
	}
	r := g.byID[id]
	if r == nil {
		r = &refusal{label: t.label}
		g.byID[id] = r
	}
	// A refusal inside the wait (a grab already on its way when the first
	// refusal came in, or a Test) doesn't make the wait longer: the
	// camera told us nothing new. It does start it over.
	if !now.Before(r.until) {
		r.count++
	}
	r.status, r.last = status, now
	r.until = now.Add(loginWait(r.count))
	if t.forced {
		r.forced = now
	}
	for id, old := range g.byID {
		if now.Sub(old.last) > forgetAfter && !old.probing {
			delete(g.byID, id)
		}
	}
	return &LoginRefusedError{Status: status, Refusals: r.count, msg: msg}
}

// accepted records that the camera took the login: the wait is over for
// everyone using it.
func (t *ticket) accepted() {
	if t == nil {
		return
	}
	t.g.mu.Lock()
	defer t.g.mu.Unlock()
	t.release()
	delete(t.g.byID, t.login)
	delete(t.g.byID, t.path)
}

// done records a grab that ended without the camera saying anything about
// the login (unreachable, timed out, a server error): the wait stays as it
// was, and the next grab after it asks again.
func (t *ticket) done() {
	if t == nil {
		return
	}
	t.g.mu.Lock()
	defer t.g.mu.Unlock()
	t.release()
}

func (t *ticket) release() {
	for _, r := range t.probing {
		r.probing = false
	}
	t.probing = nil
}

// LoginRetryIn says how long until watchglass asks the camera in rawURL
// (as an error message shows it: the password may be masked) again after
// it turned the login down. ok is false when no refusal is waiting for that
// camera and user. 0 means the next grab asks.
func LoginRetryIn(rawURL string) (wait time.Duration, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, false
	}
	_, _, label, ok := gateKeys(u, nil)
	if !ok {
		return 0, false
	}
	return logins.retryIn(label)
}

func (g *gate) retryIn(label string) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// The message names the camera and user, not the password: of the
	// waits for that user (an old password typed wrong, then a new one),
	// the one the camera refused last is the one in use.
	var latest *refusal
	for _, r := range g.byID {
		if r.label == label && (latest == nil || r.last.After(latest.last)) {
			latest = r
		}
	}
	if latest == nil {
		return 0, false
	}
	if d := latest.until.Sub(g.now()); d > 0 && !latest.probing {
		return d, true
	}
	return 0, true
}

// refusedStatus is 401 or 403 when ffmpeg's error output says the camera
// turned the login down ("Server returned 401 Unauthorized (authorization
// failed)", "method DESCRIBE failed: 401 (Unauthorized)", "403 Forbidden"),
// else 0.
func refusedStatus(msg string) int {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "401 unauthorized"), strings.Contains(low, "401 (unauthorized)"):
		return http.StatusUnauthorized
	case strings.Contains(low, "403 forbidden"), strings.Contains(low, "403 (forbidden)"):
		return http.StatusForbidden
	}
	return 0
}

// userinfoRe finds the user:password@ of a URL in free text (ffmpeg prints
// its input URL, password and all, in its errors). The password runs to
// the last "@" before the path, query or fragment, as net/url splits it,
// so a raw "@" in it is covered and "http://cam:8080/snap@2x.jpg" is left
// alone. secretParamRe finds a password carried in a query string
// (?user=admin&password=...).
var (
	userinfoRe    = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)([^\s/@:"'<>]*):([^\s/?#"'<>]*)@`)
	secretParamRe = regexp.MustCompile(`(?i)([?&](?:password|pass|passwd|pwd|token|apikey|api_key)=)[^&\s"'<>]*`)
)

// RedactText masks every URL password in s the way RedactURL does
// ("user:xxxxx@", "password=xxxxx"). inputs are URLs known to be in play
// (the source), whose exact spelling is masked first, in case a password
// has characters the patterns stop at.
func RedactText(s string, inputs ...string) string {
	for _, in := range inputs {
		if shown := RedactURL(in); shown != in {
			s = strings.ReplaceAll(s, in, shown)
		}
	}
	s = userinfoRe.ReplaceAllString(s, "${1}${2}:xxxxx@")
	return secretParamRe.ReplaceAllString(s, "${1}xxxxx")
}
