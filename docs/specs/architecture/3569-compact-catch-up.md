# Compact catch-up and coalesced live progress

## Files read

- `internal/thread/compact_queries.go` → `CompactQueryResult`, `compactQuery`, `compactMembership`, `refreshRootActivity`: root membership, header/message split, active extras.
- `internal/thread/query_tree.go` → `membership`, `advance`, `evidence`: dirty set over old and new ancestor chains; every dirty row gets `refreshHeader(row, f.version)`.
- `internal/thread/child_headers.go` → `refreshHeader`, `childHeader.subtreeVersion`: the version a row's public subtree last changed, i.e. the root's compact freshness.
- `internal/thread/catch_up.go` → `CatchUp`, `prepareCatchUp`, `MaxCatchUpGap`: complete-view evidence (`complete`, `unprovenThrough`, `suppressed`) that compact must not borrow.
- `internal/thread/observations_store.go` → `Changes`, `notify`, `retain`: waiter loop on `w.changed` captured under `Store.mu`.
- `internal/thread/store.go` → `publish`, `run`, `Unload`, `Shutdown`: index construction vs advance; lifecycle paths nil `w.query` and notify.
- `internal/thread/compact_queries_test.go`, `query_incremental_test.go` → `testCompactOracle`, `testPublicationFixture`, work counters.
- `docs/specs/architecture/3559-compact-thread-contract.md` → Design: compact intervals, explicit progress, projection-scoped evidence.
- `docs/knowledge/features/thread-package-cache-and-epochs.md` § "Catch-up continuity": complete evidence semantics.

## Context

#3568 shipped compact windows/pages. Consumers reconnecting or following live need compact intervals: which roots changed since a version, without touched-row (`touched`) or descendant (`active`) trees, which scale with hidden inventory. Relay delivery and flush scheduling are #3562/#3563. No other fetched feature branch touches these files. No decision record needed; the documentation stage extends the existing topics.

Sizing: one deliverable; ~120 production, ~330 test, ~110 plan lines; zero new exported types (two methods, two fields on `CompactQueryResult`); zero consumer migrations; four ACs; seven reset branches.

## Design

**Root touch tree.** `queryIndex` gains persistent `rootTouches`, keyed `(header.subtreeVersion, ID)` for every public row with `Parent == 0`, maintained in `compactMembership`. `advance` already marks every affected row and all old/new ancestors dirty and refreshes their header freshness to `f.version`, so a root's key is exactly the last version its compact state (own fields, header count/activity/freshness) changed. A nested burst under a stable root `Rev` moves the root's key. Each root occupies one key, so a range read yields each root once with its final state.

**Compact evidence** (index fields, copied by value through `advance`):
- `compactFrom`: `fold.version` when this index was constructed (first publication of a worker run). Touches before it are unknown. A reload or rebuild that reuses the epoch resets it, so intervals starting earlier reset.
- `compactLost`: latest version at which a row left the root set (withdrawn, suppressed, or reparented under another row). The client has no root-removal operation, so intervals spanning it reset. Nested suppression/reparenting is repaired by the dirty ancestors' headers.

Complete evidence (`complete`, `unprovenThrough`, `suppressed`, retained `ranges`) is never consulted, and compact evidence is never consulted by complete reads.

**API** (`CompactQueryResult` gains `FromVersion uint64` and `ResetRequired bool`):
- `CompactCatchUp(ctx, id, epoch, afterVersion, limit)`: version zero delegates to `compactQuery` newest (same validation, clamp, result; epoch ignored so recovery always succeeds). Nonzero ignores limit, captures once, and returns roots in `rootTouches[afterVersion+1 ..]` plus `activeRoots` extras, deduplicated.
- `CompactChanges(ctx, id, epoch, after)`: waits while usable, epoch matches and `after == Version`; then returns roots touched in the interval without extras (coalesced final states). An interval with no touched roots returns usable, `FromVersion`/`Version` and no rows: explicit nonvisual progress. Lifecycle change returns the nonusable state; retirement returns the zero result.

Shared `compactInterval(ctx, id, snap, q, epoch, after, extras)` produces either the interval or a scoped reset (`State`, `ConversationID`, `Projection`, `Epoch`, `Version`, `ResetRequired`; no rows, no `FromVersion`, no order bounds). Reset when: epoch mismatch; `after > Version`; `Version-after > MaxCatchUpGap`; `q == nil`; `q.coveredThrough != Version`; `after < q.compactFrom`; `q.compactLost > after`. Row conversion and message rendering reuse `compactQuery`'s add/`renderItems` path, extracted into helpers; tool/agent rows use prepared headers only.

## Concurrency model

No new goroutines. Capture `snapshot`/`query` under `Store.mu`; select and render outside. `CompactChanges` captures `w.changed` under the same lock as the version check, as `Changes` does, and every lifecycle path already notifies. Indexes are immutable after publication; new fields are scalars copied in `advance`.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Nested bursts under stable root `Rev`, repeated activation of one root | `TestCompactInterval`: oracle of independently recorded per-root change versions |
| Hidden-only (private/nonvisual) source advance | `TestCompactInterval`: usable, explicit progress, no rows |
| Reparent nested→nested, nested→root, root→nested; root suppression | `TestCompactInterval`: repaired vs reset per evidence |
| Same-epoch reload/rebuild (index rebuilt, `compactFrom` moves) | `TestCompactCatchUpReload` |
| Waiting reader across publication, cancellation, unload, concurrent captures | `TestCompactChangesWait` |

## Error handling

Cancelled context first, then ID, then (version zero) limit. Nonusable states return `{State, Err}` only. Render failure returns `StateUnavailable/ErrUnavailable` through existing guarded withdrawal; cancellation returns zero result and `ctx.Err()` without withdrawal.

## Testing strategy

Tests first. A fixture publication helper records, independently of the index, the version at which each root's subtree (computed from fold items by parent walk, before and after each publication) last differed; the oracle derives expected roots for every `after` and compares exact header/message facts with `testCompactOracle`-style checks. Reset table covers each branch, including equal-epoch reload. Work test: grow hidden descendants and summary/body size with fixed affected roots/extras; request visits bounded, zero preparation counts, lookups only for selected messages. Waiting test with `-race`: blocked reader released by publication, cancellation, unload; concurrent complete `CatchUp`/`Changes` unaffected. Checks: `internal/thread` race tests, `go vet ./...`, `go build ./cmd/pyry` on Linux.

## Open questions

- Is a client at exactly `compactFrom` after a same-epoch rebuild safe? Yes: epoch and version name one deterministic consumed source state, and the rebuild's root touches are all `compactFrom`, so the interval is exact.

## Documentation handoff

Pending for the documentation stage:
- `docs/knowledge/features/thread-package-background-store-queries.md`, extend "Compact root windows and pages" (or sibling section): catch-up and live interval semantics, coalesced final root facts, explicit nonvisual progress, scoped reset and version-zero reinitialization, preparation/request work bounds; production installation pending #3563.
- `docs/knowledge/features/thread-package-cache-and-epochs.md` § "Catch-up continuity": compact projection evidence (`compactFrom`, `compactLost`) vs complete-thread certification and expansion completion, including reset when compact evidence is absent under a reused epoch.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] SHOULD FIX: `epoch` and `afterVersion` come from a remote consumer via #3562. `compactInterval` must check `after > Version` before computing `Version-after` or `after+1`, so `MaxUint64` cannot wrap into a full-inventory range. Canonical ID validation stays the existing `conversations.ValidID` check.
- [Trust boundaries] SHOULD FIX: tool/agent roots are emitted only from prepared `childHeader` facts; tool bodies and hidden descendants never reach `renderItems`. The work test asserts lookups name selected messages only.
- [Tokens, secrets, credentials] No credential handling. Synthetic secret-bearing tool content in tests must never appear in compact output.
- [File operations] No new paths or writes; message hydration reuses the existing `s.lookup` containment.
- [Subprocesses] None.
- [Cryptography] None.
- [Network and I/O] OUT OF SCOPE: wire budgets, frame fragmentation and authenticated admission belong to #3562; flush cadence for waiting readers to #3563. Per-request output is bounded by affected roots within `MaxCatchUpGap` plus indexed active roots.
- [Errors, logs, telemetry] Render failure exposes only `ErrUnavailable`; no payload logging. Reset results carry no rows or bounds, so a stale client cannot read partial state.
- [Concurrency] One mutex (`Store.mu`), held only to capture `snapshot`, `query` and `changed`. A waiting reader owns no worker; unload, shutdown and failure notify, so waiters return. Cancellation returns a zero result and never withdraws.
- [Threat model] Compact results certify compact root coverage only; they cannot be presented as complete-thread or expansion certificates (separate evidence, separate fields).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17

## Revisions

- 2026-10-17: `refreshHeader` now removes the stored row rather than the row it is passed. `publish`'s first-index path applies the capture header before calling it, so removal keyed by the replacement header's `subtreeVersion` missed the `rootTouches` entry and left one stale entry per row. Stale keys sat at or below `compactFrom` and were never selected, but they made request rank/visit work grow with inventory (`TestCompactIntervalWork`: 35 vs 59 visits before, 11 vs 11 after). Contract unchanged; other trees' keys are identical between the two rows.
- Final measure: ~95 production, ~480 test, ~120 plan lines; zero new exported types; native verification on Linux.
