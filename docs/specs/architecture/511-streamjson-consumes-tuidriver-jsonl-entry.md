# Spec — #511: `streamjson.Emitter` consumes `tuidriver.JSONLEntry`

Confirms PO's size: **S**. Two production files (`emitter.go` + a ~10-LOC adapter in `runner.go`), one test file (`emitter_test.go`), no dependency bump (`tuidriver.JSONLEntry` + `IsEndTurn` shipped before #509's `go get`).

## Files to read first

- `internal/agentrun/streamjson/emitter.go` (334 LOC, full file).
  - `Emit(ev jsonl.Event) error` at lines 171–202 — the only function whose signature changes.
  - `usageTotals` at lines 98–103 — internal accumulator, **stays unchanged**.
  - State-update block at lines 181–193 — five field reads that must be remapped to `tuidriver.JSONLEntry` accessors.
  - Byte-passthrough at lines 195–196 — `append([]byte(nil), ev.Raw...) + '\n'`. Shape stays; source field changes to `entry.RawLine`.
  - Doc comment at lines 161–170 — needs one-line update (s/`jsonl.Event`/`tuidriver.JSONLEntry`/) plus a sentence pinning the byte-passthrough source as `RawLine` (not `Raw`).
- `internal/agentrun/streamjson/emitter_test.go` (729 LOC, full file). Every `jsonl.Event{...}` and `jsonl.UsageBlock{...}` literal becomes a `tuidriver.JSONLEntry{...}` literal. Hot spots:
  - `TestEmit_RawPassthrough_PreservesBytesVerbatim` (lines 80–125) — three table rows, each constructs `jsonl.Event{Raw: ...}`. Rewrite to `tuidriver.JSONLEntry{RawLine: ...}`.
  - `TestEmit_AggregatesUsage` (lines 127–176) — five-entry table, two carry `Usage`. Rewrite. **The usage entries must carry a `Message.Raw["usage"]` map** since that's where the new code reads it from.
  - `TestEmit_NumTurnsCountsAssistantEvents` (lines 178–199) — eleven `jsonl.Event{Kind: k, Raw: ...}` literals. Rewrite (`Kind` → `Type`).
  - `TestEmit_LastStopReasonWins` (lines 201–220) and similar small tests — each `jsonl.Event{Kind, StopReason, EndOfTurn, Raw}` literal becomes `tuidriver.JSONLEntry{Type, Message: &tuidriver.EntryMessage{StopReason: ...}, RawLine: ...}`.
  - `TestCapturedFixture_ByteEquivalence` (lines 530–613) — currently drives the fixture through `jsonl.NewReader`. Must stop importing `jsonl`. Replacement strategy in § Testing.
- `internal/agentrun/ptyrunner/runner.go` lines 360–399 — the watcher → emitter wiring. The `OnEvent` closure at 365–370 grows the adapter. The `watcher.OnEvent func(jsonl.Event)` Config field signature **does not change** (per AC #6 path (a)).
- `internal/agentrun/jsonl/reader.go` lines 144–158 (`rawAssistantMessage` decode) — reference shape for the usage walk. The new `streamjson` helper does the equivalent extraction but starting from `entry.Message.Raw["usage"]` (a `map[string]any` produced by tuidriver's `parseMessage`) rather than from raw JSON bytes. Type switch is `float64 → int`, not `int`.
- `internal/agentrun/streamjson/testdata/captured_run.jsonl` — fixture. Used by `TestCapturedFixture_ByteEquivalence` and `TestNew_InitLineKeyOrderMatchesFixture`. **Stays untouched.**
- tuidriver module cache (`$GOMODCACHE/github.com/pyrycode/tui-driver@v0.0.0-20260523181457-c2dcd1e49992/pkg/tuidriver/jsonl.go`):
  - `JSONLEntry` (lines 108–113) — four fields: `Type string`, `Message *EntryMessage`, `Raw map[string]any`, `RawLine []byte`. `RawLine` contract: "byte-identical to what parseEntry consumed, with the trailing `\r\n` or `\n` stripped" (lines 96–101). Same shape as `jsonl.Event.Raw` (`reader.go:66-71`).
  - `EntryMessage` (lines 120–125) — `ID`, `StopReason`, `Content []ContentBlock`, `Raw map[string]any`. The library guarantees `Raw` is populated for every parsed message envelope.
  - `IsEndTurn(e JSONLEntry) bool` (lines 297–305) — assistant ∧ `StopReason == "end_turn"` ∧ `AssistantText(e) != ""`. Verified semantically equivalent to `jsonl.Event.EndOfTurn` (`reader.go:62-64`).
  - `parseMessage` (lines 251–266) — confirms `Message.Raw` is the verbatim `message` map decoded with `encoding/json`, so numeric fields surface as `float64`.

## Context

After `tui-driver` #101 (CLOSED, vendored), the per-entry parser the watcher consumes from is owned by the tui-driver library. The downstream stream-json emitter is the last consumer in the runtime hot-path still typed on the local `jsonl.Event` shape. Migrating it to `tuidriver.JSONLEntry` collapses one of the two seams in this slice (the other — replacing the watcher's `jsonl.NewReader` with `tuidriver.TailJSONL` — is #512's job).

Four of the five reads in `Emit` have a direct accessor on `JSONLEntry`; the fifth (Usage) requires a ~15-LOC map-walk that mirrors the existing `rawAssistantMessage` decode and stays private to `streamjson`. A future `tuidriver.Usage()` helper is a tui-driver follow-up — not gating here.

Path (a) — **adapter at the runner.go call site, watcher stays on `jsonl.Event`** — is the design choice. Rationale:

- `internal/agentrun/jsonl/tail/watcher.go`, `internal/agentrun/jsonl/reader.go`, `internal/agentrun/budget/budget.go`, `internal/agentrun/selfcheck/`, and `internal/e2e/realclaude/fixtures.go` all stay byte-stable in this slice — they're #512's surface.
- The adapter (~10 LOC inside the `OnEvent` closure) is throwaway code that #512 deletes when the watcher pivots to `tuidriver.TailJSONL`. Local, single call site, zero blast radius.
- Path (b) would force `budget.Counter` to migrate in the same ticket (AC #6 says so), which fans this out across `budget.go`, `budget_test.go`, and the matching watcher_test.go fixtures — closer to ~600 LOC and three packages. Out of bounds for S.

## Design

### Imports

`emitter.go`: **add** `github.com/pyrycode/tui-driver/pkg/tuidriver`. **Drop** `github.com/pyrycode/pyrycode/internal/agentrun/jsonl`. All other imports unchanged.

`emitter_test.go`: **add** `github.com/pyrycode/tui-driver/pkg/tuidriver`. **Drop** `github.com/pyrycode/pyrycode/internal/agentrun/jsonl`.

`runner.go`: no import changes. `jsonl` and `tuidriver` are both already imported; `tuidriver` because of `Spawn`/`EnsureClaudeEnv`/`SessionJSONLPath`, `jsonl` because of the watcher Config type.

### `emitter.go` — signature and reads

Signature:

```go
func (e *Emitter) Emit(entry tuidriver.JSONLEntry) error
```

The function body keeps its shape exactly. The five `ev.X` reads remap as follows (all reads happen under `e.mu`; no concurrency change):

| Old (`jsonl.Event`) | New (`tuidriver.JSONLEntry`) | Notes |
|---|---|---|
| `ev.Kind == "assistant"` | `entry.Type == "assistant"` | `Type` carries the verbatim envelope kind; `jsonl.Event.Kind` was whitelisted to a known set, but the only kind `Emit` branches on is `"assistant"`, so the whitelisting is irrelevant here. |
| `ev.StopReason` | `entry.Message.StopReason` | Read **inside** the `Type == "assistant"` branch so we know `Message != nil` (tuidriver populates `Message` iff the envelope carried a `message` object — true for every assistant entry). Defensive nil-check: if `entry.Message == nil` on an assistant entry (malformed envelope), treat `StopReason` as `""` (same as today's empty-message branch). One inline `if entry.Message != nil { e.lastStopReason = entry.Message.StopReason }` suffices. |
| `ev.EndOfTurn` | `tuidriver.IsEndTurn(entry)` | Function call, not a field. Computes assistant ∧ `Message.StopReason == "end_turn"` ∧ non-empty text — semantically equivalent to today's `EndOfTurn` field. |
| `ev.Usage != nil { e.aggUsage.* += ... }` | `if u, ok := readUsage(entry); ok { e.aggUsage.* += u.* }` | New private function. See § Usage walk. |
| `ev.Raw` (line 195) | `entry.RawLine` | Byte-for-byte equivalent contract. The `append([]byte(nil), entry.RawLine...) + '\n'` shape at lines 195–196 is unchanged. |

The state-update ordering at lines 179–193 stays as-is — first the counters/flags update, then the write. Same comment-block rationale ("totals stay consistent even if the write fails") still applies.

The `usageTotals` accumulator type at lines 98–103 stays private and unchanged. The `Emitter` struct shape is unchanged.

### Usage walk (new private helper)

```go
// usageBlock mirrors message.usage. Used only as the readUsage return type;
// not exported. Mirrors jsonl.UsageBlock's four-field shape so the existing
// aggregation arithmetic doesn't change.
type usageBlock struct {
    InputTokens              int
    OutputTokens             int
    CacheCreationInputTokens int
    CacheReadInputTokens     int
}

// readUsage extracts the four token-counter fields from the entry's message.usage
// object. Returns (zero, false) when the entry has no Message, no Message.Raw,
// no "usage" key, or a non-map value at "usage" — same observable behaviour as
// today's `ev.Usage == nil` gate.
func readUsage(entry tuidriver.JSONLEntry) (usageBlock, bool)
```

Implementation (~15 LOC): test `entry.Message`, then `entry.Message.Raw["usage"]`, then `.(map[string]any)` type-assert. Pull each of the four fields via `m["input_tokens"].(float64)` etc. — `encoding/json` decodes numbers as `float64` into `map[string]any`. Convert to `int` (truncation is fine; the counters are integer-valued upstream).

The two-return idiom (`u, ok := readUsage(entry)`) preserves "field absent" semantics. **Do NOT** infer presence from "all four fields zero" — a legitimate zero-cost assistant entry would silently drop from aggregation.

Tests pin the four failure modes (no Message, no Raw, no usage key, non-map value); see § Testing.

### `runner.go` — call-site adapter (path (a))

The `OnEvent` closure at runner.go:365–370 currently:

```go
OnEvent: func(ev jsonl.Event) {
    if err := emitter.Emit(ev); err != nil && emitErr == nil {
        emitErr = err
    }
    counter.OnEvent(ev)
},
```

becomes:

```go
OnEvent: func(ev jsonl.Event) {
    if err := emitter.Emit(eventToEntry(ev)); err != nil && emitErr == nil {
        emitErr = err
    }
    counter.OnEvent(ev) // unchanged — budget still on jsonl.Event per #512 split
},
```

`eventToEntry` is a private file-scope helper in `runner.go` (~12 LOC). Shape:

```go
// eventToEntry converts a jsonl.Event into a tuidriver.JSONLEntry shape
// sufficient for streamjson.Emitter's reads. Throwaway adapter — #512 deletes
// it when the watcher pivots to tuidriver.TailJSONL.
//
// Populates: Type (from Kind), RawLine (from Raw, byte-identical), and a
// minimal Message{StopReason, Raw{"usage": ...}} when Kind == "assistant".
// The Raw["usage"] sub-map carries the four counter fields as float64 so
// readUsage's type-assertion path matches tuidriver's own parseMessage output.
func eventToEntry(ev jsonl.Event) tuidriver.JSONLEntry
```

Rules:
- `Type` = `ev.Kind` (whitelisted kinds — `assistant`, `user`, `tool_use`, …; empty string for unrecognised, same as today).
- `RawLine` = `[]byte(ev.Raw)`. Direct cast (the underlying byte slice is the same).
- `Message`: nil for non-assistant entries. For assistant entries: `&tuidriver.EntryMessage{StopReason: ev.StopReason, Raw: usageMap(ev.Usage)}` where `usageMap` returns `nil` when `ev.Usage == nil`, otherwise `map[string]any{"usage": map[string]any{"input_tokens": float64(ev.Usage.InputTokens), …}}`.
- `Entry.Raw`: leave nil. The emitter never reads `entry.Raw` directly (it reads `entry.Message.Raw` and `entry.RawLine`).
- `Content` on `EntryMessage`: leave nil. `tuidriver.IsEndTurn` reads `AssistantText(entry)` which iterates `Message.Content`; for the emitter's purposes, the EndOfTurn signal is what we ultimately care about. **However**, because `IsEndTurn` requires non-empty text and `Content` is nil in the adapter, `IsEndTurn(entry)` would return `false` even on a real end-of-turn entry. Two options:
  - (i) Populate the adapter's `Content` with a single `ContentBlock{Type: "text", Raw: map[string]any{"text": "x"}}` whenever `ev.EndOfTurn` is true, mirroring `jsonl.Event.EndOfTurn` byte-for-byte at the conversion boundary. Trade: synthetic content block, but `IsEndTurn` sees what it expects.
  - (ii) Hoist `ev.EndOfTurn` into a synthetic `Message.StopReason == "end_turn"` plus a sentinel text block. Same result, different framing.

  **Pick (i).** It's structurally aligned with what tuidriver expects (an assistant entry whose text is non-empty), and one synthetic `Content` block per assistant entry costs ~5 LOC inside `usageMap` / a sibling helper. **Document the synthetic text block in the helper's doc-comment so the developer doesn't grep for a real source.**

  The byte-equivalence test (#506, `make e2e-realclaude`) is not affected: `RawLine` is the verbatim source line, the synthetic Content/Raw blocks influence only `IsEndTurn` / `readUsage` paths, never the bytes written to stdout.

### Error / refusal handling

Unchanged. The sticky `writeErr` short-circuit, the post-Close no-op behaviour, and `SetExitReason` all keep their current shapes. No new error sentinel introduced.

### Concurrency

Unchanged. `Emit` continues to take `e.mu` for the full body. `readUsage` is a pure function with no shared state; safe to call from any goroutine. The adapter `eventToEntry` is pure and called only from the watcher's Run goroutine (same site as today's `Emit`).

## Testing strategy

### `emitter_test.go` rewrite

Every `jsonl.Event{...}` literal → `tuidriver.JSONLEntry{...}` literal. Every `jsonl.UsageBlock{...}` → in-line `Message.Raw["usage"] = map[string]any{...}`. Helper to keep tests readable:

```go
// entry constructs a tuidriver.JSONLEntry that the Emitter can consume. Mirrors
// the structural shape of internal/agentrun/ptyrunner/runner.go:eventToEntry —
// any drift between the two breaks ptyrunner-driven coverage. assistantEntry is
// the assistant-only variant carrying StopReason + optional usage + the
// synthetic text block IsEndTurn needs to fire.
func entry(t, rawLine string) tuidriver.JSONLEntry
func assistantEntry(rawLine, stopReason string, usage *usageBlock, endOfTurn bool) tuidriver.JSONLEntry
```

These are file-scope helpers in `emitter_test.go`. **Do not** lift `eventToEntry` from `runner.go` into a cross-package helper — `streamjson` doesn't import `jsonl`, the adapter is throwaway, and a shared helper would couple two packages that #512 wants to decouple.

Scenarios to preserve (each table row keeps its old assertions):

- **`TestEmit_RawPassthrough_PreservesBytesVerbatim`** — three rows: assistant with usage / tool_use no usage / unrecognised kind with non-canonical whitespace. Assert byte-equality of `buf.Bytes()[initLen:]` against `entry.RawLine + '\n'`.
- **`TestEmit_AggregatesUsage`** — five-entry sequence. Two carry usage maps; one assistant without usage; one user / one tool_use. Assert trailer totals: input=30, output=4, cache_creation=300, cache_read=5.
- **`TestEmit_NumTurnsCountsAssistantEvents`** — eleven entries across mixed kinds. Assert `tr.NumTurns == 5`.
- **`TestEmit_LastStopReasonWins`** — three assistant entries; assert `tr.StopReason == "end_turn"`.
- **`TestEmit_NoAssistantEvent_StopReasonEmpty`** — user/tool_use/tool_result; assert empty stop_reason.
- **`TestTrailer_CompletionDefault` / `MaxTurns` / `Error` / `DefaultErrorFallback_NoEOT`** — unchanged in shape; entry construction switches.
- **`TestSetExitReason_Idempotent`, `TestClose_Idempotent`, `TestEmit_AfterClose_NoOp`, `TestEmit_WriteErrorIsSticky`, `TestNew_InitWriteFailureReturnsError`, `TestTrailer_DurationMSUsesNowSeam`, `TestTrailer_SessionIDRoundTrips`, `TestTrailer_ConstantFields`, `TestNew_WritesInitLineFirst`, `TestNew_InitLineKeyOrderMatchesFixture`, `TestNew_EmptyToolsMarshalsAsEmptyArray`** — straightforward literal rewrites or zero changes if they don't construct events at all.

### New tests (usage absent-vs-present matrix)

Four single-purpose tests pinning `readUsage`'s "absent" paths. Each emits one assistant entry with the matrix below, then asserts `tr.Usage.InputTokens == 0` (and friends) AND that other state updates still occurred (`tr.NumTurns == 1`):

- `TestReadUsage_NilMessage` — `JSONLEntry{Type: "assistant", Message: nil, RawLine: ...}`. (Defensive — a malformed assistant envelope. Today's `jsonl.Event` shape would also surface `Usage == nil` here.)
- `TestReadUsage_NoRawMap` — `JSONLEntry{Type: "assistant", Message: &EntryMessage{Raw: nil}, RawLine: ...}`.
- `TestReadUsage_NoUsageKey` — `Message.Raw = map[string]any{"stop_reason": "tool_use"}`.
- `TestReadUsage_NonMapUsage` — `Message.Raw = map[string]any{"usage": "not a map"}`. **Must not panic.**

Each test reuses `newTestEmitter`; one Emit + one Close; assert trailer usage totals are all zero. ~12 LOC per test.

### `TestCapturedFixture_ByteEquivalence` rewrite

Currently drives the fixture through `jsonl.NewReader`. The rewrite drops `jsonl` and constructs `tuidriver.JSONLEntry` values directly from the fixture's line bytes. Shape:

1. Read `testdata/captured_run.jsonl`, split on `\n`, drop blank trailing line, isolate trailer.
2. For each non-init, non-result line, build a `tuidriver.JSONLEntry` via a `lineToEntry(line []byte) tuidriver.JSONLEntry` test helper that mirrors `tuidriver.parseEntry`'s logic locally (since `parseEntry` is unexported in tuidriver):
   - `RawLine: bytes.Clone(line)`
   - `json.Unmarshal(line, &raw)`; set `Raw = raw`
   - `Type, _ = raw["type"].(string)`
   - if `m, ok := raw["message"].(map[string]any); ok` → populate `Message{StopReason, Content[], Raw: m}` walking `m["content"].([]any)` for each block's `type` field.
3. Emit each entry; Close; assert byte-equivalence of non-result lines and the trailer's documented field subset (unchanged from today).

The helper duplicates ~25 LOC of tuidriver-internal parsing. **Accept the duplication** — extracting `tuidriver.ParseEntry` is a tui-driver follow-up (filed under "out of scope"), and #512's watcher pivot to `tuidriver.TailJSONL` makes both the duplicate AND the helper itself disposable. Document the duplication in a one-line comment pointing at the tuidriver follow-up.

`TestNew_InitLineKeyOrderMatchesFixture` does not touch events; it reads only `testdata/captured_run.jsonl`'s first line. No change required.

### `make check` and `make e2e-realclaude`

Both must stay green:

- `make check` (build + unit tests + `go vet`) — should pass purely from the rewrite. No new dependency.
- `make e2e-realclaude` (byte-equivalence test #506) — the live integration runs a real `claude` child; the byte stream pyry emits goes through `RawLine` now, so byte-for-byte equality with the captured `claude -p` reference output is preserved.

## Migration / rollout

Single commit. No feature flag, no behaviour change observable at the stream-json wire surface.

`internal/agentrun/jsonl` package stays in the tree — #512 deletes it. `budget.Counter.OnEvent` stays on `jsonl.Event` — #512 migrates it. The watcher Config field `OnEvent func(jsonl.Event)` stays — #512 changes it when it pivots to `tuidriver.TailJSONL`.

## Open questions

- **`eventToEntry`'s synthetic `Content` block.** Picking option (i) above creates a `[]ContentBlock{{Type: "text", Raw: map[string]any{"text": "x"}}}` whenever `ev.EndOfTurn` is true. The single `"x"` byte is arbitrary; any non-empty string passes `AssistantText`'s `!= ""` check. The synthetic content is never re-emitted (the byte-passthrough uses `RawLine`, not Content). If a future tuidriver change makes `IsEndTurn` walk additional fields, the adapter shape may need to grow — but #512 deletes this adapter anyway, so the question is moot beyond this slice.

## Out of scope (handled by #512 / future tickets)

- `internal/agentrun/budget/budget.go`'s `Counter.OnEvent(ev jsonl.Event)` — migrates with #512.
- `internal/agentrun/selfcheck/selfcheck.go`'s independent `jsonl.NewReader` consumer.
- `internal/agentrun/jsonl/tail/watcher.go`'s `OnEvent func(jsonl.Event)` Config field.
- `internal/e2e/realclaude/fixtures.go`'s `type JSONLEntry = jsonl.Event` alias.
- Deleting the `internal/agentrun/jsonl/` package.
- A `tuidriver.Usage(entry)` accessor helper (tui-driver follow-up; not gating here — `readUsage` lives inside `streamjson` for this slice).
- A `tuidriver.ParseEntry(line []byte) (JSONLEntry, bool)` exported variant of the package-internal parser (tui-driver follow-up; tests duplicate ~25 LOC of the parsing logic in the interim).

## Scope self-check

Files this spec prescribes new or modified content for (production source files only — `*.go`, excluding `*_test.go`, excluding spec/docs/markdown):

1. `internal/agentrun/streamjson/emitter.go` — modified (signature change, body re-reads, ~15-LOC `readUsage` helper).
2. `internal/agentrun/ptyrunner/runner.go` — modified (one closure body change + ~12-LOC `eventToEntry` adapter).

Count: **2**. Below the ≥5 split threshold.

Edit fan-out: `streamjson.Emitter.Emit` has one production call site (`runner.go:365`). Confirmed via grep `grep -rn 'emitter\.Emit\|\.Emit(ev' internal/agentrun/`. Below the 10-call-site red line.

Total LOC projection: ~50 emitter.go + ~22 runner.go + ~200 emitter_test.go rewrite + ~50 four new readUsage tests + ~50 captured-fixture helper = ~370 LOC written. Well under the ~600 LOC ceiling.
