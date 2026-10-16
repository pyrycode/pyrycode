# Revision-scoped direct-child summary pages

## Files read
- `internal/thread/queries.go`: `captureQuery` atomically captures immutable metadata and readiness.
- `internal/thread/query_tree.go`: `membership`, `advance`, `treePut`, `visit` maintain persistent affected-only selection.
- `internal/thread/publication.go`: `publicationCapture.capture` freezes public slots, including late membership changes.
- `internal/thread/fold.go`: `Item`, `Items`, `plainSummary` define identity/public membership; plainSummary alone is unbounded.
- `internal/thread/store.go`: `publish` replaces roots; retirement leaves captured storage readable.
- `internal/thread/query_incremental_test.go`, `queries_test.go`: incremental oracle, work counters and retirement barriers.
- `docs/knowledge/features/thread-package-background-store-queries.md`: public membership, reference-free metadata and complete-query bounds differ from strict child pages.
- `docs/knowledge/features/thread-package.md`, `development-verification.md`, `CODING-STYLE.md`: lifecycle and verification conventions.

## Context
Complete queries include closure, active extras and rendered detail. Expanding one parent requires strictly bounded direct children. This is one independently usable Store API, with no consumer migration or persisted format change. No decision record is needed. No overlapping feature branch edits the planned files.

## Design
`Store.ChildPage(ctx, conversationID, epoch, parent, revision, limit, continuation)` returns `ChildPageResult` and an error. `ChildSummary` explicitly copies metadata fields, supplies a bounded plain `Headline`, and marks `DetailOmitted`; it has no body. `ChildCursor` binds conversation, epoch/version, parent/revision and last delivered order/ID. `ChildPageResult.CursorAfter(n)` constructs continuation after any nonempty returned prefix; invalid prefix lengths fail.

Generalize the existing persistent AVL insertion internally to an optional leading group key. Existing trees use group zero unchanged; a children root uses parent as group and order/ID as the remaining tuple. Every public membership insertion/removal updates that root, including order zero. Two tuple ranks and a bounded visit select exactly the requested parent, ascending, with a hard ceiling of 256 even across ties. Subtree sizes establish authoritative More/Complete without descendant enumeration. Initial preparation is O(N log N), reads O(log N + limit), incremental updates follow affected rows/tree paths.

Copy only scalar/string metadata from captured rows. Normalize at most the first 256 input bytes of Summary, falling back to a bounded kind prefix if empty. Repair invalid UTF-8 and truncate only at rune boundaries to 256 output bytes; detach the resulting headline. Never call rendering/history lookup.

## Concurrency model
No new goroutines. Capture under Store.mu, select outside the lock against immutable roots. Cancellation checks discard all partial output and never withdraw a worker. Publication, unload and replacement do not invalidate captures; each subsequent call captures/validates current state.

## State transitions and identity reuse
| Event | Race-enabled test |
| --- | --- |
| Late resolution, withdrawal, reparent/order changes | TestChildPageIncrementalOracle |
| Repeated continuation and arbitrary prefix replay, tied/zero orders | TestChildPageSelection |
| Unrelated version advance or parent revision/epoch change | TestChildPageRefusals |
| Capture concurrent with feed/publication, retirement and replacement under same ID | TestChildPageCaptureLifecycle |
| Cancellation during selection while another reader succeeds | TestChildPageCancellation |
| Overlapping IDs in different conversations | TestChildPageSelection |

## Error handling
Canonical-ID and positive-limit errors reuse existing errors. New sentinel errors distinguish invalid child scope, absent/private parent, stale epoch/revision/version and invalid/mismatched cursor. A cursor position must name a captured public direct child with the exact order. Nonusable views retain existing State/Err semantics and expose no completion, rows or capture. Usable malformed/missing indexes fail closed. Validation/cancellation errors return zero results. Empty parents succeed with Complete true.

## Testing strategy
Write focused tests before production and observe compilation failure for the missing API. Compare three or more concatenated pages to an independently filtered/sorted Fold.Items oracle, plus synthetic ties/zero orders. Verify exact metadata, UTF-8 bounds/fallback, prefix resume, stale/malformed refusal and lifecycle capture/cancellation barriers. Measure metadata visits after preparation across growing unrelated rows, deeper descendants and large content/summary; assert zero history lookups. Measure affected-only preparation separately. Run Linux `go test -race ./internal/thread`, `go vet ./...`, `go build ./cmd/pyry`; full make check belongs to the verifier gate.

## Open questions
None. Cursor is a typed Store value, not a wire encoding or authorization credential.

## Documentation handoff
Pending documentation stage: `docs/knowledge/features/thread-package-background-store-queries.md`, “Bounded queries”: document the child-page API, summary fields/256-byte headline cap and bounded metadata-prefix work, public membership, parent-only tuple ordering, strict limits, captured revision/version and continuation refusal/prefix resumption. State that summaries omit detail without hydration, explain valid empty completion and lifecycle/error behavior, and distinguish these pages from complete-query coverage and its work bounds.

## Security review
**Verdict:** PASS
- Trust boundaries: ChildPage validates canonical conversation and nonzero parent/revision/nonempty epoch; public index lookup excludes private parents. Cursor scope and current capture are checked before selection. Authorization remains caller-owned (#3522/#3523).
- Tokens/cryptography: cursor and epoch comparisons establish consistency, not secrets or authorization; no credentials, randomness or crypto added.
- Files/subprocess/network/I/O: no paths, subprocesses, sockets or history reads; persisted checkpoints unchanged. Wire parsing and byte packing remain #3522/#3523.
- Errors/logs: fixed sentinels contain no content, source errors or paths; no new logs.
- Concurrency: Store.mu is the sole acquisition lock; all selection uses persistent captured roots. No new goroutines or shared mutable result storage.
- Threat model: bounded 256-row and 256-byte-prefix work prevents content-dependent query scans; malformed/mismatched typed cursors fail closed. Network authorization/framing stays outside Store.

**Reviewer:** builder (self-review)
**Date:** 2026-10-16

Sizing recheck: approximately 760 written lines (280 production, 410 tests, 70 plan), three exported types, zero migrated consumers, five criteria and fewer than ten distinct rejection branches.

## Revisions
- 2026-10-16, verifier finding 1: `TestChildPageCaptureLifecycle` must pause only the first metadata visit without serializing other readers behind the pause. Replace the blocking `sync.Once` hook with an atomic first-entry barrier, and require a concurrent `Snapshot` of the same publication to finish before appending history. Preserve publication, unload, replacement, captured-result equality and stale-continuation assertions. Production contracts and the security review remain unchanged.
