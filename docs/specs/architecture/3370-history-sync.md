# Conversation-scoped history sync

## Files read

- `internal/history/log.go` → `convLog`, `AppendWithMetadata`, `load`, `writeSegment`, `resolveDir`: append serialization, cursor recovery and exact containment.
- `internal/history/segment.go` → `parseSegmentName`, `listSegments`: segment naming; the reader's nonregular-leaf filter cannot certify sync targets.
- `internal/history/forward.go` → `notifyCommitted`, `follow`: sync must publish no follower wakeups.
- `internal/history/log_test.go` → `appendN`, `historyDir`: existing paging and recovery fixtures.
- `docs/knowledge/features/history-package.md` → Directory resolution and Concurrency: re-resolve every call and retain the store-wide mutex.
- `docs/knowledge/features/history-package-failed-write-recovery.md`: cursor reload after a failed write must preserve independent pending sync state.
- `docs/knowledge/features/history-package-shape.md` → Shape: authenticated-conversation precondition and opaque payload boundary.
- `docs/knowledge/decisions/045-thread-index-over-history.md` → Decision 4 and Reliability: files precede directory durability before checkpointing.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: failure fixtures and race-enabled verification.

## Context

Appends currently reach the OS page cache without fsync. Thread checkpoints need an explicit durability barrier; #3336 will adopt it. This implements that barrier alone, with no production caller, format change or new decision record. #3369 coordinates edits to `log.go` but supplies no required API; no fetched feature branch overlaps the planned files.

Sizing: about 650 written lines including tests and this plan, zero new exported types/interfaces, zero consumer updates, four acceptance criteria and at most ten failure categories. This is smaller than #3045's replay lifecycle and within all five limits.

## Design

Add `func (s *Store) Sync(convID conversations.ConversationID) error` in `internal/history/sync.go`. Success covers all successful appends preceding its mutex boundary. Appends ordered afterwards remain pending. Ordinary `os.File.Sync` uses read-only descriptors on Linux/macOS; no full-flush call or append fsync.

`convLog` retains a separate first-success flag, pending segment-number set and directory-dirty flag. Successful appends mark their segment and fresh segment directory entry. `load` never resets these fields. Sync does not load/decode entries or touch append/read/follower state.

Every call re-resolves exact containment and lists segment names without the reader's nonregular filtering. Reject named unsafe leaves and absent pending targets. On first sync, merge all existing segments into the pending set before I/O, retaining them if anything fails. Missing/empty logs are no-ops only without known pending writes; they create nothing and do not suppress a future conservative first sync. After successful file syncs and closes, sync/close the history directory on first existing-log sync or fresh segment creation. Clear pending state only after full success. Repeated calls without writes perform no sync syscalls.

Use a store-local open/sync/close seam for hermetic observation and faults. Opens use `O_NOFOLLOW`, segment descriptors are checked as regular before and after open, and nonblocking opens avoid hanging on a substituted special leaf. Directory opens require a directory. Retain the existing daemon-owned-directory check/use threat boundary.

## Concurrency model

Hold `Store.mu` throughout target capture, validation and sync; appends cannot cross the captured boundary. No new goroutine or shutdown path. Test goroutines are released and joined.

## State transitions and identity reuse

| Event | Race-enabled coverage |
| --- | --- |
| Append, sync, append in same segment, roll and sync | `TestSyncTargetsAndSideEffects` |
| Multiple rolls before sync and conversation isolation | `TestSyncMultipleRollsAndIsolation` |
| Cold/reopened/read-loaded/append-loaded first sync; repeat | `TestSyncFirstAndEmpty` |
| Any sync/open/close failure and retry | `TestSyncFailuresRetry` |
| Pending segment/directory disappears then is restored | `TestSyncMissingAndUnsafeTargets` |
| Segment leaf replaced between listing and open | `TestSyncRechecksOpenedLeaf` |
| Failed append reload with previous pending writes and torn-tail roll | `TestSyncFailedAppendRecovery` |
| Concurrent append ordered after captured sync boundary | `TestSyncSerializesAppend` |

## Error handling

Validate ID shape before filesystem access and retain the authenticated-conversation precondition. Wrap I/O failures with operation context without payload bytes. Always close an opened descriptor, propagate sync and close errors together, and retain every pending target and directory obligation on failure. Return `ErrCorruptSegment` for unsafe segment leaves and the existing containment sentinel for redirects.

## Testing strategy

Write tests first and observe their failure before implementation. Observe exact sync paths and file-before-directory ordering, injected open/file-sync/directory-sync/close failures, retries, cold and warm no-ops, missing targets, directory redirects, read-only descriptors, append serialization, unchanged bytes/IDs/counters/followers and append absence of sync. Existing history paging, forward and failed-write suites run with `go test -race ./internal/history/...`; run `go vet ./...` and `go build ./cmd/pyry`. The verifier owns the full-module gate. No live tests or native evidence are required by this ticket.

## Open questions

None. The signature is `Sync(convID) error`, and the existing store-wide lock defines the boundary.

## Documentation handoff

Pending for the documentation stage: `docs/knowledge/features/history-package-shape.md`, **Shape**: list `func (s *Store) Sync(convID conversations.ConversationID) error` and state that successful sync covers preceding appends, includes new segment directory entries, propagates failure and leaves append durability unchanged until explicitly called.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `Sync` validates shape; authorization remains the caller's authenticated-conversation precondition, never a capability inferred from the ID.
- [Tokens/secrets] No generation or credential access. Descriptors sync opaque bytes without decoding content.
- [File operations] SHOULD FIX: reader-style filtering hides unsafe or missing targets. Strict name listing, remembered pending targets, `O_NOFOLLOW`, regular-descriptor checks and exact `resolveDir` equality implement the required refusal. Read-only opens create nothing and preserve permissions.
- [Subprocesses/cryptography] No subprocess or cryptographic operation is introduced.
- [Network/I/O] Local passive storage only. No payload allocation; directory enumeration follows the existing segment index design.
- [Errors/logs/telemetry] Contextual filesystem errors remain internal; no new wire caller or logging is added, and no content enters errors.
- [Concurrency] One existing mutex orders append and sync; partial failure retains obligations, and restart conservatively syncs all existing segments.
- [Threat model] Daemon-owned `0700` paths retain the existing single-call ancestor check/use window. Hostile external writers are unsupported; no broader network trust is granted.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14

## Revisions

- 2026-10-14: Cursor recovery also observes existing segment identities before a Store's first sync. `load` retains those targets as pending, so deleting a read-loaded target cannot turn its first sync into a successful empty-log no-op. Recovery still never resets pending work, and sync itself never decodes entries. Final sizing is about 754 written lines, zero exported types/interfaces, zero consumer updates, four acceptance criteria and ten failure categories, within all limits.
