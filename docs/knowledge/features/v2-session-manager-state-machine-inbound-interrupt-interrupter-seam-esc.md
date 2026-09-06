# Inbound interrupt (#707, widened #2103) — `Interrupter` seam + Esc routing

`interrupt` is a v2 **control** envelope (phone → binary), intercepted in
`dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (the same
boundary `rekey_request` / `request_snapshot` / `modal_cancel` use) — there is
**no** `dispatch.Route` handler. It is the **remote interrupt**: a paired phone's
equivalent of pressing **Esc** at the local terminal. The daemon maps it to the
internal neutral `turnevent.Cancel` command and routes it to the supervised claude
as a single Esc — claude's own interrupt. **`security-sensitive`**: it is the first
inbound frame whose authorization *is* the `interactive` capability (spec-stage
security review, verdict PASS). See [`codebase/707.md`](../codebase/707.md).

**Since #2103** it carries an optional `InterruptPayload{ConversationID string}` —
the identical shape [#2099 gave `new_session`](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md):
present and canonical, the daemon stops **that** conversation's turn and touches
no other, including whichever one the cursor points at; absent, empty, or a body
that fails to decode are one wire meaning — stop the conversation the daemon's
cursor points at, the pre-#2103 behaviour verbatim, so an un-upgraded client keeps
working. There is still no `modal_id`-style nonce, `answer_token`, or idempotency
key (unlike modals): a replayed `interrupt` simply sends another Esc, and an Esc
with no running turn is a no-op in claude.

- **`Interrupter` consumer seam.** The relay declares the one-method interface,
  **since #2103** `Interrupter interface{ SendEsc(conversationID string) error }`
  (beside `ScreenSnapshotter` / `ModalResolver`), and reaches the keystroke surface
  through it, so `internal/relay` imports neither `internal/supervisor` nor
  `internal/streamsup` nor tui-driver — and, because of the widened signature,
  neither `internal/conversations` nor `internal/sessions` either, so it cannot
  shape-check or resolve the id itself; that obligation lives in the interface's
  doc block, the same posture `SessionStarter` already carries. **Since #1121** the
  `V2SessionConfig.Interrupter` field is wired not to the bootstrap supervisor
  directly but to a `cmd/pyry`-side adapter,
  `activeInterrupter`, that resolves the **active conversation's bound runner**
  (`active.CurrentConversation()` → `CurrentSessionID` → `Pool.Lookup` →
  `sess.Runner()`, mirroring the follow-active `boundHost` resolution used by the
  turn/modal streams — see [conversation-session-binding.md](conversation-session-binding.md))
  and dispatches by concrete runner type: `*supervisor.Supervisor` via the sealed
  `SendEsc` (#726), `streamRunner` (the stream-json adapter) via `Interrupt`
  (#1120). `activeInterrupter.SendEsc()` keeps the seam's original method name even
  though the actuation is a per-runner interrupt, not literally an Esc — this
  interface doc already abstracted `SendEsc` as "claude's own interrupt," so
  `internal/relay` and this seam's own tests needed zero changes. Before #1121 the
  field was wired with one line, `Interrupter: sup` (the bootstrap supervisor) —
  a latent mis-routing bug: since #678 routed turns to per-conversation bound
  runners, an interrupt from a phone actuated the idle bootstrap child instead of
  the runner actually running the active conversation's turn. See
  [codebase/1121.md](../codebase/1121.md). The stream-json arm of this dispatch
  (`streamRunner.Interrupt`, #1120) was unit-proven at #1121 but had never run live
  against a **minted, non-bootstrap** conversation until the #1136 e2e
  (`relay_v2_stream_interrupt_test.go`), which confirms the interrupt reaches that
  conversation's own runner and not the bootstrap — see
  [codebase/1136.md](../codebase/1136.md). The PTY arm (`*supervisor.Supervisor.SendEsc`,
  #726) had the same gap: every green PTY-tier interrupt test drove the bootstrap
  session until the #1191 e2e (`relay_v2_perconv_interrupt_test.go`), which closes it the
  same way — minted topology, on-disk transcript pair as the mis-route detector — and
  additionally proves non-vacuity by mutating the production wiring closure
  (`cmd/pyry/main.go:1017`) back to its pre-#1121 form. See
  [codebase/1191.md](../codebase/1191.md).
- **`handleInterrupt(s, env)`** — the only new logic (**since #2103** the
  dispatcher passes the envelope; before that it was `handleInterrupt(s)` with
  nothing to decode). Runs on the manager's **single Run dispatch goroutine**, so
  the `s.interactive` read is lock-free under the package's single-owner
  invariant — still no `ctx`: no cancellable work, no reply, no broadcast
  (fire-and-forget). Order is load-bearing — **capability gate before decode**,
  so a non-interactive conn's bytes are never parsed
  (`TestV2Session_Interrupt_NonInteractiveNeverDecodes`, #2103):
  1. **`if !s.interactive` → return** (no Esc). The new inbound capability gate
     (AC-2 negative path). A **one-line check, NOT a reusable inbound-gate
     abstraction** — `interrupt` is its only consumer (dequeue is ungated,
     `modal_answer` uses the per-device gate #702, `modal_cancel` a nonce), so a
     shared gate would be a one-consumer abstraction (YAGNI). The `s.interactive`
     flag is server-authoritative (#626) — set fail-closed from the daemon's
     `negotiateCapabilities`, never from the phone's raw advertisement, so a spoofed
     `capabilities` advertisement can never flip it. **Since #1192** this arm also
     `Info`-logs `v2.interrupt.non_interactive` (`conn_id` only — never
     `s.peerStatic` or `s.device`), because it was previously indistinguishable in
     the daemon log from three other silent faults (frame never arrived, route
     inert, keystroke ignored) — see below.
  2. **`if m.cfg.Interrupter == nil`** → debug-log `v2.interrupt.inert`, return
     (foreground / pre-wire; mirrors `handleModalCancel`'s nil-resolver guard).
  3. **Tolerant decode, then `m.cfg.Interrupter.SendEsc(p.ConversationID)`**
     (**since #2103**) — `_ = json.Unmarshal(env.Payload, &p)`; a decode failure
     leaves the zero value, whose empty `ConversationID` *is* the cursor path, so a
     malformed or absent body degrades to the pre-#2103 behaviour rather than
     dropping the frame. The decode error and the payload bytes are never echoed to
     the client and never logged here — `encoding/json` quotes attacker bytes into
     its error string, and `p.ConversationID` is itself unbounded until the seam's
     shape check has run. The actuation is best-effort: an error (no live session /
     teardown) is `Warn`-logged (`v2.interrupt.keystroke_err`, `conn_id` + the
     error only — never the conversation id, never payload bytes) and tolerated;
     nothing to roll back.

`Cancel` is **declared vocabulary, not the live path** (the `modal_cancel`
precedent): the mobile `interrupt` routes to Esc directly via this seam, it does
**not** construct a `turnevent.Cancel` value — that mobile-wire → neutral-`Cancel`
translation is the future ACP adapter's job (#600). **Multi-phone:** any interactive
paired phone can send `interrupt`, and (since #1121) it actuates the **active
conversation's** bound runner rather than whichever child happens to be the
bootstrap supervisor — consistent with the broadcast fan-out model (a user's paired
devices are one trust domain, and there is one daemon-global `active` conversation).
**Residual scope, updated by #2103:** the bare frame's isolation boundary is still
"active conversation," not "sending conn's own conversation" — a bare `interrupt`
from conn A still stops whatever the global cursor points at, which may not be A's
own conversation (#1121's non-regression argument still holds for that path
unchanged). Naming a conversation removes that ambiguity for any client that
upgrades: it moves *which* conversation one frame can reach from "the cursor's" to
"any registered one," not the trust boundary — a hostile-but-paired device could
already reach any conversation with a two-frame dance (route a `send_message` to
move the shared cursor, then send the bare frame), so the field only removes a
dance a *benign* client was previously unable to perform. See
[ADR 025 § Security model](../decisions/025-mobile-remote-head-interactive-session.md#security-model--remote-permission-granting-default-safe)
and the identical argument in
[new_session § Multi-phone](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#multi-phone--scoping).

**Observability (#1192, #1193).** Before #1192 the whole route was silent on
success and on almost every failure, so a live daemon log showing nothing after
an inbound `interrupt` frame could not distinguish "the frame never arrived"
from "the conn was not interactive," "the route went inert," or "the keystroke
actuated and claude ignored it" — a real live-daemon incident
(`real-claude-interrupt`, pyrycode-desktop#483) burned 2m10s on exactly this
ambiguity. #1192 instruments the three arms reachable without a signature
change, each at `Info` (the daemon's default level) so the record is visible
without `-pyry-verbose`: `handleInterrupt` step 1 above
(`v2.interrupt.non_interactive`, `conn_id`), and two arms inside
`activeInterrupter.SendEsc()` — `v2.interrupt.no_active_conv` (no fields; its
existence is the information) when `currentConv()` is `""`, and
`v2.interrupt.no_bound_runner` (`conversation_id`) when `resolveRunner` returns
`!ok`. The records identify the **conversation**, not the session:
`resolveBoundRunner` never surfaces the bound `CurrentSessionID` to its caller,
and widening that signature to do so was ruled out as disproportionate to the
diagnostic gained.

**#1193 closes the invariant.** The remaining two arms lived behind
`interruptRunner` (`cmd/pyry/main.go`) — reaching them needed a signature
change, which is why they were split into their own ticket. At the time,
`interruptRunner` dispatched to `streamRunner.Interrupt()` or
`*supervisor.Supervisor.SendEsc()`; `#1348` deleted `internal/supervisor`
outright, leaving the `SendEsc` arm type-dead, and `#1548` deleted it along with
the `armSendEsc` constant — `interruptRunner` is now an optional
`if`-assertion with a single surviving actuation, `streamRunner.Interrupt()`.
`interruptRunner` still returns the `interruptArm` it dispatched to
(`armInterrupt` / `armNone`) alongside the chosen method's error, and
`activeInterrupter.SendEsc()` — the only scope holding the conversation id —
emits from it: `v2.interrupt.dispatched` (`conversation_id`, `arm`) on a
successful dispatch, unconditionally including when the dispatched arm's own
call returned an error (it records *which arm ran*, not that the child
quiesced — the actuation failure itself is `handleInterrupt`'s
`v2.interrupt.keystroke_err` `Warn`, which carries the error but not the arm);
`v2.interrupt.no_actuator` (`conversation_id`) when the resolved runner exposes
neither method. Every path through the route now emits, so **an empty
`v2.interrupt.*` log on a wired daemon means the frame never arrived** — no
separate "frame arrived" record was needed to get that. Two doc comments that
carried this as an explicit interim caveat (`activeInterrupter.SendEsc`,
`cmd/pyry/main.go`; `handleInterrupt`, `internal/relay/v2session_modal.go`) were
flipped from caveat to invariant. "Wired" is load-bearing: `handleInterrupt`'s
step-2 nil-`Interrupter` arm still records at `Debug` (invisible at the
daemon's default `LevelInfo`, raised only by `-pyry-verbose`), but production
always wires the `Interrupter`. See [`codebase/1192.md`](../codebase/1192.md),
[`codebase/1193.md`](../codebase/1193.md).

## `activeInterrupter.SendEsc` — resolution order (#2103)

Named id resolution slots in before the existing arms, in an order that mirrors
`activeSessionStarter.StartNewSession` deliberately:

1. `conversationID == ""` → fall back to `currentConv()`; still `""` →
   `v2.interrupt.no_active_conv`, inert. **The empty-string branch runs before
   `conversations.ValidID`** — `ValidID("")` is false, so reversing the two would
   refuse every un-upgraded client's bare frame. The cursor's own id is
   daemon-authored and deliberately not shape-checked, since checking it would
   change behaviour on the one path this branch exists to preserve.
2. named but `!conversations.ValidID` → `v2.interrupt.invalid_conv_id` with the id
   run through `boundedConvID` (reused from #2099's `cmd/pyry/main.go` helper, not
   re-invented) — the one arm an arbitrary client-chosen string reaches a log call.
3. `resolveRunner(convID)` fails → `v2.interrupt.no_bound_runner`. **Unknown id and
   known-but-unbound id share this one record**, because `resolveBoundRunner`
   refuses both identically and is left untouched: its `conv.CurrentSessionID ==
   ""` guard is the #678 isolation point that stops a fall-through to
   `Pool.Lookup("")`'s bootstrap session, so AC-3's "never the bootstrap session"
   is preserved by not touching that function rather than by rebuilding it. The
   non-distinction also denies a paired client an existence oracle over
   conversation ids.
4. dispatch as before — `v2.interrupt.no_actuator` / `v2.interrupt.dispatched`.

## No liveness guard — a blocker's late fix is not automatically the twin's requirement

[`new_session`'s package overview](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#a-capability-probe-answers-could-not-is--the-arm-that-shipped-broken)
asked #2103 to settle, once for both frames, whether a named-but-childless
conversation needs the same `State().ChildPID == 0` guard #2099 needed after
shipping without it. **For `interrupt` the answer is no, and no asymmetry
results.** `new_session`'s guard exists because `RestartFresh` on a childless
runner is observable damage with nothing to show for it — it rekeys the pool,
persists `sessions.json`, rebinds the conversation and broadcasts a
`session_transition` for a chat that never had a turn. `interrupt`'s actuation has
no such hazard: `streamRunner.Interrupt()` → `(*streamsup.Runner).Interrupt()` →
`WriteInterrupt(r.Stdin(), …)`, and `WriteInterrupt` checks its writer for `nil`
**first**, returning `ErrNoLiveChild` having written nothing and mutated nothing.
A named conversation with no live child is therefore already inert by
construction, taking the same arm the bare path takes today — the error
propagates to `handleInterrupt`'s tolerated `keystroke_err` warn.
`TestActiveInterrupter_NoLiveChildIsAttemptedNotGuarded` pins this: the actuation
is *attempted*, not refused, on both the named and bare rows, and is the
regression pin against a later reader "fixing" this by copying the twin's probe —
doing so would add a refusal the bare path does not have today, which is exactly
what AC-2 (bare frame stays byte-identical) exists to prevent.

**Generalizes:** before copying a guard from a sibling ticket, trace the sibling's
actuation to its own write site. A guard earns its keep only when the actuation it
protects can leave the world worse off with nothing to show for it; an actuation
that refuses cleanly without writing anything already has no such failure mode.

## Log level: Info, not `new_session`'s Debug

`new_session`'s refusal arms all log at `Debug`, because the id is client-supplied
and that package's posture treats a client string as not worth an Info record.
`interrupt`'s new refusal arm (`v2.interrupt.invalid_conv_id`) instead follows the
seam it joins: every existing `activeInterrupter` arm is `Info` by the explicit
\#1192/#1193 decision above, whose stated contract is that a wired daemon leaving no
`v2.interrupt.*` record means the frame never arrived. A `Debug` refusal here would
break that closure at the daemon's default `LevelInfo`. The cost — a hostile paired
client can drive Info-level records carrying up to 64 bytes of content it chose —
is bounded by the same `boundedConvID` copy-and-truncate discipline `new_session`
uses, not by lowering the level.

## Related

- [`new_session`'s package overview](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md)
  — the identical optional-payload shape, tolerant-decode posture, and the
  cursor-vs-named question this doc answers for `interrupt`.
- [Draining turnevents into the interactive emitter § per-event session
  gate](streamsup-package-draining-turnevents-into-the-interactive-emitter.md)
  — why a background conversation's `turn_end` never reaches the wire while the
  cursor points elsewhere, the fact #2103's e2e is built around: with the cursor on
  A and the frame naming B, B's `TurnEnd` is dropped by this gate before it is ever
  shaped into an envelope, so the e2e proves the interrupt reached B from the
  `v2.interrupt.dispatched` record and the drain's own `stream_turn.not_active`
  drop record instead of a `turn_end` that structurally cannot arrive. Worth
  checking, before writing a test against an acceptance criterion, whether the
  observable it names actually survives to the surface the criterion watches.
