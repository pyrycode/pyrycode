# Spec #1118 — Robust argv-record wait for the ACP interactive-spawn tests

**Size:** XS (architect override from PO's `s` — test-only, one file, no signature change, no edit fan-out).
**Security-sensitive:** No (test-harness timing; no product code, no trust/input surface).

## Files to read first

- `cmd/pyry/acp_test.go:403-425` — `waitOneClaudeArgv`, the shared poll helper. Line 409 holds the too-tight `3 * time.Second` deadline. This is the single wait everything collapses onto; extract its exact loop shape (20 ms poll, `>1` line ⇒ divergence-6 fatal, empty ⇒ keep polling, one line ⇒ `strings.Fields`).
- `cmd/pyry/acp_test.go:239-262` — the inline poll **copy** inside `TestACP_SessionNew_SpawnsOneInteractiveClaude` (its own `3 * time.Second` at line 243). Byte-for-byte the same logic as the helper, but it also binds `fields` for the argv-equality assertion at line 266-268. This is the duplicate to delete.
- `cmd/pyry/acp_test.go:148-166` — `fakeClaudeScript`: the `/bin/sh` fake claude. It `printf '%s\n' "$*" >> argvFile` **after** the pool has exec'd it through the PTY/service-mode path — the source of the async gap the poll bridges. Do not change it.
- `cmd/pyry/acp_test.go:427-444` — `stripMCPSettingsPair`: consumed on the wait's return value, unchanged.
- `cmd/pyry/acp_test.go:677` and `cmd/pyry/acp_conformance_test.go:481` — the two existing `waitOneClaudeArgv` callers (SessionLoad, FullSessionDrive). The helper signature stays `(t, argvFile)`, so **both are untouched**.
- `docs/lessons.md:356-359` — "Control socket dialability lags `Phase: running` — poll, don't single-shot." The established codebase rule for this exact class: when a readiness signal is set by code that runs *before* the observable, poll the observable with a bounded deadline; do not lean on the readiness signal as a memory barrier. This is why the fix stays a robust poll rather than new `WaitForPTY` plumbing.
- `internal/supervisor/supervisor.go:612-631` — `WaitForPTY` contract, for context on why it is **not** used here (it signals child-spawned, which happens-before the shell's first `printf`, so it cannot serve as the memory barrier for the argv write).

## Context

QA filed three `cmd/pyry` failures under `make check`, all with the identical assertion `fake claude never recorded its argv`:

- `TestACP_SessionNew_SpawnsOneInteractiveClaude` (`acp_test.go:261`)
- `TestACP_SessionLoad_ResumesExistingClaude` (`waitOneClaudeArgv` caller)
- `TestACPConformance_FullSessionDrive` (`acp_conformance_test.go:481`)

Root cause (PO diagnosis, confirmed): a **load-sensitive test-harness flake**, not a product defect. The fake claude is a `/bin/sh` script that appends its argv to `argvFile` only after the pool spawns it through the PTY/service-mode machinery. The test side polls that file with a hardcoded **3 s** deadline. In isolation the argv lands well under 3 s (8/8 pass, ~1.9 s each); under full-`make check` fork/exec contention the QA failing runs each clocked ~3.03–3.07 s — i.e. they hit the 3 s boundary by a hair. `pty.Start` is not interruptible and stretches to "hundreds-of-ms-to-seconds" under `-race` contention (lessons.md:94), so the spawn latency the poll must absorb is genuinely load-dependent. Same family as the #1116 bootstrap-timing flake and the #260 poll-don't-single-shot lesson.

The 3 s deadline is duplicated at two poll sites, both in `cmd/pyry/acp_test.go`: the shared `waitOneClaudeArgv` helper and an inline copy in the session/new test. Two copies of the same too-tight budget is the smell.

## Design

Two changes, both in `cmd/pyry/acp_test.go`, both test-only.

### 1. Single named budget replaces the two hardcoded 3 s deadlines

Add one package-level constant near `waitOneClaudeArgv`:

- `argvRecordTimeout = 15 * time.Second` — with a doc comment stating: *generous budget for the fake child's post-exec argv write under full-suite fork/exec contention; the failure it guards (child never spawned) is real but rare, so a wide margin trades a slightly slower true-failure signal for zero false-negatives under load.*

`waitOneClaudeArgv` uses `argvRecordTimeout` in place of the literal `3 * time.Second` at line 409. The 20 ms poll interval is unchanged.

**Value rationale (architect decision, not a range to punt):** observed worst-case near-miss ~3.07 s; sibling budgets in the same file are 5 s (`pool.Ready()`) and 10 s (serve shutdown). 15 s is ~5× the observed worst-case and 1.5× the largest sibling budget — robustly above worst-case, still short enough to give a clean per-test failure message (`fake claude never recorded its argv`) rather than letting a genuinely-broken child hang to the whole-binary `-timeout`. The upper-bound cost is paid only on a genuine never-spawn failure (rare); the lower-bound cost of too-tight is the recurring flake that masks regressions — asymmetric, so err generous. Do **not** tie the budget to `t.Deadline()`: that would let a truly-broken child hang until the binary timeout, degrading the failure signal.

### 2. Collapse the inline copy onto the shared helper

In `TestACP_SessionNew_SpawnsOneInteractiveClaude`, delete the inline poll block (the `ok := func() bool { ... }()` closure at lines 241-259 and its `if !ok` fatal at 260-262) and replace it with a single call:

```
fields := waitOneClaudeArgv(t, argvFile)
```

The helper returns `strings.Fields(lines[0])`, exactly what the inline block bound to `fields`; the subsequent `stripMCPSettingsPair(t, fields)` / `slices.Equal(..., want)` assertion (lines 266-269) is unchanged. The helper already carries the same divergence-6 `>1`-line fatal and the same empty/one-line handling, so nothing is lost in the collapse.

After this, all three tests reach the argv wait through the one `waitOneClaudeArgv` path — the two sites can no longer drift, satisfying the ticket's "fixed consistently" AC.

### Why not synchronize on child readiness (option b)

The ticket offered "have the fake child signal argv-recorded" as an alternative. Rejected:

- The only IPC between the test process and the separate `/bin/sh` child is the filesystem (`argvFile`) and the PTY. The PTY marker (`CLAUDE_SCREEN_OUTPUT`) is deliberately kept **off** every test-visible sink (AC-1 asserts it never reaches the frame stream), so it cannot be the barrier.
- `WaitForPTY` (supervisor.go:621) signals *child-spawned*, which happens-**before** the shell's first `printf ... >> argvFile`. It therefore cannot serve as the memory barrier for the argv write; per lessons.md:359 a bounded poll of the observable is still required after it. Threading `pool`+`id` into the helper and the conformance harness to gate on `WaitForPTY`, only to still poll the residual exec→first-write gap, is disproportionate plumbing for a test flake ("belt-and-suspenders, same fabric").
- A FIFO barrier would trade the poll-timeout for an open-blocking-timeout and break the append-based divergence-6 line count. No net win.

The codebase's own established answer to "readiness set upstream of the observable" is the bounded generous poll (lessons.md:356-359). This fix is that answer, deduplicated.

## Concurrency model

Unchanged. The wait is a single-goroutine bounded poll of an on-disk file written by an out-of-band child process; no new goroutines, channels, or synchronization primitives. Only the poll's upper bound widens.

## Error handling

Unchanged and preserved exactly:

- **Never recorded** → after `argvRecordTimeout` the helper `t.Fatal("fake claude never recorded its argv")`. Same message, later deadline.
- **Spawned more than once** (divergence-6 violation) → `t.Fatalf("claude spawned %d times, want exactly 1 …")` fires the instant a second line appears; the wider budget does not weaken this (it gives the *first* line more time, and still catches a second line on any poll iteration before returning).
- **Exactly one line** → returns `strings.Fields(lines[0])`; caller strips the `--settings` pair and asserts `--session-id <uuid>`.

## Testing strategy

No new tests — this fixes existing tests. Verification is a load repro, since a deterministic fails-on-main/passes-after liveness proof is impossible for a probabilistic-under-load flake (ticket acknowledges this):

- **Build/consistency:** `go build ./... && go vet ./...`; confirm `grep -n '3 \* time.Second' cmd/pyry/acp_test.go` returns nothing and the only remaining argv-wait deadline is the single `argvRecordTimeout` constant used by `waitOneClaudeArgv`; confirm `TestACP_SessionNew_SpawnsOneInteractiveClaude` now calls `waitOneClaudeArgv` (no inline poll closure remains).
- **Load repro (the pragmatic evidence):** run the three named tests at high `-count` under `-race` while the rest of the suite contends, e.g. the ticket repro at `-count=20`, and/or back-to-back `make check`. They must not fail with `fake claude never recorded its argv`.
  ```
  go test -race -count=20 -run '^TestACPConformance_FullSessionDrive$|^TestACP_SessionLoad_ResumesExistingClaude$|^TestACP_SessionNew_SpawnsOneInteractiveClaude$' ./cmd/pyry/...
  ```
- **Correctness preserved:** the same runs still enforce exactly-one-spawn (divergence-6) and `argv == --session-id <uuid>` after the `--settings` strip.

## Acceptance criteria mapping

- AC-1 (tolerates worst-case latency under contention) → the `3 s` → `argvRecordTimeout` (15 s) widening in the single shared helper.
- AC-2 (both poll sites fixed consistently) → the inline copy in session/new collapses onto `waitOneClaudeArgv`; one wait path, no drift.
- AC-3 (existing correctness assertions unchanged) → divergence-6 `>1` fatal and the `--session-id <uuid>` equality (post `--settings` strip) are untouched.
- AC-4 (test-only) → only `cmd/pyry/acp_test.go` (a `_test.go` file) changes; zero product/non-test code.

## Open questions

None. The value 15 s is decided (see rationale); the shape is decided; scope is a single test file.
