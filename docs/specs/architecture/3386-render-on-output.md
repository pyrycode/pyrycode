# Reference-backed Store output

## Files read

- `internal/thread/publication.go` → `publicationCapture.capture`: affected comparisons and stable slots.
- `internal/thread/render.go` → `renderShared`, `contentRecipe`, `recentSource`: shared rendering and source precedence.
- `internal/thread/store.go` → `run`, `publish`, `Snapshot`, `start`: progress, worker identity and retirement.
- `internal/thread/queries.go` → `captureQuery`, `window`: persistent selection and public closure.
- `internal/thread/query_tree.go` → `queryRow`, `advance`: immutable row and bucket updates.
- `internal/thread/catch_up.go` → `prepareCatchUp`, `CatchUp`: coverage and reset contracts.
- `internal/thread/observations_store.go` → `retain`, `Changes`: bounded detached change output.
- `internal/thread/observations.go` → `itemDifference`: suffix and simultaneous replacement semantics.
- `internal/thread/render_test.go`, `publication_test.go`, `queries_test.go`, `catch_up_test.go`: reusable corpus, source and lifecycle proofs.
- `docs/knowledge/features/thread-package-background-store.md` → Snapshots and retry, Bounded queries: preserve immutable roots, nonloading selection and atomic failure.
- `docs/knowledge/features/thread-package.md` → Cache and epochs: checkpoints remain content-free.
- `docs/knowledge/decisions/045-thread-index-over-history.md`: rendering from history with bounded caches; no new decision record needed.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: behavioral tests, captured-source and evidence boundaries.

## Context

Converted fold rows hold references, but publication slots and query roots still retain rendered histories.
Remove this copy without changing public item shapes or bytes. Deferred inline kinds belong to #3334.
Overlap: #3383 changes checkpoint lifecycle and #3389 adds metadata status in `store.go`; keep edits local and preserve both contracts.
Sizing: about 740 written lines (250 production, 410 tests/helpers, 80 plan), zero new exports, at most eight simultaneous internal call-site edits, five criteria, four failure outcomes. Recount before commit: within all five limits.

## Design

Capture immutable recipe values beside affected metadata and on persistent query rows. Recipe maps already use copy-on-write; text ID prefixes are immutable.
Render affected before/after states transiently with their respective recipes using the worker source, then retain only metadata/recipes in comparison slots and query rows.
Keep existing `itemDifference` semantics and bounded rendered change ring. Worker checkpoint progress stays metadata-only.
Factor the shared renderer so captured recipes and a request-local lookup source can use it without reading the live fold or recent cache.
Select query rows, active extras and complete parents before collecting their recipes and reading history. Baseline renders all public rows.
Capture the worker identity with the query root. History reads happen outside `Store.mu`; output content is detached.
On render failure, atomically withdraw only the captured current worker, clear publication/query/ring state, notify waiters and cancel its producer. Retry reconstructs from history.

## Concurrency model

Existing worker and tail goroutines retain their cancellation/join paths. No new goroutines.
Only the worker uses its mutable recent source and capture map. Requests use immutable query roots and request-local rendering sources.
`Store.mu` guards worker identity and withdrawal, never history reads. Request cancellation takes precedence over render failure and cannot withdraw a worker.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Later feed while old query renders, including repeated assistant appends | `TestStoreCapturedRecipes` |
| Missing/unreadable baseline or query reference, repair and Retry | `TestStoreOutputFailure` and existing `TestStoreReferenceWorkRetry` |
| Old captured request fails after Retry replacement | `TestStoreOutputFailure` |
| Request cancellation during lookup | `TestStoreOutputCancellation` |
| Live delta/replacement and bounded retained output | existing affected-publication and Store observation tests, `TestStoreReferencePublication` |
| Retirement/shutdown while a captured reader completes | `TestStoreOutputCancellation` plus existing Store lifecycle tests |

## Error handling

Missing references and lookup errors discard the whole result and return generic unavailable State/Err, with no source error or path.
Context-taking requests return their context error and discard output. Reset/nonusable queries do no rendering.
Old worker failure cannot affect its replacement. Existing nonloading and caller-authorization contracts remain unchanged.

## Testing strategy

Write focused regression tests first and observe failure. Reuse source-priority, generated, recorded and shadow goldens without changing captures.
Assert selected-only lookup, detached outputs, immutable recipes across feeds, failure/retry, cancellation and zero converted content/summary retention after GC on `testFoldCorpus(6738)`; report recipes, deferred payloads, caches and ring separately.
Run `go test -race ./internal/thread ./internal/history ./cmd/pyry ./internal/relay`, `go vet ./...` and `go build ./cmd/pyry`.
The dispatcher owns `make check` (full-module verifier gate); no live-Claude evidence is required.

## Open questions

None.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/thread-package-background-store.md`, Loading and replay readiness, Snapshots and retry, Bounded queries: baseline/query rendering can read history after metadata selection, uses one immutable publication and fails atomically through unavailable/retry. Replace obsolete no-history-I/O content-read claims while retaining nonloading, cancellation, continuity and authorization contracts.
- Pending documentation stage: `docs/knowledge/features/thread-package.md`, Cache and epochs: distinguish reference-only converted publications from deferred inline kinds and bounded caches.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: rendering accepts immutable entry IDs from the fold; public queries retain canonical ID checks and existing caller authorization.
- Tokens/credentials: none introduced or handled by the changed path.
- File operations: reuse `history.Store.Lookup` containment and leaf validation; no new file paths or writes.
- Subprocesses/cryptography: neither is introduced; epoch generation is unchanged.
- Network/I/O: selection precedes reads; preserve query limits, closure and existing byte-bounded caches/ring. No network surface added.
- Errors/logs: SHOULD FIX: every render boundary must replace source errors with `ErrUnavailable`, including successful partial lookups followed by missing references; test this explicitly.
- Concurrency: SHOULD FIX: withdrawal must compare captured worker identity under `Store.mu`; a failed old request must never cancel a Retry replacement. Never expose the live recipe map/recent source to requests.
- Threat model: public content membership and detachment are preserved. Deferred inline/private conversion remains OUT OF SCOPE in #3334.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15

## Revisions

- 2026-10-15: `TestStoreOldRenderFailure` covers the old-request/Retry identity scenario separately from `TestStoreOutputFailure`. Bounded-query tests now constrain selected reference IDs instead of allocations or zero history reads: decoding a selected segment allocates independently of metadata selection work.
- 2026-10-15: Request-triggered withdrawal can expose unavailable state while the cancelled producer is still exiting. `Retry` joins that producer outside `Store.mu` before replacement, preserving shutdown ownership and preventing overlapping recovery writes.
- 2026-10-15: Remember request render failure on the worker so an overlapping Unload cannot reinterpret its cancellation as clean retirement; `TestStoreRenderRetryJoinsProducer` covers both join and failed certification.
- 2026-10-15: Read `cmd/pyry/daemon_thread_queries_test.go` → `TestDaemonThreadQueriesBounded`: its allocation ceiling also assumed no history reads. Preserve its installed bounded-API/source and wire proofs, and constrain selected row count instead; focused thread tests enforce exact reference reads.
