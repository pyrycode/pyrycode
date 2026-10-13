# Repeatable persistent app-data restoration (#3285)

## Files read

- `internal/apps/backups.go` → `VerifyBackup`, `backupIdentity`, `backupOps.tree`, `copyFile`, `backupDir`: persisted evidence, containment, inventory and synchronized copying.
- `internal/apps/backup_sqlite.go` → `verifyBackupSQLite`, `backupSQLiteEntries`: standalone SQLite integrity/foreign-key validation without migrations.
- `internal/apps/runtime.go` → `privateDir`, `regularAt`, `supportedNode`: pinned no-follow descriptors and selected runtime contract.
- `internal/apps/releases.go` → `BackupReference`: exact local host/app/publish/root binding.
- `internal/apps/backups_test.go` → `backupFixture`, `backupNode`, `TestBackupSQLiteWAL`: canonical fixtures, selected Node and committed WAL proof.
- `docs/knowledge/features/apps-package.md` § Persistent-data snapshots: inventories include empty directories; verification must not manufacture schema or sidecars.
- `docs/knowledge/features/apps-package-concurrency-and-testing.md` § Testing: actual supported Node execution and canonical temporary roots matter.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change / Test execution and artifact survival: assert effects and executed counts.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Registration, observed state and safe publication: restored data alone does not establish rollback health.

## Context

Publication and restart recovery need a separately callable primitive that restores a complete verified pre-update tree. Snapshot #3284 is merged. The callers own serialization, writer quiescence, journals and old-release health; this ticket changes no registry lifecycle. No decision record is needed.

## Design

Add `RestoreData(ctx, root, server, app, ref, node) error` in `internal/apps/restores.go`. Every invocation calls `VerifyBackup` before any current-data mutation and independently inventories the freshly verified backup data. Open the app and backup through pinned no-follow descriptors. Preflight the entire current data tree without changing permissions/content, rejecting symlinks, nonregular entries and multiply linked files before deletion begins.

Replacement removes current data through descriptor-relative recursion, then recreates the backup data directory/tree if present. Reuse `backupOps.tree`/`copyFile` with a per-instance immutable-source option: restoration must not even chmod backup files. Set destination directories/files to 0700/0600. Compare restored inventory with the freshly verified source inventory, check SQLite integrity/foreign keys through the existing fixed Node verifier, then sync restored files/directories and the app parent before success. Whole-tree replacement removes candidate-only directories/files and every transient SQLite sidecar. Absent, empty and zero-byte database states remain distinct.

No staging tree or journal is introduced. Errors after deletion begins can leave partial current data; a new call rebuilds it from the intact backup. This is permitted only under caller-owned quiescence and the consumers' recovery journal. Backup deletion is always caller-owned.

Feature-branch overlap check after fetch: no other feature branch touches the existing backup files or proposed restoration files. Size recheck: one deliverable, approximately 650 total written lines, zero new exported types/interfaces, zero existing consumer updates, four acceptance criteria, at most nine distinct rejection/failure categories.

## Concurrency model

Synchronous descriptor operations; no new goroutines. Callers serialize operations and quiesce all app writers throughout each call. Existing context-bound Node children are joined before return; per-instance hooks inject failures without global state.

## State transitions and identity reuse

| Event | Race-enabled coverage |
|---|---|
| Absent/empty/uninitialized/nested snapshot restored over candidate data | `TestRestoreStates` |
| Repeated successful restoration under the same reference | `TestRestoreStates`, `TestRestoreSQLiteWAL` |
| Copy/replacement/verification/sync failure and cancellation, then fresh caller retry | `TestRestoreFailures` |
| Interruption after replacement begins, including partial deletion | `TestRestoreFailures` |
| Backup tampering before or between attempts | `TestRestoreTampering` |
| Foreign/escaping reference or unsafe current/backup tree | `TestRestoreContainment` |

## Error handling

Return contextual errors preserving `errors.Is` for cancellation, injected failures and syscalls. Failure never establishes restoration success. Reject unsafe current data before mutation; backup verification failure leaves current data untouched. Retain the complete sealed backup after all attempts, including interrupted replacement. No migrations, template preference assumptions or invented checksum ledger.

## Testing strategy

Write tests first and observe the missing API failure. Table-driven states, backup tampering and containment checks; injected copying, replacement, restored verification and sync errors plus cancellation before/during replacement; retry with a JSON-round-tripped persisted reference and fresh public caller, checking unchanged independently verifiable backup. Real supported Node 24.15+ keeps a SQLite writer open with checkpoint disabled, proves the saved preference is absent from the main file alone, snapshots WAL and ledger/nested data, then mutates schema/data and restores. Assert original schema/values/ledger, private modes, missing candidate content/sidecars and backup retention. Record JSON executed/pass/fail/skip counts. Run touched-package race tests, `go vet ./...` and `go build ./cmd/pyry`; the verifier owns the full-module gate.

## Open questions

None. The selected default Node is unsupported; use an explicitly selected Node 24.15.0 downloaded into task scratch space for the required SQL execution.

## Documentation handoff

Pending documentation stage: `docs/hosted-apps.md`, “Runtime and layout” and “Create, publish and update”: document whole-tree/schema restoration, candidate-only content and transient sidecar removal, fresh verification after restart, absent/empty/uninitialized restoration, repeatability after interruption and backup retention until caller-owned durable recovery completion. State caller-owned writer quiescence and that arbitrary OS/fleet effects are outside restoration.

## Security review

**Verdict:** PASS

- Trust boundaries: `VerifyBackup` and `backupIdentity` validate exact local identities/root and freshly compare persisted evidence before replacement; current-tree preflight rejects unsafe entries independently.
- Tokens/secrets: no credentials generated or logged; private data retained at 0700/0600.
- File operations: MUST FIX addressed in design: copied backup must not receive the snapshot helper's source chmod. Immutable-source option prevents that write; destination modes are explicit. Descriptor-relative no-follow traversal and hard-link checks prevent outside writes. Interrupted deletion is recoverable from the untouched backup.
- Subprocesses: reuse fixed SQLite script, explicit supported executable, argv without shell, empty environment, bounded context and joined child.
- Cryptography: existing SHA-256 consistency inventory; no new keys/nonces or secret comparisons. Evidence cannot authenticate against an OS-account attacker rewriting both evidence and backup.
- Network/I/O: no network in production, existing bounded evidence parsing and cancellation-aware copy; no new server or socket.
- Errors/logs/telemetry: contextual local errors preserve causes; no data logging or new telemetry; consumers own wire sanitization.
- Concurrency: no new worker or lock; caller-owned writer quiescence excludes concurrent source/current mutation. Cancellation after mutation returns failure and retry remains possible.
- Threat model: OUT OF SCOPE: publication journals/quiescence/old-release health (#3262), interrupted-publication orchestration (#3263), arbitrary OS/fleet effects. This primitive proves restored data only.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-12

## Revisions

2026-10-12: Supplemental coverage in `TestRestoreSQLiteGenericAndCancellation` proves initialized arbitrary schema restoration without inventing a ledger, cancellation/subprocess failure during restored SQLite verification, joined children and fresh-caller retry. `TestRestoreContainment` also asserts sibling-app preservation. The API, replacement design and security verdict remain unchanged; actual written work is approximately 590 lines, within all five sizing limits.
