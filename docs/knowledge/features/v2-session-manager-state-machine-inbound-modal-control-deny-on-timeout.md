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
  and reaches the daemon's outstanding-modal state
  through it, so `internal/relay` imports neither `internal/supervisor`,
  `internal/modalbridge`, `internal/audit`, nor `cmd/pyry`. The `cmd/pyry`
  `modalResolverV2` (`cmd/pyry/modal_resolve_v2.go`) implements it: `ResolveCancel`
  does registry `Resolve` → the same two-arm actuation the stream-json bridge gives
  `ResolveAnswer` below (#2416 — stream-deny via `ResolveStream`, else an unrouted
  Warn) → `audit.Log({cancelled, remote})`; `ResolveAnswer` is the gated answer
  arm (#717 — `Lookup` → fail-closed gate → `option_id` classification → `Resolve`
  consume → unrouted-warn fallback → audit).
  `classifyAnswer`'s own membership scan — is `option_id` one of the ids the modal's
  `Outstanding.Options` actually surfaced — runs *before* the per-id switch that
  decides the outcome, and rejects anything absent from that list regardless of
  what the switch would otherwise do with it (#1545). A pin test aimed at the
  switch (not the scan) has to put its target id inside the fixture's own
  `Options`; giving it as a bare `option_id` on a modal that never surfaced it
  exercises only the membership scan and passes whether or not the switch case
  for that id still exists. #1545's `TestModalResolverV2_Answer_TrustOptionIDsRejected`
  is built this way to pin `proceed`/`exit` rejection at the switch itself.
  Wired in `cmd/pyry/relay.go`'s `startRelayV2`
  over the **daemon-singleton** `modalbridge.New()` registry (the same instance
  [#798](../codebase/798.md) live-wires the producer into). `newModalResolverV2`
  takes only `(reg, logger)` — no keystroker. The seam that used to sit here
  (`modalKeystroker`, nil-safe-wrapped via `modalKeystrokerOrNoop`, #1131) existed
  for the PTY-mode `*supervisor.Supervisor`; #1348 deleted that supervisor and the
  terminal it typed into, and every production call from then on passed a
  `noopKeystroker` whose methods all returned nil, so the seam had actuated
  nothing since #1348. #1546 deleted the interface, the no-op type, the
  resolver's `kb` field/constructor parameter, and the answer digit
  `classifyAnswer` computed only for it. The `!handled` fallback in both
  `ResolveCancel` and `ResolveAnswer` (nil `streamApprovals`, or `ResolveStream`
  declining the id) now logs one Warn (`modal_cancel.unrouted` /
  `modal_answer.unrouted`, carrying `modal_id` only — no body, prompt, title or
  option text) instead of routing a keystroke; consume, audit and the returned
  dismissal are unchanged — on `ResolveAnswer`'s unrouted arm the audit record
  still carries the outcome `devices.AuthorizeRemotePermission` computed (e.g.
  `allowed`) even though nothing actuated it, exactly as it did under
  `noopKeystroker`; the audit log reflects the authorization decision, not
  whether anything downstream consumed it. Production cannot reach this branch
  — `streamApprovalBridge.Surface` is the registry's only producer and always
  correlates — so the Warn is observability for a branch, not a new failure
  mode. A stream-json approval has no PTY modal to dismiss; on timeout it
  denies fail-closed via the permbridge completer's own deny-on-timeout (#1103)
  — the only deny-on-timeout left since #1539 deleted `ResolveTimeout` and the
  relay-side arm that called it, see § Deny-on-timeout below — and on cancel
  (#2416) it denies immediately through the same stream-deny arm `ResolveAnswer`
  uses. See [codebase/1131.md](../codebase/1131.md) for the now-deleted
  nil-safe wrapper's history.
- **`handleModalCancel`** — nil-resolver ⇒ debug-log + return (inert). Else decode
  `ModalCancelPayload` (a decode failure is tolerated → empty `modal_id` → the
  resolver's unknown-id no-op, never echoed), `ResolveCancel(modal_id, s.device)`;
  on `ok=false` return (unknown/already-resolved id → no actuation, no audit, no
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

#### Deny-on-timeout (#725) — deleted by #1539; permbridge's timer is the only one left

**This arm no longer exists.** `ModalResolver.ResolveTimeout`,
`(*V2SessionManager).ArmModalTimeout`, `handleModalTimeout`, the `modalTimeout`
channel/`Run` arm and the `modalDenyTimeout` package var were all deleted by
\#1539. `ModalResolver` now has two methods (`ResolveCancel`, `ResolveAnswer`).
The deletion changed no runtime behaviour: the arm had been production-dead
since #1348 (see the note at the end of this section), so nothing that used to
be denied on this window stopped being denied. The permission bridge's own
timer (#1103, `time.AfterFunc` → `expire` in `internal/permbridge`) is the
**only** deny-on-timeout left; see [permbridge-package.md](permbridge-package.md).

What follows is kept as a historical record of how the mechanism worked while it
was live (#798 through #1348) and reachable only by tests thereafter (#1348
through #1539). Treat every present-tense claim below as **pre-#1539**.

The fail-closed safety net: if **no** authorized device answers within a bounded
window, the daemon **safe-denies** the modal rather than leave claude blocked
forever or risk a silent grant. It reused `broadcastModalDismissed` unchanged and
added a third `ModalResolver` arm (`ResolveTimeout`) plus a daemon-global timer
funnelled onto `Run`. **`security-sensitive`** (a timer on the permission surface;
spec-stage security review verdict PASS). See [`codebase/725.md`](../codebase/725.md).

Unlike `modal_answer`/`modal_cancel`, a timeout is **not** an inbound frame — it
originates internally and rides a new path:

- **Arm (off `Run`).** The design: a producer surfacer calls
  `(*V2SessionManager).ArmModalTimeout(ctx, modalID)` **immediately after `reg.Record`**
  — before the marshal/broadcast, so a modal that fails to marshal, or one surfaced to
  **zero** interactive conns, is still denied on the window (claude is blocked
  regardless of who is watching). From [#798](../codebase/798.md) until #1348
  ("refactor: delete the terminal-driving interactive path and everything on it")
  that producer was `interactiveModalEmitterV2.Handle` (`cmd/pyry`, PTY path); #1348
  deleted it along with the rest of the terminal-driving interactive path, and nothing
  has taken over as its production caller since — see the note in § Deny-on-timeout
  below. `ArmModalTimeout` only calls `time.AfterFunc(modalDenyTimeout, cb)` and
  touches no `Run`-owned state, so it is safe off the `Run` goroutine. `modalDenyTimeout`
  is a package var (2 min default, test-overridable; ADR 025 specifies "a bounded
  window" but no number).
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
**Was never armed in production after #1348, then deleted entirely by #1539.** This was live from [#798](../codebase/798.md) — which wired the PTY-side surfacer (`interactiveModalEmitterV2.Handle` in the now-deleted `cmd/pyry/interactive_modal_v2.go`) to call `ArmModalTimeout` on every surfaced permission/trust modal — until #1348 deleted the terminal-driving interactive path and, with it, that call site. From #1348 to #1539, `ArmModalTimeout`'s only callers were three test files (`v2session_appframe_test.go`, `v2session_debugbundle_test.go`, `v2session_modal_test.go`); nothing production-side armed this timer, so an unanswered PTY-modal fail-closed deny never fired on this window during that window. Found and confirmed independently by both the architect and code-review while investigating #1909 (`docs/specs/architecture/1909-approval-window-ten-minutes.md`, which raises `mcpApprovalTimeout`, a different constant, and touches this file's neighboring `reconcileModals` doc, not this mechanism). #1539 then deleted `ArmModalTimeout`, `handleModalTimeout`, `modalDenyTimeout`, the `modalTimeout` field/`make`/`Run` arm and `ModalResolver.ResolveTimeout` outright, since nothing production-side would ever call them again — a #1539 spec-stage security review (verdict PASS) confirmed the only live producer of `modalbridge.Registry`, `(*streamApprovalBridge).Surface`, already runs behind permbridge's #1103 timer for every modal it records, so no recorded modal ever relied solely on this arm. The stream-json approval path was unaffected throughout: it never used this timer in the first place (see § Stream-json approval bridge below) and fails closed via `permbridge`'s own registry-owned timer (`mcpApprovalTimeout`) instead. The tests that proved the off-`Run`-arm → on-`Run`-fire crossing under `-race` (`TestV2Session_ModalTimeout_FanOut` and its siblings) were deleted along with the arm; the two off-`Run` proofs that borrowed this arm as a sync vehicle (`TestV2Session_SlowHandler_ModalTimeoutStillFires`, debugbundle arm (3)) were retargeted onto other arms — see [the test-surface doc](v2-session-manager-test-surface-same-package-unit-tests-internal-relay.md).

#### Stream-json approval bridge — the verdict arm (#1080)

`ResolveAnswer` gained a **second** actuation arm alongside the unrouted-warn
fallback above: a `modal_answer` for a **stream-json** permission request (a
[`permbridge`](permbridge-package.md)-parked completer, #1103) resolves that
completer to allow/deny instead of falling through to the Warn — no on-screen
modal exists on the stream-json path, and (since #1546) there is no keystroke
seam left at all. Everything up through the gate → classify → modalbridge-consume
steps in `ResolveAnswer` is **unchanged and runs first**, regardless of which arm
actuates.

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
  absent from `byModal` — not a stream approval) does the unrouted-warn fallback
  fire. `ResolveStream` resolves `perm.Resolve(toolUseID, ...)` — `Allow`
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
- **No new timer.** The stream modal deliberately never called `ArmModalTimeout`
  (deleted by #1539) — `permbridge`'s own registry-owned timer is the sole
  timeout authority (two timers would drift), and `ResolveTimeout` (also
  deleted) routed a keystroke, which had no target on the stream-json path
  (the keystroke seam itself is gone entirely as of #1546). `retire` (invoked
  promptly on `Await`'s return) is the client-dismissal backstop instead.
- **`ResolveCancel` shares the same verdict arm (#2416).** A `modal_cancel` for a
  stream-json permission is no longer routed through the keystroke fallback either:
  `ResolveCancel` computes `handled := r.streamApprovals != nil &&
  r.streamApprovals.ResolveStream(modalID, false, false, reasonRemoteDeny)` in the
  exact spot the unconditional `SendEsc` used to sit (the call is long gone; #1546
  deleted the keystroke seam entirely), so a cancelled stream approval
  denies immediately instead of parking until `permbridge`'s own `mcpApprovalTimeout`
  fires (ten minutes by default). Before #2416 this was the one gap in "no on-screen
  modal exists on the stream-json path": cancelling one still consumed and dismissed
  it on the phone side, but told claude nothing, so the completer sat parked until
  the timeout — a silent ten-minute stall a client had no way to see coming. Nothing
  about the correlation bookkeeping changes: `retire` (above) is still the sole
  deleter, and a modal absent from `byModal` — unreachable in production, since
  `streamApprovalBridge.Surface` is the registry's only producer and always
  correlates — still takes the unchanged `!handled` fallback, which now logs an
  unrouted Warn instead of routing a keystroke. `RemoteAnswerable` and the
  per-device answer gate are deliberately **not** applied to this arm: a cancel is
  only ever a deny (the fail-closed direction an interaction-required permission's
  allow-only gate exists to prevent), and the terminal path's cancel was already an
  unconditional deny via ESC (back when a terminal path existed), so this brings
  the stream path level with it rather than granting anything new.
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

#### The question arm (#1973) — a second discriminant ahead of the permission path

`Surface` gained a branch ahead of the arm above: when a
[`questionbridge.Registry`](questionbridge-package.md) is wired and
`questionbridge.Parse(req.ToolName, req.Input)` reports a well-formed,
in-bounds batch, `surfaceQuestion` records it (`Registry.Record` mints the
nonce and stamps the daemon-asserted ids), stores the correlation, and
broadcasts one `question_shown` instead of a permission `modal_shown`. A
malformed or out-of-bounds `AskUserQuestion` input — `Parse`'s two negatives
are deliberately undistinguished — falls through to the unchanged permission
path, the fail-closed degrade for a batch nobody can render. `retireQuestion`
mirrors `retire`: unconditional correlation delete, then the registry's
one-shot `Resolve` decides whether this closure or a resolution path (#1990's
refusal, #1991's answer) broadcasts the dismissal.

**#1990 added the first deliberate resolution, `streamApprovalBridge.RefuseQuestion`,
beside this backstop.** It reads `byQuestion` before consuming the one-shot
(the reverse order lets a refusal win the race and then find no `tool_use_id`,
denying nobody while also silencing the backstop) and hands claude its deny
only after the consume (the reverse order lets `retireQuestion`'s
control-server-deferred call win the one-shot first and broadcast
`unanswered` for a batch the operator actually refused) — the same
before/after shape `ResolveAnswer` already keeps around `modalbridge`. It
also broadcasts from a **detached goroutine** rather than inline like
`retireQuestion`: its only intended caller (#1986's gated resolver) runs on
the `Run` goroutine and owes its own caller the `consumed` bool synchronously,
so it cannot itself hop off `Run` before calling this method, and
`broadcast`'s `ActiveConns` call would deadlock `Run` if made from it
directly. See [questionbridge-package.md](questionbridge-package.md) for the
registry and [`specs/architecture/1990-question-refusal.md`](../../specs/architecture/1990-question-refusal.md)
for the full ordering argument.

**#1991 added the second resolution, `streamApprovalBridge.AnswerQuestion`,
and it needs one more read than the refusal because an allow needs input
where a deny needs none.** Before consuming the questionbridge one-shot it
takes `permbridge.Registry.Lookup(toolUseID)` — comma-ok — to fetch claude's
own parked `Request.Input`; a miss means permbridge already resolved the
approval on its own timer, so `AnswerQuestion` returns having done nothing,
the same inert-on-miss shape `RefuseQuestion` uses for the one-shot itself,
just one step earlier. `RefuseQuestion`'s unconditional resolve is the one
place its shape does not carry over: a `Deny` needs no input, an `Allow`
does.

**Splicing claude's own bytes and re-marshalling the daemon's parked batch
are not interchangeable copies of the same information.** `AnswerQuestion`
builds claude's updated tool input by extracting the `questions` value out
of `permbridge.Request.Input` as a `json.RawMessage`, never by re-marshalling
`protocol.Question` — because `protocol.Question` tags the multi-select flag
`multi_select` where claude's own tool input spells it `multiSelect`, and
claude accepts the call either way, silently, whichever key is present. A
test that decodes the updated input and checks its shape passes on both
routes; only a byte comparison against the parked `Request.Input`, plus a
grep for the surviving key spelling, catches the wrong one. Generalizes past
this ticket: a "pass X through verbatim" claim about a value that also has a
same-shaped sibling type needs that byte-level pin, not a decode-and-check
test.

**Testing the detached dismissal goroutine's push-failure arm and asserting
the log buffer stays empty are mutually exclusive, not merely awkward
together.** The error line `broadcast` writes on a failed `Push` is written
by the detached goroutine *after* the `Push` a test's wait signal observes,
so nothing establishes a happens-before edge between the two — forcing the
failure both fails `-race` and leaves the buffer non-empty, so an
empty-buffer claim can only be tested with that arm left unforced. #1990's
refusal probe already made this choice; #1991's answer probe kept the same
split rather than trying to cover both in one test. #1986's gated-resolver
suite kept the same split: its content-free-audit test runs with the push
arm unforced, and leaves the push-failure arm covered where #1990/#1991
already cover it rather than re-proving it a third time.

**`byQuestion` is a second map, not a second key space inside `byModal`, and
that is load-bearing, not a style choice.** `ResolveStream` treats *any*
`byModal` hit as "resolve this parked completer" — so a batch id stored
there would let a `modal_answer` naming it allow claude's `AskUserQuestion`
call with nobody having answered it. Today that is also gated one level up
(`ResolveAnswer` looks the id up in `modalbridge` first, and a batch is never
recorded there), but that guarantee belongs to another caller and holds only
until one changes; a separate map makes "a lookup can't become an
authorization" structural instead of borrowed. Generalizes: when a lookup
result is used to decide whether to *act* (not just whether to *find*),
sharing its key space with a second identity class turns every future caller
of that map into a place the two classes can be confused.

**An "inherits X for free" claim is worth resolving at the seam X actually
reads, not at the seam that raised the claim.** The ticket asserted a
question inherits #1912's deny-deadline re-arm (`permbridge.Register`'s
per-parked-approval extension for an approval a client can still answer)
with "no question-specific work". That re-arm's `AnswerableFunc` is wired to
`ApprovalAnswerable`, which scans `byModal`'s values — so with the
correlation correctly kept out of that map, a batch parked only in
`byQuestion` would read as unanswerable and every question would deny at the
first elapsed window, the opposite of inheriting the extension. Both
`ApprovalAnswerable` and `ApprovalParked` were widened to read `byModal` and
`byQuestion` through one `parkedToolUseIDs` helper (snapshot both under `mu`,
release, then ask) so the inheritance is actually true rather than merely
asserted. A claim that a mechanism applies "for free" to a new case is only
as good as the seam it's checked against — this one would have passed every
test that never lets a timer fire.

**The dismissal `source` is one sentinel (`no_answer`), not the published
`timeout`.** § Question's carry-over table anticipated `source: timeout` for
the window-elapsing path and something else for disconnect/shutdown, but
`retireQuestion`'s closure runs identically on all three no-answer terminal
paths and carries nothing that tells them apart — emitting `timeout` would
name a cause that is wrong two paths out of three. Fixed in
`docs/protocol-mobile.md` by this ticket. The general lesson: before
mirroring a documented vocabulary at the call site, check whether that call
site can actually observe the distinction the vocabulary assumes — a table
written ahead of the producer can encode a discrimination the eventual code
position structurally cannot make.

No audit record is written on this arm — `audit.Entry`'s `ModalID`/
`ModalClass` fields are modal-shaped, and a batch has neither. See
[questionbridge-package.md](questionbridge-package.md) for the registry this
arm records into.
