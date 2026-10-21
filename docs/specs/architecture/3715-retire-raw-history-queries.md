# Retire raw conversation history queries (#3715)

## Files read

- `internal/relay/v2session.go` → `dispatchAppFrame`: authenticated interception and ordinary dispatch fallback.
- `internal/relay/v2session_appframe.go` → `appFrameWorker`: FIFO worker and Run-owned encrypted replies.
- `internal/relay/v2session_history_request.go` → `handleRequestHistory`, `serveHistoryPage`: endpoint-only admission, paging and rejection machinery to remove.
- `internal/relay/v2session_seams.go`, `v2session_seam_types.go` → `HistoryPage`, `HistoryPager`: obsolete configuration and outcome types.
- `internal/dispatch/dispatch.go` → `Route`, `sendError`: static, correlated, non-retryable unknown-type rejection.
- `internal/protocol/history.go`, `codes.go`, `compat_test.go`: raw request/reply vocabulary, fixtures and partition tables.
- `cmd/pyry/conversation_history.go`, `relay.go` → `newHistoryPager`, `appendConversationHistory`, `historyPageFailure`, `startRelayV2`: remove only the pager and wiring; retain durable producers and startup error classification.
- `cmd/pyry/relay_guard_test.go`: remove retired endpoint classifications.
- `cmd/pyry/daemon_thread_queries_test.go` → `testDaemonThreadQueryWire`, `TestDaemonThreadQueriesRecovery`: preserve authenticated newest-window, older-page and catch-up evidence.
- `internal/relay/v2session_catchup_test.go` → `testThreadQueryRequestedScope`: preserve explicit conversation scope and reconnect/replay evidence.
- `cmd/pyry/*history*_test.go`, `*receipts_test.go`, `thread_shadow_legacy_test.go`, `interactive_provenance_test.go`: mixed producer, receipt, live/replay and read-mark tests need local raw-paging assertions removed without losing their owned checks.
- `internal/e2e/relay_v2_history_test.go`, `channel_post_live_test.go`, `channel_post_completion_test.go`: retire raw endpoint tests and exclusive helpers; keep live IDs and channel completion/replay checks.
- `docs/knowledge/features/protocol-package.md`, `protocol-package-types-history-payloads.md`, `history-package.md`, `thread-package.md`, `v2-session-manager-state-machine-inbound-request-history-historypager-seam.md`: raw history is the fold source; projected receipts cannot replace it.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, `development-verification-protocol-boundaries.md`, `docs/knowledge/decisions/042-daemon-built-thread.md`, `docs/protocol-mobile.md` § Security model: conventions, authoritative query coverage and threats.

## Context

Current apps recover content through daemon-built threads. Retire the independently callable raw endpoint while preserving its source storage and other legacy behaviors for the #3714 family. No new decision record is needed; ADR 042 owns the contract.

One observable deliverable: authenticated raw history requests become ordinary unknown types. Protocol declarations, pager plumbing and positive paging tests have no independently useful retirement, so the verification floor keeps them together. Estimate: at most 650 added/rewritten lines including this plan, zero new exported types, two acceptance criteria and no new reject branches. The 25 pager consumer sites exceed the ten-site ceiling; the documented floor overrides that ceiling. Recount before commit: the same bounds apply to this design.

Fetched feature branches overlap locally with #3240 (`v2session.go`, permission observation in different blocks) and #3296 (`v2session_catchup_test.go`, test synchronization). Neither supplies required functionality or rewrites the removed endpoint; keep edits local.

## Design

Delete the raw history switch/worker arm, handler file, seam field/types, daemon adapter and installation. Delete `RequestHistoryPayload`, `HistoryEntry`, `HistoryPagePayload`, their type constants, fixtures and table entries. Retain `CodeHistoryUnavailable`, source-history APIs and `historyPageFailure`.

The literal `request_history` reaches `dispatch.Route`, which rejects it with `protocol.unknown_type`, message `unknown envelope type`, `retryable:false` and the request's `in_reply_to`. No endpoint payload is parsed or history consulted. Malformed envelopes and encrypted-envelope features retain their ordinary generic errors.

Delete endpoint-only tests, including skipped ones, and exclusive helpers. Mixed tests keep raw source assertions using `history.Entry`, live/replay receipt projections, read watermarks and provenance; any receipt comparison needed by these tests derives from the retained live projection, never a replacement pager. Keep existing thread query providers and authoritative wire checks; remove only their raw pager installation/positive assertions.

## Concurrency model

No new goroutines or locks. Ordinary requests retain the per-connection FIFO worker and Run-owned cipher serialization. Removing the pager also removes its disk reads and fit/retry loop. Existing cancellation and teardown paths remain.

## State transitions and identity reuse

| Event | Race-detector proof |
| --- | --- |
| Repeated retired requests on one authenticated connection, distinct correlated IDs | `TestV2Session_RetiredHistoryQuery` |
| Thread recovery/restart, newest-window and repeated older pages | `TestDaemonThreadQueriesRecovery` |
| Reconnect and explicit requested conversation rather than active cursor | `TestThreadCatchUpRequestedScope`, `TestThreadPageRequestedScope` |
| Thread query live ordering and host isolation | existing `TestThreadCatchUp*`, `TestThreadPage*` focused families |

## Error handling

Reuse `Route`'s static rejection. Do not create a retirement-specific handler, leak request fields or return a successful empty page. Retain generic malformed/encrypted rejection and shared history-unavailable failures for list/read.

## Testing strategy

Write an authenticated Noise dispatch regression first and observe failure before removal. Assert exact error payload, correlation, repeated requests, no peer disclosure and no request secrets in replies/logs. Cover generic malformed and encrypted-envelope rules. Run affected protocol, relay, daemon and fake-e2e tests through test admission with `-race -json`; count executed/passed/failed/skipped test events and record Linux. Compile the affected tagged live package without executing live tests. Run vet/build checks; complete acceptance gates belong to dispatcher/verifier.

## Open questions

None. Endpoint-only tests are deleted; mixed tests retain the behavior their producer/live/read paths own.

## Documentation handoff

Pending for documentation stage:

- `docs/protocol-mobile.md`, **Conversation history (v2)**, **Application message types**, endpoint-only error descriptions and affected examples: retire raw request/history-page/backfill content recovery and direct readers to **Thread query messages**. Retain the shared `history.unavailable` contract for list/read failures.
- Update `docs/knowledge/features/protocol-package.md`'s history request/reply topic and `docs/knowledge/features/protocol-package-types-history-payloads.md` to describe retirement without implying source history was removed.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `handleNoiseMsg` authenticates/decrypts before `dispatchAppFrame`; `Route` rejects the unrecognized type before any handler. MUST FIX avoided by deleting the interception rather than installing a special handler that could expose data.
- [Tokens] No credentials are created, stored or changed; regression requests use synthetic payload canaries and existing test pairing.
- [Files] Removal eliminates remote paging file reads; source append, startup reconciliation and thread-store confinement remain unchanged.
- [Subprocesses] No new subprocess execution or caller-controlled command arguments.
- [Cryptography] Existing handshake and Run-owned Noise ciphers remain unchanged; no nonce/key logic is moved.
- [Network and I/O] Existing envelope limits and bounded worker queues remain; no new I/O. Encrypted-envelope feature rejection remains owned by `IsKnownAppType`.
- [Errors/logs] SHOULD FIX: assert exact static unknown-type payload and absence of request canaries in logs/replies through authenticated dispatch, including repeat requests and another peer.
- [Concurrency] No new lifecycle state; existing FIFO worker forwards errors to Run and exits on connection or manager cancellation.
- [Threat model] Retirement removes a content-disclosure surface; relay MITM, token revocation, replay protection and static-key compromise mitigations stay at their existing boundaries. Other legacy publication/receipt retirement belongs to #3714 siblings and companion receiver tickets desktop#2209/mobile#2167.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-21
