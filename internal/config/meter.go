package config

import (
	"fmt"
	"slices"
)

// HasValue reports whether a trigger type reads a number out of the
// region and publishes it as the watch's value (Home Assistant's Value
// sensor, the history's value column): numeric, which also fires on a
// threshold, and value, which only records the number and never fires.
func HasValue(triggerType string) bool {
	return triggerType == "numeric" || triggerType == "value"
}

// CounterClasses are the device classes Home Assistant only keeps as a
// running total (state_class total or total_increasing, never
// measurement): a watch with one of them is always a counter.
var CounterClasses = []string{"energy", "water", "gas", "volume"}

// counterOK are the device classes that may also be a counter by choice:
// Home Assistant takes total_increasing for them as well as measurement.
// A watch with no device class may be a counter too.
var counterOK = []string{"weight", "duration"}

// IsCounter reports whether the watch's number is a counter: a total that
// only goes up, like the kWh on an electricity meter. It is one when it
// says counter: true, and always when its device class is one Home
// Assistant keeps only as a total (CounterClasses).
func (w Watch) IsCounter() bool {
	return HasValue(w.Trigger.Type) && (w.Counter || slices.Contains(CounterClasses, w.DeviceClass))
}

// StateClass is the Home Assistant state_class of the watch's Value
// sensor: total_increasing for a counter, so HA keeps a running sum and
// the Energy dashboard can use it, and measurement for everything else.
func (w Watch) StateClass() string {
	if w.IsCounter() {
		return "total_increasing"
	}
	return "measurement"
}

// RiseConfirm is how many readings in a row must agree before a rise
// above MaxRise is believed: every reading the Value's median is taken
// from (2*confirm-1, where an ordinary change needs only most of them),
// and at least 2, so a rise limit is never a no-op on a watch that
// confirms on one reading. 5 with the default confirm of 3.
func (w Watch) RiseConfirm() int {
	c := w.Trigger.Confirm
	if c <= 0 {
		c = 3 // Validate's default
	}
	if n := 2*c - 1; n > 2 {
		return n
	}
	return 2
}

// checkCounter validates counter and max_rise against the watch's type
// and device class. Validate calls it after the unit check.
func checkCounter(w Watch) error {
	if (w.Counter || w.MaxRise != 0) && !HasValue(w.Trigger.Type) {
		return fmt.Errorf("counter and max_rise only apply to numeric and value watches")
	}
	if w.Counter && w.DeviceClass != "" && !slices.Contains(CounterClasses, w.DeviceClass) && !slices.Contains(counterOK, w.DeviceClass) {
		return fmt.Errorf("device_class %s can't be a counter: Home Assistant keeps it as a measurement, not a running total", w.DeviceClass)
	}
	if nonFinite(w.MaxRise) || w.MaxRise < 0 {
		return fmt.Errorf("max_rise must be a number above 0, or left out for no limit")
	}
	if w.MaxRise > 0 && !w.IsCounter() {
		return fmt.Errorf("max_rise only applies to a counter: add counter: true, or leave max_rise out")
	}
	return nil
}
