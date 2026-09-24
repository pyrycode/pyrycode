# #2609 — Codex command and file-change items as tool events

## Files read

- `internal/codexsup/translate.go` → `Translator`, `Translate`, `item`, the method and item-type lanes (`mappedMethods`, `ignoredMethods`, `unrecognizedMethods`, `mappedItemTypes`, `unrecognizedItemTypes`), `truncate` — the only production file this ticket edits.
- `internal/codexsup/translate_test.go` → `TestTranslateCapturedCommandTurnUsesTotalDelta` (asserts the `commandExecution` Unrecognized row this ticket removes), `TestTranslateHandBuilt` (table the new hand-built cases join), `TestHandBuiltFramesMatchSchema`, `replay`, `only`.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `ThreadItem`'s `commandExecution` and `fileChange` arms, `FileUpdateChange`, `CommandExecutionStatus`, `PatchApplyStatus`, `ItemStartedNotification`/`ItemCompletedNotification` (required `startedAtMs`/`completedAtMs`, `threadId`, `turnId`).
- `internal/codexsup/testdata/capture/{command_accepted,command_declined,file_edit}.jsonl` — one completed command (`exitCode` 0, `aggregatedOutput` null) and two declined commands. No `fileChange` item anywhere.
- `internal/turnevent/event.go` → `ToolStart`, `ToolUpdate` (the `ResultDetail` provenance contract: digits, spaces, ASCII letters, U+2212, U+00B7; no peer byte), `Location`; `content.go` → `TextContent`; `taxonomy.go` → `ToolKindExecute`, `ToolKindEdit`, `ToolStatusCompleted`, `ToolStatusFailed`.
- `internal/streamsup/parser.go` → `toolResultContent` (empty text → nil content, text carried unbounded), `rawInput`, `maxResultDetailBytes` (48) — the precedents this mapping follows.
- `internal/turnbridge/outbound.go` → the `ToolStart`/`ToolUpdate` arms of the outbound switch: `Title` becomes the wire `name`, `RawInput` is capped downstream by `inputSummary`/`inputFields`, `Content` by `resultSummary`; `IsError` is `Status == failed`. Every `ToolUpdate` is one `tool_result` frame, hence one update per call.
- `docs/specs/architecture/2608-codex-turn-events.md` — the translator's design and its Security review, which this extends.

No other in-flight branch touches `internal/codexsup`.

## Context

#2608 left `commandExecution` and `fileChange` as `Unrecognized` rows on the `codex_item` lane. A client should see them as tool rows, the same `tool_use` / `tool_result` pair a Claude Bash or Edit call produces, so no client change is needed. The translator is still unwired (#2585).

## Design

All in `translate.go`.

**Lanes.**
- `commandExecution` and `fileChange` move from `unrecognizedItemTypes` to `mappedItemTypes`.
- `item/commandExecution/outputDelta` and `item/fileChange/outputDelta` move from `unrecognizedMethods` to `ignoredMethods`, with a comment: the completed item's aggregated output or diff is the single result, and every `ToolUpdate` becomes one `tool_result` frame.
- `item/fileChange/patchUpdated`, `item/commandExecution/terminalInteraction` and the other tool-shaped items stay unrecognized.

**Dispatch.** `item` gains two cases ahead of the existing ones: `commandExecution` → `commandItem(method, params)`, `fileChange` → `fileChangeItem(method, params)`. Each re-decodes params into its own typed struct and returns `undecodable(params)` on a decode failure. Neither keeps state: `item/started` maps to `ToolStart` and `item/completed` maps to `ToolUpdate`, independently. Outside the capture there is no reason to expect a completed item without its start, and pairing them would add per-item state that has no observed need.

**Command.**
- `ToolStart{ToolCallID: item.id, Title: cut(command, maxToolTitle), Kind: ToolKindExecute, RawInput: {"command":…,"cwd":…}}`. `RawInput` is `json.Marshal` of a two-field struct, so the bytes are deterministic and the keys are the ones `inputFields` shows.
- `ToolUpdate{ToolCallID: item.id, Status, Content, ResultDetail}`:
  - `Status`: `completed` → `ToolStatusCompleted`, anything else (`failed`, `declined`, an out-of-contract `inProgress` or unknown value) → `ToolStatusFailed`. An item that completed without succeeding has not succeeded.
  - `Content`: `TextContent{aggregatedOutput}`, or nil when it is null or empty. Empty → nil follows streamsup's `toolResultContent`, which gives an empty result no content. The AC names null. Empty gets the same answer because a `TextContent{""}` row renders the same as no content.
  - `ResultDetail`: `"declined"` when the status is `declined`, else `"exit N"` when `exitCode` is non-null, else `""`. N is formatted from the decoded integer. A negative code uses U+2212 rather than an ASCII hyphen, because U+2212 is in the `ResultDetail` alphabet on `turnevent.ToolUpdate` and the hyphen is not. At most `"exit −2147483648"`, 18 bytes, under 48.

**File change.**
- `ToolStart{ToolCallID: item.id, Title: "apply_patch", Kind: ToolKindEdit, Locations: one Location{Path} per change, Line 0}`. The AC fixes no title. `apply_patch` is the name of the Codex tool that produces this item, and the wire field is the tool's name, as with Claude's `Edit`. It is a translator literal, so it carries no peer bytes. `RawInput` is nil. The paths are already in `Locations`, and an input would duplicate them.
- `ToolUpdate{ToolCallID: item.id, Status, Content, ResultDetail}`. Status and the `"declined"` detail follow the command rules, and there is no exit code. `Content` is one `TextContent` whose text is each change as `path + "\n" + diff`, the changes joined by `"\n"`. It is nil when there are no changes. The diff is Codex's unified diff verbatim. No file is read from disk.

**Bounds.** A new constant `maxToolTitle = 4 << 10` sits beside `maxBannerText`, and `Title` is cut to it with `cut`, which also scrubs a rune the cut splits. Output, diffs, paths and the `RawInput` values are carried like streamsup carries `tool_result` text and tool input. The only bound here is `acp`'s line cap, and turnbridge caps them on the way to the wire. The `Translator` doc comment is updated to name that carried set.

## Concurrency model

None added. `Translate` is still single-goroutine and the new paths hold no state.

## Error handling

- Params that fail to decode, such as an `exitCode` that is not an integer, give one `Unrecognized` on the `undecodable` site. This is the existing rule.
- A missing optional field (null `exitCode` or `aggregatedOutput`) is decoded into a pointer and yields no detail or no content. It is not an error.

## Testing strategy

- **Captures** (`TestTranslateCapturedCommands`, table over the three captures): each yields exactly one `ToolStart` and one `ToolUpdate`, compared with `reflect.DeepEqual`. `command_accepted` gives execute, title `/bin/zsh -lc 'touch accepted.txt'`, `RawInput` `{"command":…,"cwd":"/capture/cwd"}`, then completed, nil content, `"exit 0"`. `command_declined` and `file_edit` give failed, nil content, `"declined"`. None yields an `Unrecognized`.
- **Existing assertion**: `TestTranslateCapturedCommandTurnUsesTotalDelta` drops its one-`commandExecution`-row assertion and asserts no `Unrecognized` rows.
- **Hand-built frames**, validated by the existing `TestHandBuiltFramesMatchSchema`, joining `TestTranslateHandBuilt`'s exact-sequence table:
  - `command_failed.jsonl`: started, two `outputDelta`s (ignored), completed `failed` with `exitCode` 2 and output text. Want: ToolStart, then ToolUpdate failed with `TextContent` and `"exit 2"`.
  - `file_change.jsonl`: three items. The first is `completed` with two changes (an update and an add), with one `fileChange/outputDelta` between start and completion. The second is `failed`, the third `declined`. Want, per item: ToolStart edit with its Locations, then ToolUpdate with the path-plus-diff text and status and detail as specified.
- **Inline unit cases** (`TestTranslateToolItemEdges`): a negative exit code renders with U+2212; an oversize command title is cut to `maxToolTitle`; empty `aggregatedOutput` gives nil content; an `exitCode` string gives the undecodable row; `item/commandExecution/outputDelta` yields nothing.
- `TestMethodListsPartitionServerNotifications` and `TestItemTypesClassified` already guard the lane moves.

## Open questions

1. Does Codex put the planned `changes` on `fileChange`'s `item/started`? The schema requires `changes` on every `ThreadItem`, so the Locations come from the start frame. No capture shows it. The hand-built frames assume it, and a future recapture confirms or revises it.

## Documentation handoff

None required by the ticket. For the documentation stage: the `codexsup` package overview should record the command and file-change mapping once it exists, if the overview covers the translator.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. `Translator.Translate` is still the only boundary. `commandItem` and `fileChangeItem` decode untrusted params into typed structs and emit `turnevent` values. The command, cwd, output, paths and diffs are carried and never interpreted: nothing is executed, parsed as a path, opened, or read from disk. The ticket's "do not rebuild file contents from the disk" rule is what keeps a peer-chosen path from becoming a read.
- [Trust boundaries] SHOULD FIX, addressed in Design: `ResultDetail` must hold no peer byte. It is built only from the literal `"declined"`, the literal `"exit "`, the digits of the decoded integer and U+2212. It is never built from the status string or any other text. The inline test pins the negative form.
- [Trust boundaries] SHOULD FIX, addressed in Design: `Title` becomes the wire tool name and is built from the peer's command. It is cut at `maxToolTitle` with invalid UTF-8 scrubbed. The file-change title is a literal.
- [Tokens] No findings. The hand-built frames use fixed placeholder ids and `/capture/cwd` paths, and carry no account data or local paths. The captures are unchanged.
- [File operations] No findings. The translator touches no file, and `Location.Path` is display data.
- [Subprocess] No findings. The ticket runs no command. Commands in frames are strings.
- [Crypto] Not applicable.
- [Network & I/O] No findings. Output, diff text and `Locations` scale with the frame, which `acp`'s `maxLineBytes` bounds, as streamsup's tool results are. `RawInput` and `Content` are capped downstream by turnbridge's `inputFields`/`inputSummary` and `resultSummary`. A file change with very many changes yields a large but line-bounded `Locations` slice. This is OUT OF SCOPE for a translator-level cap until the wiring ticket (#2585) shows a need.
- [Logs] No findings. Nothing is logged.
- [Concurrency] No findings. There are no goroutines and no new state.
- [Threat model] OUT OF SCOPE. Whether a client sanitises a model-influenced tool name or result text is the existing `tool_use` / `tool_result` contract that Claude rows already rely on, and it is exercised end to end when #2585 wires the translator.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
