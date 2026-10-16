# Complete reference-fold checkpoint export

## Files read
- `internal/thread/fold.go` → `Fold`, `Feed`, `Item`: serialized ownership and complete internal creation order.
- `internal/thread/render.go` → `contentRecipe`, `recipeField`, `newReferenceFold`: source references, scalar literals and equality digests.
- `internal/thread/main.go`, `main_markers.go` → `mainTurn`, `mainCall`, `mainOutcome`, `mainCallMarker`: historical openings, unfinished text and compact finals.
- `internal/thread/agent.go`, `agent_markers.go`, `shell.go` → `agentGroup`, `agentReports`, `registerShell`: shared ordinary owners and report slots.
- `internal/thread/child.go` → `childSnapshot`, `resolveChildren`: reversible snapshots, final markers and scoped parent joins.
- `internal/thread/boundaries.go`, `sends.go`, `observations.go`, `cache.go`: pending companions, delivery suppression, touch membership and `foldingRules`.
- `internal/thread/render_test.go`, `agent_markers_accounting_test.go`: reference fixture and unchanged fixed-shape workload.
- `docs/knowledge/features/thread-package.md` and `thread-package-main-thread-folding-validation.md`: no content retention; logical accounting is distinct from encoded bytes.
- `docs/knowledge/decisions/045-thread-index-over-history.md`, `docs/knowledge/features/development-verification.md`: checkpoint boundaries and evidence requirements.

## Context
Export complete in-memory continuation state without fetching history or rendering conversation content. Restore (#3490), recorded proofs (#3491) and persistence (#3336) follow separately. ADR 045 already owns the decision. No overlapping feature branches touch the planned files.

## Design
`ExportCheckpoint() (CheckpointState, []CheckpointItem, error)` supports reference folds only. Two explicit JSON mirror parts use schema 1 and current `foldingRules`; format changes require a schema bump. Metadata has no Content field. Recipes copy all references, digests and scalar literal bytes.

Private state includes owner/version/watermarks, publication/touch membership, successor/scope/pending boundaries, accepted/message indexes and an ordered item-ID manifest. The later importer must compare every item record against that manifest, including otherwise unreferenced rows. Item indexes retain their existing zero-based/sentinel meanings.

One-based table references (zero means nil) preserve turn generations, shared main/agent shell ownership and shared report slots independently of equal values. Turn roots retain current keys, original openings and open-text order. Calls retain compact finished markers without reconstructing mutable owners. Child roots retain scoped parent joins and reversible intrinsic/previous records only when present. Agent roots retain lifetimes, observations, calls, tasks and pending/final report membership. Entity registration reserves identity before traversing edges to terminate cycles. Deterministic root traversal makes exports reproducible.

Derived `childEdges`, `childAgents`, `taskIDs` and text slots can be rebuilt from exported joins, tasks and ordered open texts. Inline text buffers, legacy payload containers, source/caches, work counters and per-feed/publication scratch are excluded. All exported mutable storage is detached; the fold is unchanged.

## Concurrency model
Export runs synchronously at a completed feed boundary under Fold's existing serialized-owner contract. No goroutines or locks are added.

## State transitions and identity reuse
| Event | Race-enabled test |
| --- | --- |
| Empty export and inline rejection | `TestCheckpointEmpty` |
| Reused turn keys with original/historical openings and open text | `TestCheckpointRelationships` |
| Shared shell owners and report slots versus equal independent reports | `TestCheckpointRelationships` |
| Pending boundaries/sends, hidden children, suppressed deliveries, reversible/final children | `TestCheckpointRecords` |
| Export mutation and later feeds | `TestCheckpointOwnership` |

## Error handling
Inline folds return a generic unsupported-export error and no document. No I/O is performed. The later importer validates hostile or incomplete documents; export does not establish persistence durability or validate external documents.

## Testing strategy
Write export tests first and observe missing-API failure. JSON round trips assert decoded records/identities and complete internal order. A failing lookup source proves no rendering/read. Extend `TestReferenceFinalReportAccounting` without changing its workload: log both encoded parts and private bytes/turn for all four shapes; hex digests have fixed 64-byte encoding, so payload padding must change private encoded size by exactly zero. Run focused tests, then `go test -race ./internal/thread ./cmd/pyry ./internal/relay`, `go vet ./...`, and `go build ./cmd/pyry`. The dispatcher owns `make check` and the full-module race gate.

## Open questions
None. Sizing sketch: approximately 790 written lines, two exported types, zero migrated consumers, four acceptance criteria and one reject branch; re-count before plan commit remains within all limits.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/thread-package-main-thread-folding-validation.md`, **Validation and testing**: describe two-part export, stable internal item order, explicit schema/rules contract and measured encoded private-state boundary, including separate item-record cost.
- Pending documentation stage: `docs/knowledge/features/thread-package.md`, **Cache and epochs**: identify available in-memory export; restoration and Store checkpoint loading remain follow-ups.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `ExportCheckpoint` accepts no external document; explicit mirrors exclude source and inline payloads. Import validation is OUT OF SCOPE in #3490.
- Tokens/secrets: recipes retain only existing small scalar facts and references; no credential creation, logging or source lookup. Metadata summaries remain existing item metadata.
- File operations, subprocesses, network/I/O: none; persistence containment/durability is OUT OF SCOPE in #3336.
- Cryptography: existing equality digests export as fixed-size hex; they establish content equality, not document authentication.
- Errors/logs/telemetry: the only error is generic and contains no owner, content or host path; accounting logs sizes only.
- Concurrency: detached maps, slices and literal bytes prevent a caller mutating live fold state; reserve table identities before following cyclic shell links.
- Threat model: this is private in-memory state, with no relay/protocol admission changes. Hostile restore and disk corruption checks belong to #3490/#3336.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-16
