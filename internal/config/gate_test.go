package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gatedYAML = `# my meter
watches:
  - name: meter-import
    source: http://cam/snap.jpg
    engine: sevenseg
    region: {x: 0.315, y: 0.392, w: 0.41, h: 0.074}
    # read the total only while the code box shows 1.8.0
    gate:
      region: {x: 0, y: 0.39, w: 0.1, h: 0.04} # the OBIS code
      pattern: '^1\.8\.0$'
    trigger:
      type: value
    unit: kWh
    device_class: energy
`

func TestGateLoads(t *testing.T) {
	cfg := loadOrFatal(t, writeTemp(t, gatedYAML))
	w := cfg.Watches[0]
	if w.Gate == nil {
		t.Fatal("gate not loaded")
	}
	if w.Gate.Region != (Region{X: 0, Y: 0.39, W: 0.1, H: 0.04}) || w.Gate.Pattern != `^1\.8\.0$` {
		t.Errorf("gate = %+v", *w.Gate)
	}
	// Engine and preprocess default to the watch's.
	if w.GateEngine() != "sevenseg" || w.GatePreprocess() != w.Preprocess {
		t.Errorf("gate engine %q, preprocess %+v", w.GateEngine(), w.GatePreprocess())
	}
	w.Gate.Engine = "tesseract"
	w.Gate.Preprocess = &Preprocess{Threshold: 128}
	if w.GateEngine() != "tesseract" || w.GatePreprocess().Threshold != 128 {
		t.Errorf("gate's own engine and preprocess not used: %q %+v", w.GateEngine(), w.GatePreprocess())
	}
}

func TestGateIsChecked(t *testing.T) {
	good := func() Watch {
		return Watch{Name: "m", Source: "http://cam/x", Region: Region{W: 1, H: 1}, Engine: "sevenseg",
			Trigger: Trigger{Type: "value"},
			Gate:    &Gate{Region: Region{X: 0.1, Y: 0.1, W: 0.2, H: 0.1}, Pattern: "^2$"}}
	}
	for _, tc := range []struct {
		name string
		edit func(w *Watch)
		want string
	}{
		{"pixel_change", func(w *Watch) { w.Trigger = Trigger{Type: "pixel_change", Threshold: 5} }, "not pixel_change"},
		{"no pattern", func(w *Watch) { w.Gate.Pattern = "" }, "gate needs a pattern"},
		{"bad pattern", func(w *Watch) { w.Gate.Pattern = "(" }, "gate pattern"},
		{"outside", func(w *Watch) { w.Gate.Region.X = 0.95 }, "gate region"},
		{"empty", func(w *Watch) { w.Gate.Region.W = 0 }, "gate region"},
		{"engine", func(w *Watch) { w.Gate.Engine = "easyocr" }, "unknown gate engine"},
		{"preprocess", func(w *Watch) { w.Gate.Preprocess = &Preprocess{Upscale: 9} }, "gate preprocess upscale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := good()
			tc.edit(&w)
			c := Config{Watches: []Watch{w}}
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
	c := Config{Watches: []Watch{good()}}
	if err := c.Validate(); err != nil {
		t.Fatalf("good gate refused: %v", err)
	}
}

// Validate runs on copies of the live config (the web UI's save, Save's
// read-back check); clamping the gate's region must not reach the live one.
func TestValidateDoesNotTouchSharedGate(t *testing.T) {
	g := &Gate{Region: Region{X: 0.0063, Y: 0, W: 0.9938, H: 0.5}, Pattern: "x"}
	live := Config{Watches: []Watch{{Name: "m", Source: "http://cam/x", Region: Region{W: 1, H: 1}, Trigger: Trigger{Type: "value"}, Gate: g}}}
	next := live
	next.Watches = append([]Watch(nil), live.Watches...)
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
	if g.Region.W != 0.9938 {
		t.Errorf("Validate changed the shared gate: %+v", g.Region)
	}
	if next.Watches[0].Gate.Region.W != 1-0.0063 {
		t.Errorf("the validated copy wasn't clamped: %+v", next.Watches[0].Gate.Region)
	}
}

// The gate survives a UI save with the comments around and inside it, its
// region stays on one line with a 0 coordinate kept, and a removed gate
// goes from the file.
func TestGateSurvivesSaveWithComments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(gatedYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := loadOrFatal(t, p)
	cfg.Watches[0].Gate.Pattern = `^2\.8\.0$`
	cfg.Watches[0].Trigger.Confirm = 2
	saveOrFatal(t, p, cfg)
	raw := readFile(t, p)
	for _, want := range []string{
		"# my meter",
		"# read the total only while the code box shows 1.8.0",
		"region: {x: 0, y: 0.39, w: 0.1, h: 0.04} # the OBIS code",
		`pattern: '^2\.8\.0$'`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("saved file lost %q:\n%s", want, raw)
		}
	}
	back := loadOrFatal(t, p)
	if g := back.Watches[0].Gate; g == nil || g.Pattern != `^2\.8\.0$` || g.Region.X != 0 || g.Region.W != 0.1 {
		t.Fatalf("gate read back as %+v", g)
	}

	// Moving the gate box keeps it on one line.
	back.Watches[0].Gate.Region = Region{X: 0, Y: 0, W: 0.2, H: 0.05}
	saveOrFatal(t, p, back)
	raw = readFile(t, p)
	if !strings.Contains(raw, "region: {x: 0, y: 0, w: 0.2, h: 0.05}") {
		t.Errorf("gate region not written whole on one line:\n%s", raw)
	}

	// A new gate on a watch that had none is written as a person would.
	plain := strings.Replace(gatedYAML, gatedYAML[strings.Index(gatedYAML, "    # read"):strings.Index(gatedYAML, "    trigger:")], "", 1)
	if err := os.WriteFile(p, []byte(plain), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = loadOrFatal(t, p)
	if cfg.Watches[0].Gate != nil {
		t.Fatal("fixture still has a gate")
	}
	cfg.Watches[0].Gate = &Gate{Region: Region{X: 0, Y: 0.5, W: 0.25, H: 0.1}, Pattern: "^2$"}
	saveOrFatal(t, p, cfg)
	raw = readFile(t, p)
	if !strings.Contains(raw, "    gate:\n      region: {x: 0, y: 0.5, w: 0.25, h: 0.1}\n      pattern: ^2$\n") {
		t.Errorf("new gate written as:\n%s", raw)
	}
	if strings.Contains(raw, "engine: \"\"") || strings.Contains(raw, "preprocess:") {
		t.Errorf("new gate spells out its defaults:\n%s", raw)
	}

	// Taking the gate off removes the block.
	cfg.Watches[0].Gate = nil
	saveOrFatal(t, p, cfg)
	if raw = readFile(t, p); strings.Contains(raw, "gate") {
		t.Errorf("removed gate still in the file:\n%s", raw)
	}
}
