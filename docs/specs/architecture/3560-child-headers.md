# Bounded child headers and ancestor freshness

## Files read
- `internal/thread/child_pages.go` → `ChildPage`, `childHeadline`: strict tuple paging, cancellation and metadata projection.
- `internal/thread/query_tree.go` → `membership`, `advance`: persistent memberships and bucket copying.
- `internal/thread/queries.go` → `newPublicationIndex`: initial public index construction.
- `internal/thread/publication.go` → `publicationCapture.capture`: affected rows are already rendered during preparation, including reference-backed outcomes.
- `internal/thread/store.go` → `publish`: atomic index/scalar installation and worker ownership.
- `internal/thread/render.go` → `renderSharedLookup`: captured recipes resolve terminal evidence before publication.
- `internal/protocol/thread_expansion.go` → `ThreadSummary`, `threadHeadline`: semantic bounds only; thread remains independent of protocol.
- `internal/thread/child_pages_test.go`, `query_incremental_test.go`: independent membership oracle, immutable capture barriers and separate preparation/read counters.
- `docs/knowledge/features/thread-package-background-store-queries.md` and `thread-package-background-store-verification.md`: Shown is not membership; synthetic withdrawal must also suppress the independent Fold oracle.

## Context
Child pages need truthful header facts without body hydration. The merged protocol declaration specifies optional error/count/existence/freshness facts and a 200-code-point text bound. Complete items, revisions and certification remain unchanged. No decision record is needed. No other fetched feature branch overlaps the planned production files.

## Design
Capture bounded headline and error evidence from the already rendered affected public rows into publication-owned metadata, never Item fields. Failed/denied outcomes may use status diagnostics; confirmed successful terminal statuses produce known empty error text, other statuses remain unknown. Do not inspect descendant outcomes.

Store prepared scalar header facts on immutable query rows. Initial construction establishes public direct-child counts (including zero) from the child tree; initial freshness uses consumed progress. Tail preparation collects changed public rows and both old/new ancestor chains before replacing their header facts. Refresh counts using tree ranks, not descendant enumeration. Freshness uses consumed progress below 2^53; values outside the representable domain stay unknown. Complete-index counts include hidden/dropped public rows and exclude private/suppressed slots. NoChild remains attribution.

ChildPage copies prepared facts into detached optional values, preserving source Rev and all existing ownership/lifecycle fields. Text normalization collapses whitespace/control separators, repairs invalid UTF-8 and truncates at 200 Unicode code points including ellipsis. Preparation may inspect full source text; reads copy only bounded prepared text.

## Concurrency model
The existing serial fold worker prepares metadata outside Store.mu and installs immutable roots/scalars under Store.mu. Readers borrow that capture outside the mutex. Copy affected ancestor lookup buckets before updates; never mutate shared optional values. No new goroutines or locks.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Multi-level addition, content/Shown change and completion with unchanged ancestor Rev | TestChildHeaderFreshness |
| Reparent and withdrawal invalidate both chains; late public appearance counts only once | TestChildHeaderFreshness, TestChildPageIncrementalOracle |
| Unrelated/no-item publication preserves branch freshness | TestChildHeaderFreshness |
| New publication, unload/replacement and caller mutation preserve old captures | TestChildPageCaptureLifecycle, TestChildHeaderFreshness |

## Error handling
Existing request refusals and cancellation precedence remain unchanged. Header preparation adds no I/O or request rejection. Unknown terminal evidence stays nil; a failure/denial is never represented as known empty. Numeric overflow is represented as unknown rather than wrapping or changing source revisions.

## Testing strategy
Write failing header tests first. Extend the independent direct-child oracle to count each returned row's public children. Cover text normalization/bounds, error unknown/empty/diagnostic, reference-backed Store outcomes, unchanged captures and ancestor freshness. Retain existing pagination/refusal/cancellation tests. Count preparation separately from requests and retain bounded-read growth tests with hidden descendants and large text/bodies. Run admitted race tests for internal/thread, go vet ./... and go build ./cmd/pyry on macOS; the verifier owns the full-module gate.

## Open questions
None.

## Documentation handoff
Pending documentation stage: update `docs/knowledge/features/thread-package-background-store-queries.md`, “Revision-scoped direct-child summaries”, with the new text bound, error presence, source revision versus subtree freshness, public visibility/count semantics and preparation/request work bounds. Describe the new header facts as Store facts pending relay translation; do not claim compact delivery is enabled.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: ChildPage retains canonical conversation validation and public parent lookup; authorization remains its caller's responsibility. Header metadata derives only from public captured rows.
- Tokens/credentials: no credential generation/storage or logging is added; source display text remains inert data.
- File operations/subprocesses/cryptography: no new paths, processes, persistence or cryptographic operations.
- Network/I/O: no new I/O; request limits remain hard ceilings and reads never scan source bodies or descendants.
- Errors/logs: SHOULD FIX: distinguish unknown outcomes from known empty success and use only own failure status, preventing descendant error attribution. Covered by focused header tests.
- Concurrency: SHOULD FIX: ancestor buckets need the same copy-on-write discipline as directly affected rows; retained capture and caller-mutation tests cover this.
- Threat model: relay translation/authenticated compact admission and delivery are outside this Store slice, owned by #3562/#3563.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-17

Sizing: one deliverable, approximately 720 written lines (280 production, 370 tests/edits, 70 plan), zero new exported types/interfaces, eight internal publication callers updated, four acceptance criteria, zero new reject branches. Rechecked before plan commit.

## Revisions
- 2026-10-17: Publication integration passes the prepared capture through eight `publish` callers and two `advance` callers (10 existing consumers total). Header diagnostics use the explicitly allowed own-status fallback, avoiding content parsing and descendant attribution. Pointer-copy counters include the two additional header string pointers. Final work remains below 500 added/edited lines, with no exported type or rejection-class additions.
