# Immutable capture batches and attempt evidence

## Files read
- `cmd/pyry/memory_transcript_fold.go` → `memoryTranscriptReader.files`: exporter-owned generation, ordered rows and three distinct boundaries.
- `cmd/pyry/memory_transcript_files.go` → `memoryTranscriptDir`, `publishMemoryTranscript`: held-directory confinement and destination revalidation.
- `internal/memoryruntime/storage.go` → `regularFile`, `acquire`, `publish`: private records, kernel flock and durable atomic replacement.
- `docs/knowledge/features/memoryruntime-package.md` → Concurrent installation and publication: process-local locks do not exclude other daemons.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change: inspect complete reopened values and failure outcomes.
- `docs/guide.md` → Recent conversation transcripts: stable rows, generation reuse and eligible-text boundaries.
- `CODING-STYLE.md`: Go, test and persistence contracts. No existing memorycapture overview exists.

## Context
Capture must publish its exact input before execution and retain reported changed paths even when execution fails. This package is independently usable before runners and acknowledgement. No production consumers change. Remote #3171, #2873 and #2882 do not touch this package.
Sizing: approximately 850–950 written lines, five exported types, zero consumer migrations, four criteria, at most ten grouped state rejection cases. The line ceiling is exceeded; the actual grandchild lineage #3101 → #3173 → #3177 requires building through, recorded with `needs-human:sizing`.

## Design
`Identity` contains caller-resolved canonical vault, conversation and transcript generation. A hash of this tuple names a record under `~/.pyry/memory/capture`; no identity becomes a filesystem component. `Message` preserves caller-owned identity, revision, text and attribution. `Payload` preserves capture agent/model, ordered messages, metadata and LastTextEntry, LastDeliveredEntry and optional ClosingEntry. `Batch` wraps its content-derived identity, immutable Payload and separate ChangedPaths union. `Store` owns the private held directory and exposes `Publish(ctx, identity, payload)`, `Pending(ctx, identity)` and `Report(ctx, identity, batchID, paths)` plus `Close`.
Publication returns the existing batch before considering newer payload/configuration. Each operation reads from disk under a kernel flock, preventing lost updates across processes/handles. Serialization and fresh decoding detach all caller-owned slices, raw JSON and closure pointers. A versioned record binds identity and batch digest; malformed or inconsistent records fail closed. No acknowledgement or progress stub is introduced.
All evidence paths must be canonical relative slash paths, contain no traversal, and refer only to the vault namespace; reject the entire update before persistence if any path is invalid. Sort and deduplicate the union. No note files are read or written.
Persistence uses descriptor-relative no-follow leaf operations, exact private modes, owner/type/link checks, a unique exclusive temporary file, write/sync/close, destination identity revalidation, rename and directory sync. Revalidate the directory path and lock/record inode at operation boundaries. Failed writes return no executable batch; successful rename followed by sync failure remains recoverable. Recovered records are synced before being authorized for execution. Package-local operation hooks inject each failure boundary and substitution.

## Concurrency model
No background goroutines. A Store read lock protects its held directory from Close. A cancellable nonblocking flock serializes disk read/modify/write across all handles/processes; closing its operation-local descriptor releases it. Validate lock inode after acquiring it and again before publication. Cancellation is checked while waiting and before publication.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| First publication, closure-only publication | TestImmutableRetry |
| Retry after failed/cancelled execution, newer text/config and returned mutation | TestImmutableRetry |
| Reopen and routing ID reused in a different generation | TestIdentityIsolation |
| Concurrent publishers and accepted evidence updates across handles/processes | TestConcurrentStores, TestHelperProcess |
| Failed attempt followed by empty evidence retry | TestEvidence |
| Pre-rename failure and post-rename sync failure followed by retry | TestPublicationFailures, TestEvidenceFailures |
| Record, lock or directory substitution | TestFilesystemAttacks |

## Error handling
Unknown/mismatched evidence, invalid identities/paths, unsafe filesystem entries, corruption and I/O failures return errors and preserve complete previous state. Pending absence returns nil. No payload text is included in errors. Crash leftovers are never read as authoritative records.

## Testing strategy
Write behavioral tests first and observe undefined API failures. Cover deep returned-value mutation, immutable retries, reopen, closure-only inputs, identity isolation, concurrent processes, all-or-nothing path rejection, corruption, modes/ownership/link/type attacks and swaps before rename. Table-test write, file-sync, close, rename and directory-sync failures for both first publication and evidence union, asserting full reopened records and recovery of post-rename bytes. Run package race tests, module vet and binary build; the verifier owns the full-module gate.

## Open questions
None. Exporter supplies canonical vault identity, eligibility, revisions and generation; the store does not infer them. Note-write crash-window discovery is owned by #3174 and acknowledgement by #3178.

## Documentation handoff
Pending documentation stage: add `docs/knowledge/features/memorycapture-package.md`, section “Batch progress”. Describe pending-before-execution, immutable retry payloads, captured row revisions, Last text entry versus Last delivered entry versus immutable Closing entry, and durable failed-attempt changed paths. State that this storage slice ships before acknowledgement and runner wiring. Add its entry to `docs/knowledge/CATALOG.md`. “Completion receipts” belongs to #3178.

## Security review
**Verdict:** PASS
- Trust boundaries: Publish validates identity and payload; record decoding verifies schema, identity and batch digest before authorization. Report validates the complete path list before updating.
- Tokens/credentials: no credentials, tokens or launch configuration are accepted. Stored conversation text requires exact 0600 records and 0700 private directories.
- File operations: MUST FIX resolved in design: path-only checks permit substitution. Held descriptors, no-follow traversal, inode comparison and destination revalidation are required, including the lock file and temporary publication source.
- Subprocesses: no production subprocesses; helper processes exist only in isolated tests.
- Cryptography: SHA-256 binds payload identity, not authentication; random temporary names use crypto/rand. Same-account malicious text rewriting is outside cryptographic authenticity guarantees.
- Network/I/O: no sockets/network. Record reads must be bounded and malformed/truncated records rejected.
- Errors/logs: errors identify operations, never captured text; no payload logging.
- Concurrency: kernel flock spans the entire transaction; no goroutines, stale in-memory evidence or process-local-only exclusion.
- Threat model: filesystem redirection and unsafe state are in scope; executing capture, recovering unreported note edits (#3174) and receipts (#3178) are explicitly deferred.
**Reviewer:** builder (self-review)
**Date:** 2026-10-11

## Revisions
- 2026-10-11: `TestFilesystemAttacks/regular-swap` demonstrated inode reuse after closing the validated record. Keep that descriptor alive throughout the transaction and duplicate the temporary descriptor through close/rename. Revalidate the published inode and complete bytes after directory sync, with `TestPostRenameSubstitution` covering post-rename record, lock and directory swaps. The contract remains no executable batch on any failure, including ambiguous committed writes.
- 2026-10-11: Reject invalid UTF-8 rather than let JSON silently replace captured text or identity bytes. Sync every private directory's parent during New, including existing entries from another creator or an earlier failed mkdir sync. Bound record reads at 64 MiB. Partial-write and structured-corruption tests extend the fault proof. Final written work exceeds the initial 850–950-line sketch (approximately 1,300 lines); the recorded grandchild exception still applies. Five exported types and zero consumers remain unchanged.
- 2026-10-11: `TestOwnershipValidation` covers foreign-owned records, private directories and home directories without requiring chown privileges; the real foreign-owner filesystem fixture remains conditional on root. Final written work is approximately 1,440 lines including the plan. All persisted payload and evidence contracts are unchanged.
