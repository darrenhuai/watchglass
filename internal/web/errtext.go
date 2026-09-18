package web

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/darrenhuai/watchglass/internal/ocr"
)

// Error text for people. The Go error chains watchglass produces are exact
// but long ("no reading for 2 consecutive polls: grab: snapshot http://…:
// Get "http://…": dial tcp …: connectex: No connection could be made…"),
// and they used to be printed whole in a table cell, three times on the
// detail page and as the body of the error page. The UI now leads with a
// short sentence and keeps the full chain one click away; nothing here
// changes what is logged or what notifications carry.

var (
	errURLRe    = regexp.MustCompile(`(?i)\b(?:https?|rtsps?)://[^\s"'<>]+`)
	errStatusRe = regexp.MustCompile(`\bstatus (\d{3})\b`)
	// ffmpeg prefixes its log lines with the emitting context's address,
	// e.g. "[tcp @ 000001a887cf3200] ", which is noise to a reader.
	ffmpegCtxRe  = regexp.MustCompile(`\[[^\]@\s]+ @ (?:0x)?[0-9A-Fa-f]+\]\s*`)
	watchPrefix  = regexp.MustCompile(`^watch (?:\d+|"(?:[^"\\]|\\.)*"):? `)
	notifyWrapRe = regexp.MustCompile(`^creating sender for URLs \[[^\]]*\]: `)
)

const summaryMaxRunes = 90

// summarizeErr turns a source/OCR health message or a grab error into one
// short sentence for the dashboard cell, the detail header, the snapshot
// placeholder and the Test result. The host is taken from the first URL
// in the message with any user:password stripped, so a summary never
// shows credentials even where the raw chain would.
func summarizeErr(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	low := strings.ToLower(msg)
	host := errHost(msg)
	by := func(withHost, without string) string {
		if host != "" {
			return strings.ReplaceAll(withHost, "HOST", host)
		}
		return without
	}

	// The runner wraps a failed OCR pass in "ocr: " and a failed grab in
	// "grab: "; an OCR failure is not a camera problem.
	if strings.Contains(low, "ocr: ") && !strings.Contains(low, "grab: ") {
		switch {
		case strings.Contains(msg, ocr.ErrNoTesseract.Error()):
			return "OCR failed: tesseract isn't installed"
		case strings.Contains(msg, ocr.ErrNoRapidOCR.Error()):
			return "OCR failed: rapidocr isn't available"
		}
		return "OCR failed: " + lowerFirst(clip(innermost(msg[strings.Index(low, "ocr: ")+len("ocr: "):])))
	}

	switch {
	case strings.Contains(low, "refused"), strings.Contains(low, "no connection could be made"):
		return by("Connection refused by HOST", "Connection refused")
	case strings.Contains(low, "no such host"), strings.Contains(low, "name or service not known"),
		strings.Contains(low, "name resolution"):
		return by("Host HOST not found", "Host not found")
	case strings.Contains(low, "i/o timeout"), strings.Contains(low, "deadline exceeded"),
		strings.Contains(low, "client.timeout"), strings.Contains(low, "timed out"), strings.Contains(low, "timeout"):
		return by("Timed out reaching HOST", "Timed out")
	case strings.Contains(low, "no route to host"), strings.Contains(low, "network is unreachable"),
		strings.Contains(low, "unreachable network"), strings.Contains(low, "host is unreachable"):
		return by("No route to HOST", "Network unreachable")
	case strings.Contains(low, "connection reset"), strings.Contains(low, "forcibly closed"):
		return by("Connection reset by HOST", "Connection reset")
	}
	if strings.Contains(low, "401 unauthorized") || strings.Contains(low, "403 forbidden") {
		return by("HOST turned down the login", "The camera turned down the login")
	}
	if m := errStatusRe.FindStringSubmatch(msg); m != nil {
		return by("HOST answered HTTP "+m[1], "Camera answered HTTP "+m[1])
	}
	switch {
	case strings.Contains(low, "ffmpeg") && (strings.Contains(low, "executable file not found") ||
		strings.Contains(low, "no such file or directory") && !strings.Contains(low, "://")):
		return "ffmpeg isn't installed"
	case strings.Contains(low, "decode") || strings.Contains(low, "image header") || strings.Contains(low, "unknown format"):
		return by("HOST didn't send a readable image", "Not a readable image")
	case strings.Contains(low, "exceeds") && strings.Contains(low, "cap"):
		return by("HOST sent an image that is too large", "Image too large")
	case strings.HasPrefix(low, "ffmpeg: ") || strings.Contains(low, " ffmpeg: "):
		return "ffmpeg couldn't read the stream: " + lowerFirst(clip(firstLine(innermost(msg))))
	}
	return upperFirst(clip(firstLine(innermost(msg))))
}

// errHost returns host[:port] of the first URL in msg, without userinfo.
func errHost(msg string) string {
	raw := errURLRe.FindString(msg)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(strings.TrimRight(raw, `.,;:)"`))
	if err != nil {
		return ""
	}
	return u.Host
}

// innermost is the last ": "-separated segment of an error chain — the
// actual cause — with ffmpeg context addresses stripped. A segment that is
// only a quoted value (`unknown service: "nope"`) keeps its label.
func innermost(msg string) string {
	msg = strings.TrimSpace(ffmpegCtxRe.ReplaceAllString(msg, ""))
	parts := strings.Split(msg, ": ")
	i := len(parts) - 1
	for i > 0 && (len(strings.TrimSpace(parts[i])) < 4 || strings.HasPrefix(strings.TrimSpace(parts[i]), `"`)) {
		i--
	}
	return strings.TrimSpace(strings.Join(parts[i:], ": "))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= summaryMaxRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:summaryMaxRunes-1])) + "…"
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// lowerFirst lowercases a leading capital unless the word looks like an
// acronym or a name ("HTTP", "RTSP", "Error" stays readable either way).
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || !unicode.IsUpper(r) {
		return s
	}
	if r2, _ := utf8.DecodeRuneInString(s[n:]); unicode.IsUpper(r2) {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}

// fieldError is one rejected form field: which input it belongs to (the
// form field name, "" for a problem with no single field) and what to say.
type fieldError struct {
	Field string
	Msg   string
}

// configFieldErrors maps config.Validate's wording (internal/config keeps
// it, because the CLI and the YAML loader report the same errors against
// the file) to the form field it concerns and a sentence for the form.
// Matched by substring after the "watch N:" / "watch "name":" prefix.
var configFieldErrors = []struct {
	needle, field, msg string
}{
	{"interval must be >= 1s", "interval", "Interval must be at least 1s, for example 5s or 1m30s."},
	{"max_interval must be >= interval", "max_interval", "Max interval can't be shorter than Interval. Leave it empty to turn it off."},
	{"health_after must be >= 0", "health_after", "Health after can't be negative."},
	{"region must be finite", "region", "The region isn't a valid rectangle. Drag on the snapshot to draw it again."},
	{"region must be normalized", "region", "The region must fit inside the frame. Drag on the snapshot to draw it again."},
	{"unknown trigger type", "ttype", "Choose a trigger type."},
	{"unknown engine", "engine", "Choose an engine."},
	{"trigger threshold must be finite", "tthreshold", "Threshold must be a number."},
	{"pixel_change threshold must be > 0", "tthreshold", "Threshold must be above 0: the percent of the region that has to change."},
	{"ocr_match requires a pattern", "pattern", "ocr_match needs a pattern to look for, for example (?i)print complete."},
	{"numeric op must be gt or lt", "op", "Choose gt or lt: whether the reading must go above or below Threshold."},
	{"preprocess threshold must be 0-255", "pp_threshold", "Binarize must be between 0 and 255."},
	{"preprocess upscale must be 0-4", "pp_upscale", "Upscale must be off, 2x, 3x or 4x."},
	{"notify: empty URL", "notify", "Remove the empty notify line."},
	{"source is required", "source", "Enter the camera's source URL."},
	{"source has no arguments", "source", "An ffmpeg: source needs its input arguments after the colon, for example ffmpeg:-i rtsp://cam/stream."},
	{"name is required", "name", "Enter a name for the watch."},
	{"leading or trailing whitespace", "name", "The name can't start or end with a space."},
	{"must not contain '/', '?', '#', or control characters", "name", "Names can't contain /, ?, # or control characters."},
	{"is reserved: it can't be used in a URL path", "name", "A name can't be just \".\" or \"..\": a link to it would lead somewhere else."},
}

var (
	dupNameRe    = regexp.MustCompile(`^duplicate watch name ("(?:[^"\\]|\\.)*")$`)
	badSourceRe  = regexp.MustCompile(`^unsupported source ("(?:[^"\\]|\\.)*"): expected one of (.+)$`)
	badPatternRe = regexp.MustCompile(`trigger: pattern: (?:error parsing regexp: )?(.+)$`)
	badNotifyURL = regexp.MustCompile(`notify: invalid URL ("(?:[^"\\]|\\.)*")`)
	// A regexp/syntax.Error prints as "code: `fragment`".
	regexpCodeRe  = regexp.MustCompile("^(.+?): `(.*)`$")
	repeatCountRe = regexp.MustCompile(`^\{(\d+)(?:,(\d*))?\}$`)
)

// repeatCountProblem says why the repeat count frag ("{2,1}", "{1001}")
// was refused. Go reports three different problems with the one code
// "invalid repeat count": a minimum above the maximum, a count above 1000,
// and nested repeats whose counts multiply past 1000 (where frag, the
// outer count, can be small on its own).
func repeatCountProblem(frag string) string {
	m := repeatCountRe.FindStringSubmatch(frag)
	if m == nil {
		return `"` + frag + `" isn't a valid repeat count (the most is 1000).`
	}
	lo, _ := strconv.Atoi(m[1])
	hi := -1
	if m[2] != "" {
		hi, _ = strconv.Atoi(m[2])
	}
	switch {
	case hi >= 0 && lo > hi:
		return `"` + frag + `" isn't a valid repeat count: the minimum is larger than the maximum.`
	case lo > 1000 || hi > 1000:
		return `"` + frag + `" asks for too many repeats (the most is 1000).`
	default:
		return `"` + frag + `" repeats a group that already repeats, and together they come to more than 1000 repeats.`
	}
}

// regexpProblems says each regexp/syntax error code in words; %s is the
// offending fragment where showing it helps. Go's own text ("missing
// closing ): `(`") reads as a pile of punctuation inside a sentence.
var regexpProblems = map[string]string{
	"missing closing )":                       `A "(" is never closed.`,
	"unexpected )":                            `There's a ")" with no "(" before it.`,
	"missing closing ]":                       `A "[" is never closed.`,
	"missing argument to repetition operator": `"%s" has nothing before it to repeat. Put \ in front of it to match it literally.`,
	"invalid nested repetition operator":      `"%s" repeats a repeat.`,
	"invalid escape sequence":                 `"%s" isn't an escape Go understands.`,
	"trailing backslash at end of expression": `It ends in a lone \.`,
	"invalid character class range":           `"%s" isn't a valid character range.`,
	"invalid character class":                 `"%s" isn't a valid character class.`,
	"invalid or unsupported Perl syntax":      `"%s" isn't supported: Go regular expressions have no lookarounds or backreferences.`,
	"invalid named capture":                   `"%s" isn't a valid named group.`,
	"invalid repetition operator":             `"%s" isn't a valid repeat.`,
	"invalid UTF-8":                           `It contains invalid UTF-8.`,
	"expression nests too deeply":             `It nests too deeply.`,
	"expression too large":                    `It's too large.`,
}

// regexpProblem is detail (a regexp/syntax error without its "error
// parsing regexp: " prefix) as one sentence.
func regexpProblem(detail string) string {
	if m := regexpCodeRe.FindStringSubmatch(detail); m != nil {
		if m[1] == "invalid repeat count" {
			return repeatCountProblem(m[2])
		}
		if f, ok := regexpProblems[m[1]]; ok {
			if strings.Contains(f, "%s") {
				return fmt.Sprintf(f, m[2])
			}
			return f
		}
		return upperFirst(m[1]) + ` near "` + m[2] + `".`
	}
	return upperFirst(strings.TrimSuffix(detail, ".")) + "."
}

// friendlyConfigError maps a config.Validate (or mutateConfig) error to the
// form field it concerns and a sentence for a person. An error it doesn't
// recognise keeps its own wording, minus the "watch N:" prefix, capitalised,
// with Field "".
func friendlyConfigError(err error) fieldError {
	msg := strings.TrimSpace(err.Error())
	bare := watchPrefix.ReplaceAllString(msg, "")
	if m := dupNameRe.FindStringSubmatch(bare); m != nil {
		return fieldError{"name", "A watch named " + m[1] + " already exists."}
	}
	if m := badSourceRe.FindStringSubmatch(bare); m != nil {
		list := strings.Fields(m[2])
		return fieldError{"source", "That source isn't supported. It must start with " + joinOr(list) + "."}
	}
	if m := badPatternRe.FindStringSubmatch(bare); m != nil {
		return fieldError{"pattern", "Pattern isn't a valid regular expression. " + regexpProblem(m[1])}
	}
	if m := badNotifyURL.FindStringSubmatch(bare); m != nil {
		return fieldError{"notify", "Notify URL " + m[1] + " needs a scheme, for example ntfy://ntfy.sh/topic."}
	}
	for _, c := range configFieldErrors {
		if strings.Contains(bare, c.needle) {
			return fieldError{c.field, c.msg}
		}
	}
	return fieldError{"", upperFirst(bare)}
}

// friendlyStartError is the reason a saved watch didn't start, without the
// "watch "name":" prefix and shoutrrr's URL-list wrapper.
func friendlyStartError(err error) string {
	msg := watchPrefix.ReplaceAllString(strings.TrimSpace(err.Error()), "")
	switch {
	case errors.Is(err, ocr.ErrNoTesseract):
		return "This trigger type reads text with tesseract, and tesseract isn't installed. Install it, or switch Engine to sevenseg or rapidocr."
	case errors.Is(err, ocr.ErrNoRapidOCR):
		return "This watch reads with rapidocr, and no Python with the rapidocr package was found. Run pip install rapidocr onnxruntime, or switch Engine."
	case strings.HasPrefix(msg, "notify: "):
		return "A notify URL can't be used: " + innermost(notifyWrapRe.ReplaceAllString(strings.TrimPrefix(msg, "notify: "), "")) + "."
	case strings.HasPrefix(msg, "source: "):
		return "The source can't be used: " + strings.TrimSuffix(innermost(strings.TrimPrefix(msg, "source: ")), ".") + "."
	}
	return upperFirst(msg)
}

func joinOr(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
