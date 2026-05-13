// Package relay drives the binary's outbound long-lived connection to the
// relay: opens the WSS via internal/transport, runs the one-shot
// hello/hello_ack handshake on every fresh conn, and exposes inbound
// frames as protocol.RoutingEnvelope values via Frames(). It does NOT
// dispatch on envelope types, validate device tokens, or interpret
// application-level error payloads — those concerns layer above this
// package in a future ticket (supervisor wiring + per-message handlers).
//
// The single source of truth for the headers, handshake timing, and close
// codes is docs/protocol-mobile.md (§ Authentication, § Connection
// lifecycle, § Worked example). When that document changes, this package
// changes.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/transport"
)

// statusServerIDConflict is the WS close code the relay sends when a
// server-id is already claimed (docs/protocol-mobile.md § Error codes).
// Typed locally so callers don't have to import the websocket package
// for the value.
const statusServerIDConflict websocket.StatusCode = 4409

// handshakeTimeout is the deadline for receiving hello_ack after sending
// hello. Wire-spec 5s per docs/protocol-mobile.md § Connection lifecycle.
// Exposed as a package var (lowercase) so tests can substitute a shorter
// value; not part of the public API.
var handshakeTimeout = 5 * time.Second

// Sentinel errors. Callers distinguish fatal vs. retryable via errors.Is.
var (
	// ErrServerIDConflict is the terminal error returned by Wait when
	// the relay refused our claim with WS close 4409. Another binary is
	// currently holding the same server-id and the relay's 30-second
	// grace window has not elapsed. Operator escalation: another pyry is
	// already running for this server-id, or a stale connection on the
	// relay side has not yet been reaped.
	ErrServerIDConflict = errors.New("relay: server-id conflict (close 4409)")

	// ErrInvalidConfig is returned by Connect on missing required fields.
	ErrInvalidConfig = errors.New("relay: invalid config")
)

// Config carries the static configuration for a Connection. The caller
// resolves ServerID via internal/identity.LoadOrCreate before constructing
// Config — the relay package never touches the on-disk store, keeping the
// net package free of pairing / storage concerns.
type Config struct {
	ServerID      identity.ServerID
	RelayURL      string
	BinaryVersion string
	Logger        *slog.Logger

	// AllowInsecureScheme, when true, lets RelayURL use plain ws:// in
	// addition to wss://. Test-only seam so e2e suites can point pyry at
	// an httptest-hosted fakerelay over plaintext. Production callers
	// leave this false; cmd/pyry flips it only when the operator sets
	// PYRY_ALLOW_INSECURE_RELAY=1.
	AllowInsecureScheme bool
}

// Connection runs the binary↔relay leg of the wire protocol. Lifecycle is
// tied to the context passed to Connect; cancellation closes the WS
// cleanly. Wait blocks until the lifecycle terminates; the returned error
// is the terminal classification (ErrServerIDConflict for fatal 4409,
// ctx.Err for graceful shutdown, or a wrapped transport error for
// unexpected halts).
type Connection struct {
	cfg    Config
	client *transport.Client

	frames chan protocol.RoutingEnvelope

	closeOnce sync.Once
	closed    chan struct{}

	// done closes when run exits; result is set by run before close.
	done   chan struct{}
	result error
}

// Connect builds the transport, starts the lifecycle goroutine, and
// returns immediately. The connection is not yet Ready — observe Frames
// to consume post-handshake inbound frames, or call Wait to block on
// terminal classification. The caller is responsible for invoking Close
// during shutdown to release resources; ctx cancellation also drains the
// lifecycle.
func Connect(ctx context.Context, cfg Config) (*Connection, error) {
	if cfg.Logger == nil {
		return nil, fmt.Errorf("%w: Logger is required", ErrInvalidConfig)
	}
	if cfg.ServerID == "" {
		return nil, fmt.Errorf("%w: ServerID is required", ErrInvalidConfig)
	}
	if cfg.RelayURL == "" {
		return nil, fmt.Errorf("%w: RelayURL is required", ErrInvalidConfig)
	}
	if cfg.BinaryVersion == "" {
		return nil, fmt.Errorf("%w: BinaryVersion is required", ErrInvalidConfig)
	}
	parsedURL, err := url.Parse(cfg.RelayURL)
	if err != nil {
		return nil, fmt.Errorf("%w: RelayURL parse: %v", ErrInvalidConfig, err)
	}
	if parsedURL.Scheme != "wss" && !(cfg.AllowInsecureScheme && parsedURL.Scheme == "ws") {
		return nil, fmt.Errorf("%w: RelayURL scheme must be wss (got %q)",
			ErrInvalidConfig, parsedURL.Scheme)
	}

	headers := http.Header{}
	headers.Set("x-pyrycode-server", string(cfg.ServerID))
	headers.Set("x-pyrycode-version", cfg.BinaryVersion)
	headers.Set("user-agent", "pyry/"+cfg.BinaryVersion)

	tcfg := transport.Config{
		URL:             cfg.RelayURL,
		Headers:         headers,
		WriteTimeout:    10 * time.Second,
		Logger:          cfg.Logger,
		FatalCloseCodes: []websocket.StatusCode{statusServerIDConflict},
	}
	c := &Connection{
		cfg:    cfg,
		client: transport.New(tcfg),
		frames: make(chan protocol.RoutingEnvelope),
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go c.run(ctx)
	return c, nil
}

// connectWithClient is a test seam: builds a Connection that wraps the
// supplied transport client. Bypasses Connect's URL validation so tests
// can use a ws:// httptest server. Production callers use Connect.
func connectWithClient(ctx context.Context, cfg Config, client *transport.Client) *Connection {
	c := &Connection{
		cfg:    cfg,
		client: client,
		frames: make(chan protocol.RoutingEnvelope),
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go c.run(ctx)
	return c
}

// Frames returns the channel of post-handshake inbound frames. The
// channel closes when the lifecycle terminates. Frames are delivered in
// the order the underlying conn produces them; reconnects are
// transparent (a fresh hello/hello_ack handshake runs first, then frames
// resume on the new conn).
func (c *Connection) Frames() <-chan protocol.RoutingEnvelope { return c.frames }

// Wait blocks until the lifecycle terminates and returns the terminal
// classification: ErrServerIDConflict (fatal), ctx.Err (graceful
// shutdown), or a wrapped transport error.
func (c *Connection) Wait() error {
	<-c.done
	return c.result
}

// Close requests a clean shutdown. Idempotent. After Close, Frames
// drains and closes; Wait returns.
func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.client.Close()
	})
	return nil
}

// --- internals ---

func (c *Connection) run(ctx context.Context) {
	defer close(c.done)
	defer close(c.frames)

	transportErrCh := make(chan error, 1)
	go func() { transportErrCh <- c.client.Connect(ctx) }()
	defer c.client.Close()

	for {
		select {
		case <-ctx.Done():
			c.result = ctx.Err()
			return
		case <-c.closed:
			c.result = nil
			return
		case err := <-transportErrCh:
			c.result = c.classifyTransportErr(err)
			return
		case <-c.client.Connected():
			if err := c.handshake(ctx); err != nil {
				c.cfg.Logger.Warn("relay: handshake failed; recycling conn",
					"err", err)
				c.client.DropConn()
				continue
			}
			c.forwardFrames(ctx)
		}
	}
}

func (c *Connection) handshake(ctx context.Context) error {
	payload := protocol.HelloServerPayload{
		Role:             "server",
		ServerID:         string(c.cfg.ServerID),
		BinaryVersion:    c.cfg.BinaryVersion,
		ProtocolVersions: []string{"v1"},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal hello payload: %w", err)
	}
	helloEnv := protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeHello,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
	helloRaw, err := json.Marshal(helloEnv)
	if err != nil {
		return fmt.Errorf("marshal hello envelope: %w", err)
	}
	if err := c.client.Send(helloRaw); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	frame, err := c.client.Receive(deadlineCtx)
	if err != nil {
		if errors.Is(deadlineCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("hello_ack timeout after %s", handshakeTimeout)
		}
		return fmt.Errorf("recv hello_ack: %w", err)
	}

	// Relay-to-binary frames are ALWAYS wrapped in RoutingEnvelope —
	// including hello_ack (docs/protocol-mobile.md § Worked example,
	// conn_id "-").
	var routing protocol.RoutingEnvelope
	if err := json.Unmarshal(frame, &routing); err != nil {
		return fmt.Errorf("decode routing envelope: %w", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(routing.Frame, &env); err != nil {
		return fmt.Errorf("decode inner envelope: %w", err)
	}
	if env.Type != protocol.TypeHelloAck {
		return fmt.Errorf("expected hello_ack, got type %q", env.Type)
	}
	c.cfg.Logger.Info("relay: handshake complete",
		"server_id", string(c.cfg.ServerID))
	return nil
}

func (c *Connection) forwardFrames(ctx context.Context) {
	for {
		raw, err := c.client.Receive(ctx)
		if err != nil {
			// Expected: transport.ErrDisconnected (conn dropped; run will
			// re-handshake on next Connected), transport.ErrClosed (Close
			// called), or ctx.Err (shutdown). Logged for diagnosability —
			// an unrecognised err is a breadcrumb for transport API drift.
			c.cfg.Logger.Debug("relay: forwardFrames exiting", "err", err)
			return
		}
		var routing protocol.RoutingEnvelope
		if err := json.Unmarshal(raw, &routing); err != nil {
			c.cfg.Logger.Warn("relay: malformed routing envelope; dropping",
				"err", err)
			continue
		}
		select {
		case c.frames <- routing:
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		}
	}
}

func (c *Connection) classifyTransportErr(err error) error {
	if errors.Is(err, transport.ErrFatalClose) {
		if status := websocket.CloseStatus(err); status == statusServerIDConflict {
			return ErrServerIDConflict
		}
	}
	return err
}
