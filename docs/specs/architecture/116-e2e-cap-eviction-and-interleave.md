---
ticket: 116
title: Multi-session lifecycle e2e — cap eviction + idle/cap interleave
status: spec
size: S
---

# Files to read first

Read these before doing any exploration. They are the load-bearing surfaces this spec composes; reading them up front is cheaper than rediscovering them via grep.

- `cmd/pyry/main.go:248-310` — flag block in `runSupervisor`, the `sessions.New(sessions.Config{...})` literal, and the `control.NewServer(... nil)` call. New flag and the two wirings (`ActiveCap`, `sessioner`) land in this region.
- `internal/sessions/pool.go:75-93` — `Config.ActiveCap` doc and contract (zero = uncapped, treats `<=0` as unset). The wire-in must preserve this — `-pyry-active-cap=0` (default) maps to today's behaviour byte-for-byte.
- `internal/sessions/pool.go:803-882` — `Pool.Create`. New-session args are `append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))` — load-bearing for the supervised-child choice (see "Supervised child selection" below).
- `internal/sessions/pool.go:1004-1083` — `Pool.Activate` + `pickLRUVictim`. The cap-bind/evict path the test exercises. `pickLRUVictim` excludes the target and returns nil when `active < activeCap`.
- `internal/sessions/session.go:281-330` — `runActive`'s idle timer. The timer is **one-shot** (armed once at `runActive` entry, reset only when it fires while `attached>0`). `touchLastActive` does not reset it — relevant for the interleave test's timing.
- `internal/control/client.go:94-118` — `SessionsNew(ctx, socketPath, label) (string, error)`. The test driver. One-shot dial; the server side has `sessioner` configured by this ticket.
- `internal/control/server.go:33-122` — existing `Sessioner` interface and `NewServer` parameter list. `*sessions.Pool` satisfies `Sessioner` directly (per #75 spec § "No adapter needed"); main.go's `nil` becomes `pool`.
- `internal/e2e/harness.go:160-220,315-390` — `StartIn` variadic `extraFlags`, `spawn`, `spawnOpts`. `-pyry-active-cap=N` and `-pyry-claude=<script>` flow through `extraFlags` last-wins.
- `internal/e2e/idle_test.go` — full file; reuses `waitForBootstrapState` and the registry-poll pattern. New tests use the same shape.
- `internal/e2e/restart_test.go:13-49,117-148` — `registryEntry` / `registryFile` types, `newRegistryHome`, `readRegistry`, `mustReadFile`. Reused verbatim by the new test file.
- `docs/specs/architecture/41-concurrent-active-cap.md` — full spec. Cap policy, capMu serialisation, LRU victim picking. Confirms cap-evict transitions the victim through `Session.Evict` → `stateEvicted` (same on-disk write as idle).
- `docs/specs/architecture/40-idle-eviction-lazy-respawn.md` — full spec. Idle timer arming and the `lifecycle_state == "evicted"` write rule.
- `docs/specs/architecture/75-control-sessions-new.md:188-225` — confirms `*sessions.Pool` satisfies `control.Sessioner` directly; the "wire pool with one line" call site is `cmd/pyry/main.go`'s `control.NewServer`.

# Context

#41 landed the concurrent-active cap with LRU eviction. Its package-level race test (`TestPool_ActiveCap_RaceConcurrentActivate` in `internal/sessions/pool_cap_test.go`) had to switch to Bridge mode to avoid PTY contention; binary-level behaviour against a real PTY-backed child is uncovered. #40's idle eviction has package-level coverage and a sibling e2e (#115). What's missing is binary-boundary cap coverage and binary-boundary coverage of the cap+idle interleave — two policies sharing the same `Session.Evict` primitive.

This ticket adds two e2e tests against a real `pyry` daemon, real `internal/sessions` lifecycle goroutines, real `internal/control` server, real on-disk `sessions.json`. Sessions are minted via the `sessions.new` control verb shipped in #75; the harness drives it through `control.SessionsNew` (no `pyry sessions new` CLI required — that's #76, still in flight).

Two scaffolding gaps are closed by this ticket:

1. **`-pyry-active-cap` flag.** `pyry` exposes `-pyry-idle-timeout` (#40) but no flag for `Config.ActiveCap`. Add `-pyry-active-cap=N` (default `0` = uncapped, byte-for-byte today's behaviour).
2. **`Sessioner` wiring.** #75's spec leaves `cmd/pyry/main.go` passing `nil` as the last `NewServer` arg, with the wire-in deferred to #76. Without it, the e2e test's `SessionsNew` call gets `Response.Error == "sessions.new: no sessioner configured"`. This ticket needs functioning `sessions.new`, so it wires `pool` (the `*sessions.Pool` already constructed at the previous statement) as the `sessioner` argument. One line.

#76 (the CLI verb on top) will need to skip its own line of wiring once it lands; trivial conflict (one-line removal). Documented under "Coordination with #76" below.

# Design

## Approach

One file modified (`cmd/pyry/main.go` — flag + two wirings) and one file added (`internal/e2e/cap_test.go` — two tests + one helper). No harness changes (the variadic-flags hook on `StartIn` is already in tree from #115).

## Production change — `cmd/pyry/main.go`

Three edits, all clustered in `runSupervisor`:

1. **New flag.** Slot beside `-pyry-idle-timeout`:

   ```go
   idleTimeout := fs.Duration("pyry-idle-timeout", 15*time.Minute, "evict idle claudes after this duration (0 disables)")
   activeCap   := fs.Int("pyry-active-cap", 0, "max concurrently active claudes (0 = uncapped)")
   ```

2. **Wire `ActiveCap` into `sessions.Config`:**

   ```go
   pool, err := sessions.New(sessions.Config{
       Logger:            logger,
       RegistryPath:      registryPath,
       ClaudeSessionsDir: claudeSessionsDir,
       IdleTimeout:       *idleTimeout,
       ActiveCap:         *activeCap, // NEW
       Bootstrap:         sessions.SessionConfig{ /* unchanged */ },
   })
   ```

3. **Wire `pool` as `Sessioner`.** Replace the trailing `nil` and the "intentionally nil here" comment block:

   ```go
   // Pool satisfies control.Sessioner directly — Pool.Create returns
   // sessions.SessionID, matching Sessioner.Create's signature with no
   // adapter (contrast with poolResolver for the read-side Lookup).
   ctrl := control.NewServer(socketPath, poolResolver{pool}, logRing, cancel, logger, pool)
   ```

   The "intentionally nil here" comment block above the call goes away — its rationale (deferred wiring) is satisfied.

**Validation in `runSupervisor` is unnecessary.** Negative values map to "unset" via `pool.go`'s contract (`<=0` → uncapped). `flag.Int` accepts negatives without error; `Pool.New` documents the treatment. No prefix check needed; matches `-pyry-idle-timeout=-1s`'s no-special-handling precedent.

## Supervised child selection

`Pool.Create` constructs new-session args as `append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))`. With the harness default `claudeBin=/bin/sleep` and `claudeArgs=["99999"]`, the new-session exec is `/bin/sleep 99999 --session-id <uuid>` — both BSD and GNU `sleep(1)` reject that (BSD: `usage: sleep seconds`; GNU: unknown option). The supervisor would crash-loop the child. Lifecycle state would still flip to `stateActive` (Pool tracks lifecycle independently of supervisor health), so cap-evict logic is uncoupled from this — but the test would generate noisy stderr and risk flake on the supervisor backoff window racing assertions.

**Solution: a tiny shell-script `claude` stand-in that ignores its args.**

```sh
#!/bin/sh
exec sleep 99999
```

Written to `<home>/sleep-claude.sh` by the test, made executable, passed via `-pyry-claude=<path>` in the `StartIn` extraFlags. Both bootstrap (`<script> 99999`) and new sessions (`<script> 99999 --session-id <uuid>`) `exec sleep 99999` and idle until SIGTERM.

This is preferable to extending the existing `internal/e2e/internal/fakeclaude` binary with a "no-rotation" mode (which would mix concerns — that binary exists for rotation tests). The script is two lines; embed it in the test file as a string constant.

## Test 1 — `TestE2E_ActiveCap_EvictsLRU`

```go
func TestE2E_ActiveCap_EvictsLRU(t *testing.T) {
    home, regPath := newRegistryHome(t)
    claudeBin := writeSleepClaude(t, home)
    h := StartIn(t, home,
        "-pyry-active-cap=2",
        "-pyry-claude="+claudeBin,
    )

    // Bootstrap is the first active session; capture its ID for later
    // assertions. readRegistry tolerates the file already being on disk;
    // pyry writes it during pool init.
    bootstrapID := waitForBootstrap(t, regPath, 5*time.Second)

    // Create α — count goes 1 → 2; no cap-evict.
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    alpha, err := control.SessionsNew(ctx, h.SocketPath, "alpha")
    if err != nil {
        t.Fatalf("sessions.new alpha: %v", err)
    }

    // 50ms gap so lastActiveAt timestamps are distinguishable. Pool's
    // pickLRUVictim ranks by lastActiveAt; identical timestamps would
    // make the LRU choice non-deterministic on fast clocks.
    time.Sleep(50 * time.Millisecond)

    // Create β — count would go 3 → cap-evict LRU = bootstrap.
    beta, err := control.SessionsNew(ctx, h.SocketPath, "beta")
    if err != nil {
        t.Fatalf("sessions.new beta: %v", err)
    }

    time.Sleep(50 * time.Millisecond)

    // Create γ — count would go 3 → cap-evict LRU = α.
    gamma, err := control.SessionsNew(ctx, h.SocketPath, "gamma")
    if err != nil {
        t.Fatalf("sessions.new gamma: %v", err)
    }

    // Final state: bootstrap evicted, α evicted, β active, γ active.
    waitForSessionState(t, regPath, bootstrapID, "evicted", 3*time.Second)
    waitForSessionState(t, regPath, alpha, "evicted", 3*time.Second)
    waitForSessionState(t, regPath, beta, "active", 3*time.Second)
    waitForSessionState(t, regPath, gamma, "active", 3*time.Second)
}
```

**Why three `sessions.new` calls (four sessions total) instead of two.** AC#2 says "creates three sessions (via `sessions.new` per dependency below)" — three via the verb. Bootstrap stays as the fourth. The third call (`gamma`) is the one whose LRU pick is non-trivial: it must skip β (just activated) and evict α (older). Two calls would only test "the new session evicts the oldest active peer once"; three exercises the LRU comparison across two non-bootstrap sessions, which is the regression-shaped failure mode the package-level race test could not hit at the binary boundary.

**`bootstrapID` capture.** The bootstrap UUID is generated by Pool on first start; tests can't predict it. `waitForBootstrap` polls the registry for the entry with `Bootstrap == true` and returns its `ID`. Defined in this file (file-local).

## Test 2 — `TestE2E_ActiveCap_IdleInterleave`

```go
func TestE2E_ActiveCap_IdleInterleave(t *testing.T) {
    home, regPath := newRegistryHome(t)
    claudeBin := writeSleepClaude(t, home)
    h := StartIn(t, home,
        "-pyry-active-cap=2",
        "-pyry-idle-timeout=2s",
        "-pyry-claude="+claudeBin,
    )

    bootstrapID := waitForBootstrap(t, regPath, 5*time.Second)

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    // T+ε: create α. count = 2 (bootstrap, α). No cap-evict yet.
    alpha, err := control.SessionsNew(ctx, h.SocketPath, "alpha")
    if err != nil {
        t.Fatalf("sessions.new alpha: %v", err)
    }

    // Brief wait — 1s. Spaces α's idle timer (armed at α-activate) and
    // β's idle timer (armed at β-activate) by ~1s, so they fire ~1s
    // apart. Within that window we observe one evicted, one still active.
    time.Sleep(1 * time.Second)

    // T+~1s: create β. count would be 3 → cap-evict LRU = bootstrap
    // (lastActiveAt = T0; older than α's T0+ε).
    beta, err := control.SessionsNew(ctx, h.SocketPath, "beta")
    if err != nil {
        t.Fatalf("sessions.new beta: %v", err)
    }

    // Phase 1 — cap-evict observed. Bootstrap goes to evicted while α
    // and β stay active. Observable window: between β's activation
    // (~T0+1s) and α's idle fire (~T0+ε+2s ≈ T0+2s) — ~1s.
    waitForSessionState(t, regPath, bootstrapID, "evicted", 2*time.Second)
    assertActive(t, regPath, alpha)
    assertActive(t, regPath, beta)

    // Phase 2 — idle-evict of the surviving non-most-recent session. α's
    // idle timer fires at ~T0+ε+2s; β's at ~T0+1s+ε+2s = T0+3s+ε. Wait
    // until α has evicted but β has not — the "1s gap" between their
    // idle fires.
    waitForSessionState(t, regPath, alpha, "evicted", 3*time.Second)
    assertActive(t, regPath, beta)

    // Optional final cleanup — let β idle-evict too and verify the
    // pool ends in an all-evicted state. Not strictly required by the
    // AC but pins the absence of a state-machine wedge.
    waitForSessionState(t, regPath, beta, "evicted", 3*time.Second)
}
```

**Timing rationale.** `idleTimeout = 2s`, brief-wait = 1s. Idle timers are one-shot and armed at `runActive` entry (effectively at the Activate moment for each session — `runActive` runs sub-millisecond after `transitionTo(stateActive)`). The 1s gap between α's and β's activations becomes a 1s gap between their idle fires. That 1s window is the test's CI slack — bigger than the typical Go test scheduler jitter (~100ms) and the registry poll cadence (50ms).

**Why `assertActive` and not `waitForSessionState(..., "active", ...)` for the negative checks.** `waitForSessionState` returns as soon as the state is observed once; it doesn't catch the case where the session momentarily flips to "active" then evicts before the next poll. `assertActive` is a single registry read at the moment of the call — confirming the session is active *right now*. Use `waitForSessionState` for "wait for X to happen" and `assertActive` for "X must be true at this checkpoint".

**Why we don't keep β alive past idle by holding an attach.** The runActive idle timer is reset only when it fires while `attached > 0`. To keep β alive past `idleTimeout`, the test would need an attach held across the 2s window — which means a second goroutine reading from the conn (or the conn's read buffer fills and back-pressures the bridge). Both are fine but add machinery the AC doesn't require. The simpler "let β eventually evict too" assertion is enough; the load-bearing observation is "α evicted while β still active" between Phases 1 and 2.

## Test file — `internal/e2e/cap_test.go`

Build tag `e2e`. Same package as `idle_test.go` and `restart_test.go` (file-local helpers `waitForBootstrapState`, `newRegistryHome`, `readRegistry`, `mustReadFile`, `registryEntry`, `registryFile` are package-visible).

```go
//go:build e2e

package e2e

import (
    "context"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/pyrycode/pyrycode/internal/control"
)

const sleepClaudeScript = `#!/bin/sh
exec sleep 99999
`

// writeSleepClaude writes a tiny shell-script claude stand-in to home and
// returns its absolute path. The script ignores all positional args and
// exec()s sleep — necessary because Pool.Create appends "--session-id <uuid>"
// to ClaudeArgs, and BSD/GNU sleep both reject unknown args. The bootstrap
// path also runs through this script (passes "99999" verbatim, which the
// script also ignores).
func writeSleepClaude(t *testing.T, home string) string {
    t.Helper()
    path := filepath.Join(home, "sleep-claude.sh")
    if err := os.WriteFile(path, []byte(sleepClaudeScript), 0o755); err != nil {
        t.Fatalf("write sleep-claude script: %v", err)
    }
    return path
}

// waitForBootstrap polls the registry until the bootstrap entry appears
// (Pool writes it during init) and returns its ID. Used by tests that
// don't pre-populate the registry — the harness creates the bootstrap
// on first start.
func waitForBootstrap(t *testing.T, regPath string, timeout time.Duration) string {
    t.Helper()
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        // The registry file may not exist immediately after StartIn returns
        // — Pool writes it during init, which races with the readiness
        // gate. Tolerate ENOENT by retrying.
        data, err := os.ReadFile(regPath)
        if err == nil {
            var reg registryFile
            if json.Unmarshal(data, &reg) == nil {
                for _, e := range reg.Sessions {
                    if e.Bootstrap {
                        return e.ID
                    }
                }
            }
        }
        time.Sleep(50 * time.Millisecond)
    }
    t.Fatalf("no bootstrap entry observed in registry within %s", timeout)
    return "" // unreachable
}

// waitForSessionState polls regPath until the entry with the given id has
// lifecycle_state matching want ("evicted" or "active"). "active" matches
// either an empty/missing field (omitempty default) or the literal string
// "active" — same convention as waitForBootstrapState in idle_test.go.
func waitForSessionState(t *testing.T, regPath, id, want string, timeout time.Duration) {
    t.Helper()
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        reg := readRegistry(t, regPath)
        for _, e := range reg.Sessions {
            if e.ID != id {
                continue
            }
            got := e.LifecycleState
            if want == "active" && (got == "" || got == "active") {
                return
            }
            if want == "evicted" && got == "evicted" {
                return
            }
        }
        time.Sleep(50 * time.Millisecond)
    }
    t.Fatalf("session %s lifecycle_state never became %q within %s\nfile:\n%s",
        id, want, timeout, mustReadFile(t, regPath))
}

// assertActive checks the registry RIGHT NOW for the given id and fails
// the test if its lifecycle_state is "evicted". Distinct from
// waitForSessionState(..., "active", ...) — that polls for the first
// observation; assertActive is a one-shot checkpoint. Use this when the
// invariant is "X must be true at this exact moment", not "X eventually
// becomes true".
func assertActive(t *testing.T, regPath, id string) {
    t.Helper()
    reg := readRegistry(t, regPath)
    for _, e := range reg.Sessions {
        if e.ID == id {
            if e.LifecycleState == "evicted" {
                t.Fatalf("expected session %s active, but lifecycle_state=%q", id, e.LifecycleState)
            }
            return
        }
    }
    t.Fatalf("session %s not present in registry\nfile:\n%s", id, mustReadFile(t, regPath))
}

// (Test bodies as specified above.)
```

**Imports in the test file.** `encoding/json` is needed for `waitForBootstrap`'s `Unmarshal` (the helper bypasses `readRegistry` to tolerate ENOENT during the boot race; `readRegistry` `t.Fatal`s on read errors). Add it to the import list above (omitted from the sketch for brevity).

**File layout — final.**

```
internal/e2e/
├── harness.go          [unchanged — variadic flags already in tree from #115]
├── idle_test.go        [unchanged — waitForBootstrapState reused via package scope]
├── restart_test.go     [unchanged — registryFile/Entry, newRegistryHome, readRegistry, mustReadFile reused]
├── cap_test.go         [NEW — two tests + writeSleepClaude + waitForBootstrap + waitForSessionState + assertActive]
└── ...
```

## Coordination with #76

#76 ("`pyry sessions new` — CLI router + verb") will, per its planned design, replace the `nil` arg to `control.NewServer` with `pool`. This ticket lands that wiring first. When #76's spec / implementation runs:

- It must skip the `nil → pool` rewrite (already done by this ticket).
- It must remove or update its docs that describe the wiring as "the CLI ticket's first step".

No file overlap risk in the queue today (PR list is empty as of architect run; verified). If #76 reaches PR before #116 lands, #116 will need a one-line rebase: keep the `pool` arg, drop the comment block. Trivial.

# Concurrency model

No new goroutines in production.

Test-side: zero new goroutines in the test bodies. `control.SessionsNew` is a synchronous one-shot dial+RPC. The supervisor's bridge goroutines (one per session) are spawned by Pool in response to Activate, not by the test. Polling loops are sequential within the test goroutine.

The cap-evict path's serialisation is on `Pool.capMu` (per #41 spec); the test does not exercise concurrent `SessionsNew` calls, so no race contention. The 50ms inter-call sleep in Test 1 is for timestamp distinguishability (LRU pick), not race avoidance.

**Registry-read torn-write concern.** Same as in #115: `saveLocked` uses `os.WriteFile` (atomic open-truncate-write, not rename). A concurrent reader can in principle observe a partial write. `readRegistry` `t.Fatal`s on `Unmarshal` failure — if the test ever flakes here, the fix is to wrap the read with a recover-and-retry. Defer until observed; #115's tests have not flaked on this surface.

# Error handling

| Failure | Handling |
|---|---|
| `-pyry-active-cap` parse error | flag.ContinueOnError already plumbed; the parse error returns from `runSupervisor` as today. |
| `control.SessionsNew` returns transport error | `t.Fatalf("sessions.new alpha: %v", err)`. |
| `control.SessionsNew` returns server error (e.g., `"sessions.new: no sessioner configured"`) | Same `t.Fatalf` — surfaces as `Response.Error` propagated through the client. The "no sessioner configured" string is the canary that the production wiring is missing; if seen in CI, the dev's first check should be the `nil → pool` edit landed. |
| Bootstrap never appears in registry within 5s | `t.Fatalf` from `waitForBootstrap`. |
| LRU victim isn't the predicted ID | `waitForSessionState(..., "evicted", ...)` times out → `t.Fatalf` with the registry contents printed. |
| `assertActive` finds the session evicted | `t.Fatalf` with the registry contents printed. |
| `writeSleepClaude` fails | `t.Fatalf("write sleep-claude script: %v", err)`. |
| Registry torn read | `readRegistry`'s `t.Fatal` on `Unmarshal`. Defer fix until observed (see Concurrency note). |

# Testing strategy

The two tests defined here ARE the testing strategy for this ticket. No new test scaffolding beyond the three file-local helpers (`writeSleepClaude`, `waitForBootstrap`, `waitForSessionState`, `assertActive`).

**Manual smoke (for the PR description):**

1. `go test -tags=e2e -run TestE2E_ActiveCap ./internal/e2e/...` — both tests pass within ~10s combined.
2. `go test ./...` (no tag) — both new tests skipped, default suite unaffected.
3. `go test -race -tags=e2e -run TestE2E_ActiveCap ./internal/e2e/...` — race detector clean.

**Unit-level guard already in place.** `internal/sessions/pool_cap_test.go` covers `pickLRUVictim` correctness, cap-zero parity, the cap=1 edge case, and concurrent activation under cap. This ticket's e2e tests verify the binary-boundary integration of those primitives — they do not duplicate the unit-level coverage.

**No `pyry status` cross-check.** `VerbStatus` resolves to the bootstrap session today (`s.sessions.Lookup("")`). For Test 1 the bootstrap is cap-evicted, so `pyry status` reports a non-running phase — same shape as #115's idle test. Including it would re-test the same surface as the registry assertion; skipped to keep the test focused.

# Open questions

1. **Should `-pyry-active-cap` accept a unit suffix?** It's an integer; `flag.Int` is the right tool. No.
2. **Should the test cover the cap=1 pathological case?** Covered at the package level (`TestPool_ActiveCap_OneSessionAtCapOne`). Adding it here would re-test, not extend. Defer.
3. **Should the test exercise `sessions.new` against a daemon with `activeCap=0` (uncapped)?** The wire surface is the same; the cap-binding code path is what differs. Covered implicitly — in Test 1, the first `sessions.new` call (α) does not hit the cap-evict branch (count goes 1→2, cap=2, no evict). That's the activeCap-bound-but-not-tripped path; the activeCap=0 path is the same code with `pickLRUVictim` returning nil at the `activeCap <= 0` short-circuit. Adequately covered by package tests.
4. **Test 2 timing margin.** 1s gap between α's and β's idle fires. If CI flakes, raise idle to 3s and brief-wait to 1.5s — same upgrade path as #115 documented. Don't shrink; the AC says "wait past idleTimeout" and 2s is the minimum useful value.

# Why size:S

PO sized this S. Re-checking against the architect red lines:

- **Files added:** 1 (`cap_test.go`). Files modified: 1 (`cmd/pyry/main.go`). Total: 2 files. ≤ 3 ✓
- **Production lines:** ~5 — one new flag declaration, one new `ActiveCap` field in the `Config` literal, one `nil → pool` edit, one comment-block deletion. Test lines: ~180 (two tests + four helpers). Production well under 100. ✓
- **New exported types/interfaces:** 0. ✓
- **Consumer call sites that need updating:** 0 — `NewServer` signature is unchanged; only its argument changes. No fan-out. ✓
- **Acceptance criteria worth of work:** 4 ACs. AC#1 (flag) is the same 2-LOC change AC#2/AC#3 consume; AC#4 (build-tag isolation) is satisfied by the existing `//go:build e2e` pattern. Effectively two test cases plus the wiring. ✓

No red line tripped. Single architect run; one developer run; no split.

**Edit fan-out check.** The only multi-site edit is the four file-local helpers in the new test file (one-shot writes, not rewrites of existing code). The `nil → pool` edit is a single occurrence. No cascade.
