package web

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/darrenhuai/watchglass/internal/config"
)

// freeUnits are the Unit suggestions while no device class is chosen:
// units people read off panels that no HA device class covers, plus the
// commonest classed ones.
var freeUnits = []string{"°C", "%", "W", "kWh", "V", "A", "kg", "min", "rpm", "L", "m³", "bar"}

// unitSuggestions is the Unit field's datalist for a device class.
func unitSuggestions(class string) []string {
	if units, ok := config.DeviceClassUnits[class]; ok {
		return units
	}
	return freeUnits
}

// unitsJSON is every device class's units for app.js (data-units on the
// Device class select), so the suggestions and the check follow the
// select without a round trip. "" holds the free suggestions.
func unitsJSON() string {
	m := map[string][]string{"": freeUnits}
	for k, v := range config.DeviceClassUnits {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// unitList spells a list the way the copy reads it: "°C, °F or K".
func unitList(units []string) string {
	if len(units) < 2 {
		return strings.Join(units, "")
	}
	return strings.Join(units[:len(units)-1], ", ") + " or " + units[len(units)-1]
}

// unitMessage is the field error for a unit/device class pair that
// config.CheckUnit refused, in the form's words. A unit that is one of
// the class's own in another spelling ("C", "degC", "us") gets it offered.
func unitMessage(unit, class string, err error) string {
	units, known := config.DeviceClassUnits[class]
	switch {
	case class != "" && !known:
		return "Pick a device class from the list, or none."
	case class != "" && unit == "":
		return upperFirst(class) + " needs a unit: " + unitList(units) + "."
	case class != "":
		// The Device class help under the field already lists the units,
		// so a near miss just gets the fix.
		msg := strconv.Quote(unit) + " isn't a unit Home Assistant takes for " + class
		if near := nearUnit(unit, units); near != "" {
			return msg + ". Did you mean " + near + "?"
		}
		return msg + " (" + unitList(units) + ")."
	}
	return upperFirst(err.Error()) + "."
}

// nearUnit finds the unit in units that unit is another spelling of.
func nearUnit(unit string, units []string) string {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.NewReplacer("°", "", "deg", "", "º", "", "μ", "u", "µ", "u", "₂", "2", " ", "").Replace(s)
		return s
	}
	for _, u := range units {
		if norm(u) == norm(unit) {
			return u
		}
	}
	return ""
}
