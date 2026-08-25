// Package config loads and validates the watchglass YAML configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

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
	Region   Region   `yaml:"region"`
	Trigger  Trigger  `yaml:"trigger"`
	Notify   []string `yaml:"notify"`
}

type Config struct {
	Watches []Watch `yaml:"watches"`
}

var validTypes = map[string]bool{
	"pixel_change": true, "ocr_match": true, "ocr_changed": true, "numeric": true,
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
	seen := map[string]bool{}
	for i := range cfg.Watches {
		w := &cfg.Watches[i]
		if w.Name == "" {
			return nil, fmt.Errorf("watch %d: name is required", i)
		}
		if seen[w.Name] {
			return nil, fmt.Errorf("duplicate watch name %q", w.Name)
		}
		seen[w.Name] = true
		if w.Source == "" {
			return nil, fmt.Errorf("watch %q: source is required", w.Name)
		}
		if w.Interval == 0 {
			w.Interval = Duration(5 * time.Second)
		}
		if time.Duration(w.Interval) < time.Second {
			return nil, fmt.Errorf("watch %q: interval must be >= 1s", w.Name)
		}
		r := w.Region
		if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > 1 || r.Y+r.H > 1 {
			return nil, fmt.Errorf("watch %q: region must be normalized 0-1 with positive size", w.Name)
		}
		if !validTypes[w.Trigger.Type] {
			return nil, fmt.Errorf("watch %q: unknown trigger type %q", w.Name, w.Trigger.Type)
		}
		if w.Trigger.Confirm == 0 {
			w.Trigger.Confirm = 3
		}
	}
	return &cfg, nil
}
