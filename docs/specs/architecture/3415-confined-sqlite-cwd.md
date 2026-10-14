# Confined SQLite subprocess cwd

## Files read
- `internal/apps/backup_sqlite.go` → `runBackupNode`, `sqliteSnapshot`, `verifyBackupSQLite`: descriptor-path cwd and SQL paths.
- `internal/apps/backups.go` → `backupDir`, `snapshot`, `verify`: no-follow directory selection, inventory and evidence publication.
- `internal/apps/runtime.go` → `privateDir`, `regularAt`, `supportedNode`: private filesystem and runtime boundaries.
- `internal/apps/backups_test.go` → `TestBackupSQLiteWAL`: WAL-only rows, ledger, standalone mode and fresh immutable verification.
- `internal/apps/backups_rework_test.go` → `TestBackupSQLiteCancellation`, `TestBackupSQLiteStorageErrors`: joining and explicit failure without evidence.
- `internal/apps/publication_update_template_test.go` → `TestUpdateTemplateRollback`, `updateSQL`: maintained-template publication and saved preferences.
- `origin/feature/3404` → `TestCommittedTemplateUpdateSnapshot`: carry the standalone assertion, without recovery code or its macOS skip.
- `docs/knowledge/features/apps-package.md` § Persistent-data snapshots and `apps-package-testing.md` § Testing: absent data and skips cannot prove initialized SQL behavior.
- `docs/knowledge/features/development-verification.md`: scoped checks and durable evidence.
- `CODING-STYLE.md`: subprocess tests, shutdown and contract comments.

## Context
macOS rejects `/dev/fd/<directory fd>` as an exec cwd. A pathname fallback would lose descriptor confinement. One repair delivers portable initialized snapshot/verification; recovery remains independent. No concurrent feature branch changes the selected production/test files.

## Design
Add a private re-exec bootstrap in `internal/apps/backup_node.go`. `runBackupNode` passes the no-follow-opened directory as child fd 3 through `ExtraFiles`. An exact private argv shape invokes the bootstrap before main: validate the directory, `fchdir`, close the fd and `exec` the selected absolute Node executable with fixed SQL/mode and an empty environment. No shell, descriptor pathname or parent-process chdir is used. Bootstrap and Node failures remain explicit.

Snapshot SQL must not traverse `../data` after entering the work directory: move copied main/sidecar files into the selected work directory with descriptor-relative `renameat`, run SQLite on local filenames, install the standalone result through the existing descriptor-relative rename, and remove work source/sidecars before removing work. The original registration data is never moved. Verification already uses only local filenames.

Estimated total written work: 450–600 lines including plan, tests and evidence; zero exported types/interfaces, two internal launch callers, four acceptance criteria, fewer than ten reject outcomes. Recount before plan commit remains within all five limits.

## Concurrency model
The parent starts and joins one child under the existing 60-second context and WaitDelay. Re-exec becomes Node in the same PID, so cancellation kills/joins the same child. Bootstrap cwd changes occur only in the disposable child. No new parent goroutine or shared state.

## State transitions and identity reuse
| Event | Race-detector test |
| --- | --- |
| Selected directory renamed/replaced or substituted with a symlink at launch | `TestBackupNodeDirectoryIdentity` (snapshot and verify) |
| Cancellation after child start | `TestBackupSQLiteCancellation` |
| Repeated verification after source removal | `TestBackupSQLiteWAL` |
| Initialized maintained-template update | `TestUpdateTemplateSnapshot` |

## Error handling
Runtime probe/unsupported version, bootstrap cwd/exec failure, SQL failure and cancellation return errors. Failed snapshots publish no verification evidence. Existing storage failures retain explicit phase errors. Verification never repairs sealed contents. Interrupted copied-work moves leave an incomplete backup, requiring a new publish ID.

## Testing strategy
Write launch identity tests first and observe their failure against the old path-based launch. Cover renamed directories and symlink/replacement sentinels, unchanged parent cwd, SQL/nonexistent-executable failure, and reused existing cancellation/WAL/storage tests. Carry the retained standalone initialized assertion and add a healthy maintained-template update proving saved preferences and verified pre-migration backup. Run apps race tests, module vet and pyry build. Push the full candidate before requesting native macOS evidence; record exact revision, runtime, commands and executed/pass/fail/skip counts. Full-module race suite belongs to the verifier.

## Open questions
- Native task recipes must provide the required apps tests, rather than just platform availability; inspect actual returned artifacts before claiming proof.

## Revisions
- 2026-10-14: The retained assertion is carried in `internal/apps/committed_recovery_template_test.go`, its original #3404 file, without importing recovery or the macOS skip. #3404 is an additive overlap for this assertion; its merge must keep one unskipped copy of `TestCommittedTemplateUpdateSnapshot`.
- 2026-10-14: Inspection of `native_recipe` in the dispatcher's `fleet-support.py` resolves the native recipe question: `platform-probe` explicitly executes no candidate tests, while the executed recipes cover memoryruntime or Android. The required native apps run cannot be supplied by these recipes. The managed route probe is tracked separately and must not be reported as test proof. Native macOS acceptance remains incomplete builder work until an apps-capable route executes the named tests and returns evidence.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/apps-package.md`, “Persistent-data snapshots”: portable subprocess-directory mechanism and no-follow confinement/failure guarantees.
- Pending documentation stage: `docs/knowledge/features/apps-package-testing.md`, “Testing”: initialized macOS SQL/update proof; absent-data publications and skipped arms do not establish it.

## Security review
**Verdict:** PASS
- Trust boundaries: private bootstrap accepts only its exact argv shape, fixed mode and inherited directory; selected Node remains checked by `supportedNode`.
- Tokens/secrets: no credentials enter either subprocess; environment stays empty.
- File operations: MUST FIX addressed in design: `../data` could follow a substituted parent. Snapshot uses local work filenames after descriptor-relative moves. Directory replacement cannot redirect `fchdir`; directories/files retain 0700/0600 and existing no-follow opens. Arbitrary same-account mutation of files within an already selected private tree is outside the existing consistency threat model.
- Subprocesses: no shell or unchecked cwd path; executable is made absolute before cwd changes. Bootstrap exits on failed fchdir/exec; parent captures its error and joins.
- Cryptography: no new cryptography or secret comparisons.
- Network/I/O: no network boundary; subprocess timeout and joining remain bounded.
- Errors/logs: bootstrap diagnostics contain operation errors, no data/credentials; existing error contract remains explicit.
- Concurrency: no parent cwd mutation, added locks or goroutines; cancellation owns the single child PID.
- Threat model: preserve no-follow directory identity under launch substitution; no expansion to account-level attacker authentication of backup evidence.

**Reviewer:** builder (self-review)
**Date:** 2026-10-14
