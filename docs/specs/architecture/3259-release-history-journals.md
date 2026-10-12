# Additive release history and publish journals (#3259)

## Files read

- `internal/apps/registry.go` → `Record`, `changeLocked`, `cloneSnapshot`: durable mutation boundary and host revisions.
- `internal/apps/storage.go` → `Open`, `validStorageSchema`, `validateSnapshot`, `writeSnapshot`: validate before normalization; preserve atomic persistence.
- `internal/apps/lifecycle.go` → `cloneRecord`, `applyLifecycle`, `CommitObservation`, `normalizeSnapshot`, `notify`: detached ownership and metadata preservation.
- `internal/apps/manifest.go` → `strictJSON`, `validRelease`, `validID`: exact nested JSON and canonical identity/version checks.
- `internal/apps/registry_test.go`, `lifecycle_test.go`, `observation_test.go`: existing persistence, normalization and mutation helpers/contracts.
- `internal/apps/runtime.go` → `prepareRelease`: runtime consumers currently find builds independently; no adoption here.
- `docs/knowledge/features/apps-package.md` § Validation and stored identity, Manifest updates and retention, Lifecycle and restart state: exact schema before decoding, manifest-only registration equality, app-revision observations.
- `docs/knowledge/features/development-verification.md` § Establish the change surface, Verify inherited premises: distinguish storage additions from caller adoption.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Runtime, roots and service conventions; Registration, observed state and safe publication: release identity and durable activation boundary.

## Context

Persist release/recovery metadata before downstream publishers and recovery consume it.
Absent metadata remains empty; legacy active/pending pointers need no invented history.
This is one storage contract deliverable, with no build, verification, migration or process orchestration.
Sizing: about 710 total written lines (285 production, 345 tests, 80 plan), four new exported structs,
two existing validator call sites, four acceptance criteria, at most eight metadata rejection categories.
No other fetched feature branch overlaps the planned files.

## Design

Add optional `Record.ReleaseHistory []Release` and `Record.PublishJournal *PublishJournal`.
`Release` stores release version, lowercase SHA-256 build ID, absolute build root,
exact numeric Node/npm versions and lowercase SHA-256 lockfile digest.
The build root must exactly equal the daemon-selected root/app ID/builds/build ID.
A release version is unique within an app and its stored entry cannot be replaced.
`ReleaseReference` stores server/app identity and release version; it need not have a
history entry, since previous releases may predate additive metadata.
`BackupReference` stores server/app identity, canonical UUIDv4 publish ID and absolute
root exactly equal to daemon root/app ID/backups/publish ID.
`PublishJournal` stores candidate, optional previous reference, verified-backup reference,
and explicit `ActivationCommitted bool`. False chooses restoration of previous data/release;
true chooses the candidate. References/decisions do not establish verification or readiness.

`RecordRelease(serverID, appID, Release) (bool, error)` appends while retaining history;
identical entries are no-ops and a conflicting existing version returns `ErrConflict`.
`UpdatePublish(serverID, appID, *PublishJournal, *Lifecycle) (bool, error)` replaces or clears
the outstanding journal, optionally replacing lifecycle fields in the same durable snapshot.
A different publish ID cannot replace an outstanding journal; clear it first.
Callers supply confirmed activation fields (candidate active/title/state, cleared pending/error)
or recovery result fields. No orchestration or health inference occurs inside this API.
Route both through `changeLocked` with an internal metadata mutation callback. Existing calls
preserve metadata. Extend `validateSnapshot` with root context (two call sites), checking metadata
before normalization/persistence. Extend exact schema validation at every new object/array level.
Extend `cloneRecord` to copy history, journal and optional previous reference independently.
Discovery mapping stays unchanged. Retain the temporary-file/sync/close/rename writer.

## Concurrency model

Reuse the registry mutex across identity checks, detached candidate mutation, validation,
persistence, snapshot swap and synchronous detached subscriber delivery. No new goroutines.

## State transitions and identity reuse

| Event | Race-enabled coverage |
|---|---|
| Append, duplicate, conflicting release version; preserve other mutations | `TestReleaseHistoryRoundTrip` |
| First publish and both durable decisions across reopen/normalization | `TestPublishAtomicTransitions` |
| Activation/recovery snapshot; clear then next publish | `TestPublishAtomicTransitions` |
| Invalid input or failed persistence; retry/no-op without revisions/events | `TestMetadataFailures` |
| Concurrent writes across apps; ordered host revisions and detached deliveries | `TestMetadataIsolationAndOrdering` |
| Malformed nested metadata before restart normalization | `TestMetadataStorageValidation` |

## Error handling

Reuse identity/foreign/not-found/removed/exhausted errors and `ErrConflict` for immutable
version or outstanding-publish conflicts. Invalid metadata returns `ErrInvalidStorage`;
existing lifecycle operations retain `ErrInvalidLifecycle`. Validation/persistence errors
leave memory, bytes, revisions and subscribers unchanged. Generic validation errors contain no input.

## Testing strategy

Write focused tests first and demonstrate failure before implementation. Exercise legacy absence,
round trips, additive history, journal decisions/first publish, atomic lifecycle commits,
metadata preservation, invalid schema/references/paths/digests, failure rollback and isolation.
Run `go test -race ./internal/apps/...`, `go vet ./...`, `go build ./cmd/pyry`.
The verifier owns the full-module gate. No live-Claude work is required.

## Open questions

None. Runtime/backup filesystem and digest-content verification belong to downstream consumers.

## Documentation handoff

Pending documentation stage: `docs/knowledge/features/apps-package.md`, “Validation and stored identity”
and “Lifecycle and restart state”: additive stored fields, absence/legacy semantics, app-scoped
build/backup references, detached ownership, before/after-activation journal meaning and atomic
activation/recovery commit contract. Publishers/recovery are downstream consumers; metadata
alone proves neither readiness nor verified/restored data.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `validStorageSchema` requires exact nested keys before decoding; `validateSnapshot` validates stored and mutation metadata before persistence/normalization. References match both registered identities.
- [Tokens/secrets] No credentials are added; release digests and publish IDs are identifiers, not authorization tokens.
- [File operations] Exact absolute owned-root equality rejects arbitrary/traversing paths. Registry remains 0600 under 0700 root with atomic writes. OUT OF SCOPE: symlink-safe build/backup access and backup verification belong to #3260/#3262/#3263; storage never opens these references.
- [Subprocesses] No subprocesses or environment handling are added.
- [Cryptography] Digests are canonical SHA-256 encodings; content hashing/verification belongs to #3260, with no cryptographic claim by storage.
- [Network/I/O] No wire changes or network readers; metadata stays local registry storage.
- [Errors/logs] Generic metadata rejection contains no paths, payloads or process diagnostics; no new logging.
- [Concurrency] One existing mutex covers validation through notification; every mutable nested value is cloned. Failed persistence cannot advance journal decisions or revisions.
- [Threat model] Trusted hosted-app contract applies; stored “verified” backup references prove no verification. Recovery/readiness enforcement remains #3262/#3263.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-12
