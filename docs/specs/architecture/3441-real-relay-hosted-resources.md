# Real-relay hosted resource acceptance

## Files read

- `internal/e2e/liverelay/liverelay_test.go` → `spawnDaemon`, `buildHello`, `startRelay`, Noise helpers: existing loopback harness and joined cleanup.
- `internal/e2e/hosted_apps_resources_test.go` → `hostedWire`, `hostedResourceHandler`, `TestHostedAppsPairedResources`: encrypted resource and commit/response barrier analogue.
- `internal/e2e/hosted_apps_test.go` → `hostedSeed`: stopped-daemon registry and immutable release setup.
- `internal/e2e/hosted_apps_sql_test.go` → `TestHostedAppsRecoverySQL`: actual-template copy and migration preparation only.
- `internal/e2e/hosted_apps_runtime_test.go` → `TestHostedAppProcess`: subprocess fixture and service identity evidence.
- `internal/relay/v2session_hosted_resources.go` → `runHostedResource`: content-free worker-exit observation.
- `templates/hosted-app/source/src/service/main.ts` → preferences POST/GET: unchanged maintained service, metadata-only 204.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Asset/API fields and HTTP semantics; Bounded encrypted transfers and retirement: normative byte, credit and generation contracts.
- `docs/knowledge/features/apps-package-lifecycle-and-restart-state.md` § Lifecycle and restart state: readiness uses the selected Node; app roots reject symlink ancestors; migration must precede ownership.
- `docs/knowledge/features/e2e-harness.md`, `docs/knowledge/features/development-verification.md`: isolated processes, actual binary provenance and counted evidence.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: bounded I/O, cancellation and untrusted relay boundary.

## Context

#3440 activated the production resource bridge. This single acceptance gate proves it through the existing real relay, with synthetic deterministic disconnects and the maintained compiled template on supported actual Node. No production or relay protocol changes, shared harness or decision record are needed.

Sizing: approximately 730 written lines including plan, zero exported types, two existing helper callers remain source compatible through variadic inputs, four acceptance criteria and fewer than ten fixture reject branches. No concurrent feature branch touches `internal/e2e/liverelay` at planning time.

## Design

Add package-local files behind `e2e_liverelay`. Extend `spawnDaemon` with optional environment inputs and `buildHello` with optional capabilities. Seed one registry/release before starting the daemon, retaining canonical temp paths and private permissions. A phone authenticates with existing paired-device/Noise helpers, verifies hosted capability acknowledgement and refreshes discovery on every connection. Resource assertions inspect decrypted raw envelope keys as well as correlation, app/release, HTTP status, header allowlist, exact chunk order/count/bytes/digest. Explicit no-frame windows prove credit gating and metadata-only empty completion.

The synthetic service re-executes the race test binary through a local Node stand-in. Its mutation writes persisted bytes and an invocation record before signalling a commit barrier; all response bytes remain withheld. A fresh read has its own barrier, so releasing the retired response is deterministically ordered while its replacement ID is active. No write is replayed. Service PID, host/app identity and data remain stable. Worker exit logs are awaited after each disconnect.

The template arm requires explicit `HOSTED_APP_NODE` and `HOSTED_APP_TEMPLATE_BUILD` inputs, failing rather than skipping when missing. Validate actual Node in the supported range; copy unchanged compiled service, migration files and package metadata. Run migration and seed defaults before daemon spawn. A PATH launcher selects that exact runtime without changing template code. Encrypted POST selects nondefault preferences, verifies empty 204 metadata without credit/completion, then encrypted GET verifies exact JSON bytes. Build provenance and counted commands belong in the PR.

## Concurrency model

Existing relay/daemon owners join processes on cleanup. Synthetic HTTP handlers use a mutex for persisted mutation state, bounded polling for barriers and a signal-driven server shutdown. Phone operations are serial. Cleanup releases barriers before stopping the daemon; no handler can wait indefinitely. Production resource goroutines remain owned by their Noise connection generation.

## State transitions and identity reuse

| Event | Required race test |
|---|---|
| Partial multi-chunk read disconnect, worker exit, same ID after fresh handshake/discovery | `TestLiveRelay_HostedResources` |
| Mutation commits, disconnect before reply, retired worker exits, replacement read reuses write ID, late response released while replacement is active | `TestLiveRelay_HostedResources` |
| Repeated reconnect retains service, data and host/app identity; write invocation count increases exactly once | `TestLiveRelay_HostedResources` |
| Empty POST completes at metadata, subsequent read on same authenticated connection | `TestLiveRelay_HostedTemplate` |

## Error handling

Missing required runtimes/build inputs fail the new acceptance tests. Existing optional relay resolution retains its skip diagnostic, which establishes no acceptance. Every wait is bounded and fixture process shutdown joins handlers. Failures report synthetic fixture data and content-free process diagnostics, never pairing tokens.

## Testing strategy

Compile the new tests before adapting harness boundaries to observe the expected missing-interface failure. Run both named arms with `-race -count=1`, a separately race-built daemon and real loopback relay. Run the complete touched `e2e_liverelay` package under race, `go vet ./...` and `go build` to scratch. Retain exact revisions, commands, Node version, template provenance and executed/passed/failed/skipped counts in the PR. No live Claude is required. The verifier owns the full-module gate.

## Open questions

None. Node 24.15.0 is downloaded to scratch because the host default is unsupported Node 25.8.0; the sibling real-relay checkout is available.

## Documentation handoff

Pending documentation stage: update `docs/hosted-apps.md`, “Client resource bridge / Capability and envelope vocabulary” and “Implementation and validation ownership”; `docs/protocol-mobile.md`, “Hosted apps (v2)”; and `docs/knowledge/features/apps-package-lifecycle-and-restart-state.md`, “Lifecycle and restart state”. Add counted real-relay/template evidence, reconcile validation deferrals with #3440's implemented support/readiness rules and counted fake-relay evidence, and preserve native viewer, publisher and live-channel acceptance as downstream work without claiming those gates passed.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: decrypted resource assertions reject conversation metadata keys and forbidden headers; relay sees existing encrypted routing frames only. No production trust boundary changes.
- Tokens: existing paired-device setup generates disposable credentials under isolated HOME; never print tokens or private keys.
- Files: fixture roots are canonical, directories 0700/files 0600; registry writes and migrations occur only before daemon ownership. Fixture copy rejects symlinks and nonregular files.
- Subprocesses: executable paths use `exec.Command`; the fixture PATH launcher quotes fixed absolute paths. Existing daemon and relay cleanup joins processes; synthetic child shuts down on SIGTERM.
- Cryptography: use existing Noise initiator/cipher states and stdlib SHA-256, with fresh Noise state per connection and no custom cryptography.
- Network/I/O: loopback-only relay/backend, existing bounded WebSocket reader, fixed small bodies, HTTP timeouts and finite barrier deadlines; assert one-credit-at-a-time output.
- Errors/logs: only synthetic public response bytes may appear in assertions; worker observations contain event/connection identifiers, no content or credentials.
- Concurrency: mutex-protected synthetic mutation and filesystem barriers; release and join pending handlers during cleanup. Late old replies must never satisfy a reused ID.
- Threat model: authenticated encrypted resource acceptance covers content blindness and connection-generation fencing. Native renderer/network isolation remains desktop #1927/mobile #2063; publication remains #3125 (OUT OF SCOPE).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15
