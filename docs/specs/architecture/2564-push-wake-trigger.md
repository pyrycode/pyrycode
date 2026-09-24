# #2564 — ask the relay to wake an absent phone on turn end and permission prompts

## Files read

- `internal/relay/connection.go` → `Connection.Send`, `Connection.CloseConn` — `CloseConn` is the precedent for a relay-addressed send; both return the transport sentinels unchanged.
- `internal/protocol/envelope.go` → `RoutingEnvelope` — `conn_id` and `frame` carry no `omitempty`, so the `push_wake` shape cannot ride this type without emitting both keys. It needs its own marshal type.
- `docs/protocol-mobile.md` § `push_wake` — the exact wire shape `{"push_wake":{"platform":"fcm","token":"…"}}`, relay is the only reader, token never logged.
- `internal/relay/v2session.go` → `ActiveConn`, `handleActiveConns`, `ActiveConns` — the conn snapshot; `DeviceName` is hello-authored and unvalidated. `ActiveConns` returns nil on ctx cancellation.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` token-accept tail — `s.device` is set before `s.state = V2StateOpen`, so every enumerable session has an authenticated device.
- `internal/devices/device.go` → `Device.TokenHash`, `Device.Platform`, `Device.PushToken`, `HashToken`; `internal/devices/registry.go` → `Registry.List` (copying, mutex-guarded, any goroutine).
- `internal/relay/handlers` → `RegisterPushToken` — the only writer of `PushToken`; the token is unbounded (envelope cap only) and deliberately never logged.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle` (`turnevent.TurnEnd` arm), fields `hist` / `usageRec` — the assigned-after-construction pattern for optional dependencies (86 constructor call sites).
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface`, `broadcast`, fields `questions` / `toolCallInFlight` — the live `modal_shown` fan-out and the same post-construction pattern.
- `internal/relay/v2session_modal.go` → `reconcileModals` — reconnect replay pushes directly from the relay package and never passes through `streamApprovalBridge.broadcast`, so it cannot reach the trigger.
- `cmd/pyry/relay.go` → `startRelayV2` — the one place that holds `conn`, `registry`, `mgr`, the bridge and the emitter.
- `internal/relay/v2session_client_identity_test.go` → `openWithIdentity` — real-handshake helper the new snapshot assertion reuses.

Overlap check: only `origin/feature/449` (closed ticket, stale since 2026-05) touches `internal/relay/v2session.go`. No dependency.

## Context

Backgrounded phones drop their relay connection. The relay now sends an FCM data message when the daemon asks with a `push_wake` envelope (pyrycode-relay #132, #133). The daemon stores each device's push token but never uses it. This ticket adds the daemon-side trigger: on a turn end or a live permission prompt, wake each FCM device that has no open session, at most once per 30 s per device.

## Design

### `internal/relay` — two small surfaces

1. `func (c *Connection) SendPushWake(platform, token string) error` in `connection.go`. Marshals an unexported `pushWakeEnvelope{PushWake pushWakeRequest}` (`json:"push_wake"`, fields `platform` and `token`) through a pure helper `marshalPushWake(platform, token string) ([]byte, error)`, then `c.client.Send(raw)`. Returns the transport sentinels unchanged. A marshal failure returns a fixed-text error that does not wrap the encoder error, so no error from this method can carry the token. Never sealed; no `conn_id`, no `frame` key.
2. `ActiveConn.DeviceTokenHash string` in `v2session.go`: the `TokenHash` of the device the handshake authenticated (`s.device`), filled in `handleActiveConns` (empty only if `s.device` were nil, which the accept path rules out). Daemon-authored, but credential-derived: same MUST-NOT-log obligation as the other fields. This is what "belongs to that device" is decided on — never `DeviceName`.

### `cmd/pyry/push_wake.go` — `pushWaker`

```go
const pushWakeWindow = 30 * time.Second

type pushWaker struct {
    conns  interface{ ActiveConns(context.Context) []relay.ActiveConn }
    devs   interface{ List() []devices.Device }
    send   func(platform, token string) error
    now    func() time.Time
    logger *slog.Logger
    trig   chan struct{}        // cap 1
    last   map[string]time.Time // TokenHash → last successful wake; run goroutine only
}
func newPushWaker(conns, devs, send, logger) *pushWaker
func (w *pushWaker) Trigger()           // nil-receiver no-op; non-blocking send on trig, drop if one is pending
func (w *pushWaker) run(ctx context.Context) // loop: ctx.Done → return; trig → wakeAbsent(ctx)
func (w *pushWaker) wakeAbsent(ctx context.Context)
```

`wakeAbsent` does one pass:

1. Snapshot `conns.ActiveConns(ctx)` into a set of non-empty `DeviceTokenHash`. **If `ctx.Err() != nil` after the snapshot, return without sending**: a cancelled snapshot is nil and would otherwise read as "nobody connected" and wake every device at shutdown.
2. `now := w.now()`; prune `last` entries at or past the window (bounds the map to recently woken devices, including removed ones).
3. For each `devs.List()` device: skip unless `Platform == "fcm"` and `PushToken != ""`; skip if its `TokenHash` is in the open set; skip if `now - last[TokenHash] < pushWakeWindow`. Otherwise `send("fcm", PushToken)`. On success stamp `last[TokenHash] = now` and log Debug `event=push_wake.sent`. On failure log Debug `event=push_wake.send_err` with **no err field and no device field**, do not stamp, do not retry.

The pending cap-1 trigger coalesces bursts; the window is what enforces "one wake per device per 30 s". A trigger that lands while a pass runs is kept (buffer slot) and runs one more pass against the then-current state.

### Triggers — reuse the two fan-out points

- `interactiveTurnEmitterV2` gains `waker *pushWaker`, assigned after construction like `hist` / `usageRec` (nil ⇒ no wake, keeping the 86 test constructions unchanged). The `turnevent.TurnEnd` arm calls `e.waker.Trigger()` after `endTurn()`, i.e. after the turn fan-out.
- `streamApprovalBridge` gains `waker *pushWaker`, assigned after construction like `questions`. `Surface` calls `b.waker.Trigger()` after `b.broadcast(protocol.TypeModalShown, …)`. The question arm, the dismissal broadcasts and `reconcileModals` do not trigger.
- `startRelayV2` builds `waker := newPushWaker(mgr, registry, conn.SendPushWake, logger)` after `mgr`, starts `go waker.run(ctx)`, and assigns it to the bridge (inside the `w.approvals != nil` block) and the emitter (inside the stream branch), both before the goroutines that read those fields start.

## Concurrency model

- One new goroutine, `pushWaker.run`, started in `startRelayV2` and exiting on the daemon ctx. It is the only reader/writer of `last`.
- `Trigger` is called from the emitter's producer goroutine and the control-server handler goroutines. It is a non-blocking channel send: the turn and modal fan-out never wait on a snapshot, a registry read or a relay write.
- `ActiveConns` is called from `run`, never from the manager's Run goroutine, so the snapshot funnel cannot self-deadlock.
- `Registry.List` is mutex-guarded and copies; `Connection.SendPushWake` goes through the transport's own send path.

## Error handling

- Relay disconnected (`ErrNotConnected` / `ErrDisconnected` / `ErrClosed`): logged content-free, dropped, not stamped — the next trigger may try again.
- Older relay: drops the envelope, connection stays open (spec). Nothing to handle.
- Shutdown: `run` returns on ctx; a pass in flight bails after the snapshot.

## Testing strategy

`internal/relay`:
- `marshalPushWake` produces exactly `{"push_wake":{"platform":"fcm","token":"tok"}}` — no `conn_id`, no `frame`.
- `SendPushWake` before connect returns `transport.ErrNotConnected`.
- Real handshake via `openWithIdentity` whose hello claims another device's name: `DeviceTokenHash == devices.HashToken(v2TestToken)`.

`cmd/pyry/push_wake_test.go` (fake lister / fake devices / recording send, injected `now`, calls `wakeAbsent` directly):
- Eligibility table: fcm+token+absent → one wake with that token; open session → none; `apns` → none; empty token → none; open conn whose hello `DeviceName` equals the absent device's name but whose `DeviceTokenHash` is someone else's → the absent device is still woken.
- Coalescing: wake at t0; t0+29 s → none; a second device first eligible at t0+29 s → woken; t0+30 s → first device woken again.
- Send failure: fake returns an error whose text embeds the token; the log buffer never contains the token; the device is not stamped (next pass retries).
- Cancelled ctx → no sends.
- `Trigger` on nil receiver is a no-op; two `Trigger` calls without a reader do not block.

Trigger wiring:
- Emitter: `TurnEnd` inside a turn leaves one pending trigger; a `TextChunk` alone leaves none.
- Bridge: `Surface` of a permission request leaves one pending trigger; its retire (a `modal_dismissed`) adds none.

## Size

Six production files (one over the five-file line) — the relay surface has exactly one consumer, so the floor rule keeps it in this ticket, as the ticket body states. Estimated ~550 lines total.

## Open questions

- Should a failed send count toward the window? Resolved: no — the AC's window starts "after a wake is sent", and a disconnected relay did not send it.

## Documentation handoff (pending — documentation stage)

- `docs/knowledge/features/relay-package.md`: record the daemon-side trigger — the two waking events (`TurnEnd`, live `modal_shown` from `streamApprovalBridge.Surface`), per-device 30 s coalescing, an open session suppresses the wake, matching is by the authenticated device (`ActiveConn.DeviceTokenHash`), never the hello name.
- `docs/protocol-mobile.md` § `push_wake`: the line "See pyrycode-relay#130 (relay-side send) and #2564 (daemon-side trigger)" should name the shipped relay tickets (pyrycode-relay #132, #133).

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the suppression decision reads only `ActiveConn.DeviceTokenHash`, filled from `s.device` which `handleNoiseInit` binds after the token validated; the hello-authored `DeviceName` is never consulted. A phone cannot suppress another device's wake by claiming its name; the eligibility table pins this with a spoofed-name row. The pushed token itself comes from `devices.Registry`, written only by `RegisterPushToken` for the authenticated device.
- [Tokens] No findings — `PushToken` flows `Registry.List` → `send` → marshalled bytes and nowhere else. `wakeAbsent` logs no device field and no err; `SendPushWake`'s marshal-failure error is fixed text. The failure-path test asserts the token never reaches the log even when the send error embeds it. `DeviceTokenHash` is documented MUST-NOT-log on `ActiveConn`. No audit entry is written.
- [Tokens] OUT OF SCOPE — `PushToken` has no byte bound (only the ~64 KB envelope cap, per `RegisterPushToken`'s recorded decision). It is forwarded verbatim per spec; the relay is its reader and validator. A bound belongs with the register handler, not here.
- [File operations] Not applicable — the waker reads the in-memory registry and writes no file.
- [Subprocess] Not applicable — no exec.
- [Crypto] No findings — the envelope is deliberately not Noise-sealed (spec: relay is the reader); it travels over the existing WSS leg. No new randomness or comparisons of secrets.
- [Network & I/O] No findings — sends are bounded by the per-device 30 s window and the relay's per-server-id rate limit; the cap-1 trigger means a trigger storm yields at most one pass in flight plus one pending. Transport write timeout applies.
- [Error messages, logs] No findings — only `event` keys are logged on both paths.
- [Concurrency] SHOULD FIX (addressed in design) — a snapshot taken during shutdown returns nil and would wake every device; `wakeAbsent` bails when `ctx.Err() != nil` after the snapshot, with a test. `last` is single-goroutine; `Trigger` never blocks the fan-out; `run` exits on ctx.
- [Threat model] No findings — protocol-mobile § `push_wake` states the relay learns only a token to wake; the envelope carries no session, turn or prompt content, which this design preserves.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24
