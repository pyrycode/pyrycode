# Operator-driven manual re-key (#462) — `Rekey` method satisfying `control.Rekeyer`

`*V2SessionManager` satisfies [`control.Rekeyer`](control-plane.md#rekey-v2-conn-re-key-trigger-seam-13d-1-459) (pinned by `var _ control.Rekeyer = (*V2SessionManager)(nil)`). The public method `Rekey(ctx context.Context, connID string) error` funnels each request onto Run's single dispatch goroutine via a new unbuffered `manualRekey chan manualRekeyReq` field; a fourth `Run` select arm dequeues the request and dispatches to a private `handleManualRekey` method that runs on the owner goroutine. The shape preserves the single-owner-goroutine invariant for `s.send` / `s.state` / `s.rekeyTimer` / `s.awaitingRekeyReply` — `Rekey` itself does only channel I/O on the caller's goroutine; every session-state read or write happens in `handleManualRekey` on `Run`'s goroutine. **No new lock, no new atomic, no new long-lived goroutine.**

```go
type manualRekeyReq struct {
    connID string
    reply  chan error // cap=1 per request; manager's send is non-blocking
}

// In V2SessionManager:
//   manualRekey chan manualRekeyReq  // unbuffered: backpressure is correct

// In Run's select:
case req := <-m.manualRekey:
    req.reply <- m.handleManualRekey(runCtx, req.connID)
```

The channel is unbuffered: backpressure is the right semantics — if `Run` is busy processing a frame, `Rekey` waits. A buffer would mislead the caller into thinking the request was accepted when in reality `Run` hadn't yet observed it. The caller's `ctx` is the escape arm in both `Rekey` select blocks (enqueue and reply). The per-request reply channel is `cap=1` so the manager's `req.reply <- err` send is non-blocking even if the caller's ctx fires between enqueue and reply. The `manualRekey` channel is NOT closed on `Run` exit (matches the existing posture for `m.cfg.Frames`); in-flight callers unblock via `ctx.Done`.

**Method-name divergence.** Ticket #462's AC text said `TriggerRekey(connID string) error`; the slice A `control.Rekeyer` interface declared `Rekey(ctx context.Context, connID string) error`. The interface signature wins because it lets `*V2SessionManager` satisfy `control.Rekeyer` directly with no adapter. The compile-time `var _ control.Rekeyer = (*V2SessionManager)(nil)` assertion pins the contract so a future refactor cannot drift the name back to the AC's informal label.

**`handleManualRekey` is the lookup + emit dispatch site.** Three reject branches, then the emit:

| Precondition | Returned sentinel |
| --- | --- |
| `m.sessions[connID]` miss | `ErrConnNotFound` (wraps `control.ErrConnNotFound`) |
| `s.state != V2StateOpen` | `ErrSessionNotOpen` |
| `s.awaitingRekeyReply` (a prior emit is in flight) | `ErrSessionNotOpen` |
| All checks pass | stop+nil `s.rekeyTimer`; call `emitRekeyRequest(ctx, s, "manual")`; return nil |

The `!= V2StateOpen` and `awaitingRekeyReply` branches return the SAME sentinel because from the operator's perspective both mean "the conn is not in a state where a manual rekey can be initiated." Distinguishing them externally would leak internal state-machine vocabulary into the operator surface with no actionable consequence. The collapse also imposes a natural per-conn rate limit: at most one manual rekey per `rekeyReplyTimeout` (30 s in production) — a second `Rekey` arriving within that window hits the awaiting-reply branch.

**`ErrConnNotFound` wraps `control.ErrConnNotFound` via `%w`.** Slice A's dispatcher uses `errors.Is(err, control.ErrConnNotFound)` (not `==`) to map to `ErrCodeConnNotFound = "conn_not_found"` on the wire. `%w` keeps that mapping firing without any further plumbing on either side. `ErrSessionNotOpen` has no wire-code analogue today — slice A defined no `ErrCodeSessionNotOpen`; the control dispatcher surfaces it through `Response.Error` verbatim with no `ErrorCode`. Sentinels live in `internal/relay` so the wire-mapping layer can import them without leaking relay-internal state-machine vocabulary into `internal/control`. The `relay → control` import is non-cyclic.

**Timer rebase.** `handleManualRekey` calls `s.rekeyTimer.Stop()` and sets `s.rekeyTimer = nil` BEFORE the emit. `Stop()`'s bool return is intentionally ignored — see "Stale-wake benign race" below. The natural [#453](../codebase/453.md) responder cycle on the phone's reply re-arms a fresh `rekeyTimer` via `rekeyComplete` from the swap moment, not the previous emit moment, so the next scheduled emit lands at T_swap + `rekeyInterval`, never at the original boundary. On reply-timeout the conn closes at WS 4426 and the session is removed entirely.

**`emitRekeyRequest` refactor.** The function signature gained a `reason string` parameter; the body deltas are mechanically minimal (the struct literal's `Reason:` field, the `Info` log's `reason` value). Call sites: `handleWake`'s `wakeRekeyEmit` arm passes `"scheduled"`; `handleManualRekey` passes `"manual"`. **One emit function, two callers — no parallel emit machinery.** The AEAD-seal posture, the awaiting-defensive skip, the marshal-failure WARN, the `awaitingRekeyReply = true` set, and the `armRekeyReplyTimer` call all run on both paths byte-identically. Mobile Protocol v2's `payload.reason = "manual"` is wire-pinned by `docs/protocol-mobile.md` § Re-key as *"operator-triggered via `pyry rekey <conn_id>`"*; the literal is the only semantic difference between the scheduled and manual emits.

**Stale-wake benign race.** Sequence: scheduled `rekeyTimer` fires at T=0, the `AfterFunc` callback pushes `wakeSignal{s, wakeRekeyEmit}` onto `m.wake` (cap 16). `Run` picks up the `manualRekey` arm first (Go's `select` is fair-random). `handleManualRekey` runs `Stop()` (returns false — fired), nils the timer, runs the manual emit (which sets `awaitingRekeyReply = true`). On a subsequent `Run` iteration the stale wake is processed: `handleWake` → `wakeRekeyEmit` arm → `emitRekeyRequest(ctx, s, "scheduled")` → the defensive `awaitingRekeyReply` check catches it and logs `v2.rekey.emit.skipped_already_awaiting`. **No double emit; one spurious WARN.** The defensive skip stays in `emitRekeyRequest` precisely so this race remains benign on the scheduled path; the manual path's explicit pre-check makes the defensive skip structurally unreachable from `handleManualRekey` (the explicit check fires first).

**AEAD-seal-failure posture: pause-not-close.** A sub-emit failure on the manual path leaves the conn with `rekeyTimer = nil` and `awaitingRekeyReply = false` — automatic scheduled re-keying is paused indefinitely until a phone-initiated re-key rebases the timer via `rekeyComplete`. Acceptable per the architect's security review: seal failures are realistically unreachable under correct flynn/noise (same posture as `sealError`); the remediation is operator-visible (`v2.rekey.emit.seal_failed` log line) and the operator can re-run `pyry rekey <conn_id>` once slice B2 lands. Re-arming the timer in the seal-failure branch would mask the underlying error AND introduce a code path the scheduled emit doesn't have — extra surface area for an unobserved failure mode.

**Control-socket wire-up of `Rekey` is out of scope.** `NewV2SessionManager` gained its first production caller in [#549](../codebase/549.md) (the `PYRY_MOBILE_V2=1` daemon cutover constructs the manager and drives `Run`), but #549 deliberately does **not** call `ctrlServer.SetRekeyer(mgr)` — that is a named non-goal. So the `Rekey` method is still reachable from `internal/relay` tests only until the control-socket wire-up lands in a separate ticket. The sibling slice B2 ships the `pyry rekey <conn_id>` operator verb in `cmd/pyry`; once both B2 and the `SetRekeyer` wire-up land on top of #549's manager construction, the verb is end-to-end functional.
