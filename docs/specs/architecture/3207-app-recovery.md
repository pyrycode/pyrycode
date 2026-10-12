# Published-service reconciliation and recovery (#3207)

## Files read
- `internal/apps/owner.go` → `NewOwner`, `Start`, `launch`, `Stop`, `Shutdown`, `cleanup`: admission, spawn acknowledgement and owned-group cleanup.
- `internal/apps/health.go` → `supervise`, `confirmRunning`, `usableLocked`: bounded fresh health and private generation identity.
- `internal/apps/lifecycle.go` → `Subscribe`, `CommitObservation`, `intentLocked`: synchronous notifications and conditional observations.
- `internal/apps/registry.go` → `List`, `changeLocked`: detached snapshots and persistence-before-notification.
- `internal/apps/owner_test.go`, `health_test.go` and execution/cleanup tests: controlled child helpers and supported Node proof.
- `docs/knowledge/features/apps-package.md` § Lifecycle and restart state, Concurrency and persistence: registry-before-owner locks, callbacks cannot re-enter, reopened registry takes exclusive ownership.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Runtime, roots and service conventions; Registration, observed state and safe publication: normative timing, intent and data guarantees.

## Context
Complete callable supervision; daemon wiring remains #3204. Available committed releases recover independently; persisted running never supplies readiness. Candidate manifests remain inactive. Native parentage is #3122 → #3203 → #3207; existing needs-human:sizing permits building this grandchild despite the estimate exceeding 800 lines. Sketch: roughly 900 written lines including tests/plan, zero exported types/interfaces, zero consumer updates, five criteria, at most ten recovery rejection branches. A hypothetical split would separate reconciliation/cancellation from recovery timing and restart proof, but the split-depth gate prohibits it. No overlapping feature branch touches the planned owner/health files.

## Design
Subscribe before the initial List. The callback only sends a nonblocking coalesced wake signal; it holds no owner lock and calls no methods. A single background reconciler lists the latest registry snapshot on every wake. Validate its host revision under the registry lock before applying it; discard stale snapshots and wake again. Thus notifications need no unbounded event queue, and a removal cannot be resurrected by an older snapshot.

Keep generation admission under registry then owner locks, shared by explicit starts and automatic reconciliation. Automatic launches require committed available intent and active release and conditionally observe starting without writing desired intent. Launch preparation/spawn remains asynchronous and secure. Explicit Start durably resumes stopped intent, otherwise coalesces; a completed failed attempt during scheduled backoff returns its failure/unavailability without bypassing the schedule. Unpublished starts commit available intent but launch nothing. Healthy metadata-only updates retain their generation; startup revision changes retain conditional rejection semantics.

Track per-app next retry delay and deadline privately. Completed launch retirement (including port release, group cleanup and child wait) schedules 1, 2, 4, 8, 16, then 30 seconds indefinitely. A reconciler-owned timer wakes at the earliest deadline; it never performs launch or cleanup synchronously. Healthy running for 60 seconds resets the next delay to one second. Package-private current-time injection allows deterministic deadline/reset tests while the production clock remains time.Now. No private address/generation is persisted.

Stop/removal/release changes cancel incompatible generations immediately when reconciled. Stop suppresses automatic work even if intent persistence fails; explicit Start may resume it. Shutdown closes admission, detaches subscription outside owner locks, cancels the reconciler and generations, then joins all workers without changing desired intent. Failure observations recheck generation, available intent and release under registry then owner locks and conditionally update the latest matching record, preserving newer metadata.

## Concurrency model
One cancellable reconciler/timer plus independent launch/health workers per app and their existing child wait goroutines. Notifications only wake; workers signal completion after cleanup. The registry mutex precedes the owner mutex everywhere they are combined. Shutdown joins the reconciler before final generation joins. No retry callback goroutines survive shutdown.

## State transitions and identity reuse
| Event | Race test |
|---|---|
| Initial snapshot races with stop/removal/publication; callback non-reentry | TestOwnerReconcileSnapshot |
| Failed launch, crash, sustained health failure and collision retry after cleanup | TestOwnerRecovery |
| Duplicate Start and metadata notifications during backoff/running | TestOwnerRecovery, TestOwnerReconcileMetadata |
| Stale probe/target/timer and repeated app/release/port | TestOwnerTargetReuse, TestOwnerRecovery |
| Stop during startup/backoff, removal, stopped-write failure | TestOwnerReconcileCancellation, TestOwnerTargetRetirement |
| Shutdown and reopen exclusive root ownership/data retention | TestOwnerReopenRecovery |
| Independent sibling launch/stop/recovery | TestOwnerRecoveryIsolation |
| Conditional lifecycle write failure cleans candidates | TestOwnerHealthPersistence, TestOwnerRecoveryPersistence |
| Healthy reset and production delay progression | TestOwnerRetryTimings |

## Error handling
Reuse safe static unavailable/health/exit errors. No target before successful conditional running commit. Retain asynchronous lifecycle-write/cleanup errors for Shutdown. Retire candidates regardless of persistence errors. Automatic admission-write failures also enter backoff instead of spinning. Cancellation removes retry eligibility; late completion cannot replace newer ownership.

## Testing strategy
Write focused recovery tests before implementation and observe their expected failures. Reuse controlled real-child fixtures; inject only retry/reset time and wake the reconciler. Run go test -race ./internal/apps, go vet ./..., and go build ./cmd/pyry (output outside the tree). Actually execute TestOwnerSupportedNode using supported Node 24. Full module and full Node/SQLite/channel integration are verifier/#3204 gates.

## Open questions
None. The active committed release is the only execution source; startup metadata revision changes remain conservatively rejected and retried.

## Documentation handoff
Pending for documentation stage: update `docs/hosted-apps.md` § Runtime and layout, Startup and health, Registration and observed state, and `docs/knowledge/features/apps-package.md` § Lifecycle and restart state. State automatic owner-construction/snapshot/subscription reconciliation, retry delays and retirement/cleanup timing, 60-second reset, stopped/removal cancellation, fresh-health restart/data retention, shutdown intent preservation and sibling independence. Retain exact runtime/environment allowlist, private endpoint/generation ownership, health/stop timings, conditional commits and safe state/error guarantees. Distinguish callable supervision from #3204 daemon wiring, #3125 publishing/rollback and #3123 resource capability activation.

## Security review
**Verdict:** PASS
- Trust boundaries: prepareRelease still validates committed build/identity; probeHealth validates fresh bounded matching health. Candidate edits cannot activate code.
- Tokens: launch retains the exact eight-variable environment; no credentials or new tokens.
- File operations: reuse no-follow secure data/build preparation, 0700 directories/0600 database and atomic registry writes; no new file protocol.
- Subprocesses: fixed selected Node argv, fresh process groups/reservations, five-second graceful/forced cleanup and child wait before retry.
- Cryptography: no cryptographic changes; generation pointers establish private ownership, not secret authentication.
- Network/I/O: existing one-second bounded 4096-byte health replies and disabled redirects/proxies/reuse remain enforced.
- Errors/logs: static safe registry messages; detailed diagnostics remain local owner errors.
- Concurrency: SHOULD FIX: callbacks must never acquire owner mutex because observations notify while holding it. Use nonblocking wake only; join all reconciliation/launch workers.
- Threat model: trusted local app execution remains the contract; encrypted resource authorization belongs to #3123, publication immutability/journals to #3125.
**Reviewer:** builder (self-review)
**Date:** 2026-10-12

## Revisions
- 2026-10-12: completed retirement conditionally observes stopped against the latest record when stopped/unpublished intent still applies, preserving metadata. Existing stopped-intent tests now allow that post-cleanup observation revision. Duplicate starts during failure cleanup wait for retirement without marking it explicitly retired, so they cannot suppress the failure's backoff. A failed starting write retains the admitted record for release identity checks rather than accepting an empty result as a release change.
- 2026-10-12: retirement also checks the confirmed-running duration for the retry reset, covering a crash at the 60-second boundary before the next successful probe (`TestOwnerRetryResetOnExit`). Existing active-release-change tests permit subsequent reconciliation observations while still requiring committed metadata preservation; test build preparation precedes available publication.
