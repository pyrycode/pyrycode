# ADR 016: Bootstrap session ignores persisted `lifecycle_state` on warm-start

## Status

Accepted (ticket #202). E2E regression coverage added in #253 (`internal/e2e/bootstrap_warm_start_test.go`).

## Context

`internal/sessions` persists each session's `lifecycle_state` (`"active"` / `"evicted"`) to `sessions.json` so warm-starts pick up where the previous process left off. Idle eviction (#40) and the cap policy (#41) both transition sessions to `evicted`, write the field, and rely on `Pool.Activate` (driven by an attach client) to wake them on next use.

The bootstrap session is special: it is the per-process auto-spawn entry the daemon brings up at startup so `claude` is available for the first attach. Unlike user-minted sessions, **nothing drives `Pool.Activate` on the bootstrap** — `Pool.Run` schedules `sess.Run(gctx)` and proceeds to `errgroup.Wait`; only consumer-driven paths (attach handler, `sessions.new`, `GetOrCreate`) ever call `Activate`.

The interaction with persisted state was latent until #190's `pyry update` daemon-restart wiring made it reliable to bounce the daemon *after* an idle eviction had a chance to land. The failure mode (#202):

1. Daemon starts cold, bootstrap is `active`, claude spawns. Working as designed.
2. Idle timeout fires after `IdleTimeout` of no attached client; `transitionTo(stateEvicted)` writes `lifecycle_state: "evicted"` for the bootstrap to disk. Working as designed.
3. Daemon is restarted (machine reboot, manual `launchctl kickstart`, `pyry update`'s post-replace daemon restart, crash-loop respawn). New process calls `Pool.New`. `pickBootstrap` returns the persisted entry. `parseLifecycleState("evicted") = stateEvicted`. The session initialises with "evicted" semantics (`evictedCh = closedChan()`, fresh open `activeCh`).
4. `Pool.Run` calls `p.supervise(bootstrap)`. `Session.Run`'s first `snapshotState()` returns `stateEvicted`; `runEvicted` enters its `select { case <-ctx.Done(): … case <-s.activateCh: … }`.
5. Nothing ever sends on `activateCh`. The daemon hangs forever — `pyry status` answers (control plane is up) but reports `StartedAt: 0001-01-01T00:00:00Z` and `Uptime: 2562047h47m16.854775807s` (`time.Duration(math.MaxInt64).String()`), the smoking-gun zero-time tell that `Supervisor.Run` was never called.

Trigger requires non-TTY stdin (which selects supervisor mode rather than the foreground client). With a TTY the foreground client mode runs and the bug is invisible.

## Decision

In `Pool.New`'s `pickBootstrap` arm, **discard the persisted `LifecycleState` and force `lcState = stateActive`**. Non-bootstrap sessions retain their persisted state — lazy respawn on attach is still correct for them.

```go
// before
lcState = parseLifecycleState(entry.LifecycleState)

// after
lcState = stateActive
_ = entry.LifecycleState // intentionally ignored
```

The two-line change is the entire production-code fix (`internal/sessions/pool.go:275`).

## Rationale

### Why the load layer, not `Pool.Run`

Three alternatives were considered and rejected:

- **Call `p.Activate(ctx, p.bootstrap)` after `p.supervise(bootstrap)` in `Pool.Run`.** Works, but does an extra registry write at every warm-start where the bootstrap was loaded as evicted (`Activate` synchronously drives `runEvicted → transitionTo(stateActive) → persist → close(activeCh)`), and parks `Pool.Run` synchronously inside that window. Fixing the load is cleaner: the bootstrap is just always loaded as active, no transition fires, no extra disk write, no extra synchronisation.
- **Send on `activateCh` directly from `Pool.Run` (fire-and-forget).** Bypasses the `Activate` / `Evict` contract that always waits on the broadcast channel and persists before returning. Brittle: a future refactor that changes the lifecycle goroutine's drain order would fail silently.
- **Special-case `runEvicted` to peek for the bootstrap on first entry.** Conflates two concerns inside the lifecycle goroutine. A future contributor reading `runEvicted` would have to remember the bootstrap-special-case; the state machine becomes harder to reason about.

The chosen fix puts the special-casing at the boundary (the load layer in `Pool.New`) where it is most visible and most localised. The state machine itself stays uniform — bootstrap and non-bootstrap sessions follow the same `Session.Run` rules; only their initial condition differs.

### Why the bootstrap is different from non-bootstrap sessions

Idle eviction is a within-process resource-management semantic — "free claude after the idle window so RAM scales with active conversations, not registered sessions." For non-bootstrap sessions that semantic correctly carries across daemon process boundaries: a session a user minted last week and never re-attached should warm-start in `evicted`, and the next attach drives `Activate` to spawn claude. There is always a consumer-driven entry point on those sessions.

The bootstrap has no such entry point. Its contract is "claude is available on daemon startup" — the per-process auto-spawn invariant the foreground client relies on. Carrying `evicted` across the process boundary inverts that contract: the new process inherits a state whose only legal exit is an `Activate` call that nothing in the supervisor-mode startup path ever makes.

### Disk consistency

The on-disk `lifecycle_state: "evicted"` for the bootstrap is left untouched at startup time — the existing "warm start is not a state change → don't rewrite" invariant in `Pool.New` holds. The first state transition the running daemon makes (almost always: the next idle eviction once the new process's idle timer fires) rewrites the registry with the then-current state. Brief disk/memory disagreement during the warm-start window is harmless; in-memory state is the source of truth for a running daemon.

### Why this is structurally correct, not a defensive hack

The fix doesn't paper over the symptom (the zero-time `StartedAt` sentinel surfaced by `pyry status`); it makes the underlying state-machine path that produced the sentinel unreachable. Once `lcState = stateActive` on warm-load, `Session.Run` enters `runActive`, which spawns `Supervisor.Run`, which sets `State.StartedAt = time.Now()`. The zero-time tell becomes structurally unreachable on the supervisor-mode startup path. AC #3 is satisfied without changes to the `status` verb.

## Consequences

**Going forward:**

- The bootstrap session always warm-starts active. Operators never see a hung daemon under non-TTY stdin from a previously-evicted bootstrap.
- Non-bootstrap sessions keep their persisted state. The cap-eviction warm-start path and lazy-respawn-on-attach path are unaffected — both have always relied on consumer-driven `Activate` calls.
- The `lifecycle_state` field still rides on the bootstrap entry in `sessions.json` for forward compatibility (older binaries don't set `DisallowUnknownFields`), but its value for the bootstrap is informational only — it records what the bootstrap was when the previous process exited, not what the current process honours.
- Future "always-on" sessions added by Phase 2.0 (auto-mint workloads with no consumer-driven `Activate`) need the same warm-start treatment. The pattern: any session whose contract is "this must be active when the daemon comes up" must load as `stateActive` regardless of persisted state.

**Trade-offs accepted:**

- A bootstrap that was deliberately evicted in the previous process (idle timeout fired, no attach client arrived) will respawn claude immediately on the next daemon start. This costs a fresh claude startup at machine boot or after `pyry update`, which is the right behaviour anyway: the daemon should come up ready to attach, not in a half-cocked evicted state that the next attach has to wake up.
- A loud `pyry stop` followed immediately by `pyry start` no longer preserves the bootstrap's evicted state. In practice operators don't `stop`/`start` to skip a respawn; they just leave the daemon up and let idle eviction handle it within the running process.

## Related

- [ADR 005](005-idle-eviction-state-machine.md) — the per-session two-state machine this carve-out applies to.
- [ADR 013](013-evict-activate-persist-ordering.md) — the persist-before-wake contract that makes `Activate` the only legal exit from `evicted`.
- [`features/idle-eviction.md`](../features/idle-eviction.md) — feature documentation; the bootstrap-warm-start contract is documented there.
- [`features/e2e-harness.md` § Bootstrap Warm-Start Pattern](../features/e2e-harness.md) — `bootstrap_warm_start_test.go` (#253), the e2e regression test that pins this carve-out plus its non-bootstrap boundary.
- [`docs/specs/architecture/202-supervise-bootstrap-evicted-warm-start-hang.md`](../../specs/architecture/202-supervise-bootstrap-evicted-warm-start-hang.md) — build-time spec.
- [`docs/specs/architecture/253-e2e-bootstrap-warm-start-ignores-persisted-lifecycle-state.md`](../../specs/architecture/253-e2e-bootstrap-warm-start-ignores-persisted-lifecycle-state.md) — e2e regression spec.
