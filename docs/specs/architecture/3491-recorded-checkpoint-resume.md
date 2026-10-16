# Recorded-history checkpoint resume (#3491)

## Files read

- `cmd/pyry/thread_corpus_test.go` → `TestThreadRecordedBaselines`, `testRecordedFrames`, `testNormalizeRecorded`: the three committed inputs, real parser/emitter derivation and synthetic shell context.
- `cmd/pyry/corpus_golden_test.go` → `testFoldGoldenCheck`, `testFoldDigest`: unchanged golden and semantic coverage; JSON hashes alone cannot prove exact Content bytes or private continuation.
- `internal/thread/checkpoint_import.go` → `NewReference`, `HistorySource`, `ImportCheckpoint`: restore against the same owner/source without history lookup or prefix replay.
- `internal/thread/checkpoint_export.go` → `ExportCheckpoint`, `checkpointRecord`: deterministic detached state and complete creation-ordered metadata/recipe inventory.
- `internal/thread/checkpoint_document.go` → `CheckpointState`, `CheckpointItem`: public contract for otherwise private visibility, publication, touches, watermarks and continuation facts.
- `internal/thread/fold.go`, `internal/thread/render.go` → `Feed`, `Render`: serialized entry consumption and detached exact rendered items.
- `docs/knowledge/features/thread-package.md` § Reusable fold regression corpus: retain committed golden expectations and independent recorded semantics.
- `docs/knowledge/features/thread-package-main-thread-folding-validation.md` § Validation and testing: compare every internal row, and compare Content bytes directly rather than canonicalized JSON.
- `docs/knowledge/features/thread-package-agents-and-background-work.md`: recorded frames alone do not establish shell creation/result/turn-end context.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: use controlled corruption to establish oracle sensitivity.
- `docs/knowledge/decisions/045-thread-index-over-history.md`: resume equivalence includes unfinished work; persistence/loading belong to later tickets.

## Context

#3489 and #3490 are merged and supply export/import plus synthetic proofs. This
ticket adds one offline integration proof across all entry boundaries of all
three committed real recordings. No production behavior or decision record changes.
Fetched feature branches have no overlap with the two planned test files.

## Design

Call a new same-package resume helper from `TestThreadRecordedBaselines` using
its already-derived normalized raw entries. Leave recording derivation, semantic
assertions and goldens unchanged, including synthetic shell context.

Prepare expected snapshots at zero and after each entry using one uninterrupted
`thread.NewReference` fold. Each snapshot holds detached `Render` items and both
`ExportCheckpoint` parts. Compare typed values directly, retaining exact Content
bytes, all Item fields/revisions, complete nonpublic metadata/recipes and all
exported continuation facts, including touches, visibility/publication and
consumed/last-shown/suppression/unproven watermarks.

For every cut from zero through full, separately JSON marshal/unmarshal the two
saved export parts and call `ImportCheckpoint` with the same conversation owner
and a test-owned history source. Assert import makes no lookups. Compare immediately,
then feed only one suffix entry at a time and compare against each saved baseline.
The source exposes only entries through the current boundary and returns an error
for unavailable IDs. It supplies immutable entries; it does not fold or load Store.
Count cuts/comparisons and report them so an accidentally empty loop cannot pass.

Sizing: one deliverable, approximately 240 written lines including this plan,
zero exported types/interfaces, one updated consumer, three acceptance criteria,
and no production state-machine branches; all five limits remain within budget.

## Concurrency model

All new fold/source access is synchronous and serialized by the owning subtest.
No new goroutines, locks or timers; existing emitter/history fixture cleanup remains.

## State transitions and identity reuse

| Event | Race-tested coverage |
| --- | --- |
| Empty/full checkpoint and every emitted entry, including hidden and companion facts | `TestThreadRecordedBaselines` for all three recordings |
| Restore unfinished state, then advance each suffix entry with recorded turn/call/task identities | `TestThreadRecordedBaselines` with full rendered and exported comparison at every step |

## Error handling

Feed, render, export, JSON and import failures fail the owning subtest. Missing or
future source IDs fail explicitly. Diagnostics name cut, suffix boundary and
comparison category; there is no silent skip or fallback replay.

## Testing strategy

For this test-only change, run controlled corruptions first to prove the recorded
gate detects lost exported facts and raw Content differences, then remove those
faults. Run the recorded family under the race detector and the role-owned Linux
checks: `go test -race ./internal/thread ./cmd/pyry ./internal/relay`, `go vet ./...`
and `go build -o /tmp/builder-3491/pyry ./cmd/pyry`. The dispatcher's verifier gate
runs `make check` and the full-module suite. Do not regenerate captures/goldens
or run live Claude.

## Open questions

None.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/thread-package-main-thread-folding-validation.md`, **Validation and testing**, and `docs/knowledge/features/thread-package.md`, **Reusable fold regression corpus**: record resume equivalence at every entry cut and suffix entry of all three committed real histories. Identify retained synthetic shell creation/result/turn-end context and distinguish this offline regression gate from live capture evidence.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `testRecordedFrames` retains the real parser/emitter boundary; `ImportCheckpoint` validates the JSON-round-tripped document with the expected owner. Only fixed committed recordings supply input.
- [Tokens/secrets] No credential access, generation or live capture. Existing committed redacted fixtures remain the sole evidence source.
- [File operations] Fixed existing fixture paths and `t.TempDir` history storage only; checkpoints stay in memory. File integrity, atomic persistence and startup loading are OUT OF SCOPE in #3336 / #3337.
- [Subprocesses] The new proof launches no child process or shell and never executes recorded content.
- [Cryptography] No cryptographic operations or authentication changes; typed equality compares fixture state, not secrets.
- [Network and I/O] No network path; source lookups are in-memory and limited to the available history prefix.
- [Errors/logs] Report boundaries and comparison categories; no new operational logs or telemetry, credentials or host content.
- [Concurrency] One serialized owner per fold/source, with no new goroutines or shutdown paths.
- [Threat model] Offline tests verify continuation consistency only; production authorization, checkpoint authenticity and untrusted-file recovery are unchanged and remain with the existing importer and #3336 / #3337.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-16

## Revisions

### 2026-10-16 — validation runtime

The default three-package race command passed `cmd/pyry` (402.072s) and
`internal/relay` (60.576s), but the unchanged `TestCheckpointResumeCorpus` hit the
thread package's ten-minute timeout (600.179s). No thread files changed in this
ticket. Track that existing gate-runtime problem in #3494 (Backlog, priority:low).
Complete the same thread assertions separately with `go test -race -timeout 20m
./internal/thread`; record its result in the PR without treating a longer limit
as a repair of #3494. The recorded proof independently passed all three histories:
60 cuts and 687 immediate/suffix comparisons. No design or coverage changes.
