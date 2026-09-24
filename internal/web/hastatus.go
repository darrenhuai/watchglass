package web

import (
	"errors"
	"net/http"
	"net/url"
)

// haView is the topbar's Home Assistant line: whether watchglass is
// connected to the MQTT broker, and if not, why. It is shown only when
// config.yaml has an mqtt: block.
type haView struct {
	Show bool
	// State is "connected", "connecting" or "down".
	State string
	// Text follows "Home Assistant: ".
	Text string
	// Title is the tooltip: the broker, and the raw error when down.
	Title string
}

// haStatus reads MQTTStatus fresh on every call: the topbar is rendered
// on every page and polled by topbar.js.
func (s *Server) haStatus() haView {
	if s.MQTTStatus == nil {
		return haView{}
	}
	broker := ""
	s.mu.Lock()
	if s.cfg.MQTT != nil {
		broker = s.cfg.MQTT.Broker
		if u, err := url.Parse(broker); err == nil {
			broker = u.Redacted()
		}
	}
	s.mu.Unlock()
	state, err := s.MQTTStatus()
	v := haView{Show: true, Title: "MQTT broker " + broker}
	switch {
	case state == "connected":
		v.State, v.Text = "connected", "connected"
	case state == "connecting" && err == nil:
		v.State, v.Text = "connecting", "connecting…"
	default:
		v.State, v.Text = "down", "not connected"
		if err != nil {
			v.Text += ": " + err.Error()
			var d interface{ Detail() string }
			if errors.As(err, &d) && d.Detail() != "" && d.Detail() != err.Error() {
				v.Title += ": " + d.Detail()
			}
		}
		v.Title += ". Retrying every few seconds; changes to the mqtt: block in " + s.configFile() + " need a restart."
	}
	return v
}

// haStatusFragment serves the topbar line alone, for topbar.js to poll:
// the broker going away or coming back shows without a reload.
func (s *Server) haStatusFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "haStatus", s.haStatus())
}
