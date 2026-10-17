# Store restoration from validated checkpoints

## Files read
- `internal/thread/store.go` — `run`, `start`, `publish`: readiness, reader hooks and joined lifecycle.
- `internal/thread/checkpoint_read.go` — `readCheckpoint`: complete graph validation without history reads.
- `internal/thread/checkpoint_import.go` — `ImportCheckpoint`: private continuation and visibility restoration.
- `internal/thread/checkpoint_write.go` — `commit`, `check`, `replaceState`: durable generations and reconciliation.
- `internal/thread/store_checkpoint.go` — worker acknowledgements, dirty marking and final certification.
- `internal/thread/publication.go` — `publicationCapture.capture`: initial candidates must include restored rows.
- `internal/history/forward.go` — exclusive positioning delivered by #3552, already merged.
- `internal/thread/checkpoint_resume_test.go` — `testResumeProof`: exact content, recipes and private publication oracle.
- `internal/thread/store_checkpoint_test.go` — existing dirty/final fault seams and timer control.
- `docs/knowledge/features/thread-package-cache-and-epochs.md` — durability ordering and epoch authority.
- `docs/knowledge/features/thread-package-background-store-replay.md` — readable Page bound and unavailable/retry contract.
- `docs/knowledge/features/thread-package-main-thread-folding-validation-checkpoints.md` — inventory proofs must include recipe-only changes.
- `docs/knowledge/features/history-package.md`, `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md` — history ownership and scoped verification.

## Context
ADR 045 requires restoring the saved index and folding only its surviving suffix.
The scalar certificate currently requires prefix replay and cannot authorize the
new continuation graph. No new decision record is needed. No other feature branch
touches the planned production files.

## Design
`run` probes Page for the readable bound, builds its recent source, and imports
checkpoint state. Reject incompatible/corrupt graphs, V beyond the readable bound,
or a changed checksum at V. Real history/I/O/containment failures remain unavailable.
Absent/rejected state starts a new reference fold and epoch; scalar-only migration
also refolds once with a new epoch. Valid clean state retains its epoch; valid dirty
state retains its fold but receives a new epoch. Scalar certificates cannot override it.

Before usability, durably dirty both existing state and scalar certificate while
preserving the saved fold boundary. Validate before truncating the generation through
a contained no-follow regular-file descriptor; check truncate, sync and close.
Remove only obsolete admitted generation leaves after establishing named state,
rechecking containment/admission and preserving that state's named generation.
The writer repeats cleanup after establishing a new generation.

Initialize all restored publication candidates, and initialize worker row-byte
acknowledgements plus writer acknowledgement from the complete saved inventory.
Position concrete production ForwardReaders at V without widening `Store.forward`;
wrapped fixture readers filter already consumed entries. Replay through the Page
bound before publication; retain existing tail, catch-up and retirement contracts.
Full-refold writers keep their fresh-generation policy across failed transactions.

## Concurrency model
Fold, source, acknowledgements and disk operations remain worker-owned outside
Store.mu. Existing context cancellation, tail join and identity checks are retained.
No new goroutines or locks.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Clean/dirty load, zero/private/unfinished cuts, no suffix, repeated unload/reload | `TestStoreResumeCuts` |
| Suffix at load and subsequent tail appends, retirement acknowledgement | `TestStoreResumeCuts` |
| Rejected checkpoint, scalar migration, reused entry V, fresh-generation retry | `TestStoreResumeReject` |
| Dirty surface failure followed by repair and Retry | `TestStoreCheckpointDirty` |
| Excess generation bytes, obsolete or unsafe leaves, reconciliation failure and Retry | `TestStoreResumeReconcile` |

## Error handling
Reuse reader/import absence and invalid-state errors for full refold. Preserve
ErrPersistence for actual persistence/containment failures and ErrUnavailable for
history failures. Never publish a cache-only view. Failed final certification keeps
the existing dual-surface invalidation behavior.

## Testing strategy
Write focused Store regressions first. Use `testResumeProof` on persisted results
and full replay; compare initial public snapshots and subsequent suffix publications.
Extend dirty fault tests with repaired Retry. Reuse existing reader/writer corruption
and transaction matrices. Measure Store readiness for the same 30000-entry fixture
under full replay/resume, stating V/suffix, OS and boundary; report history child's
real cold discovery/suffix accounting separately from Page/checksum/rendering reads.
Run admitted race thread/history packages, go vet ./..., and build cmd/pyry on Linux.
The dispatcher owns the full hermetic gate; no live-Claude check is required.

## Open questions
None. Estimated written work: 780 lines including this plan, zero exported types,
no public consumer migrations, five acceptance criteria, ten failure categories.

## Documentation handoff
Pending documentation stage:
- `docs/knowledge/features/thread-package-cache-and-epochs.md`: “Incremental checkpoint format”, “Store replay and public touches”, “Epoch recovery and scalar certificate lifecycle” and catch-up continuity: validated resume order, clean/dirty epochs, scalar migration, fallback/unavailable, descriptor truncation, cleanup and cold-read accounting.
- `docs/knowledge/features/thread-package-background-store-replay.md`: “Loading and replay readiness”: the same delivered loading contract.
- `docs/knowledge/features/thread-package.md`: “Cache and epochs”: replace pending-#3337 full-replay claims.
- `docs/knowledge/features/thread-package-main-thread-folding-validation-checkpoints.md`: “Export, import and ownership”: replace pending full-replay claims with restoration.

## Security review
**Verdict:** PASS
- Trust boundaries: MUST FIX addressed in design: imported graph alone is insufficient; validate readable bound and checksum at V before mutation/use.
- Files: MUST FIX addressed in design: truncate only no-follow regular descriptors; cleanup admits exact package leaves, rechecks containment and preserves named state. Unsafe leaves fail closed.
- Tokens/cryptography: reuse crypto/rand epoch tokens and SHA-256 entry checksum; no credentials or new primitives.
- Subprocess/network: no new subprocess or network boundary.
- Errors/logging: retain public sentinel errors and existing structured logging; no payload/path logging.
- Concurrency: existing worker serialization and joins; dirty durability precedes publication and original clean flag selects epoch.
- Threat model: history remains authoritative; no cache-only availability or protocol changes.
**Reviewer:** builder (self-review)
**Date:** 2026-10-17

## Revisions
- 2026-10-17: Read-accounting proof uses an external history test and a test-only
  counter adapter to exercise the real Store without exporting instrumentation
  in production. It separates Forward discovery/suffix, Page, checksum lookup,
  load and requested rendering. Existing scalar rejection tests explicitly remove
  checkpoint state to retain their scalar-only migration premise. Fixture-reader
  filtering uses the immutable saved boundary, so later duplicates still fail.
- 2026-10-17: Seed restored publication slots, recipes and activity directly from
  validated metadata rather than rendering every restored row just to classify
  unused initial changes. Only suffix candidates require change classification.
  Load timing ends at first usable Status; requested rendering is measured separately.
- 2026-10-17: Fixture certificates checksum the actual stored entry returned by
  Lookup: history JSON escaping can change raw payload bytes without changing
  rendered content, which would otherwise make the supposed resume test refold.
