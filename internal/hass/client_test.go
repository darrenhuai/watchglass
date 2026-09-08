package hass

import (
	"os"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/darrenhuai/watchglass/internal/config"
)

func mqttCfg() config.MQTT {
	return config.MQTT{
		Broker: "tcp://127.0.0.1:1883", Username: "u", Password: "p",
		ClientID: "watchglass", BaseTopic: "watchglass", DiscoveryPrefix: "homeassistant",
	}
}

// fakeToken is a no-op mqtt.Token: BuildOptions's OnConnect handler discards
// the token from its own Publish call, so nothing here needs to be real.
type fakeToken struct{}

func (fakeToken) Wait() bool                     { return true }
func (fakeToken) WaitTimeout(time.Duration) bool { return true }
func (fakeToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (fakeToken) Error() error { return nil }

// spyPahoClient is a minimal mqtt.Client fake: only Publish is exercised by
// the OnConnect handler under test, but the interface requires the rest.
type spyPahoClient struct {
	published []string
}

func (c *spyPahoClient) IsConnected() bool      { return true }
func (c *spyPahoClient) IsConnectionOpen() bool { return true }
func (c *spyPahoClient) Connect() mqtt.Token    { return fakeToken{} }
func (c *spyPahoClient) Disconnect(uint)        {}
func (c *spyPahoClient) Publish(topic string, qos byte, retained bool, payload interface{}) mqtt.Token {
	c.published = append(c.published, topic)
	return fakeToken{}
}
func (c *spyPahoClient) Subscribe(string, byte, mqtt.MessageHandler) mqtt.Token {
	return fakeToken{}
}
func (c *spyPahoClient) SubscribeMultiple(map[string]byte, mqtt.MessageHandler) mqtt.Token {
	return fakeToken{}
}
func (c *spyPahoClient) Unsubscribe(...string) mqtt.Token     { return fakeToken{} }
func (c *spyPahoClient) AddRoute(string, mqtt.MessageHandler) {}
func (c *spyPahoClient) OptionsReader() mqtt.ClientOptionsReader {
	return mqtt.ClientOptionsReader{}
}

func TestBuildOptionsRecipe(t *testing.T) {
	cfg := mqttCfg()
	opts := BuildOptions(cfg, func(string, ...any) {}, nil)
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
	opts := BuildOptions(cfg, func(string, ...any) {}, nil)
	if opts.Username != "" || opts.Password != "" {
		t.Error("empty credentials must stay empty")
	}
}

// TestBuildOptionsOnConnectCallsOnReadyAfterOnlinePublish is the regression
// test for Bug 4 (initial HA discovery sync racing paho's async connect):
// the fix moves the discovery sync into the OnConnect handler so it only
// ever runs against a real, established connection. This asserts the
// handler publishes "online" before invoking onReady, and invokes it
// exactly once per connect.
func TestBuildOptionsOnConnectCallsOnReadyAfterOnlinePublish(t *testing.T) {
	cfg := mqttCfg()
	fc := &spyPahoClient{}
	calls := 0
	publishedAtReadyTime := -1
	onReady := func() {
		calls++
		publishedAtReadyTime = len(fc.published)
	}
	opts := BuildOptions(cfg, func(string, ...any) {}, onReady)

	opts.OnConnect(fc)

	if calls != 1 {
		t.Fatalf("onReady called %d times, want exactly 1", calls)
	}
	if publishedAtReadyTime != 1 {
		t.Errorf("onReady fired after %d publishes, want 1 (online status must land first)", publishedAtReadyTime)
	}
	if len(fc.published) != 1 || fc.published[0] != cfg.BaseTopic+"/status" {
		t.Errorf("published = %v, want a single publish to %s/status", fc.published, cfg.BaseTopic)
	}
}

// TestBuildOptionsOnConnectNilOnReadySafe covers the nil onReady case: a
// broker connection with no registered publisher (cfg.MQTT nil path never
// reaches BuildOptions, but defensive nil-safety keeps this handler usable
// standalone and mirrors the doc comment's contract).
func TestBuildOptionsOnConnectNilOnReadySafe(t *testing.T) {
	cfg := mqttCfg()
	opts := BuildOptions(cfg, func(string, ...any) {}, nil)
	fc := &spyPahoClient{}

	opts.OnConnect(fc) // must not panic
}

// Live-broker test: opt-in only, skipped everywhere a broker isn't provided.
func TestConnectLiveBroker(t *testing.T) {
	broker := os.Getenv("WATCHGLASS_TEST_BROKER")
	if broker == "" {
		t.Skip("WATCHGLASS_TEST_BROKER not set")
	}
	cfg := mqttCfg()
	cfg.Broker = broker
	c, err := Connect(cfg, t.Logf, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()
	if err := c.Publish("watchglass/test", 1, false, []byte("hello")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}
