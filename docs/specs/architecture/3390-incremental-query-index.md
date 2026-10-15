# Incremental query publication

## Files read
- `internal/thread/publication.go` → `publicationCapture.capture`: affected rows, immutable content and stable slots.
- `internal/thread/store.go` → `run`, `publish`, `captureQuery`: worker ownership and atomic publication.
- `internal/thread/queries.go` → `queryIndex`, `window`: continuous ties, active extras and parent closure.
- `internal/thread/catch_up.go` → `prepareCatchUp`, `CatchUp`: touch, gap, suppression and coverage evidence.
- `internal/thread/publication_test.go`, `corpus_generator_test.go`, `corpus_golden_test.go`: affected-row oracle, ownership, generated and shadow fixtures.
- `cmd/pyry/thread_corpus_test.go` → `testRecordedFrames`, `testNormalizeRecorded`: the three recorded histories.
- `docs/knowledge/features/thread-package-background-store.md` → Bounded queries and Store verification: reads must remain detached and cannot block unrelated conversations.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: cancellation, race checks and independent evidence.

## Context
Every tail publication currently scans and sorts all public rows. Maintain the same query contract from captured affected slots and fold coverage metadata. No persistence or wire changes and no decision record needed. Branches #3382 and #3389 overlap `store.go` in checkpoint and metadata plumbing; this change remains local to publication.

## Design
Use package-private immutable AVL trees for item-ID lookup and ordered, active and latest-public-touch membership. Nodes retain immutable item pointers, ordering keys and subtree sizes. Updates copy affected paths, rebalance deterministically and support rank/range selection without a full scan. Initial replay and the fresh-build oracle populate trees from all public items.
Capture preserves affected slot indexes for publication and returns the immutable stable slot vector, including private nil slots. Detached full outputs skip nil slots. Ordinary publication copies that pointer vector once, with no Content copy or public-row discovery scan, then updates trees from affected old/new items. Remove old memberships before inserting current ones, allowing older-order insertion and delivery suppression. Carry touch versions separately from Rev. Coverage, suppression and missing-history metadata advance even with no affected items; maintain invalid-touch evidence without rescanning.
Query selection uses bounded tree ranges plus active and parent closure; scalar snapshot metadata and tree roots install together. Existing public validation, reset rules and detached output remain unchanged.

## Concurrency model
No new goroutines or locks. The conversation worker owns capture and tree updates. `Store.mu` installs immutable roots/items and scalar metadata atomically; selection and Content copying run after releasing it. Old roots remain valid through feed, retry, replacement and retirement.

## State transitions and identity reuse
| Event | Race-detector proof |
| --- | --- |
| Repeated touches, active settlement, late public children, reparenting and delivery suppression | per-entry incremental/rebuild oracle and existing `TestStoreQueriesUpdates` |
| No-item progress, missing IDs, epoch/reset/coverage | oracle and existing `TestCatchUpContracts`, `TestCatchUpMissingHistory` |
| Old captured roots during later feed/replacement/retirement | retained-root oracle and existing ownership/query isolation tests |
| Retry, unload/reload and clean reopen | existing Store lifecycle and bounded recovery tests |

## Error handling
Preserve all existing Feed/render/checkpoint failures. Only successful capture is published. Cancellation discards partial query output; tree traversal must check context during selection. Missing catch-up parents still reset.

## Testing strategy
First add a complete-publication benchmark and deterministic preparation-growth assertion; observe the rebuild path fail. Compare incremental versus freshly rebuilt query/catch-up results and full metadata after every generated/shadow/recorded entry, disregarding only item order. Reuse recorded helpers and existing fixtures without changing goldens. Retain old roots and compare after subsequent updates.
Measure state-changing add and fixed append publications at 50,000 prepared public items, including capture, Changes retention and index installation, excluding replay/checkpoints/output copies. Compare semantic work at 12,500 and 50,000 items; separately count pointer copies. Record before/after benchmark evidence in the PR. Run scoped race tests for thread, daemon and relay, `go vet ./...` and `go build ./cmd/pyry`; dispatcher owns `make check`.
Sizing: one deliverable, approximately 790 written lines (350 production, 365 tests/benchmarks, 75 plan/revisions), no exported types/interfaces, at most eight updated consumers, four criteria and fewer than ten reject branches.

## Open questions
None; balanced immutable paths preserve captured-read ownership without global-lock selection.

## Documentation handoff
Pending documentation stage: `docs/knowledge/features/thread-package-background-store.md`, **Bounded queries**: describe incremental maintenance after initial replay, older-order insertion/suppression and consistent concurrent selection, replacing the per-publication rebuild cost claim. Preserve externally visible bounds, reset, authorization and detached-result rules.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: query validation stays in `query`/`CatchUp`; callers retain conversation authorization. Private nil slots must never enter tree membership or detached outputs.
- Tokens and cryptography: no new secrets or primitives; epoch generation remains unchanged.
- Files: no new paths or I/O; checkpoint containment and atomic writes remain unchanged.
- Subprocesses and network/I/O: none added; queries remain memory-only and bounded by selected results and tree height.
- Errors/logs/telemetry: preserve generic errors and add no payload logging.
- Concurrency: immutable roots and item bytes are mandatory; in-place tree rotations would corrupt captured readers. Use copied rotation nodes and test retained roots under race detection.
- Threat model: deterministic AVL balance bounds adversarial ordering; existing relay authentication/authorization and request validation remain the boundary.
**Reviewer:** builder (self-review)
**Date:** 2026-10-15
