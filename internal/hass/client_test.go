package hass

import (
	"os"
	"testing"

	"watchglass/internal/config"
)

func mqttCfg() config.MQTT {
	return config.MQTT{
		Broker: "tcp://127.0.0.1:1883", Username: "u", Password: "p",
		ClientID: "watchglass", BaseTopic: "watchglass", DiscoveryPrefix: "homeassistant",
	}
}

func TestBuildOptionsRecipe(t *testing.T) {
	cfg := mqttCfg()
	opts := BuildOptions(cfg, func(string, ...any) {})
	if got := opts.Servers; len(got) != 1 || got[0].String() != "tcp://127.0.0.1:1883" {
		t.Errorf("servers = %v", got)
	}
	if opts.ClientID != "watchglass" {
		t.Errorf("client id = %q", opts.ClientID)
	}
	if opts.Username != "u" || opts.Password != "p" {
		t.Errorf("credentials not set")
	}
	if !opts.AutoReconnect || !opts.ConnectRetry {
		t.Error("auto-reconnect/connect-retry must be enabled")
	}
	if opts.WillTopic != "watchglass/status" || string(opts.WillPayload) != "offline" || !opts.WillRetained {
		t.Errorf("LWT wrong: topic=%q payload=%q retained=%v", opts.WillTopic, opts.WillPayload, opts.WillRetained)
	}
	if opts.WillQos != 1 {
		t.Errorf("will qos = %d", opts.WillQos)
	}
}

func TestBuildOptionsOmitsEmptyCredentials(t *testing.T) {
	cfg := mqttCfg()
	cfg.Username, cfg.Password = "", ""
	opts := BuildOptions(cfg, func(string, ...any) {})
	if opts.Username != "" || opts.Password != "" {
		t.Error("empty credentials must stay empty")
	}
}

// Live-broker test: opt-in only, skipped everywhere a broker isn't provided.
func TestConnectLiveBroker(t *testing.T) {
	broker := os.Getenv("WATCHGLASS_TEST_BROKER")
	if broker == "" {
		t.Skip("WATCHGLASS_TEST_BROKER not set")
	}
	cfg := mqttCfg()
	cfg.Broker = broker
	c, err := Connect(cfg, t.Logf)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()
	if err := c.Publish("watchglass/test", 1, false, []byte("hello")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}
