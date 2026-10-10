# Thread observations and last shown versions

## Files read

- `internal/thread/fold.go` → `Feed`, `Items`: consumed IDs and public projection.
- `internal/thread/main.go` → `foldWork`: nonempty message text appends.
- `internal/thread/child.go` → `resolveChildren`: public parent resolution follows private joins.
- `internal/thread/sends.go` → `sendFact`: delivery suppresses its linked standalone row.
- `internal/thread/store.go` → `run`, `publish`, `Snapshot`, `Unload`, `Shutdown`: immutable publications and joined workers.
- `internal/thread/cache.go` → `recoveryCandidate`: cache items never seed continuation; epoch reuse is separate from range retention.
- `internal/thread/{fold,main,sends,store}_test.go`: existing facts, send joins and worker barriers.
- `docs/knowledge/features/thread-package.md` § Cache and epochs: reconstruct private state from history, preserve compatibility rules.
- `docs/knowledge/features/thread-package-background-store.md` § Loading and replay readiness / Isolation and lifecycle: no usable partial replay and no I/O under the publication lock.
- `docs/knowledge/features/thread-package-main-thread-folding.md`: hidden items remain in the public projection; suppression differs from visibility.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Versioning / Update messages / Read marks: entry IDs, base revisions and last-shown semantics.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: detached ownership, named symbols and counted checks.

## Context

Downstream live and summary consumers need one reusable observation seam. History remains durable truth; this change neither persists read marks nor emits wire traffic. No additional decision record is needed. No other fetched feature branch touches `internal/thread`.

Sizing: one observation contract, four acceptance criteria, two exported structs, no simultaneous consumer migrations, at most eight observation reject/outcome branches. Budget approximately 780 written lines including plan, implementation and tests; the snapshot/publication analogue #2712 includes similar concurrency proof but no fold differences.

## Design

`Snapshot` gains `LastShownVersion`. `Fold` reconstructs it while feeding each entry: newly public shown items and nonempty text appended to an already public shown message raise it to the current consumed entry. An ever-published identity set distinguishes first appearance from visibility changes; unresolved children count after joins. Final revisions never reconstruct this fact. Existing cache schema/rules and epoch validation remain unchanged because items are unchanged and the watermark is reconstructed every load.

Add `Observation` (a snapshot plus `FromVersion`, `Changes`, `BaselineRequired`) and `Change` (item ID, previous/new revision, consumed version, optional full addition, named replacement fields, text suffix). Field names match exported `Item` fields. Fields apply before the text suffix, then the revision; replacement content may clear fields. A suffix uses content's `text` field on message kinds only. Additions carry complete detached items. Projection differences run after joins. Suppressing any prior row makes the entire range baseline-required; there is no remove operation.

`Fold.Observe()` returns a baseline; `Fold.FeedChanges(entries)` returns the difference from its pre-feed baseline and consumed end version (including successfully consumed entries before an error). Existing `Feed` and `Items` remain usable unchanged.

`Store.Observe(id)` returns a detached baseline. `Store.Changes(ctx, id, epoch, after)` returns retained progress from exactly that publication boundary or an explicit baseline-required outcome. At the latest boundary it waits for progress or cancellation. Publication-boundary ranges need not admit intermediate IDs inside a chunk. Keep at most 64 publication batches and 1 MiB of encoded change data per worker; oversize, missing boundary, epoch mismatch and suppression require a baseline. Recovery retains no old batches, even when epochs match. Each result identifies start/end, epoch and end watermark. Only usable baselines include items; unavailable results have no versions/watermark.

## Concurrency model

Existing per-conversation goroutine owns folding. Differences and copies run outside the store lock. Under the same mutex used for publication, change requests capture both immutable publication/ranges and a notification channel. Publishing closes/replaces that channel, so a commit between baseline acquisition and waiting is found by version lookup. Waiters create no goroutines, write no files and hold no lock while waiting/copying. Lifecycle transitions notify waiters and discard ranges. Checkpoints still precede publication.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Baseline then commit before wait, no-op commit, repeated waits | `TestStoreObservations` |
| Delivery row published, later linking outcome suppresses it | `TestStoreObservationDelivery` |
| Retention overflow or wrong epoch | `TestStoreObservationRetention` |
| Unload/reload with reused epoch, clean cache restart | `TestStoreObservationRecovery` |
| Wait cancellation, unload, shutdown, unavailable/rebuilding | `TestStoreObservationLifecycle` |
| Private child becomes public, append, terminal status, dropped sends | `TestFoldObservations` |

## Error handling

Existing fold ID validation and partial-consumption behavior remain. Unusable views expose the existing state/error, no partial items or fabricated watermark. Consumer cancellation returns its context error. A usable view with an unprovable range exposes `BaselineRequired` with no applicable changes. Cache failures continue withdrawing publication and preserving existing generic errors.

## Testing strategy

Write offline assertions first and observe failure. Independently apply additions, fields and suffixes to a baseline and compare with full items. Test field clearing/replacement, multiple affected rows, private children, malformed/unsupported/foreign and hidden facts, empty suffixes, drop and delivery. Compare entry/chunk watermarks and replay/recovery. Deterministic publication boundaries and cancellation prove waiting/lifecycle isolation. Run `go test -race ./internal/thread/...`, `go vet ./...`, and `go build -o /tmp/builder-3074/pyry ./cmd/pyry`. The verifier owns the full-module gate.

## Open questions

None.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/thread-package.md` § Cache and epochs and `docs/knowledge/features/thread-package-background-store.md` § Loading and replay readiness / Isolation and lifecycle, document baseline/range observations, detached ownership, bounded retention, unavailable/baseline-required outcomes including linked delivery suppression. State the reconstructed last-shown rule, its zero value and `last-shown > ReadUpTo`; distinguish epoch reuse from retained change-range continuity.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `Feed` retains object/owner/ID validation; observation content is inert recorded data and callers retain conversation authorization responsibility.
- Tokens/cryptography: no new secrets or crypto; epochs retain `randomToken` and existing reuse rules.
- File operations: no new paths or cache fields; existing contained no-follow reads and atomic 0600 checkpoint writes remain.
- Subprocesses: no new subprocess execution.
- Network/I/O: no transport. SHOULD FIX: cap retained data by bytes as well as batches so a large recorded item cannot create unbounded delta storage.
- Errors/logs/telemetry: no payload logging; errors remain generic storage sentinels or context cancellation.
- Concurrency: SHOULD FIX: capture wakeup under publication lock and signal retirement/failure as well as successful progress; otherwise a waiter could miss a commit or hang on unload.
- Threat model: wire framing, negotiated authorization and emission are downstream in #3077; the API does not authenticate consumers.

**Reviewer:** builder (self-review)
**Date:** 2026-10-10
