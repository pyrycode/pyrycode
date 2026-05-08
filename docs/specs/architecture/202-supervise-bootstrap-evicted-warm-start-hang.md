# #202 — supervisor hangs in startup under non-TTY stdin (bootstrap-evicted warm-start)

## Files to read first

- `internal/sessions/pool.go:249-352` — `New()` pickBootstrap branch; the load path that captures `lcState` from the persisted registry entry. The fix lives here.
- `internal/sessions/session.go:21-51` — `lifecycleState` enum, `String()`, and `parseLifecycleState`. Confirm "active" / "evicted" wire encoding and the unknown-string-defaults-to-active rule.
- `internal/sessions/session.go:242-265` — `Session.Run` state-machine loop. The two `case` arms; the evicted arm calls `runEvicted` which blocks on `activateCh` until something signals.
- `internal/sessions/session.go:336-346` — `runEvicted`. The exact channel the bootstrap parks on when this bug fires (`<-s.activateCh` / `<-ctx.Done()`).
- `internal/sessions/pool.go:704-750` — `Pool.Run`. Schedules `sess.Run(gctx)` for the bootstrap via `p.supervise`; **never calls `Activate` on the bootstrap**. Confirms the fix-site choice (`New` not `Run`).
- `internal/supervisor/supervisor.go:152-213` — `Supervisor.Run`. Sets `State.StartedAt = time.Now()` at line 156 and logs `"spawning claude"` at line 175. The two observable signals AC#2 and AC#3 reference.
- `internal/sessions/pool_cap_test.go:14-50` — `helperPoolCap` pattern: `/bin/sleep 3600` as fake claude, Bridge mode for stdin/stdout pump, short backoff. The regression test reuses this exact recipe.
- `internal/sessions/registry.go:14-51` — `registryFile` / `registryEntry` schema and `loadRegistry`. The test fixture writes one of these with `lifecycle_state: "evicted"`.

## Context

`pyry` invoked from launchd / systemd / a wrapper that pipes `</dev/null` reaches the `"pyrycode starting"` log line and brings the control server up (`pyry status` and `pyry stop` respond) but never logs `"spawning claude"` and never spawns the child. `pyry status` reports the Go zero-time tell: `Started at: 0001-01-01T00:00:00Z`, `Uptime: 2562047h47m16.854775807s` (`time.Duration(math.MaxInt64).String()`). A `sample` of the daemon shows the main goroutine parked on `pthread_cond_wait` — a Go-level channel/sync wait that never gets signalled.

### Diagnosis

The hang is **not** a regression in the supervisor or sessions packages between v0.9.1 and v0.10.1. `git diff v0.9.1..v0.10.1 -- internal/supervisor/ internal/sessions/` produces no output: those packages are byte-identical. The only diffs in the v0.9.1 → v0.10.1 range are additive — the new `pyry update` subcommand routing in `cmd/pyry/main.go` (4 lines) and the new `cmd/pyry/update.go` + `internal/update/` package. Neither runs at all in the supervisor-mode startup path the user reproduces; `runUpdate` dispatches only when `os.Args[1] == "update"`.

The root cause is a **latent bug from `#40` (Phase 1.2c-A, idle eviction + lazy respawn, shipped in v0.8.0)**: the bootstrap session's lifecycle state is persisted to `sessions.json`, but `Pool.Run` does not re-activate the bootstrap on warm-start. The exact flow that produces the hang:

1. Daemon starts cold. `Pool.New` builds the bootstrap with `lcState = stateActive` (registry didn't exist; `parseLifecycleState("")` defaults to active). `Pool.Run` schedules `sess.Run(gctx)` via `p.supervise`. `Session.Run` enters the `stateActive` arm → `runActive` → `go s.sup.Run(subCtx)` → claude spawns. Working as designed.
2. Idle timeout fires after 15 min with no attached client. `runActive` returns `nil`, the `Session.Run` loop calls `transitionTo(stateEvicted)` which writes `lifecycle_state: "evicted"` for the bootstrap to `sessions.json`. JSONL on disk is preserved.
3. Daemon is restarted (machine reboot, manual `launchctl kickstart`, `pyry update`'s post-replace daemon restart added in #190, or a crash-loop respawn). The new process calls `Pool.New`. `pickBootstrap` returns the persisted entry. `lcState = parseLifecycleState("evicted") = stateEvicted`. `Pool.New` initialises the session with `evictedCh = closedChan()`, `activeCh = make(chan struct{})` — i.e. "evicted" semantics.
4. `Pool.Run` calls `p.supervise(bootstrap)`. The lifecycle goroutine starts. `Session.Run`'s first `snapshotState()` returns `stateEvicted`. `runEvicted` enters its `select { case <-ctx.Done(): … case <-s.activateCh: … }`.
5. Nothing ever sends on `activateCh`. `Pool.Activate` is the only sender, and `Pool.Run` never calls it for the bootstrap. The cap path (`Pool.Activate`'s only other entry) is consumer-driven (attach / GetOrCreate / `pyry sessions new`); none of those fire under `pyry --dangerously-skip-permissions </dev/null`.
6. `Pool.Run` proceeds to `g.Wait()` — which is the `pthread_cond_wait` the user's `sample` snapshot captured. Forever.

`State.StartedAt = 0001-01-01T00:00:00Z` is the smoking gun: `Supervisor.Run` (line 152) is the only writer of that field, and it sets it unconditionally on entry (line 156-160). Zero-value means `Supervisor.Run` was never called for the bootstrap — which is exactly what step 4-5 above describes. `runActive` (the only caller of `s.sup.Run`) was never reached.

### Why the user sees this as a v0.10.1 regression

`pyry update` (v0.10.0) added a self-update verb. v0.10.1 (#190) wired automatic daemon restart into that verb. v0.10.1 is therefore the first version where the daemon restarts itself **after** an idle eviction had a chance to persist `lifecycle_state: "evicted"` for the bootstrap. Pre-`pyry update`, kickstart cycles were operator-driven and rare — the user likely restarted the daemon shortly after install (before the first idle-eviction window elapsed) so `sessions.json` still had `lifecycle_state: "active"` (or no field at all). The v0.10.1 auto-restart is the trigger that exposes the latent bug, not the cause.

The user's stated suspicion (#190 introduced a startup-coordination splice point) is wrong about the mechanism but right about the trigger window. The architecture for #190 (`docs/specs/architecture/190-update-daemon-restart-wiring.md`, ADR 015) confirms #190 only touches `cmd/pyry/update.go` post-binary-replace; it does not touch the supervisor startup path.

## Design

### One-line fix

In `internal/sessions/pool.go:New`, when the bootstrap is loaded from a persisted registry entry, **discard the persisted `LifecycleState` and force `lcState = stateActive`**. The bootstrap is a per-process invariant whose contract is "claude is available on daemon startup"; idle eviction is an in-process resource-management semantic that should not survive across daemon process boundaries. Other (non-bootstrap) sessions retain their persisted state — lazy respawn on attach is still correct for them.

The patched line is `pool.go:275`:

```go
// before
lcState = parseLifecycleState(entry.LifecycleState)

// after
//
// Bootstrap-only: ignore persisted lifecycle_state. The bootstrap is the
// per-process auto-spawn entry; daemon-mode startup contract is "claude
// is available". Idle eviction within a process is correct (frees claude
// after 15min of no attach), but a fresh daemon process starts a fresh
// active state — there is no carry-over. Persisting "evicted" for the
// bootstrap and then waking with no attach client to drive Activate
// would hang the daemon forever (see #202). Non-bootstrap sessions keep
// their persisted state — lazy respawn on attach is still correct there.
lcState = stateActive
_ = entry.LifecycleState // intentionally ignored; see comment above
```

The two-line change in the `pickBootstrap` arm is the entire production-code fix.

### Why this site, not `Pool.Run`

Three alternatives were considered and rejected:

- **Call `p.Activate(ctx, p.bootstrap)` after `p.supervise(bootstrap)` in `Pool.Run`.** Works, but does the wrong thing for the bootstrap-evicted path: `Activate` blocks on `<-activeCh` until the lifecycle goroutine has driven `runEvicted → transitionTo(stateActive) → persist → close(activeCh)`. That's an extra registry write at every warm-start where the bootstrap was loaded as evicted, plus a small startup window where `Pool.Run` is parked synchronously. Fixing the load is cleaner: bootstrap is just always loaded as active, no transition fires, no extra disk write, no extra synchronisation.
- **Send on `activateCh` directly from `Pool.Run` (fire-and-forget).** Bypasses the `Activate`/`Evict` contract documented in `session.go:171-201` (always wait on `activeCh`, persist completes before return). Brittle: if a future refactor changes the lifecycle goroutine's drain order, the unsynchronised send pattern fails silently.
- **Special-case `runEvicted` to peek for the bootstrap on first entry.** Conflates two concerns inside the lifecycle goroutine. The state machine becomes harder to reason about; a future contributor reading `runEvicted` has to remember the bootstrap-special-case.

The chosen fix puts the special-casing at the boundary (the load layer in `Pool.New`) where it is most visible and most localised. The state machine itself stays uniform — the bootstrap and non-bootstrap sessions follow the same `Session.Run` rules; only their initial condition differs.

### Concurrency model

Unchanged. The fix is a pure-load-layer change: a single field assignment in `Pool.New` before the `Session` struct is constructed. No new goroutines, no new channels, no new locks. The existing `lcMu` discipline, the `Pool.mu` / `Session.lcMu` lock order, and the `activeCh` / `evictedCh` / `activateCh` / `evictCh` channel topology are all preserved.

### Error handling

No new failure modes. `Pool.New` never returned an error for a persisted-but-bogus `lifecycle_state` (the existing `parseLifecycleState` defaults unknowns to active); ignoring the field for the bootstrap doesn't introduce one either.

### Disk consistency

The on-disk `lifecycle_state: "evicted"` for the bootstrap is left untouched at startup time — the existing "warm start is not a state change → don't rewrite" invariant (pool.go:339-346) holds. The first state transition the running daemon makes (almost always: the next idle eviction once the new process's idle timer fires) will rewrite the registry with the then-current state. Brief disk/memory disagreement during the warm-start window is harmless; in-memory state is the source of truth for a running daemon.

### Status command's zero-time tell

AC#3 asks `pyry status` to report a real `Started at` and `Uptime` post-fix. This is satisfied structurally by the fix: once `lcState = stateActive` on warm-load, `Session.Run` goes straight into `runActive`, which spawns `Supervisor.Run`, which sets `State.StartedAt = time.Now()` at supervisor.go:156. The zero-time sentinel becomes unreachable on the supervisor-mode startup path. No changes to the `status` verb or to `Supervisor.State`.

The technical-notes paragraph in the ticket ("consider whether `status` should detect-and-print a clearer message when the supervisor hasn't initialized") is correctly flagged as out of scope: the fix makes the sentinel unreachable in the only path that produced it. Any "detect zero-time and print friendlier text" work belongs to a separate UX ticket.

## Testing strategy

### Regression test

`internal/sessions/pool_test.go` gains one new test: `TestPool_BootstrapEvictedOnDisk_StartsClaudeOnWarmStart`.

Shape (~40-60 lines):

```go
func TestPool_BootstrapEvictedOnDisk_StartsClaudeOnWarmStart(t *testing.T) {
    t.Parallel()
    if _, err := exec.LookPath("/bin/sleep"); err != nil {
        t.Skipf("benign binary not available: %v", err)
    }

    // Pre-write a registry with a single bootstrap entry persisted as
    // evicted. This is the exact on-disk shape produced by an idle-evicted
    // bootstrap that survives a daemon restart (the trigger condition for
    // #202).
    dir := t.TempDir()
    regPath := filepath.Join(dir, "sessions.json")
    bootstrapID := SessionID("550e8400-e29b-41d4-a716-446655440000")
    now := time.Now().UTC()
    reg := &registryFile{
        Version: 1,
        Sessions: []registryEntry{{
            ID:             bootstrapID,
            Label:          "",
            CreatedAt:      now,
            LastActiveAt:   now,
            Bootstrap:      true,
            LifecycleState: "evicted",
        }},
    }
    if err := saveRegistryLocked(regPath, reg); err != nil {
        t.Fatalf("saveRegistryLocked: %v", err)
    }

    logger := slog.New(slog.NewTextHandler(io.Discard, nil))
    pool, err := New(Config{
        Logger:       logger,
        RegistryPath: regPath,
        Bootstrap: SessionConfig{
            ClaudeBin:      "/bin/sleep",
            ClaudeArgs:     []string{"3600"},
            BackoffInitial: 10 * time.Millisecond,
            BackoffMax:     10 * time.Millisecond,
            BackoffReset:   time.Second,
            // Bridge mode mirrors the non-TTY supervisor path the user
            // reproduces (cmd/pyry/main.go:373). It also keeps the
            // supervisor's I/O pumps off os.Stdin so the test doesn't
            // contend with `go test`'s stdin.
            Bridge: supervisor.NewBridge(logger),
        },
    })
    if err != nil {
        t.Fatalf("sessions.New: %v", err)
    }

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    runErr := make(chan error, 1)
    go func() { runErr <- pool.Run(ctx) }()

    sess := pool.Default()
    deadline := time.After(5 * time.Second)
    for {
        if sess.State().Phase == supervisor.PhaseRunning && sess.State().ChildPID > 0 {
            break
        }
        select {
        case <-deadline:
            t.Fatalf("supervisor did not reach PhaseRunning within 5s; "+
                "state=%+v (would have failed against the v0.10.1 binary)",
                sess.State())
        case <-time.After(20 * time.Millisecond):
        }
    }

    // AC#3: StartedAt is real, not the Go zero-time sentinel.
    if sess.State().StartedAt.IsZero() {
        t.Errorf("StartedAt is zero after PhaseRunning; want non-zero")
    }

    cancel()
    select {
    case err := <-runErr:
        if !errors.Is(err, context.Canceled) {
            t.Errorf("Pool.Run err = %v, want context.Canceled", err)
        }
    case <-time.After(5 * time.Second):
        t.Fatal("Pool.Run did not return after cancel")
    }
}
```

This test:

- **Reproduces the bug structurally.** The pre-written registry with `lifecycle_state: "evicted"` is the exact on-disk shape the user's launchd-supervised daemon arrives at after one idle-eviction-then-restart cycle. No real claude, no real PTY, no TTY, no GitHub network — runs in CI under `go test -race` like every other sessions test.
- **Would have failed against the v0.10.1 binary.** Pre-fix, `pickBootstrap` loads `lcState = stateEvicted`, the lifecycle goroutine parks in `runEvicted`, `Phase` stays `PhaseStarting`, the 5s deadline fires. AC#4's "would have failed against the v0.10.1 binary" requirement is met by construction.
- **Asserts both AC#2 and AC#3 in one shot.** AC#2 ("reaches `spawning claude` within 5s") is captured by `Phase == PhaseRunning && ChildPID > 0` (the supervisor sets both at supervisor.go:175-181 immediately after the spawn-log line). AC#3 ("real `Started at`, not zero") is the explicit zero-time check after the phase transition.
- **Exercises the actual code path in question.** Uses real `Pool.New` → real `Pool.Run` → real `Session.Run` → real `Supervisor.Run` against a benign `/bin/sleep` child. The only test scaffolding is the registry fixture and the Bridge.

### Existing test surface

- `TestSession_ShutdownFromEvicted` (session_test.go:343) covers the unrelated case of clean ctx-cancel from `runEvicted` for non-bootstrap-driven flows. Unaffected by the fix; remains green.
- `TestPool_New_BootstrapInstalled`, `TestPool_New_Reconciles_*`, and the rest of `pool_test.go`'s warm-start coverage exercise non-evicted persisted states; they don't construct an evicted-on-disk fixture today, which is why the bug went uncaught. Unchanged by the fix; remain green.

No existing test should break. The fix only changes behaviour for one previously-unexercised input shape: a registry whose bootstrap entry has `lifecycle_state: "evicted"`.

### Manual verification (post-merge, not CI)

The reporter's repro is the natural manual smoke test:

```bash
# Reproduce the failing state by forcing the on-disk evicted persist:
# 1. Run pyry, attach, detach, wait for idle timeout (or set
#    --pyry-idle-timeout 5s for the test).
# 2. Confirm sessions.json shows lifecycle_state: "evicted" for the
#    bootstrap entry.
# 3. SIGTERM the daemon.
# 4. Restart with: pyry --dangerously-skip-permissions </dev/null
# Pre-fix: hangs forever, status shows StartedAt = 0001-01-01.
# Post-fix: spawning claude logs within 5s, status shows real StartedAt.
```

Out of scope here — the regression test in CI is the merge-gating signal.

## Open questions

None known. The fix is local, the test reproduces the bug structurally, and the diagnosis is grounded in the actual code paths and the persisted-state contract.

## Out of scope

- The friendlier `pyry status` rendering for un-initialised supervisors (ticket-body technical note). Belongs to a separate UX ticket; not load-bearing for this fix.
- The defensive `install.sh` post-install check (already split out per the ticket body). Not blocked by this fix.
- Auditing whether non-bootstrap sessions need similar warm-start activation logic. They do not — non-bootstrap sessions are designed for lazy respawn on attach (Phase 1.3's whole point); persisted `evicted` is the correct steady state for them between daemon restarts. The bootstrap is special precisely because nothing else drives its activation under non-TTY stdin.
