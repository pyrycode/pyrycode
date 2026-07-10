# Spec #875 — Flush held v2 push queues immediately on relay reconnect

**Ticket:** pyrycode#875 (bug, `size:xs`). Follow-up to #874.
**Not security-sensitive.** This changes only WAKE TIMING. The hold-vs-seal decision
— and therefore every Noise send-nonce increment — stays gated by the #874
`transportDown()` probe downstream of this change; the reconnect edge cannot itself
cause a seal into a dead link. No wire-format change.

## Files to read first

- `internal/relay/v2session.go:2957-3054` — `drainOnce`: the transport-down HOLD (#874),
  the FIFO pop, and the `more`→`drainCh` self-perpetuating re-signal. **This is the pump
  the new wake arm feeds.** Extract: it already re-checks `transportDown()` before the pop,
  so the reconnect arm needs only to wake it — it seals nothing itself.
- `internal/relay/v2session.go:2917-2946` — `Push`: the canonical non-blocking, cap-1,
  drop-on-full `drainCh` re-signal idiom (lines 2933-2936). Copy this idiom into the new
  Run arm; it is the same 4 lines used again in `drainOnce` (3049-3053) and
  `drainReplayOnce` (3115-3118).
- `internal/relay/v2session.go:864-899` — `Run`: the select loop the new arm joins. Note
  the existing `case <-m.drainCh: m.drainOnce(runCtx)` arm — the new arm re-signals that
  channel, it does NOT call `drainOnce` directly.
- `internal/relay/v2session.go:555-584` — `V2SessionConfig` + the #874 `Connected func() bool`
  field doc. The new `Reconnect <-chan struct{}` field sits beside it and follows the same
  "optional; nil ⇒ pre-change behaviour" convention.
- `internal/relay/connection.go:254-284` — `Connection.run()`. Line 273's
  `case <-c.client.Connected():` arm is **the sole consumer** of the transport's fresh-conn
  edge and the exact fan-out point for the new signal.
- `internal/relay/connection.go:100-139, 162-175` — `Connect` + `connectWithClient`: the two
  constructors that must initialise the new `reconnected` channel.
- `internal/relay/connection.go:198-205` — `Connection.Connected() bool` (#874): the model for
  the new `Reconnected() <-chan struct{}` accessor (a thin passthrough over transport state).
- `internal/transport/wssclient.go:337-364` — `Connected()` documents **"Multiple observers are
  NOT supported"** (line 340) and `IsConnected()` is the level poll. This is *why* the manager
  cannot observe `transport.Client.Connected()` directly (see § Design decision).
- `internal/transport/wssclient.go:429-450` — `serve()`: `setConn(conn)` (445, sets `c.conn`
  non-nil) runs BEFORE the `connectedCh` send (448). This ordering is what guarantees
  `transportDown()` reads *up* when the reconnect edge fires.
- `internal/relay/v2session_test.go:4682-4739` — `TestV2Session_Push_HeldWhileTransportDown_ReflushContiguous`
  (the #874 test) + the `newGatedRecorder()` / `gated.up` / `gated.connected` / `driveToOpen` /
  `assertHeldQueued` / `waitForEnvelopes` / `decryptAppFrame` fixtures the new manager-level test
  reuses verbatim.
- `internal/relay/connection_test.go:347-391` — `TestTransportDropPostConnect_Reconnects`: the
  drop-post-connect→reconnect harness the new connection-level fan-out test mirrors.
- `cmd/pyry/relay.go:445-533` — the `V2SessionConfig` literal; line 448 `Connected: conn.Connected`
  is where `Reconnect: conn.Reconnected()` is added.

## Context

#874 made the v2 push drain **hold** queued control envelopes unsealed while the relay
transport is down, rather than burning a Noise send-nonce on a dead link. But a held
envelope flushes only when the *next* `Push` re-signals `drainCh`. With no subsequent push,
a held `modal_shown` sits until something else stirs the queue. During the relay's 30-second
client grace, that latency is the difference between the prompt reaching the still-connected
phone and the daemon's 2-minute deny-on-timeout eating it.

#875 closes the gap: when the transport reconnects, wake the drain immediately — no
intervening `Push` required. The seals still happen in `drainOnce`, in FIFO order, only for
frames the (now-up) transport can carry.

## Design decision: fan out at the Connection layer, do NOT observe the transport directly

The ticket's AC4 says "wire the new seam to `transport.Client.Connected()`." Implemented
literally, that is **unsafe** and the developer must not do it:

- `transport.Client.Connected()` is documented **single-observer** ("Multiple observers are
  NOT supported", `wssclient.go:340`) over a cap-1 drop-on-full channel.
- `relay.Connection.run()` is **already** that sole observer (`connection.go:273`): it consumes
  each fresh-conn edge to re-enter `forwardFrames`. A second observer on the same channel would
  *steal* connect signals from the connection's own frame-forwarding loop — a catastrophic
  regression on the whole relay leg.
- `cmd/pyry` holds only a `*relay.Connection`, never the private `*transport.Client`, so it
  could not reach the raw channel anyway.

So the reconnect edge is **fanned out at the `relay.Connection` layer**: the connection, which
already observes the edge, re-broadcasts it to the manager through a new
`Reconnected() <-chan struct{}` accessor. This is the exact shape #874 used for its level poll
(`transport.Client.IsConnected()` → `relay.Connection.Connected()` → `cmd/pyry`); #875 adds the
edge-signal sibling. The manager stays the sole consumer of `relay.Connection.Reconnected()`,
honouring the single-observer contract one layer down.

## Design

Three production files. No new goroutine, no new lock, no wire change.

### 1. `internal/relay/connection.go` — re-broadcast the reconnect edge

- Add field `reconnected chan struct{}` to `Connection`.
- Initialise `reconnected: make(chan struct{}, 1)` in **both** constructors: `Connect`
  (~line 130) and `connectWithClient` (~line 166). Cap-1 drop-on-full — same discipline as the
  transport's own `connectedCh`.
- In `run()`'s existing `case <-c.client.Connected():` arm (line 273), after the "conn
  established" log and **before** `c.forwardFrames(ctx)` (which blocks), do a non-blocking send:

  ```go
  select {
  case c.reconnected <- struct{}{}:
  default:
  }
  ```

- Add the accessor:

  ```go
  // Reconnected returns a channel that emits once per fresh binary↔relay conn
  // (initial connect and every reconnect). Cap-1 drop-on-full, single observer —
  // the v2 push drain uses it to flush held control envelopes the instant the leg
  // recovers, without waiting for the next Push (#875). Derived from the connection's
  // own (sole) observation of transport.Client.Connected(); the manager must NOT
  // observe the transport channel directly (§ Design decision).
  func (c *Connection) Reconnected() <-chan struct{} { return c.reconnected }
  ```

Behaviour note for the doc comment: the arm also fires on the *initial* connect, not only
reconnects. That is fine — a first-connect signal triggers at most one empty `drainOnce` pass
(nothing queued yet), which is a harmless no-op. Distinguishing first-connect from reconnect
would add state for zero benefit.

### 2. `internal/relay/v2session.go` — optional wake source + one Run arm

- Add to `V2SessionConfig`, beside the #874 `Connected func() bool` field:

  ```go
  // Reconnect, when non-nil, is an edge-triggered signal that fires once per fresh
  // relay transport conn. On each fire, Run re-signals the push drain so a control
  // envelope held while the leg was down (see Connected) flushes the instant the leg
  // recovers, without waiting for the next Push (#875). Production wires
  // (*relay.Connection).Reconnected(). Cap-1 drop-on-full, single observer.
  //
  // Optional: nil ⇒ no new wake source. Run's select arm reads a nil channel, which
  // is never ready, so the drain flushes only on the pre-#875 Push-driven re-signal —
  // byte-identical to the foreground / unwired / existing-test posture. This wakes the
  // drain only; it seals nothing. drainOnce still consults Connected before the pop, so
  // a conn that drops again between this edge and the pop burns no nonce (#874). NOT a
  // security decision.
  Reconnect <-chan struct{}
  ```

- Add one arm to `Run`'s select (after the `case <-m.drainCh` arm):

  ```go
  case <-m.cfg.Reconnect:
      // Fresh transport conn: wake the drain so any #874-held head flushes now
      // (not on the next Push). Re-signal drainCh — do not call drainOnce here — so
      // the existing drain arm owns the single pop path and its FIFO self-re-signal.
      select {
      case m.drainCh <- struct{}{}:
      default:
      }
  ```

  A nil `m.cfg.Reconnect` makes this arm's receive a nil-channel read — permanently
  not-ready, so the select behaves exactly as before. No guard needed.

The developer may factor the 4-line re-signal into a `func (m *V2SessionManager) signalDrain()`
helper called by this arm (and, if they wish, by `Push`/`drainOnce`), **but that refactor is
optional and out of scope** — inlining a 4th copy matches the existing style (Push:2933,
drainOnce:3049, drainReplayOnce:3115). Prefer the inline copy; do not sweep the other three.

### 3. `cmd/pyry/relay.go` — wire the seam

In the `relay.V2SessionConfig{...}` literal (`startRelayV2`, ~line 445), directly below
`Connected: conn.Connected,`:

```go
Reconnect: conn.Reconnected(),
```

## Concurrency model

- **No new goroutine.** The fan-out send runs on `Connection.run()`'s existing goroutine; the
  drain re-signal runs on the manager's existing `Run` goroutine. Both are non-blocking cap-1
  sends into channels the same-side loop drains — neither can wedge its host loop.
- **Transport is up when the edge fires.** `serve()` calls `setConn(conn)` (`c.conn` non-nil)
  *before* it signals `connectedCh` (`wssclient.go:445` then `:448`). So by the time
  `Connection.run()` observes the edge and pokes `reconnected`, `IsConnected()` — hence
  `transportDown()` — reads up. The `drainOnce` the arm triggers therefore *pops*, not holds.
- **No lost wakeup.** `reconnected` and `drainCh` are both cap-1 buffered: a signal raised while
  the consumer loop is mid-work waits in the buffer. A dropped *second* signal (channel already
  full) is harmless — the pending first triggers the drain, and `drainOnce`'s `more`→`drainCh`
  re-signal self-perpetuates until the queue empties. The FIFO flush of N held envelopes needs
  only one surviving reconnect signal.
- **Residual TOCTOU is #874's, unchanged.** If the conn drops again between this edge and the
  pop, `drainOnce` re-checks `transportDown()` and holds again — the 1-frame up→down window is
  the pre-existing #874 residual, not widened here.

## Error handling

No new error paths. The reconnect arm cannot fail (a non-blocking channel send). The fan-out
send cannot fail. Session-level drop classification (`ErrConnNotFound` / `ErrSessionNotOpen` /
seal failure) stays entirely inside `forwardEnvelope`, reached only on the transport-up pop
path, unchanged from #874.

## Testing strategy

Reuse the #874 fixtures; do not build new harness scaffolding.

**Manager-level — flush on reconnect with NO intervening Push** (`v2session_test.go`, mirror
`TestV2Session_Push_HeldWhileTransportDown_ReflushContiguous`):
- Build a test `reconnect := make(chan struct{}, 1)` and pass it as `Reconnect` alongside the
  `gated` recorder's `Connected: gated.connected`. Drive to open with the transport up.
- `gated.up.Store(false)`; `Push` two control envelopes (ids 1, 2) → assert both **held**
  (`assertHeldQueued`, nothing delivered).
- `gated.up.Store(true)`; then `reconnect <- struct{}{}` — **no further `Push`**.
- Assert both held envelopes are delivered exactly once, in FIFO order (ids 1 then 2), and both
  decrypt under `sess.initRecv` (contiguous nonce — a burned/gapped nonce MAC-fails). This is
  the AC3 core: delivery driven by the reconnect signal alone.

**Manager-level — nil Reconnect is inert** (`v2session_test.go`):
- One case building the manager with `Reconnect: nil`: a held envelope stays held until a
  subsequent `Push` re-signals (pre-#875 behaviour). Confirms AC1's "when nil, behaves exactly
  as before." (The bulk of the existing suite already omits the field and must stay green.)

**Manager-level — reconnect while still down does not force a seal** (`v2session_test.go`):
- `gated.up.Store(false)`; `Push` one control envelope → held. Fire `reconnect <- struct{}{}`
  while still down. Assert it stays held (the arm woke the drain, but `transportDown()` held the
  pop — no nonce burned). Guards the "seals nothing itself" contract.

**Connection-level — the fan-out fires** (`connection_test.go`, mirror
`TestTransportDropPostConnect_Reconnects`):
- After a post-connect drop→reconnect, assert `conn.Reconnected()` receives a signal (with a
  bounded timeout). Optionally assert it fires on the *initial* connect too.

Run the manager-level tests under `-race` (they exercise the cross-goroutine `reconnected` →
`Run` → `drainCh` → `drainOnce` path), consistent with the existing push/drain suite.

## Open questions

- **Fold the re-signal into a helper?** Left to the developer as an optional, in-file nicety
  (see § Design 2). Not required; the inline copy is the recommended, minimal change.
- None affecting the contract.
