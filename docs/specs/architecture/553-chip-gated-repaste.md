# Spec #553 — Chip-gated repaste (hasPastedChip gate + #227 protection)

**Ticket:** [#553](https://github.com/pyrycode/pyrycode/issues/553) · **Size:** S · **Package:** `internal/agentrun/ptyrunner`

This is a **faithful re-implementation from an already-validated, fully-preserved
design** — not a redesign. The 2026-05-30 work was written, unit-tested, and proven
clean RED→GREEN, then lost uncommitted on a wiped local branch. The design lives in
the recovered session log and the live-probe evidence; the job is to re-create it
byte-for-intent. **The evidence base is closed — do NOT re-run live probes.**

## Files to read first

- `internal/agentrun/ptyrunner/runner.go:336-372` — the prompt-deliver retry loop. The
  unconditional re-deliver `logger.Warn` at **L364** is the exact line the chip gate
  replaces. Extract: loop structure, `committed` flag semantics, `promptDidCommit` call
  shape.
- `internal/agentrun/ptyrunner/runner.go:520-556` — `maxPromptAttempts`/`defaultPromptCommitTimeout`/
  `promptCommitPoll` consts + `promptDidCommit`. Extract: the detector idiom (`IsThinking`
  OR `os.Stat(jsonl)`), where to add `hasPastedChip` and `commitModeJSONLDelay` neighbours.
- `internal/agentrun/ptyrunner/runner.go:39-56` — import block. **`"bytes"` is NOT yet
  imported** — the developer must add it for `bytes.Contains`.
- `internal/agentrun/ptyrunner/runner_test.go:19-36` — `loggerSyncWriter` (mutex-guarded
  `strings.Builder`). The integration tests capture `cfg.Logger` through this.
- `internal/agentrun/ptyrunner/runner_test.go:536-565` — `TestRun_MaxTurnsExhaustion_NoBenignWarns`.
  **This is the pattern to copy** for both new integration tests: `slog.NewTextHandler(logBuf,
  &slog.HandlerOptions{Level: slog.LevelWarn})`, run, assert on `logBuf.String()`.
- `internal/agentrun/ptyrunner/runner_test.go:567-604` — `TestRun_WatchdogFires`. Shows the
  `PromptCommitTimeout` override + short-window idiom the new tests reuse.
- `internal/agentrun/ptyrunner/helper_test.go:69-164` — `runHelper`: the idle-render `switch`
  (L76-98) and the `jsonl` delayed-write goroutine (L125-164). The two new modes extend
  both; `writeSessionJSONLBody` is extracted from L149-162.
- `internal/agentrun/ptyrunner/helper_test.go:48-79` — `helperRunCfg` + `happyPathBody`. The
  new integration tests call `helperRunCfg(t, "<mode>", …, happyPathBody)`.
- tui-driver `pkg/tuidriver/ansi.go:18-23` — `StripANSI(snap []byte) []byte`. Confirmed
  present at the pinned module version; the detector calls it.
- tui-driver `pkg/tuidriver/state.go:38-54` — `IsIdle` / `IsThinking`. `hasPastedChip`
  mirrors `IsThinking`'s one-line `bytes.Contains(StripANSI(snap), …)` shape. Note: the
  chip text carries no `✻`, so `IsIdle` still fires when the chip is present.

## Context

PR #547 (Mode B paste-recovery, merged) re-delivers a prompt **unconditionally** whenever
`promptDidCommit` times out (`runner.go:364`). Under a slow MCP cold-start (~25% of runs)
neither commit signal is up inside the window — no spinner, no session JSONL yet — even
though the paste *did* commit. The unconditional re-deliver then fires the destructive
`ClearInputLine()` + repaste on an in-flight turn: **regression #227**.

A closed N=60 live probe on claude 2.1.158 established the discriminator: the `Pasted text`
input-box chip is present ⟺ the paste is uncommitted (wedge & chip = 9/9, wedge & no-chip = 0,
no-wedge & chip = 0 — zero counterexamples; `"Pasted text"` validated as the exact anchor).
This ticket restores the gate that tells a **genuine wedge** (chip present → re-deliver) from a
**committed-but-slow** turn (no chip → do NOT re-deliver).

Blocks #552 (TUI flight-recorder), which edits the same files and is gated on this landing.

## Design

Two production changes in `runner.go`, both small and additive.

### 1. The `hasPastedChip` detector

A pure function over a buffer snapshot, placed next to `promptDidCommit`. Contract:

```go
// hasPastedChip reports whether the input box still shows the "Pasted text"
// chip — positive evidence the bracketed paste is uncommitted. Matches after
// StripANSI so an ANSI-escaped chip still hits.
func hasPastedChip(snap []byte) bool // = bytes.Contains(tuidriver.StripANSI(snap), []byte("Pasted text"))
```

Mirrors `tuidriver.IsThinking`'s shape. Requires adding `"bytes"` to the import block.

### 2. Gate the re-deliver branch

Replace the single unconditional warn at `runner.go:364` with a chip-gated decision. The
rest of the loop (attempt cap, `ClearInputLine` on attempt > 1, `WritePrompt`, the
`promptDidCommit` check, the `committed` flag, the post-loop backstop) is **unchanged**.

Contract for the replaced branch (the two log strings are **test observables — copy verbatim,
do not paraphrase**):

```go
if promptDidCommit(ctx, sess, jsonlPath, commitTimeout) {
    committed = true
    break
}
if !hasPastedChip(sess.Buffer.Snapshot()) {
    // No chip → paste committed; signals just lag. Re-delivering here is the
    // destructive #227 path. Treat as committed-but-slow and stop retrying.
    logger.Warn("ptyrunner: commit signals slow but input box empty (no pasted-text chip) — assuming committed-but-slow, not re-delivering")
    committed = true
    break
}
logger.Warn("ptyrunner: prompt uncommitted (pasted-text chip present); re-delivering")
```

Decision semantics:

| `promptDidCommit` | chip present? | action | `committed` | log line |
|---|---|---|---|---|
| true | — | break | true | (none) |
| false | **no** | break (committed-but-slow) | **true** | `…input box empty (no pasted-text chip) — assuming committed-but-slow, not re-delivering` |
| false | **yes** | loop → re-deliver | false | `…prompt uncommitted (pasted-text chip present); re-delivering` |

Setting `committed = true` on the no-chip path is evidence-based (no-chip ⟺ committed, 0
counterexamples) and suppresses the misleading `…may wedge` backstop warn. **Either break path
still falls through to `WaitForSessionJSONL`**, so a committed-but-slow turn completes cleanly
once its lagging JSONL lands — the gate only decides whether to *re-paste*, never whether to
*wait*.

**Discriminator invariant (load-bearing for the tests):** the substring
`(pasted-text chip present); re-delivering` is unique to the wedge line and absent from the
committed-but-slow line (which ends `…not re-delivering`). Both messages contain the word
"re-delivering"; only the wedge line contains the parenthesized marker. The tests key on the
marker, not on "re-delivering".

## Concurrency model

No new concurrency in production code. The gate is a synchronous decision inside the existing
single-goroutine retry loop, before the watchdog/emitter goroutines spawn. `sess.Buffer.Snapshot()`
is already the thread-safe read used by `promptDidCommit` and the idle wait.

Test-side: the two new fake-claude modes each add one goroutine that waits on `stdinSeen`,
sleeps `commitModeJSONLDelay`, then writes the JSONL body — identical lifecycle to the existing
`jsonl` mode's writer goroutine, just delayed.

## Error handling

The gate adds no new error paths. `hasPastedChip` is total (a `bytes.Contains` over a possibly-empty
snapshot — empty → false). The existing wrapped-error returns (`clear input line`, `write prompt`)
are untouched. A false negative from the detector (chip missed) costs at most one skipped re-delivery
on a genuine wedge, which the downstream JSONL-wait + watchdog still backstop; a false positive costs
one extra (non-destructive on a still-garbled line) re-paste. Both are bounded by `maxPromptAttempts`.

## Testing strategy

### Unit — `TestHasPastedChip` (table-driven, ~5 cases)

Pure detector over hand-built snapshots. Scenarios (inputs → expected):

- chip present, plain ASCII (`…[Pasted text +3 lines]…❯`) → `true`
- no chip (`❯ ` only) → `false`
- ANSI-escaped chip (CSI sequences interleaved, e.g. dim-on/reset around the chip text) →
  `true` (asserts the `StripANSI` step is load-bearing)
- empty snapshot (`[]byte{}` or `nil`) → `false`
- near-miss substring (`"Pasted "` / `"Paste text"` — present but not the exact anchor) → `false`

### Integration — two logger-asserting tests

Both observe the loop's decision through the **captured `cfg.Logger`** (NOT a fake-claude stderr
sentinel — the fake-claude's `os.Stderr` is not wired into the parent's `cfg.Stderr` under the PTY;
that was the first attempt's mistake and it observed nothing). Copy the
`TestRun_MaxTurnsExhaustion_NoBenignWarns` wiring: `loggerSyncWriter` + slog `TextHandler` @ `LevelWarn`.
Each test sets `cfg.PromptCommitTimeout = 200 * time.Millisecond` and a ~10s context.

- **`TestRun_CommitWedge_ChipPresent_ReDelivers`** — mode `commit_wedge_chip`, body `happyPathBody`.
  Assert: `Run` returns nil; captured log **contains** `(pasted-text chip present); re-delivering`.
- **`TestRun_CommitSlow_NoChip_DoesNotReDeliver`** — mode `commit_slow_nochip`, body `happyPathBody`.
  Assert: `Run` returns nil; captured log **contains** `input box empty (no pasted-text chip)`
  **AND does NOT contain** `(pasted-text chip present); re-delivering` (the #227 protection).

### Fake-claude fixtures (`helper_test.go`)

Add two modes + one extracted helper + one const:

- **`commit_wedge_chip`** — at idle, render `"[Pasted text +3 lines]" + idleGlyph + " "` (chip present;
  no `✻`, so `IsIdle` still fires). On `stdinSeen`, sleep `commitModeJSONLDelay`, then write the body.
- **`commit_slow_nochip`** — at idle, render `idleGlyph + " "` (no chip — same render as `jsonl`/`idle`;
  the *only* difference from `jsonl` is the delayed write). On `stdinSeen`, sleep `commitModeJSONLDelay`,
  then write the body.
- **`writeSessionJSONLBody(path, body string)`** — extract the existing `MkdirAll` → `OpenFile`
  (`O_CREATE|O_WRONLY|O_APPEND`, `0o600`) → `WriteString` → `Sync` → `Close` block from the `jsonl`
  goroutine (`helper_test.go:149-162`) so all three modes share it. Refactor `jsonl`/`jsonl_exit143`
  to call it (no delay) to prove the extraction is behaviour-preserving.
- **`commitModeJSONLDelay`** — a package const, ~500 ms.

**Timing contract (why it is robust, not why it is tight):** the only requirement is
`commitModeJSONLDelay > PromptCommitTimeout` so the parent's first commit window elapses with no
JSONL and the gate is exercised. With 500 ms vs 200 ms there is a 300 ms margin. Correctness does
**not** depend on the wedge committing within the `maxPromptAttempts × PromptCommitTimeout` retry
budget: even if `committed` stays false and the `…may wedge` backstop fires, control falls through to
`WaitForSessionJSONL`, which picks up the delayed body and completes the run cleanly. This is what
makes the suite `-race -count=5` stable.

### RED→GREEN (required, demonstrate it)

1. Disable the gate: change `if !hasPastedChip(…)` to `if false && !hasPastedChip(…)`.
2. Run `go test ./internal/agentrun/ptyrunner/`. Confirm `TestRun_CommitSlow_NoChip_DoesNotReDeliver`
   **FAILS** — with the gate off, the committed-but-slow turn falls through to the unconditional
   re-deliver (the destructive #227 path), so the captured log contains the wedge marker and the
   negative assertion trips. `TestRun_CommitWedge_ChipPresent_ReDelivers` stays PASS (a genuine wedge
   re-delivers either way).
3. Restore the gate. Confirm both PASS.
4. **Grep for `if false` before finishing** — no gate-disabling toggle may remain.

### Green + race-clean (AC)

`go build ./... && go vet ./internal/agentrun/ptyrunner/ && go test ./internal/agentrun/ptyrunner/`
all pass; `go test -race -count=5 ./internal/agentrun/ptyrunner/` is clean. **Read the actual `ok …`
line and paste it into the closing comment** — do not claim green from memory.

## Operator handback (do NOT skip)

Do **NOT** install or merge. Hand back to the operator for rebuild/install (inode-cache gotcha +
the ~2026-06-14 `claude -p` billing deadline). Branch off **current** `main`. Rollback = `git checkout main`.

## Open questions

- **Detector placement** — `hasPastedChip` is specified in `runner.go` next to `promptDidCommit` to
  keep the production-file count at 1. If the developer prefers a `detector.go`/`detector_test.go`
  split, that is acceptable and still under the file-count gate, but the single-file placement matches
  the existing convention (all package detectors and helpers currently live in `runner.go`).
- **Chip glyph form** — the fixture renders the literal `[Pasted text +3 lines]`. The detector keys
  only on the `"Pasted text"` substring (the validated anchor), so the exact bracket/line-count
  decoration in the fixture is illustrative and need not match any specific claude build.
