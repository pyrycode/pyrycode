# Indexed compact root windows and pages

## Files read

- `internal/thread/queries.go` → `queryIndex`, `window`, `captureQuery`: immutable capture and continuous order ranges.
- `internal/thread/query_tree.go` → `membership`, `advance`: persistent memberships and old/new ancestor invalidation.
- `internal/thread/child_pages.go`, `child_headers.go` → `ChildSummary`, `childRankWork`, `childHeader.apply`: bounded prepared facts and detached optional values.
- `internal/thread/store.go`, `store_render.go` → `publish`, `renderItems`, `withdraw`: atomic installation, selected hydration and worker identity guards.
- `internal/thread/child_headers_test.go`, `query_incremental_test.go`, `child_pages_test.go`: publication fixtures, independent membership and work counters.
- `docs/knowledge/features/thread-package-background-store-queries.md` and `thread-package-background-store-verification.md`: separate preparation/copy/request work; disarm capture barriers for independent readers.
- `docs/specs/architecture/3559-compact-thread-contract.md` → Design: compact root coverage and mixed messages/headers.
- `CODING-STYLE.md`: errors, ownership and test conventions.

## Context

Complete windows select descendants and render bodies. Add independently callable compact root reads without changing complete consumers or source revisions. Catch-up/live delivery belongs to #3569; relay/daemon installation to #3562/#3563. No decision record needed. No other fetched feature branch touches the proposed existing files.

Sizing, checked against the design: one deliverable, approximately 740 written lines including tests and this plan; one exported result type, zero consumer migrations, four acceptance criteria, at most six rejection branches. All five limits remain satisfied before plan commit.

## Design

`CompactNewestWindow(ctx, id, limit)` and `CompactHistoryPage(ctx, id, upperOrder, limit)` return `CompactQueryResult`: state, conversation, projection, epoch/version, full message Items, prepared ChildSummary Headers, lower/upper/continuation bounds, OlderExists and generic Err. Only usable results carry identity/bounds. Projection is `compact-v1`. Headers preserve source revisions and distinguish unknown facts from known empty facts.

Maintain persistent ordered-root and active-root trees. A separate parent-grouped active-branch tree contains public rows whose own subtree has activity. Preparation refreshes these facts bottom-up along affected old/new ancestor paths, using tree ranks rather than enumerating children. Initial construction may visit all public rows. Existing bucket and pointer copies remain separately counted.

Root selection mirrors complete range/tie rules, but counts only nonzero-order roots. Newest adds indexed active roots, including order zero, deduplicated without changing bounds or the root's own Active. Pages have no extras. Tool/agent rows become prepared headers before any rendering; only selected non-tool/agent messages reach `renderItems`. No complete selection or inventory filtering occurs.

## Concurrency model

No new goroutines. Capture scalars and immutable index under Store.mu, select/render outside it. Persistent roots and cloned dirty buckets preserve captured reads through publication, retirement and replacement. Result content and optional facts are detached.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Activation, completion, reparenting, withdrawal, repeated updates | `TestCompactIncremental`: compare with independent final Fold facts and retain old capture. |
| New publication and caller mutation during old reads | `TestCompactCapture`: concurrent complete reads and detached compact output. |
| Unload/replacement while selected content is being read; old source failure | `TestCompactCapture`: old identity cannot retire replacement. |
| Cancellation before/during selection or rendering | `TestCompactLifecycle`: discard full result and preserve healthy worker. |

## Error handling

Already-cancelled context precedes canonical ID and positive-limit checks; limits clamp to MaxQueryItems. Lifecycle states return no usable identity or prefix. Missing usable index is unavailable. Required message hydration failure returns only StateUnavailable/ErrUnavailable through existing guarded withdrawal. Cancellation returns a zero result/context error, never withdrawal.

## Testing strategy

Write failing compact tests before production code. Independent root oracle checks exact messages and prepared header facts, ties, empty bounds, hidden-heavy exhaustive pages and nested active extras. Incremental tests cover affected ancestor paths and retained captures. Work tests increase active hidden descendants and tool payload sizes at fixed roots/extras, constrain request visits and exact selected message lookup IDs, and count preparation and copies separately. Preserve complete-query tests. Run native Linux thread race package checks, go vet and binary build; full-module gates belong to dispatcher. No real-Claude proof required.

## Open questions

None.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/thread-package-background-store-queries.md`, alongside “Bounded queries”, describe compact root membership, mixed full messages/prepared headers, ties, active extras, page continuation/coverage, lifecycle behavior and preparation/request work bounds. State that catch-up/live progress and production installation are separate follow-ups; compact pages certify root ranges, not complete-thread or expansion coverage.

## Security review

**Verdict:** PASS

- [Trust boundaries] SHOULD FIX: select tool/agent headers directly from prepared facts; synthetic secret-bearing bodies and growing hidden activity must never reach compact hydration.
- [Tokens, secrets, credentials] No credential handling; untrusted summaries remain inert bounded text and no payload is logged.
- [File operations] Reuse canonical-ID validation and existing history lookup containment; no new paths or writes.
- [Subprocesses] No production child processes.
- [Cryptography] No cryptographic changes.
- [Network and I/O] Selected messages alone hydrate through existing context-aware lookup. Authenticated admission and wire budgets are OUT OF SCOPE for #3562/#3563.
- [Errors, logs, telemetry] SHOULD FIX: required message failure must discard already prepared headers, exposing only ErrUnavailable.
- [Concurrency] Immutable captures, detached pointers and existing worker identity/cancellation checks prevent old-reader retirement of replacements; no new goroutines or lock ordering.
- [Threat model] Caller authorizes the conversation; compact coverage is explicitly projection scoped and cannot certify expansion or complete inventory.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-17

## Revisions

- 2026-10-17: `TestCompactCapture` additionally covers rendering cancellation and current-worker read failure, alongside replacement/unrelated-reader isolation. `TestCompactPublicMembership` exercises actual delivery suppression and late resolution; `TestCompactPreparationWork` separates semantic maintenance from copied storage. No API or selection contract changed.
- Final sizing remains below 720 written lines, with one new exported type, zero consumer migrations, four acceptance criteria and six validation/lifecycle/render failure cases. Native verification scope is Linux.
