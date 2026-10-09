# Legacy unread targets (#3029)

## Files read

- `cmd/pyry/legacy_runtime_receipts.go` → `legacyHistoryReader.LatestDisplayableEntryID`, `legacyRuntimeReceipt`: shared list/clamp view and validated receipt projection.
- `cmd/pyry/legacy_runtime_receipts_test.go` → `TestLegacyRuntimeReceipts_CompletedTurn`, `Watermark`, `ReadMarks`, `testLegacyCheckpoint`: real emitter/store coverage and explicit presentation seam.
- `cmd/pyry/history_projection.go` → `legacyHistoryType`, `historyEntryShown`: wire eligibility and raw visibility are different contracts.
- `cmd/pyry/conversation_history.go` → `newHistoryPager`: preserves eligible bytes regardless of visibility; projects validated runtime facts within one bounded raw page.
- `cmd/pyry/relay.go` → `startRelayV2`: both handlers already receive the legacy reader.
- `internal/history/log.go` → `LatestEntryID`, `LatestDisplayableEntryID`, `Page`: validation, containment, raw watermarks and newest-first bounded walking.
- `internal/relay/handlers/list_conversations.go`, `mark_conversation_read.go` → `ListConversationsWithAgents`, `MarkConversationRead`: correlated replies and persisted monotonic clamping.
- `docs/knowledge/features/history-package.md` § Reader, `history-package-producers.md` § Legacy eligibility, `history-package-watermarks.md` § Unread state: receipt accounting grants no sight; raw visibility must remain unchanged.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: assert decoded serialized values and distinguish receipt identity from presentation.
- `docs/protocol-mobile.md` § Security model: authenticated encrypted transport and replay protections remain in force.

## Context

Completed runtime-enabled replies send durable IDs 1–6, with normal turn_end at 5 and idle status at 6. Mobile counts latest 5 and presents checkpoint 6, but the daemon currently clamps to raw-visible assistant delta 4. Legacy targets must follow the client's wire-type classification instead of stored visibility. This is one compatibility deliverable; no decision record is needed. No fetched feature branch overlaps the production/test files.

## Design

Keep `LatestDisplayableEntryID(convID) (uint64, error)` as the shared handler seam. Validate through raw `LatestEntryID`, using zero only for an actually empty log; an all-hidden raw displayable result cannot short-circuit the legacy scan. Walk raw pages of 128 newest-first without caching.

Exactly `turn_state`, `stall`, `api_retry`, `compacting`, and `session_transition` never count, even with explicit shown=true. All other `legacyHistoryType` entries count regardless of shown. A fact accepted by `legacyRuntimeReceipt` counts as its projected banner at the original durable ID, regardless of shown. Unsupported entries (including invalid runtime facts) retain conservative absent/true visibility counting and false exclusion. Counting unsupported content never projects a receipt or establishes presentation.

No changes to raw APIs, payloads, IDs, timestamps, provenance, pager boundaries, publication, recipient gates, client code or future thread semantics. Registry clamping remains max(held, min(presented checkpoint, legacy latest)).

## Concurrency model

No new state, locks or goroutines. Store calls retain their mutex/containment checks; pages remain bounded, and the existing registry serializes atomic persistence. Production emitter and ring remain on their existing lanes.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Runtime-enabled/disabled completed reply; warm/reopened store; repeated overlapping history/live/replay receipt identity | `TestLegacyRuntimeReceipts_CompletedTurn` |
| Normal, failed/stopped completion and nonvisual metadata/runtime tails | `TestLegacyRuntimeReceipts_WireTargets` |
| Repeated mark, held mark above clamp, reload, host/conversation isolation | `TestLegacyRuntimeReceipts_ReadMarks` and `TestLegacyRuntimeReceipts_CompletedTurn` |
| Append after lookup and raw watermark preservation | `TestLegacyRuntimeReceipts_Watermark` |
| Receipt-only repeated bounded pages and no presentation | `TestLegacyRuntimeReceipts_BoundedPages` |
| Unknown, malformed, unidentified receipts or numeric holes | `TestLegacyRuntimeReceipts_Barriers` |

## Error handling

Raw lookup and page errors propagate to existing handlers; nil storage, invalid IDs and containment failures remain errors. Invalid runtime facts retain fallback targets but cannot close presentation holes. No new errors, logs or payload disclosure.

## Testing strategy

Write regressions first and observe incorrect clamps before implementing. Extend existing real-store/emitter fixtures with serialized history/live/replay client-derived latest, explicit completed checkpoint, correlated list/mark replies and persisted marks. Independently mirror the client's five excluded wire types in the test helper. Cover eligible types and four validated facts with absent/false/true shown, excluded types with all visibility states, unsupported visibility fallback, invalid receipts, receipt-only logs, raw metadata preservation and warm/reopened reads.

Run focused race regressions, race tests for `cmd/pyry`, `go vet ./...`, and `go build -o /tmp/builder-3029/pyry ./cmd/pyry`. The verifier owns `make check` and full-module tests, including the ticket's hermetic gate requirement. Dispatcher/mobile #1989 owns fresh full live proof of unchanged `InteractiveStreamE2ETest.interactiveTurn_attentionDot_followsARealTurn`, including phone/peer reads, isolation, permission waiting/answer assertions and existing deadlines. No separate authenticated run; live proof remains pending.

## Open questions

None. The issue supplies the unchanged client's classification and completed-turn contract.

## Sizing

One deliverable; forecast approximately 600 total written lines including plan/tests. Zero new exported types/interfaces, zero signature call-site updates, four acceptance criteria, no new state machine. All five limits rechecked before plan commit.

## Documentation handoff

Pending documentation stage: update `docs/protocol-mobile.md`, “A history entry”, “Joining a page to the live stream” and “Marking a conversation read”; and `docs/knowledge/features/history-package-watermarks.md`, “Unread state uses a separate, lazily recovered watermark (#2954)”. Document legacy wire-type targets, receipt counting and completed-turn clamps, replacing the old stored-visibility/runtime-exclusion claims. Distinguish raw `shown`, received identity, foreground sight and daemon confirmation.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] No findings. `legacyHistoryType` is the transport allowlist and `legacyRuntimeReceipt` verifies ownership/identity. Unsupported fallback counting cannot certify harmless receipt bytes; tests retain holes and malformed receipt barriers.
- [Tokens and cryptography] No findings. The reader handles durable entries only; no credentials, key generation, nonce use or encrypted transport changes.
- [File operations] No findings. `LatestEntryID` validates IDs and rechecks containment before scanning; `Page` retains cursor checks and regular-file rules. Registry persistence remains atomic through `AdvanceReadUpTo`; no new paths or file modes.
- [Subprocesses] No findings. The reader starts no subprocess and interprets no shell input.
- [Network and I/O] No findings. Authenticated dispatch gates are unchanged. Each scan page remains bounded at 128; no new socket or unbounded page allocation.
- [Errors, logs and telemetry] No findings. Errors use existing handler behavior, and no new logs or telemetry expose payloads or metadata.
- [Concurrency] No findings. No additional locks/cache; repeated/overlapping receipts cannot grant presentation, and registry marks remain monotonic and host/conversation isolated.
- [Threat model] No findings. Noise authentication, replay protection and device revocation are unchanged. Receipt counting conveys no permissions or executable input and never infers foreground sight.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-09

## Revisions

- 2026-10-09: The completion-outcome and nonvisual-tail scenarios use the dedicated `TestLegacyRuntimeReceipts_CompletionTails` sibling rather than the visibility matrix in `WireTargets`. The explicit completed version remains presented while metadata/runtime tails extend its checkpoint. `Watermark` also walks beyond 128 excluded entries. Contracts and production scope are unchanged.
