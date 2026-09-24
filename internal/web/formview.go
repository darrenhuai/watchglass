package web

import (
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/ocr"
)

// preprocessSet reports whether any preprocess option is on. The detail
// form folds the Preprocess section away while everything in it is at its
// default (most watches never touch it) and keeps it open when a setting
// is tuned, so nothing configured is ever out of sight.
func preprocessSet(p config.Preprocess) bool {
	return p.Grayscale || p.Invert || p.Threshold > 0 || p.Upscale >= 2
}

// preprocessSummary is the one-line readout beside the folded section's
// title: "off", or the options that are on, e.g. "grayscale · binarize 128
// · 2×". app.js (ppSummary) keeps it in step as the controls change; the
// wording here and there must agree.
func preprocessSummary(p config.Preprocess) string {
	var parts []string
	if p.Grayscale {
		parts = append(parts, "grayscale")
	}
	if p.Invert {
		parts = append(parts, "invert")
	}
	if p.Threshold > 0 {
		parts = append(parts, "binarize "+strconv.Itoa(p.Threshold))
	}
	if p.Upscale >= 2 {
		parts = append(parts, strconv.Itoa(p.Upscale)+"×")
	}
	if len(parts) == 0 {
		return "off"
	}
	return strings.Join(parts, " · ")
}

// triggerLabels are the short human readings of each trigger type, shown
// beside its id in the Type select ("ocr_match — text matches pattern")
// and as the title of the index pill. The ids stay: they are what
// config.yaml takes.
var triggerLabels = map[string]string{
	"pixel_change": "pixels change",
	"ocr_match":    "text matches pattern",
	"ocr_changed":  "text changes",
	"numeric":      "number vs threshold",
}

func triggerLabel(t string) string { return triggerLabels[t] }

// engineName is the engine a watch reads with: "" means tesseract.
func engineName(e string) string {
	if e == "" {
		return "tesseract"
	}
	return e
}

// missingEngine names the external engine the watch's engine needs but
// this box lacks, or "" when it can run. Mirrors app.js engineMissing().
func missingEngine(engine string, tess, rapid bool) string {
	switch engineName(engine) {
	case "sevenseg":
		return ""
	case "rapidocr":
		if !rapid {
			return "rapidocr"
		}
		return ""
	}
	if !tess {
		return "tesseract"
	}
	return ""
}

// engineNote is the one note under the Engine select. The server renders
// it for the saved watch (and for a page without JS); app.js
// (engineNoteFor) rebuilds it as Type and Engine change, so the copy here
// and there must agree. Warn marks a selected trigger type that can't run
// on the selected engine: the type stays selected (a disabled selected
// option would be dropped from the form), the selects are styled as
// blocked, and this note says why and what to do.
type engineNote struct {
	Text string
	Warn bool
	Show bool
	// Quiet: the note is there for screen readers only (sr-only). That is
	// the plain pixel_change case: the set-aside row and Type's help
	// already say Engine isn't used, and a visible line that goes away
	// when a text type is picked would pull Type up by that line.
	Quiet bool
}

const sevenSegAlt = "sevenseg (the seven-segment decoder)"

// engineNoteFor's install hint names the command for this OS
// (ocr.TesseractInstall); app.js gets the same command from the Engine
// select's data-tesseract-install.
func engineNoteFor(w config.Watch, tess, rapid bool) engineNote {
	pixel := w.Trigger.Type == "pixel_change"
	missing := missingEngine(w.Engine, tess, rapid)
	install := "install tesseract (" + ocr.TesseractInstall() + ") and restart watchglass"
	var text string
	switch missing {
	case "":
		if !pixel {
			return engineNote{}
		}
		text = "pixel_change compares pixels and doesn't use Engine."
	case "rapidocr":
		if pixel {
			text = "pixel_change doesn't use Engine, but rapidocr isn't available on this box, so the text types are locked on it: pip install rapidocr onnxruntime and restart watchglass, or switch Engine."
		} else {
			text = "rapidocr isn't available on this box (Python with the rapidocr package). Install it with pip install rapidocr onnxruntime and restart watchglass, or switch Engine."
		}
	default:
		if pixel {
			alts := sevenSegAlt
			if rapid {
				alts = "rapidocr or " + sevenSegAlt
			}
			text = "pixel_change doesn't use Engine, but tesseract isn't installed, so the text types are locked while Engine is tesseract: switch to " + alts + ", or " + install + "."
		} else {
			alts := sevenSegAlt + " for digit displays"
			if rapid {
				alts = "rapidocr for printed text or " + alts
			}
			text = "Tesseract isn't installed, so this trigger can't run. Switch Engine to " + alts + ", or " + install + "."
		}
	}
	return engineNote{Text: text, Warn: missing != "" && !pixel, Show: true, Quiet: missing == ""}
}

// confirmHelps say what Confirm counts for each type (trigger.go's
// candidate key); pixel_change doesn't confirm and its row is hidden.
// app.js CONFIRM_HELP holds the same sentences.
var confirmHelps = map[string]string{
	"ocr_match":   "Readings in a row that must match Pattern before it fires. Blank means 3.",
	"ocr_changed": "Readings in a row that must show the same new text before it counts. Blank means 3.",
	"numeric":     "Readings in a row that must be on the same side of the threshold. Blank means 3.",
}

func confirmHelp(t string) string { return confirmHelps[t] }

// patternHelps are the hints under Pattern (app.js PATTERN_HELP holds the
// same text). The row only shows for ocr_match and numeric. A `quoted`
// stretch is a machine token, set in monospace like the pattern itself.
var patternHelps = map[string]string{
	"ocr_match": "Plain words work; `(?i)` ignores case; `a|b` matches either.",
	"numeric":   "Leave it empty to use the first number read.",
}

// patternHelp is patternHelps[t] as markup: the text escaped, each
// `quoted` stretch in a <span class="mono">.
func patternHelp(t string) template.HTML {
	var b strings.Builder
	for i, part := range strings.Split(patternHelps[t], "`") {
		if i%2 == 1 {
			b.WriteString(`<span class="mono">` + template.HTMLEscapeString(part) + `</span>`)
		} else {
			b.WriteString(template.HTMLEscapeString(part))
		}
	}
	return template.HTML(b.String()) // #nosec G203 -- fixed strings, every part escaped
}

// isFresh reports whether a watch's trigger is still what Create gave it
// (web.go create): pixel_change at 25% with a 5m cooldown, the default
// engine, no preprocessing. Only then does the detail page offer the
// "What are you watching?" presets, which overwrite exactly those fields.
func isFresh(w config.Watch) bool {
	t := w.Trigger
	return w.Engine == "" && w.Preprocess == (config.Preprocess{}) &&
		t.Type == "pixel_change" && t.Threshold == 25 && time.Duration(t.Cooldown) == 5*time.Minute &&
		t.Pattern == "" && t.Op == "" && (t.Confirm == 0 || t.Confirm == 3)
}

// userinfoPassword is the password part of a URL's userinfo, wherever a
// URL sits in a source: the whole of an http(s) or rtsp source, or one of
// an ffmpeg: source's input arguments. The password runs to the LAST "@"
// before the path, query or fragment, as net/url splits it: Go accepts
// http://admin:p@ss@cam/ with a raw "@" in the password, and stopping at
// the first "@" would leave "ss@" on the page.
var userinfoPassword = regexp.MustCompile(`(://[^/?#@\s:]*):[^/?#\s]*@`)

// redactSource is a watch's source for the page, with any password masked
// the way url.Redacted masks it in grab errors ("user:xxxxx@"). The page
// has no field that edits the source, so the password never needs to be
// on it; the header line is what people screenshot.
func redactSource(src string) string {
	return userinfoPassword.ReplaceAllString(src, "${1}:xxxxx@")
}
