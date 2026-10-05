# #2829 — Claude native prompt suggestions as a neutral turn event

## Files read

- `internal/streamsup/parser.go` → `Parser.consumeLine`: the top-level type switch; `prompt_suggestion` currently reaches `default` and rings `emitUnrecognized(UnrecognizedLineType, …)`. The new arm sits beside `conversation_reset`.
- `internal/streamsup/parser.go` → `streamLine`: stays segmentation-only (`type`, `subtype`, `message`); the suggestion gets its own decode target, as `conversationResetLine` does.
- `internal/streamsup/parser.go` → `emitConversationReset`: the per-type decode precedent, including "never log the json error, it quotes input".
- `internal/streamsup/parser.go` → `result` arm: the cross-line state the new arm must not touch (`clearAPIRetry`, accumulator, compacting, denials).
- `internal/turnevent/event.go` → `Event`, the `isTurnEvent` marker block, `UserEcho`: where the variant and its marker go.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant`: totality guard reads every `isTurnEvent` marker, so the new variant needs a row (`turnMarkNone`; `turnMarkFor`'s default already answers it).
- `cmd/pyry/interactive_turn_v2.go` → `Handle` default and `eventKind`; `internal/turnbridge/outbound.go` → `MapEvent` default: both drop an unknown variant content-free, so no consumer changes here (wire and daemon integration are #2831).
- `internal/streamsup/user_echo_test.go`: test shape for "exactly one event, nothing in the log".

No other feature branch touches these files.

## Context

Claude's stream-json emits a top-level `{"type":"prompt_suggestion","suggestion":"…","uuid":"…","session_id":"…"}` after `result` when prompt suggestions are enabled (SDK `SDKPromptSuggestionMessage`). Today it becomes an `Unrecognized` row. This slice makes the parser recognise it and publish the text as a neutral event so #2831 can reuse it instead of making a second model call. The parser alone does not enable generation (no spawn flag here) and does not broadcast anything.

## Design

`turnevent.PromptSuggestion{Text string}` — a value variant with an `isTurnEvent` marker. It carries the text only; neither `uuid` nor `session_id` is carried, because neither establishes daemon turn attribution and the session is already tagged by `streamTurnSink.sinkForTag`.

`consumeLine` gains `case "prompt_suggestion": p.emitPromptSuggestion(line)`. The arm consumes the line on every path (valid or not), so `emitUnrecognized` is unreachable for this type by matching. It touches no parser state: no `clearAPIRetry`, no accumulator, no turn boundary.

`decodePromptSuggestion(line []byte) (string, bool)` — a pure function, so the gate is table-testable:

1. Decode `{"suggestion": json.RawMessage}` from the line. Decode error, absent key → reject. Raw longer than the widest JSON spelling of 1024 decoded bytes (6 bytes per `\u00XX` escape, plus quotes) → reject before string decoding, so an oversized value is never decoded into a Go string.
2. Raw bytes must be valid UTF-8 (`utf8.Valid`), checked **before** string decoding, because `encoding/json` turns invalid bytes into U+FFFD silently.
3. Raw must be a JSON string (first byte `"`): rejects `null`, numbers, objects, arrays.
4. Raw must contain no lone-surrogate `\u` escape: `encoding/json` also maps those to U+FFFD, the same silent rewrite step 2 forbids for bytes.
5. Unmarshal into `string`. Then reject: empty or all `unicode.IsSpace`; any of CR, LF, U+0085, U+2028, U+2029; `len(text) > 1024`.

Accepted text is emitted verbatim — no trim, truncation or normalisation. Nested suggestion-shaped content (e.g. inside `message` of an `assistant` line, or inside a `stream_event`) never reaches this arm because dispatch is on the top-level `type` only.

## Concurrency model

None new. The arm runs on the parser's single consuming goroutine, like every other arm.

## Error handling

Every reject is silent: no event, no `Unrecognized`, no log line. Nothing about the line is logged on any path (the json error is not logged either, since it quotes input). A rejected line does not disturb later lines — the arm returns and the next line parses normally.

## Testing strategy

New `internal/streamsup/prompt_suggestion_test.go`, all hermetic, each parser built with a Debug-level text logger into a buffer:

- result → suggestion → next turn: a `result`, then a suggestion, then an assistant text line and another `result`. Events are TurnEnd, PromptSuggestion, TextChunk, TurnEnd in that order; no `Unrecognized`.
- Valid table: plain text, leading/trailing spaces and tabs preserved, multibyte text at exactly 1024 bytes, extra fields (`uuid`, `session_id`) ignored. Each yields exactly one `PromptSuggestion` with the exact text.
- Reject table: missing, `null`, number, object, array, `""`, Unicode-whitespace-only (incl. U+3000), each of the five line breaks (raw and escaped forms), 1025 bytes multibyte, invalid UTF-8 byte inside the string, lone-surrogate escape. Each yields zero events; a following valid suggestion line still yields its event.
- Nested: an `assistant` line whose text block is `{"type":"prompt_suggestion",…}`-shaped yields no `PromptSuggestion`.
- Logs: on every case above the captured log contains neither the suggestion text nor the raw line.
- `cmd/pyry/stream_turn_busy_test.go`: one row `{turnevent.PromptSuggestion{Text: "…"}, turnMarkNone}` for the totality guard.

Existing result and unknown-type tests cover the unchanged behaviour and run unmodified.

## Open questions

- Whether the reject path should leave a content-free Debug record. Decided: no — nothing reads it and silence makes the log AC structural.

## Documentation handoff

Pending for the documentation stage: `docs/knowledge/features/streamsup-package-turn-io-envelope-write-stdout-parser.md`, stdout-parser mapping discussion — describe native `prompt_suggestion` recognition → `turnevent.PromptSuggestion`, accepted text published unchanged, the validation bounds (string type, non-blank, valid UTF-8 with no replacement through decoding, no CR/LF/NEL/U+2028/U+2029, ≤ 1024 UTF-8 bytes), silent reject with no `Unrecognized`, and post-result delivery. State that the parser alone does not enable Claude generation or broadcast suggestions (#2831).

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The one boundary is `decodePromptSuggestion`: claude-authored stdout to an in-process value. Downstream holds only `turnevent.PromptSuggestion.Text`, which is still untrusted model-authored text; the type's doc says so, so #2831 cannot mistake a validated shape for vetted content. Dispatch is on the top-level `type` in `consumeLine` only, so nested content cannot forge the event.
- [Trust boundaries] OUT OF SCOPE: C0 controls other than the rejected line breaks (ESC/ANSI sequences, NUL) and bidi overrides pass the gate verbatim, because the AC fixes the reject set and forbids rewriting. Rendering and any reuse of the text as a prompt are publication decisions owned by #2831.
- [Tokens] No findings. The event carries no identifier or credential; `uuid` and `session_id` are not decoded at all.
- [File operations] Not applicable by design: the arm performs no I/O.
- [Subprocesses] Not applicable by design: no spawn flag or child argument changes; activation is #2831.
- [Cryptography] Not applicable by design: no keys, randomness or secret comparisons.
- [Network and I/O] SHOULD FIX (built in): the line is already bounded by `Parser.Write`'s `maxBuf`, but the raw value is length-checked against the widest escaped spelling of 1024 bytes before decoding, so a long value costs no string allocation. The 1024-byte decoded cap is enforced after decoding.
- [Errors, logs, telemetry] No findings. Neither accept nor reject paths log anything; the `json` error is not logged because it quotes input. Tests assert the captured Debug log holds neither text nor raw line on every case.
- [Concurrency] No findings. Runs on the parser's single consuming goroutine; no new goroutine, lock or shared state, and no parser turn state is read or written.
- [Threat model] No mobile or relay surface changes here; the suggestion reaches no wire. Prompt-injection risk from reusing model-authored text is #2831's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05
