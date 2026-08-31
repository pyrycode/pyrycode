# Inbound modal control (#727/#717) + deny-on-timeout (#725) — `ModalResolver` seam + `modal_dismissed` broadcast

`modal_answer` / `modal_cancel` are v2 **control** envelopes (phone → binary),
intercepted in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route`
(the same boundary `rekey_request` / `request_snapshot` use) — there is **no**
`dispatch.Route` handler. This is the **inbound** half of the daemon-side modal
bridge: the outbound half surfaces a modal to phones ([`modal_shown` + the
outstanding-modal registry](modalbridge-package.md), #716); this slice lets a
phone *resolve* it. The seam is the foundation #717 (gated `modal_answer`) and #725 (deny-on-timeout) layer on; #727 proves it via `modal_cancel` (dismiss =
fail-safe deny). **`security-sensitive`**: an inbound untrusted frame mutates the
modal lifecycle and fans out a broadcast on the internet-exposed relay (spec-stage
security review, verdict PASS). See [`codebase/727.md`](../codebase/727.md).

Modal control is **fire-and-broadcast, not request/reply** — there is no reply to
the caller, so no decode error or attacker-controlled byte is ever echoed back.

- **`ModalResolver` consumer seam.** The relay declares the two-method interface
  (beside `ScreenSnapshotter`) and reaches the daemon's outstanding-modal state
  through it, so `internal/relay` imports neither `internal/supervisor`,
  `internal/modalbridge`, `internal/audit`, nor `cmd/pyry`. The `cmd/pyry`
  `modalResolverV2` (`cmd/pyry/modal_resolve_v2.go`) implements it: `ResolveCancel`
  does registry `Resolve` → keystroker `SendEsc` → `audit.Log({cancelled, remote})`;
  `ResolveAnswer` is the gated answer arm (#717 — `Lookup` → fail-closed gate →
  `option_id` classification → `Resolve` consume → safe-answer keystroke → audit).
  Wired in `cmd/pyry/relay.go`'s `startRelayV2`
  over the **daemon-singleton** `modalbridge.New()` registry (the same instance
  [#798](../codebase/798.md) live-wires the producer into). The keystroker argument
  is nil-safe-wrapped via `modalKeystrokerOrNoop` (#1131, the fourth and final
  typed-nil `w.sup` guard): PTY mode passes `w.sup` straight through (it satisfies
  `modalKeystroker`), while the stream-json bootstrap path (typed-nil `w.sup`,
  #1077) gets a non-nil `noopKeystroker` — `ResolveCancel`/`ResolveTimeout` call
  `SendEsc()` **unconditionally**, so (unlike `screenSnapshotterOrNil`'s genuine-nil
  return) the guard must return something callable, not nil. A stream-json approval
  has no PTY modal to dismiss; it denies fail-closed via the permbridge completer's
  own deny-on-timeout (#1103), never through this keystroker. See
  [codebase/1131.md](../codebase/1131.md).
- **`handleModalCancel`** — nil-resolver ⇒ debug-log + return (inert). Else decode
  `ModalCancelPayload` (a decode failure is tolerated → empty `modal_id` → the
  resolver's unknown-id no-op, never echoed), `ResolveCancel(modal_id, s.device)`;
  on `ok=false` return (unknown/already-resolved id → no keystroke, no audit, no
  broadcast — **AC-4**); on `ok=true` call `broadcastModalDismissed`.
- **`handleModalAnswer`** — symmetric to `handleModalCancel`. #727 shipped this
  arm with a deferred-no-op `ResolveAnswer` (always `ok=false`); #717 filled the
  gated answer arm in the resolver impl, so the broadcast line is now live: an
  authorized `modal_answer` returns `ok=true` with `Outcome` = the answered
  `option_id`, fanning the dismissal. The manager code is unchanged — #717 touched
  only `cmd/pyry/modal_resolve_v2.go` (see [`codebase/717.md`](../codebase/717.md)).
  An ungated / forged / stale answer still returns `ok=false` (no broadcast).
- **`broadcastModalDismissed(ctx, modalID, d)`** — the **load-bearing concurrency
  fact**: it fires from inside `dispatchAppFrame`, on the single `Run` goroutine,
  and **MUST NOT call `ActiveConns`** (which funnels its request *back* onto this
  same goroutine via `m.snapshot` → **deadlock**). Instead it reads `m.sessions`
  **directly** (the `handleActiveConns` pattern), filters `s.state == V2StateOpen
  && s.interactive` (the same #607 gate `modal_shown` rides), and `Push`es a
  `modal_dismissed{modal_id, outcome, source}` per conn. `Push` is
  `Run`-goroutine-safe — it touches only `m.queues` under `pushMu` and returns
  immediately (seal+forward on a later `Run` iteration via `drainOnce`), so the
  fan-out never blocks the dispatch goroutine. One shared `time.Now().UTC()`;
  envelope `ID: 1` is non-load-bearing (the phone correlates on `modal_id`;
  `modal_dismissed` is a control event, `EventID == nil`, never in the #647 ring,
  so no per-session counter is added to `V2Session`). A per-conn `Push` error
  (ctx teardown / `ErrConnNotFound` from a raced teardown) is debug-logged with
  the transport sentinel only and the fan-out continues — payload bytes are never
  logged.

The fan-out reaches *every* interactive conn, including ones that never saw this
modal's `modal_shown`; the payload carries only the opaque `modal_id` +
`cancelled`/`remote` (no modal body), so a conn with no matching outstanding modal
just ignores it. **`TestV2Session_ModalCancel_FanOut`** drives the cancel through
the real `Frames`/`Run` loop with three heads (two interactive, one not) — an
accidental `ActiveConns` call would hang it, making the test a *structural*
no-deadlock proof — and asserts the dismissal reaches both interactive heads and
neither the non-interactive one.

#### Deny-on-timeout (#725) — fail-closed safe-deny on an unanswered modal

The fail-closed safety net: if **no** authorized device answers within a bounded
window, the daemon **safe-denies** the modal rather than leave claude blocked
forever or risk a silent grant. It reuses `broadcastModalDismissed` unchanged and
adds a third `ModalResolver` arm (`ResolveTimeout`) plus a daemon-global timer
funnelled onto `Run`. **`security-sensitive`** (a timer on the permission surface;
spec-stage security review verdict PASS). See [`codebase/725.md`](../codebase/725.md).

Unlike `modal_answer`/`modal_cancel`, a timeout is **not** an inbound frame — it
originates internally and rides a new path:

- **Arm (off `Run`).** The producer surfacer (`interactiveModalEmitterV2.Handle`,
  cmd/pyry, live-wired by [#798](../codebase/798.md)) calls `(*V2SessionManager).ArmModalTimeout(ctx, modalID)`
  **immediately after `reg.Record`** — before the marshal/broadcast, so a modal that
  fails to marshal, or one surfaced to **zero** interactive conns, is still denied on
  the window (claude is blocked regardless of who is watching). `ArmModalTimeout` only
  calls `time.AfterFunc(modalDenyTimeout, cb)` and touches no `Run`-owned state, so it
  is safe off the `Run` goroutine. `modalDenyTimeout` is a package var (2 min default,
  test-overridable; ADR 025 specifies "a bounded window" but no number).
- **Funnel (`AfterFunc` callback → `Run`).** `cb` does
  `select { case m.modalTimeout <- modalID: case <-ctx.Done(): }` — the `armRekeyTimer`
  callback shape. `modalTimeout` is a **daemon-global** buffered (`wakeBufferSize`=16)
  channel keyed by `modal_id` (unlike `wake`, keyed by `*V2Session` — a modal is not
  bound to one conn). The `*time.Timer` is **deliberately discarded, never `Stop`ped**:
  the registry's one-shot `Resolve` is the idempotency gate, so a timer that fires after
  an answer/cancel already consumed the modal simply no-ops; an un-fired `AfterFunc`
  parks no goroutine, so leaving it un-`Stop`ped leaks nothing (avoids a
  `map[modalID]*time.Timer` + new lock for zero correctness gain).
- **Fire (on `Run`).** A new `Run` select arm
  `case modalID := <-m.modalTimeout: m.handleModalTimeout(runCtx, modalID)`.
  `handleModalTimeout` is a near-copy of `handleModalCancel`: nil-resolver ⇒ inert
  debug-log; else `ResolveTimeout(modalID)`; on `ok=false` return (already
  answered/cancelled — no keystroke, no audit, no broadcast); on `ok=true` call
  `broadcastModalDismissed` (the **same** fan-out, with `{denied_timeout, timeout}`).
- **`ResolveTimeout`** (cmd/pyry `modalResolverV2`) mirrors `ResolveCancel`: registry
  `Resolve` → best-effort `SendEsc` → `audit.Log({denied_timeout, timeout})` → return
  `{denied_timeout, timeout}, true`. Differences: **no device** (empty audit identity —
  the documented no-device-timeout case), and `denied_timeout`/`timeout` classification.

**Exactly-once is structural, not lock-defended.** Answer/cancel-resolution
(`handleModalAnswer`/`handleModalCancel`, via `m.cfg.Frames`) and timeout-resolution
(`handleModalTimeout`, via `m.modalTimeout`) are **both arms of the same `Run`
`select`** — serviced one at a time. Whichever `Run` services first consumes the modal
via the one-shot `Resolve`; the loser sees `ok=false` and no-ops. So an answer-vs-timeout
race cannot double-deny, double-broadcast, or double-audit; the registry mutex still
guards `Record` (surfacer goroutine) against `Resolve` (`Run`). The timeout leg **only
ever drives the deny keystroke**, never a grant — fail-closed by construction (ADR 025
§ Security model: "answered with the SAFE default (deny / ESC) … Never auto-grant").
**Live in production since [#798](../codebase/798.md)** wired the surfacer: a real
permission/trust modal now `Record`s an entry and arms this timer, so an unanswered modal is
safe-denied on the window even if it reached zero phones (net-positive availability — before #798 nothing armed it in production). `TestV2Session_ModalTimeout_FanOut` proves the
off-`Run`-arm → on-`Run`-fire crossing under `-race`.

#### Stream-json approval bridge — the verdict arm (#1080)

`ResolveAnswer` gained a **second** actuation arm alongside the tui keystroke arm
above: a `modal_answer` for a **stream-json** permission request (a
[`permbridge`](permbridge-package.md)-parked completer, #1103) resolves that
completer to allow/deny instead of routing a keystroke — no on-screen modal exists
on the stream-json path, so there is nothing to press Esc/Enter into. Everything
up through the gate → classify → modalbridge-consume steps in `ResolveAnswer` is
**unchanged and runs first**, regardless of which arm actuates.

- **`streamApprovalBridge`** (`cmd/pyry/modal_resolve_v2.go`, co-located with
  `modalResolverV2`) is the join: it owns the `modal_id ⇄ tool_use_id`
  correlation neither `permbridge.Registry` nor `modalbridge.Registry` holds.
  `Surface(req permbridge.Request) (retire func())` — called from
  `internal/control/server.go`'s `handleApprove` via the new
  `SetApprovalSurfacer` seam (mirrors `SetRekeyer`/`SetApprovalRegistry`) —
  raises a parked approval as the **same** permission `modal_shown` clients
  already answer (via `modalbridge.PermissionRequestForClass` +
  `modal.Record`, so the 4-option/reject-once-default payload is
  byte-compatible by construction) and stores `byModal[modalID] = toolUseID`.
  `handleApprove` `defer`s the returned `retire` immediately after `Surface`
  returns, so it fires on **every** terminal `Await` path uniformly (answer,
  timeout, disconnect, shutdown).
- **`modalResolverV2.streamApprovals streamApprovalResolver`** — an optional
  nil-default field (the #1014 pattern; 18 test call sites stay untouched). At
  the actuate step, `ResolveAnswer` computes `allow :=
  devices.AuthorizeRemotePermission(dev, outcome)` **once** and dispatches:
  `r.streamApprovals != nil && r.streamApprovals.ResolveStream(modalID, allow,
  reasonRemoteDeny)`; only when that returns `false` (nil bridge, or `modalID`
  absent from `byModal` — not a stream approval) does the tui keystroke arm
  run. `ResolveStream` resolves `perm.Resolve(toolUseID, ...)` — `Allow`
  echoing the parked `Input` byte-verbatim, or the fixed content-free
  `reasonRemoteDeny` constant — and does **not** delete the correlation or
  consume modalbridge (both already handled elsewhere).
- **Single-arbiter dismissal, unconditional-delete correlation.** `retire`
  deletes `byModal[modalID]` **unconditionally** on every call, then
  `modal.Resolve(modalID)` — the modalbridge one-shot — decides whether *it*
  (not `retire`) already broadcast the dismissal: a miss means `ResolveAnswer`
  already consumed it (answer path, no second broadcast); a hit means
  timeout/disconnect/shutdown, so `retire` audits `denied_timeout` and
  broadcasts `modal_dismissed` itself (AC-3, no stale modal). The
  architect's spec originally gated the delete behind the `modal.Resolve`
  `ok` branch, which leaked `byModal` forever on every *answered* approval
  (that branch always misses on the answer path) — caught as a MUST FIX in
  the spec's own security review and fixed before code landed; a
  no-correlation-leak test on both the answer and timeout paths is the
  regression guard.
- **No new timer.** The stream modal deliberately does **not** call
  `ArmModalTimeout` — `permbridge`'s own registry-owned timer is the sole
  timeout authority (two timers would drift), and `ResolveTimeout` routes an
  Esc keystroke, which has no target on the stream-json path. `retire`
  (invoked promptly on `Await`'s return) is the client-dismissal backstop
  instead.
- **Wiring.** `startRelayV2` constructs the bridge over the **same**
  `*permbridge.Registry` `runSupervisor` created and the **same**
  `*modalbridge.Registry` the emitter/resolver already share, sets
  `modalResolver.streamApprovals = bridge` **before** `mgr.Run` starts (so no
  data race on the resolver field from an in-flight `modal_answer`), and
  returns `bridge.Surface` outward through `startRelay` to `main.go`, which
  calls `ctrl.SetApprovalSurfacer(surface)`. A nil `w.approvals`
  (foreground/v1/relay disabled) leaves `streamApprovals` nil and
  `SetApprovalSurfacer(nil)` — `handleApprove` still parks and blocks, just
  with no client-facing modal, the pre-#1080 behaviour.

See [permbridge-package.md](permbridge-package.md) for the registry primitive
this bridges, [control-plane.md § Approve](control-plane.md#approve-mcpapprove-verb--forward-to-permbridge-block-default-deny-1104)
for the `handleApprove`/`SetApprovalSurfacer` side, and
[codebase/1080.md](../codebase/1080.md) for the ticket record.
