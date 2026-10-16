# Reference-fold checkpoint restoration

## Files read
- `internal/thread/checkpoint_document.go`, `checkpoint_export.go` → `CheckpointState`, `checkpointExporter`: complete item inventory and pointer identity tables.
- `internal/thread/fold.go`, `render.go` → `New`, `newReferenceFold`, `Render`: runtime initialization, source ownership and atomic unavailable rendering.
- `internal/thread/main.go`, `main_markers.go` → `mainTurn`, `mainCall`: negative item sentinels, generations and open-text slots.
- `internal/thread/agent.go`, `agent_markers.go`, `shell.go` → `agentGroup`, `agentReports`, `registerShell`: task membership, reciprocal ordinary owners and shared report markers.
- `internal/thread/child.go`, `boundaries.go`, `sends.go` → `saveChild`, `boundary`, `sendFact`: derived edges, reversible snapshots, attribution and suppression.
- `internal/thread/checkpoint_export_test.go`, `render_test.go`, lifecycle fixtures: JSON helper, independent reference baseline and existing semantic assertions.
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
