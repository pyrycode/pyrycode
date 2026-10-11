# Capture receipts and revision completion

## Files read

- `internal/memorycapture/batches.go` → `Store.Publish`, `Store.Pending`, `Store.Report`, `checksum`: immutable retry input, evidence union and version-1 encoding.
- `internal/memorycapture/storage.go` → `readRecord`, `Store.transaction`, `Store.publish`: held-directory confinement, flock and complete-record recovery after rename.
- `internal/memorycapture/batches_test.go` → `testStore`, `testPayload`, `TestHelperProcess`: reuse fixtures and process-test conventions.
- `internal/memorycapture/storage_test.go` → `TestFilesystemAttacks`, `TestCancelledLockAndPublication`: private-storage and cancellation contracts.
- `docs/knowledge/features/memorycapture-package.md` → “Concurrency and durable publication”, “Private records and validation”: keep validated descriptors alive and recover complete renamed records before serving queries.
- `docs/knowledge/features/development-verification.md` → “Test execution and artifact survival”: inspect reopened state rather than inferring rollback from errors.
- `docs/guide.md` → “Recent conversation transcripts”: exporter owns eligibility, stable rows, revision inputs and immutable closure; three boundaries remain distinct.
- `CODING-STYLE.md`: stdlib tests, cancellable transactions and errors as values.

## Context

#3177 supplies durable retry inputs but cannot prove successful processing. This slice adds one completion contract for execution, scheduling and retirement consumers; it executes no agents, indexes no notes and removes no transcripts. Receipts acknowledge processing, not that every statement became a fact. No decision record is needed.

Sizing: approximately 290 production, 380 tests and 85 plan lines, total 755; two new exported types, no consumer migrations, five acceptance criteria and at most eight new domain rejection branches. Recount before plan commit confirms these limits. No overlapping feature branches were found after fetching origin.

## Design

- Keep `record.Batch` as the pending batch; its zero value means none. Add an optional ordered completed-batch list. Version 2 records preserve the whole acknowledged payload and evidence. Version 1 remains readable with exact payload/evidence and no completions; omission of the new field preserves its original checksum encoding. Upgrade on acknowledgement or subsequent new publication.
- Completed batches are the durable progress ledger: their message identity/revision pairs are the processed set. No separate progress cache can disagree with receipts. Validation checks every pending/completed payload digest, path union, unique completed batch identity and absence of an already-completed pending ID.
- `Acknowledge(ctx, identity, batchID) (*Receipt, error)` requires caller-authorized execution success. Under the existing transaction, replay looks up history first; otherwise only the matching pending batch moves to history and clears pending. Every error returns a nil receipt, including ambiguous post-rename failure.
- `Receipt` exposes batch ID, `Identity`, processed `MessageRevision` pairs, `LastTextEntry`, `LastDeliveredEntry`, optional `ClosingEntry` and changed-path union. Projection and fresh decoding detach returned values. History retains capture input privately to validate the batch digest.
- `Receipts(ctx, identity) ([]Receipt, error)` returns acknowledgement-order history scoped to the canonical vault/conversation/generation tuple, surviving subsequent publication.
- `Unprocessed(ctx, identity, currentMessages) ([]Message, error)` validates caller-owned rows and preserves their order/attribution while selecting pairs absent from all acknowledged payloads. Stable IDs and scalar boundaries alone never establish progress.
- `Complete(ctx, identity, currentMessages, closure) (bool, error)` checks one durable snapshot: a non-nil matching acknowledged closure, every current pair processed and no pending batch. An open transcript is false. It does not infer closure from text boundaries.
- `Publish` preserves pending-first immutable retry behavior. Without pending, an exact historically committed payload returns nil without creating work; new publication retains all history. Callers select subsequent payloads using `Unprocessed` and may publish closure-only input.

## Concurrency model

No production goroutines or caches are added. All operations use `Store.transaction`: handle read lock, then cancellable process-shared flock, spanning read/modify/atomic publication. `Close` waits for operations. Concurrent reports accepted before acknowledgement are included; reports after it reject a no-longer-pending batch. Test goroutines/processes are joined with bounded contexts.

## State transitions and identity reuse

| Event | Race-enabled coverage |
| --- | --- |
| Reopen pending, failed attempt, empty retry, acknowledge, reopen committed | `TestAcknowledgementHistory` |
| Wrong/unknown ack, cancellation, duplicate ack and replay while newer pending | `TestAcknowledgementHistory`, `TestAcknowledgementFailures` |
| New batch and configuration changes with immutable pending | `TestAcknowledgementHistory`, existing `TestImmutableRetry` |
| Changed old row and concurrently observed new row | `TestRevisionCompletion` |
| Open text, newly observed closure-only ack, late text, edited old row | `TestRevisionCompletion` |
| Vault/conversation/generation reuse including routing ID reuse | `TestCompletionIsolation` |
| Concurrent reports, acknowledgements and later publication across handles/processes | `TestConcurrentCompletion` |
| Pre-/post-rename recovery and version-1 upgrade | `TestAcknowledgementFailures`, `TestVersionOneCompletion` |

## Error handling

Unknown/wrong acknowledgement returns `ErrMismatch` without mutation. Invalid caller row inputs return `ErrInvalid`. Unsafe/corrupt records fail visibly; query errors return no evidence or completion. Existing write, file-sync, close and rename faults leave pending retryable before rename; directory-sync failures may leave the complete new receipt/progress state, which recovery syncs. Existing 64 MiB record cap remains; exceeding it rejects the update without replacing state.

## Testing strategy

Write completion tests before implementation and observe missing-API failures. Assert complete receipt projections, replay with newer pending, detached history, revisions rather than high-water marks, closure-only completion and late-text invalidation. Reuse hooks for acknowledgement failures and helpers for process concurrency. Add version-1 bytes with the original schema/checksum and verify upgrade. Exercise corrupt/unsafe records through acknowledgement and each query without overwrite. Run `go test -race ./internal/memorycapture`, `go vet ./...` and `go build ./cmd/pyry`; the verifier owns the full-module gate.

## Open questions

None. Progress is the union of historically acknowledged pairs, not just the latest revision per row; opaque revisions have no order. History queries require the complete identity tuple so conversations with a reused generation label cannot mix.

## Documentation handoff

Pending for the documentation stage:
- In `docs/knowledge/features/memorycapture-package.md`, complete “Batch progress” with processed row revision tracking and add “Completion receipts”. Describe receipt identities/boundaries, durable failed-attempt changed-path unions, acknowledgement replay, historical receipt queries and finality against current eligible revisions, including closure-only acknowledgement and late-text invalidation.
- State there: receipts acknowledge processing and do not certify every statement as a fact. Retain the three distinct boundaries and immutable retry payloads.
- Update the package entry in `docs/knowledge/CATALOG.md` to include the completed contract.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] Persisted completed inputs pass `readRecord` validation; caller messages pass the existing field validation. Completion uses exact opaque revision pairs and a matching closure, never a scalar watermark.
- [Tokens] No credentials or tokens are introduced. Receipt contents remain private account state.
- [File operations] Existing descriptor-relative traversal, exact 0700/0600 modes, single-link checks, held inodes and atomic replacement apply to acknowledgements and all queries. MUST FIX addressed in design: preserve version-1 checksum encoding with an omitted empty completion field.
- [Subprocesses] Production starts no subprocess; tests re-exec their own binary with bounded cancellation.
- [Cryptography] Existing SHA-256 digests detect inconsistency, not malicious account-owner rewriting. No keys/nonces or secret comparisons are introduced.
- [Network and I/O] No networking; the existing 64 MiB cap bounds all retained history reads/writes.
- [Errors/logs] Existing sentinel errors retain content-free domain failures; no payload logging is added.
- [Concurrency] One flock spans acknowledgement and evidence reads/writes. Progress derives from committed history, preventing torn receipt/progress recovery; replay cannot clear a newer pending batch.
- [Threat model] Same-vault execution serialization and note-write/receipt crash-window recovery remain OUT OF SCOPE under #3174. Eligibility/immutable closure remain exporter-owned; scheduling, indexing and deletion stay downstream.

**Reviewer:** builder (self-review)
**Date:** 2026-10-11
