# Retain accepted attachment facts through delivery

## Files read

- `internal/thread/sends.go` → `sendFact`, `deliveredSendContent`: inline delivery merge and acceptance metadata authority.
- `internal/thread/render.go` → `renderSharedLookup`: source-backed delivery uses the same helper.
- `internal/thread/cache.go`, `internal/thread/checkpoint_read.go` → `foldingRules`, `readCheckpoint`: incompatible certificates/checkpoints trigger a full replay and fresh epoch.
- `internal/thread/sends_test.go`, `sends_queue_handle_test.go`, `render_sends_test.go` → send lifecycle, metadata, provenance and reference-fold fixtures.
- `cmd/pyry/queued_attachment_facts_test.go` → `TestQueuedAttachmentWire`: authenticated uploads, stored byte retrieval and acceptance facts.
- `cmd/pyry/queued_send_history.go` → `accepted`, `written`, `delivered`: recorded facts and source-safe send transitions.
- `cmd/pyry/daemon_thread_compact_test.go` → `testCompactQuery`, `testCompactRead`: authenticated compact queries and publication barriers.
- `docs/knowledge/features/thread-package-main-thread-folding-sources.md` → accepted-send identity and source-backed rendering contract.
- `docs/knowledge/features/thread-package-cache-and-epochs.md` → rules must advance; invalidate checkpoint authority as well as scalar evidence when testing migration.
- `docs/knowledge/features/development-verification.md`, `docs/protocol-mobile.md` § Security model: controlled regressions and remote display trust boundaries.

## Context

Delivery currently replaces acceptance-owned attachment names with ID-only message content. The useful behavior is retaining those recorded display facts across delivery and subsequent reads; cache migration and regression coverage establish that same invariant. No separate deliverable or decision record is needed.

Sketch and final-plan sizing: approximately 500 total written lines, two production files, no exported types/interfaces, no consumer signature updates, four acceptance criteria and fewer than ten metadata rejection branches. The #3324 send merge/cache slice is the closest analogue. Concurrent #3725 edits other send logic and advances rules to 8; this change stays local to the shared helper and selects 7 relative to merged main's 6.

## Design

`deliveredSendContent` retains existing safe delivered text/IDs and receiving metadata. It removes all case variants of delivery-owned `attachments`, then projects only usable acceptance facts: string ID and filename, ID membership in acceptance `attachment_ids`, nonempty basename without slash, backslash, drive prefix or dot-directory names. Preserve exact ID/name strings and array order, and serialize only those two fields. Missing, null, non-array or wholly unusable metadata omits `attachments`; delivery never fills a gap. Parse metadata independently so one malformed entry cannot discard usable neighbors or the accepted send. Existing identity, sender/time/queue metadata, revision and receiving provenance remain governed by the existing fold.

Advance `foldingRules` to 7. A prior-rule checkpoint and scalar certificate with otherwise matching evidence must rebuild from history under a new epoch. No attachment storage lookup is added to folding.

## Concurrency model

No new goroutines or shared state. Inline folds remain serialized by their owner; Store rendering and lifecycle retain existing synchronization. Wire fixtures join their existing publisher/store workers at cleanup and reopen only after shutdown.

## State transitions and identity reuse

| Event | Race-test proof |
| --- | --- |
| Acceptance → written → delivered, with stable item identity and receiving placement | `TestSendAttachmentFacts`, `TestDeliveredAttachmentWire` |
| Duplicate delivery/outcome, retaining the first accepted facts | `TestSendAttachmentFacts` |
| Source-backed rendering after cached history entries are evicted | `TestSendAttachmentFacts` |
| Shutdown/reopen and fresh full/compact queries and pages | `TestDeliveredAttachmentWire` |
| Prior-rule checkpoint/certificate reopened with matching history evidence | `TestStoreAttachmentCacheRules` |

## Error handling

Unusable display metadata is omitted without an error or invented filename. Existing history/source errors still withhold usable responses. No new errors, logs, filesystem writes, storage authority or fallbacks are introduced in the helper.

## Testing strategy

Write and run the focused regressions before production edits. Table-driven fold coverage checks inline/reference agreement, filtering, conflicting delivery names, legacy IDs, metadata and repeat outcomes. Reuse authenticated upload/send fixtures for real stored bytes, written/delivered revisions, a compact full-root replacement, full/compact catch-up and pages, then fresh daemon/store reopen and byte retrieval. Invalidate prior-rule authority without changing version/checksum/watermark and assert epoch rotation and correct replayed names. Retain existing send/provenance and compact tool/agent-header checks. Run focused Linux race tests through test admission with JSON counts, plus `go vet ./...` and `go build ./cmd/pyry`; complete suites belong to the dispatcher/verifier.

## Open questions

None. Acceptance provides filename authority; delivered safe text and attachment-ID behavior keep their existing contract.

## Documentation handoff

- Pending documentation stage: `docs/protocol-mobile.md`, **Full thread item**: state that acceptance-owned attachment ID/name facts survive delivery, compact replacement and reopening; legacy or unusable metadata supplies no invented filenames or host paths.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `deliveredSendContent` treats recorded metadata as untrusted display data; independently parse entries, require accepted-ID membership and construct only ID/name fields. Delivery facts, including case variants, never become name authority.
- Tokens/secrets/cryptography: no credential, nonce, encryption or authentication changes; authenticated wire tests reuse the existing Noise fixture.
- File operations: the helper performs none. Reject both platform separators, drive prefixes and dot-directory names rather than deriving basenames from host paths; stored bytes remain behind existing authorized retrieval.
- Subprocesses: no production subprocess change. Tests use the existing fake boundary and never execute attachment names or composed prompts.
- Network/I/O: existing bounded authenticated routes and projections are reused; no new endpoints or reads. Metadata filtering is linear in the recorded payload.
- Errors/logs/telemetry: no new logging or raw error surfaces; regression assertions exclude host paths and forged names from delivered content and logs.
- Concurrency: no new shared state; lifecycle regression shuts down before reopening. Invalid old rules must rotate the epoch despite otherwise matching evidence.
- Threat model: this changes display history only. Existing Noise authorization, prompt-injection mitigations and client text rendering remain in their current owners; no new capability is granted by a display filename.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-21

## Revisions

- 2026-10-21: The first wire regression also observed a legitimate producer-gap retirement when the legacy delivery message became public before its outcome. `daemonThreadPublisher.publish` in `cmd/pyry/daemon_thread_relay.go` requires reset after such suppression. Keep that contract: publish acceptance/written boundaries before querying them, then idle-unload and load the linked delivery pair together under the shadow coordination lock. This adds a clean idle reload under the same acceptance identity to `TestDeliveredAttachmentWire`; its later fresh daemon reopen remains separate. Use the queue's fake child write seam and actual `queuedSendHistory.written`/`delivered` producers for lifecycle facts. Production design and sizing remain unchanged.
