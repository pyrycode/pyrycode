# Paired app resource activation

## Files read

- `cmd/pyry/daemon_apps.go` → `newDaemonAppsManager`, `newDaemonAppsOwner`: instance-owned registry/owner composition and subscription cleanup.
- `cmd/pyry/main.go` → `runSupervisor`; `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2`: ownership, cancellable outbound delivery and shutdown ordering.
- `internal/apps/owner.go` → `NewOwner`; `internal/apps/runtime.go` → `prepareRelease`, `supportedNode`: selected executable and runtime support rules.
- `internal/apps/asset.go`, `internal/apps/api.go`, `internal/apps/response.go` → resource preparation: confined generation-bound handles and bounded stable response ownership.
- `internal/relay/v2session_hosted.go`, `v2session_hosted_resources.go`, `v2session_handshake.go`: negotiation, generation retirement, credit and cancellation.
- `cmd/pyry/daemon_apps_test.go` → `TestDaemonAppsComposition`; `internal/e2e/hosted_apps_test.go`, `hosted_apps_runtime_test.go`: stopped seeding, synthetic Node process and paired-phone harness.
- `docs/specs/architecture/3120-hosted-app-contract.md` §§ Capability and envelope vocabulary, Asset/API fields and HTTP semantics, Bounded encrypted transfers and retirement: normative wire contract.
- `docs/knowledge/features/apps-package-lifecycle-and-restart-state.md` § Lifecycle and restart state: retain exclusive instance ownership through joined owner shutdown; service health is distinct from hosting infrastructure readiness.
- `docs/knowledge/features/apps-package-resource-serving.md`, `development-verification.md`, `e2e-harness.md`: exact byte proofs, retirement ownership and race-built child binary injection.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: testing and trust boundaries.

## Context

Resource backends and encrypted transfers exist but production installs discovery alone. Activate them on the daemon's existing registry, host identity and owner. Synthetic runtime/fake-relay acceptance belongs here; actual Node/template and real-relay proof belongs to #3441. No new decision record is needed.

## Design

Expose `Owner.CheckRuntime(ctx) error` to attest the exact absolute executable already selected by the owner. Reuse the existing supported-version predicate, a five-second context bound, empty child environment and bounded output. Run this probe once during manager construction, before message processing starts; cache its result in a constant nonblocking `HostedAppsReady` callback. No handshake or resource request performs a readiness probe. An empty registry or unhealthy/stopped application does not affect the infrastructure attestation.

Pass the existing owner through `relayWiring` to `newDaemonAppsManager`. Install discovery, both resource providers and context-aware delivery together; readiness requires nonnil registry/owner/delivery plus successful runtime attestation. Missing components withhold capability without preventing normal manager use. Keep subscriptions and cleanup ordering intact. Shared-file overlap with #3436 is in unrelated push wiring; #3240 edits different owner tests.

Extend composition tests for supported and degraded dependency/runtime combinations. Replace the production-unsupported premise with explicit degraded runtime cases. Add a resource acceptance test using stopped seeding, immutable HTML/module/binary fixtures and a synthetic service that records mutations and gates their responses using files in its private data directory. Tests never write registry state while the daemon runs.

Sizing: one observable activation deliverable with its acceptance proof; approximately 780 written lines, no exported types, three manager consumers, four acceptance criteria, no new protocol reject branches. This is a grandchild (#3123 → #3248 → #3440). Existing owner/relay state machines are reused.

## Concurrency model

The bounded runtime subprocess runs before manager `Run`, using the daemon startup context. It has no background lifetime. Existing owner workers remain daemon-owned. Existing relay workers stage resources off the message loop and retire on cancellation/disconnect. Test HTTP handlers use synchronized mutation state and a cancellation-aware response barrier; test service shutdown closes the server and joins its child.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Empty/stopped registry, supported/unsupported/missing runtime or delivery | `TestDaemonAppsComposition`, `TestDaemonAppsRuntimeReadiness`, `TestHostedAppsProductionReadiness` |
| Resource completion, zero-body mutation, readback, invalid request | `TestHostedAppsPairedResources` |
| Credit wait followed by cancellation while unrelated channel runs | `TestHostedAppsPairedResources` |
| Disconnect during partial read, reconnect/discovery and reused ID | `TestHostedAppsPairedResources` |
| Commit before response, disconnect, reconnect/discovery and reused ID | `TestHostedAppsPairedResources` |

## Error handling

Runtime failure caches false readiness and exposes no subprocess output or paths over the wire. Existing app validators map unavailable/stale/hostile requests to contract errors. Existing retirement disposes responses and reservations without stopping services or replaying uncertain mutations.

## Testing strategy

Write failing composition/runtime and fake-relay assertions first. Run focused proofs uncached with race instrumentation and a separately race-built daemon through `PYRY_E2E_BIN`. Check exact response status, allowed headers, bytes, digest, zero chunks for 204 and absence of conversation metadata. Reuse old IDs only after reconnect, negotiation and fresh discovery. Count the backend mutation once across the deterministic commit/response barrier. Run touched-package race tests, `go vet ./...` and `go build ./cmd/pyry`; the verifier owns the full-module gate. Record revision, commands, synthetic executable identity and executed/passed/failed/skipped counts in the PR.

## Open questions

None.

## Documentation handoff

Pending documentation stage: update `docs/hosted-apps.md`, “Client resource bridge / Capability and envelope vocabulary” and “Implementation and validation ownership”; `docs/protocol-mobile.md`, “Hosted apps (v2)”; and `docs/knowledge/features/apps-package-lifecycle-and-restart-state.md`, “Lifecycle and restart state”. Replace resource-routing/production-attestation deferrals with implemented support, infrastructure/runtime readiness rules and counted fake-relay evidence. Explicitly leave real-relay/template validation to #3441. Preserve native viewer, publisher and live-channel acceptance as downstream work without claiming those gates passed.

## Security review

**Verdict:** PASS

- Trust boundaries: reuse `routeHosted` paired authorization, capability gating and host identity; install the same owner as supervision, never a client-selected target.
- Tokens/cryptography: existing paired-device Noise helpers and production authentication remain authoritative; no new keys or secret persistence.
- Files: `Owner.PrepareAsset` retains confined no-follow immutable-build access; fixtures are prepared only while stopped. Barriers are confined to synthetic private data.
- Subprocesses: `CheckRuntime` probes only the owner's selected absolute executable with fixed argv, empty environment, bounded output and cancellation. No wire value enters argv.
- Network/I/O: existing asset/API validators, response budgets, credit limits and loopback generation ownership remain enforced. SHOULD FIX: acceptance must include URL/authority attempts and forbidden headers, plus exact opaque query bytes.
- Errors/logging: cache only a boolean for runtime support; no raw probe output, backend headers or paths reach app metadata/errors.
- Concurrency: existing connection generation retirement owns cancellation; SHOULD FIX: prove ID reuse after both partial-read disconnect and committed-write disconnect using a deterministic response barrier.
- Threat model: untrusted relay still sees only Noise ciphertext; paired callers retain host-level app authority. Actual native view isolation and real-relay/template evidence are OUT OF SCOPE here, owned by downstream native tickets and #3441.

**Reviewer:** builder (self-review)
**Date:** 2026-10-15

## Revisions

- 2026-10-15: the asset backend file is `internal/apps/assets.go`. Keep the bounded runtime probe in `internal/apps/runtime_check.go` to avoid expanding the existing launch preparation. Composition notifications now include real owner observations, so assert committed revisions rather than assuming registration/removal always consume exactly two revisions. The commit/response barrier holds the old handler after cancellation and releases it only after the new connection has submitted its reused-ID read.
