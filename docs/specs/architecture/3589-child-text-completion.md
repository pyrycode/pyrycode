# Intrinsic completion of Agent reply text

## Files read
- `internal/streamsup/parser_assistant.go` → `emitAssistant`: settled assistant text currently loses its complete-block provenance.
- `internal/streamsup/parser_stream_event.go` → `emitStreamEvent`: partial text and message endings are distinct; endings without correlated text cannot complete children.
- `internal/turnevent/event_turn.go` → `TextChunk`, `internal/turnbridge/outbound.go` → `MapEvent`, `internal/protocol/interactive.go` → `AssistantDeltaPayload`: producer-to-history boundary.
- `cmd/pyry/interactive_turn_v2.go` → `handleForSource`, `flushDelta`, `deltaAddress`: retained child lanes and ordered coalescing, including #2960.
- `internal/thread/child.go` → `childWork`, `resolveChildren`, `restoreChild`: intrinsic state must precede derived ancestor interruption.
- `internal/thread/main.go` → `foldWork`, `closeText`: text runs and finality.
- `internal/thread/cache.go` → `foldingRules`: incompatible persisted projections must refold.
- `internal/thread/checkpoint_resume_test.go` → `testCheckpointResume`: continuation proof across every cut.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → recorder/scanner; `parent_tool_use_capture_test.go` → authenticated persistent runner pattern.
- `docs/knowledge/features/thread-package-agents-and-background-work.md` § Parent repair: preserve scoped identity and reversible parent endings.
- `docs/knowledge/features/development-verification-captures-and-live-evidence.md`: rig witness, loss accounting and artifact survival.
- `docs/knowledge/features/streamsup-package-turn-io-envelope-write-stdout-parser.md`: production requests forwarded child text and partial messages.

## Context
Finished background Agent prose is owned correctly but remains intrinsically active. Ancestor termination consequently projects it as interrupted. The foreground capture proves a complete attributed `assistant.message.content` text block (with a message ID and assistant role); `stop_reason` is null and cannot serve as the completion criterion. Background confirmation and its scrubbed replay artifact remain mandatory dispatcher-owned live evidence. No new decision record is needed.

## Design
Retain a `text_complete` fact on `TextChunk` and `AssistantDeltaPayload` only for a validated complete attributed assistant text block. Main text and incremental deltas keep existing behavior. The emitter flushes prior buffered text in order and publishes complete child text immediately, splitting oversized text with the completion marker only on its last piece. The source/message/parent correlation is established by that complete envelope, rather than parent task success.

After `childWork` folds and registers that text, `closeText` records intrinsic done/inactive state before `resolveChildren`. Existing reversible ancestor closure and ownership validation stay intact. Completion creates no independent item or main turn; older raw history without the fact retains existing behavior. Increment `foldingRules` to reject old cached/checkpoint projections.

Reuse the bounded stdout recorder in an authenticated persistent runner. Stage an explicit background Agent behind a rig-owned release file: release it only after the launching main result, then require attributed final prose after release. Retain original raw lines only in a private artifact directory. Publish a content-free report and an allowlisted, rig-authored JSONL replay (synthetic stable IDs and prose, original observed event shapes/order), with Claude version, candidate revision, source indices and loss counts. Fail if the genuine boundary, loss-free capture or independent completion cannot be established. The dispatcher runs this scenario; artifacts return to the builder for commitment and recorded replay proof.

Sizing after design: one deliverable, approximately 720 written lines including plan, at most two local test record types and no new exported types/interfaces, one consumer path, four acceptance criteria, fewer than ten reject branches. No other fetched feature branch overlaps the planned production files.

## Concurrency model
Parser, emitter and fold keep their existing serialized ownership. The live rig joins its runner goroutine on cancellation; the release-file witness needs no extra goroutine.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Main turn ends before final child text; Agent ends afterward | `TestChildTextCompletionBoundary`, parser/emitter boundary test |
| Parent ends before complete text, then a late task link repairs the parent | `TestChildTextCompletionRepair` |
| Partial/malformed/other-child evidence and absent parent | `TestChildTextCompletionNegative` |
| Checkpoint before/after completion and suffix replay | `TestChildTextCompletionCheckpoint` |
| Reused source/parent identifiers | existing `TestChildLifetimeReuse` and `TestChildScopes` |

## Error handling
Malformed or uncorrelated source shapes never set the fact. Incomplete legacy text remains subject to ancestor interruption. Live recording fails closed on dropped/truncated data, missing main/result boundary, missing attributed text or unsafe publish bytes; logs contain counts only.

## Testing strategy
Write failing fold and production-parser/emitter boundary regressions first. Assert identity, session, turn, exact text, raw order, no duplicate prose or synthetic main turn. Test source negatives, late-link repair and every checkpoint cut. Run affected race tests, `go vet ./...`, `go build ./cmd/pyry`, and compile the tagged live package. Full module and genuine live acceptance are dispatcher-owned and pending.

## Open questions
- Background capture must confirm the complete attributed assistant block as intrinsic evidence; if it differs, revise the contract on artifact return before acceptance.
- Commit the recorded replay input and rerun its offline regression after the dispatcher capture.

## Documentation handoff
Pending documentation stage: in `docs/protocol-mobile.md`, “Full thread item”, and `docs/knowledge/features/thread-package-agents-and-background-work.md`, “Parent repair”, state which observed source fact independently completes child prose, how it survives replay/reopen, and that ancestor completion cannot complete unfinished children. Describe `assistant_delta.text_complete` in its owning protocol section, including absence behavior for older history.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] SHOULD FIX: `emitAssistant` must require the attributed assistant/message/text shape; arbitrary stop-looking strings, partials and task outcomes cannot assert completion.
- [Tokens, errors and logs] Capture reports must contain only counts and provenance. Original stdout stays private; publish only rig-authored allowlisted replay values and deny-scan the exact published bytes.
- [File operations] The rig creates a private directory and release file with 0700/0600 modes. No model-supplied path is opened by the recorder. Existing checkpoint atomicity is reused.
- [Subprocesses] Reuse the authenticated runner and fixed argv. Rig prompts run only in its empty work directory. Cancel and join the runner on every exit.
- [Cryptography] No key, nonce or cryptographic change; synthetic replay identifiers carry no authority.
- [Network and I/O] Existing stdout recorder caps remain authoritative; any loss refuses fixture promotion. No new network endpoint.
- [Concurrency] Existing parser/emitter/fold serialization stays unchanged; the live runner has a joined shutdown path.
- [Threat model] Parent ownership remains a scoped daemon fold decision; no client completion inference or capability is introduced.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-18
