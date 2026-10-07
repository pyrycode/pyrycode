# #2936 — Keep background thinking out of the main turn

## Files read

- `internal/turnevent/event.go` → `ThoughtChunk`, `ThinkingProgress`, `TextChunk`: neutral event attribution contract.
- `internal/streamsup/parser.go` → `emitAssistant`, `emitStreamEvent`, `emitThinkingProgress`, `parentToolUseID`: three producers and bounded, failure-isolated parent validation.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor`, `observe`, `observeMark`: classification precedes mutation, including Send now carry/grace.
- `cmd/pyry/interactive_turn_v2.go` → `HandleFor`: published lifecycle is independent of busy tracking.
- `internal/streamsup/parent_tool_use_test.go`, `parser_stream_event_test.go`, `parser_test.go`: validation, content-free deltas and progress rate-bound coverage.
- `cmd/pyry/stream_turn_busy_test.go`, `interactive_turn_v2_test.go`, `interactive_turn_v2_initial_thinking_test.go`: idle/open lifecycle and parser-to-consumer fixtures.
- `internal/e2e/relay_v2_stream_parent_events_test.go` → `TestRelayV2_StreamParentActivityDoesNotHoldQueuedProbe`: replay gate, stdin receipt and child continuity proof.
- `docs/knowledge/features/streamsup-package.md`, `streamsup-package-per-conversation-turn-busy-tracking.md`, `streamsup-package-per-conversation-turn-busy-track-send-now-carry.md`: main thinking must remain an opener; ignoring an event must also preserve carry generation.
- `docs/knowledge/features/e2e-harness.md`, `e2e-harness-stream-interactive-harness-pattern-startstreamin.md`, `development-verification.md`: require processing barriers and separate idle/open assertions.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: testing conventions and existing trust model.

## Context

After the main result, background Agent thinking loses its parent attribution and reopens both the delivery busy mark and published main turn. No later main result need arrive, so ordinary queued messages can remain held. This completes the thinking remainder of #2781 without changing the mobile payload shape. No decision record is needed.

One deliverable: background thinking cannot hold or change the main turn. Estimated written work is 450–550 lines including tests and this plan, with no new exported types/interfaces, two consumer sites, four acceptance criteria, and no new state-machine reject branches. Codegraph impact omits construction sites; source search confirms three streamsup producers and one empty-parent Codex producer. These counts remain within all five limits after writing this plan. Remote feature-branch inspection found no overlap with the planned production files or regression harness.

## Design

Add `ParentToolCallID string` to `ThoughtChunk` and `ThinkingProgress`, matching `TextChunk`. Full assistant thinking uses the already-decoded parent. Partial-message and numeric progress decode the emitting line's parent as `json.RawMessage` and use `parentToolUseID`. Missing, invalid and oversized values degrade to empty without rejecting otherwise valid thinking. Never persist a previous line's parent. Partial thinking remains content-free; numeric progress values and the existing accumulator/rate gate remain unchanged.

`turnMarkFor(Event) turnMark` returns none for either thinking variant with a nonempty parent, before `observe` resolves or mutates tracker state. Empty-parent thinking remains an opener, including notification-started turns; `TurnEnd` stays the closer. The tracker retains no new IDs, content or time data.

`interactiveTurnEmitterV2.HandleFor` ignores parented thinking before starting a turn, flushing deltas, changing phase or publishing progress. Empty-parent thinking keeps the current lifecycle and publication contract.

## Concurrency model

No new goroutines, locks or timers. Parser events continue through the existing FIFO drain, which updates the synchronized tracker before the single-owner emitter. Existing Send now grace generation and timer lifetime remain unchanged.

## Error handling

Reuse `parentToolUseID` validation and its existing bound. Parent decode failures remain isolated from numeric and content decoding. Existing malformed delta rejection, parse buffer caps, numeric validation and progress rate bound remain intact. No new error or logging paths.

## Testing strategy

- Table-driven parser coverage for all three producers: valid parent, missing/empty parent, invalid JSON shapes, exact cap and oversized value. Feed parented then unparented lines through the same parser; assert content-free deltas and exact progress values. Retain existing rate-bound tests.
- Extend parent-attributed tracker scenarios to both thinking variants. Prove idle/open membership and broadcast preservation, Send now carry and held-close generation preservation, and successful grace release. Prove notification then empty-parent thinking opens and a main result closes.
- Emitter tests for both variants from idle and an open responding turn: no frames, no new identity/phase, no buffer flush. Existing main-thread state/progress assertions remain in force.
- Extend the #2781 hermetic replay cases with each thinking producer after the main result, followed by a parented tool sentinel in the same FIFO drain. Receive that sentinel before sending the ordinary probe. No result/completion is replayed; require stdin receipt and echo within five seconds and the same PID/restart count, with no interrupt in thinking cases. Run the new regression against the pre-fix daemon to establish failure.
- Run scoped race tests for `internal/streamsup`, `internal/turnevent`, `cmd/pyry`, and the targeted `e2e` regression; run `go vet ./...` and `go build ./cmd/pyry`. The verifier owns the full-module gate.

## Open questions

None. Parent validation deliberately retains the existing empty-parent fallback contract.

## Documentation handoff

Pending for the documentation stage:

- `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-tracking.md`, under “Per-conversation turn-busy tracking”: replace the unattributed-thinking gap with parent-attributed thinking not affecting the main busy mark or published lifecycle; empty-parent thinking remains an opener, including notification-started main turns.
- `docs/protocol-mobile.md`, under `thinking_progress`: state that the daemon publishes this progress and thinking `turn_state` only for main-thread thinking; parent-attributed subagent thinking does not change the main turn. The wire payload shape is unchanged.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Child stdout remains untrusted. `parentToolUseID` validates shape and bounds per emitting line; raw parent fields isolate malformed attribution from valid content/progress. Classification does not authorize operations.
- [Tokens, secrets, credentials] No credential handling changes. Partial thinking has no content field in its decode target; full thought text stays internal and is never published by `HandleFor`.
- [File operations] No production file operations added; replay fixtures use the existing test temp-directory lifecycle.
- [Subprocesses] No command, environment or shutdown changes. The regression reuses the existing fake child and verifies it stays running.
- [Cryptography] No key, nonce, random generation or comparison changes; existing paired fake-phone transport is reused.
- [Network and I/O] Existing parser buffer and parent-ID cap bound inputs; progress retains its rate gate. No new socket reads, payload fields or connection paths.
- [Errors, logs, telemetry] No new logs or errors. Parent IDs and thinking content are not added to tracker state or log fields.
- [Concurrency] `turnMarkNone` returns before tracker mutation, preserving carry/grace; emitter guards precede turn/buffer side effects. Existing lock ordering and shutdown paths remain intact.
- [Threat model] This fixes child-output lifecycle confusion within the existing authenticated relay model. `docs/protocol-mobile.md` § Security model's authentication, replay and prompt-injection mitigations are unchanged; no new threat deferral is introduced.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-07

## Revisions

- 2026-10-07: The scoped race run found `TestStreamTurnDrainV2_AttributedTextExcludesThinkingAndSignature` still expected the now-forbidden parent thinking state. Update its expected four envelopes and pin their types while retaining its confidentiality assertions. This is an inherited expectation change under the planned lifecycle contract, with no production design change. Also update the event and parser-test commentary that assumed numeric progress had no parent field.
