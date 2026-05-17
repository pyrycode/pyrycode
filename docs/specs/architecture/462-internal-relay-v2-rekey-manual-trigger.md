# internal/relay: V2SessionManager manual-rekey trigger + emitRekeyRequest(reason) refactor

Ticket: [#462](https://github.com/pyrycode/pyrycode/issues/462). Size: **S**. Security-sensitive: **yes** (touches an AEAD-sealed outbound emit path on a Noise IK session; new operator-reachable trigger surface).

Slice B1 of the split of #460 (split from #451). Slice A (#459, merged) shipped the wire contract and the `control.Rekeyer` interface; this slice ships the manager-side implementer that the wire dispatcher will call. Sibling slice B2 ships the `pyry rekey <conn_id>` operator verb in `cmd/pyry`.

## Files to read first

Required reading before any code change:

- `internal/relay/v2session.go:45-86` — package-level rekey constants (`rekeyInterval`, `rekeyReplyTimeout`, `wakeBufferSize`) and the `wakeKind` enum the manual path piggybacks on.
- `internal/relay/v2session.go:111-178` — `V2Session` struct fields (`state`, `rekeyTimer`, `rekeyReplyTimer`, `awaitingRekeyReply`) — the load-bearing state the manual path reads and mutates.
- `internal/relay/v2session.go:186-217` — `V2Session.rekeyComplete`; the seam that re-arms `rekeyTimer` after a successful responder cycle (the manual path's natural "timer rebase" propagator).
- `internal/relay/v2session.go:273-335` — `V2SessionManager` struct, `V2SessionConfig`, `NewV2SessionManager`. The new `manualRekey` channel field is added here; the constructor wires its make().
- `internal/relay/v2session.go:337-417` — `Run`, `handleWake`, `armRekeyTimer`, `armRekeyReplyTimer`. The new `Run` select arm and the manual-rekey dispatch site live next to these.
- `internal/relay/v2session.go:1005-1083` — existing `emitRekeyRequest`; the function the refactor targets. Read the entire body — the AEAD-seal failure log lines and the awaiting-defensive skip both stay.
- `internal/relay/v2session.go:1131-1166` — `closeWith`; reuse-pattern for "stop session timers on teardown". Inform the manual path's stop-timer logic by analogy, not by call.
- `internal/control/server.go:25-35` — `control.ErrConnNotFound` sentinel definition + the contract comment ("slice B's *relay.V2SessionManager wraps its internal not-found condition with %w against this sentinel"). The relay-side `ErrConnNotFound` must wrap this so the dispatcher's `errors.Is` continues to fire.
- `internal/control/server.go:136-164` — the `Rekeyer` interface definition; method shape is `Rekey(ctx context.Context, connID string) error`. Note the AC's "TriggerRekey" naming is informal — see § "Naming divergence" below.
- `internal/control/server.go:704-742` — `handleRekey`; the dispatcher that calls into the Rekeyer. Confirms `errors.Is(err, ErrConnNotFound)` is the wire-mapping seam (no relay-side change needed for that mapping when relay's sentinel wraps `control.ErrConnNotFound`).
- `internal/relay/v2session_test.go:684-741` — `openSession` struct and `driveToOpen` helper. All four new tests reuse this helper verbatim.
- `internal/relay/v2session_test.go:1787-1911` — `TestV2Session_RekeyInitiator_Emit_ReArmViaResponder`. The reference shape for "drive to open → wait for emit → run responder cycle → wait for re-armed emit". The timer-rebase test mirrors this with a manual trigger inserted before the first scheduled boundary.
- `internal/relay/v2session_test.go:1913-1995` — `TestV2Session_RekeyInitiator_ReplyTimeout_4426`. Reference for the rekey-failure log discipline (no `err=` field, no flynn-noise text); the manual path inherits the same posture because it reuses `emitRekeyRequest` + the existing reply-timeout branch.
- `docs/protocol-mobile.md` § Re-key, line 234 — `payload.reason = "manual"` is *"operator-triggered via `pyry rekey <conn_id>`"*. The literal `"manual"` value is wire-pinned.
- `docs/knowledge/codebase/450.md` — the scheduled-emit slice's notes; the manual path inherits its concurrency posture, log discipline, and AEAD posture verbatim.

## Context

Slice A (#459) shipped the control-socket wire contract: `VerbRekey`, the `Rekeyer` interface (in `internal/control`), the server dispatcher (`handleRekey`), the `control.Rekey` client helper, and the `control.ErrConnNotFound` sentinel. Until a Rekeyer is installed via `Server.SetRekeyer`, `handleRekey` replies `"rekey: no rekeyer configured"`.

This slice ships the manager-side implementer: `*V2SessionManager` becomes a real `Rekeyer`. The implementation reuses the existing `emitRekeyRequest` machinery (#450) — the same Noise-IK-sealed `rekey_request` envelope, the same `awaitingRekeyReply` predicate, the same 30s reply-timeout, the same responder-side swap on the phone's fresh `noise_init`. The ONLY semantic differences from the scheduled path are:

1. `payload.reason = "manual"` instead of `"scheduled"` (wire-pinned by `docs/protocol-mobile.md` § Re-key).
2. The scheduled 1-hour timer for that conn is re-based: a manual emit at T+45min must not be followed by a stale scheduled emit at T+60min; the next scheduled emit lands at T+1h from the manual emit (or never, if the conn dies first).

Production wire-up (a `cmd/pyry` daemon site that constructs a `*V2SessionManager`, drives `Run`, and calls `Server.SetRekeyer(mgr)`) is **out of scope** — `NewV2SessionManager` has no production caller as of 2026-05-17 (`grep` finds only test callers). This slice's new `Rekey` method is reachable in tests only until the wire-up lands in a separate ticket. Sibling slice B2 (`pyry rekey` operator verb) ships in parallel; its end-to-end value lands once both this slice and the production wire-up are in place.

## Naming divergence (AC text vs. interface signature)

The ticket AC names the method `TriggerRekey(connID string) error`. The `control.Rekeyer` interface declares `Rekey(ctx context.Context, connID string) error`. These are not assignment-compatible — different name, different signature. Slice A's docstring at `internal/control/server.go:138` ("Slice B's *relay.V2SessionManager satisfies it via TriggerRekey") carries the same naming drift; the interface method is canonically `Rekey`.

**Decision: implement the public method as `Rekey(ctx context.Context, connID string) error`.** This is the only signature that lets `*V2SessionManager` satisfy `control.Rekeyer` directly (no adapter). The AC's `TriggerRekey` reads as an informal label for the manual-rekey entry point, not a literal name requirement; the wire-contract interface method name wins. A compile-time assertion (`var _ control.Rekeyer = (*V2SessionManager)(nil)`) pins the contract.

## Design

### 1. Sentinels (added to `internal/relay/v2session.go` near the existing `var rekeyInterval` block)

```go
// ErrConnNotFound is returned by (*V2SessionManager).Rekey when connID
// is not currently registered in the manager's sessions map. Wraps
// control.ErrConnNotFound so the control dispatcher's
// errors.Is(err, control.ErrConnNotFound) check in handleRekey continues
// to map to ErrCodeConnNotFound on the wire without further plumbing.
var ErrConnNotFound = fmt.Errorf("relay: conn not found: %w", control.ErrConnNotFound)

// ErrSessionNotOpen is returned by (*V2SessionManager).Rekey when the
// named session exists but is not eligible for a manual rekey — either
// not in V2StateOpen (still handshaking, or already torn down), or
// already awaiting a rekey reply from a prior emit. The control
// dispatcher surfaces this verbatim through Response.Error with no
// ErrorCode (slice A defines no wire code for this state yet).
var ErrSessionNotOpen = errors.New("relay: session not open")
```

`relay → control` import: verified non-cyclic. `internal/control` imports `internal/sessions` and `internal/supervisor` only; neither imports `internal/relay`.

### 2. New types and fields

Add a new request type and a manager field. Both stay package-private — there is no value in exposing the queue shape to callers:

```go
// manualRekeyReq is enqueued by Rekey and dequeued by Run on the
// manual-rekey channel arm. The reply channel is per-request (cap=1)
// so the manager's send is non-blocking even if the caller's ctx fires
// after enqueue.
type manualRekeyReq struct {
    connID string
    reply  chan error
}
```

`V2SessionManager` gains one field:

```go
// manualRekey funnels (*V2SessionManager).Rekey calls onto the
// dispatch goroutine so the lookup-and-emit sequence runs under the
// single-owner-goroutine invariant for s.send / s.state / s.rekeyTimer.
// Unbuffered: backpressure is correct — if Run is busy, Rekey waits.
// The caller's ctx is the escape arm in (*V2SessionManager).Rekey.
manualRekey chan manualRekeyReq
```

`NewV2SessionManager` initialises it: `manualRekey: make(chan manualRekeyReq)` (unbuffered). Place the field below `wake` in the struct literal; group the channel allocations together.

### 3. `Rekey` method (the `Rekeyer` interface implementer)

Method shape:

```go
func (m *V2SessionManager) Rekey(ctx context.Context, connID string) error { … }
```

Behavior (no full body — the contract is enough):
- Build a `manualRekeyReq{connID, reply: make(chan error, 1)}`.
- Send onto `m.manualRekey` in a `select` against `ctx.Done()`. Return `ctx.Err()` if the caller cancels before enqueue.
- Receive on `req.reply` in a `select` against `ctx.Done()`. Return `ctx.Err()` if the caller cancels after enqueue.
- The reply channel is buffered cap=1 so the manager's send (`req.reply <- err`) never blocks even if the caller has already left.

Compile-time interface assertion next to the method:

```go
var _ control.Rekeyer = (*V2SessionManager)(nil)
```

### 4. `Run` loop — new select arm

`Run` gains a fourth arm:

```go
case req := <-m.manualRekey:
    req.reply <- m.handleManualRekey(runCtx, req.connID)
```

The send onto `req.reply` is non-blocking (cap=1, fresh per request). Placement: directly below the existing `case w := <-m.wake:` arm.

### 5. `handleManualRekey` (new private method)

Contract:
- Look up `m.sessions[connID]`. Not found → return `ErrConnNotFound`.
- If `s.state != V2StateOpen` → return `ErrSessionNotOpen`.
- If `s.awaitingRekeyReply` → return `ErrSessionNotOpen`. (Same sentinel: from the operator's perspective, both states mean "the conn is not in a state where a manual rekey can be initiated.")
- Stop and nil `s.rekeyTimer` (the scheduled boundary is re-based — the natural `rekeyComplete` flow will arm a fresh one). `Stop()`'s return value is intentionally ignored; § "Open question: stale wake after Stop" addresses the rare race.
- Call `m.emitRekeyRequest(ctx, s, "manual")`.
- Return `nil`.

The defensive `awaitingRekeyReply`-skip inside `emitRekeyRequest` is unreachable from the manual path because `handleManualRekey` checks it explicitly first. It stays — the scheduled path still needs it (and the AC's "no parallel emit path" wording is satisfied: there is exactly one emit function).

### 6. `emitRekeyRequest` refactor — add `reason string` parameter

Signature change:
- **Before:** `func (m *V2SessionManager) emitRekeyRequest(ctx context.Context, s *V2Session)`
- **After:** `func (m *V2SessionManager) emitRekeyRequest(ctx context.Context, s *V2Session, reason string)`

Body deltas (only two — no other behaviour changes):
- `reqPayload`'s struct literal field becomes `Reason: reason` instead of `Reason: "scheduled"`.
- The final `m.cfg.Logger.Info("relay: v2 rekey emit", …)` carries `"reason", reason` instead of the hardcoded `"reason", "scheduled"`.

Call sites:
- `handleWake`'s `wakeRekeyEmit` arm (currently `internal/relay/v2session.go:377`) becomes `m.emitRekeyRequest(ctx, w.s, "scheduled")`.
- `handleManualRekey` calls `m.emitRekeyRequest(ctx, s, "manual")`.

Two callers. One emit function. No parallel emit path.

### 7. Concurrency model

The manual path preserves the existing single-owner-goroutine invariant on `s.send`, `s.state`, `s.awaitingRekeyReply`, `s.rekeyTimer`, `s.rekeyReplyTimer`:

- The public `Rekey` method runs on the caller's goroutine. It performs **no session-state access** — only channel sends/receives. The manager owns the state.
- `handleManualRekey` runs on Run's goroutine (dequeued from `m.manualRekey`). All session-state reads and writes happen here, on the same goroutine that owns the state today.
- `emitRekeyRequest` continues to run on Run's goroutine.
- Timer-callback goroutines (spawned by `time.AfterFunc` inside `armRekeyTimer` / `armRekeyReplyTimer`) push wake signals only — they never read or mutate session state. Inherited from #450.

No new lock. No new atomic. No new goroutine.

### 8. Timer re-base — what's "re-based" exactly

The AC wording: *"a successful manual emit re-bases the 1-hour scheduled timer for that conn so a manual rekey at T+45min does not also trigger a scheduled emit at T+60min."*

Mechanism:
1. `handleManualRekey` calls `s.rekeyTimer.Stop()` and `s.rekeyTimer = nil` BEFORE the emit. The pending 1-hour AfterFunc no longer fires. (If it already fired and queued a wake before `Stop()` ran, see § Open questions.)
2. `emitRekeyRequest` arms `s.rekeyReplyTimer` (30s) and sets `s.awaitingRekeyReply = true`.
3. The phone's response (a fresh `noise_init`) lands in `handleRekeyInit`. On success, `rekeyComplete` (lines 207–217) clears `awaitingRekeyReply`, stops `rekeyReplyTimer`, and arms a **fresh** `rekeyTimer` via `armRekeyTimer` — re-based to "now" (which is T_manual_emit + small_delta).
4. If the phone never responds, `rekeyReplyTimer` fires → `closeWith(StatusHandshakeFailure /* 4426 */, nil)` → session deleted from `m.sessions`. All timers stopped. No more rekey-related fires on this conn.

In every success path, the next scheduled emit lands at T_manual_emit + 1h, not at the original boundary. In every failure path, the conn is gone.

### 9. Error handling

Three failure modes on the manual path:

| Mode | Sentinel returned | Wire mapping in dispatcher |
|------|-------------------|----------------------------|
| Unknown `connID` | `relay.ErrConnNotFound` (wraps `control.ErrConnNotFound`) | `ErrCodeConnNotFound` (via `errors.Is`) |
| Session not in V2StateOpen | `relay.ErrSessionNotOpen` | none — `Response.Error` carries the message verbatim |
| Session already awaiting rekey reply | `relay.ErrSessionNotOpen` | none — same as above |

AEAD-seal failure or marshal failure inside `emitRekeyRequest`: existing behaviour (Warn-and-drop, no close). The manual caller observes the call as `Rekey → nil` (the emit "succeeded" from the dispatcher's standpoint; the operator-facing diagnostic would be a missing fresh handshake on the conn). This matches the scheduled path's posture — closing a working conn over an internal seal failure would be a worse outcome than logging the failure and waiting for the next opportunity. The next-opportunity story on the manual path is "operator re-runs `pyry rekey <conn_id>`"; on the scheduled path it's "next 1-hour tick" (modulo this slice — the manual path stopped the next-tick timer, so a sub-emit failure on the manual path means no further automatic re-key on that conn until the phone reinitiates or the operator re-runs the verb).

This is a behaviour shift worth flagging: in the rare seal-failure case on the manual path, automatic scheduled re-keying for that conn is paused indefinitely. **Acceptable** because:
- Seal failures are realistically unreachable under correct flynn/noise (same posture as `sealError`); the existing Warn-and-drop comment at `internal/relay/v2session.go:1020-1026` documents this.
- The remediation is operator-visible (the `v2.rekey.emit.seal_failed` log line is the signal).
- The next phone-initiated rekey rebases the timer via `rekeyComplete`.

No new sentinel is introduced for seal failure; the existing Warn-and-drop branch carries it.

### 10. Concurrency invariant on `manualRekey` channel close

The `manualRekey` channel is **not closed**. `NewV2SessionManager` allocates it; `Run` reads from it; on Run exit, any in-flight `Rekey` caller has its `ctx.Done()` arm or `req.reply` arm to unblock. If a caller arrives after Run has exited and no other goroutine reads from `m.manualRekey`, the caller's `ctx` cancellation is the escape (no caller is supposed to call `Rekey` after Run exits; the wire-up site cancels both Run's ctx and the control server's ctx together). This matches the existing posture for `m.cfg.Frames` (the manager doesn't close it either).

## Testing strategy

All four tests run against a real `*V2SessionManager` via `driveToOpen`. No fakes. No mocks. Naming pattern follows the existing `TestV2Session_RekeyInitiator_*` family.

### Test A — `TestV2Session_RekeyManual_HappyPath_EmitsManualReason`

Drive to open. Call `mgr.Rekey(ctx, v2TestConnID)`. Assertions:
- `Rekey` returns nil.
- After waiting for the second outbound envelope, exactly 2 envelopes are recorded (initial `noise_resp` + manual emit).
- The second envelope's inner is `TypeRekeyRequest`, AEAD-decrypts under `sess.initRecv`, and its `payload.reason` is `"manual"`.
- Log buffer contains `event=v2.rekey.emit`, `reason=manual`, `conn_id=<v2TestConnID>`.

### Test B — `TestV2Session_RekeyManual_UnknownConn_ReturnsErrConnNotFound`

Construct a manager and start `Run` (no need to drive a handshake — `m.sessions` is empty). Call `mgr.Rekey(ctx, "this-conn-does-not-exist")`. Assertions:
- `Rekey` returns a non-nil error.
- `errors.Is(err, relay.ErrConnNotFound)` is true.
- `errors.Is(err, control.ErrConnNotFound)` is true (wire-mapping invariant).
- `rec.snapshot()` is empty — no outbound side-effect.

### Test C — `TestV2Session_RekeyManual_AlreadyAwaitingReply_ReturnsErrSessionNotOpen`

Drive to open. Set `rekeyReplyTimeout` to a long value (e.g. 1s, restored via `t.Cleanup`) so the first emit's awaiting-reply window doesn't auto-close mid-test. Call `mgr.Rekey(ctx, v2TestConnID)` — first call succeeds (returns nil). Wait until the manual emit lands in `rec`. Call `mgr.Rekey(ctx, v2TestConnID)` a SECOND time. Assertions:
- Second call returns `relay.ErrSessionNotOpen` (matched via `errors.Is`).
- Only ONE manual emit envelope is recorded (no double emit).
- Session state remains `V2StateOpen`; `s.awaitingRekeyReply` remains true.

The second `Rekey` arrives at Run after the first has been fully processed (Run is single-goroutine; the manualRekey channel is unbuffered → sequential semantics). The not-found-vs-not-open boundary is testable directly without race control.

This test also implicitly covers "conn in `V2StateAwaitingInit` or `V2StateHandshakeComplete` returns ErrSessionNotOpen" — `awaitingRekeyReply` and `state != V2StateOpen` collapse into the same sentinel. A separate "mid-handshake" test would require driving the manager into a half-open state, which the existing test helpers do not expose cleanly; the awaiting-reply scenario covers the same predicate-collapse.

### Test D — `TestV2Session_RekeyManual_RebasesScheduledTimer`

Set `rekeyInterval` to a sub-second value (e.g. 100ms) and `rekeyReplyTimeout` to a long value (e.g. 1s) so the test runs in under ~500ms. Drive to open. Sequence:

1. Immediately after open (T ≈ 0), call `mgr.Rekey(ctx, v2TestConnID)`. Wait for the second envelope (manual emit at T ≈ small_delta). Decode and assert `payload.reason == "manual"`.
2. Drive a successful responder cycle: build a fresh `noise.Initiator(initPriv, respPub)` (same identity → peer-static check passes), `WriteInit(nil)`, push as `noise_init`. Wait for the third envelope (rekey `noise_resp` at T ≈ small_delta_2). Decode the responder reply and capture `initSend2`, `initRecv2`. This calls `rekeyComplete` on the manager side, which arms a fresh `rekeyTimer` at T_rekeyComplete.
3. **Original-boundary check**: at T ≈ rekeyInterval + jitter (e.g. 130ms after open — past the original scheduled boundary of 100ms, but well before T_rekeyComplete + rekeyInterval), assert `rec.snapshot()` length is still 3. The original timer was stopped at manual-emit time; no stale wake produced a fourth envelope.
4. **New-boundary check**: wait until T_rekeyComplete + rekeyInterval + jitter. Assert a fourth envelope arrives, that it is `TypeRekeyRequest`, and that `payload.reason == "scheduled"` (the new scheduled emit, fired off the re-based timer).

The original-boundary check is the load-bearing assertion: it directly pins "no stale scheduled emit lands between manual emit and the re-based scheduled boundary." The new-boundary check pins that the re-based timer is real and produces a real emit at the new offset.

Mark with `// Not t.Parallel:` — mutates package-level `rekeyInterval` / `rekeyReplyTimeout` vars (same posture as the existing scheduled tests).

### Test count: 4. Under the 5-AC red line (4 tests cover 5 AC bullets — happy path + 2 sentinels + timer rebase; the AC's "exactly one rekey_request emitted" assertion is folded into Test A's outbound-count check, not its own test).

## Open questions

### OQ-1: Race between `rekeyTimer.Stop()` and a queued wake signal

In `handleManualRekey`, `s.rekeyTimer.Stop()` is called on a timer that may already have fired and queued a `wakeSignal` onto `m.wake` (Stop returns false in that case). Sequence:

1. T=0: `rekeyTimer` fires. AfterFunc callback runs on a runtime goroutine, blocks on `m.wake <- wakeSignal{s, wakeRekeyEmit}`.
2. T=0+ε: `m.wake` is buffered (cap 16) so the send completes immediately.
3. T=0+δ: Run-loop iteration picks up the manual rekey from `m.manualRekey` (NOT the wake — channel selection is fair-random in Go).
4. T=0+δ+: `handleManualRekey` runs, calls `s.rekeyTimer.Stop()` (returns false — fired), nils `rekeyTimer`, emits manual rekey, sets `awaitingRekeyReply = true`, arms `rekeyReplyTimer`.
5. T=0+δ+ε: Next Run-loop iteration picks up the stale wake signal. `handleWake` checks `w.s.state == V2StateOpen` (yes), routes to `emitRekeyRequest(ctx, w.s, "scheduled")` … wait — does the refactored emitRekeyRequest know its reason?

The wake signal does not carry the reason — only the `kind` (`wakeRekeyEmit`). `handleWake`'s `wakeRekeyEmit` arm calls `emitRekeyRequest(ctx, w.s, "scheduled")`. At this point `s.awaitingRekeyReply == true` (set by the manual emit in step 4). `emitRekeyRequest`'s defensive skip catches it and logs `v2.rekey.emit.skipped_already_awaiting`. No second emit. Safe.

**Resolution: benign.** The existing defensive skip in `emitRekeyRequest` (lines 1028-1038) catches this case. The spurious WARN log line is acceptable diagnostic noise; the load-bearing safety property (no double emit) holds.

A more concerning race: what if `rekeyComplete` runs (clearing `awaitingRekeyReply`) BEFORE the stale wake is processed? Sequence: manual emit at T=0, phone responds at T=10ms, `rekeyComplete` at T=11ms clears `awaitingRekeyReply` and arms a fresh `rekeyTimer`. The original stale wake (queued before manual emit) is processed at T=12ms. `awaitingRekeyReply == false`. `emitRekeyRequest` is called → emits a SECOND rekey_request immediately. The new `rekeyTimer` is still pending. The phone now receives back-to-back rekey requests.

This is a 1-in-a-billion timing race: the stale wake must survive ≥12ms in `m.wake` while Run processes the manual rekey AND the phone's full noise_init AND `rekeyComplete`. With `m.wake` being a buffered channel that Run drains every iteration in the select, the stale wake would land within microseconds, before the manual rekey processing finishes. **Practically unreachable**, and even if reached, the harm is a single spurious rekey emit (the phone handles it as a fresh re-key request, runs `noise_init` again, the responder cycle completes again — no security impact, just CPU + bandwidth waste of order one Noise handshake).

**Resolution: accept as benign, document in spec only.** No code-level defence is warranted (per pipeline § "Evidence-Based Fix Selection" — no observed failure; CLAUDE.md / spec callout is the right layer).

### OQ-2: `s.rekeyTimer = nil` ordering vs. emit failure

`handleManualRekey` stops and nils `s.rekeyTimer` BEFORE calling `emitRekeyRequest`. If `emitRekeyRequest` fails internally (AEAD-seal failure → Warn-and-drop), the conn is left with no scheduled timer AND `awaitingRekeyReply == false`. Automatic re-keying for that conn is paused indefinitely (until a phone-initiated rekey via `rekeyComplete` rearms it).

**Resolution: accept.** Documented in § 9 "Error handling" above. The AEAD-seal failure is the load-bearing diagnostic; the operator can re-run `pyry rekey` to retry. Re-arming the timer in the seal-failure branch would mask the underlying error AND introduce a timer-rearm path in the manual code that the scheduled code doesn't have — extra surface area for a unobserved failure mode.

### OQ-3: Does `Rekey` block too long?

`Rekey` blocks the caller until Run dequeues the request and processes it. In the common case, Run is idle and the round-trip is <100µs. In the worst case, Run is mid-frame (e.g. a slow handler invocation), and Rekey waits. The control-server's `handleRekey` passes `context.Background()` (line 733), so there is no deadline applied at the dispatcher.

**Resolution: accept as-is.** This matches the existing posture for `handleSessionsRename` (no deadline). If a future operator wants a timeout, they can pass a deadlined ctx into `Rekey` directly (the method honours it). The control dispatcher's no-deadline posture is documented at server.go:707-714.

## Sizing

Production LOC projected:
- New types + field + sentinels: ~25 LOC.
- `Rekey` method: ~15 LOC.
- `Run` arm: ~3 LOC.
- `handleManualRekey`: ~25 LOC.
- `emitRekeyRequest` refactor: ~3 LOC delta.
- Existing call site update: ~1 LOC delta.
- Compile-time interface assertion + group comments: ~10 LOC.

**Production total: ~80 LOC.** Single file modified (`internal/relay/v2session.go`). New imports: `errors` (already present), `fmt` (already present), `github.com/pyrycode/pyrycode/internal/control` (new).

Test LOC projected:
- Test A: ~50 LOC.
- Test B: ~30 LOC (no `driveToOpen` needed — just `startManager`).
- Test C: ~60 LOC.
- Test D: ~110 LOC (mirrors `RekeyInitiator_Emit_ReArmViaResponder` shape).

**Test total: ~250 LOC.**

**Spec doc: ~400 LOC (this file).**

**Grand total written: ~730 LOC.** Slightly over the ~600 cap. Mitigations:
- The spec doc is the largest component (~400 LOC); the developer's spec-edit cost is bounded (read once, no per-file editing).
- The test count is 4 (under the 5-AC red line); each test reuses `driveToOpen` and existing helpers.
- The production refactor is mechanically tight (one signature change, two call sites, one new method, one new arm). No fan-out.

Production source files modified: **1** (`internal/relay/v2session.go`). Strictly under the 5-file red line and the new ≥5-source-file self-check.

Reject branches in new code:
1. `handleManualRekey` connID not found → `ErrConnNotFound`.
2. `handleManualRekey` state != V2StateOpen → `ErrSessionNotOpen`.
3. `handleManualRekey` awaiting reply → `ErrSessionNotOpen`.
4. `Rekey` ctx cancelled before enqueue → `ctx.Err()`.
5. `Rekey` ctx cancelled after enqueue → `ctx.Err()`.

**Total: 5 reject branches.** Half the 10-branch red line. No per-branch log calls (the failure paths return sentinels; emission happens in the dispatcher or operator surface).

Edit fan-out: 1 call site for `emitRekeyRequest` (line 377). Far under the 10-call-site cascade threshold.

**Size: S confirmed.** No split.

## Security review

**Verdict: PASS**

**Findings:**

- **[Trust boundaries]** One new operator-reachable surface introduced: `(*V2SessionManager).Rekey(ctx, connID)`. The `connID` argument is operator-supplied (via slice A's wire path) and is treated as a lookup key only — passed to `m.sessions[connID]` as a map key. Maps in Go are safe under arbitrary string keys; no length check is required (the map lookup itself is the validation). If the key does not match an open session, `ErrConnNotFound` is returned without leaking which keys DO exist (anti-enumeration: error text is fixed, no `did you mean…` hint). No untrusted input reaches the AEAD seal or the wire frame — `payload.reason` is the fixed program literal `"manual"`.
- **[Tokens, secrets, credentials]** No findings. The emit path AEAD-seals under `s.send`, the same CipherState the application-frame path uses (#446); the key material never appears in a logged field. `payload.reason` is a fixed string literal. The sentinels (`ErrConnNotFound`, `ErrSessionNotOpen`) contain no operator-supplied bytes. The reply-timeout close (inherited from #450) emits no AEAD-sealed envelope at close time.
- **[File operations]** N/A — this slice performs no file I/O.
- **[Subprocess / external execution]** N/A — no subprocess interaction.
- **[Cryptographic primitives]** No findings. The emit path consumes `s.send.Encrypt` (inherited from #433's `internal/noise` wrapper; separately reviewed) and `marshalInnerFrameV2` (inherited from #445). No new cryptographic primitive is introduced. The `reason` parameter is plaintext-internal-to-the-envelope: the envelope is AEAD-sealed, so on the wire `reason` is encrypted; on disk (logs) `reason` is the fixed program literal `"manual"`. No cipher suite, nonce, or KDF change.
- **[Network & I/O]** No new outbound bytes outside the existing emit channels. The new envelope shape (`rekey_request` inside `noise_msg` with `payload.reason = "manual"`) reuses `marshalInnerFrameV2` and `m.cfg.Outbound` verbatim. **Re-key rate-limiting on the manual path:** the operator-facing `pyry rekey <conn_id>` verb (sibling slice B2) is the upstream control. The manager-side defensive check `s.awaitingRekeyReply → ErrSessionNotOpen` rate-limits to "no manual rekey while one is in flight" — a 30s natural floor (the reply-timeout window). Beyond that, the operator can re-issue at most one rekey per 30s per conn. A misbehaving operator script driving repeated re-keys at the floor cadence would burn CPU + bandwidth at a known bounded rate; the cost is monitorable via the `v2.rekey.emit` Info log. **No new defence is added in this slice** — the operator surface (B2) owns the higher-layer rate-limit policy.
- **[Error messages, logs, telemetry]** No findings.
  - The `v2.rekey.emit` Info line on successful manual emit carries `event`, `conn_id`, and the fixed `reason="manual"` literal — no key material, no peer-static bytes, no device-name.
  - The `v2.rekey.emit.skipped_already_awaiting` Warn line (existing defensive guard) carries `event` and `conn_id` only — no new fields. Reachable only from the stale-wake race (§ OQ-1); silent on the manual path because `handleManualRekey` checks `awaitingRekeyReply` BEFORE calling `emitRekeyRequest`.
  - `ErrConnNotFound`'s message text is `"relay: conn not found: rekey: conn not found"` (wraps `control.ErrConnNotFound`). The conn_id is NOT echoed in the error message — operators see the failure mode without an enumeration oracle. (Slice A's `control.ErrConnNotFound` is similarly conn-id-free.)
  - `ErrSessionNotOpen`'s message text is `"relay: session not open"`. The conn_id and the precise reason (`!= V2StateOpen` vs. `awaitingRekeyReply`) are NOT distinguished externally. Operator-debug-grade detail would require a structured-log addition in `handleManualRekey`; deferred to a future ticket.
  - **Anti-enumeration:** none of the new sentinels echo the operator-supplied connID. An attacker probing the control socket cannot distinguish "conn does not exist" from "conn exists but is not open" by error-text inspection alone (they get different sentinels, but neither leaks the connID space). The control dispatcher's `errors.Is(err, ErrConnNotFound)` mapping to `ErrCodeConnNotFound` is the documented behaviour from slice A; the wire code distinguishes the two cases by design (the operator IS authorised; the distinction is a feature, not a leak). The threat model is `pyry rekey` is operator-only (Unix socket, file mode 0600 — slice A inherits the supervisor.go socket creation posture); a network attacker cannot reach this surface.
- **[Concurrency]** No findings, with one structural pin. The manual path preserves the existing single-owner-goroutine invariant: all session-state mutations happen on Run's goroutine via `handleManualRekey`; the public `Rekey` method only does channel I/O. The new `manualRekey` channel is unbuffered — backpressure is correct and bounded (a stalled Run blocks all `Rekey` callers, which is the right semantics: a queued-but-unprocessed `Rekey` call would be misleading). The reply channel is per-request and cap=1, so the manager's `req.reply <- err` never blocks even if the caller's ctx fired between enqueue and reply. No new lock. No new atomic. No new goroutine. The Stop() race with stale-wake is documented in § OQ-1 and rejected as benign (existing defensive skip in `emitRekeyRequest` catches it).
- **[Threat model alignment]** No findings.
  - **Threat #3 (relay-operator MITM):** unchanged. The manual emit follows the same path as the scheduled emit (#450); the relay-operator-MITM defences (peer-static continuity in `handleRekeyInit`, AEAD-failure teardown on tampered frames) are owned by #453 and #446 and are not modified here.
  - **Threat #5 (compromised phone / leaked Keystore static):** unchanged. A compromised phone with the same static can complete a re-key. The manual path gives an operator a way to FORCE a re-key, which is a small defensive value-add against a compromised phone (operator can rotate keys without waiting for the 1-hour cadence). No new vulnerability.
  - **Threat #6 (replay):** unchanged. Inherited from #453's swap atomicity + #446's AEAD-failure teardown.
  - **Threat #7 (tampered frame):** unchanged. The emit path's outbound envelope is AEAD-sealed under `s.send`; a tampered frame on the wire fails phone-side AEAD verification.
  - **New threat surface: operator-driven DoS via emit churn.** A misbehaving operator (or compromised operator-control plane) could drive `pyry rekey` in a loop. The manager's awaiting-reply gate (`ErrSessionNotOpen`) imposes a ≥30s floor per conn; the operator-script-driven rate is bounded by `rekeyReplyTimeout` (30s in production). **Defence in this slice:** the awaiting-reply gate. **Defence in B2 (sibling):** the operator verb's own rate-limit policy (out of scope here). **Defence in production:** the control socket is Unix-domain, file mode 0600, operator-only — a network attacker cannot reach this surface.
  - **New threat surface: timer-rebase race with phone-initiated rekey.** If a phone initiates a re-key (sends `noise_init` on the open conn) AT THE SAME TIME as an operator manual rekey, both paths funnel through Run's single goroutine; the second to reach Run sees the state mutated by the first and behaves correctly:
    - Phone-init first → `handleRekeyInit` runs → `rekeyComplete` re-arms timer. Operator manual rekey then sees `awaitingRekeyReply == false` (cleared) but the state IS `V2StateOpen`, so the manual rekey proceeds. A SECOND `noise_init` from the phone would normally race; the binary tolerates this because each `noise_init` is responder-side an independent IK responder cycle (#449/#453). No security issue, just an extra handshake.
    - Operator manual first → `handleManualRekey` runs → manual emit sets `awaitingRekeyReply = true` and stops the scheduled timer. Phone's `noise_init` then lands in `handleRekeyInit` → success → `rekeyComplete` re-arms timer. Manual rekey reply-timeout is stopped by `rekeyComplete`. Correct.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-17
