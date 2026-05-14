# Spec — agent-run/jsonl: surface all line kinds + raw bytes + usage block on Event

**Ticket:** #353
**Size:** S (additive API extension; ~80 net LOC production + tests)
**Security-sensitive:** no

## Context

`internal/agentrun/jsonl.Reader.Next()` currently filters to assistant entries only and returns a 3-field `Event{StopReason, TextChars, EndOfTurn}`. Raw line bytes and any per-entry `usage` block are dropped after parsing.

A future stream-json emitter needs (a) every line type, byte-equivalent to what `claude -p --output-format stream-json` produces, and (b) per-assistant token usage so it can aggregate a result trailer. This ticket extends the reader contract to surface that data; no new consumer of the new fields is introduced here.

Existing `EndOfTurn` semantics for assistant entries are preserved verbatim. The only behavioural change for current callers is that the `Reader` (and therefore the `tail.Watcher`) now also yields Events for non-assistant lines — the in-tree consumer (`tail.Watcher`) forwards those verbatim, so the change is a no-op for end-of-turn detection.

## Files to read first

- `internal/agentrun/jsonl/reader.go:39-59` — current `Event` shape and its semantic contract.
- `internal/agentrun/jsonl/reader.go:101-118` — `rawLine` (type-only) and `rawAssistantMessage` JSON shapes.
- `internal/agentrun/jsonl/reader.go:132-183` — the `Next` loop, including the `raw.Type != "assistant" { continue }` line that this ticket removes.
- `internal/agentrun/jsonl/reader.go:194-198` — `AssistantCount()` contract (stays assistant-only).
- `internal/agentrun/jsonl/reader_test.go:144-171` — `TestReader_NonAssistantLinesSkipped`; repurpose to assert non-assistant lines now flow through with the expected `Kind` and `Raw`.
- `internal/agentrun/jsonl/tail/watcher.go:238-258` — `drain` loop; the only in-tree consumer of `Reader.Next()`. No functional change needed (it already forwards every Event to `OnEvent`).
- `internal/agentrun/jsonl/tail/watcher_test.go:230-250` — `TestWatcher_LateCreate` writes a `user` line between two `assistant` lines and asserts `len(events) == 2`; after the change this becomes 3.
- `docs/lessons.md` § JSONL parsing — confirms the deterministic end-of-turn rule and "MUST NOT log file contents" invariant.

## Design

### New exported type — `UsageBlock`

Added to `internal/agentrun/jsonl/reader.go`. Mirrors the assistant `message.usage` JSON object verbatim. Pointer-valued on `Event` to distinguish "not present" from "present with all zeros".

```
type UsageBlock struct {
    InputTokens              int
    OutputTokens             int
    CacheCreationInputTokens int
    CacheReadInputTokens     int
}
```

### Extended `Event` shape

`Event` gains three exported fields; existing three are preserved with their current contracts. Field order: existing three first, then `Raw`, `Kind`, `Usage` — additive at the end so a `gofmt` diff stays minimal and no struct-literal callers (none in tree) would break ordering.

```
type Event struct {
    StopReason string         // existing — assistant only; "" for non-assistant
    TextChars  int            // existing — assistant only; 0 for non-assistant
    EndOfTurn  bool           // existing — assistant only; false for non-assistant
    Raw        json.RawMessage // NEW — verbatim line bytes, trailing '\n' stripped
    Kind       string         // NEW — see classification table
    Usage      *UsageBlock    // NEW — non-nil only on assistant entries with a usage object
}
```

The doc comment on `Event` is rewritten: drop "Non-assistant entries are silently skipped" and instead document each field's behaviour across kinds.

### `Kind` classification

`Kind` derives from the parsed JSON `type` field on the line. Recognised values are surfaced verbatim; anything else (including a missing field) maps to `""`.

| `type` field value         | `Event.Kind`     |
| -------------------------- | ---------------- |
| `"assistant"`              | `"assistant"`    |
| `"user"`                   | `"user"`         |
| `"tool_use"`               | `"tool_use"`     |
| `"tool_result"`            | `"tool_result"`  |
| `"system"`                 | `"system"`       |
| `"attachment"`             | `"attachment"`   |
| anything else, or missing  | `""`             |

The "anything else, or missing" bucket includes the `summary` line shape that today's `TestReader_NonAssistantLinesSkipped` already exercises (`{"type":"summary",...}`). That is intentional: classification is whitelisted to the six kinds named in the acceptance criteria. Any future kind added by claude lands in the unrecognised bucket until this whitelist is updated; downstream re-emitters can still pass it through unchanged via `Raw`.

### `Raw` semantics

- Holds the line bytes the reader consumed, with the trailing `'\n'` stripped.
- If the line ends with `"\r\n"`, the `'\r'` is preserved in `Raw` (only `'\n'` is stripped). This matches the existing line-delimiter behaviour of `Next` and avoids guessing at intent if claude ever writes CRLF.
- Typed as `json.RawMessage` so consumers can re-emit without re-encoding.
- **Must be a freshly-allocated `[]byte`, not a sub-slice of the reader's internal buffer.** The current loop does `line := r.buf[:i]; r.buf = r.buf[i+1:]`; subsequent `append(r.buf, ...)` may write into the backing array where `line` previously lived, mutating `Raw` after the caller has it. The developer must `copy()` into a new slice before assigning to `Event.Raw`. A unit test asserts byte-equivalence after a second `Next` call has been made.

### `Usage` parsing

Extend `rawAssistantMessage` with a pointer-typed `Usage` field so the JSON decoder leaves it `nil` when the field is absent:

```
type rawAssistantMessage struct {
    StopReason string `json:"stop_reason"`
    Content    []struct {
        Type string `json:"type"`
        Text string `json:"text"`
    } `json:"content"`
    Usage *struct {
        InputTokens              int `json:"input_tokens"`
        OutputTokens             int `json:"output_tokens"`
        CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
        CacheReadInputTokens     int `json:"cache_read_input_tokens"`
    } `json:"usage"`
}
```

When non-nil after unmarshal, the loop builds an exported `UsageBlock` from it and assigns the pointer to `Event.Usage`. For every non-assistant line, `Usage` stays `nil`.

### Rewritten `Next` control flow

The current loop:

1. find `'\n'`, slice `line`, advance `r.offset`;
2. unmarshal into `rawLine`; on error → log + skip + `continue`;
3. if `raw.Type != "assistant"` → `continue`;
4. unmarshal `raw.Message` → `rawAssistantMessage`; on error → log + skip + `continue`;
5. bump `assistantCount`, compute `textChars`, return `Event`.

New loop:

1. find `'\n'`, slice `line` from `r.buf`, **copy** into a freshly-allocated `lineCopy []byte`, advance `r.offset`;
2. unmarshal `lineCopy` into `rawLine`; on error → log + skip + `continue` (do **not** surface a malformed line as an Event — the contract that "malformed-JSON lines are logged at Warn and skipped" is preserved);
3. classify `raw.Type` against the whitelist → `kind`;
4. if `kind != "assistant"`, return `Event{Raw: lineCopy, Kind: kind}` (all other fields zero/nil);
5. if `kind == "assistant"`, unmarshal `raw.Message` → `rawAssistantMessage`; on error → log + skip + `continue` (same as today);
6. bump `assistantCount`, compute `textChars`, build optional `Usage`, return the full `Event` with all six fields populated.

`Offset()`, `AssistantCount()`, `ErrLineTooLarge`, and the malformed-line log rate-limit are unchanged.

### `tail.Watcher` consequences

`Watcher.drain()` at `internal/agentrun/jsonl/tail/watcher.go:242-258` already forwards every Event to `OnEvent` and stops on `EndOfTurn`. No code change is required there. The acceptance-criterion line "the watcher adds no filtering of its own beyond what the reader does" is satisfied by the existing implementation.

However, `TestWatcher_LateCreate` and any other tail test that asserts `len(events)` must be updated: the recorder will now see one extra Event per non-assistant line in the test fixtures. The developer adjusts those assertions to count the actual line shapes written.

### Behaviour matrix (for the test author)

| Input line                                       | `Kind`         | `Raw` (verbatim) | `StopReason` | `TextChars` | `EndOfTurn` | `Usage`     |
| ------------------------------------------------ | -------------- | ---------------- | ------------ | ----------- | ----------- | ----------- |
| assistant + `end_turn` + text                    | `"assistant"`  | yes              | `"end_turn"` | `>0`        | `true`      | `nil` or set |
| assistant + `end_turn` + empty content (transitional) | `"assistant"` | yes              | `"end_turn"` | `0`         | `false`     | `nil` or set |
| assistant + `tool_use`                           | `"assistant"`  | yes              | `"tool_use"` | sum text    | `false`     | `nil` or set |
| assistant with `usage`                           | `"assistant"`  | yes              | per-line     | per-line    | per-line    | non-nil      |
| assistant without `usage`                        | `"assistant"`  | yes              | per-line     | per-line    | per-line    | `nil`        |
| user                                             | `"user"`       | yes              | `""`         | `0`         | `false`     | `nil`        |
| tool_use                                         | `"tool_use"`   | yes              | `""`         | `0`         | `false`     | `nil`        |
| tool_result                                      | `"tool_result"`| yes              | `""`         | `0`         | `false`     | `nil`        |
| system                                           | `"system"`     | yes              | `""`         | `0`         | `false`     | `nil`        |
| attachment                                       | `"attachment"` | yes              | `""`         | `0`         | `false`     | `nil`        |
| `{"type":"summary",…}` or any other              | `""`           | yes              | `""`         | `0`         | `false`     | `nil`        |
| `{}` (no `type` field)                           | `""`           | yes              | `""`         | `0`         | `false`     | `nil`        |
| malformed JSON                                   | (not surfaced) | —                | —            | —           | —           | —           |

## Concurrency model

Unchanged. `Reader` remains "not safe for concurrent use; one per source." `tail.Watcher.Run` remains the sole goroutine touching its `Reader`. The added `copy()` for `Raw` is local and goroutine-private.

## Error handling

Unchanged. Malformed-JSON lines are logged at Warn (rate-limited as today) and skipped without surfacing an Event. Read errors from the underlying source still propagate wrapped as `"jsonl: read at offset %d: %w"`. `ErrLineTooLarge` semantics are unchanged.

## Testing strategy

Tests live in `internal/agentrun/jsonl/reader_test.go` and `internal/agentrun/jsonl/tail/watcher_test.go`. Stdlib `testing` only, table-driven where natural.

### `reader_test.go` — repurposed and new

- **Repurpose `TestReader_NonAssistantLinesSkipped` → `TestReader_NonAssistantLinesSurfaced`.** Same input (user + system + summary + assistant). Assert that `drainAll` returns four Events in source order, with the expected `Kind` per line (`"user"`, `"system"`, `""`, `"assistant"`), and that only the last Event has `EndOfTurn == true`. Assert `AssistantCount() == 1` (unchanged contract). Assert `Offset()` reaches end of buffer (unchanged).

- **New `TestReader_RawByteEquivalence`.** Feed three lines whose payloads include nested JSON, whitespace, and unicode (ensure not just ASCII). For each yielded Event, assert `string(ev.Raw) == <input line without trailing '\n'>`. Critically, also call `Next` one additional time *before* inspecting earlier `Raw` slices — this catches a buffer-aliasing regression where `Raw` shares memory with the reader's internal buffer.

- **New `TestReader_KindClassification`.** Table-driven over the seven `type` values (six known + one unknown like `"summary"`) plus the missing-`type` case (`{}` and `{"foo":"bar"}`). Each row asserts the resulting `Event.Kind` matches the table above.

- **New `TestReader_UsageParsedOnAssistant`.** Single assistant line with a `usage` object containing all four fields with distinct non-zero values. Assert `ev.Usage != nil` and that each field of `UsageBlock` matches the JSON.

- **New `TestReader_UsageNilOnAssistantWithoutUsage`.** Single assistant line with no `usage` field. Assert `ev.Usage == nil`.

- **New `TestReader_UsageNilOnNonAssistant`.** A `user` line that happens to carry a `usage`-shaped sub-object on its message (defensive — even if claude never writes this, the reader contract says non-assistant `Usage` is always `nil`). Assert `ev.Usage == nil`.

- **Preserved unchanged.** `TestReader_CleanSingleEndTurn`, `TestReader_DoubleEndTurn_FirstSkipped`, `TestReader_NoEndTurn_SignalNeverFires`, `TestReader_PartialLine_BuffersUntilNewline`, `TestReader_MalformedLineSkippedAndCounted`, `TestReader_ResumeFromOffset`, `TestReader_OffsetAdvancesPerLine`. The fixtures under `testdata/` are real claude sessions; they contain non-assistant lines, so the existing tests that drain the fixture and count `EndOfTurn` continue to work but will now yield more Events. Any test that asserts `len(events)` against an assistant-only count needs to instead count `EndOfTurn`s or `AssistantCount()`. Of the preserved tests, none currently assert `len(events)` against an assistant-only number — they count `EndOfTurn`s and `AssistantCount()` — so no edits are required.

### `tail/watcher_test.go` — assertion updates only

- `TestWatcher_LateCreate` (line 244): change `if len(events) != 2` → `if len(events) != 3` (assistant + user + assistant), and continue to assert `events[len(events)-1].EndOfTurn`.
- `TestWatcher_ExistingFile`: input has only two assistant lines, so `len(events) == 2` still holds; leave as-is.
- Any other test that compares `len(events)` against an assistant-only count should be updated to count the actual line shapes written by that test.

### What is intentionally not tested

- **Tail watcher re-yielding non-assistant Events to `OnEvent`.** The watcher's `drain` loop already calls `OnEvent(ev)` unconditionally, so the existing watcher tests that exercise the loop implicitly cover this. No new tail-level test is added.
- **Behavioural change for downstream consumers.** No downstream consumer of `Event.Raw` / `Event.Kind` / `Event.Usage` is added by this ticket; that is split #335's next slice.

## Open questions

- Should the unrecognised-`type` bucket include a debug-level log? Decision: no. The whole point of the empty-`Kind` fallback is that the reader is forward-compatible with new claude line types; logging every unknown line would be noise once claude adds a new kind. The raw bytes are preserved in `Event.Raw`; downstream consumers can decide what to do.
- Should `Event.Raw` strip a trailing `'\r'` when CRLF is detected? Decision: no — leave it. Today's reader does not do CRLF handling, and inventing semantics here would risk byte-equivalence guarantees against claude's actual output. If claude ever writes CRLF, the consumer can normalise.
