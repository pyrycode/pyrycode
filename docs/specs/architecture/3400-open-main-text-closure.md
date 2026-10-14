# Open main text closure (#3400)

## Files read

- `internal/thread/fold.go` → `Fold`, `feedEntry`: delivered messages currently discover closure through retained openings.
- `internal/thread/main.go` → `mainTurn`, `mainWork`, `foldWork`, `closeText`: generation identity, shared child/main folding and scoped closure.
- `internal/thread/child.go` → `childWork`: child turns share `foldWork` but must never enter conversation-wide tracking.
- `internal/thread/observations.go` → `markAffected`, `recordShown`: preserve mutation hooks and count storage copying, ordering and cleanup.
- `internal/thread/main_test.go` → `TestMainTextRuns`, `TestMainScopeAndRecovery`: existing semantic assertions and replay helper.
- `internal/thread/public_touches_test.go` → `TestPublicTouchHistoricalPeakWork`: map iteration can scan retained historical capacity despite yielding few keys.
- `internal/thread/corpus_golden_test.go` → `TestFoldBaselines`: unchanged digests cover whole, chunked and single-entry feeds.
- `docs/knowledge/features/thread-package.md` and `thread-package-main-thread-folding.md` § Scoped joins, pending outcomes and closure: recovery references retain original generations; inactive late text can coalesce.
- `CODING-STYLE.md`: comments state current contracts; tests exercise behavior with the race detector.

## Context

Delivered messages scan all retained openings even when most have no coalescing text. Retaining those openings is necessary for recovery; scanning them for closure is not. This ticket changes closure discovery only. Incremental appends and aggregate performance evidence belong to #3401. No decision record is needed.

Sizing: one deliverable, three acceptance criteria, approximately 350 written lines, no exported types, three local integration points and fewer than ten branches. Recount against this plan remains within all builder limits.

Overlap: origin/feature/3384 modifies `Fold`, `feedEntry` and rendering; its renderer/touch contracts remain intact. No dependency on its implementation. #3385 is ordered later and has no active overlapping branch.

## Design

Retain a dense `Fold.openTexts` slice of main-turn pointers and a one-based slot in each tracked `mainTurn`. Register only when `foldWork` creates a new main text row, including inactive late text. Child text never registers. Existing appends do not register again.

`closeText` removes a tracked turn by swapping in the final slice entry, repairing that turn's slot, clearing the released pointer and truncating the slice. Existing status, revision, child restoration and public touch behavior remains unchanged. Tool launches and turn endings keep calling the same scoped helper. Delivered messages repeatedly close only slice members until empty. Traversal uses current length, never capacity or a sparse map. No ordering is needed before closure; `recordShown` retains its existing affected-item ordering.

Retain all `openings` entries and scoped `turns` independently. Replaced generations and previous legacy scopes remain tracked while they have coalescing text. Inactive rows leave tracking without changing their settled revisions.

A package-private optional work counter counts closure discovery, slot inspections including skips, registration/removal, moved-slot updates, pointer cleanup and slice growth/copying. Tests additionally measure existing public work to include affected ordering/cleanup. Setup and snapshots are excluded; fixed-run ingestion and repeated cycles are included.

## Concurrency model

`Fold` remains owned by one serialized caller. No goroutines, locks, asynchronous work or shutdown paths are added.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| New text, append, scoped tool closure, subsequent text | `TestMainTextRuns`, `TestMainTextClosureTracking` |
| Reused turn ID, retained older generation, legacy scope change, original recovery | `TestMainTextClosureTracking`, `TestMainScopeAndRecovery` |
| Ending followed by inactive late text, user closure and fresh inactive row | `TestMainTextClosureTracking` |
| Child traffic, queued sends and tool reports do not split main text | `TestMainTextRuns`, `TestMainTextClosureTracking` |
| Repeated registration/closure, empty closure and historical peak reuse | `TestMainTextClosureWork` |

## Error handling

No new errors. Existing decoding, source matching and reference validation remain the admission boundary. Tracking is private and only holds validated main turns with a text row.

## Testing strategy

Write the regression first, instrument the old scan and observe deterministic work growth fail. Compare at least 4x retained closed openings and 4x historical open-run peaks with fixed measured registrations, appends, scoped removals, user payloads and repeated/empty closures. Check no tracking leaks and independent item, revision and watermark assertions. Small fixtures use whole, split and single-entry replay. Preserve all corpus and recorded goldens unchanged.

Run `go test -race ./internal/thread ./cmd/pyry`, `go vet ./...` and `go build ./cmd/pyry` (output outside the worktree). The verifier owns `make check` and full-module race coverage; hand off the issue's requested full gate explicitly.

## Open questions

None. Open means eligible for coalescing (`text >= 0`), including inactive late text.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/thread-package-main-thread-folding.md`, **Scoped joins, pending outcomes and closure**, to state that delivered messages discover only open main text runs, including inactive late-text runs still eligible for coalescing, while closed openings remain available to original recovery references and scoped coalescing rules remain unchanged.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `mainDecode`, `mainWork` source/reference checks and `standalone` continue validating history before tracking. Tracking pointers never admit child lanes; source/legacy isolation is unchanged.
- [Tokens, cryptography] No credentials, randomness, secret storage or cryptographic operations enter this in-memory index.
- [File operations, subprocesses, network and I/O] Production changes perform no I/O or process creation; existing history ingestion remains the boundary.
- [Errors, logs, telemetry] No new errors or logs expose content. Optional counters contain only work counts and are enabled by package tests.
- [Concurrency] Serialized `Fold` ownership covers slice and slot updates atomically with ingestion. No new goroutines or locks.
- [Threat model] Dense traversal prevents retained closed history or a former open-run peak from amplifying each new message's closure work. Memory for original recovery references remains intentionally retained by the existing history-fold contract.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14
