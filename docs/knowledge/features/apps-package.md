# `internal/apps` — durable host-app registrations

This package owns strict manifest validation, bundled registered source creation
and identity-based inspection, durable registrations, persistent-data snapshots
with restart verification and callable health-confirmed committed-release
process ownership and generation-bound stable
response preparation independent of conversations.
The [hosted-app reference](../../hosted-apps.md#manifest-and-identity)
owns the field tables and registration behavior; the
[v1 contract](../../specs/architecture/3120-hosted-app-contract.md) owns the broader
hosting design. Registration and stored `running` prove neither publication nor
readiness. The registry commits lifecycle observations and normalizes restart
state. Process spawn proves only starting; the owner confirms health before
exposing private targets; owner construction reconciles registrations and recovers
failed generations automatically. The daemon owns one owner for its discovery
registry, independently of channels; publication (#3125) and encrypted resource
activation (#3123) remain separate responsibilities.

## Package topics

| Document | Topic |
|---|---|
| [Lifecycle and restart state](apps-package-lifecycle.md) | Conditional commits, process ownership, health, reconciliation and restart normalization. |
| [Resource-serving](apps-package-resource-serving.md) | Private targets, stable responses and generation-bound cancellation. |
| [Concurrency and testing](apps-package-concurrency-and-testing.md) | Lock ordering, persistence, subscriptions and discriminating fixtures. |

## Validation and stored identity

Exact JSON schema validation must precede Go struct decoding. A decoder using
`DisallowUnknownFields` still accepts case variants of struct keys, so a later
`APP_ID` or `REVISION` can mask an invalid canonical value. `strictJSON` rejects
duplicates, nulls and trailing values; `validStorageSchema` and `storageObject`
then require exact mandatory/optional keys at snapshot, record, tombstone and
last-error levels. `validMetadataSchema` extends this check to release-history
entries, publish journals and their release/backup references. Supplied metadata
must have the exact nested keys and types, including an explicit boolean
`activation_committed` even when false; optional means omitted, never null.
`ValidateManifest` separately enforces exact manifest keys and the fixed v1
values. Do not relax storage validation to the wire contract's additive
unknown-field policy.

Existing storage must decode into a zero-value snapshot. Prefilling it with the
caller's host ID makes a missing stored `server_id` appear local and can open the
same corrupt file under different hosts. `Open` initializes the caller identity
only when storage is missing, validates the stored host otherwise, and leaves
rejected bytes untouched. The canonical UUIDv4 check reuses
[`identity.ParseServerID`](identity-package.md#parseserverid--validation).
Identity is `(server_id, app_id)`; separate hosts require separate roots.

Validate a create title before JSON encoding: Go replaces malformed UTF-8 in
strings, so validating only the encoded manifest would silently accept a changed
title. `Registry.Create` checks raw `validText` first, validates the stamped
manifest before writes and revalidates the generated manifest before committing.
`Registry.Inspect` resolves committed identity and derived editable locations
without reading source; edited bytes cannot mint another identity or establish
activation. See [creation and inspection](../../hosted-apps.md#registered-creation-and-inspection).

`registry.json` records optionally store `release_history` (`Record.ReleaseHistory`)
and `publish_journal` (`Record.PublishJournal`). Absence means empty history and
no outstanding publish. Legacy active/pending release pointers remain valid
without invented history; reopen preserves identities, intent, releases,
tombstones and metadata apart from existing observed-state/revision normalization.
`validateSnapshot` checks supplied metadata before normalization or persistence;
invalid metadata returns `ErrInvalidStorage` and leaves stored bytes untouched.

Each `Release` stores `release_version`, `build_id`, `build_root`, `node_version`,
`npm_version` and `lockfile_digest`. Build ID and lockfile digest are lowercase
64-hex SHA-256 encodings; release and exact Node/npm versions use canonical
numeric `major.minor.patch` strings, never ranges. `build_root` must exactly equal
the absolute daemon-selected `APPS_ROOT/<app_id>/builds/<build_id>`.
`Registry.RecordRelease` appends while retaining prior entries. Release versions
are unique per app: an identical entry is a no-op, and replacing an existing
version's metadata returns `ErrConflict`.

A `PublishJournal` contains `candidate`, optional `previous`, `verified_backup`
and `activation_committed`. Both `ReleaseReference` values carry `server_id`,
`app_id` and `release_version` scoped to the containing local registration.
Do not require a matching history entry: that would prevent recovery of a legacy
active release whose build metadata was never recorded. `BackupReference` carries
the same identities, a canonical UUIDv4 `publish_id` and `root` exactly equal to
`APPS_ROOT/<app_id>/backups/<publish_id>`. Foreign identities, arbitrary locations
and escaping or unclean path spellings are rejected. Storage validates these
references without opening their targets or verifying digest contents; see the
[runtime/root contract](../../specs/architecture/3120-hosted-app-contract.md#3-runtime-roots-and-service-conventions).

## Persistent-data snapshots

Registry validation of `BackupReference` proves identity/root shape, not backup
contents. `SnapshotData` and `VerifyBackup` supply independent data guarantees;
[the reference](../../hosted-apps.md#runtime-and-layout) owns their layout and
[publication contract](../../hosted-apps.md#publication-and-recovery) owns caller
serialization, writer quiescence and durable recovery. Neither primitive changes
registry lifecycle, selects releases or restores data (#3285).

A main-file copy can omit committed SQLite writes still in WAL. `sqliteSnapshot`
uses the selected supported Node's backup API on the private copied database and
sidecars, then normalizes the replacement to DELETE journal mode before sealing.
Read-only SQLite opening alone is insufficient: a WAL-mode file can create
sidecars during verification. `backupSQL` rejects non-standalone headers before
opening and checks integrity/foreign keys without schema-specific queries or
migrations; existing migration-ledger rows survive unchanged.

Sidecar rejection must precede the initialized-SQL branch. Otherwise absent or
zero-byte `app.sqlite` bypasses SQL opening and allows transient WAL/SHM/journal
files to be inventoried as durable data. `backupSQLiteEntries` rejects orphan
source sidecars and every sealed sidecar, even when altered evidence describes
them accurately. Absence, an empty data directory and an uninitialized main file
remain distinct; verification must never manufacture schema to make them agree.

Inventory directory entries are necessary to detect added/missing empty
directories. `backupEntry.Path` preserves Unix filename bytes through JSON:
ordinary strings replace invalid UTF-8 and would change the path being verified.
`backupEvidence` binds the exact reference to an ordered kind/size/SHA-256
inventory. `VerifyBackup` requires canonical evidence and freshly compares the
whole tree without consulting source data or trusting a cached verdict. Evidence
is a consistency check, not authentication against an OS-account attacker who
rewrites both the tree and evidence.

Exclusive destination creation rejects repeated publish IDs, including incomplete
attempts. Evidence is published only after files, directories and its parent are
synced, followed by a final backup-directory sync. A reported final failure must
invalidate evidence even after cancellation. `invalidateBackupEvidence` joins
the sealing failure with evidence-removal and invalidation-sync errors; hiding
those cleanup errors would conceal uncertainty about persisted evidence. Callers
must treat every snapshot error as failure, retain recoverable source data and
retry with a new publish ID. Context cancellation joins started Node children
before return; per-instance hooks avoid shared failure-injection races. See
[snapshot testing](apps-package-concurrency-and-testing.md#testing).

## Template toolchain

The [standalone source scaffold](../../hosted-apps.md#template-and-saved-preferences)
shares the manifest contract with `ValidateManifest`; generation and a successful
build establish neither registration nor service readiness.

An npm `preinstall` hook runs too late to reject an unsupported runtime before
dependency installation. Keep the template's npm `devEngines` runtime error
policy, `engines` range and `.npmrc` `engine-strict=true` together with
`assertRuntime` checks in command/compiled entries. The check concerns the actual
executable and must not silently choose another Node version.

Portable compiler/bundler choices keep the scaffold's dependency closure small
across Linux and macOS. TypeScript 7's native platform packages expanded the
lockfile substantially; the template pins the JavaScript TypeScript 6 compiler
and `esbuild-wasm` instead. TypeScript 6's side-effect import checking needs the
explicit `declare module '*.css'` in `assets.d.ts` for the frontend stylesheet
import; removing it breaks frontend typechecking.

`scaffold` and `Registry.Create` share `templates/hosted-app/inventory.json` and
the original assets, including required dotfiles, without a second maintained
copy. New shared service modules, migration SQL, browser scripts and tests must
enter that inventory; testing the maintained source tree alone can pass while
generated apps lack dependencies or service coverage. Verify a fresh generated
app through install, frontend/service typecheck, tests and build. `npm test` compiles its
service dependencies itself, so a prior build cannot mask missing test setup.

Inventory edits must also update the exact embed paths in `hostedtemplate.Assets`.
Go directory embedding ignores `.gitignore`; filtering generated output happens
after compilation and cannot protect the distributed executable from private
working-tree bytes. Embed exact maintained files, never directories or globs,
so environment files, SQLite data/sidecars, dependencies, build output and
unlisted adjacent source stay outside the binary even when present at build time.

## Template SQL and readiness

`schema` must validate the immutable preference table definition as well as
columns, saved values and integrity. Matching column names/types alone lets a
table stripped of CHECK constraints or STRICT mode report ready. Startup and
health also require the complete checksum ledger; they cannot repair/migrate
storage as a readiness side effect. The [template reference](../../hosted-apps.md#migrations)
owns migration commands and persistent-data placement; [ADR 043](../decisions/043-hosted-app-runtime-and-isolation.md)
owns the trusted-service isolation boundary.

SQLite identifiers are case-insensitive, and unqualified object lookup searches
the temporary schema before `main`. A case-sensitive ledger guard therefore
lets a temporary uppercase/mixed-case ledger capture runner writes: migration
can exit successfully with committed schema but no durable checksum, then fail
on rerun. `migrationSQL` reserves ledger identifiers case-insensitively and
rejects non-main operations; runner ledger, schema and preference API SQL target
`main` explicitly. Keep both protections so validation cannot accept temporary
shadows as durable state.

Migration authorization lasts only while the SQL file executes. Temporary or
persistent triggers could defer ledger mutation until runner SQL executes after
authorization is removed. Deny both trigger forms, alongside transaction control,
connection changes and external effects, so all pending schema/ledger work stays
within the runner's single transaction.

## Manifest updates and retention

The stored manifest can describe a candidate title/release while the committed
record still describes the previous one. `Registry.UpdateManifest` changes only
the manifest and record revision; it preserves committed title, active/pending
release, desired/state and last error. Treating the manifest as an activated
release would bypass publication and health confirmation.

Registration equality compares manifests, not whole records. Re-registering an
identical validated manifest after lifecycle changes must leave committed title,
releases, state and errors intact. Comparing it with the initial registration
defaults would turn that no-op into a conflict or overwrite confirmed state.

`Registry.Remove` retains source/build/data and permanently tombstones the ID.
Tombstones survive reopen and are excluded from the 256 live-registration limit;
discarding them to bound storage would permit removed identities to be reused.
