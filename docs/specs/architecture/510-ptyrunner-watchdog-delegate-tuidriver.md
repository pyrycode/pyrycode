# Spec — #510: `ptyrunner/watchdog` delegates spinner regex + tick loop to tuidriver

Confirms PO's size: **S**. One production file shrinks from 89 LOC to ~20 LOC of thin glue; one test function deletion; `runner.go` is not touched (the glue keeps the existing function signature). Sibling of #509 (same delegation pattern, the JSONL side).

## Files to read first

- `internal/agentrun/ptyrunner/watchdog.go` (full file, 89 LOC) — the only production file in scope. Every symbol it defines is on the chopping block:
  - 15 — `defaultWatchdogTick` constant. Goes away (upstream handles the zero-tick fallback via `WatchdogOpts.Tick <= 0 → DefaultWatchdogTick`).
  - 17–27 — `spinnerSecondsRe` var + its "consumer-policy not driver-policy" rationale comment. Goes away; the rationale is now stale (the library owns the parse).
  - 29–42 — `parseSpinnerSeconds`. Goes away.
  - 44–88 — `runWatchdog`. Body is replaced by a one-line delegation to `tuidriver.RunWatchdog` plus the existing three side-effect lines on a non-nil return; the **function signature is kept exactly as today** so `runner.go:384` does not change.
- `internal/agentrun/ptyrunner/runner.go:380-387` — the spawn site. Confirms the `wg.Add(1)` / `defer wg.Done()` / `defer wg.Wait()` / `defer cancel()` shape the ticket says to preserve. No edits required to this file; verify by inspection only.
- `internal/agentrun/ptyrunner/runner.go:155-165` — `Config.WatchdogTick` and `Config.WatchdogTrackerOpts` doc comments. Stay accurate after the refactor (the zero-default behaviour is unchanged — just the fallback site moves into the library). No edits required.
- `internal/agentrun/ptyrunner/runner_test.go:464-497` — `TestRun_WatchdogFires`. Stays green unchanged; verifies the end-to-end fire path (trailer `Subtype = "error_during_execution"`, `IsError = true`, wall-clock under 5 s). Read it to confirm the assertions still hold after the body swap.
- `internal/agentrun/ptyrunner/runner_test.go:499-526` — `TestParseSpinnerSeconds` (the table test). Deleted as part of this ticket; equivalent coverage is upstream (see next bullet).
- `github.com/pyrycode/tui-driver/pkg/tuidriver` package docs — confirm the four exported names this spec relies on:
  - `func RunWatchdog(ctx context.Context, buf *Buffer, tr *Tracker, opts WatchdogOpts) error` — blocks the calling goroutine, returns `nil` on ctx cancellation, returns the `CheckWatchdog` error verbatim on wedge; applies its own `tick <= 0 → DefaultWatchdogTick` fallback; nil `buf` / `tr` panic on first dereference (matches current local behaviour).
  - `type WatchdogOpts struct { Tick time.Duration }` — the only field.
  - `func ParseSpinner(snap []byte) (verb string, totalSeconds int, ok bool)` — class-A only; strips ANSI internally; class-B/C/D return `ok=false`. Called by `RunWatchdog` internally on each tick; pyrycode never calls it directly.
  - `const DefaultWatchdogTick = 1 * time.Second` — the fallback constant; matches the local `defaultWatchdogTick` value 1:1.
- `github.com/pyrycode/tui-driver/pkg/tuidriver/state_test.go` (TestParseSpinnerClassAMatches at L150, TestParseSpinnerNonClassA at L198) — confirms the upstream test coverage that subsumes the deleted local `TestParseSpinnerSeconds` (class-A single/two-word verb, class-A minutes+seconds, class B ellipsis, class C parenthesised, no-glyph, glyph-only — same fixture set the local test exercises).
- `github.com/pyrycode/tui-driver/pkg/tuidriver/watchdog_test.go` — confirms the upstream test coverage for the loop itself (`TestRunWatchdogContextCancellation`, `TestRunWatchdogPTYQuietWedge`, `TestRunWatchdogSpinnerFreezeWedge`, `TestRunWatchdogDefaultTickApplied`). The pyrycode side does **not** need to re-cover any of these; the `TestRun_WatchdogFires` integration test is the only watchdog test that remains on the pyrycode side, and it asserts the glue's side effects (`SetExitReason` + `cancel()`) — not the loop mechanics.

Module version: `go.mod` already pins `github.com/pyrycode/tui-driver v0.0.0-20260523181457-c2dcd1e49992`, which is the version that shipped `RunWatchdog`/`ParseSpinner` (tui-driver #89). **No `go get` / `go mod tidy` step in this ticket.** Verify with `go doc github.com/pyrycode/tui-driver/pkg/tuidriver RunWatchdog`; if the output is empty, escalate — the version pin is wrong and this spec's premise no longer holds.

## Context

`internal/agentrun/ptyrunner/watchdog.go` carries two responsibilities the tui-driver library now owns:

1. **The class-A spinner-seconds regex** (`spinnerSecondsRe` + `parseSpinnerSeconds`, 27 LOC, watchdog.go:17–42). The var's package comment (lines 24–26) claims the regex is "consumer-policy (which spinner classes to parse) not driver-policy" — that distinction is no longer accurate. tui-driver #89 promoted the regex into `tuidriver.ParseSpinner`, which has the same class-A shape (`✻\s+\S+(?:\s+\S+)?\s+for\s+(?:(\d+)m\s+)?(\d+)s`), returns `ok=false` for class B/C/D, and is covered upstream by `TestParseSpinnerClassAMatches` / `TestParseSpinnerNonClassA` over the same fixture set the local table test uses.
2. **The watchdog tick loop** (`runWatchdog`, 35 LOC, watchdog.go:53–88). The per-tick work — `Snapshot → StripANSI → parseSpinnerSeconds → tr.ObserveSpinner → tr.CheckWatchdog` — is now `tuidriver.RunWatchdog`, calibrated to the same 1 Hz default cadence (`tuidriver.DefaultWatchdogTick`).

After this ticket, the only pyrycode-specific watchdog logic is the wiring on a non-nil return from `tuidriver.RunWatchdog`: log the error, set `streamjson.ExitReasonError` on the emitter, cancel the run context. Those three side effects are pyrycode-specific (streamjson is a pyry-internal package; tui-driver cannot know about it) — they stay in the glue layer.

This ticket is independent of the JSONL-side delegation (#509). Both can ship in parallel — file-overlap check passed at architect time (no other in-flight branch touches `internal/agentrun/ptyrunner/`).

## Design

### `internal/agentrun/ptyrunner/watchdog.go`

The file is shrunk in place to a single thin glue function. Final shape (~20 LOC including imports and the doc comment):

**Imports.** Drop `regexp` and `strconv` (no longer needed). Keep `context`, `log/slog`, `time`, `github.com/pyrycode/tui-driver/pkg/tuidriver`, and `github.com/pyrycode/pyrycode/internal/agentrun/streamjson`.

**Symbols to delete.** `defaultWatchdogTick` constant, `spinnerSecondsRe` var, `parseSpinnerSeconds` function. None of these are referenced anywhere outside `watchdog.go` itself (verified by `grep -rn "parseSpinnerSeconds\|spinnerSecondsRe\|defaultWatchdogTick" internal/` returning only watchdog.go hits).

**`runWatchdog` function — keep the signature byte-for-byte identical to today:**

```go
func runWatchdog(
    ctx context.Context,
    buf *tuidriver.Buffer,
    tr *tuidriver.Tracker,
    emitter *streamjson.Emitter,
    cancel context.CancelFunc,
    tick time.Duration,
    logger *slog.Logger,
)
```

This preserves `runner.go:384` unchanged. Do not "simplify" the signature by dropping `tick` (the value still needs to flow from `cfg.WatchdogTick`) or by dropping the `logger` parameter (the slog.Default() fallback is upstream of this call site, at runner.go:254-257).

**Body — one delegation + the three side effects on a non-nil return:**

- Call `tuidriver.RunWatchdog(ctx, buf, tr, tuidriver.WatchdogOpts{Tick: tick})`.
- If the return is non-nil: `logger.Warn("ptyrunner: watchdog fired", "err", err)` (verbatim log message — `TestRun_WatchdogFires` does not assert the log line, but other tests grep for "watchdog fired" if anything is added later, so the string stays). Then `emitter.SetExitReason(streamjson.ExitReasonError)` and `cancel()`.
- If the return is nil (ctx cancellation): do nothing and let the function return.

The local zero-tick fallback (`if tick <= 0 { tick = defaultWatchdogTick }`) is removed because `tuidriver.RunWatchdog` performs the same fallback against `tuidriver.DefaultWatchdogTick` (1 second — the same value). The behavioural envelope is preserved.

**Doc comment for `runWatchdog` (one short paragraph):**

State that the function delegates the per-tick work to `tuidriver.RunWatchdog` and maps a non-nil return into pyrycode-specific side effects (log, `SetExitReason(ExitReasonError)`, `cancel()`). Keep the discipline sentence verbatim: *"Discipline: the only thing logged is the tuidriver-generated watchdog error string (which carries last-state + duration but no Event content)."* That discipline is reaffirmed elsewhere in the package doc (runner.go:22–27) and a future reader needs to see it on this function too.

Do **not** keep the deleted var's "consumer-policy not driver-policy" rationale — that distinction is what this ticket retires.

### `internal/agentrun/ptyrunner/runner.go`

**No edits.** The function signature of `runWatchdog` is preserved, so `runner.go:384` (`runWatchdog(runCtx, sess.Buffer, tracker, emitter, cancel, cfg.WatchdogTick, logger)`) is untouched. The `Config.WatchdogTick` doc-comment (lines 155–159) stays accurate as written — the zero-default behaviour is preserved, just the fallback site moves into the library.

If the developer is tempted to "tighten" the `Config.WatchdogTick` doc-comment to name `tuidriver.DefaultWatchdogTick` explicitly, leave it alone — the existing wording ("zero defaults to 1 second") is correct and changing it would expand the spec's blast radius for no behavioural gain.

### `internal/agentrun/ptyrunner/runner_test.go`

**Delete `TestParseSpinnerSeconds`** (lines 499–526, ~28 LOC). The symbol it tests no longer exists; equivalent class-A/B/C/no-glyph/glyph-only coverage is provided upstream by `tuidriver` `TestParseSpinnerClassAMatches` / `TestParseSpinnerNonClassA`. Do not replace it with a shim test that calls `tuidriver.ParseSpinner` from pyrycode — that would be re-testing library code from the consumer side, which is the anti-pattern the upstream tests exist to prevent.

**Keep `TestRun_WatchdogFires`** (lines 464–497) verbatim. Its assertions cover:

- `cfg.WatchdogTick = 50 * time.Millisecond` flows through `tuidriver.WatchdogOpts{Tick: tick}` to the upstream loop (the loop fires fast enough that the test wall-clock stays under 5 s).
- `cfg.WatchdogTrackerOpts = {PTYQuietLimit: 200ms, SpinnerFreezeLimit: 200ms}` is consumed by `tuidriver.NewTracker` at runner.go:339 (unchanged by this ticket) and drives the wedge inside the loop.
- The glue layer's three side effects fire on wedge: `SetExitReason(ExitReasonError)` produces the trailer `Subtype = "error_during_execution"` and `IsError = true`; `cancel()` collapses `watcher.Run(runCtx)` so `Run` returns `nil` on the operator-shutdown branch at runner.go:390-392.

That single integration test is sufficient — it exercises the end-to-end wiring, and the per-arm wedge mechanics (PTY-quiet vs spinner-freeze) are owned upstream.

### Verifying AC #3 (class B/C don't engage the freeze arm)

The acceptance criterion says: *"Class B / C spinner renderings (no `for Ns` counter) do NOT engage the spinner-freeze arm of the tracker."*

The current local code preserves this via the explicit `ok && tuidriver.IsThinking(stripped)` AND. After the refactor, `tuidriver.RunWatchdog` calls `tr.ObserveSpinner(ok, totalSeconds)` where `ok` comes from `tuidriver.ParseSpinner`. The ticket's Technical Notes explain why this is equivalent: `ParseSpinner`'s class-A regex requires the literal `✻` glyph and the literal `for` keyword, so `ok == true` implies `IsThinking == true` (the glyph is present), and class B/C/D return `ok=false` (no `for` keyword), which `ObserveSpinner` treats identically to "spinner not visible". The freeze arm therefore stays dormant on class B/C renderings — same outcome the local code produced, achieved by a tighter contract upstream.

This invariant is asserted upstream by `tui-driver` `TestRunWatchdogSpinnerFreezeWedge` (which fires the freeze arm on a class-A sequence with a stuck counter) and `TestParseSpinnerNonClassA` (which confirms class B/C return `ok=false`). No new pyrycode-side test is required to re-assert this; if the developer is tempted to add a "class B does not fire the freeze arm" integration test, the answer is no — that's library-contract testing, not pyrycode-glue testing.

## Concurrency model

Unchanged from today.

- `runner.go:380-387` spawns the goroutine via `wg.Add(1)` / `defer wg.Done()` and registers `defer wg.Wait()` / `defer cancel()` after it. The defer-LIFO chain documented at runner.go:206-216 (`cancel() → wg.Wait() → counter.Stop() → emitter.Close() → sess.Close()`) is the load-bearing cleanup order; this ticket does not perturb any of it.
- The goroutine calls `runWatchdog(runCtx, ...)`, which calls `tuidriver.RunWatchdog(runCtx, ...)`. `RunWatchdog` blocks on the calling goroutine (no internal goroutines spawned, per its package doc). When `runCtx` is cancelled — either by the operator's parent cancel propagating, or by the budget's `Terminate` hook at runner.go:344-349, or by our own `cancel()` on watchdog fire — `RunWatchdog` returns `nil` and `runWatchdog` returns.
- `tuidriver.Buffer.Snapshot` and `tuidriver.Tracker.ObserveSpinner` / `Tracker.CheckWatchdog` are thread-safe per their package contracts (per `RunWatchdog`'s package doc), so the loop does not race with the prompt-write or tail-watcher goroutines that also read the buffer.

The capstone ticket #513 collapses the current goroutine into `Session.Events()`; this ticket explicitly does **not** preempt that work — the spawn shape stays exactly as today so #513's diff stays minimal.

## Error handling

`tuidriver.RunWatchdog` returns:
- `nil` on `ctx.Done()` (operator shutdown, budget termination, our own `cancel()`). Glue does nothing.
- A non-nil error verbatim from `tr.CheckWatchdog(buf)` on either arm firing (PTY-quiet or spinner-freeze). Glue logs, sets `streamjson.ExitReasonError`, cancels `runCtx`.

There is no error shape the glue cares to distinguish — the error string upstream already names the failure mode and the last recorded state, and the consumer's only response is "tear down the run". A future maintainer tempted to `errors.Is` against a tui-driver sentinel here should not — the contract is "non-nil means wedge, end the run", and the error string is logged verbatim for operator forensics.

## Testing strategy

- **No new tests.** Coverage migrates upstream where it already exists:
  - `TestParseSpinnerSeconds` → `tui-driver` `TestParseSpinnerClassAMatches` + `TestParseSpinnerNonClassA` (same fixture set; the local table test is deleted).
  - The tick-loop mechanics (cancellation, default-tick application, both arms firing) are covered by `tui-driver` `TestRunWatchdog*` (four tests).
- **One integration test stays:** `TestRun_WatchdogFires` (runner_test.go:464-497) is the end-to-end assertion that pyrycode's glue wires the three side effects on a real wedge. Unchanged.
- **`make check`** is the gate (AC #4). Runs `go vet`, `staticcheck`, `go test -race ./...`. After the refactor: zero new symbols, two fewer imports (`regexp`, `strconv`) in `watchdog.go`, one fewer test function in `runner_test.go`. Lint should be clean.
- **`make e2e-realclaude`** is the byte-equivalence gate (AC #5, ticket #506). The trailer shape on watchdog fire (`Subtype = "error_during_execution"`, `IsError = true`, empty `TerminalReason`) is what the byte-equivalence assertion pins; the side-effect ordering inside the glue is unchanged, so the wire bytes stay identical. If e2e-realclaude reports drift, the regression is almost certainly in the glue's call order — verify `SetExitReason` fires **before** `cancel()` (matching today's order at watchdog.go:82-83).

## Open questions

None. The upstream API surface is frozen (tui-driver #89 already shipped at the pinned version), the fixture set the local `TestParseSpinnerSeconds` exercises maps 1:1 onto the upstream test cases, and the AC #3 class-B/C invariant is structurally preserved by `ParseSpinner`'s contract. Developer judgement-call surface is minimal — the only choice is "delete watchdog.go entirely vs keep it as a thin glue file", and this spec recommends the latter (rationale: keeps `runner.go:384` unchanged via the preserved function signature, which is the smallest possible blast radius for this refactor).

Acceptance criteria mapping:
- AC #1 (no more `spinnerSecondsRe` / `parseSpinnerSeconds`; glue layer delegates) → § Design `watchdog.go`.
- AC #2 (existing tests unchanged; `TestParseSpinnerSeconds` removed) → § Testing strategy.
- AC #3 (class B/C don't engage freeze arm) → § Verifying AC #3.
- AC #4 (`make check` clean) → § Testing strategy.
- AC #5 (`make e2e-realclaude` byte-equivalence green) → § Testing strategy.
