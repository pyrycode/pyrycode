# Persistent app-data snapshots (#3284)

## Files read

- `internal/apps/runtime.go` → `privateDir`, `regularAt`, `supportedNode`: no-follow storage descriptors and actual selected-runtime validation.
- `internal/apps/releases.go` → `BackupReference`, `validMetadata`: host/app/publish identity and exact backup-root contract.
- `internal/apps/manifest.go` → `validID`: canonical UUIDv4 validation.
- `internal/apps/owner_test.go` → `TestOwnerSupportedNode`: explicit supported Node selection and skip semantics.
- `templates/hosted-app/source/src/service/database.ts` → `database`, `schema`: defensive SQLite, integrity and foreign-key conventions.
- `templates/hosted-app/source/src/service/migrations.ts` → `ledger`: preserve main migration rows without invoking migrations.
- `docs/knowledge/features/apps-package.md` § Validation and stored identity, Template SQL and readiness, Lifecycle and restart state: metadata validation proves no content verification; startup must not migrate.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change, Test execution and artifact survival: actual WAL proof and executed counts are necessary.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Runtime, roots and service conventions, Registration, observed state and safe publication: all-data snapshot before mutation with caller-owned serialization/quiescence.
- Node 24 SQLite API, `sqlite.backup` and `DatabaseSync`: supported backup and read-only integrity API.

## Context

Publication and recovery need restart-verifiable all-data snapshots; registry references alone prove no integrity. This primitive excludes restoration (#3285), release selection, retirement, migrations and journals. No separate decision record is needed.

## Design

`SnapshotData(ctx, root, serverID, appID, publishID, node) (BackupReference, error)` creates a new private exact-root backup. `VerifyBackup(ctx, root, serverID, appID, reference, node) error` independently checks persisted evidence and the exact tree, without source access or cached state. Both validate daemon-selected clean absolute root and selected identity before I/O. Existing destination directories are conflicts, including incomplete ones; retry uses a new publish ID.

The backup contains `data/` when source data exists and `verification.json` with a version, exact reference and ordered path/kind/size/SHA-256 inventory. Absence of `data/`, an empty `data/`, and zero-byte `app.sqlite` remain distinct. Directory entries, including empty directories, are part of the inventory. Evidence is not an authenticity signature against an attacker rewriting both files and evidence under the daemon's OS account.

No-follow descriptor-relative traversal accepts only directories and single-link regular files; directories/files become 0700/0600. Copy hashes are compared with independently read destination hashes. An initialized top-level `app.sqlite` is copied with its WAL/SHM into the private backup, then Node's backup API creates a standalone replacement through a private work directory. Normalize that replacement to DELETE journal mode, check integrity/foreign keys, replace the copied main file and discard WAL/SHM. Preserve all migration-ledger rows without schema-specific SQL. Verification rejects sidecars and WAL-mode headers before a read-only SQLite integrity check. Node receives fixed script/argv, empty environment and pinned private cwd, never app code.

Sync every snapshot file and directory, evidence and backup parent before publishing evidence via rename; sync the backup directory after rename. Remove evidence on a reported final sync/cancellation failure. Incomplete destinations remain recoverable but unverified and cannot overwrite any prior backup.

No overlapping feature branch currently changes the new snapshot-family files. Estimated written work: 760 lines (production/tests/plan), zero exported types, zero existing consumer updates, four acceptance criteria, at most ten reject categories. Recount before plan commit: within all five limits.

## Concurrency model

Synchronous functions; no background goroutines or shared mutable hooks. Caller serializes app operations and quiesces all writers for the entire snapshot. Context cancels traversal/copy and the selected Node child; subprocess is joined before return. Hooks belong to each internal operation instance for deterministic failure tests.

## State transitions and identity reuse

| Event | Race-enabled coverage |
|---|---|
| New snapshot / restart with source removed | `TestBackupStates`, `TestBackupSQLiteWAL` |
| Repeated publish ID after success or partial failure | `TestBackupStates`, `TestBackupFailures` |
| Evidence/tree mutation after sealing | `TestBackupTampering` |
| Cancellation before/during copy and sealing | `TestBackupFailures` |
| Peer close followed by repeated queued idle wakes (rework revision) | `TestV2Session_PeerClose_TearsDownWithoutReply` |

## Error handling

Return identity/storage/conflict or wrapped copy/SQLite/verification/sync errors. No migration, repair, source writes, cached verification, or accepted partial evidence. Invalid source entries reject before SQLite runs. Verified backups are never overwritten. Source corruption is an error, not an uninitialized database.

## Testing strategy

Tests first: absent/empty/zero-byte/nested data and fresh verification without source; missing/changed/added entries and malformed/foreign evidence; escaping identities, symlink components/entries and nonregular files; cancellation and per-instance copy/SQLite/verification/sync failure hooks. Real Node 24.15+ SQL test keeps its writer connection open with checkpoint disabled, proves preference absent from main alone but present in WAL, and asserts ledger/file preservation, standalone DELETE mode, stable sealed bytes and no sidecars across repeat verification. Record JSON test counts. Run `go test -race ./internal/apps/...`, `go vet ./...`, `go build ./cmd/pyry`; full-module verifier gate remains downstream.

## Open questions

None.

## Documentation handoff

Pending documentation stage: `docs/hosted-apps.md`, “Runtime and layout” and “Create, publish and update”: document all-data/WAL snapshots, private storage, persisted verification after restart, explicit absent/empty/uninitialized states, immutable verified backups and caller-owned writer quiescence. State that #3285 supplies restoration, durable recovery completion is caller-owned, and arbitrary OS/fleet effects are outside these data guarantees.

## Security review

**Verdict:** PASS

- Trust boundaries: exact reference/identity checks precede disk access; evidence is compared with a fresh inventory, not trusted as a cached verdict.
- Tokens/secrets: no credentials generated or inherited; private data never logged.
- File operations: no-follow pinned directories/files, reject hard links/nonregular entries, 0700/0600; exclusive destination creation and last-step evidence publication prevent overwrite/partial acceptance.
- Subprocesses: selected executable version validated without fallback, fixed Node script, argv (no shell), empty environment, bounded context and joined child. Only private copied SQL data is processed.
- Cryptography: standard SHA-256 content digests, no secret comparisons or key/nonce lifecycle.
- Network/I/O: no network; local evidence parse bounded; copy checks cancellation between bounded reads.
- Errors/logs: return contextual errors to local consumers; wire translation belongs to consumers; no payload logging.
- Concurrency: callers own writer quiescence and serialization; partial snapshots lack published evidence; no worker survives return.
- Threat model: trusted services/local OS account per hosted-app contract. OUT OF SCOPE: restoration #3285, publication quiescence/journals #3262, interrupted-publication recovery #3263, arbitrary OS/fleet effects.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-12

## Revisions

2026-10-12: Evidence uses canonical JSON with byte-preserving paths (bounded to 16 MiB) so altered/unknown fields are rejected. Snapshot copying also hardens source permissions after rejecting hard links; source bytes remain untouched. Standalone header/sidecar rejection runs inside the fixed Node verifier before opening SQLite.

2026-10-12, verifier finding 1: `backupSQLiteEntries` rejects top-level WAL/SHM/journal sidecars when the source database is absent or uninitialized, and rejects every sealed sidecar independently of database size and persisted inventory. Absent and zero-byte states remain distinct. `TestBackupUninitializedSidecars` covers creation and fresh verification with matching tampered evidence and no source tree.

2026-10-12, verifier finding 2: storage syscalls now carry operation context while preserving `errors.Is`, including nested traversal. Failed sealing joins the original failure with both evidence removal and invalidation-sync errors; invalidation runs despite cancellation. `TestBackupStorageErrors`, `TestBackupInvalidationErrors` and `TestBackupSQLiteStorageErrors` prove errno preservation, cleanup error aggregation and rejection of incomplete backups.

2026-10-12, verifier finding 3: cancellation coverage now includes pre-start, copy, sealing, independent verification before/during traversal and started Node children in snapshot/verification. Per-instance process-start notification supports deterministic cancellation; every started child is waited on before return. `TestBackupCancellation` and `TestBackupSQLiteCancellation` assert cancellation, unchanged source/sealed bytes, no acceptable partial evidence and joined children.

Security re-review of these revisions: PASS. The inventory guard closes the sidecar trust-boundary gap before sealing/SQL verification. Contextual local errors expose no data contents; failed invalidation is reported, never silently accepted. Process-start test hooks are per-instance, production callers install none, and the synchronous child wait preserves shutdown ownership. Other reviewed boundaries and documented out-of-scope consumers are unchanged.

2026-10-12, verifier finding on `f0535807`: a controlled 150 ms delay between `ActiveConns` and the recorder snapshot reproduces the three-envelope failure; the additional envelope is `c-stays` at close code 4408. `TestV2Session_PeerClose_TearsDownWithoutReply` now holds automatic idle delivery outside the bounded test window, retains the actual peer session on Run, and explicitly delivers two stale idle wakes after joining Run. Assertions cover silent handling of the junk notice, closed state, worker shutdown, timer/session/queue removal, no late replies and survivor isolation. No relay production behavior changes. Focused race repeats under constrained scheduling and both touched package suites cover the repair; the dispatcher still owns the complete gate rerun and independent review.

Additional files read for this repair:
- `internal/relay/v2session_idle_test.go` → `TestV2Session_PeerClose_TearsDownWithoutReply`, `handshakeConnToOpen`: separate observations let an unrelated idle deadline change the recorder.
- `internal/relay/v2session_test.go` → `startManager`, `v2Recorder`: joined Run permits race-free state inspection and synchronous stale-wake delivery.
- `internal/relay/v2session.go` → `handleWake`, `armIdleTimer`, `teardown`: closed-state guard suppresses queued wakes; teardown stops timers and removes only the selected queue/session.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: outbound callback executes on Run and the opened session owns its worker and timers.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-peer-close-handlepeerclose-teardown.md`: peer notice must publish nothing and preserve other sessions; idle sweeping is a separate fallback.

Overlap check after fetching feature branches: no other feature branch changes `internal/relay/v2session_idle_test.go`. Security re-review: PASS; this repair changes only the fixture and its evidence, with joined dispatch before state inspection and cleanup of surviving timers.
