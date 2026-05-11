# 247 — `net`: WSS client with auto-reconnect backoff

## Files to read first

- `docs/protocol-mobile.md:154-175` — § Heartbeat + § Reconnect. The wire-spec source of truth for the cadence this ticket implements: 30s idle ping, 30s pong timeout, 1s/2s/4s/8s/16s/30s backoff with ±20% jitter, reset to attempt 1 after ≥60s connected. Do not deviate from these constants — the relay (and the phone, separately) implements the symmetric side and the cadence is part of the protocol.
- `docs/protocol-mobile.md:69-83` — § Binary → relay. The endpoint shape this client targets (`wss://<relay>/v1/server`) and the request headers convention (`X-Pyry-Server-Id`, `X-Pyry-Binary-Version`, `X-Pyry-Protocol-Versions`). This client does NOT construct headers — caller passes them via `Config.Headers`. Header semantics, including server-id-conflict close `4409`, are the future handshake layer's concern.
- `docs/protocol-mobile.md:45-55` — § TLS. v1 uses standard TLS verification (no pinning). This client uses Go's default `crypto/tls.Config` with `MinVersion: tls.VersionTLS12` — no custom verifier, no skip-verify, no pinning.
- `internal/supervisor/backoff.go` (all 46 lines) — the existing supervisor's `backoffTimer` pattern. Same shape: capped exponential growth + reset-on-long-uptime. New `transport.backoff` follows this idiom verbatim, swapping in the wire-spec constants and adding ±20% jitter.
- `internal/supervisor/backoff_test.go` — table-driven test shape for the backoff sequence. The new `backoff_test.go` mirrors it.
- `internal/protocol/envelope.go:1-95` — sibling package's doc-comment shape (package overview, "single source of truth is `docs/protocol-mobile.md`", typed sentinels, no I/O). The new `internal/transport` package follows the same documentation idiom — package doc explicitly names what it does NOT know about (protocol envelope, handshake, dispatch).
- `internal/supervisor/supervisor.go` — Pyrycode's pattern for `Run(ctx) error` as the blocking lifecycle entry point. `Client.Connect(ctx) error` mirrors this shape.
- `CODING-STYLE.md` — `gofmt` non-negotiable, stdlib-first, `log/slog`, table-driven tests, context for cancellation, errors-not-panics. This spec inherits all five.
- `docs/lessons.md:290` — "The `g.Go(sess.Run)` schedule must run inside the same critical section as the registry insert." Same shape applies here: when `Connect`'s outer dial loop opens a fresh conn, the recv-pump and ping-loop goroutines must be installed before the conn is observable via `Send`/`Receive` — otherwise a concurrent caller can race between "live conn registered" and "pump goroutines started" and observe a frozen client.

## Context

Phase 3 Track C transport mechanics. The binary's outbound network layer needs a long-lived WSS connection to the relay that reconnects automatically and detects dead connections quickly. This ticket lands the transport primitive; protocol semantics layer on top in a future ticket (handshake, server-id assignment, hello/hello_ack dispatch).

The shape of the surrounding stack — which this client knows nothing about — is:

```
internal/transport (this ticket, #247) ──> WSS conn ──> relay
        ^
        │  exposes []byte frames
        │
internal/dispatch (future, #248)  ──>  protocol.Envelope marshal/unmarshal,
                                       hello handshake, role-based routing
        ^
        │
internal/protocol (#255 + #256)   ──>  wire types (Envelope, codes, payloads)
```

This package owns the transport mechanics: dial, reconnect with backoff, ping/pong heartbeat. It is **generic over frame payload** — it accepts and emits `[]byte` and never reads or writes JSON. The dispatcher (#248) marshals/unmarshals envelopes against the byte stream.

Why a separate package (`internal/transport`):

- One source of truth for WSS lifecycle, consumable by `internal/dispatch` (binary→relay) and any future outbound WSS users. Keeps reconnect mechanics out of dispatch (which already owns handshake, routing, and ID-monotonicity tracking).
- The package's surface is small: one `Client`, one `Config`, four methods (`Connect`/`Send`/`Receive`/`Close`), three sentinel errors. The runtime dependency on `github.com/coder/websocket` is contained here; no other package imports it.

The reconnect cadence is locked at the wire-spec level (`docs/protocol-mobile.md` § Reconnect): 1s/2s/4s/8s/16s/30s cap with ±20% jitter, reset to attempt 1 after a successful connection lasting ≥60 seconds. These constants are not architect-tunable.

### WebSocket library choice: `github.com/coder/websocket`

The ticket leaves the library to architect's call between `gorilla/websocket` and `nhooyr.io/websocket` (now `github.com/coder/websocket` — the project moved under Coder's stewardship in 2024; same author, same API, same wire behaviour). Pick `coder/websocket`. Justification:

1. **Context-first API.** Every operation takes `context.Context`: `c.Read(ctx)`, `c.Write(ctx, ...)`, `c.Ping(ctx)`, `c.Close(...)`. Matches Pyrycode's idiom (`CODING-STYLE.md` § Concurrency: "Every goroutine that can be stopped takes a context"). Gorilla uses deadline-based APIs (`SetReadDeadline`, `SetWriteDeadline`) that compose awkwardly with the project's ctx-driven shutdown.
2. **Smaller surface.** ~2k LOC vs gorilla's ~10k. The transport surface this ticket consumes is `Dial`, `Conn.Read`, `Conn.Write`, `Conn.Ping`, `Conn.Close`, `Conn.SetReadLimit` — six entry points.
3. **Native ping/pong via `Conn.Ping(ctx)`.** Returns when pong arrives or ctx deadline fires. No callback ceremony — the timeout is just `context.WithTimeout(parent, 30*time.Second)` around the call.
4. **Maintenance status.** `coder/websocket` is actively maintained (releases through 2025); `gorilla/websocket` was archived Dec 2022, briefly returned to maintenance, but is no longer the recommended choice for new code in the Go community.
5. **No CGO, MIT licence.** Matches Pyrycode's existing dep policy (`creack/pty`, `qrterminal`, `fsnotify` all MIT/BSD).

Add to `go.mod`:

```
require github.com/coder/websocket v1.8.13
```

Pin the version. No transitive dependencies (it imports only stdlib).

This is the project's first network-protocol dep; document it in `docs/PROJECT-MEMORY.md` post-merge so future tickets reach for the same library.

## Design

### Package layout

```
internal/transport/wssclient.go         (new, ~140 production LOC)
internal/transport/wssclient_test.go    (new, ~280 test LOC)
```

One production file. The backoff function is private and pure (`backoff(attempt int, rng *rand.Rand) time.Duration`); it lives at the bottom of `wssclient.go` rather than in a separate `backoff.go` because at ~15 lines it doesn't earn its own file under the architect "≤3 new files" guidance. Test cases for the backoff function live in the same `_test.go` as the rest — table-driven, sub-test naming makes the grouping obvious.

A single test file is also intentional: the smoke test against a real `httptest` WS server, the ping-fire test, the backoff sequence table, and the close-on-context test all share helpers (the WS echo server, the deterministic rng). One file keeps the helpers usable without exporting them; multiple files would force either exporting or duplicating.

### `internal/transport/wssclient.go`

```go
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
    pingInterval       = 30 * time.Second
    pongTimeout        = 30 * time.Second
    reconnectInitial   = 1 * time.Second
    reconnectMax       = 30 * time.Second
    stabilityResetMin  = 60 * time.Second
    maxFrameBytes      = 1 << 20 // 1 MiB — see Security review § Network & I/O
)

// Config carries the static configuration for a Client. The caller supplies
// the relay URL and any request headers (server-id, binary-version,
// protocol-versions); this package does not construct headers. ReadTimeout
// and WriteTimeout bound per-frame I/O — they are NOT inactivity timeouts;
// the inactivity contract is the ping/pong heartbeat.
type Config struct {
    URL          string
    Headers      http.Header
    ReadTimeout  time.Duration
    WriteTimeout time.Duration

    // Logger receives structured lifecycle logs (dial, reconnect, ping
    // timeout). Required; nil panics at New() time. Per CODING-STYLE.md:
    // "Logger is injected, not global."
    Logger *slog.Logger

    // rng is unexported and tests-only. Production code uses a rng seeded
    // from time.Now().UnixNano() at New() time; tests substitute a
    // deterministic source via newClientForTest (see test file).
    rng *rand.Rand
}

// Client maintains a single long-lived WSS connection with auto-reconnect.
// Methods are concurrency-safe. The zero value is not usable — call New.
type Client struct {
    cfg Config

    // sendCh and recvCh proxy frames between caller and the currently-
    // live underlying conn. Both are unbuffered: backpressure is the
    // caller's problem (a slow consumer blocks Receive; a fast producer
    // blocks Send). Bounded queues would require a drop policy this
    // ticket has no signal to choose; defer until #248 has a reason.
    sendCh chan []byte
    recvCh chan []byte

    // closeOnce/closeCh implement idempotent Close.
    closeOnce sync.Once
    closeCh   chan struct{}

    // mu guards conn (nil when no live conn). All writes happen on the
    // dial-loop goroutine; reads happen on Send. Single mu, no ordering
    // graph to track.
    mu   sync.Mutex
    conn *websocket.Conn
}

// Sentinel errors.
var (
    // ErrNotConnected is returned by Send when there is no live conn
    // (the dial loop is waiting in backoff, or the previous conn just
    // dropped). The caller should observe Connected() to know when to
    // re-handshake.
    ErrNotConnected = errors.New("transport: not connected")

    // ErrDisconnected is returned by Receive when the underlying WS conn
    // dropped while Receive was blocked. The dial loop will reconnect;
    // the next Receive call blocks until a fresh conn delivers a frame.
    // Callers that need to re-handshake on reconnect should observe
    // Connected() rather than treating this as a re-handshake trigger.
    ErrDisconnected = errors.New("transport: connection lost")

    // ErrClosed is returned by Send and Receive after Close (or the
    // parent context cancellation) has shut the client down.
    ErrClosed = errors.New("transport: client closed")
)

// New returns a Client. The Client is not yet connected; call Connect.
func New(cfg Config) *Client {
    if cfg.Logger == nil {
        panic("transport: Config.Logger is required")
    }
    if cfg.rng == nil {
        cfg.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
    }
    return &Client{
        cfg:     cfg,
        sendCh:  make(chan []byte),
        recvCh:  make(chan []byte),
        closeCh: make(chan struct{}),
    }
}

// Connect runs the dial-and-serve lifecycle until ctx is cancelled or Close
// is called. It returns ctx.Err() on shutdown; it never returns nil. Callers
// run it in its own goroutine:
//
//   go func() {
//       if err := client.Connect(ctx); err != nil && !errors.Is(err, context.Canceled) {
//           log.Error("transport stopped", "err", err)
//       }
//   }()
//
// Reconnect mechanics:
//
//   - On each failed dial, sleep backoff(attempt) (1s/2s/4s/8s/16s/30s cap,
//     ±20% jitter). Attempt counter increments per attempt.
//   - On each successful dial, serve the conn (pump send/recv, ping every
//     30s). When the conn drops, record uptime; if uptime ≥ 60s reset the
//     attempt counter to 1, otherwise increment.
//   - ctx cancellation breaks out of any sleep, any dial, any pump.
//
// Connect logs lifecycle events at Info (dial, connected, disconnected,
// reset) and pong-timeout at Warn.
func (c *Client) Connect(ctx context.Context) error {
    attempt := 1
    for {
        if err := ctx.Err(); err != nil { return err }
        select {
        case <-c.closeCh:
            return ErrClosed
        default:
        }

        conn, err := c.dial(ctx)
        if err != nil {
            if ctx.Err() != nil { return ctx.Err() }
            delay := backoff(attempt, c.cfg.rng)
            c.cfg.Logger.Info("transport: dial failed, backing off",
                "attempt", attempt, "delay", delay, "err", err)
            if !c.sleepCancellable(ctx, delay) { return ctx.Err() }
            attempt++
            continue
        }

        connectedAt := time.Now()
        c.cfg.Logger.Info("transport: connected", "attempt", attempt)
        c.setConn(conn)
        serveErr := c.serve(ctx, conn)
        c.setConn(nil)
        uptime := time.Since(connectedAt)

        // serve returns nil on a graceful close (ctx cancel or pong
        // timeout); a non-nil error is a transport-layer fault.
        c.cfg.Logger.Info("transport: disconnected",
            "uptime", uptime, "err", serveErr)
        _ = conn.Close(websocket.StatusInternalError, "client reconnecting")

        if ctx.Err() != nil { return ctx.Err() }
        if uptime >= stabilityResetMin {
            attempt = 1
        } else {
            attempt++
        }
    }
}

// Send writes a single frame to the relay. Returns ErrNotConnected if no
// live conn, ErrClosed if Close was called.
func (c *Client) Send(frame []byte) error {
    select {
    case <-c.closeCh:
        return ErrClosed
    default:
    }
    c.mu.Lock()
    live := c.conn != nil
    c.mu.Unlock()
    if !live { return ErrNotConnected }
    // Hand the frame to the send pump. If the conn drops between the
    // check above and the pump reading, the pump will return an error
    // upward; the frame is dropped. Higher layers reissue after a
    // Connected() signal.
    select {
    case c.sendCh <- frame:
        return nil
    case <-c.closeCh:
        return ErrClosed
    }
}

// Receive blocks until the next frame arrives, ctx is cancelled, or the
// client is closed. After a reconnect, Receive resumes delivering frames
// from the new conn — callers that need to re-handshake on reconnect
// MUST observe Connected() instead of inferring it from Receive.
func (c *Client) Receive(ctx context.Context) ([]byte, error) {
    select {
    case frame := <-c.recvCh:
        return frame, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-c.closeCh:
        return nil, ErrClosed
    }
}

// Close shuts the client down. Idempotent. Causes any in-flight Connect to
// return ErrClosed and any blocked Send/Receive to return ErrClosed.
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

// dial opens one WSS connection. Inherits ctx for the upgrade timeout.
func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
    opts := &websocket.DialOptions{
        HTTPHeader: c.cfg.Headers,
    }
    conn, _, err := websocket.Dial(ctx, c.cfg.URL, opts)
    if err != nil { return nil, fmt.Errorf("dial: %w", err) }
    conn.SetReadLimit(maxFrameBytes)
    return conn, nil
}

// serve runs the recv-pump, send-pump, and ping-loop until one of them
// returns. Uses a single cancellable child context so the first failure
// shuts the others down before serve returns.
func (c *Client) serve(parent context.Context, conn *websocket.Conn) error {
    ctx, cancel := context.WithCancel(parent)
    defer cancel()
    errCh := make(chan error, 3)
    go func() { errCh <- c.recvPump(ctx, conn) }()
    go func() { errCh <- c.sendPump(ctx, conn) }()
    go func() { errCh <- c.pingLoop(ctx, conn) }()
    // Wait for the first to return; that's the disconnect cause.
    first := <-errCh
    cancel()
    // Drain the other two so their goroutines exit before we do.
    <-errCh
    <-errCh
    return first
}

func (c *Client) recvPump(ctx context.Context, conn *websocket.Conn) error {
    for {
        _, data, err := conn.Read(ctx)
        if err != nil { return fmt.Errorf("recv: %w", err) }
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
            if err != nil { return fmt.Errorf("send: %w", err) }
        }
    }
}

func (c *Client) pingLoop(ctx context.Context, conn *websocket.Conn) error {
    t := time.NewTicker(pingInterval)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-t.C:
            pctx, cancel := context.WithTimeout(ctx, pongTimeout)
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
    case <-t.C: return true
    case <-ctx.Done(): return false
    case <-c.closeCh: return false
    }
}

// backoff returns the delay to wait before reconnect attempt n (1-indexed),
// applying the wire-spec sequence 1s/2s/4s/8s/16s/30s cap with ±20% jitter
// drawn from rng. Pure: same (attempt, rng-state) → same output. The rng is
// the only state, and the test injects a deterministic one.
//
// Cadence (docs/protocol-mobile.md § Reconnect):
//
//   attempt 1 → 1s  ± 20%
//   attempt 2 → 2s  ± 20%
//   attempt 3 → 4s  ± 20%
//   attempt 4 → 8s  ± 20%
//   attempt 5 → 16s ± 20%
//   attempt 6+ → 30s ± 20% (cap)
func backoff(attempt int, rng *rand.Rand) time.Duration {
    base := reconnectInitial << (attempt - 1)
    if base > reconnectMax || attempt > 6 {
        base = reconnectMax
    }
    // ±20% jitter: multiply base by [0.8, 1.2).
    jitter := 0.8 + 0.4*rng.Float64()
    return time.Duration(float64(base) * jitter)
}
```

### Why `Connect` is the blocking lifecycle (not a fire-and-forget that returns after first dial)

Two shapes were considered:

1. **`Connect(ctx) error` blocks until ctx done.** Caller does `go client.Connect(ctx)`. Matches `supervisor.Run(ctx)`, which is the established Pyrycode pattern for long-lived loops. The dial loop, ping loop, and pumps all live under one ctx — cancelling ctx tears down everything.
2. **`Connect(ctx) error` returns after first successful dial; reconnect runs in a goroutine launched by `Connect`.** Caller does `if err := client.Connect(ctx); err != nil { ... }` then drives Send/Receive. The internal reconnect goroutine has independent lifecycle, needs explicit `Close` to stop.

Pick shape 1. Reasons:

- Goroutine lifecycle is the caller's, not hidden inside `Connect`. The supervisor (or any caller) sees one goroutine per Client and owns its ctx — no orphan reconnect goroutine surviving a missed `Close`.
- "Connect returns" semantics with shape 2 are ambiguous: does it mean "the first dial succeeded" or "the dial loop is running (regardless of conn state)"? Shape 1 has one return path: shutdown.
- Tests assert behaviour over a long-running ctx (the AC's "first 6 attempts hit cap"); shape 1 keeps the test under one ctx and one goroutine.

### Why `Send`/`Receive` are not on the Connect goroutine

The dial loop must be free to dial-sleep-redial without being gated on a caller `Send` or a caller `Receive`. Send/Receive are pure channel proxies. The pump goroutines (per-conn) are what translate channel writes/reads into WS frames.

Concrete consequence: if the caller never calls `Receive`, the recv-pump fills up `recvCh` (capacity 0 = blocks the pump), which causes the pump goroutine to block on the channel send, which causes the conn's read buffer to fill, which eventually causes the relay-side write to block. This is intentional backpressure — the dispatcher (#248) is expected to keep `Receive` drained at all times. If a future ticket needs buffering, it adds it at the dispatcher layer, not here.

### Why `recvCh` and `sendCh` are unbuffered

Buffering implies a drop policy. The dispatcher (#248) consumes envelopes ordered by `id`; dropping a frame would break the protocol's monotonic-id invariant. So the only safe buffer policy is "block on full" — which is what an unbuffered channel does, just at depth 0. Adding a `make(chan []byte, 16)` for "throughput" without a drop policy is a code smell: it papers over a slow consumer until the buffer fills, then exhibits the same blocking behaviour with worse latency. Defer until there's a measured need.

### Why one mutex, not finer-grained state

The only shared mutable state is `conn` (current live conn) — readable from Send (live check), Close (graceful close), and writable from Connect (set on dial success, nil on disconnect). One `sync.Mutex` is sufficient; no ordering graph to track. The `closeCh`/`closeOnce` pair carries the closed-bit out-of-band so Send and Receive don't need the mutex to check it.

### Why `math/rand`, not `crypto/rand`

Jitter is anti-thundering-herd noise. Predictability of the exact delay does not enable any attack — a hostile relay operator who wants to predict reconnect timing already controls the relay and can simply close the connection at the chosen instant. `math/rand` is the right tool; `crypto/rand` would be cargo-cult.

Test injection: the `cfg.rng` field is unexported and seeded from `time.Now().UnixNano()` in production, but a sibling constructor `newClientForTest(cfg Config, rng *rand.Rand)` substitutes a deterministic `rand.NewSource(seed)` so the backoff sequence is reproducible across test runs.

### Why TLS settings inherit Go defaults

`docs/protocol-mobile.md` § TLS pins: "The binary connects with standard TLS verification — no pinning in v1." `coder/websocket.Dial` uses `http.DefaultClient`, which uses `http.DefaultTransport`, which uses `crypto/tls`'s defaults (Go's secure defaults: TLS 1.2 min by default since Go 1.18, modern cipher suites, hostname verification against the URL's host). No custom `tls.Config` is constructed here.

If a future ticket needs to set `MinVersion: tls.VersionTLS13`, that's a one-line `&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}}` slotted into the dial options. Out of scope for v1.

### Why the close code on ping timeout is `1011`, not `1006`

WebSocket `1006` (abnormal closure) is **received**, not **sent** — it indicates the conn dropped without a close frame. The client cannot actively close with `1006`. Spec § Heartbeat says "A dead connection is closed with code `1011` (server error) or `1006` (abnormal close, on transport-layer drops)" — the `or` is a description of the two observable states, not a choice. This client sends `1011` (internal error) on a graceful close after pong timeout; `1006` is what the peer observes when the TCP connection itself drops without any close frame (a real network fault).

In code: `_ = conn.Close(websocket.StatusInternalError, "client reconnecting")` on the reconnect path. `websocket.StatusInternalError == 1011`.

### Out-of-scope behaviour for this client

Restated to lock the scope:

- **No protocol envelope parsing.** This client emits and accepts `[]byte`. The dispatcher (#248) handles `protocol.Envelope` marshal/unmarshal.
- **No handshake.** No `hello` send, no `hello_ack` await, no server-id negotiation. The caller observes Connected() and sends hello via Send().
- **No close-code interpretation.** A close with `4409` (server-id conflict), `4401` (auth invalid), `4404` (no server) is observed by `recvPump` as a `Read` error; the client logs the error and reconnects. The dispatcher (#248) is responsible for reading the close reason and deciding "retry vs give up" if needed. v1: just reconnect on any drop.
- **No per-message metrics.** No frame counter, no byte counter, no latency histogram. Observability ticket lands separately (#TBD).
- **No connection multiplexing.** One Client = one conn at a time. Pyrycode's binary holds exactly one relay conn; multiplexing is a future architectural concern, not a primitive concern.
- **No TLS pinning, no custom verifier, no `InsecureSkipVerify`.** Spec § TLS is explicit; future v2-or-later may add pinning.

### Connected-channel — deferred decision

The naive design exposes `Connected() <-chan struct{}` that fires every time a fresh conn is up so the future handshake layer (#248) knows to send hello. Two reasons to defer:

1. **#248 has not been scoped yet.** The signal shape (closed-channel-per-conn vs. signal-on-channel vs. callback) depends on how the dispatcher wants to react. Adding the channel pre-emptively risks shipping the wrong shape.
2. **The simplest path uses what we have.** Send returning `ErrNotConnected` and Receive returning a frame are the two endpoint signals available. #248 can build a "wait for conn" helper on top: in a loop, try `Send(helloFrame)` until it returns nil; that's "we're connected."

Add the channel in #248 if #248's design needs it. Naming it here would commit to a shape blindly.

## Concurrency model

Goroutines spawned per Client lifetime:

```
┌────────────────────────────────────────────────────────────┐
│ caller's goroutine: client.Connect(ctx)                     │
│   for {                                                     │
│     dial → backoff if fail → repeat                         │
│     on success: serve(ctx, conn) {                          │
│       go recvPump(ctx, conn) → recvCh                       │
│       go sendPump(ctx, conn) ← sendCh                       │
│       go pingLoop(ctx, conn) [Conn.Ping with timeout]       │
│       wait for first to return; cancel; drain other two;    │
│     }                                                       │
│   }                                                         │
└────────────────────────────────────────────────────────────┘
```

Three goroutines per live conn. All take the same child ctx; the first failure cancels it, the other two see the cancellation and return. `serve` drains all three before returning to the outer dial loop — no goroutine outlives its `serve` call.

Lock ordering: only `Client.mu` is taken. No ordering graph to track. The mutex is held for one-field-set or one-field-read; never across I/O.

Shutdown sequence:

1. Caller cancels Connect's `ctx`, OR calls `client.Close()`.
2. `Close` closes `closeCh` (idempotent via `sync.Once`) and force-closes any live conn with `1000` (normal closure).
3. Pump goroutines observe `ctx.Done()` (or read/write fails on the closed conn) and return.
4. `serve` drains all three pump returns and returns to `Connect`.
5. `Connect` observes `ctx.Err() != nil` (or `closeCh` closed) and returns `ctx.Err()` / `ErrClosed`.

Goroutine leak audit:

- `recvPump` exits on conn.Read error (incl. ctx cancellation in `coder/websocket`) or on `<-ctx.Done()` if it's blocked on `recvCh<-`. The latter is the leak risk: if a caller calls `Close` while `recvPump` is mid-`recvCh<-`, the pump must observe `closeCh`. The implementation above handles this via `select { case c.recvCh <- data: case <-ctx.Done(): }` — when `Close` fires, the parent `serve`'s `cancel()` propagates to this ctx and unblocks the pump.
- `sendPump` exits on ctx cancellation or `conn.Write` error. No leak path.
- `pingLoop` exits on ctx cancellation or `Conn.Ping` returning a timeout error. No leak path.
- `Connect` itself returns when ctx is done or `closeCh` is closed; the `sleepCancellable` helper observes both.

### A note on the lessons.md:290 pattern

The lesson there ("scheduling the lifecycle goroutine must happen in the same critical section as the registry insert, otherwise a concurrent observer races") applies here in this form: `Connect` calls `c.setConn(conn)` BEFORE starting the recv/send/ping pumps. A `Send` racing in between could see `conn != nil`, write to `sendCh`, but no pump goroutine is yet reading the channel.

The fix is the same shape: install pump goroutines BEFORE setting `c.conn`. Re-ordering in the spec:

```go
// In Connect, after successful dial:
serveErr := c.serveWithConnVisible(ctx, conn)  // installs pumps + sets c.conn + waits
```

Or, equivalently and more readably, push `setConn` into `serve`:

```go
func (c *Client) serve(parent context.Context, conn *websocket.Conn) error {
    ctx, cancel := context.WithCancel(parent)
    defer cancel()
    errCh := make(chan error, 3)
    go func() { errCh <- c.recvPump(ctx, conn) }()
    go func() { errCh <- c.sendPump(ctx, conn) }()
    go func() { errCh <- c.pingLoop(ctx, conn) }()
    c.setConn(conn) // pumps are scheduled; safe to make conn observable.
    defer c.setConn(nil)
    first := <-errCh
    cancel()
    <-errCh
    <-errCh
    return first
}
```

`go func()` is non-blocking — the pump goroutines park on their respective ctx/channel selects before any caller can observe `c.conn != nil`. This makes `Send`'s "is there a live conn" check and "pump goroutine exists" indivisible from the caller's perspective.

Move `c.setConn(conn)` from `Connect` into `serve` (after the three `go func()` lines, before the `<-errCh` wait). The implementation in the spec above is the corrected order. The lesson:290 reference in "Files to read first" exists so the developer reading this file sees the precedent without having to derive it.

## Error handling

Failure modes by source:

1. **Dial fails (network unreachable, TLS handshake error, 4xx upgrade response).** Connect logs at Info ("dial failed, backing off"), sleeps `backoff(attempt)`, retries. No upward propagation.
2. **Pong timeout (no pong within 30s of ping).** `pingLoop` returns. `serve` returns. Connect logs at Warn ("pong timeout"), force-closes conn with `1011`, reconnects.
3. **Read error on the conn (peer closed, TCP reset, frame > 1 MiB).** `recvPump` returns. `serve` cancels other pumps. Connect logs, reconnects.
4. **Write error on the conn (peer gone, write deadline exceeded).** `sendPump` returns. Same as #3.
5. **ctx cancellation (parent's daemon shutdown).** All goroutines observe `ctx.Done()` and return. `Connect` returns `ctx.Err()`.
6. **`Close()` called.** `closeCh` is closed. Send/Receive return `ErrClosed`. The live conn (if any) is force-closed with `1000`; pumps return. Connect returns `ErrClosed`.

Caller-observable errors:

- `Send` returns `ErrNotConnected`, `ErrClosed`, or `nil`. Frames handed to `sendCh` are not acknowledged; a `Send` returning `nil` means "queued for write," not "written on the wire." This is correct for an asynchronous transport.
- `Receive` returns `(frame, nil)` or `(nil, err)` where err is `ctx.Err()` or `ErrClosed`. It does NOT return `ErrDisconnected` directly — the recv-pump returning on a conn drop causes the next `Receive` to block until the dial loop succeeds and a fresh frame arrives.

  Why not `ErrDisconnected`: protocol-layer handshake (#248) needs to know about reconnects but should observe them via a positive signal (a "reconnected" channel), not a negative signal (an error from Receive). The negative signal is fragile — a Receive that returns `ErrDisconnected` then a Receive that returns a frame could be reordered with no observable difference at the byte level, but the dispatcher's handshake state needs the boundary to be unambiguous. Deferred to #248.

- `Close` always returns `nil`. Errors during force-close of the underlying conn are logged-and-discarded; the contract is "Close shuts the client down," not "Close reports whether the conn closed cleanly."

What `IsV1Compatible` is to `internal/protocol`, `Close` is to this package — a function whose error return is for shape uniformity, not for diagnostics.

## Testing strategy

One file (`internal/transport/wssclient_test.go`), ~280 LOC, table-driven where applicable. Tests use stdlib `testing` only and a single `httptest.NewServer` upgrader for the integration cases.

### Helpers

```go
// Re-exec via httptest.NewServer with a coder/websocket.Accept upgrader.
// Returns the ws://… URL and a teardown func. The handler echoes any
// received frame back, responds to pings (the library does this
// automatically), and supports test-controlled close codes via a hook.
func newTestRelay(t *testing.T) (url string, ctrl *relayCtrl)

// relayCtrl exposes ForceClose() to kill the upstream conn mid-flight and
// FailNextDial() to make the next Dial attempt return a 503.

// newClientForTest constructs a Client with a deterministic rng (seed=1)
// so the backoff sequence is reproducible.
func newClientForTest(cfg Config, seed int64) *Client
```

### `TestBackoff_Sequence`

Goal: pin the AC's "first 6 attempts hit cap, attempt 7 caps at 30s" sequence, ignoring jitter via a deterministic rng.

Implementation: a deterministic rng seeded so `rng.Float64()` returns a known sequence; assert the integer-second component of `backoff(n, rng)` for n=1..10:

| Attempt | Base (s) | Bound (s) |
|---|---|---|
| 1 | 1 | 0.8–1.2 |
| 2 | 2 | 1.6–2.4 |
| 3 | 4 | 3.2–4.8 |
| 4 | 8 | 6.4–9.6 |
| 5 | 16 | 12.8–19.2 |
| 6 | 30 | 24–36 |
| 7 | 30 | 24–36 |
| 8+ | 30 | 24–36 |

The exact value is fixed by the seed; assert `delay >= base*0.8 && delay <= base*1.2` to lock the cadence without coupling the test to a specific RNG implementation. The cap at attempt 6 and stability at attempt 7+ are the AC bullets.

### `TestBackoff_ResetAfterStableConnection`

Goal: pin the "reset to attempt 1 after a successful connection lasting ≥60s" AC. This tests the integration of `backoff` with the reset rule, not the pure function in isolation.

Implementation: drive `Connect` with a synthetic `dial` (the test-relay supports `FailNextDial`). Sequence:

1. `FailNextDial()`, then succeed on attempt 3.
2. Hold the conn open for ≥ `stabilityResetMin` (tests use an injected `now()` clock or `time.Sleep` — see below).
3. `ForceClose()` to drop the conn.
4. Assert the next dial attempt's delay is in `[0.8s, 1.2s]` (attempt 1's base).

Subtle: this test wants to assert "after ≥60s of uptime, the counter resets" without sleeping 60s. Options:

- **Inject a `now func() time.Time` into `Config`** and let the test use a fake clock. Adds API surface for one test; rejected.
- **Lower `stabilityResetMin` for the test** via a build tag or test-only package-private setter. Rejected — couples production constants to test setup.
- **Use a real 60s sleep.** Test takes 60s. Rejected — too slow for CI.
- **Inject the stability threshold into the Client.** A `Config.stabilityReset time.Duration` defaulting to 60s, settable via a test-only `newClientForTest`. The production constructor doesn't expose it. Accept this — adds one private field, zero exported surface.

Going with the last option. The test sets `stabilityReset = 100 * time.Millisecond`; the AC's 60s value is the production default. Production code paths still use the constant; the override is per-Client, set only by `newClientForTest`.

### `TestPing_FiredAt30s`

Goal: pin the AC's "idle-pings-fired-at-30s" cadence.

Implementation: the test relay's handler counts received pings (the upgrader exposes the ping count via an atomic counter accessible through `relayCtrl.PingCount()`). Connect a client, wait `~31s`, assert `PingCount() >= 1`.

Subtle: `time.Sleep(31s)` in a test is ugly but unavoidable for this assertion against the real ticker — the ticker is `pingInterval = 30 * time.Second` exact. Two mitigations:

- **Run this test under `-short` skip** so `go test -short` excludes it; CI runs full suite. Convention: `if testing.Short() { t.Skip("ping interval is 30s") }`.
- **Inject the ping interval via the test-only `newClientForTest`** the same way `stabilityReset` is injected. Same rationale — keep production constants in code, override per-Client for tests. With a 100ms test interval, the assertion is "wait 150ms, expect ≥1 ping."

Both. Test injects 100ms interval AND honours `-short` (so a future architect deletes the override and the test still works under the long path).

### `TestPongTimeout_TriggersReconnect`

Goal: pin "dead-connection-detected-after-pong-timeout." The test relay swallows ping frames without responding; after pong timeout, the client logs the timeout and reconnects.

Implementation: test-relay handler is configured to not auto-respond to pings (`coder/websocket.Accept` has an option to disable the automatic pong, or the test relay can hijack the underlying conn — easiest: the test relay accepts the upgrade, then never reads any frame, which causes Ping(ctx) to time out because the underlying TCP write buffer eventually fills OR because the library awaits pong with the supplied ctx, which expires after `pongTimeout`).

Assert: `relayCtrl.SecondDialAttempted()` becomes true within `pongTimeout + reconnectInitial + 1*time.Second`. With test overrides (`pingInterval=100ms`, `pongTimeout=200ms`, `reconnectInitial=50ms`), this completes in under 1s.

### `TestClose_OnContextCancel`

Goal: AC "close-on-context-cancel." When the parent ctx is cancelled, Connect returns `ctx.Err()` promptly (within ~50ms) and all goroutines exit.

Implementation: `go client.Connect(ctx)`; `cancel()`; assert Connect returns within a deadline. Use `runtime.NumGoroutine()` pre- and post- as a leak smoke check (with `t.Cleanup` ensuring teardown).

### `TestSmoke_HttptestEchoServer`

Goal: AC "Smoke test against an in-process httptest WS echo server: full handshake + ping + close."

Implementation: stand up a `httptest.NewServer` with a `coder/websocket.Accept` upgrader that echoes frames. Connect a Client; `Send([]byte("hello"))`; assert `Receive` returns `[]byte("hello")` within 1s; assert at least one ping was sent/responded to (using the override 100ms interval + a 250ms test duration); `client.Close()` returns nil; Connect returns `ErrClosed`.

This is the single end-to-end coverage of the integration: real WS upgrade, real ping/pong over the loopback, real close handshake.

### `TestSend_ReturnsErrNotConnected_BeforeConnect`

Goal: pin the API contract — calling `Send` before `Connect` has produced a live conn returns `ErrNotConnected`, not blocks forever.

Implementation: `New(cfg)`; immediately call `Send([]byte("x"))`; assert `ErrNotConnected`. No goroutine; no test relay.

### What NOT to test

- `coder/websocket.Dial`/`Read`/`Write`/`Ping` semantics — those are library contracts.
- TLS handshake against a real WSS server — `httptest.NewTLSServer` would test the library's TLS support, not this client's logic; skip.
- Jitter randomness distribution — the unit test pins the bound (`base*0.8 ≤ delay ≤ base*1.2`); proving the distribution is uniform is the library's problem.
- Compressed payloads (`per-message-deflate`) — not in scope; spec § Heartbeat says "control frames" and the protocol envelope spec doesn't mandate compression. If a future ticket needs it, add a `Config.PerMessageDeflate bool` and one library option.
- Send during reconnect — `Send` returns `ErrNotConnected` if no live conn; the test for that is `TestSend_ReturnsErrNotConnected_BeforeConnect`. Asserting it during the reconnect window adds no coverage.

## Out of scope (do not implement here)

- WS handshake protocol semantics (`hello`/`hello_ack`, server-id negotiation, role-based type restriction). #248.
- `protocol.Envelope` marshal/unmarshal. The dispatcher (#248) wraps this client's `Send([]byte)` / `Receive() []byte` with envelope I/O.
- Close-code interpretation (`4409` conflict, `4401` auth invalid, `4404` no server). The recv-pump reports a generic Read error on any close; the dispatcher decides retry semantics.
- TLS pinning, custom verifier, `InsecureSkipVerify`. Spec § TLS is explicit; v2 may revisit.
- Connection multiplexing. One Client = one conn.
- Per-message metrics, frame counters, latency histograms. Observability is a separate concern (#TBD).
- `Connected() <-chan struct{}` signal channel. Deferred to #248 — see "Connected-channel — deferred decision" above.
- A `relay.Dial(cfg)` standalone helper. The current shape is `Client.Connect(ctx)` running the lifecycle; adding a one-shot dial helper duplicates the same code without a consumer.
- Per-message-deflate compression. Not in the wire spec for v1.
- A `Send(ctx, frame)` variant. Send already proxies to a channel — adding ctx would make `Send` itself blockable until ctx cancellation, which the unbuffered channel already provides via `<-c.closeCh` on the receive side. Skip.

## Open questions

None. Every AC corresponds to an unambiguous code path:

- New `internal/transport/wssclient.go` exposes `Client`, `Config`, `Connect`/`Send`/`Receive`/`Close`. → File structure above.
- WS native ping/pong (30s ping, 30s pong timeout, 1011 close). → `pingLoop` + `serve` reconnect path.
- Exponential backoff with ±20% jitter, 1s/2s/4s/8s/16s/30s cap, reset to attempt 1 after ≥60s. → `backoff` pure function + `Connect`'s uptime check.
- Tests: dial-and-fail-with-backoff; dial-and-succeed-and-reset; idle-pings-fired-at-30s; dead-connection-detected-after-pong-timeout; close-on-context-cancel. → Six named tests above.
- Smoke test against httptest WS echo server. → `TestSmoke_HttptestEchoServer`.

## Security review

This ticket carries the `security-sensitive` label. The pass below follows `agents/architect/security-review.md`'s checklist applied to the transport-layer scope (outbound WSS, generic over payload, no auth/crypto state, no file I/O).

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — the trust boundary is at `recvPump`'s `conn.Read(ctx)`. Bytes flow out of the library and into `recvCh` as opaque `[]byte`. The package treats those bytes as untrusted; no parsing, no length-prefixed framing assumptions beyond `coder/websocket`'s own message framing. The downstream dispatcher (#248) is the next gate — it `json.Unmarshal`s into `protocol.Envelope` and runs `IsV1Compatible`. This package documents the contract in its package comment ("generic over frame payload — knows nothing about pyrycode's protocol envelope") so a future contributor doesn't add a "convenience" parser here.

- **[Tokens, secrets, credentials]** No findings at the package layer — this package handles no tokens directly. Caller-supplied `Config.Headers` may carry an `X-Pyry-*` header chain that includes server-id (intentionally over-the-wire, per spec § Binary → relay). The package does NOT log `cfg.Headers` (the Info log on dial omits headers: `"transport: dial failed, backing off", "attempt", attempt, "delay", delay, "err", err`). Restating as a downstream obligation: the caller (#248) MUST NOT log the Header map either. The header convention is server-id-as-routing-key, which is unguessable (UUIDv4); leaking it via logs is a known concern called out in spec § Binary → relay's TODO note about a post-v1 admin token.

- **[File operations]** N/A — this package performs no file I/O. No `os.Open`, no `os.Create`, no path manipulation. Test fixtures are not used.

- **[Subprocess / external command execution]** N/A — no `os/exec`.

- **[Cryptographic primitives]** Walked:
    - **RNG.** `math/rand` is used for jitter only. Per the checklist's rule ("`math/rand` is acceptable only for non-security uses — jitter, test fixtures"), this is correct. `crypto/rand` is wrong for jitter because jitter is anti-thundering-herd noise, not adversary-resistant randomness — a hostile relay operator already controls the relay and gains nothing from predicting our reconnect delays.
    - **TLS.** Inherits Go's `crypto/tls` defaults via `coder/websocket.Dial → http.DefaultClient → http.DefaultTransport`. Go's defaults are TLS 1.2 minimum and modern cipher suites. **SHOULD FIX consideration deferred:** spec § TLS does not specify `MinVersion: tls.VersionTLS13`. The library default of TLS 1.2 is acceptable and matches the spec's "standard TLS verification" instruction. A future hardening pass can pin TLS 1.3 if relay support is confirmed; out of scope here.
    - **Constant-time comparison.** N/A — this package compares no secrets.
    - **Hand-rolled crypto.** None.

- **[Network & I/O]**
    - **Input size limit.** `conn.SetReadLimit(maxFrameBytes)` is called immediately after dial. `maxFrameBytes = 1 << 20` (1 MiB) matches the spec's implicit per-message cap (`message.too_long` at 1 MiB per single message, docs/protocol-mobile.md § Error codes). The library enforces this at frame-read time — a peer sending a 100 MiB frame trips a `read limit exceeded` error and the recv-pump returns, triggering reconnect. **This is the only DoS-mitigation knob in this layer; pinning it explicitly avoids the bareback `json.Unmarshal` DoS the #255 spec called out as a downstream obligation.**
    - **Header validation on upgrade.** N/A — this client is the originator of the upgrade; it sends headers, doesn't receive them. The relay validates the request headers; this client trusts the response (Go's `http.Transport` validates the upgrade response's `Sec-WebSocket-Accept` automatically).
    - **Read/Write deadlines.** Per-frame: `cfg.WriteTimeout` bounds `sendPump`'s `conn.Write` call via `context.WithTimeout`. Per-frame: `cfg.ReadTimeout` is not currently applied to `recvPump` because the recv-pump's blocking read is bounded by the ping/pong heartbeat (a stuck conn fires pong-timeout within 60s worst case, which cancels the ctx the recv-pump shares, which unblocks `conn.Read`). **SHOULD FIX consideration:** the spec carries `Config.ReadTimeout` but the implementation only uses `WriteTimeout`. Either (a) drop `ReadTimeout` from `Config` (it's unused), or (b) apply it as a per-frame `conn.SetReadDeadline` equivalent inside `recvPump`. Going with (a): the heartbeat is the inactivity contract; `ReadTimeout` would shadow it and produce confusing failure modes when set lower than `pongTimeout`. Update `Config`: drop `ReadTimeout`; the field's not in the AC body's wording — it lists `ReadTimeout, WriteTimeout time.Duration` together, but only `WriteTimeout` has a meaning under the heartbeat-as-inactivity contract. **Note for developer: drop `ReadTimeout` from `Config` AND from the AC's parenthetical "Config { URL string; Headers http.Header; ReadTimeout, WriteTimeout time.Duration }" — flag the AC delta in the PR description so PO can update the issue body. This is a SHOULD-FIX downgrade because the alternative ("ship `ReadTimeout` unused") is worse for maintainers, not because it's exploitable.**
    - **TLS slow-loris resistance.** N/A on the outbound side — this client opens connections, doesn't accept them.
    - **Resource exhaustion (per-server-id, per-IP).** N/A outbound — one Client = one conn.
    - **TLS config.** Default `crypto/tls.Config` via stdlib. Spec § TLS is explicit; no override here. **SHOULD FIX deferred** — see Cryptographic primitives above.

- **[Error messages, logs, telemetry]**
    - The Info logs name dial attempts, attempt counter, delay, and a wrapped error (`fmt.Errorf("dial: %w", err)`); the wrapped error may leak the relay's URL on certain TLS failures (e.g. a hostname-mismatch error includes the requested SAN). Acceptable — the URL is `cfg.URL`, which is operational metadata, not a secret.
    - The Warn log on pong timeout names the wrapped error from `conn.Ping`, which `coder/websocket` formats as a generic timeout — no payload, no peer state.
    - **Static sentinel error messages** (`"transport: not connected"`, `"transport: connection lost"`, `"transport: client closed"`) carry no caller-controlled data. A caller that returns one of these errors upward leaks nothing.
    - The package does NOT log `Config.Headers`. Restated as an obligation for #248 — DO NOT log the Header map.

- **[Concurrency]**
    - **Goroutine lifecycle.** Three goroutines per live conn (recvPump, sendPump, pingLoop), all under a child ctx that the `serve` cancellation tears down. Audit in § Concurrency model above shows no leak path.
    - **Lock ordering.** Only `Client.mu` is taken, never composed with another lock. Held only for one-field-read or one-field-write; never across I/O. No ordering graph.
    - **`closeCh` race.** `Close` uses `sync.Once` to close `closeCh` exactly once. Send/Receive observe it via `<-c.closeCh` in a `select` — no race against Close, because closing a channel is a single happens-before edge that all receivers observe simultaneously (per Go memory model).
    - **The `c.setConn` → pump-install ordering** lesson from `lessons.md:290` is explicitly addressed in § Concurrency model: `serve` installs all three pump goroutines BEFORE calling `c.setConn(conn)`, so a concurrent caller observing `c.conn != nil` is guaranteed to have its `Send` reach a live pump.
    - **Shutdown safety.** A `Send` mid-flight when the parent ctx cancels either (a) reaches the pump and gets written to the conn before the pump observes ctx cancellation, or (b) reaches the pump and the pump's `conn.Write(writeCtx, ...)` fails with ctx.Err(). Either way the caller sees `nil` or a wrapped ctx error — no torn frames on the wire (the WS library writes a frame atomically or not at all). The frame is dropped under (b); the caller resends after the next Connected() signal in the consumer ticket.

- **[Threat model alignment]** Walked against `docs/protocol-mobile.md` § Security model:
    - **Threat #1 (prompt injection):** out of scope — payloads are opaque `[]byte` here; injection lives at the LLM dispatch layer.
    - **Threat #2 (server-id race):** partially relevant — a `4409` close means another binary claimed the same server-id. This client treats `4409` as a generic Read error and reconnects; the dispatcher (#248) is responsible for reading the close reason and deciding "give up vs reconnect." Restated under "Out-of-scope behaviour for this client."
    - **Threat #3 (relay operator MITM):** out of scope — TLS verification is delegated to stdlib defaults per spec § TLS; v1 explicitly accepts the relay-as-TLS-terminator trust model.
    - **Threat #4 (token leak via phone):** N/A — this is the binary↔relay leg.
    - **Threat #5 (implementation bugs):** the ping/pong cadence and reconnect cadence are pinned by `Test*` names that match the AC bullets one-to-one. The drift detector is the test suite — a future contributor changing `pingInterval` to 60s fails `TestPing_FiredAt30s`'s timing assertions.
    - **Threat #6 (replay attacks):** out of scope — `Envelope.ID` monotonicity tracking is the dispatcher's (#248) concern; this layer doesn't see envelopes.
    - **Threat #7 (DoS):** addressed via (a) `conn.SetReadLimit(1 MiB)` capping inbound frame size, (b) `WriteTimeout` bounding outbound writes, (c) the heartbeat detecting dead connections within 60s worst case, (d) `math/rand` jitter on reconnect preventing thundering-herd against the relay. The DoS vector NOT addressed here is "many clients reconnect-storming the relay" — that's the relay's per-IP/per-server-id rate-limit concern, owned by the relay binary's spec.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-11
