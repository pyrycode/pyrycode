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
- Same path, **Standalone source entries**, **Main work source entries** and **Scoped joins, pending outcomes and closure**: remove stale agent/shell inline-fallback descriptions.
- `docs/knowledge/features/thread-package-background-store.md`, **Snapshots and retry**: remove the deferred-inline-kind payload-retention statement in snapshot/publication rendering, keeping Store publications separate from fold/source heap accounting.

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

## Revisions
- 2026-10-15 rework, verifier finding 1: equal durable/mapped call reports, task reports and task links changed reference IDs without changing rendered bytes. Recipe entry fields now carry ingestion-time content digests; `equalRecipe` compares those facts independently of source identity in `applyAgent` and `resolveChildren`. Provenance-only replacements still update copy-on-write recipes and publication/query roots, including child intrinsic/previous state, while preserving revisions, public touches and shown bookkeeping. `TestReferenceEqualCompanion` covers both arrival orders, Agent/Bash, main/nested calls, descendant closure/reopening, confirmed eviction, live changes, captured Store query roots and wire bytes (24 cases). The regression failed before repair for all three evidence kinds. Security follow-up: reuse the existing unkeyed SHA-256 fingerprint over transient compacted/escaped JSON, including links; no new I/O, goroutines, credential handling or payload retention. Verdict remains PASS.
- 2026-10-15: `TestReferenceRepeatedAgentReport` showed that comparing new report IDs alone advances revisions for identical status-empty enrichment. `addReport` now fingerprints compacted, HTML-escaped inert JSON on arrival and reuses an earlier equal-content reference within the same report kind/status/provenance. Reports still retain their own terminal-order IDs and arrival validity facts; mapped companions keep mapped content provenance. No full agent content is encoded for production lifecycle comparison.
- 2026-10-15: The recursive guard starts at `Fold`. Its explicit boundaries are public `Item`, recipes (runtime scalar-only checks), the bounded source, and legacy `texts`/`legacyOutcomes`/child ending storage. Runtime checks require all those legacy containers and all item/child Content slots to be empty in production.
- 2026-10-15: The broad race run exceeded its default 10-minute timeout after spending several minutes in instrumented heap measurement. Heap measurement is now explicitly selected with `-measure-thread-heap`, preserving the same separate-process workload while keeping the normal race gate focused on lifecycle/integration tests. The standalone non-race measurements below passed; instrumented allocation timing is not the heap acceptance boundary.
- 2026-10-15 security follow-up: the report-equality fingerprint uses standard SHA-256 over transient compacted/escaped JSON. It introduces no key, nonce, credential, authentication or authorization use; the security verdict remains PASS. This qualifies the original review's statement that there were no production cryptography changes.

## Offline retention evidence
`TestReferenceDiskHeap` runs in its own test-binary process. Both revisions used the identical test file and `testFoldCorpus(10000)` with an 8192-byte unknown JSON string added to every entry, 100-entry feed chunks and a JSONL disk source with retained ID offsets. Actual encoded history: **85,441,272 bytes**. The generator's entry array, input/chunk buffers and a complete rendered snapshot are released before GC; the fold, source, recent cache and disk-offset index remain live. Store publications are excluded, not claimed solved by this ratio.

| Revision | Pre-fold HeapAlloc | Post-GC HeapAlloc | Retained delta | Fraction of encoded log |
| --- | ---: | ---: | ---: | ---: |
| Pre-family `44fe69498699cd69b0cc54543e7dabe0703c28a7` | 271,560 | 54,760,816 | 54,489,256 | 63.7739% |
| Candidate implementation `e9c0ece7ef5b95f5deffc280c0eaeab0a8564617` | 267,824 | 5,067,408 | 4,799,584 | 5.6174% |
| Final code/test selection `803eebddd4f1ea345c68ee13422355970a3f6677` | 281,296 | 4,943,464 | 4,662,168 | 5.4566% |

Reproduce candidate: `go test ./internal/thread -run '^TestReferenceDiskHeap$' -count=1 -v -args -measure-thread-heap`. For baseline, export that revision to scratch, copy only `render_heap_test.go` from the candidate, and run the same command with `PYRY_THREAD_HEAP_BASELINE=1` (disables only the candidate threshold). An exploratory 4096-byte-padding candidate measured 10.6546%; this is a workload-specific boundary, not a universal ratio for arbitrarily small records.

Unchanged `testFoldCorpus(6738)` accounting: converted item Content 748,225 → 0 bytes; child intrinsic/previous Content 452,847 → 0; lifecycle compatibility payload 291,215 → 0; copied parent-ending payload 0; assistant summaries including child copies 68,907 → 0; user summaries 2,448 → 0. Logical recipes: 292,400 bytes across 1,155 items including child recipe slots; call/task/report facts including fingerprints: 106,624 bytes. Recent cache: 90,482 bytes. Logical accounting excludes container overhead and shared-storage deduplication; the heap comparison includes retained allocations.

Separately, existing `TestStoreReferencePublication` passed on the unchanged 6,738-entry corpus: publication/query converted Content 708,853 → 0 bytes, selected summaries 61,155 → 0, logical public recipes 184,620 bytes (shared values, container overhead excluded), recent cache 90,482 bytes, zero deferred inline/private/parent-ending payload and an empty initial-publication change ring. This is #3386's separate structural accounting, not a Store heap ratio or part of the fold/source heap claim.

Final accounting refinement: the initial logical recipe totals above counted field structs but omitted their small scalar buffers. Including those bytes gives **299,904** logical fold/child recipe bytes and **190,356** logical public recipe bytes. Both focused accounting tests passed after this correction. The measured post-GC heap results already included scalar allocations and are unchanged.

## Final validation
- `go test -race -timeout=15m ./internal/thread ./cmd/pyry ./internal/relay`: thread passed (725.917s), relay passed (183.328s); daemon reported two timing failures in `TestDaemonThreadMetadataAllocations` (replay readiness deadline) and `TestDaemonThreadQueriesRecovery` (missing switch frame).
- Without code changes, `go test -race ./cmd/pyry -run '^TestDaemonThread(MetadataAllocations|QueriesRecovery)$' -count=1 -v` passed: 2 top-level tests executed, 2 passed, 0 skipped (6.05s and 2.44s). An earlier broad daemon run also passed. Timing sensitivity is tracked for investigation in #3466, filed in Backlog at priority low; the broad daemon run is not claimed green.
- `go vet ./...`, daemon build, explicit standalone heap proof, and focused retention/publication accounting passed. Recorded/golden expectations and capture inputs were not modified. `make check` remains pending for the dispatcher verifier under builder role policy.

## Rework validation
- Repair revision `476c7664d5b9ababd54135b0bb48b3de765dacba`: `go test -race ./internal/thread -run '^TestReferenceEqualCompanion$' -count=1 -v` passed: 1 top-level test and 24 scenarios executed/passed, 0 skipped. The final cases include links arriving while nested work already inherits ancestor closure. The focused race selection of `TestAgentCompanionPendingFinals`, `TestReferenceRepeatedAgentReport`, `TestReferenceEqualCompanion`, `TestReferenceRetainedBytes` and `TestStoreReferencePublication` also passed: 5 top-level tests executed/passed, 0 skipped.
- `go test -race -timeout=20m ./internal/thread ./cmd/pyry ./internal/relay` passed all three packages: thread 866.164s, daemon 594.155s, relay 93.764s. `go vet ./...` and `go build -o /tmp/builder-3453-pyry ./cmd/pyry` passed. The dispatcher verifier owns the pending complete `make check` gate.
- Unchanged `testFoldCorpus(6738)` still retains zero item/child Content, lifecycle compatibility payload, private outcome and parent-ending payload bytes. Including fingerprints, logical fold/child recipes are 367,360 bytes and lifecycle facts 115,328 bytes; recent cache remains 90,482 bytes. Separate Store publication accounting retains zero converted Content/summary/private payload, with 242,580 logical public recipe bytes.
- The isolated `TestReferenceDiskHeap` passed on `476c7664d5b9ababd54135b0bb48b3de765dacba` using the unchanged generator, parameters and **85,441,272 encoded log bytes** above. Pre-fold heap: **271,288 bytes**; post-GC heap: **5,497,320 bytes**; retained delta: **5,226,032 bytes (6.1165%)**, versus pre-family `44fe69498699cd69b0cc54543e7dabe0703c28a7` at **54,489,256 bytes (63.7739%)**. Fold/source/cache/disk offsets remain live; input/chunks/rendered snapshots are released and Store publications excluded. No other test run was active during this measurement.
