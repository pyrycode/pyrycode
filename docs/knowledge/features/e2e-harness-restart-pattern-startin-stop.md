# Restart Pattern (`StartIn` + `Stop`)

`StartIn` + `Stop` together let a test prove on-disk invariants survive
daemon restart: pre-populate `HOME` → `Start` → `Stop` → second `StartIn`
against the same `HOME` → assert the file directly.

```go
home, err := os.MkdirTemp("", "pyry-rs-*")
if err != nil { t.Fatalf("mkdir home: %v", err) }
t.Cleanup(func() { _ = os.RemoveAll(home) })

regDir := filepath.Join(home, ".pyry", "test")
_ = os.MkdirAll(regDir, 0o700)
_ = os.WriteFile(filepath.Join(regDir, "sessions.json"), []byte(registryJSON), 0o600)

h1 := e2e.StartIn(t, home)
h1.Stop(t)

h2 := e2e.StartIn(t, home) // same socket path, same registry; reads back the pre-write
_ = h2
// Inspect the registry file at <home>/.pyry/test/sessions.json directly.
```

### Why `os.MkdirTemp` instead of `t.TempDir()` for the HOME

Unix sockets cap `sun_path` at 104 bytes on macOS (108 on Linux).
`t.TempDir()` embeds the (long) test name into its path; for tests with
descriptive names (e.g. `TestE2E_Restart_PreservesActiveSessions`) the
appended `pyry.sock` overflows the limit. `os.MkdirTemp("", "pyry-rs-*")`
keeps the prefix tiny. Tests using `Start(t)` (short name or short dir) are
unaffected; the restart test's tighter budget motivates the explicit
`os.MkdirTemp` + `t.Cleanup(os.RemoveAll)`. See `lessons.md § Unix-socket
sun_path limits and t.TempDir()`.

### Why the same socket path works across the two spawns

`StartIn` derives `socket := filepath.Join(home, "pyry.sock")` — both
spawns use the same path. The second daemon's `Server.Listen`
(`internal/control/server.go`) handles a stale socket file via dial-probe
→ ECONNREFUSED → `os.Remove` → `net.Listen`; no test-level coordination
needed. By the time `Stop` returns, `cmd.Wait` has reaped the first
process, the listener fd is closed, and ECONNREFUSED is deterministic.
The defensive `os.Remove(h.SocketPath)` in teardown belt-and-suspenders
the SIGKILL path.

### Idempotency invariant

`cleanupOnce` (a `sync.Once`) guards a single teardown. Whichever fires
first — explicit `Stop(t)` or `t.Cleanup`'s deferred call — wins; the
other is a no-op. Two harnesses (`h1`, `h2`) own independent
`cleanupOnce` / `doneCh` / `cmd`; `t.Cleanup` runs LIFO, so `h2.teardown`
fires first against the live second daemon, then `h1.teardown` (no-op,
already torn down via `Stop`).

### `restart_test.go` — three restart-survival tests

Three tests live in `restart_test.go`, all built on the same `StartIn → Stop
→ StartIn` cycle against a pre-populated `<HOME>/.pyry/test/sessions.json`:

| Test | Ticket | Asserts |
|---|---|---|
| `TestE2E_Restart_PreservesActiveSessions` | #106 | registry file present after first `Stop`; `version` preserved; session count preserved; per-session `lifecycle_state` and `bootstrap` flag preserved |
| `TestE2E_Restart_PreservesEvictedSessions` | #107 | a non-bootstrap entry pre-written with `lifecycle_state: "evicted"` is still `"evicted"` after restart (no silent warm-promotion); paired with bootstrap-active and a non-bootstrap-active control so "evicted stays evicted" is meaningful next to a sibling that's provably not evicted |
| `TestE2E_Restart_LastActiveAtSurvives` | #107 | three sessions with `lastActiveAt` values spread by 10 min and 1 hour roundtrip across restart via `time.Time.Equal` (catches a re-stamp to `time.Now()` that would silently break the cap-policy LRU order) |

Deliberately **not** asserted by any of them: byte-identity of the file
(coupling to `MarshalIndent` output inverts the dependency direction — a
benign formatting change would break the tests). The first test also
deliberately omits `LastActiveAt` equality; that property is the dedicated
subject of the third test.

#### Helper: `newRegistryHome` (rule of three)

Once #107 landed, all three tests share the same four-line HOME bootstrap
(`os.MkdirTemp` for sun_path safety, `t.Cleanup(RemoveAll)`, `mkdir -p
<home>/.pyry/test`). #107 extracted this into a file-local helper —
package-internal, intentionally not promoted to `harness.go`'s public
surface (three callers ≠ a public API):

```go
// newRegistryHome creates a short-named temp HOME (sun_path-safe), pre-creates
// <home>/.pyry/test/, registers cleanup, and returns the home dir and the
// sessions.json path the harness's -pyry-name=test daemon will read.
func newRegistryHome(t *testing.T) (home, regPath string)
```

`registryEntry` / `registryFile` mirror types and the `writeRegistry` /
`readRegistry` / `mustReadFile` helpers from #106 stay file-local and
unchanged — same dependency-direction reasoning (importing the unexported
production schema solely for tests would invert it).

#### Fixture choice: bootstrap-active anchors every restart test

Each restart test pre-writes exactly one `bootstrap: true, lifecycle_state:
"active"` entry alongside the entries it cares about. The bootstrap-active
anchor keeps the harness's ready gate working the conventional way: the
supervisor spawns `/bin/sleep infinity`, the control server comes up, the
ready-poll succeeds. The bootstrap-evicted permutation — "daemon comes up
cleanly with an evicted bootstrap on disk" — is functionally distinct
(pre-fix would enter `runEvicted` instead of spawning the child), so it
lives in its own file as `bootstrap_warm_start_test.go` (#253, see below).
The three restart tests stay scoped to non-bootstrap survival; failures
isolate cleanly between the two files.

The lifecycle strings written to disk are `"active"` and `"evicted"` —
exactly what `lifecycleState.String()` (`internal/sessions/session.go`)
emits and `parseLifecycleState` parses. Don't invent or guess values; the
production code is the source of truth.

#### Equality, not byte-identity, for `LastActiveAt`

`TestE2E_Restart_LastActiveAtSurvives` uses `time.Time.Equal` per entry,
not byte-equal on the file:

- **What `Equal` accepts.** Today's roundtrip is byte-exact for any UTC,
  monotonic-stripped `time.Time`. `Equal` also tolerates a future
  re-encode through `time.Now().UTC()` (which strips monotonic but
  preserves wall time) — the AC's "tight tolerance".
- **What `Equal` rejects.** A re-stamp to `time.Now()` produces a delta of
  seconds-to-hours against the 10-min and 1-hour pre-write offsets;
  `Equal` rejects loudly. The 10-min / 1-hour spread is far larger than
  any plausible JSON-roundtrip drift or test wall-clock.
- **Monotonic-clock trap.** The "want" values are obtained by re-reading
  the file with `readRegistry` *after* `writeRegistry`, not by reusing the
  in-memory pre-write struct. `time.Time` written via `MarshalIndent`
  retains monotonic-clock state in the original Go value but strips it
  after the JSON unmarshal trip the daemon takes. Comparing pre-write
  in-memory vs. post-restart parsed would diverge on monotonic alone even
  though the bytes on disk are identical. See `lessons.md § JSON
  roundtrip strips monotonic-clock state from time.Time`.

Cross-axis combinations (lifecycle × timestamp survival in one test) are
not the AC's ask and would confuse failure isolation. Each test pins one
invariant.

#### Why this works against today's pyry without behaviour changes

The restart-time code path against a pre-populated registry is:
`loadRegistry` reads → `pickBootstrap` selects the lone `bootstrap: true`
entry; non-bootstrap entries are *not* materialised into `Pool.sessions` →
`reg != nil` skips the cold-start save → (pre-#839: `reconcileBootstrapOnNew`
no-op'd here because `~/.claude/projects/<encoded-cwd>` doesn't exist under
the test HOME; post-#839 this step is gone entirely — the bootstrap resolves
its `--session-id` deterministically from the registry, no scan) →
bootstrap enters `runActive`, idle timer disabled → SIGTERM
cancels ctx → `runActive` returns `ctx.Err` *before* `transitionTo
(stateEvicted)`, so no terminal save fires. Net: nothing in pyry calls
`saveLocked` between pre-write and the second `loadRegistry`. The non-
bootstrap entries persist on disk *because pyry doesn't touch them*, not
because pyry materialises them — that is the realistic-today shape of the
guarantee, and the test locks in the no-save-without-state-change
invariant. Future tickets that materialise non-bootstrap entries will need
to preserve their lifecycle state across restart explicitly; this test
will then catch any regression.

#### File split rationale

Lives in its own `restart_test.go` rather than extending
`cli_verbs_test.go`. The latter is *CLI surface coverage* (one test per
shipped verb); this test is *daemon-level disk-state survival* and doesn't
drive a CLI verb. Mirrors the `harness_test.go` (mechanics) vs.
`cli_verbs_test.go` (verb surface) split #52 established.

The local `registryFile` / `registryEntry` mirror types are duplicated
intentionally — `internal/sessions`'s on-disk types are unexported, and
exporting them solely for one test would invert the dependency direction.
The schema is small and stable; if a field is added, the mirror grows it
too.

### `bootstrap_warm_start_test.go` — bootstrap warm-start carve-out (#253)

Two tests pin [ADR 016](../decisions/016-bootstrap-ignores-persisted-lifecycle-state.md)'s
load-layer carve-out at the e2e tier. `Pool.New` forces the bootstrap to
warm-load as `stateActive` regardless of the persisted `lifecycle_state`,
because nothing in supervisor-mode startup drives `Pool.Activate` on it
(non-TTY stdin: launchd / systemd / piped wrapper). Non-bootstrap sessions
keep their persisted state — lazy respawn on attach is the driver there.

- **`TestE2E_BootstrapWarmStart_IgnoresEvictedOnDisk`** drives the
  v0.10.1 regression class end-to-end. Two-phase shape: (Phase A)
  `StartIn` cold so the daemon picks its own bootstrap UUID and writes
  the registry, `waitForBootstrap` to capture it, `Stop`; (Phase B) plain
  `readRegistry` / mutate `LifecycleState = "evicted"` on the bootstrap
  entry / `writeRegistry` — race-free because no daemon is running
  between the two `StartIn`s; (Phase C) `StartIn` warm against the
  mutated registry, poll `pyry status` for `Phase: running` within 5 s
  (matches the harness's `readyDeadline`; pre-fix the daemon parks
  forever so any reasonable deadline fires). Negative-greps the v0.10.1
  failure-mode signatures: `Started at:    0001-01-01T00:00:00Z` and
  `Uptime:        2562047h47m16` (prefix-match because
  `Duration.Round(time.Second)` of `math.MaxInt64` overflows back to the
  input — the rendered sentinel is the full `2562047h47m16.854775807s`,
  but matching the prefix keeps the assertion robust to a future change
  that clamps before rounding). Cross-checks the wire view via
  `pyry sessions list --json` decoded into `[]control.SessionInfo` and
  asserts the bootstrap entry's `state == "active"` (`--json`, not the
  table form, so the assertion rides a stable wire field rather than
  tabwriter alignment).
- **`TestE2E_BootstrapWarmStart_NonBootstrapEvictedPersists`** pins the
  carve-out boundary: the load-layer special-case is bootstrap-only.
  Single-phase — pre-seed two entries (bootstrap as `active`,
  non-bootstrap as `evicted`), `StartIn`, then `waitForSessionState` on
  the non-bootstrap UUID for `"evicted"` (5 s envelope absorbs the
  warm-load reconciliation pass). Disk is the canonical observation
  point because non-bootstrap sessions live on disk between minting and
  the next consumer-driven `Activate`. Cross-checks the bootstrap stays
  `active` via `pyry sessions list --json`. A future refactor that
  over-corrects the bootstrap fix into "ignore evicted for *all*
  sessions" trips this test even though Test 1 still passes.

#### Why two-phase (not pre-seeded only) for Test 1

A pre-seeded registry with `bootstrap=true, lifecycle_state="evicted"` and
a single `StartIn` reproduces the bug structurally but not the user's
trigger path (machine boots → daemon starts → idle eviction fires →
daemon restarts via launchd / systemd / `pyry update` → hang). The
two-phase shape (cold start → mutate → warm start) is faithful to that
flow and lets the bootstrap UUID be daemon-authored rather than
test-invented. The mutation step edits `lifecycle_state` only, leaving
`UUID` / `created_at` / `last_active_at` / `bootstrap` intact —
minimum-fidelity stand-in for "idle eviction had a chance to fire."

The alternative (drive the cold daemon with `-pyry-idle-timeout=1s`,
wait for the eviction, stop, restart) works but adds 1–2 s of timer
waiting per run and couples the regression test to the idle-eviction
timing. The two-phase mutate stays decoupled — the regression is in the
warm-load layer, not the eviction layer.

#### No new helpers

All scaffolding reused verbatim: `newRegistryHome`, `writeRegistry`,
`readRegistry`, `mustReadFile` from `restart_test.go`;
`waitForBootstrap` from `cap_test.go`; `waitForSessionState` from
`cap_test.go`. `registryFile` / `registryEntry` mirrors are package-
internal under `package e2e` and are usable directly from the new file.
Zero harness extensions, zero production-code changes.

### `conv_sweep_test.go` — conversations sweep loop e2e (#263)

One test —
`TestE2E_ConvSweep_RemovesUnpromotedKeepsPromoted` — closes the gap that
`internal/sessions.TestPool_Run_RegistersSweepLoop_HappyPath` cannot
reach: the in-package test exercises `Pool.Run`'s `if p.convReg != nil`
arm directly with already-set `pool.convReg` / `pool.convRegistryPath`
fields, but a regression in `cmd/pyry/main.go`'s `sessions.Config`
construction (e.g. forgetting to wire `ConversationsRegistry` /
`ConversationsRegistryPath`) would leave `p.convReg == nil` silently and
still pass. Same regression class as v0.10.1's hang — daemon-wiring bugs
that unit tests cannot see.

The test seeds two conversations with `LastUsedAt = time.Now().UTC().Add
(-60 * 24 * time.Hour)` (well past the 30-day archive threshold) into
`<home>/.pyry/test/conversations.json` via the canonical
`conversations.Registry` writer — one promoted (`IsPromoted: true`), one
unpromoted. Then `StartIn(t, home, "-pyry-conv-sweep-interval=100ms")`
spawns pyry with the `#262` flag, polls the on-disk file via
`conversations.Load(convPath)` every 50 ms with a 5 s deadline until
`len(loaded.List()) == 1`, asserts the survivor is the promoted entry
(by ID and `IsPromoted` flag), then `h.Stop(t)` drives a SIGTERM and
asserts (a) `processAlive(pid) == false` after Stop returns and (b)
no `panic` / `runtime/` / `goroutine ` substring in `h.Stderr`.

#### Why seed via `conversations.Registry`, not raw JSON

`restart_test.go` mirrors the (unexported) sessions-registry shape
locally because it has no other choice. This test does have a choice —
the `conversations` package's `Registry` / `Conversation` / `Load` /
`Save` / `Create` / `List` are all exported — and using the canonical
writer kills two failure modes at once: (a) field-tag drift between a
test's mirror struct and production, (b) atomic-write semantics (the
seed file lands via the same temp+rename rename the daemon will use, not
via raw `os.WriteFile`). Side benefit: the seed file exercises the same
`Save` path that the sweep itself will exercise on tick — the test's
"before" and "after" use the same on-disk codec.

#### Polling cadence — 50 ms gap, 5 s deadline

50 ms poll gap against the 100 ms tick gives ~10 chances inside the 5 s
budget. Larger gaps risk flaky misses on a slow CI runner; smaller gaps
add no signal. The `time.NewTicker` inside `RunSweepLoop` does NOT fire
immediately — first tick is at `+interval` (~100 ms after `Pool.Run`
registers the goroutine), well inside the 5 s envelope. If the test
starts flaking on heavily-loaded macOS CI runners, the right fix is to
raise the budget (e.g. 10 s), NOT to lower the tick interval — the
daemon's `time.NewTicker` cadence is what's being measured, and a sub-
100 ms interval would start interacting with the runner's scheduler
granularity.

#### Why `h.Stop` plus `processAlive` plus stderr scan, not just `h.Stop`

`h.Stop(t)` is the harness's blessed graceful shutdown path: SIGTERM →
3 s grace → SIGKILL → 1 s grace. The 4 s upper bound sits comfortably
under AC#4's 5 s budget, so no custom shutdown helper is needed. Two
follow-up assertions cover the failure modes Stop alone doesn't fail
on: (a) Stop hit the killGrace path with `doneCh` still open (Stop only
`t.Logf`'s that case, doesn't fail) — `processAlive(pid)` catches it,
and (b) the daemon panicked on its way down (Stop doesn't inspect
stderr) — the panic / `runtime/` / `goroutine ` substring scan, lifted
verbatim from `cli_verbs_test.go`'s vocabulary, catches it. Neither
case is hypothetical — (a) is the regression class this whole test
exists for; (b) is the v0.10.1 incident shape.

#### What the test does NOT assert

No "fresh-and-unpromoted control" entry to prove the predicate is
`IsPromoted`-aware AND `LastUsedAt`-aware in the same test — that's
already covered by the in-package `pool_conv_sweep_test.go` (which
seeds `archivable=2, fresh=1` against the same predicate). This e2e
exists to cover the daemon-wiring gap, not to re-assert predicate
semantics. No goroutine-leak assertion either — AC#4 explicitly says
"a clean process exit is sufficient evidence." Adding `runtime
.NumGoroutine()` checks would require probing inside the daemon
process from out-of-process, which we don't have access to.

#### No new helpers

All scaffolding reused: `newRegistryHome` from `restart_test.go` (its
sessions-registry path return value is intentionally discarded — we
want only the `<home>/.pyry/test/` mkdir for `conversations.json`'s
parent); `mustReadFile` from `restart_test.go` for the polling-timeout
diagnostic dump; `processAlive` from `harness_test.go`; the panic /
`runtime/` / `goroutine ` substring vocabulary from `cli_verbs_test.go`.
The seed and the post-sweep readback both go through
`conversations.Load` / `Save`, not local JSON helpers. Zero harness
extensions, zero production-code changes.
