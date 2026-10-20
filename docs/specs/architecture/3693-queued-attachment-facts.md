# Stored attachment facts at queued acceptance

## Files read
- `cmd/pyry/queued_send_history.go` → `queuedSendFact`, `accepted`: safe projection and acceptance completion barrier.
- `cmd/pyry/main.go` → `runSupervisor`: installs the producer before queue startup.
- `cmd/pyry/relay.go` → `startRelayV2`: existing attachment lookup discards path-bearing errors.
- `internal/attachments/storage.go` → `ResolvePath`: conversation-pair containment and stored basename authority.
- `internal/relay/handlers/send_message.go` → `SendMessage`: routing, shape refusal and first-occurrence ID deduplication precede enqueue.
- `cmd/pyry/queued_send_handle_test.go` → `TestQueuedSendHandleQueries`: reopened history and queue-ID reuse proof.
- `cmd/pyry/daemon_thread_compact_test.go` → `TestDaemonThreadCompactNegotiation`, `testCompactQuery`: authenticated compact negotiation and query helpers.
- `cmd/pyry/daemon_live_relay_test.go` → `testDaemonRelayWire`: authenticated wire lifecycle and joined shutdown.
- `docs/knowledge/features/history-package-producers.md` § Accepted sends: record only the safe queued projection; delivery must await acceptance completion.
- `docs/knowledge/features/attachments-package-directory-resolution-and-creation.md` § ResolvePath: registry membership is the caller's obligation; sanitized names remain private in diagnostics.
- `docs/knowledge/features/thread-package.md`, `msgqueue-package.md`, `development-verification.md`: durable facts, restart loss and focused proof boundaries.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: logging, filesystem and authenticated transport contracts.

## Context
Queued user roots currently retain attachment IDs without display names. Record stored names at acceptance so clients can retire their optimistic row. This is one useful behavior with its regression coverage; delivery replacement retention is separately owned by #3694. No new decision record is needed.

## Design
Add an optional `attachments` array to `queuedSendFact`, with private entries containing exactly `attachment_id` and `filename`. Add an optional name resolver field on `queuedSendHistory`, leaving its constructor and the queue API unchanged. `accepted` resolves each distinct projected attachment ID in first-occurrence order, retaining text and all existing IDs even when a name is unavailable. No facts means the array is omitted.

A private resolver factory in the producer file captures the instance directory and conversation registry. Each lookup first requires registry membership, then calls `attachments.ResolvePath`, discards errors and returns only `filepath.Base` of a successful stored path. `runSupervisor` installs it before queue startup. No send filename, delivery prompt, registry upload memory or history-read lookup participates.

Existing folds, publication, compact full user roots and raw history replay carry the additive JSON unchanged. Legacy ID-only entries remain unchanged.

Overlap: #3682 changes a separate approval-wiring block of `main.go`; this installation is additive and independent.

Sizing rechecked: about 550–650 written lines including plan and tests; 0 exported types/interfaces, 1 production installation, 4 acceptance criteria, 2 new lookup refusal branches. All limits hold.

## Concurrency model
Resolver configuration is immutable before producers run. Lookup runs synchronously within acceptance observation, outside the history reference-map mutex; no new goroutines or locks. Existing acceptance completion gates delivery. Wire/queue test lifecycles use cancellation and join their workers before reopening stores.

## State transitions and identity reuse
| Event | Race-detector proof |
| --- | --- |
| Duplicate attachment references at acceptance | `TestQueuedAttachmentWire` preserves deduplicated IDs and ordered facts. |
| Same attachment ID in two conversations | `TestQueuedAttachmentFacts` resolves the owning conversation's stored name. |
| File disappears after validation | `TestQueuedAttachmentUnavailableAfterValidation` retains acceptance text/IDs and available facts. |
| History/thread reopen with unresolved queue | `TestQueuedAttachmentWire` reopens stores and serves fresh content with restart-loss status and unchanged names. |
| Legacy acceptance and reused queue ID | Existing `TestQueuedSendHandleQueries`, plus legacy omission in `TestQueuedAttachmentFacts`. |

## Error handling
Lookup failure omits that fact only; it never refuses an already accepted send. The resolver returns no error value to the producer and emits no diagnostics. Send validation continues to refuse missing/invalid IDs with `attachment.not_found`, and malformed shapes with the existing protocol refusal. Storage failure keeps the producer's existing best-effort history behavior.

## Testing strategy
Write regression tests before implementation and observe missing-fact failures. Use authenticated daemon wire helpers and production intake/lookup with real stored bytes, hold queue delivery, verify exact facts in initial publication and compact catch-up, retrieve and compare bytes, then reopen history/thread state and query fresh content after startup reconciliation. Focused cases cover conversation isolation, duplicates, hostile names, absent metadata, legacy omission and malformed/missing references, with content-free diagnostics. Retain existing compact tool/agent header proof. Run affected `cmd/pyry` race tests, `go vet ./...` and `go build ./cmd/pyry` on Linux; full suites belong to the verifier/dispatcher.

## Open questions
None.

## Documentation handoff
- Pending documentation stage: `docs/protocol-mobile.md`, **Full thread item**: document additive queued-acceptance `content.attachments`, exact entry keys, stored-basename authority, conversation scope, omission/unavailable metadata and ID-only legacy behavior. Delivery retention is #3694's handoff.
- Pending documentation stage: `docs/knowledge/features/history-package-producers.md`, **Accepted sends and linked outcomes (#2972)**: describe recording safe stored attachment facts at acceptance, retaining IDs and excluding host paths/content-bearing diagnostics.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] Registry membership in the stored-name resolver precedes `ResolvePath`; acceptance receives only safe `QueuedMessage` fields. Send-supplied names cannot become facts.
- [Tokens, secrets, credentials] No credential generation, storage or lifecycle changes. Tests use existing authenticated Noise helpers with disposable keys.
- [File operations] `ResolvePath` enforces canonical IDs, exact conversation containment and regular stored leaves. The new code reads names only, creates no files and persists through the existing history writer. Concurrent privileged file removal is tolerated by omitting unavailable facts; the existing private-state-directory threat bound remains unchanged.
- [Subprocesses] No new subprocess invocation or environment behavior. Held test delivery cannot send composed bytes to a child.
- [Cryptography] No new cryptographic operations; authenticated wire helpers reuse production Noise transport.
- [Network and I/O] No new endpoint or reader; existing bounded attachment intake and send ID-count limits bound remote inputs. Name lookup is once per distinct accepted ID.
- [Errors, logs, telemetry] Path-bearing lookup errors are discarded at the resolver; neither names nor paths are logged. Regression assertions cover diagnostic privacy.
- [Concurrency] No new shared mutable lifecycle state or goroutines. Immutable resolver configuration precedes queue startup; acceptance retains its existing completion barrier.
- [Threat model] Paired clients retain existing conversation access. Attachment IDs remain lookup keys rather than capabilities; no cross-conversation fallback or name guessing. OUT OF SCOPE: delivery-content retention belongs to #3694.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-20
