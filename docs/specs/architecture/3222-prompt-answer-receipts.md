# #3222 — Account for remote prompt answers on legacy routes

## Files read
- `cmd/pyry/prompt_answer_history.go` → `recordPromptAnswer`, `promptAnswerFact`, `boundPromptAnswerProjection`: winning resolution, captured ownership and 16 KiB context cap.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge`, `resolveCapturedStreamDecision`: immutable owner snapshots and registry winner arbitration.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyRuntimeReceipt`, `publishLegacyHistory`, `legacyHistoryReader.LatestDisplayableEntryID`: one receipt boundary and existing legacy accounting target.
- `cmd/pyry/conversation_history.go` → `newHistoryPager`: projects one raw page without filling holes or changing boundaries.
- `cmd/pyry/relay.go` → `startRelayV2`: singleton history, shared replay ring and manager access gates.
- `cmd/pyry/prompt_answer_history_test.go` → permission/question, storage, retirement and losing-resolution tests: preserve child outcomes and captured identity.
- `cmd/pyry/operator_message_v2.go` → `notifyReceipt`: bounded mailbox can drop publication, so cannot guarantee this ticket's successful-append receipt contract.
- `docs/knowledge/features/history-package.md`, `history-package-producers.md` § Resolved remote prompt answers, `history-package-producers-legacy-compatibility.md` § Legacy eligibility: visibility is not transport permission; absent provenance must remain absent.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries and test execution: assert exact fields and named counts.
- `docs/protocol-mobile.md` § A history entry, Joining a page to the live stream, Security model; `CODING-STYLE.md`: receipt neutrality, durable joins and concurrency conventions.

## Context
The legacy pager omits retained prompt answers, breaking completed-turn continuity. This single deliverable accounts for validated original answer IDs across history/live/replay without exposing raw prompt content. Mobile #2098 owns live notification proof. No new decision record is needed.
Overlap: #3164 adds separate thread wiring in `startRelayV2`; build through with local additions.
Sizing: approximately 650 total written lines, zero exported types/interfaces, two wiring edits, four acceptance criteria, no new resolution state-machine branches; receipt validation has fewer than ten reject groups. Within all five limits.

## Design
- Add answer-specific validation to `legacyRuntimeReceipt`, retaining raw exclusion from `legacyHistoryType`. Require producer fields (including explicit booleans), original correlation, matching conversation, exactly remote source, nonzero resolved timestamp equal to entry time, known decision/behavior pairs and no denied session grant.
- Validate context as a producer-shaped object bounded by `maxPromptAnswerProjection`; reject malformed/unknown context fields and invalid question/value shapes. Optional asking session and provenance stay optional. Visibility never participates in validation.
- Reuse the five-field `BannerPayload`; pager and legacy latest automatically consume the same adapter.
- Add an optional private publisher dependency to the bridge without changing its constructor. A dedicated publisher owns its mutex, serial envelope counter and shared replay ring. `recordPromptAnswer` serializes its existing single append and publication using this publisher, outside bridge/registry locks, and publishes only a validated successful append through `publishLegacyHistory`.
- `startRelayV2` installs the dependency and shared ring before manager workers run. The existing broadcaster applies interactive, conversation membership and agent-access gates; record into replay before fanout, even without recipients. No changes to raw payload, metadata, thread folding, child verdict or dismissal.

## Concurrency model
No new goroutines. Concurrent answer handlers use the dedicated publication mutex across append and fanout; bridge and registry mutexes are released before this path. Peer delivery never owns the bridge/registry mutex. Ring/storage retain their existing internal locking and shutdown behavior.

## State transitions and identity reuse
| Event | Race-detector coverage |
| --- | --- |
| Winning permission allow/deny/cancel; duplicate resolution | `TestPromptAnswerHistoryPermission`, `TestPromptAnswerReceiptsPublication` |
| Question answer/refusal; losing expired/consumed answer | `TestPromptAnswerHistoryQuestion`, `TestPromptAnswerHistoryLosingResolution` |
| Original session/conversation rebound; retirement; reused tool ID | `TestPromptAnswerHistoryPermission`, `TestPromptAnswerHistoryRetirement` |
| Concurrent different answers and duplicate attempt | `TestPromptAnswerReceiptsConcurrent` |
| No connected clients, later replay; reopened store | `TestPromptAnswerReceiptsPublication`, `TestPromptAnswerReceiptsPages` |
| Nil/failed append | `TestPromptAnswerHistoryStorage` |
No new lifecycle state or persistent identity allocator.

## Error handling
Invalid facts remain holes. Nil/failed storage emits no receipt, preserves resolution/dismissal and never retries. Existing content-free marshal/append logs and peer delivery handling remain in force.

## Testing strategy
Write receipt assertions and real-store page regressions first and observe failure. Cover all decision pairs, independently malformed/missing/foreign fields and durable identity checks; receipt-only and mixed pages with all visibility states and reopened storage; original limits/cursors/at_start; exact five-field bytes and latest accounting. Exercise winning resolvers, duplicate/losing outcomes, live/replay equality, disconnected replay, concurrency and failed storage. Run focused named daemon tests under race detection, then `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-3222/pyry ./cmd/pyry`. Record named counts and final commit in PR. Full-module suite belongs to verifier.

## Open questions
None. Mobile #2098's real Claude/background/FCM/private preview/redaction/tap gate remains pending and is not replaced by daemon tests.

## Documentation handoff
Pending for documentation stage:
- `docs/protocol-mobile.md`, “A history entry” and “Joining a page to the live stream”: include validated remote prompt-answer receipts in the supported accounting set, with their validation boundary, identical history/live/replay bytes and durable identity, and no presentation/read-mark authority.
- `docs/knowledge/features/history-package-producers.md`, “Resolved remote prompt answers (#2973)”, and `docs/knowledge/features/history-package-producers-legacy-compatibility.md`, “Legacy eligibility and explicit visibility (#2965)” and its compatibility matrix: replace the claim that answers are excluded from all receipts with the safe receipt contract; retain raw-payload exclusion and optional original provenance.

## Security review
**Verdict:** PASS
**Findings:**
- [Trust boundaries] `legacyRuntimeReceipt` validates stored facts before accounting, including explicit required booleans and bounded typed context. Neither shown metadata nor current routing grants eligibility.
- [Tokens, secrets] The receipt reconstructs only the five banner fields; correlation/session, prompt context, raw answers and provenance cannot cross. No credentials are created or changed.
- [File operations] Existing `appendConversationHistory` and store containment/permissions remain the only filesystem boundary; no new path construction.
- [Subprocesses] Child verdict and input remain unchanged; no new subprocess invocation.
- [Cryptography] Existing authenticated manager delivery remains in use; no primitive/key/nonce changes.
- [Network/I/O] The 16 KiB encoded context bound precedes context decoding. No socket/read/header policy changes; encrypted peer delivery retains existing limits and access gates.
- [Errors/logs] Preserve fixed event/reason logging and never print validation payloads or storage errors.
- [Concurrency] Dedicated publication locking avoids racing counters and ordering concurrent answers; bridge/registry locks cannot span fanout. No worker/shutdown additions.
- [Threat model] Existing Noise authentication, per-device access and agent gates prevent widened access; receipts give no prompt actuation, presentation or read-mark authority. Mobile #2098 owns the pending notification privacy gate.
**Reviewer:** builder (self-review)
**Date:** 2026-10-12
