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
		Name:       "rt",
		Source:     "http://cam/snap.jpg",
		Interval:   Duration(7 * time.Second),
		Region:     Region{X: 0.1, Y: 0.2, W: 0.3, H: 0.4},
		Preprocess: Preprocess{Grayscale: true, Invert: true, Threshold: 128, Upscale: 2},
		Trigger:    Trigger{Type: "ocr_match", Pattern: "(?i)done", Confirm: 2, Cooldown: Duration(10 * time.Minute)},
		Notify:     []string{"ntfy://ntfy.sh/t"},
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

func TestSourceKind(t *testing.T) {
	cases := map[string]string{
		"rtsp://cam/stream":          "ffmpeg",
		"rtsps://cam/stream":         "ffmpeg",
		"http://cam/snapshot.jpg":    "http",
		"https://cam/snapshot.jpg":   "http",
		"ffmpeg:-f lavfi -i testsrc": "ffmpeg",
		"v4l2:/dev/video0":           "ffmpeg",
		"dshow:video=Integrated Cam": "ffmpeg",
	}
	for src, want := range cases {
		got, err := SourceKind(src)
		if err != nil {
			t.Errorf("SourceKind(%q): unexpected error %v", src, err)
			continue
		}
		if got != want {
			t.Errorf("SourceKind(%q) = %q, want %q", src, got, want)
		}
	}
	for _, bad := range []string{"", "cam.local/snap.jpg", "ftp://cam/x", "rtsp", "file:///tmp/x.png", "ffmpeg:", "ffmpeg:   "} {
		if _, err := SourceKind(bad); err == nil {
			t.Errorf("SourceKind(%q): expected error", bad)
		}
	}
}

func TestValidateRejectsBadSourceAndIntervals(t *testing.T) {
	base := func() *Config {
		return &Config{Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Interval: Duration(5 * time.Second),
			Region:   Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger:  Trigger{Type: "pixel_change", Threshold: 10},
		}}}
	}
	badScheme := base()
	badScheme.Watches[0].Source = "ftp://cam/x"
	if err := badScheme.Validate(); err == nil {
		t.Error("unknown scheme: expected error")
	}
	shortMax := base()
	shortMax.Watches[0].MaxInterval = Duration(time.Second)
	if err := shortMax.Validate(); err == nil {
		t.Error("max_interval below interval: expected error")
	}
	negHealth := base()
	negHealth.Watches[0].HealthAfter = -1
	if err := negHealth.Validate(); err == nil {
		t.Error("negative health_after: expected error")
	}
	ok := base()
	ok.Watches[0].MaxInterval = Duration(time.Minute)
	ok.Watches[0].HealthAfter = 3
	if err := ok.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	// max_interval equal to interval is allowed (adaptive polling simply off)
	eq := base()
	eq.Watches[0].MaxInterval = eq.Watches[0].Interval
	if err := eq.Validate(); err != nil {
		t.Errorf("max_interval == interval rejected: %v", err)
	}
}

func TestValidateDefaultsHealthAfter(t *testing.T) {
	cfg := &Config{Watches: []Watch{{
		Name: "a", Source: "http://x/s.jpg",
		Region:  Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger: Trigger{Type: "pixel_change", Threshold: 10},
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Watches[0].HealthAfter != 3 {
		t.Errorf("default health_after = %d, want 3", cfg.Watches[0].HealthAfter)
	}
}

func TestMQTTBlockRoundTripAndDefaults(t *testing.T) {
	cfg := &Config{
		MQTT: &MQTT{Broker: "tcp://192.168.1.5:1883", Username: "u", Password: "p"},
		Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.MQTT.ClientID != "watchglass" || cfg.MQTT.BaseTopic != "watchglass" || cfg.MQTT.DiscoveryPrefix != "homeassistant" {
		t.Errorf("defaults not applied: %+v", cfg.MQTT)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.MQTT == nil || got.MQTT.Broker != "tcp://192.168.1.5:1883" || got.MQTT.Username != "u" {
		t.Errorf("mqtt block did not round-trip: %+v", got.MQTT)
	}
}

func TestMQTTValidation(t *testing.T) {
	base := func() *Config {
		return &Config{Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}}}
	}
	noBroker := base()
	noBroker.MQTT = &MQTT{}
	if err := noBroker.Validate(); err == nil {
		t.Error("mqtt block without broker: expected error")
	}
	badScheme := base()
	badScheme.MQTT = &MQTT{Broker: "http://broker:1883"}
	if err := badScheme.Validate(); err == nil {
		t.Error("http broker scheme: expected error")
	}
	nilBlock := base()
	if err := nilBlock.Validate(); err != nil {
		t.Errorf("nil mqtt block should validate: %v", err)
	}
}

func TestAuthValidation(t *testing.T) {
	base := func() *Config {
		return &Config{Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}}}
	}
	missing := base()
	missing.Auth = &Auth{Username: "u"}
	if err := missing.Validate(); err == nil {
		t.Error("auth without password: expected error")
	}
	ok := base()
	ok.Auth = &Auth{Username: "u", Password: "p"}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid auth rejected: %v", err)
	}
	if err := base().Validate(); err != nil {
		t.Errorf("nil auth rejected: %v", err)
	}
}

func TestHistoryDaysNormalization(t *testing.T) {
	mk := func(days int) *Config {
		return &Config{HistoryDays: days, Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}}}
	}
	cases := []struct {
		in, want int
		wantErr  bool
	}{
		{0, 30, false},  // absent/zero -> default 30
		{7, 7, false},   // explicit positive kept
		{-1, -1, false}, // -1 -> forever (preserved verbatim)
		{-2, 0, true},   // anything below -1 rejected
	}
	for _, c := range cases {
		cfg := mk(c.in)
		err := cfg.Validate()
		if c.wantErr {
			if err == nil {
				t.Errorf("history_days=%d: expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("history_days=%d: %v", c.in, err)
			continue
		}
		if cfg.HistoryDays != c.want {
			t.Errorf("history_days=%d normalized to %d, want %d", c.in, cfg.HistoryDays, c.want)
		}
	}
}

func TestAuthAndHistoryRoundTrip(t *testing.T) {
	cfg := &Config{
		HistoryDays: 7,
		Auth:        &Auth{Username: "u", Password: "p"},
		Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}},
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.HistoryDays != 7 || got.Auth == nil || got.Auth.Username != "u" || got.Auth.Password != "p" {
		t.Errorf("round trip lost fields: days=%d auth=%+v", got.HistoryDays, got.Auth)
	}
}

func TestForeverRetentionSurvivesRoundTrip(t *testing.T) {
	cfg := &Config{
		HistoryDays: -1, // forever
		Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}},
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.HistoryDays != -1 {
		t.Errorf("forever retention (-1) did not survive round-trip: got %d, want -1", got.HistoryDays)
	}
}
