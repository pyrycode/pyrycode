# Idle conversation last-shown metadata

## Files read
- `cmd/pyry/daemon_thread_relay.go` — `installDaemonThreadRelay`: registry authorization and loaded metadata callback.
- `cmd/pyry/thread_shadow.go` — `discover`, `follow`: discovery warms history progress and idle workers retire.
- `internal/thread/store.go` — `Status`, `Unload`, `run`: metadata-only readiness, joined retirement and retained progress.
- `internal/thread/cache.go` — `recoveryCandidate`, `readCacheFile`, `validEvidence`: current evidence validation and contained no-follow reads.
- `internal/history/log.go` — `LatestEntryID`, `load`, `resolveDir`: shared append cursor, cold segment recovery and containment.
- `internal/history/latest_entry_test.go` — `TestLatestEntryIDRecovery`: segment counters and small segmented fixtures.
- `internal/thread/certificate_test.go` — `TestStoreCertificateWrites`: clean retirement evidence.
- `cmd/pyry/thread_shadow_test.go` — `testShadowWait`, `testShadowAppend`: deterministic fold fixtures.
- `docs/knowledge/features/thread-package.md` — Shadow lifecycle and evidence; Cache and epochs: metadata is not replay/checksum or catch-up continuity proof; missing empty evidence must not become authoritative zero.
- `docs/knowledge/features/history-package-watermarks.md` — warm append cursor is authoritative without segment reads; cold lookup otherwise loads.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, ADR 042 read marks and `docs/protocol-mobile.md` security model: conventions, test evidence and trust boundaries.

## Context
Idle unloading currently omits the thread unread watermark. A clean certificate can supply it only when independently established raw progress equals its consumed version. This is metadata eligibility, not a new epoch or replay contract; no decision record is needed.
One deliverable, estimated 550 written lines including tests/plan, zero new exported types, one updated consumer, three acceptance criteria, fewer than ten rejection groups. Production spans three files because history needs a nonloading cursor API. All five sizing limits remain below their boundaries.
Overlap: #3391 edits the loaded callback; retain its Status migration. The remote branch has no additional required production change against current main; no dependency.

## Design
- `history.Store.CachedLatestEntryID(id) (uint64, bool, error)` validates ID and directory containment and returns the existing loaded append cursor. Missing/cold progress returns unknown; it never calls `load`, creates directories or reads segments. Known empty progress returns present zero.
- `thread.Store.UnloadedLastShownVersion(id) (uint64, bool)` rejects any worker (including retiring), closed store and unknown/failed history progress. Under the store lock, read at most one certificate with `recoveryCandidate` bounded by H; require a validated nonempty epoch and exactly V = H. Return its watermark, including zero. Errors become absence. This method never uses cached consumed progress as H.
- `installDaemonThreadRelay` checks registry membership first, reads `Status`, returns usable loaded metadata directly, and invokes the fallback only for not-loaded state. Rebuilding/unavailable views stay absent. No item snapshot, query, worker start or write occurs.

## Concurrency model
No new goroutines. The fallback holds thread's mutex through its short metadata read, preventing load/retirement admission from crossing eligibility checks. Lock order is thread then history; history never calls thread while holding its lock. Existing workers checkpoint outside thread's mutex and retain a worker entry until joined retirement, so cannot overlap eligible fallback. History's mutex serializes cursor reads with appends; concurrent lookup is an observation at that cursor boundary.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Loaded to joined unloaded, same ID | `TestDaemonThreadLastShownLifecycle` |
| Later hidden append invalidates; reload/fold restores; repeated unload | `TestDaemonThreadLastShownLifecycle` |
| Warm/cold store reopen and valid empty progress | `TestDaemonThreadLastShownCertificates`, `TestCachedLatestEntryID` |
| Rebuilding/unavailable cannot use old clean evidence | `TestDaemonThreadLastShownUnavailable` |
| Registry removal and unknown IDs | `TestDaemonThreadLastShownCertificates` |
| Concurrent append and metadata read | `TestCachedLatestEntryIDConcurrent` |

## Error handling
Reuse certificate schema/rules, epoch, evidence presence and checksum-shape validation. Reject dirty, malformed, unsafe/unreadable leaves, directory/history failures and either version mismatch. Missing/invalid candidates have no epoch, distinguishing them from valid zero evidence. Return only absence, without logging disk content or host paths.

## Testing strategy
Write regressions first and observe failures. Installed-callback tests generate real clean certificates, mutate individual predicates, cover warm/cold cursors and zero, poison segment paths to prove successful reads do not need them, and count fold entry to prove no workers. Loaded repeated reads must allocate zero and ignore unusable certificate paths. History tests use existing segment-open/byte counters across cold and warm reads and append races. Thread tests reject active/retiring/closed workers and count writes/copies through existing hooks. Validate `go test -race ./internal/thread ./internal/history ./cmd/pyry ./internal/relay`, `go vet ./...`, and `go build -o /tmp/builder-3330/pyry ./cmd/pyry`. The dispatcher owns `make check` and the full-module race gate per builder instructions.

## Open questions
None.

## Documentation handoff
Pending documentation stage: update `docs/knowledge/features/thread-package.md`, **Shadow lifecycle and evidence** and **Cache and epochs**, and `docs/protocol-mobile.md`, **`conversations`** and the `conversation_updated` catalog row. Describe loaded status versus unloaded clean-certificate readings, exact V = H eligibility, authoritative zero and omission for stale/invalid/unknown progress; preserve read-mark and capability meanings.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] Registry authorization in `installDaemonThreadRelay` precedes certificate access. `recoveryCandidate` validates disk metadata; no certificate grants registry authority or catch-up continuity.
- [Tokens/secrets] No credentials or tokens are created or exposed. Epoch is validated metadata, never authentication.
- [File operations] Reuse `readCacheFile` fixed leaf, ID validation, `LogDir` containment, no-follow/nonblocking open and pre/post regular-file checks. No writes or changed modes.
- [Subprocesses] No subprocess path or shell input is added.
- [Cryptography] Existing checksum syntax is checked, without claiming replay equality or introducing cryptographic primitives.
- [Network/I/O] No new socket reads or transport framing. One certificate open at most, independent of history size; segment reads and cold initialization are forbidden.
- [Errors/logs] File failures return absence and no paths/payloads reach clients or new logs.
- [Concurrency] Thread-to-history lock order; worker presence blocks fallback during retirement and load. No goroutines need new shutdown paths.
- [Threat model] Existing authenticated encrypted relay projection is unchanged; only numeric metadata crosses it. Registry lookup and contained file access address caller-selected IDs. Host-file replacement cannot establish replay/checksum continuity, which remains the ordinary load path's responsibility.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15

## Revisions
- 2026-10-15: Added `TestDaemonThreadLastShownIO`, a Linux inotify observation of the installed callback, to count actual certificate opens and detect segment opens/writes across two history sizes, warm/cold cursors and registry removal. Portable tests still cover all eligibility predicates and loaded allocation behavior. Estimated total remains below 700 written lines; no production interface changes.
