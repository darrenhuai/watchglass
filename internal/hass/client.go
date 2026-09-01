// Package hass publishes watchglass state to an MQTT broker with Home
// Assistant auto-discovery, so every watch appears as HA entities with zero
// YAML. Publish-only by design: no command topics until auth exists.
package hass

import (
	"errors"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"watchglass/internal/config"
)

// Client is the thin MQTT surface the publisher needs. Tests fake it.
type Client interface {
	Publish(topic string, qos byte, retain bool, payload []byte) error
	Close()
}

var errPublishTimeout = errors.New("mqtt publish timed out")

// BuildOptions assembles the paho connection recipe. Split out so the
// recipe is unit-testable without a broker.
func BuildOptions(cfg config.MQTT, logf func(string, ...any)) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5*time.Second).
		SetConnectTimeout(10*time.Second).
		SetOrderMatters(false).
		SetWill(cfg.BaseTopic+"/status", "offline", 1, true)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}
	statusTopic := cfg.BaseTopic + "/status"
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		c.Publish(statusTopic, 1, true, "online")
		logf("mqtt: connected to %s", cfg.Broker)
	})
	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		logf("mqtt: connection lost: %v", err)
	})
	return opts
}

type pahoClient struct {
	c           mqtt.Client
	statusTopic string
}

// Connect starts the MQTT connection and returns immediately: a dead broker
// never blocks watchglass startup — paho retries in the background and the
// OnConnect handler announces availability when it lands.
func Connect(cfg config.MQTT, logf func(string, ...any)) (Client, error) {
	c := mqtt.NewClient(BuildOptions(cfg, logf))
	c.Connect() // deliberately not waited on; ConnectRetry owns the outcome
	return &pahoClient{c: c, statusTopic: cfg.BaseTopic + "/status"}, nil
}

func (p *pahoClient) Publish(topic string, qos byte, retain bool, payload []byte) error {
	tok := p.c.Publish(topic, qos, retain, payload)
	if !tok.WaitTimeout(5 * time.Second) {
		return errPublishTimeout
	}
	return tok.Error()
}

func (p *pahoClient) Close() {
	tok := p.c.Publish(p.statusTopic, 1, true, "offline")
	tok.WaitTimeout(2 * time.Second)
	p.c.Disconnect(250)
}
