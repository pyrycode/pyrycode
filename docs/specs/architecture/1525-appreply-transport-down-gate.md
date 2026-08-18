# Spec: Probe transport-down in `forwardAppReply` before the seal (#1525)

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/relay/v2session.go` → **`forwardAppReply`** — the only function this ticket edits. Read the whole body plus its doc comment: the `V2StateOpen` gate, the `s.send.Encrypt` seal, the two never-emit-unsealed drop branches, and the trailing `m.send`.
- `internal/relay/v2session.go` → **`transportDown`** — the predicate to reuse verbatim. Nil `Connected` ⇒ reads "up".
- `internal/relay/v2session.go` → **`drainOnce`** — the #874 precedent for an *in-function* hold. Read its leading transport-down comment block; it states the whole rationale (probe off-lock, before the seal, because the post-send `Outbound` error is too late).
- `internal/relay/v2session.go` → **`handleWake`**, the `wakeRekeyEmit` arm — the #912 precedent for a *call-site* gate, and the reason this ticket does **not** copy that placement (see § Design, "Placement").
- `internal/relay/v2session.go` → **`send`** — why the post-hoc error is useless here: it swallows the `Outbound` error at Debug, after the nonce is spent.
- `internal/relay/v2session.go` → **`Run`**, the `m.appReply` arm — the single call site of `forwardAppReply`.
- `internal/relay/v2session.go` → **`forwardToRun`** — the worker→Run hand-off, and its `s.done` arm. Load-bearing for § Testing strategy (why the AC5 test is a direct call, not a driven scenario).
- `internal/relay/v2session.go` → **`forwardEnvelope`** — its doc comment states the package's no-AEAD-bytes-in-logs discipline that AC3 points at.
- `internal/relay/v2session_seams.go` → **`V2SessionConfig`**, the `Connected` field — the seam's documented contract, including "NOT a security decision" and the accepted up→down residual race.
- `internal/relay/v2session_test.go` → **`gatedRecorder`**, **`newGatedRecorder`**, its `outbound` / `connected` methods — the transport-flip harness. Note: when down, `outbound` records **nothing** and returns `errTransportDown`. That detail decides how AC2 must be asserted.
- `internal/relay/v2session_test.go` → **`driveToOpen`**, **`openSession`** — handshake driver; `initRecv` is the nonce oracle.
- `internal/relay/v2session_test.go` → **`decryptAppFrame`**, **`sealAppFrame`** — the oracle and its inbound twin.
- `internal/relay/v2session_test.go` → **`bufferLogger`** (a `slog.LevelDebug` handler), **`waitForLogContains`**, **`waitForEnvelopes`** — log capture + the synchronisation knob for a manager action that emits no envelope.
- `internal/relay/v2session_test.go` → **`TestV2Session_Push_ReconnectWhileDownDoesNotSeal`** — the #874 precedent test named in AC1.
- `internal/relay/v2session_test.go` → **`TestV2Session_RekeyScheduled_DeferredWhileTransportDown_EmitsOnRecovery`** — the #912 precedent test named in AC1. Its "clean decrypt under `initRecv` proves zero seals happened while down" comment is the oracle argument to mirror.
- `internal/relay/v2session_appframe_test.go` → **`blockingHandler`**, **`prolificHandler`**, **`waitForConnNoiseMsg`** — the handler doubles that make a reply arrive at a chosen moment. `blockingHandler` is what holds a handler open across the transport flip.
- `internal/relay/v2session_appframe_test.go` → **`TestV2Session_SlowHandler_DoesNotStallOtherConn`** — the closest structural template for the new test (two handler types registered, one blocking, one prolific).
- `internal/relay/connection.go` → **`Connected`**, and `internal/transport/wssclient.go` → **`IsConnected`** — the production seam behind the probe. Both are mutex/`closeCh`-guarded; the probe is safe to call from Run.
- `cmd/pyry/relay.go` → **`startRelayV2`**, the `relay.V2SessionConfig{…}` literal — confirms `Connected: conn.Connected` is wired in production, so this guard is live rather than nil-inert.
- `docs/knowledge/features/v2-session-manager.md` § "Out of scope (deferred)" — the bullet beginning **"Extending the `transportDown()` guard beyond `drainOnce`"**. This is the deferral the ticket overrides. Read it; do **not** edit it (see § Note for the documentation phase).

## Context

`forwardAppReply` seals a handler reply under `s.send` with no transport-liveness probe ahead of the `Encrypt`, and hands the result to `send`, which swallows the `Outbound` error at Debug. A reply sealed while the binary↔relay leg is down is therefore dropped *after* the Noise send counter advanced.

That is not a lost message; it is a permanent desync. `noise.CipherState` carries a monotonic 64-bit counter, and relay↔binary is a single multiplexed WebSocket with no per-connection disconnect frame — the phone observes silence, leaves its recv `CipherState` untouched, and the next frame that *does* arrive fails AEAD. The daemon's own mirror of that situation closes at `StatusProtocolMismatch` (4421) on inbound `Decrypt` failure, and flynn/noise leaves the receive counter unchanged on failure, so every subsequent frame fails identically. One burned nonce tears down a session that was never broken, and it does not self-heal.

#874 fixed this on the push drain and #912 on the two rekey-emit paths. Both shipped the same shape: probe before the seal, because reacting to the post-send error is structurally too late.

**Why the #874 deferral expired.** #874 deliberately left the dispatch-reply seal alone, on the reasoning that a reply is sealed microseconds after its inbound frame arrives, so a within-grace blip cannot land inside that window. #965 then moved handler execution onto a per-conn worker: the reply now returns to `Run` up to a full handler duration later — as long as `createConversationMintTimeout` (30 s). The window is now wide enough to hold an entire blip. The deferral sentence has never been revised since the change that invalidated it.

No live repro exists; the justification is the widened window plus two shipped precedents on the same hazard in the same file.

## Design

One guard, one function, one production file.

### Placement

Inside `forwardAppReply`, **after** the existing `V2StateOpen` gate and **before** `s.send.Encrypt`.

*Inside the function, not at the call site* — and that is a deliberate reading of #912's precedent rather than a departure from it. #912 gated at the call site because its two callers **diverge after the decision**: the scheduled wake re-arms a retry timer and returns; the manual path must report back to its control-plane caller. `forwardAppReply` has exactly one caller (`Run`'s `m.appReply` arm) and nothing diverges after the decision — the reply is dropped, full stop, with no timer to re-arm, no error to return, and no bookkeeping to undo. So the correct analogue here is `drainOnce`, which holds inside the function. Applying #912's placement rule to a single-caller, no-divergence site would add an indirection that buys nothing.

*After the state gate* — AC5. Ordering is load-bearing exactly as it was in #912: the pre-existing eligibility drop must keep its exact behaviour and its exact log line in **every** transport state. A session torn down mid-handler is dropped on the not-open branch whether the leg is up or down.

### Contract of the new branch

```go
// signature unchanged
func (m *V2SessionManager) forwardAppReply(s *V2Session, reply protocol.RoutingEnvelope)
```

The branch: if `m.transportDown()`, emit one Debug line and `return`. It seals nothing, marshals nothing, forwards nothing, buffers nothing, and mutates no session state.

The comment on the branch must record *why the probe is before the seal* (the post-send `Outbound` error arrives after the nonce is already spent) and *why the reply is not parked* (see below) — matching the density of the `drainOnce` and `wakeRekeyEmit` comment blocks it sits beside.

### What this change explicitly does not do

The reply is undeliverable either way today; `send` already drops it. This ticket stops paying a nonce for that non-delivery — it does not make the reply arrive. Parking the reply for post-recovery delivery needs a new buffer, an eviction policy, and an ordering rule against the push queue; that is a strictly larger design and is out of scope. The developer must not add buffering, retry, or re-signalling.

### Log line (AC3)

Exactly one Debug line on the down path, carrying **only**:

- `event` — `v2.app_reply.dropped_transport_down`
- `conn_id` — `s.connID`

The event slug **is** the reason AC3 asks for, mirroring #912's `v2.rekey.emit.deferred_transport_down`. Do not add a separate `reason` key; do not add `reply.Frame`, the inner envelope type, `InReplyTo`, ciphertext, plaintext, key bytes, or any length derived from them.

Debug, not Warn/Info, for two reasons: the sibling not-open drop in this same function is Debug, and `send`'s own comment states the house posture — transport-down is expected during reconnect, keep the warn channel clean. A blip on a chatty conn produces one line per reply, so this level choice is also what keeps a blip from flooding an operator's warn stream.

### Byte-identity for unwired callers (AC4)

`transportDown()` is `m.cfg.Connected != nil && !m.cfg.Connected()`. Every existing `internal/relay` test that does not wire `Connected` reads "up" and takes the pre-change path instruction-for-instruction. No existing test may need editing; if the developer finds one that does, that is a signal the guard landed in the wrong place — stop and re-read § Placement.

## Concurrency model

No new goroutines, no new channels, no locks.

`forwardAppReply` runs on the `Run` goroutine, the single owner of `s.send` and the sole writer of `s.state`. The probe is a plain read of `m.cfg.Connected`, a function field set once in `NewV2SessionManager` and never mutated afterwards. Production's `Connected` is `(*relay.Connection).Connected` → `(*transport.Client).IsConnected`, which gates on `closeCh` and reads `c.conn` under `c.mu` — safe to call from Run under `-race`.

The single-frame TOCTOU at the up→down transition instant carries over unchanged from #874 and #912: the probe can read "up" microseconds before the leg drops, sealing one frame that is then dropped. Both precedents accepted this explicitly and the `Connected` seam's own doc comment states it ("a true result is best-effort … a false result reliably holds"). It is not this ticket's problem and must not be defended further — no retry, no double-check, no post-send compensation.

The down→up direction needs no handling at all, because nothing is held: an unsealed reply is discarded, not queued, so there is no stranded state for a reconnect edge to flush. That is the one structural difference from #874, where the held head *does* need `m.cfg.Reconnect` to wake the drain.

## Error handling

The four exits from `forwardAppReply` after this change, in order:

1. **`s.state != V2StateOpen`** — existing. Debug, unchanged text, unchanged position. Reached in every transport state.
2. **`m.transportDown()`** — new. Debug, content-free, one line. Nothing sealed, nothing forwarded.
3. **`s.send.Encrypt` error** — existing. Warn, reply dropped rather than emitted unsealed (#446). Reachable only on the transport-up path now, exactly as `drainOnce`'s downstream drops are.
4. **`marshalInnerFrameV2` error** — existing. Warn, same posture.

No new error value, no new sentinel, no return-type change. The function stays `func(...)` with no result — there is no caller that could act on one.

## Testing strategy

Package `relay`, in-package tests. Put the new tests in `internal/relay/v2session_appframe_test.go` under a banner comment (`--- app-reply transport-down gate tests (#1525) ---`), because the subject is the #965 appReply path and the handler doubles live there. The gated-transport and log helpers come from `v2session_test.go`, same package, no import or move needed. Run under `-race`.

### Test 1 — the nonce oracle (AC1, AC2, AC3)

Name it for what it pins, e.g. `TestV2Session_AppReply_TransportDown_BurnsNoNonce`.

Wiring:

- `newGatedRecorder()`; `bufferLogger()` for the log assertion.
- `Outbound` is **not** `gated.outbound` directly. Wrap it in a closure that increments an `atomic.Int64` **before** delegating. This is load-bearing — see "Why the wrapper" below.
- `Connected: gated.connected`.
- `Handlers`: `protocol.TypeSendMessage → blockingHandler(entered, release)` and `protocol.TypeListConversations → prolificHandler()`. Two distinct types so the second frame is served by a handler the first frame's `release` did not already unblock.
- `driveToOpen` with the transport **up**, so the handshake completes and `initRecv` is live. Record the attempt count after the handshake as the baseline (it will be 1 — the `noise_resp`).

Scenario:

- Feed app frame ID 1, `TypeSendMessage`, sealed under `sess.initSend`. Wait on `entered` — the handler is now parked on the worker goroutine, Run is free.
- `gated.up.Store(false)` — the leg drops mid-handler. This is the ticket's failure scenario in miniature.
- `close(release)` — the handler returns its reply, which travels worker → `forwardToRun` → Run's `m.appReply` arm → `forwardAppReply`, with the transport down.
- Synchronise on `waitForLogContains(t, logBuf, "event=v2.app_reply.dropped_transport_down")`. The drop emits no envelope, so the log is the only observable edge; this is precisely the case `waitForLogContains` documents itself for. **This assertion is AC3.**
- Assert the Outbound attempt counter is still at its post-handshake baseline. **This is AC2.**
- `gated.up.Store(true)`, then feed app frame ID 2, `TypeListConversations`, payload `{"count":1}`.
- `waitForEnvelopes` / `waitForConnNoiseMsg` for the recovery reply, then `decryptAppFrame(t, env, sess.initRecv)`. **This is AC1.** Assert `InReplyTo == 2`.

Why it is genuinely red against the unguarded tree: the down-window reply seals at nonce 0 and is then dropped by `Outbound`; the recovery reply seals at nonce 1; `initRecv` still expects 0, so `decryptAppFrame` MAC-fails and `t.Fatalf`s. Against the guarded tree the recovery reply is the first seal, lands at nonce 0, and decrypts cleanly. Confirm this by running the test against the unmodified function before wiring the guard.

**Why the wrapper.** `gatedRecorder.outbound` records nothing *and* returns an error when down. So "the recorder observed zero envelopes" is true under the down-path guard **and** under a wrong implementation that skips the seal but still hands the raw unsealed `reply.Frame` to `send`. Asserting on `rec` alone would make AC2 vacuous. The attempt counter sits outside the up/down branch and is the only thing that discriminates — it is what actually pins the #446 never-emit-unsealed rule the function's doc comment states.

### Test 2 — ordering, state gate wins (AC5)

Name it for what it pins, e.g. `TestV2Session_AppReply_NotOpenPrecedesTransportDown`.

Assert this by **calling `forwardAppReply` directly**, not by driving a scenario:

- Construct a manager via `NewV2SessionManager` with `Connected` returning `false`, `bufferLogger()`, and an `Outbound` that increments a counter. Do **not** start `Run`.
- Hand-build a `&V2Session{connID: …, state: V2StateClosed}` — the function reads only `s.state` and `s.connID` before returning on this branch, so the remaining fields (including the nil `s.send`) are never touched.
- Call `m.forwardAppReply(s, protocol.RoutingEnvelope{ConnID: …, Frame: …})`.
- Assert the buffer contains the existing not-open drop line and does **not** contain `v2.app_reply.dropped_transport_down`, and that the Outbound counter is zero.

A direct call rather than a driven teardown, deliberately: driving it would mean closing the session mid-handler, and `forwardToRun`'s `select` has an `s.done` arm alongside the `m.appReply` send. With `s.done` already closed, Go picks among ready cases at random, so the reply may never reach `forwardAppReply` at all. A driven version of this test would be flaky by construction. The direct call has no concurrency, no timing, and discriminates the exact thing AC5 is about — which of the two checks runs first. Because `Run` is not started, this touches no single-owner invariant.

### Test 3 — transport-up regression (AC4)

No new test. AC4 is "byte-identical when up, and identical when `Connected` is nil", and the existing suite already asserts that densely: `TestV2Session_SlowHandler_DoesNotStallOtherConn`, `TestV2Session_OpenState_ProlificHandler_NoDeadlock`, `TestV2Session_OpenState_ProlificHandler_EmissionOrder`, `TestV2Session_AppFrame_PerConnSerialization` and the rest of the `v2session_appframe_test.go` set all drive replies through this function with `Connected` unset. **They must pass unmodified.** Writing a fourth test asserting the same thing would add turns and pin nothing new; the AC is satisfied by leaving them untouched, and by the up-leg half of Test 1.

## Open questions

- **Log level.** Debug is specified above, argued from the sibling drop in the same function and `send`'s reconnect posture. If code-review prefers Info (as #912's deferral used), that is a one-word change and does not affect any assertion — `bufferLogger` captures at `LevelDebug` and matches on the `event=` substring either way.
- **Test home.** `v2session_appframe_test.go` is chosen because the subject is the appReply path. The #874/#912 transport-down cluster lives in `v2session_test.go` under its own banner. Either is defensible; do not split the two new tests across both files.

## Note for the documentation phase (not a developer AC)

`docs/knowledge/features/v2-session-manager.md` § "Out of scope (deferred)" carries a bullet beginning "Extending the `transportDown()` guard beyond `drainOnce`", which lists the dispatch-reply seal among the guards deferred "gated on observed failures, not pre-emptive". That sentence goes stale for this seal when #1525 lands. After the PR merges, the remaining deferrals in that bullet are `drainReplayOnce` (#1490) and the close-frame seals; the `sealError` / `closeWith` seals stay out of scope by design, since they run on teardown paths where the session dies regardless and a nonce burned there costs nothing. The developer must not edit that file.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. This function sits entirely on the trusted side: the plaintext crossed the untrusted→trusted boundary earlier, at `handleNoiseMsg`'s AEAD `Decrypt` under `s.recv`, and the reply originates from a local handler, never from the wire. The new input to the decision is `m.cfg.Connected`, a construction-time func supplied by `cmd/pyry`'s `startRelayV2` from `(*relay.Connection).Connected` — process-local, not attacker-reachable. Noted deliberately: `transportDown()` is **manager-global**, not per-conn, so a false reading suppresses replies on every conn — but the relay leg genuinely is shared, so when it reads down no conn's frame reaches anyone regardless. No cross-conn amplification beyond what the transport already imposes.
- **[Tokens, secrets, credentials]** No findings — no token or credential handling on this path. The change strictly reduces exposure: on the down path a reply that may carry user content is neither sealed nor logged.
- **[File operations]** Not applicable — no filesystem access anywhere in `forwardAppReply` or in the probe chain (`Connected` → `IsConnected` reads an in-memory conn pointer under a mutex).
- **[Subprocess / external command execution]** Not applicable — no process execution on this path.
- **[Cryptographic primitives]** No findings, and this is the category that matters most, so stated concretely. The change **removes** an `s.send.Encrypt` call on the down path; skipping a seal cannot cause key or nonce **reuse** — the counter simply does not advance. The opposite failure, a nonce **gap**, is the defect being fixed. `CipherState` holds only a key plus a counter, and nothing else in the manager observes "how many seals have happened", so a skipped seal leaves no derived state inconsistent. Interaction with re-key checked explicitly: `rekeyComplete` replaces `s.send` wholesale, so a skipped seal neither strands nor corrupts a swap. And the guard cannot be turned into an attack primitive, because it only ever *skips* seals — manufacturing a gap would require a seal-then-drop, which is exactly what this removes.
- **[Network & I/O]** No findings. No new reads, no new parsing, so no new size cap is owed. Resource exhaustion checked: the reply is **discarded, not buffered** (parking is explicitly out of scope), so the change adds no unbounded queue; `forwardToRun`'s blocking hand-off keeps the worker's existing backpressure intact. Log-volume amplification considered — one Debug line per dropped reply during a blip — and bounded by `appFrameQueueDepth` per conn plus the Debug level, which is off in production by default. That is why § Design pins Debug rather than Warn.
- **[Error messages, logs, telemetry]** No findings, with the MUST-NOT-log list pinned in § Design: no `reply.Frame`, no inner envelope type, no `InReplyTo`, no ciphertext, no plaintext, no key bytes, no length derived from any of them. MUST-log is `event` + `conn_id` only, matching the package's no-AEAD-bytes-in-logs discipline stated in `forwardEnvelope`'s doc comment. No telemetry or metrics surface is touched.
- **[Concurrency]** No findings on the added code. The probe is a plain read of an immutable-after-construction func field, executed on `Run` — the single owner of `s.send` and sole writer of `s.state`. No lock is taken, so no lock-ordering question arises. The production seam is race-safe: `IsConnected` gates on `closeCh` and reads `c.conn` under `c.mu`. No goroutine is spawned, so no lifecycle or leak question arises; shutdown behaviour is unchanged because the guard holds no state to unwind.
- **[Concurrency — TOCTOU]** OUT OF SCOPE. The single-frame race at the up→down transition instant (probe reads "up", leg drops, one frame seals and is dropped) carries over unchanged from #874 and #912, and is documented on the `Connected` seam itself. Both precedents accepted it; the ticket names it as not-to-be-defended-further. Any future work on it would have to change the seam's contract, not this call site.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md`'s relevant property is that relay↔binary carries no per-connection disconnect frame, so a phone cannot observe a drop and cannot resynchronise its recv counter — which is what converts a burned nonce from a lost message into a silent, non-self-healing desync surfacing as a 4421 teardown of a healthy session. This change directly reduces that exposure on the widest-window seal in the file. Residual, unchanged from today: a genuinely undeliverable reply is still lost and the phone still sees no error — `send` already dropped it before this ticket. Named out of scope with owners: the seven inline `forwardEnvelope` reply seals are #1526, `drainReplayOnce` is #1490 (both blocked by this ticket so one guard shape lands across both halves), and reply parking for post-recovery delivery is unowned by design, per the ticket's own Technical Notes.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
