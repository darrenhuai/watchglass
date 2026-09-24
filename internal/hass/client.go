// Package hass publishes watchglass state to an MQTT broker with Home
// Assistant auto-discovery, so every watch appears as HA entities with zero
// YAML. Publish-only by design: no command topics until auth exists.
package hass

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"

	"github.com/darrenhuai/watchglass/internal/config"
)

// Client is the thin MQTT surface the publisher needs. Tests fake it.
type Client interface {
	Publish(topic string, qos byte, retain bool, payload []byte) error
	// Connected reports whether the broker connection is up right now.
	// The publisher doesn't try to publish state while it isn't: paho
	// would hold each publish for its full timeout and then drop it (a
	// clean session discards it on the next connect anyway), and the next
	// connect republishes everything (Publisher.resync).
	Connected() bool
	// Status is the connection state for the web UI: StateConnected,
	// StateConnecting (the first attempt hasn't finished) or StateDown,
	// with a *ConnError saying why when it is down.
	Status() (string, error)
	Close()
}

// Connection states, as Status reports them.
const (
	StateConnected  = "connected"
	StateConnecting = "connecting"
	StateDown       = "not connected"
)

// ConnError is why the broker isn't connected: Reason in a few plain words
// ("connection refused"), for the web UI; Err is the error paho gave.
type ConnError struct {
	Reason string
	Err    error
}

func (e *ConnError) Error() string { return e.Reason }
func (e *ConnError) Unwrap() error { return e.Err }

// Detail is the raw error for a tooltip or a log line, without paho's
// "network Error : " wrapper or a trailing full stop.
func (e *ConnError) Detail() string {
	if e.Err == nil {
		return ""
	}
	msg := e.Err.Error()
	if before, after, ok := strings.Cut(msg, " : "); ok && strings.EqualFold(before, packets.ErrorNetworkError.Error()) {
		msg = after
	}
	return strings.TrimSuffix(msg, ".")
}

var errPublishTimeout = errors.New("mqtt publish timed out")

// connRetryEvery is the pause between failed attempts before the first
// connect; maxReconnectEvery caps paho's backoff after a connection that
// worked is lost (its default of 10 minutes means a broker restarted
// during an HA update would come back to watchglass long after HA).
const (
	connRetryEvery    = 5 * time.Second
	maxReconnectEvery = 15 * time.Second
	// failLogEvery rate-limits "can't connect" lines: paho retries every
	// few seconds for as long as the broker is away.
	failLogEvery = time.Minute
)

// tracker keeps the connection state paho reports, for Status and for the
// rate-limited log lines. paho calls it from its own goroutines.
type tracker struct {
	broker string // redacted
	logf   func(string, ...any)
	now    func() time.Time

	mu      sync.Mutex
	state   string
	err     error
	lastLog time.Time // last "can't connect" line; zero after a connect
}

func newTracker(broker string, logf func(string, ...any)) *tracker {
	return &tracker{broker: RedactBroker(broker), logf: logf, now: time.Now, state: StateConnecting}
}

// RedactBroker hides a password written into the broker URL, for logs.
func RedactBroker(broker string) string {
	if u, err := url.Parse(broker); err == nil {
		return u.Redacted()
	}
	return broker
}

func (t *tracker) connected() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state, t.err, t.lastLog = StateConnected, nil, time.Time{}
	t.logf("mqtt: connected to %s", t.broker)
}

// failed records one failed connection attempt. The first failure after a
// connect (or at startup) is logged straight away; after that at most one
// line a minute, however often paho retries.
func (t *tracker) failed(err error) {
	ce := &ConnError{Reason: connReason(err), Err: err}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state, t.err = StateDown, ce
	if now := t.now(); t.lastLog.IsZero() || now.Sub(t.lastLog) >= failLogEvery {
		t.lastLog = now
		t.logf("mqtt: can't connect to %s: %s (retrying): %s", t.broker, ce.Reason, ce.Detail())
	}
}

func (t *tracker) lost(err error) {
	reason := "connection lost"
	if err != nil {
		reason += ": " + connReason(err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state, t.err = StateDown, &ConnError{Reason: reason, Err: err}
	t.logf("mqtt: connection to %s lost: %v (reconnecting)", t.broker, err)
}

func (t *tracker) status() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state, t.err
}

// connReason turns a connect error into a few words a person can act on.
// The raw error stays in the log and in ConnError.Err.
func connReason(err error) string {
	var errno syscall.Errno
	var dns *net.DNSError
	var nerr net.Error
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certErr *tls.CertificateVerificationError
	switch {
	case err == nil:
		return "unknown error"
	case errors.Is(err, packets.ErrorRefusedBadUsernameOrPassword):
		return "wrong username or password"
	case errors.Is(err, packets.ErrorRefusedNotAuthorised):
		return "not authorised (check the username and password)"
	case errors.Is(err, packets.ErrorRefusedIDRejected):
		return "client ID rejected"
	case errors.Is(err, packets.ErrorRefusedServerUnavailable):
		return "broker unavailable"
	case errors.Is(err, packets.ErrorRefusedBadProtocolVersion):
		return "the broker refused the MQTT version"
	// 10061 and 10065/10051 are Winsock's WSAECONNREFUSED and
	// WSAEHOSTUNREACH/WSAENETUNREACH; syscall's constants are the POSIX ones.
	case errors.As(err, &errno) && (errno == syscall.ECONNREFUSED || errno == 10061):
		return "connection refused"
	case errors.As(err, &errno) && (errno == syscall.EHOSTUNREACH || errno == syscall.ENETUNREACH || errno == 10065 || errno == 10051):
		return "host unreachable"
	case errors.As(err, &dns):
		return "no such host"
	case errors.As(err, &unknownCA), errors.As(err, &hostErr), errors.As(err, &certErr):
		return "certificate not trusted"
	case errors.As(err, &nerr) && nerr.Timeout():
		return "no answer (timed out)"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.As(err, &errno) && (errno == syscall.ECONNRESET || errno == 10054):
		return "the broker closed the connection"
	}
	msg := err.Error()
	// paho wraps network errors as "network Error : <cause>".
	if _, after, ok := strings.Cut(msg, " : "); ok {
		msg = after
	}
	return msg
}

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
//
// Every failed attempt (before the first connect and while reconnecting)
// reaches the connection notification handler, which is how a broker on a
// dead port gets a log line and a reason in the web UI instead of silence.
func BuildOptions(cfg config.MQTT, logf func(string, ...any), onReady func()) *mqtt.ClientOptions {
	return buildOptions(cfg, newTracker(cfg.Broker, logf), onReady)
}

func buildOptions(cfg config.MQTT, t *tracker, onReady func()) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(connRetryEvery).
		SetMaxReconnectInterval(maxReconnectEvery).
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
		t.connected()
		if onReady != nil {
			onReady()
		}
	})
	opts.SetConnectionNotificationHandler(func(_ mqtt.Client, n mqtt.ConnectionNotification) {
		switch n := n.(type) {
		case mqtt.ConnectionNotificationFailed:
			t.failed(n.Reason)
		case mqtt.ConnectionNotificationLost:
			t.lost(n.Reason)
		}
	})
	return opts
}

type pahoClient struct {
	c           mqtt.Client
	t           *tracker
	statusTopic string
}

// Connect starts the MQTT connection and returns immediately: a dead broker
// never blocks watchglass startup — paho retries in the background and the
// OnConnect handler announces availability when it lands. onReady, if
// non-nil, is forwarded to BuildOptions — see its doc comment for why
// callers needing an initial discovery sync must do it from there rather
// than right after this function returns.
func Connect(cfg config.MQTT, logf func(string, ...any), onReady func()) (Client, error) {
	t := newTracker(cfg.Broker, logf)
	c := mqtt.NewClient(buildOptions(cfg, t, onReady))
	c.Connect() // deliberately not waited on; ConnectRetry owns the outcome
	return &pahoClient{c: c, t: t, statusTopic: cfg.BaseTopic + "/status"}, nil
}

func (p *pahoClient) Publish(topic string, qos byte, retain bool, payload []byte) error {
	tok := p.c.Publish(topic, qos, retain, payload)
	if !tok.WaitTimeout(5 * time.Second) {
		return errPublishTimeout
	}
	return tok.Error()
}

// Connected is paho's IsConnectionOpen: true only once the MQTT handshake
// has completed (IsConnected is also true while ConnectRetry is still
// trying).
func (p *pahoClient) Connected() bool { return p.c.IsConnectionOpen() }

// Status trusts paho's own view of an open connection over the tracker:
// paho delivers its connected and lost notifications on separate
// goroutines, so the tracker could briefly hold them out of order.
func (p *pahoClient) Status() (string, error) {
	if p.c.IsConnectionOpen() {
		return StateConnected, nil
	}
	state, err := p.t.status()
	if state == StateConnected {
		// Open is false but no lost notification has landed yet.
		return StateDown, &ConnError{Reason: "connection lost"}
	}
	return state, err
}

func (p *pahoClient) Close() {
	if p.c.IsConnectionOpen() {
		tok := p.c.Publish(p.statusTopic, 1, true, "offline")
		tok.WaitTimeout(2 * time.Second)
	}
	p.c.Disconnect(250)
}
