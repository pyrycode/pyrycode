# 2324 — Publish tool-progress heartbeats as v2 frames

## Files read

- `internal/turnevent/event.go` → `ToolProgress` — defines the already-bounded tool-call join id and signed elapsed-seconds value that must cross the wire unchanged.
- `internal/protocol/codes.go` → `TypeToolUse`, `TypeToolDenied`, the v2-only constant blocks — establishes the outbound-only type vocabulary and the totality table obligations for a new constant.
- `internal/protocol/interactive.go` → `ToolUsePayload`, `ToolResultPayload`, `ToolDeniedPayload` — establishes `tool_use_id` as the shared join key and the no-`omitempty` payload discipline.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope`, `TestToolDeniedPayload_RoundTrip`, the zero-value fixture tests — supplies the canonical-byte round-trip and raw-key-presence patterns.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestTypeConstants_V1V2Partition` — exhaustively partitions outbound-only protocol constants from accepted inbound types.
- `internal/turnbridge/outbound.go` → `MapEvent`, `TurnContext`, the `ToolStart`, `ToolUpdate`, and `ToolCallDenied` arms — owns the neutral-event-to-wire translation and supplies conversation/turn addressing.
- `internal/turnbridge/outbound_test.go` → `TestMapEventOutbound`, `TestMapEvent_ToolDeniedCarriesNothingElse` — pins scalar field mapping and exact payload key sets.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`, `emitMapped`, `emit`, `eventKind` — `Handle` is the whitelist currently dropping the event, while `emit` owns the single interactive-capability gate and replay/history fan-out.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_ToolDeniedFansOutToInteractiveOnly` — proves the nearest turn-scoped, lifecycle-neutral event joins a preceding tool row and reaches only interactive clients.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes` — totality-guards relay direction classification and distinguishes push-only frames from request/reply vocabulary.
- `docs/protocol-mobile.md` → “Interactive events”, `tool_use`, `tool_result`, `tool_denied`, `model_announced`, and the four statements reading “nineteen” — defines the public client contract and its heading-derived live-event count.
- `docs/knowledge/features/protocol-package-interactive-event-payloads.md` → no-`omitempty` and fixture guidance — requires explicit zero encodings and byte round trips rather than decoded-value-only assertions.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Protocol boundaries” — requires checking silent type-switch drops, pairwise-distinct fixtures, raw key presence, and remarshal-through-envelope proof.
- `docs/specs/architecture/2323-tool-progress-heartbeat-turn-event.md` → `ToolProgress` producer contract and security review — confirms that the producer drops unusable ids, preserves signed readings, adds no cadence state, and deliberately deferred wire publication here.

## Context

Ticket #2323 turns each matched Claude `tool_progress` heartbeat into `turnevent.ToolProgress`, but `MapEvent` and `interactiveTurnEmitterV2.Handle` still silently drop that known variant. A client therefore cannot update the elapsed counter on the open `tool_use` row. This ticket completes the existing interactive-v2 path with a small push-only frame; it adds no terminal event because `tool_result` already closes the row, and no daemon timer or rate limiter because Claude owns the heartbeat cadence.

No ADR is warranted. The frame follows the established `tool_denied` path—constant, scalar payload, pure mapper arm, turn-scoped emitter arm, and push classification—without introducing a new architectural choice.

## Design

Add `TypeToolProgress = "tool_progress"` as an outbound v2-only protocol constant. It is classified in `v2OnlyTypes` and `excludedTypes` as a push, and is never admitted to the inbound application type set.

Add `protocol.ToolProgressPayload` with four always-present fields in wire order:

| Go field | JSON key | Source |
|---|---|---|
| `ConversationID string` | `conversation_id` | `TurnContext.ConversationID` |
| `TurnID string` | `turn_id` | `TurnContext.TurnID` |
| `ToolUseID string` | `tool_use_id` | `turnevent.ToolProgress.ToolCallID` |
| `ElapsedSeconds int` | `elapsed_seconds` | `turnevent.ToolProgress.ElapsedSeconds` |

No field uses `omitempty`. The tool id deliberately adopts the same `tool_use_id` spelling as `ToolUsePayload`, `ToolResultPayload`, and `ToolDeniedPayload`, so clients join rather than create a second row. The signed integer crosses unchanged: negative and zero readings are upstream facts, not validation failures. The payload carries no session id, UUID, tool name, sequence number, or parent id.

Extend `MapEvent` with a pure `turnevent.ToolProgress` arm returning `TypeToolProgress` and the four-field payload. It applies no cap, clamp, cadence check, or lookup: #2323 already validates the join id at construction, and the bridge supplies only the active conversation and turn identifiers.

Extend `interactiveTurnEmitterV2.Handle` with the turn-scoped lifecycle-neutral sequence used by `ToolCallDenied`: ensure a turn exists, flush any buffered assistant delta to preserve ordering, then call `emitMapped`. It does not call `transitionTo`, mutate tool state, retain the latest reading, deduplicate, or synthesize a close. `emit` remains the single capability gate and the existing ring/history/fan-out path.

Publish a `tool_progress` subsection beside the three tool lifecycle frames in `docs/protocol-mobile.md`, describing its join, signed report semantics, cadence, absence semantics, and non-actuating trust posture. Adding its `####` heading changes the heading-derived live-event count from nineteen to twenty in all four live statements: the Interactive events introduction and the `session_transition`, `model_list`, and `slash_command_list` distinctions.

## Concurrency model

No goroutine, timer, channel, lock, shared field, or shutdown path is added. `MapEvent` remains pure and synchronous. `Handle` continues to run on its existing single event goroutine, and forwards each heartbeat independently without retaining counter state.

## Error handling

- A progress event with no active conversation follows the existing no-cursor drop before the type switch; neither id nor elapsed value is logged.
- Once a cursor exists, every `ToolProgress` maps successfully, including zero and negative elapsed readings.
- JSON marshaling, history append, and connection push failures use the emitter's existing handling; the new scalar payload introduces no new error path.
- A heartbeat that arrives without a client-visible matching `tool_use` remains a harmless unjoined report. The daemon does not act on the id or invent a row, and the producer already drops malformed or over-cap ids.

## Testing strategy

- Add populated and all-zero committed envelope fixtures. The populated fixture uses distinct sentinel values and the round-trip test asserts every decoded field before remarshal. The zero fixture inspects all four raw payload keys before decoding and byte-round-tripping, so `omitempty`, tag renames, additions, and declaration-order changes fail.
- Add `TypeToolProgress` to the protocol v2-only partition and relay push classification totality tables.
- Add a `TestMapEventOutbound` row with distinct conversation, turn, tool id, and signed elapsed values, plus an exact-key-set test proving no session id, UUID, tool name, sequence, or parent id leaks into the payload.
- Add an interactive-emitter test that sends a `ToolStart` followed by a negative `ToolProgress`, joins their decoded `tool_use_id` and `turn_id`, asserts one progress frame on the interactive connection, and asserts none on a non-interactive connection. The negative value distinguishes pass-through from clamping or daemon-computed time.
- Run RED before production edits with the new protocol, bridge, emitter, compatibility, and relay tests. Then run `go test -race ./internal/protocol/... ./internal/turnbridge/... ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The preceding ticket fixed the producer's id, signed-number, cadence, and lifecycle contracts; the existing `tool_denied` lane settles the turn-scoped emitter shape.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — Claude subprocess output crossed the explicit decode and id-boundary in `consumeToolProgress` before becoming `turnevent.ToolProgress`; this design only maps that typed, bounded report and does not elevate it into trusted state.
- [Tokens, secrets, credentials] No findings — `ToolCallID` is an opaque join handle, not an authorization token; it is neither generated nor persisted here, and the plan keeps it out of logs and errors.
- [File operations] No findings — production behavior adds no filesystem read or write; committed fixtures are fixed repository paths read only by tests.
- [Subprocess execution] No findings — no command, argument, environment, signal, or child lifecycle changes; the frame only reports output from the already-running child.
- [Cryptographic primitives] No findings — no key material, randomness, hashing, encryption, nonce, or secret comparison changes.
- [Network and I/O] No findings — the payload is four bounded scalars and travels through `emit`, which retains the existing interactive-capability gate and envelope-size enforcement. No inbound handler, socket read, connection, timeout, or resource pool is added.
- [Errors, logs, telemetry] No findings — `eventKind` already emits only the package-owned literal `tool_progress`; the new mapping and handler add no error text or telemetry containing the Claude-authored id or elapsed reading.
- [Concurrency] No findings — mapping and forwarding stay synchronous on the emitter's existing single goroutine, with no retained progress state or check-then-mutate sequence.
- [Threat model alignment] No findings — the frame is an encrypted, capability-gated report and never an actuator. A compromised Claude can forge an elapsed reading or reuse another bounded tool id, causing only misleading client display; neither daemon nor client may treat it as authority, timing evidence, or permission to act.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10

## Revisions

None.
