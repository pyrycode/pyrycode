# Reference-fold checkpoint restoration

## Files read
- `internal/thread/checkpoint_document.go`, `checkpoint_export.go` → `CheckpointState`, `checkpointExporter`: complete item inventory and pointer identity tables.
- `internal/thread/fold.go`, `render.go` → `New`, `newReferenceFold`, `Render`: runtime initialization, source ownership and atomic unavailable rendering.
- `internal/thread/main.go`, `main_markers.go` → `mainTurn`, `mainCall`: negative item sentinels, generations and open-text slots.
- `internal/thread/agent.go`, `agent_markers.go`, `shell.go` → `agentGroup`, `agentReports`, `registerShell`: task membership, reciprocal ordinary owners and shared report markers.
- `internal/thread/child.go`, `boundaries.go`, `sends.go` → `saveChild`, `boundary`, `sendFact`: derived edges, reversible snapshots, attribution and suppression.
- `internal/thread/checkpoint_export_test.go`, `render_test.go`, lifecycle fixtures: JSON helper, independent reference baseline and existing semantic assertions.
- `internal/thread/checkpoint_relationship_test.go` → `TestCheckpointResumeShellPromotion`, `TestCheckpointTurnTextOwnership`, `TestCheckpointFinishedMarkerValidation`: rework regressions for promoted scopes, text ownership and compacted markers.
- `docs/knowledge/features/thread-package.md`, `thread-package-main-thread-folding-validation.md`: source-backed recipes must not retain inline payloads; settled output alone misses late provenance replacement.
- `docs/knowledge/decisions/045-thread-index-over-history.md`, `docs/knowledge/features/development-verification.md`: checkpoint boundary and validation ownership.

## Context
Restore the existing two-part export without prefix replay. Persistence (#3336), Store loading (#3337), and recorded integration (#3491) remain separate. ADR 045 owns the decision. No overlapping feature branch touches the planned files.

Sizing: approximately 1,200–1,500 written lines, two new exported types/interfaces at most, no migrated consumers, five acceptance criteria. Structural validation has more than ten rejection conditions. This is already a grandchild of #3335 through #3478; verified parent links and existing `needs-human:sizing` require building through the overage. Natural seams would be graph restoration/validation and reusable synthetic continuation proof, but recursive splitting is prohibited.

## Design
Expose `NewReference(conversationID, source)` for callers with a `Lookup([]uint64) ([]history.Entry, error)` source. `ImportCheckpoint(conversationID, source, state, items) (*Fold, error)` validates compatibility, ownership, complete item manifest, references, index relationships and digests before returning an independent fold. `CheckpointCompatibilityError` supports `errors.As` for schema/rules rejection.

Allocate every identity-table object before wiring edges, preserving cycles and equal-but-independent objects. Copy maps, slices, recipes and literals. Initialize scratch through the existing constructor; rebuild text slots, child edges/agent membership and task IDs without resolving or rendering saved items. No inline format/cache, history lookup or prefix Feed occurs.

## Concurrency model
Synchronous operations under Fold's serialized-owner contract; caller owns source lifetime. No goroutines or locks added.

## State transitions and identity reuse
| Event | Race-enabled test |
| --- | --- |
| Empty/full and 200 distinct large-corpus cuts; suffix feeding | `TestCheckpointResumeCorpus` |
| Unfinished text, late tool reports after turn end and reused turns | `TestCheckpointResumeFocused` |
| Genuine agent ending, late links/reports, provisional child reopening | `TestCheckpointResumeFocused` |
| Compaction then divider, background shell after turn, scoped identity reuse | `TestCheckpointResumeFocused` |
| Equal-content companions in both orders | `TestCheckpointResumeCompanions` |
| Shared owners/report slots, document and fold mutation | `TestCheckpointImportOwnership` |
| First-lifetime child-shell promotion, including after a boundary and later reports | `TestCheckpointResumeShellPromotion` |
| Open text across turns, retained generations, sources and child lanes | `TestCheckpointTurnTextOwnership` |
| Finished ordinary and launcher calls later claimed by children | `TestCheckpointFinishedMarkerValidation` |

## Error handling
Compatibility errors are typed; all other malformed documents return an error and nil fold. Validate integer references before indexing and retain field-specific negative sentinels and history gaps. Errors identify structural categories without payloads. Source/render failures after restoration continue returning `ErrUnavailable` without partial results.

## Testing strategy
Write import tests first and observe missing API failure. Reuse `testCheckpointJSON` and `testReferenceFold`. A reusable same-package helper prepares an uninterrupted baseline and compares each restored cut immediately and after every suffix entry, including rendered bytes, item metadata, revisions and publication/watermark state. Shared baseline/digests may bound cost without dropping per-entry checks. Existing semantic fixtures/goldens remain unchanged. Test corruption, graph identity and nested isolation separately. Run focused tests, touched-package race checks (`internal/thread`, `cmd/pyry`, `internal/relay`), `go vet ./...`, and `go build ./cmd/pyry`. The dispatcher owns `make check` and the full-module gate.

## Open questions
None; performance of synthetic equivalence preparation will determine whether incremental exact digests are needed.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/thread-package.md`, **Cache and epochs**: distinguish available in-memory restoration from Store's still-full replay.
- Pending documentation stage: `docs/knowledge/features/thread-package-main-thread-folding-validation.md`, **Validation and testing**: document schema/rules rejection, complete stable item order, independent shared relationships, the source-backed construction/import API and reusable resume-equivalence helper with synthetic cut coverage. Recorded-frame coverage follows in #3491.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: MUST FIX addressed in design: `ImportCheckpoint` checks complete inventory, all table bounds and item relationships before exposing any graph; malformed documents cannot index unchecked memory or return a partial fold.
- Rework trust-boundary review: `promoteShellEvidence` changes the reciprocal ordinary-shell owner's group while retaining the child-call route. Import permits that difference only from a legacy group to a named lifetime with the same source/tag and an actual shared shell in the destination group's call slot. `checkpointImporter.turnText` validates unique text ownership, turn/source, main opening generations and child group/parent lanes from saved state without history I/O. Finished markers require a tool row or exactly the -2 launcher sentinel and a non-nil terminal. The three verifier MUST FIX findings are covered by rejection tests and valid all-cut continuation scenarios; no new trust boundary, I/O or concurrency is added.
- Tokens/secrets: no credential operations; recipes use existing scalar facts/references and no content is logged.
- File operations: none; file integrity, containment and atomic writes are OUT OF SCOPE in #3336.
- Subprocesses: none; source callbacks are invoked only by existing subsequent rendering/feeding.
- Cryptography: validate fixed-size SHA-256 equality digests; these are equality evidence, not checkpoint authentication.
- Network/I/O: import performs no I/O; supplied in-memory document allocation bounds belong to the downstream checkpoint reader in #3336.
- Errors/logs/telemetry: structural errors omit saved payloads and source errors retain existing unavailable behavior.
- Concurrency: constructor scratch and detached nested storage prevent mutation across document/fold boundaries; owner serializes access and source lifetime.
- Threat model: private in-process restoration adds no relay admission surface; authenticated checkpoint persistence is downstream in #3336.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-16

## Revisions
- 2026-10-16: production API is `NewReference`, `ImportCheckpoint` and `HistorySource`, with typed `CheckpointCompatibilityError`. Restoration lives in `checkpoint_import.go`; no existing production call sites change. Validation also enforces reciprocal ordinary-shell owners, older-only ancestor dependencies and scalar-only recipe literals.
- The reusable `testCheckpointResume` helper prepares one uninterrupted baseline. It retains exact per-item JSON/SHA-256 signatures and updates their aggregate through the existing post-join candidate capture, comparing at every suffix boundary. Each import independently renders/hashes its complete internal inventory, including hidden and suppressed rows. Raw Content is additionally hashed as bytes because embedding `json.RawMessage` in JSON would normalize whitespace/escaping. A negative control proves differing rendered bytes cannot compare equal. No baseline preparation or rendering feeds a restored prefix.
- `TestCheckpointResumeCorpus` uses 201 distinct cuts: empty plus the final 200 boundaries of `testFoldCorpus(30000)`, including the full cut. Focused histories use every cut; `TestCheckpointEqualCompanionProvenance`, `TestCheckpointLifecycleAssertions` and `TestCheckpointSnapshotIsolation` add direct semantic and ownership assertions. Genuine terminal conflicts remain unchanged; a durable-only ordinary shell first becomes foreground eligible on its mapped companion and legitimately revises then.
- Written work is approximately 1,100 lines including plan and tests, two exported types/interfaces, zero migrated consumers and five acceptance criteria. The verified grandchild sizing exception remains applicable.
- The first scoped race run timed out at 600 seconds in repeated `checkpointKeys` sorting during corpus export. The proof now shares an immutable prefix checkpoint and runs large-history cuts through eight bounded workers; each has independent folds/source, and channel closure plus a WaitGroup joins all workers, including assertion failure. Small histories use one worker. This changes test preparation only: every final cut still round-trips both export parts and compares every suffix boundary with the uninterrupted baseline. Completion/comparison counters reject dropped cuts.
- The bounded rerun measured roughly 20 seconds per full checkpoint cut under race instrumentation. To keep the required offline proof within the package gate, `checkpointKeys` now formats each immutable key once per export, rather than formatting both keys on every sort comparison. The comparison strings, stable ordering, two-part schema and exported bytes are unchanged; existing export/relationship/ownership tests cover the same contract.
- Continuation: the complete 201-cut proof passed with 50,101 comparisons in 378.98 seconds, but the package later timed out in `TestAffectedPublicationOracle` at the default 600-second limit. `TestCheckpointResumeCorpus` now participates in the package's existing parallel-test scheduling so its long sequential setup no longer postpones all existing parallel tests. Its bounded workers and every comparison remain unchanged.
- Final validation: `go test -race -json ./internal/thread ./cmd/pyry ./internal/relay` passed. Thread: 1,791 tests/subtests passed and one opt-in heap measurement skipped; CLI: 16,565 passed and two existing live/platform skips; relay: 1,534 passed. The corpus completed 201 distinct cuts and 50,101 immediate/suffix comparisons in 392.34 seconds; the complete thread package finished in 527.444 seconds within the unchanged default timeout. `go vet ./...` and the daemon build passed. Goldens are unchanged. The verifier owns `make check` and the full-module gate; documentation remains pending as listed above.
- 2026-10-16 rework, verifier findings 1–3 at `f78f2c1f81ab`: preserve distinct saved child-route and promoted ordinary-shell scopes, validating source and destination membership instead of requiring group equality. Validate coalescing text against its turn/source, unique owner, retained main-opening generation or child group/parent. Reject finished-marker -1, non-tool rows and absent terminals while preserving -2 launchers. The focused tests first failed on all three reported causes; their valid histories use the reusable all-cut proof, including suffix entries after promotion and late child claims. Rework adds about 300 lines, no exported types or migrated consumers; the recorded grandchild sizing exception still applies. Fetched feature branches have no overlap with these changed files.
