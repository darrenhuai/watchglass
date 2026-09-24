package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A17: unit and device_class load, survive a UI save with the file's
// comments intact, and go away (omitempty) when cleared.
func TestUnitAndDeviceClassRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	src := `watches:
  - name: boiler
    source: http://cam/snap.jpg
    region: {x: 0, y: 0, w: 1, h: 1}
    engine: sevenseg
    # flow temperature on the front panel
    unit: "°C"
    device_class: temperature
    trigger: {type: numeric, op: gt, threshold: 70}
    notify: []
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w := cfg.Watches[0]; w.Unit != "°C" || w.DeviceClass != "temperature" {
		t.Fatalf("watch = %+v", w)
	}
	cfg.Watches[0].Unit = "K"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"# flow temperature on the front panel", `unit: "K"`, "device_class: temperature"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("saved file lost %q:\n%s", want, raw)
		}
	}
	cfg.Watches[0].Unit, cfg.Watches[0].DeviceClass = "", ""
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "unit:") || strings.Contains(string(raw), "device_class:") {
		t.Errorf("cleared fields should go:\n%s", raw)
	}
}

func TestUnitAndDeviceClassAreChecked(t *testing.T) {
	base := func() Config {
		return Config{Watches: []Watch{{
			Name: "a", Source: "http://cam/snap", Region: Region{W: 1, H: 1},
			Trigger: Trigger{Type: "numeric", Op: "gt", Threshold: 5},
		}}}
	}
	for _, c := range []struct {
		name    string
		mod     func(*Watch)
		wantErr string
	}{
		{"free unit", func(w *Watch) { w.Unit = "rpm" }, ""},
		{"class and its unit", func(w *Watch) { w.Unit, w.DeviceClass = "%", "humidity" }, ""},
		{"every listed unit", func(w *Watch) { w.Unit, w.DeviceClass = "inH₂O", "pressure" }, ""},
		{"unknown class", func(w *Watch) { w.Unit, w.DeviceClass = "kWh", "energy" }, `device_class "energy" isn't one watchglass sends`},
		{"class without unit", func(w *Watch) { w.DeviceClass = "temperature" }, "device_class temperature needs a unit: °C, °F, K"},
		{"wrong unit for class", func(w *Watch) { w.Unit, w.DeviceClass = "C", "temperature" }, `unit "C" doesn't go with device_class temperature`},
		{"unit too long", func(w *Watch) { w.Unit = strings.Repeat("x", 17) }, "longer than 16"},
		{"control character", func(w *Watch) { w.Unit = "k\ng" }, "control character"},
		{"on a text watch", func(w *Watch) { w.Trigger = Trigger{Type: "ocr_changed"}; w.Unit = "°C" }, "only apply to numeric watches"},
	} {
		cfg := base()
		c.mod(&cfg.Watches[0])
		err := cfg.Validate()
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: error %v, want it to contain %q", c.name, err, c.wantErr)
		}
	}
	for class := range DeviceClassUnits {
		found := false
		for _, c := range DeviceClasses {
			found = found || c == class
		}
		if !found {
			t.Errorf("device class %q has units but isn't offered", class)
		}
	}
}

// Home Assistant spells its micro units with the Greek mu (U+03BC) and
// refuses the micro sign (U+00B5) for current ("µA" isn't in its
// AMBIGUOUS_UNITS), so watchglass offers HA's spelling, takes both, and
// keeps HA's.
func TestMicroUnitsUseHomeAssistantsMu(t *testing.T) {
	const mu, microSign = "μ", "µ"
	for class, units := range DeviceClassUnits {
		for _, u := range units {
			if strings.Contains(u, microSign) {
				t.Errorf("%s offers %q with the micro sign; HA spells it %q", class, u, strings.ReplaceAll(u, microSign, mu))
			}
		}
	}
	want := map[string]string{"voltage": mu + "V", "current": mu + "A", "weight": mu + "g", "duration": mu + "s"}
	for class, unit := range want {
		found := false
		for _, u := range DeviceClassUnits[class] {
			found = found || u == unit
		}
		if !found {
			t.Errorf("%s doesn't offer %q (U+03BC)", class, unit)
		}
		for _, typed := range []string{unit, strings.ReplaceAll(unit, mu, microSign)} {
			cfg := Config{Watches: []Watch{{
				Name: "a", Source: "http://cam/snap", Region: Region{W: 1, H: 1},
				Trigger: Trigger{Type: "numeric", Op: "gt", Threshold: 5},
				Unit:    typed, DeviceClass: class,
			}}}
			if err := cfg.Validate(); err != nil {
				t.Errorf("%s %q: %v", class, typed, err)
				continue
			}
			if got := cfg.Watches[0].Unit; got != unit {
				t.Errorf("%s %q loaded as %q, want HA's %q", class, typed, got, unit)
			}
		}
	}
	if got := NormalizeUnit(microSign + "m"); got != mu+"m" {
		t.Errorf("free unit %q = %q", microSign+"m", got)
	}
}
