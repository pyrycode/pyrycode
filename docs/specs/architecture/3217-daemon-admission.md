# Private daemon relay admission

## Files read

- `internal/config/config.go` → `Config`, `Load`: additive defaults and unsafe parser wrapping.
- `internal/config/memory.go` → `UpdateMemory`, `memoryObject`: raw-value preservation and atomic replacement.
- `cmd/pyry/main.go` → `runSupervisor`: startup config and daemon/control lifecycle.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelay`: loaded identity, relay inputs and shutdown classification.
- `cmd/pyry/claude_account_startup_test.go` → `startAccountInstance`: existing hermetic daemon startup/stop pattern.
- `internal/control/client.go` → `Status`, `Logs`, `Stop`: local observable service.
- `internal/relay/connection_admission_test.go` → `TestAdmissionHeadersAndReconnect`, `TestAdmissionUnauthorizedTerminal`: library delivery, frame hygiene and terminal error proofs.
- `internal/transport/upgrade_rejection_test.go` → `TestUpgradeRetryRecovery`; `internal/transport/wssclient_test.go` → `TestFatalCloseThreshold_PersistentConflictGoesFatal`: retained recovery policy.
- `docs/protocol-mobile.md` § Authentication → Binary → relay → Daemon admission (declared): authoritative storage, snapshot, rejection and rollout contract.
- `docs/knowledge/features/config-package.md` § Load semantics and UpdateMemory: preserve additive defaults and unknown raw numbers.
- `docs/knowledge/features/relay-package.md` § Reconnect semantics; `relay-package-consumers.md`: keep conflict shutdown separate; comments break alignment groups for local wiring edits.
- `docs/knowledge/features/development-verification.md` § Verify inherited premises: reuse merged library proofs.
- `CODING-STYLE.md`: local edits, value-free errors, scoped tests and joined goroutines.

## Context

Merged #3215/#3216 already deliver a selected token and terminate on HTTP 401. This daemon companion supplies private startup configuration and an operator diagnostic while retaining local service. Relay #167 and explicit operator activation remain separate; enforcement defaults off. No new decision record is needed.

## Design

Add optional `Config.RelayAdmissionTokens map[string]string`. Validate the raw field before typed overlay decoding: absent is nil, `{}` is valid, null/non-object or any token outside exactly 64 lowercase ASCII hexadecimal bytes fails. Preserve exact keys and bytes, validating all entries regardless of selection.

A shared config reader opens the parent directory and reads the file relative to that directory handle. Obtain file and parent metadata from these same open handles, binding validation to the bytes read. When the map is present require a regular 0600 file and a 0700 directory, both owned by `os.Geteuid()`. Followed filesystem targets must satisfy those checks; no permission repair occurs. Legacy files without the map retain their storage compatibility. Use existing `x/sys/unix` for descriptor-relative opening; no dependency is added.

`Load` discards every JSON decoding cause and returns static text. `UpdateMemory` uses the same admission validation/storage boundary, retains raw admission/unrelated values, and discards parser causes in root and nested memory objects. Existing atomic writes remain unchanged; callers still serialize updates.

Pass the loaded admission map in `relayWiring`. Immediately after loading the instance identity, `startRelay` selects that exact key into `relay.Config.AdmissionToken`. The library snapshots the string into its headers for every attempt. No flag, environment override, identity fallback, URL or pairing/frame field is introduced.

In the existing `conn.Wait` classifier, handle `relay.ErrAdmissionFailed` separately with static text telling the operator to correct configuration and restart. Do not attach the error or cancel daemon context. Existing conflict shutdown, retry policy and phone/Noise wiring remain unchanged. No overlapping feature branch touches the planned production files.

Sizing after plan: one deliverable, approximately 720 total written lines (150 production, 490 tests/helpers, 80 plan), zero new exported types/interfaces, one composition consumer, five acceptance criteria, at most ten rejection branches. Within all five limits; comparable to the supplied #3116 analogue.

## Concurrency model

No new production goroutines or locks. Startup configuration is read synchronously and never mutated; selection becomes a library-owned string. Existing relay cancellation/cleanup joins the wait classifier and v2 drains. Tests stop/join daemon processes and close loopback sockets, with mutex-protected relay attempt records.

## State transitions and identity reuse

| Event | Race-enabled proof |
|---|---|
| Config missing/absent/empty/invalid and repeated memory update | `TestRelayAdmissionConfig`, `TestRelayAdmissionStorage`, `TestRelayAdmissionUpdateMemory` |
| Two named instances select independently; selected entry absent | `TestDaemonAdmissionSelectionAndRotation`, `TestDaemonAdmissionRejection` |
| File edit followed by reconnect retains startup selection | `TestDaemonAdmissionSelectionAndRotation` |
| Overlap accepts old/new; revocation drops sockets then rejects old | `TestDaemonAdmissionSelectionAndRotation` |
| Correct config and restart same instance loads new selection | `TestDaemonAdmissionSelectionAndRotation` |
| Initial or reconnect 401, supplied or absent token, repeated observation | `TestDaemonAdmissionRejection` checks counted attempts, local status and diagnostic |

## Error handling

Admission shape/storage and JSON errors are static and retain no decoder cause. Read errors retain ordinary filesystem classification. The terminal admission diagnostic contains neither credential/map nor peer text. A 401 ends only the relay leg; local status/stop remain usable until explicit daemon restart.

## Testing strategy

Write config and daemon loopback tests first and observe failure. Table-test malformed map/entries, unselected entries, exact keys, file/directory permissions and effective ownership (metadata seam where changing actual ownership is unavailable), and legacy compatibility. Inspect errors recursively and confirm rejected updates preserve bytes; successful updates preserve complete maps and unrelated raw values. Start real `runSupervisor` with existing inert child stubs, inspect every request header and URL, logs/status and local pairing payload. Exercise old/new overlap, disconnect, revocation, correction/restart and first-401 terminal behavior. Reuse library frame-hygiene and transport recovery/4409 proofs.

Run race tests for `internal/config` and `cmd/pyry`, focused existing relay/transport recovery tests, `go vet ./...`, and `go build` into scratch. Verifier owns the full-module gate. No live Claude/Relay execution or artifacts are required.

## Open questions

None.

## Documentation handoff

Pending documentation stage:
- In `docs/knowledge/features/config-package.md`, add **Relay admission credentials** and reconcile **Load semantics**: optional exact-identity map, missing versus malformed/present-empty behavior, effective-user ownership/mode checks, secret-safe load/update errors, map-preserving memory updates, no secret flag/env override and restart-only application.
- In `docs/knowledge/features/relay-package.md`, reconcile **Daemon admission security boundary (declared)** and **Reconnect semantics** to state that configured daemon delivery and local-service-preserving 401 handling are implemented; Relay #167 and operator activation remain separate.
- In `docs/protocol-mobile.md`, under **Daemon admission (declared)**, update implementation ownership to name this completed daemon companion, retaining the off-mode production default, Relay #167 prerequisite and operator-only rollout checklist. Do not claim enforcement or live rollout verification.

## Security review

**Verdict:** PASS

- [Trust boundaries] Raw config field validation precedes typed decoding; every supplied token is validated. `startRelay` selects only the loaded exact identity; 401 classification trusts the library sentinel alone.
- [Tokens] Plaintext private storage requires effective ownership and exact modes even for `{}`. Restart reloads rotation; no token generation, equality authentication or secret override is added.
- [File operations] Descriptor-relative reads and handle metadata bind ownership/mode checks to bytes and parent. Followed targets must be private; same-user filesystem replacement remains inside the trusted operator boundary. Atomic memory writes and caller serialization remain. No caller-controlled path joins are added.
- [Subprocesses] No production subprocess or environment-secret changes. Hermetic daemon tests use inert existing stubs.
- [Cryptography] No primitives, comparisons or keys change. Production TLS and Noise remain; Relay token comparison/enforcement belongs to Relay #167.
- [Network and I/O] Reuse transport limits, timeouts and validated admission header; no new listener or frame surface.
- [Errors, logs, telemetry] SHOULD FIX, included in design: discard parser causes for all config decode errors, including unrelated/nested fields, rather than sanitizing outer text. Admission diagnostic is static without peer error attributes.
- [Concurrency] No new shared mutable production state; existing cancellation joins all workers. Test socket/attempt records are synchronized and all daemons joined.
- [Threat model] Relay compromise/stolen-token risks and explicit enforcement activation remain with Relay #167/operator rollout. This slice preserves phone authorization and encrypted transport, and does not claim enforcement.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-12
