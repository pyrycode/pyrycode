# #479 — `internal/agentrun/ptyrunner.Run` pyry-side max-turns budget + watchdog with shared ctx-cancel teardown

Sub-issue of [#329](https://github.com/pyrycode/pyrycode/issues/329). Builds on [#478](https://github.com/pyrycode/pyrycode/issues/478) (JSONL tail + stream-json emit) and [#471](https://github.com/pyrycode/pyrycode/issues/471) (spawn skeleton). Siblings: [#469](https://github.com/pyrycode/pyrycode/issues/469) (trust + settings helpers), [#475](https://github.com/pyrycode/pyrycode/issues/475) (trust pre-write follow-up), [#482](https://github.com/pyrycode/pyrycode/issues/482) (real-claude byte-equivalence smoke test, blocked on this), [#470](https://github.com/pyrycode/pyrycode/issues/470) (`cmd/pyry/agent_run.go` cutover, blocked on this + #482 + #469).

## Files to read first

- `internal/agentrun/ptyrunner/runner.go` — the slice being extended in-place. Read end-to-end (~352 lines). Pay close attention to: package doc (lines 1-34, dependency-direction note narrows further — `budget` is now allowed), `Config` struct (lines 70-143, `MaxTurns` flips to required + two new optional watchdog-tuning fields), the `Run` body (lines 184-318, the budget/watchdog/shared-cancel wiring slots in after the existing `streamjson.New` and before `tail.New`, plus a defer-registration reordering so the watchdog goroutine drains before `emitter.Close`), and the existing return-composition (lines 307-317, stays as-is — the budget/watchdog paths land via `cancel()` and `SetExitReason` and collapse to `ctx.Err() != nil → return nil`).
- `internal/agentrun/ptyrunner/runner_test.go` — extends with two new test funcs and a `MaxTurns` row in `TestRun_MissingRequiredFields`. Reuse `helperRunCfg`, `syncBuffer`, `parseTrailer`, and the existing fixture-body constants (`happyPathBody`, `noEotBody`). The base `Config` in `TestRun_MissingRequiredFields` gains a `MaxTurns: 1` line so existing rows still validate past the new check.
- `internal/agentrun/ptyrunner/helper_test.go` — no shape change. The existing `jsonl` mode already supports the budget test (multi-line body, all non-EOT) and the watchdog test (empty body — helper writes idle glyph and idles). No new helper mode is required, but the architect's preferred refinement (a `jsonl_no_idle_traffic` body-env that means "write nothing, just idle past the watchdog limit") is optional — see § Testing strategy for the no-new-mode rationale.
- `internal/agentrun/budget/budget.go` — the leaf budget primitive this slice composes. Read end-to-end (~188 lines). Key contracts: `New(Config{MaxTurns, Terminate, Kill, GracePeriod, Logger})` validates and constructs; `OnEvent(ev jsonl.Event)` counts assistant entries and fires Terminate when the count reaches MaxTurns on a non-`EndOfTurn` event; `OnEndOfTurn()` records natural completion; `Reason()` exposes `ReasonCompletion` / `ReasonMaxTurns` / `""`; `Stop()` cancels the pending SIGKILL grace timer. The Terminate / Kill hooks fire synchronously from the goroutine invoking OnEvent / OnEndOfTurn (the tail.Watcher goroutine in production); the grace timer fires from `time.AfterFunc`'s own goroutine. Safe for concurrent use.
- `internal/agentrun/streamjson/emitter.go:158-174` — `SetExitReason` is idempotent (first non-empty value sticks) and safe for concurrent use; the budget callback AND the watchdog goroutine both call it without coordination. Default classification at Close runs only if SetExitReason was never called — `ExitReasonCompletion` if EOT was seen during Emit, else `ExitReasonError`. The budget path sets `ExitReasonMaxTurns` (subtype `error_max_turns` / terminal_reason `max_turns` / is_error `true`); the watchdog path sets `ExitReasonError` (subtype `error_during_execution` / terminal_reason `""` / is_error `true`). See `wireFields` at lines 240-249.
- `github.com/pyrycode/tui-driver/pkg/tuidriver/tracker.go` — the watchdog primitive. `NewTracker(TrackerOpts{PTYQuietLimit, SpinnerFreezeLimit}) *Tracker` (zero/negative values → defaults of 30s each); `RecordTransition(state string)` for state-name bookkeeping that surfaces in watchdog error strings; `ObserveSpinner(visible bool, totalSeconds int)` from each tick to drive the freeze arm; `CheckWatchdog(buf *Buffer) error` returns nil if both arms are satisfied, else a descriptive error. The Tracker does NOT spawn its own goroutine — the consumer drives the tick cadence (typical: 1Hz, per spike binaries).
- `github.com/pyrycode/tui-driver/pkg/tuidriver/state.go:38-54` — `IsThinking(snap []byte) bool` reports whether the ✻ glyph is present after StripANSI. Used as the spinner-visible signal for `ObserveSpinner`. Note: `ParseSpinnerTokens` (lines 67-96) extracts the ↓N tokens counter from class-C spinner renderings, NOT the seconds counter the freeze arm needs — that parser lives in the spike binaries and must be reproduced here. See § Design / Watchdog goroutine.
- `github.com/pyrycode/tui-driver/cmd/spike-one-turn/main.go:43-50, 124-147, 263-276` — reference implementation of the watchdog goroutine: 1Hz ticker, parses the class-A `✻ Verb for Ns` (or `✻ Verb for Nm Ks`) rendering for the seconds counter, calls `tr.ObserveSpinner(ok, total)` then `tr.CheckWatchdog(rb)`. This slice mirrors the pattern.
- `docs/knowledge/codebase/478.md` — sibling slice's knowledge doc. Read for tone + section ordering when the documentation phase writes 479's knowledge doc (this slice does NOT write the knowledge doc — see § Out of scope).

## Context

[#478](https://github.com/pyrycode/pyrycode/issues/478) wired the happy-path JSONL tail + stream-json emit into `ptyrunner.Run`. Default exit-reason classification covers two cases: end-of-turn observed (`ExitReasonCompletion`) and end-of-turn NOT observed (`ExitReasonError`, by absence). This slice layers the two pyry-side safety nets on top:

1. **Pyry-side max-turns budget.** Interactive claude does NOT honor `--max-turns` (only `claude -p` / stream-json mode does); pyry must enforce the cap itself. `internal/agentrun/budget.Counter` is the leaf primitive; this slice composes it into `Run` via `tail.Config.OnEvent` / `tail.Config.OnEndOfTurn`. On budget hit the Terminate hook (a) sets `ExitReasonMaxTurns` on the streamjson emitter, (b) cancels the run ctx, (c) SIGTERMs the claude child. The result trailer carries `subtype:"error_max_turns"` / `terminal_reason:"max_turns"` / `is_error:true`.
2. **Stuck-claude watchdog.** `tui-driver`'s two-arm watchdog tracker (PTY-heartbeat + spinner-freeze) runs in a goroutine ticking at 1 Hz. On a watchdog fire — claude wedged, deadlocked, or stuck in a long tool call without PTY progress — the goroutine sets `ExitReasonError` on the streamjson emitter and cancels the run ctx. The result trailer carries `subtype:"error_during_execution"` / `terminal_reason:""` / `is_error:true` (same shape `streamrunner` produces on its non-completion paths today).

Both exit reasons must propagate through `streamjson.Emitter` so the dispatcher receives the same wire shape regardless of which arm fired. The end-to-end empirical validation against real claude is carved off into [#482](https://github.com/pyrycode/pyrycode/issues/482) (the real-claude smoke test), which is blocked on this slice.

Strategic motivation (recap of #471, #478): Anthropic's 2026-06-15 billing-policy split explicitly names "Interactive Claude Code in the terminal or IDE" as subscription-eligible. The stream-json subprocess surface is not enumerated and risks landing metered. The PTY pivot is the proactive move to the named-eligible surface; this slice gets the safety nets in place so #470's cutover ships with the same operational guarantees `streamrunner` has today.

## Design

### Package boundary

Stays at `internal/agentrun/ptyrunner/`. One new production file, one modified production file:

| File | Change |
| --- | --- |
| `runner.go` | Package doc (forbidden-imports list narrows — `budget` is now wired); `Config` (`MaxTurns` flips to required + two new optional fields `WatchdogTick` and `WatchdogTrackerOpts`); `Run` body (shared `runCtx`/`cancel`; `budget.New` with closure-bound Terminate/Kill hooks; watchdog goroutine spawn with `sync.WaitGroup` drain; defer-registration reordering so watchdog drains BEFORE emitter.Close BEFORE sess.Close). |
| `watchdog.go` | **New file.** Contains the spinner-seconds regex + `parseSpinnerSeconds` helper, the watchdog goroutine entry `runWatchdog`, and the `watchdogTick` default constant. Kept separate from `runner.go` for code-clarity — the regex + goroutine are a self-contained subsystem and folding them inline would push `runner.go` past 400 lines and obscure `Run`'s top-to-bottom narrative. |

| File | Change |
| --- | --- |
| `runner_test.go` | Two new test funcs (`TestRun_BudgetHitBeforeEndOfTurn`, `TestRun_WatchdogFires`); one new row in `TestRun_MissingRequiredFields` for `MaxTurns`; existing `helperRunCfg` callers gain `MaxTurns: 1` where not already set (default applies to the existing happy-path / ctx-cancel / emit-error tests too — see § Testing). |
| `helper_test.go` | No structural change. The existing `jsonl` mode covers both new test bodies (multi-line non-EOT for budget; empty body + the existing idle-after-WritePrompt behaviour for watchdog). |

Two new exported types (`WatchdogTrackerOpts` is just an alias for `tuidriver.TrackerOpts` carried via a struct field — no new declared type); no new exported funcs. Three new unexported names in `watchdog.go` (`parseSpinnerSeconds`, `runWatchdog`, `spinnerSecondsRe`) and one new unexported package-scope constant (`watchdogTick`).

### Package doc — forbidden-imports list narrows further

The 478 package doc forbids only `internal/supervisor`. After this slice, that line is unchanged (the budget package is `internal/agentrun/budget`, a sibling subpackage already implicitly allowed by the "sibling agentrun subpackages allowed" note). The verifying command stays identical:

```
go list -deps ./internal/agentrun/ptyrunner/... | grep pyrycode/internal/supervisor
```

Expected output: empty.

Update the "this slice wires the tail + emit; budget + watchdog land in a follow-up" sentence (currently at runner.go lines 14-17) to past tense and integrate the budget + watchdog wiring: replace with a single sentence noting this slice composes the budget Counter + watchdog Tracker on top of the JSONL tail + stream-json emit from #478. Drop the "the pyry-side max-turns budget and the watchdog goroutine land in a follow-up slice on top of this one" sentence — that follow-up is this slice.

Extend the logging-discipline paragraph (lines 19-24) with one sentence: "The wired `budget` Counter and the watchdog goroutine inherit the same discipline — the Counter logs only count + max_turns numerics and the watchdog goroutine logs only the tuidriver-generated watchdog error string; neither logs Event content."

### `Config` changes

Two field-shape changes (one promotion, two new optional fields); no positional reordering.

**`MaxTurns` flips from forward-compat-only to required.** Current field comment (lines 98-103) explains it's declared for forward-compat with this slice. Update to:

> MaxTurns is the assistant-entry cap enforced by the pyry-side budget Counter (`internal/agentrun/budget`). Required; must be > 0. The interactive-TUI claude path intentionally omits `--max-turns` from argv because interactive claude does not honor it; this field is the load-bearing enforcement point. On budget hit the run is terminated with `ExitReasonMaxTurns` set on the streamjson emitter.

Validation joins the existing chain at the top of `Run`:

```
if cfg.MaxTurns <= 0 {
    return errors.New("ptyrunner: MaxTurns required")
}
```

Slots between the `Stderr` check (line 213) and the logger resolution (line 215). Single-line message string `"ptyrunner: MaxTurns required"` — the developer's pattern stays "ptyrunner: <field> required".

**New optional fields: `WatchdogTick` and `WatchdogTrackerOpts`.** Add after `Env`, before `Logger`. Field comments:

> WatchdogTick is the cadence at which the watchdog goroutine polls the rolling buffer + spinner state. Optional; zero defaults to 1 second (matches the spike binaries). Tests typically set 50ms to keep wall-clock low.

> WatchdogTrackerOpts is forwarded verbatim to `tuidriver.NewTracker`. Optional; zero values pick the tuidriver-package defaults (PTYQuietLimit = 30s, SpinnerFreezeLimit = 30s). Tests use short values (~200ms) to fire the watchdog within the test deadline.

Threaded into `tuidriver.NewTracker(cfg.WatchdogTrackerOpts)` and into the goroutine's `time.NewTicker(tick)` where `tick = cfg.WatchdogTick` or 1s on zero. No validation — both zero values are the documented "use defaults" signal.

### `Run` — new wiring inside the existing happy-path

The slice extends the current `Run` body in-place. Existing structure stays through step 7 (cfg validation, logger resolution, exec.CommandContext, EnsureClaudeEnv, Spawn, sess.Close defer, WaitUntil, modal/MCP/network detectors, WritePrompt). Then:

8. `runCtx, cancel := context.WithCancel(ctx)` — the single shared cancellation point used by the tail watcher AND the watchdog goroutine. Both the budget Terminate callback and the watchdog goroutine call `cancel()` on their respective trigger; the watcher's `Run(runCtx)` returns `ctx.Err()` once either fires. **This is the explicit AC choice — single shared ctx with a single cancel point, NOT separate teardown ordering.** Rationale: the tail watcher and watchdog goroutine observe disjoint signals (file events vs PTY state) but compose into a single "did this turn end cleanly?" question. Wiring both to one cancel keeps the answer atomic and avoids a fragile "first to fire wins" race protocol across two cancel funcs. See § Concurrency model for the defer ordering that makes this safe.
9. Construct the `streamjson.Emitter` (same call as in #478, unchanged). Register the existing `defer emitter.Close()` — but see § Cleanup ordering below; the registration moves down so the watchdog's `wg.Wait()` runs FIRST (LIFO).
10. Construct the budget Counter. The Terminate / Kill closures capture `emitter`, `cancel`, and `cmd` from the enclosing scope. Wrap a `budget.New` error as `fmt.Errorf("ptyrunner: budget: %w", err)`.

    Terminate closure:

    > Three side effects in order, all idempotent under concurrent fires:
    > 1. `emitter.SetExitReason(streamjson.ExitReasonMaxTurns)` — first SetExitReason call wins, so a concurrent watchdog fire that lost the race still observes the max_turns classification at trailer-composition time.
    > 2. `cancel()` — signals runCtx; the tail watcher's `Run(runCtx)` returns `ctx.Err()` on its next select iteration.
    > 3. `cmd.Process.Signal(syscall.SIGTERM)` — interactive claude's PTY pipeline keeps the child alive until SIGTERM; without this, the budget's "I'm done at MaxTurns" signal reaches the emitter trailer but the child keeps generating tokens until sess.Close's own SIGTERM at defer time. Returning the Signal error lets the budget package's logger record terminate-failure cases (e.g. process already exited).

    Kill closure: a one-liner `cmd.Process.Signal(syscall.SIGKILL)` returning the result. Defensive — fires from the budget package's 5s grace timer if SIGTERM did not produce an exit. The deferred `counter.Stop()` (step 13 below) cancels this timer on the normal path so SIGKILL is not double-fired against an already-dead process.

    The closures access `cmd.Process` — non-nil after `tuidriver.Spawn` returns successfully (Spawn's StartPTY calls `cmd.Start()` internally). Defensive `if cmd.Process == nil { return nil }` guard is unnecessary in the post-spawn path but cheap insurance; developer's call.

11. Register `defer counter.Stop()` (cancels the pending SIGKILL grace timer on the normal exit path; harmless if the timer never started).
12. Construct the `tail.Watcher` (same call as in #478, but the OnEvent / OnEndOfTurn closures gain budget wiring):

    OnEvent closure:

    > Synchronously calls `emitter.Emit(ev)` THEN `counter.OnEvent(ev)`. The Emit-error-capture pattern from #478 is unchanged (`if err := emitter.Emit(ev); err != nil && emitErr == nil { emitErr = err }`); the new `counter.OnEvent(ev)` call appends. Order matters: Emit first so the trailer reflects what was actually written; counter second so the budget hit fires AFTER the budget-trigger event was emitted.

    OnEndOfTurn closure:

    > `counter.OnEndOfTurn()` — records natural completion in the budget so `counter.Reason()` returns `ReasonCompletion` (informational; ptyrunner doesn't read Reason() back on this path because the emitter's default classification already handles the success trailer). The #478 spec noted the body was empty; this slice replaces the no-op with the counter call. No other side effect.

13. Construct the `tuidriver.Tracker` (`tuidriver.NewTracker(cfg.WatchdogTrackerOpts)`) and call `tracker.RecordTransition("prompt-written")` immediately. This stamps a useful "last state" string into any watchdog error message — operators reading logs see `watchdog: PTY quiet for 35s (last state: prompt-written)` instead of `(last state: )`.
14. Spawn the watchdog goroutine via `sync.WaitGroup` (`wg.Add(1)` then `go func() { defer wg.Done(); runWatchdog(runCtx, sess.Buffer, tracker, emitter, cancel, cfg.WatchdogTick, logger) }()`). See § Watchdog goroutine for `runWatchdog`'s body.
15. **Defer ordering — register in this LIFO-correct sequence (top to bottom of Run reads as registered top to bottom; fire order is reversed):**

    - `defer sess.Close()` — already registered at step 4 (existing #471 behaviour, unchanged).
    - `defer wg.Wait()` — registered HERE (after step 14's goroutine spawn).
    - `defer cancel()` — registered HERE (immediately after wg.Wait; both are idempotent and cheap).
    - `defer counter.Stop()` — registered at step 11 above.
    - `defer emitter.Close()` — registered at step 9 above.

    Wait — the registration order in the code above doesn't match the desired LIFO fire order. Restate the desired property and the registration order that produces it:

    **Desired fire order (top = runs first):**

    1. `cancel()` — signals runCtx so the watchdog goroutine exits its select loop and so the tail watcher (if not already returned) sees ctx.Done.
    2. `wg.Wait()` — blocks until the watchdog goroutine returns. After this, no further `SetExitReason(ExitReasonError)` calls can race with `emitter.Close`.
    3. `counter.Stop()` — cancels the budget's pending SIGKILL grace timer (no-op if not armed).
    4. `emitter.Close()` — writes the `result` trailer to cfg.Stdout using whichever ExitReason was set (or the default classification if SetExitReason was never called).
    5. `sess.Close()` — SIGTERM → grace → SIGKILL the claude child; close the PTY; reap.

    **Registration order to produce the above (top = registered first, fires last):**

    1. `defer sess.Close()` — at step 4, right after Spawn.
    2. `defer counter.Stop()` — at step 11.
    3. `defer emitter.Close()` — at step 9 (NOTE: this slice MOVES this defer from its current #478 position. In #478 it sits right after `streamjson.New` and BEFORE `tail.New`. In this slice, the emitter.Close defer must be registered AFTER the watchdog goroutine spawn so that wg.Wait + cancel — registered later — fire BEFORE it).
    4. `defer wg.Wait()` — at step 14, immediately after wg.Add(1) + goroutine spawn.
    5. `defer cancel()` — at step 14, immediately after the wg.Wait defer.

    The architect's strong recommendation: put a comment block immediately above the chain of defers reminding the next-reader that defer LIFO is doing structural work and that REORDERING THESE DEFERS reverses the cleanup ordering. The #478 spec already pinned this for emitter.Close-before-sess.Close; this slice extends the pin to cancel-before-wg.Wait-before-emitter.Close.

16. `runErr := watcher.Run(runCtx)` (blocks until EOT, runCtx cancel, or non-ctx I/O error — same as #478).
17. **Return-value composition** — same shape as #478, but use `runCtx.Err()` instead of `ctx.Err()` for the collapse check (collapsing on the *shared* cancel ctx captures budget-hit and watchdog-fire cases as nil-returns, which is the intended dispatcher contract: both arms are operator-shutdown-equivalent and the wire-side classification is carried by the trailer's `subtype` field, not by an error return):

    - If `runCtx.Err() != nil`, return `nil`.
    - Else if `emitErr != nil`, return `fmt.Errorf("ptyrunner: emit: %w", emitErr)`.
    - Else if `runErr != nil`, return `fmt.Errorf("ptyrunner: tail: %w", runErr)`.
    - Else return `nil`.

    Note that the *parent* `ctx.Err()` (the caller's ctx) is captured by `runCtx.Err()` because `runCtx` is a child of `ctx` — when parent is cancelled, child is too. So this single check covers operator-shutdown collapse for the parent ctx AS WELL AS budget/watchdog-driven internal cancellation.

### Watchdog goroutine (`watchdog.go`)

New file. Contents are tightly scoped: one regex, one parser helper, one goroutine entry. Total: ~50 LOC.

```
package ptyrunner

import (
    "context"
    "log/slog"
    "regexp"
    "strconv"
    "time"

    "github.com/pyrycode/tui-driver/pkg/tuidriver"

    "github.com/pyrycode/pyrycode/internal/agentrun/streamjson"
)

const defaultWatchdogTick = 1 * time.Second

// spinnerSecondsRe matches the class-A spinner rendering ("✻ Verb for Ns"
// or "✻ Verb for Nm Ks") and captures the minutes/seconds components.
// Class B ("✻ Channeling…") and class C ("✻ Actualizing… (2s · ↓N tokens)")
// do not match; for those renderings the watchdog falls back to the
// PTY-quiet arm only.
//
// Mirrors the spike-binary regex at github.com/pyrycode/tui-driver
// cmd/spike-one-turn/main.go:58 (and the matching parser at lines 265-276).
// Kept here rather than in tuidriver because the regex is consumer-policy
// (which spinner classes to parse) not driver-policy.
var spinnerSecondsRe = regexp.MustCompile(...)

// parseSpinnerSeconds returns (totalSeconds, true) when stripped contains a
// class-A spinner; (0, false) otherwise.
func parseSpinnerSeconds(stripped []byte) (int, bool) { ... }
```

`runWatchdog` signature + behaviour (developer writes the body in the project's idiom):

> `func runWatchdog(ctx context.Context, buf *tuidriver.Buffer, tr *tuidriver.Tracker, emitter *streamjson.Emitter, cancel context.CancelFunc, tick time.Duration, logger *slog.Logger)`
>
> Behaviour: if `tick <= 0`, default to `defaultWatchdogTick`. Loop with a `time.Ticker`; on each tick, snapshot the buffer, StripANSI it (`tuidriver.StripANSI`), parse the spinner seconds (`parseSpinnerSeconds`), observe (`tr.ObserveSpinner(ok, total)` where `ok` is BOTH `IsThinking(stripped)` AND the parser-returned ok — `ok=true` only when both fire; this prevents the freeze arm from engaging on class B/C spinners which would produce false positives), then check (`tr.CheckWatchdog(buf)`). On non-nil CheckWatchdog: log at Warn (`logger.Warn("ptyrunner: watchdog fired", "err", werr)` — the err string is the tuidriver-generated `"watchdog: PTY quiet for Ns (last state: ...)"` which carries no Event content), then `emitter.SetExitReason(streamjson.ExitReasonError)`, then `cancel()`, then return. On `<-ctx.Done()`: return immediately.

The reference behaviour matches the spike binary's loop (`tui-driver/cmd/spike-one-turn/main.go:124-147`) with two ptyrunner-specific additions: SetExitReason before cancel (so the trailer reflects the watchdog-fire classification), and the Warn log (the spike binary uses `log.Printf` to stderr; ptyrunner uses structured slog).

Tests for the parser go inline in `runner_test.go` as a small table test (`TestParseSpinnerSeconds` — 4-6 rows: class A `for 5s` returns 5/true; class A `for 1m 30s` returns 90/true; class B/C/D return 0/false; non-spinner text returns 0/false). The Tracker itself is exhaustively tested in tuidriver — ptyrunner does not re-test ObserveSpinner / CheckWatchdog mechanics.

### Cleanup ordering — extended from #478

The #478 invariant was "emitter.Close BEFORE sess.Close" via defer LIFO. This slice extends it to a five-step chain:

```
cancel() → wg.Wait() → counter.Stop() → emitter.Close() → sess.Close()
```

Each step has a load-bearing property:

| Step | Property | What breaks if reordered |
| --- | --- | --- |
| `cancel()` first | Signals watchdog goroutine to exit its select loop. | If wg.Wait fires first, it blocks forever (watchdog is selecting on ctx.Done which is not yet signalled). |
| `wg.Wait()` second | Guarantees the watchdog goroutine is fully drained before any further cleanup. | If emitter.Close fires while watchdog is still ticking, watchdog's `SetExitReason(ExitReasonError)` call races with Close's trailer composition. SetExitReason is safe (no-op after Close fires), but the race produces non-deterministic trailer classification (sometimes max_turns wins, sometimes error wins — depending on goroutine interleaving). Draining first makes the trailer deterministic. |
| `counter.Stop()` third | Cancels the budget's pending SIGKILL grace timer. | If left running, the timer's `cmd.Process.Signal(SIGKILL)` fires later, AFTER sess.Close has already SIGTERM'd + reaped the child. The Signal returns "process already finished" — benign but log-spammy. |
| `emitter.Close()` fourth | Writes the `result` trailer to cfg.Stdout. The dispatcher reads this as the "this turn ended" signal. | If sess.Close fires first, sess.Close's SIGTERM races with claude's last few PTY writes; the dispatcher may receive a truncated trailer or a trailer missing fields. Same rationale as #478 — extended here because the budget/watchdog paths also produce trailers. |
| `sess.Close()` last | SIGTERM → grace → SIGKILL the claude child; close the PTY; reap cmd.Wait. | No correctness break if reordered to fire earlier, but it's the only step that operates on the OS process — running it last means the child is alive long enough for the trailer to be composed against the final Buffer / emitter state. |

The architect's strong recommendation, repeated from § Design step 15: a comment block above the chain of defers naming the desired LIFO fire order. The defer chain is the *only* place this ordering is enforced — there is no runtime assertion. Future refactors that reorder defers will silently break the invariant.

### What does NOT change

- `buildArgs` is untouched. `--max-turns` is still NOT in argv (interactive claude doesn't honor it; the budget package is the enforcement point).
- `isCtxErr` helper stays as-is. Used by Spawn / WaitUntil error paths only.
- Modal / MCP / network detectors run BEFORE budget + watchdog construction (lines 251-263). A modal trip returns the sentinel error before any of the new wiring registers any defer — so the existing `sess.Close` defer fires alone, and no trailer is written. Dispatcher contract unchanged on modal paths.
- The `OnEvent` Emit-error capture closure shape is unchanged from #478. The closure body grows by one line (`counter.OnEvent(ev)`).
- The `OnEndOfTurn` closure goes from empty body to one-line body (`counter.OnEndOfTurn()`). The emitter's default classification still handles the success-trailer composition; `counter.OnEndOfTurn` just informs the budget's bookkeeping (visible via `counter.Reason()` if any future caller cares — none today).

## Concurrency model

`Run` now coordinates four goroutines:

1. **Caller goroutine (`Run`'s own).** Constructs everything; calls `watcher.Run(runCtx)` synchronously; runs the deferred cleanup chain on return.
2. **tail.Watcher goroutine** (managed internally by `watcher.Run`). Fires OnEvent / OnEndOfTurn callbacks synchronously on its own goroutine. The OnEvent closure → `emitter.Emit` + `counter.OnEvent`. The OnEndOfTurn closure → `counter.OnEndOfTurn`. The counter's Terminate hook (when fired) is called from THIS goroutine. The Terminate hook calls `emitter.SetExitReason` (safe; mutex-guarded), `cancel()` (safe; idempotent), and `cmd.Process.Signal(SIGTERM)` (safe; *os.File.Write-equivalent is goroutine-safe).
3. **Watchdog goroutine** (spawned in step 14). 1Hz ticker; reads `sess.Buffer` (safe; Buffer.Snapshot is mutex-guarded) and `tracker` state (safe; Tracker is mutex-guarded). On fire, calls `emitter.SetExitReason` + `cancel()` then returns. On `<-runCtx.Done()` returns immediately.
4. **Budget grace-timer goroutine** (spawned by `time.AfterFunc` inside `budget.Counter.Terminate`). Fires once after `GracePeriod` (default 5s) if not `counter.Stop()`'d. Calls the Kill hook → `cmd.Process.Signal(SIGKILL)`. Cancelled by `defer counter.Stop()` on the normal exit path.

Shared state with concurrent access:

- `emitter` — `Emit` / `SetExitReason` / `Close` are all mutex-guarded inside the emitter; this slice does not add any locking.
- `counter` — `OnEvent` / `OnEndOfTurn` / `Reason` / `Stop` are all mutex-guarded inside the counter; this slice does not add any locking.
- `tracker` — `RecordTransition` / `ObserveSpinner` / `CheckWatchdog` are all mutex-guarded inside the tracker; this slice does not add any locking.
- `cancel` — `context.CancelFunc` is documented safe for concurrent + repeated calls.
- `cmd.Process.Signal` — `*os.Process.Signal` is safe for concurrent calls per Go's process docs; returns "process already finished" on a no-op call.
- `emitErr` (the closure-bound emit-error variable from #478) — single writer (the tail.Watcher goroutine via the OnEvent closure), single reader (the caller goroutine AFTER `watcher.Run` returns). No mutex needed; #478's reasoning holds.

The `wg.Wait` in the defer chain is what makes the "no watchdog fire after emitter.Close" property hold. Without it, the watchdog could be inside `CheckWatchdog → SetExitReason → cancel` while emitter.Close is composing the trailer; the trailer would be deterministic (SetExitReason is no-op after Close), but the watchdog's Warn log would fire after the trailer was already written, which reads confusingly in operator logs. Draining first makes the log order match the wire order.

## Error handling

Three new error families (the existing eleven from #478 are unchanged):

| Family | Source | Return shape |
| --- | --- | --- |
| Validation | `cfg.MaxTurns <= 0` | `errors.New("ptyrunner: MaxTurns required")` |
| Budget setup | `budget.New` fail (MaxTurns invalid — already caught upstream; nil Terminate / nil Kill — neither reachable in practice post-validation, but defensive) | `fmt.Errorf("ptyrunner: budget: %w", err)` |
| Budget runtime | Terminate hook's `cmd.Process.Signal` returns non-nil (e.g. process already exited) | The budget package logs this at Warn internally (`budget: terminate failed`). ptyrunner does NOT surface it as a Run return value — the run continues toward the deferred trailer/sess.Close path. |

Watchdog-fire path: returns nil from `runWatchdog`; `Run` returns nil via the `runCtx.Err() != nil` collapse. The trailer's `subtype:"error_during_execution"` carries the diagnostic to the dispatcher. The Warn log inside `runWatchdog` provides operator-visible context.

Budget-hit path: same shape as watchdog-fire. `Run` returns nil via the runCtx collapse; the trailer's `subtype:"error_max_turns"` carries the diagnostic.

The dispatcher contract on both arms: `Run` returns nil, but the wire trailer carries the failure classification. This mirrors `streamrunner`'s behaviour (operator-shutdown is collapsed to nil; the wire side carries the diagnostic).

## Testing strategy

Two new test cases plus one `TestRun_MissingRequiredFields` row plus one inline regex-parser table test. All new tests use `t.Parallel()`.

### `TestRun_BudgetHitBeforeEndOfTurn`

- Mode: `jsonl` with `MaxTurns: 1` and a body composed of one assistant entry whose `stop_reason` is NOT `end_turn` and whose text content does NOT trigger the deterministic EOT signal (the existing `noEotBody` constant satisfies both — it's a `tool_use` stop_reason with non-empty text).
- Helper writes the line to the JSONL file on first stdin byte (same `jsonl` mode as #478). Watcher fires OnEvent on the line. OnEvent closure calls Emit then counter.OnEvent. Counter's count hits 1 (== MaxTurns), Terminate fires. Terminate sets ExitReasonMaxTurns + cancels runCtx + SIGTERMs the helper. Helper's SIGTERM handler exits cleanly. Watcher returns ctx.Err(). Run returns nil. Trailer composed with ExitReasonMaxTurns.
- Assertions:
  - `Run` returns nil.
  - Stdout's first line is the verbatim JSONL line + `\n`.
  - Trailer (last line) parses as JSON with `subtype:"error_max_turns"`, `terminal_reason:"max_turns"`, `is_error:true`, `num_turns:1`, `session_id` matches `cfg.SessionID`.
- Wall-clock: < 5s. Test deadline 10s.

### `TestRun_WatchdogFires`

- Mode: `jsonl` with `MaxTurns: 10` (high enough not to fire) and an EMPTY body (`GO_PTYRUNNER_JSONL_BODY=""`). The helper writes only the initial idle glyph; no PTY traffic after WritePrompt; no JSONL file ever created.
- `WatchdogTick: 50 * time.Millisecond`, `WatchdogTrackerOpts: tuidriver.TrackerOpts{PTYQuietLimit: 200 * time.Millisecond, SpinnerFreezeLimit: 200 * time.Millisecond}`.
- Run reaches WritePrompt, constructs emitter + counter + tracker, spawns watchdog goroutine, calls `watcher.Run(runCtx)`. Watcher waits for the JSONL file forever (it's never created). Watchdog goroutine ticks; first tick at ~50ms; `sess.Buffer.QuietFor()` is ~50ms+ time-since-WritePrompt (helper hasn't written anything since the initial idle glyph); 50ms > 200ms is false on the first tick; subsequent ticks accumulate quiet time; eventually `QuietFor > 200ms` and CheckWatchdog returns non-nil. Goroutine sets ExitReasonError + cancels runCtx + returns. Watcher.Run returns ctx.Err(). Run returns nil. Trailer composed with ExitReasonError.
- Assertions:
  - `Run` returns nil within 5s.
  - Trailer parses as JSON with `subtype:"error_during_execution"`, `terminal_reason:""`, `is_error:true`.
- Wall-clock: ~250-400ms (PTYQuietLimit + one tick of slack). Test deadline 5s.
- Note on the helper's initial-idle-glyph write timing: the helper writes the idle glyph BEFORE the watchdog goroutine spawns (because the goroutine spawns after WritePrompt, which is after WaitUntil(IsIdle), which is after the helper writes the glyph). So `Buffer.QuietFor` at the moment the goroutine starts ticking is already significant. This is fine — the test asserts the watchdog fires, not when.

### `TestRun_MissingRequiredFields` — add `MaxTurns` row

- New row: `{"no MaxTurns", func(c *Config) { c.MaxTurns = 0 }, "MaxTurns required"}`.
- Base config in the loop body gains `MaxTurns: 1` so existing rows still pass the new validation.

### `TestParseSpinnerSeconds` — inline table test in `runner_test.go` (or `watchdog_test.go` if developer prefers — file split is developer's call)

- Six rows: class-A `"✻ Baked for 5s"` → (5, true); class-A `"✻ Baked for 1m 30s"` → (90, true); class-B `"✻ Channeling…"` → (0, false); class-C `"✻ Actualizing… (2s · ↓1 tokens)"` → (0, false); no-spinner `"some text without glyph"` → (0, false); spinner-glyph-only `"✻"` → (0, false).
- Pure-function test; no Run invocation; runs in microseconds.

### Existing test compatibility

`TestRun_HappyPath_EmitsAndEndOfTurn`, `TestRun_CtxCancelDuringStream`, `TestRun_EmitErrorPropagation`, `TestRun_CtxCancelDuringSpawn`, `TestRun_TrustModalDetected`, `TestRun_McpFailureDetected`, `TestRun_NetworkFailureDetected` — all need `MaxTurns: 1` (or higher) in their `helperRunCfg`-returned Config, otherwise the new validation rejects them at entry. The cleanest fix: `helperRunCfg` sets `MaxTurns: 5` as a default that's high enough for none of the existing tests to trigger a budget hit. The budget test overrides to `MaxTurns: 1`. The watchdog test overrides to `MaxTurns: 10` (or leaves the default 5; either is safe — the watchdog test's body is empty so the counter never increments).

No test needs to override `WatchdogTick` / `WatchdogTrackerOpts` except `TestRun_WatchdogFires`. The default 1s tick + 30s/30s limits mean existing tests' wall-clock is unaffected by the watchdog goroutine (no fire within their 10s deadline).

### Helper extension — none required

The existing `jsonl` mode covers both new test bodies. The budget test passes a non-empty body containing one non-EOT line; the watchdog test passes an empty body. The helper's existing branch `if body == "" { return }` (lines 120-122) means the body-writer goroutine is a no-op for the watchdog test, and the helper just idles on SIGTERM as for the modal/banner tests. No new helper mode is needed — this keeps `helper_test.go` unchanged in this slice.

### What is NOT tested in this slice

- Real-claude byte-equivalence smoke test → [#482](https://github.com/pyrycode/pyrycode/issues/482) (carved off this ticket).
- Budget grace-timer-to-SIGKILL fallback → covered by `budget_test.go`; ptyrunner does not re-test.
- tuidriver Tracker mechanics (PTY-quiet detection, spinner-freeze detection) → covered by `tuidriver/tracker_test.go`; ptyrunner does not re-test.
- Concurrent fire of budget + watchdog → not constructible in a unit test without real claude (the budget needs JSONL events to fire; the watchdog needs PTY quiet; an in-tree fake-claude that produces both signals simultaneously would be elaborate). Both code paths are idempotent (SetExitReason first-wins, cancel idempotent), so the test gap is acceptable — the real-claude smoke test in #482 exercises the live composition.

## Security-sensitive label check

Issue labels (from `gh issue view 479 --json labels`): `size:s`, `done:po`, `wip:architect`. **`security-sensitive` is NOT present.** Skip the security-review pass per the architect agent's CLAUDE.md.

## Self-check — production file count

Production source files prescribed by this spec (excluding tests, markdown, the spec itself):

1. `internal/agentrun/ptyrunner/runner.go` — modified.
2. `internal/agentrun/ptyrunner/watchdog.go` — created.

Count: **2**. Well under the 5-file red line. Total LOC projection: ~80 production + ~150 tests + ~50 spec-related = ~280 LOC of net additions, well under the 600-LOC total red line. Distinct error/exit branches added: ~3 (MaxTurns validation, budget setup wrap, budget runtime log-only) plus 2 new exit-reason wire paths (max_turns trailer, error_during_execution trailer) — under the 10-branch red line. No edit fan-out (ptyrunner has no production callers; `cmd/pyry/agent_run.go` is still on streamrunner, cutover lands in #470).

## Open questions

- **Should the budget Terminate closure also call `tracker.RecordTransition("budget-hit")`?** Architect's call: yes, do it. The transition string surfaces in any subsequent watchdog error (if PTY stays quiet after budget-SIGTERM and the watchdog fires before the watchdog goroutine exits via runCtx-cancel), producing `watchdog: PTY quiet for Ns (last state: budget-hit)`. Costs one line, zero downside. Mirror for the watchdog goroutine isn't useful (the watchdog itself is the thing that fires).
- **Should `runWatchdog` log per-tick metrics (PTYQuietFor, spinner state)?** No. The package's logging discipline is "errors and counts only"; per-tick metrics would spam logs at 1Hz. The Warn-on-fire log is enough.
- **Should the spec mandate a specific defer-order comment?** Architect leaves the wording to the developer but recommends a block-comment above the chain naming the desired fire order (cancel → wg.Wait → counter.Stop → emitter.Close → sess.Close) and citing this spec by ticket number. Without it, the next refactor that reorders defers will silently break the invariant.
- **Should `WatchdogTrackerOpts` be embedded directly rather than wrapped?** Architect picked the wrapped form (`tuidriver.TrackerOpts` as a field type) over embedding so the Config field reads as a single named slot and so future ptyrunner-specific watchdog tuning fields can be added alongside without breaking the embedding. Either shape works; developer may swap if they prefer embedding.

## Out of scope (per ticket body + per architect CLAUDE.md rule)

- Real-claude byte-equivalence smoke test → [#482](https://github.com/pyrycode/pyrycode/issues/482) (carved off this ticket; blocks on this slice landing).
- `cmd/pyry/agent_run.go` cutover → [#470](https://github.com/pyrycode/pyrycode/issues/470) (blocked on this slice + #482 + #469 trust/settings).
- `streamrunner` deletion → not in this migration phase.
- **Knowledge-base note `docs/knowledge/codebase/479.md`** — the ticket body lists this as an AC, but the architect's CLAUDE.md ("Do NOT include `docs/knowledge/codebase/<N>.md` as an AC") supersedes. The documentation phase writes this doc from the spec + the merged diff; the developer's worktree should only mutate code, tests, and the spec file itself. Worked examples #471 and #478 both had architects who pushed this housekeeping into developer scope and burned turn budget; this slice omits the AC. Flag for the dispatcher: the knowledge doc still gets written, by documentation, after the PR merges. Developer's deliverables stop at the last code/test AC.
