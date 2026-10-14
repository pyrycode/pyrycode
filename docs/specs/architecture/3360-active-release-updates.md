# Active-release publication and verified recovery

## Files read
- `internal/apps/publication.go`: `Publish`, admission, conditional mutations and failure completion.
- `internal/apps/publication_process.go`: owned launch, atomic activation and removal evidence.
- `internal/apps/health.go`: `confirmRunning`, `probeHealth`, generation-bound targets and bodies.
- `internal/apps/owner.go`, `internal/apps/reconcile.go`: Stop/Shutdown joins, reconciliation suppression and lock order.
- `internal/apps/runtime.go`, `internal/apps/candidate_record.go`: fresh retained selection and numeric progression; recorded historical retries bypass progression.
- `internal/apps/backups.go`, package overview § Persistent-data restoration: restoration needs writer quiescence and does not establish previous-release health.
- `docs/knowledge/features/apps-package-testing.md` § Publication fixtures: inspect injected durable decisions; healthy fixtures need production deadlines.
- Existing publication, retirement and storage-durability tests: process doubles and uncertainty evidence.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Registration, observed state and safe publication: authoritative cutover contract.

## Context
Extend synchronous first publication to updates without sacrificing the healthy predecessor during checks. A failed candidate always returns failure, independently of recovery health. Startup recovery belongs to #3263; no new decision record is needed.

Sizing: approximately 1200 written lines, no new exported types, fewer than ten consumer updates, five acceptance criteria, more than ten reject/failure branches. The issue already carries `needs-human:sizing` and records the grandchild exception (#3125 → #3262 → #3360), so build through the overage. Candidate/activation and verified recovery would be the natural seams absent that exception. No touching feature-branch overlap was found after fetching origin.

## Design
Keep one `publication` per app. Capture predecessor record/generation at admission and retain available intent. Checked preparation preserves running state; once checked, reject historical downgrade and return a healthy identical-active no-op without cutover. Set pending/starting conditionally, then retire/cancel and positively join the predecessor before snapshot. Freshly verify backup and durably record previous/candidate/backup before any data preparation.

Reuse candidate migration and private launch. Matching health atomically commits candidate title/release/running and journal decision. Durable activation is authoritative even if journal clearing fails. An uncertain registry decision or unconfirmed writer retirement bars restoration and targets.

Update failure completion separately from first-publication completion. Before retirement retain a usable predecessor and safe error. After retirement, join candidate writers; if mutation was possible, restore complete verified data. Restart the freshly verified previous immutable build privately only while still registered/available and owned. Previous health atomically clears recovery evidence and exposes running with a static candidate-failure error; it never marks candidate activation successful. Failed health/restoration/finalization keeps failed state and suppresses reconciliation. Stop/removal restores data without restart; shutdown joins restoration or retains evidence.

## Concurrency model
Registry then owner is the sole nested lock order. Disk/build/process work and worker joins run outside both locks. Ownership suppresses starts/reconciliation throughout publication/recovery. Existing supervisors join leaders and groups; recovery uses owner lifetime rather than the cancelled candidate context, with final currentness checks before spawn and health commit. No new unjoined worker family.

## State transitions and identity reuse
| Event | Race-enabled proof |
|---|---|
| Same-ID activation and subsequent identical retry | `TestUpdateSuccessAndRetry` |
| Parked build, contention, other-app independence and build rejection | `TestUpdatePreparation` |
| Exclusive retirement before data mutation, stale requests/bodies | `TestUpdateExclusiveRetirement` |
| Migration/startup failure, previous health, restore/finalization failure | `TestUpdateFailures` |
| Stop/removal/shutdown during parked build or mutation; late health | `TestUpdateRetirement` |
| Activation durability uncertainty or committed journal-clear failure | `TestUpdatePersistence` |
| Historical recorded version retry | `TestUpdateSuccessAndRetry` |
| Transactional SQL failure and post-migration startup failure | `TestUpdateTemplateRollback` |
| Live migration descendant with denied retirement | `TestUpdateWriterRetirementUncertain` |
| Caller cancellation recovery and stopped → available intent reuse | `TestUpdateCancellationRecovery` |

## Error handling
All failed candidates return static `PublishFailure` feedback distinguishing retained predecessor, confirmed restoration, failed restoration and unconfirmed recovery. Restore I/O errors use `app.io_failed`. Do not leak paths, child output or secrets. Preserve previous builds/backups and journals until durable completion; no destructive cleanup is added.

## Testing strategy
Write update tests first and confirm the active admission rejection fails them. Use shipped child-process doubles for lifecycle/failure/durability tests, inspecting disk journals and actual process retirement. Run both maintained-template rollback arms with supported real Node/npm, assert preference, complete schema/checksum ledger and candidate-only data removal plus old health. Record executed/pass/fail/skip counts. Run `go test -race ./internal/apps/...`, `go vet ./...`, `go build ./cmd/pyry` (binary output outside worktree). Full-module verification belongs to the verifier; no live Claude.

## Open questions
None: recovery health is distinct from candidate activation, and uncertain durability forbids restoration.

## Documentation handoff
Pending documentation stage: `docs/hosted-apps.md`, “Registration and observed state”, “Migrations” and “Create, publish and update”: old-service availability during checks, exclusive mutation, health-before-activation, confirmed rollback requirements, restore/unhealthy-old-release failure outcomes, identical-active no-op and cancellation evidence. Explain committed-activation versus unconfirmed-recovery feedback. State startup recovery remains #3263 until it lands.

## Security review
**Verdict:** PASS
- Trust boundaries: retain `PrepareCandidate` identity/byte checks and fresh `VerifyBuild`; historical retries need an explicit publication progression guard.
- Tokens: minimal `serviceEnvironment` and static feedback exclude inherited credentials and child diagnostics.
- File operations: reuse descriptor-relative no-follow snapshot/restore, private modes, independent verification and synced registry writes; do not restore beneath an unjoined writer.
- Subprocesses: direct Node exec, no shell interpretation, process-group retirement and existing 60-second migration/15-second health deadlines.
- Cryptography: existing SHA-256 immutable inventory only; no new secrets or primitives.
- Network/I/O: reuse capped health parsing, private loopback client and generation-bound cancellation; no new listener protocol.
- Errors/logs: static phase/recovery text and safe `last_error`; tests include private sentinels.
- Concurrency: conditional spawn/health commit under registry then owner locks; durable activation dominates failure cleanup. Recovery must preserve newer stopped intent and tombstones.
- Threat model: trusted service code may affect OS/fleet state outside app data; that remains outside the hosted-app rollback contract. Interrupted-startup recovery is OUT OF SCOPE, owned by #3263.

**Reviewer:** builder self-review. **Date:** 2026-10-14.

## Revisions
- 2026-10-14: `intentLocked` normalizes observed state on available intent, including healthy updates. Admission instead uses a conditional `commitObservationLocked` intent mutation, preserving running state and avoiding an activation revision on identical retry.
- 2026-10-14: maintained-template unit fixtures execute their own migrated services and would reject an unconditional injected failure during checks. Rollback tests scope injected subprocess effects to the real publication data directory and retain baseline migrations inside template unit fixtures; actual publication still builds/checks and executes the new candidate SQL. External SQL evidence proves transactional rollback or successful schema migration before publication restoration.
- 2026-10-14: recovery ignores candidate-context cancellation but remembers incompatible registry notifications under the registry mutex. This prevents stopped → available identity reuse from restarting an obsolete recovery generation.
- 2026-10-14 (verifier finding 1): predecessor retirement can fail before a verified backup exists. Persist `Record.RetirementPending` atomically with exclusive starting state before cancelling the predecessor, then replace it with the verified journal in one durable write. Unconfirmed retirement retains this barrier across Start, Publish, reconciliation, targets, removal and owner replacement; public feedback reports recovery pending. Safe failure completion or health-confirmed recovery clears it only after positive retirement. `TestUpdatePredecessorRetirementUncertain` covers ordinary and published predecessors, including a joined ordinary leader with a live descendant and admission after reopen. The optional exact-key boolean preserves existing storage compatibility; no backup is claimed before verification. This closes the security review's exclusive-writer gap without changing subprocess or filesystem trust boundaries.
- 2026-10-14 (verifier finding 2): recovered generations participate in shutdown and stopped-intent completion observations, preserving the safe update error. `TestUpdateRecoveredCompletion` verifies durable stopped state, unchanged intent/release/error and actual retirement after successful recovery. Rework estimate: fewer than 350 written lines, no new exported types, no consumer signature changes; no concurrent branch overlap after fetching origin.
