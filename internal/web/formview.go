package web

import (
	"strconv"
	"strings"

	"github.com/darrenhuai/watchglass/internal/config"
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
}

const sevenSegAlt = "sevenseg (the seven-segment decoder)"

func engineNoteFor(w config.Watch, tess, rapid bool) engineNote {
	pixel := w.Trigger.Type == "pixel_change"
	missing := missingEngine(w.Engine, tess, rapid)
	// The row itself is hidden for a pixel_change watch whose engine is
	// present on a box with tesseract (updateTriggerFields in app.js).
	if pixel && tess && missing == "" {
		return engineNote{}
	}
	var text string
	switch missing {
	case "":
		if !pixel {
			return engineNote{}
		}
		text = "Not used by pixel_change. It only matters if Type becomes a text trigger."
	case "rapidocr":
		if pixel {
			text = "Not used by pixel_change. rapidocr isn't available on this box, so the text triggers are locked on this engine: pip install rapidocr onnxruntime, or switch Engine."
		} else {
			text = "rapidocr isn't available on this box (Python with the rapidocr package). Install it with pip install rapidocr onnxruntime, or switch Engine."
		}
	default:
		if pixel {
			alts := sevenSegAlt
			if rapid {
				alts = "rapidocr or " + sevenSegAlt
			}
			text = "Not used by pixel_change. Tesseract isn't on PATH, so the text triggers are locked while Engine is tesseract: switch to " + alts + " first, or install tesseract."
		} else {
			alts := sevenSegAlt + " for digit displays"
			if rapid {
				alts = "rapidocr for printed text or " + alts
			}
			text = "Tesseract isn't on PATH, so this trigger can't run. Switch Engine to " + alts + ", or install tesseract."
		}
	}
	return engineNote{Text: text, Warn: missing != "" && !pixel, Show: true}
}
