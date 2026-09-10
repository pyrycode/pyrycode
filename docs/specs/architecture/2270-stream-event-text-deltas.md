# Stream-event text deltas

## Files read

- `internal/streamsup/parser.go` → `Parser`, `streamLine`, `consumeLine`, `emitAssistant`, `emitUnrecognized` — owns newline segmentation, cross-line turn state, completed assistant-block mapping, and visible rejection.
- `internal/streamsup/parser_test.go` → `TestParserMapsStreamJSONLines`, `collectEvents` — establishes the parser's table-driven mapping and fallback behavior.
- `internal/streamsup/capture_test.go` → `droppedLineCapture`, `capturedLines` — provides the precedent for replaying committed real-Claude bytes under the hermetic package test tier.
- `internal/e2e/realclaude/testdata/stream_event_v2.1.259.json` → `lines`, `message_ids_observed`, `event_type_census`, `delta_type_census` — authoritative event order, field placement, message identities, and captured subtype census.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” and “Content blocks are held as `[]json.RawMessage`” — documents parser ownership, its single result reset boundary, and raw-inner-payload preservation.
- `docs/knowledge/features/development-verification.md` → “Captures and live evidence” and “Prove that tests distinguish the change” — requires a committed-fixture reader to fail closed and event cardinality to be asserted explicitly.
- `CODING-STYLE.md` → “Concurrency”, “Testing”, and “Comments — Citing Other Code” — fixes the package's test, synchronization, and citation conventions.

## Context

Claude 2.1.259 can emit token-level text in top-level `stream_event` lines before the settled `assistant` line. `consumeLine` currently treats that top-level type as unknown, while `emitAssistant` publishes the settled text, so enabling partial messages without parser support would produce noise and paragraph-level delivery. The committed capture proves that message identity exists only on the inner `message_start`, that content indexes restart for each message, and that settled text follows all deltas for its open text block.

This ticket makes the parser understand that measured family while leaving downstream event mapping and production argv unchanged. It does not warrant an ADR: it extends the existing parser boundary with a captured vendor-wire variant and introduces no new package or external contract.

## Design

`Parser` gains bounded scalar state for the current stream-event message and its currently open content block: message ID, block index/type, and whether that text block emitted a delta. `message_start` replaces all of that state. `content_block_start` replaces the open-block state. A matching `content_block_stop` clears only the open block. The existing `result` arm clears the entire stream-event state before publishing `TurnEnd`, preserving the parser's single turn-reset boundary.

`consumeLine` adds a `stream_event` arm that delegates to an `emitStreamEvent`-style helper. The helper decodes `event` as raw JSON first, then a minimal inner discriminator. This keeps nested control data out of `streamLine`, preserves raw bytes for `Unrecognized`, and prevents nested values from being reinterpreted as top-level boundaries.

The inner-event behavior is:

- `message_start`: require a non-empty `event.message.id`, then make it current and reset open-block state.
- `content_block_start`: require a usable non-negative index and non-empty block type, then record the open block.
- `content_block_delta/text_delta`: require a current message, a matching open text block/index, and a decodable text string; publish one `turnevent.TextChunk` with the current message ID and mark the block as delta-backed.
- `content_block_delta/thinking_delta`, `signature_delta`, and `input_json_delta`: consume without an event after their inner discriminator and index decode safely.
- `content_block_stop`, `message_delta`, and `message_stop`: consume without an event; a matching stop closes the tracked block.
- Unknown or undecodable inner events, invalid required routing fields, and unattributed text deltas: publish exactly one `turnevent.Unrecognized` and no text.

`emitAssistant` retains its existing mapping except for the captured settled-text shape. It suppresses a completed text block only when the assistant message matches the current message, the line carries exactly one text block, and the currently open text block has already emitted deltas. This deliberately preserves all multi-block behavior and every assistant text line that lacks delta evidence. Thinking and tool-use blocks remain unchanged, so partial JSON never creates a tool event and the completed `tool_use` remains the sole source.

No model output is logged. Rejection uses the existing capped `emitUnrecognized` event path; diagnostics contain only discriminators already present in that event contract.

## Concurrency model

No goroutines or channels are added. `Parser.Write`, its state transitions, and sink calls remain serialized by the existing single stdout-forwarder invariant. The new fields require no mutex and are reset on the same goroutine that reads them.

## Error handling

Whole-line JSON decode failures retain the existing undecodable result. A valid outer `stream_event` whose `event` value cannot be decoded safely emits one `Unrecognized`. Unknown inner event or delta types also emit one `Unrecognized`, while the measured non-text set is explicitly consumed. State is updated only after required routing fields decode and validate, so rejected input cannot change the current message or open block. A `result` clears stream-event attribution even when its own subtype is unfamiliar.

## Testing strategy

- Add a committed-capture reader fixed to `stream_event_v2.1.259.json`; it requires `is_capture`, the pinned Claude version, JSON-string payload encoding, and non-empty captured lines.
- Replay all 45 capture lines through one parser. Assert all seven text deltas become `TextChunk`s, the first and second message IDs are both attributed correctly, concatenated emitted text equals concatenated completed assistant text, exactly one tool start is emitted, and no `Unrecognized` is produced.
- Assert the captured non-text event census contributes no client-visible event by replaying each stream event independently with any necessary message/block prefix and checking cardinality.
- Add table-driven synthetic cases for standalone assistant text, unknown/undecodable inner events, missing message identity, mismatched routing, and result-boundary attribution reset.
- Run the new tests before production changes to establish RED, then `go test -race ./internal/streamsup/...`, `go vet ./...`, and `go build ./cmd/pyry` after GREEN.

## Open questions

None. The committed capture settles message-ID placement, ordering, content-index reuse, and the settled duplicate shape.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/streamsup-package.md`, in the “Turn I/O — envelope write + stdout parser” area, to describe `stream_event` text-delta attribution, settled-text suppression, and result-boundary reset. No protocol reference or client documentation changes are required because `TextChunk` and its downstream `assistant_delta` mapping are unchanged.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — subprocess stdout crosses the explicit boundary at `Parser.consumeLine`; the new inner decoder validates routing fields before mutating parser state or publishing text.
- [Tokens, secrets, credentials] No findings — the change creates, stores, compares, and logs no credential or token. Message IDs are routing identifiers already present on existing `TextChunk` events.
- [File operations] No findings — production code performs no file operation; the test reads one repository-fixed committed fixture path.
- [Subprocess / external command execution] No findings — spawn arguments, environment inheritance, signaling, and command execution are unchanged.
- [Cryptographic primitives] No findings — the design adds no randomness, key material, hashing, or cryptographic comparison.
- [Network & I/O] No findings — `Parser.Write` retains `defaultMaxParseBuf` as the input-line bound, `emitUnrecognized` retains `maxUnrecognizedRaw` as the rejected-payload bound, and the new state retains only bounded scalar routing metadata rather than accumulated model text.
- [Error messages, logs, telemetry] No findings — model delta text is emitted only as `TextChunk`; the design adds no payload logging and uses the existing capped client-visible rejection path.
- [Concurrency] No findings — the existing `Parser` single-writer invariant covers all new state; no lock, goroutine, or shutdown path is introduced.
- [Threat model alignment] No findings — this parser change neither authenticates remote callers nor changes relay/protocol boundaries. Unknown and unsafe child-wire variants fail visibly instead of being silently trusted.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

## Revisions

None.
