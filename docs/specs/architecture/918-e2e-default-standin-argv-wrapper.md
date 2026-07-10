# Spec #918 — e2e default stand-in tolerates daemon-appended argv (`--session-id`)

**Size:** XS (PO sized S; overriding downward — one production file, a symbol relocation, one default-value swap).
**Security-sensitive:** no (no label; test-support code, no design surface, no untrusted input).

## Files to read first

- `internal/e2e/harness.go:516-566` — `spawnWith`, the shared spawn core. The zero-value `spawnOpts` path (`o.claudeBin == ""` → `/bin/sleep`) is the single line this ticket changes. Note the last-wins flag layout (`-pyry-claude=` in the standard set, `extraFlags` appended after).
- `internal/e2e/harness.go:461-475` — `spawnOpts`; the doc already states "Zero-value yields the existing `/bin/sleep 99999` behaviour." That contract text changes with this ticket.
- `internal/e2e/harness.go:173-188` — `Start` doc comment describing the supervised stand-in as `/bin/sleep 99999`. Update to describe the wrapper.
- `internal/e2e/cap_test.go:16-35` — `sleepClaudeScript` const + `writeSleepClaude(t, home)`. **This is the fixture being relocated.** Note it currently lives under `//go:build e2e` (line 1).
- `internal/supervisor/supervisor.go:771-780` — `buildClaudeArgs`: `if sessionID != "" { return append(args, "--session-id", sessionID) }`. The authoritative #839 append site (pool.go resolves the id and passes it in). Confirms the appended flag lands after the configured claude args, and that any stand-in must tolerate a trailing `--session-id <uuid>`.
- `internal/e2e/bootstrap_warm_start_test.go:32-75` — target test #1. Drives `StartIn(t, home)` twice on the default path and asserts `Phase: running`. Read only to confirm no assertion touches the stand-in binary (line 64's `/bin/sleep` is a narrative comment, not an assertion).
- `internal/e2e/startup_test.go:13-40` — `StartExpectingFailureIn` corrupt-registry test. Read to confirm the no-regression claim: it asserts on `.pyry/test/sessions.json`, never on `home`'s file listing, so an added `home/sleep-claude.sh` is invisible to it.
- Build-tag map (verified, do not re-derive): `harness.go` and `attach_pty.go` compile under `e2e || e2e_install`; **every** `*_test.go` in the package (including `cap_test.go`) compiles under `e2e` only. The `e2e_install` install tests (`install_{darwin,linux}_test.go`) never call `Start`/`StartIn`/`spawnWith` — they spawn with an explicit `-pyry-claude=/bin/sleep` override, so they are untouched by the default-path change.

## Context

**Problem.** The fake-daemon e2e suite's *default* supervised stand-in (the zero-value `spawnWith` `spawnOpts` path used by `Start` / `StartIn` / `StartInWithEnv`) invokes `/bin/sleep` directly. Since #839 (PR #882), the daemon appends `--session-id <uuid>` to **every** bootstrap spawn (`buildClaudeArgs`, `supervisor.go:771`). Both BSD and GNU `sleep(1)` reject the unknown flag with a usage banner and exit immediately, so the bootstrap child crash-loops under backoff (observed `Phase: backoff, Restart count: 4`). Three default-path tests that need the child to reach and hold `running` go red:

- `TestE2E_BootstrapWarmStart_IgnoresEvictedOnDisk`
- `TestE2E_IdleEviction_LazyRespawn`
- `TestRelayV2_Daemon/v2_enabled_request_snapshot_round_trip`

**Why now.** The suite is in no standing gate, so this has sat red since #882. While these three are red the suite cannot catch a regression in warm-start, idle eviction, or the v2 snapshot round-trip — all daemon-core behaviours.

**Why the fix is trivial.** The argv-ignoring wrapper stand-in already exists and is proven: `sleepClaudeScript` (`#!/bin/sh` + `exec sleep 99999`) written by `writeSleepClaude(t, home)`. Every `Pool.Create`-driven test (`sessions_new`, `sessions_rm`, `sessions_list`, `sessions_rename`) already routes through it and stays green post-#839, precisely because the wrapper drops all positional args. This ticket points the *default* spawn at the same wrapper. The wrapper ignores the bootstrap invocation's `99999` and any appended `--session-id <uuid>` identically, so no per-invocation argv reasoning is needed.

**Scope (re-confirmed 2026-07-11).** This ticket fixes only the default `/bin/sleep` stand-in → **3 tests**. The other two red mechanisms are disjoint and tracked separately: `spawnAttachableDaemon`'s Go-test-binary stand-in (4 `TestE2E_Attach_*` tests) is #257; the argv-immune-fakeclaude `TestRelayV2_InterruptStopsRunningTurn` failure is a daemon-side #839 regression, #929. **Do not touch `attach_pty.go` or any daemon-side code** — file-disjointness is what lets #918/#257/#929 land in parallel without merge conflict (branch-overlap check at architect time: no overlap on `harness.go`/`cap_test.go`).

## Design

Two edits, both in `internal/e2e`, no new files.

### 1. Relocate the wrapper fixture into an `e2e || e2e_install` file

`spawnWith` lives in `harness.go` (`//go:build e2e || e2e_install`). `writeSleepClaude` / `sleepClaudeScript` live in `cap_test.go` (`//go:build e2e`). Referencing the helper from `harness.go` as-is would break the `e2e_install` build (undefined symbol). Move both identifiers **verbatim** from `cap_test.go` into `harness.go`:

- Cut `const sleepClaudeScript = …` and `func writeSleepClaude(t *testing.T, home string) string { … }` from `cap_test.go`.
- Paste them into `harness.go`, adjacent to `spawnOpts` / `spawnWith` (the code that consumes them).

This is a pure move — no signature change, no rename. All ~24 existing `writeSleepClaude` call sites (`cap_test.go`, `sessions_*_test.go`, `auto_attach.go`) resolve to the same package symbol and need **zero** edits. `sleepClaudeScript` has no references outside `writeSleepClaude`.

Chosen over a new `standin.go` file: the wrapper *is* the default stand-in, so it belongs with `spawnWith` (which already documents "The supervised claude is …"). One fewer file, one build-tag line fewer to get right.

While moving, generalize `sleepClaudeScript`'s doc comment: it currently frames the wrapper as a `Pool.Create` fixture; note it is now the default stand-in for **every** supervised spawn, so an appended `--session-id <uuid>` (or any future spawn-time flag) can't crash-loop the child.

### 2. Point the default `spawnWith` path at the wrapper

In `spawnWith`, change the default-binary line only:

```go
if o.claudeBin == "" {
    o.claudeBin = writeSleepClaude(t, home)   // was: "/bin/sleep"
}
```

Leave `o.claudeArgs`'s `{"99999"}` default unchanged — the wrapper ignores it, and keeping it minimizes the diff and preserves the `--` arg shape the daemon builds on. `writeSleepClaude` already has `t` and `home` in scope inside `spawnWith`; it writes `home/sleep-claude.sh` (0o755) and returns its absolute path.

Update the `Start` doc comment (`harness.go:173-188`) and `spawnOpts`'s "Zero-value yields … `/bin/sleep`" line to describe the wrapper (`#!/bin/sh` + `exec sleep 99999`, argv-ignoring) instead of a bare `/bin/sleep`.

### Interaction with flag last-wins (no behavioural change for existing callers)

- Tests that thread `-pyry-claude=` as an **extraFlag** (`StartIn(t, home, "-pyry-claude="+writeSleepClaude(t,home))` — the `sessions_*` suite) keep `spawnOpts.claudeBin == ""`. `spawnWith` now also writes `home/sleep-claude.sh` and sets the standard `-pyry-claude=` to it; their extraFlag then overrides with the *same path and identical content*. Idempotent double-write of one file; the daemon uses the last flag. No behavioural change.
- Tests that set `spawnOpts.claudeBin` directly (`StartRotation*` → fakeclaude) have `o.claudeBin != ""`, so the guard is false and the wrapper is **not** written. Unchanged.
- Default-path tests (`StartIn`/`StartInWithEnv`/`Start`, and `StartExpectingFailureIn` via `spawn`) now get `home/sleep-claude.sh` and a wrapper child. This is the fix.

## Concurrency model

None. Single-threaded test-setup code: write a file, then `exec.Command`. No goroutines, channels, or shared state introduced. The existing spawn/wait goroutine in `spawnWith` is untouched.

## Error handling

- `writeSleepClaude` fails the test via `t.Fatalf` if `os.WriteFile` errors (e.g. `home` unwritable). This matches its existing contract; `home` is always a writable temp dir on the default path (every default-path test already writes into `home`, e.g. `newRegistryHome` seeding `.pyry/test/sessions.json`).
- `StartExpectingFailureIn` path: the daemon fails at startup (corrupt registry / workdir confinement) **before** ever exec'ing `-pyry-claude`, so the wrapper is written but never run. The failure assertions target `.pyry/test/sessions.json` / stderr, not `home`'s listing, so the added `home/sleep-claude.sh` is inert. No regression.

## Testing strategy

No new tests; no test-body assertion changes (AC3). Verification is the three previously-red tests going green plus the suite staying green:

```
go test -tags e2e -race -count=1 ./internal/e2e/...
```

- **Targets green:** `TestE2E_BootstrapWarmStart_IgnoresEvictedOnDisk`, `TestE2E_IdleEviction_LazyRespawn`, `TestRelayV2_Daemon/v2_enabled_request_snapshot_round_trip` — child now reaches and holds `running` because the wrapper tolerates the appended `--session-id`.
- **No-regression argument for "no live claude" tests** (`relay_v2_dequeue`, `relay_v2_daemon` negative paths): the wrapper is behaviourally identical to `/bin/sleep` from the daemon's view — a live process that never emits a claude handshake. Assertions keyed on "binary offline / no live claude" are unaffected; only the child's *survival under extra argv* changed.
- **`e2e_install` build must still compile** (this is the whole reason for the relocation):

  ```
  go build -tags e2e_install ./internal/e2e/...
  ```

  Fails today only if the helper is referenced from `harness.go` while still defined in `cap_test.go`; passes once the move is complete.
- Out of scope, still red (expected — AC4): the 4 `TestE2E_Attach_*` tests (#257) and `TestRelayV2_InterruptStopsRunningTurn` (#929). Do not attempt to fix them here.

## Scope self-check

Production source files (`.go`, non-`_test.go`) modified: **1** (`internal/e2e/harness.go`). `cap_test.go` is a `_test.go` (excluded). No new production files. Well under the 5-file gate. No edit fan-out: the relocation is intra-package with zero call-site changes.

## Open questions

- None blocking. `bootstrap_warm_start_test.go:64`'s comment ("Supervisor.Run spawns `/bin/sleep`") becomes mildly stale (now a wrapper over `sleep`). It is a comment, not an assertion — leave it to honour AC3 ("no test-body assertion changes"); touching it is unnecessary churn.
- `docs/lessons.md` #116 ("Bootstrap-only e2e tests don't hit this") is invalidated by #839 and this fix, but `lessons.md` is frozen (2026-05-11) and owned by the documentation phase — **do not edit it.** The documentation phase folds this correction into `docs/knowledge/codebase/918.md` after merge.
