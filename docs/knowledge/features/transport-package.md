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

    // First selected non-101 HTTP rejection terminates Connect. Empty retries.
    TerminalUpgradeStatuses []int

    // Listed WS close codes terminate Connect at the consecutive threshold.
    // Empty preserves reconnect on every drop.
    FatalCloseCodes []websocket.StatusCode
    // Zero or one terminates on the first listed close; larger values retry
    // below the threshold. A cycle ending without a listed close resets it.
    FatalCloseThreshold int
}

type Client struct { /* opaque */ }
type Generation struct { /* opaque socket identity and lifetime */ }

func (*Generation) Context() context.Context // cancelled when the socket generation retires

// Contains only the numeric HTTP status; Unwrap returns ErrUpgradeRejected.
type UpgradeRejectionError struct { StatusCode int }

func New(cfg Config) *Client

func (*Client) Connect(ctx context.Context) error // blocking; ctx.Err() | ErrClosed | wrapped ErrFatalClose | wrapped *UpgradeRejectionError
func (*Client) Send(frame []byte) error           // ErrNotConnected | ErrClosed | ErrDisconnected | nil
func (*Client) SendContext(ctx context.Context, frame []byte) error // ordered socket completion; cancellation/deadline bounded
func (*Client) Receive(ctx context.Context) ([]byte, error) // ErrClosed | ErrDisconnected | ctx.Err() | frame
func (*Client) Generation() *Generation // live socket snapshot; nil when unavailable
func (*Client) SendGeneration(ctx context.Context, g *Generation, frame []byte) error // ordered completion on exactly g's socket
func (*Client) ReceiveGeneration(ctx context.Context) ([]byte, *Generation, error) // same receive stream, with pump-stamped ownership
func (*Client) Connected() <-chan struct{}        // emits on every fresh conn (#248 addition)
func (*Client) IsConnected() bool                  // synchronous level poll, agrees with Send's live-conn predicate (#874 addition)
func (*Client) DropConn()                          // force-close live conn; reconnect via backoff (#248 addition)
func (*Client) Close() error                      // idempotent

var (
    ErrNotConnected    = errors.New("transport: not connected")
    ErrClosed          = errors.New("transport: client closed")
    ErrDisconnected    = errors.New("transport: connection lost")            // #248
    ErrFatalClose      = errors.New("transport: fatal close code")           // #248; status via websocket.CloseStatus(err)
    ErrUpgradeRejected = errors.New("transport: websocket upgrade rejected") // HTTP handshake failure; status via errors.As to *UpgradeRejectionError
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

`Connect` owns the retry-policy state. With `Config.TerminalUpgradeStatuses`
unset, HTTP upgrade failures retry through cancellable backoff. A selected
non-101 rejection (for example, 401) ends the loop on its first occurrence,
either at initial dial or after a live connection drops; there is no further
dial or threshold wait. Unselected statuses, including 5xx, and network failures
continue to retry. A malformed HTTP 101 handshake remains retryable even if 101
is listed in the policy.

Configured fatal WebSocket closes, including 4409, use
`Config.FatalCloseThreshold` across both dial and established-connection paths.
Below the threshold, `retryFatalClose` warns and sleeps through the backoff
ladder; at the threshold, `Connect` returns `ErrFatalClose`. Upgrade success
alone does not reset this consecutive counter: a cycle ending without a listed
close does. The counter is independent of the backoff attempt counter, which
resets after at least 60s uptime. Terminal HTTP rejection bypasses the close
counter entirely.

`Client.Send` and `Client.SendContext` share the same unbuffered `sendPump`
admission path. `Send` returns when accepted; `SendContext` waits for the pump's
socket-write acknowledgement. Its caller context and `WriteTimeout` bound both
admission and I/O; after admission it joins completion, leaving no queued or
active write when it returns. Each request retains the socket identity captured
for admission, so it cannot migrate to a reconnect. Pump shutdown cancels active
I/O even when the caller's context remains live.

Cancellation or deadline expiry before admission is local to the caller and
does not retire an otherwise healthy socket. After acceptance, `sendPump`
synchronously checks the original caller context, then the bounded write and
pump contexts and any explicit generation context immediately before socket
writing, including after `transport.send.accepted`. Caller cancellation takes
precedence, preserving `context.Canceled` or `context.DeadlineExceeded`. If a
check rejects the write, the pump disposes the request without publication,
closes that exact socket generation with `CloseNow`, acknowledges completion
once with a non-nil sanitized error, and exits. A failed socket write likewise
retires the socket before acknowledgement; `serve` joins all three pumps before
reconnecting. A request whose captured socket differs from the pump's socket
completes with `ErrDisconnected` without closing the new socket.

Passing an already-cancelled context to `websocket.Conn.Write` cannot itself
fence publication. A parent's `Done` closure also does not join cancellation of
its descendants: native cancellation closes the parent channel before walking
its children. Checking only the derived write context can therefore allow a
write after the caller is already cancelled. `sendRequest.callerCtx` retains
the original caller for the direct fence; the independent pump-context check
avoids depending on when `context.AfterFunc` runs during shutdown. Retiring the
socket after discarding a sealed frame preserves the relay's
Noise stream boundary; session removal alone cannot stop late transport output.

A separate direct socket writer can overtake a frame already accepted by
`Send`, even if both writes serialize on the WebSocket writer lock. For Noise,
that can deliver nonce 1 before nonce 0, or application output before the initial
`noise_resp`. Socket completion and writer serialization cannot replace shared
admission ordering. `Connection.SendContext` adapts this contract; the relay
still owns sender IDs and [sealing order](v2-session-manager-concurrency.md#hosted-discovery-and-ordered-delivery).

### Socket generation ownership

A shared receive channel cannot identify the originating socket after replacement,
and neither a reconnect edge nor successful write completion establishes inbound
ownership. `Generation` captures the owning client, socket and cancellation
lifetime. `serve` installs it before starting pumps and publishes connectivity
after starting them. Pump exit, replacement, `DropConn`, `Close` and lifecycle
cancellation retire it. `Client.Generation` snapshots it without consuming
`Connected` notifications.

`SendGeneration` pins admission and ordered write completion to that generation;
retired or foreign generations fail rather than moving a request to a replacement
socket. `recvPump` stamps each frame before handing it to the shared stream,
so delayed receipt retains its original ownership. `ReceiveGeneration` and
`Receive` consume the same ordered stream and must not have separate observers.
The [push feedback API](relay-package-push-wake.md#correlated-feedback-and-connection-lifetime)
uses this identity alongside request IDs; synchronous lifetime checks prevent
notification while cancellation callbacks are still awaiting cleanup.
`TestGenerationAdmission` and `TestGenerationReceiveOwnership` hold admission
and pump delivery across replacement to prove these boundaries.

### Pump lifecycle and shutdown

```
caller's goroutine: client.Connect(ctx)
  for {
    dial → selected non-101 rejection: return
         → other failure: classify close threshold, then backoff and repeat
    on success: serve(ctx, conn) {
      go recvPump(ctx, conn) → recvCh
      go sendPump(ctx, conn) ← sendCh
      go pingLoop(ctx, conn)
      setConn(conn)     // AFTER the three go statements (lessons.md:290)
      wait for first errCh return; bounded close-status grace; cancel; drain
    }
  }
```

Shutdown sequence (either `Close()` or `ctx` cancellation):

1. `Close` closes `closeCh` exactly once (`sync.Once`), force-closes any live conn with `1000` (normal closure).
2. Pumps observe `ctx.Done()` or read/write fails on the closed conn → return.
3. `serve` drains all three pump returns → returns to `Connect`.
4. `Connect` observes `ctx.Err() != nil` (or `closeCh` closed) → returns `ctx.Err()` / `ErrClosed`.

## Configuration and usage

- **`Config.Headers` is caller-owned.** The package does not construct the `X-Pyry-Server-Id`, `X-Pyry-Binary-Version`, or `X-Pyry-Protocol-Versions` headers; the relay layer (#248) builds them and passes them in. This package does NOT log `Config.Headers` — downstream consumers must not either.
- **`Config.WriteTimeout`** bounds per-frame send I/O. It is NOT an inactivity timeout — the heartbeat is the inactivity contract. Left unset (non-positive), `New` substitutes `defaultWriteTimeout` (10s, matching the production `internal/relay` caller) — a zero value would otherwise make `sendPump`'s `context.WithTimeout(ctx, 0)` instantly deadline-exceed every write, reconnect-looping the client (#916).
- **`Config.Logger`** is required; nil panics at `New` time. Per-CODING-STYLE injected, not global. Lifecycle events logged at Info (dial, connected, disconnected); pong timeout logged at Warn.
- **`Config.URL`** is the full WSS URL (e.g. `wss://relay.pyrycode.dev/v1/server`). TLS verification uses Go's `crypto/tls` defaults (TLS 1.2 min, hostname-checked against URL host, system root CAs).

## Error model

| Method | Returns |
|---|---|
| `Connect` | `ctx.Err()` (ctx cancellation), `ErrClosed` (Close called), wrapped `*UpgradeRejectionError` for a selected non-101 HTTP status, or wrapped `ErrFatalClose` when the configured consecutive close threshold is reached. Never `nil`. |
| `Send` | `nil` (frame queued to send pump — not "written on the wire"), `ErrNotConnected` (no live conn), `ErrDisconnected` (live conn dropped while Send was blocked enqueuing — #248), `ErrClosed`. |
| `SendContext` | Admission sentinels as for `Send`, context cancellation/deadline, or a sanitized socket-write error; `nil` means the accepted write completed. |
| `Receive` | `(frame, nil)`, `(nil, ctx.Err())`, `(nil, ErrClosed)`, `(nil, ErrDisconnected)` (#248 — the underlying conn dropped while Receive was blocked, OR no conn is currently live; observe `Connected()` to learn when a fresh conn becomes available). |
| `Close` | Always `nil`. Idempotent. |

`Send` returning `nil` means "queued to the send pump"; the pump may still fail to write (then the conn drops and reconnect kicks in). The caller cannot distinguish "frame hit the wire" from "frame queued and then dropped" — that's by design for an async transport. Re-issue on the next live conn if needed.

### HTTP status recovery and diagnostic safety

Select terminal HTTP statuses with `Config.TerminalUpgradeStatuses`, for example
`[]int{http.StatusUnauthorized}`. Recover the numeric status without matching
text:

```go
var rejection *transport.UpgradeRejectionError
if errors.As(err, &rejection) {
    log.Error("relay upgrade rejected", "http_status", rejection.StatusCode)
}
```

`UpgradeRejectionError` holds only `StatusCode`; its `Unwrap` returns only
`ErrUpgradeRejected`. Thus `errors.Is(err, ErrUpgradeRejected)` remains valid,
and a terminal HTTP rejection is distinct from `ErrFatalClose` and context
cancellation. HTTP policy selection belongs to the caller; this transport
change does not activate production daemon admission. See the declared
[admission and reconnect contract](../../protocol-mobile.md#admission-failure-and-reconnect)
and the [relay consumer](relay-package.md). Relay policy selection and private
daemon configuration/startup wiring are owned by #3216 and #3217 respectively;
enforcement requires both daemon and Relay implementations and operator activation.

`safeTransportError` rebuilds errors at dial, receive, send and ping boundaries,
including the injected dial boundary in `Connect`. Diagnostics use fixed
operation categories (`dial`, `recv`, `send`, `ping`), numeric HTTP/close codes,
known transport sentinels and canonical cancellation/deadline values. Logs use
numeric `http_status`, `close_status` or `status` attributes where applicable.
Raw HTTP response objects, headers and bodies, original library errors and
peer-authored WebSocket close reasons are discarded from both logs and every
reachable error-chain branch. Wrapping the original error beneath safe text
would still expose reflected credentials to callers inspecting its causes.

Close errors are reconstructed as `websocket.CloseError` with the observed code
and an empty reason. `websocket.CloseStatus` and `errors.Is` matching for known
sentinels and cancellation survive, including on returned `ErrFatalClose`
errors. Arbitrary library error identities and text do not. The same boundary
applies to ping warnings, below-threshold close warnings and write
acknowledgements delivered to `SendContext`; `serve` still prefers a returned
close status and retains its bounded grace when pumps race.

### #248 additions

The deferred-decision note in #247's spec assigned four additions to #248 (the relay layer). All are purely additive:

- **`Connected() <-chan struct{}`** — buffer-1 with drop-on-full emit. Fires AFTER `setConn` in `serve` so consumers waking on `Connected` find `Send`/`Receive` already wired against the live conn (same precedent as `docs/lessons.md:290`). Single-observer — multiple observers can miss events by design.
- **`Config.FatalCloseCodes` + `ErrFatalClose`** — close codes in this list halt `Connect`'s reconnect loop when `Config.FatalCloseThreshold` consecutive observations are reached. The check fires on BOTH the post-`serve` path AND the post-`dialFn` path (when `coder/websocket` surfaces a close error directly from `Dial` rather than from a subsequent `Read` — happens when the peer closes during/immediately after the WS upgrade). The post-`serve` path also walks the three pump-return errors and PREFERS one with a recognizable `websocket.CloseStatus` (recvPump observes the `CloseError` but sendPump/pingLoop can return generic `use of closed network connection`; without preferring the close-status error, fatal-close classification flakes under `-race`). To keep the `CloseError` actually present in `errs[]` for the preference loop to pick, `serve` grants a bounded `closeFrameGrace = 50ms` wait when the first pump return has no close status before calling `cancel()` (#290) — without the grace, `coder/websocket`'s `prepareRead.done()` deferred override replaces `recvPump`'s pending `CloseError` with `ctx.Err()` when ctx fires mid-Read, and no slot of `errs[]` carries the status. Grace is wire-internal (no `Config` knob; tests substitute via `testOpts.closeFrameGrace`).
- **`ErrDisconnected` + per-conn `connDone`** — `connDone` is per-conn, pre-closed at construction (`Receive`/`Send` before `Connect` return `ErrDisconnected` immediately rather than blocking forever), replaced with a fresh open channel inside `serve` before pumps install, closed in `serve`'s deferred teardown. `Receive` and `Send` capture `connDone` once under `connDoneMu` then select — a serve iteration that replaces `c.connDone` between the capture and the select cannot accidentally wake the caller on the new channel. Without this, a `Receive` blocked when the conn drops would stay blocked until the next conn delivers a frame, wedging the relay's `forwardFrames` receive loop above this layer permanently.
- **`DropConn()`** — force-closes the live conn (if any) via `conn.CloseNow()` (abrupt 1006-equivalent, no close-frame round-trip). The serve loop sees the closed conn, returns to the dial loop, reconnects via backoff. Does NOT halt the dial loop. Idempotent (no-op when no conn is live). `CloseNow` over `Close(status, reason)` so the caller is not blocked for up to 10s on a close handshake when the only purpose is to recycle the conn. It exists for a consumer wanting to recycle a transport-healthy-but-application-stuck conn; the relay package was its original caller (the binary↔relay handshake-failure path), which #582 retired — `DropConn` now has no caller in `internal/relay` but remains for future consumers.

### #874 addition — `IsConnected() bool`, a synchronous level poll

`IsConnected() bool` reports whether a live conn exists and the client is not closed — a cheap synchronous poll for a caller that must decide **before** a side-effecting operation, in contrast to `Connected() <-chan struct{}` (edge-triggered, fires once per fresh conn, meant for re-running an application handshake). Contract: `false` iff `closeCh` is closed OR `c.conn == nil` (checked under `c.mu`, mirroring `Send`'s own guards); `true` otherwise. Agrees with `Send`'s live-conn predicate — a `false` result reliably means `Send` would return `ErrNotConnected`/`ErrClosed`; a `true` result is best-effort (the conn can still drop before the next `Send`, an inherent TOCTOU with any pre-check that doesn't hold a lock across the send). `Close` does not nil `c.conn` (only closes `closeCh`), so `IsConnected` gates on `closeCh` too — a closed client reads down rather than stale-true.

Added for the v2 push drain's pre-seal transport probe ([`relay.V2SessionConfig.Connected`](v2-session-manager.md#transport-down-hold-on-the-push-drain-874--connected-probe--transportdown), #874): the drain must learn the transport is down **before** sealing a queued envelope (the Noise send nonce is burned by the seal, not the send), and the existing `Connected()` channel is edge-triggered — wrong shape for a "is it up right now" check. `internal/relay/connection.go`'s `Connection.Connected() bool` is a one-line passthrough.

### #631 additions — loud on a rejected upgrade

A non-101 HTTP response to the WS upgrade (for example, a `404` because the
relay URL has the wrong path) gets a first-failure warning per outage. The
current classification and policy are:

- **Numeric classification (`realDial`).** A failed `websocket.Dial` with a
  non-nil response produces a sanitized `*UpgradeRejectionError` carrying only
  `resp.StatusCode`; `errors.Is(err, ErrUpgradeRejected)` remains true. A non-nil
  response can also be HTTP 101 with invalid handshake headers. Response
  presence alone therefore cannot select a terminal rejection. Such a malformed
  101 remains retryable even if configured as terminal; the library error may
  contain reflected header values and must be discarded. A network failure
  with no response gets a fixed `dial` category and any supported safe
  classifications, without retaining the original error.
- **Loud-once-per-outage WARN (`Connect`).** For retryable upgrade failures, a
  loop-local `warnedUpgrade` gates one WARN with the hint “verify the relay URL
  path is correct”; later attempts use INFO. Successful connection re-arms the
  warning for the next outage. Logs carry the sanitized error and numeric
  `http_status` (0 if no typed HTTP status is available), without response data.
  The hint contains no protocol-specific path: the layer that knows about
  `/v1/server` is [relay](relay-package.md#configuration-constraints). Network
  failures remain at INFO.
- **Caller-selected termination.** With `TerminalUpgradeStatuses` unset, even
  401 retries, preserving recovery if the relay or config is fixed live. With
  a policy, the first selected non-101 status returns immediately, before the
  warning/backoff branch. HTTP rejection does not use `FatalCloseCodes` or its
  consecutive threshold. See [HTTP status recovery and diagnostic
  safety](#http-status-recovery-and-diagnostic-safety) for the typed error
  contract and the separate production admission rollout.

## Edge cases and gotchas

- **`Send` before first dial returns `ErrNotConnected`** (not block-forever). Production callers wait for `Connected()` (#248) before issuing the first Send.
- **`Receive` returns `ErrDisconnected` on conn drop** (#248). Callers re-establish by observing `Connected()` for the next live conn — `ErrDisconnected` is "your current Receive call returned because the wire dropped, not because data arrived."
- **Inbound frames are capped at 1 MiB** via `conn.SetReadLimit`. A peer sending more trips `read limit exceeded`, the recv-pump returns, the dial loop reconnects. This is the only DoS knob in this layer.
- **`recvCh` and `sendCh` are unbuffered.** A slow `Receive` consumer applies back-pressure all the way to the relay's write buffer. The dispatcher (#248) must keep `Receive` drained continuously.
- **`math/rand` for jitter, not `crypto/rand`.** Jitter is anti-thundering-herd noise; predictability of the exact delay enables no attack (a hostile relay already controls connection-drop timing).
- **`1011` not `1006` on reconnect-close.** `1006` is received-only; this client cannot actively send it. Spec wording "1011 or 1006" describes the two observable peer states, not a choice.
- **`Config.ReadTimeout` is not present** (was in the original AC body but dropped by architect's security review). The heartbeat is the inactivity contract; a separate ReadTimeout would shadow it.

## Test surface

To expose an enqueue-to-write ordering bypass, pause after pump acceptance and
before writer acquisition. Holding the writer lock blocks both paths and can
hide the defect. `TestHostedDiscoveryRealTransportOrdering` in relay drives
authenticated real-socket legacy-to-hosted and handshake-to-hosted transitions,
then decrypts output and checks IDs/correlation. Its retirement counterpart
requires pending context sends to exit before releasing the pump and uses a
later transport marker to prove no abandoned reply remains. Transport's
`TestClientSendContextDelivery` tests accepted I/O cancellation;
`TestClientSendContextPumpShutdown` keeps the caller live to distinguish pump
shutdown from caller cancellation. These are controlled scheduling proofs,
not timing-only assertions.

`TestClientSendContextAcceptedCancellation` holds `transport.send.accepted`
before writing, then cancels the caller, expires its deadline or shuts down the
pump. In cancel/deadline modes, `testAcceptedPropagation` uses the context
`AfterFunc` contract to hold descendant cancellation while the original caller
is cancelled and both derived write contexts remain live. Releasing acceptance
must still return the caller's exact cancellation/deadline classification;
shutdown requires a non-nil error with a live caller. The sender cannot return
while disposal is gated. Cleanup releases and joins the cancellation callback,
sender and connection goroutines with bounded waits.

Client upgrade completion does not establish server-handler registration order:
the first handler can finish `websocket.Accept` after a replacement handler.
Numbering peers only after `Accept` could make the retirement observer watch the
replacement socket and stall even when the affected socket closed. The fixture
therefore waits for the original handler's `registered` acknowledgement before
submitting any send that could trigger reconnect. It observes that peer's
retirement before checking zero publication; cancel/deadline then require a
distinct `Generation` and delivery of only the new-generation marker. The old
request must never migrate or resume. `TestClientSendContextPreAdmissionCancellation`
separately requires the healthy socket to remain usable without reconnecting.
Both causes have controlled failing baselines: held descendant propagation
returned nil and stalled cancel/deadline retirement, while delayed original-peer
registration selected upgrade 2 and stalled its retirement observer. See the
[diagnosis and Linux race evidence](../../specs/architecture/3496-accepted-send-cancellation.md#revisions).

Diagnostic safety tests must inspect every single-error and multi-error unwrap
branch, plus captured log attributes; checking only `Error()` can miss an
unsafe cause or response object. `assertSafeTransportError` and
`assertSafeTransportLogs` exercise this with synthetic reflected credentials in
HTTP bodies/headers and peer close reasons. Preserve the reason-bearing error
at the boundary being tested: reading a peer close before invoking ping or send
can replace it with a generic closed-socket failure. A test can then stay green
with that pump's sanitizer removed. The ping/send cases in
`TestSafePumpDiagnostics` have this coverage limitation; a controlled test of
each boundary must fail when its sanitizer is removed. The classification
helper's independent test proves reason removal but cannot prove every caller
uses it. See [verification practices](development-verification-prove-that-tests-distinguish-the-change.md#prove-that-tests-distinguish-the-change) and the
[review finding](https://github.com/pyrycode/pyrycode/pull/3219#issuecomment-6115369696).

`internal/transport/wssclient_test.go` (~520 LOC, stdlib + `coder/websocket`):

- `newClientForTest(t, cfg, testOpts)` — shorter cadence constants + deterministic seed for sub-second tests.
- `newTestRelay(t)` — `httptest.NewServer` with a `coder/websocket.Accept` upgrader, ping counter, pong suppression, force-close, optional echo loop. `relayCtrl` registers a conn (and signals `connectedCh`) only *after* `Accept` returns — later than the client's own dial, which unblocks as soon as the 101 response is written. A test that force-closes immediately after seeing the client connect must wait on `relayCtrl.connectedCh` too, or it can race `ForceClose` against an empty conn slice. Waiting on `relayCtrl.connectedCh` is necessary but not sufficient for a *second* drop in the same test: `Client.Connected` is buffer-1 drop-on-full, so the client-side gate must also drain before the drop — otherwise the post-drop read can consume the stale first-conn token and pass vacuously instead of observing the reconnect. Three call sites now apply this ordering (`TestConnected_DropsWhenObserverSlow`, `TestBackoff_ResetAfterStableConnection`, `TestConnected_FiresOnEveryConnect`); a fourth is the point at which a shared `relayCtrl` wait helper stops being premature.

Pinned behaviour:

- `TestBackoff_Sequence` — attempts 1..10, base in `[base*0.8, base*1.2]`. Caps at 30s from attempt 6.
- `TestBackoff_ResetAfterStableConnection` — table-driven, two rows (stable uptime resets the counter to 1 / brief uptime keeps it climbing). Verified off the `"transport: connected"` log record (`connectedAttempt`/`waitConnectedAttempt` helpers), not a dial-interval bound: the post-serve path redials immediately with no backoff sleep, so no wall-clock gap between a healthy disconnect and the next dial carries a term the attempt counter controls. A flaky duration bound on this path is a sign to trace which sleep it actually contributes, not to widen the bound.
- `TestPing_FiredAt30s` — ping cadence (skipped under `-short`; uses 50ms test interval).
- `TestPongTimeout_TriggersReconnect` — pong-suppressed relay → second dial within ~80ms test pong timeout.
- `TestClose_OnContextCancel` — `cancel(ctx)` returns `Connect` with `context.Canceled` within 1s.
- `TestClose_Idempotent` — `Close()` called twice returns nil both times.
- `TestSmoke_HttptestEchoServer` — full handshake + Send/Receive echo + ≥1 ping + `Close` → `Connect` returns `ErrClosed`/`context.Canceled`.
- `TestSend_ReturnsErrNotConnected_BeforeConnect` — `Send` before `Connect` doesn't block.
- `TestSend_ReturnsErrClosed_AfterClose` / `TestReceive_ReturnsErrClosed_AfterClose` — post-Close sentinels.
- `TestNew_PanicsWithoutLogger` — pins the "Logger is required" contract.
- `TestAwaitCloseStatus_GraceBranchPreservesCloseError` (#290) — three flat sub-tests against the `awaitCloseStatus` helper: close-first skips the grace, non-close-first lets a subsequent CloseError land within grace (the regression-catching case), non-close-first with no follower expires after the grace deadline. Pins the contract for the close-frame-race grace branch independently of goroutine scheduling.
- `TestRealDial_UpgradeRejected` (#631) — `realDial` against an `httptest` handler that 404s (never upgrades) → error `errors.Is` `ErrUpgradeRejected` and contains the status.
- `TestRealDial_NetworkFailureNotUpgradeRejected` (#631) — `realDial` against a reserved-then-released port (refused, `resp == nil`) → fixed `dial` failure category, **not** `ErrUpgradeRejected`. Response-bearing handshake failures also include malformed 101 responses, covered by `TestSafeUpgradeDiagnostics`.
- `TestConnect_LoudOnFirstUpgradeReject` (#631) — `dialFn` returns an `ErrUpgradeRejected`-wrapped error every attempt; a recording `slog.Handler` asserts exactly **one** WARN (carrying the actionable path hint) on the first attempt with subsequent attempts demoted to INFO — pins AC#1's "loud on the first failed upgrade, not buried".
- `TestTerminalUpgradeRejection` — selected 401/403 returns a typed numeric status after exactly one initial dial or two dials when reconnecting; even a high fatal-close threshold cannot postpone HTTP termination.
- `TestUpgradeRetryRecovery` / `TestUpgradeRetryShutdown` — default warning re-arming, unselected 403/503 and network recovery, malformed 101 retry despite policy selection, and cancellation/Close during rejection backoff.
- `TestSafeTransportErrorClassifications` / `TestSafeUpgradeDiagnostics` — supported sentinels, cancellation and numeric codes survive reconstruction; raw response objects, reflected HTTP data and close reasons do not remain in the reachable error chain.
- `TestSafeFatalCloseDiagnostics` — safe below-threshold warnings and fatal returns across dial/serve paths; an upgrade success alone does not reset consecutive 4409 observations. Existing `TestFatalCloseThreshold_DialPathCounts` and `TestFatalCloseThreshold_CounterResetsOnHealthyConn` cover cross-path counts and reset on a non-fatal cycle, separately from uptime/backoff reset.
- `TestClient_IsConnected` (#874) — `false` before `Connect`; `true` once a live conn is established (agrees with `Send` no longer returning `ErrNotConnected`); `false` after `Close` (the `closeCh` gate, despite `c.conn` staying non-nil).
- `TestConnected_FiresOnEveryConnect` (#1803) — four ordered gates, not two: wait for the relay to register conn #1, drain `Connected()`, force-close, then wait for the relay to register conn #2 *before* re-checking `Connected()`. Splitting the reconnect wait from the signal check gives a red run a single cause — `client never reconnected to the relay` (harness/dial fault) is now unreachable from the same assertion as `Connected did not fire after reconnect` (a real signal regression), where before both collapsed into one message. Confirmed non-vacuous by mutant: a `sync.Once`-gated `connectedCh` push (fires once, never again) reddens this test at the signal gate specifically, not the reconnect gate.

## Consumers and roadmap

- **`internal/relay`** (#248, landed): wraps `Send`/`Receive` with `protocol.Envelope` marshal/unmarshal, treats the conn as established on each `Connected()` signal and goes straight to forwarding (the binary↔relay `hello`/`hello_ack` handshake was retired #582), classifies WS close `4409` via `Config.FatalCloseCodes` and the consecutive `FatalCloseThreshold` (8), allowing a stale relay claim to clear before halting a persistent conflict. See [`features/relay-package.md`](relay-package.md) and [`codebase/248.md`](../codebase/248.md). Per-envelope dispatch (`4401`/`4404` interpretation, `Envelope.ID` monotonicity) lives in a future ticket above the relay layer.
- **`internal/protocol`** (#255, landed): the envelope types this transport's frames will (mostly) carry.
- **`internal/relay.V2SessionManager`** (#874, landed): via the `Connection.Connected` → `Client.IsConnected` passthrough, gates the v2 push drain's pre-seal transport-down probe — see [`features/v2-session-manager.md`](v2-session-manager.md#transport-down-hold-on-the-push-drain-874--connected-probe--transportdown).

## Dependencies

- `github.com/coder/websocket v1.8.13` — MIT, ~2k LOC, context-first API, native ping/pong via `Conn.Ping(ctx)`, no transitive deps. First network-protocol dep in the project.
- Stdlib: `context`, `errors`, `fmt`, `log/slog`, `math/rand`, `net/http`, `sync`, `time`.

## Out of scope

See [`codebase/247.md`](../codebase/247.md) for the full deferred-list and rationale.
