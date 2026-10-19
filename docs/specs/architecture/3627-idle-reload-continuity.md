# Validated idle reload continuity

## Files read

- `internal/thread/store.go` → `start`, `run`, `publish`, `Unload`: worker replacement, retirement evidence and atomic publication.
- `internal/thread/store_resume.go` → `resumeCheckpoint`: clean checkpoint and history-boundary validation authorize epoch reuse.
- `internal/thread/publication.go` → `restore`, `capture`: saved public slots and recipes produce applicable suffix revisions and suppression resets.
- `internal/thread/queries.go`, `query_tree.go`, `compact_intervals.go` → `newPublicationIndex`, `advance`, `compactInterval`: immutable compact roots, dirty ancestors, coverage and root-removal evidence.
- `internal/thread/cache.go` → `progressRecord`: scalar clean-retirement evidence survives memory release.
- `internal/thread/compact_intervals_test.go` → `TestCompactCatchUpReload`: older lost intervals must still reset.
- `cmd/pyry/thread_shadow.go` → `follow`: deliberate idle unload preserves the publisher cursor; genuine gaps retire peers.
- `cmd/pyry/daemon_thread_demand_test.go` → `TestDaemonThreadDemandIdleReadKeepsConnection`, `TestDaemonThreadDemandWireAfterIdle`: authenticated idle lifecycle harness.
- `docs/knowledge/features/thread-package-background-store-replay.md` § Loading and replay readiness: validation and failure ordering must remain intact.
- `docs/knowledge/features/thread-package-background-store-queries.md` § Bounded queries: compact evidence cannot be inferred from epoch equality.
- `docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence.md` § Shadow lifecycle and evidence: authenticated catch-up establishes consumer state explicitly.

## Context

A clean idle unload releases the worker but retains the daemon publication cursor.
Checkpoint restore already reconstructs the prior public slots, yet the first
publication discards their suffix changes and initializes compact coverage too
late. Appending durable history therefore retires healthy thread connections.
This is one behavior repair including its Store and authenticated wire proof.
No decision record is needed. No overlapping feature branch touches planned files.

## Design

Capture the prior scalar `progressRecord` when starting a replacement worker.
Only a validated clean checkpoint whose epoch, version, checksum and shown
watermark match successful retirement may seed continuity. Keep rebuilding state
until recovery completes. Reconstruct a private query index at that saved fold
boundary and advance it with the captured suffix through the normal incremental
path. Retain the initial suffix observation just as later observations are
retained, including suppression resets. Missing history IDs invalidate complete
and compact intervals across that gap. Compact removal remains projection scoped.
Never restore old retained ranges or lower compact coverage before the saved
boundary. Fresh Stores, rejected checkpoints and failed retirement get ordinary
baseline initialization. Existing bounded retention and query/publication gates
remain unchanged; no public API or consumer call-site changes.

## Concurrency model

The existing conversation worker owns replay, capture and index preparation.
`Store.mu` protects worker publication and scalar retirement records. Query
snapshots remain rebuilding until the prepared publication installs atomically.
No new goroutines; existing cancellation and joined unload/shutdown own exit.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Clean unload, durable suffix, same epoch reload; repeat cycle | `TestStoreIdleReloadContinuity`, `TestDaemonThreadIdleAppendKeepsConnections` |
| Existing row revision and nested compact root advance | `TestStoreIdleReloadContinuity` |
| Missing/mismatched retirement evidence, rejected/dirty checkpoint, epoch replacement | `TestStoreIdleReloadRejectsContinuity` |
| History gap, published row suppression, compact root removal | `TestStoreIdleReloadRejectsContinuity` |
| Older interval lost at reload | existing `TestCompactCatchUpReload` |
| Unchanged idle query and explicit initialization | existing `TestDaemonThreadDemandIdleReadKeepsConnection`, new wire regression |

## Error handling

Checkpoint/history/persistence failures retain existing unavailable or new-epoch
fallback behavior. No usable partial replay escapes. An invalid suffix returns
baseline/reset with no applicable output; publisher retirement remains unchanged.

## Testing strategy

Write regressions first and retain unchanged-base failure output in scratch logs,
then repaired output and counts in the PR. Store tests apply complete changes to
the old baseline and compare full replay; compact tests compare changed roots
and headers without unrelated inventory. Negative controls target this reload
boundary. Wire tests use real history, Store, follower and authenticated session
manager, finish catch-up, observe actual idle unload, append, and verify live
updates on the original complete/compact peers plus another usable peer.
Run affected race tests on Linux, then `go vet ./...` and `go build ./cmd/pyry`.
Full acceptance gates belong to dispatcher/verifier; no live Claude is needed.

## Open questions

None. Estimated total written work 650–750 lines; zero new exported types,
zero simultaneous consumer updates, three acceptance criteria and fewer than
ten new reject branches. The fix and integration proof meet the verification floor.

## Documentation handoff

Pending for documentation stage: update
`docs/knowledge/features/thread-package-background-store-replay.md`, “Loading and
replay readiness”, and
`docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence.md`, “Shadow
lifecycle and evidence”: replace unconditional same-epoch reload discontinuity
wording with the validated clean-checkpoint/suffix continuity contract above;
retain genuine-gap retirement and explicit reconnect catch-up.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `resumeCheckpoint` remains the authority for disk graph, owner, schema and history checksum validation. Successful in-memory retirement must match it before continuity is admitted; epoch equality alone is insufficient.
- Tokens/secrets/cryptography: no new credentials or primitives; existing epoch generation and authenticated Noise session handling remain unchanged.
- File operations: reuse contained checkpoint/history readers, dirty-before-publication and joined clean certification. No new file operations or path inputs.
- Subprocesses: none introduced.
- Network/I/O: existing authenticated query and bounded projection encoders remain the boundary. No new network parser or unbounded inventory output.
- Errors/logs: preserve generic unavailable/reset responses; do not log history payloads or checkpoint contents.
- Concurrency: seed the index under the existing mutex while state remains rebuilding; worker identity and cancellation guards prevent stale publication. Unload still clears all item, query and range memory.
- Threat model: preserve fail-closed recovery and consumer retirement before invalid-baseline output. No authentication, authorization, transport or key lifecycle changes.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-19

## Revisions

- 2026-10-19: `initialPublicationIndex` shares the existing initialization path between saved-boundary reconstruction and ordinary first publication. `run` receives the prior scalar record captured by `start`; the query index remains inaccessible to usable queries until replay completes. Empty reloads retain no zero-width batch. This implements the planned contract without new worker state or public APIs.
- Existing assertions in `internal/thread/observations_store_test.go` → `TestStoreObservationRecovery` and `cmd/pyry/daemon_thread_relay_test.go` → `TestDaemonThreadContinuity` now require validated suffix continuity. The latter separately keeps retirement coverage for a cursor older than the saved reload boundary; `TestDaemonThreadIdleCommit` retains the held-delivery boundary proof.
- Unchanged production `66430593`, with Store regressions at `d1eb23e9`, executed 14 test events: 12 passed, two failed, zero skipped. `TestStoreIdleReloadContinuity` and `TestStoreIdleReloadNestedCompact` failed because the restored suffix required a baseline; all eleven negative cases passed. The first wire fixture attempt used an unpaired peer and failed before the append; its log is retained separately. Corrected test revision `247ae815` executed `TestDaemonThreadIdleAppendKeepsConnections` once and failed after actual idle unload and durable append: both original peers were retired (`after=[]`). Baseline logs are retained in `/tmp/builder-3627/base-store.log`, `base-wire.log` and `base-wire-paired.log`; repaired results are recorded in the PR.
- Verification remains incomplete: Linux `go vet ./...` and the daemon build passed on implementation `26014578`. The explicit affected Store/compact and daemon wire race selections waited more than nine minutes for shared admission, executed zero tests, and were terminated cleanly before the builder deadline. Their logs are `/tmp/builder-3627/fixed-store-exact.log` and `fixed-wire.log`; the earlier wildcard-selection refusal is in `fixed-store.log`. Both repaired selections, including `TestDaemonThreadDemandIdleReadKeepsConnection`, must run and pass before builder completion. No passing repaired race proof is claimed. The nested-root scenario is specifically covered by `TestStoreIdleReloadNestedCompact` in addition to the repeated message-revision test named in the transition table.
- 2026-10-19 resumed verification at `01de1e42`: all five authenticated daemon race tests passed, including the append regression and unchanged idle-read proof. The Store selection executed 16 test events, with 14 passed and two failed, zero skipped (`/tmp/builder-3627/resumed-store.log`). Both failures exposed fixture errors beyond the original baseline-reset assertions: a user message ended the assistant text run before the second reload, and a tool result without `is_error` was a neutral fact. Use a queued acceptance between text suffixes to retain the same assistant row across cycles, and an explicit successful nested result to exercise compact ancestor advancement. The compact assertion also applies returned roots to the old baseline and compares every resulting row with replay. Production design is unchanged; corrected focused Store proof remains required.
