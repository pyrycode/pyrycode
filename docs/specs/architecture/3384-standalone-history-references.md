# Standalone history references

## Files read
- `internal/thread/fold.go` → `Feed`, `feedEntry`, `addItem`, `Items`, `standalone`, `plainSummary`: ownership, detached snapshots and summary normalization.
- `internal/thread/sends.go` → `sendFact`, `deliveredSendContent`: permanent acceptance identity and delivery suppression.
- `internal/thread/main.go` → `foldWork`: turn-end rows and deferred terminal payloads.
- `internal/thread/store.go` → `run`, `publish`: recovery comparison, complete publications and joined workers.
- `internal/thread/observations.go` → `difference`, `recordShown`: public fields and watermarks must remain identical.
- `internal/history/lookup.go` → `Lookup`, `detachEntry`; `internal/history/log.go` → `Entry`: ordered unique lookup, all-or-nothing failures and metadata ownership.
- `internal/thread/corpus_golden_test.go` → `testFoldGoldenCheck`, `testFoldDigest`; `corpus_generator_test.go` → `testFoldCorpus`: committed byte-level checkpoints.
- `cmd/pyry/thread_corpus_test.go` → `TestThreadRecordedBaselines`: recorded offline emitter evidence.
- `internal/thread/store_test.go` → `testStoreReader`, `testStoreWait`: deterministic recovery and publication failure tests.
- `docs/knowledge/features/thread-package-main-thread-folding.md` § Standalone source entries / Accepted sends and durable outcomes: delivery order, ownership and suppression survive conversion.
- `docs/knowledge/features/thread-package-background-store.md` § Snapshots and retry: failures withdraw all usable state; rendered caches cannot seed private continuation.
- `docs/knowledge/features/history-package-shape.md`, `development-verification.md`, `CODING-STYLE.md`, ADR 045: authoritative history and offline evidence boundaries.

## Context
Production folds currently copy standalone payloads. This first ADR 045 conversion replaces those copies with creating-entry IDs while retaining public Items and the legacy constructor. Assistant/tool recipes and rendered Store snapshot removal remain #3385 and #3386. No new decision record is needed.
Overlaps: #3351 adds fold counters; #3352 adds public-touch tracking in `Feed`, `addItem` and `sendFact`. Keep edits local; neither supplies a required API.
Sizing: one deliverable, four acceptance criteria, approximately 700 total written lines, no exported types, three internal consumer updates, fewer than ten new failure branches. Recount before commit confirms the same bounds.

## Design
- Keep `New`, `Items`, `Observe`, `FeedChanges` source compatible. Legacy folds retain inline payloads. Add `Render() ([]Item, error)` as the single renderer, with an internal reference-fold constructor and entry source interface whose one method looks up entries by IDs.
- Production recipes map item identity to creating entry ID for plain user messages, turn ends, notices, compactions and dividers. User summaries are empty internally and derived through `plainSummary` with the existing fallback during rendering. Other metadata is folded normally; queued/accepted sends remain inline.
- Rendering filters visibility/suppression first, batch-resolves references and returns detached public Items. Untouched payloads are copied exactly, including formatting and unknown fields. Inline kinds use the same rendering path. Any missing source or source error returns no items.
- A claimed delivery resolves its message through the renderer before merging into the inline acceptance item; error aborts the feed. Ownership, sender metadata, summary, ordering and suppression retain their contracts.
- A per-worker source resolves IDs from the current feed chunk, then a recent FIFO, then `history.Store.Lookup`. Normalize remaining requests to ascending unique IDs. The chunk is borrowed only through feed/publication and released afterwards; there is no persistent entry map.
- The FIFO uses fixed backing storage and a 1 MiB inclusive accounting budget: source/entry backing storage plus detached payload capacities, copied strings and pointed metadata. Oversized entries are skipped before copying. Normalize retained timestamps to UTC to avoid retaining arbitrary location graphs. Entries and count are both bounded.
- Store replay/recovery comparisons and publication use `Render`, resolving errors before checkpoint or publication. Existing checkpoint/epoch logic remains intact. Store snapshots/query rows remain rendered in this slice.

## Concurrency model
Fold and source belong exclusively to the existing conversation worker. No new goroutines or locks. Lookup and rendering occur outside Store locks. Existing tail cancellation/acknowledgement and worker joins release the source on failure, unload or shutdown.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Whole-prefix, chunked and single-entry feeds, including late joins and repeated IDs in lookup requests | `TestFoldBaselines`, `TestReferenceSourceOrder` |
| Delivery claims an earlier plain message and suppresses its row | `TestReferenceRendering`, corpus checkpoints at delivery boundaries |
| Cache eviction and oversized-entry bypass | `TestReferenceSourceBound` |
| Missing/unreadable reference withdraws candidate; repair and Retry construct fresh source/fold | `TestStoreReferenceRetry` |
| Unload/reload and clean reopen retain compatible items/epoch evidence | existing `TestStoreContinuationReopen` and cache tests, now production reference mode |

## Error handling
Renderer/source failures return no partial public values. Store leaves the worker unavailable with generic `ErrUnavailable` and clears query/change state. Retry replays authoritative history with a new source. No payloads, paths or underlying source errors reach public errors/logs.

## Testing strategy
Write focused reference/cache/failure tests first and observe failure before implementation. Extend existing generated/shadow golden checks to reference folds with the production source adapter; never regenerate expectations. Recorded daemon baseline checks also compare real Store reference publications with legacy results. Measure retained converted bytes on the same generated input, reporting recent-cache and deferred payload bytes separately. Run the requested race suites, vet/build and `make check` (explicit ticket requirement); the verifier repeats its own full gate.

## Open questions
None. Recent entry count is capped as well as byte size; this trades hit rate for straightforward inclusive backing-storage accounting.

## Documentation handoff
Pending documentation stage:
- `docs/knowledge/features/thread-package-main-thread-folding.md`, **Standalone source entries**: describe converted kinds and deferred rendered user summaries.
- `docs/knowledge/features/thread-package-background-store.md`, **Snapshots and retry**: describe fallible renderer, feed/recent/Lookup source order, 1 MiB recent-cache bound and unavailable/retry behavior. State Store snapshots still retain rendered content until #3386.

## Security review
**Verdict:** PASS
**Findings:**
- [Trust boundaries] `Feed` retains conversation ownership and DTO validation. Source lookup is bound to the worker's caller-established conversation, never a payload conversation assertion.
- [Tokens / cryptography] No credentials or new cryptographic operations. Existing recovery epoch generation stays unchanged.
- [Files] Reuse `history.Store.Lookup` containment/regular-file checks and existing atomic cache writes; no new path construction or permissions.
- [Subprocesses / network] No subprocess or network operations introduced. Offline retained history is inert display data.
- [I/O] SHOULD FIX: check the full resolved ID set before exposing any rendered item; an incomplete source result must fail atomically. Cache budget includes backing storage and oversized entries must be rejected before cloning.
- [Errors / telemetry] Generic unavailable sentinel at Store boundary; never publish source errors, payloads or paths.
- [Concurrency] Exclusive worker ownership for recipes/source; existing joined tail and worker shutdown paths release retained entries.
- [Threat model] OUT OF SCOPE: assistant/tool payload retention and rendered snapshot retention are deferred explicitly to #3385/#3386; transport authentication and framing remain at existing relay boundaries.
**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-14

## Revisions
- 2026-10-14: The six serial generated golden feeds exceeded the race suite's default ten-minute timeout. Execute independent checkpoint/mode subtests in parallel while keeping explicit golden generation serial. Measure retention on a shared 6738-entry generated input to avoid two additional 30,000-entry folds; the already executed 30,000-entry measurement remains recorded in the PR. Production contracts and golden expectations are unchanged.
