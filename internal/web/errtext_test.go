package web

import (
	"errors"
	"fmt"
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

func TestFriendlyConfigError(t *testing.T) {
	cases := []struct{ err, field, want string }{
		{`duplicate watch name "printer"`, "name", `A watch named "printer" already exists.`},
		{`watch 0: name "a/b" must not contain '/', '?', '#', or control characters`, "name", "Names can't contain /, ?, # or control characters."},
		{`watch "cam": unsupported source "ftp://x": expected one of http:// https:// rtsp:// rtsps:// v4l2: dshow: ffmpeg:`, "source",
			"That source isn't supported. It must start with http://, https://, rtsp://, rtsps://, v4l2:, dshow: or ffmpeg:."},
		{`watch "cam": interval must be >= 1s`, "interval", "Interval must be at least 1s"},
		{`watch "cam": max_interval must be >= interval`, "max_interval", "Max interval can't be shorter than Interval"},
		{"watch \"cam\": trigger: pattern: error parsing regexp: missing closing ): `(`", "pattern", "Pattern isn't a valid regular expression: missing closing ): (."},
		{`watch "cam": trigger: ocr_match requires a pattern`, "pattern", "ocr_match needs a pattern"},
		{`watch "cam": trigger: numeric op must be gt or lt, got ""`, "op", "Choose gt or lt"},
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

func TestFriendlyStartError(t *testing.T) {
	notify := errors.New(`watch "cam-a": notify: creating sender for URLs [nope://x]: error initializing router services: unknown service: "nope"`)
	if got, want := friendlyStartError(notify), `A notify URL can't be used: unknown service: "nope".`; got != want {
		t.Errorf("notify: got %q, want %q", got, want)
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
