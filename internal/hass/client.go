// Package hass publishes watchglass state to an MQTT broker with Home
// Assistant auto-discovery, so every watch appears as HA entities with zero
// YAML. Publish-only by design: no command topics until auth exists.
package hass

import (
	"errors"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/darrenhuai/watchglass/internal/config"
)

// Client is the thin MQTT surface the publisher needs. Tests fake it.
type Client interface {
	Publish(topic string, qos byte, retain bool, payload []byte) error
	Close()
}

var errPublishTimeout = errors.New("mqtt publish timed out")

// BuildOptions assembles the paho connection recipe. Split out so the
// recipe is unit-testable without a broker.
//
// onReady, if non-nil, runs at the end of the OnConnect handler — i.e. on
// every REAL established connection, first connect and every reconnect
// alike — never right after Connect() returns. That distinction is the fix
// for Bug 4: with SetConnectRetry(true), paho reports IsConnected() == true
// while the handshake is still in flight, so code that syncs immediately
// after Connect() races a connection that isn't actually up yet. Publishing
// discovery configs in that window gets them persisted-then-destroyed by
// paho's CleanSession reset on the real connect, and their qos-1 tokens
// never complete. Running the sync from here instead guarantees the broker
// session genuinely exists. Firing on reconnect too is intentional and
// harmless: it republishes retained discovery configs, which both no-ops
// against an unchanged broker and heals one that lost retained state.
//
// CleanSession is deliberately left at paho's default (true) rather than
// disabled: once discovery only ever publishes after a real connect, the
// old persisted-message wipe this bug depended on no longer matters, and a
// clean session keeps reconnect semantics simple.
//
// onReady must not block: it runs on paho's own callback goroutine, so it
// should only hand off work (e.g. call a non-blocking SyncAsync), never do
// the syncing itself.
func BuildOptions(cfg config.MQTT, logf func(string, ...any), onReady func()) *mqtt.ClientOptions {
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
		if onReady != nil {
			onReady()
		}
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
// OnConnect handler announces availability when it lands. onReady, if
// non-nil, is forwarded to BuildOptions — see its doc comment for why
// callers needing an initial discovery sync must do it from there rather
// than right after this function returns.
func Connect(cfg config.MQTT, logf func(string, ...any), onReady func()) (Client, error) {
	c := mqtt.NewClient(BuildOptions(cfg, logf, onReady))
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
