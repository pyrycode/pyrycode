# Before-activation publication recovery

## Files read
- `internal/apps/registry.go` → `Record.recoveryPending`, `changeLocked`: existing barriers and conditional durable mutations.
- `internal/apps/owner.go`, `reconcile.go` → `NewOwner`, `Stop`, `Shutdown`, `reconcileRecords`: asynchronous ownership and joined cancellation.
- `internal/apps/publication.go`, `publication_process.go`, `publication_update.go` → `publication`, `restartPrevious`, `confirmRecoveryLocked`: reuse private previous-release health without candidate activation.
- `internal/apps/restores.go`, `backups.go` → `backupOps.restore`, `VerifyBackup`: retained, freshly verified full-tree restoration with fault hooks.
- `internal/apps/publication_update_template_test.go` → `updateSQL`, `TestUpdateTemplateRollback`: maintained-template SQL proof pattern.
- `templates/hosted-app/source/src/service/{database,migrations,environment}.ts` → `schema`, `ledger`, `migrationFiles`: stopped validation must match readiness, including preferences and complete checksums.
- `docs/knowledge/features/apps-package-lifecycle-and-restart-state.md` § Publication writers and completion: preparation can mutate data; retired writers must be joined first.
- `docs/knowledge/features/apps-package-testing.md` § Publication fixtures: same-owner rollback and SQLite integrity do not prove restart recovery or compatibility.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Registration, observed state and safe publication: durable recovery before targets.

## Context
Open validates and normalizes storage but leaves uncommitted publication evidence barred. Establish new per-app recovery ownership before ordinary reconciliation can prepare data. After-activation recovery remains #3404. No decision record required. No overlapping feature branch edits found.

Sizing: approximately 950–1100 written lines, zero exported types/interfaces or consumer migrations, five criteria, approximately eight reject outcomes. This exceeds the 800-line ceiling; #3403 is already a grandchild (#3263 → #3125), so the required needs-human:sizing judgment is recorded and building continues.

## Design
NewOwner admits one background recovery worker for each uncommitted journal, excluding retirement-only or retirement-uncertain records. Reuse publication ownership and completion channels so Start/Publish/removal remain barred and Stop/Shutdown join work. Restore through per-owner backup operations before any previous-service launch. Never migrate. Keep all evidence until conditional persistence succeeds.

Available updates reuse fresh previous-build verification and private matching health; health atomically clears journal/pending and retains committed title/release plus a static interrupted-publication error. Available first publications restore and finish failed/unpublished. Stopped updates freshly verify the previous build and run a bounded private Node validation command importing that build's schema/ledger helpers against read-only restored SQLite; no HTTP service or migrator starts. Stopped first publications finish stopped/unpublished. All completions preserve title/manifest/intent; obsolete revisions fail closed.

## Concurrency model
Registry-then-owner lock order covers admission/currentness/persistence only. Restoration, subprocess validation and health waits run in one worker per app without those locks. Workers use owner cancellation plus per-operation cancellation, subscribe to invalidating registry changes, and close done only after owned validation/process work has joined. Ordinary reconciliation and unrelated registry operations remain independent. Exclusive filesystem ownership transfers only after previous owner shutdown.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Journal synced or migration completed, owner transfer | TestStartupPublicationRecovery |
| First publication, available/stopped | TestStartupPublicationRecovery |
| Partial restore or restored-before-finalization; repeated reopen | TestStartupRecoveryRetry |
| Stop, stopped→available reuse, removal or shutdown during parked restoration | TestStartupRecoveryInvalidation |
| Failed previous build/health/snapshot/finalization | TestStartupRecoveryFailures |
| Stopped schema/preferences/checksum compatibility and real SQL restart | TestStartupTemplateRecovery |

## Error handling
Restoration failure retains evidence and uses app.io_failed where persistence succeeds. Build, compatibility or health failure retains evidence and a static app.unavailable error. Failed/uncertain final persistence leaves targets barred. Existing registry revision exhaustion refuses rather than wraps. No backup/build deletion is introduced. Retirement uncertainty alone never authorizes restoration.

## Testing strategy
Write restart tests before implementation and observe failure. Seed durable journal boundaries from actual publication, join old owner, then use real Open/NewOwner. Park restore via existing backup hooks before target admission; inspect data, lifecycle, monotonic revisions and unrelated app usability. Exercise repeated interrupted restoration and finalization. Run maintained-template migration/restart with supported Node and inspect complete SQL schema/preferences/ledger and removed candidate-only data. Run go test -race ./internal/apps/..., go vet ./..., and go build ./cmd/pyry; full-module gate belongs to verifier.

## Open questions
None. Retained journal failures are retried on fresh exclusive reopen, not a busy automatic loop.

## Documentation handoff
Pending documentation stage: update `docs/knowledge/features/apps-package-lifecycle-and-restart-state.md`, “Lifecycle and restart state”, and `docs/hosted-apps.md`, “Create, publish and update” / “Publication and recovery”. Replace pending-#3263 wording with the before/after-activation matrix, per-app admission barrier, first-publication/stopped outcomes, retryable retained snapshot and failure outcomes. State retirement-only evidence remains barred without positive writer-retirement proof. Describe before-activation recovery as implemented, after-activation resolution pending #3404 and publishing integration pending #3266.

## Security review
**Verdict:** PASS
- Trust boundaries/files: preserve strict Open validation before recovery; reuse no-follow backup restoration and sealed VerifyBuild. Never trust a stored reference as fresh evidence.
- Subprocesses: selected Node only, fixed argv and allowlisted service environment; read-only SQLite validation imports only freshly verified previous-build helpers. Bound execution and join process-group cleanup before completion.
- Tokens/cryptography/network: no new credentials, crypto or listeners; available recovery uses existing bounded private health transport.
- Errors/logs: only static publication/recovery feedback persists; no paths or subprocess diagnostics enter metadata.
- Concurrency: MUST FIX addressed in design: hold recovery admission before any preparation; cancellation plus exact revision/current journal checks prevent stale completion or tombstone revival. Stop/shutdown join owned workers.
- Threat model: trusted local service code and exclusive filesystem ownership remain required by the contract; after-activation decisions are out of scope (#3404).
**Reviewer:** builder (self-review)
**Date:** 2026-10-14
