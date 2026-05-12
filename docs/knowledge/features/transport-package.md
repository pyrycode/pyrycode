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

    // FatalCloseCodes lists WS close codes that terminate Connect's reconnect
    // loop with ErrFatalClose. Empty (default) preserves the generic
    // "reconnect on every drop" behaviour. The relay layer (#248) passes
    // []websocket.StatusCode{4409} so a server-id conflict halts immediately.
    FatalCloseCodes []websocket.StatusCode
}

type Client struct { /* opaque */ }

func New(cfg Config) *Client

func (*Client) Connect(ctx context.Context) error // blocking; ctx.Err() | ErrClosed | wrapped ErrFatalClose
func (*Client) Send(frame []byte) error           // ErrNotConnected | ErrClosed | ErrDisconnected | nil
func (*Client) Receive(ctx context.Context) ([]byte, error) // ErrClosed | ErrDisconnected | ctx.Err() | frame
func (*Client) Connected() <-chan struct{}        // emits on every fresh conn (#248 addition)
func (*Client) DropConn()                          // force-close live conn; reconnect via backoff (#248 addition)
func (*Client) Close() error                      // idempotent

var (
    ErrNotConnected = errors.New("transport: not connected")
    ErrClosed       = errors.New("transport: client closed")
    ErrDisconnected = errors.New("transport: connection lost")     // #248
    ErrFatalClose   = errors.New("transport: fatal close code")    // #248; status via websocket.CloseStatus(err)
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
| `Connect` | `ctx.Err()` (ctx cancellation), `ErrClosed` (Close called), or `ErrFatalClose` wrapping a `Config.FatalCloseCodes` hit (#248). Never `nil`. |
| `Send` | `nil` (frame queued to send pump — not "written on the wire"), `ErrNotConnected` (no live conn), `ErrDisconnected` (live conn dropped while Send was blocked enqueuing — #248), `ErrClosed`. |
| `Receive` | `(frame, nil)`, `(nil, ctx.Err())`, `(nil, ErrClosed)`, `(nil, ErrDisconnected)` (#248 — the underlying conn dropped while Receive was blocked, OR no conn is currently live; observe `Connected()` to learn when a fresh conn becomes available). |
| `Close` | Always `nil`. Idempotent. |

`Send` returning `nil` means "queued to the send pump"; the pump may still fail to write (then the conn drops and reconnect kicks in). The caller cannot distinguish "frame hit the wire" from "frame queued and then dropped" — that's by design for an async transport. Re-issue on the next live conn if needed.

### #248 additions

The deferred-decision note in #247's spec assigned four additions to #248 (the relay handshake layer). All are purely additive:

- **`Connected() <-chan struct{}`** — buffer-1 with drop-on-full emit. Fires AFTER `setConn` in `serve` so consumers waking on `Connected` find `Send`/`Receive` already wired against the live conn (same precedent as `docs/lessons.md:290`). Single-observer — multiple observers can miss events by design.
- **`Config.FatalCloseCodes` + `ErrFatalClose`** — close codes in this list halt `Connect`'s reconnect loop. The check fires on BOTH the post-`serve` path AND the post-`dialFn` path (when `coder/websocket` surfaces a close error directly from `Dial` rather than from a subsequent `Read` — happens when the peer closes during/immediately after the WS upgrade). The post-`serve` path also walks the three pump-return errors and PREFERS one with a recognizable `websocket.CloseStatus` (recvPump observes the `CloseError` but sendPump/pingLoop can return generic `use of closed network connection`; without preferring the close-status error, fatal-close classification flakes under `-race`).
- **`ErrDisconnected` + per-conn `connDone`** — `connDone` is per-conn, pre-closed at construction (`Receive`/`Send` before `Connect` return `ErrDisconnected` immediately rather than blocking forever), replaced with a fresh open channel inside `serve` before pumps install, closed in `serve`'s deferred teardown. `Receive` and `Send` capture `connDone` once under `connDoneMu` then select — a serve iteration that replaces `c.connDone` between the capture and the select cannot accidentally wake the caller on the new channel. Without this, a `Receive` blocked when the conn drops would stay blocked until the next conn delivers a frame, wedging the handshake loop above this layer permanently.
- **`DropConn()`** — force-closes the live conn (if any) via `conn.CloseNow()` (abrupt 1006-equivalent, no close-frame round-trip). The serve loop sees the closed conn, returns to the dial loop, reconnects via backoff. Does NOT halt the dial loop. Idempotent (no-op when no conn is live). `CloseNow` over `Close(status, reason)` so the caller is not blocked for up to 10s on a close handshake when the only purpose is to recycle the conn. The relay package uses this on application-layer handshake failures (`hello_ack` timeout, wrong type, malformed JSON) — the transport-layer conn is healthy but the application-layer protocol is stuck.

## Edge cases and gotchas

- **`Send` before first dial returns `ErrNotConnected`** (not block-forever). Production callers wait for `Connected()` (#248) before issuing the first Send.
- **`Receive` returns `ErrDisconnected` on conn drop** (#248). Callers re-handshake by observing `Connected()` for the next live conn — `ErrDisconnected` is "your current Receive call returned because the wire dropped, not because data arrived."
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

- **`internal/relay`** (#248, landed): wraps `Send`/`Receive` with `protocol.Envelope` marshal/unmarshal, runs the hello/hello_ack handshake on every `Connected()` signal, classifies WS close `4409` as terminal via `Config.FatalCloseCodes`. See [`features/relay-package.md`](relay-package.md) and [`codebase/248.md`](../codebase/248.md). Per-envelope dispatch (`4401`/`4404` interpretation, `Envelope.ID` monotonicity) lives in a future ticket above the relay layer.
- **`internal/protocol`** (#255, landed): the envelope types this transport's frames will (mostly) carry.

## Dependencies

- `github.com/coder/websocket v1.8.13` — MIT, ~2k LOC, context-first API, native ping/pong via `Conn.Ping(ctx)`, no transitive deps. First network-protocol dep in the project.
- Stdlib: `context`, `errors`, `fmt`, `log/slog`, `math/rand`, `net/http`, `sync`, `time`.

## Out of scope

See [`codebase/247.md`](../codebase/247.md) for the full deferred-list and rationale.
