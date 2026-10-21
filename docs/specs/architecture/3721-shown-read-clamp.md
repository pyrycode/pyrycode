# Authoritative shown-thread read acknowledgements

## Files read

- `internal/relay/handlers/mark_conversation_read.go` → `MarkConversationRead`: validation, clamp, persistence and reply/push ordering.
- `internal/conversations/registry.go` → `AdvanceReadUpTo`: atomic monotonic persistence and rollback.
- `cmd/pyry/relay.go` → `startRelayV2`: handler installation and thread provider composition.
- `cmd/pyry/daemon_thread_relay.go` → `installDaemonThreadRelay`: registered, usable or certified-unloaded authority.
- `internal/relay/v2session_thread.go` → `threadSummary`: delivery-time shown-field projection.
- `cmd/pyry/daemon_thread_watermark_test.go` → `testThreadWatermark`, certificate and lifecycle tests: exact V = H, cold cursor, no segment reads, rebuilding/unavailable refusals.
- `cmd/pyry/unread_visibility_test.go` → `TestUnreadThreadHiddenTails`: completion revisions and stale hidden-tail certificates.
- `cmd/pyry/legacy_runtime_receipts_test.go` → `testLegacyListAndMark`: mixed source/list/read checks and isolation.
- `internal/relay/handlers/mark_conversation_read_test.go`, `displayable_read_state_test.go`, `conversation_pin_projections_test.go`: handler contracts and durable metadata.
- `docs/knowledge/features/thread-package.md`, `thread-package-cache-and-epochs.md`, `thread-package-shadow-lifecycle-and-evidence.md`: retiring workers withhold certificates; independent raw progress is required even at zero; offscreen recovery remains producer-owned.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md`: replace only projected fields and preserve unrelated payload bytes.
- `docs/knowledge/features/sessions-package.md`, `development-verification.md`, `CODING-STYLE.md`, ADR 042's read marks: lifecycle, focused evidence and shared version space.

## Context

Read acknowledgements still use legacy visibility while summaries already use the authoritative shown watermark. Hidden records and completion revisions must never increase the read clamp. This changes only the read authority, leaving legacy list tracking for #3722. No new decision record is needed; ADR 042 supplies the contract.

One independently useful behavior: clamp durable read acknowledgements to available shown evidence. Refusal and regression coverage belong to that same behavior. Estimated total written work: 500–650 lines including tests and this plan; zero new exported types, 10 consumer sites (one production), three acceptance criteria, five existing refusal branches. All numeric limits remain satisfied after planning.

Overlap: #3715 changes history paging in `relay.go` and receipt fixtures in `legacy_runtime_receipts_test.go`; the read-handler wiring and read-mark helper changes are local and independent.

## Design

`MarkConversationRead` accepts `func(string) (uint64, bool)` matching `V2SessionConfig.ThreadLastShownVersion`. Membership is resolved before invoking it. Nil or absent readings always refuse with correlated retryable `history.unavailable`, including requests that would otherwise be no-ops. Present zero is authoritative. Pass the reading to unchanged `AdvanceReadUpTo`, storing `max(held, min(requested, shown))`.

Install the production handler after `installDaemonThreadRelay` so it receives exactly the same provider as summary projection. Remove its legacy history-reader argument and typed-nil store accommodation. Preserve validation, durable record construction, rollback, correlation and announce-after-reply-attempt behavior. Legacy history readers remain for lists.

The provider is unchanged: usable loaded status or fully unloaded clean certificate at independently known exact consumed V = current raw H. Rebuilding, unavailable and retiring views refuse; lookup never loads a worker, warms raw progress or scans segments. Convert mixed fixtures to real loaded thread authority while retaining source-history/list assertions. Add handler and daemon wire assertions without introducing a reusable harness.

## Concurrency model

No new goroutines or locks. Existing registry serialization owns advance/save/rollback. Provider reads use existing thread/history locking; no lock spans delivery. Existing thread workers retain their cancellation and joined-unload paths. Summary delivery rechecks authority independently.

## State transitions and identity reuse

| Event | Race-test coverage |
| --- | --- |
| Repeated/equal/older/oversized request and held mark above shown | `TestMarkConversationRead_Success`, `TestUnreadThreadHiddenTails` |
| Missing evidence even for a no-op | `TestMarkConversationRead_HistoryUnavailable` |
| Loaded → unloaded → hidden append → reload → retirement | `TestUnreadThreadHiddenTails` |
| Restart with cold progress, then independent progress known | `TestMarkConversationReadThreadRestart` |
| Conversation identity isolation | `TestMarkConversationReadThreadRestart`, `testLegacyListAndMark` callers |
| Rebuilding/unavailable/retiring and invalid certificates | `TestDaemonThreadLastShownUnavailable`, `TestDaemonThreadLastShownCertificates`, existing thread retirement tests |
| Save failure then retry and requester disconnection | `TestMarkConversationRead_SaveFailureThenRetry`, `TestMarkConversationRead_ReplyFailureStillPushes` |

## Error handling

Keep malformed and unknown-ID errors static and nonretryable. Unavailable shown authority gives the existing static retryable history error without mutation/save/announcement. Registry disappearance and persistence errors keep their existing mappings and rollback. No raw provider error or request bytes are logged. Successful no-ops reply with the held mark but do not save or announce.

## Testing strategy

First convert handler tests to the callback contract and observe compilation failure against the old signature. Exercise real daemon wire read marks alongside summary replies/pushes, including hidden completion tails, certified unloaded evidence and refusal/recovery. Prove restart and conversation isolation using persisted registry/thread evidence. Retain legacy source/list checks rather than deleting their fixtures. Run focused affected Go race tests on Linux with JSON event counts, plus `go vet ./...` and `go build` to a scratch output. Dispatcher/verifier owns complete build, unit/race, integration and live/release acceptance. No tagged test files are removed or moved.

## Open questions

None; existing provider readiness and certificate rules are the authority contract.

## Documentation handoff

Pending documentation stage: `docs/protocol-mobile.md`, **Marking a conversation read** and **conversation_updated**: replace legacy visible-entry clamping with the authoritative shown-version formula; distinguish known zero from unavailable evidence and retain refusal, persistence, correlation and announcement semantics. #3722 owns removal of the legacy list field and list guidance.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `MarkConversationRead` resolves untrusted conversation IDs through registry membership before evidence lookup; the installed provider repeats membership admission. `up_to` remains uint64-decoded. Missing evidence cannot authorize even a no-op.
- Tokens/secrets: no credentials are generated, accessed or transmitted by this change.
- File operations: unchanged `AdvanceReadUpTo` persists registry snapshots atomically and rolls back failure; paths are daemon-supplied. Certificate access retains contained no-follow scalar reads and exact V = H eligibility.
- Subprocesses: no subprocess seam is added; lookup cannot load a worker or runner.
- Cryptography: no primitive, key, nonce or secret comparison changes.
- Network/I/O: existing authenticated bounded dispatch transports remain unchanged; provider lookup adds no segment scan or unbounded socket work.
- Errors/logs/telemetry: static refusal messages and existing correlation remain; no unproven IDs or payload bytes are logged on refusal.
- Concurrency: registry owns monotonic update/save serialization; provider locks are released before registry writes and sends. No new goroutine or lock-order edge.
- Threat model: authenticated remote clients can request arbitrary marks but cannot exceed proven shown progress or use unavailable evidence. Existing held marks never decrease. Relay transport authentication/encryption and host compromise remain outside this change's authority surface.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-21

## Revisions

### 2026-10-21 — verifier finding 1: discover deferred handler registration

- Files read: `cmd/pyry/relay_guard_test.go` → `mapLiteralKeys` and `TestEveryInboundV2TypeHasHandler`; `startRelayV2`'s registration diff; `docs/knowledge/features/development-verification.md` and its source-check/testing topics; `docs/knowledge/features/relay-package.md`; the original inbound-handler guard plan for #950.
- The guard only discovers literal keys, so it misses the read handler installed after thread authority. Replace its extractor with `handlerMapKeys`, scoped to `startRelayV2`, combining the literal with indexed assignments to `sessionConfig.Handlers`. Keep strict protocol-key parsing, exactly-one-literal admission, missing-handler coverage, reverse classification and totality assertions. Production installation order stays unchanged.
- Add focused source fixtures for literal and deferred registrations, absent registration, and unrelated receiver/function assignments. Reproduce `TestEveryInboundV2TypeHasHandler` failing first, then run it and the extractor scenarios under the Linux race detector; run vet and build separately. Complete gates remain dispatcher-owned.
- This is the same coverage invariant, with its necessary regression proof: about 130 additional written lines, under 650 cumulative; no exported types, one updated consumer, no new acceptance criterion or lifecycle/reject state. #3715 overlaps only the classification tables in this file; these edits stay local to extraction and its comments.
- Security re-review: PASS. This repair only parses checked-in source in tests; it adds no runtime trust boundary, credential, file persistence, subprocess, cryptographic, network, logging or concurrency behavior. The original security review and documentation handoff remain applicable.
