# Spec #874 — v2 push drain: hold control envelopes unsealed while the relay transport is down

Layer 1 of the reconnect-reliability design. Scope: the *within-grace* window
where the same phone connection survives on the relay side during a daemon↔relay
reconnect blip. Immediate flush-on-reconnect is #875; new-connection catch-up is
the reconcile-on-connect mechanism (#829). Umbrella: #829.

**Label: `security-sensitive`** — this change reschedules the Noise send seal
(nonce++) so a frame that cannot reach the phone never burns a counter. Nonce/AEAD
sequencing integrity is the security surface. The `## Security review` section at
the end of this spec is the mandatory gate artifact.

## Files to read first

- `internal/relay/v2session.go:2935-3012` — `drainOnce`: the one-per-pass push pump. The pre-seal transport probe + early-return "hold" lands at the very top; the existing replay-gate snapshot, pop-under-`pushMu`, and `more` re-signal are otherwise unchanged.
- `internal/relay/v2session.go:3109-3143` — `forwardEnvelope`: the *single* seal-and-forward path. `s.send.Encrypt` (the nonce-burning seal) is here; its session-level error returns (`ErrConnNotFound`, `ErrSessionNotOpen`, marshal/seal failure) are the "still drop" set (AC2). **Do not** add the transport probe here — the probe gates the *pop*, upstream in `drainOnce`.
- `internal/relay/v2session.go:2782-2796` — `send`: swallows the post-send `Outbound` transport error at debug. This is why a post-send error is too late (the seal already ran); the fix is a *pre-seal* probe, not a reaction to this error.
- `internal/relay/v2session.go:144-153` — `queuedEnv` doc: envelopes are held **unsealed** because the Noise send nonce is strictly sequential. This spec extends "held unsealed" from the enqueue side to the drain side.
- `internal/relay/v2session.go:555-565` — `V2SessionConfig` + the `Outbound` field: the only transport seam today. The new `Connected func() bool` field is additive, same nil-optional idiom as the other seams in this struct.
- `internal/relay/v2session.go:719-806` — `V2SessionManager` struct + concurrency doc: `drainOnce` runs on the single Run goroutine; `pushMu` is a leaf never held across an external call. The probe is called off-lock, before `pushMu`.
- `internal/relay/connection.go:183-196` — `Connection.Send` (the production `Outbound`); wire a sibling `Connection.Connected() bool` passthrough next to it.
- `internal/transport/wssclient.go:282-341` — `Client.Send` (`live := c.conn != nil` under `c.mu`, returns `ErrNotConnected`) and `Client.Connected() <-chan struct{}` (edge-triggered channel — NOT a level poll). Add `Client.IsConnected() bool` as the level poll that agrees with `Send`'s own predicate.
- `internal/transport/wssclient.go:362-382` — `Close` / `setConn`: `Close` does **not** nil `c.conn`; it does close `c.closeCh`. `IsConnected` must gate on `closeCh` too so a closed client reads down.
- `cmd/pyry/relay.go:445-465` — the `V2SessionConfig` literal; add `Connected: conn.Connected` beside `Outbound: conn.Send`.
- `internal/relay/v2session_test.go:37-63,700-755` — `v2Recorder` + `driveToOpen`/`openSession` (gives `initRecv`, the phone-side decrypt used as the nonce-contiguity oracle). New tests extend the recorder into a toggleable gate.
- `internal/relay/v2session_test.go:3021-3121` — `TestV2Session_Push_ConcurrentWithReplies_NoNonceCorruption`: the pattern for "all outbound frames decrypt in capture order under `initRecv`" — a burned/gapped nonce MAC-fails. The new AC4 assertion reuses this oracle.
- `internal/relay/v2session_test.go:3243-3282` — `TestV2Session_forwardEnvelope_NotOpen_GateRefuses`: white-box drive of a single drain-side path without starting Run; the model for the AC2 "session-level failure still drops" assertions.

## Context

ADR 025 § Backpressure promises control messages are never dropped
(`assistant_delta` is drop-oldest, control never drops). The v2 push queue keeps
that promise on the *enqueue* side: `enqueue` never evicts a control event, and
`queuedEnv` holds every envelope **unsealed** because the Noise send nonce is
strictly sequential (`v2session.go:144`). The promise breaks on the *drain* side
during a daemon↔relay reconnect blip:

- `drainOnce` pops the head (`q.items = q.items[1:]`), then `forwardEnvelope`
  **seals** it (`s.send.Encrypt`, nonce++) and `send` swallows the transport
  error at debug ("relay: v2 outbound drop"). The popped envelope is gone — one
  lost control message.
- **Worse:** the seal burned a nonce for a frame the surviving phone never
  receives. The phone's recv nonce is now one behind; the next frame it *does*
  receive MAC-fails and the session dies with close code 4421. A blip inside the
  relay's 30-second grace can therefore both lose a `modal_shown`/`queue_state`
  **and** poison the still-live encrypted session.

The daemon cannot un-burn a flynn/noise nonce after `Encrypt`. The only fix is to
learn the transport is down **before** the seal and leave the head un-popped and
unsealed until the transport recovers — matching the enqueue-side "held unsealed"
invariant. The existing `Outbound func(...) error` seam reports the transport
state only *after* `send`, i.e. after the seal; a pre-seal check needs an additive
level-poll seam.

## Design

### New seam: `V2SessionConfig.Connected func() bool`

Additive, optional field on `V2SessionConfig`, same nil-optional idiom as the
struct's other seams:

```go
// Connected reports whether the relay transport leg is currently up. The push
// drain consults it BEFORE sealing a queued envelope: a false result leaves the
// head un-popped and unsealed, so no Noise send-nonce is burned for a frame that
// cannot reach the phone (a burned nonce gaps the phone's recv nonce → 4421).
// Optional: nil ⇒ always-connected — the drain never holds, preserving the
// pre-#874 drop-on-send posture for foreground / unwired / existing tests.
// Production wires (*relay.Connection).Connected, a level poll of the transport
// leg's live-conn state. A true result is best-effort: the conn may drop between
// the poll and the send (a single-frame residual race — see Open Questions),
// but a false result reliably holds. NOT a security decision — it gates only
// the timing of a seal that would otherwise happen anyway.
Connected func() bool
```

Private helper on the manager encapsulates the nil default:

```go
// transportDown reports whether the push drain should hold rather than seal.
// nil Connected ⇒ never down ⇒ byte-identical to pre-#874 behaviour.
func (m *V2SessionManager) transportDown() bool
    // returns m.cfg.Connected != nil && !m.cfg.Connected()
```

`NewV2SessionManager` requires no new validation — `Connected` is optional
(mirrors `Handlers`, `Snapshotter`, `ModalResolver`, … which are all nil-legal).

### `drainOnce` change (the whole behavioural change)

One guard at the top of `drainOnce`, before the replay-gate snapshot and before
`pushMu`:

- **Behavior:** if `m.transportDown()`, return immediately — pop nothing, seal
  nothing, re-signal nothing. Everything stays as-is.
- **Why off-lock and before `pushMu`:** the probe is a plain func call;
  `pushMu`'s invariant is "taken alone, never held across an external call", so
  the probe must not be inside it. Returning before the pop is what makes the
  head stay *un-popped* (AC1).
- **Lazy re-flush (AC1/AC3):** the drain does **not** re-signal `drainCh` when it
  holds. The next `Push` on any conn signals `drainCh` (existing behaviour), which
  re-enters `drainOnce`; if the transport has recovered by then the held head
  drains in FIFO order. Immediate flush-on-reconnect (no waiting for a `Push`) is
  deliberately #875.

Everything downstream of the guard — the replay-pending snapshot, the
`pushMu`-guarded first-eligible-queue pop, `forwardEnvelope`, the `more`
re-signal — is **unchanged**. When the transport is up the drain behaves exactly
as today.

### Why the probe gates the pop, not the seal (AC2)

The transport-down decision lives *upstream of the pop* in `drainOnce`. The
session-level failures — `ErrConnNotFound`, `ErrSessionNotOpen`, marshal/seal
failure — live *downstream of the pop*, inside `forwardEnvelope`, and are reached
only when the transport is up (past the guard). So:

- transport down → **hold** (never popped, never sealed);
- transport up → pop + `forwardEnvelope`; a session-level failure → **drop** with
  the existing debug log (the current outbound-drop posture, preserved verbatim).

The distinction is structural (control-flow position), not a runtime tag on the
error — no conflation is possible.

### Production wiring (two thin passthroughs)

The `Connected` seam needs a level poll of the transport leg. The honest source
of truth is `transport.Client` (`c.conn != nil`, the exact predicate `Send`
checks). `cmd/pyry` holds only `*relay.Connection`, so pass the poll through:

1. **`internal/transport/wssclient.go` — `Client.IsConnected() bool`** (new): true
   iff the client is not closed and a live conn exists. Contract: `false` when
   `closeCh` is closed OR `c.conn == nil` (both under `c.mu` / the `closeCh`
   select, mirroring `Send`'s own guards); `true` otherwise. A `true` result does
   **not** guarantee the next `Send` succeeds; a `false` result guarantees `Send`
   would return `ErrNotConnected`/`ErrClosed`. Cheap synchronous poll — for
   callers deciding *before* a side-effecting op, vs. the edge-triggered
   `Connected() <-chan struct{}`.

2. **`internal/relay/connection.go` — `Connection.Connected() bool`** (new):
   passthrough → `c.client.IsConnected()`. One line + doc noting it reflects the
   binary↔relay leg's live-conn state for the v2 drain's pre-seal probe.

3. **`cmd/pyry/relay.go`** — add `Connected: conn.Connected,` to the
   `V2SessionConfig` literal (beside `Outbound: conn.Send`).

### Scope boundary (why drainOnce only)

`drainReplayOnce` (#777/#647) and the one-shot sealed sends (dispatch replies,
close frames, `emitRekeyRequest`) share the same `s.send` nonce sequence and the
same seal-while-down hazard. They are **out of scope** for #874, and correctly so:

- During the #874 scenario (same surviving conn, within-grace blip, no new hello),
  there is no replay tail (`replayQueue` empty) and no inbound frames to reply to,
  so `drainReplayOnce` and the reply paths are inert. The push drain is the *only*
  active sealed-send path in this window — fixing it closes the scenario.
- Replay activates on a new conn / reconnect-with-`last_event_id` — that is the
  reconcile-on-connect / catch-up territory tracked under #829, not this ticket.
- `emitRekeyRequest` is a 1-hour timer that could rarely fire mid-blip; noted in
  Open Questions, deferred.

The `transportDown()` probe is reusable, so extending the guard to those paths
later (if evidence warrants) is a one-line addition each.

## Concurrency model

- No new goroutine, no new lock. `drainOnce` runs on the single Run goroutine; the
  seal (`s.send.Encrypt`) remains single-writer, so the nonce sequence stays
  monotonic (unchanged from today).
- The probe (`m.cfg.Connected()` → `Connection.Connected()` →
  `Client.IsConnected()`) is called on the Run goroutine, **before** `pushMu`,
  holding no manager lock. `IsConnected` takes the transport's own `c.mu` briefly
  and releases it — a leaf acquisition that never nests with `pushMu` or any
  manager lock (different type, different package, no shared lock).
- `pushMu`'s "taken alone, never across an external call" invariant is preserved:
  the probe is entirely outside `pushMu`.
- nil `Connected` ⇒ `transportDown()` always false ⇒ the guard never fires ⇒ the
  Run goroutine's drain path is byte-identical to pre-#874.

## Error handling

- **Transport down** → hold: no error surfaced, no log (a routine blip). The head
  waits for the next `Push`-driven re-signal.
- **Transport up, session-level failure** → the existing `forwardEnvelope` error
  returns unchanged; `drainOnce` logs the existing `v2.push.drain_drop` debug line
  and drops (AC2, current posture).
- **Residual race** (probe reads up, conn drops before `send`) → `forwardEnvelope`
  seals, `send` logs `v2.push.drain_drop`/`v2 outbound drop` and the frame is lost
  and the nonce burned — exactly today's behaviour, but now confined to a single
  frame at the up→down transition instant instead of every frame across the whole
  down window. See Open Questions.
- **`IsConnected` on a closed client** → returns false (via the `closeCh` gate),
  so a drain racing client shutdown holds rather than seal-drops. Harmless: Run is
  tearing down anyway.

## Testing strategy

`internal/relay` unit tests, fake outbound, stdlib only, `-race`. Scenarios
(developer writes bodies in the project idiom; reuse `driveToOpen` + `initRecv` as
the nonce oracle):

- **Gated recorder fixture.** Extend `v2Recorder` (or a sibling) with an atomic
  `up` flag: `outbound` records + returns nil when up, records nothing + returns a
  transport-down sentinel when down; a `connected() bool` method reads the same
  flag. Wire `Connected: gated.connected`.

- **AC1/AC3/AC4 — hold then reflush, contiguous nonce (the headline test).**
  1. `driveToOpen` with the transport **up** (handshake `noise_resp` emitted).
  2. Flip **down**. `Push` a control envelope (e.g. a `message`/`queue_state`).
  3. Assert within a bounded settle window: **no** new outbound frame beyond the
     handshake resp (held, not sealed) — negative assertion, same shape as
     `TestV2Session_Push_NonBlockingUnderStall`.
  4. Flip **up**. `Push` a second control envelope (this re-signals `drainCh` →
     lazy flush of the held head).
  5. `waitForEnvelopes` for the two new frames; decrypt both under `initRecv` in
     capture order. Both decrypt cleanly (⇒ contiguous nonce, no burn) and arrive
     in FIFO order, exactly once each (no duplicate, no loss).

- **AC2 — hold is transport-probe-gated only.**
  - Sub-case (a): `connected()==false` ⇒ a drain pass leaves the queue head
    present (un-popped) and the recorder empty.
  - Sub-case (b): `connected()==true` ⇒ the drain pops and forwards even if the
    send then fails; the head is consumed (not held). Pair with the existing
    `forwardEnvelope` gate tests to show `ErrConnNotFound`/`ErrSessionNotOpen`
    still drop (unchanged posture).

- **`transport.Client.IsConnected`** (`internal/transport`, existing httptest
  relay harness): false before any successful dial and after `Close`; true after a
  live conn is established (`setConn`).

- **`relay.Connection.Connected` passthrough** (optional): reflects the client's
  `IsConnected`; may be folded into the integration wiring rather than a dedicated
  test (one-line passthrough).

- **nil-default regression (implicit).** Every existing Push/nonce test leaves
  `Connected` nil ⇒ `transportDown()` false ⇒ the drain is unchanged. These must
  stay green — the byte-identical guarantee for the unwired/foreground path.

## Open questions

- **Residual single-frame TOCTOU.** Probe-up → conn-drops → seal → send-drops
  burns one nonce at the exact up→down transition. Inherent to any pre-seal probe
  that doesn't hold a lock spanning the transport send (impossible without
  reaching into flynn/noise). This ticket reduces exposure from *every frame
  across the whole down window* to *at most one frame at the transition instant*
  — the layer-1 "hold within grace" guarantee. Fully closing it would need a
  rekey/resync backstop (the ultimate recovery), out of scope. Recommend
  documenting as a known limitation, not defending further absent evidence a
  transition-instant burn is observed in practice.
- **Sibling sealed-send paths.** `drainReplayOnce`, dispatch replies, close
  frames, and `emitRekeyRequest` share the nonce-burn-while-down hazard but are
  inert during the #874 within-grace scenario (see Scope boundary). Whether to
  extend the `transportDown()` guard to them is a #829-umbrella follow-up, gated on
  observed failures — not pre-emptive here.
- **Held-frame staleness.** A `modal_shown` held across a multi-second blip is
  delivered late (on the next `Push`), not never. #875 (eager flush-on-reconnect)
  shortens the latency; the 2-minute deny-on-timeout is the safety net either way.
  No action here.

## Security review

Adversarial pass over this spec. The ticket carries `security-sensitive` because
it reschedules the Noise AEAD seal (nonce sequencing); this section is the gate.

**[Crypto / nonce integrity] — the core of the ticket, PASS.** flynn/noise's send
nonce is strictly sequential and monotonic; `s.send.Encrypt` is the only advance
and runs single-writer on the Run goroutine (unchanged). Today a seal-then-drop
while the transport is down burns a counter the surviving phone never consumes →
recv-side MAC failure → 4421 teardown of a *still-authenticated* session (an
availability/integrity break reachable by a mere network blip). The fix removes
the burn in the sustained-down case by gating the pop before the seal. The seal
path, its single-writer discipline, and the sequence's monotonicity are otherwise
untouched. Residual: a one-frame burn at the up→down transition instant (Open
Questions) — strictly better than status quo, never worse, and self-heals only via
a future rekey/resync (unchanged from today). **No regression; net positive.**

**[Trust boundaries] No finding.** The new datum is a `bool` transport-liveness
poll originating from the daemon's own transport client — not phone-controlled,
not attacker-influenceable (an attacker who can force the daemon↔relay leg down is
already doing exactly what this code handles gracefully). It flows
`Client.conn!=nil → Connection.Connected → V2SessionConfig.Connected →
transportDown() → drainOnce guard` as an opaque control signal; it is never
parsed, logged, or used in any authorization decision. The held envelope's
plaintext (`modal_shown`/`queue_state`/`message`) is exactly what would have been
sent anyway — no new content is exposed or retained beyond the existing bounded
`pushQueue`.

**[Authorization / gating] No finding.** `Connected` gates *timing*, not
*permission*. It cannot cause a frame to be delivered that would otherwise be
refused: the V2StateOpen security gate and per-conn addressing remain in
`forwardEnvelope`, downstream of the probe and reached only on the transport-up
path. A compromised/buggy `Connected` returning a wrong value degrades only
liveness — false-down holds a frame until the next `Push` (delivered late, never
mis-delivered); false-up seals-and-drops one frame (today's behaviour). Neither
widens delivery. nil `Connected` fails safe to today's posture.

**[Secrets / logging] No finding.** No key, token, nonce, ciphertext, or payload
byte enters any new log field. `StaticPriv` and the CipherStates are untouched.
The hold path logs nothing; the existing `v2.push.drain_drop` debug line (conn_id
+ err, no content) is unchanged. `IsConnected` returns a bare bool and logs
nothing.

**[Denial of service / resource] No finding.** No unbounded growth: held
envelopes accumulate only in the existing `pushQueue`, still bounded by
`pushQueueCap` and the existing droppable-delta eviction policy (`enqueue`
unchanged). A sustained-down transport now retains control envelopes up to that
bound instead of sealing-and-dropping them — bounded memory, and the whole point
of the ticket. No new goroutine, lock, or channel; no new blocking path (`pushMu`
still never held across an external call).

**[Fail-safe defaults] PASS.** Every degenerate input fails toward the safe /
prior behaviour: nil `Connected` ⇒ never holds (byte-identical to pre-#874);
`IsConnected` on a closed client ⇒ reads down ⇒ holds (Run tearing down anyway);
probe error is impossible (pure bool poll, no error channel).

**Verdict: PASS.** The change is a net security improvement (removes a
network-blip-triggered session-poisoning nonce burn) with no new attack surface,
no new secret handling, and fail-safe defaults. No revisions required before
commit.
