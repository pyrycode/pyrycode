# Assistant and ordinary tool history references

## Files read
- `internal/thread/main.go` → `foldWork`, `applyTerminal`, `setContent`: append and first-terminal semantics.
- `internal/thread/fold.go` → `addItem`, `Feed`, `Items`: private storage and public snapshots.
- `internal/thread/render.go` → `render`, `recentSource`: single renderer, bounded source and all-or-error reads.
- `internal/thread/child.go` → `saveChild`, `restoreChild`, `resolveChildren`: intrinsic/derived ownership and revisions.
- `internal/thread/observations.go` → `difference`, `recordShown`: suffix delivery, touches and visibility.
- `internal/thread/shell.go` → `registerShell`: lifecycle selection, including deferred shell payloads.
- `internal/thread/render_test.go`, `corpus_golden_test.go` → reference failures, byte comparison and unchanged goldens.
- `cmd/pyry/corpus_golden_test.go`, `thread_corpus_test.go` → production Store proof at every recorded version.
- `docs/knowledge/features/thread-package-main-thread-folding.md` → Main-work source entries; scoped closure and first-terminal rules.
- `docs/knowledge/features/thread-package-agents-and-background-work.md` → Parent repair; intrinsic restoration must undo only provisional closure.
- `docs/knowledge/features/development-verification.md` → inherited premises and source scope.
- `docs/knowledge/decisions/045-thread-index-over-history.md` → history remains the content owner.

## Context
Production Store folds already reference standalone creating entries. Assistant text
and ordinary tool content still duplicate history. Extend the existing renderer,
leaving public APIs and wire shape unchanged. No new decision record is needed.
#3401 overlaps main/fold/child append code but supplies no prerequisite API;
reference append will bypass accumulated inline buffers when that work integrates.

## Design
Replace private creating-ID recipes with recipes holding a creating ID, ordered
text IDs and named enrichment references or computed JSON. Assistant summaries
are derived with `plainSummary` and the existing empty fallback at rendering.
Only nonempty subsequent deltas add text IDs and normalize content; creating
JSON remains byte-identical until enrichment. Decode each text entry separately,
then concatenate and marshal the enriched field map with existing JSON semantics.
Select ordinary tools after the existing launcher/shell lifecycle classification:
Claude/legacy shells stay inline, Codex tools remain eligible regardless of name.
Reference enrichment uses terminal IDs; fallback callers of `setContent` remain
compatible. Inline parent endings and private outcome raw bytes remain deferred.
Recipes use value snapshots and copy-on-write field maps, so child base/previous
recipes remain independent. Child comparisons include recipe state without
reading history during ingestion. All public reads use the shared renderer.
No exported types/signatures change; no unbounded content source is introduced.

## Concurrency model
No new goroutines or locks. Fold access stays serialized by its owner; Store
workers keep the existing cancellation and joined tail lifecycle.

## State transitions and identity reuse
| Event | Race-enabled coverage |
| --- | --- |
| Empty/nonempty append, closure, late text, first terminal and repeated reports | `TestReferenceWorkRendering`, `TestFoldBaselines` |
| Terminal before creation and source eviction | `TestReferenceWorkRendering` |
| Provisional child ending, late append, reopening and intrinsic ending | `TestReferenceWorkRendering`, `TestFoldBaselines` |
| Scoped call/turn reuse and child attribution | `TestFoldBaselines`, `TestThreadRecordedBaselines` |
| Missing/unreadable text or enrichment source, then repair/retry | `TestStoreReferenceWorkRetry` |

## Error handling
Resolve every creating/text/enrichment reference before exposing any items.
Source failures withhold the whole candidate; Store reports generic unavailable
state and Retry rebuilds after repair. Observe must not claim usable failed renders.

## Testing strategy
Write focused compatibility/retention tests first and observe failed retention.
Reuse unchanged generated/shadow and recorded golden gates in reference mode.
Extend the 6738-entry byte comparison to child copies, recipes, bounded cache,
assistant summaries and separately deferred private/inline payloads.
Run scoped race tests for thread/cmd/relay, `go vet ./...`, `go build ./cmd/pyry`.
The full `make check` requested by the issue belongs to the verifier gate.
Sizing: one deliverable, four ACs, zero exports/caller signature changes, fewer
than ten failure branches; projected 740 written lines including this plan.

## Open questions
None. Legacy New folds retain inline compatibility; Store snapshot retention is #3386.

## Documentation handoff
Pending documentation stage: update `docs/knowledge/features/thread-package-main-thread-folding.md`, **Main-work source entries** and **Scoped joins, pending outcomes and closure**, to describe reference-backed text/ordinary tools, render-time summaries and exact-byte enrichment, retaining scope/terminal/reopening rules.
Pending documentation stage: update `docs/knowledge/features/thread-package-agents-and-background-work.md`, **Parent repair**, distinguishing reference-backed ordinary child items from deferred inline parent-ending and shell/agent conversions.

## Security review
**Verdict:** PASS
- Trust boundaries: `mainDecode`/`decode` still validate inert recorded JSON; rendering never executes unknown fields. Missing references must fail atomically.
- Tokens/errors: no new credentials or telemetry; Store maps source failures to `ErrUnavailable`, excluding payloads/paths from public errors.
- Files: no new file operations; history source owns contained stored-entry reads.
- Subprocesses/cryptography/network: no changes to these boundaries; existing history and relay limits remain in force.
- Concurrency: immutable field snapshots protect child restoration; owner serialization remains mandatory.
- Threat model: OUT OF SCOPE retained private outcome and inline parent-ending payloads belong to #3334; Store rendered snapshot retention belongs to #3386.
**Reviewer:** builder self-review. **Date:** 2026-10-15.
