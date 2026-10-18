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

## Revisions
- 2026-10-18: the source predicate is restricted to one nonblank text block with assistant role and a nonempty message ID, matching the inspected foreground shape. Suppressed partial-message companions cannot assert completion. The fold saves the completing raw entry under `completion` using its existing history-reference recipe, including when completion follows provisional ancestor interruption. Disk checkpoint reopen joins the every-cut continuation proof. The first artifact return must also remove the recorded reader's initial missing-fixture skip; the genuine live scenario always runs regardless of fixture presence.
- 2026-10-18 (verifier findings 1–3): sealed-source splits carry completion only on the last piece, proved by `TestInteractiveTurnEmitterV2_SealedChildCompletionSplit` through the fold. Release is serialized with recorder writes and records the next source index; final prose must follow that boundary and a successful child tool result echoing a rig-only release token. Pre-release finals, wrong tools and failed results are negative controls. Replay preserves every assistant/user/result/stream_event line and parser-relevant task evidence in observed order, with stable synthetic identities and rig-authored content. Two persistent parsers compare normalized relevant events at every source index before promotion, refusing omissions or scrubbing that change parsing. The recorded regression consumes the full sequence, checks actual child identity/order and terminal content without synthetic task outcomes. These repair the existing completion invariant and its one-consumer capture proof; no independently useful behavior is added. Live capture and artifact commitment remain dispatcher/return-stage work.
- Repair sizing: source `be88f46c` against main `fef0f53f` measures 1,403 additions plus 20 deletions (1,423 written lines), above the 800-line ceiling; the earlier approximately 1,100-line estimate was low. Under Builder handbacks, “Check the floor”, the capture/replay plumbing stays with its sole completion consumer and regression: splitting it would leave this bug fix without its required background proof. The additional fallback changes repair the required live scenario’s single-attempt staging and explain rejection without weakening validation. No new exported types/interfaces or simultaneous consumer changes. The four acceptance criteria and production reject-branch count remain unchanged.
- Security recheck: release uses an unpredictable rig-only token and the recorder mutex; only its fixed replacement is published. Every captured line is replayed, with no line omissions. The allowlist removes non-thread inventories and result accounting; comparison retains turn outcomes, ownership, tool results and text events at each source index. `TestBackgroundReplyReplayTaskContext` covers task start/end context and inert result accounting; `TestBackgroundReplyReplayOpenPartial` proves stream-block state is retained. `testRecordedCheckpointResume` now checks every recorded-history cut and suffix.
