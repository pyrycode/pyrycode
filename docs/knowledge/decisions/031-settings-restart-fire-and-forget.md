# ADR 031: Live-restart on settings change is fire-and-forget, modeled as an induced child-exit

## Status

Accepted (ticket #842).

## Context

#845 persists a `set_session_settings` change via `Pool.UpdateSettings`, but the change only reached claude on the session's *next* spawn — YOLO and reasoning-effort have no live actuator, so a running session kept its stale model/effort/YOLO until it happened to restart for an unrelated reason (crash, eviction). #842 closes that gap: a persisted change must take effect on the *currently running* session, resuming the conversation.

Two questions shaped the design:

1. **How does a still-running supervisor pick up new spawn argv without a rebuild?** The supervisor bakes `Config.ClaudeArgs` at construction and has no seam to swap them.
2. **What does `UpdateSettings` do about a relaunch that fails?** The ticket's AC #4 explicitly forbids the state "settings on disk, stale child still running, success reply sent" — persist-then-restart needs a defined failure contract.

## Decision

**The restart is fire-and-forget and non-blocking.** `Pool.UpdateSettings` calls a new `Supervisor.Restart(args []string)` after releasing `Pool.mu`; `Restart` swaps the live spawn args under a leaf `restartMu` and, if a child is running, sends a coalescing hint on a buffered channel and cancels a *per-iteration* derived ctx (not the supervisor's own ctx). It returns immediately — it does not wait for the old child to die or the new one to come up.

**The restart is modeled as an induced child-exit**, not a distinct code path. `Supervisor.Run` already has a forever-retry loop for crash recovery; the only change is (a) reading spawn args from the live, mutex-guarded field instead of the immutable config field, and (b) running each iteration on a ctx derived from the parent so a restart-kill can be told apart from a real shutdown (`ctx.Err() != nil` after `runOnce` returns). A deliberate restart skips the backoff delay a crash would incur.

**AC #4 is satisfied by "recover to a defined state," not "block for confirmation."** The stale child is deterministically killed (SIGKILL via ctx cancel, the same mechanism shutdown already uses); the supervisor's existing forever-retry guarantees it comes back; the success reply is honest because the moment it's sent, the old (wrong-settings) process is already gone or already being replaced. The window between "the reply says success" and "the new child has actually finished spawning" is not closed synchronously.

## Rationale

**Why not block `UpdateSettings` until the relaunch completes?**

- #845's handler runs synchronously on the v2 manager's single dispatch goroutine. Blocking it for a kill + respawn (which can take the crash-recovery path's full backoff-free but still real-world spawn latency) would stall that connection's entire frame processing — other verbs on the same phone connection queue behind one settings change.
- A synchronous wait needs the caller's ctx threaded through the `SettingsUpdater` interface that #845 already shipped across `internal/relay` → `cmd/pyry` → `internal/sessions` — a cross-package signature change to an interface two sibling tickets already depend on, for a property (confirmed-relaunched) the client doesn't actually need: it needs "my change took effect," which persisting + killing the stale process already guarantees semantically.
- The supervisor's crash-recovery loop is already the thing a client implicitly trusts when a spontaneous crash happens mid-session — reusing it for a deliberate restart doesn't introduce a new reliability contract, it reuses an existing one.

**Why model it as an induced child-exit instead of a dedicated "rebuild the supervisor" path?**

- A rebuild would mean constructing a second `Supervisor`, migrating `WriteUserTurn`'s cursor state and the bridge, and re-wiring whatever owns the `Run` goroutine — touching the `Session` active/evicted state machine this ticket was scoped to leave alone (`runActive` reacts only when `Run` *returns*; an induced-exit-and-relaunch never returns, so the state machine doesn't even see it happen).
- The existing loop already contains every mechanism a restart needs: kill-and-relaunch, resume-via-`--continue`/`--session-id`, and retry-on-failure. Swapping argv and inducing one exit is strictly less code and less new surface than parallel machinery that duplicates it.

**Why coalesce rather than queue multiple restarts?**

A buffered-1 channel plus "last write wins" on `claudeArgs` is correct for the actual use case (a client changes settings, possibly rapidly): each `Restart` call always writes the newest args, and at most one pending relaunch is needed to converge on them — queuing every request would relaunch the child once per request even when only the last set of args matters, adding kill/resume churn with no observable benefit.

## Consequences

**Going forward:**

- A relaunch failure after a settings change is invisible to the calling client in the same request — it surfaces the way any spontaneous crash-and-retry does today (via the supervisor's own logging/backoff state), not as a rejected `set_session_settings` reply. If a future consumer needs "confirm the new child is actually up," that is a new capability (e.g. polling `Supervisor.State()` after the call), not a change to this contract.
- Any future live-mutation of a running session's spawn behavior (not just settings) can reuse the same `Restart(args)` seam rather than inventing another swap mechanism.
- `State().RestartCount` intentionally still counts only crash-restarts; a settings-restart does not increment it. A distinct counter is a follow-up if operators want it surfaced (not filed as a ticket by this pass).

**Trade-offs accepted:**

- There is an inherent, irreducible window between "settings persisted + stale child killed" and "new child fully up" — a running process cannot be un-bypassed (YOLO revoke) or re-modeled (model/effort) in place; killing it is the only way to guarantee its replacement reflects the new settings, and killing is not instantaneous.
- A kill can drop an uncommitted assistant turn mid-response, identical to what a crash does. This is the ticket's own explicit "kill + resume" contract, not a regression introduced by the fire-and-forget choice.
- A minted session's induced restart relaunches with `--session-id <id>` (no `--continue`) — the same argv shape a spontaneous crash-recovery respawn already uses for minted sessions. Whether `claude --session-id <existing-id>` resumes rather than forks/errors on a second spawn was flagged as an open question in the architecture spec and inherited, not newly introduced, by this decision.

## Related

- [features/sessions-package.md](../features/sessions-package.md) § `Pool.UpdateSettings` / `Session.spawnArgs` — the persist-then-restart call site.
- [architecture/system-overview.md](../architecture/system-overview.md) § `supervisor.Supervisor` — `Restart`, the live-args seam, and the per-iteration ctx.
- [codebase/842.md](../codebase/842.md) — full implementation writeup.
- [codebase/840.md](../codebase/840.md) — `Pool.UpdateSettings`'s original persist-only tail this ticket extends.
- [codebase/845.md](../codebase/845.md) — the `SettingsUpdater` interface / handler this ticket does not touch.
- `docs/specs/architecture/842-live-restart-on-settings-change.md` — full architecture spec, including the § Security review (verdict PASS) covering the YOLO fail-safe across the restart.
- [ADR 030](030-plain-bool-failsafe-persisted-flag.md) — the YOLO fail-safe this ticket's restart must preserve across a relaunch.
