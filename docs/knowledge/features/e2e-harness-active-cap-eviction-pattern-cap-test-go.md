# Active-Cap Eviction Pattern (`cap_test.go`, #116)

Two tests in `internal/e2e/cap_test.go` (build tag `e2e`) close the
binary-boundary gap on the concurrent active cap (#41). The package-level
race test (`TestPool_ActiveCap_RaceConcurrentActivate`) had to switch to
Bridge mode to avoid PTY contention; #116 verifies the LRU policy at the
binary boundary against real PTY-backed children, plus the cap+idle
interleave that two policies sharing `Session.Evict` couldn't otherwise
exercise together.

| Test | Asserts |
|---|---|
| `TestE2E_ActiveCap_EvictsLRU` | with `-pyry-active-cap=2`, three `sessions.new` calls (α, β, γ) cap-evict the right LRU peer each time: bootstrap → α → (β, γ remain) |
| `TestE2E_ActiveCap_IdleInterleave` | with `-pyry-active-cap=2 -pyry-idle-timeout=2s`, β's mint cap-evicts bootstrap (Phase 1); α then idle-evicts ~2s later while β is still active (Phase 2); β eventually idle-evicts too (state-machine wedge check) |

Three production wirings the tests consume (all in `cmd/pyry/main.go`):

1. **`-pyry-active-cap=N`** flag (default `0` = uncapped) flowing into `sessions.Config.ActiveCap`. Negative values map to "unset" via `Pool.New`'s contract; no validation in `runSupervisor`.
2. **`pool` as `control.NewServer`'s `Sessioner` argument** (was `nil` in #75's wire-only slice). With this wired, the existing `sessions.new` verb actually mints sessions; without it the test driver gets `"sessions.new: no sessioner configured"`.
3. **Registration in `pyryFlagValues`** so `splitArgs` keeps `-pyry-active-cap` on pyry's side instead of forwarding it to claude.

### Tiny shell-script claude stand-in

`Pool.Create` constructs new-session args as `append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))`. With the harness default `claudeBin=/bin/sleep` (pre-#918) and `claudeArgs=["99999"]`, the new-session exec was `/bin/sleep 99999 --session-id <uuid>` — both BSD and GNU `sleep(1)` reject the unknown arg and exit with the usage banner. The supervisor would crash-loop the child; lifecycle state would still flip to `stateActive` (Pool tracks lifecycle independently of supervisor health), so cap-evict logic is uncoupled from this — but it generates noisy stderr and risks racing the supervisor backoff window against assertions.

The fix: a two-line shell script written by the test as `<home>/sleep-claude.sh`, made executable, passed via `-pyry-claude=<path>` through `StartIn`'s variadic `extraFlags`:

```sh
#!/bin/sh
exec sleep 99999
```

Both bootstrap (`<script> 99999`) and new sessions (`<script> 99999 --session-id <uuid>`) `exec sleep 99999` and idle until SIGTERM. Preferred over extending `internal/e2e/internal/fakeclaude` (rotation-test binary) with a "no-rotation" mode — would mix concerns; the script is two lines.

### File-local helpers — three new, one polling vs. one-shot

Three helpers introduced by #116, plus `writeSleepClaude` (moved to `harness.go` by #918 — see § Default Stand-In Argv Tolerance):

- **`writeSleepClaude(t, home) string`** — writes the script and returns its absolute path. Originally file-local to `cap_test.go`; relocated verbatim into `harness.go` by #918 so `spawnWith`'s default path can call it too. Every existing call site is unaffected — same package symbol.
- **`waitForBootstrap(t, regPath, timeout) string`** — polls the registry for the entry with `Bootstrap == true`, returns its `ID`. Tolerates the registry file not yet existing (Pool's first save races the readiness gate). Distinct from `waitForBootstrapState` (#115) which polls a *known* id for a *specific* state — `waitForBootstrap` discovers the id.
- **`waitForSessionState(t, regPath, id, want, timeout)`** — polls until the entry with `id` has `lifecycle_state` matching `want` (`"evicted"` or `"active"`). The `"active"` arm tolerates either an empty/missing field (today's `omitempty` default) or the literal `"active"` — same convention as `waitForBootstrapState`.
- **`assertActive(t, regPath, id)`** — one-shot registry checkpoint; fails if the session is `"evicted"` *right now*. Distinct from `waitForSessionState(..., "active", ...)` which returns on first observation; `assertActive` is for "X must be true at this exact moment", not "X eventually becomes true". Used in the interleave test's Phase 1/Phase 2 gate to catch a session that flips to active then evicts before the next poll.

`waitForBootstrap`, `waitForSessionState`, and `assertActive` stay file-local to `cap_test.go` — rule of three, and none of them are needed from the default spawn path. Only `writeSleepClaude` (and its `sleepClaudeScript` const) moved.

### Why three `sessions.new` calls in `EvictsLRU`, not two

AC asks for "creates three sessions". Bootstrap is the fourth. The third call (γ) is the one whose LRU pick is non-trivial — it must skip β (just activated) and evict α (older). Two calls would only test "the new session evicts the oldest active peer once"; three exercises the LRU comparison across two non-bootstrap sessions, the regression-shaped failure mode the package-level race test (Bridge mode, no PTY) couldn't hit at the binary boundary.

The 50ms inter-call sleep is for **timestamp distinguishability**, not race avoidance. `Pool.pickLRUVictim` ranks by `lastActiveAt`; identical timestamps on a fast clock would make the LRU pick non-deterministic.

### Interleave timing — 1s gap between idle fires

`-pyry-idle-timeout=2s` + 1s wait between α and β activations. Idle timers are one-shot, armed at `runActive` entry (effectively at the Activate moment). The 1s gap between activations becomes a 1s gap between idle fires. That window is the test's CI slack — bigger than typical Go test scheduler jitter (~100ms) and the registry poll cadence (50ms). Don't shrink past 2s; the AC says "wait past idleTimeout" and 2s is the minimum useful value. If CI flakes, raise to idle=3s + wait=1.5s.

### Why no `pyry status` cross-check

`VerbStatus` resolves to the bootstrap session today (`s.sessions.Lookup("")`). For `EvictsLRU` the bootstrap is cap-evicted, so `pyry status` reports a non-running phase — same shape as #115's idle test. Including it would re-test the same surface as the registry assertion; skipped to keep the test focused.

### Production diff is small but non-zero

`cmd/pyry/main.go` gains ~7 LOC: the new flag, its wire-in to `sessions.Config.ActiveCap`, the `pyryFlagValues` registration, and the `nil → pool` swap on `control.NewServer` with the comment block rewritten. Test diff ~204 LOC, single new file. Default `go test ./...` unaffected.
