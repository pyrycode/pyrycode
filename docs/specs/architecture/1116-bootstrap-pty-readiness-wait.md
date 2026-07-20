# Spec #1116 — Deterministic bootstrap-readiness wait in `pool_test.go`

**Size:** S (test-only; zero production change). **Security-sensitive:** no.

## Files to read first

- `internal/sessions/pool_test.go:601-612` — RotateID test's poll loop (site 1). 2s wall-clock deadline over `pool.Default().State().ChildPID > 0`; this is the flake shape.
- `internal/sessions/pool_test.go:928-939` — `TestPool_Supervise_AfterRunReturns_ReturnsErrPoolNotRunning` poll loop (site 2).
- `internal/sessions/pool_test.go:957-975` — `TestPool_Supervise_ConcurrentCalls_RaceClean` poll loop (site 3).
- `internal/sessions/pool_test.go:57-79` — `helperPoolWithSleepArgs`: bootstrap = `/bin/sleep 3600`, Bridge set. Used by sites 2 & 3. Note the RotateID test (site 1) builds its own `New(Config{...})` inline (line 574) with `/bin/sh -c "exec sleep 3600"` — the helper must work for both `pool` values.
- `internal/sessions/session.go:335-355` — `Session.Activate` already funnels through `s.sup.WaitForPTY(ctx)` at line 354. This is the exact accessor pattern to mirror; `sup` is the unexported `Runner` field.
- `internal/supervisor/supervisor.go:611-631` — `WaitForPTY`: blocks on `sessReadyCh` until `setSession(non-nil)` closes it, or ctx cancels. Returns `nil` on readiness, `ctx.Err()` on cancel. Channel is allocated in `supervisor.New` (line 657), so it is non-nil before `Run` — a pre-`Run` wait blocks (does not nil-hang) until the child spawns.
- `internal/supervisor/supervisor.go:940-1000` — `runOnce` ordering: `tuidriver.Spawn` → `onSpawn(pid)` sets `ChildPID` (line 946) → `setSession(sess)` fires readiness (line 964 bridge mode / line 1000 foreground mode). Readiness is a **strictly-later** event than `ChildPID > 0`, and fires for **any** spawned child including `/bin/sleep`.
- `internal/sessions/pool.go:1088-1106` — `Pool.Run` ordering: `runGroup`/`runCtx` wired (1091) → `supervise(bootstrap)` schedules `sess.Run` (1099) → `close(readyCh)` (1106). Establishes that when readiness fires (child spawned inside that scheduled goroutine), `runGroup` was already non-nil.
- `docs/lessons.md:94` — `pty.Start` is not interruptible; under `-race` + contention it "stretches into hundreds-of-ms-to-seconds per cycle." This is the documented root cause the 2s deadline collides with.

## Context

`TestPool_Supervise_AfterRunReturns_ReturnsErrPoolNotRunning`, `TestPool_Supervise_ConcurrentCalls_RaceClean`, and the `RotateID` bootstrap wait all detect "bootstrap child started" by **polling `pool.Default().State().ChildPID > 0` against a fixed 2s deadline**. Under `-race`'s scheduling slowdown, `tuidriver.Spawn`/`pty.Start` can exceed 2s; the poll expires before the child spawns and the test fails on `pool_test.go:938: bootstrap child never started` — a timing artefact, not a defect. Recurring family; unmasked most recently by PR #1115 (which touches zero `internal/sessions/` files).

The fix replaces the wall-clock poll with the **event-driven PTY-readiness signal that already exists**: the bootstrap supervisor's `WaitForPTY`, reachable in-package via `pool.Default().sup`. `Session.Activate` already uses this exact signal (session.go:354).

## Design

**One accessor, one signal, one helper.** All three sites become a call to a small local helper in `pool_test.go`:

```
func waitBootstrapReady(t *testing.T, pool *Pool)  // t.Helper()
```

Behaviour (contract, not implementation):
- Derive a backstop context: `context.WithTimeout(context.Background(), <backstop>)`, `defer cancel()`.
- Call `pool.Default().sup.WaitForPTY(ctx)`. On non-nil error, `t.Fatalf("bootstrap PTY never ready: %v", err)`.
- Happy path returns via the readiness event, **not** by exhausting the timer.

`sup` is the unexported `Runner` field on `*Session` (session.go:127); the test file is `package sessions` (pool_test.go:1), so the access is direct — **no production accessor added, zero production change.** For the helpers used here `RunnerFactory` is unset, so `sup` is a real `*supervisor.Supervisor` (pool.go:361-362) whose `WaitForPTY` is the genuine blocking implementation.

### Why `WaitForPTY`, not `Pool.Ready()`

`Pool.Ready()` (pool.go:1168) closes at pool.go:1106 — immediately after `supervise(bootstrap)` *schedules* the goroutine, **before** the child actually spawns. Site 1 (RotateID) needs a **live, non-zero bootstrap PID** for the rotation watcher to probe; `Ready()` does not guarantee that. `WaitForPTY` fires from `setSession` (supervisor.go:964/1000), one line after `onSpawn` sets `ChildPID` (supervisor.go:946) — so readiness **implies** `ChildPID > 0`. It therefore satisfies all three sites with a single uniform signal; `Ready()` would need site-specific reasoning. Reject `Ready()`.

### Why this is deterministic (no residual race)

- **Readiness ⇒ child spawned:** `setSession(sess)` runs only after `tuidriver.Spawn` succeeds; it is the same code path the old poll was waiting to observe, minus the wall clock.
- **Readiness ⇒ `runGroup` wired:** Pool.Run sets `runGroup` (1091) strictly before `supervise` (1099) strictly before the scheduled goroutine reaches `runOnce`/spawn. So when `WaitForPTY` returns for sites 2 & 3, a subsequent `supervise` cannot hit `ErrPoolNotRunning` for a spurious "not yet wired" reason.
- **No child exit between readiness and assertion:** the fixtures (`/bin/sleep 3600`, `/bin/sh -c "exec sleep 3600"`) never exit, so `ChildPID` stays live after readiness. The explicit `if ChildPID == 0 { t.Fatal("bootstrap child never started") }` re-check at sites 1–3 is therefore **removed** — readiness *is* the started signal; keeping a ChildPID re-assertion would reintroduce a (smaller) sample-timing dependency for no coverage gain.

### Per-site edits (all in `internal/sessions/pool_test.go`)

- **Site 1 (RotateID, ~601-612):** replace the `deadline`/`for`/`time.Sleep(20ms)` loop **and** the `if ChildPID == 0 { t.Fatal(...) }` with `waitBootstrapReady(t, pool)`. The downstream jsonl-write + registry-poll block is unchanged.
- **Site 2 (`…AfterRunReturns…`, ~928-939):** replace the loop + the `if ChildPID == 0 { cancel(); <-done; t.Fatal(...) }` block with `waitBootstrapReady(t, pool)`. `cancel()` + `select{<-done…}` + the `supervise` assertion below are unchanged.
- **Site 3 (`…ConcurrentCalls…`, ~964-975):** same replacement as site 2. The N=32 dummy fan-out below is unchanged.

Net: three loops (~10 lines each) + three ChildPID re-checks removed; one ~8-line helper added; three one-line call sites. `time` import stays (used elsewhere in the file, e.g. `time.After`, `time.Now` in other tests, backoff configs).

## Concurrency model

No new goroutines. `WaitForPTY` is a lock-brief snapshot of `sessReadyCh` under the supervisor's `sessMu`, then a `select` on that channel vs `ctx.Done()` (supervisor.go:621-631) — already proven safe from any goroutine (`Session.Activate` calls it in production). The test goroutine blocks on it while the bootstrap's own errgroup goroutine drives `runOnce` → `setSession`.

## Error handling / backstop

The backstop context deadline is a **safety net only** — it exists so a genuinely wedged bootstrap fails the test with a clear message instead of hanging until the Go test timeout. It must be generous enough to never trip in the happy path under `-race` (the whole point of the ticket): pick a value comfortably above the observed worst-case `pty.Start` under contention — **30s** is the recommended value (>> the failing 2s, well under Go's default 10m test timeout; `-count=20 -race` runs stay bounded). Do **not** reuse 2s. On backstop expiry, `WaitForPTY` returns `ctx.Err()` (`context.DeadlineExceeded`) and the helper `t.Fatalf`s.

## Testing strategy

The change is itself test infrastructure; verification is the ticket's ACs:

- `go test -race -count=20 -run '^TestPool_Supervise_AfterRunReturns_ReturnsErrPoolNotRunning$|^TestPool_Supervise_ConcurrentCalls_RaceClean$' ./internal/sessions/` → 20/20.
- `go test -race ./internal/sessions/` → green (no regression to the rest of the package, including `TestPool_Run_StartsWatcher`, which shares the RotateID fixture region).
- Sanity: the RotateID assertion path (registry rotates to `newID`) still passes — readiness guarantees the live bootstrap PID the watcher probes.
- Confirm no `time.Sleep`/fixed wall-clock deadline remains as the **primary** "bootstrap started" synchronization at the three sites (grep the three functions).

## Out of scope (deferred, evidence-based)

The identical `pool.Default().State().ChildPID > 0` wait exists at ~20 call sites across `pool_cap_test.go`, `pool_get_or_create_test.go`, `pool_create_test.go`, `pool_remove_test.go` via the shared `pollUntil` helper with 2–10s budgets — **not observed flaking**. Do **not** migrate them here; that cascade (4 extra files, >5 edit sites) would push the ticket past S. File a follow-up only if the family recurs at one of those sites. (Ticket Technical Notes, honoured.)

## Open questions

None. The signal, accessor, and backstop are all determined above. The only free parameter is the exact backstop duration; 30s recommended, developer may pick any value in the 20–60s range provided it is not the failing 2s and completes via the event in the happy path.
