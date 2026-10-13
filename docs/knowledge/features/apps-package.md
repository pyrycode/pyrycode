# `internal/apps` — durable host-app registrations

This package owns strict manifest validation, bundled registered source creation
and identity-based inspection, durable registrations, persistent-data snapshots
with restart verification and callable health-confirmed committed-release
process ownership and generation-bound stable
response preparation and confined callable assets independent of conversations.
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

See [validation and stored identity](apps-package-validation-and-stored-identity.md#validation-and-stored-identity) for the schema and identity contracts.

## Topic map

| Section | Topic |
|---|---|
| [Validation and stored identity](apps-package-validation-and-stored-identity.md#validation-and-stored-identity) | Exact manifest/storage schemas and committed identities. |
| [Template toolchain](#template-toolchain) | Runtime rejection, shared inventory and safe embedding. |
| [Template SQL and readiness](#template-sql-and-readiness) | Durable ledger/schema validation and migration boundaries. |
| [Manifest updates and retention](#manifest-updates-and-retention) | Candidate metadata, no-op registrations and tombstones. |
| [Lifecycle and restart state](apps-package-lifecycle-and-restart-state.md#lifecycle-and-restart-state) | Owned processes, health, retries, private targets and restart state. |
| [Resource-serving](apps-package-resource-serving.md#resource-serving) | Confined callable assets and stable response ownership. |
| [Concurrency and persistence](apps-package-concurrency-and-persistence.md#concurrency-and-persistence) | Lock order, cancellation, committed snapshots and subscriptions. |
| [Testing](apps-package-testing.md#testing) | Discriminating fixtures, source-error proofs and toolchain checks. |

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
