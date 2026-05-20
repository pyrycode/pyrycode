# #478 — `internal/agentrun/ptyrunner.Run` JSONL tail + stream-json emit + end-of-turn

Sub-issue of [#329](https://github.com/pyrycode/pyrycode/issues/329). Sibling to [#469](https://github.com/pyrycode/pyrycode/issues/469) (trust + settings helpers), [#475](https://github.com/pyrycode/pyrycode/issues/475) (trust pre-write follow-up, landed), and the follow-up budget+watchdog slice. Builds on [#471](https://github.com/pyrycode/pyrycode/issues/471)'s scaffolding.

## Files to read first

- `internal/agentrun/ptyrunner/runner.go` — the slice being extended in-place. Read end-to-end (~266 lines). Pay close attention to: package doc (lines 1-31, will be revised here), `Config` struct (lines 64-127, gains a required `HomeDir` test seam and `Stdout` becomes required), `Run` body (lines 151-233, gains emitter + tail.Watcher wiring after `WritePrompt`).
- `internal/agentrun/ptyrunner/runner_test.go` — extends with four new test funcs; the existing `helperRunCfg`, `TestRun_HappyPath`, modal/banner, and `TestRun_CtxCancelDuringSpawn` cases stay as-is. `TestRun_MissingRequiredFields` gains a `Stdout` entry.
- `internal/agentrun/ptyrunner/helper_test.go` — extends with a `jsonl` helper mode that, after observing the parent's `WritePrompt` arriving on stdin, writes a small JSONL stream to a per-test path supplied via env. The existing modes (`idle`, `trust`, `mcp_failure`, `network_failure`, `slow_spawn`) stay as-is.
- `internal/agentrun/jsonl/tail/watcher.go` — the watcher this slice composes. Skim end-to-end (~295 lines). Key contracts: `New(Config{Workdir, SessionID, HomeDir, OnEvent, OnEndOfTurn, Logger})` validates and arms fsnotify; `Run(ctx)` blocks until EOT fires or ctx cancels and returns `nil` on EOT / `ctx.Err()` on cancel. The watcher owns `agentrun.EncodeProjectDir`, the bounded retry, and the drain loop — ptyrunner only supplies the callbacks.
- `internal/agentrun/jsonl/tail/watcher_test.go:54-88` — the `startWatcher` helper and the `eventRecorder` pattern. This slice's tests reuse the same `OnEvent` closure shape but the recorder lives inside ptyrunner's tests (no cross-package dependency needed).
- `internal/agentrun/streamjson/emitter.go` — the emitter this slice composes. Read end-to-end (~273 lines). Key contracts: `New(Config{Writer, SessionID, Logger})` requires both writer + non-empty SessionID; `Emit(jsonl.Event) error` is sticky on write failure (subsequent Emits no-op) and a no-op after Close; `Close()` writes the `result` trailer exactly once and is idempotent; the default exit-reason classification is `ExitReasonCompletion` iff EOT was observed during Emit, else `ExitReasonError`. The slice MUST NOT call `SetExitReason` (the budget slice owns that seam).
- `internal/agentrun/streamjson/emitter_test.go:18-46` — the `newTestEmitter` shape and the deterministic-clock pattern. Reused conceptually in the JSONL-tail tests (mostly just to inspect the trailer's `subtype` field).
- `internal/agentrun/streamrunner/runner.go:116-175` — the operator-shutdown collapse pattern (`if ctx.Err() != nil { return nil }`) and the "writers are opaque" discipline. The ptyrunner equivalent collapses both the watcher's ctx.Err() AND any Emit-time error captured in the closure.
- `internal/agentrun/jsonl/reader.go:42-92` — the `jsonl.Event` shape (notably `Raw`, `EndOfTurn`, `Usage`). The emitter consumes these; ptyrunner just forwards them without inspecting fields.
- `docs/knowledge/codebase/471.md` — sibling-slice's knowledge doc. Read for tone + section ordering; the 478 knowledge doc mirrors this shape.

## Context

[#471](https://github.com/pyrycode/pyrycode/issues/471) introduced `internal/agentrun/ptyrunner/runner.go` with spawn + idle wait + modal detectors + `WritePrompt` + clean shutdown. Nothing imports it yet. This slice extends `Run` in-place to add the JSONL tail + stream-json emission — the happy-path half of the dispatcher contract. The safety net (pyry-side max-turns budget + watchdog) and the real-claude byte-equivalence smoke test land in a separate follow-up slice on top of this one. The `cmd/pyry/agent_run.go` cutover is [#470](https://github.com/pyrycode/pyrycode/issues/470), blocked on the follow-up slice + [#469](https://github.com/pyrycode/pyrycode/issues/469).

The dispatcher needs zero changes: `internal/agentrun/streamjson/emitter.go` already reproduces the exact wire shape the current `streamrunner` path emits, including the `result` trailer.

Strategic motivation (recap of #471): Anthropic's 2026-06-15 billing-policy split explicitly names "Interactive Claude Code in the terminal or IDE" as subscription-eligible. The stream-json subprocess surface is not enumerated and risks landing metered. The PTY pivot is the proactive move to the named-eligible surface before that date.

## Design

### Package boundary

Stays at `internal/agentrun/ptyrunner/`. No new files. Three existing files change:

| File | Change |
| --- | --- |
| `runner.go` | Package doc (dependency-direction note narrows), `Config` (one new field `HomeDir`; `Stdout` becomes required), `Run` body (new emitter+watcher wiring after `WritePrompt`). |
| `runner_test.go` | `helperRunCfg` gains JSONL-path threading via env; four new test funcs; `TestRun_MissingRequiredFields` gains the `Stdout` case. |
| `helper_test.go` | New `jsonl` helper mode that writes a JSONL stream after observing the parent's `WritePrompt` on stdin. |

No new exported types, no new exported funcs. The slice extends an existing exported function (`Run`) in-place and a Config struct's surface (one new field).

### Package doc — dependency-direction note

The current package doc (`runner.go` lines 13-17 and 23-31) reflects the now-superseded #472. Update both blocks to:

- Lines 13-17: drop the "subsequent slices wire …" enumeration and the "Config declares MaxTurns and Stdout for forward-compatibility with #472" sentence. Replace with one sentence noting this slice wires the tail + emit, and one sentence noting that budget + watchdog land in a follow-up.
- Lines 23-31: the forbidden-import list narrows to **only** `github.com/pyrycode/pyrycode/internal/supervisor`. The sibling subpackages (`jsonl`, `jsonl/tail`, `streamjson`) are now allowed imports (and used). Budget remains out of scope here but it's not worth a separate negative assertion — drop it from the forbidden list. Update the `go list -deps` verification command accordingly:

```
go list -deps ./internal/agentrun/ptyrunner/... | grep pyrycode/internal/supervisor
```

Expected output: empty.

Keep the logging-discipline paragraph (lines 19-21) as-is and extend it with one sentence: "The wired `jsonl/tail` watcher and `streamjson` emitter inherit the same discipline — neither logs Event content; ptyrunner does not add any log call that would either."

### `Config` changes

Two field-shape changes; no positional reordering.

**`Stdout` becomes required.** The field comment on lines 107-110 currently says "declared for forward-compatibility with #472 (stream-json re-emit); NOT written to in this slice. Run does not touch this field." Update to: "Required. The `streamjson.Emitter` writes per-event stream-json lines and the `result` trailer here. Production callers pass `os.Stdout`; tests pass a `bytes.Buffer` or a failing writer."

**New field: `HomeDir string`.** Add after `Stderr`, before `Env`. Field comment:

> HomeDir is an optional test seam. When non-empty, it overrides the home directory used by the JSONL watcher (`~/.claude/projects/<encoded-workdir>/`). Production callers leave it empty; the watcher consults `os.UserHomeDir()` in that case. Tests use a `t.TempDir()` value so each test gets an isolated `~/.claude/projects` tree.

Threaded to `tail.Config.HomeDir` unchanged. No validation (an empty string is the documented "use real home" signal).

**`MaxTurns` keeps its forward-compat marker.** The budget slice is the consumer; the field comment stays as-is in shape but the referenced slice number changes from "#472" to "the follow-up slice" (no GitHub number assigned yet at the time this spec is written; a literal "the follow-up slice on top of #478" prose phrase is fine).

### `Run` — new wiring after `WritePrompt`

The slice extends the current `Run` body in-place. Existing structure stays:

1. Validate required fields (now includes `Stdout`).
2. Resolve logger.
3. `exec.CommandContext` + env + `EnsureClaudeEnv`.
4. `tuidriver.Spawn` + `defer sess.Close()` (close-error logged Warn, not returned).
5. `tuidriver.WaitUntil(IsIdle)` (ctx-cancel collapses to nil).
6. Modal / MCP / network detectors against the post-idle snapshot.
7. `sess.WritePrompt(string(cfg.PromptBytes))`.

**New (after step 7):**

8. Construct the `streamjson.Emitter` with `Writer: cfg.Stdout`, `SessionID: cfg.SessionID`, `Logger: logger`. Wrap any New error as `fmt.Errorf("ptyrunner: emitter: %w", err)`. Register `defer emitter.Close()` (return value handled per § "Cleanup order" below). This defer registers AFTER the `sess.Close()` defer, so LIFO ordering runs emitter.Close FIRST, then sess.Close — the explicit ordering AC requires.
9. Declare `var emitErr error` in the enclosing scope.
10. Construct the `tail.Watcher`:
    - `tail.New(tail.Config{Workdir: cfg.WorkDir, SessionID: cfg.SessionID, HomeDir: cfg.HomeDir, OnEvent: <closure>, OnEndOfTurn: <closure>, Logger: logger})`.
    - `OnEvent` closure: calls `emitter.Emit(ev)`. If the return is non-nil AND `emitErr == nil`, store it: `emitErr = err`. The emitter is sticky internally; subsequent Emit calls no-op, so the closure correctly captures only the first failure.
    - `OnEndOfTurn` closure: empty body. The emitter's `EndOfTurnSeen` state is set inside `Emit` when `ev.EndOfTurn == true`, so Close's default classification will produce `ExitReasonCompletion`. The callback exists only because `tail.Config.OnEndOfTurn` is required; no work is needed here.
    - Wrap any `tail.New` error as `fmt.Errorf("ptyrunner: tail: %w", err)`.
11. Call `runErr := watcher.Run(ctx)` (blocks until EOT or ctx-cancel).
12. **Return-value composition** (replaces the `return nil` at end of `Run`):
    - If `ctx.Err() != nil`, return `nil`. This collapses operator-shutdown for all of: watcher returning ctx.Err(), in-flight Emit failures during teardown, broken-pipe writes from the dispatcher closing its end. Mirrors streamrunner's `if ctx.Err() != nil { return nil }`.
    - Else if `emitErr != nil`, return `fmt.Errorf("ptyrunner: emit: %w", emitErr)`. Prioritized over a non-nil `runErr` because the emit failure is operator-actionable (broken pipe, full disk) and the watcher likely returned cleanly at EOT regardless.
    - Else if `runErr != nil`, return `fmt.Errorf("ptyrunner: tail: %w", runErr)`. Non-ctx tail errors are I/O failures from the watcher (file disappeared mid-drain, permission denied, etc.).
    - Else return `nil` (clean EOT cycle).

The validation-error variant for missing `Stdout` matches the existing pattern at lines 152-178: `errors.New("ptyrunner: Stdout required")`.

### Cleanup order — emitter.Close BEFORE sess.Close

This is the load-bearing correctness rule of the slice. Go defer is LIFO, so the runtime ordering is the reverse of the registration ordering. The implementation looks like:

```
sess := tuidriver.Spawn(…)              // step 4
defer sess.Close()                       // ❷ runs SECOND
…
emitter := streamjson.New(…)             // step 8
defer emitter.Close()                    // ❶ runs FIRST
…
watcher.Run(ctx)                         // step 11
return …                                 // step 12
```

When `Run` returns (any path: clean EOT, ctx-cancel, error), the deferred Closes fire in order:

1. `emitter.Close()` — writes the `result` trailer to `cfg.Stdout`. If EOT was observed during Emit, the trailer carries `subtype:"success"` / `terminal_reason:"completed"` / `is_error:false`. If EOT was NOT observed (ctx-cancel mid-stream, modal trip, prompt-write failure), the trailer carries `subtype:"error_during_execution"` / `terminal_reason:""` / `is_error:true`. This is the slice-relevant default classification per AC.
2. `sess.Close()` — SIGTERM → grace → SIGKILL the claude child. The trailer is already flushed by this point, so the dispatcher receives a complete stream even if the child takes the full grace window to exit.

Both Close calls have advisory return values:
- `emitter.Close()` returns an error if the trailer write to `cfg.Stdout` failed. The spec discards this error (no log, no surface). Rationale: the writer is the same one the per-event Emit calls have been writing to; if Emit was succeeding, Close's single ~300-byte write almost certainly succeeds; if Emit was failing, the dispatcher already sees a broken stream and there's nothing operator-actionable to add. (Optional refinement: log it at Warn level mirroring the `sess.Close` pattern. Not required.)
- `sess.Close()` keeps its existing Warn-level log on non-nil return.

### What does NOT change

- `buildArgs` is untouched. The interactive-TUI argv still omits `--input-format`, `--output-format`, `--verbose`, `--dangerously-skip-permissions`, `--max-turns`, `--allowed-tools`. The JSONL-tail design does not need any new claude flag.
- `isCtxErr` helper stays as-is. The new ctx-collapse at step 12 uses `ctx.Err() != nil` directly (no need to call `isCtxErr` — the watcher's returned error is structured and we check the context state directly).
- Modal / MCP / network detectors run BEFORE the emitter is constructed. A modal trip returns the sentinel error and the emitter never opens, so no trailer is written. The dispatcher sees `streamrunner`-shaped output only when the run actually reaches `WritePrompt`. (#470's cutover surfaces the sentinel errors with operator hints; that surface is unchanged.)

## Concurrency model

`Run` stays single-goroutine on the happy path. The internals invoked from `Run` may use goroutines (notably `tail.Watcher.Run`'s fsnotify channel select, and `tuidriver.Session`'s internal PTY pumps), but ptyrunner does NOT spawn a goroutine of its own in this slice.

- `tuidriver.Spawn` internally manages the PTY pump goroutines (mirrored writes from claude → `sess.Buffer` rolling snapshot; mirrored reads from `WritePrompt` calls → PTY master).
- `watcher.Run(ctx)` runs synchronously in `Run`'s caller goroutine. The `OnEvent` and `OnEndOfTurn` closures fire from the same goroutine — see `watcher.go` lines 33-35. This means the `emitErr` closure assignment does not need a mutex: only one writer, one reader (after `watcher.Run` returns).

The watcher's bounded existence-retry (`probeRetryDelays = [0, 50ms, 200ms]`) is what handles the race where claude's interactive-TUI has not yet created the JSONL file by the time `WritePrompt` returns. Do NOT layer another retry on top of it inside ptyrunner — the watcher's worst-case 250ms already mirrors `internal/sessions/rotation/watcher.go`.

## Error handling

Three error families post-slice. Each maps to a distinct wrapping:

| Family | Source | Return shape |
| --- | --- | --- |
| Validation | `cfg.Stdout == nil` | `errors.New("ptyrunner: Stdout required")` (plus existing nine variants) |
| Emitter setup | `streamjson.New` fail (nil writer or empty SessionID — neither reachable in practice post-validation, but defensive) | `fmt.Errorf("ptyrunner: emitter: %w", err)` |
| Tail setup | `tail.New` fail (empty workdir/sid, nil callbacks, fsnotify alloc failure) | `fmt.Errorf("ptyrunner: tail: %w", err)` |
| Tail runtime | `watcher.Run` returns non-ctx I/O error | `fmt.Errorf("ptyrunner: tail: %w", err)` |
| Emit | First non-nil `emitter.Emit` return captured in closure | `fmt.Errorf("ptyrunner: emit: %w", err)` |

Ctx-cancel collapses all post-spawn errors to `nil` (step 12). Modal / MCP / network detectors return their existing sentinel errors unchanged.

The emitter and watcher Close paths do not surface errors (advisory; the slice's correctness contract is "the trailer was attempted before the child was killed", not "the trailer reached the dispatcher").

## Testing strategy

Four new test cases plus one `TestRun_MissingRequiredFields` row.

### Helper extension (`helper_test.go` — new `jsonl` mode)

The helper needs to write a JSONL stream into the per-test fake home, but only after the parent has emitted `WritePrompt` (so the watcher's existence-wait actually exercises the CREATE path rather than the initial-stat path on every test — exercising both eventually adds coverage but isn't required by AC).

Sketch (~30 lines):

- Read `GO_PTYRUNNER_JSONL_PATH` (per-test path the test writer sets to the encoded JSONL path; empty for non-jsonl modes).
- Read `GO_PTYRUNNER_JSONL_BODY` env: a Go-source-escaped multi-line string the test supplies (lines joined with `\n`). Empty means "write nothing" (for ctx-cancel test scaffolding only — see below).
- After writing the idle glyph and starting the stdin-drain goroutine, wait for the first byte to arrive on stdin (the parent's `WritePrompt` bracketed-paste sequence starts with `\x1b[200~`). Block on a one-shot channel set from the drain goroutine.
- Once stdin yields a byte, the helper writes `GO_PTYRUNNER_JSONL_BODY` to `GO_PTYRUNNER_JSONL_PATH` (open with `O_CREATE|O_WRONLY|O_APPEND`, mode 0600), then `Sync()` and `Close()`. Lines are written in one shot — fsnotify CREATE + WRITE both fire and the watcher's drain loop consumes everything before EOT.
- Continue with the existing SIGTERM handler — the helper holds open until ptyrunner's `sess.Close()` SIGTERMs it.

For the **ctx-cancel-during-stream** test, the helper variant `jsonl_no_eot` writes a non-EOT-terminating body (e.g. one `tool_use` assistant entry), then idles. The test cancels ctx after observing the line in Stdout (via polling buffer state).

For the **emit-error-propagation** test, the helper writes a normal EOT body; the test wires a failing `io.Writer` as `cfg.Stdout` so Emit's first call returns the failure.

Both helper variants reuse the `jsonl` mode keyed off body content / second env var; no need for two distinct mode strings unless that's cleaner — architect's preference is one mode + body env var, but the developer may split into two mode strings for clarity.

### `helperRunCfg` extension

`helperRunCfg` gains a fifth argument (or grows a struct-config — developer's preference): the JSONL path that the helper will write to. The function computes it from `t.TempDir()` (the home dir) + `agentrun.EncodeProjectDir(workdir)` + `<sid>.jsonl`. Threaded via env (`GO_PTYRUNNER_JSONL_PATH`) AND set as `cfg.HomeDir` so the watcher and helper agree on the path.

For the existing four modal/banner/idle tests, the JSONL path env is left empty — those tests still pass because the helper never enters the "wait for stdin" branch (they SIGTERM-exit immediately on `sess.Close()` from the modal trip; `WritePrompt` is never called; the emitter is never constructed; the watcher is never started). Verify this is the case by re-reading the modal-trip paths — they `return ErrTrustModalDetected` etc. before reaching step 7.

**However**, two existing tests DO reach `WritePrompt`: `TestRun_HappyPath` and `TestRun_CtxCancelDuringSpawn`. After this slice:

- `TestRun_HappyPath` needs to be rewritten as `TestRun_HappyPath_EmitsAndEndOfTurn` (see § "New tests" #1). The bare "spawn → idle → WritePrompt → return" assertion no longer reflects the slice's behavior.
- `TestRun_CtxCancelDuringSpawn` exercises ctx-cancel BEFORE spawn completes (helper sleeps 5s before writing the idle glyph; test cancels at 100ms). The watcher is never started in that path. The test stays as-is, but a new `Stdout` field must be added to the helper config (a `bytes.Buffer{}` is fine — no events ever land).

### New tests

**1. `TestRun_HappyPath_EmitsAndEndOfTurn` (replaces existing `TestRun_HappyPath`)**

- Mode: `jsonl` with body =
  - `{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"test","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
  - One line is sufficient — the emitter aggregates state from a single assistant entry, EOT fires, watcher returns clean.
- Wire `cfg.Stdout = &bytes.Buffer{}`.
- After `Run` returns nil:
  - Buffer's first line equals the JSONL line verbatim + `\n` (verifies Raw-passthrough).
  - Buffer's second line parses as JSON and has `subtype == "success"`, `terminal_reason == "completed"`, `is_error == false`, `num_turns == 1`, `stop_reason == "end_turn"`, `session_id == cfg.SessionID`.
- Asserts elapsed `< 5s`.

**2. `TestRun_EmitterTrailerCarriesCompletion`**

This can fold into test #1 above as additional assertions (already covered by the trailer-parse step). Keeping it separate is also fine for readability. The architect's preference is to fold — one less helper test mode invocation cost — but the developer may split if the assertions get unwieldy.

**3. `TestRun_CtxCancelDuringStream`**

- Mode: `jsonl_no_eot` (or `jsonl` with a non-EOT body). Body = one `tool_use` assistant entry (no `end_turn`).
- Wire `cfg.Stdout = &bytes.Buffer{}`.
- Start `Run` in a goroutine; capture its returned error.
- Poll the Stdout buffer until the first JSONL line appears (proves the watcher has surfaced the event through Emit). Time out at 5s.
- Cancel the ctx.
- Assert `Run` returns `nil` within 8s (the SIGTERM-grace window).
- Assert the buffer's last line parses as JSON and has `subtype == "error_during_execution"`, `terminal_reason == ""`, `is_error == true`. (Confirms the default-classification path fires when EOT was not observed.)

**4. `TestRun_EmitErrorPropagation`**

- Mode: `jsonl` with the same EOT body as test #1.
- Wire `cfg.Stdout` as a custom `failingWriter` — a one-line type that returns `errors.New("simulated pipe broken")` from every `Write`. (Define inline at top of `runner_test.go` or in a new tiny helper file — developer's call. Keep it ~6 lines.)
- Assert `Run` returns a non-nil error AND `strings.Contains(err.Error(), "ptyrunner: emit:")` AND `strings.Contains(err.Error(), "simulated pipe broken")`.
- The watcher should still drain to EOT (Emit is sticky-error but the watcher proceeds), so `Run` returns the wrapped emit error rather than blocking. Time out at 5s as belt-and-suspenders.

**5. `TestRun_MissingRequiredFields` — add `Stdout` row**

- New row: `{"no Stdout", func(c *Config) { c.Stdout = nil }, "Stdout required"}`.
- Base config in the loop body gains a non-nil `Stdout: &bytes.Buffer{}` so existing rows still pass.

### Test-pattern notes

- Reuse `discardLogger`-style suppression for Logger in tests (avoid spew during `go test -v`). The watcher_test.go uses `slog.New(slog.NewTextHandler(io.Discard, nil))`; ptyrunner tests can inline the same.
- All new tests use `t.Parallel()` (matches existing pattern).
- `context.WithTimeout` budget: 10s (mirrors existing modal-detected tests). EOT happy path under 5s.
- The encoded-path computation in `helperRunCfg`:
  - `home := t.TempDir()`
  - `workdir := t.TempDir()`
  - `encoded, err := agentrun.EncodeProjectDir(workdir)` (ptyrunner tests already live in the same module; importing `internal/agentrun` for `EncodeProjectDir` is fine — that import was already there transitively via the watcher).
  - `path := filepath.Join(home, ".claude", "projects", encoded, sid+".jsonl")`
  - The watcher's `New` is responsible for `os.MkdirAll`; the helper just `OpenFile`s the path.

### What is NOT tested in this slice

- Real-claude byte-equivalence smoke test → follow-up slice (real-claude harness needs the budget package wired too).
- Large JSONL stream / multi-MB events → the existing `jsonl/tail/watcher_test.go::TestWatcher_FixtureIntegration` covers the watcher end of this on the 64-line `clean.jsonl`. ptyrunner doesn't need to re-verify.
- Concurrent dispatcher reads from `cfg.Stdout` → out of scope (the dispatcher contract is a single-reader pipe).

## Security-sensitive label check

Issue labels (from `gh issue view 478 --json labels`): `size:s`, `done:po`, `wip:architect`. **`security-sensitive` is NOT present.** Skip the security-review pass per the architect agent's CLAUDE.md.

## Open questions

- **Should `emitter.Close()`'s return value be logged at Warn?** Architect's call: skip the log to keep noise down. The trailer is best-effort; if Emit was already failing, the dispatcher already sees the broken stream. If the developer disagrees, adding `if err := emitter.Close(); err != nil { logger.Warn("ptyrunner: emitter close failed", "err", err) }` in the defer body is a one-line change with no downstream effect.
- **Where exactly to construct the emitter — before or after the watcher?** Spec puts the emitter construction first (step 8) so that `defer emitter.Close()` registers BEFORE `defer watcher cleanup` (which is handled inside `watcher.Run` itself, not by ptyrunner). Defer LIFO then runs emitter.Close FIRST, sess.Close SECOND, which is the AC ordering. If the developer finds a case where the emitter should construct AFTER the watcher (e.g. to avoid emitting on a failed tail setup), the trailer-on-tail-setup-failure case can be addressed by short-circuiting in step 10's error wrap: return before registering the emitter defer. Architect doesn't see the case; happy to discuss in review.
- **Should `OnEndOfTurn` do anything?** Spec says no — the emitter's own `EndOfTurnSeen` state is set inside `Emit`, so the trailer classification fires automatically on Close. The callback exists only because `tail.Config.OnEndOfTurn` is required (will fail validation if nil). Documented in the field comment of `tail.Config.OnEndOfTurn` already. The follow-up budget slice may wire OnEndOfTurn to signal an early-return path to bail out of a turn-count tally; out of scope here.

## Knowledge note

Write `docs/knowledge/codebase/478.md` mirroring `471.md`'s shape:

- One-paragraph header naming the sub-issue (#329 parent), the sibling tickets (#469, #475, #470, follow-up budget+watchdog), the slice's narrow scope (extends `Run` in-place, no new files, no new exports), and the dispatcher-contract continuity statement (zero changes downstream of `streamjson.Emitter`).
- **Implementation** section — bullet per substantive change. Cover:
  - The `runner.go` edits in plain prose (package doc narrows, `Stdout` becomes required, `HomeDir` added as test seam, `Run` extended after step 7).
  - Cleanup-order rationale: defer LIFO → emitter.Close before sess.Close. Why it matters: the trailer is the dispatcher's "this turn ended" signal; flushing it before SIGTERM means the dispatcher gets a complete record even if the child takes the full grace window.
  - Emit-error capture via closure + step-12 priority (ctx-cancel > emit-error > tail-error).
  - `OnEndOfTurn` is a no-op callback because the emitter's internal state owns the EOT classification.
  - The forbidden-imports narrowing — only `internal/supervisor` is forbidden now; subscription docs the `go list -deps` verification command in updated form.
  - The helper's new `jsonl` mode + the stdin-first-byte gate + env-threaded JSONL path.
- **Patterns established** section — name the patterns this slice generalizes for future agentrun slices:
  - "Wire a sibling-package side effect into a spawn primitive via defer-LIFO Close ordering" — pin this here so the follow-up budget slice doesn't reinvent it.
  - "Capture Emit-error in a closure when the producer's signature has no error return" — applies generally to any case where ptyrunner composes a write-side primitive with a watcher-side primitive whose callbacks have no error return.
- **Lessons learned** section — populate after implementation. Likely topics: the helper's stdin-gate timing (if it's brittle), the realpath-encoded path on darwin (already covered by watcher tests but may resurface in helper-mode tests), failing-writer test patterns.
- **Files** section — list the three modified files + the new knowledge doc.
- **Related** section — link to #471, #469, #475, the follow-up slice, #329, #470, codebase/471.md, codebase/390.md.

## Out of scope (per ticket body)

- Budget enforcement (`internal/agentrun/budget`) and watchdog goroutine (`tuidriver.NewTracker`) → follow-up slice on top of this one.
- `cmd/pyry/agent_run.go` cutover → [#470](https://github.com/pyrycode/pyrycode/issues/470), blocked on the follow-up slice + [#469](https://github.com/pyrycode/pyrycode/issues/469).
- `streamrunner` deletion → not in this migration phase.
- Real-claude byte-equivalence smoke test → follow-up slice.
