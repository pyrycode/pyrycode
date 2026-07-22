# 033. Supervisor self-heal uses a dedicated rotation primitive, not `RotateForNewSession`, and skips the skip-set

## Status

Accepted (#1165)

## Context

The bootstrap supervisor's backoff loop retries forever on a deterministic
fast-crashing child (e.g. #1163's "Session ID already in use" collision,
root-fixed in the sibling ticket #1164). #1165 adds a generic backstop: after
`FastCrashThreshold` consecutive non-zero exits within `FastCrashWindow` of
spawn, the supervisor signals the pool to mint a fresh pinned bootstrap id and
retry on that instead of looping forever on the wedged one.

The pool already had a mint-and-rekey primitive for a very similar case:
`Pool.RotateForNewSession` (#1125), the daemon-driven analog used by the
`new_session` control verb. It mints via `NewID()`, re-keys under `Pool.mu`
via the shared `rekeyLocked` helper, persists, and additionally:

1. registers the new id in the allocated-UUID skip-set
   (`registerAllocatedUUIDLocked`), and
2. fires a `SessionTransition{Reason: ReasonClear}` to notify clients and
   rebind the conversation.

The question: should self-heal call `RotateForNewSession` directly, or does
it need its own primitive?

## Decision

Added a new pool method, `Pool.RotateBootstrapForSelfHeal`, that reuses
`rekeyLocked` + `saveLocked` (the same shared seam) but does **neither** of
`RotateForNewSession`'s two extras:

- **No skip-set registration.** `RotateForNewSession`'s skip-set entry exists
  because *that* caller is about to spawn `claude --session-id <newID>`
  itself, and the rotation watcher's `handleCreate` needs to know the
  upcoming `<newID>.jsonl` CREATE is expected, not a live self-rotation.
  Self-heal doesn't need this: the rekey commits `p.bootstrap → newID`
  *before* the next spawn creates `<newID>.jsonl`, so by the time the
  watcher's `handleCreate` fires, `Snapshot()` already reports `{ID: newID}`
  and the existing `ref.ID == stem` guard (`internal/sessions/rotation/watcher.go`)
  returns early on its own. This is structurally identical to cold start,
  which likewise never pre-registers the bootstrap id in the skip-set. Adding
  an entry here would defend a probe-timing race the guard already covers —
  declined per Evidence-Based Fix Selection (no observed gap).
- **No client transition.** `ReasonClear` is the wire signal for "the user
  ran `/clear`." A self-heal rotation is a supervisor-internal crash-recovery
  action, not a user action; firing `ReasonClear` would misrepresent it to
  any client tracking the bootstrap id. Client notification of a self-heal is
  explicitly out of scope for #1165 (see Consequences).

The supervisor signals the rotation through a new `Config.SelfHeal func() error`
callback — the same pull-not-push shape as `Config.ResolveSessionID` (#839):
the supervisor never learns `SessionID`, and the rotated id is picked up
automatically on the next spawn via the existing `ResolveSessionID` pull. Two
new `Config` fields, `FastCrashWindow` (default 2s) and `FastCrashThreshold`
(default 4), gate when the callback fires and are documented at their
declaration and at the `Run`-loop accounting site (not buried in a helper).

## Rationale

- **Same fabric, different intent.** `rekeyLocked`/`saveLocked` is the right
  shared seam (three rotation call sites now: the fsnotify watcher's
  `onRotate`, `RotateForNewSession`, and this). But the two *behaviors*
  layered on top of the rekey — skip-set registration and client
  notification — are specific to "the daemon is about to spawn on this id
  itself, on a user-initiated new-session request." Self-heal is neither
  user-initiated nor does it need the daemon to pre-announce the id (the
  guard already handles it). Bolting self-heal onto `RotateForNewSession`
  would mean either accepting a semantically wrong `ReasonClear` event or
  adding a boolean parameter to suppress it case-by-case — a dedicated method
  with its own doc comment is clearer than a flag that silently changes
  observable behavior.
- **Belt-and-suspenders, different fabric.** #1164 is a deterministic
  *startup* resolution (resume when a transcript exists). #1165 is a
  deterministic *runtime* backstop (rotate after N fast crashes regardless of
  cause). Reusing `RotateForNewSession` would couple the backstop's behavior
  to a primitive whose contract (skip-set + client notify) was designed for
  an unrelated caller; a dedicated primitive keeps the two fixes decoupled.
- **Config fields, not consts, for the thresholds.** Zero-value defaults
  (mirroring `BackoffInitial`/`Max`/`Reset`) keep every existing
  `supervisor.Config{...}` literal byte-identical (additive-only), and let
  tests set a small threshold/window instead of racing real sub-second
  timing against the ~200ms child re-exec overhead.

## Consequences

- A third pool rotation entry point exists alongside `RotateID` (fsnotify
  watcher) and `RotateForNewSession` (`new_session` verb) — all three share
  `rekeyLocked`, so a future change to the re-key invariant only needs to
  touch one place.
- Client notification of a self-heal is deliberately absent: a remote client
  tracking the bootstrap id will hold a stale id after a self-heal rotation,
  with no `SessionTransition` to inform it (unlike a `/clear`, which does
  fire one). If operators observe stale client views after a self-heal in
  practice, that's a follow-up ticket to design the notification (a new
  transition reason, not `ReasonClear`) — not a defect in this decision.
- `SelfHeal == nil` (every non-bootstrap `supervisor.Config` — per-caller
  sessions, foreground, tests) leaves the entire path inert; behavior is
  byte-identical to pre-#1165 retry-forever.

## Related

- [codebase/1165.md](../codebase/1165.md) — implementation summary.
- [codebase/1125.md](../codebase/1125.md) — `RotateForNewSession`, the primitive this deliberately does not reuse.
- [ADR 032](032-bootstrap-resume-per-spawn-existence-probe.md) — the sibling root fix (#1164), different fabric.
- [features/sessions-package.md](../features/sessions-package.md) § `Pool.RotateBootstrapForSelfHeal`.
- `docs/specs/architecture/1165-supervisor-fast-crash-self-heal.md` — full architect spec.
