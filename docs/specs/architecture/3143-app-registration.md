# Durable app registration (#3143)

## Files read

- `docs/specs/architecture/3120-hosted-app-contract.md` → Manifest and validation, Runtime, Registration: normative fixed values, identity, revisions and retention.
- `docs/hosted-apps.md` → Manifest and identity: current contract reference.
- `internal/identity/server_id.go` → `ParseServerID`: canonical lowercase UUIDv4 validation, reused for host and app UUID shape.
- `internal/conversations/registry.go` → `Registry`, `writeRegistrySnapshot`, `AdvanceReadUpTo`: atomic storage and persistence-before-observation precedents.
- `internal/apps/storage.go` → `Open`, `validateSnapshot`: stored identities and revisions must come from exact required keys, before semantic validation.
- `internal/apps/storage_test.go` → `TestStorageExactKeys`, `TestStorageAliasShadowing`: absent keys and case aliases at every storage level, including later aliases masking invalid canonical values.
- `docs/knowledge/features/identity-package.md` → corruption and error taxonomy: never repair corrupt storage implicitly or echo unvalidated IDs.
- `docs/knowledge/features/conversations-package.md` → value copying and concurrency: mutable nested values require independent snapshots.
- `docs/knowledge/features/development-verification.md` → Protocol boundaries: test missing, null and wrong types distinctly.
- `CODING-STYLE.md` → persistence and comments: stable sorted snapshots, same-directory sync/close/rename, contract comments.

## Context

Apps need durable host-scoped registrations independent of conversations. This leaf package owns strict manifest validation and registration storage; it proves neither publication nor readiness. Lifecycle mutations and normalization belong to #3144; consumers to #3138/#3139; package/compiled-file checks and publication to #3125. No new decision record is needed. Fetched feature branches have no overlap with these new files.

Sizing, checked against sketch and final plan: one durable-registration deliverable, five acceptance criteria, approximately 760 total written lines including tests and this plan, four exported types, zero consumer migrations, at most nine mutation refusal categories. Three production files: manifest, registry, storage.

## Design

`ValidateManifest(data []byte, serverID string) (Manifest, error)` validates canonical local identities, every table constraint and strict JSON syntax. Immutable `Manifest` holds canonical JSON and identity/title/release accessors; canonicalization makes formatting and key order irrelevant. A token parser rejects duplicate keys, nulls and excessive nesting before schema checks, without filesystem or subprocess access.

`Open(root, serverID string) (*Registry, error)` validates an absolute root and canonical host, reads only `registry.json`, and rejects invalid storage without writing. `Register(data []byte) (bool, error)` accepts caller-minted manifest identity; `UpdateManifest(appID string, data []byte) (bool, error)` requires matching existing identity and replaces only the manifest. `Remove(appID string) (bool, error)` records permanent tombstones. Booleans report actual committed changes. `List() ([]Record, uint64)` returns detached, sorted records and their host revision in one snapshot. Package sentinel errors distinguish invalid input/storage, foreign host, conflict, missing registration, tombstone, capacity and sequence exhaustion.

Records retain immutable manifest, app ID, committed title, desired/state, optional release/error fields and record revision. Initial desired/state are available/stopped with unset releases/error. Storage carries host ID/revision, sorted records and sorted tombstones. Load validates schema, IDs, manifests, lifecycle field values, revision bounds, uniqueness/overlap and capacity. Storage encoding may omit unset lifecycle fields; it is not a wire DTO.

## Concurrency model

One registry mutex serializes mutations, persistence and snapshots. Build a detached candidate, persist it, then swap committed state while still locked. Readers cannot observe uncommitted records or revisions. No package goroutines or callbacks; a private writer seam supports deterministic failure/visibility tests. One registry owner per root, as with host identity bootstrap; cross-process writers are outside this package.

## State transitions and identity reuse

| Event | Race-enabled test |
|---|---|
| Re-register / conflicting registration / repeated explicit update | `TestRegistryMutations` |
| Multiple manifest updates preserve title and lifecycle | `TestRegistryMutations` |
| Remove / repeated remove / revival attempts / freed capacity | `TestCapacityAndRetention` |
| Reopen preserves state, revision and tombstones | `TestRegistryMutations`, `TestCapacityAndRetention` |
| Write failure / retry / concurrent readers and writers | `TestPersistenceAndVisibility` |
| Sequence exhaustion with repeated no-ops | `TestExhaustionAndCorruption` |
| Same app ID on two different hosts | `TestTwoHosts` |

## Error handling

Reject malformed manifests and invalid/foreign snapshots with package errors, leaving bytes and memory unchanged. Missing storage is revision zero; empty/corrupt storage is an error. No-op detection precedes exhaustion. Failed temp creation/write/sync/close/rename never swaps memory. Registry writes create private directories and 0600 temporary files, sync and close before same-directory rename; cleanup removes only the temporary file. Removal never walks app source/build/data directories.

## Testing strategy

Write table-driven manifest tests first, including every field missing/null/wrong-typed/invalid, nested duplicates/unknown fields, UTF-8/size and string/version boundaries. Registry tests cover semantic equality, unchanged refusal snapshots, lifecycle preservation, capacity, stable storage/modes, reopen/corruption, max sequence, injected failures, blocked readers during persistence, concurrent revision monotonicity, retention and two-host isolation. Run `go test -race ./internal/apps/...`, `go vet ./...`, `go build -o /tmp/builder-3143/pyry ./cmd/pyry` and diff checks. Full-module gates belong to the verifier; no live test is required.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage: `docs/hosted-apps.md` under `Manifest and identity`, describe implemented validation, host/app identity, durable registration revisions and removal retention/tombstones. State that registration alone proves neither publication nor readiness, and manifest-only updates preserve the committed title and active release. Lifecycle/restart revisions and notifications remain pending #3144.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Resolved MUST FIX from re-review: `DisallowUnknownFields` accepts case aliases and cannot establish an exact storage schema. `Open` uses `validStorageSchema`/`storageObject` to require exact mandatory keys and allow only exact optional keys in snapshots, records, tombstones and last-error objects before decoding. Existing files decode into a zero-value snapshot; a missing stored host never inherits the caller's identity. `ValidateManifest` independently validates exact manifest keys. `strictJSON` rejects duplicate keys, nulls and excessive nesting; immutable canonical manifests cannot be mutated through returned snapshots.
- [Tokens/secrets and cryptography] No credentials, randomness or cryptographic operations; IDs are routing identifiers validated by `ParseServerID`, never minted here.
- [File operations] Fixed registry filename beneath a caller-selected absolute private root; manifest paths never become filesystem paths. Atomic rename replaces a registry symlink rather than writing its target. Root selection and protection against a hostile same-OS-account root owner are outside this leaf package (#3139); publication containment belongs to #3125.
- [Subprocesses] No commands run; command arrays must equal the fixed contract values.
- [Network/I/O] No socket access; manifests capped at 16384 bytes. Local registry growth includes permanent tombstones; no arbitrary tombstone cap that would permit ID reuse.
- [Errors/logging] Static validation errors never echo manifest content; no logging or telemetry. Consumers translate package errors to transport responses.
- [Concurrency] One lock and candidate/persist/swap prevent partial observation or rollback races. Interrupted atomic writes leave the old or new full snapshot.
- [Threat model] Host equality prevents foreign registration without restamping. Authentication/viewer isolation and publish journals are OUT OF SCOPE (#3138/#3139, #3123/#3125); registration cannot establish readiness.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-10

## Revisions

- 2026-10-10: formatted acceptance tests reached 499 lines before implementation, exceeding the sketch's test allowance; the finished implementation plus tests and plan is approximately 1120 lines. Actual parentage is #3143 → #3136 → #3121. Per the grandchild rule, applied `needs-human:sizing`, recorded the measurement and hypothetical manifest/storage split on the issue, and continued the assigned scope. No interfaces or behavior changed. The concurrency test caught a host-ID read from the replaceable snapshot outside the mutex; validation now runs inside the same mutex critical section as mutations, consistent with the planned single-lock model.
- 2026-10-10 (verifier finding 1): the initial security review missed Go's case-insensitive struct decoding. Require exact required/optional storage keys before decoding and initialize caller identity only when storage is missing. Re-review corrected the trust-boundary finding above. `TestStorageExactKeys` covers all required omissions and optional omissions, aliases and unknown keys at each storage level; `TestStorageAliasShadowing` covers missing host/revision and later aliases hiding invalid IDs, revisions and last-error values. Both tests proved the reported acceptance failures before the repair and assert rejected files remain byte-identical. No exported interfaces, lifecycle behavior or storage encoding changed.
