package config

import (
	"math"
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

// TestValidateTriggerConstraints mirrors trigger.New's exact constraints so
// a config that passes Validate can never fail supervisor.Start on a
// per-watch basis (Bug 1: a config that Validate waves through but Start
// rejects gets persisted, and the NEXT daemon boot crash-loops on it).
func TestValidateTriggerConstraints(t *testing.T) {
	base := func() *Config {
		return &Config{Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region: Region{X: 0, Y: 0, W: 1, H: 1},
		}}}
	}
	cases := []struct {
		name    string
		trigger Trigger
		wantErr bool
	}{
		{"pixel_change zero threshold", Trigger{Type: "pixel_change", Threshold: 0}, true},
		{"pixel_change negative threshold", Trigger{Type: "pixel_change", Threshold: -5}, true},
		{"pixel_change positive threshold ok", Trigger{Type: "pixel_change", Threshold: 10}, false},
		{"ocr_match missing pattern", Trigger{Type: "ocr_match"}, true},
		{"ocr_match empty pattern", Trigger{Type: "ocr_match", Pattern: ""}, true},
		{"ocr_match unparseable regex", Trigger{Type: "ocr_match", Pattern: "("}, true},
		{"ocr_match good pattern", Trigger{Type: "ocr_match", Pattern: "(?i)done"}, false},
		{"numeric missing op", Trigger{Type: "numeric", Threshold: 10}, true},
		{"numeric bad op", Trigger{Type: "numeric", Op: "eq", Threshold: 10}, true},
		{"numeric op with unparseable pattern", Trigger{Type: "numeric", Op: "gt", Pattern: "(", Threshold: 10}, true},
		{"numeric good op, default pattern", Trigger{Type: "numeric", Op: "gt", Threshold: 10}, false},
		{"numeric good op, explicit pattern", Trigger{Type: "numeric", Op: "lt", Pattern: `Temp: (\d+)C`, Threshold: 10}, false},
	}
	for _, c := range cases {
		cfg := base()
		cfg.Watches[0].Trigger = c.trigger
		err := cfg.Validate()
		if c.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}
	}
}

// TestValidateNotifyURLs is the second half of Bug 1: a notify URL that
// can't even parse as a URL with a scheme would previously sail through
// Validate and only blow up in supervisor.Start's shoutrrr.CreateSender.
func TestValidateNotifyURLs(t *testing.T) {
	base := func() *Config {
		return &Config{Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}}}
	}
	empty := base()
	empty.Watches[0].Notify = []string{""}
	if err := empty.Validate(); err == nil {
		t.Error("empty notify URL: expected error")
	}
	noScheme := base()
	noScheme.Watches[0].Notify = []string{"not a url with spaces and no scheme"}
	if err := noScheme.Validate(); err == nil {
		t.Error("notify URL without scheme: expected error")
	}
	bareHost := base()
	bareHost.Watches[0].Notify = []string{"ntfy.sh/topic"}
	if err := bareHost.Validate(); err == nil {
		t.Error("scheme-less notify URL: expected error")
	}
	ok := base()
	ok.Watches[0].Notify = []string{"ntfy://ntfy.sh/topic", "discord://token@id", "generic+https://host/path"}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid notify URLs rejected: %v", err)
	}
}

// TestValidateRejectsUnroutableNames is Bug 6: a watch name containing
// '/', '?', '#', or control characters produces a URL the web UI's own
// routes (/watch/{name}, /watch/{name}/save, ...) can never address again.
func TestValidateRejectsUnroutableNames(t *testing.T) {
	mk := func(name string) *Config {
		return &Config{Watches: []Watch{{
			Name: name, Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}}}
	}
	bad := []string{
		"kitchen/oven", "a?b", "x#y", "a\nb",
		"", " leading space", "trailing space ", "\ttab-prefixed",
	}
	for _, name := range bad {
		if err := mk(name).Validate(); err == nil {
			t.Errorf("name %q: expected error", name)
		}
	}
	good := []string{"kitchen oven", "3d printer bay 2", "厨房烤箱", "a-b_c.d"}
	for _, name := range good {
		if err := mk(name).Validate(); err != nil {
			t.Errorf("name %q: unexpected error: %v", name, err)
		}
	}
}

// TestValidateRejectsPixelChangeNonPositiveThreshold is a minor: threshold
// <= 0 fires on every tick since any diff percentage satisfies pct >= 0.
func TestValidateRejectsPixelChangeNonPositiveThreshold(t *testing.T) {
	mk := func(threshold float64) *Config {
		return &Config{Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: threshold},
		}}}
	}
	if err := mk(0).Validate(); err == nil {
		t.Error("threshold 0: expected error")
	}
	if err := mk(-1).Validate(); err == nil {
		t.Error("negative threshold: expected error")
	}
	if err := mk(20).Validate(); err != nil {
		t.Errorf("positive threshold rejected: %v", err)
	}
}

// TestValidateRejectsNonFiniteFloats is the other minor: NaN silently
// bypasses every plain comparison (NaN < x, NaN > x, NaN <= x are all
// false), so a NaN region or threshold slips through the existing bounds
// checks untouched.
func TestValidateRejectsNonFiniteFloats(t *testing.T) {
	base := func() *Config {
		return &Config{Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}}}
	}
	fields := []struct {
		name string
		set  func(*Config, float64)
	}{
		{"region.x", func(c *Config, v float64) { c.Watches[0].Region.X = v }},
		{"region.y", func(c *Config, v float64) { c.Watches[0].Region.Y = v }},
		{"region.w", func(c *Config, v float64) { c.Watches[0].Region.W = v }},
		{"region.h", func(c *Config, v float64) { c.Watches[0].Region.H = v }},
		{"trigger.threshold", func(c *Config, v float64) { c.Watches[0].Trigger.Threshold = v }},
	}
	for _, f := range fields {
		nan := base()
		f.set(nan, math.NaN())
		if err := nan.Validate(); err == nil {
			t.Errorf("%s = NaN: expected error", f.name)
		}
		posInf := base()
		f.set(posInf, math.Inf(1))
		if err := posInf.Validate(); err == nil {
			t.Errorf("%s = +Inf: expected error", f.name)
		}
		negInf := base()
		f.set(negInf, math.Inf(-1))
		if err := negInf.Validate(); err == nil {
			t.Errorf("%s = -Inf: expected error", f.name)
		}
	}
}
