// Package docscheck holds no code, only tests that keep the Markdown docs
// honest: every relative link and #anchor resolves, every YAML config in
// them loads, and every recipe reads its committed sample frame the way the
// recipe says it does.
//
// A recipe ties a watch to a sample with an HTML comment, which GitHub
// doesn't render:
//
//	<!-- sample-check: <watch name> <image path> met|not-met|skipped [reads=<text>] -->
//
// The image path is relative to the Markdown file. The watch is looked up in
// the file's ```yaml blocks. The check crops the image to the watch's region,
// applies its preprocess settings, reads it with its engine and asks the
// Test page's own verdict code (web.Verdict) what Test this region says
// about that reading: "Condition met" (met), "Condition not met" (not-met)
// or "Reading skipped" (skipped: a numeric watch on the seven-segment
// decoder ignores a reading with a '?' in it). reads= compares the text
// exactly (use it only for sevenseg, whose output doesn't depend on a
// tesseract version).
package docscheck
