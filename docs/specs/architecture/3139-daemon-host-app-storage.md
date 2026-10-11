# Daemon host-app storage and restart isolation

## Files read

- `cmd/pyry/main.go` → `runSupervisor`: daemon lifetime and relay composition.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2`: manager construction and producer cleanup.
- `cmd/pyry/paths.go`, `cmd/pyry/pair.go` → `resolveInstanceDirPath`, `resolveServerIDPath`: sanitized per-instance paths.
- `internal/apps/storage.go`, `registry.go`, `lifecycle.go` → `Open`, `List`, `Subscribe`: committed normalization and synchronous callbacks.
- `docs/knowledge/features/apps-package.md` → lifecycle/concurrency/testing: never reopen to inspect restart evidence; transfer ownership between daemon and seeding registry.
- `internal/relay/v2session_hosted_changes.go`, `v2session_handshake.go` → `HostedAppsChanged`, `negotiateCapabilities`: bounded handoff and complete readiness admission.
- `internal/relay/v2session_hosted_test.go`, `cmd/pyry/pairing_mint_v2_test.go`: authenticated in-process fixtures.
- `internal/e2e/relay_v2_handshake_test.go`, `relay_v2_daemon_test.go`, `relay_v2_stream_announced_reset_test.go`: fake-phone handshake and channel reset fixtures.
- `docs/knowledge/features/e2e-harness.md`, `development-verification.md`: readiness barriers, artifact evidence and scoped checks.
- `docs/specs/architecture/3120-hosted-app-contract.md` → runtime roots, registration and capability vocabulary: authoritative product contract.
- `CODING-STYLE.md`: Go contracts, file size and test conventions.

## Context

Durable registration and relay discovery exist but daemon ownership is missing. Open the store even without relay, preserve channel-independent data, and prove real restart isolation. This changes no hosting readiness or service execution contract and needs no new decision record.

## Design

Add a daemon storage helper resolving an absolute apps root beneath `resolveInstanceDirPath`, loading the persisted pairing/relay server ID, creating/enforcing a private 0700 root, and returning only a fully opened `apps.Registry`. Rejected storage produces a static safe reason and nil registry; existing bytes remain intact. Registry persistence remains the package's atomic 0600 implementation.

Pass the registry through an optional `relayWiring` field. A small manager-construction helper installs discovery and the existing cancellable outbound sender, constructs the manager, then attaches `Subscribe(mgr.HostedAppsChanged)` only after successful construction. It returns the idempotent unsubscribe for relay cleanup. Keep `HostedAppsReady` untouched: production supplies no attestation while supervision (#3122) and resource routing (#3123) are absent. In-process tests supply ready fake dependencies through the config, with no environment bypass.

Overlap: #3163 edits main/relay composition for live readings; these additions are local and do not depend on its changes.

Sizing: one deliverable (daemon-owned durable app infrastructure), four criteria; approximately 650–750 total written lines, zero exported types/interfaces, one production composition call site, no new state-machine reject branches. All five limits remain within their boundaries.

## Concurrency model

No new production goroutine. Registry callbacks synchronously enqueue via `HostedAppsChanged`, without registry re-entry or peer I/O. Attach before manager Run; detach before relay drain. Construction failure attaches nothing. Existing manager and relay goroutines retain their cancellation/join paths. One owner per root; seed only while the daemon is stopped.

## State transitions and identity reuse

| Event | Race-enabled proof |
|---|---|
| Committed registration/lifecycle and removal | `TestDaemonAppsComposition`: authenticated snapshot and ordered events through installed subscription. |
| Repeated cleanup / mutation after detachment | `TestDaemonAppsComposition`: unsubscribe twice, subsequent mutation excluded, fresh list sees commit. |
| Manager construction failure | `TestDaemonAppsCompositionFailure`: no subscription installed. |
| Restart with same host/app identity, then unchanged restart | `TestHostedAppsRestartIsolation`: inspect daemon-written JSON before reopen, changed revisions only. |
| Same app ID on two hosts, one restarting | `TestHostedAppsRestartIsolation`: separate host metadata/data/revisions. |
| Channel stop, clear and idle eviction | `TestHostedAppsChannelIndependence`: unchanged registration/data after harness operations. |

## Error handling

App root/identity/open failures degrade only app availability and log static reason codes; do not log wrapped storage errors or contents. No failed registry enters relay wiring. Existing relay errors still abort startup; subscription is installed only after construction succeeds and cleanup detaches before manager drain.

## Testing strategy

Write failing focused tests first. Unit storage tests cover missing/corrupt/foreign/unreadable roots, modes, sanitized absolute paths and content-free logs. In-process authenticated Noise tests cover ready fake discovery, committed lifecycle/removal, unsubscribe and absent production readiness. Tagged fake-daemon tests seed via apps APIs, inspect raw persisted snapshots after startup, preserve saved bytes and verify restart/two-home/channel isolation. Run race tests for cmd/pyry and focused tagged e2e tests, then go vet ./... and go build ./cmd/pyry. The verifier owns the full-module gate; no live Claude is needed.

## Open questions

None. Registry/sender presence is explicitly insufficient to attest hosting readiness.

## Documentation handoff

Pending documentation stage: `docs/hosted-apps.md` under `Manifest and identity` and `Runtime and layout` must record the per-instance `~/.pyry/<sanitized-instance>/apps` root, persisted host identity, channel-independent retention, restart normalization and monotonic revisions. State that wired registration/discovery infrastructure does not activate hosting capability; production activation still awaits #3122/#3123.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `apps.Open` validates exact storage schema and local persisted host identity before any registry reaches discovery. Remote requests cannot select the root or host.
- [Tokens/secrets] No new credentials; reuse `identity.LoadOrCreate`. Static reason codes omit wrapped errors, storage contents and paths.
- [File operations] Absolute sanitized instance root, 0700 directory; reject a symlink/non-directory root. Registry retains atomic 0600 writes. Concurrent hostile writers under the same OS account remain outside the existing trusted-service model.
- [Subprocesses] No app process or shell execution added; #3122 owns process supervision.
- [Cryptography] Existing authenticated Noise and pairing paths are reused unchanged.
- [Network/I/O] Existing manager caps and cancellable sender are reused; no new network listener or algorithm.
- [Errors/logs] SHOULD FIX incorporated: log only fixed local failure reasons, never the returned storage error.
- [Concurrency] Attach after successful construction, before Run; idempotent unsubscribe outside callbacks before drain avoids registry-lock re-entry and shutdown races.
- [Threat model] Production capability remains withheld; service health and hostile resource containment belong to #3122/#3123. This ticket does not claim readiness from stored running state.

**Reviewer:** builder (self-review per checklist)
**Date:** 2026-10-11
