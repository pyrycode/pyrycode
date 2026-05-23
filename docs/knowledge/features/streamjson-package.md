# `internal/agentrun/streamjson` — stdout emitter mirroring `claude -p` shape

Leaf package that turns the parsed `tuidriver.JSONLEntry` stream into the line-delimited stream-json shape the dispatcher already speaks. The dispatcher historically spawned `claude -p --output-format stream-json` and read its stdout; after the agent-run migration it spawns `pyry agent-run --output-format stream-json` and expects byte-equivalent output. `streamjson.Emitter` is the bridge: composes the leading `type:"system" subtype:"init"` envelope on construction (#498), re-emits each `entry.RawLine` verbatim, aggregates the per-entry `usage` block via the private `readUsage` walk, and composes a single `type:"result"` trailer line when the run terminates.

After #511 `Emit` consumes `github.com/pyrycode/tui-driver/pkg/tuidriver.JSONLEntry` directly; the watcher's `OnEvent func(jsonl.Event)` Config field is still alive (until #512 pivots it onto `tuidriver.TailJSONL`), so `ptyrunner/runner.go` carries a throwaway `eventToEntry` adapter at the closure call site.

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
    Cwd       string           // required (#498); stamped into init.cwd
    Tools     []string         // required (#498), non-nil; empty slice OK; stamped into init.tools
    Model     string           // required (#498); stamped into init.model
    Now       func() time.Time // optional; defaults to time.Now (test seam)
    Logger    *slog.Logger     // optional; defaults to slog.Default()
}

// New constructs an Emitter and writes the leading
// {"type":"system","subtype":"init",...} envelope to cfg.Writer before
// returning. Returns (nil, err) if Writer is nil, SessionID is empty, Cwd
// is empty, Tools is nil, Model is empty, or the init write fails (#498).
func New(cfg Config) (*Emitter, error)

// Emit re-emits entry.RawLine + '\n', aggregating per-entry usage (private
// readUsage walks Message.Raw["usage"]) and counting assistant entries
// (entry.Type == "assistant"). End-of-turn is read via tuidriver.IsEndTurn.
// Safe for concurrent use. Sticky write error: once a write fails, subsequent
// calls no-op (returning nil) so the watcher can drain to EOT or ctx cancel
// without thrashing a broken pipe. Emit after Close is a no-op.
func (e *Emitter) Emit(entry tuidriver.JSONLEntry) error

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

## Wire shape (the init envelope)

`New` composes the leading `system/init` line synchronously before returning, sourcing all fields from `Config`. This restores the [[Drop-In Contract]] for ptyrunner's stream-json output — `parseInitSessionID` (`internal/e2e/realclaude/fixtures.go`) and any dispatcher parser keyed on the leading envelope now work against ptyrunner without runner-specific branching. The wire shape is fixture-pinned (`testdata/captured_run.jsonl:1`):

| Field | Type | Value |
|-------|------|-------|
| `type` | string | literal `"system"` |
| `subtype` | string | literal `"init"` |
| `cwd` | string | `Config.Cwd` (in ptyrunner: `cfg.WorkDir`) |
| `tools` | []string | `Config.Tools` (in ptyrunner: `cfg.AllowedTools`) — non-nil; empty slice marshals as `[]`, never `null` |
| `model` | string | `Config.Model` (in ptyrunner: `cfg.Model`) |
| `session_id` | string | `Config.SessionID` |

Key order in the marshalled struct is fixed (pinned by `TestNew_InitLineKeyOrderMatchesFixture` byte-comparing against the captured fixture). The unexported `initLine` struct (`emitter.go:296-309`) declares the six fields in this order; reordering breaks byte-equivalence even though the resulting JSON stays semantically valid. Same convention `trailer` uses.

The synthesis lives in `streamjson`, not `ptyrunner`, because the package already owns the wire shape for the trailer; siblings of one wire contract belong in one place. The init write happens synchronously inside `New` before any other goroutine can observe the Emitter, so the leading-line invariant is structural (no mu, no race).

Fields deliberately NOT in the envelope: `claude_code_version` (would require a cache-it-once `claude --version` shell-out), `permissionMode` (mirrors `--permission-mode default`), `apiKeySource` (`"none"` when token-via-env), `mcp_servers`. These are optional-if-cheap per the #498 spec; none are gating. The fixture is the source of truth for the required set, not streamrunner's live output.

On init-write failure, `New` returns `(nil, fmt.Errorf("streamjson: emit init: %w", err))` — the Emitter is unusable when its first write fails, so the caller never sees a half-constructed emitter (`TestNew_InitWriteFailureReturnsError`).

## Wire shape (the trailer)

The trailer is the second line `streamjson` composes itself. Between init and trailer, every line is `Event.Raw + '\n'` byte-for-byte from the watcher's parser. Field-set was derived from captured `claude -p --output-format stream-json` `type:"result"` fixtures — pyry emits the subset the v1 dispatcher parser (`agent-dispatcher/src/dispatch.ts`) reads, plus the fields the v2 fixture-compare test asserts on. Anything outside this set is omitted; the dispatcher tolerates missing fields via `r.<field> || <default>`.

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

For every `JSONLEntry` whose `Message.Raw["usage"]` is a `map[string]any` (i.e. assistant lines that carry a `message.usage` block), the private `readUsage` helper extracts the four counter fields (`input_tokens`, `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`) and `Emit` sums them into the run-wide totals. The trailer's `usage` object always emits all four keys with their integer totals — never `omitempty`. A missing key would be a different wire shape than zero.

`readUsage` returns `(usageBlock, false)` (and the aggregator skips the entry) when the entry has no `Message`, no `Message.Raw`, no `"usage"` key, or a non-map value at `"usage"` — same observable behaviour as the pre-#511 `ev.Usage == nil` gate. `encoding/json` decodes JSON numbers into `map[string]any` as `float64`, not `int` — the helper type-asserts to `float64` then truncates; asserting straight to `int` would silently zero every entry (the pyrycode-side bug class #511's "Patterns established" calls out explicitly).

Extra usage fields claude carries (`server_tool_use`, `service_tier`, `cache_creation`, `iterations`, `speed`, `inference_geo`) are NOT emitted. A future `tuidriver.Usage(entry)` upstream helper would let consumers share one implementation; for now `readUsage` lives in `streamjson`.

## Raw passthrough

- One `JSONLEntry` → one `entry.RawLine + '\n'` write. **Re-marshalling from `entry.Raw` (the `map[string]any`) is explicitly disallowed** — it would normalise key order and whitespace, breaking byte-equivalence with `claude -p`. `RawLine`'s tuidriver contract: "byte-identical to what parseEntry consumed, with the trailing `\r\n` or `\n` stripped"; the emitter re-appends `'\n'`.
- All `entry.Type` values flow through, including `Type == ""` (unrecognised envelopes). The forward-compatibility story is identical to the underlying reader's: new claude line types land in the `""` bucket and survive in `RawLine` until tuidriver's whitelist widens.
- No newline insertion inside the emitted JSON object — `TestEmit_RawPassthrough_PreservesBytesVerbatim` pins this by feeding a non-canonical whitespace pattern (`{"type": "user",  "msg":"hi"}` with double space) and asserting it round-trips byte-for-byte.

## Concurrency model

One mutex, three call sites:

- `Emit` fires from the watcher goroutine (synchronously per parsed line).
- `SetExitReason` fires from the budget Counter's timer goroutine in #334's future integration.
- `Close` fires from the driver goroutine after both have stopped.

The mutex is held only for the duration of a single `Emit` write + state update or a single `Close` write + flag flip. No nested locks. `Emit` after `Close` no-ops (returns nil) so a spurious late entry from a slow watcher unwind cannot panic. `readUsage` is a pure function with no shared state.

## Error model

| Failure | Behaviour |
|---|---|
| `Writer.Write` fails (broken pipe) inside `Emit` | Sticky `writeErr`; subsequent `Emit` calls no-op; `Close` still attempts the trailer (best-effort) and returns its own error |
| `Close` called twice | Second and later calls return the first's error verbatim, do not re-write |
| `Emit` called after `Close` | No-op returning nil |
| `New(Config{Writer: nil})` / empty `SessionID` / empty `Cwd` / nil `Tools` / empty `Model` | Returns `(nil, err)`; no Emitter constructed |
| `Writer.Write` fails during the init synthesis inside `New` | Returns `(nil, fmt.Errorf("streamjson: emit init: %w", err))`; no Emitter constructed (pinned by `TestNew_InitWriteFailureReturnsError`) |

The emitter MUST NOT log entry content. The package doc-comment is load-bearing — logs only counts, durations, and error messages; never `entry.RawLine` bytes nor per-entry usage values. Mirrors the upstream tuidriver invariant.

## Wire-up (`cmd/pyry/agent_run.go`)

The `runAgentRun` flow after #354:

1. Parse flags, resolve `home`, `MarkWorkdirTrusted`, `WriteSettings`, print `settings-file:` marker, `ReadFile(prompt)`.
2. **Mint a UUIDv4** via `newSessionUUID` (mirrors `internal/conversations/id.go:NewID`; this is a leaf call site, not extracted).
3. Construct `streamjson.Emitter{Writer: stdout, SessionID: <uuid>}`.
4. `ctx, cancel := signal.NotifyContext(SIGTERM, SIGINT)`. End-of-turn → `cancel`: this propagates EOT through Drive's ctx → claude SIGTERM → child exit → Drive returns nil → errgroup unblocks.
5. Construct `tail.Watcher` with `OnEvent: func(ev) { _ = emitter.Emit(eventToEntry(ev)) }` (after #511; `eventToEntry` is a throwaway adapter in `runner.go` that wraps `jsonl.Event` into `tuidriver.JSONLEntry` until #512 pivots the watcher to `tuidriver.TailJSONL`), `OnEndOfTurn: cancel`.
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
- `TestNew_ValidatesConfig` — table-driven over the five required-field cases (nil Writer, empty SessionID, empty Cwd, nil Tools, empty Model) (#498).
- `TestNew_WritesInitLineFirst` — first newline-terminated line decodes to `initLine{Type:"system",Subtype:"init",...}` with field values matching `Config` inputs (#498).
- `TestNew_InitLineKeyOrderMatchesFixture` — byte-compares the synthesised init against `testdata/captured_run.jsonl:1`. Pins the `initLine` struct's tag declaration order; a future reorder fails locally instead of only under `make e2e-realclaude` (#498).
- `TestNew_EmptyToolsMarshalsAsEmptyArray` — `Tools: []string{}` produces `"tools":[]` (never `"tools":null`); guards against a future nil-slice regression (#498).
- `TestNew_InitWriteFailureReturnsError` — `failingWriter` rejects the first write; `New` returns `(nil, err)` with `errors.Is(err, w.err)` and `"streamjson: emit init"` substring (#498).
- `TestCapturedFixture_ByteEquivalence` — replays `testdata/captured_run.jsonl` (the synthesised fixture matching captured `claude -p` shape) through `Emit`+`Close`; asserts non-result lines byte-equivalent (including the init at `out[0]`, which the producer-side synthesis emits from `Config`), plus structural equality on `type`/`subtype`/`is_error`/`num_turns`/`stop_reason`/`terminal_reason` and on all four `usage` integer fields. After #498 the fixture's first line is fed via `Config.{Cwd,Tools,Model,SessionID}` (not through `Emit`) — feeding it through `Emit` would duplicate it; the test loop iterates `nonResult[1:]`. After #511 the loop drops `jsonl.NewReader` and uses a local `lineToEntry` helper that mirrors tuidriver's package-internal `parseEntry`/`parseMessage` (~25 LOC of duplication accepted with a one-line comment pointing at the future `tuidriver.ParseEntry` export). Known-expected diff list documented inline (timestamps, session ids, claude-only fields like `modelUsage`/`permission_denials`/`uuid`/`api_error_status`/`duration_api_ms`, extra usage fields).
- `TestReadUsage_NilMessage` / `_NoRawMap` / `_NoUsageKey` / `_NonMapUsage` (#511) — pin the four absent paths of the private `readUsage` helper: each emits one assistant entry with the matrix and asserts `tr.NumTurns == 1` (state machine still updates) plus all four usage totals zero. `_NonMapUsage` is the most defensive — it asserts the `.(map[string]any)` assertion doesn't panic on a string value.

`cmd/pyry/agent_run_test.go` extends:

- `TestBuildClaudeArgs_Shape` — `--session-id <uuid>` pair appears, `<uuid>` matches the UUIDv4 shape.
- `TestRunAgentRun_DrivesFakeClaude` — fake-claude writes a canned `end_turn` JSONL line into `<home>/.claude/projects/<encoded(workdir)>/<sid>.jsonl`; pyry's stdout (captured into a `*bytes.Buffer`) contains the `settings-file:` marker, the assistant line byte-equivalent, and a `type:"result"` trailer with `subtype:"success"` as the last line.
- `TestNewSessionUUID_Shape` — pins the UUIDv4 format (8-4-4-4-12 hex, version 4 nibble, variant 10 nibble).

## Open questions / out of scope

- **Final assistant text in `result`.** Always `""`. v1 dispatcher reads `r.result || ""` so an empty value is fine. If a downstream consumer requires the final text (logs, retry classification), the emitter can grow a `lastAssistantText` field by re-parsing the last `entry.Type == "assistant"` `RawLine` on Close. Deferred until observed.
- **`duration_api_ms`.** Claude emits both wall and API-portion durations. Pyry has no clean way to measure the API portion separately. Omit; the dispatcher can derive if it starts caring.
- **`tuidriver.Usage(entry)` upstream accessor.** `readUsage` lives in `streamjson` as of #511 because it's the only consumer of `Message.Raw["usage"]` so far. If a second consumer surfaces (likely `budget.Counter` after #512), promote the helper into tui-driver so both share one implementation.
- **#334 budget integration wire-up.** The `SetExitReason(ExitReasonMaxTurns)` setter is the seam; the actual call site lives in the budget Counter's Terminate hook in a sibling integration ticket.

## Related

- [jsonl-reader.md](jsonl-reader.md) — `internal/agentrun/jsonl` (#348, #353). Producer-side `Event` contract. Will be deleted by #512; `streamjson` no longer consumes it after #511.
- [jsonl-tail-watcher.md](jsonl-tail-watcher.md) — `internal/agentrun/jsonl/tail` (#349, #501, #509). The watcher whose `OnEvent` callback drives `Emit`; the `OnEvent func(jsonl.Event)` signature is preserved through #511 and migrates to `tuidriver.JSONLEntry` under #512 alongside the watcher's pivot to `tuidriver.TailJSONL`.
- [pyry-agent-run-command.md](pyry-agent-run-command.md) — the verb that constructs and drives the Emitter.
- [budget-package.md](budget-package.md) — `internal/agentrun/budget` (#334). Future caller of `SetExitReason(ExitReasonMaxTurns)`. Still on `jsonl.Event` through #511; #512 migrates it.
- [#511](../codebase/511.md) — `streamjson.Emitter` migration from `jsonl.Event` to `tuidriver.JSONLEntry` and the throwaway `eventToEntry` adapter in `ptyrunner/runner.go`.
