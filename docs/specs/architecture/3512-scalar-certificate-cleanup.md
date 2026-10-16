# Contained scalar-certificate cleanup (#3512)

## Files read

- `internal/thread/cache.go` → `writeCacheFile`: temporary lifetime, replacement and existing containment rechecks.
- `internal/history/log.go` → `LogDir`, `resolveDir`: exact symlink containment on each lookup.
- `internal/thread/store.go` → `checkpoint`, `NewStore`: scalar certificate caller and injected replacement seam.
- `internal/thread/certificate_test.go` → `TestStoreCertificateWrites`, `TestStoreCertificateFinalSyncFailure`: existing success and durability coverage.
- `internal/thread/store_test.go` → `testThreadStore`, `testStoreAppend`: isolated history fixture without starting workers.
- `docs/knowledge/features/thread-package.md` § Cache and epochs: history is authoritative; certificate admission must not widen deletion.
- `docs/knowledge/features/development-verification-prove-that-tests-distinguish-the-change.md`: prove the regression fails before repair.
- Commit `5356a27d` → `checkpointWriter.replaceState`, `TestWriteCheckpointSecurity`: analogous cleanup guard and directory redirect sequence on feature/3504.
- Rework: `cmd/pyry/reply_fallback_test.go` → `TestReplyFallbackProcessEvidence`: the own-deadline arm deliberately waits nearly thirty seconds.
- Rework: `internal/apps/retirement_errors_test.go` → `TestPublishServiceRetirementUncertain`: the failed readiness arm precedes the process-retirement assertions.

## Change

Replace the unconditional deferred temporary removal in `Store.writeCacheFile` with a fresh `history.LogDir(id)` check requiring success and equality with the original directory. When containment is lost, leave the original temporary file and any outside namesake intact. Preserve the existing error, replacement, close and durability contracts. This adds one guard and no types, interfaces, callers or lifecycle state. No feature branch currently overlaps the two files to edit; feature/3504's checkpoint guard is an independent analogue, not a dependency. Expected total written work is about 150 lines including this security review and focused tests, below all five sizing limits (0 exports, 0 consumer updates, 2 acceptance criteria, 1 added reject branch).

## Testing strategy

Add `TestStoreCertificateTemporaryCleanup` with success, failed replacement and redirected failed replacement cases. Seed real history and an existing certificate. In the replacement callback, rename the history directory, symlink its old path to an outside directory, create a marker matching the temporary basename, and return a failure. Assert `ErrPersistence`, unchanged marker/history/certificate bytes and retained temporary bytes in the renamed directory. Success must publish the new certificate without temporaries; ordinary failure must preserve the old certificate/history and remove its temporary. Run the test before repair to observe marker deletion, then run `go test -race ./internal/thread/...`, `go vet ./...` and `go build -o /tmp/builder-3512/pyry ./cmd/pyry` on macOS. The verifier owns the full-module gate.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `writeCacheFile` retains canonical conversation ID and package-owned leaf admission; cleanup must independently revalidate the filesystem boundary through `LogDir`.
- [Tokens, secrets, credentials] No credential generation, storage or exposure changes; existing scalar certificate fields and temporary naming remain unchanged.
- [File operations] The planned fresh exact-directory check closes the observed redirected cleanup path. Existing temporary mode `0600`, atomic rename and destination admission remain intact. This follows the established path-recheck contract; it does not claim descriptor-relative atomicity against a swap between the check and removal.
- [Subprocesses] The changed path starts no subprocesses.
- [Cryptography] No changes to random tokens, hashes, keys, nonces or comparisons.
- [Network and I/O] No socket or protocol changes; only best-effort temporary removal gains a boundary check.
- [Errors, logs, telemetry] Preserve generic `ErrPersistence`; introduce no logs or path disclosure.
- [Concurrency] Add no goroutines or shared state. The deterministic replacement callback runs synchronously; store serialization and file-close ordering remain unchanged.
- [Threat model] Address local directory redirection at deferred cleanup while preserving history as the durable source. No new relay, mobile or CLI trust boundary is introduced.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-16

## Revisions

- 2026-10-16: The combined thread race run exhausted Go's default ten-minute package timeout during `TestCheckpointResumeCorpus`, with the cleanup regression and other tests already passing. Follow the existing Makefile split: run `go test -race -run '^TestCheckpointResumeCorpus$' ./internal/thread` and `go test -race -skip '^TestCheckpointResumeCorpus$' ./internal/thread/...` separately, giving the complete corpus its own timeout budget. No implementation contract changes.
- 2026-10-17: Rework finding 1 requests completion of the dispatcher gate; the verifier found no cleanup-code defect. Retrieved the verifier evidence and read the baseline-instability tracker #3520. No production, test, security or timeout contract changes are justified by the focused reruns below. The complete dispatcher `make check` followed by `make build` remains pending before approval; focused passes do not replace it or establish the historical failures' cause.

## Rework validation

At reviewed head `b6892492fe23423e13958e8cd261784fd339e084`, on macOS arm64 with Go 1.26.2, each uncached command completed successfully. Counts include parent tests and subtests; all had zero failures and zero skips.

| Command | Executed / passed |
| --- | --- |
| `go test -race -json -count=1 -run '^TestReplyFallbackProcessEvidence$' ./cmd/pyry/...` | 10 / 10 |
| `go test -race -json -count=1 -run '^TestPublishServiceRetirementUncertain$' ./internal/apps/...` | 4 / 4 |
| `go test -race -json -count=1 -run '^TestStoreCertificateTemporaryCleanup$' ./internal/thread/...` | 4 / 4 |

`go vet ./...` and `go build -o /tmp/builder-3512/pyry ./cmd/pyry` also passed. Raw JSON and stderr are in the run's scratch files `/tmp/builder-3512/rework-{reply,apps,cleanup}.*`; counted outcomes are retained here. `own_deadline` passed in 29.90 seconds; the earlier package timeout interrupted that arm after only 21 seconds, so the timeout does not demonstrate that arm exceeding its own budget. The focused apps readiness pass does not explain its historical timeout. Linux is outside this macOS ticket's verification scope.

## Documentation handoff

- Pending for the documentation stage: update `docs/knowledge/features/thread-package.md` § Cache and epochs, scalar-certificate persistence description: temporary cleanup requires a fresh successful `history.Store.LogDir` result equal to the original directory; losing containment intentionally retains the original temporary and preserves an outside namesake. Preserve the documented path-recheck scope without claiming descriptor-relative atomicity.
