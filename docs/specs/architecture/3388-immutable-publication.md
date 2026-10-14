# Immutable affected-item publication

## Files read
- `internal/thread/fold.go` → `Feed`, `addItem`: partial progress and creation order.
- `internal/thread/observations.go` → `recordShown`, `difference`: post-join visibility and exact replacement/suffix contract.
- `internal/thread/render.go` → `render`, `recentSource`: resolve reference-backed content without changing public ownership.
- `internal/thread/store.go` → `run`, `publish`, `Snapshot`: successful-chunk publication and checkpoint scheduling.
- `internal/thread/queries.go`, `internal/thread/catch_up.go` → `queryIndex`, `prepareCatchUp`: immutable captured reads and rebuilt indexes.
- `internal/thread/observations_store.go` → `retain`, `Changes`: detached bounded change retention.
- `internal/thread/corpus_test.go`, `internal/thread/observations_test.go`: unchanged corpus and independent patch application.
- `docs/knowledge/features/thread-package-background-store.md` → snapshots, retention and lifecycle: private unresolved activity prevents retirement; retained suffixes must not pin accumulated text.
- `docs/knowledge/features/thread-package.md` → Cache and epochs; ADR 042 Update messages: preserve checkpoint evidence and wire semantics.

## Context
Tail publication currently renders/copies all content and compares every public row. Replace capture and comparison with final affected rows while retaining index rebuilds. No folding, wire or persistence format changes. No new decision record is needed.
Overlap: #3382 changes checkpoint plumbing, #3389 adds metadata access, and #3401 adds fold ingestion/text state. Keep edits local; no design needs their unmerged symbols.

## Design
Store enables an internal post-join candidate collector on its fold; public `Feed` and `FeedChanges` keep their signatures and partial-error behavior. Deduplicate candidates across successful chunks/replay and compare final membership against the previous publication, never intermediate entry visibility.
An internal capture owner retains stable row slots containing immutable item pointers and an active-index set including private rows. Clone the pointer slice, replace only affected public values, and resolve reference recipes only for those values. Fold content is replaced rather than mutated; internal rendering borrows immutable bytes. Public rendering and public Store results continue copying content.
Run the existing `difference` on ordered candidate subsets, preserving every field, addition, revision and suffix rule. Withdrawal of a previously public row requests a baseline. No-op chunks still advance metadata and notify waiters. Failed feeds never call capture/publication.
Indexes rebuild from immutable pointers; existing test constructors may adapt value slices into pointers. Snapshot materializes detached values outside the mutex. Checkpoints materialize complete item values only at initial, scheduled or lifecycle checkpoint boundaries, retaining the base schema and recovery validation.

## Concurrency model
The existing conversation worker exclusively owns folding, candidate collection and capture. No new goroutine. Store.mu publishes metadata, immutable index and retained batches atomically. Readers hold immutable pointers after releasing the lock; existing cancellation and joins remain unchanged.

## State transitions and identity reuse
| Event | Race-detector proof |
| --- | --- |
| Repeated touches, late appearance/reparenting, delivery/suppression, no-op entries | `TestAffectedPublicationOracle` across single-entry and chunked corpus feeds |
| Later feeds while old publications/query reads/results remain retained | `TestAffectedPublicationOwnership` and existing query concurrency tests |
| Failed chunk with partial progress, retry, unload/reload, retirement | existing `TestStoreFailureRetry`, `TestStoreLifecycle`, cache continuation tests |
| Active private work becomes public or settles | oracle activity check and existing checkpoint scheduling tests |

## Error handling
Reference lookup failure rejects the entire candidate publication; the worker withdraws usability as before. Feed errors retain public Fold partial progress but never become Store progress. Checkpoint errors preserve existing generic persistence failure behavior.

## Testing strategy
Write focused oracle and ownership tests before implementation and observe their failure. Reuse the fixed corpus and unchanged golden gates. Compare old render/difference and candidate capture/difference phases on identical bounded prepared inputs, excluding replay, indexes, checkpoint I/O and rendering output. Run thread race tests, the requested daemon/relay race packages, go vet and go build. The dispatcher owns `make check` and the full-module verifier gate.
Sizing: one publication behavior; approximately 720 written lines (300 production, 340 tests/benchmarks, 80 plan/revisions), zero exported types, fewer than ten consumer sites, four acceptance criteria and fewer than ten reject branches.

## Open questions
None. Preserve reference-backed rendering already present in the implementation base.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/thread-package-background-store.md`, **Loading and replay readiness** and **Snapshots and retry**: describe affected-item comparison and immutable shared internal publications while public results remain detached.
- Pending documentation stage: `docs/knowledge/features/thread-package.md`, **Cache and epochs**: distinguish publication capture from checkpoint materialization where applicable and preserve activity, epoch and retention rules.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: `Feed` validates recorded input as before; recipe resolution remains through the Store-owned source. MUST FIX addressed in design: public APIs must copy shared content before exposing it to callers.
- Tokens and cryptography: no new credentials, randomness or cryptographic operation; epoch creation is unchanged.
- File operations: checkpoint materialization retains `writeCacheFile` containment, modes and atomic replacement; no new paths or file operations.
- Subprocesses and network/I/O: none added; daemon delivery and wire encoding remain unchanged.
- Errors/logs/telemetry: lookup failure continues withdrawing publication with generic errors; no payload logging added.
- Concurrency: Store.mu protects publication replacement; fold mutation is worker-owned and old publications contain value copies with immutable content storage. Reader mutation tests must prove public detachment.
- Threat model: authorization stays at existing conversation-facing callers; this change adds no external endpoint or permission.
**Reviewer:** builder (self-review)
**Date:** 2026-10-14

## Revisions
- 2026-10-14: `TestStoreReferenceRetry` previously required unrelated no-op tails to reread unchanged source content. Tail failure now uses a delivery join to an evicted reference, proving a required read failure rejects partial progress and fresh retry recovers. Internal additions share immutable Content; the public `difference` wrapper continues detaching additions. Each captured item has its own allocation so retaining one unchanged pointer cannot pin all replaced content from an earlier chunk.
