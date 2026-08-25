package config

import (
	"os"
	"path/filepath"
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
