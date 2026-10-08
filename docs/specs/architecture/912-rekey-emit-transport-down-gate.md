# Spec: Gate the scheduled + manual rekey emit behind `transportDown()` (#912)

**Ticket:** #912 — Scheduled rekey emits blind while the relay transport is down — burns a send nonce, then tears the session down as a mislabelled rekey failure
**Size:** XS (confirmed — PO sized XS; single production file, ~30 production lines, one additive exported sentinel)
**Security-sensitive:** yes — the gate decides whether a Noise send-nonce is consumed on the internet-exposed relay transport (a nonce-writer hold, same class as #874). See § Security review.

---

## Context

`#874` taught the push drain (`drainOnce`) to hold unsealed envelopes while the relay transport is down: the `transportDown()` probe runs off-lock on the single-owner Run goroutine, **before** the `s.send.Encrypt` seal, so no Noise send-nonce is burned for a frame that cannot reach the phone. The timer-driven and operator-driven rekey emit (`emitRekeyRequest`) never got the same treatment.

When the 1-hour `wakeRekeyEmit` fires (or an operator runs `pyry rekey`) while the transport is down, `emitRekeyRequest` seals a `rekey_request` under `s.send` (**burning a nonce**), `m.send` silently drops the frame, and `awaitingRekeyReply` + the 30s `rekeyReplyTimer` are armed anyway. The phone never sees the request; 30s later `handleWake`'s `wakeRekeyReplyTimeout` arm closes the session with `StatusHandshakeFailure` (4426) and logs `noise.rekey_failed` — attributing a transport outage to a rekey failure. Even if the transport recovers inside the 30s window, the burned nonce gaps the phone's recv nonce → next decrypt MAC-fails → 4421 close of a healthy session.

Recovery is automatic (fresh handshake on reconnect), so severity is low — this is the **"error mislabelled"** shape: a relay blip overlapping a rekey boundary force-tears a healthy session and buries a misleading Warn that costs diagnosis time. The fix reuses #874's existing `transportDown()` gate.

## Design

### The invariant

> **A rekey `rekey_request` must never be sealed (nonce++) while `transportDown()` reports the transport down.** The two callers of `emitRekeyRequest` gate on `transportDown()` first and diverge on the recovery action.

### Where the gate goes — call sites, not `emitRekeyRequest`

Both callers already **diverge after the check** (scheduled re-arms a timer; manual returns an error), and the manual path has an ordering constraint that forces the check upstream of `emitRekeyRequest`:

`handleManualRekey` **stops the scheduled 1-hour timer** (`s.rekeyTimer.Stop(); s.rekeyTimer = nil`, line 3151) *before* calling `emitRekeyRequest`, relying on `rekeyComplete` to re-arm it on the phone's reply. If the check lived inside `emitRekeyRequest` and returned "deferred," the scheduled timer would already be stopped-and-nilled with nothing to re-arm it — the session would silently lose its rekey cadence. **Therefore the manual gate must run before the timer stop**, which means it cannot live inside `emitRekeyRequest`.

Decision: **gate at each call site**, on the Run/dispatch goroutine, before `emitRekeyRequest`. `emitRekeyRequest` stays a pure "do the emit" primitive (unchanged body). This mirrors #874 gating at the top of `drainOnce` rather than inside `forwardEnvelope`.

`emitRekeyRequest` gets **one added doc line** stating the precondition — callers MUST have checked `transportDown()`. No defensive guard is added inside it (see § Security review — evidence-based: both current callers gate; the reserved future `"compromise"` caller adds its own gate; a redundant guard would need a return-signal the primitive doesn't have). This is a documented contract, not enforced code — acceptable because the seal site has exactly two callers, both edited here.

### Scheduled path — `handleWake`, `wakeRekeyEmit` arm

Replace the single `m.emitRekeyRequest(ctx, w.s, "scheduled")` call with a gate:

- `transportDown()` **false** → `emitRekeyRequest(ctx, w.s, "scheduled")` (unchanged; inert gate).
- `transportDown()` **true** → **do not emit.** Re-arm `w.s.rekeyTimer = m.armRekeyRetryTimer(ctx, w.s)` for a short bounded retry, emit an Info deferred-log line, `return`. No seal, no `awaitingRekeyReply`, no reply timer.

The fired 1-hour timer that delivered this wake is one-shot and inert; overwriting `w.s.rekeyTimer` with the retry timer is safe (no `Stop()` needed — it already fired). The retry timer's callback pushes `wakeRekeyEmit` again, so on the next fire `handleWake` re-checks `transportDown()` and either emits (recovered) or re-arms (still down) — a **self-healing bounded-retry loop**.

### `armRekeyRetryTimer` — new helper

A copy of `armRekeyTimer` (line 1017) that arms at `rekeyRetryInterval` instead of `rekeyInterval` and pushes `wakeRekeyEmit` (same kind — the retry re-enters the scheduled arm). Signature: `func (m *V2SessionManager) armRekeyRetryTimer(ctx context.Context, s *V2Session) *time.Timer`. ~7 lines. Rationale for a dedicated helper over parametrizing `armRekeyTimer`: `armRekeyTimer` has two happy-path callers (425, 1433); a duration parameter would touch both. A dedicated helper is zero-fan-out and reads at the call site.

### `rekeyRetryInterval` — new package var

```go
// rekeyRetryInterval is the short, bounded re-arm cadence used when a
// scheduled rekey wake fires while transportDown(). Much shorter than the
// 1-hour rekeyInterval so a transient relay blip at the rekey boundary is
// retried soon after the transport recovers; comfortably longer than the
// relay reconnect backoff ceiling (~30s) so a single retry usually lands on
// a recovered transport; well under idleTimeout (15m) so a genuinely-gone
// phone is reaped by the idle sweep rather than by an endless retry spin.
// Test-overridable (lowercase, save/restore); not part of the public API.
var rekeyRetryInterval = 1 * time.Minute
```

**Bounded-spin safety:** while the transport stays down, the retry loop re-arms every `rekeyRetryInterval` but emits nothing (no inbound either). The 15-minute `idleTimeout` sweep (which counts inbound-frame silence) tears the session down independently, so a permanently-dead phone is reaped there — the retry never spins forever on a dead session. Document this interaction in the `rekeyRetryInterval` doc-comment.

### Manual path — `handleManualRekey`

Insert the gate after the eligibility checks and **before** the timer `Stop()`:

```
ErrConnNotFound   (no session)
→ ErrSessionNotOpen (state != V2StateOpen)
→ ErrSessionNotOpen (awaitingRekeyReply — prior emit in flight)
→ NEW: if m.transportDown() { return ErrTransportDown }   // scheduled timer untouched
→ s.rekeyTimer.Stop(); s.rekeyTimer = nil                  // (existing)
→ m.emitRekeyRequest(ctx, s, "manual")                     // (existing)
```

Ordering rationale: `transportDown` sits last among the guards, immediately before the timer stop + seal, so (a) a nonexistent/ineligible conn still gets its precise error regardless of transport, and (b) on transport-down the scheduled timer is **untouched** — the session keeps its 1-hour cadence and simply reports "retry later" to the operator.

### `ErrTransportDown` — new sentinel

```go
// ErrTransportDown is returned by (*V2SessionManager).Rekey when the named
// session is open and eligible but the relay transport is currently down, so
// a manual rekey would seal a rekey_request that cannot reach the phone
// (burning a Noise send-nonce and arming a doomed reply window). Distinct
// from ErrSessionNotOpen so the operator sees "transport down — retry", not
// "not open". Surfaced verbatim by the control dispatcher (no wire ErrorCode,
// same posture as ErrSessionNotOpen).
var ErrTransportDown = errors.New("relay: transport down, rekey deferred — retry")
```

No control-package change: `handleRekey` (server.go:794) puts `err.Error()` into `Response.Error` for any non-nil error and only maps `ErrConnNotFound` to a code. Update `Rekey`'s and `handleManualRekey`'s doc-comments to list `ErrTransportDown` in the return set.

### Why the gate is inert on the transport-up path (AC5)

`transportDown()` returns false whenever `Connected` is up **or** nil. Every existing rekey test constructs `V2SessionConfig` without `Connected` → nil → gate false → both paths byte-identical to today. No existing test changes.

## Data flow

```
Scheduled (transport DOWN):
  1h rekeyTimer fires → wake{wakeRekeyEmit} → Run → handleWake
     └─ transportDown()? YES → armRekeyRetryTimer (1m) → log deferred → return
        (no seal, no nonce burn, no awaitingRekeyReply, no reply timer, stays V2StateOpen)
  … transport recovers …
  retry timer fires → wake{wakeRekeyEmit} → handleWake
     └─ transportDown()? NO → emitRekeyRequest("scheduled")  [normal emit]

Manual (transport DOWN):
  Rekey(ctx, connID) → m.manualRekey → Run → handleManualRekey
     └─ open, not awaiting, transportDown()? YES → return ErrTransportDown
        (scheduled timer untouched, no seal, no reply window)
     → control handleRekey → Response{Error: "relay: transport down, rekey deferred — retry"}
```

## Concurrency model

No new goroutines beyond the `time.AfterFunc` callback that `armRekeyRetryTimer` spawns — identical lifecycle to `armRekeyTimer`'s callback (selects on `m.wake` vs `runCtx.Done()`; a fired-but-undelivered retry wake drains via the ctx arm on Run exit, leaking nothing).

**TOCTOU-free by single-owner ownership:** both gate sites run on the Run goroutine (`handleWake` via the `m.wake` arm, `handleManualRekey` via the `m.manualRekey` arm). `s.send` is mutated only by the Run goroutine. Between a caller's `transportDown()` check and `emitRekeyRequest`'s `s.send.Encrypt`, no other goroutine touches `s.send` — so check-then-seal is atomic. No lock, no atomic introduced (inherits the package's single-owner-goroutine invariant). `transportDown()` itself reads `m.cfg.Connected` (a `func() bool` level-poll set at construction, never mutated), so the probe is race-free.

Residual window (accepted, strictly better than status quo): a transport that flips up→down in the instant between the check and the seal burns one nonce — the same 1-frame TOCTOU #874 accepted, and identical to a frame that was in flight when the transport dropped. The status quo burns a nonce on *every* down-boundary emit; this reduces it to a vanishing race.

## Error handling

| Condition | Behaviour |
|---|---|
| Scheduled emit, transport down | Re-arm retry timer, Info log, no emit, session stays `V2StateOpen`. Self-heals on retry when transport recovers. |
| Manual rekey, transport down | Return `ErrTransportDown` (verbatim to operator), scheduled timer untouched, no side effect. |
| Scheduled/manual, transport up | Unchanged — emit, arm reply window, `awaitingRekeyReply = true`. |
| Marshal/seal failure inside `emitRekeyRequest` | Unchanged — logged + dropped, conn stays open (only reached transport-up). |
| Transport recovers within retry interval | Next retry fire emits normally under the (unburned, contiguous) `s.send` nonce. |

Logging: the deferred scheduled path emits one structured Info line, e.g. `event=v2.rekey.emit.deferred_transport_down`, `conn_id=<id>`, `reason=scheduled`, `retry_in=<rekeyRetryInterval>`. Content-free (conn-id + event only — no payload, no keys), consistent with the package's existing `v2.rekey.*` field discipline. The manual deferral needs no new log line — the returned `ErrTransportDown` is the operator-visible signal.

## Testing strategy

Add to `internal/relay/v2session_test.go` (same-package, stdlib `testing`, `-race`). Reuse `gatedRecorder` (togglable `Connected`), `driveToOpen`, `waitForEnvelopes`, `decryptAppFrame`, and `sess.initRecv` (the nonce oracle). Follow the not-`t.Parallel` + save/restore convention for the package-var overrides (`rekeyInterval`, `rekeyReplyTimeout`, `rekeyRetryInterval`).

Scenarios (developer writes the bodies in the project idiom):

- **Scheduled deferred while down, then emits on recovery (AC1/AC2/AC3).** Open with `Connected` gated **down**; sub-second `rekeyInterval`, short `rekeyRetryInterval`. After a settle window: assert no second envelope reached the recorder (only the handshake resp), `s.awaitingRekeyReply == false`, `s.rekeyReplyTimer == nil`, `s.state == V2StateOpen`, and (the nonce oracle) that the session's `s.send` nonce is un-advanced. Then flip `Connected` **up**; poll until the `rekey_request` emit appears; decrypt it under `sess.initRecv` (proves the nonce sequence is contiguous — a burned nonce during the down window would MAC-fail here); assert inner type `TypeRekeyRequest`, reason `"scheduled"`.
- **No `noise.rekey_failed`, no 4426 while down (AC3).** Same down setup with a `bufferLogger`; let more than `rekeyReplyTimeout` elapse; assert the log never contains `noise.rekey_failed`, no envelope carries `CloseCode == StatusHandshakeFailure`, and `s.state == V2StateOpen`. (Directly refutes the status-quo tear-down.)
- **Manual rekey while down returns `ErrTransportDown` (AC4).** Open with `Connected` gated **down**; call `sess.mgr.Rekey(ctx, connID)`; assert `errors.Is(err, ErrTransportDown)` **and** `!errors.Is(err, ErrSessionNotOpen)` (distinctness); assert no second envelope, `!s.awaitingRekeyReply`, `s.rekeyReplyTimer == nil`, and `s.rekeyTimer != nil` (scheduled cadence preserved — the timer was not stopped).
- **Regression: transport up unchanged (AC5).** A variant of the two happy-path tests (`…Emit_ReArmViaResponder`, `…RekeyManual_HappyPath`) with `Connected` gated **up** — assert byte-for-byte the same emit + `awaitingRekeyReply == true` + armed reply timer. (The existing nil-`Connected` tests already prove inertness; this adds the explicit up-poll variant.)

## Open questions

- **`rekeyRetryInterval` value (1 minute).** Chosen per the constraints in the doc-comment (> reconnect backoff ceiling, ≪ 1h, < idle timeout). If a future config surface (the `modalDenyTimeout`/#708 posture) makes these tunable, this joins them; not in scope here.
- **Reconnect-eager retry.** #875 wakes `drainCh` on the `Reconnect` edge for the push drain. A future enhancement could likewise fire an eager scheduled-rekey retry on reconnect (instead of waiting up to `rekeyRetryInterval`). Out of scope — the timer-retry fully satisfies AC2, and cross-wiring rekey into the reconnect signal is a larger change. Note for a possible #829-umbrella follow-up.
- **Third `emitRekeyRequest` caller (`"compromise"`).** Reserved but unimplemented. Whoever adds it MUST gate on `transportDown()` at its call site — captured by the precondition doc-line on `emitRekeyRequest`. No enforced guard today (see § Security review).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary. The gate sits entirely on the trusted binary side of the relay boundary; it consumes only `m.cfg.Connected` (a construction-time `func() bool`) and internal session state. No untrusted phone/relay input reaches the decision. The relevant boundary the gate *protects* is the Noise send-nonce sequence on `s.send` — internet-exposed — and the fix strictly reduces what crosses it (no blind seal while down).
- **[Cryptographic primitives — nonce reuse/burn]** This is the core of the ticket. The Noise send-nonce is strictly sequential; the status quo seals-then-drops a `rekey_request` while the transport is down, gapping the phone's recv nonce → MAC failure → 4421 kill. The fix gates the seal on `transportDown()` **before** `s.send.Encrypt`, on the single-owner Run goroutine, so no nonce is burned for an undeliverable frame. **Net-positive**, identical class and reasoning to #874. No key/nonce is reused; the seal path is untouched when it *does* run (transport up), preserving in-order single-writer nonce advance.
- **[Concurrency — TOCTOU]** The MUST-FIX risk for this ticket is a check-then-seal gap. Addressed structurally: both gate sites and the seal run on the Run goroutine; `s.send` has a single owner; nothing mutates `s.send` between check and seal. `transportDown()` reads an immutable config field. No lock/atomic added. Residual 1-frame up→down race documented in § Concurrency, accepted (strictly better than status quo, matches #874's accepted residual).
- **[Concurrency — goroutine lifecycle]** The retry `time.AfterFunc` callback selects on `m.wake` vs `runCtx.Done()` (copied from `armRekeyTimer`); a fired-but-undelivered wake drains on Run exit — no leak. The self-healing retry loop is bounded: a permanently-down transport is reaped by the 15-minute idle sweep, so the loop cannot spin forever on a dead session (documented in `rekeyRetryInterval`).
- **[Error messages, logs, telemetry]** `ErrTransportDown`'s text is generic operator guidance ("transport down — retry"); it leaks no conn-id, token, key, or internal state. The new deferred-log line is content-free (event + conn-id + reason + retry interval), matching the package's `v2.rekey.*` field discipline. No payload, header, or key material logged.
- **[Threat model alignment — protocol-mobile.md § Re-key / Security model]** The design preserves the 1-hour rekey cadence (§ Re-key) — a transport blip *defers* rekey, it does not skip it (retry re-arms; on reconnect the cadence resumes, and any successful handshake re-bases via `rekeyComplete`). It removes a spurious `StatusHandshakeFailure` teardown that mislabels a transport outage as a crypto failure. No downgrade: nothing lets an attacker *suppress* rekey — a genuinely-gone phone is still reaped by idle teardown; only the *blind emit* is deferred.
- **[Tokens/secrets]** N/A — no token/credential handling in this path (the rekey nonce hazard is covered under Cryptographic primitives above).
- **[File operations / Subprocess / Network I/O size-caps]** N/A — no filesystem, subprocess, or new socket-read surface; this is an in-memory timer/state change on an already-established, size-capped (`maxNoisePayloadBytes`, unchanged) transport.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-10
