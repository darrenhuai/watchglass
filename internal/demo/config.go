package demo

import "strings"

// PrinterRegion and ScaleRegion frame the status line of the printer LCD
// and the four digits of the seven-segment readout, as fractions of the
// frame (640x360 and 640x240).
const (
	PrinterRegion = "{x: 0.0312, y: 0.4167, w: 0.9375, h: 0.2222}"
	ScaleRegion   = "{x: 0.14, y: 0.14, w: 0.72, h: 0.72}"
	// ScaleThreshold is above every reading but the held one: the frames
	// read 23.5, 23.7, 24.1, 24.6, 25.0, then hold 25.3.
	ScaleThreshold = "25"
)

// Config is the config file `watchglass -demo` starts from: demo-printer,
// which fires when the LCD says PRINT COMPLETE, and demo-scale, which
// fires when the readout goes above 25. readText is whether an engine that
// reads printed text (tesseract) is available; without one, demo-printer
// watches for the pixels changing instead, which still fires when the LCD
// flips to the inverted PRINT COMPLETE screen.
func Config(readText bool) []byte {
	printerTrigger := `    trigger:
      type: ocr_match
      pattern: "(?i)print complete"
      confirm: 2
      cooldown: 30s
`
	if !readText {
		printerTrigger = `    # tesseract isn't installed, so this watches pixels instead of reading
    # the text. Install tesseract and restart the demo to see ocr_match.
    trigger:
      type: pixel_change
      threshold: 20
      cooldown: 30s
`
	}
	var b strings.Builder
	b.WriteString(`# watchglass demo. This file is written fresh every time watchglass starts
# with -demo (or WATCHGLASS_DEMO=1); edits made in the web UI last until the
# next start. The sources are the fake cameras built into watchglass: each
# plays a 40 second loop, five frames 4 seconds apart and then the last
# frame held for 20 seconds.
watches:
  - name: demo-printer
    source: demo:printer
    interval: 2s
    region: ` + PrinterRegion + `
`)
	b.WriteString(printerTrigger)
	b.WriteString(`    notify: []
  - name: demo-scale
    source: demo:sevenseg
    interval: 2s
    region: ` + ScaleRegion + `
    engine: sevenseg
    trigger:
      type: numeric
      pattern: "([0-9.]+)"
      op: gt
      threshold: ` + ScaleThreshold + `
      confirm: 2
      cooldown: 30s
    notify: []
`)
	return []byte(b.String())
}
