package web

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
)

// connErr stands in for hass.ConnError: the text is the reason, Detail
// the raw error.
type connErr struct{ reason, raw string }

func (e connErr) Error() string  { return e.reason }
func (e connErr) Detail() string { return e.raw }

// A18: the topbar says whether Home Assistant (the MQTT broker) is
// connected, and why not, on every page; with no mqtt: block it says
// nothing and loads no script.
func TestTopbarShowsHomeAssistantStatus(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	for _, path := range []string{"/", "/watch/printer"} {
		_, body := get(t, h, path)
		if strings.Contains(body, "ha-status") || strings.Contains(body, "topbar.js") {
			t.Errorf("%s: no mqtt: block, but the topbar has an HA line", path)
		}
	}
	if resp, _ := get(t, h, "/ha-status"); resp.StatusCode != 200 {
		t.Errorf("/ha-status without mqtt = %d", resp.StatusCode)
	}

	s.cfg.MQTT = &config.MQTT{Broker: "tcp://user:hunter2@127.0.0.1:18884"}
	state, err := "not connected", error(connErr{"connection refused", "dial tcp 127.0.0.1:18884: connect: connection refused"})
	s.MQTTStatus = func() (string, error) { return state, err }
	for _, path := range []string{"/", "/watch/printer", "/ha-status"} {
		_, body := get(t, h, path)
		for _, want := range []string{
			`<p id="ha-status" class="ha-status is-down" role="status" data-src="/ha-status"`,
			`<span class="led led-error" aria-hidden="true"></span><span class="ha-status-text"><span class="ha-status-name">Home Assistant:</span> not connected: connection refused</span>`,
			`title="MQTT broker tcp://user:xxxxx@127.0.0.1:18884: dial tcp 127.0.0.1:18884: connect: connection refused. Retrying every few seconds; changes to the mqtt: block in config.yaml need a restart."`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %s; body:\n%s", path, want, body)
			}
		}
		if strings.Contains(body, "hunter2") {
			t.Errorf("%s: the broker password is on the page", path)
		}
	}
	if _, body := get(t, h, "/"); !strings.Contains(body, `<script src="/static/topbar.js" defer></script>`) {
		t.Error("the page must load topbar.js to keep the line current")
	}

	state, err = "connected", nil
	_, body := get(t, h, "/ha-status")
	if !strings.Contains(body, `class="ha-status is-connected"`) || !strings.Contains(body, `led-green`) ||
		!strings.Contains(body, `Home Assistant:</span> connected</span>`) {
		t.Errorf("connected; body:\n%s", body)
	}
	state, err = "connecting", nil
	_, body = get(t, h, "/ha-status")
	if !strings.Contains(body, `is-connecting`) || !strings.Contains(body, `connecting…`) {
		t.Errorf("connecting; body:\n%s", body)
	}
}

func numericSaveForm(unit, class string) url.Values {
	return url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"numeric"}, "op": {"gt"},
		"tthreshold": {"25"}, "confirm": {"2"}, "cooldown": {"0s"}, "interval": {"5s"}, "notify": {""},
		"unit": {unit}, "device_class": {class}}
}

// A17: Unit and Device class are on the page for numeric watches only,
// save into the file next to its comments, are refused with the units HA
// takes, and go away when the type stops being numeric.
func TestUnitAndDeviceClassFields(t *testing.T) {
	s, cfgPath := newTestServer(t)
	h := s.Handler()

	_, body := get(t, h, "/watch/printer")
	if !strings.Contains(body, `<fieldset id="fs-ha" hidden>`) {
		t.Errorf("a pixel_change watch's Home Assistant fieldset must start hidden; body:\n%s", body)
	}
	if !strings.Contains(body, "used once config.yaml has an mqtt: block") {
		t.Error("without mqtt: the fieldset says when it is used")
	}
	if !strings.Contains(body, `<p id="unit-help" class="field-hint">Shown after the number, like °C or rpm. Optional.</p>`) {
		t.Error("with no device class the unit is optional")
	}

	raw, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(cfgPath, append([]byte("# my cameras\n"), raw...), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, _ := postForm(t, h, "/watch/printer/save", numericSaveForm("°C", "temperature"))
	if resp.StatusCode != 303 {
		t.Fatalf("save = %d", resp.StatusCode)
	}
	raw, _ = os.ReadFile(cfgPath)
	for _, want := range []string{"# my cameras", "unit: °C", "device_class: temperature"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("file lost or lacks %q:\n%s", want, raw)
		}
	}
	s.MQTTStatus = func() (string, error) { return "connected", nil }
	_, body = get(t, h, "/watch/printer")
	for _, want := range []string{
		`<fieldset id="fs-ha">`,
		`sent with the number over MQTT`,
		`id="f-unit" name="unit" value="°C" list="unit-list"`,
		`<datalist id="unit-list"><option value="°C"></option><option value="°F"></option><option value="K"></option></datalist>`,
		`<option value="temperature" selected>temperature</option>`,
		`Home Assistant takes °C, °F or K for temperature.`,
		`<p id="unit-help" class="field-hint">Shown after the number. Needed for temperature.</p>`,
		`data-units="{`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("numeric watch: missing %s", want)
		}
	}

	// A unit the class doesn't take is refused, naming the ones it does,
	// and nothing is written.
	before, _ := os.ReadFile(cfgPath)
	resp, body = postForm(t, h, "/watch/printer/save", numericSaveForm("C", "temperature"))
	if resp.StatusCode != 400 || !strings.Contains(body, "&#34;C&#34; isn&#39;t a unit Home Assistant takes for temperature. Did you mean °C?") ||
		!strings.Contains(body, `id="f-unit" name="unit" value="C"`) || !strings.Contains(body, `href="#f-unit"`) {
		t.Errorf("wrong unit: %d; body:\n%s", resp.StatusCode, body)
	}
	resp, body = postForm(t, h, "/watch/printer/save", numericSaveForm("", "humidity"))
	if resp.StatusCode != 400 || !strings.Contains(body, "Humidity needs a unit: %.") {
		t.Errorf("class without unit: %d; body:\n%s", resp.StatusCode, body)
	}
	resp, body = postForm(t, h, "/watch/printer/save", numericSaveForm("kWh", "energy"))
	if resp.StatusCode != 400 || !strings.Contains(body, "Pick a device class from the list, or none.") || !strings.Contains(body, `href="#f-dclass"`) {
		t.Errorf("unknown class: %d; body:\n%s", resp.StatusCode, body)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Error("a refused save must not write")
	}

	// Home Assistant's micro is the Greek mu (U+03BC). Its own spelling
	// saves as is, and the micro sign a keyboard types (U+00B5) is saved
	// as HA's, since HA refuses "µA" with device class current.
	for _, typed := range []string{"μA", "µA"} {
		if resp, body := postForm(t, h, "/watch/printer/save", numericSaveForm(typed, "current")); resp.StatusCode != 303 {
			t.Fatalf("save %q/current = %d; body:\n%s", typed, resp.StatusCode, body)
		}
		if w, _ := s.findWatch("printer"); w.Unit != "μA" {
			t.Errorf("%q saved as %q, want μA (U+03BC)", typed, w.Unit)
		}
		if raw, _ := os.ReadFile(cfgPath); !strings.Contains(string(raw), "unit: μA") {
			t.Errorf("%q: file lacks HA's spelling:\n%s", typed, raw)
		}
	}
	if resp, body := postForm(t, h, "/watch/printer/save", numericSaveForm("°C", "temperature")); resp.StatusCode != 303 {
		t.Fatalf("restore °C = %d; body:\n%s", resp.StatusCode, body)
	}

	// A client that doesn't send the fields keeps them.
	form := numericSaveForm("", "")
	form.Del("unit")
	form.Del("device_class")
	if resp, _ := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("save without the fields = %d", resp.StatusCode)
	}
	if w, _ := s.findWatch("printer"); w.Unit != "°C" || w.DeviceClass != "temperature" {
		t.Errorf("fields not sent must keep their values, got %q %q", w.Unit, w.DeviceClass)
	}

	// Another type drops them (a hidden fieldset still submits).
	form = saveForm("")
	form.Set("unit", "°C")
	form.Set("device_class", "temperature")
	if resp, body := postForm(t, h, "/watch/printer/save", form); resp.StatusCode != 303 {
		t.Fatalf("save as pixel_change = %d; body:\n%s", resp.StatusCode, body)
	}
	raw, _ = os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "unit:") || strings.Contains(string(raw), "device_class:") {
		t.Errorf("a pixel_change watch keeps no unit:\n%s", raw)
	}
}

func TestUnitMessage(t *testing.T) {
	for _, c := range []struct{ unit, class, want string }{
		{"degC", "temperature", `"degC" isn't a unit Home Assistant takes for temperature. Did you mean °C?`},
		{"us", "duration", `"us" isn't a unit Home Assistant takes for duration. Did you mean μs?`},
		{"kwh", "power", `"kwh" isn't a unit Home Assistant takes for power (W, kW, mW, MW, GW or TW).`},
		{"", "voltage", "Voltage needs a unit: V, mV, μV, kV or MV."},
	} {
		err := config.CheckUnit(c.unit, c.class)
		if err == nil {
			t.Fatalf("%q/%q passed", c.unit, c.class)
		}
		if got := unitMessage(c.unit, c.class, err); got != c.want {
			t.Errorf("%q/%q: %q, want %q", c.unit, c.class, got, c.want)
		}
	}
}
