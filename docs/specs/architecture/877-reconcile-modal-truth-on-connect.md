# #877 — Reconcile modal truth on connect (re-send outstanding `modal_shown`)

**Size:** S · **Security-sensitive:** yes · **Package:** `internal/relay` (+ `cmd/pyry` wiring)

## Files to read first

- `internal/relay/v2session.go:1375-1418` — `handleNoiseInit` success tail: where `s.interactive`
  is set, `s.state = V2StateOpen`, the push queue is created, timers are armed, and the `#647`
  replay hook fires. **The reconcile call site is here.**
- `internal/relay/v2session.go:2100-2137` — `broadcastModalDismissed`: the near-exact structural
  precedent — a manager-internal modal *control* fan-out that builds a `protocol.Envelope`
  (`ID: 1` non-load-bearing), gates on `V2StateOpen && s.interactive`, and calls `m.Push`. The
  reconcile is this, **unicast to one conn** and sourced from a snapshot instead of a dismissal.
- `internal/relay/v2session.go:2944-2973` — `Push`: enqueues under `pushMu`, signals `drainCh`;
  returns `ErrConnNotFound` when no queue exists. Safe to call on the Run goroutine.
- `internal/relay/v2session.go:2984-3060` / `3178-3213` — `drainOnce` / `forwardEnvelope`: the
  seal-and-forward path. Confirms the `V2StateOpen` gate is **re-checked at seal time**, and that
  `#874` transport-down HOLD and `#777` replay-gating apply to the reconcile's queued frames too.
- `internal/relay/v2session.go:555-737` — `V2SessionConfig`: the optional-seam family the new
  field joins. `SnapshotSettings`/`SnapshotUsage` are the primitive-returning-closure precedents;
  `ModalResolver`/`Snapshotter` the consumer-declared-interface precedents.
- `internal/modalbridge/modal.go:188-214` — `Registry.Snapshot()` (the #876 seam this consumes):
  pure read, mints no id, clones `Options`, returns non-nil-empty for an empty registry.
- `cmd/pyry/interactive_modal_v2.go:214-246` — `broadcastInteractive`: today's raise-time modal
  fan-out on the **producer** goroutine (unguarded `nextID`). Read to understand why the reconcile
  **avoids** this goroutine's send state rather than sharing it.
- `cmd/pyry/relay.go:403-407, 445-497` — `modalReg := modalbridge.New()` and the
  `NewV2SessionManager(relay.V2SessionConfig{…})` literal: the single wiring site.
- `internal/protocol/messaging.go:104-125` — `ModalShownPayload` (six fields, no `answer_token`;
  `modal_id` is the sole nonce/correlation key). `AnswerToken` (messaging.go:127-143) lives only on
  the **inbound** `ModalAnswerPayload` — the re-send carries none.
- `internal/relay/v2session_modal_test.go:110-232, 234-315` — `openModalConn` /
  `noiseMsgsForConn` / `assertModalDismissed` harness helpers + the fan-out test shape to mirror.

## Context

`modal_shown` is broadcast exactly once, at raise time, to the conns connected at that instant
(`cmd/pyry/interactive_modal_v2.go` `broadcastInteractive`; `EventID == nil`, so it never enters
the turn-event replay ring). A phone that connects or reconnects *after* that instant never learns
a permission prompt is pending — the prompt then silently rides the daemon's 2-minute
deny-on-timeout window and is denied without the user ever seeing it.

This ships the **modal half of #829** (ADR 025) via the *enumerate-current-truth* shape: on a v2
session reaching `V2StateOpen` with the `interactive` capability, re-send the still-outstanding
`modal_shown` (same `modal_id`, same payload) **only to that connection**. Reconciling from current
state is idempotent — a still-pending prompt is re-sent; an already-resolved one simply is not — so
there is no stale-replay risk and no special-casing of how long the client was away. It consumes the
#876 read seam (`modalbridge.Registry.Snapshot()`, merged) and reuses the existing `modal_shown`
wire vocabulary; **no new payload type**. This ticket establishes the on-connect reconcile hook; the
`queue_state` twin (#878) reuses the same hook in its own ticket.

## Design

Two additive changes, both in `internal/relay`, plus a one-line `cmd/pyry` wire.

### 1. New optional seam on `V2SessionConfig`

```go
// OutstandingModals enumerates the daemon's currently-outstanding modals as
// marshal-ready modal_shown payloads (each already stamped with its original
// modal_id) for connect-time reconcile (#877). Called on the Run goroutine from
// handleNoiseInit's interactive-open tail; the returned payloads are unicast to
// the just-opened conn only. A pure read: it mints no nonce and retires nothing,
// so it neither re-arms the deny-on-timeout nor changes answerability.
//
// Optional: nil ⇒ no reconcile — byte-identical to the pre-#877 / foreground /
// existing-test posture. Production wires modalbridge.Registry.Snapshot.
OutstandingModals func() []protocol.ModalShownPayload
```

**Why a closure returning `[]protocol.ModalShownPayload`, not a `*modalbridge.Registry` import.**
`internal/relay` does **not** import `internal/modalbridge` today (verified), and every optional
control seam on `V2SessionConfig` is either a consumer-declared interface (`ModalResolver`,
`Snapshotter`) or a primitive/`protocol`-typed closure (`SnapshotSettings`, `SnapshotUsage`).
`protocol.ModalShownPayload` is already imported by `internal/relay`, so the closure crosses the
boundary with **no new import and no cycle**, matching the house pattern (CODING-STYLE: define the
dependency where it is consumed). It is not validated in `NewV2SessionManager` — like the other
optional seams, nil is a legal disabled state.

### 2. `reconcileModals` helper (Run-goroutine only)

```go
// reconcileModals unicasts the current outstanding modal_shown set to a freshly
// interactive-open conn (#877). Structural sibling of broadcastModalDismissed,
// minus the fan-out: it addresses exactly s.connID. Run-goroutine only (called
// from handleNoiseInit's success tail), so s.interactive / s.connID are read
// lock-free under the package's single-owner invariant. No-op when the conn is
// non-interactive (AC2) or the seam is unwired.
func (m *V2SessionManager) reconcileModals(ctx context.Context, s *V2Session)
```

Behaviour (each bullet maps to an AC and to a `broadcastModalDismissed` line it mirrors):

- **Guard first:** `if !s.interactive || m.cfg.OutstandingModals == nil { return }` — the
  capability gate (AC2) and the unwired/foreground opt-out. `s.interactive` is the same negotiated
  flag `broadcastModalDismissed` gates on.
- **Snapshot once:** `outstanding := m.cfg.OutstandingModals()`. Empty slice ⇒ the loop body never
  runs ⇒ nothing sent (AC3). A modal resolved before this point is absent from the snapshot ⇒ never
  re-sent (AC4 — enforced by `Snapshot`'s current-truth semantics, not by relay logic).
- **One shared `ts := time.Now().UTC()`** for the batch (matches `broadcastModalDismissed`).
- **Per payload:** `json.Marshal(p)`; on the defensive marshal-error branch (a closed
  string/`[]struct`, cannot fail in practice) emit a **content-free** Warn (`event`, `conn_id`,
  `modal_id` only) and `continue`. Otherwise `m.Push(ctx, s.connID, protocol.Envelope{ID: 1, Type:
  protocol.TypeModalShown, TS: ts, Payload: payloadJSON})`.
  - `ID: 1` — non-load-bearing; the phone correlates on `modal_id` (identical rationale and value to
    `broadcastModalDismissed`).
  - `EventID` left nil — a control event, never part of the turn-event replay ring
    (`forwardEnvelope`'s dedup and the ring are inert for `EventID == nil`).
- **Push error:** debug-log the transport sentinel + `conn_id` (never payload bytes); on
  `ctx.Err() != nil` return early (teardown), else `continue` — same posture as
  `broadcastModalDismissed` / `broadcastInteractive`.

### 3. Call site in `handleNoiseInit`

One line in the success tail, immediately **before** the `#647` replay hook (v2session.go:1415):

```go
m.reconcileModals(ctx, s)
if helloPayload.LastEventID != nil {
    m.replayMissed(ctx, s, *helloPayload.LastEventID)
}
```

Placement requirements (all satisfied here): after `s.interactive`/`s.state = V2StateOpen` are set
(the guard and the seal-time gate pass) and after `m.queues[s.connID]` is created at
v2session.go:1395 (so `Push` finds the queue, never `ErrConnNotFound`). Ordering **relative to
`replayMissed` is immaterial**: `replayMissed` enqueues into the separate per-session `replayQueue`
(drained by `drainReplayOnce` → `forwardEnvelope` directly), not into `m.queues`; the reconcile's
`modal_shown` sits in `m.queues` and is held by `#777` replay-gating until `replayQueue` empties,
then drains. Placing the reconcile first documents intent (surface the time-sensitive prompt ahead
of any replayed history).

### 4. `cmd/pyry` wiring (one line)

In the `NewV2SessionManager(relay.V2SessionConfig{…})` literal (cmd/pyry/relay.go:445), add:

```go
OutstandingModals: modalReg.Snapshot,
```

`modalReg` is the daemon-singleton `modalbridge.New()` already constructed at relay.go:407 and
shared with the surfacer (`newInteractiveModalEmitterV2`) and the inbound resolver — so the
enumerate-current-truth source is the exact registry the raise-time producer `Record`s into.

### Why this avoids the emitter's send state (the ticket's crux)

The raise-time fan-out (`broadcastInteractive`) runs on the **producer** goroutine in `cmd/pyry` and
advances an **unguarded** per-conn `nextID`. The reconcile runs on the manager's **Run** goroutine.
Reaching the emitter's `nextID` across that boundary would be a data race for zero benefit, because
the `ID` field on modal *control* envelopes is non-load-bearing — the phone correlates on `modal_id`
(pinned by `broadcastModalDismissed`'s `ID: 1` and its comment, and by
`ModalShownPayload.ModalID` being "the sole correlation key"). So the reconcile is **entirely
relay-side**, carries a fixed non-load-bearing `ID`, and touches no `cmd/pyry` emitter state. This is
the "avoids the emitter's send state" answer the ticket left to the architect — no new
cross-goroutine coupling, no lock added.

## Concurrency model

- `reconcileModals` executes on the single Run dispatch goroutine (called synchronously from
  `handleNoiseInit`), so reads of `s.interactive`, `s.connID`, and `m.cfg` need no lock/atomic —
  the same single-owner invariant `broadcastModalDismissed`, `handleInterrupt`, and
  `handleActiveConns` rely on.
- `m.Push` takes the leaf `pushMu` around the enqueue and signals `drainCh`; it never blocks on a
  send and is already documented safe to call from the Run goroutine. The actual seal-and-forward
  happens on a later Run pass in `drainOnce`/`forwardEnvelope`, one frame at a time.
- **Snapshot→deliver race (benign, self-healing).** A modal may be resolved (local answer, remote
  answer/cancel, or deny-on-timeout) in the window between `OutstandingModals()` reading it and the
  phone receiving the re-send. The phone then holds a `modal_shown` for a now-resolved `modal_id`;
  its later answer hits `Registry.Resolve` miss → inert (AC5). Because any resolution that occurs
  *after* this conn opened also fans a `modal_dismissed` to it (`broadcastModalDismissed` includes
  every `V2StateOpen && interactive` conn, now including this one), and both frames traverse the
  same FIFO push queue with the `modal_shown` enqueued first, the phone always observes
  shown-then-dismissed and clears the stale prompt. No ordering inversion is possible.

## Error handling

- **Marshal failure:** defensive only (`ModalShownPayload` is a closed struct of strings/`[]struct`).
  Content-free Warn + skip that one payload; the others still send.
- **`Push` failure:** `ErrConnNotFound` is unreachable here (queue created two statements earlier,
  same goroutine); `ctx.Err()` (teardown) returns early; any other → debug-log + continue. Never a
  panic, never a body byte in a log.
- **Empty / nil:** nil seam or empty snapshot ⇒ zero sends (AC3), no error.
- **Deny-on-timeout untouched:** the reconcile calls neither `ArmModalTimeout` nor `Resolve`, so the
  raise-time timer stands and answerability is unchanged (AC5) — structural, not a check.

## Testing strategy

Add `internal/relay/v2session_modalreconcile_test.go` (same package), reusing `openModalConn` /
`noiseMsgsForConn` and the recorder-poll pattern from `v2session_modal_test.go`. The manager is
constructed with `OutstandingModals` wired to a test closure (or a real `modalbridge.New()` +
`Record` for one integration-flavored case). A small `assertModalShown` helper (mirroring
`assertModalDismissed`) decrypts the first post-handshake `noise_msg` and asserts `Type ==
TypeModalShown`, `modal_id`, and payload fields. Scenarios:

- **Interactive open with one outstanding modal → unicast re-send (AC1).** Seam returns one payload;
  open an interactive conn; assert that conn receives a `modal_shown` `noise_msg` carrying the
  original `modal_id` + payload. Assert its `ID` is not asserted-upon as load-bearing (correlate on
  `modal_id`).
- **Only the opening conn receives it — not other open interactive conns (AC1 unicast).** Stand up
  conn A (interactive, already open), then open conn B (interactive); assert B receives the re-send
  and A receives **no** new `modal_shown` (the reconcile addresses `s.connID` only, unlike the
  all-conns `broadcastModalDismissed`).
- **Non-interactive open → no re-send (AC2).** Open a conn advertising no `interactive` capability
  with a modal outstanding; assert zero `modal_shown` `noise_msg`s to it.
- **Nothing outstanding → no re-send (AC3).** Seam returns an empty slice; open interactive conn;
  assert no `modal_shown`.
- **Two outstanding modals → both re-sent, keyed by `modal_id`, order-independent (AC1).** Assert via
  a `map[modal_id]payload` built from the decrypted frames.
- **nil seam → inert (foreground/unwired byte-stability).** `OutstandingModals` nil; open interactive
  conn; assert no `modal_shown` and session stays `V2StateOpen`.
- **Resolved-before-open not re-sent (AC4).** Use a real registry: `Record` then `Resolve` a modal so
  `Snapshot` is empty; open interactive conn; assert no re-send.
- **Answer to a re-sent-but-already-resolved `modal_id` is inert (AC5).** With a real registry +
  resolver, re-send a modal, resolve it, then feed an inbound `modal_answer` for that `modal_id`;
  assert `Resolve` miss (no keystroke, no dismissal) — reuses `fakeModalResolver` accounting.
- **No timer re-arm (AC5).** Assert `reconcileModals` invokes no `ArmModalTimeout` path (structural:
  a spy `modalTimeout` count / the absence of a new timer). Lightweight — the primary guarantee is
  that the helper's code contains no arm/resolve call; a focused assertion documents it.

Run `go test -race ./internal/relay/... ./cmd/pyry/...` and `go vet ./...`.

## Open questions

- **Delivery ordering vs `#777` replay for a reconnecting client that advertised `last_event_id`:**
  the reconcile `modal_shown` is held in `m.queues` behind the replay tail and drains after it. This
  is acceptable (replayed history first, then the pending prompt), but if product later wants the
  prompt to jump the replay tail, that is a follow-up — out of scope here.
- **`#878` (`queue_state`) reuses this hook.** The seam it adds is a sibling `OutstandingQueue`-style
  config field + a second call in the same success tail; nothing here needs to generalize
  preemptively (resist the shared-abstraction pull until the twin lands — CODING-STYLE over-DRY).

## Security review

**Verdict:** PASS

This ticket carries the `security-sensitive` label, so this adversarial pass over the spec is
mandatory. The active crux is **[Trust boundaries]** joined by **[Tokens/secrets]** and **[Error
messages, logs, telemetry]**: the change dispatches a `modal_id`-nonce-bearing, permission-body
payload over the internet-exposed v2 relay to a possibly-untrusted peer. The #876 sibling was *not*
sensitive (a registry-internal read that takes no input and produces no wire traffic); this ticket
is, because it is exactly the wire producer #876's knowledge doc named as the one that earns the
label. Each finding cites a real anchor.

**Findings:**

- **[Trust boundaries]** No MUST-FIX. Two gates keep the re-send from reaching an unauthorized peer,
  both reused, both deterministic:
  - *Authentication.* `reconcileModals` is called only from `handleNoiseInit`'s success tail, i.e.
    after Noise_IK completed and the device token validated (v2session.go:1375-1389); an unpaired or
    token-rejected peer closes at 4401 and never reaches this code. `m.Push` → `drainOnce` →
    `forwardEnvelope` then **re-checks `s.state == V2StateOpen`** at seal time (v2session.go:3185), so
    even a conn torn down between enqueue and drain is dropped there, never delivered to an
    un-authenticated peer.
  - *Capability.* The `!s.interactive` guard (AC2) means a conn that handshook without the
    `interactive` capability receives nothing. `s.interactive` is set from the same negotiated slice
    the ack echoed (v2session.go:1384-1388), so a spoofed/unsupported advertisement can never flag
    the session. Within a server-id, paired interactive devices are one trust domain (ADR 025
    § Security model) — any of them may view a pending modal, consistent with the raise-time
    `broadcastInteractive` posture; viewing is ungated, answering is gated separately (#702/#717).
  - *Unicast, not broadcast.* The re-send addresses exactly `s.connID`, so a modal is never leaked to
    a *different* conn that happened to be open — a tighter surface than `broadcastModalDismissed`'s
    fan-out. Pinned by the "only the opening conn receives it" test.
- **[Tokens/secrets]** No MUST-FIX — the reason for the label, and it is fail-safe by construction.
  - The re-sent payload's `modal_id` is a one-time opaque nonce (#716). The reconcile **mints none**:
    it consumes `Registry.Snapshot()`, a pure read that never touches `newModalID`
    (modal.go:188-214, #876). So no fresh nonce enters the wire and the nonce space is unchanged
    (AC5). Re-sending the *same* `modal_id` grants no new capability — answerability is governed by
    the registry's one-shot `Resolve`, which the reconcile never calls.
  - `modal_shown` carries **no `answer_token`** (that field exists only on the inbound
    `ModalAnswerPayload`, messaging.go:143). There is no client-minted idempotency key to forge or
    replay on this outbound path.
  - *Replay/idempotency of the re-send itself.* A phone answering a re-sent modal whose id was
    already resolved (locally, remotely, or by timeout) hits `Resolve` miss → inert (AC5). A second
    reconnect that re-sends the same still-pending `modal_id` is harmless (idempotent surface). No
    double-answer is possible: `Resolve` deletes on first consumption.
  - The 32-byte X25519 static key, CipherStates, and AEAD ciphertext are the unchanged v2 transport's
    concern; this change adds no key handling. The payload is sealed under `s.send` in
    `forwardEnvelope` like every other push frame — E2E-encrypted to the paired phone.
- **[Cryptographic primitives]** N/A — no crypto added. The reconcile hands plaintext envelopes to
  the existing `forwardEnvelope` seal path; nonce sequencing stays strictly Run-goroutine-serial (the
  frame is enqueued unsealed and sealed in FIFO by the single drain), so no send-nonce is burned out
  of order and the `#874` transport-down HOLD still applies (a re-send queued while the leg is down
  burns no nonce).
- **[Error messages, logs, telemetry]** No MUST-FIX — the co-crux. The modal body
  (`Title`/`Prompt`/options) is application content and MUST NOT be logged at any level. The helper
  logs only content-free discriminants: the defensive marshal-error branch and the `Push`-drop branch
  carry `event` + `conn_id` (+ `modal_id`, an opaque nonce, not a secret) — never `Title`, `Prompt`,
  option labels, `payloadJSON`, or a raw `err` that could quote payload bytes. This matches the
  emitter's SECURITY doc comment (interactive_modal_v2.go:26-31) and `broadcastModalDismissed`'s
  no-body-in-logs discipline. A test asserts no body substring appears in the reject/drop log record.
- **[Network, I/O & DoS]** No MUST-FIX. The re-send count is bounded by the number of outstanding
  modals, which is bounded by claude's single-modal-at-a-time reality and the registry's `Record`/
  `Resolve` lifecycle (typically 0–1). Each payload's `Prompt` is already capped at `maxPromptBytes`
  (4096) at `Record` time (modal.go:51, 229), so a re-send cannot inflate an un-droppable control
  frame beyond that bound. `modal_shown` is a control envelope (never dropped by the push queue), but
  the fan-out is a single unicast per outstanding modal to one conn — no amplification, no
  per-connect unbounded work. No new socket/HTTP/TLS surface.
- **[Concurrency]** No finding. No new goroutine, no new shared state, no new lock. `reconcileModals`
  runs on the Run goroutine and reuses `pushMu` (leaf) only transitively via `m.Push`. The
  Snapshot→deliver race is benign and self-healing (see § Concurrency model): the worst case is a
  transient stale `modal_shown` that the subsequent `modal_dismissed` clears, with FIFO ordering
  guaranteeing shown-before-dismissed. No nonce is reused; no CipherState is read off-Run.
- **[File operations] / [Subprocess execution]** N/A — the change touches neither the filesystem nor
  any subprocess. It reads an in-memory registry and enqueues an envelope.
- **[Threat model alignment]** In scope and addressed: leaking a pending permission prompt to an
  un-authenticated peer (blocked by the 4401 handshake + the seal-time `V2StateOpen` re-check), to a
  non-interactive peer (blocked by the `s.interactive` guard, AC2), or to the wrong conn (prevented
  by unicast to `s.connID`); minting or re-arming a nonce/token/timer on reconnect (structurally
  impossible — `Snapshot` mints nothing, the helper calls no arm/resolve, AC5); double-answering via
  a re-sent id (blocked by one-shot `Resolve`, AC5); body content in logs (content-free logging).
  Out of scope, each with its owner: the `queue_state` twin (#878, reuses this hook); the end-to-end
  acceptance slice and #829's disposition (operator decides — rescope or supersede); mobile-client
  match-and-replace behaviour (separate contract ticket, no daemon change).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-10
