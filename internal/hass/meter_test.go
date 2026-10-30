package hass

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
)

func meterWatch(name, unit, class string) config.Watch {
	w := testWatch(name)
	w.Trigger = config.Trigger{Type: "value", Confirm: 3}
	w.Unit, w.DeviceClass = unit, class
	return w
}

// V1.2 golden: a value watch with device class energy is a counter, so its
// value sensor is total_increasing, which HA sums and the Energy dashboard
// takes as a grid consumption source.
func TestCounterDiscoveryGolden(t *testing.T) {
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{meterWatch("house-meter", "kWh", "energy")})
	got := fc.find(t, "homeassistant/sensor/watchglass-house-meter/value/config")
	const want = `{"availability_topic":"watchglass/status",` +
		`"device":{"identifiers":["watchglass_house-meter"],"manufacturer":"watchglass","name":"watchglass house-meter"},` +
		`"device_class":"energy","name":"Value","state_class":"total_increasing",` +
		`"state_topic":"watchglass/house-meter/value","unique_id":"watchglass_house-meter_value","unit_of_measurement":"kWh"}`
	if got.payload != want || !got.retain {
		t.Errorf("value config\n got %s\nwant %s", got.payload, want)
	}
}

// Every counter class, a counter by choice, and the measurements a value
// watch can also be.
func TestValueWatchStateClasses(t *testing.T) {
	for _, c := range []struct {
		w    config.Watch
		want string
	}{
		{meterWatch("w", "m³", "water"), "total_increasing"},
		{meterWatch("w", "m³", "gas"), "total_increasing"},
		{meterWatch("w", "L", "volume"), "total_increasing"},
		{meterWatch("w", "Wh", "energy"), "total_increasing"},
		{func() config.Watch { w := meterWatch("w", "pulses", ""); w.Counter = true; return w }(), "total_increasing"},
		{meterWatch("w", "%", "battery"), "measurement"},
		{meterWatch("w", "", "ph"), "measurement"},
		{meterWatch("w", "", ""), "measurement"},
	} {
		fc := &fakeClient{}
		p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
		p.Sync([]config.Watch{c.w})
		var m map[string]any
		if err := json.Unmarshal([]byte(fc.find(t, "homeassistant/sensor/watchglass-w/value/config").payload), &m); err != nil {
			t.Fatal(err)
		}
		if m["state_class"] != c.want {
			t.Errorf("%s %q counter=%v: state_class %v, want %s", c.w.DeviceClass, c.w.Unit, c.w.Counter, m["state_class"], c.want)
		}
		if c.w.DeviceClass != "" && m["device_class"] != c.w.DeviceClass {
			t.Errorf("device_class = %v, want %s", m["device_class"], c.w.DeviceClass)
		}
		if _, has := m["unit_of_measurement"]; has != (c.w.Unit != "") {
			t.Errorf("%s: unit_of_measurement %v for unit %q", c.w.DeviceClass, m["unit_of_measurement"], c.w.Unit)
		}
	}
}

// A value watch keeps its value entity when it turns into a numeric one
// and back: both have a number.
func TestValueEntityKeptBetweenValueAndNumeric(t *testing.T) {
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{meterWatch("m", "kWh", "energy")})
	fc.pubs = nil
	p.Sync([]config.Watch{numericWatch("m", "kWh", "energy")})
	if c := fc.find(t, "homeassistant/sensor/watchglass-m/value/config"); c.payload == "" {
		t.Error("the value entity was cleared on a switch to numeric")
	}
	for _, pb := range fc.pubs {
		if pb.topic == "watchglass/m/value" && pb.payload == "" {
			t.Error("the last value was cleared on a switch to numeric")
		}
	}
}

// A value watch never fires, so it has no Motion entity: HA would show a
// motion sensor on the meter that never changes. One left by an earlier
// type is cleared once, and a watch that goes back to firing gets it
// back.
func TestValueWatchHasNoMotion(t *testing.T) {
	const topic = "homeassistant/binary_sensor/watchglass-m/motion/config"
	fc := &fakeClient{}
	p := NewPublisher(fc, mqttCfg(), func(string, ...any) {})
	p.Sync([]config.Watch{meterWatch("m", "kWh", "energy")})
	if c := fc.find(t, topic); c.payload != "" || !c.retain {
		t.Errorf("a value watch must clear its motion config (left by an earlier run), got %+v", c)
	}
	fc.pubs = nil
	p.Sync([]config.Watch{meterWatch("m", "kWh", "energy")})
	for _, pb := range fc.pubs {
		if strings.Contains(pb.topic, "/motion") {
			t.Errorf("cleared again: %+v", pb)
		}
	}
	fc.pubs = nil
	p.Sync([]config.Watch{numericWatch("m", "kWh", "energy")})
	if c := fc.find(t, topic); c.payload == "" {
		t.Error("a numeric watch fires and must have its motion entity")
	}
	fc.pubs = nil
	p.Sync([]config.Watch{meterWatch("m", "kWh", "energy")})
	if c := fc.find(t, topic); c.payload != "" || !c.retain {
		t.Errorf("motion config must be cleared on a switch to value, got %+v", c)
	}
	for _, pb := range fc.pubs {
		if pb.topic == "watchglass/m/motion" {
			t.Errorf("motion is a pulse, not retained state, and has nothing to clear: %+v", pb)
		}
	}
}
