# Durable app lifecycle and committed changes (#3144)

## Files read

- `internal/apps/registry.go` → `Registry.change`, `cloneSnapshot`: serialized candidate persistence, no-op ordering and detached records.
- `internal/apps/storage.go` → `Open`, `validateSnapshot`, `writeSnapshot`: strict storage validation, bounded revisions and atomic private-file replacement.
- `internal/apps/manifest.go` → `ValidateManifest`, `validText`, `validRelease`: immutable manifests and existing UTF-8/release bounds.
- `internal/apps/registry_test.go`, `storage_test.go` → `testOpen`, `testChange`, `TestRegistryMutations`, `TestStorageExactKeys`: reuse fixtures and preserve invalid-storage rejection.
- `docs/knowledge/features/apps-package.md` → Concurrency and persistence: equal registration must compare manifests, even when committed lifecycle differs; lock host identity reads.
- `docs/specs/architecture/3120-hosted-app-contract.md` → Registration, observed state and safe publication; Errors and lifecycle test obligations: confirmed observations and static error metadata.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md` → transactional persistence, race tests and independent boundary assertions.

## Context

Persist intent and confirmed observations without advertising stored readiness on restart. Ordered whole-record changes let later discovery installation consume committed host state. No process, health, publication journal or relay work belongs here. No decision record is needed. No fetched feature branch overlaps the planned files.

Sizing: one durable lifecycle behavior, four acceptance criteria, approximately 650 written lines (220 production, 350 tests, 80 plan), two exported types, zero production consumer migrations, at most ten distinct rejection branches reusing the store's existing errors and validators. The completed plan remains within all five limits.

## Design

- `Lifecycle` contains only committed title, desired/state, active/pending releases and optional safe `LastError`. `UpdateLifecycle(serverID, appID, fields) (bool, error)` replaces those fields atomically on an existing local identity; identity, manifest and revision cannot be supplied. Use `validateSnapshot` on a candidate, mapping invalid caller fields to package `ErrInvalidLifecycle`. Caller must supply confirmed health observations and static messages, never diagnostics.
- Reuse `Registry.change` for all mutations; compare lifecycle values semantically before checking exhaustion. Registration equality remains manifest equality. Persist the candidate before swapping memory or notifying.
- `Change` carries server/app identity and host revision, plus a detached whole `Record`; a nil record denotes removal. `Subscribe(func(Change)) func()` installs a current-process consumer and returns idempotent unsubscribe. Each consumer gets its own deep copy. No baseline replay or durable event log.
- `Open` delegates to an unexported opener accepting a persistence function for failure injection. Validate storage before normalization. In sorted app-ID order, normalize available records with active releases to starting and all others to stopped. Stamp only changed states, preflight the total remaining revision budget, then persist once before returning the registry. Unchanged open performs no write.

## Concurrency model

One registry mutex serializes mutation, persistence, snapshot exposure, subscription changes and synchronous notification delivery. Callbacks run under that lock, must return promptly and must not re-enter any registry method (including unsubscribe). This avoids unbounded queues, dropped events and delivery goroutines; consumers own any later transport work. No goroutines are added.

## State transitions and identity reuse

| Event | Race-enabled test |
|---|---|
| Repeated lifecycle mutation, repeated registration after lifecycle change, stop and successful cutover | `TestLifecycleMutations` |
| Invalid/foreign/unknown/removed identity; removal cannot revive | `TestLifecycleValidation` |
| Restart mixed observed states; unchanged second reopen; tombstones retained | `TestRestartNormalization` |
| Multiple restart changes with insufficient revisions or failed persistence | `TestNormalizationFailure` |
| Concurrent registration, manifest/lifecycle update and removal; detached records | `TestCommittedChanges` |
| Repeated unsubscribe and new subscription without replay | `TestCommittedChanges` |
| Mutation failure and exhaustion, semantic no-op at ceiling | `TestLifecycleFailureAndExhaustion` |

## Error handling

Return existing identity/foreign-host/not-found/removed/exhaustion errors and new invalid-lifecycle sentinel. Persistence diagnostics are returned only, never inserted into last error. Failed writes leave records, tombstones, revisions and consumers untouched. Invalid storage is rejected without repair; failed normalization returns no registry.

## Testing strategy

Write behavioral tests before production changes and observe failure. Table-driven lifecycle boundaries include UTF-8 byte limits, safe codes, optional fields and running requiring an active release. Check complete records and file bytes, ordered concurrent events, consumer/caller pointer isolation and no notifications before persistence. Update the existing reopen assertion to require stopped-state normalization. Run `go test -race ./internal/apps/...`, `go vet ./...`, and `go build -o /tmp/builder-3144/pyry ./cmd/pyry`. The verifier owns the full-module gate; no live proof is required.

## Open questions

None. The synchronous subscription contract is explicit; later installation is owned by #3138/#3139.

## Documentation handoff

Pending for documentation stage: in `docs/hosted-apps.md` under `Manifest and identity`, update `Durable registration` and `Registration and observed state` to describe implemented desired versus observed state, safe error metadata, ordered committed changes and durable revisions for restart state changes. Replace wording deferring these features to #3144 and the claim that reopen never advances revisions. State that stored running and registration alone prove neither publication nor readiness; health checks and process reconciliation remain separate.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `UpdateLifecycle` accepts only lifecycle fields, checks local host/app identity under the mutex, and reuses `validateSnapshot`; unknown and tombstoned identities cannot be revived. Storage is strictly validated before normalization.
- [Tokens, secrets, credentials] No credentials are generated or consumed. `LastError` is explicit caller-supplied safe metadata; never derive it from returned parser/I/O errors.
- [File operations] Reuse `writeSnapshot`: 0700 private root, 0600 same-directory temporary file, sync/close/rename. IDs never select file paths. Root ownership and symlink policy remain the existing trusted caller contract.
- [Subprocesses] None started; lifecycle reports observations supplied by trusted callers.
- [Cryptography] No keys, nonces or cryptographic changes.
- [Network and I/O] No sockets or transport installation; normalization is one bounded snapshot write, at most 256 live records.
- [Errors, logs, telemetry] Validate static messages with existing 1–160 UTF-8-byte/control-character bounds and contract codes. No new logs; persistence diagnostics stay separate from metadata. Content provenance is a caller obligation, not inferable from message text.
- [Concurrency] One lock spans validation through commit and detached callbacks. No queues or goroutines. Document callback non-reentrancy and prompt-return requirements.
- [Threat model] OUT OF SCOPE: relay authorization and wire mapping belong to #3138/#3139; confirmed health and process generations to #3122; publication/recovery journals to #3125.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-10
