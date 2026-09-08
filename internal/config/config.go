// Package config loads and validates the watchglass YAML configuration.
package config

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Preprocess holds per-watch image adjustments applied before OCR so text on
// LCDs/consoles becomes legible to the engine. The zero value is a no-op.
// Pixel-diff triggers always compare raw crops; preprocessing is OCR-only.
type Preprocess struct {
	Grayscale bool `yaml:"grayscale,omitempty"`
	Invert    bool `yaml:"invert,omitempty"`
	Threshold int  `yaml:"threshold,omitempty"` // 0 = off; 1-255 binarize at this gray level
	Upscale   int  `yaml:"upscale,omitempty"`   // 0/1 = off; 2-4 integer nearest-neighbor
}

// MQTT configures the optional Home Assistant integration: one broker
// connection, retained auto-discovery configs, and per-watch state topics.
// Omit the block entirely to disable MQTT.
type MQTT struct {
	// Broker is the connection URL: tcp://host:1883, ssl://host:8883,
	// mqtt://, mqtts://, or ws://.
	Broker   string `yaml:"broker"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	// ClientID defaults to "watchglass".
	ClientID string `yaml:"client_id,omitempty"`
	// BaseTopic prefixes every state topic; defaults to "watchglass".
	BaseTopic string `yaml:"base_topic,omitempty"`
	// DiscoveryPrefix is Home Assistant's discovery prefix; defaults to
	// "homeassistant".
	DiscoveryPrefix string `yaml:"discovery_prefix,omitempty"`
}

// Auth enables HTTP Basic authentication on the web UI. Credentials live in
// this file in plaintext (same posture as the MQTT password — the file is
// written 0o600); omit the block for no auth (fine on localhost).
type Auth struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Duration is a time.Duration that unmarshals from YAML strings like "5s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(dur)
	return nil
}

// MarshalYAML writes durations in human-readable form ("5s", "10m") so the
// config file the web UI saves stays hand-editable.
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// Region is a normalized rectangle; all fields are 0.0–1.0.
type Region struct {
	X float64 `yaml:"x"`
	Y float64 `yaml:"y"`
	W float64 `yaml:"w"`
	H float64 `yaml:"h"`
}

type Trigger struct {
	Type      string   `yaml:"type"`      // pixel_change | ocr_match | ocr_changed | numeric
	Pattern   string   `yaml:"pattern"`   // regex for ocr_match / numeric extraction
	Op        string   `yaml:"op"`        // numeric: gt | lt
	Threshold float64  `yaml:"threshold"` // pixel_change: percent 0-100; numeric: compare value
	Confirm   int      `yaml:"confirm"`   // N consecutive identical readings before a state is believed
	Cooldown  Duration `yaml:"cooldown"`  // suppress re-fires within this window
}

type Watch struct {
	Name     string   `yaml:"name"`
	Source   string   `yaml:"source"`
	Interval Duration `yaml:"interval"`
	// MaxInterval enables adaptive polling: when set above Interval, the poll
	// gap doubles (capped here) while nothing changes and snaps back to
	// Interval on any change. Zero or == Interval disables it.
	MaxInterval Duration `yaml:"max_interval,omitempty"`
	// HealthAfter is how many consecutive grab failures mark a stream down
	// (and send one notification). Defaults to 3.
	HealthAfter int        `yaml:"health_after,omitempty"`
	Region      Region     `yaml:"region"`
	Preprocess  Preprocess `yaml:"preprocess,omitempty"`
	Trigger     Trigger    `yaml:"trigger"`
	Notify      []string   `yaml:"notify"`
}

type Config struct {
	// HistoryDays is how many days of readings to keep in the history
	// database. Default 30; -1 keeps everything forever and is preserved
	// verbatim through save/load cycles (doesn't omitempty away). Consumers
	// gate pruning on HistoryDays > 0.
	HistoryDays int     `yaml:"history_days,omitempty"`
	Auth        *Auth   `yaml:"auth,omitempty"`
	MQTT        *MQTT   `yaml:"mqtt,omitempty"`
	Watches     []Watch `yaml:"watches"`
}

var validTypes = map[string]bool{
	"pixel_change": true, "ocr_match": true, "ocr_changed": true, "numeric": true,
}

// validWatchName rejects names that would make a watch unaddressable
// through the web UI's own routes (/watch/{name}, /watch/{name}/save, ...):
// '/' breaks path segmentation, '?' and '#' truncate the path at the query
// string / fragment, and control characters (including bare newlines) are
// never legitimate in a display name. Leading/trailing whitespace is
// rejected outright rather than silently trimmed, since the create handler
// already trims before this runs — any survives-to-here whitespace means a
// caller (e.g. a saved config.yaml hand-edited or written by another tool)
// bypassed that.
func validWatchName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("name %q must not have leading or trailing whitespace", name)
	}
	for _, r := range name {
		if r == '/' || r == '?' || r == '#' || unicode.IsControl(r) {
			return fmt.Errorf("name %q must not contain '/', '?', '#', or control characters", name)
		}
	}
	return nil
}

// nonFinite reports whether v is NaN or +/-Inf. Plain comparisons (<, <=,
// >, >=) are all false against NaN, so bounds checks like "x < 0" silently
// let a NaN through; this must be checked explicitly wherever a float
// crosses from user input (web form or hand-edited YAML) into Validate.
func nonFinite(v float64) bool {
	return math.IsNaN(v) || math.IsInf(v, 0)
}

// SourceKind classifies a watch's source URL into the source tier that can
// read it: "http" for plain snapshot URLs (pure Go, no dependencies) or
// "ffmpeg" for anything needing a decoder subprocess.
func SourceKind(source string) (string, error) {
	switch {
	case strings.HasPrefix(source, "rtsp://"), strings.HasPrefix(source, "rtsps://"):
		return "ffmpeg", nil
	case strings.HasPrefix(source, "http://"), strings.HasPrefix(source, "https://"):
		return "http", nil
	case strings.HasPrefix(source, "ffmpeg:"):
		// Duplicated in source.ffmpegInputArgs (config cannot import source
		// without an import cycle) — keep both checks in sync.
		if strings.TrimSpace(strings.TrimPrefix(source, "ffmpeg:")) == "" {
			return "", fmt.Errorf("ffmpeg: source has no arguments")
		}
		return "ffmpeg", nil
	case strings.HasPrefix(source, "v4l2:"),
		strings.HasPrefix(source, "dshow:"):
		return "ffmpeg", nil
	}
	return "", fmt.Errorf("unsupported source %q: expected one of "+
		"http:// https:// rtsp:// rtsps:// v4l2: dshow: ffmpeg:", source)
}

// Validate applies defaults (interval 5s, confirm 3) and validates every
// watch. Load calls it after parsing; the web UI calls it before Save.
func (c *Config) Validate() error {
	if c.MQTT != nil {
		m := c.MQTT
		if m.Broker == "" {
			return fmt.Errorf("mqtt: broker is required")
		}
		validBroker := false
		for _, p := range []string{"tcp://", "ssl://", "mqtt://", "mqtts://", "ws://"} {
			if strings.HasPrefix(m.Broker, p) {
				validBroker = true
			}
		}
		if !validBroker {
			return fmt.Errorf("mqtt: broker %q must start with tcp:// ssl:// mqtt:// mqtts:// or ws://", m.Broker)
		}
		if m.ClientID == "" {
			m.ClientID = "watchglass"
		}
		if m.BaseTopic == "" {
			m.BaseTopic = "watchglass"
		}
		if m.DiscoveryPrefix == "" {
			m.DiscoveryPrefix = "homeassistant"
		}
	}
	if c.Auth != nil && (c.Auth.Username == "" || c.Auth.Password == "") {
		return fmt.Errorf("auth: username and password are both required")
	}
	switch {
	case c.HistoryDays == 0:
		c.HistoryDays = 30
	case c.HistoryDays < -1:
		return fmt.Errorf("history_days must be a positive day count, 0 (default 30), or -1 (forever)")
	}
	seen := map[string]bool{}
	for i := range c.Watches {
		w := &c.Watches[i]
		if err := validWatchName(w.Name); err != nil {
			return fmt.Errorf("watch %d: %w", i, err)
		}
		if seen[w.Name] {
			return fmt.Errorf("duplicate watch name %q", w.Name)
		}
		seen[w.Name] = true
		if w.Source == "" {
			return fmt.Errorf("watch %q: source is required", w.Name)
		}
		if _, err := SourceKind(w.Source); err != nil {
			return fmt.Errorf("watch %q: %w", w.Name, err)
		}
		if w.Interval == 0 {
			w.Interval = Duration(5 * time.Second)
		}
		if time.Duration(w.Interval) < time.Second {
			return fmt.Errorf("watch %q: interval must be >= 1s", w.Name)
		}
		if w.MaxInterval != 0 && time.Duration(w.MaxInterval) < time.Duration(w.Interval) {
			return fmt.Errorf("watch %q: max_interval must be >= interval", w.Name)
		}
		if w.HealthAfter < 0 {
			return fmt.Errorf("watch %q: health_after must be >= 0", w.Name)
		}
		if w.HealthAfter == 0 {
			w.HealthAfter = 3
		}
		r := w.Region
		if nonFinite(r.X) || nonFinite(r.Y) || nonFinite(r.W) || nonFinite(r.H) {
			return fmt.Errorf("watch %q: region must be finite", w.Name)
		}
		if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > 1 || r.Y+r.H > 1 {
			return fmt.Errorf("watch %q: region must be normalized 0-1 with positive size", w.Name)
		}
		if !validTypes[w.Trigger.Type] {
			return fmt.Errorf("watch %q: unknown trigger type %q", w.Name, w.Trigger.Type)
		}
		if nonFinite(w.Trigger.Threshold) {
			return fmt.Errorf("watch %q: trigger threshold must be finite", w.Name)
		}
		// Mirror trigger.New's exact constraints so a config that passes
		// Validate can never fail supervisor.Start: a config that's wrong in
		// a way only Start catches gets persisted anyway (the web UI has
		// already shown success), and the NEXT daemon boot crash-loops on
		// the very same watch it just failed to (re)start.
		switch w.Trigger.Type {
		case "ocr_match":
			if w.Trigger.Pattern == "" {
				return fmt.Errorf("watch %q: trigger: ocr_match requires a pattern", w.Name)
			}
			if _, err := regexp.Compile(w.Trigger.Pattern); err != nil {
				return fmt.Errorf("watch %q: trigger: pattern: %w", w.Name, err)
			}
		case "numeric":
			if w.Trigger.Op != "gt" && w.Trigger.Op != "lt" {
				return fmt.Errorf("watch %q: trigger: numeric op must be gt or lt, got %q", w.Name, w.Trigger.Op)
			}
			// Pattern is optional for numeric (trigger.New falls back to a
			// generic number pattern); only validate it when set.
			if w.Trigger.Pattern != "" {
				if _, err := regexp.Compile(w.Trigger.Pattern); err != nil {
					return fmt.Errorf("watch %q: trigger: pattern: %w", w.Name, err)
				}
			}
		case "pixel_change":
			if w.Trigger.Threshold <= 0 {
				return fmt.Errorf("watch %q: trigger: pixel_change threshold must be > 0", w.Name)
			}
		}
		if w.Trigger.Confirm == 0 {
			w.Trigger.Confirm = 3
		}
		if w.Preprocess.Threshold < 0 || w.Preprocess.Threshold > 255 {
			return fmt.Errorf("watch %q: preprocess threshold must be 0-255", w.Name)
		}
		if w.Preprocess.Upscale < 0 || w.Preprocess.Upscale > 4 {
			return fmt.Errorf("watch %q: preprocess upscale must be 0-4", w.Name)
		}
		// Notify URLs: cheap sanity via url.Parse + a non-empty scheme.
		// Deliberately does NOT call shoutrrr.CreateSender here (that's a
		// per-service network/credential-shaped validation this layer isn't
		// meant to duplicate) — just enough to reject the empty/garbage
		// strings that would otherwise only surface as a Start failure.
		for _, n := range w.Notify {
			if strings.TrimSpace(n) == "" {
				return fmt.Errorf("watch %q: notify: empty URL", w.Name)
			}
			u, err := url.Parse(n)
			if err != nil || u.Scheme == "" {
				return fmt.Errorf("watch %q: notify: invalid URL %q (must include a scheme, e.g. ntfy://...)", w.Name, n)
			}
		}
	}
	return nil
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save writes cfg to path atomically (tmp file + rename) so a crash mid-write
// never truncates the user's config.
func Save(path string, cfg *Config) error {
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := path + ".tmp"
	// 0o600: the file can hold a plaintext MQTT password (config.MQTT.Password).
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
