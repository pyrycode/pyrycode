# `internal/transport` — WSS client with auto-reconnect backoff

## What it is

Long-lived WebSocket-Secure client for the binary's outbound connection to the relay. Generic over frame payload: accepts and emits `[]byte`. Knows nothing about pyrycode's protocol envelope, handshake, routing, or close-code semantics. Owns three things:

1. **Dial-and-serve lifecycle** with auto-reconnect.
2. **Native WebSocket ping/pong heartbeat** (RFC 6455 control frames).
3. **Exponential backoff with ±20% jitter and stability-reset.**

Wire-spec source-of-truth: `docs/protocol-mobile.md` § Heartbeat, § Reconnect, § TLS. When that document changes, this package changes.

## Surface

```go
package transport

type Config struct {
    URL          string
    Headers      http.Header // caller supplies (server-id, binary-version, …)
    WriteTimeout time.Duration
    Logger       *slog.Logger // required; nil panics at New time
}

type Client struct { /* opaque */ }

func New(cfg Config) *Client

func (*Client) Connect(ctx context.Context) error // blocking; returns ctx.Err() or ErrClosed
func (*Client) Send(frame []byte) error           // ErrNotConnected | ErrClosed | nil
func (*Client) Receive(ctx context.Context) ([]byte, error)
func (*Client) Close() error                      // idempotent

var (
    ErrNotConnected = errors.New("transport: not connected")
    ErrClosed       = errors.New("transport: client closed")
)
```

Run pattern (caller-owned goroutine):

```go
client := transport.New(transport.Config{
    URL:          "wss://relay.pyrycode.dev/v1/server",
    Headers:      http.Header{"X-Pyry-Server-Id": []string{serverID}},
    WriteTimeout: 10 * time.Second,
    Logger:       log,
})

go func() {
    if err := client.Connect(ctx); err != nil && !errors.Is(err, context.Canceled) {
        log.Error("transport stopped", "err", err)
    }
}()

// Once a conn is live (poll Send until !ErrNotConnected, or wait for the
// signal #248 will add):
_ = client.Send(envelopeBytes)
frame, _ := client.Receive(ctx)
```

## Cadence (locked at wire-spec level)

| Knob | Value | Source |
|---|---|---|
| Idle ping interval | 30s | `protocol-mobile.md` § Heartbeat |
| Pong timeout | 30s | § Heartbeat |
| Reconnect backoff | 1s / 2s / 4s / 8s / 16s / 30s cap, ±20% jitter | § Reconnect |
| Stability reset threshold | ≥60s uptime resets attempt counter | § Reconnect |
| Max inbound frame size | 1 MiB (`SetReadLimit`) | § Error codes (`message.too_long`) |
| Close code on reconnect | `1011` (internal error) | § Heartbeat |

Attempt counter increments per failed dial; resets to 1 after a serve loop that lasted `>= 60s`.

## Lifecycle and goroutines

Per Client lifetime: one caller-owned goroutine running `Connect`. Per live conn: three pump goroutines (`recvPump`, `sendPump`, `pingLoop`) under a child ctx. All three drain before `serve` returns to the outer dial loop — no goroutine outlives its `serve` call.

```
caller's goroutine: client.Connect(ctx)
  for {
    dial → backoff on fail → repeat
    on success: serve(ctx, conn) {
      go recvPump(ctx, conn) → recvCh
      go sendPump(ctx, conn) ← sendCh
      go pingLoop(ctx, conn)
      setConn(conn)     // AFTER the three go statements (lessons.md:290)
      wait for first errCh return; cancel; drain other two
    }
  }
```

Shutdown sequence (either `Close()` or `ctx` cancellation):

1. `Close` closes `closeCh` exactly once (`sync.Once`), force-closes any live conn with `1000` (normal closure).
2. Pumps observe `ctx.Done()` or read/write fails on the closed conn → return.
3. `serve` drains all three pump returns → returns to `Connect`.
4. `Connect` observes `ctx.Err() != nil` (or `closeCh` closed) → returns `ctx.Err()` / `ErrClosed`.

## Configuration and usage

- **`Config.Headers` is caller-owned.** The package does not construct the `X-Pyry-Server-Id`, `X-Pyry-Binary-Version`, or `X-Pyry-Protocol-Versions` headers; the handshake layer (#248) builds them and passes them in. This package does NOT log `Config.Headers` — downstream consumers must not either.
- **`Config.WriteTimeout`** bounds per-frame send I/O. It is NOT an inactivity timeout — the heartbeat is the inactivity contract.
- **`Config.Logger`** is required; nil panics at `New` time. Per-CODING-STYLE injected, not global. Lifecycle events logged at Info (dial, connected, disconnected); pong timeout logged at Warn.
- **`Config.URL`** is the full WSS URL (e.g. `wss://relay.pyrycode.dev/v1/server`). TLS verification uses Go's `crypto/tls` defaults (TLS 1.2 min, hostname-checked against URL host, system root CAs).

## Error model

| Method | Returns |
|---|---|
| `Connect` | `ctx.Err()` (ctx cancellation), `ErrClosed` (Close called). Never `nil`. |
| `Send` | `nil` (frame queued to send pump — not "written on the wire"), `ErrNotConnected` (no live conn), `ErrClosed`. |
| `Receive` | `(frame, nil)`, `(nil, ctx.Err())`, `(nil, ErrClosed)`. Does NOT return a "disconnected" error — recv-pump returning on a conn drop just blocks the next `Receive` until reconnect. |
| `Close` | Always `nil`. Idempotent. |

`Send` returning `nil` means "queued to the send pump"; the pump may still fail to write (then the conn drops and reconnect kicks in). The caller cannot distinguish "frame hit the wire" from "frame queued and then dropped" — that's by design for an async transport. Re-issue on the next live conn if needed.

## Edge cases and gotchas

- **`Send` before first dial returns `ErrNotConnected`** (not block-forever). Production callers usually want to wait — poll `Send` with a backoff, or wait for the "Connected" signal that #248 will add.
- **`Receive` does not signal reconnects.** Frames from the new conn flow into `recvCh` after reconnect; callers needing to re-handshake must observe live state explicitly (deferred to #248).
- **Inbound frames are capped at 1 MiB** via `conn.SetReadLimit`. A peer sending more trips `read limit exceeded`, the recv-pump returns, the dial loop reconnects. This is the only DoS knob in this layer.
- **`recvCh` and `sendCh` are unbuffered.** A slow `Receive` consumer applies back-pressure all the way to the relay's write buffer. The dispatcher (#248) must keep `Receive` drained continuously.
- **`math/rand` for jitter, not `crypto/rand`.** Jitter is anti-thundering-herd noise; predictability of the exact delay enables no attack (a hostile relay already controls connection-drop timing).
- **`1011` not `1006` on reconnect-close.** `1006` is received-only; this client cannot actively send it. Spec wording "1011 or 1006" describes the two observable peer states, not a choice.
- **`Config.ReadTimeout` is not present** (was in the original AC body but dropped by architect's security review). The heartbeat is the inactivity contract; a separate ReadTimeout would shadow it.

## Test surface

`internal/transport/wssclient_test.go` (~520 LOC, stdlib + `coder/websocket`):

- `newClientForTest(t, cfg, testOpts)` — shorter cadence constants + deterministic seed for sub-second tests.
- `newTestRelay(t)` — `httptest.NewServer` with a `coder/websocket.Accept` upgrader, ping counter, pong suppression, force-close, optional echo loop.

Pinned behaviour:

- `TestBackoff_Sequence` — attempts 1..10, base in `[base*0.8, base*1.2]`. Caps at 30s from attempt 6.
- `TestBackoff_ResetAfterStableConnection` — uptime ≥ `stabilityReset` resets attempt counter to 1.
- `TestPing_FiredAt30s` — ping cadence (skipped under `-short`; uses 50ms test interval).
- `TestPongTimeout_TriggersReconnect` — pong-suppressed relay → second dial within ~80ms test pong timeout.
- `TestClose_OnContextCancel` — `cancel(ctx)` returns `Connect` with `context.Canceled` within 1s.
- `TestClose_Idempotent` — `Close()` called twice returns nil both times.
- `TestSmoke_HttptestEchoServer` — full handshake + Send/Receive echo + ≥1 ping + `Close` → `Connect` returns `ErrClosed`/`context.Canceled`.
- `TestSend_ReturnsErrNotConnected_BeforeConnect` — `Send` before `Connect` doesn't block.
- `TestSend_ReturnsErrClosed_AfterClose` / `TestReceive_ReturnsErrClosed_AfterClose` — post-Close sentinels.
- `TestNew_PanicsWithoutLogger` — pins the "Logger is required" contract.

## Consumers and roadmap

- **#248 — dispatch layer** (next ticket): wraps `Send([]byte)` / `Receive() []byte` with `protocol.Envelope` marshal/unmarshal, runs the hello/hello_ack handshake, observes a "Connected" signal (this layer will add the signal channel once #248's reaction shape is known), interprets close codes (`4409`/`4401`/`4404`), enforces `Envelope.ID` monotonicity.
- **`internal/protocol`** (#255, landed): the envelope types this transport's frames will (mostly) carry.

## Dependencies

- `github.com/coder/websocket v1.8.13` — MIT, ~2k LOC, context-first API, native ping/pong via `Conn.Ping(ctx)`, no transitive deps. First network-protocol dep in the project.
- Stdlib: `context`, `errors`, `fmt`, `log/slog`, `math/rand`, `net/http`, `sync`, `time`.

## Out of scope

See [`codebase/247.md`](../codebase/247.md) for the full deferred-list and rationale.
