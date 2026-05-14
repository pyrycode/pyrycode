# `internal/agentrun/streamjson` — stdout emitter mirroring `claude -p` shape

Leaf package that turns the parsed `jsonl.Event` stream into the line-delimited stream-json shape the dispatcher already speaks. The dispatcher historically spawned `claude -p --output-format stream-json` and read its stdout; after the agent-run migration it spawns `pyry agent-run --output-format stream-json` and expects byte-equivalent output. `streamjson.Emitter` is the bridge: re-emits each `Event.Raw` verbatim, aggregates the per-event `Usage` blocks, and composes a single `type:"result"` trailer line when the run terminates.

## Public API

```go
type ExitReason string

const (
    ExitReasonCompletion ExitReason = "completion"
    ExitReasonMaxTurns   ExitReason = "max_turns"
    ExitReasonError      ExitReason = "error"
)

type Config struct {
    Writer    io.Writer        // required; production passes os.Stdout
    SessionID string           // required; UUIDv4 the caller minted and passed to claude
    Now       func() time.Time // optional; defaults to time.Now (test seam)
    Logger    *slog.Logger     // optional; defaults to slog.Default()
}

func New(cfg Config) (*Emitter, error)

// Emit re-emits ev.Raw + '\n', aggregating ev.Usage and counting assistant
// entries. Safe for concurrent use. Sticky write error: once a write fails,
// subsequent calls no-op (returning nil) so the watcher can drain to EOT or
// ctx cancel without thrashing a broken pipe. Emit after Close is a no-op.
func (e *Emitter) Emit(ev jsonl.Event) error

// SetExitReason overrides the default exit-reason classification used by
// Close. Pluggable seam for the future #334 budget integration (its
// Terminate hook calls SetExitReason(ExitReasonMaxTurns) before SIGTERM).
// Idempotent; the first non-empty value sticks. Safe for concurrent use.
func (e *Emitter) SetExitReason(r ExitReason)

// Close writes the final type:"result" trailer line. Exactly once after Emit
// has stopped firing; idempotent on second and later calls (returns the
// first call's error). Default classification when SetExitReason was not
// called: end-of-turn observed → completion; not observed → error.
func (e *Emitter) Close() error
```

## Wire shape (the trailer)

The trailer is the only line `streamjson` composes itself. Every other line is `Event.Raw + '\n'` byte-for-byte from the watcher's parser. Field-set was derived from captured `claude -p --output-format stream-json` `type:"result"` fixtures — pyry emits the subset the v1 dispatcher parser (`agent-dispatcher/src/dispatch.ts`) reads, plus the fields the v2 fixture-compare test asserts on. Anything outside this set is omitted; the dispatcher tolerates missing fields via `r.<field> || <default>`.

| Field | Type | Value |
|-------|------|-------|
| `type` | string | literal `"result"` |
| `subtype` | string | per termination table below |
| `is_error` | bool | per termination table below |
| `duration_ms` | int64 | `time.Since(start).Milliseconds()` |
| `num_turns` | int | count of `Kind == "assistant"` events seen by `Emit` |
| `result` | string | always `""` — pyry does not synthesise final assistant text; dispatcher logs assistant events independently |
| `stop_reason` | string | `StopReason` of the last assistant event (`""` if none) |
| `session_id` | string | the UUIDv4 minted by the caller and passed to claude via `--session-id` |
| `total_cost_usd` | float | always `0` — dispatcher owns the rate table; pyry emits raw counts only |
| `usage` | object | aggregated `input_tokens` / `output_tokens` / `cache_creation_input_tokens` / `cache_read_input_tokens` |
| `terminal_reason` | string | per termination table below |

Field order in the marshalled struct is fixed (pinned by `TestCapturedFixture_ByteEquivalence`) so the fixture-compare test diffs cleanly.

### Termination table

Internal `ExitReason` → wire fields:

| `ExitReason` | `subtype` | `terminal_reason` | `is_error` | Source |
|---|---|---|---|---|
| `completion` | `"success"` | `"completed"` | `false` | clean watcher exit (end-of-turn fired) |
| `max_turns` | `"error_max_turns"` | `"max_turns"` | `true` | future #334 budget Counter calls `SetExitReason(ExitReasonMaxTurns)` |
| `error` | `"error_during_execution"` | `""` | `true` | watcher I/O failure, child non-zero exit, or torn down before EOT |

Both `subtype` and `terminal_reason` are emitted: the v1 dispatcher reads `r.subtype` (log) and `r.terminal_reason` (stored on `StreamResult`); the v2 fixture-compare test asserts on `subtype`. Emitting both is the only choice that lets pyry slot in without dispatcher changes.

### Usage aggregation

For every `Event` with `Usage != nil` (i.e. assistant lines that carry a `message.usage` block), the four `int` fields are summed across the run. The trailer's `usage` object always emits all four keys with their integer totals — never `omitempty`. A missing key would be a different wire shape than zero.

Extra usage fields claude carries (`server_tool_use`, `service_tier`, `cache_creation`, `iterations`, `speed`, `inference_geo`) are NOT emitted; `jsonl.UsageBlock` (the producer side, #353) only mirrors the four ints. Expand that struct in a sibling ticket if a consumer needs the richer subset.

## Raw passthrough

- One `Event` → one `ev.Raw + '\n'` write. No re-encoding under any circumstance — that would break byte-equivalence with `claude -p`. `Event.Raw` already has the trailing `'\n'` stripped by the reader (`internal/agentrun/jsonl/reader.go`); the emitter re-appends it.
- All `Kind` values flow through, including `Kind == ""` (unrecognised types). The forward-compatibility story is identical to the reader's: new claude line types land in the `""` bucket and survive in `Raw` until the reader's whitelist widens.
- No newline insertion inside the emitted JSON object — `TestEmit_RawPassthrough_PreservesBytesVerbatim` pins this by feeding a non-canonical whitespace pattern (`{"type": "user",  "msg":"hi"}` with double space) and asserting it round-trips byte-for-byte.

## Concurrency model

One mutex, three call sites:

- `Emit` fires from the watcher goroutine (synchronously per parsed line).
- `SetExitReason` fires from the budget Counter's timer goroutine in #334's future integration.
- `Close` fires from the driver goroutine after both have stopped.

The mutex is held only for the duration of a single `Emit` write + state update or a single `Close` write + flag flip. No nested locks. `Emit` after `Close` no-ops (returns nil) so a spurious late event from a slow watcher unwind cannot panic.

## Error model

| Failure | Behaviour |
|---|---|
| `Writer.Write` fails (broken pipe) inside `Emit` | Sticky `writeErr`; subsequent `Emit` calls no-op; `Close` still attempts the trailer (best-effort) and returns its own error |
| `Close` called twice | Second and later calls return the first's error verbatim, do not re-write |
| `Emit` called after `Close` | No-op returning nil |
| `New(Config{Writer: nil})` / empty `SessionID` | Returns error; no Emitter constructed |

The emitter MUST NOT log Event content. The package doc-comment is load-bearing — logs only counts, durations, and error messages; never `Event.Raw` bytes nor per-event `Usage` values. Mirrors the `internal/agentrun/jsonl` invariant.

## Wire-up (`cmd/pyry/agent_run.go`)

The `runAgentRun` flow after #354:

1. Parse flags, resolve `home`, `MarkWorkdirTrusted`, `WriteSettings`, print `settings-file:` marker, `ReadFile(prompt)`.
2. **Mint a UUIDv4** via `newSessionUUID` (mirrors `internal/conversations/id.go:NewID`; this is a leaf call site, not extracted).
3. Construct `streamjson.Emitter{Writer: stdout, SessionID: <uuid>}`.
4. `ctx, cancel := signal.NotifyContext(SIGTERM, SIGINT)`. End-of-turn → `cancel`: this propagates EOT through Drive's ctx → claude SIGTERM → child exit → Drive returns nil → errgroup unblocks.
5. Construct `tail.Watcher` with `OnEvent: func(ev) { _ = emitter.Emit(ev) }`, `OnEndOfTurn: cancel`.
6. `errgroup.WithContext(ctx)` — `g.Go(watcher.Run)`, `g.Go(agentrun.Drive)`. Both share `gctx`; first error cancels both.
7. `runErr := g.Wait()` → `classifyForEmitter(em, runErr)` → `emitter.Close()` → return wrapped `runErr` (or nil on operator ctx-cancel).

`classifyForEmitter`: `nil` and `context.Canceled` are NOT overrides — the emitter's `Close` default handles the EOT-observed vs not-observed split. Any other error (`*exec.ExitError`, watcher I/O failure, etc.) calls `SetExitReason(ExitReasonError)`.

`buildClaudeArgs` grew by one pair: `--session-id <uuid>`. `TestBuildClaudeArgs_Shape` pins this alongside the two pre-existing security invariants (`--permission-mode default` MUST appear; `--allowedTools` MUST NOT appear).

`runAgentRun` now takes an explicit `io.Writer` for stdout (was `os.Stdout` global) so tests capture without redirection ceremony. `main.go` passes `os.Stdout`; tests pass `&bytes.Buffer{}`.

## Testing

`internal/agentrun/streamjson/emitter_test.go` covers:

- Raw-byte passthrough (assistant w/ usage, tool_use w/o usage, unrecognised `Kind == ""`, non-canonical whitespace).
- Token aggregation (sums across multiple assistants; nil-Usage on an assistant is permitted by #353 and must not crash).
- `num_turns` counts only assistant events; `stop_reason` reflects the last assistant; `""` when no assistant was seen.
- All three trailer compositions (completion / max_turns / error) + the no-EOT-no-override default-error fallback.
- `SetExitReason` and `Close` idempotence; `Emit` after `Close` no-ops; sticky write error.
- `cfg.Now` seam for `duration_ms`; `SessionID` round-trip; `total_cost_usd` and `result` constants.
- `TestCapturedFixture_ByteEquivalence` — replays `testdata/captured_run.jsonl` (the synthesised fixture matching captured `claude -p` shape) through `Emit`+`Close`; asserts non-result lines byte-equivalent, plus structural equality on `type`/`subtype`/`is_error`/`num_turns`/`stop_reason`/`terminal_reason` and on all four `usage` integer fields. Known-expected diff list documented inline (timestamps, session ids, claude-only fields like `modelUsage`/`permission_denials`/`uuid`/`api_error_status`/`duration_api_ms`, extra usage fields).

`cmd/pyry/agent_run_test.go` extends:

- `TestBuildClaudeArgs_Shape` — `--session-id <uuid>` pair appears, `<uuid>` matches the UUIDv4 shape.
- `TestRunAgentRun_DrivesFakeClaude` — fake-claude writes a canned `end_turn` JSONL line into `<home>/.claude/projects/<encoded(workdir)>/<sid>.jsonl`; pyry's stdout (captured into a `*bytes.Buffer`) contains the `settings-file:` marker, the assistant line byte-equivalent, and a `type:"result"` trailer with `subtype:"success"` as the last line.
- `TestNewSessionUUID_Shape` — pins the UUIDv4 format (8-4-4-4-12 hex, version 4 nibble, variant 10 nibble).

## Open questions / out of scope

- **Final assistant text in `result`.** Always `""`. v1 dispatcher reads `r.result || ""` so an empty value is fine. If a downstream consumer requires the final text (logs, retry classification), the emitter can grow a `lastAssistantText` field by re-parsing the last `Kind == "assistant"` `Event.Raw` on Close. Deferred until observed.
- **`duration_api_ms`.** Claude emits both wall and API-portion durations. Pyry has no clean way to measure the API portion separately. Omit; the dispatcher can derive if it starts caring.
- **Richer `usage` fields.** Pyry's `jsonl.UsageBlock` mirrors only the four ints. Expand at the producer (#353's struct) before the emitter, since the source data is what's missing.
- **#334 budget integration wire-up.** This ticket added the `SetExitReason(ExitReasonMaxTurns)` setter; the actual call site lives in the budget Counter's Terminate hook in a sibling integration ticket.

## Related

- [jsonl-reader.md](jsonl-reader.md) — `internal/agentrun/jsonl` (#348, #353). Producer side of the `Event` contract: `Event.Raw` / `Event.Kind` / `Event.Usage`.
- [jsonl-tail-watcher.md](jsonl-tail-watcher.md) — `internal/agentrun/jsonl/tail` (#349). The watcher whose `OnEvent` callback fires `Emit` and whose `OnEndOfTurn` fires the parent-ctx `cancel`.
- [pyry-agent-run-command.md](pyry-agent-run-command.md) — the verb that constructs and drives the Emitter.
- [budget-package.md](budget-package.md) — `internal/agentrun/budget` (#334). Future caller of `SetExitReason(ExitReasonMaxTurns)`.
