# Spec — agent-run: stream-json stdout emitter mirroring `claude -p` shape + result trailer (#354)

**Size:** S — ~180 net LOC production across 2 files. Edit fan-out: zero (greenfield package + one caller). Branch-overlap: clean.

**Security-sensitive:** no. Pyry is shaping its own stdout from data it parsed out of files in the operator's own home directory; the only adversarial surface is JSONL content, and the `Raw` byte-passthrough explicitly forbids re-encoding (so no injection seam at the JSON layer beyond what `internal/agentrun/jsonl` already trusts).

## Context

Today the dispatcher (`agent-dispatcher/src/dispatch.ts`) spawns `claude -p --output-format stream-json` and reads line-delimited JSON from claude's stdout, looking for a final `type: "result"` line as the trailer.

After the agent-run migration (parent #329), the dispatcher spawns `pyry agent-run --output-format stream-json` and expects the same shape on pyry's stdout. The producer side of this contract — the JSONL `Event` stream (with `Raw`, `Kind`, `Usage`) — is on `main` as of #353. The consumer side — the `tail.Watcher` driving a `jsonl.Reader` against `~/.claude/projects/<encoded>/<sid>.jsonl` — is on `main` as of #349. **Nothing wires them together yet:** `cmd/pyry/agent_run.go` currently spawns claude via `agentrun.Drive`, discards PTY stdout into `io.Discard`, and writes nothing to its own stdout beyond the `settings-file: <path>` marker emitted by #339.

This ticket lands the JSONL → stdout bridge: a new leaf package that re-emits each watcher `Event.Raw` verbatim and composes a single `type: "result"` trailer when the run terminates, plus the orchestration in `cmd/pyry/agent_run.go` that pre-mints a session UUID, passes it to claude via `--session-id <uuid>`, constructs the watcher, and runs the watcher and `Drive` under one `errgroup`.

The budget Counter from #334 is **not** wired here. The Counter's integration is a separate ticket; the emitter exposes a pluggable `SetExitReason` setter so that future integration can call `SetExitReason("max_turns")` from the Counter's `Terminate` hook without re-architecting this code.

## Files to read first

- `internal/agentrun/jsonl/reader.go:42-93` — `Event{StopReason, TextChars, EndOfTurn, Raw, Kind, Usage}` and `UsageBlock{InputTokens, OutputTokens, CacheCreationInputTokens, CacheReadInputTokens}` shapes. Kind whitelist: `"assistant"|"user"|"tool_use"|"tool_result"|"system"|"attachment"|""`.
- `internal/agentrun/jsonl/tail/watcher.go:33-65` — `tail.Config` callback contract (`OnEvent func(jsonl.Event)`, `OnEndOfTurn func()`); both fire from the `Run` goroutine.
- `internal/agentrun/jsonl/tail/watcher.go:140-203` — `Run` lifecycle: blocks until end-of-turn fires, ctx cancels, or an unrecoverable I/O error; returns nil after `OnEndOfTurn` is invoked.
- `cmd/pyry/agent_run.go:184-228` — current `runAgentRun` shape; the spawn entry-point this spec replaces.
- `cmd/pyry/agent_run.go:263-271` — current `buildClaudeArgs`; adds one `--session-id <uuid>` pair.
- `internal/agentrun/drive.go:50-107` — `Drive` lifecycle, in particular its contract: nil on operator-driven ctx cancel, `*exec.ExitError` on a child non-zero exit that was NOT ctx-driven.
- `internal/conversations/id.go:8-19` — `NewID()` pattern for UUIDv4-shaped IDs (`crypto/rand` + version/variant nibbles). Mirror this in the new emitter wire-up (or call it through; see § Design).
- `internal/agentrun/budget/budget.go` package doc — confirms `SetExitReason("max_turns")` is the planned plug point for the future Counter integration.
- Captured `claude -p --output-format stream-json` `type:"result"` trailers from a sibling dispatcher fixture (project-external; see § Trailer schema below for the embedded extracts) — derive the exact wire field-set.

## Trailer schema (derived from captured fixture)

Four captured `result` lines from a real `claude -p --output-format stream-json` run (kept verbatim for posterity; do not invent fields outside this set):

```text
// success
{"type":"result","subtype":"success","is_error":false,"api_error_status":null,
 "duration_ms":2358,"duration_api_ms":3565,"num_turns":1,"result":"PARSER",
 "stop_reason":"end_turn","session_id":"28b6666c-…","total_cost_usd":0.17967025,
 "usage":{"input_tokens":6,"cache_creation_input_tokens":28645,
          "cache_read_input_tokens":0,"output_tokens":8, …extra fields…},
 "modelUsage":{…},"permission_denials":[],"terminal_reason":"completed",
 "fast_mode_state":"off","uuid":"ce58a3c6-…"}

// max_turns
{"type":"result","subtype":"error_max_turns","duration_ms":3903,"is_error":true,
 "num_turns":2,"stop_reason":"tool_use","session_id":"58d209f8-…",
 "total_cost_usd":0.095…,"usage":{…},"terminal_reason":"max_turns",
 "permission_denials":[],"fast_mode_state":"off","uuid":"8928c763-…",
 "errors":["Reached maximum number of turns (1)"]}

// error_during_execution
{"type":"result","subtype":"error_during_execution","is_error":true,
 "duration_ms":1200,"num_turns":1,"result":"Internal execution error.",
 "session_id":"…","total_cost_usd":0.001234,"terminal_reason":"","uuid":"…"}
```

**Architect decision — field-set the emitter writes.** Pyry emits the subset the v1 dispatcher's parser (`agent-dispatcher/src/dispatch.ts:213-329`) reads off `result`, plus the fields needed by the v2 fixture-compare test. Fields NOT in this list are not written; the v1 dispatcher tolerates absent fields via `r.<field> || <default>`. Field order does not matter on the wire but is fixed below so the fixture test diffs cleanly.

| Field             | Type    | Value                                                                                                  |
|-------------------|---------|--------------------------------------------------------------------------------------------------------|
| `type`            | string  | literal `"result"`                                                                                     |
| `subtype`         | string  | per termination table below                                                                            |
| `is_error`        | bool    | per termination table below                                                                            |
| `duration_ms`     | int     | `time.Since(start).Milliseconds()` rounded to nearest int                                              |
| `num_turns`       | int     | count of `Kind=="assistant"` events seen by `Emit` (matches `jsonl.Reader.AssistantCount()` semantics) |
| `result`          | string  | always `""` — pyry does not synthesise the final assistant text; the dispatcher logs assistant events independently |
| `stop_reason`     | string  | `StopReason` of the last assistant `Event` seen (`""` if none)                                        |
| `session_id`      | string  | the UUIDv4 pyry minted and passed via `--session-id`                                                  |
| `total_cost_usd`  | number  | always `0` — per the ticket body, the dispatcher knows the rate table; pyry emits raw counts only    |
| `usage`           | object  | aggregated `UsageBlock` totals — see § Usage aggregation                                              |
| `terminal_reason` | string  | per termination table below                                                                            |

**Termination metadata table.** Both `subtype` and `terminal_reason` are present (matches every captured fixture). The internal `exit_reason` from the ticket body's source table maps to wire fields:

| Internal `exit_reason` | `subtype`                  | `terminal_reason` | `is_error` | Source                                       |
|------------------------|----------------------------|-------------------|------------|----------------------------------------------|
| `completion`           | `"success"`                | `"completed"`     | `false`    | deterministic end-of-turn (`Watcher` returns nil) |
| `max_turns`            | `"error_max_turns"`        | `"max_turns"`     | `true`     | future #334 integration calls `SetExitReason("max_turns")` |
| `error`                | `"error_during_execution"` | `""`              | `true`     | `Drive` returned a non-`ctx-cancel`-derived error (typically `*exec.ExitError`) |

Why **both** `subtype` and `terminal_reason`: the v1 dispatcher reads `r.subtype` (logged) and `r.terminal_reason` (stored on `StreamResult`); the v2 fixture-compare test (under `agent-dispatcher-v2/test/fixtures/claude-stream/`) asserts on `subtype`. Emitting both is the only choice that lets pyry slot in without dispatcher changes. The internal `exit_reason` is the abstraction the future #334 wiring sets; the wire-side translation is a constant table inside the emitter.

## Design

### Package layout

```
internal/agentrun/streamjson/
  emitter.go         ~120 lines — Emitter + Config + UsageTotals
  emitter_test.go    table-driven; see § Testing strategy
  testdata/
    captured_run.jsonl     fixture for the byte-equivalence test (developer captures; see § Testing)
```

`streamjson` is a sibling of `jsonl/` and `budget/` under `internal/agentrun/`. Dependency direction respected: imports `internal/agentrun/jsonl` (for `Event` / `UsageBlock`), nothing upward.

### Types

`Emitter` aggregates state across the run, re-emits each `Event.Raw` to its `Writer`, and composes the final trailer on `Close`. All exported methods are safe for concurrent use because `Emit` fires from the watcher goroutine while `SetExitReason` (planned) fires from the budget Counter's timer goroutine in the #334 follow-up; `Close` fires from the driver goroutine after both have stopped.

Signatures only — implementation details (mutex placement, helper extraction) are the developer's call.

```go
type Config struct {
    Writer    io.Writer   // required; production passes os.Stdout
    SessionID string      // required; the UUIDv4 minted by the caller and passed to claude
    Now       func() time.Time // optional; defaults to time.Now (test seam)
    Logger    *slog.Logger     // optional; defaults to slog.Default()
}

type Emitter struct { /* private */ }

func New(cfg Config) (*Emitter, error)

// Emit re-emits ev.Raw verbatim followed by '\n', aggregating ev.Usage and
// counting assistant entries. Safe for concurrent use. Returns the first
// write error encountered; once an error is returned, subsequent calls are
// no-ops (the writer is presumed dead).
func (e *Emitter) Emit(ev jsonl.Event) error

// SetExitReason overrides the default exit-reason classification used by
// Close. Pluggable seam for the future #334 budget integration:
//   counter := budget.New(budget.Config{Terminate: func() error {
//       emitter.SetExitReason(ExitReasonMaxTurns)
//       return cmd.Process.Signal(syscall.SIGTERM)
//   }, ...})
// Idempotent: only the first non-empty value sticks; later calls are no-ops.
// Safe for concurrent use.
func (e *Emitter) SetExitReason(r ExitReason)

// Close writes the final `type:"result"` trailer line and flushes. Idempotent
// — second and subsequent calls return the first call's error verbatim
// without re-writing. MUST be called exactly once after Emit calls have
// stopped; calling Close while Emit is still firing is a race.
//
// Default exit-reason classification when SetExitReason was not called:
//   - end-of-turn was observed during Emit  → ExitReasonCompletion
//   - end-of-turn was NOT observed          → ExitReasonError
// This matches the operator's expectations: a clean watcher exit (Run
// returned nil after OnEndOfTurn fired) is completion; any other tear-down
// (claude crashed, ctx cancelled before EOT, etc.) is error.
func (e *Emitter) Close() error

type ExitReason string

const (
    ExitReasonCompletion ExitReason = "completion"
    ExitReasonMaxTurns   ExitReason = "max_turns"
    ExitReasonError      ExitReason = "error"
)
```

### Internal state (developer's call on layout — sketch only)

The emitter must track, under one mutex:

- `numTurns int` — incremented when `Emit` sees `Kind == "assistant"`.
- `endOfTurnSeen bool` — set true when `Emit` sees `EndOfTurn == true`.
- `lastStopReason string` — overwritten on every `Kind == "assistant"` event.
- `aggUsage usageTotals` — `input_tokens` / `output_tokens` / `cache_creation_input_tokens` / `cache_read_input_tokens`; summed from each non-nil `Event.Usage`.
- `exitReason ExitReason` — defaults to `""`, may be set once by `SetExitReason`.
- `start time.Time` — captured at `New` time via `cfg.Now()`.
- `closed bool` + `closeErr error` — for `Close` idempotence.
- `writeErr error` — sticky write error; once set, `Emit` returns it without writing.

### Raw passthrough rules

- Per `Event`, write `ev.Raw` then `'\n'` in one buffered op (developer chooses whether to use a `bufio.Writer` or coalesce manually — for a per-line write of ≤80 KiB, an unbuffered `Writer.Write` of `append(ev.Raw, '\n')` into a freshly-allocated slice is acceptable).
- **No newline insertion inside the JSON object.** `Event.Raw` from `internal/agentrun/jsonl` is already a complete single line with the trailing `'\n'` stripped (`reader.go:188-197`); the emitter only re-appends the line terminator. No re-encoding under any circumstance — that would break byte-equivalence with `claude -p`.
- All `Kind` values flow through unchanged, including `Kind == ""` (unrecognised types).
- `Emit` returns the writer's first error verbatim wrapped as `fmt.Errorf("streamjson: emit: %w", err)`. The watcher's `OnEvent` callback does not propagate errors back, so the wrapper exists for trailer-side logging; the watcher will continue calling `Emit` even on error, but each subsequent call short-circuits via `writeErr`.

### Usage aggregation

For each `Event` where `Usage != nil` (i.e. assistant entries with a `usage` object), sum the four `int` fields into the running totals. The trailer's `usage` object is `{"input_tokens": <sum>, "output_tokens": <sum>, "cache_creation_input_tokens": <sum>, "cache_read_input_tokens": <sum>}`. Always emit all four keys with their integer totals, never `omitempty` — the dispatcher reads them by name and a missing key is a different shape than zero.

### Trailer composition

`Close` constructs the trailer object using `encoding/json` Marshal of a struct with explicit JSON tags matching the field table above. Resolve the internal `exitReason` to wire-side `subtype` / `terminal_reason` / `is_error` via the termination metadata table; if `exitReason == ""` at Close time, fall back to the end-of-turn-observed classification described in the `Close` doc comment.

The marshalled bytes are followed by `'\n'`. Stop after this line — there is no second trailer, no second newline, no final separator.

### Lifecycle (`cmd/pyry/agent_run.go`)

The `runAgentRun` function grows from `parse → trust → settings → fmt.Println → ReadFile → Drive` to:

1. Existing: parse args, resolve `home`, mark workdir trusted, write settings file, emit `settings-file: <path>`, read prompt bytes.
2. **NEW:** mint a UUIDv4-shaped session id via `crypto/rand` (mirror `internal/conversations/id.go:NewID`'s 7-line pattern; this is a leaf call site, no need to extract a shared helper).
3. **NEW:** construct `streamjson.Emitter` with `Writer: os.Stdout`, `SessionID: <uuid>`.
4. **NEW:** construct `tail.Watcher` with `Workdir: parsed.workdir`, `SessionID: <uuid>`, `HomeDir: home`, `OnEvent: func(ev) { _ = emitter.Emit(ev) }`, `OnEndOfTurn: cancel` (where `cancel` is the parent-ctx cancel func — described below).
5. **NEW:** `ctx, cancel := signal.NotifyContext(...)`; defer cancel. The watcher's `OnEndOfTurn` callback is exactly `cancel`. This propagates end-of-turn → Drive's ctx → claude SIGTERM → child exit → Drive returns nil → errgroup unblocks.
6. **NEW:** errgroup: `g.Go(watcher.Run)`, `g.Go(Drive)`. Both honour the same gctx (or both share the outer ctx — developer's call; pin one in the spec or live with either).
7. `err := g.Wait()`. Classify:
    - `err == nil` → leave `exitReason` unset (Close defaults to `completion` because end-of-turn was observed).
    - `errors.As(err, &*exec.ExitError)` → `emitter.SetExitReason(ExitReasonError)`.
    - `errors.Is(err, context.Canceled)` and end-of-turn was observed → leave unset (operator may have SIGINTed after EOT fired but before Drive teardown; treat as completion).
    - Any other error → `emitter.SetExitReason(ExitReasonError)`.
8. `if cerr := emitter.Close(); cerr != nil { logger.Warn(...) }` — Close's error is informational; the trailer either landed on stdout or it didn't, and there's nothing to do but log.
9. Return the `g.Wait` error (wrapped with the existing `"agent-run: drive: %w"` prefix for `*exec.ExitError`; nil otherwise).

### `buildClaudeArgs` change

Append `"--session-id", uuid` to the returned slice. One-line change. The existing `TestBuildClaudeArgs_Shape` argv-shape pin grows by one stanza; the two security invariants (`--permission-mode default` MUST appear, `--allowedTools` MUST NOT appear) are untouched.

### Wiring sketch (not implementation)

```go
// in runAgentRun, replacing the current Drive call:
sessionID, err := newSessionUUID()
if err != nil { return fmt.Errorf("agent-run: mint session id: %w", err) }

emitter, err := streamjson.New(streamjson.Config{
    Writer:    os.Stdout,
    SessionID: sessionID,
})
if err != nil { return fmt.Errorf("agent-run: stream emitter: %w", err) }

ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
defer cancel()

watcher, err := tail.New(tail.Config{
    Workdir:     parsed.workdir,
    SessionID:   sessionID,
    HomeDir:     home,
    OnEvent:     func(ev jsonl.Event) { _ = emitter.Emit(ev) },
    OnEndOfTurn: cancel,
})
if err != nil { return fmt.Errorf("agent-run: tail watcher: %w", err) }

g, gctx := errgroup.WithContext(ctx)
g.Go(func() error { return watcher.Run(gctx) })
g.Go(func() error {
    return agentrun.Drive(gctx, agentrun.DriveConfig{
        ClaudeBin:        claudeBin,
        WorkDir:          parsed.workdir,
        Args:             buildClaudeArgs(parsed, settingsPath, sessionID),
        PromptBytes:      promptBytes,
        TrustDialogDelay: parseDurationEnv("PYRY_AGENT_RUN_TRUST_DELAY"),
        PromptDelay:      parseDurationEnv("PYRY_AGENT_RUN_PROMPT_DELAY"),
    })
})
runErr := g.Wait()

// classify runErr into emitter exit reason, then Close
// (see step 7 above)
if cerr := emitter.Close(); cerr != nil { /* Warn-log */ }

return runErr  // wrap as before
```

Total `runAgentRun` growth: ~30 lines net over the current ~45-line body. `buildClaudeArgs`: +1 line.

## Concurrency model

- One watcher goroutine (calls `Emit` synchronously per parsed line; calls `cancel` on EOT).
- One Drive goroutine (PTY drain + script user-turn + Wait).
- Both run under `errgroup.WithContext(ctx)`. First error cancels both.
- The watcher's `OnEndOfTurn` fires from its own goroutine; it calls `cancel` which races with Drive's `cmd.Wait`. That's fine: Drive's `waitAndMap` masks ctx-cancel-driven exits to nil.
- After `g.Wait()` returns, both goroutines have stopped — no `Emit` calls are in flight. `Close` is safe to call from the driver goroutine without further synchronisation beyond the emitter's own mutex.
- The emitter's mutex is fine-grained: held for the duration of one `Emit` call (one stdout write + state update) or one `Close` call (one stdout write + idempotence flag flip). No nested locks.

## Error handling

| Failure mode                                  | Behaviour                                                                                                  |
|-----------------------------------------------|------------------------------------------------------------------------------------------------------------|
| `crypto/rand` fails during UUID mint          | `runAgentRun` returns `"agent-run: mint session id: %w"`; no trailer emitted (no Emitter was constructed)  |
| `tail.New` fails (e.g. bad workdir encode)    | `runAgentRun` returns the wrapped error; no trailer emitted                                                |
| `Emit`'s stdout write fails (e.g. broken pipe)| Sticky `writeErr`; subsequent `Emit` calls no-op; watcher keeps running until EOT or ctx cancel; `Close` still attempts to write the trailer (best-effort) and returns the error |
| `Watcher.Run` returns non-nil before EOT      | errgroup cancels gctx; Drive sees ctx-cancel and returns nil; `g.Wait` returns the watcher's error; emitter classifies as `ExitReasonError` |
| `Drive` returns `*exec.ExitError`             | errgroup cancels gctx; watcher returns ctx.Err; `g.Wait` returns the ExitError; emitter classifies as `ExitReasonError` |
| Operator SIGTERM/SIGINT before EOT            | `signal.NotifyContext` cancels ctx; both goroutines unwind; watcher returns ctx.Err, Drive returns nil; emitter classifies as `ExitReasonError` (no EOT observed) |
| Operator SIGTERM/SIGINT after EOT             | EOT already fired (`endOfTurnSeen=true`); errgroup unwinds with possibly ctx.Canceled; emitter defaults to `ExitReasonCompletion` per the EOT-observed branch |
| `Close` called twice                          | Second and later calls return the first's error verbatim, do not re-write the trailer                       |

The emitter MUST NOT log file contents at any layer (mirrors the `internal/agentrun/jsonl` invariant). It logs offsets, error messages, and aggregated counts — never `Event.Raw` byte content, never `Usage` field values from individual events (totals only).

## Testing strategy

### `internal/agentrun/streamjson/emitter_test.go`

Stdlib `testing`, table-driven where possible, `t.Parallel()` per test. No new dependencies.

1. **Raw-byte passthrough** — table over `Kind` values (`assistant` with `Usage`, `tool_use` without `Usage`, unrecognised `Kind == ""`). For each: construct an `Event` with a fixed `Raw` payload, call `Emit`, assert the buffer (`bytes.Buffer` as `Writer`) holds `Raw + '\n'` byte-for-byte. Especially: assert no re-encoding occurs by including a non-canonical whitespace pattern (`{"type": "user",  "msg":"hi"}` with double space) in `Raw` and asserting the double space survives.
2. **Token aggregation** — feed three assistant events with distinct `Usage` values plus interleaved non-assistant events (which carry `Usage == nil`); call `Close`; assert the trailer's `usage` object contains the four summed integers. Include one assistant event with `Usage == nil` (the spec for #353 explicitly allows this) and confirm it does not crash.
3. **`num_turns` counts assistant events** — interleave 5 assistant events, 3 user events, 2 tool_use events, 1 system event; assert trailer `num_turns == 5`.
4. **`stop_reason` reflects the last assistant event** — assistant events with `StopReason` values `["tool_use", "tool_use", "end_turn"]`; assert trailer `stop_reason == "end_turn"`.
5. **`stop_reason` is `""` when no assistant event was seen** — emit only `user` / `tool_use` events; assert `stop_reason == ""`.
6. **Trailer composition — completion** — emit events ending with an `EndOfTurn=true` assistant event, do not call `SetExitReason`, call `Close`; assert `subtype == "success"`, `terminal_reason == "completed"`, `is_error == false`.
7. **Trailer composition — max_turns** — emit events without EOT, call `SetExitReason(ExitReasonMaxTurns)`, call `Close`; assert `subtype == "error_max_turns"`, `terminal_reason == "max_turns"`, `is_error == true`.
8. **Trailer composition — error** — emit events without EOT, call `SetExitReason(ExitReasonError)`, call `Close`; assert `subtype == "error_during_execution"`, `terminal_reason == ""`, `is_error == true`.
9. **Trailer composition — default error fallback** — emit events without EOT, do NOT call `SetExitReason`, call `Close`; assert `subtype == "error_during_execution"` (the "no EOT observed and no override" branch).
10. **`SetExitReason` idempotent** — call twice with different values; only first sticks.
11. **`Close` idempotent** — call twice; second call returns the first call's error, does not write to the buffer a second time.
12. **`Emit` after `Close`** — call `Close`, then `Emit`; spec must define behaviour. Default: `Emit` no-ops and returns nil (the run is over; spurious late events from a slow watcher goroutine should not panic).
13. **Write error is sticky** — use a `failingWriter` that errors after the first byte; assert second `Emit` returns nil without writing.
14. **`Now` seam** — inject a `cfg.Now` that returns deterministic values 1s apart between New and Close; assert `duration_ms == 1000`.
15. **Session id round-trips** — `cfg.SessionID == "abc-…"` → trailer `session_id == "abc-…"`.
16. **`total_cost_usd` and `result` are always present and constant** — assert `total_cost_usd == 0` and `result == ""` in every trailer.
17. **Captured-fixture byte-equivalence** — load `testdata/captured_run.jsonl` (a recorded `claude -p` run, line-delimited JSONL where the last line is the `type:"result"` trailer); replay every non-result line through `Emit` and call `Close`; produce a byte buffer; diff against the original fixture byte-for-byte. **Known-expected diff list** (must be documented inline in the test, applied via field-by-field substitution before comparison): `usage` may contain extra fields (`server_tool_use`, `service_tier`, `cache_creation`, `inference_geo`, `iterations`, `speed`) which pyry omits; `modelUsage`, `permission_denials`, `fast_mode_state`, `uuid`, `errors`, `api_error_status`, `duration_api_ms` are claude-only and not emitted by pyry; `session_id` / `duration_ms` / `total_cost_usd` are intentionally different (we minted a different UUID, our timing differs, we emit 0 cost). The test asserts: (a) `type`, `subtype`, `is_error`, `num_turns`, `stop_reason`, `terminal_reason` match the fixture exactly; (b) `usage.input_tokens` / `output_tokens` / `cache_creation_input_tokens` / `cache_read_input_tokens` match the fixture's same four keys exactly; (c) every non-result line was re-emitted byte-equivalent to its fixture counterpart.

   **Capturing the fixture** is the developer's responsibility — there is no fixture in tree today. Run `claude -p --output-format stream-json --max-turns 2 --model claude-haiku-4-5 -p "list files in /tmp"` (or any deterministic short prompt) once on a dev machine, redirect stdout to `internal/agentrun/streamjson/testdata/captured_run.jsonl`, hand-redact any operator-identifying fields (cwd in `result`, hostnames in `session_id` references) if present. Commit the fixture. If `claude -p` is unavailable in the developer's environment (Phase D of the migration removes it), the developer may synthesise a hand-crafted fixture that matches the schemas listed in this spec; document inline that it is synthesised.

### `cmd/pyry/agent_run_test.go`

Extend existing tests. Stdlib only, hermetic via `t.Setenv("HOME", t.TempDir())` per the existing `newValidArgsFixture` pattern.

1. **`TestBuildClaudeArgs_Shape`** — extend to assert `--session-id <uuid>` appears as a pair, with `<uuid>` matching `internal/conversations/id.go:ValidID`'s shape. The existing assertions on `--permission-mode default` MUST appear / `--allowedTools` MUST NOT appear are unchanged.
2. **`TestRunAgentRun_DrivesFakeClaude` (extend)** — the existing fake-claude path uses `PYRY_CLAUDE_BIN` and timing knobs at ~50ms. Extend to also: capture stdout via a `*bytes.Buffer` swapped in for `os.Stdout`, write a tiny canned JSONL to `<home>/.claude/projects/<encoded(workdir)>/<sid>.jsonl` from the fake-claude binary itself (one `assistant` line with `stop_reason: "end_turn"` and one usage block, terminated by EOL), and assert that pyry's stdout contains (a) the existing `settings-file:` marker, (b) the assistant line byte-equivalent, and (c) one `type:"result"` trailer with `subtype:"success"` as the last line. This is the end-to-end smoke; deeper field-equivalence is covered by the emitter package's fixture test.

   `os.Stdout` is process-global, so the test pattern is to `t.Setenv` a path and pipe the binary's stdout through the test runner — or, more cleanly, refactor `runAgentRun` to take an explicit `io.Writer` parameter for stdout. **Architect's call: pass `os.Stdout` through a function parameter** so the test can capture without stdout-redirection ceremony. The `main.go` switch passes `os.Stdout` literally; tests pass `&bytes.Buffer{}`.

   (This refactor touches `cmd/pyry/agent_run.go` only; it does not propagate to `agentrun.Drive` or `streamjson` — both already take `io.Writer` via their own config. One signature change, one call-site update in `main.go`.)
3. **No new test for the errgroup wiring per se** — points 1 and 2 cover the surface. Race-detector cleanliness (`go test -race`) is enforced by the existing CI pipeline.

## Open questions

1. **Result text (`result` field).** The spec sets it to `""`. The v1 dispatcher reads `r.result || ""`, so an empty value is functionally equivalent to "no result text". If a downstream consumer turns out to require the final assistant text (logs, retry classification), the emitter can grow a `lastAssistantText string` field by re-parsing the last `Kind=="assistant"` `Event.Raw` on `Close`. Defer until observed.
2. **`duration_api_ms` field.** Claude emits both `duration_ms` (wall) and `duration_api_ms` (network). Pyry has no clean way to measure the API portion separately. Omit. If the dispatcher starts caring, derive at the dispatcher.
3. **Extra usage fields (`server_tool_use`, `service_tier`, `cache_creation`, `iterations`).** Claude's `usage` object is richer than the four-int subset pyry emits. Pyry has no source for these from `jsonl.UsageBlock` (which mirrors only the four ints). Document the omission in the captured-fixture test's known-diff list; expand `UsageBlock` in a sibling ticket if a consumer needs them.
4. **What if `tail.New` fails because the encoded project dir cannot be created?** Today the error propagates up and `runAgentRun` returns it; no `type:"result"` line is emitted. The dispatcher's `child.on("close", ...)` path raises `"no result message received"`. That is consistent with how the dispatcher already handles a claude binary that fails to start. No new behaviour required.
5. **Session-id format.** Claude accepts UUIDv4 for `--session-id`. The minted id mirrors `internal/conversations/id.go:NewID`'s shape exactly. No new exported helper; this is one call site. If a second caller needs the same pattern, extract then.

## Split proposal — N/A

Production source file count = 2 (`internal/agentrun/streamjson/emitter.go` + `cmd/pyry/agent_run.go`). Net production LOC ≈ 180. Edit fan-out: zero (greenfield + one caller). Acceptance-criteria count = 5. All under the red lines. Spec ships as one ticket.
