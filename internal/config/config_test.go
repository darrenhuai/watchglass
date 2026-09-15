package config

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
		Name: "a", Source: "http://x", Interval: Duration(7 * time.Second), MaxInterval: Duration(90 * time.Second),
		Region: Region{X: 0, Y: 0, W: 1, H: 1}, Trigger: Trigger{Type: "pixel_change", Threshold: 10, Cooldown: Duration(time.Hour)},
	}}}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	// time.Duration's own "1h0m0s" is not how anyone writes an hour.
	for _, want := range []string{"interval: 7s", "max_interval: 1m30s", "cooldown: 1h"} {
		if !strings.Contains(string(raw), want+"\n") {
			t.Errorf("expected %q, got:\n%s", want, raw)
		}
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

// --- Save: merge into the existing file ---------------------------------
//
// The tests below start from testdata/annotated.yaml, a frozen copy of the
// example config (the most heavily commented file we have), copied into a
// temp dir so Save can write to it. It is frozen so that rewording the
// example never breaks a merge test; TestExampleConfigSavesUntouched is
// the one that runs against the live example.

func copyFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/annotated.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return writeTemp(t, string(raw))
}

// roundTrip writes yml, Loads it, applies mut, Validates, Saves, and demands
// that Load then reads back exactly the config that was saved. It returns
// the file after the save for further assertions.
func roundTrip(t *testing.T, yml string, mut func(*Config)) string {
	t.Helper()
	p := writeTemp(t, yml)
	cfg := loadOrFatal(t, p)
	if mut != nil {
		mut(cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("mutation produced an invalid config: %v", err)
	}
	saveOrFatal(t, p, cfg)
	after := readFile(t, p)
	got := loadOrFatal(t, p)
	if !reflect.DeepEqual(canon(got), canon(cfg)) {
		t.Errorf("round trip differs\n got: %+v\nwant: %+v\nfile:\n%s", canon(got), canon(cfg), after)
	}
	return after
}

func TestExampleConfigSavesUntouched(t *testing.T) {
	raw, err := os.ReadFile("../../examples/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p := writeTemp(t, string(raw))
	saveOrFatal(t, p, loadOrFatal(t, p))
	if after := readFile(t, p); after != string(raw) {
		t.Errorf("no-op save changed examples/config.yaml:\n%s", after)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func loadOrFatal(t *testing.T, p string) *Config {
	t.Helper()
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, readFile(t, p))
	}
	return cfg
}

func saveOrFatal(t *testing.T, p string, cfg *Config) {
	t.Helper()
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

var commentLine = regexp.MustCompile(`(?m)^\s*#`)

// squash is the layout we promise to keep: every non-blank line, in order,
// with runs of spaces collapsed. Blank lines and comment alignment are the
// two things yaml.v3 cannot round-trip, so they are the two things this
// normalizes away.
func squash(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		out = append(out, indent+strings.Join(strings.Fields(line), " "))
	}
	return out
}

func TestSaveUnchangedLeavesFileAlone(t *testing.T) {
	p := copyFixture(t)
	before := readFile(t, p)
	stat, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Load applies defaults (history_days 30, health_after 3, confirm 3)
	// the fixture never spells out; a no-change save must not spell them
	// out either.
	saveOrFatal(t, p, loadOrFatal(t, p))
	if after := readFile(t, p); after != before {
		t.Errorf("no-op save changed the file:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if again, _ := os.Stat(p); !again.ModTime().Equal(stat.ModTime()) {
		t.Error("no-op save rewrote the file (mtime changed)")
	}
	if _, err := os.Stat(p + ".tmp"); err == nil {
		t.Error("no-op save left a .tmp file behind")
	}
}

func TestSaveKeepsCommentsAndLayoutOnEdit(t *testing.T) {
	p := copyFixture(t)
	before := readFile(t, p)
	cfg := loadOrFatal(t, p)
	cfg.Watches[0].Region = Region{X: 0.3, Y: 0.5, W: 0.4, H: 0.2}
	saveOrFatal(t, p, cfg)
	after := readFile(t, p)

	nb, na := len(commentLine.FindAllString(before, -1)), len(commentLine.FindAllString(after, -1))
	if nb < 30 {
		t.Fatalf("fixture has only %d comment lines; expected the annotated example", nb)
	}
	if na != nb {
		t.Errorf("comment lines: %d before, %d after\n%s", nb, na, after)
	}
	// Every line other than the edited region must come back verbatim and in
	// the same order: that covers key order, the flow-style region, the
	// double-quoted pattern and instrument-readout's "80.0" in one sweep.
	sb, sa := squash(before), squash(after)
	if len(sb) != len(sa) {
		t.Fatalf("line count changed: %d -> %d\n%s", len(sb), len(sa), after)
	}
	var diffs []int
	for i := range sb {
		if sb[i] != sa[i] {
			diffs = append(diffs, i)
		}
	}
	if len(diffs) != 1 {
		t.Fatalf("expected exactly one changed line, got %d: %v\n%s", len(diffs), diffs, after)
	}
	if got, want := sa[diffs[0]], "    region: {x: 0.3, y: 0.5, w: 0.4, h: 0.2}"; got != want {
		t.Errorf("changed line = %q, want %q", got, want)
	}
	if got, want := sb[diffs[0]], "    region: {x: 0.25, y: 0.45, w: 0.5, h: 0.1}"; got != want {
		t.Errorf("changed line was %q, want printer-lcd's region", got)
	}
	for _, want := range []string{`pattern: "(?i)print complete"`, "threshold: 80.0", `pattern: "([0-9]+\\.[0-9])"`} {
		if !strings.Contains(after, want) {
			t.Errorf("lost %q:\n%s", want, after)
		}
	}
	got := loadOrFatal(t, p)
	if got.Watches[0].Region != (Region{X: 0.3, Y: 0.5, W: 0.4, H: 0.2}) {
		t.Errorf("region did not persist: %+v", got.Watches[0].Region)
	}
}

func TestSaveDeletesWatchKeepingNeighbours(t *testing.T) {
	p := copyFixture(t)
	cfg := loadOrFatal(t, p)
	kept := cfg.Watches[:0:0]
	for _, w := range cfg.Watches {
		if w.Name != "printer-motion" {
			kept = append(kept, w)
		}
	}
	cfg.Watches = kept
	saveOrFatal(t, p, cfg)
	after := readFile(t, p)
	for _, gone := range []string{"printer-motion", "# fire when >25% of pixels change"} {
		if strings.Contains(after, gone) {
			t.Errorf("deleted watch left %q behind:\n%s", gone, after)
		}
	}
	for _, want := range []string{
		"# An RTSP camera pointed at a lab instrument. Needs ffmpeg on PATH.",
		"# persistent decoder, so idle cost stays near zero.",
		"# history_days: 30",
		"# auth:",
		"#   password: change-me",
		"# mqtt:",
		"#   broker: tcp://homeassistant.local:1883",
		"#   password: secret",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("lost %q:\n%s", want, after)
		}
	}
	got := loadOrFatal(t, p)
	if len(got.Watches) != 2 || got.Watches[0].Name != "printer-lcd" || got.Watches[1].Name != "instrument-readout" {
		t.Errorf("watches after delete: %+v", got.Watches)
	}
}

func TestSaveAppendsNewWatchWithoutEmptyFields(t *testing.T) {
	p := copyFixture(t)
	before := readFile(t, p)
	cfg := loadOrFatal(t, p)
	cfg.Watches = append(cfg.Watches, Watch{
		Name: "door-motion", Source: "http://192.168.1.70/snap.jpg",
		Region:  Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger: Trigger{Type: "pixel_change", Threshold: 20},
	})
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	saveOrFatal(t, p, cfg)
	after := readFile(t, p)

	// Layout: everything up to the last existing watch is untouched, the
	// new watch follows, the trailing commented-out blocks close the file.
	sb, sa := squash(before), squash(after)
	cut := 0
	for i, line := range sb {
		if strings.Contains(line, "ntfy://ntfy.sh/my-lab-topic") {
			cut = i + 1
		}
	}
	head, tail := sb[:cut], sb[cut:]
	if len(sa) < len(sb) || !reflect.DeepEqual(sa[:cut], head) {
		t.Fatalf("existing watches changed:\n%s", after)
	}
	if !reflect.DeepEqual(sa[len(sa)-len(tail):], tail) {
		t.Errorf("trailing comment blocks changed:\n%s", after)
	}
	block := strings.Join(sa[cut:len(sa)-len(tail)], "\n")
	if !strings.HasPrefix(block, "  - name: door-motion") {
		t.Errorf("new watch not appended last:\n%s", block)
	}
	for _, bad := range []string{"pattern:", "op:", "cooldown:", "preprocess:", "max_interval:"} {
		if strings.Contains(block, bad) {
			t.Errorf("new watch carries an empty %s line:\n%s", bad, block)
		}
	}
	// Written the way the hand-written watches are: flow-style region with
	// a plain y key, not yaml.Marshal's block mapping with a quoted "y".
	for _, want := range []string{"type: pixel_change", "threshold: 20", "region: {x: 0, y: 0, w: 1, h: 1}"} {
		if !strings.Contains(block, want) {
			t.Errorf("new watch lacks %q:\n%s", want, block)
		}
	}
	got := loadOrFatal(t, p)
	if len(got.Watches) != 4 || got.Watches[3].Name != "door-motion" || got.Watches[3].Trigger.Threshold != 20 {
		t.Errorf("new watch did not persist: %+v", got.Watches)
	}
}

func TestSaveKeepsUnknownKeys(t *testing.T) {
	p := writeTemp(t, `custom_top: 1
watches:
  - name: a
    source: http://x/s.jpg
    colour: blue
    region: {x: 0, y: 0, w: 1, h: 1}
    trigger: {type: pixel_change, threshold: 10}
`)
	cfg := loadOrFatal(t, p)
	cfg.Watches[0].Interval = Duration(10 * time.Second)
	saveOrFatal(t, p, cfg)
	after := readFile(t, p)
	for _, want := range []string{"custom_top: 1", "colour: blue", "interval: 10s"} {
		if !strings.Contains(after, want) {
			t.Errorf("lost %q:\n%s", want, after)
		}
	}
	if got := loadOrFatal(t, p); time.Duration(got.Watches[0].Interval) != 10*time.Second {
		t.Errorf("interval = %v", time.Duration(got.Watches[0].Interval))
	}
}

func TestSaveDropsClearedOptionalKeyAndItsComment(t *testing.T) {
	p := copyFixture(t)
	cfg := loadOrFatal(t, p)
	cfg.Watches[2].MaxInterval = 0
	saveOrFatal(t, p, cfg)
	after := readFile(t, p)
	for _, gone := range []string{"max_interval", "# Adaptive polling"} {
		if strings.Contains(after, gone) {
			t.Errorf("cleared max_interval left %q behind:\n%s", gone, after)
		}
	}
	if !strings.Contains(after, "# Notify after this many consecutive failed grabs") {
		t.Errorf("neighbouring comment lost:\n%s", after)
	}
	if got := loadOrFatal(t, p); got.Watches[2].MaxInterval != 0 {
		t.Errorf("max_interval = %v, want 0", time.Duration(got.Watches[2].MaxInterval))
	}
}

// TestSaveDropsScalarsClearedToZero: the UI blanks the fields a trigger
// type doesn't read (parseWatchForm), and a pattern: "" or a stale
// "threshold: 0 # fire when >25% ..." line is worse than no line. Region is
// the exception: a 0 there is a coordinate.
func TestSaveDropsScalarsClearedToZero(t *testing.T) {
	p := copyFixture(t)
	cfg := loadOrFatal(t, p)
	cfg.Watches[0].Trigger = Trigger{Type: "pixel_change", Threshold: 15, Confirm: 3, Cooldown: cfg.Watches[0].Trigger.Cooldown}
	cfg.Watches[0].Region.X = 0
	cfg.Watches[1].Trigger = Trigger{Type: "ocr_changed", Confirm: 3, Cooldown: cfg.Watches[1].Trigger.Cooldown}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	saveOrFatal(t, p, cfg)
	after := readFile(t, p)
	for _, gone := range []string{`pattern: ""`, "threshold: 0", "# fire when >25% of pixels change"} {
		if strings.Contains(after, gone) {
			t.Errorf("cleared scalar left %q behind:\n%s", gone, after)
		}
	}
	for _, want := range []string{"threshold: 15", "region: {x: 0, y: 0.45, w: 0.5, h: 0.1}", `pattern: "([0-9]+\\.[0-9])"`, "cooldown: 30m"} {
		if !strings.Contains(after, want) {
			t.Errorf("lost %q:\n%s", want, after)
		}
	}
	got := loadOrFatal(t, p)
	if got.Watches[0].Trigger.Pattern != "" || got.Watches[0].Region.X != 0 || got.Watches[1].Trigger.Threshold != 0 {
		t.Errorf("cleared values did not persist:\n%+v\n%+v", got.Watches[0], got.Watches[1])
	}
}

// TestSaveWritesMissingFileAndRefusesBrokenOne: a file that isn't there is
// written from scratch, in the same pruned layout a merge appends; a file
// Load can't read (someone's hand edit, half done) is left exactly as it
// is and the save fails, rather than being replaced by a comment-free
// marshal of the in-memory config.
func TestSaveWritesMissingFileAndRefusesBrokenOne(t *testing.T) {
	cfg := &Config{
		HistoryDays: 7,
		Auth:        &Auth{Username: "u", Password: "p"},
		Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg",
			Region:  Region{X: 0, Y: 0, W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
			Notify:  []string{"ntfy://ntfy.sh/t"},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "config.yaml")
	saveOrFatal(t, missing, cfg)
	got := loadOrFatal(t, missing)
	if !reflect.DeepEqual(canon(got), canon(cfg)) {
		t.Errorf("round trip differs:\n got %+v\nwant %+v", got, cfg)
	}
	fresh := readFile(t, missing)
	for _, bad := range []string{"pattern:", "op:", "cooldown:", "confirm:", "history_days: 30", `"y"`} {
		if strings.Contains(fresh, bad) {
			t.Errorf("fresh file carries %q:\n%s", bad, fresh)
		}
	}
	if !strings.Contains(fresh, "region: {x: 0, y: 0, w: 1, h: 1}") || !strings.HasPrefix(fresh, "history_days: 7\n") {
		t.Errorf("fresh file not in the merged layout:\n%s", fresh)
	}
	// An empty config on a missing file still leaves a file with a
	// watches key, like the quick start's "watches: []".
	empty := filepath.Join(t.TempDir(), "config.yaml")
	saveOrFatal(t, empty, &Config{})
	if got := readFile(t, empty); got != "watches: []\n" {
		t.Errorf("empty config on a missing file = %q", got)
	}

	for label, broken := range map[string]string{
		"unparseable":   "just: [a sequence\n",
		"not a mapping": "- name: a\n- name: b\n",
		"typo":          strings.Replace(readFile(t, copyFixture(t)), "    trigger:\n      type: numeric", "    trigger\n      type: numeric", 1),
	} {
		p := writeTemp(t, broken)
		if _, err := Load(p); err == nil {
			t.Fatalf("%s: fixture unexpectedly loads", label)
		}
		err := Save(p, cfg)
		if err == nil || !strings.Contains(err.Error(), "parse existing config") {
			t.Errorf("%s: Save error = %v, want a parse error", label, err)
		}
		if after := readFile(t, p); after != broken {
			t.Errorf("%s: Save touched a file it could not read:\n%s", label, after)
		}
		if _, err := os.Stat(p + ".tmp"); err == nil {
			t.Errorf("%s: refused save left a .tmp file behind", label)
		}
	}
}

func TestSaveKeepsFileIndent(t *testing.T) {
	two := `watches:
  - name: a
    source: http://x/s.jpg
    region: {x: 0, y: 0, w: 1, h: 1}
    trigger:
      type: pixel_change
      threshold: 10
`
	// yaml.v3's own 4-space layout: sequence items at 4, keys inside them
	// at 6, and a nested block realigned to the next multiple of 4.
	four := `watches:
    - name: a
      source: http://x/s.jpg
      region: {x: 0, y: 0, w: 1, h: 1}
      trigger:
        type: pixel_change
        threshold: 10
`
	for label, yml := range map[string]string{"two": two, "four": four} {
		p := writeTemp(t, yml)
		cfg := loadOrFatal(t, p)
		cfg.Watches[0].Trigger.Threshold = 20
		saveOrFatal(t, p, cfg)
		after := readFile(t, p)
		want := strings.Replace(yml, "threshold: 10", "threshold: 20", 1)
		if after != want {
			t.Errorf("%s-space file:\n--- want\n%s\n--- got\n%s", label, want, after)
		}
	}
}

// --- Save: anchors, merge keys and the layouts yaml.v3 can't keep ----------

// A watch anchored and inherited by the next one is the documented YAML way
// to say "b is like a, except...".
const templatedYAML = `watches:
  - &lcd
    name: printer-a
    source: http://a/snap.jpg
    interval: 10s
    region: {x: 0.25, y: 0.45, w: 0.5, h: 0.1}
    trigger: {type: ocr_match, pattern: done}
    notify: [ntfy://ntfy.sh/lab]
  - <<: *lcd
    name: printer-b
    source: http://b/snap.jpg
`

// (notify is a block list here: yaml.v3 re-emits a URL inside a flow list
// quoted, which would muddy the string checks below.)
const anchoredYAML = `watches:
  - name: a
    source: http://x/a.jpg
    region: &full {x: 0, y: 0, w: 1, h: 1}
    interval: &iv 10s
    trigger: {type: pixel_change, threshold: 10}
    notify: &n
      - ntfy://ntfy.sh/a
  - name: b
    source: http://x/b.jpg
    region: *full
    interval: *iv
    trigger: {type: pixel_change, threshold: 10}
    notify: *n
`

// TestSaveKeepsAnchorsResolvable: whatever a save does to the watch an
// anchor sits on — delete it, rename it, move it below the watch that
// aliases it — the file it writes must still resolve every alias, and
// Load must read exactly what was saved.
func TestSaveKeepsAnchorsResolvable(t *testing.T) {
	noDangling := func(t *testing.T, after string) {
		t.Helper()
		for _, bad := range []string{"*lcd", "<<", "*full", "*iv", "*n\n"} {
			if strings.Contains(after, bad) {
				t.Errorf("reference %q left behind:\n%s", bad, after)
			}
		}
	}
	t.Run("no-op keeps the template", func(t *testing.T) {
		if after := roundTrip(t, templatedYAML, nil); after != templatedYAML {
			t.Errorf("no-op save rewrote a templated file:\n%s", after)
		}
	})
	t.Run("no-op keeps plain aliases", func(t *testing.T) {
		if after := roundTrip(t, anchoredYAML, nil); after != anchoredYAML {
			t.Errorf("no-op save rewrote an aliased file:\n%s", after)
		}
	})
	t.Run("delete the anchored watch", func(t *testing.T) {
		after := roundTrip(t, templatedYAML, func(c *Config) { c.Watches = c.Watches[1:] })
		noDangling(t, after)
		for _, want := range []string{"name: printer-b", "interval: 10s", "pattern: done", "ntfy://ntfy.sh/lab"} {
			if !strings.Contains(after, want) {
				t.Errorf("inherited %q not written out:\n%s", want, after)
			}
		}
	})
	t.Run("rename the anchored watch", func(t *testing.T) {
		noDangling(t, roundTrip(t, templatedYAML, func(c *Config) { c.Watches[0].Name = "printer-a-renamed" }))
	})
	t.Run("move the anchored watch below its inheritor", func(t *testing.T) {
		after := roundTrip(t, templatedYAML, func(c *Config) { c.Watches[0], c.Watches[1] = c.Watches[1], c.Watches[0] })
		noDangling(t, after)
		if strings.Index(after, "printer-b") > strings.Index(after, "printer-a") {
			t.Errorf("order not swapped:\n%s", after)
		}
	})
	t.Run("alias under an unknown key", func(t *testing.T) {
		yml := `watches:
  - name: a
    source: http://x/a.jpg
    region: &full {x: 0, y: 0, w: 1, h: 1}
    trigger: {type: pixel_change, threshold: 10}
  - name: b
    source: http://x/b.jpg
    x-template-region: *full
    region: {x: 0.1, y: 0.1, w: 0.5, h: 0.5}
    trigger: {type: pixel_change, threshold: 10}
`
		after := roundTrip(t, yml, func(c *Config) { c.Watches = c.Watches[1:] })
		if !strings.Contains(after, "x-template-region: {x: 0, y: 0, w: 1, h: 1}") {
			t.Errorf("dangling alias under an unknown key not expanded:\n%s", after)
		}
	})
	t.Run("editing the anchor pins the alias to the old value", func(t *testing.T) {
		after := roundTrip(t, anchoredYAML, func(c *Config) {
			c.Watches[0].Region = Region{X: 0.5, Y: 0.5, W: 0.5, H: 0.5}
			c.Watches[0].Interval = Duration(30 * time.Second)
			c.Watches[0].Notify = []string{"ntfy://ntfy.sh/other"}
		})
		noDangling(t, after)
		for _, want := range []string{"region: &full {x: 0.5, y: 0.5, w: 0.5, h: 0.5}", "region: {x: 0, y: 0, w: 1, h: 1}", "interval: 10s", "interval: &iv 30s"} {
			if !strings.Contains(after, want) {
				t.Errorf("lost %q:\n%s", want, after)
			}
		}
	})
	t.Run("editing the alias side leaves the anchor alone", func(t *testing.T) {
		after := roundTrip(t, anchoredYAML, func(c *Config) {
			c.Watches[1].Region = Region{X: 0.5, Y: 0.5, W: 0.5, H: 0.5}
			c.Watches[1].Notify = nil
		})
		for _, want := range []string{"region: &full {x: 0, y: 0, w: 1, h: 1}", "interval: *iv", "notify: &n\n      - ntfy://ntfy.sh/a\n", "region: {x: 0.5, y: 0.5, w: 0.5, h: 0.5}", "notify: []"} {
			if !strings.Contains(after, want) {
				t.Errorf("lost %q:\n%s", want, after)
			}
		}
	})
}

// TestSaveWritesThroughMergeKeys: a key a watch inherits through
// "<<: *defaults" reads as the inherited value when left out, not as the
// default or zero, so setting it back to the default or clearing it has
// to be spelled out — while a no-op save keeps the template as written.
func TestSaveWritesThroughMergeKeys(t *testing.T) {
	const yml = `x-defaults: &defaults
  interval: 10s
  max_interval: 1m
  health_after: 5
  preprocess: {grayscale: true}
  notify: [ntfy://ntfy.sh/lab]
watches:
  - <<: *defaults
    name: a
    source: http://x/a.jpg
    region: {x: 0, y: 0, w: 1, h: 1}
    trigger: {type: pixel_change, threshold: 10}
  - <<: *defaults
    name: b
    source: http://x/b.jpg
    interval: 20s
    region: {x: 0, y: 0, w: 1, h: 1}
    trigger: {type: pixel_change, threshold: 10}
`
	if after := roundTrip(t, yml, nil); after != yml {
		t.Errorf("no-op save rewrote a merge-keyed file:\n%s", after)
	}
	for label, c := range map[string]struct {
		mut  func(*Config)
		want string
	}{
		"interval back to default": {func(c *Config) { c.Watches[0].Interval = Duration(5 * time.Second) }, "interval: 5s"},
		"max_interval cleared":     {func(c *Config) { c.Watches[0].MaxInterval = 0 }, "max_interval: 0s"},
		"health_after to default":  {func(c *Config) { c.Watches[0].HealthAfter = 3 }, "health_after: 3"},
		"notify cleared":           {func(c *Config) { c.Watches[0].Notify = nil }, "notify: []"},
		"preprocess cleared":       {func(c *Config) { c.Watches[0].Preprocess = Preprocess{} }, "preprocess: {}"},
	} {
		t.Run(label, func(t *testing.T) {
			after := roundTrip(t, yml, c.mut)
			if !strings.Contains(after, "    "+c.want+"\n") {
				t.Errorf("explicit %q not written:\n%s", c.want, after)
			}
			if strings.Count(after, "<<: *defaults") != 2 {
				t.Errorf("template not kept:\n%s", after)
			}
		})
	}
	t.Run("top level", func(t *testing.T) {
		top := "x-common: &common\n  history_days: 7\n<<: *common\nwatches:\n  - name: a\n    source: http://x/a.jpg\n    region: {x: 0, y: 0, w: 1, h: 1}\n    trigger: {type: pixel_change, threshold: 10}\n"
		if after := roundTrip(t, top, func(c *Config) { c.HistoryDays = 30 }); !strings.Contains(after, "\nhistory_days: 30\n") {
			t.Errorf("history_days not written out:\n%s", after)
		}
	})
	t.Run("edit the inheritor of a watch template", func(t *testing.T) {
		after := roundTrip(t, templatedYAML, func(c *Config) { c.Watches[1].Interval = Duration(5 * time.Second); c.Watches[1].Notify = nil })
		for _, want := range []string{"<<: *lcd", "    interval: 5s\n", "    notify: []\n"} {
			if !strings.Contains(after, want) {
				t.Errorf("lost %q:\n%s", want, after)
			}
		}
	})
	t.Run("edit the template the other watch inherits from", func(t *testing.T) {
		after := roundTrip(t, templatedYAML, func(c *Config) { c.Watches[0].Interval = Duration(30 * time.Second) })
		if strings.Count(after, "interval: 30s") != 1 || strings.Count(after, "interval: 10s") != 1 {
			t.Errorf("the inheritor must keep 10s spelled out:\n%s", after)
		}
	})
}

func TestSaveKeepsNotifyItemComments(t *testing.T) {
	const yml = `watches:
  - name: a
    source: http://x/s.jpg
    region: {x: 0, y: 0, w: 1, h: 1}
    trigger: {type: pixel_change, threshold: 10}
    notify:
      # my phone
      - ntfy://ntfy.sh/phone   # push to phone
      - 'discord://tok@id'   # lab channel
      # foot comment under the list
`
	add := func(c *Config) { c.Watches[0].Notify = append(c.Watches[0].Notify, "ntfy://ntfy.sh/extra") }
	after := roundTrip(t, yml, add)
	want := []string{"      # my phone\n", "      - ntfy://ntfy.sh/phone # push to phone\n", "      - 'discord://tok@id' # lab channel\n", "      - ntfy://ntfy.sh/extra\n      # foot comment under the list\n"}
	for _, w := range want {
		if !strings.Contains(after, w) {
			t.Errorf("adding a URL lost %q:\n%s", w, after)
		}
	}
	after = roundTrip(t, yml, func(c *Config) { c.Watches[0].Notify = c.Watches[0].Notify[1:] })
	for _, w := range []string{"# my phone\n      - 'discord://tok@id' # lab channel\n      # foot comment under the list\n"} {
		if !strings.Contains(after, w) {
			t.Errorf("removing the first URL lost %q:\n%s", w, after)
		}
	}
	if strings.Contains(after, "push to phone") {
		t.Errorf("removed item's own comment survived:\n%s", after)
	}
	after = roundTrip(t, yml, func(c *Config) { c.Watches[0].Notify = c.Watches[0].Notify[:1] })
	if !strings.Contains(after, "# push to phone\n      # foot comment under the list\n") {
		t.Errorf("removing the last URL lost the list's foot comment:\n%s", after)
	}
}

// TestSaveKeepsLineEndingsAndMarkers: a CRLF file (git's autocrlf checkout
// on Windows, PowerShell's redirects) stays CRLF, a byte order mark stays,
// and an explicit "---" stays where it was.
func TestSaveKeepsLineEndingsAndMarkers(t *testing.T) {
	lf := "# header\n# line two\n---\nwatches:\n  - name: a\n    source: http://x/s.jpg   # cam\n    region: {x: 0, y: 0, w: 1, h: 1}\n    trigger:\n      type: pixel_change\n      threshold: 10\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	for label, yml := range map[string]string{"crlf": crlf, "bom+crlf": "\xef\xbb\xbf" + crlf, "bom": "\xef\xbb\xbf" + lf, "marker first": "---\n# c\nwatches: []\n"} {
		t.Run(label, func(t *testing.T) {
			if after := roundTrip(t, yml, nil); after != yml {
				t.Errorf("no-op save rewrote the file:\n%q", after)
			}
			after := roundTrip(t, yml, func(c *Config) {
				c.Watches = append(c.Watches, Watch{Name: "b", Source: "http://x/b.jpg", Region: Region{W: 1, H: 1}, Trigger: Trigger{Type: "ocr_changed"}})
			})
			if strings.HasPrefix(yml, "\xef\xbb\xbf") != strings.HasPrefix(after, "\xef\xbb\xbf") {
				t.Errorf("byte order mark changed:\n%q", after)
			}
			if strings.Contains(yml, "\r\n") && strings.Count(after, "\n") != strings.Count(after, "\r\n") {
				t.Errorf("line endings changed:\n%q", after)
			}
			if !strings.Contains(yml, "\r\n") && strings.Contains(after, "\r") {
				t.Errorf("line endings changed:\n%q", after)
			}
			plain := strings.TrimPrefix(strings.ReplaceAll(after, "\r\n", "\n"), "\xef\xbb\xbf")
			if strings.HasPrefix(yml, "---") {
				if !strings.HasPrefix(plain, "---\n# c\nwatches:\n") {
					t.Errorf("document start marker or comment moved:\n%s", plain)
				}
			} else if !strings.HasPrefix(plain, "# header\n# line two\n---\nwatches:\n") || !strings.Contains(plain, "# cam") {
				t.Errorf("header, marker or line comment lost:\n%s", plain)
			}
			if !strings.Contains(plain, "name: b") {
				t.Errorf("edit lost:\n%s", plain)
			}
		})
	}
}

// TestSaveKeepsCompactSequences: yq, kubectl and ansible write list items
// at their key's column; yaml.v3 can only indent them, so the merge
// dedents its output back to the file's layout.
func TestSaveKeepsCompactSequences(t *testing.T) {
	const yml = `# compact sequence style
watches:
- name: a
  source: http://x/s.jpg   # cam
  region: {x: 0, y: 0, w: 1, h: 1}
  trigger:
    type: pixel_change
    threshold: 10   # ten percent
  notify:
  - ntfy://ntfy.sh/t   # phone
- name: b
  source: http://x/b.jpg
  region: {x: 0.1, y: 0.1, w: 0.5, h: 0.5}
  trigger:
    type: ocr_changed
`
	after := roundTrip(t, yml, func(c *Config) {
		c.Watches[0].Trigger.Threshold = 42
		c.Watches[0].Notify = append(c.Watches[0].Notify, "ntfy://ntfy.sh/u")
		c.Watches = append(c.Watches, Watch{Name: "c", Source: "http://x/c.jpg", Region: Region{W: 1, H: 1}, Trigger: Trigger{Type: "ocr_changed"}})
	})
	want := strings.Replace(yml, "threshold: 10", "threshold: 42", 1)
	want = strings.Replace(want, "  - ntfy://ntfy.sh/t   # phone\n", "  - ntfy://ntfy.sh/t   # phone\n  - ntfy://ntfy.sh/u\n", 1)
	want += "- name: c\n  source: http://x/c.jpg\n  region: {x: 0, y: 0, w: 1, h: 1}\n  trigger:\n    type: ocr_changed\n"
	if got, want := squash(after), squash(want); !reflect.DeepEqual(got, want) {
		t.Errorf("compact layout not kept:\n--- want\n%s\n--- got\n%s", strings.Join(want, "\n"), after)
	}
}

// TestSaveDeletingWatchKeepsParkedComments: yaml.v3 hands a deleted watch
// the comment block above it, which is usually a commented-out watch the
// user parked there; that block stays. A comment written under a watch at
// the dash column, which yaml.v3 hangs on the NEXT watch, stays with the
// watch it followed.
func TestSaveDeletingWatchKeepsParkedComments(t *testing.T) {
	const parked = "  # - name: disabled\n  #   source: http://old/snap.jpg\n  #   trigger: {type: pixel_change, threshold: 5}\n"
	yml := "watches:\n  - name: a\n    source: http://x/s.jpg\n    region: {x: 0, y: 0, w: 1, h: 1}\n    trigger: {type: pixel_change, threshold: 10}\n" +
		parked + "  - name: b\n    source: http://x/b.jpg\n    region: {x: 0, y: 0, w: 1, h: 1}\n    trigger: {type: ocr_changed}\n"
	drop := func(name string) func(*Config) {
		return func(c *Config) {
			kept := c.Watches[:0:0]
			for _, w := range c.Watches {
				if w.Name != name {
					kept = append(kept, w)
				}
			}
			c.Watches = kept
		}
	}
	// The block keeps its lines and order; its indentation follows where it
	// lands (above the next watch, or at the end of the list).
	trimmed := func(s string) []string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	for _, name := range []string{"a", "b"} {
		after := roundTrip(t, yml, drop(name))
		if got := strings.Join(trimmed(after), "\n"); !strings.Contains(got, strings.Join(trimmed(parked), "\n")) {
			t.Errorf("deleting %s lost the parked block:\n%s", name, after)
		}
		if strings.Contains(after, "name: "+name) {
			t.Errorf("deleting %s left it behind:\n%s", name, after)
		}
	}

	const trailing = "watches:\n  - name: motion\n    source: http://x/s.jpg\n    region: {x: 0, y: 0, w: 1, h: 1}\n    trigger: {type: pixel_change, threshold: 25}\n  # comment written under motion, at the dash column\n\n  - name: readout\n    source: http://x/r.jpg\n    region: {x: 0, y: 0, w: 1, h: 1}\n    trigger: {type: ocr_changed}\n"
	after := roundTrip(t, trailing, func(c *Config) { c.Watches[0].Interval = Duration(7 * time.Second) })
	if i, j, k := strings.Index(after, "threshold: 25"), strings.Index(after, "# comment written under motion"), strings.Index(after, "name: readout"); !(i < j && j < k) {
		t.Errorf("comment moved away from the watch it followed:\n%s", after)
	}
	if after := roundTrip(t, trailing, drop("readout")); !strings.Contains(after, "# comment written under motion") {
		t.Errorf("deleting readout took motion's comment:\n%s", after)
	}
	if after := roundTrip(t, trailing, drop("motion")); strings.Contains(after, "# comment written under motion") {
		t.Errorf("deleting motion left its comment behind:\n%s", after)
	}
}

// TestSaveKeepsCommentsOnlyFile: Load reads a file of nothing but comments
// (or nothing at all, or a bare "---") as an empty config, so a save into
// one keeps the comments and writes the same pruned layout a merge appends.
func TestSaveKeepsCommentsOnlyFile(t *testing.T) {
	add := func(c *Config) {
		c.Watches = []Watch{{Name: "new", Source: "http://x/a.jpg", Region: Region{W: 1, H: 1}, Trigger: Trigger{Type: "pixel_change", Threshold: 1}}}
	}
	for label, yml := range map[string]string{"comments": "# my precious template\n\n# watches:\n#   - name: x\n", "empty": "", "marker": "---\n", "null": "~\n", "empty map": "{}\n"} {
		t.Run(label, func(t *testing.T) {
			if after := roundTrip(t, yml, nil); after != yml {
				t.Errorf("no-op save rewrote %q as %q", yml, after)
			}
			after := roundTrip(t, yml, add)
			if strings.HasPrefix(yml, "#") && !strings.HasPrefix(after, "# my precious template\n\n# watches:\n#   - name: x\nwatches:\n") {
				t.Errorf("comments lost or moved:\n%s", after)
			}
			for _, bad := range []string{`pattern: ""`, "op:", "confirm:", `"y"`, "history_days"} {
				if strings.Contains(after, bad) {
					t.Errorf("not the merged layout (%q):\n%s", bad, after)
				}
			}
			if !strings.Contains(after, "\n  - name: new\n    source: http://x/a.jpg\n    region: {x: 0, y: 0, w: 1, h: 1}\n") {
				t.Errorf("not the merged layout:\n%s", after)
			}
		})
	}
}

func TestSaveKeepsHeaderWhenFirstKeyGoes(t *testing.T) {
	body := "watches:\n  - name: a\n    source: http://x/s.jpg\n    region: {x: 0, y: 0, w: 1, h: 1}\n    trigger: {type: pixel_change, threshold: 10}\n"
	for label, c := range map[string]struct {
		block string
		mut   func(*Config)
	}{
		"auth": {"auth:\n  username: u\n  password: p\n", func(c *Config) { c.Auth = nil }},
		"mqtt": {"mqtt:\n  broker: tcp://ha.local:1883\n", func(c *Config) { c.MQTT = nil }},
	} {
		t.Run(label, func(t *testing.T) {
			after := roundTrip(t, "# My watchglass config. Edit with care.\n# Second header line.\n"+c.block+body, c.mut)
			if want := "# My watchglass config. Edit with care.\n# Second header line.\n" + body; after != want {
				t.Errorf("header lost with the first key:\n--- want\n%s\n--- got\n%s", want, after)
			}
		})
	}
}

// TestSaveHoistsCommentOffFilledList: yaml.v3 can't print an end-of-line
// comment on a block list, and would hang it on the next watch's dash.
func TestSaveHoistsCommentOffFilledList(t *testing.T) {
	for _, empty := range []string{"notify: []   # none yet", "notify: ~   # none yet", "notify:   # none yet"} {
		yml := "watches:\n  - name: a\n    source: http://x/s.jpg\n    region: {x: 0, y: 0, w: 1, h: 1}\n    trigger: {type: pixel_change, threshold: 10}\n    " + empty + "\n  - name: b\n    source: http://x/b.jpg\n    region: {x: 0, y: 0, w: 1, h: 1}\n    trigger: {type: ocr_changed}\n"
		after := roundTrip(t, yml, func(c *Config) { c.Watches[0].Notify = []string{"ntfy://ntfy.sh/z"} })
		if !strings.Contains(after, "    notify: # none yet\n      - ntfy://ntfy.sh/z\n  - name: b\n") {
			t.Errorf("%s: comment not hoisted onto the key:\n%s", empty, after)
		}
	}
}

// TestSchemaRefusesUnmergeableFields: a Config field the merge could not
// keep correct fails at init, with the field named, not at the first save.
func TestSchemaRefusesUnmergeableFields(t *testing.T) {
	for label, typ := range map[string]reflect.Type{
		"map":       reflect.TypeOf(struct{ Labels map[string]string }{}),
		"interface": reflect.TypeOf(struct{ Extra any }{}),
		"inline": reflect.TypeOf(struct {
			Region Region `yaml:",inline"`
		}{}),
	} {
		t.Run(label, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("walkType accepted the field")
				}
				if msg := r.(string); !strings.Contains(msg, "Labels") && !strings.Contains(msg, "Extra") && !strings.Contains(msg, "Region") {
					t.Errorf("panic does not name the field: %s", msg)
				}
			}()
			walkType(typ, "", map[string]reflect.Type{})
		})
	}
	// And the real Config passes: schema is built at init, so reaching
	// here is the proof; the paths it found are the ones Load reads.
	for _, p := range []string{"history_days", "auth.password", "mqtt.broker", "watches.name", "watches.region.x", "watches.trigger.cooldown", "watches.preprocess.upscale", "watches.notify"} {
		if schema.types[p] == nil {
			t.Errorf("schema lacks %s", p)
		}
	}
}

// TestLoadsBackGuard: whatever the merge produces is read back through
// Load's own path before it is written; bytes that would not read back as
// the saved config are an error, never a file.
func TestLoadsBackGuard(t *testing.T) {
	cfg := &Config{Watches: []Watch{{Name: "a", Source: "http://x/s.jpg", Region: Region{W: 1, H: 1}, Trigger: Trigger{Type: "pixel_change", Threshold: 10}}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	good, _ := yaml.Marshal(cfg)
	if err := loadsBack(good, cfg); err != nil {
		t.Errorf("faithful bytes rejected: %v", err)
	}
	for label, bad := range map[string]string{"unparseable": "watches: [\n", "different": "watches: []\n", "invalid": "watches:\n  - name: a\n"} {
		if err := loadsBack([]byte(bad), cfg); err == nil {
			t.Errorf("%s: accepted", label)
		}
	}
	if got := cfg.Watches[0].Trigger.Confirm; got != 3 {
		t.Errorf("guard mutated the caller's config: confirm = %d", got)
	}
}

// canon is Config equality for round-trip tests: nil and empty slices mean
// the same thing on disk, so they must compare equal.
func canon(c *Config) Config {
	return c.normalized()
}

// mutate applies one random, Validate-clean edit of the kind the web UI (or
// a hand edit) makes.
func mutate(r *rand.Rand, c *Config, step int) {
	pick := func(s ...string) string { return s[r.Intn(len(s))] }
	dur := func(s string) Duration {
		d, err := time.ParseDuration(s)
		if err != nil {
			panic(err)
		}
		return Duration(d)
	}
	newWatch := func() Watch {
		return Watch{
			Name:    "w" + strconv.Itoa(step) + pick("", "-2", " two"),
			Source:  pick("http://192.168.1.9/snap.jpg", "rtsp://cam:554/s1", "ffmpeg:-f lavfi -i testsrc"),
			Region:  Region{X: 0.1, Y: 0.1, W: 0.5, H: 0.5},
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}
	}
	if len(c.Watches) == 0 || r.Intn(12) == 0 {
		c.Watches = append(c.Watches, newWatch())
		return
	}
	w := &c.Watches[r.Intn(len(c.Watches))]
	switch r.Intn(16) {
	case 0:
		w.Interval = dur(pick("1s", "2s", "5s", "10s", "30s", "1m", "90s"))
		if w.MaxInterval != 0 && w.MaxInterval < w.Interval {
			w.MaxInterval = w.Interval * 2
		}
	case 1:
		w.MaxInterval = w.Interval * Duration(r.Intn(6))
	case 2:
		w.HealthAfter = r.Intn(7)
	case 3:
		if r.Intn(4) == 0 {
			w.Region = Region{X: 0, Y: 0, W: 1, H: 1}
		} else {
			rnd := func() float64 { return float64(r.Intn(50)) / 100 }
			w.Region = Region{X: rnd(), Y: rnd(), W: 0.05 + rnd(), H: 0.05 + rnd()}
		}
	case 4:
		w.Preprocess = Preprocess{
			Grayscale: r.Intn(2) == 0, Invert: r.Intn(2) == 0,
			Threshold: r.Intn(256) * r.Intn(2), Upscale: r.Intn(5),
		}
	case 5:
		switch pick("pixel_change", "ocr_match", "ocr_changed", "numeric") {
		case "pixel_change":
			w.Trigger = Trigger{Type: "pixel_change", Threshold: []float64{5, 25, 33.3, 80.0}[r.Intn(4)]}
		case "ocr_match":
			w.Trigger = Trigger{Type: "ocr_match", Pattern: pick(`(?i)done`, `\d+`, `"quoted"`, `a: b`, `# hash`, `yes`, `80`, `it's`)}
		case "ocr_changed":
			w.Trigger = Trigger{Type: "ocr_changed"}
		case "numeric":
			w.Trigger = Trigger{Type: "numeric", Op: pick("gt", "lt"), Threshold: []float64{80.0, 12.5, -3, 0, 100}[r.Intn(5)],
				Pattern: pick("", `([0-9]+\.[0-9])`, `Temp: (\d+)C`)}
		}
		w.Trigger.Confirm = r.Intn(6)
		w.Trigger.Cooldown = dur(pick("0s", "30s", "5m", "1h", "90m"))
	case 6:
		w.Trigger.Confirm = r.Intn(6)
	case 7:
		w.Trigger.Cooldown = dur(pick("0s", "30s", "5m", "1h", "90m"))
	case 8:
		urls := []string{"ntfy://ntfy.sh/a", "ntfy://ntfy.sh/b", "discord://token@id", "generic+https://host/path?x=1"}
		switch r.Intn(3) {
		case 0:
			w.Notify = append(append([]string(nil), w.Notify...), urls[r.Intn(len(urls))])
		case 1:
			if len(w.Notify) > 0 {
				i := r.Intn(len(w.Notify))
				w.Notify = append(append([]string(nil), w.Notify[:i]...), w.Notify[i+1:]...)
			}
		case 2:
			w.Notify = nil
		}
	case 9:
		i := r.Intn(len(c.Watches))
		c.Watches = append(c.Watches[:i:i], c.Watches[i+1:]...)
	case 10:
		if len(c.Watches) > 1 {
			i, j := r.Intn(len(c.Watches)), r.Intn(len(c.Watches))
			c.Watches[i], c.Watches[j] = c.Watches[j], c.Watches[i]
		}
	case 11:
		w.Name = "renamed-" + strconv.Itoa(step)
	case 12:
		c.HistoryDays = []int{0, 7, 30, 365, -1}[r.Intn(5)]
	case 13:
		if r.Intn(2) == 0 {
			c.Auth = nil
		} else {
			c.Auth = &Auth{Username: pick("admin", "u"), Password: pick("p", "change-me", "s3cret:with#chars")}
		}
	case 14:
		if r.Intn(2) == 0 {
			c.MQTT = nil
		} else {
			c.MQTT = &MQTT{
				Broker:   pick("tcp://ha.local:1883", "ssl://ha.local:8883", "ws://ha.local/mqtt"),
				Username: pick("", "watchglass"), Password: pick("", "secret"),
				ClientID: pick("", "wg"), BaseTopic: pick("", "cams"), DiscoveryPrefix: pick("", "ha"),
			}
		}
	case 15:
		w.Source = pick("http://192.168.1.50/snapshot.jpg", "https://cam/snap.png", "rtsp://cam:554/s2", "dshow:video=Integrated Cam")
	}
}

// propertyTemplatedYAML leans on everything yaml.v3 lets a hand-written
// file do that a marshal never would: a defaults block inherited through a
// merge key, a watch used as the template for the next one, and anchors on
// a region and a notify list that other watches alias.
const propertyTemplatedYAML = `# templated
x-defaults: &defaults
  interval: 10s
  max_interval: 1m
  health_after: 5
  preprocess: {grayscale: true}
  notify: [ntfy://ntfy.sh/lab]
watches:
  - &lcd
    name: printer-a
    source: http://a/snap.jpg
    interval: 10s
    region: &full {x: 0, y: 0, w: 1, h: 1}
    trigger: {type: ocr_match, pattern: done}
    notify: &n [ntfy://ntfy.sh/lab]
  - <<: *lcd
    name: printer-b
    source: http://b/snap.jpg
  - <<: *defaults
    name: motion
    source: http://c/snap.jpg
    region: *full
    trigger: {type: pixel_change, threshold: 10}
    notify: *n
`

// TestSaveRoundTripsUnderRandomMutation is the property the merge has to
// hold whatever the UI does: after any sequence of edits, Load reads back
// exactly the Config that was saved, and the annotated file stays a file.
func TestSaveRoundTripsUnderRandomMutation(t *testing.T) {
	for label, start := range map[string]string{"annotated": readFile(t, copyFixture(t)), "templated": propertyTemplatedYAML} {
		t.Run(label, func(t *testing.T) {
			p := writeTemp(t, start)
			cfg := loadOrFatal(t, p)
			r := rand.New(rand.NewSource(20260914))
			for step := 0; step < 200; step++ {
				mutate(r, cfg, step)
				if err := cfg.Validate(); err != nil {
					t.Fatalf("step %d: mutation produced an invalid config: %v", step, err)
				}
				if err := Save(p, cfg); err != nil {
					t.Fatalf("step %d: Save: %v", step, err)
				}
				raw := readFile(t, p)
				got, err := Load(p)
				if err != nil {
					t.Fatalf("step %d: Load after Save: %v\n%s", step, err, raw)
				}
				if !reflect.DeepEqual(canon(got), canon(cfg)) {
					t.Fatalf("step %d: round trip differs\n got: %+v\nwant: %+v\nfile:\n%s", step, canon(got), canon(cfg), raw)
				}
				if !strings.HasPrefix(raw, strings.SplitN(start, "\n", 2)[0]) {
					t.Fatalf("step %d: header comment lost:\n%s", step, raw)
				}
			}
		})
	}
}
