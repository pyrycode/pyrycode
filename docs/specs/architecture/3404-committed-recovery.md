# Recover committed publication without data rollback

## Files read
- `internal/apps/startup_recovery.go` → `admitStartupRecoveryLocked`, `startupRecoveryCurrentLocked`, `recoverStartup`, `validateStoppedRecovery`: existing per-app admission, conditional completion and read-only compatibility checks.
- `internal/apps/owner.go`, `reconcile.go` → `newOwner`, `Stop`, `Shutdown`, `reconcileRecords`: admission before reconciliation, cancellation and worker joins.
- `internal/apps/publication_update.go`, `publication_process.go`, `health.go` → `restartPrevious`, `confirmRecoveryLocked`, `launchPublication`, `confirmRunning`, `usableLocked`: fresh verification and private matching health before durable target admission.
- `internal/apps/registry.go`, `releases.go`, `storage.go` → `changeLocked`, journal validation, `Open`: exact revision commits, strict storage and durable normalization.
- `internal/apps/startup_recovery_test.go`, `startup_recovery_template_test.go`, `publication_update_template_test.go` → restart fixtures, stopped SQL validator and `updateSQL` evidence.
- `templates/hosted-app/source/src/service/{database,migrations}.ts` → `schema`, `ledger`, `migrationFiles`: complete schema/preferences/applied checksum compatibility without migration.
- `docs/knowledge/features/apps-package-lifecycle-and-restart-state.md` § Startup publication recovery: retained journal is a barrier; only live activated publication has a committed-journal target exception.
- `docs/knowledge/features/apps-package-concurrency-and-persistence.md` § Registry durability: uncertain persistence bars further writes and targets.
- `docs/knowledge/features/apps-package-testing.md` § Publication fixtures: fresh exclusive reopen, rather than same-owner health, establishes recovery.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Registration, observed state and safe publication: activation selects the candidate and prohibits data rollback.

## Context
Committed journals currently remain barred on restart. Complete their selected candidate against current data without restoration or migration. Reuse the before-activation ownership and validation paths. No decision record required. No overlapping feature branch edits found in planned production files.

Sizing: one deliverable, approximately 650–750 total written lines, zero new exported types/interfaces, two internal consumer updates, five acceptance criteria and six reject outcomes. All five limits hold after planning.

## Design
Admit all journals without RetirementPending before reconciliation. For committed journals, retain candidate active release/title and skip restoration entirely. Generalize the private verified-release restart path to select the existing active candidate for committed recovery and the predecessor for rollback. Available recovery confirms matching private candidate health, then conditionally clears pending/journal/error in one durable write. Stopped recovery freshly verifies that candidate and reuses read-only schema/preferences/complete checksum validation without service startup; only successful conditional completion clears evidence/error. Before-activation outcomes and live publishing semantics remain unchanged.

## Concurrency model
Existing per-app workers, cancellation, subscriptions and shutdown joins remain authoritative. Lock order is registry then owner for currentness and persistence only. Build verification, stopped validation and private health wait outside locks. Add an existing per-instance fault-hook phase after positive validation but before completion, outside locks, to prove interruption and stale intent. Exact revision/journal/ownership checks reject obsolete work. Failures retain evidence until a fresh exclusive reopen rather than automatically retrying.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Activation persisted, first publication/update, available/stopped, exclusive reopen | TestCommittedStartupRecovery |
| Repeated interrupted final persistence, revision exhaustion or uncertain completion, fresh retry | TestCommittedRecoveryRetry |
| Stop, stopped→available reuse, removal, shutdown or newer metadata after validation | TestCommittedRecoveryInvalidation |
| Bad/missing candidate build, unhealthy candidate, stopped incompatibility, retained evidence | TestCommittedRecoveryFailures, TestCommittedTemplateRecovery |
| Unrelated registration and healthy target while recovery is parked | TestCommittedRecoveryInvalidation |
| SQL migration and candidate-only row/file preserved across fresh-owner restart | TestCommittedTemplateRecovery |
Existing TestStartupPublicationRecovery/Retry/Invalidation/Failures and TestStartupTemplateRecovery protect before-activation outcomes and retirement barriers.

## Error handling
Build/compatibility/health failures persist static app.unavailable metadata where possible. Failed/uncertain finalization retains the journal and candidate decision; never restore or select previous data. Previous build and backup remain retained. Existing changeLocked refuses revision exhaustion without wrap. Retirement uncertainty retains RetirementPending and bars reopen recovery.

## Testing strategy
Tests first: interrupt real publication after durable activation, join prior owner, reopen Open/NewOwner and park after validation before persistence. Inspect candidate title/release, retained data/evidence, revisions and target barriers. Inject failed and uncertain finalization repeatedly and prove a fresh successful retry. Real maintained-template SQL tests use supported Node/npm, save preferences, migrate candidate schema/create candidate-only row/file and compare complete SQL evidence after restart. A migrator witness must remain unchanged on reopen. Record actual runtime and executed/pass/fail/skip counts. Run scoped race tests, go vet ./... and go build ./cmd/pyry; verifier owns full-module gate. No live Claude required.

## Open questions
None. Committed candidate selection is already enforced by strict journal validation; stopped compatibility validation exists in the merged sibling.

## Documentation handoff
Pending documentation stage: `docs/knowledge/features/apps-package-lifecycle-and-restart-state.md`, “Startup publication recovery” under “Lifecycle and restart state”, and `docs/hosted-apps.md`, “Create, publish and update” → “Publication and recovery” → “Update and startup recovery contract”. Replace pending-#3404 wording with committed available/stopped outcomes for first publication and update: candidate/current data retained, no migrations/restoration, fresh build/health or stopped compatibility validation, conditional completion and per-app admission barrier, retryable retained evidence and failure outcomes. Preserve shipped before-activation matrix and retirement-only barrier without positive writer-retirement proof. Distinguish package recovery from daemon integration #3266 and publishing control #3301.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: strict Open/journal validation precedes recovery; VerifyBuild freshly verifies selected retained bytes, and confirmRunning requires exact identity/release health.
- Tokens/secrets: no credential handling or new environment fields; serviceEnvironment remains the fixed allowlist.
- Files: no restoration/deletion on committed recovery; immutable selected builds use existing no-follow selection/verification and registry writes retain sync/rename durability. Exclusive root ownership remains required.
- Subprocesses: fixed Node argv, private bounded validator and existing process-group cleanup; stopped validation is read-only and starts neither service nor migrator.
- Cryptography: no new primitives; sealed build digest verification reuses VerifyBuild.
- Network/I/O: existing private probe timeout, redirect/proxy rejection and 4096-byte cap remain unchanged.
- Errors/logs: persist only static safe errors, never SQL diagnostics or paths.
- Concurrency: exact revision/journal checks and cancellation prevent stale stop/reuse/removal results; fault hooks must run outside both locks. Shutdown joins workers and writers before root transfer.
- Threat model: trusted app code and exclusive filesystem ownership are contract assumptions. Daemon composition/control are downstream #3266/#3301 responsibilities.
**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-15

## Revisions
- 2026-10-15: `validMetadata` validates journal identity/shape but does not enforce candidate equality with ActiveRelease. Select the journal candidate explicitly for committed recovery and commit that pointer with successful completion; preserve the current title and all newer fields through exact revision checks. Rename the shared private restart helper to `restartRecovery` because it now selects either recovery release.
- 2026-10-15: self-review found that skipping restoration also skips its data-path confinement. Before committed validation/start, open the existing data directory and database through `privateDir`/`regularAt` without creating files or following symlinks. A stopped symlink database must retain the journal rather than validate foreign data.
- 2026-10-15: supported real Node exposed pre-existing macOS initialized-SQL snapshot failure in `backupOps.runBackupNode` (`/dev/fd` subprocess cwd). Filed #3415 in Backlog with priority:low and retained `TestCommittedTemplateUpdateSnapshot` skipped on macOS. The required real SQL restart arm uses actual first publication with two migrations, then writes saved preferences through its health-confirmed target before owner transfer; update recovery remains covered by the controlled real-publication restart cases. The main SQL recovery arm must execute and pass without skips.
