# 2329 — carry `parent_tool_use_id` on `assistant_delta`

## Files read

- `internal/protocol/interactive.go` → `AssistantDeltaPayload`, `ToolUsePayload` — the delta wire shape and the existing grouping-hint contract to mirror without `omitempty`.
- `internal/protocol/interactive_test.go` → `TestAssistantDeltaPayload_RoundTrip`, `roundTripEnvelope` — the fixture-backed round trip that pins decoded values and remarshalled bytes.
- `internal/protocol/testdata/assistant_delta.json` → the canonical non-empty `assistant_delta` fixture whose payload gains a distinct parent id.
- `internal/turnbridge/outbound.go` → `MapEvent` — the pure `TextChunk` to `AssistantDeltaPayload` mapping.
- `internal/turnbridge/outbound_test.go` → `TestMapEventOutbound` — the table that already covers the ordinary and zero-sequence text mappings.
- `internal/turnevent/event.go` → `TextChunk` — the upstream bounded `ParentToolCallID` value supplied by #2328.
- `docs/knowledge/features/protocol-package-interactive-event-payloads.md` → interactive payload contracts — establishes that every field is emitted and parent ids are untrusted grouping hints, not capabilities.
- `docs/knowledge/features/development-verification.md` → protocol boundaries — requires a remarshalled round trip, explicit field assertions, and raw JSON inspection for presence-sensitive zero values.

## Change

Add `ParentToolUseID string` with JSON key `parent_tool_use_id` to `AssistantDeltaPayload`, without `omitempty`, and copy `TextChunk.ParentToolCallID` into it in `MapEvent`. The value is already bounded at the subprocess parser boundary and crosses this pure adapter byte-for-byte; empty means main-thread text. No emitter state, lane selection, capability negotiation, or validation changes here—those remain in #2330.

Extend the fixture-backed protocol round trip with a distinct non-empty parent id. Add a separate zero-value marshal assertion that inspects raw JSON to prove the key remains present as an empty string. Split the mapping rows into attributed and main-thread cases so neither a dropped value nor a stamped constant can pass.

## Testing strategy

- Run the focused protocol test before production changes and confirm it fails because the payload lacks `ParentToolUseID` and the zero-value JSON key.
- Run the focused turnbridge mapping test before production changes and confirm the attributed row fails because `MapEvent` does not copy the parent id.
- After implementation, run `go test -race ./internal/protocol/... ./internal/turnbridge/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md` under `assistant_delta` to list `parent_tool_use_id`, define empty as the main thread, and describe it as a grouping hint joined to the parent Agent frame's `tool_use_id`, not as a capability.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `TextChunk.ParentToolCallID` has already crossed the subprocess boundary and been bounded by `parentToolUseID`; `MapEvent` only copies that immutable value into the wire DTO.
- [Tokens, secrets, credentials] No findings — the parent id is an untrusted correlation handle, not authentication material, and this change neither generates, stores, nor authorizes with it.
- [File operations] No findings — no filesystem operation is introduced or changed.
- [Subprocess / external command execution] No findings — no command, argument, environment, or subprocess lifecycle changes; this ticket only extends a pure mapping.
- [Cryptographic primitives] No findings — no cryptography or key material is involved.
- [Network & I/O] No findings — no new read path is added; the upstream `parentToolUseID` bound prevents attacker-sized identifiers from reaching this payload, and this adapter deliberately does not establish a competing cap.
- [Error messages, logs, telemetry] No findings — the identifier is added to the client payload only and is not logged, interpolated into errors, or collected as telemetry.
- [Concurrency] No findings — `MapEvent` copies value fields and adds no shared state, lock, channel, or goroutine.
- [Threat model alignment] No findings — the wire comment and tests preserve the existing `ToolUsePayload` rule: clients may use the value only as an inert grouping hint, must tolerate unknown ids at top level, and must not treat a match as authority. Stateful emitter lanes remain out of scope in #2330.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-11

## Revisions

None.
