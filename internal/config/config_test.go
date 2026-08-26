package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const validYAML = `
watches:
  - name: printer-lcd
    source: http://cam.local/snapshot.jpg
    interval: 5s
    region: {x: 0.1, y: 0.2, w: 0.5, h: 0.1}
    trigger:
      type: ocr_match
      pattern: "(?i)print complete"
      confirm: 2
      cooldown: 10m
    notify:
      - ntfy://ntfy.sh/mytopic
`

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Watches) != 1 {
		t.Fatalf("want 1 watch, got %d", len(cfg.Watches))
	}
	w := cfg.Watches[0]
	if w.Name != "printer-lcd" {
		t.Errorf("name = %q", w.Name)
	}
	if time.Duration(w.Interval) != 5*time.Second {
		t.Errorf("interval = %v", time.Duration(w.Interval))
	}
	if w.Region.W != 0.5 {
		t.Errorf("region.w = %v", w.Region.W)
	}
	if time.Duration(w.Trigger.Cooldown) != 10*time.Minute {
		t.Errorf("cooldown = %v", time.Duration(w.Trigger.Cooldown))
	}
	if w.Trigger.Confirm != 2 {
		t.Errorf("confirm = %v", w.Trigger.Confirm)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
watches:
  - name: a
    source: http://x/snap.jpg
    region: {x: 0, y: 0, w: 1, h: 1}
    trigger: {type: pixel_change, threshold: 10}
    notify: [ntfy://ntfy.sh/t]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	w := cfg.Watches[0]
	if time.Duration(w.Interval) != 5*time.Second {
		t.Errorf("default interval = %v, want 5s", time.Duration(w.Interval))
	}
	if w.Trigger.Confirm != 3 {
		t.Errorf("default confirm = %d, want 3", w.Trigger.Confirm)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"missing name":     `watches: [{source: "http://x", region: {x: 0, y: 0, w: 1, h: 1}, trigger: {type: pixel_change}}]`,
		"region overflow":  `watches: [{name: a, source: "http://x", region: {x: 0.6, y: 0, w: 0.5, h: 1}, trigger: {type: pixel_change}}]`,
		"zero-size region": `watches: [{name: a, source: "http://x", region: {x: 0, y: 0, w: 0, h: 1}, trigger: {type: pixel_change}}]`,
		"bad trigger type": `watches: [{name: a, source: "http://x", region: {x: 0, y: 0, w: 1, h: 1}, trigger: {type: banana}}]`,
		"duplicate names":  `watches: [{name: a, source: "http://x", region: {x: 0, y: 0, w: 1, h: 1}, trigger: {type: pixel_change}}, {name: a, source: "http://y", region: {x: 0, y: 0, w: 1, h: 1}, trigger: {type: pixel_change}}]`,
	}
	for label, yml := range cases {
		if _, err := Load(writeTemp(t, yml)); err == nil {
			t.Errorf("%s: expected error, got nil", label)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	cfg := &Config{Watches: []Watch{{
		Name:     "rt",
		Source:   "http://cam/snap.jpg",
		Interval: Duration(7 * time.Second),
		Region:   Region{X: 0.1, Y: 0.2, W: 0.3, H: 0.4},
		Preprocess: Preprocess{Grayscale: true, Invert: true, Threshold: 128, Upscale: 2},
		Trigger:  Trigger{Type: "ocr_match", Pattern: "(?i)done", Confirm: 2, Cooldown: Duration(10 * time.Minute)},
		Notify:   []string{"ntfy://ntfy.sh/t"},
	}}}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	w := got.Watches[0]
	if time.Duration(w.Interval) != 7*time.Second {
		t.Errorf("interval = %v", time.Duration(w.Interval))
	}
	if time.Duration(w.Trigger.Cooldown) != 10*time.Minute {
		t.Errorf("cooldown = %v", time.Duration(w.Trigger.Cooldown))
	}
	if w.Preprocess != (Preprocess{Grayscale: true, Invert: true, Threshold: 128, Upscale: 2}) {
		t.Errorf("preprocess = %+v", w.Preprocess)
	}
	if w.Region.W != 0.3 {
		t.Errorf("region = %+v", w.Region)
	}
}

func TestValidateRejectsBadPreprocess(t *testing.T) {
	base := func() *Config {
		return &Config{Watches: []Watch{{
			Name: "a", Source: "http://x",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}}}
	}
	tooHot := base()
	tooHot.Watches[0].Preprocess.Threshold = 300
	if err := tooHot.Validate(); err == nil {
		t.Error("threshold 300: expected error")
	}
	tooBig := base()
	tooBig.Watches[0].Preprocess.Upscale = 9
	if err := tooBig.Validate(); err == nil {
		t.Error("upscale 9: expected error")
	}
	ok := base()
	if err := ok.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestSaveWritesHumanReadableDurations(t *testing.T) {
	cfg := &Config{Watches: []Watch{{
		Name: "a", Source: "http://x", Interval: Duration(5 * time.Second),
		Region: Region{X: 0, Y: 0, W: 1, H: 1}, Trigger: Trigger{Type: "pixel_change", Threshold: 10},
	}}}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "interval: 5s") {
		t.Errorf("expected human-readable duration, got:\n%s", raw)
	}
}
