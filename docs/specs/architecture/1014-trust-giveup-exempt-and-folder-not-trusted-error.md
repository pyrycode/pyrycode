# Spec: Exempt the pending trust window from the give-up bound, and surface a typed "folder not trusted" session error on deny/timeout (#1014)

**Size:** S · **Security-sensitive:** yes · Split from #992 · Blocked by #1013 (landed, `b394e55`).

## TL;DR

#1013 made `deliverViaSession` return a retryable `ErrTrustModalPending` while claude's
startup trust-folder modal is up, so the msgqueue holds the queued turn instead of
clobbering the consent gate. Two behaviours remain to close #988 defect 1:

1. **AC-1 (give-up exemption).** The msgqueue drain currently counts *every* consecutive
   delivery failure toward its 2-minute give-up bound (#1000). A `ErrTrustModalPending`
   hold is a *legitimate* wait for a remote decision, not a failure — it must not burn the
   bound, or a slow-but-valid remote accept races a premature give-up. Fix: an injected
   `Pending` classifier seam on `msgqueue.Config`; a pending return resets the give-up
   streak and retries without counting.
2. **AC-2/3 (typed error).** When trust is *refused* — a remote `modal_answer{exit}`
   (deny), or the deny-on-timeout (#725) — surface a typed, client-visible `session_error`
   (`CodeSessionBlocked`, reason `"folder not trusted"`) over the v2 wire, reusing the
   #1008 give-up → `session_error` frame path, instead of only looping a silent retry.
   Fix: the trust-class deny/timeout branches of `modalResolverV2` emit into the *same*
   `giveUps` channel the `sessionErrorEmitterV2` already drains.

No auto-trust is introduced (AC-4). Two production sites change (`internal/msgqueue/queue.go`,
`cmd/pyry/modal_resolve_v2.go`) plus their `cmd/pyry` wiring (`main.go`, `relay.go`).

## Context

**Problem.** After #1013, a queued turn for a conversation whose claude child spawned in an
untrusted cwd is *held*: `WriteUserTurn → deliverViaSession → readyForDelivery` returns
`ErrTrustModalPending` (before `DeliverPrompt`), the msgqueue drain retries the FIFO head
every 1s. Two gaps remain:

- The drain's give-up latch (`firstFailedAt`, `giveUpAfter = 2 min`) treats the pending
  hold as a persistent failure. If the operator takes ~2 min to decide, the give-up fires
  and drops the head *even though a valid accept was imminent* — the exact race #988 flags.
- On a deny/timeout the client sees nothing typed: the modal is ESC-dismissed, claude exits
  and respawns into the same untrusted cwd, the trust modal re-appears, and the turn stays
  held with only msgqueue's per-retry `Warn` in the daemon log — a silent wedge from the
  client's view.

**Why the typed error must fire at the resolution site, not the drain.** A deny is *not*
observable as a distinct terminal state at the drain: ESC on claude's trust dialog = "exit"
→ claude quits → supervisor respawns → untrusted cwd again → trust modal again →
`ErrTrustModalPending` again. From the drain, "denied then re-pending" is indistinguishable
from "still pending." The deterministic signal "this trust prompt was refused" exists only
at `modalResolverV2.ResolveTimeout` / `ResolveAnswer` (deny), where the class and outcome are
known. So AC-1 (exemption) lives at the drain; AC-2/3 (typed error) lives at the resolver.
They are independent mechanisms at different sites.

**Why no auto-trust (AC-4).** Interactive sessions run `--dangerously-skip-permissions`;
folder-trust is the last consent gate (#988, ADR 025). The only resolutions stay the pre-built
authorized ones: a remote `modal_answer` behind the fail-closed `MayAnswerRemotePermission`
gate, a local operator at the attach TTY, or the fail-closed deny-on-timeout. This ticket adds
no trust decision and marks nothing trusted.

**What already exists (reuse, do not rebuild).**

- The give-up seam (`msgqueue.Config.OnGiveUp`, `GiveUpFunc`, the drain's `firstFailedAt`
  latch and `giveUp` branch) — #1000.
- The typed error producer: `giveUps` channel → `sessionErrorEmitterV2.broadcast` →
  `TypeSessionError` + `CodeSessionBlocked` + `SessionErrorPayload{ConversationID, Code,
  Message}`, fanned to every interactive conn — #1008. The `sessionErrorNotify(giveUps,
  logger)` closure is the non-blocking, drop-on-full send into it.
- The deny/timeout resolution: `modalResolverV2.ResolveTimeout` (ESC + audit `denied_timeout`)
  and `ResolveAnswer` (`classifyAnswer` maps `exit → OutcomeDeny/verbEsc`, `proceed →
  OutcomeAllow/verbAcceptTrust`) — #717/#725. `modalDenyTimeout = 2 min`
  (`internal/relay/v2session.go:93`) arms on every trust surface (#725), so a pending head is
  *always* resolved (accept or deny-on-timeout) within 2 min — there is no silent-forever hold.
- The retryable sentinel `supervisor.ErrTrustModalPending` (`internal/supervisor/supervisor.go:67`)
  — #1013. This ticket keys AC-1 off it via `errors.Is`.

## Files to read first

- `internal/msgqueue/queue.go:435-500` — `drain`, the retry-on-error loop with the drain-local
  `firstFailedAt` latch. **AC-1 lands in the `if err != nil` block (455-487).** Note the reset
  at 497 (`firstFailedAt = time.Time{}`) on a confirmed delivery — the exemption reuses this
  exact "reset the streak" semantics.
- `internal/msgqueue/queue.go:116-140` — `Config` (add the `Pending` field beside `OnGiveUp`)
  and `queue.go:83-114` — the `DeliverFunc`/`ChangeFunc`/`GiveUpFunc` injected-seam doc style
  to mirror. `queue.go:194-224` — `New` (thread the new field into the struct like `onGiveUp`).
- `internal/msgqueue/queue.go:502-532` — `giveUp` (the real-failure branch that must stay
  unchanged for non-pending errors).
- `cmd/pyry/modal_resolve_v2.go:43-54` — `modalResolverV2` struct + `newModalResolverV2` (add
  two **nil-default fields**, do NOT change the constructor signature — see Design §3).
- `cmd/pyry/modal_resolve_v2.go:120-150` — `ResolveTimeout` (emit on trust class after the
  successful `Resolve`) and `166-235` — `ResolveAnswer` (emit on trust class + `OutcomeDeny`
  after `auditAnswer`). `250-282` — `optProceed`/`optExit` local-const duplication pattern +
  `classifyAnswer` (returns `outcome devices.RemotePermissionOutcome`).
- `cmd/pyry/session_error_v2.go:31,99-181` — `giveUpNotice{convID, reason}`,
  `sessionErrorNotify` (the closure to reuse), and `broadcast` (stamps `CodeSessionBlocked` +
  `SessionErrorPayload`). Confirms the reason rides as `Message`, is never logged, and the
  producer holds no queue handle (the AC-3 no-queued-text guarantee).
- `cmd/pyry/main.go:812-844` — `giveUps` channel + `msgqueue.New(Config{... OnGiveUp:
  sessionErrorNotify(giveUps, logger) ...})` + `newSessionErrorEmitterV2(giveUps, ...)`. Wire
  the `Pending` classifier here and build the shared blocked-notify closure here.
- `cmd/pyry/relay.go:166-216` — `relayWiring` fields (`active`, `sup`, `queue`, `sessionErr`
  already present; add `blockedNotify`). `relay.go:479` — the single production
  `newModalResolverV2(modalReg, w.sup, logger)` call site (set the two new fields here).
- `cmd/pyry/main.go:1131-1195` — `activeConversation` (`CurrentConversation() string`, mu-guarded,
  safe from any goroutine) — the convID source, the same follow-active cursor the turn/modal
  producers use via `resolveTarget`.
- `internal/protocol/messaging.go:247` — `SessionErrorPayload{ConversationID, Code, Message}`;
  `internal/protocol/codes.go:34` — `CodeSessionBlocked` ("terminal give-up; NOT a retry hint").
- `internal/modalbridge/modal.go:34-35,75-82` — `classTrust = "trust"` (the wire class string,
  unexported; duplicate a local `classTrust` const in `modal_resolve_v2.go` like `optExit`) and
  `Outstanding` (carries `Class`, **no convID** — the reason AC-2's convID comes from `active`).
- `docs/knowledge/codebase/1013.md` — the sentinel + retryable-not-park decision this builds on;
  its "Related" note: *"Do not touch `giveUpAfter` or `drain`'s give-up branch here — that's
  #1014's surface."* This ticket is that surface.
- **Coexistence with in-flight PR #935 (issue #934):** #935 is an open, **test-only** flaky-fix
  touching `internal/msgqueue/queue_test.go` inside `TestQueue_SnapshotAll_RaceWithEnqueueAndDrain`
  (lines ~713-759). **Do NOT modify `queue_test.go`.** Put the new msgqueue tests in a NEW file
  `internal/msgqueue/giveup_exempt_test.go` (package `msgqueue`). Different files never merge-conflict,
  so this dissolves the overlap structurally (see § Concurrency / integration).

## Design

### 1. AC-1 — give-up exemption via an injected `Pending` classifier (`internal/msgqueue`)

`internal/msgqueue` must not import `internal/supervisor` (it is a leaf with an injected
`DeliverFunc`). So the "is this error a legitimate hold?" decision is an injected predicate,
wired in `cmd/pyry` where both packages are importable — mirroring `Deliver`/`OnChange`/`OnGiveUp`.

**New seam type + Config field** (`queue.go`, beside `GiveUpFunc`/`OnGiveUp`):

```
// PendingFunc classifies a delivery error as a legitimate hold (the head is being
// deliberately withheld awaiting an external decision) rather than a failure.
type PendingFunc func(error) bool
```

`Config.Pending PendingFunc` — doc: while it returns true the drain retries the head WITHOUT
counting the elapsed window toward `GiveUpAfter`, and RESETS the give-up streak so a transient
real failure that precedes the hold never leaks into the held window. `nil` ⇒ every non-nil
delivery error counts (pre-#1014 behaviour). Store it on `Queue` as `pending PendingFunc`; `New`
copies it like `onGiveUp` (no default — nil is valid).

**Drain change** — inside `drain`'s `if err != nil` block, *before* the `firstFailedAt`
bookkeeping, add one branch:

- **Behaviour contract:** if `q.pending != nil && q.pending(err)` → this is a hold:
  1. reset the streak: `firstFailedAt = time.Time{}` (identical to the confirmed-delivery reset
     at line 497 — so a prior real-failure streak cannot survive into the pending window, and
     the give-up bound never trips while pending);
  2. do NOT call `giveUp`;
  3. retry the same head after `q.retry` (reuse the existing `sleepCtx` + `continue`);
  4. log at **Debug** (not Warn), content-free (`conversation_id`, `queued_msg_id`, `queued_at`
     — NEVER `head.text`), so a long legitimate pending window doesn't spam the operator log at
     Warn. This is the "quiet the retry volume" #1013 deferred here.
- The existing real-failure path (455-487: set `firstFailedAt`, Warn, check `giveUpAfter`,
  `giveUp`) is unchanged and still fires for every non-pending error (`ErrNoLiveSession`,
  `ErrTurnNotCommitted`, wrapped ctx timeouts) — the give-up bound is untouched for real wedges.

The classifier is specific (a single dedicated sentinel), so no genuine failure is ever
misclassified as a hold. The `ctx.Err()` shutdown check at 456 stays first, ahead of the new
branch (shutdown always wins).

**Wiring** (`cmd/pyry/main.go`, in the `msgqueue.New(Config{...})` at 820):

```
Pending: func(err error) bool { return errors.Is(err, supervisor.ErrTrustModalPending) },
```

`main.go` already imports `errors` (line 36) and `internal/supervisor` (line 61).

### 2. AC-2/3 — typed "folder not trusted" error at the resolution site (`cmd/pyry`)

Reuse the #1008 path end-to-end: feed a `giveUpNotice{convID, reason}` into the *same* `giveUps`
channel the `sessionErrorEmitterV2` drains, so the client receives an identical `TypeSessionError`
+ `CodeSessionBlocked` frame — no new producer, no new wire type.

**The notify seam.** Build ONE closure in `main.go` and share it:

```
blocked := sessionErrorNotify(giveUps, logger) // type msgqueue.GiveUpFunc == func(convID, reason string)
```

Use `blocked` for both `Config.OnGiveUp` (unchanged behaviour) and a new `relayWiring.blockedNotify`
field. (`sessionErrorNotify`'s drop-full log says "give-up", a content-free cosmetic; reuse is
faithful to "reuse the give-up path" and the 16-deep buffer makes overflow unreachable. A
dedicated variant is optional and not required.)

**The convID.** `Outstanding` carries no conversation id, and threading one through
`modalbridge.Record` → `Outstanding` → the producer would span 7 production files (oversized).
Instead stamp `active.CurrentConversation()` at resolution time — the same follow-active cursor
the modal producer already resolves its target from (`resolveTarget`, #679). In the common
single-conversation flow the modal was surfaced against `active` and the held turn belongs to
`active`, so the two agree. (Edge case + accepted limitation in § Open questions.)

**Resolver changes** (`modal_resolve_v2.go`):

- Add two **nil-default fields** to `modalResolverV2`: `activeConv func() string` and
  `notifyBlocked func(convID, reason string)`. **Do NOT change `newModalResolverV2`'s signature**
  — it has 18 test call sites; changing it forces a mechanical cascade across two test files.
  Leave the constructor as-is (fields default nil ⇒ emit disabled ⇒ the 18 existing tests are
  untouched and unchanged in behaviour). Set the two fields at the single production site.
- Add a local const `classTrust = "trust"` (mirrors the existing `optProceed`/`optExit`
  duplication of modalbridge's unexported constants) and a reason const
  `reasonFolderNotTrusted = "folder not trusted"` (static, content-free — AC-3).
- One private helper `emitFolderNotTrusted()`:
  contract — if `r.notifyBlocked != nil && r.activeConv != nil`, call
  `r.notifyBlocked(r.activeConv(), reasonFolderNotTrusted)`; else no-op. Never touches modal body.
- **`ResolveTimeout`**: after the successful `r.reg.Resolve` (the winner path, `ok == true`) and
  the existing ESC/audit, if `out.Class == classTrust` call `emitFolderNotTrusted()`. The loser
  path (`ok == false`, an answer/cancel won the race) returns before any emit — unchanged.
  Permission-class timeouts do NOT emit (AC scoped to trust).
- **`ResolveAnswer`**: after `auditAnswer`, if `out.Class == classTrust && outcome ==
  devices.OutcomeDeny` (i.e. `exit`) call `emitFolderNotTrusted()`. A trust `proceed`
  (`OutcomeAllow`) does NOT emit — the turn will run once trust clears. Permission answers do
  NOT emit.
- **`ResolveCancel`** is left unchanged (out of scope): a `modal_cancel` is a neutral dismiss,
  not the "deny" or "timeout" AC-2 names; the ESC re-surfaces the trust modal, and its
  deny-on-timeout then emits. Noted for the reviewer.

**Wiring** (`relay.go`): add `blockedNotify func(convID, reason string)` to `relayWiring` (set
from `main.go`'s `blocked`). At the `relay.go:479` call site, construct the resolver and set the
two fields before use (all package `main`):

```
mr := newModalResolverV2(modalReg, w.sup, logger)
mr.activeConv = w.active.CurrentConversation
mr.notifyBlocked = w.blockedNotify
// ... ModalResolver: mr,
```

### 3. Why nil-default fields, not constructor params

`newModalResolverV2` has 18 test call sites (`modal_resolve_v2_test.go` ×16,
`interactive_modal_v2_test.go` ×2) plus the 1 production site. Adding required params forces ~18
mechanical test edits (over the edit-fan-out red line). Nil-default fields set at the single
production site keep every existing test compiling and unchanged, and match the codebase's
established "nil disables the optional seam" convention (`OnChange`, `OnGiveUp`, `ModalResolver`,
`DebugBundler`, `SettingsUpdater` are all nil-disabled). Only the NEW emit tests set the fields.

### Data / control flow (deny-on-timeout, end-to-end)

```
turn queued for conv A ─ drain ─ WriteUserTurn ─ deliverViaSession ─ readyForDelivery
   claude spawned in untrusted cwd ─ startup trust modal up
   A) producer (PRE-BUILT): TypeModalShown broadcast + ArmModalTimeout(2 min)
   B) delivery: readyForDelivery ⇒ ErrTrustModalPending
      → drain: Pending(err)==true ⇒ reset firstFailedAt, Debug-log, retry 1s   ...... AC-1
      (give-up bound never counts the pending window)
   nobody answers within 2 min:
      handleModalTimeout ─ ResolveTimeout(modalID)  [winner: Resolve ok]
         → ESC (fail-closed deny) + audit denied_timeout   (PRE-BUILT)
         → out.Class == "trust" ⇒ notifyBlocked(active.CurrentConversation(), "folder not trusted")
            → giveUps ← giveUpNotice{A, "folder not trusted"}
            → sessionErrorEmitterV2.broadcast ⇒ TypeSessionError{CodeSessionBlocked} to interactive conns  AC-2/3
   claude exits (ESC=exit) → respawn → trust modal again → held again (Pending, exempt)
```

Remote-deny path is identical except the trigger is `ResolveAnswer(exit)` (immediate, no 2-min
wait). Accept path (`proceed`) emits nothing and the turn runs on the next retry (unchanged).

## Concurrency model

No new goroutines, channels, or locks.

- The `Pending` branch runs on the pre-existing per-conversation `drain` goroutine; `firstFailedAt`
  is drain-local (no synchronisation). The classifier closure is a pure `errors.Is` (no state).
- The emit runs on the v2 manager's single `Run` dispatch goroutine (where `ResolveTimeout` /
  `ResolveAnswer` already run). `notifyBlocked` is `sessionErrorNotify`'s non-blocking, drop-on-full
  buffered send (safe from any goroutine, MUST-NOT-BLOCK-honouring). `activeConv()` reads under
  `activeConversation.mu` (safe from any goroutine). The `giveUps` channel is already concurrency-safe
  and already has two senders conceptually (the drain's `OnGiveUp` and now the resolver) — a channel
  send from two goroutines is fine.
- **Integration safety (PR #935):** the msgqueue tests live in a new file `giveup_exempt_test.go`;
  `queue_test.go` is untouched, so `git merge main` after #935 (or #1014) lands cannot conflict on
  that file. Production `queue.go` has zero #935 overlap (#935 is test-only).

## Error handling / failure modes

| Delivery / resolution event | Behaviour after this ticket |
|---|---|
| `ErrTrustModalPending` at drain | `Pending`==true ⇒ reset streak, Debug-log, retry; give-up NOT counted (AC-1) |
| `ErrNoLiveSession` / `ErrTurnNotCommitted` / ctx timeout at drain | `Pending`==false ⇒ existing real-failure path, counts toward 2-min give-up (unchanged) |
| real failure streak, THEN trust modal appears | pending resets `firstFailedAt` ⇒ prior streak discarded (correct: situation is now a legitimate hold) |
| trust modal deny-on-timeout (`ResolveTimeout`, class trust, winner) | ESC + audit (pre-built) + emit `session_error{A,"folder not trusted"}` (AC-2/3) |
| trust `modal_answer{exit}` (`ResolveAnswer`, `OutcomeDeny`) | keystroke + audit (pre-built) + emit (AC-2/3) |
| trust `modal_answer{proceed}` (`OutcomeAllow`) | accept keystroke (pre-built); NO emit; turn runs on next retry |
| permission-class deny/timeout | NO emit (AC scoped to folder-trust) |
| `ResolveTimeout`/`ResolveAnswer` loser (already resolved) | returns before emit (unchanged) |
| `active.CurrentConversation() == ""` | frame carries empty `conversation_id` (fail-safe; broadcast still informs interactive conns) |

Fail-closed posture preserved: the safe default is still non-delivery of untrusted content into
the consent gate (#1013); this ticket only changes *timing accounting* (exempt) and *notification*
(emit) — it makes no delivery or trust decision.

## Testing strategy

Table-driven, stdlib `testing`, no live claude (drive the seams). Bullet the scenarios; write test
code in the project idiom.

**`internal/msgqueue/giveup_exempt_test.go` (NEW file — do NOT touch `queue_test.go`).**
Drive the public API (`New` with a scripted `Deliver` + a `Pending` closure + an `OnGiveUp` spy +
short `GiveUpAfter`/`RetryInterval`, then `Enqueue` + `Run` under a bounded ctx). Scenarios:

- **Pending never gives up:** `Deliver` returns a sentinel classified pending for longer than
  `GiveUpAfter` (several retry cycles); assert `OnGiveUp` is never called and the head is still
  queued (`Snapshot` non-empty). Then flip `Deliver` to succeed; assert the head advances
  (delivered) — "accept ⇒ turn runs."
- **Non-pending still gives up:** `Deliver` returns a *different* error the `Pending` closure
  rejects; assert `OnGiveUp` fires within ~`GiveUpAfter` with a content-free reason and the head
  drops — the give-up bound is intact for real wedges.
- **Reset semantics:** `Deliver` returns a non-pending error for < `GiveUpAfter`, then a pending
  error, then a non-pending error again for < `GiveUpAfter`; assert `OnGiveUp` does NOT fire
  (the pending reset the streak, so neither non-pending window alone crosses the bound).
- **nil `Pending` = pre-#1014:** with `Pending: nil`, a persistently-failing head gives up as
  before (regression guard that the branch is opt-in).
- Confidentiality: assert no test ever needs `head.text` in a log; the pending Debug log carries
  only ids/timestamps.

**`cmd/pyry/modal_resolve_v2_test.go` (ADD cases — existing 18 constructor calls unchanged).**
Build a `modalResolverV2` via `newModalResolverV2`, then set `activeConv` (fake returning a known
convID) and `notifyBlocked` (spy capturing `(convID, reason)`); `Record` a trust modal in a real
`modalbridge.Registry`; use the existing fake keystroker. Scenarios:

- **Trust timeout emits:** `ResolveTimeout` on a recorded **trust** modal ⇒ spy called once with
  `(knownConvID, "folder not trusted")`; assert reason equals the constant (content-free) and
  convID came from `activeConv`.
- **Permission timeout does NOT emit:** `ResolveTimeout` on a **permission** modal ⇒ spy not called.
- **Trust answer deny emits:** `ResolveAnswer` with `exit` on a **trust** modal from a gated device
  ⇒ spy called once; **trust `proceed`** ⇒ spy NOT called; **permission** answer ⇒ spy NOT called.
- **Loser does not emit:** pre-`Resolve` the modal id, then `ResolveTimeout`/`ResolveAnswer` ⇒
  spy not called (unknown-id no-op).
- **Nil seam safe:** a resolver with `notifyBlocked`/`activeConv` left nil (the pre-existing
  construction) ⇒ trust timeout/deny runs the pre-built path with no panic and no emit (proves the
  18 unchanged tests keep passing and foreground/v1 stays inert).

**AC-4 (no-regression, by construction).** The diff imports no `internal/agentrun/trust`, calls no
`trust.MarkWorkdirTrusted`, and does not touch `internal/sessions/buildSession`. Keep the #173
`ptyrunner.ErrTrustModalDetected` agent-run tests green (separate path). Grep-assertable.

## Open questions

- **convID accuracy under an active-conversation switch.** `activeConv()` is read at *resolution*
  time. If the operator switches the active conversation to B during A's pending trust window
  (only possible by routing a new turn to B), a subsequent `ResolveTimeout` for A's modal would
  stamp B. This is a cosmetic wire mis-attribution on a broadcast-to-all-interactive `session_error`
  (content-free reason, no security impact), and does not occur in the common single-conversation
  flow. Capturing the surface-time convID would require threading it through `modalbridge.Outstanding`
  (7 production files → a separate ticket if ever needed). **Accepted limitation**; matches #1013's
  analogous "producer follows active" caveat.
- **Repeated emission while a folder stays untrusted.** Each deny→respawn re-surfaces the trust
  modal and arms a fresh 2-min deny-on-timeout, so a `session_error` re-fires every ~2 min until the
  folder is trusted (accept) or the turn is dequeued. The turn is deliberately *not* dropped on deny
  (a later accept can still run it; the ACs don't mandate a drop, and dropping would need a
  cross-component head-kill). The client is informed each window — not silent. If a single-shot
  notification is ever wanted, that is a follow-up, not this ticket.

## Security review

**Verdict:** PASS

**Trust boundaries / consent gate (the core property).** No MUST FIX. This ticket adds *no*
resolution path and *no* trust decision. Trust can still be granted only by the pre-built
authorized routes: a remote `modal_answer{proceed}` behind the fail-closed
`Device.MayAnswerRemotePermission` gate (`modal_resolve_v2.go:183`, unchanged), a local operator
at the attach TTY, or the fail-closed deny-on-timeout ESC (#725). `readyForDelivery` (#1013) still
returns `ErrTrustModalPending` before `DeliverPrompt`, so no queued (untrusted) byte reaches the
consent gate — that structural property is unchanged; the give-up branch this ticket edits runs
*after* delivery already declined. The exemption changes only *timing accounting*, never *who may
accept*.

**Auto-trust / privilege — AC-4.** No MUST FIX. The diff marks nothing trusted: the msgqueue branch
is pure timing bookkeeping; the resolver emit is a content-free notification. No import of
`internal/agentrun/trust`, no `trust.MarkWorkdirTrusted`, no change to `internal/sessions/buildSession`.
Agent-run's own fail-loud readiness (#173) is a different code path, untouched.

**Confidentiality — AC-3 (reason is content-free).** No MUST FIX, and this is load-bearing. The
emitted reason is the static compile-time constant `"folder not trusted"` — it can never carry
`head.text` (untrusted phone content), the modal body/prompt/title, or any secret. It rides as
`SessionErrorPayload.Message` on the existing `sessionErrorEmitterV2`, which holds **no** `*Queue`
handle by construction (`session_error_v2.go:56-62` — the type-system AC-3 guarantee, unchanged
here) and never logs `Message`. The `convID` stamped is `active.CurrentConversation()` — a
daemon-resolved, non-secret routing id, the same class of value the existing give-up path stamps.
The new drain Debug log carries only `conversation_id`/`queued_msg_id`/`queued_at`, never `head.text`.

**Injection — untrusted content into a control surface.** N/A/addressed. No untrusted data flows
into the classifier (`errors.Is` on a static sentinel), the reason (static const), or the emit
(daemon-resolved convID). The trust-class gate uses `out.Class` from the daemon's own registry, not
a phone-supplied field.

**Tokens / secrets / credentials.** N/A. `ResolveAnswer`'s `answerToken` / device gating are the
pre-built path, unchanged; the emit is added strictly after the existing gate/consume/audit.

**DoS / resource bounds — fail-closed.** No MUST FIX. AC-1's exemption means a pending head is
retried indefinitely (1s cadence) rather than given up at 2 min — but this is *bounded and
non-silent*: `MaxQueuedPerConversation` (100) caps the backlog, `WaitReady` returns promptly while a
trust modal is up (each retry is cheap), and the guaranteed 2-min deny-on-timeout (#725) fires a
`session_error` so the client is always informed. The emit path is the existing 16-deep drop-on-full
buffered channel — non-blocking, honouring `GiveUpFunc`'s MUST-NOT-BLOCK, memory-bounded. No new
goroutine, lock, or unbounded buffer. A hostile actor cannot amplify: triggering repeated emissions
requires the operator's own untrusted folder plus a paired device, and each is one bounded frame per
2-min window. The classifier is a specific single-sentinel match, so a real wedge is never
misclassified as an exempt hold — the give-up bound still protects genuinely failing conversations.

**Concurrency.** No MUST FIX. No new goroutine/lock; the `giveUps` channel and `activeConversation`
mutex are pre-existing and concurrency-safe; a second channel sender (the resolver, on the manager's
Run goroutine) is safe. See § Concurrency model.

**Error messages / logs / telemetry.** No MUST FIX. New strings are static and content-free
(`"supervisor: trust modal pending"` reused from #1013; `"folder not trusted"`). The pending-branch
log is Debug (reduces the #1013 Warn spam) and content-free. No payload bytes, `err.Error()` on a
marshal path, or modal body reaches any log.

**File ops / subprocess / crypto.** N/A — no filesystem, `exec`, or crypto in the changed code. The
`session_error` frame rides the manager's established Noise_IK sealed transport, unchanged.
