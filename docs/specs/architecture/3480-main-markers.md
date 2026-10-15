# Compact finished main work

## Files read
- `internal/thread/main.go`: `mainWork`, `foldWork`, `closeText`, `applyTerminal`; first endings, opening generations and late text.
- `internal/thread/child.go`: `childWork`; late parent evidence can transfer an ordinary call to a child lane.
- `internal/thread/shell.go`: `registerShell`, `promoteShellEvidence`; ordinary owners must remain shared with agent calls.
- `internal/thread/fold.go`, `observations.go`, `boundaries.go`: ownership, publication and scope boundaries remain unchanged.
- `internal/thread/agent_markers_accounting_test.go`, `agent_markers_test.go`, `render_test.go`, `main_closure_test.go`: accounting, differential replay and original-opening fixtures.
- `docs/knowledge/features/thread-package-main-thread-folding-lifecycle.md`: inactive late text still needs coalescing closure; first endings win.
- `docs/knowledge/features/thread-package-main-thread-folding-validation.md`: logical accounting categories and limitations.
- `docs/knowledge/features/thread-package-agents-and-background-work.md`: provisional shell closure is reversible after a late task link.
- `docs/knowledge/decisions/045-thread-index-over-history.md`, `CODING-STYLE.md`: content-free private facts and scoped implementation.

## Context
Production reference folds retain finished ordinary call objects and their mutable call map after completion. Reduce those to value markers while retaining the turn's bounded original-opening, source, first-ending and coalescing facts. ADR 045 already records this decision. Legacy `New` remains the differential oracle. No overlapping pushed feature branches touch the selected production files.

## Design
Keep `mainTurn` as the bounded turn marker/cursor: its key, original opening, first ending and current text tracking are all needed by future folding. Its `calls` map becomes exclusively mutable/unresolved work and shared shells. Add a separate value-marker map for finished ordinary calls, holding only group identity, creating item index and first outcome reference; a sentinel item index represents a launcher. Release the old `calls` map when empty, including its allocation. Never retain a `mainCall` behind the marker index.

Reference mode reduces a created ordinary call after its first terminal, and launchers after creation. Turn closure reduces remaining completed ordinary calls after applying its first ending. Later events reconstruct only the addressed marker transiently, then reduce again. Uncreated calls retain pending outcomes; shells retain the original shared owner because late linking/promotion can change their public state. Child lanes continue using mutable calls. `childWork` can take an ordinary marker when late parent evidence arrives, reconstructing the original item/group/first outcome before transferring ownership. Item metadata/recipes and source caches are unchanged.

## Concurrency model
No goroutines or locks added. The existing serialized Fold owner performs reduction and reconstruction synchronously.

## State transitions and identity reuse
| Event | Race-enabled regression |
| --- | --- |
| First ending then late result/creation and redundant terminals | `TestReferenceMainMarkers` |
| Inactive late text then delivery/divider and repeated endings | `TestReferenceMainMarkers` |
| New opening reuses turn/call IDs; recovery targets original opening | `TestReferenceMainMarkers` |
| Finished ordinary call gains child parent evidence | `TestReferenceMainMarkerChild` |
| Shell main closure then lifetime promotion, late task link and completion | `TestReferenceMainMarkerShell` |
| Same IDs in distinct recorded sources/lifetimes and legacy scopes | `TestReferenceMainMarkerShell`, existing identity/replay fixtures |

## Error handling
No new error branch. `mainDecode` and original-opening source/turn validation remain the boundary. Marker reconstruction cannot replace the first terminal; pending and shared evidence is excluded from reduction.

## Testing strategy
Write structural/intermediate regressions first and observe their failure against mutable finished call maps. Compare full items/content, observations, touches, publication membership, suppression and watermarks with legacy for whole, chunked and single-entry feeds. Extend existing fixed-shape accounting at 16/64 turns and 16/4096 padding bytes, separating main markers, agent markers, metadata/recipes, unresolved work, other lifecycle and caches. Bound marker entries/fields independently from redundant facts and content size. Run focused regressions, thread/cmd/relay race suites, `go vet ./...` and `go build ./cmd/pyry`. The dispatcher owns `make check` and full-module race validation.

## Sizing
One deliverable; approximately 550 written lines including plan and tests, no exported types, fewer than ten internal consumers, four acceptance criteria and no new reject branches. Rechecked against the written design: all five limits hold.

## Open questions
None.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/thread-package-main-thread-folding-validation.md`, **Validation and testing**: describe finished main-turn/call markers, their first-ending/original-opening guarantees, the byte-accounting method and measured boundary, distinguishing other lifecycle state.
- Pending documentation stage: `docs/knowledge/features/thread-package-agents-and-background-work.md`, **Background shells**: state that main-turn closure does not authorize discarding shell continuation or late-link evidence.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `mainDecode` validates inert history identity; referenced recovery still checks original source and turn. SHOULD FIX: explicitly test original-opening reuse and scoped shell evidence through compaction.
- Tokens/secrets: markers contain identity and entry/item references, never rendered content; no credential handling added.
- File operations, subprocesses, cryptography, network/I/O: no production operations added; existing history rendering boundary is unchanged.
- Errors/logs/telemetry: no payload logging or diagnostics added; accounting emits aggregate sizes only.
- Concurrency: synchronous serialized Fold mutation, with no new goroutine or shutdown path.
- Threat model: retain unresolved joins and shared shell ownership; dropping them would allow a later same-ID event to attach to the wrong lifetime. Store/export persistence is out of scope in #3478 and subsequent work.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15

## Revisions
- 2026-10-15: ordinary duplicate facts can return directly from a finished marker; only a child claim needs reconstruction. Duplicate creations still close the turn's current text run. Store scope/lifetime in the call marker and derive its recorded source from the owning turn, avoiding duplicate source fields. First-lifetime promotion also updates itemless launcher markers, preserving the existing child-transfer rule. `TestReferenceMainMarkerPending` additionally checks this promotion, uncreated outcomes, source/lifetime mismatch and release of empty finished-turn maps.
- 2026-10-15: the pending-child-claim regression exposed one remaining empty mutable map after transferring the last unresolved call. `takeMainCall` now releases that allocation immediately, including for the legacy path; rendered output and call ownership are unchanged.

## Offline retention evidence
`TestReferenceFinalReportAccounting` passed under `-race` using the existing eight-entry fixed-shape workload: main Read/result, Agent launch, independently finished child Read/result, task link/completion and main ending. Each turn has its own fixed-length `sNNNN` recorded Claude source. Main turns retain two value markers and no mutable calls; child lanes retain their existing representation.

| Turns | Entries | Padding bytes/entry | Main markers/turn | Agent markers/turn | Metadata/recipes/turn | Active/unresolved/turn | Other lifecycle/turn | Source cache total |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 16 | 128 | 16 | 174 | 71 | 1061 | 242 | 606 | 42904 |
| 16 | 128 | 4096 | 174 | 71 | 1061 | 242 | 606 | 565144 |
| 64 | 512 | 16 | 174 | 71 | 1062 | 242 | 603 | 61176 |
| 64 | 512 | 4096 | 174 | 71 | 1062 | 242 | 603 | 1046520 |

`testFinalReportBytes` follows retained fields, counting scalar sizes and string bytes, including intrinsic/previous child state. The main category includes turn/opening index keys, bounded turn cursor facts and finished call values/outcomes. Existing agent report markers are separate. Mutable main calls (shared shells or uncreated calls), active turns, pending child reports and agent-report candidates are unresolved work. Child lifecycle/snapshots and other agent indexes remain other lifecycle state. The workload has no public active items; unresolved category bytes are private speculative child/result/agent joins, not evidence of active public work.

These are logical bytes, not heap or whole-Fold targets: map/slice/pointer/string headers, allocation capacity/alignment, authoritative fixture history and Store snapshots are excluded. Pointer targets count once at their first owning traversal; strings count per field without a backing-sharing deduction. Source cache bytes use the existing independent bounded cache accounting. Values per turn are rounded. Entry references are fixed-width uint64 and indexes use the host's 64-bit int; identity lengths affect byte counts. Decimal JSON recipe IDs explain the small metadata difference at the larger history size. The structural test bounds both fields and entries as redundant terminals/results grow, independently of these accounting exclusions. Main marker storage contains identity strings and fixed outcome/source references, with no rendered content. Other uncompacted categories are not claimed as savings.

Focused race regressions passed, including intermediate first-ending, late creation/text, original-opening recovery, late child transfer, pending outcomes and shared shell promotion/link/completion. Goldens were not edited. The builder's downstream race suites, vet and build are recorded in the PR; the dispatcher owns the full `make check` gate.
