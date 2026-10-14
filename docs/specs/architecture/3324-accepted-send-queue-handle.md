# Accepted-send queue handle

## Files read

- `cmd/pyry/queued_send_history.go` → `accepted`, `queuedSendFact`, `validQueuedSendFact`: safe acceptance producer and receipt validation.
- `cmd/pyry/startup_send_history.go` → `readStartupSends`, `closeStartupSends`: restart loss joins by durable acceptance ID.
- `internal/thread/sends.go` → `sendFact`, `deliveredSendContent`: permanent acceptance identity and terminal content merge.
- `internal/thread/fold.go` → `decode`, `validRecordedFields`: typed scalar validation and inert copied content.
- `internal/thread/cache.go` → `foldingRules`: semantic cache compatibility.
- `cmd/pyry/queued_send_history_test.go`, `cmd/pyry/daemon_thread_queries_test.go`: queue lifecycle and authenticated query seams.
- `internal/thread/sends_test.go`, `internal/thread/cache_test.go`: replay partitions, terminal neutrality and incompatible cache coverage.
- `docs/knowledge/features/thread-package-main-thread-folding.md` § Accepted sends and durable outcomes: joins use history IDs; delivered sender fields require deterministic casing handling.
- `docs/knowledge/features/thread-package.md` § Cache and epochs: semantic changes must bump folding rules.
- `docs/knowledge/features/msgqueue-package.md` § Durability boundary: restart does not restore the queue; queue IDs can repeat.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: inspect original JSON for omission and precision.
- `docs/protocol-mobile.md` § Full thread item, Queue (v2), Security model: inert content, command targeting and encrypted client boundary.

## Context

Queued thread items need the genuine queue handle before delivery so clients can target existing queue commands. History item identity is a separate durable namespace. This is one deliverable: publish the queue handle with its lifetime. No new decision record is required.

## Design

Add an optional uint64 `queued_msg_id` to the producer fact and fold record. New acceptances copy `QueuedMessage.ID`; zero IDs produce no acceptance. Both validators accept legacy omission but reject supplied zero, null, noninteger, wrong-type, negative and overflowing values, preserving the full uint64 range without float conversion. Validate case variants consistently with JSON decoding.

Accepted content retains the recorded handle and item ID remains the creating history ID. Delivery removes all delivered queue-handle variants before restoring the acceptance-owned handle when present. Thus legacy acceptances stay handle-free even when delivered messages contain a handle. Drop and loss retain acceptance content unchanged. Terminal handles are historical data: only `status == queued`, `active == true` and a valid handle qualify for queue actions, scoped by conversation.

Increment `foldingRules`; existing store/query/publication paths carry the same inert content without new wire fields or command changes. Keep unsolicited `queue_state` withheld. No overlapping remote feature branch touches the planned production files.

Sizing after plan: approximately 550 total written lines, zero exported types/interfaces, zero consumer signature updates, five acceptance criteria, no new state machine; added validation/guard branches remain below ten.

## Concurrency model

No new goroutines or locks. Acceptance remains synchronous with the existing queue observer; terminal outcomes retain their existing arbitration and readiness barrier. Fold access remains serialized by its owner. Test harness workers use existing joined cleanup.

## State transitions and identity reuse

| Event | Race-enabled regression |
|---|---|
| Acceptance, missed acknowledgement and catch-up/page/live recovery | `TestQueuedSendHandleQueries` |
| Delivery or drop of queue 7 versus history item 100 | `TestQueuedSendHandleActions` |
| Two devices sharing an app message ID | `TestSuggestionEnqueuerIdentity`, `TestSendQueueHandleLifetime` |
| Drop/loss/delivery and redundant terminals | `TestSendQueueHandleLifetime` |
| Restart loss followed by queue-ID reuse | `TestQueuedSendHandleQueries` |
| Reopening an incompatible pre-change cache | `TestStoreQueueHandleCacheRules` |
| Legacy acceptance followed by delivery | `TestSendQueueHandleLifetime` |

## Error handling

Malformed supplied handles consume fold version without creating or reserving an item. Invalid producer zero IDs are logged with a fixed event and rejected before recording. History append failures retain existing best-effort behavior. Cache incompatibility triggers existing replay/epoch recovery.

## Testing strategy

Write failing tests first. Table-test both validation boundaries with legacy omission, uint64 extrema and invalid representations. Verify raw JSON precision and acceptance-owned terminal preservation, including delivered casing variants. Use real queue and authenticated thread/query seams to exercise handle 7 against item 100, missed acknowledgements, command actions, restart loss and reused IDs. Verify old-rule cache invalidation from persisted raw acceptance history. Run focused regressions, race tests for `cmd/pyry` and `internal/thread`, `go vet ./...` and `go build ./cmd/pyry`; the verifier owns the full `make check` gate.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md`, **Full thread item** and **Queue (v2)**: document `user_message.content.queued_msg_id` as the positive uint64 queue handle, distinct from item `id`; command targets are `conversation_id` plus `queued_msg_id`. State that the handle is retained as historical data after delivery/drop/loss, that actions require a queued active item with a valid handle, and that legacy acceptances omit it. Explain conversation/run scope and restart reuse, and retain the statement that unsolicited `queue_state`, including connect-time reconciliation, stays withheld on thread connections.
- `docs/knowledge/features/thread-package-main-thread-folding.md`, **Accepted sends and durable outcomes**: record the added field, acceptance-owned preservation through the delivery merge and other terminal outcomes, legacy absence, and terminal action lifetime.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The daemon queue assigns `QueuedMessage.ID`; `accepted` rejects zero. `validQueuedSendFact` and `sendFact` independently validate persisted supplied handles. Delivery must strip its own handle before merging, preventing legacy data from acquiring an actionable-looking identity.
- [Tokens, secrets, credentials] Queue IDs are public conversation-local identifiers, not credentials or authority. No credential handling changes.
- [File operations] Existing history and cache storage own paths, permissions and atomic persistence; no new paths or file operations are introduced.
- [Subprocesses] No subprocess arguments or environments change.
- [Cryptography] Existing Noise transport remains unchanged; no keys, randomness or nonce handling are added.
- [Network and I/O] Existing bounded thread encoding carries inert JSON. No new input command, socket or unbounded read is introduced.
- [Errors, logs, telemetry] Zero-ID rejection logs only a fixed event. No text, payload, host path or credential is logged.
- [Concurrency] Existing acceptance readiness and queue terminal arbitration remain intact. Restart loss resolves durable IDs, so numeric queue-ID reuse cannot terminalize an older acceptance again.
- [Threat model] Handle publication grants no new authorization. Clients must gate actions on queued/active status; terminal handles cannot establish action lifetime. Client rendering and production activation belong to Desktop #1906/#1908 and later Mobile work, outside this daemon ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14
