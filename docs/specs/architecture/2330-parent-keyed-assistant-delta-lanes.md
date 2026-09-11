# Parent-keyed assistant-delta lanes

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2`, `Handle`, `startTurnIfNeeded`, `endTurn`, `flushDelta`, `emitMapped`, `emit` — owns the outer-turn lifecycle, assistant-text coalescer, frame splitting, replay-ring append, durable-history append, and interactive fan-out that lane isolation must preserve.
- `cmd/pyry/interactive_turn_v2_chunk_test.go` → `TestInteractiveTurnEmitterV2_OversizedDeltaFitsEnvelopeCap`, `TestInteractiveTurnEmitterV2_UnderCapDeltaEmitsExactlyOne` — pins the existing per-frame split and main-lane sequence behavior that attributed text must reuse unchanged.
- `cmd/pyry/interactive_turn_v2_switch_test.go` → `TestInteractiveTurnV2_SwitchResetsTurnIdentity`, `TestInteractiveTurnV2_SwitchFlushesAbandonedDeltaBeforeNewState` — pins flush-before-switch ordering and fresh identity after an active-conversation change.
- `cmd/pyry/interactive_turn_v2_test.go` → `fakeInteractiveBcast`, `assistantDeltas`, `pushTypes` — supplies the hermetic envelope capture and decoding helpers for the new lane tests.
- `internal/turnbridge/outbound.go` → `MapEvent` — already copies `TextChunk.ParentToolCallID` to `AssistantDeltaPayload.ParentToolUseID`; the emitter must keep using this mapping rather than introducing a second payload path.
- `internal/protocol/interactive.go` → `AssistantDeltaPayload` — defines the wire fields whose lane identity, sequence, parent attribution, and text are asserted.
- `docs/knowledge/features/turnbridge-package.md` → `MapEvent` and `TurnContext` — documents that turn addressing is an emitter-owned lifecycle decision and that text attribution now crosses the mapper verbatim.
- `docs/knowledge/features/development-verification.md` → source-reading and protocol-proof guidance — requires current source plus marshalled-payload evidence rather than relying on declarations alone.

## Context

`turnevent.TextChunk` and `turnbridge.MapEvent` now carry a bounded assistant line's parent tool-use id, but `interactiveTurnEmitterV2` still assigns every assistant delta the main outer turn's `turnID` and `seq`. Its single coalescing key is only the Claude message id. Forwarding attributed subagent text through that state would therefore merge child prose with the parent's text or publish several children in one ordered lane.

This ticket makes the emitter lane-aware without enabling subagent-text production. The empty parent id remains the established main lane. Each non-empty parent id identifies a child lane only within the current outer turn. Ticket #2331 owns enabling `--forward-subagent-text` and live-Claude proof. No ADR is needed: this is the local state-machine implementation of the parent-attribution wire contract already established by the preceding split tickets.

## Design

Add an unexported assistant-delta lane value holding a turn id and next sequence number. `interactiveTurnEmitterV2` retains its existing main `turnID` and `seq` fields so lifecycle frames and the unchanged main-thread path keep their established addressing. A map keyed by non-empty parent id holds child lane values for the lifetime of one outer turn.

The emitter resolves delta addressing as follows:

- Empty parent id returns the existing main `turnID` and `seq`.
- A previously seen non-empty parent id returns its stored child turn id and next sequence.
- The first text for a non-empty parent id mints a conversation-style id, stores sequence zero, and reuses that lane for later text in the same outer turn.
- Mint failure drops that child text event using the existing content-free warning posture. It does not buffer prose that could later be emitted under a wrong identity.

The coalescing buffer gains the parent id captured when buffering starts. Text appends only when both captured parent id and message id match the arriving `TextChunk`. A mismatch in either field flushes the earlier buffer before the later text is buffered. The emitter remains a single active buffer, rather than one buffer per lane: this preserves global arrival order when main and child streams interleave while still allowing same-lane, same-message chunks to coalesce.

`flushDelta` captures text, conversation id, message id, and parent id before clearing state. Each size-bounded chunk is reconstructed as a `turnevent.TextChunk` carrying both the message id and parent id, then sent through `emitMapped`. Address resolution supplies the lane turn id and sequence to `turnbridge.MapEvent`; successful emitted chunks advance only that lane's counter. Consequently the established `splitDeltaText`, `emit`, event-ring append, durable-history append, droppable classification, and connection fan-out remain the only frame path.

`endTurn` clears all child lane entries along with closing the main outer turn. It also clears the main identity and counter after all turn-end frames have been emitted. The existing `Handle` ordering remains load-bearing: `TurnEnd` flushes text before its mapped end and idle frames, while an active-conversation switch flushes against the captured old conversation before calling `endTurn`. The next outer turn or conversation therefore mints a fresh main id, lazily mints fresh child ids, and begins every lane at sequence zero.

Data flow:

```text
TextChunk(parent, message)
  → flush prior buffer when (parent, message) changes
  → resolve main or child lane identity
  → buffer text and captured keys
  → boundary / timer / TurnEnd / switch
  → splitDeltaText
  → emitMapped → emit → ring + history + interactive fan-out
```

## Concurrency model

No goroutine, channel, lock, or timer ownership changes. `Handle` and timer-driven `flushDelta` continue to run serially on the stream drain's single goroutine. The child-lane map, lane counters, and captured parent id follow the same single-owner rule as the existing lifecycle and coalescer fields. `eventring.Ring` and `history.Store` keep their existing internal synchronization and are reached only through `emit`.

## Error handling

- Main turn-id mint failure keeps the existing behavior in `startTurnIfNeeded`: warn without application content and leave the turn closed for a later retry.
- Child lane-id mint failure warns using only event and conversation discriminants, drops that attributed text event, and leaves no partial child-lane or coalescer state.
- Payload mapping, marshaling, replay append, history append, and connection-push errors retain their current handling because attributed deltas use `emitMapped` and `emit` unchanged.
- Empty text follows the current coalescer behavior. It can establish no buffered prose and emits no empty delta.

## Testing strategy

Add hermetic `cmd/pyry` tests beside the emitter tests, using `fakeInteractiveBcast` to inspect marshalled assistant-delta payloads.

- Interleave main, child A, main, child A, and child B chunks across shared and distinct message ids. Assert global text order, unchanged parent ids, distinct stable turn ids per lane, and independent sequences beginning at zero.
- Send same-lane same-message chunks and then change only the message id; assert coalescing and message-boundary flush remain unchanged. Change only the parent id while retaining the message id; assert the earlier lane flushes before the later one.
- Exercise timer flush for attributed text and then a later chunk in the same child lane; assert the stable child turn id and next sequence.
- Buffer child text before `TurnEnd`; assert it precedes turn-end and idle frames. Start a new outer turn and assert fresh main and child turn ids, sequence zero, and no prior text or parent attribution.
- Buffer attributed text before an active-conversation switch; assert the old delta precedes the new conversation's responding frame, then assert fresh identities and counters in the new conversation.
- Attach an in-memory history store where practical and inspect the event ring to prove attributed deltas traverse the existing retention paths; retain existing tests as the unchanged main-thread, splitting, and droppable-path proof.

Run the required touched-scope gate:

- `go test -race ./cmd/pyry/...`
- `go vet ./...`
- `go build ./cmd/pyry`

## Open questions

None. The ticket contract fixes the lane key, lifetime, ordering, and downstream path; the existing single-owner emitter model resolves the storage and synchronization choices.

## Documentation handoff

The ticket contains no Documentation handoff section and specifies no shared-document edit. Protocol and feature documentation remain owned by the later documentation stage; this builder changes only the architecture plan, production code, and tests.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — parent ids and prose cross from Claude subprocess output into the emitter only after `internal/streamsup` constructs a bounded `turnevent.TextChunk`; this design neither reparses nor weakens that boundary and `turnbridge.MapEvent` remains the sole wire mapping.
- [Tokens, secrets, credentials] No findings — lane turn ids are opaque routing identifiers minted through `conversations.NewID`, the existing main-turn mechanism. Neither ids nor prose are credentials, and the new failure path logs no parent id or text.
- [File operations] No findings — the lane state performs no file or path operation. Durable persistence remains encapsulated by the existing `appendConversationHistory` path in `emit`.
- [Subprocess execution] No findings — the change consumes already-produced events and neither starts a process nor changes arguments, environment, or signal handling. Ticket #2331 alone changes the Claude forwarding flag.
- [Cryptographic primitives] No findings — no primitive is introduced. Lane identifiers reuse the crypto-random `conversations.NewID` source already used for main turns.
- [Network & I/O] No findings — no read boundary, timeout, or connection limit changes. Attributed deltas retain `splitDeltaText` and `maxDeltaTextBytes`, then use the existing `emit` fan-out and envelope path.
- [Error messages, logs, telemetry] No findings — child-id mint failure follows `startTurnIfNeeded`'s content-free warning pattern and must not include the untrusted parent id or assistant text. All downstream failures retain `emit`'s no-application-output logging rule.
- [Concurrency] No findings — lane state is confined to the emitter's existing single-owner goroutine. No lock, goroutine, channel, check-then-mutate race, or shutdown path is added.
- [Resource exhaustion] No findings — the map necessarily retains one small entry per distinct bounded parent id to meet stable per-lane sequencing, and its lifetime is bounded by `endTurn`; the change adds no second prose buffer and preserves the existing per-frame byte cap. A cardinality cap would contradict the requirement that every distinct parent receive a lane and has no observed failure motivating code-level enforcement.
- [Threat model alignment] No findings — this relay-facing change preserves capability filtering, event classification, envelope-size bounds, replay retention, and durable history. Enabling subagent forwarding and its live-Claude evidence are explicitly out of scope under ticket #2331.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

## Revisions

None.
