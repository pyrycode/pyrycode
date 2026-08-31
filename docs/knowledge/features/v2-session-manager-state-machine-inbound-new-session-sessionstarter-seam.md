# Inbound new_session (#831) — `SessionStarter` seam + `/clear` routing

`new_session` is a v2 **control** envelope (phone → binary), intercepted in
`dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (beside
`TypeInterrupt`) — there is **no** `dispatch.Route` handler. It is the **remote
start-new-session**: a paired phone's equivalent of typing **`/clear`** at the
local terminal. The daemon routes it directly to the supervised claude as a
`/clear` via the sealed `supervisor.StartNewSession` seam (#830) — split from #824, and structurally the `interrupt` (#707) shape one verb over.
**`security-sensitive`**: it reuses the `interactive`-capability-is-the-
authorization posture #707 established (spec-stage security review, verdict
PASS). See [`codebase/831.md`](../codebase/831.md).

The frame carries **no payload** — a bare control frame, with no
`conversation_id`, no `modal_id` nonce, no `answer_token`, and no idempotency
key (same shape as `interrupt` / `request_debug_bundle`). A replayed
`new_session` simply drives another `/clear` (harmless), so no nonce / dedup is
needed. **Unlike `interrupt` it maps to no neutral `turnevent` command** — there
is no announced ACP counterpart requiring one; a future ACP `session/new` would
add that translation additively, out of scope here.

- **`SessionStarter` consumer seam.** The relay declares the one-method
  interface `SessionStarter interface{ StartNewSession() error }` (beside
  `Interrupter`) and reaches the keystroke surface through it, so
  `internal/relay` imports neither `internal/supervisor` nor tui-driver.
  `*supervisor.Supervisor` satisfies it via the **sealed `StartNewSession`**
  (#830, shipped unwired for exactly this consumer) — **zero new supervisor
  code**. **Since #1125** the `V2SessionConfig.SessionStarter` field is wired not
  to the bootstrap supervisor directly but to a `cmd/pyry`-side adapter,
  `activeSessionStarter` — the `new_session` routing twin of `activeInterrupter`
  (#1121, above). It resolves the **active conversation's bound runner**
  (`active.CurrentConversation()` → `CurrentSessionID` → `Pool.Lookup` →
  `sess.Runner()`, via the new sibling helper `resolveBoundSession`) and
  dispatches by concrete runner type through `startFreshRunner`, matched
  `RestartFresh` first:
  - **`streamRunner` (bound to a `*streamsup.Runner`)** — the new **direct**
    path. `Pool.RotateForNewSession(oldID)` mints a fresh daemon-minted id,
    re-keys the pool entry, and registers it in the allocated skip-set *before*
    `runner.RestartFresh(newID)` spawns `claude --session-id <newID>` (§
    [sessions-package.md](sessions-package.md), § [rotation-watcher.md](rotation-watcher.md)
    — ordering is load-bearing, else the fsnotify watcher double-rotates).
    **No `/clear` keystroke is sent.**
  - **`*supervisor.Supervisor` (PTY-bound)** — unchanged: `StartNewSession()`
    types `/clear` and the rotation watcher drives `Pool.RotateID` on the
    resulting self-rotation, exactly as before #1125. Kept for zero-regression
    parity with a PTY-bound conversation (today's only reachable case is a
    remote conversation bound to a stream-json runner; the PTY arm is a
    documented open question, not yet retired).
  - **default (unknown runner)** — inert, no actuation.

  Before #1125 the field was wired with one line, `SessionStarter: sup` (the
  bootstrap supervisor) regardless of which conversation the client was
  actually in — the same latent bootstrap mis-route #1121 fixed for
  `interrupt`, plus an **indirect** rotation (the pool-side id flip happened
  only when the watcher later observed claude's self-rotation). See
  [codebase/1125.md](../codebase/1125.md).
- **`handleNewSession(s)`** — the only new logic, a line-for-line mirror of
  `handleInterrupt`. Runs on the manager's **single Run dispatch goroutine**, so
  the `s.interactive` read is lock-free under the package's single-owner
  invariant. The signature takes **only `s`** (no `ctx`, no `env`) — the same
  documented deviation `handleInterrupt` established: nothing to decode, no
  cancellable work, no reply, no broadcast (fire-and-forget). Order is
  load-bearing — **capability gate first**:
  1. **`if !s.interactive` → return** (no `/clear`). The inbound capability gate
     (AC #4 negative path). A **one-line check, NOT a reusable abstraction** —
     `new_session` / `interrupt` / `dequeue_message` each keep their own bare
     check (CODING-STYLE over-DRY). `s.interactive` is server-authoritative
     (#626) — set fail-closed from the daemon's `negotiateCapabilities`, never
     from the phone's raw advertisement.
  2. **`if m.cfg.SessionStarter == nil`** → debug-log `v2.new_session.inert`,
     return (foreground / pre-wire; mirrors `handleInterrupt`'s nil-`Interrupter`
     guard). AC #5.
  3. **`m.cfg.SessionStarter.StartNewSession()`** — best-effort. An error (no
     live session / mid-teardown → `ErrNoLiveSession`) is `Warn`-logged
     (`v2.new_session.keystroke_err`, `conn_id` + the supervisor sentinel only —
     never payload bytes or the rendered screen) and tolerated; nothing to roll
     back, no reply owed. AC #5.

**No new emitter, no ack path.** The client observes the resulting break
through the **pre-existing** `session_transition` marker: when `/clear` rotates
claude's session UUID, the rotation watcher fires `notifyTransition(ReasonClear)`
and the existing #656/#657 emitter fans `reason: "clear"` to every interactive
conn — this ticket builds neither, exactly as `interrupt` is fire-and-forget.
**Since #1125**, on the stream arm the same `ReasonClear` fire instead comes
**directly** from `Pool.RotateForNewSession` (no watcher round-trip) — the wire
observable is unchanged, only the trigger moved from indirect (observe claude's
self-rotation) to direct (daemon drives the rotation itself).
**Multi-phone / scoping:** any interactive paired phone can trigger
`new_session`, and (since #1125) it actuates the runner bound to the **active
conversation** rather than unconditionally the bootstrap supervisor — the same
scoping `interrupt` gained under #1121, and the same **residual** scope: routed
to "active conversation," not "the sending conn's own conversation." This is
consistent with the broadcast fan-out model (a user's paired devices are one
trust domain, one daemon-global `active` conversation) and is a strict
isolation improvement over pre-#1125 (which routed every `new_session` to the
shared bootstrap regardless of conn). Per-connection isolation, if ever needed,
is a larger design not built here.
