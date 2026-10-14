# Bounded thread cache checkpoints

## Files read
- `internal/thread/store.go` — `Store.run`, `publish`, `Unload`, `Shutdown`: serialized complete progress and joined retirement.
- `internal/thread/cache.go` — `checkpoint`'s persistence path, `recoveryCandidate`, `finishRun`: atomic replacement, epoch eligibility and final certification.
- `internal/history/forward.go` — `ForwardReader.Tail`, `deliver`: blocking committed tail with serialized acknowledged chunks.
- `internal/thread/store_test.go` — `TestStoreReplayTailIsolation`, `TestStoreLifecycle`, reader wrappers and full-replay assertions.
- `internal/thread/cache_test.go` — continuation, interruption, containment, unloaded-participant and certificate regression coverage.
- `docs/knowledge/features/thread-package.md` § Cache and epochs — cached public items cannot supply private continuation or last-shown state.
- `docs/knowledge/features/thread-package-background-store.md` § Isolation and lifecycle — caller drains producers/catches up before shutdown; keep unloaded final validation.
- `docs/knowledge/decisions/042-daemon-built-thread.md` — history remains authoritative.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md` — generic errors, cancellation and behavioral proof.

## Context
An active conversation rewrites its complete JSON cache on every publication. Bound those replacements without delaying observations or changing recovery semantics. One deliverable: checkpoint cadence and its lifecycle exceptions. No new decision record is required.
Sizing: about 130 production lines, 430 test/helper lines and 65 plan lines (625 total); zero exported types, zero consumer migrations, four acceptance criteria and fewer than ten new reject branches. Branch #3324 overlaps only `foldingRules` in `cache.go`; no dependency and no edit needed there.

## Design
`Store.run` retains latest fully folded progress, saved version and last successful checkpoint time. Initial recovery checkpoints before first publication. Tail progress publishes immediately; version advancement, including unsupported/no-public-change entries, is dirty. A dirty timer expires at last-success + 30 seconds; later publications cannot move it. Already-expired dirty progress checkpoints immediately. Successful writes clear dirty state and start a new interval. No timer/replacement is needed while unchanged.
Compute activity from all private fold items. Only a true-to-false transition forces a checkpoint; further inactive publications use the interval. Retirement flushes dirty complete progress only, preserving errors. `finishRun` continues final validation/replacement of every lifetime participant before certification, without folding unloaded history.
Private clock/timer factory hooks in `Store` allow deterministic tests; production uses `time.Now` and stoppable `time.NewTimer`. Public APIs, cache schema and folding rules remain unchanged.

## Concurrency model
One worker owns fold/progress/checkpoint state and selects timer, cancellation and tail chunks. After initial recovery, one tail-reader goroutine transfers at most one chunk and waits for acknowledgement before returning its callback. It never accesses the fold. All channel waits honor worker cancellation. On every exit the worker cancels and joins the reader, stops its timer, then performs retirement bookkeeping. No I/O or joins hold the store mutex; other conversations remain independent.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Continuous dirty publications; no later append; unchanged/overdue progress | `TestStoreCheckpointSchedule` |
| Independent conversation deadlines | `TestStoreCheckpointIsolation` |
| Active-to-idle then already-inactive; repeated intervals | `TestStoreCheckpointLifecycle` |
| Unload/reload and clean shutdown/reopen | `TestStoreCheckpointLifecycle`, existing `TestStoreCacheEpochs` |
| Unclean stop with cache behind; pending child/report evidence across saved boundary | `TestStoreCheckpointUncleanContinuation` |
| Initial/scheduled/lifecycle failure and retry; unloaded final validation | `TestStoreCheckpointFailures`, existing `TestStoreCacheShutdown`, `TestStoreCacheFinalCertificate` |
| Cancellation during folding/retirement, repeated/concurrent joins | existing `TestStoreLifecycle`, `TestStoreCacheInterrupted` |

## Error handling
Only successful complete folds advance progress. Failed initial or scheduled checkpoint returns `ErrPersistence` internally, withdraws usability with generic `ErrUnavailable`, and prevents certification. Lifecycle results retain `ErrPersistence`; incomplete recovery remains unavailable. Reader/fold failure never checkpoints partial progress. Existing containment/no-follow and atomic replacement paths remain mandatory for all writes.

## Testing strategy
Write scheduling tests first, observe cadence failure, then implement. Fake time controls stoppable deadlines; `Store.replace` records successful actual cache replacements and injects errors. Synchronize publications/timer installation/replacement completion rather than sleeping to assert absence. Compare unclean reopen and subsequent tails against fresh full-history replay, including revisions, consumed version and `LastShownVersion`, and assert epoch rotation separately.
Run `go test -race ./internal/thread ./internal/history`, `go vet ./...`, `go build -o /tmp/builder-3341/pyry ./cmd/pyry`. The verifier owns `make check` (the ticket requests it; the builder role forbids duplicating that full gate). No live-Claude evidence is required.

## Open questions
None. Preserve final unloaded cache replacement even when progress is unchanged; it validates the entire run before certification.

## Documentation handoff
Pending for documentation stage: update `docs/knowledge/features/thread-package.md`, “Cache and epochs”, and `docs/knowledge/features/thread-package-background-store.md`, “Loading and replay readiness” and “Isolation and lifecycle”. Replace checkpoint-before-every-publication with initial checkpoint before usability; tail publication can exceed saved version; dirty progress has a non-postponable 30-second deadline, no unchanged periodic rewrite; active-to-idle, unload and shutdown flush complete dirty progress. Preserve full-history reconstruction, caller-owned final draining, unloaded final validation and clean-run epoch rules.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: `Load` validates IDs; `Fold.Feed` consumes raw history; `recoveryCandidate` compares cached items with fresh replay and never seeds continuation.
- Tokens/cryptography: unchanged `randomToken` uses 128-bit `crypto/rand` epochs and run IDs; scheduling introduces no credentials or new token use.
- File operations: retain `writeCacheFile`'s contained directory recheck, no-follow regular reads, synced/closed `0600` temporary files and atomic replacement. Timed writes cannot bypass this path.
- Subprocesses/network: no new subprocess, socket or external input boundary; only existing bounded history chunks cross the internal channel.
- Errors/logs: snapshot failure stays generic; raw injected persistence errors are discarded by `writeCacheFile`; no new payload/path logging.
- Concurrency: SHOULD FIX during implementation: cancellation must release both chunk send and acknowledgement waits and join the reader before worker completion; timer ownership remains on the worker to prevent concurrent fold/cache access.
- Threat model: losing unsaved cache progress is intentional and recoverable from history. Existing history durability and hostile filesystem race model remain unchanged; no new security scope is deferred.
**Reviewer:** builder (self-review)
**Date:** 2026-10-14

## Revisions
- 2026-10-14: `TestStoreCheckpointCancelledFold` exposed cancellation during the optional fold barrier losing the preceding complete dirty publication. Preserve a cancellation result from `feed` so retirement flushes only that completed progress; other failed folds remain unavailable. Add this race test and `TestStoreCheckpointInitialReadiness` to the lifecycle/readiness proofs.
