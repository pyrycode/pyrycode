# Entry-scoped public touches

## Files read

- `internal/thread/fold.go` → `Feed`, `addItem`, `Items`: entry boundaries, stable indexes and public omission.
- `internal/thread/observations.go` → `recordShown`: retained touches, first appearance and append watermark.
- `internal/thread/main.go` → `foldWork`, `closeText`, `applyTerminal`, `setContent`: intrinsic mutation hooks.
- `internal/thread/agent.go` → `applyAgent`: actual lifecycle changes and parent repair scheduling.
- `internal/thread/shell.go` → `applyShellForeground`: ordinary shells mutate through `applyTerminal`.
- `internal/thread/child.go` → `saveChild`, `resolveChildren`: scoped dependents, older-only parents and descendant expansion.
- `internal/thread/sends.go` → `sendFact`: delivery suppression preserves stable indexes.
- `internal/thread/corpus_golden_test.go`, `observations_test.go`, `child_work_test.go`: committed digests, public changes and affected-tree proofs.
- `docs/knowledge/features/thread-package.md` § Cache and epochs; main-thread folding § Visibility and live-state exclusions; background store § Bounded queries: watermark and suppression contracts.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Sessions, agents, messages, read marks: shown appearance/append advances read marks, lifecycle changes do not.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: serialized ownership, exact bytes and scoped checks.

## Context

An old child tree makes `recordShown` inspect the entire item history on every entry. This ticket changes only visibility bookkeeping; snapshot, difference and query rebuilding remain for #3328. The #3350 baseline is merged. #3351 overlaps `Fold` and `applyAgent`; its indexed lifecycle recomputation is independent of our local mutation hook. No decision record is needed.

## Design

Maintain a package-private deduplicated set of affected stable item indexes on `Fold`, reusable by later publication work. Additions, intrinsic mutations, child registration/reparenting, final child resolution and send suppression enqueue indexes directly. No discovery walk over unrelated items is allowed.

After `resolveChildren` has expanded the affected descendant closure and settled joins, process affected indexes in creation order. Retain a private ID-to-public-reachability map: children require an older reachable parent; hidden rows still count as reachable. Update that cache only for candidates. Existing `published` remains ever-public appearance state, independently of reachability and `Shown`. Preserve the existing touch rule (first public appearance or current revision), shown watermark rule (first public appearance or nonempty append), and withdrawal touch deletion/suppression boundary. Clear only the entry's candidate set after processing. No public types or signatures change.

Test-only work instrumentation counts hook registration/deduplication, candidate collection, ordering comparisons, reachability and item/parent lookups, skips, updates and clearing. Existing child counters additionally account for discovery/maintenance of the descendant closure. Lifecycle recomputation and text serialization are excluded.

Sizing: one deliverable, three acceptance criteria, zero new exported types, one internal signature caller, fewer than ten reject branches; forecast 550–700 written lines including tests and this plan, below 800.

## Concurrency model

`Fold` remains owner-serialized. New maps share its lifetime; no goroutines, locks or asynchronous work are introduced.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Added/hidden row; nonempty/empty append; lifecycle-only mutation | `TestPublicTouchesReference`, `TestFoldObservations` |
| Late parent repair, unchanged descendant revision becoming public | `TestPublicTouchesReference`, `TestFoldObservationChildren` |
| Reparenting to missing ancestor, descendant omission, repeated repair/reappearance | `TestPublicTouchesReference`, `TestPublicTouchAffectedWork` |
| Acceptance followed by delivery suppression; dropped/lost outcome | `TestPublicTouchesReference`, `TestStoreObservationDelivery` |
| Whole, chunked and single-entry replay with source/lifetime reuse | `TestFoldBaselines`, `TestThreadRecordedBaselines` |

## Error handling

Keep `Feed` validation and partial-error semantics. Candidates come only from internal stable indexes. Invalid or foreign facts do not enter mutation hooks. Suppression retires existing catch-up continuity without lowering `lastShown`; no new error surface exists.

## Testing strategy

First add an independent full-walk reference and deterministic work-growth proof. Scale unrelated settled rows and independent trees from 4 to 16 while keeping each affected tree/entry fixed; require less than 2x total counted visibility/discovery work for append, late parent, omission/reparenting and delivery. Compare touches, suppression, appearance history and watermark after each entry, and cached reachability with `Items`. Reuse unchanged corpus digests and observation/catch-up/store tests. Run touched-package race tests, `go vet ./...` and build to a scratch output. The dispatcher owns `make check` (including its full-module suite).

## Open questions

None. Parent reachability is creation-ordered, not display-order based; delivery/order changes cannot reorder the dependency walk.

## Documentation handoff

- Pending documentation stage: update `docs/knowledge/features/thread-package.md`, "Cache and epochs", to describe entry-scoped public-touch/appearance tracking after private joins, preserving `LastShownVersion` and suppression rules.
- Pending documentation stage: update `docs/knowledge/features/thread-package-main-thread-folding.md`, "Visibility and live-state exclusions", to explain that affected descendants are included when public reachability changes.

## Security review

**Verdict:** PASS

- Trust boundaries/threat model: `Feed`/`decode` retain recorded-data validation and conversation ownership; child resolution retains source/lifetime and older-parent checks. Cached reachability must match `Items`, including omitted ancestors and claimed delivery; reference tests enforce this confidentiality boundary.
- Tokens/cryptography: no credentials, randomness or cryptographic operations enter these in-memory indexes.
- Files/subprocesses/network and I/O: production changes do no I/O and start no subprocess; existing store and relay boundaries remain authoritative.
- Errors/logs/telemetry: counters contain only integer work totals and remain test-only; payloads are not logged and existing validation errors are unchanged.
- Concurrency: serialized `Fold` ownership remains mandatory; no new goroutines or lock ordering.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-14

## Revisions

- 2026-10-14, verifier finding 1: map iteration and `clear` traverse retained
  storage from the largest previous candidate set, so counting yielded keys
  missed historical-size work. `affected` is now a dense reusable index slice
  with a separate membership map used only for point lookup/insertion/deletion.
  `recordShown` sorts and visits the current slice, deletes membership for just
  those indexes, then resets the slice length without traversing its capacity.
  Registration counts duplicate lookups, insertions, appends and candidate copies
  when storage grows; cleanup counts each current candidate and deletion.
  `TestPublicTouchHistoricalPeakWork` closes unrelated trees of 4,096 and 16,384
  children before measuring fixed appends and duplicate registration. It requires
  dense traversal storage to prevent uncounted retained map scans and less than
  2x work after a 4x historical peak. Existing semantic/reference/corpus proofs
  still cover appearance, watermark and suppression. Security re-review: PASS;
  the container change preserves serialized ownership, stable-index validation
  and creation-ordered reachability, adds no I/O or payload exposure, and has no
  new security findings.
