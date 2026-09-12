# #2389 — Bind local pairing minting to daemon relay state

## Files read

- `cmd/pyry/main.go` → `runSupervisor` — owns the post-relay, pre-`Serve` control-server installation window.
- `cmd/pyry/relay.go` → `startRelay`, `startRelayV2` — loads the active server id, registry, static key, and resolved relay URL and constructs the process-local pairing minter.
- `cmd/pyry/pairing_mint_v2.go` → `pairingMinterV2`, `newPairingMinterV2`, `MintPairing`, `auditMint` — existing daemon-state holder and remote authenticated mint contract.
- `cmd/pyry/pair.go` → `mintDevice`, `mintRequest`, `registryGrants` — shared CSPRNG, fallback naming, timestamping, locked load/mutate/save, and grantor recheck path.
- `cmd/pyry/pairing_mint_v2_test.go` → `newMintFixture`, `TestPairingMinterV2_PermittedMint`, `TestPairingMinterV2_RevokedGrantorIsRefusedAtWriteTime`, `assertMintLogIsClean` — current proof of payload provenance, privilege narrowing, write-time revocation, and secret-free logs.
- `cmd/pyry/pair_lock_test.go` → `holdPairLock`, `freezeRegistry` — deterministic lock-timeout and no-write test patterns.
- `internal/control/server.go` → `SetPairingProvider`, `handlePairingMint` — narrow local provider seam and fixed client-visible error projection.
- `internal/control/client.go` → `MintPairing` — client operation consumed by the two-socket integration proof.
- `internal/control/pairing_test.go` → `startServerWithPairingProvider`, `TestServer_MintPairing_ErrorProjection` — established control socket/provider test shape and redaction assertions.
- `internal/devices/registry.go` → `Load`, `Registry.Save` — path-only errors, hash-only records, 0600 atomic persistence, and the rename commit point.
- `internal/debugbundle/bundle.go` → `Assemble` — diagnostic bundles copy the daemon log snapshot without synthesizing credential-bearing metadata.
- `docs/knowledge/features/control-plane.md` → “Server Construction”, “Handshake Deadline”, “Testing” — current optional-provider and fixed-error contracts established by #2388.
- `docs/knowledge/features/pyry-pair-command.md` → “operation order”, “Token visibility”, “Concurrency model” — shared mint ordering and file-lock guarantees.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-mint-pairing-pairingminter-seam.md` → `pairingMinterV2` contract — remote privilege, write-time revocation, and unprivileged-device invariants that must remain unchanged.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface”, “Prove that tests distinguish the change” — requires construction-site and independent-branch proof.
- `docs/protocol-mobile.md` → “Pairing flow”, “Security model” — bearer visibility, per-device revocation, relay confidentiality, and static-key handling constraints.

## Context

Ticket #2388 added an optional `pairing.mint` control operation, but no daemon
provider installs it. The daemon already constructs `pairingMinterV2` only after
it has loaded the identity, registry, and static key used by its active relay leg.
Returning a narrow closure from that construction point lets `runSupervisor`
install exactly that capability on its control server. It avoids re-reading saved
configuration whose values may differ from the running process and avoids a
second registry instance.

This is one independently checkable deliverable. The planned change modifies
three production files, adds no exported type, updates two direct consumers of
relay-start return signatures, has three acceptance criteria, and introduces no
new state-machine reject branch. Expected written work is approximately 450–550
lines including tests and this plan, within the ticket's estimate and all six
one-ticket boundaries. The refreshed remote-branch check found no overlap on the
planned files.

No ADR is warranted: this completes the provider seam and ownership decision
already recorded by #2388 rather than creating a new architectural boundary.

## Design

### One local capability over the existing minter

Add an unexported `localPairingProvider` function type with the same two inputs
and `(string, error)` result as `control.Server.SetPairingProvider`. Add a local
method on `pairingMinterV2` that:

1. calls `mintDevice` with the minter's existing `devicesPath`, the bounded
   request-path lock wait, the supplied device label and permission choice, and
   no remote grantor hash;
2. returns an empty string on every failure after emitting one safe daemon log;
3. encodes a successful `mintedDevice` from the minter's existing server id,
   relay URL, and static public key; and
4. emits one success event containing no token, hash, encoded pairing, or
   caller-provided label.

Factor the small payload construction into a private method shared by the local
and remote success paths. The remote `MintPairing` retains its privilege gate,
in-lock grantor recheck, literal `allowRemotePermissions: false`, outcome mapping,
and audit behavior. The shared encoder prevents the two daemon mint surfaces
from drifting on identity, relay, or key provenance.

The local provider intentionally accepts the requested permission boolean. The
Unix control socket is mode 0600 and represents the host operator's authority,
matching `pyry pair --allow-remote-permissions`; it is not the remote phone path.

### Relay-to-control wiring

Extend `startRelayV2` to return `pairingMinterV2`'s local method as a
`localPairingProvider` after the v2 manager and its supporting services have been
constructed successfully. Extend `startRelay` to carry that closure outward.
Every startup failure returns a nil provider, and the relay-disabled early return
also returns nil.

`runSupervisor` receives the provider alongside the existing relay cleanup and
fan-out hooks. It calls `ctrl.SetPairingProvider` between `control.NewServer` and
`ctrl.Listen`, so there is no accepting window in which a relay-enabled daemon
has not yet installed the provider. Passing nil when relay is disabled preserves
the optional control seam and yields #2388's fixed not-configured response.

No identity, key, relay, or registry file is loaded again. The closure points to
the same minter instance already installed as the remote session manager's
`PairingMinter`, so both entry points share immutable provenance and `mintDevice`.

## Concurrency model

No goroutine or channel is added. The local provider runs synchronously on the
control server's existing per-connection goroutine. It takes only the existing
per-registry `devices.WithLock` region inside `mintDevice`; the control server
releases `Server.mu` before invoking it. The lock order remains file lock then
`Registry.mu`, and the finite request-path lock wait bounds contention.

Shutdown remains unchanged: `Server.Serve` drains in-flight handlers and the
relay cleanup drains the session manager. The provider closure holds immutable
configuration plus a logger and owns no lifecycle resource.

## Error handling

- Relay disabled or setup not completed: no provider is installed; the client
  receives `pairing.mint: provider not configured` and no pairing.
- Lock acquisition, registry load, or registry save failure: the local method
  logs one path-capable but credential-free daemon diagnostic, returns an empty
  pairing plus an internal error, and `handlePairingMint` projects it to the
  fixed `pairing.mint: operation failed` response with no result field.
- Token generation failure follows the same fixed error path. `mintDevice`
  returns no `mintedDevice`, so no token is encoded, logged, or returned.
- A persistence failure occurs before payload encoding. Because `Registry.Save`
  commits at its final rename and returns no later error, an error cannot leave a
  returned credential whose hash was not durably installed.
- Success is the sole path returning the encoded plaintext bearer. Logs contain
  only fixed event names; diagnostic bundles inherit that credential-free log
  snapshot.

The local provider does not return the underlying error to a client. Host paths
remain confined to daemon diagnostics, while tokens, token hashes, pairing
strings, and their prefixes remain absent from both diagnostics and errors.

## Testing strategy

- First add a two-control-socket test using two local minters with distinct
  server ids, relay URLs, static keys, and registry paths. Mint through each
  socket, decode both results, and assert each payload carries only its targeted
  daemon values and only its matching registry contains the returned token hash.
- Use the same table to cover both permission values, an explicit device label,
  the generated fallback label, the exact redemption window, and clean daemon
  logs/diagnostic-bundle log members.
- Add a relay-disabled test asserting `startRelay` returns a nil local provider
  and that a control server left with that result returns no pairing.
- Add control-round-trip failure cases for held-lock timeout, malformed registry
  load, and save refusal. Each must return the fixed operation error, no pairing,
  no host-path detail to the client, no newly usable registry record, and no
  credential material in the daemon log or assembled diagnostic bundle.
- Keep the existing remote tests as regression coverage for write-time grantor
  revocation, literal unprivileged creation, and remote error redaction.

RED is the new focused local-provider tests failing because there is no local
method/provider return or installation. GREEN is `go test -race ./cmd/pyry/...`.
The role-owned gate then runs `go vet ./...` and `go build ./cmd/pyry`.

## Open questions

None. The ticket and #2388 already decide the permission authority, provider
shape, fixed control errors, and documentation ownership.

## Documentation handoff

Pending for the documentation stage:

- Update `docs/knowledge/features/control-plane.md`, sections “Server
  Construction” and “Testing”, to state that relay-enabled daemons install
  `SetPairingProvider` from the in-process `pairingMinterV2`, relay-disabled
  daemons leave it nil, and local failure details remain daemon-only.
- Update
  `docs/knowledge/features/v2-session-manager-state-machine-inbound-mint-pairing-pairingminter-seam.md`,
  the `pairingMinterV2` ownership and shared-mint sections, to record the local
  control sibling while preserving the remote write-time revocation and
  unprivileged-device contract.
- Update `docs/knowledge/features/pyry-pair-command.md`, “operation order” and
  “Token visibility”, to include local control as a third caller of `mintDevice`
  whose successful bearer egress is the control response rather than stdout or
  an AEAD relay frame.

No pairing-format or protocol-reference change is required.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `handlePairingMint` remains the sole local
  request boundary; the request can choose only a label and permission boolean,
  while the closure supplies every daemon-authored value. The mode-0600 socket
  treats the same host user as the operator authority.
- [Tokens, secrets, credentials] SHOULD FIX — the new success and failure logs
  must use fixed event text and must not include the supplied label, plaintext
  token, token hash, encoded pairing, or any prefix/length derived from them.
  Tests inspect both direct logs and diagnostic-bundle log members.
- [Tokens, secrets, credentials] No further findings — `mintDevice` retains
  `crypto/rand`, hash-only persistence, the existing redemption window, and
  failure-before-encode ordering; existing per-device revocation continues to
  govern the remote mint.
- [File operations] No findings — no request value becomes a path. The provider
  closes over `resolveDevicesPath`'s sanitized instance path and reuses
  `devices.WithLock` plus `Registry.Save`'s 0600 atomic temp-file/rename contract.
- [Subprocess execution] Not applicable — this path launches no subprocess and
  passes no request value to a shell or command.
- [Cryptographic primitives] No findings — the change adds no primitive, does
  not handle the private static key, and reuses the existing CSPRNG, SHA-256
  token hashing, and public-key encoding paths.
- [Network and I/O] No findings — #2388's bounded line-framed Unix control
  exchange, per-connection deadlines, and synchronous provider contract remain
  unchanged. The local result never enters the relay leg.
- [Errors, logs, telemetry] SHOULD FIX — provider errors may retain a host path
  only inside credential-free daemon diagnostics; `handlePairingMint` must keep
  projecting every provider error to its fixed response and discarding any
  returned pairing. Tests cover lock, load, and save failures independently.
- [Concurrency] No findings — one existing file lock covers load, optional
  grantor recheck, append, and save; no new lock, goroutine, or ordering edge is
  introduced.
- [Threat model alignment] No findings — local minting deliberately grants the
  mode-0600 host operator the same permission choice as `pyry pair`. It neither
  widens remote phone minting authority nor exposes the bearer to the relay, so
  the protocol security model's token-leak, relay-MITM, denial-of-service, and
  static-key-compromise postures are unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-12

## Revisions

### 2026-09-12 — verifier rework

- Added a structural production-wiring guard in `TestLocalPairingProviderWiredFromRelayConstructionToControl` that reads the actual `startRelayV2`, `startRelay`, and `runSupervisor` AST. Together with the control-socket behavior test, it proves the successfully constructed minter's local method is carried through both relay return layers and installed on the control server; deleting or substituting any link fails the guard.
- Strengthened the malformed-registry load case with a distinctive credential-like sentinel. The test now proves the malformed bytes remain unchanged and that neither the daemon log nor diagnostic-bundle log member contains the sentinel.
- Corrected the shared-mint contract comments in `mintRequest`, `mintDevice`, and the `PairingMint` relay wiring to name all three callers. The remote phone path remains the only caller supplying a grantor hash and still passes literal false for remote permissions.
