# Durable incremental checkpoints (#3504)

## Files read
- `internal/thread/checkpoint_read.go` → `readCheckpoint`, `readCheckpointItems`: committed format and retry authority.
- `internal/thread/checkpoint_export.go` → `ExportCheckpoint`, `checkpointRecord`: detached full inventory, including recipes.
- `internal/thread/checkpoint_import.go` → `ImportCheckpoint`: continuation graph validation.
- `internal/thread/cache.go` → `openCacheFile`, `writeCacheFile`, `syncCacheDir`, `removeLegacy`: containment, leaf admission and replacement rules.
- `internal/thread/store.go` → `Store`, `checkpoint`: scalar certificates and existing replay remain separate.
- `internal/history/sync.go` → `Sync`; `cache_dir.go` → `EnsureLogDir`: history durability and contained empty directories.
- `internal/thread/checkpoint_resume_test.go` → `testResumeProof`: immediate and per-entry continuation oracle.
- `cmd/pyry/daemon_thread_demand_test.go` → `TestDaemonThreadDemandWireAfterIdle`; `internal/relay/v2session_catchup.go` → `cancelThreadQuery`: initial live publication can legitimately cancel an unfinished idle reload query.
- `docs/knowledge/features/thread-package.md` § Cache and epochs: public rows omit required private continuation state; unloaded reads remain scalar-only.
- `docs/knowledge/features/thread-package-main-thread-folding-validation.md` § Validation and testing: reuse resume proofs.
- `docs/knowledge/features/history-package.md`, `CODING-STYLE.md`, ADR 045: history ownership and durability order.

## Context
One deliverable: a package-local durable transaction callable by the later worker integration. Store loading still replays history. ADR 045 already records the decision; no new ADR is needed.
Sizing: about 760 written lines (280 production, 410 tests, 70 plan), zero exported types, zero migrated consumers, four acceptance criteria, and no dispatch state machine. No overlapping remote branches touch the proposed new writer/test files.

## Design
Add `checkpoint_write.go` with a package-local writer holding Store, conversation, source and an acknowledged state. `commit` accepts detached complete fold state/inventory, caller-selected changed records and scalar certificate evidence. The caller supplies all private/recipe changes and serializes attempts for this conversation.
Validate the complete candidate and certificate before side effects. Sync history covering the candidate, then call `readCheckpoint` on every attempt. Missing/invalid state selects a fresh random generation and full inventory; persistence errors fail. A valid named generation supplies the reconciliation boundary, regardless of prior acknowledgement. Reject candidates older than surviving state.
Open the admitted index with no-follow/nonblocking flags and descriptor regular-file checks; fresh generations use exclusive creation at 0600. Recheck exact `LogDir` containment before mutations. Truncate only excess bytes beyond validated committed length, append checked length/CRC frames, sync and close, then sync the directory before state publication (also on retry).
Write complete state through a 0600 same-directory temporary file, checking short writes, sync and close. Recheck destination admission and containment, rename, then sync the directory. Advance acknowledgement only on complete success. Failed clean attempts conservatively invalidate named checkpoint state with best-effort directory sync; dirty post-rename failures may retain the complete newer state for retry. Never touch the scalar certificate.
Local operation/write seams inject faults without changing Store-wide behavior. No generation cleanup or compaction.

## Concurrency model
Synchronous transaction; no goroutines or locks added. Its owner serializes fold exports, changed-record selection and commits outside `Store.mu`. History methods retain their own locking.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| First checkpoint / empty inventory | `TestWriteCheckpointResume` |
| Repeated incremental checkpoints and recipe-only changes | `TestWriteCheckpointResume` |
| Partial append then retry against surviving old state | `TestWriteCheckpointFaults` |
| Rename succeeds, directory sync fails, retry preserves newer named boundary | `TestWriteCheckpointFaults` |
| Missing/invalid state then fresh full generation | `TestWriteCheckpointRecovery` |
| Failed clean attempt invalidates clean claim | `TestWriteCheckpointFaults` |

## Error handling
Return `ErrPersistence` for I/O failures without acknowledging progress. Validation failures never write. Surviving old/new state must remain internally complete; absent/invalid state yields no restored fold. Cleanup closes/removes unpublished temporary bytes best effort; failed clean-claim invalidation cannot authorize publication or certification.

## Testing strategy
Write tests first. Reuse `testResumeProof` through representative saved cuts and each suffix entry, checking exact appended frame bytes and unchanged prefix. Table-driven faults cover history, truncation, partial/short append, index sync/close/directory sync, temporary write/sync/close, rename and final directory sync with retry. Security cases reject redirected directories and symlink/directory/FIFO leaves, preserving history and outside bytes. Run focused red/green checks, then `go test -race ./internal/thread ./internal/history`, `go vet ./...`, and `go build -o /tmp/builder-3504/pyry ./cmd/pyry` on macOS. The verifier owns `make check`; Linux is outside this run's verification scope.

## Open questions
None.

## Documentation handoff

Satisfied in the documentation stage:

- [Cache and epochs](../../knowledge/features/thread-package.md#cache-and-epochs)
  now maps to the [owning section](../../knowledge/features/thread-package-cache-and-epochs.md#durable-transaction-and-retry-authority):
  history/index/state durability order, complete state rewrites versus selected
  record appends, interruption/reconciliation/retry authority, separate scalar
  certificates, continued Store replay and #3501 scheduling/final certification.
- [Validation and testing](../../knowledge/features/thread-package-main-thread-folding-validation.md#validation-and-testing)
  now maps to the [transaction proof](../../knowledge/features/thread-package-main-thread-folding-validation-checkpoints.md#durable-transaction-round-trip-and-fault-proof):
  saved-boundary/every-suffix round trips, recipe-only changes, exact append bytes,
  interruption/fault/retry coverage and contained temporary cleanup.
- Split both near-cap overviews into linked children, retained their original
  paths/anchors as maps, retargeted inbound section links and catalogued every
  new document. The startup topic map is unchanged.

## Security review
**Verdict:** PASS
- Trust boundaries: `commit` validates detached exports with `ImportCheckpoint`; retry authority crosses through `readCheckpoint`, never remembered offsets.
- Tokens/cryptography: generation uses existing crypto-random `randomToken`; CRC-32C detects corruption, not hostile authentication. No credentials added.
- Files: MUST FIX addressed in design: never truncate from stale acknowledgement after uncertain rename. Enforce package leaf admission, exact containment rechecks, no-follow regular descriptors, exclusive fresh creation and private modes.
- Subprocesses/network: none added; source reads reuse bounded committed-frame validation.
- Errors/logs: generic persistence errors; no content, paths or secrets logged.
- Concurrency: caller serializes per-conversation writes; no new goroutine or Store lock held over I/O.
- Threat model: local cache integrity follows existing cache admission. History-bound restoration belongs to #3337; scheduling and clean certification policy belong to #3501.
**Reviewer:** builder (self-review)
**Date:** 2026-10-16

## Revisions
- 2026-10-16: `commit` checks that surviving records plus supplied deltas equal the complete export before truncation. The fault fixture distinguishes retry changes since acknowledged A from the narrower C-only delta used to prove preservation of surviving B. `TestWriteCheckpointOrderAndRecipe` rejects an omitted recipe-only change without index mutation; `TestWriteCheckpointFreshFaults` covers failures before the first named state exists.
- 2026-10-16: `TestWriteCheckpointSecurity/during-state-write` exposed deferred temporary cleanup addressing an outside leaf after directory redirection. `replaceState` now rechecks exact directory containment before cleanup, leaving the original temporary file when containment is lost.
- 2026-10-17: Verifier finding 1 reproduced `TestDaemonThreadDemandWireAfterIdle` failing 13/50 race-enabled runs. The fixture installed its publisher after initial retirement, allowing first live publication to cancel the reload query through `cancelThreadQuery`. Install the publisher before starting discovery so the initial cursor exists before the phone connects; preserve the one-row, version-1, final-response assertion and report response metadata on failure. No production behavior changes. Rework requires complete `make check` and `make build` on macOS with `GOMAXPROCS=4`, empty `GOFLAGS` and the unchanged package timeout, in addition to focused repetition. `origin/feature/3512` touches separate scalar-certificate code; no overlap in the rework files.
- 2026-10-17: The complete four-thread gate exhausted `cmd/pyry`'s aggregate timeout at 600.715 seconds without an individual assertion failure; the timeout found several parallel tests still running. Retry the complete gate with `GOMAXPROCS=8` to provide more parallel-test capacity, retaining empty `GOFLAGS`, every gate target and the default ten-minute package timeout. This execution-profile change supplies no attribution for the historical aggregate timeout.
- 2026-10-17: Recovery merged main through `bd89eca2`, including readiness-cost repair `6196e535`, apps lifecycle fixtures #3524 and canonical-path runtime repair #3525. The sole conflict in `TestDaemonThreadDemandWireAfterIdle` retained main's publisher-ordering comment and the existing diagnostic assertion; `05208421` remains in ancestry. No checkpoint or resolver production code changes are needed here. The merged resolver supplies the private 35,000-sibling operation-count regression, fresh identity, casing, special-leaf and checked-close proofs. Run fresh complete macOS `make check` with `GOMAXPROCS=4`, explicit `GOFLAGS='-count=1 -json'` solely for uncached, counted evidence, and `TEST_TIMEOUT=10m` to retain the original deadline despite main's temporary thirty-minute default. Include the dependency-selected full checkpoint corpus and configured fake-daemon e2e, then run `make build` with empty `GOFLAGS`. Earlier red gates remain failures; the memory helper's two-minute deadline does not prove an allocation failure or OOM, and the startup failure remains separately unattributed. Independent verifier review remains required.
- 2026-10-17: The instrumented module suite passed, but global `GOFLAGS=-json` conflicts with staticcheck's `go list -f`. Retain that failed attempt and its complete counted race evidence; rerun the entire `make check` with empty `GOFLAGS`, four threads and the original ten-minute deadline. No assertions, package selection or production code change.

## Rework validation
- Recovery overlap check: `origin/feature/3514` changes `TestDaemonThreadDemandIdleReadKeepsConnection` in the shared test file; this merge resolves only `TestDaemonThreadDemandWireAfterIdle`. The functions are independent; no unmerged dependency is required.
- macOS arm64, Go 1.26.2; `GOMAXPROCS=4`, empty `GOFLAGS`, default ten-minute package timeout.
- `TestDaemonThreadDemandWireAfterIdle` before repair: 50 executed, 37 passed, 13 failed, zero skipped. Diagnostic repetition: 20 executed, 14 passed, six failed with an error response.
- After repair: 50 executed, 50 passed, zero skipped under `go test -race -count=50 -json -run '^TestDaemonThreadDemandWireAfterIdle$' ./cmd/pyry`.
- Controlled source overlays hold the provider result until the coordinator considers publication/idle retirement. With late publisher installation the query is cancelled (one executed, one failed); with early installation the identical control returns the complete final response (one executed, one passed). Neither control uses sleeps; both ran with the race detector. Scratch inputs/results: `/tmp/builder-3504/idle-control.py`, `idle-{old,fixed}-control.json`.
- Full acceptance remains unresolved: the four-thread module run passed `internal/thread` (309.372 seconds) and `internal/history`, but `cmd/pyry` timed out (600.715 seconds). The eight-thread retry failed `TestDaemonAppsShutdownRetainsOwnership/normal` (startup composition and cleanup waits), `TestStoreObservationRetainedSuffixMemory` (helper killed after 120.19 seconds), and package timeouts in `cmd/pyry` (601.644 seconds) and `internal/thread` (601.069 seconds). Neither run reached the standalone corpus, staticcheck, substrate guard or fake-daemon e2e. `make build` passed with four threads. Logs: `/tmp/builder-3504/rework-make-check.log`, `rework-make-check-8.log`, `rework-make-build.log`. Further failure attribution and complete green `make check` remain required; focused idle success does not satisfy verifier finding 1.
- Isolated diagnostics for the two additional failures passed with eight threads: `TestDaemonAppsShutdownRetainsOwnership` plus its two subtests (three records passed), and `TestStoreObservationRetainedSuffixMemory` (one record passed), zero skipped, under the race detector. JSON: `/tmp/builder-3504/rework-additional-focused.json`. These focused results establish neither full-suite attribution nor complete acceptance.
- Recovery complete, 2026-10-17: macOS 26.6.2 arm64 / Go 1.26.2. Corrected `GOMAXPROCS=4 GOFLAGS= make check TEST_TIMEOUT=10m` passed every tier: module race tests (`cmd/pyry` 303.754s, `internal/thread` 256.590s), staticcheck, substrate guard, seven configured fake-daemon e2e packages (root 201.813s), and the full checkpoint corpus (201 cuts plus parent: 202 executed/passed, zero skipped, 249.481s). Subsequent `GOMAXPROCS=4 GOFLAGS= make build` passed. Linux and live Claude were not run. Documentation and independent verifier review remain later-stage handoffs.
- Fresh counted evidence on identical production/tests: the first instrumented module tier executed 28,162 named records, passed 28,132 and skipped 30, with no failures; that aggregate attempt failed only at staticcheck's logging incompatibility. `TestDaemonThreadDemandWireAfterIdle` and `TestStoreObservationRetainedSuffixMemory` each passed 1/1; `TestDaemonAppsShutdownRetainsOwnership` and its children passed 3/3; resolver fanout, identity, special-leaf and descriptor tests passed. Pre-merge-commit demand checks passed 14/14, zero skipped, and the merge build passed. Retain the historical failures without attributing every timeout to the resolver.
- [Complete recovery evidence](../../../internal/thread/testdata/3504-rework-evidence.tar.gz), SHA256 `1a1e8839676e50189010e46f549c2efe6192f40188cd6a13010678ca5bd0e57d`, retains commands, complete stdout/stderr, exit statuses, counted summaries and historical fault/control inputs. All 31 payload checksums were verified against its manifest; the failed JSON-logging attempt remains separate from the successful complete gate.
