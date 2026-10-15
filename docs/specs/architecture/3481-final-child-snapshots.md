# Compact intrinsically final child snapshots

## Files read
- `internal/thread/child.go` → `saveChild`, `restoreChild`, `resolveChildren`: scoped edges, intrinsic restoration and revision comparison.
- `internal/thread/main.go` → `closeText`, `applyTerminal`: independent text and ordinary-call finality.
- `internal/thread/agent.go` → `applyAgent`: foreground results can reopen; non-result finals and enrichment recompute from references.
- `internal/thread/main_markers.go` → `takeMainCall`: preserve late main-to-child transfer.
- `internal/thread/observations.go` → `recordShown`, and `publication.go` → `publicationCapture.capture`: membership and affected recipes drive visibility/publication.
- `internal/thread/render_agents_test.go`, `render_work_test.go`, `render_test.go`, `render_endings_test.go`, `child_work_test.go`, `agent_markers_accounting_test.go`: payload guards, provisional closure, targeted repair and logical accounting.
- `docs/knowledge/features/thread-package-agents-and-background-work.md` § Parent repair: intrinsic recipes must remain independent of derived closure; parent edges survive finality.
- `docs/knowledge/features/thread-package-main-thread-folding-validation.md` § Validation and testing; ADR 045: existing accounting boundary and small continuation state.
- `CODING-STYLE.md`; `docs/knowledge/features/development-verification.md`: contract comments and scoped evidence.

## Context
Reference folds retain two full Items and recipes per child after independent completion. The public item/recipe already represents an independently closed child. Release redundant snapshots while preserving its scoped parent marker. Legacy `New` remains the compatibility oracle. No new decision record is needed.

## Design
Move intrinsic/previous Items, recipes and restoration/cache facts into an optional `childSnapshot`. `childState` retains scoped group/parent identity and a bounded independent-finality fact. Mutation hooks establish finality for closed text, terminal ordinary non-shell calls, and agents with non-foreground terminal evidence. Foreground agent/shell completion and unfinished work retain reversible snapshots. No finality is inferred from derived inactivity or `EndedOrder` alone.

`restoreChild` reconstructs a temporary snapshot from the public item/recipe when a compact child is mutated or reparented. The public state of such a child has no ancestor-derived closure. Existing dirty resolution compares intrinsic updates with previous public state, repairs indexed parents and revisions, then releases the snapshot again if independently final. `applyAgent` reads the public intrinsic state when no snapshot exists. Source-only recipe replacements must still mark publication candidates even when rendered equality preserves revisions/touches. First-terminal facts remain with existing call/report owners, without rendered content in markers.

No concurrent feature branch currently touches the planned production/accounting files. Estimate: about 520 written lines, zero exported types, at most seven existing mutation consumers, four acceptance criteria, no new error/reject branches; within all five limits. One deliverable: compact child continuation state with its behavioral and retention proof.

## Concurrency model
Fold access remains serialized by its owner. No goroutines, locks or shutdown paths change.

## State transitions and identity reuse
| Event | Race-detector coverage |
| --- | --- |
| Nested ancestor closure, late text and task-link reopening | `TestReferenceChildMarkers`, existing `TestReferenceInheritedEnding` |
| Independent text/tool/agent completion while parent changes | `TestReferenceChildMarkers` |
| Late parenting/reparenting, visibility repair and ancestor ending enrichment | `TestReferenceChildMarkers`, `TestReferenceFinalChildReachability`, `TestChildTargetedRepair` |
| Repeated reports/text enrichment and equal-content replacements | `TestReferenceChildMarkers`, `TestReferenceFinalChildTextEnrichment`, `TestReferenceEqualCompanion` |
| Reused source/lifetime/legacy IDs and late main marker transfer | `TestReferenceChildMarkerIdentity`, `TestChildNewLifetimeEarlyReport`, main-marker regressions |

## Error handling
No new error paths. Existing validation/scoped joins and source rendering failures remain authoritative. Missing source dependencies continue withholding complete output.

## Testing strategy
Write focused tests first and observe retention assertions fail. Compare exact legacy Items, observations, touches, publication visibility, versions, suppression and unproven boundaries under whole/chunked/single-entry feeds. Assert intermediate reversible and independently final states. A reflective private-state guard rejects Item/recipe snapshots behind any final-child marker index, including empty Content. Extend existing 16/64-turn by 16/4096-byte accounting, separating child markers/reversible snapshots from public recipes, existing main/agent markers, unresolved work, other lifecycle state and source caches. Record before/after logical bytes and exclusions.

Run `go test -race ./internal/thread ./cmd/pyry ./internal/relay`, `go vet ./...`, and `go build -o /tmp/builder-3481/pyry ./cmd/pyry`. The dispatcher owns the full `make check` verifier gate, including committed recordings/offline fixtures; do not duplicate it here.

## Open questions
None. Background shell lifecycle snapshots remain reversible; this slice does not compact their separate owners.

## Retention measurements
`TestReferenceFinalReportAccounting` ran the same eight-entry fixed-shape turns before and after implementation. Logical accounting sums scalar sizes and string bytes, excluding map/slice/string/pointer headers, alignment, unused capacity, authoritative fixture history and Store snapshots. Shared pointer targets count once; strings count per field without a shared-backing deduction. Source cache bytes use `recentSource`'s separate accounting. These are retained-field measurements, not heap/RSS estimates.

| Turns | Padding/entry | Private bytes/turn before → after | Public metadata/recipes | Child parent markers before → after | Reversible child snapshots before → after | Other lifecycle | Source cache total |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 16 | 16 | 1397 → 1093 | 757 | 99 → 100 | 305 → 0 | 506 | 42904 |
| 16 | 4096 | 1397 → 1093 | 757 | 99 → 100 | 305 → 0 | 506 | 565144 |
| 64 | 16 | 1394 → 1090 | 758 | 99 → 100 | 305 → 0 | 503 | 61176 |
| 64 | 4096 | 1394 → 1090 | 758 | 99 → 100 | 305 → 0 | 503 | 1046520 |

Every row additionally retains 71 agent-marker, 174 main-marker and 242 active/unresolved logical bytes per turn. Public items are inactive; pending speculative child/agent joins explain the unresolved category. Private totals sum those categories, child markers/snapshots and other lifecycle, excluding public metadata/recipes and source caches. Public and other-lifecycle figures round to whole bytes/turn; private totals round after summation.

The baseline walker classified snapshot Items/recipes with public metadata. Its original metadata totals were 1061/1062 bytes/turn; other lifecycle was 606/603. Reclassifying the two retained child Item/recipe copies (304 bytes), restoration boolean (1 byte), and parent marker/index facts (99 bytes) yields the before columns above. The new finality boolean adds one marker byte, for a net 304-byte reduction per turn. Required public recipes/text lists are outside the private marker bound. A runtime graph walk rejects snapshots even behind another private index and with empty Content.

The host reports a 96-byte `childState` and a separate 512-byte `childSnapshot` including headers/alignment; final children allocate only the former. These host sizes are separate from logical totals. Entry references are fixed-width uint64 and indexes are host-width int; fixed-length `sNNNN` identities keep marker cost stable, while longer identities increase logical bytes. Decimal JSON `ended_order` values explain the small public-recipe difference between history sizes. No rendered content moves into markers.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/thread-package-agents-and-background-work.md`, **Parent repair**: describe final child markers, which snapshots they release, why provisional ancestor/foreground closure stays resumable, and why final children retain reachability/repair facts.
- Pending documentation stage: `docs/knowledge/features/thread-package-main-thread-folding-validation.md`, **Validation and testing**: describe the structural retention check, byte-accounting method and measured boundary, distinguishing reversible child state and public recipes from marker savings.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `mainDecode`, `agentWork` and `childGroup` retain existing validation and original source/lifetime/scope identity. Marker reconstruction cannot adopt a different source's Item.
- Tokens/secrets: no credential handling; marker facts and test logs contain references and counts only, never rendered content.
- File operations: production changes perform no file operations; persistence/export is outside this slice (#3478 and later Store work).
- Subprocesses and cryptography: no production subprocess or cryptographic changes.
- Network/I/O: no new transport/read boundaries; source lookup and missing-dependency handling stay unchanged.
- Errors/logs/telemetry: no new public errors or payload logging; accounting reports only sizes.
- Concurrency: owner-serialized restoration/resolution remains atomic with respect to publication; no new goroutines.
- Threat model: scoped parent validation and older-only edges in `resolveChildren` prevent cross-source parenting; structural tests enforce no retained content in markers. Network admission and durable persistence remain outside this in-memory refactor.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15
