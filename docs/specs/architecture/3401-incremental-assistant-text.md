# Incremental assistant text

## Files read
- `internal/thread/main.go`: `foldWork`, `closeText`, `setContent`; scoped coalescing and terminal enrichment.
- `internal/thread/fold.go`: `Feed`, `addItem`, `Items`, `plainSummary`; input validation and detached snapshots.
- `internal/thread/child.go`: `restoreChild`, `resolveChildren`; intrinsic versus provisional state and revision comparisons.
- `internal/thread/observations.go`: `recordShown`; append watermark and public-touch bookkeeping.
- `internal/thread/agent.go`: `applyAgent`; ancestor closure and reopening.
- `internal/thread/corpus_generator_test.go`, `corpus_golden_test.go`, `main_closure_test.go`: unchanged corpus, byte digests and unrelated-opening regression.
- `docs/knowledge/features/thread-package.md`: retained full-feed benchmark and corpus qualifications.
- `docs/knowledge/features/thread-package-main-thread-folding.md`, `thread-package-agents-and-background-work.md`: original recovery references and intrinsic child restoration must survive provisional closure.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: ownership and verification boundaries.

## Context
Appending currently decodes, concatenates, serializes and summarizes the entire accumulated assistant text. Child resolution additionally serializes provisional parent endings and compares complete content. This single deliverable removes accumulated-text work from ingestion, retaining inline content and existing output. Publication and reference recipes remain separate. No decision record is needed.

## Design
Use private per-item text state with owned, geometrically growing JSON buffers and a normalized-summary builder. Preserve the initial payload verbatim until an actual enrichment or nonempty append. At canonicalization retain all creating fields and encoding/json key order and escaping; splice only newly encoded text before the fixed JSON suffix. Summary normalization tracks pending whitespace and processes decoded payloads independently, preserving JSON decoder UTF-8 replacement semantics.

Intrinsic and parent-derived content have separate owned buffers. Cache provisional enrichment metadata and append only new encoded text when the parent ending is unchanged; rebuild on metadata changes. Never mutate input or returned snapshots. A nonempty append proves child content changed, avoiding accumulated-byte equality; other child comparisons retain exact semantics. Non-text `setContent` callers retain their contract.

Reuse agent/public/child/closure counters and add ingestion byte, comparison and allocation/growth/copy accounting. Include initialization and fixed-field suffix copying. Existing counters cover candidate discovery and ordering; remaining payload and content work is counted at the owning operations. Explicit snapshots, corpus generation and digests are excluded.

Only #3384 currently overlaps (`fold.go`); edits are local and require no unmerged API. Size sketch and plan recount: approximately 760 written lines, zero exported types, four existing setContent consumers, four acceptance criteria, fewer than ten new branches. The estimate is tight; keep tests focused and reuse existing corpus gates.

## Concurrency model
Fold access remains serialized by its owner. No goroutines or locks are added. Builder strings are immutable after publication; snapshot Content is copied by `Items`.

## State transitions and identity reuse
| Event | Race-enabled coverage |
| --- | --- |
| Repeated main/child append, including empty/control/Unicode suffix | `TestTextAppendBytes`, `TestTextAppendScaling` |
| Provisional parent closure, append while closed, reopening and later closure | `TestTextAppendChildren`, `TestTextAppendScaling` |
| Intrinsic ending, inactive late text and reused turn identity | existing `TestMainTextClosureTracking`, `TestMainScopeAndRecovery`, corpus goldens |
| Snapshot/input mutation and buffer growth | `TestTextAppendBytes`, `TestTextAppendChildren` |

## Error handling
Existing validation and invalid-entry errors remain unchanged. Stored JSON and strings are validated before the append helper runs. Unknown fields remain inert. No new external errors or dependencies.

## Testing strategy
Write focused byte/summary/ownership regressions and scaling assertions before implementation and observe failure. Compare content with the former map-based append operation, including escaping, case variants and initial formatting. Run unchanged `TestFoldBaselines` and daemon recorded baselines, race tests for thread and cmd/pyry, vet and build. The dispatcher owns `make check` and the full-module race gate. Run the requested single-iteration 30,000-entry benchmark and record the machine, Go version, commits and retained baseline qualification in the PR.

## Open questions
None; byte equivalence and deterministic scaling are the acceptance gates.

## Documentation handoff
Pending documentation stage: update `docs/knowledge/features/thread-package-main-thread-folding.md`, **Main-work source entries** and **Scoped joins, pending outcomes and closure**, to state that text and normalized summaries are maintained incrementally with byte-identical output, including provisional child closure/reopening. Retain scoped coalescing and original recovery-reference rules.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `decode` and `mainDecode` continue validating history before mutation; retained unknown fields are inert JSON.
- Tokens, errors/logs/telemetry: no credentials, logging or new error payloads; assistant content stays in existing owned memory.
- File operations, subprocesses, cryptography, network/I/O: none added; this is an in-memory fold change with no new external boundary.
- Concurrency: serialized Fold ownership remains required. SHOULD FIX: keep intrinsic and derived buffers separate and snapshots copied; growth/reopening tests must detect alias corruption.
- Threat model: no protocol or authorization change; existing history ingestion limits remain with their producer. Accumulated-text CPU amplification is reduced by the append and comparison design.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-14

## Revisions
- 2026-10-14: Count recursive validation and JSON operations at their owning helpers instead of charging an estimated number of payload passes. Optional private decoder counters preserve existing helper callers. Replace child dirty membership after resolution: both clearing and later iteration could scan capacity retained from an earlier large closure. Inline intrinsic and derived buffers remain separate; a nonempty append proves inequality before child comparison, even if an internal previous slice shares reused storage. Public snapshots remain detached.
