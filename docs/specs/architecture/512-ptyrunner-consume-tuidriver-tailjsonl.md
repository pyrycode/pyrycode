# Spec — #512: `ptyrunner.Run` consumes `tuidriver.TailJSONL` inline; delete `internal/agentrun/jsonl/tail/`

Confirms PO's size: **S**. Two production files modified (`internal/agentrun/ptyrunner/runner.go`, `internal/agentrun/budget/budget.go`), two production files deleted (`internal/agentrun/jsonl/tail/watcher.go` and `_test.go`), two test files migrated (`internal/agentrun/budget/budget_test.go`, light tweak to `internal/agentrun/ptyrunner/runner_test.go`). Net ~140 LOC written; ~700 LOC deleted. Single consumer per migrated symbol; no callback abstraction is added.

## Context

This is the second of two slices that decouple the runtime agent-run hot path from the local `internal/agentrun/jsonl` package's `Reader` / `Event` shape. #511 migrated `streamjson.Emitter.Emit` from `jsonl.Event` → `tuidriver.JSONLEntry` and introduced `eventToEntry` as a throwaway adapter inside `ptyrunner.Run`. This ticket pivots the watcher itself to `tuidriver.TailJSONL`, which delivers `JSONLEntry` natively — so `eventToEntry` is deleted (no caller), `budget.Counter.OnEvent` is migrated to the new shape, and the bespoke `internal/agentrun/jsonl/tail/` watcher is removed (no caller).

`internal/agentrun/jsonl` (the `Reader` / `Event` / `Config` surface) **stays for now** because `selfcheck/selfcheck.go` parses `streamjson.Emitter`'s pipe output via `jsonl.NewReader`, and `e2e/realclaude/fixtures.go` (+ test) parses captured fixture files the same way. Both consume an `io.Reader` to EOF — `tuidriver.TailJSONL` doesn't cover that shape (it opens a file path, not a reader, and treats EOF as "wait for more"). A follow-up ticket migrates those two consumers and deletes the `jsonl` package entirely. PO's refinement dropped the `package-delete-if-no-consumers-remain` AC for exactly this reason.

The `budget.Counter.OnEvent` signature change is gated to this ticket because `OnEvent` is the only `Counter` surface that takes a JSONL value, and migrating it lets `budget` drop its `internal/agentrun/jsonl` import (one of two consumers of that import — the other is `ptyrunner.eventToEntry`, also deleted here). After this ticket, `internal/agentrun/budget/` and `internal/agentrun/ptyrunner/` no longer import `internal/agentrun/jsonl`.

## Design

### `ptyrunner/runner.go` — inline channel drain replaces watcher

The current watcher-wiring block (runner.go:360–399) has four moving parts: the `emitErr` capture variable, the `tail.New(tail.Config{…})` constructor call, the `OnEvent` / `OnEndOfTurn` callback closures, and the `watcher.Run(runCtx)` blocking call. The replacement collapses the constructor + callback shape into a straight-line stat-wait + channel drain.

Replacement shape (call-site sketch — do not paste verbatim; mirror the existing return-error conventions for each wrap):

- Compose the on-disk path:
  - `home := cfg.HomeDir; if home == "" { h, err := os.UserHomeDir(); if err != nil { return fmt.Errorf("ptyrunner: home dir: %w", err) }; home = h }`
  - `jsonlPath := tuidriver.SessionJSONLPath(home, cfg.WorkDir, cfg.SessionID)`
- Wait for the file to appear (bounded by `runCtx`):
  - `if err := tuidriver.WaitForSessionJSONL(runCtx, jsonlPath); err != nil { if isCtxErr(runCtx, err) { return nil }; return fmt.Errorf("ptyrunner: wait jsonl: %w", err) }`
- Open the tail channel:
  - `entries, err := tuidriver.TailJSONL(runCtx, jsonlPath, 0); if err != nil { if isCtxErr(runCtx, err) { return nil }; return fmt.Errorf("ptyrunner: tail: %w", err) }`
- Drain inline (in the existing `Run` goroutine — NO new goroutine for the drain):
  - `for entry := range entries { … }`
  - Inside the loop:
    - `if eerr := emitter.Emit(entry); eerr != nil && emitErr == nil { emitErr = eerr }`
    - `counter.OnEvent(entry)`
    - `if tuidriver.IsEndTurn(entry) { counter.OnEndOfTurn(); break }`
- After the loop:
  - `if runCtx.Err() != nil { return nil }`
  - `if emitErr != nil { return fmt.Errorf("ptyrunner: emit: %w", emitErr) }`
  - `return nil`

Key invariants the inline shape preserves:

- **End-of-turn semantics.** `tuidriver.IsEndTurn` is checked AFTER `OnEvent`. This ordering matches today's `tail.Watcher.drain` which calls `OnEvent` before `OnEndOfTurn` (`watcher.go:219-222`). The `break` exits the loop on the first end-of-turn entry, identical to today's "stop draining" behaviour.
- **`emitErr` capture-then-prioritise.** The capture (`if … && emitErr == nil`) and the post-loop prioritisation (`emitErr` over the loop's natural exit) are preserved verbatim. Spec text (#512 body, Technical Notes) explicitly defers the deeper restructuring of `emitErr` to #513.
- **ctx-cancel collapse.** A cancelled `runCtx` causes `TailJSONL`'s goroutine to close `entries`, which terminates the `for range`. The post-loop `if runCtx.Err() != nil { return nil }` collapses cancellation to nil, matching today's `if runCtx.Err() != nil { return nil }` (`runner.go:390-392`).
- **Cleanup LIFO unchanged.** `cancel() → wg.Wait() → counter.Stop() → emitter.Close() → sess.Close()`. The only change: `wg` now tracks ONLY the watchdog goroutine (the watcher's separate goroutine collapses into the inline drain). The two `defer cancel()` and `defer wg.Wait()` lines stay in their current positions (runner.go:319–320 and 386–387).

The home-dir resolution moves from `tail.New` (deleted) into `Run`. Today's `tail.New` consults `os.UserHomeDir()` lazily; the inline version replicates that exactly (zero behaviour change). The `cfg.HomeDir` test seam continues to work because we read `cfg.HomeDir` first.

The MkdirAll defence (`watcher.go:112`) is **dropped**. Rationale: in production claude itself creates `~/.claude/projects/<encoded>/` on first JSONL write; `WaitForSessionJSONL` polls until the file appears regardless of whether the parent dir exists yet. In tests the helper (`helper_test.go:130`) already MkdirAll's before writing. No surface relies on `ptyrunner` pre-creating the dir.

### `eventToEntry` adapter — deleted

`eventToEntry` (runner.go:420–460) and its companion comments are removed in full. Its sole call site is the `OnEvent` closure inside the watcher Config struct, which is also removed. The `internal/agentrun/jsonl` import on runner.go drops with it.

### `budget/budget.go` — `OnEvent` signature + semantic change

Signature changes from:

```go
func (c *Counter) OnEvent(ev jsonl.Event)
```

to:

```go
func (c *Counter) OnEvent(entry tuidriver.JSONLEntry)
```

The body keeps its `mu` discipline. The two field reads remap:

| Old | New |
|---|---|
| `ev.Kind != "assistant"` | `entry.Type != "assistant"` |
| `if ev.EndOfTurn { … return }` (lines 109–114) | **deleted** |

### `budget.OnEvent` semantic change — boundary case

Removing the `ev.EndOfTurn` early-return branch changes one observable behaviour: the **budget boundary** case (MaxTurns assistant entries arrive, the last one is end-of-turn).

Old behaviour:
- `OnEvent(EOT entry)` increments count to MaxTurns but the EOT branch short-circuits before the budget check fires. `Terminate()` is NOT called. `OnEndOfTurn` then sets `reason = completion`.

New behaviour:
- `OnEvent(EOT entry)` increments count to MaxTurns; with no EOT branch, the budget check fires. `c.fired = true`, `c.reason = ReasonMaxTurns`, `Terminate()` is called, the grace timer is scheduled. The caller's subsequent `counter.OnEndOfTurn()` (ptyrunner invokes it because `IsEndTurn(entry)` is true) is then a no-op because the first-terminal-wins guard at budget.go:145–147 only writes when `c.reason == ""`.

The boundary run is classified as `max_turns`, not `completion`. The `Terminate` SIGTERM races claude's natural exit — both processes converge on "claude exits"; the race is benign because the streamjson trailer fields (driven by `emitter.SetExitReason(ExitReasonMaxTurns)` from ptyrunner's Terminate callback) reflect the budget-hit classification, and `Stop()` in the cleanup LIFO cancels the grace timer before SIGKILL fires.

This is a deliberate semantic change committed to in this ticket's AC ("classification is the caller's job"). The caller (ptyrunner.Run) calls `OnEndOfTurn` only when `IsEndTurn(entry)` is true — the AC is explicit that it does so AFTER `OnEvent(entry)`. The interpretation is: at the boundary, the budget wins because the budget check fires first within the OnEvent call (synchronously, under `c.mu`), and the post-call `OnEndOfTurn` cannot overwrite a non-empty reason.

The dropped test `TestOnEvent_BudgetBoundaryEndOfTurnIsCompletion` is the only test pinning the old behaviour; it is **deleted**. A short doc-comment line on the new `OnEvent` body (one-liner like `// MaxTurns is enforced on every assistant entry, including the end-of-turn entry — classification at the boundary is max_turns.`) preserves the intent for future readers.

### Concurrency model

The watcher goroutine collapses. After this ticket:

- `Run` goroutine: drains the `tuidriver.TailJSONL` channel inline. Calls `emitter.Emit`, `counter.OnEvent`, `counter.OnEndOfTurn`. Returns when EOT fires, ctx cancels, or an `emitErr` short-circuits.
- `tuidriver.TailJSONL` goroutine: owned by the tuidriver library. Closes the channel on ctx cancel or unrecoverable read error. Lifecycle bounded by `runCtx` (a child of `ctx`); cancelled by the cleanup LIFO's `defer cancel()`.
- Watchdog goroutine: unchanged. Spawned via `wg.Add(1) / go runWatchdog(…)`, drained by `defer wg.Wait()`.
- Budget grace-timer goroutine: unchanged. Spawned by `time.AfterFunc` inside `OnEvent`'s budget-hit branch; cancelled by `counter.Stop()` in the cleanup LIFO.

Net change: one fewer pyry-side goroutine. The tuidriver-library goroutine is bookkept by tuidriver itself (the channel close on ctx cancel is the lifecycle contract).

### Error handling

The new error-wrap names introduced and removed:

- **Removed**: `fmt.Errorf("ptyrunner: tail: %w", err)` (today wraps `tail.New` and `Watcher.Run`).
- **Added**: `fmt.Errorf("ptyrunner: home dir: %w", err)` (when `cfg.HomeDir == ""` and `os.UserHomeDir()` fails — defensive; production paths set HomeDir or have a valid home env).
- **Added**: `fmt.Errorf("ptyrunner: wait jsonl: %w", err)` (when `WaitForSessionJSONL` returns a non-ctx error — e.g. a permission denied on stat).
- **Added**: `fmt.Errorf("ptyrunner: tail: %w", err)` (when `TailJSONL` returns a synchronous open/seek failure — name reused for similar shape).
- **Unchanged**: `fmt.Errorf("ptyrunner: emit: %w", err)` (capture-then-prioritise pattern, surfaced from the loop body).

The `Run` doc-comment block (lines 181–216) must be updated to reflect the new wrap names. Specifically:
- Drop the `fmt.Errorf("ptyrunner: tail: %w", err) on tail.New / Watcher.Run failure` bullet (lines 198–199 today).
- Add bullets: `home dir` wrap, `wait jsonl` wrap (collapsed to nil on ctx-cancel), `tail` wrap (now meaning `TailJSONL` open/seek failure, also collapsed on ctx-cancel).
- Keep the `emit:` bullet verbatim.

The `isCtxErr` helper (runner.go:467–475) is reused for the wait-jsonl and tail-open paths — same collapse-to-nil contract that `Spawn` and `WaitUntil` use today.

## Testing strategy

### `runner_test.go` — no structural changes

The existing `Run` tests drive the full inline path through a real PTY + a real file on disk:

- `TestRun_HappyPath_EmitsAndEndOfTurn` — exercises the inline channel drain against `happyPathBody` (a one-line EOT JSONL entry). Asserts the init line, the verbatim assistant line, and the trailer's `success` / `completed` shape. **Unchanged.**
- `TestRun_CtxCancelDuringStream` — exercises the ctx-cancel collapse during the drain. The `tuidriver.TailJSONL` channel closes when `runCtx` cancels; the `for range` terminates; the post-loop `if runCtx.Err() != nil { return nil }` collapses. Asserts trailer subtype `error_during_execution`. **Unchanged.**
- `TestRun_EmitErrorPropagation` — exercises the `emitErr` capture path. **Unchanged.**
- `TestRun_BudgetHitBeforeEndOfTurn` — exercises the budget hit on a non-EOT noEot stream. **Unchanged.**
- `TestRun_WatchdogFires` — exercises the watchdog goroutine collapse with an empty JSONL body. **Unchanged.**
- `TestRun_TrustModalDetected` / `McpFailureDetected` / `NetworkFailureDetected` / `CtxCancelDuringSpawn` — short-circuit BEFORE the drain. **Unchanged.**
- `TestBuildArgs`, `TestRun_MissingRequiredFields` — no drain involvement. **Unchanged.**

The `helper_test.go` doc comment at lines 124–129 referencing `tail.New`'s `fsnotify.Add` is updated to mention `tuidriver.WaitForSessionJSONL` polling. The MkdirAll at line 130 stays — the helper still owns parent-dir creation in the jsonl test mode.

Verify by running `go test -race ./internal/agentrun/ptyrunner/...` — every test in this suite should pass without modification. If any test starts to flake or fail, that's evidence of a behaviour drift the inline drain introduced (most likely candidate: ordering of `Emit` / `OnEvent` / `IsEndTurn` checks in the loop body). The fixtures are deterministic, so a flake here is a real bug, not test-suite noise.

### `budget_test.go` — literal migration + boundary-test deletion

Bullet-pointed scenario list (each maps directly to a test function — the developer writes the test body in the project's testing idiom):

- **Helper rewrite.** `assistantEvent(endOfTurn bool) jsonl.Event` → `assistantEntry() tuidriver.JSONLEntry` returning `tuidriver.JSONLEntry{Type: "assistant"}`. The `endOfTurn` parameter is dropped (no longer carried on the entry as an OnEvent input).
- **Migration of every other `jsonl.Event{...}` literal.** Map `Kind` → `Type`. The non-assistant kinds list at line 103 stays as a string slice; each test entry becomes `tuidriver.JSONLEntry{Type: kind}`. The `Kind: "user"` literal at line 313 becomes `Type: "user"`.
- **`TestNew_Validation`** — unchanged (no entries).
- **`TestOnEvent_NonAssistantKindsDoNotCount`** — substitute `assistantEntry()` for `assistantEvent(false)`; assert the same Terminate-call sequencing.
- **`TestOnEvent_SIGTERMFiresExactlyAtBudget`** — substitute `assistantEntry()` for `assistantEvent(false)`; assert the same Terminate-at-budget / no-re-fire behaviour.
- **`TestOnEvent_SIGKILLFiresAfterGrace`** — substitute `assistantEntry()` for `assistantEvent(false)`; assert the same Kill-after-grace timing including the systemic-slop comment block.
- **`TestStop_CancelsPendingSIGKILL` / `TestStop_WithoutBudgetHit`** — substitute `assistantEntry()`; unchanged otherwise.
- **`TestOnEndOfTurn_ReasonCompletion`** — both events become `assistantEntry()` (no `true` variant); assert `Reason == ReasonCompletion` after `OnEndOfTurn`.
- **`TestOnEndOfTurn_DoesNotOverwriteMaxTurns`** — substitute `assistantEntry()` for `assistantEvent(false)`; assert the same `max_turns`-wins behaviour.
- **`TestOnEvent_BudgetBoundaryEndOfTurnIsCompletion`** — **DELETE**. The branch this test pinned is gone; the boundary case is now `max_turns`, which is already covered by `TestOnEvent_SIGTERMFiresExactlyAtBudget`. Add a one-liner reference in `OnEvent`'s body comment (see § Design) so the reader understands the deliberate semantic change.
- **`TestReason_ZeroValueBeforeTerminalEvent`** — substitute `assistantEntry()` and `tuidriver.JSONLEntry{Type: "user"}`; unchanged assertion.
- **`TestTerminateError_DoesNotBlockKill` / `TestKillError_IsLogged`** — substitute `assistantEntry()`; unchanged otherwise.

### Watcher tests — deleted with the package

`internal/agentrun/jsonl/tail/watcher_test.go` is deleted with `watcher.go`. The behaviours it covered (file-appearance wait, partial-line reassembly, fsnotify event handling, EOT detection, ctx-cancel) are now owned by `tuidriver.TailJSONL` and `tuidriver.WaitForSessionJSONL` — both covered by tui-driver's own test suite. The pyry-side tests that exercise the end-to-end behaviour (the ptyrunner tests above) are sufficient integration coverage.

### Verification commands

The ticket's AC mandates these specific checks; the developer runs them as the last step before opening the PR:

- `go list -deps ./internal/agentrun/ptyrunner/...` — `internal/agentrun/jsonl/tail` MUST NOT appear in the output. (`internal/agentrun/jsonl` MAY appear — but in fact it should also disappear because `ptyrunner` and `budget` both drop the import in this ticket. selfcheck/e2e still depend on it via other packages.)
- `make e2e-realclaude` — the byte-equivalence test (#506) must remain green. The wire shape is unchanged: `entry.RawLine` is what `Emit` writes, and `tuidriver.TailJSONL`'s `parseEntry` produces a byte-identical `RawLine` to the deleted `jsonl.Reader.Next`'s `Raw` (both copy the source line with the trailing `\n` stripped; both preserve `\r` if present).
- `make check` — build + unit tests + `go vet`.

## Migration / rollout

Single commit. No feature flag. The wire surface is unchanged (the streamjson trailer / per-line emission both stem from `entry.RawLine`, which is byte-identical to the deleted `jsonl.Event.Raw`). The behaviour change is bounded to the budget-boundary classification (now `max_turns`, was `completion`), which the byte-equivalence test does not exercise (the captured fixture is well under MaxTurns).

After merge, the next ticket in this thread (TBD) migrates `selfcheck/selfcheck.go` and `e2e/realclaude/fixtures.go` off `internal/agentrun/jsonl` (likely via a tui-driver non-tail parser or a small inlined parser) and deletes the `jsonl` package. The #513 capstone then collapses the `emitErr` capture-then-prioritise pattern in `ptyrunner.Run`.

## Open questions

- **Should `Run` MkdirAll the encoded project dir defensively?** The deleted `tail.New` did so as belt-and-suspenders. The polling stat loop in `WaitForSessionJSONL` does not require it (a missing parent surfaces as `IsNotExist`, same as a missing file). In production, claude creates the dir on its first JSONL write — the wait loop converges. **Decision: do not MkdirAll.** Evidence-based fix selection (§ Pipeline-Wide Principles): no observed failure mode where `WaitForSessionJSONL` blocks on a missing parent dir; the test helper already mkdir's; production has 100% historical reliability of claude creating the dir before writing. Adding the mkdir would be a defence against an unobserved failure.

## Out of scope (handled by future tickets)

- `internal/agentrun/selfcheck/selfcheck.go`'s `jsonl.NewReader` consumer — needs a non-tail `io.Reader → JSONLEntry` parser.
- `internal/e2e/realclaude/fixtures.go` and `fixtures_test.go`'s `jsonl.NewReader` consumer + `type JSONLEntry = jsonl.Event` alias.
- Deletion of `internal/agentrun/jsonl/` itself (gated on the two consumers above migrating).
- `ptyrunner.Run`'s `emitErr` capture-then-prioritise restructure (#513 capstone).
- A `tuidriver.ParseEntry(line []byte) (JSONLEntry, bool)` exported helper that the selfcheck / e2e migration would need.

## Scope self-check

Files this spec prescribes new or modified content for (production source files only — `*.go`, excluding `*_test.go`, excluding spec/docs/markdown):

1. `internal/agentrun/ptyrunner/runner.go` — modified (inline TailJSONL drain replaces tail.Watcher wiring; `eventToEntry` deleted; doc + import updates).
2. `internal/agentrun/budget/budget.go` — modified (`OnEvent` signature + EOT-branch deletion; doc + import updates).
3. `internal/agentrun/jsonl/tail/watcher.go` — **deleted**.

Count: **3** (2 modified, 1 deleted). Below the ≥5 split threshold.

Edit fan-out check (via `grep -rn '"github.com/pyrycode/pyrycode/internal/agentrun/jsonl/tail"' --include="*.go" .`):
- `internal/agentrun/ptyrunner/runner.go:55` — single import site, removed by this ticket.

Edit fan-out check on `budget.Counter.OnEvent(jsonl.Event)`:
- `internal/agentrun/ptyrunner/runner.go:369` — single production call site (inside the to-be-deleted closure; rewritten in the inline drain).
- `internal/agentrun/budget/budget_test.go` — ~14 in-test call sites; mechanical literal migration.

Both well below the 10-call-site red line.

Total LOC projection: ~70 runner.go churn (mostly delete) + ~15 budget.go + ~50 budget_test.go migration + ~5 runner_test.go / helper_test.go doc-comment tweaks = **~140 LOC written**, plus ~700 LOC deleted (watcher.go + watcher_test.go). Well under the ~600 LOC ceiling. Number of distinct reject branches in the new state machine: 3 wrap-and-return errors (`home dir`, `wait jsonl`, `tail`), each a one-liner. Well under the 10-branch red line.
