# #253 — e2e: bootstrap warm-start ignores persisted lifecycle_state

## Files to read first

- `internal/e2e/restart_test.go` — the closest pattern. `newRegistryHome`, `writeRegistry`, `readRegistry`, `mustReadFile` are the helpers this new test reuses verbatim. `TestE2E_Restart_PreservesEvictedSessions` (lines 150-209) is the structural twin: pre-write a registry, `StartIn → Stop → StartIn`, assert post-restart state. The bootstrap test is the same shape with one extra clause (`lifecycle_state: "evicted"` on the bootstrap entry, then status-must-be-running).
- `internal/e2e/idle_test.go:99-125` — `waitForBootstrapState`. Reuse it for the non-bootstrap-evicted case (poll the registry until the daemon has finished its warm-load reconciliation pass). Optionally reuse for the bootstrap-active assertion too.
- `internal/e2e/idle_test.go:18-38` — `TestE2E_IdleEviction_EvictsBootstrap`. Same `pyry status` stdout-grep pattern (`Phase:         running`) the new test uses on the positive side.
- `internal/e2e/harness.go:33-50, 184-220` — `StartIn` package docstring and signature. The "stop / mutate / start" recipe in the docstring is exactly what AC#1 needs; `StartIn(t, home)` returns a daemon and `h.Stop(t)` is documented as idempotent with the t.Cleanup teardown so the same `home` can be re-driven by a second `StartIn`.
- `cmd/pyry/main.go:530-562` — `runStatus`. Confirms the stdout shape: `Phase:         %s`, `Started at:    %s` (RFC3339 UTC), `Uptime:        %s`. Tests grep stdout for these labels with their exact whitespace.
- `internal/control/server.go:823-839` — `buildStatus`. `StartedAt` is rendered as `st.StartedAt.UTC().Format(time.RFC3339)`; the zero-time renders as `0001-01-01T00:00:00Z`. The post-fix assertion negates that string.
- `docs/knowledge/decisions/016-bootstrap-ignores-persisted-lifecycle-state.md` — the ADR this test pins to CI. The "non-bootstrap sessions retain persisted state" carve-out boundary is what AC#1's second case asserts.
- `docs/specs/architecture/202-supervise-bootstrap-evicted-warm-start-hang.md` — the production-side fix's spec. Re-reading the diagnosis section grounds why these specific assertions matter; in particular §"Status command's zero-time tell" explains the `Started at: 0001-01-01T00:00:00Z` and `Uptime: 2562047h47m16.854775807s` signature the test inverts.
- `docs/lessons.md:303` — the "persisted waiting-for-X across a process boundary" lesson. The test docstring should reference it so a future reader of the test file lands on the lesson without grepping.

## Context

PR #204 patched `internal/sessions/pool.go:New` to ignore persisted `lifecycle_state` on the bootstrap session: warm-loaded bootstraps are always loaded as `active`, regardless of the on-disk value. The fix shipped with a unit-level regression test (`TestPool_BootstrapEvictedOnDisk_StartsClaudeOnWarmStart` in `internal/sessions/pool_test.go:943`) that exercises `Pool.New` directly with a hand-rolled `Bridge` and a `/bin/sleep` child.

That unit test pins the load-layer behaviour, but it does not drive the regression class through a real daemon process. The class — *"persisted state needs a driver to exit it on warm-start, and the bootstrap has none under non-TTY stdin"* — is exactly the shape `lessons.md` flags as a recurring failure mode (same family as #38/#39's "single shared resource being corrupted"). Locking it into CI at the e2e layer means a future refactor that accidentally re-introduces `lcState = parseLifecycleState(entry.LifecycleState)` for the bootstrap path — or a similar shape elsewhere in the warm-load reconciliation — fails CI on the actual user-observable signature (`Started at: 0001-01-01T00:00:00Z`, `Uptime: 2562047h47m16.854775807s`, `pyry status` reports a non-running phase forever).

This is a deliberate-regression e2e: the fix is the structural one, the test is the lock-in.

## Design

### One-file change

A single new file, `internal/e2e/bootstrap_warm_start_test.go`, build-tagged `//go:build e2e`. Two `Test*` functions, no new helpers, no harness extensions. All helpers (`newRegistryHome`, `writeRegistry`, `readRegistry`, `mustReadFile`, `waitForBootstrapState`) already exist in `restart_test.go` and `idle_test.go` — they are package-internal under `package e2e` and are usable directly from a new file in the same package.

### Test 1 — `TestE2E_BootstrapWarmStart_IgnoresEvictedOnDisk`

Drives the regression class end-to-end. Shape:

1. `home, regPath := newRegistryHome(t)`
2. `h1 := StartIn(t, home)` — the daemon comes up cold, writes a registry with the bootstrap as `active` (or no `lifecycle_state` field; see §"Why two-phase, not pre-seeded only").
3. `h1.Stop(t)` — clean shutdown.
4. **Mutate `regPath`** — read the JSON via `readRegistry`, locate the entry whose `Bootstrap` is true, set its `LifecycleState` to `"evicted"`, write back via `writeRegistry`. This is a plain JSON edit on the harness's own tempdir; no special seam needed (per ticket Technical Notes).
5. `h2 := StartIn(t, home)` — the daemon comes up warm against the mutated registry. Pre-fix behaviour: `Pool.New` loads `lcState = stateEvicted` for the bootstrap, `Pool.Run` proceeds to `g.Wait()`, no Activate is ever sent on the bootstrap, the daemon parks forever. Post-fix: `lcState = stateActive` ignoring the persisted field, `Session.Run` enters `runActive`, `Supervisor.Run` is called, claude (sleep) spawns.
6. **Assert observable post-fix signals.** Inside a polling deadline (5 s, matching the harness's `readyDeadline`):
   - `pyry status` exits 0 with stdout containing `Phase:         running` (the stdout-grep idiom is established by `idle_test.go`).
   - `pyry status` stdout does NOT contain the zero-time tell `Started at:    0001-01-01T00:00:00Z`.
   - `pyry status` stdout does NOT contain the max-int64 sentinel `Uptime:        2562047h47m16s` — covers the [[Uptime is the smoking gun]] assertion in AC#2 even when `StartedAt` happens to be set late. (Note: `buildStatus` rounds Uptime to seconds, so the rendered form is `2562047h47m16s` — the unrounded `…s.854775807s` form from `lessons.md` and ADR is the supervisor's `time.Duration(math.MaxInt64).String()` before the wire-side rounding.)
   - `pyry sessions list --json` returns the bootstrap entry with `state: "active"`. Use `--json` rather than the table form to keep the assertion robust against tabwriter column alignment changes; decode into `[]control.SessionInfo` via the wire helper in `internal/control/protocol.go`.

   Polling shape: spin on `pyry status` every 100 ms until phase running or 5 s elapses, mirroring `TestE2E_IdleEviction_LazyRespawn`'s tail (idle_test.go:85-96). The deadline budget is intentional: pre-fix the daemon parks forever, so any reasonable polling deadline fires.

7. `h2.Stop(t)` is implicit via t.Cleanup; no explicit call needed.

#### Why two-phase, not pre-seeded only

A pre-seeded registry (`writeRegistry` with `bootstrap=true, lifecycle_state="evicted"`, then a single `StartIn`) reproduces the *bug structurally* but not the user's *trigger path*. The user reports: machine boots → daemon starts → idle eviction fires → daemon restarts (launchd/systemd/`pyry update` — i.e. the v0.10.1-class trigger) → hang. The two-phase shape (cold start → stop → mutate the bootstrap UUID's persisted state → warm start) is faithful to that flow. It also exercises the part the unit test cannot: the `StartIn` warm-load path against a real daemon process where the bootstrap's UUID was *originally* selected by the cold-start daemon, not invented by the test. The mutation step is the minimum-fidelity stand-in for "idle eviction had a chance to run" — it edits `lifecycle_state` only, leaves every other field (UUID, `created_at`, `last_active_at`, `bootstrap`) intact.

The alternative — drive the cold daemon with `-pyry-idle-timeout=1s`, wait for the eviction, stop, restart — works but adds 1-2 s of timer waiting per run and couples the regression test to the idle-eviction timing. Two-phase mutate is simpler and stays decoupled from the idle subsystem (the regression is in the warm-load layer, not the eviction layer; the test should pin the warm-load layer in isolation).

#### Why grep `pyry status` stdout, not the wire client

`internal/e2e/idle_test.go:35` already establishes `bytes.Contains(r.Stdout, []byte("Phase:         running"))` as the e2e idiom for asserting phase. Reusing that idiom keeps the new file's local vocabulary minimal and gives the test the same brittleness profile as the rest of the e2e suite (a stdout-format change breaks both the idle test and this one — better than splitting one test into wire-shape and another into stdout-shape). The exact whitespace `Phase:         ` (one colon, nine spaces) matches `cmd/pyry/main.go:549`'s format string; assertion strings in the test must use this exact form.

The `Started at` and `Uptime` negative assertions are also stdout-greps for the same reason.

The `pyry sessions list` assertion uses `--json` instead because `state: "active"` is a stable wire field but the table form (`STATE` column) goes through tabwriter alignment and could legitimately drift in width without changing semantics.

### Test 2 — `TestE2E_BootstrapWarmStart_NonBootstrapEvictedPersists`

Pins the carve-out boundary. ADR 016's load-layer special-case is bootstrap-only; non-bootstrap sessions correctly retain their persisted `evicted` state on warm-load (lazy respawn drives the next attach). This test catches a future refactor that over-corrects the bootstrap fix into "ignore evicted for *all* sessions" — the test fails because the non-bootstrap session would come back active, contradicting the carve-out's contract.

Shape:

1. `home, regPath := newRegistryHome(t)`
2. Pre-seed a registry with two entries:
   - bootstrap: UUID `11111111-1111-4111-8111-111111111111`, `bootstrap: true`, `lifecycle_state: "active"`.
   - non-bootstrap: UUID `22222222-2222-4222-8222-222222222222`, `label: "evicted-one"`, `lifecycle_state: "evicted"`.

   This mirrors `restart_test.go:150-180`'s `TestE2E_Restart_PreservesEvictedSessions` fixture exactly. Reuse `writeRegistry`.

3. `h := StartIn(t, home)` — single-phase. The cold-start daemon goes through the warm-load path because the registry already exists.
4. **Assert non-bootstrap entry stays evicted.** Two complementary checks (one wire, one disk):
   - `pyry sessions list --json`: decode, locate the entry whose UUID matches `22222222…`, assert `state == "evicted"`.
   - `readRegistry(t, regPath)`: assert the same entry's `LifecycleState == "evicted"`. The on-disk check is the canonical one — the carve-out is a load-layer behaviour, and disk is what the next process boundary will see.

   Use a small polling envelope here (`waitForBootstrapState`-style, but for the `evicted-one` UUID — a one-line helper or inline poll, ~5 s deadline) to absorb the warm-load reconciliation pass that may rewrite the registry on first start.

5. **Assert the bootstrap is unaffected by the seeded `active` value** — its `state` in `sessions list --json` is `active`. (Bootstrap was seeded as `active`, so this is the trivial case; the assertion's purpose is to confirm the non-bootstrap-evicted seed didn't cross-contaminate the bootstrap behaviour.)

This is the smaller of the two tests; it shares the registry-mutation grammar with Test 1 but skips the stop-and-restart loop because the carve-out fires on the cold-load path the same way it would on a warm-load.

### Concurrency model

None new. Both tests are sequential `StartIn → assert → t.Cleanup`. The polling loops are wall-clock-bounded and use existing `time.Sleep`/`time.Now()` patterns from `idle_test.go`. No goroutines, no channels, no shared state across tests. Each test owns its own `home` from `newRegistryHome` (see `restart_test.go:34-49`), so they are safe to run in parallel — but neither is marked `t.Parallel()`, mirroring the rest of `internal/e2e/` (the harness builds pyry once per process; per-test parallelism is not the bottleneck).

### Error handling

No new error paths. Every helper used (`newRegistryHome`, `writeRegistry`, `readRegistry`, `mustReadFile`, `StartIn`, `Stop`, `Harness.Run`) already calls `t.Fatalf` on failure. The new test functions inherit fatal-on-error semantics for free.

The one new failure mode is the polling deadline expiring with the daemon still in `Phase: starting` — that **is** the regression. Fail with a message that prints the last-seen `pyry status` stdout AND `mustReadFile(t, regPath)` so a CI failure has the disk state and the wire state in one report, ready to triage.

### Disk consistency

The mutation step in Test 1 edits `<home>/.pyry/test/sessions.json` directly between `h1.Stop(t)` and `h2 := StartIn(t, home)`. There is no concurrent writer at that point (the first daemon has exited; the second has not started), so a plain `os.ReadFile` + `json.Unmarshal` + struct-edit + `os.WriteFile` is race-free. No need for `saveRegistryLocked`'s rename-into-place; this is test-side fixture handling.

### Out of scope

- **Idle-eviction → restart → re-evict cycles** — covered by `idle_test.go` and `restart_test.go`. Per AC, this test does not exercise the eviction timer.
- **The unit-level load-layer test** — already shipped in PR #204 (`internal/sessions/pool_test.go:943`). This e2e is additive at a different layer, not a replacement.
- **Friendlier `pyry status` rendering for un-initialised supervisors** — a separate UX ticket per #202 spec § "Status command's zero-time tell". The negative-grep on `0001-01-01T00:00:00Z` here is the *failure-mode signature*, not a UX assertion.
- **Defensive `install.sh` post-install check** — out of scope per ticket body, tracked separately.

## Testing strategy

The two new tests *are* the testing strategy. CI runs them on every push and PR via `go test -tags=e2e ./internal/e2e/...` (existing target). Pre-fix behaviour against a hypothetical revert of #204:

- Test 1 fails: the 5 s deadline expires with `Phase: starting` and `Started at: 0001-01-01T00:00:00Z`. The fail message includes the last `pyry status` stdout — instantly recognisable as the v0.10.1 hang signature.
- Test 2 passes (the bootstrap-only carve-out is unrelated to the non-bootstrap path; reverting #204 does not touch non-bootstrap warm-load).

Post-fix behaviour (current main):

- Test 1 passes within ~1-2 s (the time for `Pool.Run → Session.Run → runActive → Supervisor.Run → spawn(/bin/sleep) → Phase=running`).
- Test 2 passes immediately after warm-load reconciliation (~50-100 ms).

Test 2 is the structural canary for the carve-out: a future change that "simplifies" the load layer to ignore `lifecycle_state` for *all* sessions breaks it, even though Test 1 still passes. Both tests together pin the precise boundary ADR 016 documents.

### Manual verification

`go test -tags=e2e -run TestE2E_BootstrapWarmStart -v ./internal/e2e/` against current main passes both tests; against a `git revert <#204-fix-commit>` build, Test 1 fails with the regression signature in the failure message.

## Open questions

None. The design is a pure additive: one new file in `internal/e2e/`, all helpers reused, no production-code changes, no harness extensions. The two acceptance-criteria cases map 1:1 onto the two test functions; the assertion vocabulary is borrowed verbatim from `idle_test.go` and `restart_test.go`.
