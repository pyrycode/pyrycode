# `internal/apps` — validation and stored identity

See the [package overview](apps-package.md) for the topic map.

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
and `publish_journal` (`Record.PublishJournal`), plus the optional exact-key boolean
`retirement_pending` (`Record.RetirementPending`). Absence of the boolean means
false; null, strings, numbers and case variants are invalid. It preserves a writer
retirement barrier before a verified backup journal exists, rather than inventing
backup metadata; see [publication admission](apps-package-concurrency-and-persistence.md#publication-admission).
Absent history/journal means empty history/no journal, but a true retirement flag
still bars publication, starts and removal after reopen. Legacy active/pending
release pointers remain valid without invented history; reopen preserves identities, intent, releases,
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

`RecordRelease` compares whole metadata entries and does not enforce numeric
progression or prove a build ran. Checked preparation's byte-identity decision
must instead return the original recorded `Release` for identical retained bytes,
including its original Node/npm versions. Comparing newly checked tool versions
would incorrectly reject an otherwise identical retry. See
[atomic candidate recording](apps-package-concurrency-and-persistence.md#checked-candidate-recording)
and the [preparation reference](../../hosted-apps.md#checked-candidate-preparation).

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

### Checked preparation inputs

Source-copy privacy must preserve executability: copying every regular file at
0600 breaks scripts invoked directly by npm. `copyCandidateSource` keeps only the
owner executable bit (0700 instead of 0600), excludes top-level `dist` and
`node_modules` and rejects symlink/nonregular copied entries through no-follow
access. `candidateInputs` checks module type, both manifest identities,
package/lock name and version, and all four dependency maps before installation
and again after build commands. Pre-install checks alone would admit a build
that rewrites its package to CommonJS; the final pass also requires the validated
manifest to remain unchanged. Package version and app release version are separate.

Incoming PATH precedence cannot pin npm lifecycle Node: npm prepends dependency
and ancestor `node_modules/.bin` paths, including for install hooks.
`candidateEnvironment` uses a private lifecycle shell to restore the selected
Node alias first, covering ordinary `node` commands and env-node shebangs while
preserving other dependency binaries. npm's configuration loader keys sources
by pathname and rejects using one path for user and global config, even
`/dev/null`; distinct empty private files avoid this and inherited global/user
configuration. HOME, npm cache/config and tool aliases remain private to staging;
the explicit minimal environment excludes inherited daemon credentials and
configuration, and supplies no app persistent-data paths.

`candidateTemporary` allocates a fresh 0700 directory under fixed `/tmp`, independent
of inherited daemon TMPDIR. It rejects `source`, `staging`, `builds` and `dist`
components in both the allocated and symlink-resolved paths before exposing the
resolved directory as TMPDIR. Template test data therefore stays outside
source/staging/build/dist trees while the service's lexical and canonical
unsafe-data-path rejection remains enforced. `PrepareCandidate` removes owned
temporary data after success, failure or cancellation, once command/process-group
cleanup has joined. Failed resolution or unsafe placement also removes owned
storage; cleanup errors become sanitized preparation failures. App scripts remain
trusted and can explicitly alter lookup or choose another executable; these guarantees provide
no [OS sandbox](../decisions/043-hosted-app-runtime-and-isolation.md).
See [candidate fixtures](apps-package-testing.md#checked-candidate-fixtures).

### Retained artifact boundaries

Manifest validity and correctly shaped release metadata cannot prove retained
bytes. `ValidateArtifacts`, `InstallBuild` and `VerifyBuild` check both expected
local identities and the retained artifact set; `RetainedBuild` establishes
neither recorded candidate success nor readiness. Installation revalidates its
own staging input rather than trusting a prior returned inventory. Package/lock
consistency and successful build commands belong to checked preparation;
migration, health and activation belong to publication. The
[reference](../../hosted-apps.md#retained-candidate-artifacts) owns those caller
boundaries and the [layout](../../hosted-apps.md#runtime-and-layout) owns exact
inventory serialization and verified reuse.

Lexically cleaning a dependency target before resolving linked directory
components can conceal an escape through `..`. `artifactScan.resolveParts`
interprets each component against the recorded `node_modules` graph before
processing its parent traversal; file access never follows artifact links.
Active-link recursion detects cycles without imposing a hop ceiling that would
reject long finite chains. Cycle detection alone still allows exponential
re-expansion of repeated acyclic targets: memoize completed resolutions per scan,
keep cached component slices immutable and check cancellation during traversal,
including cached resolutions. `scanArtifacts` also checks cancellation after
canonical inventory/digest creation before returning success. See
[retained-build fixtures](apps-package-testing.md#retained-build-fixtures).

A sealed directory cannot move between parents on Linux without write permission
to update its `..` entry. `InstallBuild` therefore seals and verifies a temporary
tree under the same `builds` parent before exclusive rename. `selectBuild`, used
by `prepareRelease`, ignores only `.install-*` directories and never selects
them. Otherwise temporary installation or interrupted-install entries would
break ordinary startup/reconciliation of the previous release. Other malformed
entries and partial digest builds remain errors; ignoring installer directories
does not validate or activate them. Copying bytes rather than hardlinking keeps
retained files independent of later staging
edits. Read-only modes prevent ordinary writes, and verification detects changed
artifacts under the trusted-service
[ADR 043 boundary](../decisions/043-hosted-app-runtime-and-isolation.md); these
guarantees do not provide OS-account isolation.
