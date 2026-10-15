# Agent and shell history references (#3453)

## Files read
- `internal/thread/agent.go` → `agentWork`, `addReport`, `applyAgent`: validated evidence, companion provenance and intrinsic recomputation.
- `internal/thread/shell.go` → `registerShell`, `applyShellForeground`: promotion and separate terminal order/content IDs.
- `internal/thread/render.go` → `contentRecipe`, `renderSharedLookup`, `recentSource`: shared rendering and bounded source selection.
- `internal/thread/fold.go` → `addItem`, `Feed`: creation and public bookkeeping.
- `internal/thread/main.go` → `outcome`, `foldWork`: legacy compatibility storage and ordinary shell creation.
- `internal/thread/child.go` → `restoreChild`, `resolveChildren`: independent intrinsic/previous recipes and ancestor references.
- `internal/thread/render_endings_test.go`, `render_test.go`, `corpus_golden_test.go`, `corpus_generator_test.go`: existing closure, retry, retention and unchanged golden scaffolding.
- `cmd/pyry/thread_agent_test.go`, `thread_shell_test.go`: recorded replay acceptance checks.
- `docs/knowledge/features/thread-package-agents-and-background-work.md` → Agent lifecycle, Background shells, Parent repair: first terminal, late links and original ancestor semantics.
- `docs/knowledge/features/thread-package-main-thread-folding.md` → Validation and testing: retained-byte boundaries and generic dependency failure.
- `docs/knowledge/features/development-verification.md` → Establish the change surface, Verify inherited premises: inspect merged sibling behavior before sizing.
- `docs/knowledge/decisions/045-thread-index-over-history.md` → Decision: history remains the only production payload store.

## Context
Complete the remaining agent/shell payload conversion after merged #3451, #3452 and #3386. No new decision record is needed. Branch overlap check found no other feature branch touching the four planned production files.
One deliverable: history-backed agent/shell lifecycle rendering with its whole-fold memory proof. Forecast: 740 written lines including tests and plan, zero exports, no public signature changes, five acceptance criteria, no new reject state machine. The comparison harness is consumed only here and remains in this ticket.

## Design
Replace call launch, task link and report payload fields with history IDs. Preserve report validity and turn facts decoded on arrival and separate terminal order/content provenance. Keep a small launch-field-presence fact where recomputation must respect an existing launch result.
Build intrinsic agent recipes from creating ID, entry-backed enrichment fields and computed scalar constants. Compare recipe structure and lifecycle metadata before restoring children or publishing touches. The renderer canonicalizes agent content and removes creating `ended_order` before applying computed fields. Shells remain ordinary tools; only linked shells use agent recomputation.
Legacy `New` uses the existing lazy compatibility map, populated only without a production source, and the same shared renderer for agent reconstruction. It retains its detached public-item behavior. Production never populates compatibility storage.
All fields/maps remain copy-on-write so child base/previous state and captured Store query roots remain independent. Existing current-chunk, inclusive 1 MiB cache and Lookup resolution serves all dependencies.

## Concurrency model
No new goroutines or locks. Fold ownership stays serialized; Store retains its existing worker cancellation, query capture and publication synchronization. Heap measurement runs alone in a helper process, keeping the fold/source live through post-GC sampling.

## State transitions and identity reuse
| Event | Race-enabled coverage |
| --- | --- |
| Early reports, launch, late task links, first terminal and companion arrival orders | `TestReferenceShellCompanion`, `TestAgentCompanionPendingFinals`, `TestReferenceRendering` |
| Shell lifetime promotion and foreground completion | existing shell lifecycle tests, `TestReferenceAgentLifecycle` |
| Nested closure, late append and reopening after eviction; immutable selected views | `TestReferenceInheritedEnding`, `TestStoreCapturedInheritedEnding` |
| Missing/unreadable launch/link/report or inherited dependency; repair and retry | `TestReferenceAgentDependencies`, `TestStoreReferenceAgentRetry` |
| Repeated folds and all existing feed partitions | `TestFoldBaselines`, recorded agent/shell replay tests |

## Error handling
Unresolved or unreadable dependencies withhold the complete render/publication using existing `ErrUnavailable`/`StateUnavailable`. No payload or path enters errors. Repair uses Store `Retry`; folding does not introduce I/O for lifecycle comparison.

## Testing strategy
Write recursive private-state raw-JSON guards and runtime no-Content assertions first; observe failure before conversion. Reuse unchanged `testFoldCorpus(6738)` for lifecycle/parent-ending zero-copy accounting and report recipe/cache cost. Keep existing golden expectations and captures untouched. Extend focused eviction/failure tests and retain every-field observation/touch comparisons.
Measure identical deterministic disk-backed history at `44fe69498699cd69b0cc54543e7dabe0703c28a7` and the candidate: generator parameters, entry count, encoded bytes, pre-fold heap and post-GC retained delta. Release input/chunks and returned rendered items; keep fold/source alive. Require candidate under 10%; exclude Store publications explicitly.
Run `go test -race ./internal/thread ./cmd/pyry ./internal/relay`, `go vet ./...`, `go build ./cmd/pyry`. The dispatcher verifier owns `make check` and the full-module gate under builder role policy.

## Open questions
None. If replay exposes a changed contract, record the decision under Revisions before handoff.

## Documentation handoff
Pending documentation stage:
- `docs/knowledge/features/thread-package-agents-and-background-work.md`, **Agent lifecycle** and **Background shells**: describe ID-backed lifecycle evidence, ingestion-time decision facts and separate terminal-order/content IDs.
- Same path, **Parent repair**: remove the agent/shell payload-copy exception.
- `docs/knowledge/features/thread-package-main-thread-folding.md`, **Validation and testing**: recursive retention guard, unchanged-corpus accounting and measured heap boundary, distinguishing fold/source memory from #3386 Store publications.

## Security review
**Verdict:** PASS
**Findings:**
- [Trust boundaries] Preserve `agentWork`/`addReport` arrival validation; the renderer resolves inert JSON objects only, never executes content.
- [Tokens, errors/logs] SHOULD FIX: test missing/unreadable dependencies with private sentinel errors and require only generic unavailable output. No credential generation or storage changes.
- [File operations] Production reuses existing history Lookup; no new path construction or writes. Heap fixtures use private temporary directories and files.
- [Subprocesses, cryptography, network/I/O] No production subprocess, crypto or network changes; the test helper executes the test binary directly and retains the existing 1 MiB source-cache bound.
- [Concurrency] No new goroutines; recipe maps are immutable once captured, and selected child rendering resolves ancestors from the same view.
- [Threat model] No authentication/authorization changes. Store publication retention is measured separately by #3386; resumable-state work remains #3335.
**Reviewer:** builder (self-review)
**Date:** 2026-10-15
