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
