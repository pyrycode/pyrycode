# 248 — `net`: outbound dial with server-id handshake

## Files to read first

- `docs/protocol-mobile.md:67-83` — § Authentication / Binary → relay. Header set (`x-pyrycode-server`, `x-pyrycode-version`, `user-agent`), first-claim-wins semantics, close code `4409` on conflict, the 30s grace window. The spec headers and the fatal-close behaviour both derive from this section verbatim.
- `docs/protocol-mobile.md:124-141` — § Connection lifecycle / Binary. The four-step handshake: open → send `hello` → await `hello_ack` ≤ 5s → hold open. The 5-second deadline is a wire-spec constant, not architect-tunable.
- `docs/protocol-mobile.md:155-175` — § Heartbeat + § Reconnect. The reconnect cadence (1/2/4/8/16/30s ± 20%, reset on ≥60s up) lives in `internal/transport`; this ticket inherits it untouched. Close codes `1011`/`1006` are observed by the transport's recv pump as a `Read` error; on each new conn, this ticket re-runs `hello`.
- `docs/protocol-mobile.md:205-250` — § Message types / `hello` + `hello_ack`. Wire shape of the binary's `hello` payload (`role: "server"`, `server_id`, `binary_version`, `protocol_versions: ["v1"]`) and the relay's `hello_ack` response (`protocol_version`, `server_id`, `conn_id`). Reference shapes for the JSON marshal/unmarshal pair.
- `docs/protocol-mobile.md:715-735` — § Worked example. Concrete byte-shape of the binary→relay frames. Note line 721: `hello_ack` arrives wrapped in `RoutingEnvelope` with `conn_id: "-"` — relay-to-binary direction is ALWAYS wrapped, even for handshake responses. The handshake decoder must unwrap before checking `Type`.
- `internal/transport/wssclient.go:1-244` — the entire generic WSS client. `Connect(ctx)` is the blocking lifecycle the relay package consumes via a goroutine; `Send([]byte)` / `Receive(ctx)` are the byte-level I/O the handshake state machine uses. Read the package doc-comment (lines 1-13) for the explicit "knows nothing about handshake or close codes" contract — this ticket is the layer that adds both.
- `internal/protocol/envelope.go:23-44` — `Envelope` and `RoutingEnvelope` shapes. `Frame json.RawMessage` is the deferred-decode seam this spec exploits for two-pass unmarshal at the handshake boundary.
- `internal/protocol/handshake.go:8-34` — `HelloServerPayload` (we marshal this) and `HelloAckPayload` (we don't currently decode the body — only `Envelope.Type` matters for accept/reject — but it's the schema downstream wiring will reach for).
- `internal/protocol/codes.go:14,38-39` — `TypeHello`, `TypeHelloAck` constants; `CodeRelayServerIDConflict` already defined for future error-payload mapping (not used at the WS-close layer in this ticket).
- `internal/identity/server_id.go` — `ServerID` typed newtype. `Config.ServerID` is `identity.ServerID`; the caller (supervisor wiring in a future ticket) is responsible for `LoadOrCreate`-ing it before constructing `Config`.
- `docs/specs/architecture/247-wssclient-with-auto-reconnect-backoff.md:488-495` — the "Connected-channel — deferred decision" note. This ticket lands what was deferred: the explicit signal so the handshake layer knows when to resend `hello`.
- `docs/lessons.md:290` (and `internal/transport/wssclient.go:264-283` for the in-code mirror) — "pump goroutines installed before the conn is observable" pattern. The `Connected` signal this ticket adds fires AFTER `setConn` for the same reason: a relay caller waking on Connected must find `Send` / `Receive` already wired against the live conn.
- `docs/protocol-mobile.md:554-633` — § Security model. Threat #2 ("Server-id race") is the directly load-bearing one for this ticket. The handshake design here is the layer where first-claim-wins enforcement actually surfaces to the binary.
- `CODING-STYLE.md` — `gofmt`, stdlib-first, `log/slog` injected, table-driven tests, `context` everywhere, errors-not-panics. Inherited verbatim.

## Context

Phase 3 Track C — composes the v1 envelope/payload types (#246, #255, #256, #271), the generic WSS client with backoff (#247), and the server-id store (A2 / #207). This ticket is where the binary actually announces itself to the relay and starts the long-lived connection that all phone traffic flows through.

The stack shape is:

```
internal/relay (this ticket, #248)  ──> hello/hello_ack state machine,
                                        Frames() <-chan RoutingEnvelope,
                                        4409 → fatal error surface
        │
        ▼
internal/transport (#247)           ──> dial, reconnect+backoff, ping/pong;
                                        Connected() signal + FatalCloseCodes
                                        added here
        │
        ▼
github.com/coder/websocket
```

`internal/transport` keeps its "generic over frame payload" contract. The two extensions this ticket adds — `Connected() <-chan struct{}` and `Config.FatalCloseCodes` plus a `DropConn` escape hatch — are still protocol-agnostic; they let an arbitrary consumer drive a per-conn lifecycle. Protocol semantics live entirely in the new `internal/relay` package.

The ticket is sized S (~150 LOC production + ~30 LOC additions to transport + linear tests). No consumer fan-out — `transport.Client` has no in-repo callers today; the additions are pure-additive.

### Why a new package, not part of `internal/transport`

`internal/transport` is the wire-mechanics primitive: it knows about ping/pong and reconnect, nothing about envelopes or roles. Pulling `protocol.Envelope`/`HelloServerPayload` into it would be a layering inversion — the package doc explicitly says it knows nothing about handshake or close-code interpretation, and #247's deferred-decision note assigned this work to #248.

`internal/relay` is a thin consumer:

- imports `internal/transport`, `internal/protocol`, `internal/identity`
- never imports `github.com/coder/websocket` directly *except* for the `websocket.StatusCode` type passed through `transport.Config.FatalCloseCodes` — and even that we expose as a typed int constant in `internal/relay` so callers don't pull the websocket package transitively for headers alone

This keeps the relay package consumable from `cmd/pyry` (supervisor wiring in a later ticket) without that file needing to know what library backs the transport.

## Design

### Package layout

```
internal/relay/connection.go         (new, ~150 production LOC)
internal/relay/connection_test.go    (new, ~280 test LOC)
internal/transport/wssclient.go      (modified, +~50 LOC additive)
internal/transport/wssclient_test.go (modified, +~120 LOC for new behaviour)
```

Two new files plus two modified. No new exported types or methods on existing types beyond the four transport additions listed below. Files = 4 total (2 new), under the architect's "≤3 new files" red line. Production-line total ≈200 LOC — over PO's 120-150 estimate, but the extra ~50 LOC in transport is the `ErrDisconnected` plumbing #247's spec promised. The ticket is still S-shaped: zero edit fan-out (no in-repo callers of transport), single new package, single new exported surface (the Connection type).

### Transport extensions (the four additions this ticket lands in `internal/transport`)

The deferred-decision note in #247's spec explicitly assigned these to this ticket. They are purely additive: zero-value `FatalCloseCodes` preserves #247's current "reconnect on every drop" behaviour; the `ErrDisconnected` sentinel was named in #247's spec but never implemented and is implemented here; existing callers (none in-repo) see no API regression.

#### Addition 1 — `Connected() <-chan struct{}` signal

```go
// connectedCh emits a value on every successful conn that survives setConn.
// Buffered to 1 with drop-on-full semantics: a slow observer sees the
// most recent connect event, not every connect since boot. The handshake
// layer is the only consumer; it drains immediately on wake.
connectedCh chan struct{}

// Connected returns the channel a consumer reads to learn that a fresh
// underlying WS conn is live. Use cases: re-running an application-layer
// handshake (hello / hello_ack) on every reconnect. Multiple observers
// are NOT supported — the channel has capacity 1 and a non-blocking send,
// so a second observer can miss events.
func (c *Client) Connected() <-chan struct{} { return c.connectedCh }
```

Construction in `New`:

```go
connectedCh: make(chan struct{}, 1),
```

Emission in `serve`, right after `setConn(conn)`:

```go
select {
case c.connectedCh <- struct{}{}:
default: // drop — observer is slow, will see next event
}
```

The non-blocking send is load-bearing: a blocking send would couple the transport's dial loop to the relay's handshake goroutine, and a stuck handshake would stall reconnect. Drop-on-full is the right semantics because the consumer only needs "*is there a fresh conn right now*", not "how many connects have I missed."

Lock order: emission happens outside any mutex. `setConn` releases `c.mu` before the select. No new lock graph edges.

#### Addition 2 — `Config.FatalCloseCodes` + `ErrFatalClose` sentinel

```go
type Config struct {
    URL          string
    Headers      http.Header
    WriteTimeout time.Duration
    Logger       *slog.Logger

    // FatalCloseCodes lists WS close codes that terminate Connect's
    // reconnect loop with ErrFatalClose. Empty (default) preserves the
    // generic "reconnect on every drop" behaviour. The relay layer (#248)
    // passes []websocket.StatusCode{4409} so a server-id conflict halts
    // immediately rather than spinning in backoff.
    FatalCloseCodes []websocket.StatusCode
}

// ErrFatalClose wraps a websocket close error whose status is in
// Config.FatalCloseCodes. Returned by Connect(); the underlying status
// is recoverable via websocket.CloseStatus(err).
var ErrFatalClose = errors.New("transport: fatal close code")
```

The check fires in `Connect()`, after `serveErr := c.serve(...)` and before the reconnect-or-cancel switch:

```go
if status := websocket.CloseStatus(serveErr); status != -1 {
    for _, fc := range c.cfg.FatalCloseCodes {
        if status == fc {
            return fmt.Errorf("%w (%d): %v", ErrFatalClose, status, serveErr)
        }
    }
}
```

`websocket.CloseStatus(err)` returns `-1` if `err` is not a close-status error; the check is safe to apply to every disconnect.

This addition deliberately does NOT inspect WS *application-level* error envelopes (an `error` envelope of type `auth.invalid_token` on a phone conn, etc.). Those are dispatcher-layer concerns and not in this ticket's scope. We only react to WS-level close codes the relay or peer sends.

#### Addition 3 — `ErrDisconnected` surfaced from `Receive` on conn drop

#247's spec named `ErrDisconnected` as the way a blocked `Receive` would learn that the underlying conn dropped, but the implementation never wired it up — `Receive`'s current select only fires on `recvCh`, caller `ctx`, or `closeCh`. The result: after a conn drop, a caller blocked in `Receive` stays blocked until the next conn delivers a frame. For this ticket's `forwardFrames` loop, that means a reconnect arrives but we never bail out to re-run the handshake on the new conn. The fresh conn would idle indefinitely waiting for our `hello`, which we'd never send because the goroutine is wedged in the previous-conn `Receive`. Pyry would silently stop processing inbound traffic after the first transport drop.

The fix: add an in-`Client` "current connection done" channel that closes when serve returns and is replaced before the next dial. `Receive` selects on it; on conn drop, `Receive` returns `ErrDisconnected`. The handshake loop observes this, returns from `forwardFrames`, and the outer `run` select catches the next `Connected` signal.

```go
// connDone is a per-conn signal channel: closed when the current live
// conn drops, replaced with a fresh channel before each new dial. While
// no conn is live, connDone is the same closed channel — Receive returns
// ErrDisconnected immediately, which is the correct "you can't receive
// when nothing's connected" semantics.
//
// connDoneMu guards reads (Receive) and writes (serve teardown / setup).
connDoneMu sync.Mutex
connDone   chan struct{}

// ErrDisconnected is returned by Receive when the underlying conn dropped
// while Receive was blocked, or when no conn is currently live. Callers
// observing this should NOT treat it as a re-handshake trigger directly —
// observe Connected() for that. ErrDisconnected is "your current Receive
// call returned because the wire dropped, not because data arrived."
var ErrDisconnected = errors.New("transport: connection lost")
```

Construction in `New`:

```go
closedCh := make(chan struct{})
close(closedCh)
c := &Client{
    // ...
    connDone: closedCh,
}
```

In `serve`, install a fresh channel before pumps start and close it at teardown:

```go
fresh := make(chan struct{})
c.connDoneMu.Lock()
c.connDone = fresh
c.connDoneMu.Unlock()
defer func() {
    c.connDoneMu.Lock()
    close(fresh)
    // Leave c.connDone pointing at the now-closed channel; the next
    // serve iteration replaces it with a fresh one before pumps start.
    c.connDoneMu.Unlock()
}()
```

`Receive` gains a fourth case:

```go
func (c *Client) Receive(ctx context.Context) ([]byte, error) {
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
```

The select reads `done` once at top — a serve iteration that replaces `c.connDone` between the load and the select cannot accidentally make us wake on the new channel. The captured value is the one we wanted to observe.

There is a benign race: a Receive call that arrives *while* a fresh conn is being installed could capture either the old (closed) or new (open) `connDone`. If old (closed), Receive returns ErrDisconnected immediately, the caller loops back, observes Connected from the run-loop select, runs the handshake. If new (open), Receive blocks normally. Both outcomes are correct.

Initial state: `connDone` is a pre-closed channel. A `Receive` call before `Connect()` has produced any live conn returns ErrDisconnected immediately rather than blocking forever. This matches `Send`'s `ErrNotConnected` shape — both pre-connect errors are "you can't do I/O yet."

#### Addition 4 — `Client.DropConn(status websocket.StatusCode, reason string)`

```go
// DropConn force-closes the live conn (if any) with the given status and
// reason. Connect's serve loop sees the closed conn, returns to the dial
// loop, and reconnects via backoff. DropConn does NOT halt the dial loop.
// Use when the consumer's application-layer protocol failed mid-conn
// (e.g. handshake timeout, malformed handshake response) and wants the
// transport to recycle the underlying WS without tearing the Client down.
// Idempotent (safe to call when no conn is live).
func (c *Client) DropConn(status websocket.StatusCode, reason string) {
    c.mu.Lock()
    conn := c.conn
    c.mu.Unlock()
    if conn != nil {
        _ = conn.Close(status, reason)
    }
}
```

This is the escape hatch the relay handshake state machine needs when `hello_ack` doesn't arrive within 5 seconds, or the first frame isn't `hello_ack`: the conn is live (no transport-layer error), but the handshake is stuck. `DropConn(StatusGoingAway, "handshake timeout")` forces a reconnect.

Choice rationale for `DropConn` vs. exposing per-conn context: per-conn context plumbing would require either passing the context to `Receive` (already done) AND having transport plumb that context into the conn's read deadline, which couples layers more than necessary. `DropConn` is the smallest API increment that achieves the same end.

### `internal/relay/connection.go`

```go
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

// Wire-spec constants. See docs/protocol-mobile.md § Connection lifecycle.
const (
    handshakeTimeout = 5 * time.Second

    // statusServerIDConflict is the WS close code the relay sends when a
    // server-id is already claimed (docs/protocol-mobile.md § Error codes
    // line 552). Typed locally so callers don't have to import the
    // websocket package for the value.
    statusServerIDConflict websocket.StatusCode = 4409

    // statusHandshakeAborted is the close code we send when forcing a
    // reconnect from the consumer side (handshake timeout, malformed
    // hello_ack). 1000 ("normal closure") communicates to the relay that
    // we're closing cleanly and don't expect special handling.
    statusHandshakeAborted = websocket.StatusNormalClosure
)

// Sentinel errors. Callers distinguish fatal vs. retryable via errors.Is.
var (
    // ErrServerIDConflict is the terminal error returned by Wait() when
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
}

// Connection runs the binary↔relay leg of the wire protocol. Lifecycle is
// tied to the context passed to Connect; cancellation closes the WS
// cleanly with status 1000. Wait() blocks until the lifecycle terminates;
// the returned error is the terminal classification (ErrServerIDConflict
// for fatal 4409, ctx.Err() for graceful shutdown, or a wrapped transport
// error for unexpected halts).
type Connection struct {
    cfg    Config
    client *transport.Client

    frames chan protocol.RoutingEnvelope

    closeOnce sync.Once
    closed    chan struct{}

    // result is set exactly once by the lifecycle goroutine before
    // signalling done. Read after <-done.
    done   chan struct{}
    result error
}

// Connect builds the transport, starts the lifecycle goroutine, and
// returns immediately. The connection is not yet Ready — observe Frames()
// to consume post-handshake inbound frames, or call Wait() to block on
// terminal classification. The caller is responsible for invoking Close()
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
    // Reject non-wss schemes. Server-id is sent in a request header; an
    // operator misconfiguration to ws:// (e.g. local-relay testing) would
    // disclose it in cleartext. Defense-in-depth — see security review
    // § Cryptographic primitives.
    parsedURL, err := url.Parse(cfg.RelayURL)
    if err != nil {
        return nil, fmt.Errorf("%w: RelayURL parse: %v", ErrInvalidConfig, err)
    }
    if parsedURL.Scheme != "wss" {
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

// Frames returns the channel of post-handshake inbound frames. The
// channel closes when the lifecycle terminates. Frames are delivered in
// the order the underlying conn produces them; reconnects are
// transparent (a fresh hello/hello_ack handshake runs first, then frames
// resume on the new conn).
func (c *Connection) Frames() <-chan protocol.RoutingEnvelope { return c.frames }

// Wait blocks until the lifecycle terminates and returns the terminal
// classification: ErrServerIDConflict (fatal), ctx.Err() (graceful
// shutdown), or a wrapped transport error.
func (c *Connection) Wait() error {
    <-c.done
    return c.result
}

// Close requests a clean shutdown. Idempotent. After Close, Frames
// drains and closes; Wait returns nil-or-ctx-error depending on race.
func (c *Connection) Close() error {
    c.closeOnce.Do(func() {
        close(c.closed)
        _ = c.client.Close()
    })
    return nil
}

// --- internals ---

func (c *Connection) run(ctx context.Context) {
    defer close(c.frames)
    defer close(c.done)

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
                c.client.DropConn(statusHandshakeAborted, "handshake failed")
                // Loop back: transport's dial loop will reconnect via
                // backoff, fire Connected again, and we retry the
                // handshake on the fresh conn. If the failure mode is
                // persistent (relay is broken), the transport's backoff
                // saturates at 30s — acceptable degraded behaviour.
                continue
            }
            c.forwardFrames(ctx)
            // forwardFrames returns when the underlying conn drops or
            // ctx is cancelled. Loop back; transport reconnects, or
            // transportErrCh fires.
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
    // including hello_ack (docs/protocol-mobile.md:721, conn_id "-").
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
            // Four causes:
            //   - transport.ErrDisconnected: the live conn dropped;
            //     transport will reconnect and fire Connected; we need
            //     to return so the outer run loop can re-handshake.
            //   - transport.ErrClosed: Close() was called.
            //   - ctx.Err(): caller-cancelled.
            //   - other: an unwrapped error (shouldn't happen given the
            //     current transport surface; we treat it like the wire
            //     dropped to stay safe).
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
```

### Concurrency model

```
Connect(ctx) returns
    │
    └── run(ctx)               <-- single owner goroutine
          │
          ├── go client.Connect(ctx)  <-- transport's dial loop
          │       │
          │       ├── recvPump  <-- transport-internal
          │       ├── sendPump  <-- transport-internal
          │       └── pingLoop  <-- transport-internal
          │
          ├── select on:
          │     - ctx.Done()                  → set result, exit
          │     - c.closed                    → set result, exit
          │     - transportErrCh              → classify + exit
          │     - client.Connected()          → run handshake, then forwardFrames
          │
          └── forwardFrames(ctx) loop reads client.Receive, pushes to c.frames
```

Exactly two goroutines owned by the relay package: the `run` lifecycle goroutine, plus the trampoline goroutine that calls `client.Connect(ctx)`. The trampoline exits when transport returns; `run` exits when any of its four select cases fires.

Channel close discipline: `c.frames` is closed exactly once by `run`'s defer; `c.done` is closed exactly once by `run`'s defer; `c.closed` is closed exactly once by `Close()` via `sync.Once`. No double-close possible.

Lock order: relay package holds no locks. The `sync.Once` is internal to `Close`. The transport package owns its own locks (unchanged from #247).

Shutdown sequence:

1. Caller cancels ctx OR calls `Close()`.
2. `run` observes the signal, exits its select.
3. `defer c.client.Close()` fires, closing transport.
4. Transport's `Connect` returns; `transportErrCh` would fire but `run` is already exiting (the trampoline goroutine still drains and the buffered channel absorbs the value).
5. `c.frames` and `c.done` close via defers. Wait() returns.

### Error handling — failure modes and recovery

| Failure mode | Surface | Recovery |
|---|---|---|
| WS close `4409` (server-id conflict) | `Wait() → ErrServerIDConflict` | None. Fatal. Operator must resolve the conflict (kill the other pyry, or wait the 30s grace if a stale conn is being reaped on the relay side). |
| WS close `1011` / `1006` / transport drop | invisible to caller; transport reconnects | `run` loops on `Connected()`, re-sends `hello`, awaits `hello_ack`. |
| `hello_ack` not received within 5s | logged at WARN; `DropConn` fires; transport reconnects | Same loop as above. Persistent failure → transport's backoff saturates at 30s. |
| First post-`hello` frame is not `hello_ack` | logged at WARN; `DropConn` fires; transport reconnects | Same as above. |
| Malformed JSON on inbound frame (post-handshake) | logged at WARN; frame dropped; loop continues | Single bad frame does not tear the conn. Subsequent frames still flow. |
| ctx cancelled | `run` returns ctx.Err() via `Wait()` | Graceful shutdown. |
| `Close()` called | `run` returns nil via `Wait()` | Graceful shutdown. |
| `Send(hello)` returns `ErrNotConnected` (race: conn dropped between Connected signal and Send) | handshake returns error → `DropConn` no-op (no conn) → transport reconnects | Same recovery loop. |

The handshake-timeout case relies on `DropConn` to force a fresh attempt. Without `DropConn`, a relay that opened the conn but never sent `hello_ack` would leave us in a hung loop until the transport's ping-timeout kicked in (60s worst case). The 5s handshake budget + immediate recycle is the wire-spec's intent (`docs/protocol-mobile.md:130` — "If `error` received instead, log and back off." — and the same applies to silent stalls).

### Testing strategy

All tests in-package (`package relay`). Stdlib only — `testing`, `httptest`, `coder/websocket` (already a dependency). No mocking framework, no testify.

Test helpers (in `connection_test.go`):

- `newTestRelay(t)` — `httptest.NewServer` wrapping a `websocket.Accept` handler. Helper-controlled to:
  - Read the first frame from a client, parse the inner Envelope, assert `Type == "hello"`, send back a `hello_ack` wrapped in `RoutingEnvelope{ConnID: "-"}` — happy path.
  - Optionally skip the `hello_ack` send to drive the 5s timeout test.
  - Optionally close with status `4409` immediately after accept to drive the conflict-fatal test.
  - Optionally close mid-handshake (after `hello` arrived, before sending `hello_ack`) with `1006`-equivalent (`conn.CloseNow()`) to drive the transport-drop-during-handshake test.
- `testLogger(t)` — discarding `slog.Logger` (mirrors the transport test helper).
- `shortenHandshakeTimeout(t)` — substitutes a 200ms timeout for `handshakeTimeout` via a test-only mutator (or by exposing `handshakeTimeout` as a `var` for tests to override). Decision: expose as a package-level `var` (lowercase) — same idiom as `internal/transport`'s test-only `pingInterval` field on `Client`. Production callers cannot reach it; tests in the same package can.

The transport extensions (`Connected`, `FatalCloseCodes`, `ErrDisconnected`, `DropConn`) need their own table-driven tests in `internal/transport/wssclient_test.go`:

- `TestConnected_FiresOnEveryConnect` — start client, force drop via the test relay's `ForceClose`, observe a second event on `Connected()`.
- `TestConnected_DropsWhenObserverSlow` — drain the channel late, verify no panic / no leak.
- `TestFatalCloseCodes_HaltsReconnect` — relay closes with 4409, `Connect()` returns `ErrFatalClose` wrapping a status-4409 error; assert `websocket.CloseStatus(err) == 4409` and `errors.Is(err, ErrFatalClose)`.
- `TestFatalCloseCodes_EmptyPreservesReconnect` — same scenario, empty `FatalCloseCodes`, assert reconnect still happens (existing #247 behaviour unchanged).
- `TestReceive_ReturnsErrDisconnectedOnConnDrop` — start client, attach via `Receive` in a goroutine, force-drop via the test relay; assert the goroutine returns `(nil, ErrDisconnected)` within a short budget.
- `TestReceive_BeforeConnectReturnsErrDisconnected` — call `Receive` on a freshly-constructed Client without invoking `Connect`; assert `ErrDisconnected`. Pins the pre-connect contract.
- `TestDropConn_TriggersReconnect` — start client, observe Connected; call DropConn; observe a second Connected after backoff.
- `TestDropConn_BeforeConnect` — call DropConn on a fresh Client (no live conn); assert no panic, no error.

The relay package tests:

| Test | Scenario | Assertion |
|---|---|---|
| `TestConnect_HappyPath` | hello → hello_ack → Ready | First frame the relay receives parses as `Envelope.Type == "hello"` with role=server, server-id, version, protocol_versions=["v1"]; headers `x-pyrycode-server`, `x-pyrycode-version`, `user-agent` present and shaped. |
| `TestHandshake_AckTimeout` | relay accepts but never sends ack | After ~200ms (shortened constant), the connection logs handshake failure; relay observes the conn close; if the relay accepts the *next* dial, the second attempt re-sends hello and succeeds → Ready. |
| `TestHandshake_UnexpectedFrame` | relay sends a non-hello_ack frame first (e.g. an `error` envelope) | Same recycle behaviour as ack-timeout: `DropConn`, reconnect, second attempt. |
| `TestServerIDConflict_FatalNoReconnect` | relay closes with status 4409 immediately on accept | `Wait()` returns `ErrServerIDConflict`; no second dial occurs (the relay's `connCount` stays at 1 after a short wait). |
| `TestTransportDropDuringHandshake` | relay accepts, reads hello, then calls `conn.CloseNow()` (transport-level drop, status 1006-equivalent) | Connection observes the drop, transport reconnects via backoff, second attempt completes the handshake → Ready. |
| `TestTransportDropPostHandshake_ReHandshakes` | after Ready, relay calls `conn.CloseNow()`; on the next accept, parses the new hello, sends new hello_ack, then sends a frame | The post-reconnect frame is delivered on the SAME `Frames()` channel (proves the re-handshake landed and forwardFrames bailed via ErrDisconnected, not via the original ctx). |
| `TestFrames_DeliversPostHandshakeInOrder` | after handshake, relay sends 3 frames each wrapped in RoutingEnvelope with distinct conn_ids and frame ids 2, 3, 4 | `Frames()` yields the three envelopes in arrival order; `ConnID` values match what the relay sent. |
| `TestClose_ShutsDownCleanly` | call `Close()` after a successful handshake | `Frames()` channel closes; `Wait()` returns nil; goroutines exit (verified by waiting on `done`). |
| `TestContextCancel_ShutsDownCleanly` | cancel ctx after a successful handshake | `Wait()` returns ctx.Err(); Frames channel closes. |
| `TestConfig_Validation_TableDriven` | missing each required field in turn, plus `RelayURL` with `ws://` / `http://` / unparseable schemes | `Connect` returns `ErrInvalidConfig` with a wrapped message naming the missing field or wrong scheme. |
| `TestHeaders_Set` | introspect the test relay's accept-time headers | `x-pyrycode-server` == cfg.ServerID, `x-pyrycode-version` == cfg.BinaryVersion, `user-agent` == "pyry/" + cfg.BinaryVersion. |

The reconnect-after-drop tests require shortening transport's `reconnectInitial` for time-bound assertions. The transport's existing test-only mutators on the `Client` struct (`newClientForTest`) are not reachable from `package relay`. Options:

1. Add a `transport.NewForTest(cfg Config, opts TestOptions) *Client` exported-for-tests helper. Cross-package test plumbing is awkward.
2. Use the production-default cadence (1s initial, ±20%) and budget tests at ~3-4s for the reconnect cases. Acceptable for the 5–6 tests that need it.
3. Use a fake transport. Heavy — requires defining an interface `Transporter` and refactoring `*transport.Client` consumers. Out of scope.

Decision: **option 2.** Three or four tests at ~3-4s each is tolerable and avoids cross-package test plumbing. Document the budget at the top of the test file.

For the `hello_ack` 5s timeout test specifically, this would mean a real 5-second wait — too slow. Solution: expose `handshakeTimeout` as a package `var` in `internal/relay/connection.go` (lowercase, package-private) and have tests substitute a short value via `testing.T.Cleanup` to restore. This is the same idiom `internal/transport`'s test file uses (substituting `pingInterval` on the `Client`).

### Logging

All structured slog. Fields used:

- `relay: handshake complete` → `server_id`
- `relay: handshake failed; recycling conn` → `err`
- `relay: malformed routing envelope; dropping` → `err`

Forbidden fields (security review § 7):

- never `token` (no tokens flow through this layer — token validation is per-phone-conn, future ticket)
- never `payload` (frame bodies can contain user message text in the future; even pre-encryption v1, log routing metadata only)
- never raw `frame` bytes
- never full `Headers` map (would leak `x-pyrycode-server` to every log; the `server_id` field is the operator-actionable subset)

The transport's existing logging (`transport: connected`, `transport: dial failed, backing off`, `transport: disconnected`) is sufficient for conn lifecycle; the relay package adds only application-layer events.

## Open questions

None blocking. Two notes for the consuming ticket (supervisor wiring, not this one):

- **`BinaryVersion` source**: the supervisor will pass `runtime/debug.ReadBuildInfo()` or a `-ldflags="-X main.version=..."` value. The relay package treats it as an opaque string; spec lives upstream.
- **What does the supervisor do on `ErrServerIDConflict`?** The decision is "log + exit with non-zero status; let launchd/systemd decide whether to restart." Not this ticket's call.

## Out of scope (per the issue body, re-stated)

- Token validation of phone connections (Track C4)
- Per-message handlers / dispatch on `Envelope.Type` (out of net scope)
- Supervisor wiring of `Connection` into the daemon lifecycle (separate ticket — will consume `Frames()` and `Wait()`)
- `register_push_token` and outbound message sending from the binary (separate ticket — will need a `Connection.Send(env protocol.Envelope, conn_id string)` method that wraps in RoutingEnvelope)

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary is explicit and single-site: bytes from `client.Receive()` are untrusted; they cross to trusted typed values via `json.Unmarshal(...) RoutingEnvelope` then `json.Unmarshal(routing.Frame, ...) Envelope` in `handshake` and `forwardFrames`. Frames that fail either decode are dropped at the trust boundary with a WARN log; no untrusted bytes reach `Frames()`. Downstream consumers receive `protocol.RoutingEnvelope` values whose `Frame` is still `json.RawMessage` (intentionally — the dispatcher does the per-type decode), and that's documented at the package level.
- **[Tokens, secrets, credentials]** No findings. No tokens flow through this package — the binary→relay leg uses server-id (a public routing key, not a secret) as the only identifier. Device tokens are validated per-phone-conn in a future ticket. Server-id appears in one log field (`server_id`) and is leaked deliberately (operator needs it to diagnose 4409 conflicts).
- **[File operations]** Not applicable. This package performs zero filesystem I/O.
- **[Subprocess / external command execution]** Not applicable.
- **[Cryptographic primitives]** No findings (defense added in design). TLS is provided by `coder/websocket` and uses Go's default `crypto/tls.Config` (TLS 1.2 minimum, secure cipher suite defaults). This package does not configure TLS — the URL scheme drives it. To prevent operator misconfiguration (`ws://` with cleartext server-id header), `Connect()` parses `cfg.RelayURL` via `url.Parse` and rejects schemes other than `wss` as `ErrInvalidConfig`. Server-id is not a credential per `docs/protocol-mobile.md` § Security model Threat 2, but the cleartext-disclosure defense is cheap and structural so we apply it.
- **[Network & I/O]**
  - *Inbound size cap:* inherited from `transport.Client.realDial`'s `conn.SetReadLimit(1 MiB)` — every frame the relay layer reads is already capped at 1 MiB by the transport. No new vector.
  - *Header validation:* the relay package only **sets** headers (writes); it doesn't read incoming headers from the relay (the WS upgrade response headers are ignored). No injection vector.
  - *Slow-loris on inbound frame size:* the 1 MiB read cap + transport's per-frame read driven by `Read(ctx)` (no per-frame deadline beyond ctx) means a malicious relay sending a 1 MiB body byte-by-byte at 1 B/sec would block `Receive` for ~12 days. This is acceptable for a single peer (the relay we operate; #587 § Threat 3 already notes "trust-based" relay model). If the relay operator is hostile, we have larger problems than slow frames.
  - *Handshake stall:* the 5s `hello_ack` deadline IS the slow-loris defense for the handshake phase. After Ready, the ping-loop's 30s ping + 30s pong-timeout in `internal/transport` is the inactivity defense.
  - *Resource exhaustion:* the relay package opens exactly one outbound connection; there is no per-server-id cap because there's only one Connection per process. N/A.
- **[Error messages, logs, telemetry]** No findings. The forbidden-fields list in § Logging above explicitly excludes payloads, raw frame bytes, and full headers. The `err` field on log lines wraps `transport`'s error which wraps `websocket`'s error — none of these include request body content; they include URLs and close-codes only. Error sentinels returned to callers (`ErrServerIDConflict`, `ErrInvalidConfig`) carry no dynamic content beyond a fixed string.
- **[Concurrency]**
  - *Lock order:* relay package holds zero locks (only `sync.Once` for `Close`). Transport gains one new mutex (`connDoneMu`) — leaf-only, no edges with existing transport mutexes (`mu`, `rngMu`).
  - *Channel close discipline:* `frames`, `done`, `closed` each closed exactly once (deferred in `run` or sync.Once in `Close`). Transport's `connDone` is closed at serve teardown and replaced (not re-closed) at next serve setup; no double-close. No double-close.
  - *Goroutine lifecycle:* `run` and the transport-trampoline goroutine each have explicit exit conditions. `forwardFrames` returns on any `Receive` error or ctx cancel. No leak surface identified.
  - *Wedged-Receive-after-disconnect bug (called out and fixed):* without `ErrDisconnected`, a Receive blocked when the conn drops stays blocked until the next conn delivers a frame — but the relay won't send anything until our handshake completes, and our handshake won't run because we're wedged in the previous Receive. Pyry would silently stop processing inbound traffic after the first transport drop. The Addition-3 fix surfaces ErrDisconnected from Receive on conn drop; forwardFrames returns; run-loop catches the next Connected and re-handshakes. Tested via `TestTransportDropPostHandshake_ReHandshakes`.
  - *Race between Connected signal and conn drop:* if the conn drops between the relay observing `Connected` and calling `Send(hello)`, `Send` returns `ErrNotConnected` and `handshake` returns an error. `DropConn` is a no-op (no live conn). `run` loops back, observes the next `Connected` or `transportErrCh`. No stuck state.
  - *Race between handshake `Receive` and conn drop mid-flight:* the handshake's Receive uses a 5s deadline context; if the conn drops mid-handshake, `Receive` returns `ErrDisconnected` immediately (before the 5s expires), handshake propagates the error, `DropConn` is a no-op (conn already gone), `run` loops and catches the next Connected. Tested via `TestTransportDropDuringHandshake`.
- **[Threat model alignment]**
  - *§ Security model Threat 2 (Server-id race):* this ticket is the layer where first-claim-wins surfaces to the binary. `ErrServerIDConflict` is the explicit operator signal. The 30s grace window is inherited from the relay; this layer does not need to know about it (we just retry-on-1006 and stop-on-4409). The deferred relay-issued admin token would slot into `Config` as a future field; today we have only headers.
  - *§ Security model Threat 3 (Relay operator MITM):* not addressed by this ticket — TLS is the only defense at the wire level. Spec calls this out as deferred to v2 E2E encryption (`payload_encrypted` in the envelope).
  - *§ Security model Threat 5 (Implementation bugs):* this package's surface for path traversal / command injection / weak randomness is empty (no fs, no exec, no token mint). The principal risk is JSON-unmarshal misuse — addressed by the explicit "decode → check `Type` → either consume or drop" sequence with no field-by-field reflection.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-12
