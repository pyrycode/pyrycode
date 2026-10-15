# Visible content unread targets

## Files read

- `cmd/pyry/history_projection.go` → `historyEntryShown`, `historyVisibilityMetadata`: producer content policy, including payload-dependent endings and dividers.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyHistoryReader.LatestDisplayableEntryID`, `legacyRuntimeReceipt`: unread currently mistakes transport accounting for content.
- `cmd/pyry/legacy_runtime_receipts_test.go` → receipt, wire-target, completion-tail and list/read fixtures: preserve delivery/checkpoint assertions while updating server targets.
- `cmd/pyry/agent_history_test.go`, `cmd/pyry/agent_session_endings_test.go`, `cmd/pyry/queued_legacy_receipts_test.go` → existing receipt watermark expectations.
- `cmd/pyry/startup_history.go` → `reconcileStartupHistory`: repeated restart dividers and recovery notices.
- `cmd/pyry/daemon_thread_watermark_test.go` → `testThreadWatermark`: loaded metadata and unloaded certificate scaffolding.
- `cmd/pyry/daemon_thread_relay.go` → `installDaemonThreadRelay`: authoritative loaded/unloaded lookup.
- `internal/thread/observations.go` → `Fold.recordShown`: only first public appearance and appended text advance shown progress.
- `internal/relay/v2session_thread.go` → `threadSummary`: negotiated projection preserves unavailable readings.
- `internal/relay/handlers/list_conversations.go`, `mark_conversation_read.go` → shared history reader, persisted monotonic clamp.
- `docs/knowledge/features/history-package-watermarks.md`, `history-package-producers-legacy-compatibility.md`: accounting and visibility must remain separate.
- `docs/knowledge/features/thread-package.md`: hidden appends invalidate stale unloaded evidence; never replace absent certificates with raw progress.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`: independent behavioural oracles, scoped race checks.

## Context

Hidden context usage and restart dividers after completed messages keep every conversation unread. The daemon producer already classifies content; legacy list/read bypass that policy. One deliverable restores the visible-content target on the existing daemon seam for both apps. No new decision record is needed.

## Design

Keep `historyEntryShown(type, payload) bool` as the shared daemon content policy. Producer metadata continues calling it. The legacy unread scan requires that classification plus absence of an explicit false visibility flag. Explicit true cannot turn metadata or unknown vocabulary into content. Old absent-metadata entries receive the same payload-aware classification. Unknown types remain non-content by default. Valid receipt projection alone never establishes visibility.

Retain the backward bounded-page scan, original durable IDs, store validation and errors. `startRelayV2` continues supplying the same reader to list and mark-read. Preserve `max(held, min(up_to, latest))`; no storage migration or read-mark rewrite. Raw store watermark APIs and legacy receipt delivery/accounting are unchanged.

Audit and test thread shown progress, loaded metadata and eligible unloaded certificates against completed-message/hidden-tail histories. Preserve first-public-appearance/text-append semantics, current certificate eligibility, and unavailable readings. No client or cache redesign.

No overlapping feature branch touched the planned files at the initial branch check. Estimated written work: 600–700 lines including plan/tests/expectation updates, zero exported types, zero changed consumer signatures, four acceptance criteria, no new state machine rejection branches. All five limits remain within bounds after planning.

## Concurrency model

No new goroutines or locks. History uses existing synchronized reads; registry owns atomic monotonic persistence. Thread test workers are joined with `Shutdown` and use bounded waits.

## State transitions and identity reuse

| Event | Race regression |
| --- | --- |
| Visible message followed by context usage, then repeated restart dividers | `TestUnreadHiddenTails` |
| Reopen and reconcile repeatedly, for read and unread marks | `TestUnreadRestartReconciliation` |
| Read an older message, then newest, then append visible content | `TestUnreadHiddenTails` |
| Loaded thread consumes hidden tail; unload certificate, invalidate with hidden append, reload | `TestUnreadThreadHiddenTails` |

All IDs remain in the original durable history space. Hidden entries retain their IDs and accounting role.

## Error handling

Preserve nil-store, invalid-ID, containment, page and unavailable-history errors. Classification adds no I/O or external errors. Failed certificate readings remain unavailable, without a raw-cursor fallback.

## Testing strategy

Write regressions first and observe failure on the old reader. Exercise reported tails, page boundaries, absent metadata, future hidden types, hidden-only histories, round-trip persistence, monotonic higher marks and later visible content. Retain receipt validation, wire identities, checkpoints and delivery checks while correcting server-watermark assertions. Use existing thread fixtures and fold rules to check hidden item/live-state updates. Run focused regressions then `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-3463/pyry ./cmd/pyry`. The verifier runs `make check` and the full-module suite.

## Open questions

None. The explicit-false veto and shared classifier define the metadata precedence; no transport eligibility fallback remains.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md`, **Application message types** (`conversations`), **Marking a conversation read**, **A history entry**: unread/list and clamping follow visible conversational content; hidden metadata/receipts do not raise it; marks remain monotonic in original durable ID space. Explain restart behavior and distinguish `last_shown_version` from raw consumed progress.
- `docs/knowledge/features/history-package-watermarks.md`, **Unread state uses a separate, lazily recovered watermark**; `docs/knowledge/features/history-package-producers-legacy-compatibility.md`, **Legacy eligibility and explicit visibility**; `docs/knowledge/features/protocol-package-types-conversations-read-payloads.md`, read/latest watermark description; `docs/knowledge/features/relay-package-handlers.md`, **MarkConversationRead** and history-dependency section: replace wire/receipt-counting unread claims with shared visibility policy, absent-metadata classification and unknown-type non-content default; retain receipt accounting separately.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The existing `MarkConversationRead` registry lookup and history ID/containment validation remain mandatory. `historyEntryShown` classifies stored payloads without granting transport access; explicit false always vetoes content.
- [Tokens] No credentials are created, read, stored or logged by the changed path.
- [File operations] Keep `LatestEntryID` and `Page` validation; no new paths or writes. Registry persistence retains its atomic save contract.
- [Subprocesses] No new subprocess invocation or environment handling.
- [Cryptography] No cryptographic changes; authenticated relay access remains enforced upstream.
- [Network and I/O] Keep bounded 128-entry history pages and existing payload/storage limits; no additional network surface.
- [Errors/logs/telemetry] Classification introduces no logs or errors containing payloads; existing handler error translation remains intact.
- [Concurrency] No new shared mutable state, lock ordering or goroutines. Tests join thread workers before cleanup.
- [Threat model] Existing authenticated conversation access in the protocol security model is unchanged. Classification cannot authorize a receipt, reveal a raw fact or advance beyond the supplied/clamped read target.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15

## Revisions

2026-10-15: The full scoped race run also identified superseded hidden-receipt target assertions in `TestQueuedSendHistoryLifecycle` (`cmd/pyry/queued_send_history_test.go`) and `TestPromptAnswerReceiptsPages` (`cmd/pyry/prompt_answer_receipts_test.go`). Update only those server target expectations; preserve their receipt validation, paging, checkpoint and delivery checks. No production design change. The final regression uses authenticated legacy/thread list and mark-read routes, tests a completion revision beyond shown progress, and covers loaded metadata and eligible/unavailable unloaded certificates. Written work remains below 800 lines, with zero exported types or changed consumer signatures.
