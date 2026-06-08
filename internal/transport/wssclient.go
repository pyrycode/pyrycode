// Package transport provides a long-lived WSS client with automatic
// reconnect, exponential backoff with jitter, and native ping/pong
// heartbeat. It is the binary's outbound network primitive to the relay.
//
// The package is generic over frame payload. It accepts and emits []byte
// and knows nothing about pyrycode's protocol envelope, handshake, or
// routing. Protocol semantics live in internal/dispatch (future ticket);
// the wire-format types live in internal/protocol.
//
// The single source of truth for the reconnect cadence and heartbeat
// constants is docs/protocol-mobile.md (§ Heartbeat, § Reconnect). When
// that document changes, this package changes.
package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Wire-spec constants. See docs/protocol-mobile.md § Heartbeat and § Reconnect.
const (
	pingInterval      = 30 * time.Second
	pongTimeout       = 30 * time.Second
	reconnectInitial  = 1 * time.Second
	reconnectMax      = 30 * time.Second
	stabilityResetMin = 60 * time.Second
	maxFrameBytes     = 1 << 20 // 1 MiB — see Security review § Network & I/O

	// closeFrameGrace bounds the wait in serve() for recvPump's
	// close-frame observation when the first pump error has no
	// recognizable WS close status. See serve() for the full rationale.
	closeFrameGrace = 50 * time.Millisecond
)

// Config carries the static configuration for a Client. The caller supplies
// the relay URL and any request headers (server-id, binary-version,
// protocol-versions); this package does not construct headers. WriteTimeout
// bounds per-frame send I/O — it is NOT an inactivity timeout; the
// inactivity contract is the ping/pong heartbeat.
type Config struct {
	URL          string
	Headers      http.Header
	WriteTimeout time.Duration

	// Logger receives structured lifecycle logs (dial, reconnect, ping
	// timeout). Required; nil panics at New() time.
	Logger *slog.Logger

	// FatalCloseCodes lists WS close codes that terminate Connect's
	// reconnect loop with ErrFatalClose. Empty (default) preserves the
	// generic "reconnect on every drop" behaviour. The relay layer (#248)
	// passes []websocket.StatusCode{4409} so a server-id conflict halts
	// immediately rather than spinning in backoff.
	FatalCloseCodes []websocket.StatusCode
}

// Client maintains a single long-lived WSS connection with auto-reconnect.
// Methods are concurrency-safe. The zero value is not usable — call New.
type Client struct {
	cfg Config

	// pingInterval, pongTimeout, reconnectInitial, stabilityReset,
	// closeFrameGrace are package-constant defaults at construction.
	// Tests substitute shorter values via newClientForTest so the
	// cadence assertions don't take minutes to run.
	pingInterval     time.Duration
	pongTimeout      time.Duration
	reconnectInitial time.Duration
	reconnectMax     time.Duration
	stabilityReset   time.Duration
	closeFrameGrace  time.Duration

	// dialFn opens one WSS connection. Production points at the real
	// websocket.Dial; tests substitute a fake to drive backoff/reset
	// behaviour without a real network.
	dialFn func(ctx context.Context) (*websocket.Conn, error)

	// rngMu guards rng. math/rand.Rand is not safe for concurrent use,
	// and the test relay's failNextDial helper reads it from a different
	// goroutine than Connect's dial loop. Production access is from
	// Connect only (single goroutine) but the mutex is cheap.
	rngMu sync.Mutex
	rng   *rand.Rand

	// sendCh and recvCh proxy frames between caller and the currently-
	// live underlying conn. Both are unbuffered: backpressure is the
	// caller's problem.
	sendCh chan []byte
	recvCh chan []byte

	closeOnce sync.Once
	closeCh   chan struct{}

	// connectedCh emits a value on every successful conn that survives
	// setConn. Buffered to 1 with drop-on-full semantics: a slow observer
	// sees the most recent connect event, not every connect since boot.
	// The handshake layer (#248) is the only consumer.
	connectedCh chan struct{}

	// connDoneMu guards connDone. connDone is a per-conn signal channel:
	// closed when the current live conn drops, replaced with a fresh
	// channel before each new dial. Initial value is a pre-closed channel
	// so Receive before Connect returns ErrDisconnected immediately.
	connDoneMu sync.Mutex
	connDone   chan struct{}

	// mu guards conn (nil when no live conn).
	mu   sync.Mutex
	conn *websocket.Conn
}

// Sentinel errors.
var (
	// ErrNotConnected is returned by Send when there is no live conn.
	ErrNotConnected = errors.New("transport: not connected")

	// ErrClosed is returned by Send and Receive after Close (or the
	// parent context cancellation) has shut the client down.
	ErrClosed = errors.New("transport: client closed")

	// ErrDisconnected is returned by Receive when the underlying conn
	// dropped while Receive was blocked, or when no conn is currently
	// live. Callers observing this should NOT treat it as a re-handshake
	// trigger directly — observe Connected() for that. ErrDisconnected
	// means "your current Receive call returned because the wire dropped,
	// not because data arrived."
	ErrDisconnected = errors.New("transport: connection lost")

	// ErrFatalClose wraps a websocket close error whose status is in
	// Config.FatalCloseCodes. Returned by Connect; the underlying status
	// is recoverable via websocket.CloseStatus(err).
	ErrFatalClose = errors.New("transport: fatal close code")

	// ErrUpgradeRejected wraps a dial error where the relay returned a
	// non-101 HTTP response to the WebSocket upgrade (e.g. 404 on a wrong
	// URL path), as distinct from a network-level dial failure (DNS,
	// connection refused, TLS). Connect surfaces it loudly (WARN) on the
	// first failed upgrade per outage; the HTTP status is in the wrapped
	// error string. It is NOT a fatal close — the loop keeps retrying.
	ErrUpgradeRejected = errors.New("transport: websocket upgrade rejected")
)

// New returns a Client. The Client is not yet connected; call Connect.
func New(cfg Config) *Client {
	if cfg.Logger == nil {
		panic("transport: Config.Logger is required")
	}
	preClosed := make(chan struct{})
	close(preClosed)
	c := &Client{
		cfg:              cfg,
		pingInterval:     pingInterval,
		pongTimeout:      pongTimeout,
		reconnectInitial: reconnectInitial,
		reconnectMax:     reconnectMax,
		stabilityReset:   stabilityResetMin,
		closeFrameGrace:  closeFrameGrace,
		rng:              rand.New(rand.NewSource(time.Now().UnixNano())),
		sendCh:           make(chan []byte),
		recvCh:           make(chan []byte),
		closeCh:          make(chan struct{}),
		connectedCh:      make(chan struct{}, 1),
		connDone:         preClosed,
	}
	c.dialFn = c.realDial
	return c
}

// Connect runs the dial-and-serve lifecycle until ctx is cancelled or
// Close is called. It returns ctx.Err() on shutdown or ErrClosed if Close
// was called; it never returns nil. Callers run it in its own goroutine.
//
// Reconnect mechanics:
//
//   - On each failed dial, sleep backoff(attempt) (1s/2s/4s/8s/16s/30s cap,
//     ±20% jitter). Attempt counter increments per attempt.
//   - On each successful dial, serve the conn (pump send/recv, ping every
//     30s). When the conn drops, record uptime; if uptime ≥ 60s reset the
//     attempt counter to 1, otherwise increment.
//   - ctx cancellation breaks out of any sleep, any dial, any pump.
func (c *Client) Connect(ctx context.Context) error {
	attempt := 1
	// warnedUpgrade gates the loud upgrade-rejection WARN to once per
	// outage: set on the first non-101 dial, re-armed on the next
	// successful connect so a fresh outage gets its own first-failure WARN.
	warnedUpgrade := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-c.closeCh:
			return ErrClosed
		default:
		}

		conn, err := c.dialFn(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A fatal close code can surface from Dial directly when the
			// relay closes the conn as part of (or immediately after) the
			// upgrade response — coder/websocket returns the close error
			// from Dial rather than from a subsequent Read. Apply the
			// same check here as the post-serve path so 4409 halts the
			// loop regardless of which path observed it.
			if status := websocket.CloseStatus(err); status != -1 {
				for _, fc := range c.cfg.FatalCloseCodes {
					if status == fc {
						return fmt.Errorf("%w (%d): %w", ErrFatalClose, status, err)
					}
				}
			}
			delay := c.backoff(attempt)
			if errors.Is(err, ErrUpgradeRejected) && !warnedUpgrade {
				warnedUpgrade = true
				c.cfg.Logger.Warn("transport: relay rejected the WebSocket upgrade; "+
					"verify the relay URL path is correct",
					"attempt", attempt, "delay", delay, "err", err)
			} else {
				c.cfg.Logger.Info("transport: dial failed, backing off",
					"attempt", attempt, "delay", delay, "err", err)
			}
			if !c.sleepCancellable(ctx, delay) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return ErrClosed
			}
			attempt++
			continue
		}

		connectedAt := time.Now()
		warnedUpgrade = false
		c.cfg.Logger.Info("transport: connected", "attempt", attempt)
		serveErr := c.serve(ctx, conn)
		uptime := time.Since(connectedAt)

		c.cfg.Logger.Info("transport: disconnected",
			"uptime", uptime, "err", serveErr)
		_ = conn.Close(websocket.StatusInternalError, "client reconnecting")

		if status := websocket.CloseStatus(serveErr); status != -1 {
			for _, fc := range c.cfg.FatalCloseCodes {
				if status == fc {
					return fmt.Errorf("%w (%d): %w", ErrFatalClose, status, serveErr)
				}
			}
		}

		select {
		case <-c.closeCh:
			return ErrClosed
		default:
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if uptime >= c.stabilityReset {
			attempt = 1
		} else {
			attempt++
		}
	}
}

// Send writes a single frame to the relay. Returns ErrNotConnected if no
// live conn at call time, ErrClosed if Close was called, or
// ErrDisconnected if the live conn drops while Send is blocked enqueuing
// the frame. A nil return means the frame was queued for the send pump,
// not that it has hit the wire.
func (c *Client) Send(frame []byte) error {
	select {
	case <-c.closeCh:
		return ErrClosed
	default:
	}
	c.mu.Lock()
	live := c.conn != nil
	c.mu.Unlock()
	if !live {
		return ErrNotConnected
	}
	// Capture connDone AFTER observing live. If a serve teardown lands
	// between live-observation and the select below, sendPump has already
	// exited and would never drain c.sendCh — the connDone close is the
	// only signal that prevents the Send goroutine from wedging forever.
	c.connDoneMu.Lock()
	done := c.connDone
	c.connDoneMu.Unlock()
	select {
	case c.sendCh <- frame:
		return nil
	case <-c.closeCh:
		return ErrClosed
	case <-done:
		return ErrDisconnected
	}
}

// Receive blocks until the next frame arrives, ctx is cancelled, the
// client is closed, or the underlying conn drops. Returns ErrDisconnected
// when the conn drops (or when no conn is currently live); the caller
// observes Connected() to learn when a fresh conn becomes available and
// re-runs any application-layer handshake on it.
func (c *Client) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-c.closeCh:
		return nil, ErrClosed
	default:
	}
	c.connDoneMu.Lock()
	done := c.connDone
	c.connDoneMu.Unlock()
	select {
	case frame := <-c.recvCh:
		return frame, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closeCh:
		return nil, ErrClosed
	case <-done:
		return nil, ErrDisconnected
	}
}

// Connected returns a channel that emits a value on every successful
// underlying conn. Buffer is 1 with drop-on-full semantics: a slow
// observer sees the most recent connect event, not every connect since
// boot. Multiple observers are NOT supported.
func (c *Client) Connected() <-chan struct{} { return c.connectedCh }

// DropConn force-closes the live conn (if any) abruptly (1006-like),
// without sending a close frame. The serve loop sees the closed conn,
// returns to the dial loop, and reconnects via backoff. DropConn does
// NOT halt the dial loop. Idempotent (safe to call when no conn is live).
//
// CloseNow is used in preference to Close(status, reason) so the caller
// is not blocked for up to 10s waiting on a close handshake when the
// only purpose is to recycle the conn. Callers wishing to communicate a
// status to the peer should use Close on the application channel before
// invoking DropConn.
func (c *Client) DropConn() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.CloseNow()
	}
}

// Close shuts the client down. Idempotent.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.closeCh)
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "client closing")
		}
	})
	return nil
}

// --- internals ---

func (c *Client) setConn(conn *websocket.Conn) {
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
}

func (c *Client) realDial(ctx context.Context) (*websocket.Conn, error) {
	opts := &websocket.DialOptions{HTTPHeader: c.cfg.Headers}
	conn, resp, err := websocket.Dial(ctx, c.cfg.URL, opts)
	if err != nil {
		// coder/websocket returns a non-nil resp iff the relay answered the
		// upgrade with a non-101 HTTP status (wrong path, misroute); a
		// network-level failure leaves resp nil. Classify so Connect can be
		// loud about the former and quiet-retry the latter.
		if resp != nil {
			return nil, fmt.Errorf("%w (HTTP %d): %w", ErrUpgradeRejected, resp.StatusCode, err)
		}
		return nil, fmt.Errorf("dial: %w", err)
	}
	conn.SetReadLimit(maxFrameBytes)
	return conn, nil
}

// serve runs the recv-pump, send-pump, and ping-loop under a single
// cancellable child context. Pump goroutines are installed BEFORE the
// live conn is made observable via setConn — see docs/lessons.md:290
// for the "lifecycle goroutine must be scheduled before the conn is
// visible to concurrent callers" pattern this mirrors.
func (c *Client) serve(parent context.Context, conn *websocket.Conn) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	fresh := make(chan struct{})
	c.connDoneMu.Lock()
	c.connDone = fresh
	c.connDoneMu.Unlock()
	defer func() {
		c.connDoneMu.Lock()
		close(fresh)
		c.connDoneMu.Unlock()
	}()
	errCh := make(chan error, 3)
	go func() { errCh <- c.recvPump(ctx, conn) }()
	go func() { errCh <- c.sendPump(ctx, conn) }()
	go func() { errCh <- c.pingLoop(ctx, conn) }()
	c.setConn(conn)
	defer c.setConn(nil)
	select {
	case c.connectedCh <- struct{}{}:
	default:
	}
	errs := awaitCloseStatus(errCh, c.closeFrameGrace)
	cancel()
	for len(errs) < 3 {
		errs = append(errs, <-errCh)
	}
	// Prefer an error with a recognizable WS close status. When the peer
	// closes, recvPump observes a CloseError but sendPump (mid-write) and
	// pingLoop can return generic "use of closed network connection" net
	// errors.
	for _, e := range errs {
		if websocket.CloseStatus(e) != -1 {
			return e
		}
	}
	return errs[0]
}

// awaitCloseStatus reads one error from errCh, then — if that error
// carries no recognizable WS close status — waits up to grace for one
// more arrival before returning. Returns the slice of errors received
// (1 or 2 elements); the caller is responsible for draining any
// remaining slots after cancelling the pump context.
//
// Rationale: coder/websocket's prepareRead.done() defers an override
// that replaces an inbound CloseError with ctx.Err() if ctx fires while
// the Read is suspended. If sendPump or pingLoop returns an error first
// and serve cancels immediately, recvPump's pending Read then surfaces
// the override instead of the peer's actual close frame — no slot of
// errs[] carries the close status and the FatalCloseCodes check in
// Connect misses. Waiting briefly for recvPump's natural return when
// the first arrival has no close status preserves the status in errs[].
// See #290.
func awaitCloseStatus(errCh <-chan error, grace time.Duration) []error {
	errs := make([]error, 0, 3)
	first := <-errCh
	errs = append(errs, first)
	if websocket.CloseStatus(first) != -1 {
		return errs
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case e := <-errCh:
		errs = append(errs, e)
	case <-t.C:
	}
	return errs
}

func (c *Client) recvPump(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}
		select {
		case c.recvCh <- data:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Client) sendPump(ctx context.Context, conn *websocket.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame := <-c.sendCh:
			writeCtx, cancel := context.WithTimeout(ctx, c.cfg.WriteTimeout)
			err := conn.Write(writeCtx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				return fmt.Errorf("send: %w", err)
			}
		}
	}
}

func (c *Client) pingLoop(ctx context.Context, conn *websocket.Conn) error {
	t := time.NewTicker(c.pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, c.pongTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				c.cfg.Logger.Warn("transport: pong timeout", "err", err)
				return fmt.Errorf("ping: %w", err)
			}
		}
	}
}

func (c *Client) sleepCancellable(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	case <-c.closeCh:
		return false
	}
}

// backoff returns the delay to wait before reconnect attempt n (1-indexed),
// applying the wire-spec sequence 1s/2s/4s/8s/16s/30s cap with ±20% jitter.
//
// Cadence (docs/protocol-mobile.md § Reconnect):
//
//	attempt 1 → 1s  ± 20%
//	attempt 2 → 2s  ± 20%
//	attempt 3 → 4s  ± 20%
//	attempt 4 → 8s  ± 20%
//	attempt 5 → 16s ± 20%
//	attempt 6+ → 30s ± 20% (cap)
func (c *Client) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	var base time.Duration
	if attempt >= 6 {
		base = c.reconnectMax
	} else {
		base = c.reconnectInitial << (attempt - 1)
		if base > c.reconnectMax {
			base = c.reconnectMax
		}
	}
	c.rngMu.Lock()
	jitter := 0.8 + 0.4*c.rng.Float64()
	c.rngMu.Unlock()
	return time.Duration(float64(base) * jitter)
}
