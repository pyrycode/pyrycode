# Spec — `internal/relay`: v2 re-key initiator on binary side (1-hour timer + `rekey_request` emit + 30s reply timeout)

Ticket: [#450](https://github.com/pyrycode/pyrycode/issues/450). Size: **S**. Security-sensitive: **yes** (touches AEAD-sealed outbound emit + close-code emission on a Noise IK session).

## Files to read first

- `internal/relay/v2session.go` — the entire file (~903 LOC). Same-file work: timer plumbing, emit helper, awaiting-reply state, reply-timeout branch, `rekeyComplete` method, one-line call addition inside `handleRekeyInit`, wake-channel arm in `Run`. **No other production source file is modified.**
  - lines 22–43 — `StatusHandshakeFailure = 4426`, `StatusProtocolMismatch = 4421`, `maxNoisePayloadBytes` const. The 4426 close code this slice emits on reply-timeout is already in scope.
  - lines 68–105 — `V2Session` struct (the three new fields land here: `rekeyTimer`, `rekeyReplyTimer`, `awaitingRekeyReply`).
  - lines 122–215 — `V2SessionConfig` + `V2SessionManager` + `NewV2SessionManager`. The wake channel allocation lands in `NewV2SessionManager`.
  - lines 217–232 — `Run`. The third select arm (`case w := <-m.wake`) lands here; `ctx` becomes `runCtx` via `context.WithCancel(ctx)` + `defer cancelRun()` so timer-callback goroutines unblock on Run exit.
  - lines 509–519 — `handleNoiseInit` success branch (the initial-handshake "advance to V2StateOpen" tail). One-line addition: `s.rekeyTimer = m.armRekeyTimer(runCtx, s)`.
  - lines 549–633 — `handleRekeyInit`. AC #2's one-line call site: `s.rekeyComplete(m, ctx)` after the `m.send(...)` on line 632.
  - lines 818–848 — `sealError`. The emit path's AEAD-seal pattern (`json.Marshal` → `s.send.Encrypt` → `marshalInnerFrameV2(TypeNoiseMsg, ciphertext)`) is the template the new `emitRekeyRequest` follows.
  - lines 852–862 — `marshalInnerFrameV2`. Used by `emitRekeyRequest`.
  - lines 864–885 — `closeWith`. Add timer-stop sequence before the existing `delete(m.sessions, s.connID)`.
- `internal/relay/v2session_test.go` lines 685–765 — `openSession`, `driveToOpen`, `sealAppFrame`, `decryptAppFrame`. Re-used unchanged.
- `internal/relay/v2session_test.go` lines 1524–1566 — `syncLogBuffer`, `bufferLogger`, `waitForLogContains`. Re-used unchanged for AC #5's reply-timeout test.
- `internal/relay/v2session_test.go` lines 1236–1357 — `TestV2Session_RekeyResponder_HappyPath_RoundTripUnderNewKeys` from #453. The test pattern (drive to open → construct a SECOND `noise.Initiator` reusing the SAME `initPriv` → feed fresh `noise_init` → `ReadResp` for `(initSend2, initRecv2)`) is the template the new `TestV2Session_RekeyInitiator_Emit_ReArmViaResponder` test follows for the re-arm leg.
- `internal/relay/connection.go:39-43` — `var handshakeTimeout = 5 * time.Second` package-var lowercase pattern. The two new constants (`rekeyInterval`, `rekeyReplyTimeout`) mirror this shape exactly — same lowercase, same package-var posture, same "tests substitute via `t.Cleanup`" idiom.
- `docs/knowledge/codebase/453.md` — `handleRekeyInit` lifecycle, the swap invariant, the unconditional-call-on-success rule for `rekeyComplete` (this slice's AC #2). The "spontaneous phone-initiated re-key still re-bases the 1-hour cadence" decision is documented there and inherited here.
- `docs/knowledge/codebase/454.md` — `TypeRekeyRequest` constant origin + the v1/v2 partition discipline. This slice's emit path consumes `protocol.TypeRekeyRequest` (added in #454) AND `protocol.TypeNoiseMsg` (already in v1TypeSet). No new `protocol` constant.
- `docs/knowledge/codebase/446.md` — `dispatchAppFrame` + the synchronous-handler ownership pattern. This slice's wake-channel design respects the same "single dispatch goroutine owns `s.send` / `s.recv`" invariant.
- `docs/protocol-mobile.md:203-236` — § Re-key. `rekey_request` envelope shape `{type, payload: {reason}}`, closed reason set `{scheduled, manual, compromise}`, "no `rekey_ack` envelope; next successful AEAD round-trip under new keys is the implicit ack" rule.
- `docs/protocol-mobile.md:442-463` — § Error codes. `noise.rekey_failed` (retryable=yes, log-only — no AEAD envelope is sealed on the timeout-close path) and `4426 Noise handshake failure` (WS close code) the timeout branch emits.
- `docs/specs/architecture/449-v2-rekey-responder.md` on the orphan `feature/449` branch — the abandoned super-slice spec. Contains the verbatim design notes for the wake-channel shape, the rekeyComplete seam, the timer-stop sequence in closeWith, and the package-var test-substitution idiom. Same orphan-spec pattern as #452/#453/#454. **Lift design verbatim where applicable.**

## Context

Mobile Protocol v2 (`docs/protocol-mobile.md` § Re-key) specifies a 1-hour scheduled re-key cadence plus an explicit `rekey_request` envelope the binary sends when it wants the phone to initiate a fresh IK handshake. The binary is always the IK responder (ADR 024); its re-key trigger is a *signal* to the phone, not a handshake. The phone reacts by sending a fresh `noise_init`, which the responder path (`handleRekeyInit`, shipped in [#453](../../knowledge/codebase/453.md)) handles to completion.

The surrounding v2 re-key surface is already merged:

- [**#453**](../../knowledge/codebase/453.md) — responder swap. `handleRekeyInit` runs IK responder on `V2StateOpen` against a phone-initiated `noise_init`, enforces peer-static continuity (Threat #3 residual-risk claim), and atomically swaps `s.send` / `s.recv`. State stays `V2StateOpen`. This slice adds the unconditional `s.rekeyComplete(m, ctx)` call at the success tail (one line after `internal/relay/v2session.go:632`).
- [**#454**](../../knowledge/codebase/454.md) — receive-side discriminator. `dispatchAppFrame` intercepts AEAD-decrypted envelopes whose `type` is `protocol.TypeRekeyRequest` and routes them to `handleRekeyRequest` (logs-only). That is the binary *receiving* a `rekey_request`; this slice ships the outbound *emit*. Orthogonal — different direction; this slice does not modify the receive path.
- [**#452**](../../knowledge/codebase/452.md) — `s.peerStatic` capture at initial handshake (used by #453's continuity check). Inert in this slice.

This slice adds, all in `internal/relay/v2session.go`:

1. A per-`V2Session` 1-hour scheduled re-key timer that arms when the session enters `V2StateOpen` and re-arms after every successful responder swap.
2. An emit helper that AEAD-seals an envelope `{type: "rekey_request", payload: {reason: "scheduled"}}` via the session's current `s.send`, wraps as `noise_msg`, and forwards.
3. Per-session awaiting-reply state (one bool + one `*time.Timer`) that tracks "we have emitted a `rekey_request` and are awaiting the phone's fresh `noise_init`".
4. A 30s reply-timeout branch that closes the conn via `closeWith(StatusHandshakeFailure /*4426*/, nil)` and emits a structured `noise.rekey_failed` log line if no fresh `noise_init` arrives in time.
5. A `rekeyComplete()` method on `*V2Session` that clears awaiting-reply state, stops the 30s timer, and re-arms the 1-hour timer from the call moment.
6. The call site for `s.rekeyComplete(m, ctx)` at the success tail of the existing `handleRekeyInit` (around `internal/relay/v2session.go:632`).

During the awaiting-reply window, transport frames continue flowing under the OLD `CipherState`s with no behavioural change — the swap is owned by `handleRekeyInit`.

## Design

### Package-var constants (test-substitutable)

Two new package-var lowercase constants land in `internal/relay/v2session.go` immediately below `maxNoisePayloadBytes` (line 43). Same shape and rationale as `connection.go:43`'s `handshakeTimeout`:

- `var rekeyInterval = 1 * time.Hour` — scheduled re-key cadence. Doc-comment names `docs/protocol-mobile.md:205` (the 1-hour rule) and explicitly notes the package-var-not-const choice ("tests substitute via `t.Cleanup` for sub-second cadence; not part of the public API").
- `var rekeyReplyTimeout = 30 * time.Second` — reply window from emit. Doc-comment names the close-code consequence (`StatusHandshakeFailure` / 4426) and the structured log event (`noise.rekey_failed`).

Test substitution pattern (mirrors what `handshakeTimeout` already uses; cite this in tests):

```go
func TestX(t *testing.T) {
    prev := rekeyInterval
    rekeyInterval = 20 * time.Millisecond
    t.Cleanup(func() { rekeyInterval = prev })
    // ...
}
```

### New per-session state on `V2Session`

Three new fields. All owned by the manager's single dispatch goroutine (same invariant as `s.send`, `s.recv`, `s.state`, `s.device`, `s.peerStatic`). Documented with a single combined doc-comment block immediately after `s.peerStatic`:

```go
// rekeyTimer fires rekeyInterval after the session entered V2StateOpen
// (initial handshake) or after the last successful responder-side
// CipherState swap (rekeyComplete). On fire, the timer's AfterFunc
// callback delivers a wakeRekeyEmit signal onto m.wake; the manager's
// Run goroutine then calls emitRekeyRequest under the
// single-owner-goroutine invariant for s.send. Stopped (and replaced
// with nil) by closeWith on any close path. Re-armed by rekeyComplete.
// Nil before initial open; nil after closeWith.
rekeyTimer *time.Timer

// rekeyReplyTimer fires rekeyReplyTimeout after a rekey_request was
// emitted. On fire, the AfterFunc callback delivers a
// wakeRekeyReplyTimeout signal onto m.wake; the manager's Run
// goroutine then closes the conn via closeWith(StatusHandshakeFailure
// /*4426*/, nil) and emits the noise.rekey_failed log line.
// rekeyComplete stops this timer (and clears awaitingRekeyReply)
// when the phone's fresh noise_init lands in handleRekeyInit before
// the timeout elapses. Nil unless awaitingRekeyReply is true.
rekeyReplyTimer *time.Timer

// awaitingRekeyReply is true between an emitRekeyRequest emit and
// either rekeyComplete (success) or the wakeRekeyReplyTimeout branch
// (failure). The bool is the canonical "are we awaiting a fresh
// noise_init" predicate; rekeyReplyTimer non-nil-ness is the
// concrete machinery but is not consulted as state — Stop() on a
// fired timer can race with the wake delivery, and the bool is the
// stable signal that rekeyComplete already won.
awaitingRekeyReply bool
```

Why a bool **and** the timer pointer? `time.Timer.Stop()` returns `false` if the timer already fired (callback may be running, the wake may already be in `m.wake`). The bool gives `handleWake`'s `wakeRekeyReplyTimeout` arm a stable late-arrival predicate: "the swap completed before this timeout fired; ignore the wake". Without the bool, a stale wake could fire a close on a session that successfully re-keyed.

### New manager wake channel

`V2SessionManager` grows one field. Documented inline (paragraph addition to the type's existing doc-comment, not a new doc-comment block):

```go
type V2SessionManager struct {
    cfg      V2SessionConfig
    sessions map[string]*V2Session

    // wake is the wake-up signal channel for per-session timers. Both
    // the 1-hour rekey-emit timer and the 30s rekey-reply-timeout
    // timer use time.AfterFunc callbacks (which run on fresh runtime
    // goroutines, NOT on the dispatch goroutine that owns s.send /
    // s.recv); the callbacks send a wakeSignal onto this channel so
    // the manager's Run goroutine can do the actual work
    // (emitRekeyRequest or closeWith) under the single-owner-
    // goroutine invariant. Buffered (wakeBufferSize) so timer
    // callbacks don't block on a busy Run goroutine; callbacks also
    // honour runCtx.Done so they unblock cleanly on Run exit and
    // leak no goroutines.
    wake chan wakeSignal
}
```

`wakeSignal` type and `wakeKind` enum land alongside the existing `V2SessionState` enum, near the top of the file. Two-value enum; tiny:

```go
type wakeKind int

const (
    wakeRekeyEmit wakeKind = iota
    wakeRekeyReplyTimeout
)

type wakeSignal struct {
    s    *V2Session
    kind wakeKind
}

// wakeBufferSize sizes the manager's wake channel. The 1-hour rekey
// cadence makes concurrent fires across sessions vanishingly rare;
// 16 is a generous safety margin that absorbs the realistic worst
// case (every session times out simultaneously while Run is busy in
// a slow handler invocation) without forcing the timer-callback
// goroutine to block. cap=1 would also be correct.
const wakeBufferSize = 16
```

### `Run` grows a third select arm

The existing `Run` loop becomes:

```go
func (m *V2SessionManager) Run(ctx context.Context) error {
    // runCtx is cancelled on Run exit so per-session timer-callback
    // goroutines unblock cleanly and leak no goroutines. armRekeyTimer
    // and armRekeyReplyTimer capture runCtx in their AfterFunc
    // closures; the closures select on (m.wake, runCtx.Done) so a
    // fired-but-undelivered wake completes via the ctx branch when
    // Run is shutting down.
    runCtx, cancelRun := context.WithCancel(ctx)
    defer cancelRun()
    for {
        select {
        case <-runCtx.Done():
            return ctx.Err()
        case env, ok := <-m.cfg.Frames:
            if !ok {
                return nil
            }
            m.handleFrame(runCtx, env)
        case w := <-m.wake:
            m.handleWake(runCtx, w)
        }
    }
}
```

Three behavioural changes:

1. `ctx` becomes `runCtx` everywhere downstream — `handleFrame`, `handleWake`, `armRekeyTimer`, `armRekeyReplyTimer`, and (transitively) `closeWith`, `handleNoiseInit`, `handleRekeyInit`. Existing `closeWith(ctx, ...)` call sites consume the new `runCtx`; the rename is purely structural.
2. The return-on-cancel arm reads `<-runCtx.Done()` (cancelled when either parent `ctx` cancels OR `defer cancelRun()` runs) but returns `ctx.Err()` — preserves the existing observable behaviour ("parent-cancelled returns `context.Canceled`; Frames-closed returns nil").
3. The new `case w := <-m.wake:` arm delegates to `handleWake`.

`handleWake` is the route table:

```go
// handleWake routes a per-session timer wake to its handler under the
// single-owner-goroutine invariant. State-check guards: a wake
// arriving on a session that has transitioned out of V2StateOpen
// (e.g. closeWith ran between the timer fire and the wake delivery)
// is dropped silently. The state-check is read-only on s.state from
// the dispatch goroutine — no race because the same goroutine that
// would have mutated s.state is the goroutine doing this read.
func (m *V2SessionManager) handleWake(ctx context.Context, w wakeSignal) {
    if w.s.state != V2StateOpen {
        return
    }
    switch w.kind {
    case wakeRekeyEmit:
        m.emitRekeyRequest(ctx, w.s)
    case wakeRekeyReplyTimeout:
        if !w.s.awaitingRekeyReply {
            // rekeyComplete already cleared the awaiting state — the
            // phone's fresh noise_init landed before the timeout fired
            // and the swap succeeded. Ignore stale wake.
            return
        }
        m.cfg.Logger.Warn("relay: v2 rekey reply timeout",
            "event", "noise.rekey_failed",
            "conn_id", w.s.connID,
            "close_code", int(StatusHandshakeFailure))
        m.closeWith(ctx, w.s, StatusHandshakeFailure, nil)
    }
}
```

### Arming helpers

`armRekeyTimer` and `armRekeyReplyTimer` are tiny — one `time.AfterFunc` each, with the callback shaped to honour `ctx.Done`. Both return the constructed `*time.Timer` to the caller.

```go
// armRekeyTimer arms the 1-hour scheduled re-key timer. The callback
// runs on a fresh runtime goroutine (time.AfterFunc semantics); it
// pushes a wakeRekeyEmit signal onto m.wake under blocking-send +
// ctx.Done semantics. ctx is the manager's runCtx; cancelled on Run
// exit, which unblocks any pending callback goroutine.
func (m *V2SessionManager) armRekeyTimer(ctx context.Context, s *V2Session) *time.Timer {
    return time.AfterFunc(rekeyInterval, func() {
        select {
        case m.wake <- wakeSignal{s: s, kind: wakeRekeyEmit}:
        case <-ctx.Done():
        }
    })
}

// armRekeyReplyTimer is the same shape with wakeRekeyReplyTimeout
// and rekeyReplyTimeout.
func (m *V2SessionManager) armRekeyReplyTimer(ctx context.Context, s *V2Session) *time.Timer {
    return time.AfterFunc(rekeyReplyTimeout, func() {
        select {
        case m.wake <- wakeSignal{s: s, kind: wakeRekeyReplyTimeout}:
        case <-ctx.Done():
        }
    })
}
```

Why blocking send (with `ctx.Done` escape) rather than non-blocking send? Dropping a wake is bad: a dropped `wakeRekeyEmit` means the 1-hour cadence misses one beat and never re-arms (the next rearm comes only via a `rekeyComplete`); a dropped `wakeRekeyReplyTimeout` means the session parks in `awaitingRekeyReply=true` forever. With `wakeBufferSize = 16` and rare fires, send-blocking is vanishingly rare; if it does happen, the callback goroutine waits microseconds for Run to drain one wake and exits cleanly. Run exit cancels `runCtx`, which closes every blocked callback's escape arm — no goroutine leak.

### Initial arm site

At the success tail of `handleNoiseInit` (immediately after `s.state = V2StateOpen` at `internal/relay/v2session.go:518`), add one line:

```go
s.state = V2StateOpen
s.rekeyTimer = m.armRekeyTimer(ctx, s)  // NEW
```

This is the only place V2StateOpen is set initially. `handleRekeyInit` keeps the session in V2StateOpen across re-keys; it calls `rekeyComplete` to re-arm.

### Emit path

```go
// emitRekeyRequest builds an AEAD-sealed rekey_request envelope under
// s.send, wraps it as a noise_msg inner frame, and forwards via
// m.send. Sets s.awaitingRekeyReply=true and arms s.rekeyReplyTimer
// for the 30s reply window. Called from handleWake's wakeRekeyEmit
// arm, on the manager's single dispatch goroutine.
//
// payload.reason is always "scheduled" in this slice. The
// "manual"/"compromise" reasons (docs/protocol-mobile.md § Re-key)
// are emitted by the future pyry rekey <conn_id> operator verb
// (#451), not by the timer-driven path.
//
// Envelope ID is fixed at 1: there is no rekey_ack response that
// would correlate by InReplyTo (the spec is explicit — the next
// successful AEAD round-trip under the new keys is the implicit
// ack). ID=1 is consistent with handleNoiseInit's hello_ack ID
// choice; the two are never in flight on the same conn (hello_ack
// is at handshake time inside noise_resp; rekey_request is
// post-open inside noise_msg).
//
// AEAD-seal failure on the emit side is realistically unreachable
// under correct flynn/noise (same posture as the existing sealError
// at line 818). The conn is NOT closed on emit failure — the
// session remains in V2StateOpen and the next 1-hour cadence will
// attempt another emit. Failure is logged at Warn.
func (m *V2SessionManager) emitRekeyRequest(ctx context.Context, s *V2Session) {
    // Defensive: a wakeRekeyEmit arriving while already awaiting
    // reply would re-emit. Skip — the in-flight emit's reply
    // window is still ticking. (Should not happen under normal
    // operation; rekeyTimer is one-shot and only re-armed by
    // rekeyComplete, which clears the bool first.)
    if s.awaitingRekeyReply {
        return
    }
    // ... build envelope { type: rekey_request, payload: { reason: "scheduled" }, id: 1, ts: now } ...
    // ... ciphertext = s.send.Encrypt(envBytes) ; on err → Warn log + return ...
    // ... frame = marshalInnerFrameV2(TypeNoiseMsg, ciphertext) ; on err → Warn log + return ...
    // ... m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame}) ...
    // ... s.awaitingRekeyReply = true ...
    // ... s.rekeyReplyTimer = m.armRekeyReplyTimer(ctx, s) ...
    // ... m.cfg.Logger.Info("relay: v2 rekey emit", event="v2.rekey.emit", conn_id, reason="scheduled") ...
}
```

**Log-line shape** (for the developer to mirror): one INFO line on successful emit (`event=v2.rekey.emit`, `conn_id`, `reason=scheduled`); one WARN line on AEAD-seal-failure with no error text (`event=v2.rekey.emit.seal_failed`, `conn_id`); one WARN line on marshal-failure (`event=v2.rekey.emit.marshal_failed`, `conn_id`). The seal/marshal failure paths do NOT close the conn.

**Logged-field discipline**: no AEAD ciphertext, no plaintext bytes, no CipherState internal state, no flynn-noise error text. `reason` is the only non-canonical field and it's a fixed string literal `"scheduled"`.

### `rekeyComplete` method on `*V2Session`

```go
// rekeyComplete is the seam that bridges responder-side swap
// completion back to initiator-side cadence. Called from the success
// tail of (*V2SessionManager).handleRekeyInit after the atomic
// s.send/s.recv swap and the v2.rekey.accept log emission.
//
// Behaviour:
//  - Clears awaitingRekeyReply (no-op if not set — a spontaneous
//    phone-initiated re-key that the binary did not request still
//    re-bases the 1-hour cadence; any successful swap is a fresh-
//    key moment).
//  - Stops and nils rekeyReplyTimer (no-op if nil).
//  - Stops and replaces rekeyTimer with a fresh one armed
//    rekeyInterval from now.
//
// Runs on the manager's single dispatch goroutine (the same
// goroutine that owns s.send/s.recv after the swap). Inherits the
// no-mutex / no-atomic invariant from the rest of the package.
//
// The argument shape (m, ctx) carries the dependencies needed to
// re-arm the 1-hour timer — m for the wake channel and Outbound,
// ctx for the AfterFunc callback's escape arm. The receiver is
// *V2Session per ticket spec; the side-effect target is session
// state, the manager and context are pragmatic Go arguments.
func (s *V2Session) rekeyComplete(m *V2SessionManager, ctx context.Context) {
    s.awaitingRekeyReply = false
    if s.rekeyReplyTimer != nil {
        s.rekeyReplyTimer.Stop()
        s.rekeyReplyTimer = nil
    }
    if s.rekeyTimer != nil {
        s.rekeyTimer.Stop()
    }
    s.rekeyTimer = m.armRekeyTimer(ctx, s)
}
```

### Call site addition in `handleRekeyInit`

Exactly one line, immediately after the existing `m.send(...)` emit of `noise_resp` at `internal/relay/v2session.go:632`:

```go
m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: respFrame})
s.rekeyComplete(m, ctx)  // NEW — AC #2 unconditional call site
```

**Unconditional on the swap success path.** A phone-initiated re-key that the binary did not request still re-bases the 1-hour cadence (the spec's "any successful swap is a fresh-key moment" rule). Clearing an unset `awaitingRekeyReply` is a no-op; stopping a nil `rekeyReplyTimer` is a no-op.

### `closeWith` timer-stop additions

Before the existing `delete(m.sessions, s.connID)` at `internal/relay/v2session.go:875`:

```go
func (m *V2SessionManager) closeWith(ctx context.Context, s *V2Session, code websocket.StatusCode, frame json.RawMessage) {
    if s.state == V2StateClosed {
        return
    }
    s.state = V2StateClosed
    // NEW — stop per-session timers to free runtime timer-heap entries
    // and ensure no pending callback goroutine blocks on a delivery
    // race (the callback's ctx.Done arm is the load-bearing teardown;
    // these Stop()s are the defensive belt).
    if s.rekeyTimer != nil {
        s.rekeyTimer.Stop()
        s.rekeyTimer = nil
    }
    if s.rekeyReplyTimer != nil {
        s.rekeyReplyTimer.Stop()
        s.rekeyReplyTimer = nil
    }
    delete(m.sessions, s.connID)
    // ... existing emit logic unchanged ...
}
```

`Stop()` is safe to call on a fired timer (returns false, no-op). The cleanup is idempotent.

### Goroutine lifecycle

Three goroutine-lifetime classes touched by this slice:

1. **Manager Run goroutine** — unchanged in count (one). The `runCtx` derived-cancel is the new structural primitive.
2. **`time.AfterFunc` callback goroutines** — at most one outstanding callback per (session, timer) pair, spawned only when the timer fires. Each callback runs to completion in microseconds (one channel send + return) under normal load; under shutdown, the callback blocks on `m.wake` send until `ctx.Done` unblocks it. **No goroutine outlives Run** — verified by AC #5's `runtime.NumGoroutine` baseline test.
3. **Runtime timer heap** — pre-fire, the timer is a runtime-managed entry, not a goroutine. `time.Timer.Stop()` removes the entry cleanly.

### Field-of-view summary table

| New construct | Location | LOC est |
| --- | --- | --- |
| `rekeyInterval`, `rekeyReplyTimeout` package-vars | top of file, below `maxNoisePayloadBytes` | ~10 |
| `wakeKind` enum + `wakeSignal` type + `wakeBufferSize` const | near `V2SessionState` block | ~20 |
| `V2Session` fields (`rekeyTimer`, `rekeyReplyTimer`, `awaitingRekeyReply`) + doc-comments | after `peerStatic` field | ~25 |
| `V2SessionManager.wake` field + doc-comment | inside the struct definition | ~12 |
| `NewV2SessionManager`: allocate `wake` | inside the existing constructor | ~1 |
| `Run`: derive `runCtx`, add wake arm | rewrite of the loop body | ~10 |
| `handleWake` | new method after `Run` | ~25 |
| `armRekeyTimer` + `armRekeyReplyTimer` | new methods | ~25 |
| `emitRekeyRequest` | new method | ~35 |
| `rekeyComplete` (method on `*V2Session`) | new method, between `V2Session.State()` and `V2SessionConfig` | ~20 |
| `handleNoiseInit`: one-line initial arm | line 518 area | ~1 |
| `handleRekeyInit`: one-line rekeyComplete call | line 632 area | ~1 |
| `closeWith`: timer-stop block | inside existing method | ~8 |
| **Production total** | | **~190 LOC** (with doc-comments) |

## Testing strategy

Tests land in `internal/relay/v2session_test.go` under a `// --- re-key initiator tests (#450) ---` header. Three tests cover AC #5; the merge of AC #5 bullets 1 + 2 (happy path + responder re-arm) into a single test is deliberate (see "Why three tests, not four" below).

All three tests substitute `rekeyInterval` and (where relevant) `rekeyReplyTimeout` to sub-second values via the `t.Cleanup` save-and-restore idiom. Pattern lifted verbatim from `connection.go`'s `handshakeTimeout` substitution (search the test file for `prev := handshakeTimeout`).

### `TestV2Session_RekeyInitiator_Emit_ReArmViaResponder`

Joint coverage of AC #5 bullet 1 (timer fires → emit observed with `payload.reason == "scheduled"`) and AC #5 bullet 2 (after a full re-key cycle completes via `handleRekeyInit`, the timer re-arms and emits a second `rekey_request`).

Inputs / setup:
- `rekeyInterval = 20 * time.Millisecond` (sub-second cadence so the test runs in well under 1s).
- `rekeyReplyTimeout = 500 * time.Millisecond` (long enough that the test's manual re-key cycle completes before the reply window elapses).
- Drive paired-device handshake to open via the existing `driveToOpen` helper.

Behaviour to assert:
- After `rekeyInterval` elapses, exactly ONE additional outbound envelope is observed (the rekey_request). Decode it via the existing `decryptAppFrame(emitEnv, sess.initRecv)` — the phone-side `initRecv` IS the binary's `s.send`, so this decrypts cleanly. Decoded inner envelope: `Type == protocol.TypeRekeyRequest`; decoded payload: `Reason == "scheduled"`.
- Test constructs a SECOND `noise.Initiator` reusing the SAME `initPriv` (peer-static continuity invariant from [#453](../../knowledge/codebase/453.md)), calls `WriteInit(nil)` (empty early-data per re-key spec), feeds the new `noise_init` via `frames <-`.
- Manager processes the rekey-noise_init, runs `handleRekeyInit`, emits noise_resp. Test observes the second outbound envelope (the noise_resp). Test's second initiator calls `ReadResp(respRaw)` to derive `(initSend2, initRecv2)`.
- **After the swap completes, the test waits another `rekeyInterval`.** A THIRD outbound envelope appears — the second `rekey_request` emit, asserting the 1-hour cadence re-armed via `rekeyComplete`. Decoded under `initRecv2` (the post-swap key), payload again `Reason == "scheduled"`.
- AEAD round-trip under the new keys (bonus): seal an arbitrary application envelope under `initSend2`, observe the reply round-trips. Asserts the post-swap channel works end-to-end.

State assertions after `sess.stop()`:
- `sess.mgr.sessions[v2TestConnID].state == V2StateOpen` (still open, never closed).
- `sess.mgr.sessions[v2TestConnID].awaitingRekeyReply == false` (cleared by rekeyComplete after the swap).

### `TestV2Session_RekeyInitiator_ReplyTimeout_4426`

Pins AC #5 bullet 3 (30s reply timeout fires → close with 4426 + `noise.rekey_failed` log emitted).

Inputs / setup:
- `rekeyInterval = 20 * time.Millisecond`.
- `rekeyReplyTimeout = 40 * time.Millisecond` (short, so the test runs in well under 1s).
- `logger, logBuf := bufferLogger()` (existing helper) — captures the structured `noise.rekey_failed` line.
- Drive to open.

Behaviour to assert:
- After `rekeyInterval`: outbound rekey_request emit observed (envelope shape spot-check, doesn't need to decode payload).
- **Test does NOT feed a `noise_init` reply.**
- After `rekeyReplyTimeout`: outbound CLOSE envelope observed — `CloseCode == uint16(StatusHandshakeFailure)` (4426), `Frame == nil`.
- `sess.mgr.sessions[v2TestConnID]` is absent (closeWith ran the existing delete).
- `waitForLogContains(t, logBuf, "event=noise.rekey_failed")` succeeds. Substring also checks `close_code=4426` and `conn_id=v2TestConnID`. No `err=` field anywhere in the captured log line (no flynn-noise error text leaked).

### `TestV2Session_RekeyInitiator_TimerCleanup_NoGoroutineLeak`

Pins AC #5 bullet 4 (timer goroutines do not outlive the manager). Two variants — collapse into one test with two phases.

Inputs / setup:
- `rekeyInterval = 50 * time.Millisecond`, `rekeyReplyTimeout = 100 * time.Millisecond`.
- `before := runtime.NumGoroutine()` at the very top of the test, BEFORE `driveToOpen` (so the count excludes test-fixture goroutines).
- Phase A — close-via-manager-exit: drive to open, sleep briefly so the 1h timer arms, call `sess.stop()` (cancels manager ctx). After stop, `runtime.NumGoroutine()` returns to within ±1 of `before` (small tolerance for runtime jitter).
- Phase B — close-via-AEAD-failure-during-awaiting-reply: drive to open, wait for emit, do NOT feed reply, sleep slightly more than `rekeyReplyTimeout` (so both timers have armed and the reply timer has fired the close), call `sess.stop()`. After stop, goroutine count returns to within ±1 of `before`.

Both phases use a small post-stop `runtime.Gosched(); runtime.GC(); time.Sleep(5 * time.Millisecond)` cycle to give `time.AfterFunc` callback goroutines a tick to unblock and exit. This is the established pattern for goroutine-leak tests on the stdlib runtime — there's no `goleak`-style framework dependency in pyrycode; the runtime baseline is the canonical signal.

Helper (one new): `waitForOutboundCount(t *testing.T, rec *v2Recorder, n int, deadline time.Duration)` — same shape as the existing `waitForEnvelopes` but with a caller-supplied deadline. Used by the reply-timeout test to wait up to ~150ms for the close envelope. ~15 LOC.

### Why three tests, not four

AC #5 names four test scenarios. The first ("timer-driven happy path with substituted-low `rekeyInterval` … after the test calls `s.rekeyComplete()` directly, the timer re-arms") is merged into the second ("end-to-end re-arm via the responder path") because:

- A test goroutine calling `s.rekeyComplete(m, ctx)` directly violates the single-owner-goroutine invariant — `s.rekeyTimer` is a field owned by the dispatch goroutine, and a test-goroutine method call on the session would race with any concurrent state read inside `handleFrame` or `handleWake`. The race is not flaky-in-practice (single-conn fixture) but it's a documentation violation, and a future refactor that adds a session mutex would have to retain the test-goroutine bypass for backward compatibility.
- The responder-path re-arm test (AC #5 bullet 2) tests the same behaviour (rekeyComplete clears state and re-arms the 1h timer) through the natural code path — the binary's only production caller of rekeyComplete is `handleRekeyInit`. Testing via the natural caller is structurally stronger than testing via a direct call from the wrong goroutine.

The merger collapses two AC bullets into one test. The four AC bullets are otherwise covered: the joint test covers bullets 1+2, the timeout test covers bullet 3, the cleanup test covers bullet 4.

## Open questions

1. **Should `emitRekeyRequest` skip-on-already-awaiting log at Info or Warn?** The defensive branch (`if s.awaitingRekeyReply { return }`) at the top of `emitRekeyRequest` should not fire under correct operation — `rekeyTimer` is one-shot and only re-armed by `rekeyComplete`, which clears the bool first. **Decision: log at Warn with `event=v2.rekey.emit.skipped_already_awaiting`.** This is a "shouldn't happen" defensive log; surfacing it as Warn means operators see it if a future refactor introduces a re-emit bug. Cost: one trivial log call. Defer the no-log alternative.

2. **Should `rekeyComplete` clear `s.peerStatic`?** No. `peerStatic` is the identity pin for the session's entire lifetime ([#452](../../knowledge/codebase/452.md) doc-comment is explicit). A successful re-key is the same peer by construction (the responder's continuity check passed). `peerStatic` MUST NOT be overwritten on re-key. The method touches three fields only: `awaitingRekeyReply`, `rekeyReplyTimer`, `rekeyTimer`.

3. **Should the manager expose a `Stop()` or `Shutdown()` for graceful shutdown?** No. The existing posture (Run returns on Frames-close or ctx-cancel; sessions are dropped without cleanup envelopes) is unchanged. The `runCtx` derived-cancel is internal plumbing for timer-callback unblocking; it does not surface a new public API. Production callers continue to cancel the parent ctx to shut the manager down.

4. **Wake-channel buffer sizing: 1, 16, or per-session?** **Decision: 16, shared across all sessions.** Per-session buffers would require allocating one channel per session and managing per-session lifetime (close on session removal); shared is simpler. cap=1 is correct but tight; the worst case (16 sessions timing out simultaneously while Run is busy) just barely exceeds cap=1. cap=16 is generous, cheap (16 pointer-sized slots), and named in a `wakeBufferSize` constant so a future tune-down is one-line.

5. **Does the wire envelope's `ts` field need to be set?** Yes — the existing `sealError` (line 832) and `handleNoiseInit`'s hello_ack (line 437) both set `TS: time.Now().UTC()`. The receive-side handler [#454](../../knowledge/codebase/454.md) doesn't read `env.TS` today but the field is part of the canonical envelope shape per `docs/protocol-mobile.md` § Wire shapes. Set it.

6. **Should the emit-side seal/marshal failure paths close the conn?** **Decision: no.** Same posture as the existing `sealError` failure path (line 498-502): log Warn, drop the frame, leave the conn open. The next 1-hour cadence will attempt another emit. The emit-side AEAD seal is realistically unreachable under correct flynn/noise (the local key state is sound; only data-side issues could trigger it, and an empty `payload.reason="scheduled"` envelope is data-side trivial). Closing the conn over an internal AEAD-seal failure would tear down a working session for a non-protocol error.

## Scope self-check

Production source files modified or created (excluding `*_test.go`, `*.md`, the spec itself):

1. `internal/relay/v2session.go` — modified.

Count: **1 production source file.** Well under the 5-file size:s ceiling.

New exported symbols: **0.** All new types/methods/fields/constants are package-private. No new types cross the `internal/relay` boundary.

Production LOC estimate (from the field-of-view summary table): **~190 LOC** total, of which ~120 is body code and ~70 is doc-comments. PO estimate was 80-100 production LOC; this is above due to the doc-comment density on three new concurrency-sensitive constructs (`wakeSignal`, the V2Session timer fields, `rekeyComplete`) — comments are load-bearing for the next reader, not optional.

Test LOC estimate: three tests at ~80-130 LOC each plus one ~15 LOC helper. Total: **~280 LOC.** Within the PO's 200-280 estimate.

Total written code (production + tests): **~470 LOC.** Under the 600-LOC red line.

Edit fan-out (per `codegraph_impact`): **zero new consumer call sites** outside `internal/relay/v2session.go`. The new wake channel, the `V2Session` field additions, and the `rekeyComplete` method are all consumed exclusively inside the same file. No cross-package edits. No type-signature changes on existing exported symbols (`V2SessionConfig`, `V2SessionManager`, `Run`, `NewV2SessionManager`).

Acceptance criteria: **5.** At the 5-AC red line. Joint test design (bullets 1 + 2 → one test) keeps the test count tight without losing coverage.

Reject branches in new code:
1. `wakeRekeyReplyTimeout` → `closeWith(4426)` (the load-bearing failure mode, AC #5 bullet 3).
2. `emitRekeyRequest` AEAD-seal failure → Warn-and-drop (no close).
3. `emitRekeyRequest` marshal-inner-frame failure → Warn-and-drop (no close).
4. `emitRekeyRequest` already-awaiting → Warn-and-return (defensive; should not fire).
5. `handleWake` session-not-open (stale wake) → silent drop.
6. `handleWake` wakeRekeyReplyTimeout but awaiting cleared (stale wake) → silent drop.

Total: **6 reject branches.** Under the 10-branch red line. Three of the six are silent drops (no log call); the three that log do so without close, so each is a 4-line block.

Size: **S confirmed.** No split.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new untrusted-input surface. This slice's emit path consumes **only** internal state (`s.send`, `s.connID`, a fixed `"scheduled"` literal) and timer fires from the runtime. No bytes from the network reach the emit path. The reply-timeout branch consumes only the timer fire and the in-process `s.awaitingRekeyReply` predicate; no untrusted input. The new wake channel carries `*V2Session` pointers and a `wakeKind` enum — both internal types, never serialised to the wire.
- **[Tokens, secrets, credentials]** No findings. The emit path AEAD-seals under `s.send`, the same CipherState the application-frame path uses (#446); the key material never appears in a logged field. `payload.reason` is a fixed string literal — no operator- or attacker-controlled bytes flow through the emitted envelope. `s.device` is preserved across re-key (inherited from #453); revocation propagation remains a separate open question (same posture as v1 and #446). The reply-timeout close path emits no AEAD-sealed envelope — the close is bare WS-level 4426 — so no key material is consumed at close time either.
- **[File operations]** N/A — this slice performs no file I/O.
- **[Subprocess / external execution]** N/A — no subprocess interaction.
- **[Cryptographic primitives]** No findings. The emit path consumes `s.send.Encrypt` (inherited from #433's `internal/noise` wrapper; separately reviewed) and `marshalInnerFrameV2` (inherited from #445). No new cryptographic primitive is introduced. The 1-hour cadence is the spec-mandated re-key trigger; shortening it (via the test-substitution package-var) does not weaken the cipher suite — the AEAD primitive is unchanged. The reply-timeout 30s is a wire-level liveness check, not a cryptographic deadline; the close at 4426 forces a fresh handshake which re-derives keys from scratch via Noise_IK.
- **[Network & I/O]** No new outbound bytes outside the existing emit channels. The new envelope shape (`rekey_request` inside `noise_msg`) reuses the existing `marshalInnerFrameV2` path and the existing `m.cfg.Outbound` forwarder; no new transport seam. The close-on-reply-timeout reuses the existing `closeWith` primitive — same outbound shape as every other binary-initiated close in the manager. **Re-key rate-limiting is NOT in this slice** — the 1-hour cadence + the awaiting-reply-skip-defensive-branch naturally cap binary-initiated re-keys at 1/hour per session; an operator forcing a manual re-key via the future `pyry rekey` verb (#451) is the only way to drive a tighter cadence in production, and that surface owns its own rate-limit policy.
- **[Error messages, logs, telemetry]** No findings. The `noise.rekey_failed` log line on reply-timeout carries `event`, `conn_id`, and `close_code` only — no `err=` field, no flynn-noise error text, no envelope bytes, no AEAD ciphertext. The `v2.rekey.emit` Info line on successful emit carries `event`, `conn_id`, and the fixed `reason="scheduled"` literal — no key material, no peer-static bytes, no device-name. The emit-failure Warn lines (`v2.rekey.emit.seal_failed`, `v2.rekey.emit.marshal_failed`) carry `event` and `conn_id` only — no flynn-noise error text, even though the underlying error would carry it. **Anti-enumeration:** the reply-timeout close does NOT log `device_name` — same anti-enumeration discipline as [#453's peer-static-mismatch branch](../../knowledge/codebase/453.md). The phone observed the rekey_request and chose not to respond; the binary's log line should not leak which device-identifier was attached to that session via an operator-side timing channel. (Future audit-log tickets may relax this if the operator-side trust model warrants it.)
- **[Concurrency]** No findings, with one structural pin. The wake channel introduces a NEW concurrency primitive (the per-session timer-callback goroutines spawned by `time.AfterFunc`), but the design preserves the existing single-owner-goroutine invariant for `s.send`/`s.recv`/`s.state`/`s.device`/`s.peerStatic`: the timer-callback goroutines do NOT touch session state directly — they push a `wakeSignal` onto `m.wake` and return; the manager's Run goroutine pops the signal and does the actual emit or close-with under the dispatch-goroutine ownership contract. The blocking-send + `runCtx.Done` shape of the timer callback guarantees no goroutine outlives Run (verified by the goroutine-leak test). `Stop()` race with timer fire is benign: a stale wake on a closed session is rejected by `handleWake`'s `state != V2StateOpen` guard; a stale `wakeRekeyReplyTimeout` after a successful swap is rejected by the `!awaitingRekeyReply` guard.
- **[Threat model alignment]** No findings.
  - **Threat #3 (relay-operator MITM):** unchanged. The continuity gate is owned by [#453](../../knowledge/codebase/453.md)'s `handleRekeyInit`; this slice does not weaken it. The binary-initiated emit forwards a `rekey_request` envelope to the phone; the phone's response (a fresh `noise_init`) lands in `handleRekeyInit` where the continuity check still gates the swap. A MITM that injects a fresh `noise_init` from a different static after the binary emits is rejected at 4426; the binary's emit does not authenticate the phone, only signals it. The reply-timeout 4426 close is the structural defence against a MITM that drops the binary's emit (the phone never sees it, never responds, the binary closes — re-handshake required).
  - **Threat #5 (compromised phone / leaked Keystore static):** unchanged. A compromised phone with the same static can complete a re-key (peer-static check passes — same key). This slice's emit gives the compromised phone an additional rekey-request signal; but the compromised phone could already initiate re-keys independently (it has the static), so the emit adds no new compromise vector.
  - **Threat #6 (replay):** unchanged. Inherited from #453's swap atomicity + #446's AEAD-failure teardown.
  - **Threat #7 (tampered frame):** unchanged. The emit path's outbound envelope is AEAD-sealed under `s.send`; a tampered frame on the wire fails phone-side AEAD verification — the binary takes no action. The reply-timeout 30s window covers the case where the phone never receives the rekey_request (the binary tears down the conn).
  - **New threat surface introduced: DoS via emit churn.** A misbehaving timer (e.g. a bug introducing a sub-second `rekeyInterval`) would burn CPU and outbound bandwidth. **Defence:** the package-var const lives at the top of the file, not in a config struct; production callers cannot override it; tests substitute via `t.Cleanup` which restores the production value at test end. The 1-hour value is a static program literal in production builds.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-17
