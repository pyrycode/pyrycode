# Spec #576 — ptyrunner test recording hermeticity (unset ambient `PYRY_RECORD_DIR`)

**Size:** XS (overrides PO's `s` — downward override is allowed). Test-only; one `os.Unsetenv` call plus a comment inside the package's existing `TestMain`. No production code, no new files, no new types.

## Files to read first

- `internal/agentrun/ptyrunner/helper_test.go:16-29` — the **existing** package `TestMain`. This is the only file you edit. The unset goes in the non-helper (parent) branch, immediately before `os.Exit(m.Run())`. `os` is already imported (line 6) — no new import.
- `internal/agentrun/ptyrunner/runner.go:316-344` — the recording gate. The decision `if dir := os.Getenv("PYRY_RECORD_DIR"); dir != ""` runs in **`Run()` inside the test process** (the parent), not in the fake-claude child. This is *why* unsetting the var in the parent `TestMain` disables recording for every non-opt-in test.
- `internal/agentrun/ptyrunner/runner.go:585-596` — `recordingPath`; the `<stamp>-<sessionID>.cast` scheme. AC #4 requires this stays byte-identical. You do not touch it.
- `internal/agentrun/ptyrunner/runner_test.go:40` — `const testSessionID`. Stays as-is (shared constant is fine once the ambient leak is closed — see "Why not the other levers").
- `internal/agentrun/ptyrunner/runner_test.go:50-77` — `helperRunCfg`, the shared `Config` builder every `TestRun_*` uses. It does **not** set `PYRY_RECORD_DIR`; the non-recording tests therefore inherit the ambient value today. After the fix the ambient value is gone, so they record nothing.
- `internal/agentrun/ptyrunner/runner_test.go:720-756` — the recording-test section header documenting the serial-vs-parallel split (`t.Setenv` users must not be parallel) and `castOkNameRe`. Read so you understand why the fix doesn't break that ordering. `castOkNameRe` stays as-is.
- `internal/agentrun/ptyrunner/runner_test.go:758-1018` — the six recording tests. Each opts in with its own `recDir := t.TempDir()` + `t.Setenv("PYRY_RECORD_DIR", recDir)` and runs serially (no `t.Parallel()`). Confirm: every one uses a **distinct** temp dir. This is what keeps their recording paths unique without any session-id change.

## Context

`make check` is non-deterministically red in `internal/agentrun/ptyrunner`. Root cause (per the ticket and QA's PR #575 baseline analysis):

1. The non-recording `TestRun_*` tests all call `t.Parallel()` and build their `Config` via `helperRunCfg`, which never sets `PYRY_RECORD_DIR`. So they **inherit whatever `PYRY_RECORD_DIR` is exported in the ambient shell**.
2. When that var is set, recording turns on for those tests. They all share `const testSessionID` and the recording filename is `<UTC-stamp-to-the-second>-<sessionID>.cast` in the **same ambient dir**.
3. Two parallel tests that start in the same wall-clock second compute the **identical** path. tui-driver opens the file `O_EXCL`, so the second spawn fails with `file exists`, which cascades into prune/rename `no such file or directory` warnings and (for `TestRun_CtxCancelDuringStream`) a `did not observe first emitted line within 5s` timeout because that spawn produced no output.

This is purely a test-isolation defect. There is no production bug; the recording scheme is correct and frozen by AC #4.

## Design

**Single change: make the package test binary hermetic against an inherited `PYRY_RECORD_DIR` by unsetting it once in `TestMain`, before `m.Run()`.**

The package already has exactly one `TestMain` (`helper_test.go:22`). It currently dispatches to the fake-claude helper when `GO_PTYRUNNER_HELPER=1`, otherwise runs the suite. The fix adds the unset to the suite (parent) branch only:

```go
func TestMain(m *testing.M) {
	if os.Getenv("GO_PTYRUNNER_HELPER") == "1" {
		runHelper() // terminates via os.Exit
		return
	}
	// #576: drop any ambient PYRY_RECORD_DIR so tests that don't explicitly
	// opt into recording never inherit it. The recording gate (runner.go) reads
	// os.Getenv in THIS process; clearing it once here makes every non-opt-in
	// TestRun_* deterministic regardless of the caller's shell. Recording tests
	// re-set it per-test via t.Setenv (which restores to unset on cleanup).
	_ = os.Unsetenv("PYRY_RECORD_DIR")
	os.Exit(m.Run())
}
```

That is the whole production-of-behavior change. Behavior contract, stated as invariants the developer must preserve / verify (not implementation):

- **Hermetic baseline.** After `TestMain`, `os.Getenv("PYRY_RECORD_DIR") == ""` for every test that does not set it itself — independent of the ambient environment (AC #2).
- **Opt-in recording still works.** The six recording tests each call `t.Setenv("PYRY_RECORD_DIR", recDir)` with a fresh `t.TempDir()`. `t.Setenv` overrides the unset baseline for that test's duration and restores to unset on cleanup. They run serially (no `t.Parallel()`), so the env never leaks into the parallel batch — the existing ordering rule at `runner_test.go:722-725` is unchanged.
- **Path uniqueness (AC #1) holds by construction.** Non-recording tests now generate *no* recording path. Recording tests each write into a distinct `t.TempDir()`, so even with the shared `testSessionID` and a same-second stamp their full paths differ. No two `TestRun_*` tests can produce the same `.cast` path.
- **Frozen surfaces.** `recordingPath` (`runner.go`), `testSessionID` (`runner_test.go:40`), and `castOkNameRe` (`runner_test.go:756`) are **not** modified — there is no session-id change for the matcher to track.

### Placement detail

The unset must be in the **parent branch** (after the `GO_PTYRUNNER_HELPER` early return), not before the dispatch. The helper child re-execs into `runHelper` and never reaches `m.Run()`; it is fake-claude and never records, so unsetting there is pointless. Keeping it in the parent branch is both correct and minimal.

### `TestRun_RecordOff_NoFileCreated` note

This test sets `t.Setenv("PYRY_RECORD_DIR", "")` explicitly. With the new unset baseline that line is now redundant (the var is already empty), but it is **harmless and self-documenting** — it asserts the explicit-off contract. Leave it as-is to keep the diff to a single file. Do not remove it.

## Why not the other levers (decision record)

QA noted three independent levers; any one stops the collision. Choosing hermeticity, rejecting the others:

| Lever | Stops collision? | Satisfies AC #2 (no perturbation)? | Cost |
|---|---|---|---|
| **Unset ambient `PYRY_RECORD_DIR` in `TestMain` (chosen)** | Yes — non-recording tests stop recording entirely | **Yes** — inherited var is gone for all tests | 3 lines, existing `TestMain`, 0 other edits |
| Per-test unique `testSessionID` | Yes — unique filenames | **No** — tests still write stray `.cast` files into the inherited ambient dir (perturbs it); also forces `castOkNameRe` to track the new id | helper + regex churn, leaves env-coupling intact |
| Sub-second timestamp | N/A | — | Forbidden by AC #4 (recording scheme is frozen) |

Hermeticity is the *root cause* (the ambient leak is the only reason non-recording tests record at all) and is the only lever that satisfies AC #2's "neither dependent on **nor perturbed by** an inherited `PYRY_RECORD_DIR`." A per-test session id would defend against recording-test collisions that are already structurally impossible (each recording test uses its own `t.TempDir()`) — a defense for an unobserved failure mode. The fix is deterministic code, not a stochastic rule.

## Concurrency model

Unchanged. Go runs serial tests (the `t.Setenv` recording tests) to completion before the parallel batch (the `t.Parallel()` `TestRun_*` tests). The unset is a one-time process-global mutation in `TestMain` before any test runs, so it neither races with nor is overwritten by the per-test `t.Setenv` calls (each of which restores to the unset baseline on its own cleanup). No `t.Setenv` is added to any parallel test, so there is no `t.Setenv`-after-`t.Parallel()` panic risk.

## Error handling

There is nothing to handle — `os.Unsetenv` on a key returns nil in practice and the result is discarded with `_ =` per the project's "document a deliberately-ignored error" convention (the inline comment serves as the justification). The fix *removes* an error cascade (`file exists` → prune/rename `no such file or directory` → stream timeout) rather than adding one.

## Testing strategy

No new test functions. Verification is by running the existing suite under the conditions the ACs name. The developer runs these and reports output:

- **AC #2 + AC #3 — hermetic under exported ambient var.** With `PYRY_RECORD_DIR` exported to a scratch dir, the full package must pass repeatedly and leave no stray recordings from non-opt-in tests:
  - `PYRY_RECORD_DIR=$(mktemp -d) go test -race ./internal/agentrun/ptyrunner/ -count=20`
  - Expect: `ok`, zero `file exists`, zero prune/rename `no such file or directory`, zero `did not observe first emitted line within 5s`. The scratch dir should contain no `.cast` files afterward (non-recording tests no longer write there).
- **AC #3 — hermetic with the var unset.** `go test -race ./internal/agentrun/ptyrunner/ -count=20` → `ok`.
- **Recording tests still pass.** The six `TestRun_Record*` tests must remain green (they assert `.cast` creation, `-ok`/`-err` tagging, 0600 perms, valid asciinema v2, prune scoping). Covered by the package run above; optionally isolate with `go test -race -run '^TestRun_Record' ./internal/agentrun/ptyrunner/ -count=5`.
- **Full gate.** `make check` (or `go vet ./... && staticcheck ./... && go test -race ./...`) green.

## Open questions

- **Is the literal reading of AC #1 ("recording paths are unique per test") satisfied without a per-test session id?** Yes. Post-fix, the only tests that generate a recording path are the six opt-in recording tests, and each uses a distinct `t.TempDir()` — so the set of generated paths is unique per test. Non-recording tests generate none. The invariant "no two `TestRun_*` tests generate the same `.cast` path" holds unconditionally. If a reviewer prefers the session-id axis also be made unique, that is an additive follow-up, not required to close the ACs — and it would reintroduce the `castOkNameRe` coupling this design deliberately avoids.
