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
