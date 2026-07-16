# Spec: Forward the startup trust-folder dialog and hold the queued turn until it resolves (#1013)

**Size:** S · **Security-sensitive:** yes · Split from #992 · Blocks #1014.

## TL;DR

The trust modal is **already surfaced and already resolvable** end-to-end (producer
#708, gated resolver #717, `classifyAnswer` maps `proceed → AcceptTrust`). The one
gap: `Supervisor.deliverViaSession` throws away `Readiness.TrustModal` and types the
queued turn **into** the pending trust modal, clobbering the consent gate. This
ticket makes the delivery path **not deliver while a trust modal is up** — it returns
a new retryable sentinel `ErrTrustModalPending` instead, so the existing msgqueue
retry loop holds the head; once a valid remote accept clears the modal, the next
retry delivers. No new surfacing code, no new resolution code, no auto-trust.

## Context

**Problem.** When the interactive session pool spawns a claude child in a
conversation's cwd the daemon host has not trusted, claude renders its startup
trust-folder prompt. `Supervisor.WriteUserTurn → deliverViaSession` gates on
`Session.WaitReady`, which returns `Readiness{Idle: true, TrustModal: true}` with a
**nil error** (the trust modal is a benign idle condition, not an
`*UnexpectedModalError`; tui-driver `ready.go`). Today `deliverViaSession` discards
that `Readiness` (`if _, err := sess.WaitReady(ctx); err != nil …`) and proceeds to
`DeliverPrompt` — typing the queued turn text into the trust modal. The turn cannot
commit while the folder is untrusted, so it fails and the msgqueue retries (#988
defect 1), and the modal has been clobbered/dismissed by the injected keystrokes.

**Why not auto-trust.** Interactive sessions run with `--dangerously-skip-permissions`,
so folder trust is the *last* consent gate claude has. Per #988's agreed design the
daemon must **not** auto-trust. Instead the trust dialog is forwarded through the
modal pipeline (ADR 025) and resolved via the supervisor's `AcceptTrust` keystroke —
but only by a client the operator authorised with `--allow-remote-permissions`
(`Device.MayAnswerRemotePermission`, fail-closed).

**What already exists (do not rebuild).** The trust modal already rides the live
pipeline built by #708/#716/#717/#725/#706:

- **Surfacing (AC-1).** The modal stream producer (`runModalStream` →
  `interactiveModalEmitterV2.handleModalShown`) already fires on
  `EventKindPtyModalShown{ModalClassTrustFolder}` — including a modal that is
  *already up* when the stream first ticks (tui-driver `events.go:214-215`: "A buffer
  already showing a modal fires EventKindPtyModalShown"). It builds a trust
  `PermissionRequest` with options `{proceed, exit}` (`modalbridge.modal.go:125-129`),
  Records it (minting a one-time `modal_id`), arms the fail-closed deny-on-timeout
  (#725), and broadcasts `TypeModalShown` to every interactive-capable conn. This is
  covered by an existing test (`interactive_modal_v2_test.go:125-140`).
- **Resolution (part of AC-3).** `modalResolverV2.ResolveAnswer` runs the fail-closed
  `MayAnswerRemotePermission` gate, then `classifyAnswer` maps the trust `proceed`
  option to `verbAcceptTrust → Supervisor.AcceptTrust()` (`"1\r"`), and `exit → SendEsc`.
  The deny-on-timeout safe-denies with ESC if nobody answers.

**The gap this ticket closes.** Only the *delivery* path is wrong: it must **hold**
(not deliver) while the trust prompt is pending, so a valid remote accept can resolve
it and the queued turn then runs. Nothing else changes.

**Out of scope (→ #1014, which this blocks).** The typed "folder not trusted" session
error surfaced to the client on deny/timeout, and the give-up-bound *exemption* for
the pending window. #1013 returns a retryable sentinel that #1014 keys off
(`errors.Is`); it does **not** touch the msgqueue give-up bound. See § Interaction
with the give-up bound.

## Files to read first

- `internal/supervisor/supervisor.go:339-373` — `deliverViaSession`, the two-mode
  delivery gate. **This is where the fix lands.** Both modes call `WaitReady`; the
  nil-mode inline at 348-360, the growth-mode via the `deliverGrowthDeps.waitReady`
  closure at 363-366.
- `internal/supervisor/supervisor.go:53-59` — existing sentinels `ErrNoLiveSession`
  / `ErrTurnNotCommitted`. Add `ErrTrustModalPending` alongside, same `var … =
  errors.New("supervisor: …")` shape.
- `internal/supervisor/supervisor.go:382-419` — `deliverGrowthDeps` +
  `confirmViaTranscriptGrowth`. Note it already returns **before** `deliver` when
  `waitReady` errors (412-413) — so re-routing the `waitReady` closure through the new
  gate gives the growth mode its hold for free.
- `internal/supervisor/supervisor.go:600-602` — `New` sets
  `deliverFn = s.deliverViaSession`. Context only; no change.
- `<gomodcache>/github.com/pyrycode/tui-driver@v1.10.0/pkg/tuidriver/ready.go` —
  `Readiness` (the `TrustModal bool` field) + `WaitReady`. Extract: trust modal is
  a **benign nil-error** return with `TrustModal:true`; only a *non-trust*
  unexpected startup modal returns `*UnexpectedModalError` (#173). So the driver
  reads the flag; it must decide the policy.
- `internal/supervisor/supervisor_test.go:1176-1240` — `TestSupervisor_ConfirmViaTranscriptGrowth`
  and the `deliverGrowthDeps` fakes (`waitReady`/`deliver`/`resolve` scripts). Extend
  here for the growth-mode hold. Also the `sup.deliverFn = func(…){…}` override pattern
  (785, 807, 831, 855) — the seam the nil-mode / sentinel unit tests use.
- `internal/msgqueue/queue.go:435-487` — `drain`: the retry-on-error loop
  (`retry = 1s`, `giveUpAfter = 2min`). Confirms a non-nil delivery return is a
  *retryable* failure that keeps the FIFO head — this is the "hold" mechanism. The
  give-up bound here is #1014's surface, not this ticket's.
- `cmd/pyry/interactive_modal_v2.go:93-139` + `interactive_modal_v2_test.go:125-140`
  — the **pre-built** producer surfacing `ModalClassTrustFolder → TypeModalShown`
  (AC-1). No change; read to confirm coverage.
- `cmd/pyry/modal_resolve_v2.go:255-282` — `classifyAnswer` (`proceed→AcceptTrust`,
  `exit→SendEsc`). The **pre-built** resolution path (AC-3). No change.
- `internal/sessions/pool.go:1241-1288` — `buildSession`, the interactive
  per-conversation spawn. Sets `ValidateConversation`, leaves `ResolveTranscript`
  empty (⇒ nil-mode delivery), and does **no** trust marking. Confirm unchanged (AC-4).
- `internal/agentrun/ptyrunner/runner.go:66-67,407` — agent-run's **own** fail-loud
  readiness (`ErrTrustModalDetected`, distinct from this ticket's sentinel). Agent-run
  does **not** use `deliverViaSession`. Confirm untouched (AC-4).

## Design

### The one behavioural change

`deliverViaSession` must consult `Readiness.TrustModal` from `WaitReady` and, when
set, **return without delivering**. Concretely, introduce one small readiness gate
that both delivery modes share, and one sentinel.

**New sentinel** (`internal/supervisor/supervisor.go`, beside the existing ones):

```
// ErrTrustModalPending is returned by the delivery path when claude's startup
// trust-folder modal is up: the queued turn is deliberately NOT delivered into
// the consent gate. Retryable — the caller (msgqueue) holds the head and retries;
// a valid remote accept (Supervisor.AcceptTrust) clears the modal and a later
// attempt delivers. #1014 keys its typed session error + give-up exemption off it.
var ErrTrustModalPending = errors.New("supervisor: trust modal pending")
```

**New gate** — a free function, one level above the concrete session so it is
unit-testable with a fake:

- Signature: `readyForDelivery(ctx context.Context, waitReady func(context.Context) (tuidriver.Readiness, error)) error`
- Behaviour (three rules, no more):
  1. call `waitReady(ctx)`; on a non-nil error return it **unwrapped** (the caller
     owns the `"wait ready: %w"` wrap — see below);
  2. if `Readiness.TrustModal` is true, return `ErrTrustModalPending` **before any
     delivery** — this is AC-2 (no `DeliverPrompt` is reached);
  3. otherwise return nil (idle, no trust modal ⇒ deliver).
- It marks **nothing** trusted and sends **no** keystroke — it only reads readiness
  and classifies. (AC-4: no auto-trust side effect on this path.)

**Wiring into both modes of `deliverViaSession`:**

- *nil-mode* (`ResolveTranscript == nil`, 348-360): replace the bare
  `if _, err := sess.WaitReady(ctx); err != nil { return fmt.Errorf("wait ready: %w", err) }`
  with `if err := readyForDelivery(ctx, sess.WaitReady); err != nil { return fmt.Errorf("wait ready: %w", err) }`.
  The rest (`deliver` → `Committed`) is unchanged.
- *growth-mode* (363-366): change the `deliverGrowthDeps.waitReady` closure from
  `func(ctx) error { _, err := sess.WaitReady(ctx); return err }` to
  `func(ctx) error { return readyForDelivery(ctx, sess.WaitReady) }`.
  `confirmViaTranscriptGrowth` already returns before `deliver` on a `waitReady`
  error (412-413), so the growth mode holds with no other change. The post-`WaitReady`
  growth baseline (416-419) is therefore captured only **after** trust clears — which
  is exactly right; a pre-accept baseline would be meaningless.

`sess.WaitReady` already has signature `func(context.Context) (tuidriver.Readiness,
error)`, so it drops straight into `readyForDelivery`'s parameter — no adapter.

### Error-wrap contract (single-wrap, no double-wrap)

`readyForDelivery` returns the raw `WaitReady` error and the bare
`ErrTrustModalPending`; **each call site** applies the `"wait ready: %w"` wrap
(nil-mode inline, growth-mode inside `confirmViaTranscriptGrowth`). So the pending
case surfaces as `wait ready: trust modal pending`, then `WriteUserTurn`'s outer
`"supervisor: write user turn: %w"` yields
`supervisor: write user turn: wait ready: trust modal pending`. The exact prose is
not load-bearing; the **contract that matters** is:

> At the `WriteUserTurn` boundary, a pending trust modal returns a non-nil error for
> which `errors.Is(err, supervisor.ErrTrustModalPending)` is true, and `DeliverPrompt`
> was never called.

This is the seam #1014 consumes.

### Data / control flow (end-to-end, happy path)

```
mobile send_message
   └─ msgqueue.drain ─ q.deliver ─ boundSession.WriteUserTurn ─ Supervisor.WriteUserTurn
                                                                     └─ deliverViaSession
                                                                          └─ readyForDelivery(WaitReady)
   claude spawned in untrusted cwd ─ startup trust modal up
      A) producer stream (PRE-BUILT): EventKindPtyModalShown{TrustFolder}
         → TypeModalShown broadcast to interactive conns  ............... AC-1
         → ArmModalTimeout (deny-on-timeout, #725)
      B) delivery: WaitReady ⇒ Readiness{TrustModal:true}
         → readyForDelivery returns ErrTrustModalPending (NO DeliverPrompt)  AC-2
         → drain logs "delivery failed, will retry", sleeps retry=1s, re-peeks SAME head
   remote accept (PRE-BUILT): modal_answer{proceed} on an --allow-remote-permissions device
      → ResolveAnswer: MayAnswerRemotePermission gate → classifyAnswer(proceed)
      → Supervisor.AcceptTrust() ⇒ "1\r" → claude dismisses trust modal, reaches idle
   next drain retry: WaitReady ⇒ Readiness{TrustModal:false}
      → readyForDelivery returns nil → DeliverPrompt delivers the queued turn ..... AC-3
```

The two goroutines never collide unsafely: the delivery goroutine only *reads* the
screen (`WaitReady`/`Snapshot`); the resolution goroutine *writes* a keystroke
(`AcceptTrust`). tui-driver's `Session` already supports concurrent read+write (the
supervisor does `ScreenSnapshot` concurrently with `WriteUserTurn` today).

### Interaction with the give-up bound (why a retryable sentinel, not a park)

Two shapes hold delivery: (A) block inside `deliverViaSession` polling `WaitReady`
until the modal clears; (B) return a retryable sentinel and let the queue's existing
retry loop hold the head. **This ticket takes (B).** Rationale:

- **The split demands it.** Shape (A) never *returns* while pending, so the give-up
  streak (`msgqueue drain.firstFailedAt`) never starts — i.e. (A) would silently
  implement #1014's "give-up-bound exemption," stealing that ticket's scope and
  coupling the two. (B) leaves the give-up bound exactly as-is; #1013 does one thing
  (don't clobber), #1014 adds the typed error **and** the exemption on top of the
  sentinel. My PO note for this split: *AC-2 delivery-clobber ≠ #1014 give-up-exemption.*
- **(B) is a strict improvement even before #1014.** Today the turn is clobbered into
  the modal (consent gate broken, turn wedged). After #1013 the modal stays intact and
  answerable, a remote accept runs the turn, and if nobody answers within the 2-minute
  give-up window the head gives up cleanly (drop + notify + respawn, #1000) rather than
  wedging. #1014 then removes the 2-minute ceiling for legitimately-pending prompts.
- **Accepted temporary limitation (state it, don't fix it here):** with (B) alone, a
  human who takes longer than `giveUpAfter` (2 min) to accept will see the head given
  up. #1013's AC-3 test uses a prompt accept, well inside the window. Removing that
  ceiling is #1014's job — do not touch `giveUpAfter` or `drain`'s give-up branch.

## Concurrency model

No new goroutines, channels, or locks. The change is a pure-function readiness
classifier plus two call-site rewires inside an existing synchronous delivery path.
The delivery goroutine (per-conversation `drain`) and the resolution goroutine (the
v2 manager's `Run` dispatch) are pre-existing and already coordinate through the
`modalbridge.Registry` one-shot `Resolve` (pre-built). `readyForDelivery` touches no
shared mutable state.

## Error handling / failure modes

| Condition at delivery | `WaitReady` returns | `readyForDelivery` | delivery outcome |
|---|---|---|---|
| trust modal up | `Readiness{TrustModal:true}, nil` | `ErrTrustModalPending` | **held**, no `DeliverPrompt`; drain retries (AC-2) |
| idle, no modal | `Readiness{Idle:true}, nil` | `nil` | deliver as today; commit ⇒ nil (AC-3 post-accept) |
| claude busy past ctx | `Readiness{}, ctx cause` | that error | wrapped `wait ready:` → retryable (unchanged) |
| child exited (deny→ESC→exit) | `Readiness{}, *ProcessExitedError` | that error | wrapped `wait ready:` → retryable/respawn (unchanged; #1014 types it) |
| non-trust unexpected modal | `Readiness{}, *UnexpectedModalError` | that error | wrapped `wait ready:` → retryable (unchanged) |
| no live session | (WriteUserTurn short-circuits before deliverFn) | — | `ErrNoLiveSession` (unchanged) |

Fail-closed posture: the *safe default is to not deliver* untrusted queued content
into a consent gate; every ambiguous readiness (error, or trust pending) results in a
non-delivery, never a `DeliverPrompt` and never a false ack.

## Testing strategy

Table-driven, stdlib `testing`, no live claude (drive the seams). AC-1 and the
resolution half of AC-3 need **no new tests** — they are pre-built and already
covered (see below); do not duplicate them.

- **`readyForDelivery` unit test (core of AC-2 + AC-3).** Fake `waitReady`:
  - returns `Readiness{TrustModal:true}, nil` ⇒ expect `ErrTrustModalPending`
    (`errors.Is`), and — the anti-clobber assertion — a `deliver` spy the test wires
    downstream is **never** called.
  - returns `Readiness{Idle:true}, nil` ⇒ expect `nil` (delivery may proceed) — AC-3
    "runs once cleared."
  - returns `Readiness{}, context.DeadlineExceeded` ⇒ expect that error back
    (unwrapped), not the sentinel.
  - returns `Readiness{}, &tuidriver.ProcessExitedError{}` ⇒ expect that error back.
- **Growth-mode hold (AC-2 on the `ResolveTranscript != nil` path).** Extend
  `TestSupervisor_ConfirmViaTranscriptGrowth` with a case whose `waitReady` fake
  returns `ErrTrustModalPending`: assert `confirmViaTranscriptGrowth` returns an error
  satisfying `errors.Is(_, ErrTrustModalPending)` **and** the `deliver` fake's call
  count is 0 (no clobber). Reuse the existing `deliverGrowthDeps` scripting.
- **`WriteUserTurn` boundary contract.** Using the `sup.deliverFn = …` override
  pattern, assert that when `deliverFn` returns `ErrTrustModalPending`,
  `WriteUserTurn` returns an error for which `errors.Is(err, ErrTrustModalPending)`
  holds (the wrap preserves the sentinel for #1014) and the conversation cursor is
  still stamped per the existing #312 contract (unchanged behaviour).
- **AC-1 (already covered — cite, don't rewrite).** `interactive_modal_v2_test.go:125-140`
  already proves `EventKindPtyModalShown{ModalClassTrustFolder}` ⇒ `TypeModalShown`
  with options `{proceed, exit}`. Optionally add a one-line comment pointer; no new
  test required.
- **AC-4 (no-regression guardrails).** Assert by construction + existing suite:
  - the diff touches only `internal/supervisor` (+ its test) — it does **not** import
    `internal/agentrun/trust`, does **not** call `trust.MarkWorkdirTrusted`, and does
    **not** modify `internal/sessions/buildSession`. A cheap explicit guard: keep the
    existing agent-run readiness tests (`ptyrunner` `ErrTrustModalDetected`, #173)
    green — they exercise the *separate* fail-loud path and must be unaffected.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries / consent gate — the core property]** No MUST FIX; this change
  is a net *tightening* of a trust boundary. Folder-trust is the last consent gate for
  a `--dangerously-skip-permissions` interactive session (#988). Before this ticket,
  `deliverViaSession` typed **untrusted, phone-originated queued text** (`head.text`)
  into that gate via `DeliverPrompt` — a queued turn whose first characters happened to
  be an accept keystroke (`"1"…`) could have driven the trust decision, i.e. untrusted
  content answering a consent prompt. This ticket makes the gate **structurally
  unreachable by untrusted content while pending**: `readyForDelivery` returns
  `ErrTrustModalPending` *before* `DeliverPrompt` is ever called, so no queued byte
  reaches the modal. The only paths that can resolve the trust modal remain the
  pre-built authorized ones: a remote `modal_answer` behind the fail-closed
  `Device.MayAnswerRemotePermission` gate (`modal_resolve_v2.go:183`), a local operator
  at the attach TTY, or the fail-closed deny-on-timeout (#725, ESC). No new resolution
  path, no widening of who may accept.

- **[Auto-trust / privilege — AC-4]** No MUST FIX. The change marks **nothing**
  trusted: `readyForDelivery` reads readiness and returns; it holds no `workdir`, calls
  no `trust.MarkWorkdirTrusted`, and does not touch `internal/sessions/buildSession`
  (which continues to spawn without trust-marking). `trust.MarkWorkdirTrusted` stays
  confined to the agent-run / ACP / selfcheck paths (`internal/agentrun/*`,
  `cmd/pyry/agent_run.go`), verified by grep. Agent-run's fail-loud readiness (#173,
  `ptyrunner.ErrTrustModalDetected`) is a *different* code path that does not use
  `deliverViaSession`, so it is untouched.

- **[Tokens, secrets, credentials]** N/A — the delivery gate handles no tokens, keys,
  or credentials. The `answerToken` / device-gating live entirely in the pre-built
  `ResolveAnswer` path, unchanged here.

- **[Injection — untrusted content into a control surface]** Addressed above and
  primary: the untrusted-content→consent-gate crossing that existed via `DeliverPrompt`
  is closed. `ErrTrustModalPending` is a static sentinel; no untrusted data flows into
  it.

- **[Error messages, logs, telemetry]** No MUST FIX. The new sentinel string is
  static ("supervisor: trust modal pending") and content-free. The only log emitted
  during the hold is msgqueue's **pre-existing** `"delivery failed, will retry"`, which
  by its established contract logs `conversation_id` + `queued_msg_id` + the err
  (now the sentinel) and **never** `head.text` (`queue.go:467,471-475`). No new log
  line, no new field, no content leak. (Note: during a pending window this pre-existing
  Warn repeats ~once/retry until accept or give-up — volume, not a leak; #1014's typed
  session error supersedes it.)

- **[File operations / subprocess / crypto]** N/A — no filesystem access, no `exec`,
  no crypto in the changed code. The forwarded `TypeModalShown` and the inbound
  `modal_answer` ride the manager's established Noise_IK sealed transport, unchanged.

- **[DoS / resource bounds — fail-closed]** No MUST FIX. The hold is **bounded**: it
  is realised by the existing 1s-retry / 2-min-give-up drain loop, not a busy-spin and
  not an unbounded park. `WaitReady` returns promptly when the trust modal is up
  (trust modal is an idle state), so each retry is cheap. No new goroutine, channel,
  lock, or unbounded buffer is introduced. The safe default on every ambiguous or
  error readiness is **non-delivery** (never a `DeliverPrompt`, never a false ack) —
  fail-closed.

- **[Concurrency]** No MUST FIX. `readyForDelivery` is a pure function over its
  `waitReady` argument and touches no shared state. The read-side (`WaitReady`) and the
  resolution-side keystroke (`AcceptTrust`) already run concurrently on the live
  `Session` under tui-driver's existing concurrent read+write support (same posture as
  today's concurrent `ScreenSnapshot` + `WriteUserTurn`). No lock is taken, so no
  lock-ordering concern is added.

## Open questions

- **AC-1 coverage depends on the modal producer following the active conversation.**
  `runModalStream` follows `active`/bound sessions (`resolveTarget`, #678). If a
  conversation with a startup trust modal is *not* the active/bound target the stream
  is watching, its `TypeModalShown` would not be forwarded — a pre-existing property of
  the producer, not introduced or worsened here. Even in that case AC-2 still holds
  (delivery is held, never clobbered) and the deny-on-timeout still safe-denies. If
  broader coverage is ever required it is a producer-side follow-up, not this ticket.
  Flagged for awareness; no action in #1013.
- Retry cadence during a pending window is the msgqueue default (1s). If #1014 finds
  the retry-log volume noisy, the typed-session-error path there is the place to quiet
  it — not the delivery gate.
