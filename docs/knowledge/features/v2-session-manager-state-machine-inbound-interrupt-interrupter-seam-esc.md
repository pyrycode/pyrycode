# Inbound interrupt (#707) — `Interrupter` seam + Esc routing

`interrupt` is a v2 **control** envelope (phone → binary), intercepted in
`dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (the same
boundary `rekey_request` / `request_snapshot` / `modal_cancel` use) — there is
**no** `dispatch.Route` handler. It is the **remote interrupt**: a paired phone's
equivalent of pressing **Esc** at the local terminal. The daemon maps it to the
internal neutral `turnevent.Cancel` command and routes it to the supervised claude
as a single Esc — claude's own interrupt. **`security-sensitive`**: it is the first
inbound frame whose authorization *is* the `interactive` capability (spec-stage
security review, verdict PASS). See [`codebase/707.md`](../codebase/707.md).

The frame carries **no payload** — a bare control frame, with no `conversation_id`,
no `modal_id` nonce, no `answer_token`, and no idempotency key (unlike modals). A
replayed `interrupt` simply sends another Esc (an Esc with no running turn is a
no-op in claude), so no nonce / dedup is needed.

- **`Interrupter` consumer seam.** The relay declares the one-method interface
  `Interrupter interface{ SendEsc() error }` (beside `ScreenSnapshotter` /
  `ModalResolver`) and reaches the keystroke surface through it, so `internal/relay`
  imports neither `internal/supervisor` nor `internal/streamsup` nor tui-driver.
  **Since #1121** the `V2SessionConfig.Interrupter` field is wired not to the
  bootstrap supervisor directly but to a `cmd/pyry`-side adapter,
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
- **`handleInterrupt(s)`** — the only new logic. Runs on the manager's **single Run
  dispatch goroutine**, so the `s.interactive` read is lock-free under the package's
  single-owner invariant. The signature takes **only `s`** (no `ctx`, no `env`) — a
  documented deviation from the `(ctx, s, env)` sibling handlers: nothing to decode,
  no cancellable work, no reply, no broadcast (fire-and-forget). Order is
  load-bearing — **capability gate first**:
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
  3. **`m.cfg.Interrupter.SendEsc()`** — best-effort. An error (no live session /
     teardown) is `Warn`-logged (`v2.interrupt.keystroke_err`, `conn_id` + the
     supervisor sentinel only — never payload bytes, there are none) and tolerated;
     nothing to roll back.

`Cancel` is **declared vocabulary, not the live path** (the `modal_cancel`
precedent): the mobile `interrupt` routes to Esc directly via this seam, it does
**not** construct a `turnevent.Cancel` value — that mobile-wire → neutral-`Cancel`
translation is the future ACP adapter's job (#600). **Multi-phone:** any interactive
paired phone can send `interrupt`, and (since #1121) it actuates the **active
conversation's** bound runner rather than whichever child happens to be the
bootstrap supervisor — consistent with the broadcast fan-out model (a user's paired
devices are one trust domain, and there is one daemon-global `active` conversation).
**Residual scope:** the isolation boundary is still "active conversation," not
"sending conn's own conversation" — routing conn A's interrupt strictly to A's
conversation regardless of the global active is a larger per-connection isolation
design, not built here (#1121's spec flags this explicitly and rules it a
non-regression: today's pre-#1121 code routed every interrupt to bootstrap
regardless of conn, so #1121 strictly improves isolation without introducing a new
cross-conversation leak).

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
