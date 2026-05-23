# #513 — ptyrunner: adopt `tuidriver.Session.Events()` unified stream with continuous modal/banner monitoring

Capstone of the [#509](../../knowledge/codebase/509.md) / [#510](../../knowledge/codebase/510.md) / [#511](../../knowledge/codebase/511.md) / [#512](../../knowledge/codebase/512.md) migration. Collapses `ptyrunner.Run`'s post-`WritePrompt` body onto a single `for ev := range ch` dispatch on `tuidriver.Session.Events()`. Net effect: mid-run trust modals, MCP failure banners, and network failure anchors are now caught continuously (today they slip past after `WritePrompt`); the inline JSONL drain disappears; the `runWatchdog` goroutine stays as-is.

## Files to read first

- `internal/agentrun/ptyrunner/runner.go:39-55` — current imports (verify `sync`, `tuidriver` already wired; nothing new to add).
- `internal/agentrun/ptyrunner/runner.go:171-219` — `Run` doc comment + cleanup-LIFO contract. The doc rewrites in lockstep with the body.
- `internal/agentrun/ptyrunner/runner.go:283-309` — pre-`WritePrompt` phase (`WaitUntil(idle)` + one-shot HasTrustModal / HasMcpFailureBanner / HasNetworkFailure + `WritePrompt`). **Unchanged by this ticket.**
- `internal/agentrun/ptyrunner/runner.go:311-416` — post-`WritePrompt` body. The block being replaced: `runCtx, cancel := context.WithCancel(ctx)` → `var emitErr error; for entry := range entries { … }` → `runCtx.Err()` / `emitErr` return-site. The new loop slots in at the same place; everything above the `entries, err := tuidriver.TailJSONL` line stays verbatim.
- `internal/agentrun/ptyrunner/watchdog.go` — `runWatchdog` wrapper. **Untouched.** Spec keeps the goroutine separate.
- `internal/agentrun/ptyrunner/runner_test.go:28-55` — `helperRunCfg` scaffolding. The three new mid-run tests reuse it verbatim with new `mode` strings.
- `internal/agentrun/ptyrunner/runner_test.go:58-63` — `happyPathBody` / `noEotBody` JSONL constants. The mid-run-mcp / mid-run-network tests pair `noEotBody` with the new helper modes (the helper writes the banner anchor mid-stream while the JSONL never terminates with end-of-turn — the banner detection short-circuits the loop).
- `internal/agentrun/ptyrunner/runner_test.go:189-247` — existing modal/banner tests. The mid-run analogues mirror these but use the new helper modes and accept either error path (banner-shown event OR pre-write modal — see § Test plan for the timing argument).
- `internal/agentrun/ptyrunner/helper_test.go:52-104` — fake-claude `runHelper` + `stdinSeen` synchronisation. The new helper modes copy the `jsonl` mode's "wait for stdin first byte, then act" shape but write a TUI anchor to stdout instead of a JSONL body to disk.
- `internal/agentrun/ptyrunner/helper_test.go:106-145` — `jsonl` mode's post-`stdinSeen` write block. The three new modes mirror this control flow but target stdout, not the JSONL path.
- `~/go/pkg/mod/github.com/pyrycode/tui-driver@v0.0.0-20260523181457-c2dcd1e49992/pkg/tuidriver/events.go:111-150` — `Event` struct + `Session.Events(ctx, jsonlPath, startOffset)` signature. Confirm the (`<-chan Event`, `error`) return shape and the per-kind payload-field population.
- `~/go/pkg/mod/github.com/pyrycode/tui-driver@v0.0.0-20260523181457-c2dcd1e49992/pkg/tuidriver/events.go:152-303` — `mergeEvents` semantics: 50 ms `DefaultPollInterval`, rising-edge classification, modal-axis-dominates-idle-thinking, banner axes independent of modal. Drives the test timing argument (the new helper modes flip the relevant predicate after `stdinSeen`; the 50 ms poll catches it well within the 10 s test deadline).
- `~/go/pkg/mod/github.com/pyrycode/tui-driver@v0.0.0-20260523181457-c2dcd1e49992/pkg/tuidriver/modal.go:22-30` — `ModalClass` constants. `ModalClassTrustFolder` is the one the loop matches; the other classes are emitted but ignored.
- `internal/agentrun/streamjson/emitter.go:177` — `Emit(entry tuidriver.JSONLEntry) error` signature. Unchanged; the new loop calls it with `ev.Entry`.
- `internal/agentrun/budget/budget.go:OnEvent` / `OnEndOfTurn` — verify `OnEvent(entry tuidriver.JSONLEntry)` (post-[#512](../../knowledge/codebase/512.md) signature) and `OnEndOfTurn()` (no args). Unchanged.
- `docs/knowledge/codebase/512.md` § "Implementation" — the inline drain shape that #513 replaces. Confirms the `emitErr` capture-then-prioritise three-source pattern (ctx-cancel → emitErr → nil) the AC explicitly preserves.
- `docs/knowledge/codebase/510.md` — watchdog delegation shape. Confirms the `tuidriver.RunWatchdog` glue layer the spec keeps as a separate goroutine.

## Context

After #512, ptyrunner's post-`WritePrompt` block has three concerns it splits across two goroutines and one inline loop:

1. **Watchdog goroutine** (`runWatchdog` → `tuidriver.RunWatchdog`) — polls the rolling buffer + spinner state at `cfg.WatchdogTick` (default 1 s), fires `cancel()` + `emitter.SetExitReason(ExitReasonError)` on wedge.
2. **Inline JSONL drain** — `for entry := range entries { emitter.Emit; counter.OnEvent; if IsEndTurn { counter.OnEndOfTurn; break } }`, fed by `tuidriver.TailJSONL`.
3. **One-shot post-idle modal/banner check** — runs once between `WaitUntil(idle)` and `WritePrompt`. After `WritePrompt`, mid-run modals and banners are not detected at all.

`tuidriver.Session.Events(ctx, jsonlPath, 0)` (at the pinned `v0.0.0-20260523181457-c2dcd1e49992`) unifies the JSONL tail and the PTY-state classification into one channel of typed `Event` values: `EventKindPtyModalShown / *Hidden`, `EventKindPtyMcpFailure{Shown,Hidden}`, `EventKindPtyNetworkFailure{Shown,Hidden}`, `EventKindPtyIdle / Thinking`, `EventKindJsonlEntry`, `EventKindJsonlEndOfTurn`. The merge loop polls the PTY axes at `DefaultPollInterval` (50 ms) and drains the internal JSONL channel in arrival order.

The ticket's goal is to collapse concerns 2 + 3 onto this unified stream — gaining continuous mid-run modal/banner detection as a free side-effect of the rising-edge classification. Concern 1 (the watchdog) **stays as a separate goroutine** because its cadence (default 1 s) and its state (a `*tuidriver.Tracker`) are independent of the Events merge loop.

## Design

### Decision 1 — keep `runWatchdog` as a separate goroutine

The AC explicitly allows either folding or keeping the watchdog. **Spec chooses keep.**

Reasoning:

- The watchdog's Tracker is a `*tuidriver.Tracker` with its own `PTYQuietLimit` / `SpinnerFreezeLimit` state machine and a configurable tick (default 1 s, tests use 50 ms). It's a separate axis from the Events merge loop.
- Folding would require either (a) a manual `time.Ticker` arm in the `select` that competes with the Events channel — duplicating what `tuidriver.RunWatchdog` already does internally — or (b) a watchdog-event-channel adapter that wraps `RunWatchdog` to emit a sentinel on wedge — extra plumbing for no win.
- Keeping `runWatchdog` separate **preserves the entire cleanup-LIFO chain verbatim** (`cancel() → wg.Wait() → counter.Stop() → emitter.Close() → sess.Close()`); the doc-comment block at `runner.go:207-219` is unchanged.
- The `Events` channel itself does NOT need a watchdog timer in its select: it's already cancellable via `runCtx`, and the merge loop closes the channel when ctx fires.

Concretely: `watchdog.go` is untouched. The `wg.Add(1) / go runWatchdog(...) / defer wg.Wait() / defer cancel()` block at `runner.go:375-382` stays as-is.

### Decision 2 — pre-`WritePrompt` phase stays as-is

The AC mandates this and `Session.Events`'s implementation enforces it (`Events` calls `TailJSONL` synchronously, which fails with `os.ErrNotExist` until the JSONL file appears, which happens only after `WritePrompt` drives claude). The pre-`WritePrompt` `WaitUntil(idle)` + post-idle one-shot `HasTrustModal / HasMcpFailureBanner / HasNetworkFailure` checks at `runner.go:283-305` are unchanged.

No alternative explored — the architect-may-explore-but-not-required carve-out in the AC is declined. Splitting the PTY axes into a separate pre-JSONL Events stream would mean either two `Session.Events` calls (one PTY-only before `WritePrompt`, one full after) or a new tui-driver entry point — both are scope expansions for no operator-visible win (the one-shot check already covers the boot window).

### Decision 3 — events to act on, events to ignore

The dispatch is a `switch ev.Kind`. The AC's load-bearing arms:

| `ev.Kind`                              | Action                                                                                                                                                          |
| -------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `EventKindPtyModalShown`               | If `ev.Modal == tuidriver.ModalClassTrustFolder`, `return ErrTrustModalDetected`. Otherwise no-op (`Permission`, `MCP`, `Agents`, `SlashPicker`, etc.).          |
| `EventKindPtyMcpFailureShown`          | `return ErrMcpFailureBanner`.                                                                                                                                   |
| `EventKindPtyNetworkFailureShown`      | `return ErrNetworkFailure`.                                                                                                                                     |
| `EventKindJsonlEntry`                  | `if eerr := emitter.Emit(ev.Entry); eerr != nil && emitErr == nil { emitErr = eerr }`; then `counter.OnEvent(ev.Entry)`. (Capture-then-prioritise; see § Decision 4.) |
| `EventKindJsonlEndOfTurn`              | `counter.OnEndOfTurn()`; break the loop (clean exit, falls through to return-site).                                                                            |
| `EventKindPtyModalHidden`              | Ignore.                                                                                                                                                          |
| `EventKindPtyMcpFailureHidden`         | Ignore.                                                                                                                                                          |
| `EventKindPtyNetworkFailureHidden`     | Ignore.                                                                                                                                                          |
| `EventKindPtyIdle`                     | Ignore.                                                                                                                                                          |
| `EventKindPtyThinking`                 | Ignore.                                                                                                                                                          |
| `EventKindUnknown`                     | Ignore (the merge loop never emits it, but the switch's default arm is benign).                                                                                  |

**Rationale for ignoring non-Trust modal classes.** The current one-shot detector uses `HasTrustModal` (boolean), not `DetectModalClass`. The other classes (`Permission`, `MCP`, etc.) are not load-bearing for ptyrunner today — the deny-default settings file produced by [#469] already suppresses permission prompts, and the MCP/Agents/SlashPicker classes are interactive UI states that the agent-run loop should not be encountering. If a future failure mode surfaces one of them, the open question is which sentinel to surface; until then, ignoring is correct per the project's Evidence-Based Fix Selection principle.

**Rationale for ignoring `*Hidden` events.** By the time a Hidden event arrives, the corresponding `Shown` would already have triggered a return (for the Trust / Mcp / Network arms). For non-Trust modals, the `Shown` arm was a no-op, so the paired `Hidden` is also a no-op. There's no scenario where the runner needs to observe a Hidden transition.

**Rationale for ignoring `Idle` / `Thinking`.** These are informational state transitions; the runner doesn't act on them post-`WritePrompt`. (The pre-`WritePrompt` `WaitUntil(IsIdle)` covers the only idle event the runner cares about.)

No log calls inside the switch. The package's logging discipline (`runner.go:18-27` doc) forbids logging entry content; an `slog.Debug("event", "kind", ev.Kind)` would also add per-event log volume on every JSONL line, which is operator-noise.

### Decision 4 — `emitErr` capture-then-prioritise, three-source

The AC explicitly preserves the current pattern (no four-source restructure — that's [#512]'s out-of-scope follow-up, deliberately deferred by PO for this ticket). The three sources at the return site:

```
if runCtx.Err() != nil {
    return nil
}
if emitErr != nil {
    return fmt.Errorf("ptyrunner: emit: %w", emitErr)
}
return nil
```

The new sentinel-error returns (`ErrTrustModalDetected` / `ErrMcpFailureBanner` / `ErrNetworkFailure`) short-circuit out of the switch before the return-site is reached, so they take priority over both `runCtx.Err()` and `emitErr`. This matches the current pre-`WritePrompt` semantics (the one-shot detector also `return`s the sentinel without consulting `ctx.Err()`).

**Loop-exit cases**:

1. `EventKindJsonlEndOfTurn` arm — `break`s the `for ev := range ch` loop; falls through to the return-site. `counter.OnEndOfTurn()` is called before the break.
2. Channel closes (ctx-cancel, internal JSONL tail closes, session terminates without EOT) — `for ev := range ch` exits naturally on the closed channel. Falls through to the return-site. If `runCtx.Err() != nil`, returns nil (operator-shutdown collapse). If not — and no `emitErr` — also returns nil. This is the [#512] NIT case (channel-close-without-EOT silently treated as clean exit); explicitly deferred to a future ticket per the AC's preservation directive.

### Decision 5 — `Session.Events` synchronous error wrap

Mirrors the current `tuidriver.TailJSONL` call site. The AC names the wrap shape:

```
ch, err := sess.Events(runCtx, jsonlPath, 0)
if err != nil {
    if isCtxErr(runCtx, err) {
        return nil
    }
    return fmt.Errorf("ptyrunner: events: %w", err)
}
```

The previous `ptyrunner: tail: %w` wrap (which named `tuidriver.TailJSONL`'s open/seek failures) is repurposed as `ptyrunner: events: %w` — same semantic class (synchronous open/seek failure from an internal `TailJSONL` call), new name reflecting the new entry point. The `Run` doc comment's error-contract list (`runner.go:178-205`) renames `tail` → `events`.

### Final post-`WritePrompt` shape (illustrative — exact identifier choices are the implementer's)

The replacement block sits between the current `if werr := tuidriver.WaitForSessionJSONL(runCtx, jsonlPath); …` line and the existing `return nil` at the bottom of `Run`. The watchdog spawn + the `WaitForSessionJSONL` call + the `home` / `jsonlPath` resolution **all stay in their current positions** — only the `entries, err := tuidriver.TailJSONL(...)` line and the `var emitErr error; for entry := range entries { … }` block change.

Signature + behaviour summary (replaces `runner.go:390-415`):

- `ch, err := sess.Events(runCtx, jsonlPath, 0)` — opens the unified stream; `ptyrunner: events: %w` on non-ctx error, collapse-to-nil on ctx-cancel via `isCtxErr`.
- `var emitErr error` — capture for the three-source prioritise pattern.
- `for ev := range ch { switch ev.Kind { … } }` — single dispatch loop. Per-arm semantics from Decision 3. The Trust / Mcp / Network arms `return` the sentinel directly. The `EventKindJsonlEndOfTurn` arm calls `counter.OnEndOfTurn()` then `break`s.
- Return site (unchanged): `if runCtx.Err() != nil { return nil }; if emitErr != nil { return fmt.Errorf("ptyrunner: emit: %w", emitErr) }; return nil`.

The block as a whole is ~30 lines (vs. the current ~25-line drain) — the switch case-arms are the new bulk. No new exported symbols; no new helper functions.

### Doc-comment updates

Two doc blocks need a sync. **Do not** add prose beyond the deltas listed.

- `runner.go:171-205` — `Run`'s package doc paragraph + return-value contract:
  - Replace "drains the per-session JSONL via tuidriver.TailJSONL inline" with "drains the per-session JSONL AND polls PTY-state transitions via tuidriver.Session.Events".
  - In the error list, rename `tail: %w` → `events: %w` (same wrap shape, different open call).
  - Add one line to the contract: "Mid-run modal/banner detection: a trust-folder modal class, MCP failure banner, or network failure anchor that becomes visible AFTER WritePrompt returns the corresponding sentinel error. The detection cadence is tui-driver's DefaultPollInterval (50 ms)."
- `runner.go:207-219` — cleanup-LIFO contract:
  - Replace "The tuidriver.TailJSONL channel is drained inline by Run's own goroutine, so wg tracks only the watchdog." with "The tuidriver.Session.Events channel is drained inline by Run's own goroutine, so wg tracks only the watchdog."
  - Otherwise unchanged. The five-step LIFO sequence is preserved verbatim.

`watchdog.go` and `buildArgs` doc comments are untouched.

## Concurrency model

Unchanged structurally — the goroutine inventory is:

1. **Run's own goroutine** — drives the `for ev := range ch` dispatch.
2. **`runWatchdog` goroutine** — wedge detector; calls `cancel()` + `SetExitReason` on fire.
3. **Tui-driver internals** — `Session.Events` spawns a single `mergeEvents` goroutine that owns the 50 ms ticker and the channel-send. It internally consumes a `TailJSONL` goroutine. Both are bookkept inside the library, not by Run's `wg`.

Cleanup-LIFO (preserved verbatim — see § Doc-comment updates):

```
cancel() → wg.Wait() → counter.Stop() → emitter.Close() → sess.Close()
```

- `cancel()` cancels `runCtx`, which causes `mergeEvents` to close `ch` (loop exits) and `runWatchdog` to return.
- `wg.Wait()` drains the watchdog goroutine before `emitter.Close()` writes the trailer (preserving the existing "no SetExitReason(ExitReasonError) race with Close" invariant).
- `counter.Stop()` cancels the budget's SIGKILL grace timer.
- `emitter.Close()` writes the `result` trailer to `cfg.Stdout`.
- `sess.Close()` SIGTERMs claude.

The `mergeEvents` goroutine is bookkept by tui-driver — its channel-close on `runCtx.Done()` is the loop-exit signal. There's no need to add `mergeEvents` to `wg` because its only side-effect (sending on `ch`) is naturally drained by the consumer in `Run` until ctx-cancel makes the channel close.

## Error handling

| Failure mode                                                                           | Surfaces as                                                                                                                |
| -------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `Session.Events` synchronous open/seek failure (non-ctx)                               | `fmt.Errorf("ptyrunner: events: %w", err)`                                                                                  |
| `Session.Events` synchronous failure on ctx-cancel                                     | nil (operator-shutdown collapse via `isCtxErr`)                                                                             |
| `emitter.Emit(ev.Entry)` failure (e.g. broken pipe on `cfg.Stdout`)                    | Captured into `emitErr`; surfaced at return-site as `fmt.Errorf("ptyrunner: emit: %w", emitErr)` only if no ctx-cancel.     |
| `EventKindPtyModalShown` with `Modal == ModalClassTrustFolder`                         | `ErrTrustModalDetected` (return short-circuits the switch).                                                                 |
| `EventKindPtyMcpFailureShown`                                                          | `ErrMcpFailureBanner`.                                                                                                       |
| `EventKindPtyNetworkFailureShown`                                                      | `ErrNetworkFailure`.                                                                                                         |
| `EventKindJsonlEndOfTurn`                                                              | nil (clean exit; `counter.OnEndOfTurn()` called, loop breaks, return-site collapses through).                                |
| Channel-close without EOT and without ctx-cancel                                       | nil (preserved from #512; the four-source restructure is explicitly deferred — see § Open questions).                       |
| Watchdog wedge                                                                         | `runWatchdog` fires `emitter.SetExitReason(ExitReasonError)` + `cancel()`; `runCtx.Err()` collapses to nil; trailer reflects `error_during_execution`. |

## Testing strategy

Three new tests, table-driven only where they share structure. Each test reuses `helperRunCfg` and the existing `parseTrailer` helper; the only new test-side machinery is three additional fake-claude helper modes.

### New helper-test modes (helper_test.go)

Three new entries on the `switch mode` block, each modelled on the existing `jsonl` mode's "write initial idle glyph, then wait for `stdinSeen`, then perform a follow-up action" shape:

- **`mid_trust`** — initial stdout: `idleGlyph + " "` (so `WaitUntil(idle)` succeeds AND the one-shot `HasTrustModal` is **false** at the pre-`WritePrompt` check). After `stdinSeen`, write `"Quicksafetycheck" + idleGlyph + " "` to stdout. The Events merge loop's 50 ms ticker observes `DetectModalClass(snap) == ModalClassTrustFolder` on its next poll and emits `EventKindPtyModalShown{Modal: ModalClassTrustFolder}`.
- **`mid_mcp_failure`** — initial stdout: `idleGlyph + " "`. After `stdinSeen`, write `"1 MCP server failed " + idleGlyph + " "` to stdout. The merge loop observes `HasMcpFailureBanner(snap)` flip false→true and emits `EventKindPtyMcpFailureShown`.
- **`mid_network_failure`** — initial stdout: `idleGlyph + " "`. After `stdinSeen`, write `"FailedToOpenSocket " + idleGlyph + " "` to stdout. The merge loop emits `EventKindPtyNetworkFailureShown`.

All three modes share the SIGTERM handler + 30 s ceiling from the existing modes. The mid-run write block uses `fmt.Fprint(os.Stdout, ...)` + `os.Stdout.Sync()` to ensure the bytes land in the PTY before the next ticker tick.

**Refactor note for the helper.** The `stdinSeen` channel currently signals only the `jsonl` mode's goroutine. The three new modes also need to gate their write on `stdinSeen`. The helper's stdin-drain goroutine and the `stdinSeen` send are already in place at lines 87-104 (mode-agnostic). The clean shape is one additional `if mode == "mid_trust" || mode == "mid_mcp_failure" || mode == "mid_network_failure"` block in `runHelper` after the existing `if mode == "jsonl"` block — it waits on `stdinSeen` and writes the mode-specific anchor to stdout. Mirror the jsonl block's `time.After(20 * time.Second)` timeout. **Do not** duplicate the stdin-drain goroutine.

### New tests (runner_test.go)

Three new top-level test functions. Each:

- Builds a `helperRunCfg` with the corresponding new mode and an empty `jsonlBody` (the mid-run modal/banner short-circuits before any JSONL line lands; the JSONL path is established by `WaitForSessionJSONL` but the helper never writes to it).
- Runs `Run` with a 10 s `context.WithTimeout`.
- Asserts `err != nil` and `errors.Is(err, <sentinel>)` for the appropriate sentinel.
- Asserts the error-message substring (the remediation/anchor hint from each sentinel's text, to match the existing `TestRun_TrustModalDetected` / `TestRun_McpFailureDetected` / `TestRun_NetworkFailureDetected` patterns).

Bullet-point scenarios for the implementer (not full test bodies — write in the project idiom):

- `TestRun_MidRun_TrustModalDetected` — mode `mid_trust`. Sentinel `ErrTrustModalDetected`. Substring `"#469's MarkWorkdirTrusted"`.
- `TestRun_MidRun_McpFailureDetected` — mode `mid_mcp_failure`. Sentinel `ErrMcpFailureBanner`. Substring `"MCP failure banner"`.
- `TestRun_MidRun_NetworkFailureDetected` — mode `mid_network_failure`. Sentinel `ErrNetworkFailure`. Substring `"FailedToOpenSocket"`.

**Table-drive note.** The three tests share scaffolding (`helperRunCfg` mode + assertions); table-driving them under a single `TestRun_MidRun_ModalAndBannerDetection` with `t.Run(tc.name, …)` subtests + `t.Parallel()` is the conventional shape and matches `TestRun_MissingRequiredFields`'s pattern. The existing pre-`WritePrompt` `TestRun_TrustModalDetected` / `TestRun_McpFailureDetected` / `TestRun_NetworkFailureDetected` are written as three separate top-level tests; either shape is acceptable — pick whichever reads cleaner in-place. **Do not** merge the mid-run tests with the pre-`WritePrompt` tests (different fixture, different timing semantic, separate coverage stories).

### Clean run

The AC's fourth new test ("clean run: `EventKindJsonlEndOfTurn` fires → `Run` returns nil with the trailer present") is **already covered by the existing `TestRun_HappyPath_EmitsAndEndOfTurn`** (lines 109-187). That test's `happyPathBody` is a single assistant entry whose `stop_reason: "end_turn"` triggers `IsEndTurn(entry) == true`; in the new design `mergeEvents` will emit `EventKindJsonlEntry` then `EventKindJsonlEndOfTurn` for that same entry, and the runner reacts to the latter. The test asserts:

- Init envelope is the first stdout line.
- Verbatim JSONL assistant entry is the second line.
- Trailer has `Subtype: "success"`, `TerminalReason: "completed"`, `IsError: false`, `NumTurns: 1`, `StopReason: "end_turn"`.

Post-migration, all of those assertions still hold because:

- The init envelope is emitted by `streamjson.New` before the Events loop opens — unchanged.
- The verbatim JSONL line is emitted by `emitter.Emit(ev.Entry)` inside the `EventKindJsonlEntry` arm — same call as the current `emitter.Emit(entry)`.
- `counter.OnEndOfTurn()` is called inside the `EventKindJsonlEndOfTurn` arm — same call as the current post-`IsEndTurn` branch.
- The trailer is written by `emitter.Close()` in the same cleanup-LIFO position.

No new test is needed for this AC line. The implementer should explicitly verify `TestRun_HappyPath_EmitsAndEndOfTurn` is unmodified after the migration — if a test-side delta is required, it's a signal something else broke.

### Existing tests that must continue to pass

- `TestRun_HappyPath_EmitsAndEndOfTurn` — see above. Unmodified.
- `TestRun_TrustModalDetected` / `TestRun_McpFailureDetected` / `TestRun_NetworkFailureDetected` — pre-`WritePrompt` one-shot detectors. The fake-claude helper writes the modal/banner anchor **at startup** (before `WaitUntil(idle)` returns), so the post-idle one-shot check at `runner.go:294-305` fires before reaching `WritePrompt`. Unchanged by this ticket.
- `TestRun_CtxCancelDuringSpawn` — collapse-to-nil at the Spawn stage. Unmodified.
- `TestRun_CtxCancelDuringStream` — partial run + ctx-cancel mid-stream. Post-migration the flow is `WritePrompt → WaitForSessionJSONL → Session.Events → first emitted line → cancel → channel closes → for-loop exits → runCtx.Err() != nil → return nil`. The assertion (`Subtype: "error_during_execution"` in the trailer) still holds because the trailer is driven by `endOfTurnSeen` being false at `emitter.Close()` time. Unmodified.
- `TestRun_EmitErrorPropagation` — `failingWriter` returns error on the first non-init Emit. Post-migration the failing call is `emitter.Emit(ev.Entry)` inside the `EventKindJsonlEntry` arm. `emitErr` captures the first error; the loop continues to `EventKindJsonlEndOfTurn`, calls `OnEndOfTurn`, breaks; return-site sees `runCtx.Err() == nil`, returns `fmt.Errorf("ptyrunner: emit: %w", emitErr)`. Assertion (substring `"ptyrunner: emit:"` + `"simulated pipe broken"`) still holds. Unmodified.
- `TestRun_BudgetHitBeforeEndOfTurn` — `noEotBody` + `MaxTurns: 1` fires the budget at the first assistant entry. Post-migration the boundary semantic (max_turns wins over completion, per [#512]) is unchanged because `counter.OnEvent` runs inside the `EventKindJsonlEntry` arm before the `EndOfTurn` event arrives. The Terminate callback sets `ExitReasonMaxTurns` + cancels `runCtx`; the channel closes; for-loop exits; return-site collapses to nil. Trailer asserts `Subtype: "error_max_turns"`. Unmodified.
- `TestRun_WatchdogFires` — empty JSONL body + 200 ms watchdog limits. The watchdog goroutine fires `SetExitReason(ExitReasonError)` + `cancel()`; channel closes; for-loop exits; return-site collapses to nil. Trailer asserts `Subtype: "error_during_execution"`. Unmodified.
- `TestBuildArgs` / `TestRun_MissingRequiredFields` — unaffected.

### Test plan checklist

- [ ] `make check` green (vet + staticcheck + race tests).
- [ ] `go test -race ./internal/agentrun/ptyrunner/...` green — race detector clean. The two writers to `runCtx` cancel (the budget Terminate hook and the watchdog goroutine) are pre-existing and ctx-safe; the new Events loop is single-goroutine on the ch consumer side.
- [ ] `go list -deps ./internal/agentrun/ptyrunner/... | grep pyrycode/internal/supervisor` empty (no-supervisor-import invariant).
- [ ] `make e2e-realclaude` green — the wire shape is identical: same `emitter.Emit(entry)` calls, same trailer, same init envelope. The byte-equivalence fixture is well under MaxTurns and does not exercise the mid-run modal/banner paths.

## Open questions

- **Channel-close-without-EOT silent-nil case.** Carried over from [#512]'s lessons-learned NIT. Today (and post-#513) a `Session.Events` channel that closes without ever emitting `EventKindJsonlEndOfTurn` and without `runCtx` being cancelled silently returns nil. The four-source restructure (ctx-cancel → emit-error → channel-close-no-EOT → clean-EOT) is **deferred** per the AC's explicit "`emitErr` capture-then-prioritise pattern preserved". File-time follow-up: a future ticket that combines this restructure with a `ptyrunner: tail closed without end_turn` wrap and a corresponding test that drives a channel-close without ctx-cancel (e.g. tui-driver-side failure injection).
- **Non-Trust modal classes ignored.** The switch ignores `Permission` / `MCP` / `Agents` / `SlashPicker` / `ModelSelect` / `AskUserQuestion` / `PermissionsConfig`. If a future failure mode surfaces one of them as a mid-run wedge, the right sentinel is unclear (the existing three sentinels are class-specific; `ErrPermissionPromptDetected` etc. would need to be added). Defer to evidence — no observed failure mode for any of these classes in agent-run today.
- **PTY-only Events stream before JSONL exists.** The AC's optional carve-out (architect-may-explore-but-not-required). Declined — see § Decision 2. If a future ticket needs continuous pre-`WritePrompt` PTY monitoring (e.g. for a fail-fast on a modal class that appears during claude's startup splash), tui-driver would need a `Session.PtyEvents(ctx) <-chan Event` variant. Out of scope here.

## Out of scope

- The four-source `emitErr` restructure (deferred — see Open questions).
- Migrating `internal/agentrun/selfcheck` and `internal/e2e/realclaude/fixtures.go` off `jsonl.NewReader` so `internal/agentrun/jsonl/` can be deleted ([#512] follow-up).
- Promoting `streamjson.readUsage` into `tuidriver.Usage` ([#511] follow-up).
- Any change to `buildArgs` or the pre-`WritePrompt` phase.

## Files this ticket touches

- `internal/agentrun/ptyrunner/runner.go` — modified. Post-`WritePrompt` body: replace the `tuidriver.TailJSONL` open + inline drain block with `tuidriver.Session.Events` open + `for ev := range ch` switch. Update doc comments at lines 171-205 and 207-219. Net delta: ~+30 LOC / −25 LOC ≈ +5 LOC. No new imports, no new exported symbols. `watchdog.go` untouched.
- `internal/agentrun/ptyrunner/runner_test.go` — modified. Three new tests (mid-run trust, mid-run mcp, mid-run network). Net delta: ~+100-130 LOC, depending on table-drive vs. three top-level. No structural changes to existing tests.
- `internal/agentrun/ptyrunner/helper_test.go` — modified. Three new helper modes (`mid_trust`, `mid_mcp_failure`, `mid_network_failure`) wired into the existing `runHelper` switch + a follow-up write block keyed by `stdinSeen`. Net delta: ~+30 LOC.

Total: 1 production source file (`runner.go`) + 2 test files. Well within the ≤ 5-file `s` budget. Total written work: ~165-185 LOC, well under the ≤ ~600-LOC `s` budget. No new files; no deletions.

Per-ticket housekeeping (knowledge-base note at `docs/knowledge/codebase/513.md`) is the **documentation phase's** job, not the developer's — do not include it as a developer AC. The spec ends with the developer's last code/test AC.
