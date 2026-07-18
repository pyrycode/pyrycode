// Package relay drives the binary's outbound long-lived connection to the
// relay: opens the WSS via internal/transport and exposes inbound frames
// as protocol.RoutingEnvelope values via Frames(). The binary↔relay leg
// is content-blind — the relay registers the binary's server-id from the
// x-pyrycode-server request header and claims the slot on WS upgrade, so
// the conn is treated as established the moment the upgrade completes;
// there is no relay-originated hello/hello_ack handshake on this leg. It
// does NOT dispatch on envelope types, validate device tokens, or
// interpret application-level error payloads — those concerns layer above
// this package in a future ticket (supervisor wiring + per-message
// handlers).
//
// The single source of truth for the headers and close codes is
// docs/protocol-mobile.md (§ Authentication, § Connection lifecycle,
// § Worked example). When that document changes, this package changes.
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

// serverIDConflictThreshold is the transport FatalCloseThreshold for the
// binary↔relay leg: the number of CONSECUTIVE 4409 closes required before
// the conflict is treated as terminal. A transient self-conflict — the
// daemon reconnecting into the relay's dead-conn-detection blind window
// after a drop (worst case ~60s: 30s ping interval + 30s pong wait relay
// side) — clears as soon as the relay reaps the stale conn and the grace
// reclaim path hands the slot over. Eight consecutive observations put
// ≥ ~73s of backoff (minimum jitter) between the first conflict and the
// fatal verdict, so only a genuinely persistent claim (another live
// binary) unwinds the daemon. Incident + derivation: #1072 (2026-07-16).
const serverIDConflictThreshold = 8

// Sentinel errors. Callers distinguish fatal vs. retryable via errors.Is.
var (
	// ErrServerIDConflict is the terminal error returned by Wait when
	// the relay refused our claim with WS close 4409 persistently
	// (serverIDConflictThreshold consecutive closes across the transport's
	// backoff ladder — a transient self-reconnect race clears well inside
	// that window, see #1072). Another binary is holding the same
	// server-id. Operator escalation: another pyry is already running for
	// this server-id.
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

	// reconnected re-broadcasts the transport's fresh-conn edge (see
	// Reconnected) to the v2 push drain. Cap-1 drop-on-full, single observer.
	reconnected chan struct{}

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
	dialURL, err := resolveDialURL(cfg.RelayURL, cfg.AllowInsecureScheme)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	headers.Set("x-pyrycode-server", string(cfg.ServerID))
	headers.Set("x-pyrycode-version", cfg.BinaryVersion)
	headers.Set("user-agent", "pyry/"+cfg.BinaryVersion)

	tcfg := transport.Config{
		URL:                 dialURL,
		Headers:             headers,
		WriteTimeout:        10 * time.Second,
		Logger:              cfg.Logger,
		FatalCloseCodes:     []websocket.StatusCode{statusServerIDConflict},
		FatalCloseThreshold: serverIDConflictThreshold,
	}
	c := &Connection{
		cfg:         cfg,
		client:      transport.New(tcfg),
		frames:      make(chan protocol.RoutingEnvelope),
		reconnected: make(chan struct{}, 1),
		closed:      make(chan struct{}),
		done:        make(chan struct{}),
	}
	go c.run(ctx)
	return c, nil
}

// resolveDialURL is the single home for relay-URL handling: it validates the
// scheme and appends the binary's /v1/server endpoint when the URL carries no
// meaningful path, mirroring the phone's /v1/client convention (both consumers
// read the same base relay_url; the phone appends /v1/client, the daemon
// appends /v1/server). An operator-supplied path is preserved unchanged.
// Returns the dial URL, or a wrapped ErrInvalidConfig.
func resolveDialURL(raw string, allowInsecure bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: RelayURL parse: %v", ErrInvalidConfig, err)
	}
	if u.Scheme != "wss" && !(allowInsecure && u.Scheme == "ws") {
		return "", fmt.Errorf("%w: RelayURL scheme must be wss (got %q)",
			ErrInvalidConfig, u.Scheme)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/server"
	}
	return u.String(), nil
}

// connectWithClient is a test seam: builds a Connection that wraps the
// supplied transport client. Bypasses Connect's URL validation so tests
// can use a ws:// httptest server. Production callers use Connect.
func connectWithClient(ctx context.Context, cfg Config, client *transport.Client) *Connection {
	c := &Connection{
		cfg:         cfg,
		client:      client,
		frames:      make(chan protocol.RoutingEnvelope),
		reconnected: make(chan struct{}, 1),
		closed:      make(chan struct{}),
		done:        make(chan struct{}),
	}
	go c.run(ctx)
	return c
}

// Frames returns the channel of inbound frames. The channel closes when
// the lifecycle terminates. Frames are delivered in the order the
// underlying conn produces them; reconnects are transparent — frames
// resume on the new conn directly.
func (c *Connection) Frames() <-chan protocol.RoutingEnvelope { return c.frames }

// Send marshals env to JSON and forwards it to the relay over the
// current transport conn. Returns transport.ErrDisconnected if the
// underlying conn is currently dropped (caller decides whether to retry
// or drop the frame); transport reconnect happens asynchronously, so a
// frame sent while disconnected is lost — that's consistent with the
// protocol's connection-lifecycle expectations
// (docs/protocol-mobile.md § Connection lifecycle).
func (c *Connection) Send(env protocol.RoutingEnvelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal routing envelope: %w", err)
	}
	return c.client.Send(raw)
}

// Connected reports whether the binary↔relay transport leg currently has a live
// conn — a level poll of the underlying transport.Client for the v2 push drain's
// pre-seal probe (V2SessionConfig.Connected, #874). A false result means Send
// would be dropped, so the drain holds the head unsealed rather than burning a
// Noise nonce on a frame that cannot reach the phone.
func (c *Connection) Connected() bool {
	return c.client.IsConnected()
}

// Reconnected returns a channel that emits once per fresh binary↔relay conn
// (the initial connect and every reconnect). Cap-1 drop-on-full, single
// observer — the v2 push drain (V2SessionConfig.Reconnect) uses it to flush
// held control envelopes the instant the leg recovers, without waiting for the
// next Push (#875). Derived from the connection's own (sole) observation of
// transport.Client.Connected(); the manager must NOT observe that transport
// channel directly, which is documented single-observer.
func (c *Connection) Reconnected() <-chan struct{} { return c.reconnected }

// CloseConn asks the relay to close the named phone conn with the given
// WS close code. Builds a close-only routing envelope (no Frame) with
// CloseCode set and forwards via the transport. Returns
// transport.ErrNotConnected when the underlying WS is currently dropped
// or transport.ErrDisconnected if the live conn drops while Send is
// blocked (caller decides whether to retry; transport reconnect runs
// asynchronously). Wire mechanism: docs/protocol-mobile.md § Routing
// envelope (CloseCode).
//
// CloseConn does NOT block on the relay's close-frame being delivered to
// the phone — the request is fire-and-forget at this layer. The per-conn
// close ack is implicit (no more inbound frames will arrive for connID).
//
// The dispatcher's auth-reject path does NOT call CloseConn; instead it
// publishes one routing envelope carrying both Frame and CloseCode so
// the error envelope and the close are atomic on the wire. CloseConn is
// the explicit surface for callers that want close-without-payload
// (none today; reserved for the idle/inactivity sweep hinted at in #307).
func (c *Connection) CloseConn(connID string, code uint16) error {
	env := protocol.RoutingEnvelope{ConnID: connID, CloseCode: code}
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal close envelope: %w", err)
	}
	return c.client.Send(raw)
}

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
			// The binary↔relay leg is content-blind: the relay registers
			// the server-id from the x-pyrycode-server header and claims
			// the slot on WS upgrade, sending no hello_ack. The conn is
			// established the moment Connected fires — go straight to
			// forwarding, no handshake.
			c.cfg.Logger.Info("relay: conn established",
				"server_id", string(c.cfg.ServerID))
			// Re-broadcast the fresh-conn edge to the v2 push drain so a
			// #874-held control envelope flushes the instant the leg recovers,
			// without waiting for the next Push (#875). Non-blocking cap-1 send —
			// a dropped duplicate is harmless (one surviving signal drains the
			// whole held FIFO). Runs before forwardFrames blocks.
			select {
			case c.reconnected <- struct{}{}:
			default:
			}
			c.forwardFrames(ctx)
		}
	}
}

func (c *Connection) forwardFrames(ctx context.Context) {
	for {
		raw, err := c.client.Receive(ctx)
		if err != nil {
			// Expected: transport.ErrDisconnected (conn dropped; run
			// re-enters forwardFrames on the next Connected),
			// transport.ErrClosed (Close called), or ctx.Err (shutdown).
			// Logged for diagnosability — an unrecognised err is a
			// breadcrumb for transport API drift.
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
