# Incremental checkpoint reader (#3503)

## Files read

- `internal/thread/cache.go` → `cacheLeaf`, `readCacheFile`, `removeLegacy`: contained regular-file reads and legacy cleanup boundary.
- `internal/thread/checkpoint_document.go`, `checkpoint_export.go`, `checkpoint_import.go` → `CheckpointState`, `ExportCheckpoint`, `ImportCheckpoint`: detached inventory and graph validation.
- `internal/thread/checkpoint_resume_test.go` → `testResumeProof`, `testCheckpointResume`: complete internal-row and every-suffix equivalence proof.
- `internal/thread/certificate_test.go` → `TestCertificateLeaves`, `TestStoreCertificateMigration`: allow-list and cleanup compatibility.
- `internal/history/log.go` → `LogDir`, `resolveDir`: exact directory containment, including warm stores.
- `docs/knowledge/features/thread-package.md` § Cache and epochs and `thread-package-main-thread-folding-validation.md` § Validation and testing: public items omit continuation rows; scalar metadata must remain separate.
- `docs/knowledge/decisions/045-thread-index-over-history.md` → Decision 4 and Reliability: committed-length generation format.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: validation and test conventions. QMD search confirmed the owning checkpoint documents.

## Context

Provide one independently testable package reader for #3337 and the durable writer #3504. Store loading still replays all history. No new decision record is needed. No active feature branch overlaps the planned files.

## Design

`thread-index-state.json` contains independently versioned schema 1, a generation filename, committed valid byte length, scalar certificate evidence and the complete exported fold state. Index names are exactly `thread-index-<32 lowercase hex>.bin`. Each frame has an eight-byte header: big-endian uint32 JSON payload length followed by big-endian uint32 CRC-32C (Castagnoli) of one `CheckpointItem` JSON payload. Empty indexes are valid.

`Store.readCheckpoint(id, source)` returns a detached fold plus its persisted state, or nil fold and an error. It does not install a worker or read history. Require explicit non-null committed length, clean flag, owner, version, last-shown and checksum; validate epoch, evidence shape, persisted schema and agreement with exported owner/version/watermark before import. Both clean and dirty states are readable.

Extract `openCacheFile` from the existing reader so JSON state and binary index use the same allow-list, containment, Lstat, O_NOFOLLOW/O_NONBLOCK and f.Stat checks. Extend admission only for the exact state/index conventions. Restrict `removeLegacy` explicitly to legacy names so checkpoint leaves survive.

Validate committed length against opened file size before reading; validate every header and payload length against remaining committed bytes before allocation/read. Ignore all bytes after the boundary without truncation. Collect last record per ID in file order, require the complete inventory to equal the manifest, arrange rows by that manifest, then delegate graph validation to `ImportCheckpoint`.

## Concurrency model

Synchronous reads, no goroutines or new locks. The caller serializes fold/source ownership and checkpoint publication; generation files are append-only and state replacement is atomic in the future writer.

## State transitions and identity reuse

| Event | Race-enabled test |
| --- | --- |
| Repeated item ID replaces its earlier record; multiple saved boundaries resume through every suffix feed | `TestReadCheckpointResume` |
| Repeated read, dirty/clean and empty checkpoint, appended/torn uncommitted bytes | `TestReadCheckpointResume`, `TestReadCheckpointInvalid` |
| Reused fold call/task identities and hidden/suppressed rows | `TestReadCheckpointResume` via synthetic corpus and send/child fixtures |

## Error handling

Missing/corrupt/inconsistent checkpoint returns no fold; filesystem security/I/O failures retain `ErrPersistence`, and invalid data uses `errCacheAbsent` or import's validation error. No partial fold escapes. No history-bound/checksum equality or epoch/fallback decisions here (#3337).

## Testing strategy

Write file-fixture tests first. Reuse `testFoldCorpus`, send and unresolved-child fixtures and `testResumeProof` at multiple cuts; compare immediately and after every suffix entry, with source reads forbidden during restoration. Mutate state evidence, compatibility, manifests, framing, CRC, lengths, file size and uncommitted tails. Exercise directory redirects, special/symlink leaves, path escape names and cleanup preservation with immutable history/outside-byte checks. Existing scalar certificate and bounded unloaded-metadata tests remain applicable.

Linux checks: focused new tests, `go test -race ./internal/thread ./internal/history`, `go vet ./...`, `go build -o /tmp/builder-3503/pyry ./cmd/pyry`. The verifier runs `make check` and the full-module suite.

Sizing before commit: approximately 650 written lines (240 production, 330 tests, 80 plan), zero exported types/interfaces, zero migrated consumers, three acceptance criteria, no new state machine (at most ten validation categories). All five limits hold.

## Open questions

None. This plan defines the framing and filenames for the follow-up writer.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/thread-package.md`, “Cache and epochs”, and `docs/knowledge/features/thread-package-main-thread-folding-validation.md`, “Validation and testing”: describe framing/generations, committed-length reading, corruption rejection and package round-trip proof. State that scalar certificates remain separate, Store loading still replays history, scheduling is delivered by #3501 and durable writing by #3504.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: persisted bytes remain untrusted until evidence, inventory and `ImportCheckpoint` graph checks succeed; no partial fold escapes `readCheckpoint`.
- Tokens/secrets: generations and epochs are identifiers validated with `validToken`; no credentials are introduced or emitted.
- File operations: `openCacheFile` preserves exact `LogDir` containment and no-follow regular-file checks. Admission is exact, not prefix-only. Reader writes nothing; cleanup admits only legacy leaves. Fixtures use 0600 files/0700 directories.
- Subprocesses/network: none introduced; reads are local files only.
- Cryptography: CRC-32C detects corruption, not authenticity. History evidence uses existing SHA-256 shape validation; history equality/authenticity is OUT OF SCOPE for #3337.
- I/O: committed/file/remaining length checks precede payload allocation and reading. JSON state is local, variable-size continuation data under the existing cache reader contract.
- Errors/logs: fixed cache errors and importer categories expose no host paths, payloads or secrets; no new logging.
- Concurrency: synchronous reader owns its descriptors and closes every one; no new shared state or goroutines. Durable atomic state publication belongs to #3504.
- Threat model: hostile redirected local paths must be rejected. Hostile concurrent replacement of directory ancestors is outside the existing `readCacheFile` security model; this change preserves its boundary.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-16

## Revisions

- 2026-10-16, verifier finding 1: extract `readCheckpointItems(io.Reader, validLength, itemIDs)` for injected frame-read failures. EOF and unexpected EOF remain invalid checkpoint data; other header/payload I/O failures return `ErrPersistence`, separately from CRC rejection. Discard all decoded rows on failure. File admission and pre-read size checks remain in `Store.readCheckpoint`.
- 2026-10-16, verifier finding 2: run every absent/null evidence mutation against both version-one and empty version-zero checkpoints. Retain clean/dirty explicit-zero success coverage in `TestReadCheckpointResume`.
- 2026-10-16, re-review gate finding 1 against `103e7793`: retrieved the complete dispatcher evidence; its wake-test failure and timed-out baseline do not establish an inherited failure or a checkpoint regression. On the merged Linux candidate, a fresh diagnostic `go test -tags e2e -race -count=1 -json -run '^TestChannelPost_E2E_DisconnectedWakeReplayAndHistory$' ./internal/e2e` executed and passed one test. No checkpoint or wake behavior changes are justified by that diagnostic. Request the dispatcher's complete `make check` followed by `make build` on this pushed revision before approval; this focused pass is not gate evidence, and the previous gate remains red until that complete rerun succeeds. The pending full gate belongs to the dispatcher; no test is skipped or weakened.

### 2026-10-16 — maintainer-approved test orchestration repair

The maintainer authorized this narrow root `Makefile` repair interactively. The automatic builder's file restriction does not grant that role general Makefile access. Continue this PR and preserve the reader's I/O-error classification and empty version-zero evidence fixes.

Both runtime repairs from #3494/#3496 are present at `fb0ca88d`. Retained passing reports show 177–213 seconds before the parallel corpus starts, followed by 326–398 seconds for the corpus itself. The failed package exhausted its 600-second budget with the corpus running for only 364 seconds. A fresh Linux race diagnostic at that exact revision passed all 201 cuts and 50,101 comparisons in 448.91 seconds (202 passing parent/subtest records, zero failures/skips). It used Go 1.26.9 on build-fractal-2 with eight logical CPUs and an eight-CPU quota; simultaneous Desktop tests, host CPU saturation and CPU-pressure samples demonstrate current contention. Memory pressure and quota-throttling deltas were zero. Historical failed runs lack resource samples, so their exact contention contribution is unknown.

Change only the `test` recipe: first run the ordinary complete package selection with the exact corpus root excluded, then require that exact corpus and all its subtests in a separate thread-package invocation. Both commands retain `-race`, ordinary offline inclusion, existing caching behavior and Go's default timeout. The two selections are complementary; the corpus is mandatory and runs once, with its unchanged 30,000 entries, 201 cuts, 50,101 comparisons, exact-content checks and security assertions. Sequential invocations prevent the rest of this gate from consuming the corpus process's deadline or overlapping its CPU work. Fleet capacity settings remain outside this patch.

Security review of this revision: **PASS**. The recipe introduces only fixed test-name patterns and an existing package path; it adds no input, credential, network, persistence or production trust boundary. Commit this revision before changing the recipe. Verify the selection partition, then run an uncached Linux complete `make check` and subsequent `make build`, retaining named counts, phase timing and any failure evidence. The complete check must fit the dispatcher's existing 20-minute command budget. The earlier disconnected-wake e2e failure still requires full-suite acceptance; focused diagnostics do not clear it. Independent verifier review and the existing documentation handoff remain required.
