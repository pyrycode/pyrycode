# `internal/apps` — durable host-app registrations

This package owns strict manifest validation and durable registrations independent
of conversations. The [hosted-app reference](../../hosted-apps.md#manifest-and-identity)
owns the field tables and registration behavior; the
[v1 contract](../../specs/architecture/3120-hosted-app-contract.md) owns the broader
hosting design. Registration and stored `running` prove neither publication nor
readiness. The registry commits lifecycle observations and normalizes restart
state; health/process reconciliation remains #3122 and publication checks #3125.

## Validation and stored identity

Exact JSON schema validation must precede Go struct decoding. A decoder using
`DisallowUnknownFields` still accepts case variants of struct keys, so a later
`APP_ID` or `REVISION` can mask an invalid canonical value. `strictJSON` rejects
duplicates, nulls and trailing values; `validStorageSchema` and `storageObject`
then require exact mandatory/optional keys at snapshot, record, tombstone and
last-error levels. `ValidateManifest` separately enforces exact manifest keys and
the fixed v1 values. Do not relax storage validation to the wire contract's
additive unknown-field policy.

Existing storage must decode into a zero-value snapshot. Prefilling it with the
caller's host ID makes a missing stored `server_id` appear local and can open the
same corrupt file under different hosts. `Open` initializes the caller identity
only when storage is missing, validates the stored host otherwise, and leaves
rejected bytes untouched. The canonical UUIDv4 check reuses
[`identity.ParseServerID`](identity-package.md#parseserverid--validation).
Identity is `(server_id, app_id)`; separate hosts require separate roots.

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

## Lifecycle and restart state

`Registry.UpdateLifecycle(serverID, appID, Lifecycle)` replaces committed title,
desired/state, active/pending releases and optional `LastError` atomically. Empty
release strings and nil errors mean unset; identity, manifest and revision remain
registry-owned. `validateSnapshot` enforces existing title/release bounds,
`available`/`stopped` intent, `starting`/`running`/`stopped`/`failed` observations
and an active release for `running`. Invalid fields return `ErrInvalidLifecycle`;
foreign, unknown and tombstoned identities cannot be updated or revived.

Callers supply confirmed observations and safe static errors. `validErrorCode`
accepts the [contract vocabulary](../../hosted-apps.md#errors); messages are
1–160 UTF-8 bytes with no controls or leading/trailing whitespace. Keep parser,
process and I/O diagnostics out of metadata; package errors are returned separately.
Validation cannot establish safe provenance of text. Health confirmation belongs
to callers; no processes or probes run here. A cutover supplies new title/active
release, running state and unset pending/error in one mutation, avoiding an
observable mix of old and new release fields.

`Open` validates storage before `normalizeSnapshot`. Every available record with
an active release becomes starting; every unpublished or intentionally stopped
record becomes stopped, regardless of stored state. All other fields and
tombstones are preserved. Only changed states receive revisions, in stable app-ID
order. The total revision budget is checked before one snapshot write; exhaustion
or persistence failure returns no registry and preserves the committed bytes.
An unchanged reopen neither writes nor advances revisions, even at the ceiling.
Opening exposes this committed normalized baseline, not earlier-process events.

## Concurrency and persistence

`Registry.change` holds one mutex across validation, detached candidate creation,
persistence, the committed snapshot swap and synchronous notifications. Even the
unchanging host ID must be read under that lock: reading `snapshot.ServerID`
outside it races with replacement of the whole snapshot. Constancy of the value
does not make its containing storage
immutable. `TestPersistenceAndVisibility` exercises concurrent registrations and
readers, blocks a writer before commit and checks that readers cannot pass it.

Detect semantic no-ops before revision exhaustion. An identical registration,
manifest or lifecycle update, or repeated removal of a tombstone, still succeeds
without a write or change at the maximum sequence. Compare lifecycle errors by
value, not pointer identity. Each changed mutation advances the durable host
sequence once and stamps its record or tombstone. A failed write never swaps
committed memory or notifies consumers.
`Registry.List` returns records and host revision from one detached snapshot,
including independent last-error values. Use one registry owner per root;
independent owners and cross-process writes to the same root are unsupported.
Reopening can commit newer revisions, so transfer ownership to the reopened
registry and stop using its predecessor. Mutating the predecessor's stale
snapshot could overwrite normalization and move durable revisions backwards.

`Registry.Subscribe(func(Change))` delivers future changes synchronously under
the same mutex in increasing host revision order across concurrent mutations.
Each consumer owns a detached whole `Record`, including its own `LastError`;
removal carries server/app identity and revision with nil `Record`. Later
mutations or another consumer's edits cannot alter earlier deliveries. No-op,
rejected and failed mutations produce no notifications. Subscriptions persist
neither events nor consumers and replay no baseline or previous-process changes.

Callbacks must return promptly and never re-enter any registry method, including
the returned idempotent unsubscribe: doing so would deadlock on the held mutex.
Slow callbacks also block mutations and readers. Consumers own later transport
work; the registry adds no delivery goroutines or queues. Discovery wire mapping
and relay/daemon installation remain #3138/#3139.

## Testing

`TestStorageExactKeys` distinguishes required omissions from optional omissions
and rejects aliases at each storage level. `TestStorageAliasShadowing` supplies
raw JSON with an invalid canonical value followed by a valid case alias, including
missing-host inputs checked against two hosts. A normal struct round-trip cannot
produce these corrupt documents. Both tests check byte-for-byte preservation of
rejected storage and exposed the prior decoder bug; broad green gates alone had
missed it. See [development verification](development-verification.md#protocol-boundaries)
for missing/null/type and serialization checks.

Valid-storage fixtures used for byte-for-byte unchanged-open assertions must
already have normalized observed state. An available active record stored as
stopped legitimately becomes starting and writes on reopen; that is not schema
repair. `testStorageDocument` uses stopped intent/state so optional-field checks
do not accidentally test normalization. Rejected storage still remains unchanged.
Restart tests must transfer ownership after reopening, rather than mutating the
previous registry and overwriting newer revisions.

`TestRestartNormalization` covers mixed states and reverses disk order to verify
stable revision assignment; `TestNormalizationFailure` checks multiple changes
against insufficient/exact revision budgets and injected write failure.
`TestCommittedChanges` checks disk and committed memory before callbacks, exact
whole records across concurrent mutation/removal, consumer isolation and no replay.
`TestLifecycleFailureAndExhaustion` checks that no-ops and failed mutations leave
file bytes, revisions and notifications unchanged.

Lowercase UUID rejection fixtures need hexadecimal letters: uppercasing a
digits-only UUID changes nothing. `TestManifestBoundaries` uses `AAAAAAAA` to
exercise this boundary, alongside UTF-8 byte limits and exact manifest constraints.
