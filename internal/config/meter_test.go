package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// V1.1: a value watch reads and publishes a number with no op or
// threshold. Before value existed, Load refused the type outright.
func TestValueWatchLoadsWithoutOpOrThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	src := `watches:
  - name: meter
    source: http://cam/snap.jpg
    region: {x: 0.1, y: 0.1, w: 0.5, h: 0.2}
    engine: sevenseg
    trigger:
      type: value
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	w := cfg.Watches[0]
	if w.Trigger.Type != "value" || w.Trigger.Confirm != 3 {
		t.Errorf("trigger = %+v", w.Trigger)
	}
	if w.IsCounter() || w.StateClass() != "measurement" {
		t.Errorf("a plain value watch is a measurement, got counter=%v %s", w.IsCounter(), w.StateClass())
	}
	// A bad pattern is still caught.
	cfg.Watches[0].Trigger.Pattern = "("
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "pattern") {
		t.Errorf("bad pattern on a value watch: %v", err)
	}
}

// V1.2: energy, water, gas and volume are always counters; counter: true
// makes any other value one; everything else stays a measurement, as
// every v0.1.12 numeric watch was.
func TestCounterStateClass(t *testing.T) {
	w := func(typ, class, unit string, counter bool) Watch {
		return Watch{Name: "m", Source: "http://cam/snap", Region: Region{W: 1, H: 1},
			Trigger: Trigger{Type: typ, Op: "gt", Threshold: 1}, DeviceClass: class, Unit: unit, Counter: counter}
	}
	for _, c := range []struct {
		w    Watch
		want string
	}{
		{w("value", "energy", "kWh", false), "total_increasing"},
		{w("value", "water", "m³", false), "total_increasing"},
		{w("value", "gas", "m³", false), "total_increasing"},
		{w("value", "volume", "L", false), "total_increasing"},
		{w("numeric", "energy", "Wh", false), "total_increasing"},
		{w("value", "", "pulses", true), "total_increasing"},
		{w("value", "weight", "kg", true), "total_increasing"},
		{w("value", "", "", false), "measurement"},
		{w("numeric", "temperature", "°C", false), "measurement"},
		{w("value", "power", "W", false), "measurement"},
		{w("value", "battery", "%", false), "measurement"},
		{w("value", "ph", "", false), "measurement"},
	} {
		cfg := Config{Watches: []Watch{c.w}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s %s: %v", c.w.Trigger.Type, c.w.DeviceClass, err)
			continue
		}
		if got := cfg.Watches[0].StateClass(); got != c.want {
			t.Errorf("%s %s counter=%v: state class %s, want %s", c.w.Trigger.Type, c.w.DeviceClass, c.w.Counter, got, c.want)
		}
	}
}

func TestCounterAndMaxRiseAreChecked(t *testing.T) {
	base := func() Config {
		return Config{Watches: []Watch{{
			Name: "a", Source: "http://cam/snap", Region: Region{W: 1, H: 1},
			Trigger: Trigger{Type: "value"},
		}}}
	}
	for _, c := range []struct {
		name    string
		mod     func(*Watch)
		wantErr string
	}{
		{"counter with rise limit", func(w *Watch) { w.Counter, w.MaxRise = true, 5 }, ""},
		{"energy rise limit", func(w *Watch) { w.Unit, w.DeviceClass, w.MaxRise = "kWh", "energy", 2.5 }, ""},
		{"rise limit without counter", func(w *Watch) { w.MaxRise = 5 }, "max_rise only applies to a counter"},
		{"negative rise limit", func(w *Watch) { w.Counter, w.MaxRise = true, -1 }, "max_rise must be a number above 0"},
		{"temperature counter", func(w *Watch) { w.Unit, w.DeviceClass, w.Counter = "°C", "temperature", true }, "device_class temperature can't be a counter"},
		{"counter on a text watch", func(w *Watch) { w.Trigger = Trigger{Type: "ocr_changed"}; w.Counter = true }, "only apply to numeric and value watches"},
		{"ph without a unit", func(w *Watch) { w.DeviceClass = "ph" }, ""},
		{"ph with a unit", func(w *Watch) { w.DeviceClass, w.Unit = "ph", "pH" }, "device_class ph has no unit"},
		{"energy in a non-energy unit", func(w *Watch) { w.DeviceClass, w.Unit = "energy", "m³" }, `unit "m³" doesn't go with device_class energy`},
		{"unit on a value watch", func(w *Watch) { w.Unit = "rpm" }, ""},
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
}

// counter and max_rise survive a UI save with the file's comments, and go
// when cleared.
func TestCounterRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	src := `watches:
  - name: meter
    source: http://cam/snap.jpg
    region: {x: 0, y: 0, w: 1, h: 1}
    engine: sevenseg
    # the house meter, total kWh
    unit: kWh
    device_class: energy
    max_rise: 5 # never more than 5 kWh between two readings
    trigger: {type: value}
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w := cfg.Watches[0]; w.MaxRise != 5 || !w.IsCounter() || w.Counter {
		t.Fatalf("watch = %+v", w)
	}
	cfg.Watches[0].MaxRise = 2
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"# the house meter, total kWh", "max_rise: 2", "# never more than 5 kWh"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("saved file lost %q:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), "counter:") {
		t.Errorf("energy is a counter by its class; the save shouldn't spell counter out:\n%s", raw)
	}
	cfg.Watches[0].MaxRise = 0
	cfg.Watches[0].Unit, cfg.Watches[0].DeviceClass, cfg.Watches[0].Counter = "pulses", "", true
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "max_rise") || !strings.Contains(string(raw), "counter: true") {
		t.Errorf("cleared max_rise should go and counter: true appear:\n%s", raw)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load back: %v", err)
	}
	if w := back.Watches[0]; !w.Counter || w.MaxRise != 0 || w.Unit != "pulses" {
		t.Errorf("loaded back %+v", w)
	}
}

// A v0.1.12 numeric watch is unchanged: same validation, a measurement.
func TestNumericWatchStaysAMeasurement(t *testing.T) {
	cfg := Config{Watches: []Watch{{
		Name: "boiler", Source: "http://cam/snap", Region: Region{W: 1, H: 1},
		Trigger: Trigger{Type: "numeric", Op: "gt", Threshold: 70}, Unit: "°C", DeviceClass: "temperature",
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if w := cfg.Watches[0]; w.IsCounter() || w.StateClass() != "measurement" {
		t.Errorf("numeric temperature: counter=%v state_class=%s", w.IsCounter(), w.StateClass())
	}
	cfg.Watches[0].Trigger.Op = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "use type value") {
		t.Errorf("numeric without op: %v", err)
	}
}
