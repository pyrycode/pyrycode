# 2328 — attach assistant parent id to text events

## Files read

- `internal/streamsup/parser.go` → `emitAssistant`, `decodeAssistantParent`, `parentToolUseID` — the assistant mapper already decodes the bounded line-level parent id and applies it to tool events.
- `internal/streamsup/parent_tool_use_test.go` → `TestParser_ParentToolUseID_ReachesBothToolEvents`, `TestParser_ParentToolUseID_RejectsEveryNonStringShape`, `TestParser_ParentToolUseID_BoundDropsRatherThanCuts`, `TestParser_ParentToolUseID_ReadsInnerDepthVerbatim` — the hermetic fixtures and assertions that define valid, absent, malformed, over-cap, and nested parent-id behavior.
- `internal/turnevent/event.go` → `TextChunk`, `ToolStart` — the neutral event contract and the existing parent-attribution semantics to mirror.
- `internal/turnevent/event_test.go` → `TestEvents_FieldRoundTrip` — the package-level field preservation check.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” — confirms that assistant stdout is mapped to neutral turn events and that the parser is the validation boundary.
- `docs/knowledge/features/turnevent-package-outbound-event-variants.md` → “The outbound `Event` variants” — records the current `TextChunk` field contract that the documentation stage must update.
- `docs/knowledge/features/development-verification.md` → source and protocol-test guidance — confirms that synthesized hermetic parser lines are the appropriate proof for invalid and chosen nested values.

## Change

Add `ParentToolCallID string` to `turnevent.TextChunk` with the same spawned-by meaning as `ToolStart.ParentToolCallID`. In `emitAssistant`, copy the already-decoded line-local `parent` value into each text block event. Do not change `ThoughtChunk`, parser state, command-line arguments, or downstream delivery: `--forward-subagent-text` remains absent, so attributed subagent prose cannot enter the current single-lane emitter yet. Extend the existing hermetic parent-id tests to prove valid ids are preserved byte-for-byte and absent, null, non-string, and over-cap values remain empty; reuse the existing two-line nesting fixture to prove the inner id reaches text without latching.

## Testing strategy

- Update `TestEvents_FieldRoundTrip` to assert the new `TextChunk` field survives value construction.
- Add focused text-block rows beside the existing parent-id parser tests, covering attributed, absent, null, non-string, over-cap, and sequential outer/inner ids while also asserting text and message ids remain unchanged.
- Run `go test -race ./internal/streamsup/... ./internal/turnevent/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/turnevent-package-outbound-event-variants.md` in “The outbound `Event` variants” so the `TextChunk` field table includes `ParentToolCallID` and its empty-main-thread meaning.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — untrusted subprocess stdout crosses one existing boundary at `parentToolUseID`; `emitAssistant` receives only the bounded decoded string and does not introduce a second interpretation.
- [Tokens, secrets, credentials] No findings — the parent id is a correlation handle, not an authentication token or credential, and this change neither generates nor stores it.
- [File operations] No findings — no filesystem operation is added or changed.
- [Subprocess / external command execution] No findings — no spawn arguments, environment variables, process lifecycle, or command execution changes; `--forward-subagent-text` remains disabled.
- [Cryptographic primitives] No findings — no cryptographic operation or key material is involved.
- [Network & I/O] No findings — input remains bounded by the parser's existing line cap and `parentToolUseID` applies `maxTaskFieldID`; over-cap ids degrade to empty rather than retaining attacker-sized data.
- [Error messages, logs, telemetry] No findings — `decodeAssistantParent` and `parentToolUseID` log nothing, and the new event field is not added to an error, log, or telemetry path.
- [Concurrency] No findings — `emitAssistant` copies an immutable line-local string into a value event; no state, lock, channel, or goroutine changes.
- [Threat model alignment] OUT OF SCOPE — lane-aware downstream delivery is intentionally deferred to the follow-on from #2192; keeping `--forward-subagent-text` disabled prevents this dormant attribution from changing current relay output.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-10

## Revisions

None.
