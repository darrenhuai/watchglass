package web

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/ocr"
)

func TestSummarizeErr(t *testing.T) {
	cases := []struct{ name, msg, want string }{
		{"windows refused",
			`no reading for 2 consecutive polls: grab: snapshot http://127.0.0.1:9/snapshot.jpg: Get "http://127.0.0.1:9/snapshot.jpg": dial tcp 127.0.0.1:9: connectex: No connection could be made because the target machine actively refused it.`,
			"Connection refused by 127.0.0.1:9"},
		{"linux refused", `snapshot http://cam.local/s.jpg: Get "http://cam.local/s.jpg": dial tcp 10.0.0.5:80: connect: connection refused`,
			"Connection refused by cam.local"},
		{"http status", "no reading for 3 consecutive polls: grab: snapshot http://cam.local:8080/snap: status 404",
			"cam.local:8080 answered HTTP 404"},
		{"timeout", `snapshot http://cam/s.jpg: Get "http://cam/s.jpg": dial tcp 10.0.0.9:80: i/o timeout`, "Timed out reaching cam"},
		{"client timeout", `Get "http://cam/s.jpg": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`, "Timed out reaching cam"},
		{"no such host", `snapshot http://nope.lan/s.jpg: Get "http://nope.lan/s.jpg": dial tcp: lookup nope.lan: no such host`, "Host nope.lan not found"},
		{"no route", `Get "http://10.9.9.9/x": dial tcp 10.9.9.9:80: connect: no route to host`, "No route to 10.9.9.9"},
		{"decode", "snapshot http://cam/s.jpg: decode: decode image header: image: unknown format", "cam didn't send a readable image"},
		{"credentials never shown",
			"grab: ffmpeg: exit status 1: [tcp @ 000001a887cf3200] Connection to tcp://cam:554 failed: rtsp://admin:hunter2@cam:554/stream: Connection refused",
			"Connection refused by cam:554"},
		{"ffmpeg missing", `grab: ffmpeg: exec: "ffmpeg": executable file not found in %PATH%`, "ffmpeg isn't installed"},
		{"ffmpeg other", "grab: ffmpeg: exit status 1: [rtsp @ 0x55d0c0a1b2c0] method DESCRIBE failed: 401 Unauthorized",
			"The camera turned down the login"},
		{"ffmpeg generic", "grab: ffmpeg: exit status 1: [in#0 @ 0000021c] Invalid data found when processing input",
			"ffmpeg couldn't read the stream: invalid data found when processing input"},
		{"ocr", "no reading for 2 consecutive polls: ocr: tesseract: exit status 1: Error opening data file tessdata/eng.traineddata",
			"OCR failed: error opening data file tessdata/eng.traineddata"},
		{"ocr engine missing", "no reading for 2 consecutive polls: ocr: " + ocr.ErrNoTesseract.Error(), "OCR failed: tesseract isn't installed"},
		{"http 401", "grab: snapshot http://admin:xxxxx@192.168.1.64/cgi-bin/snapshot.cgi: status 401",
			"192.168.1.64 turned down the login"},
		{"http 403", "snapshot http://cam.lan/ISAPI/Streaming/channels/101/picture: status 403", "cam.lan turned down the login"},
		{"fallback", "something odd happened: the widget is sideways", "The widget is sideways"},
		{"no host in message", "grab: refused", "Connection refused"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := summarizeErr(c.msg); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
		if strings.Contains(summarizeErr(c.msg), "hunter2") {
			t.Errorf("%s: summary leaks credentials", c.name)
		}
	}
	long := "boom: " + strings.Repeat("x", 300)
	if got := summarizeErr(long); len([]rune(got)) > summaryMaxRunes || !strings.HasSuffix(got, "…") {
		t.Errorf("long fallback should be clipped with an ellipsis, got %d runes: %q", len([]rune(got)), got)
	}
}

// A refused connection to a camera at 127.0.0.1 (or ::1, or localhost) gets
// the Docker/WSL loopback advice: inside a container that address is the
// container. Anything else refused, and any other loopback failure, gets
// no hint.
func TestErrHintExplainsLoopbackInContainers(t *testing.T) {
	t.Setenv("WATCHGLASS_IN_CONTAINER", "")
	const generic = "If watchglass runs in Docker or WSL, 127.0.0.1 is that container, not your computer. Use the host's LAN IP or host.docker.internal."
	cases := []struct{ name, msg, want string }{
		{"linux 127.0.0.1", `no reading for 3 consecutive polls: grab: snapshot http://127.0.0.1:8102/snapshot.jpg: Get "http://127.0.0.1:8102/snapshot.jpg": dial tcp 127.0.0.1:8102: connect: connection refused`, generic},
		{"windows 127.0.0.1", `snapshot http://127.0.0.1:9/snapshot.jpg: Get "http://127.0.0.1:9/snapshot.jpg": dial tcp 127.0.0.1:9: connectex: No connection could be made because the target machine actively refused it.`, generic},
		{"localhost", `snapshot http://localhost:8100/snapshot.jpg: Get "http://localhost:8100/snapshot.jpg": dial tcp [::1]:8100: connect: connection refused`, generic},
		{"ipv6 loopback", `snapshot http://[::1]:8100/s.jpg: Get "http://[::1]:8100/s.jpg": dial tcp [::1]:8100: connect: connection refused`, generic},
		{"rtsp loopback", `grab: ffmpeg: exit status 1: Connection to tcp://127.0.0.1:554 failed: rtsp://127.0.0.1:554/stream: Connection refused`, generic},
		{"LAN camera refused", `snapshot http://192.168.1.20/s.jpg: Get "http://192.168.1.20/s.jpg": dial tcp 192.168.1.20:80: connect: connection refused`, ""},
		{"named camera refused", `snapshot http://cam.local/s.jpg: dial tcp 10.0.0.5:80: connect: connection refused`, ""},
		{"loopback timeout", `snapshot http://127.0.0.1:8102/s.jpg: Get "http://127.0.0.1:8102/s.jpg": context deadline exceeded`, ""},
		{"loopback 404", "snapshot http://127.0.0.1:8102/snap: status 404", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := errHint(c.msg); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}

	refused := cases[0].msg
	if got, want := withHint(summarizeErr(refused), refused), "Connection refused by 127.0.0.1:8102. "+generic; got != want {
		t.Errorf("withHint:\n got  %q\n want %q", got, want)
	}
	lan := cases[5].msg
	if got := withHint(summarizeErr(lan), lan); got != "Connection refused by 192.168.1.20" {
		t.Errorf("withHint without a hint should leave the summary alone, got %q", got)
	}

	// The image sets WATCHGLASS_IN_CONTAINER=1, and then it isn't an "if".
	t.Setenv("WATCHGLASS_IN_CONTAINER", "1")
	if got := errHint(refused); !strings.HasPrefix(got, "watchglass runs in a container here, and 127.0.0.1 is the container itself.") ||
		!strings.Contains(got, "host.docker.internal") {
		t.Errorf("in a container: got %q", got)
	}
}

func TestFriendlyConfigError(t *testing.T) {
	cases := []struct{ err, field, want string }{
		{`duplicate watch name "printer"`, "name", `A watch named "printer" already exists.`},
		{`watch 0: name "a/b" must not contain '/', '?', '#', or control characters`, "name", "Names can't contain /, ?, # or control characters."},
		{`watch 0: name ".." is reserved: it can't be used in a URL path`, "name", `A name can't be just "." or "..": a link to it would lead somewhere else.`},
		{`watch "cam": unsupported source "ftp://x": expected one of http:// https:// rtsp:// rtsps:// v4l2: dshow: ffmpeg:`, "source",
			"That source isn't supported. It must start with http://, https://, rtsp://, rtsps://, v4l2:, dshow: or ffmpeg:."},
		{`watch "cam": interval must be >= 1s`, "interval", "Interval must be at least 1s"},
		{`watch "cam": max_interval must be >= interval`, "max_interval", "Max interval can't be shorter than Interval"},
		{"watch \"cam\": trigger: pattern: error parsing regexp: missing closing ): `(`", "pattern", `Pattern isn't a valid regular expression. A "(" is never closed.`},
		{"watch \"cam\": trigger: pattern: error parsing regexp: unexpected ): `abc)`", "pattern", `Pattern isn't a valid regular expression. There's a ")" with no "(" before it.`},
		{"watch \"cam\": trigger: pattern: error parsing regexp: missing argument to repetition operator: `*`", "pattern", `Pattern isn't a valid regular expression. "*" has nothing before it to repeat.`},
		{"watch \"cam\": trigger: pattern: error parsing regexp: invalid escape sequence: `\\q`", "pattern", `Pattern isn't a valid regular expression. "\q" isn't an escape Go understands.`},
		{"watch \"cam\": trigger: pattern: error parsing regexp: trailing backslash at end of expression: ``", "pattern", `Pattern isn't a valid regular expression. It ends in a lone \.`},
		{"watch \"cam\": trigger: pattern: error parsing regexp: invalid or unsupported Perl syntax: `(?=`", "pattern", `Pattern isn't a valid regular expression. "(?=" isn't supported: Go regular expressions have no lookarounds`},
		{"watch \"cam\": trigger: pattern: error parsing regexp: some future code: `x`", "pattern", `Pattern isn't a valid regular expression. Some future code near "x".`},
		{`watch "cam": trigger: ocr_match requires a pattern`, "pattern", "ocr_match needs a pattern"},
		{`watch "cam": trigger: numeric needs Compare set to above (gt) or below (lt), got ""`, "op", "Under Compare, choose whether the reading must go above or below Threshold."},
		{`numeric op must be gt or lt, got ""`, "op", "Under Compare, choose whether the reading must go above or below Threshold."},
		{`watch "cam": notify: invalid URL "nope" (must include a scheme, e.g. ntfy://...)`, "notify", `Notify URL "nope" needs a scheme`},
		{`mqtt: broker is required`, "", "Mqtt: broker is required"},
	}
	for _, c := range cases {
		got := friendlyConfigError(errors.New(c.err))
		if got.Field != c.field || !strings.HasPrefix(got.Msg, c.want) {
			t.Errorf("%s:\n got  %+v\n want field %q, msg starting %q", c.err, got, c.field, c.want)
		}
	}
}

// Go's own regexp errors (not hand-typed copies of them) come out in
// words: a change in the standard library's wording shows up here.
func TestPatternErrorsFromRealRegexps(t *testing.T) {
	for _, p := range []string{"(", "abc)", "[a", "*a", "a**", "a{2000}", `\q`, `a\`, "[z-a]", "(?=x)", "(?<n"} {
		_, err := regexp.Compile(p)
		if err == nil {
			t.Fatalf("%q compiled", p)
		}
		got := friendlyConfigError(fmt.Errorf("watch \"cam\": trigger: pattern: %w", err))
		if got.Field != "pattern" || strings.Contains(got.Msg, "`") || strings.Contains(got.Msg, " near ") ||
			!strings.HasPrefix(got.Msg, "Pattern isn't a valid regular expression. ") {
			t.Errorf("%q: %+v", p, got)
		}
	}
}

// Go reports three different repeat-count problems under one code; each
// gets its own reason rather than always blaming "too many repeats".
func TestRepeatCountErrorsFromRealRegexps(t *testing.T) {
	const pre = "Pattern isn't a valid regular expression. "
	for p, want := range map[string]string{
		"a{2,1}":      `"{2,1}" isn't a valid repeat count: the minimum is larger than the maximum.`,
		"a{1001}":     `"{1001}" asks for too many repeats (the most is 1000).`,
		"a{2,1001}":   `"{2,1001}" asks for too many repeats (the most is 1000).`,
		"(a{500}){3}": `"{3}" repeats a group that already repeats, and together they come to more than 1000 repeats.`,
	} {
		_, err := regexp.Compile(p)
		if err == nil {
			t.Fatalf("%q compiled", p)
		}
		got := friendlyConfigError(fmt.Errorf("watch \"cam\": trigger: pattern: %w", err))
		if got.Field != "pattern" || got.Msg != pre+want {
			t.Errorf("%q:\n got  %+v\n want %q", p, got, pre+want)
		}
	}
}

func TestFriendlyStartError(t *testing.T) {
	notify := errors.New(`watch "cam-a": notify: creating sender for URLs [nope://x]: error initializing router services: unknown service: "nope"`)
	if got, want := friendlyStartError(notify), `A notify URL can't be used: unknown service: "nope".`; got != want {
		t.Errorf("notify: got %q, want %q", got, want)
	}
	// Today's form: notify.NewShoutrrr names the line and host only.
	line := errors.New(`watch "cam-a": notify: line 2 (https://ntfy.sh): That's the ntfy web address. watchglass sends to ntfy with the ntfy:// form.`)
	if got, want := friendlyStartError(line), "Notify line 2 (https://ntfy.sh) can't be used: that's the ntfy web address. watchglass sends to ntfy with the ntfy:// form."; got != want {
		t.Errorf("notify line: got %q, want %q", got, want)
	}
	src := errors.New(`watch "cam": source: unsupported source scheme`)
	if got, want := friendlyStartError(src), "The source can't be used: unsupported source scheme."; got != want {
		t.Errorf("source: got %q, want %q", got, want)
	}
	tess := fmt.Errorf("watch %q %w", "cam", ocr.ErrNoTesseract)
	if got := friendlyStartError(tess); !strings.HasPrefix(got, "This trigger type reads text with tesseract") {
		t.Errorf("tesseract: got %q", got)
	}
}

// A camera that turns the login down says where the user and password go,
// or, when the URL already has them, to check them.
func TestErrHintForARefusedLogin(t *testing.T) {
	for _, c := range []struct{ msg, want string }{
		{"grab: snapshot http://admin:xxxxx@192.168.1.64/cgi-bin/snapshot.cgi: status 401", "Check the user and password in the source URL."},
		{"snapshot https://cam.lan/snap: status 401", "Put the camera's user and password in the source URL: https://user:password@camera/…"},
		{"snapshot http://cam.lan/snap: status 403", "Put the camera's user and password in the source URL: http://user:password@camera/…"},
		{"grab: ffmpeg: exit status 1: [rtsp @ 0x55d0c0a1b2c0] method DESCRIBE failed: 401 Unauthorized", "Put the camera's user and password in the source URL: http://user:password@camera/…"},
	} {
		if got := errHint(c.msg); !strings.HasPrefix(got, c.want) {
			t.Errorf("errHint(%q) = %q, want it to start %q", c.msg, got, c.want)
		}
	}
	for _, msg := range []string{"snapshot http://cam.lan/snap: status 404", "snapshot http://cam.lan/snap: status 500"} {
		if got := errHint(msg); got != "" {
			t.Errorf("errHint(%q) = %q, want none", msg, got)
		}
	}
}
