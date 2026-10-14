# Compact thread recovery evidence

## Files read

- `internal/thread/cache.go` → `cacheRecord`, `progressRecord`, `recoveryCandidate`, `finishRun`: recovery eligibility and protected cache I/O.
- `internal/thread/store.go` → `run`, `checkpoint`, `Unload`, `Shutdown`: full replay, consumed publication boundaries and bounded flushing.
- `internal/thread/cache_test.go` → cache epochs, continuation, warm truncation and security tests: existing lifecycle and containment proofs.
- `internal/thread/checkpoint_test.go` → checkpoint lifecycle, unclean continuation and cancelled-fold tests: preserve cadence and retirement boundaries.
- `internal/thread/store_test.go` → `testStoreReader`, `testStoreEqual`: controlled partial/incomplete feeds and fresh-fold parity.
- `internal/thread/sends_queue_handle_test.go` → `TestStoreQueueHandleCacheRules`: rule rejection must remain independent of content mismatch.
- `internal/history/log.go` → `Entry`, `Page`, `LatestEntryID`: stored-entry fields and readable bound versus warm append cursor.
- `internal/history/forward.go` → `Walk`, `deliver`, `Tail`: serialized callbacks and successful-consumption cursor advancement.
- `docs/knowledge/features/thread-package.md` § Cache and epochs: public items cannot restore private joins or last-shown state.
- `docs/knowledge/features/thread-package-background-store.md` § Loading and replay readiness, Snapshots and retry: replay completion, unavailable views, cancellation and caller-owned draining.
- `docs/knowledge/features/history-package.md` § Directory resolution: exact-directory equality and re-resolution preserve containment.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: contract comments, offline proofs and scoped checks.
- `docs/knowledge/decisions/045-thread-index-over-history.md`: resumable folds and lifecycle changes remain future work.

## Context

Whole-item checkpoints duplicate conversation content although recovery always
replays history. Replace them with constant-size evidence for epoch reuse. This
implements the compact-evidence portion of ADR 045; no new decision record is
needed. No overlapping feature branches touch the planned files.

Sizing: one deliverable; approximately 560 written lines including tests and
this plan, zero new exported types, one internal checkpoint caller, four
acceptance criteria and no new lifecycle reject branches beyond evidence
validation. All five limits remain within the builder boundaries.

## Design

Increment `cacheSchema` to 2 without changing `foldingRules`. `cacheRecord`
omits items and holds schema/rules, epoch, version V, last-shown watermark,
checksum, complete flag and existing recovery references. At V zero checksum
is empty and watermark zero; otherwise checksum is exactly 64 lowercase hex
characters and watermark is at most V. This record remains below 1024 bytes.

`entryChecksum(history.Entry) string` hashes ID, type, timestamp and stored
payload bytes using SHA-256. Use big-endian fixed-width integers for ID,
timestamp seconds/nanoseconds and string/byte lengths; type and payload are
length-prefixed. Timestamp encoding identifies its instant independently of
timezone/monotonic state. Segment formatting is excluded; payload bytes remain
opaque. This checksum identifies entry V, not the whole prefix or fold state.

Successful replay/tail feeds update the checksum of the last consumed entry.
Only complete publication progress carries that checksum and reconstructed
last-shown watermark into checkpoint/retirement records. Partial failed feeds
cannot advance either. Preserve current flush scheduling and all I/O helpers.

`recoveryCandidate` retains existing run/marker eligibility and readable bound,
adding compact-field validation. Replay through V must reach exactly V and
match checksum and reconstructed watermark before retaining the epoch. Replay
then continues through the readable bound, preserving its completion guard.
Rejected metadata rotates the epoch; no cached state seeds the fold. Schema 1
rotates once, with subsequent clean schema-2 opens eligible for reuse.
`finishRun` compares all compact evidence with final consumed progress before
certifying the shared run. Keep `sameItems` for observation tests.

## Concurrency model

No new goroutines or locks. The existing worker exclusively owns fold and
checksum state; its acknowledged tail goroutine exits on cancellation and is
joined. Store records remain protected by `mu`, run initialization by `initMu`.
Caller-owned producers must drain and tail catch-up must finish before shutdown.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Clean reopen, empty history and repeated clean reopen after schema migration | `TestStoreCacheCompact`, `TestStoreCacheLegacy` |
| Unload/reload with same conversation ID | `TestStoreCacheEpochs`, `TestStoreCheckpointLifecycle` |
| Warm store loses a tail, then reopens/reloads/retries | `TestStoreCacheWarmHistoryTruncation` |
| Lost tail is replaced at the same IDs | `TestStoreCacheReplacedTail` |
| Context stops a loaded worker with unsaved progress | `TestStoreCheckpointUncleanContinuation` |
| Cancelled or partially failed feed followed by retirement | `TestStoreCheckpointCancelledFold`, `TestStoreCachePartialEvidence` |
| Clean replay cut point followed by further tail entries | `TestStoreCacheContinuation` |
| Incomplete replay or failed cache/marker/certificate write | `TestStoreCacheInterrupted`, `TestStoreCacheSecurity`, `TestStoreCacheFinalCertificate` |

## Error handling

Missing, malformed, incompatible, ahead or mismatched evidence rebuilds with a
new epoch. History read/replay failure or protected cache I/O failure leaves the
view unavailable and cannot certify a clean run. Keep generic sentinels and
atomic-write failure behavior. History bytes are never changed by this code.

## Testing strategy

Write regression assertions first and observe failures. Check actual cache JSON
for required scalar fields, no items and size independent of large item content.
Pin checksum encoding and sensitivity to each input, including ambiguous byte
boundaries. Expand invalid-field and legacy-schema recovery tests; independently
exercise equal-version tail replacement, partial feeds and final certification.
Adapt rule/cadence assertions and compare items, revisions and watermark against
fresh folding at every continuation cut point. Run `go test -race
./internal/thread ./internal/history`, `go vet ./...`, and `go build ./cmd/pyry`.
The dispatcher/verifier owns `make check` and its full-module gate.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage:
- `docs/knowledge/features/thread-package.md`, “Cache and epochs”: compact fields and checksum encoding, checksum-at-V validation, old-schema rotation, continued full replay with history-derived last-shown reconstruction.
- `docs/knowledge/features/thread-package-background-store.md`, “Loading and replay readiness” and “Snapshots and retry”: replace item-comparison recovery wording with checksum validation.
- In those sections retain shared-run lifecycle, bounded checkpoints, caller-owned draining and separate change-range continuity rules until #3383 changes lifecycle. Describe ADR 045 resumable checkpoints as future work.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] Cache data remains untrusted at `recoveryCandidate`: require schema/rules, token/ID shape, complete evidence, readable bound and exact-V replay validation. It grants only epoch continuity, never content or authorization.
- [Tokens] `randomToken` retains crypto/rand 128-bit tokens. Epoch/run values are identifiers, not credentials; no new logs or exposure.
- [File operations] `readCacheFile`/`writeCacheFile` retain canonical IDs, `cacheLeaf`, exact `LogDir` containment, no-follow regular-file reads, 0600 temporary files, sync/close and atomic rename. No paths come from the checksum.
- [Subprocesses] No subprocess or environment changes.
- [Cryptography] Stdlib SHA-256 with unambiguous framing detects changed entry V. Digests are public integrity evidence, so comparisons need no secret timing protection; this is not authentication against a writer controlling both history and cache.
- [Network and I/O] No network reads; existing history chunk bounds and cache decoder stay unchanged. New writes contain only fixed-size scalar evidence.
- [Errors/logs] Generic persistence/unavailable errors remain; no payload, path or digest logging is added.
- [Concurrency] Checksum advances only after successful feeds and joins the version/watermark publication boundary; `finishRun` verifies final evidence. Interrupted writes leave the shared run incomplete.
- [Threat model] OUT OF SCOPE: directory-sync dirty marking, per-conversation clean flags and legacy cleanup belong to #3383. The shared lifecycle remains the accepted eligibility boundary for this slice.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14
