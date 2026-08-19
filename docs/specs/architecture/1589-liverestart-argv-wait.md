# Spec #1589 — Wait for the argv record before every teardown in `TestRunner_LiveRestart`

**Size:** XS (test-only, one file, no production change, no new exported symbol, no edit fan-out).
**Security-sensitive:** No (test-harness synchronization; no product code, no trust or input surface).

## Files to read first

- `internal/streamsup/interface_test.go` → `TestRunner_LiveRestart` — the test being fixed. The entire change lives in this file. Extract the current control flow: `spawns` channel fed by `cfg.onSpawn`, two `select` waits on it, `r.Restart`, `cancel()`/`join()`, then a single `os.ReadFile(argvFile)` at the bottom feeding four argv assertions.
- `internal/streamsup/interface_test.go` → `findEchoedLine` — the precedent for a single-caller test helper colocated in this file next to the test that uses it. The new wait helper follows the same placement and `t.Helper()` + `t.Fatalf` shape.
- `internal/streamsup/helper_test.go` → `helperChild`, its `case "record_block"` arm — the child-side write order: argv append + `Sync` + `Close` **first**, `signal.Notify` **second**. This ordering is the mechanism, and it is what the discriminator overlay injects a delay ahead of. **Do not modify this file** (except transiently, via `-overlay`, for the discriminator runs).
- `internal/streamsup/runner_test.go` → `waitForContains` — the package's existing bounded-poll idiom (deadline computed once, 10 ms tick, `t.Fatalf` naming the observable it never saw). The new helper mirrors this shape.
- `internal/streamsup/runner_test.go` → `launchRecorder.waitLaunch` — the "*the timeout is a FAILURE BOUND, not a calibration*" doc-comment idiom. The new helper's deadline comment says the same thing, for the same reason.
- `internal/streamsup/runner_test.go` → `TestRunner_ResumeIDStableAcrossRestart` — the **other** `GO_STREAMSUP_HELPER_ARGV_FILE` consumer. Extract why it is *not* exposed to this bug: it uses `crash` mode, whose children self-exit, so each argv write happens-before the child exit that triggers the next spawn. Its own inline comment states the ordering argument. No change there.
- `internal/streamsup/runner_test.go` → `TestRunner_OnChildExit_FiresOnDeliberateRestart` — the test with the *identical* unsynchronized `onSpawn → Restart → onSpawn → cancel` shape. It asserts only exit counts, which stay caller-caused whether or not the child records argv. Confirm, then leave alone.
- `internal/streamsup/runner.go` → `spawnAndWait` — read-only. Extract two facts: `r.cfg.onSpawn` fires immediately after `cmd.Start` returns (the child may not have executed a single line of Go yet), and `cmd.Cancel` is the SIGTERM sender wired to the iteration ctx. **This file must not change** (AC5).
- `internal/streamsup/runner.go` → `Restart` — that it cancels the live iteration ctx, which is what fires `cmd.Cancel` and SIGTERMs the child.
- `docs/lessons.md` § "Control socket dialability lags supervisor `Phase: running` — poll, don't single-shot, on post-restart status" — the house rule this fix instantiates: when a readiness signal is set by code that runs *before* the observable, poll the observable under a bounded deadline; do not lean on the readiness signal as a memory barrier.
- `docs/specs/architecture/1118-acp-argv-poll-flake.md` — prior art, same class. Read for the shape *difference*: #1118 had a poll that was merely too tight and needed widening; here there is no poll at all, and the test does not merely fail to observe the write — it kills the child before the write can happen.

## Context

`TestRunner_LiveRestart` fails ~10% of the time under full-suite `go test -race ./...`, on `main` and independent of PR #1588. It passes 20/20 in isolation. The ticket establishes the mechanism, and it is confirmed by reading `spawnAndWait`:

1. `onSpawn` fires on the Run goroutine **immediately after `cmd.Start`**. The fork/exec has returned; the child has not necessarily executed any Go code.
2. `record_block` writes its argv line and only *then* installs its SIGTERM handler.
3. The test gates solely on `onSpawn`, then immediately triggers a teardown that SIGTERMs the child — `Restart` after spawn 1 (via the iteration-ctx cancel → `cmd.Cancel`), `cancel()` after spawn 2.

Under contention the signal lands while the child is still in Go runtime startup, so it dies on the *default* SIGTERM disposition — before the argv write and before the handler exists. The filed failure shows a child torn down via `signal: terminated` after 19 ms of uptime.

Two consequences shape the design:

- **Reordering `signal.Notify` ahead of the argv write inside `record_block` does not fix this.** It narrows the window to "fork/exec → first Go statement" but cannot close it, because the child can die before running any user code at all. The fix has to be parent-side.
- **Both spawns are exposed, not just the first.** The filed failure lost spawn 1's line (`want ≥2 captured argv lines, got 1`); the widened-window repro loses both and the capture file is never created (`read argv capture: … no such file or directory`). A fix that synchronizes only before `Restart()` leaves half the flake in place.

## Design

One file: `internal/streamsup/interface_test.go`. Zero production files. Fix **direction 1** from the ticket — wait for the argv record — for the reason the ticket gives: it synchronizes on precisely the artifact the assertion consumes, and it keeps the test's distinctive value (argv as *actually delivered through exec*, not as the parent *constructed* it; `buildArgs` already has direct unit coverage in `TestBuildArgs`).

### 1. New single-caller helper: `waitArgvLines`

Placed in `interface_test.go` directly above `TestRunner_LiveRestart`, mirroring how `findEchoedLine` sits beside its caller in the same file.

```go
// waitArgvLines polls the argv capture file until it holds at least n complete
// (newline-terminated) records, and returns those records.
func waitArgvLines(t *testing.T, path string, n int) []string
```

Behaviour contract:

- `t.Helper()`; poll on a 10 ms tick against a deadline computed once, matching `waitForContains`.
- **Count only complete records.** Split the file contents on `"\n"` and discard the final element — that element is whatever follows the last newline, and is the empty string exactly when the last record is complete. This is the definition of "recorded", and it makes a torn trailing write impossible to miscount. Do **not** reuse the existing `strings.TrimRight(data, "\n")`-then-split shape here: it promotes a partial trailing write to a full record.
- **A missing file is zero records, not an error.** The child creates the capture file with `O_CREATE` on its first write, so `fs.ErrNotExist` is the normal pre-write state — keep polling. Any *other* read error is a genuine I/O fault: `t.Fatalf` immediately rather than burning the deadline on it.
- **Deadline is 5 s, and is a FAILURE BOUND, not a calibration.** Say so in the doc comment, in the manner of `launchRecorder.waitLaunch`. 5 s matches the sibling spawn-wait budgets already in this test. A healthy run reaches the record in milliseconds; only a genuinely broken child waits.
- **On deadline: `t.Fatalf` naming the missing argv record** — how many records were wanted, how many were seen, the capture path, and the bytes read. This is what discharges AC3: a child that never records fails in ~5 s with a diagnostic, rather than hanging to the package timeout.

### 2. Two call sites, one deletion

The test's flow becomes:

| Step | Change |
|---|---|
| `select` on `spawns` (spawn 1) | unchanged |
| **new** | `waitArgvLines(t, argvFile, 1)` — called as a bare statement; the return is unused here. Comment: spawn 1's record must exist *before* `Restart` SIGTERMs it, because `onSpawn` fires after `cmd.Start` and proves nothing about the child having run. |
| `r.Restart([]string{"--model", "restart-marker"})` | unchanged |
| `select` on `spawns` (spawn 2) | unchanged |
| **new** | `lines := waitArgvLines(t, argvFile, 2)` — same reason, ahead of `cancel()`. |
| `cancel()` / `join()` → `context.Canceled` | unchanged |
| `RestartCount == 0` | unchanged |
| `os.ReadFile(argvFile)` + split + `len(lines) < 2` fatal | **deleted** — superseded by the second wait, which already returned the records and already fails under a bounded deadline if fewer than two exist. |
| `first, second := lines[0], lines[1]` + the four argv assertions | unchanged, now reading the hoisted `lines` |

`os` and `filepath` stay in use in this file (`os.ReadFile` inside the helper, `filepath.Join` for `argvFile`), so no import churn.

### 3. Why waiting on the argv record is sufficient

Waiting for the record does **not** guarantee the child has installed its SIGTERM handler — the write precedes `signal.Notify`, so a still-unhandled child may die on the default disposition. That is harmless here, and the developer should not try to close it:

- The test asserts nothing about the child's exit status or exit path.
- `record_block`'s purpose ("prove the first child was terminated by `Restart`, not by its own exit") holds either way: a child killed by the default disposition was still killed by `Restart`.
- Whether the child dies via the handler or the default disposition, `spawnAndWait` returns, the Run loop finds the parent ctx unexpired, `drainRestart` returns true, and spawn 2 follows. The relaunch is not at risk.
- Handler installation has no observable in the parent, so synchronizing on it would require inventing one — new child-side machinery to defend a failure mode that has never been observed.

The argv record is the *only* observable the assertions consume, and it is exactly what the wait now covers.

### 4. Rejected: record argv parent-side via `launchRecorder`

The ticket's direction 2. It removes the child-side race entirely but weakens what the test proves: `launchRecorder` captures the argv the parent *constructed* (off the `spawnArgsRecorder` log hook), whereas the capture file demonstrates the argv as *delivered through exec*. `TestBuildArgs` already covers construction directly, so end-to-end delivery is this test's distinctive contribution and should not be traded away for a synchronization fix that direction 1 achieves without loss.

### 5. Scope boundary

- `TestRunner_ResumeIDStableAcrossRestart` (the other `GO_STREAMSUP_HELPER_ARGV_FILE` user) is **not** exposed: `crash`-mode children self-exit, so each argv write happens-before the exit that triggers the next spawn. No change.
- The four other `record_block` users record argv parent-side via `launchRecorder` (or assert only on exit counts) and read no file. No change. In particular `TestRunner_OnChildExit_FiresOnDeliberateRestart` has the identical unsynchronized teardown shape but asserts only exit counts, which stay caller-caused either way.
- **Explicitly out of scope:** the test calls `cancel`/`join` inline rather than deferring them, so any `t.Fatal` leaks the live child until its own 30 s timer. That is pre-existing at three fatal sites in this test and unrelated to the flake. Do **not** add `defer func() { cancel(); join() }()` — the happy path already calls both inline, and a second `join()` would block on the drained channel until its own 10 s fatal.

## Concurrency model

Unchanged in the runner. Test-side: the two new waits run on the test goroutine, are bounded polls over an on-disk file written by an out-of-band child process, and add no goroutines, channels, or synchronization primitives. They only insert a happens-before edge between "the child recorded its argv" and "the parent triggers a teardown that SIGTERMs it" — the edge the test previously assumed and never established.

## Error handling

| Condition | Behaviour |
|---|---|
| Capture file absent | Zero records; keep polling. Normal pre-write state (`O_CREATE` on first write). |
| Read error other than not-exist | `t.Fatalf` immediately — a real I/O fault, not something to wait out. |
| Fewer than `n` records at the deadline | `t.Fatalf` naming the missing argv record: wanted vs seen count, capture path, bytes read. Bounded at 5 s (AC3). |
| More than `n` records | Return all complete records; the caller indexes `[0]` and `[1]`. Preserves the existing `≥2` semantics — no new "exactly 2" assertion. |
| Torn trailing write | Not counted (complete-records rule), so the wait continues until the record is whole. |

## Testing strategy

No new test functions — this fixes an existing test. Verification is a deterministic red/green discriminator built by **widening the window, not reproducing the load** (AC2). All three runs below use `-race`, because that is the flag set `make check` runs under.

Build the overlay without writing into the worktree: copy `helper_test.go` into the scratch dir, insert the injection at the unique anchor `case "record_block":`, and point a `-overlay` JSON `Replace` entry at the copy. `time` and `os` are already imported in that file, so no import edit is needed.

**Run A — red, pre-fix.** Inject `time.Sleep(150 * time.Millisecond)` immediately after `case "record_block":`, so it precedes the argv write. Run against the **unmodified** test:

```
go test -overlay=<abs>/overlay-delay.json -race -run TestRunner_LiveRestart -count=3 ./internal/streamsup
```

Expect FAIL on every iteration. Do this run **before** editing `interface_test.go`. If the edit is already made, add a second `Replace` entry mapping `interface_test.go` to a copy produced by `git show HEAD:internal/streamsup/interface_test.go`.

**Run B — green, post-fix.** Same overlay, same command, `-count=5` (AC2's repeat floor). Expect PASS 5/5.

**Run C — AC3, bounded failure on a child that never records.** Second overlay: insert `os.Setenv("GO_STREAMSUP_HELPER_ARGV_FILE", "")` after `case "record_block":`, which makes `record_block` skip the write entirely (its guard is `path != ""`). Run the post-fix test once and expect FAIL in ~5 s with `waitArgvLines`' message naming the missing argv record — not a hang to the package timeout. Record the reported test duration as the evidence.

**Run D — the gate.** `make check`, green (AC5).

Record the Run A / Run B / Run C outcomes and the exact commands in the PR description; AC2 requires the discriminator be "demonstrated and recorded".

Existing-assertion preservation (AC4) is verified by inspection of the final diff, not by a new test: spawn 1 carries `--session-id <testSessionID>` and not `restart-marker`; spawn 2 carries `--resume <testSessionID>`, no `--session-id`, and `--model restart-marker`; `Run` returns `context.Canceled`; `RestartCount == 0`. All four blocks move only in the sense that their input `lines` is now bound earlier.

## Open questions

None blocking. Two calls the developer should make and note in the PR:

- **Helper name.** `waitArgvLines` is the proposal; anything that reads as a bounded wait on the capture file is fine. It is unexported, single-caller, and file-local, so the name is cheap to change.
- **Whether the 5 s bound wants a named constant.** It is used at two call sites through one helper, so a literal inside the helper with the FAILURE-BOUND comment is enough. Add a constant only if a second helper ends up sharing it.
