# #2393 — Select a running service before pairing

## Files read

- `cmd/pyry/pair.go` → `parsePairArgs`, `runPair`, `runPairDefault`, `mintDevice` — current bare issuance still loads saved identity and credential state locally, while the shared daemon mint primitive already exists.
- `cmd/pyry/main.go` → `defaultName`, `resolveSocketPath`, `sanitizeName`, `helpText` — socket-name resolution, environment defaults, terminal-safe service-name alphabet, and the public command summary.
- `internal/control/client.go` → `Status`, `MintPairing`, `request` — the existing bounded one-request control clients needed for discovery and daemon-owned issuance.
- `internal/control/protocol.go` → `StatusPayload`, `PairingPayload`, `PairingResult` — status liveness and the deliberately narrow local mint contract.
- `internal/e2e/harness.go` → `StartInWithEnv`, `RunBareIn`, `childEnv` — real-binary daemon startup, isolated homes, and the environment scrub that the `PYRY_NAME` test must override explicitly.
- `internal/e2e/pair_test.go` → `TestPair_E2E`, `TestPairList_E2E`, `TestPairRevoke_E2E`, `decodePairPayload` — the obsolete daemon-free issuance proof and the offline subcommand regressions that need fixture seeding instead.
- `internal/e2e/relay_v2_mint_pairing_test.go` → `TestRelayV2_MintPairing` — an existing in-daemon CLI mint that must expose its harness socket at the name-based path selected by the new command.
- `internal/e2e/internal/paireddevice/paireddevice.go` → `Setup` — the shared pre-daemon fixture for saved identities and offline list/revoke setup.
- `docs/specs/architecture/2388-local-pairing-code-mint.md` → `MintPairing`, `handlePairingMint` — fixed control errors, whole-exchange timeout, and bearer-redaction decisions inherited here.
- `docs/specs/architecture/2389-bind-local-pairing-mint-to-relay-state.md` → `pairingMinterV2.MintLocalPairing`, `runSupervisor` — proof that the installed provider closes over the running daemon's identity, key, relay, and registry.
- `docs/knowledge/features/pyry-pair-command.md` → bare operation order, token visibility, tests — current offline behavior and security invariants that this ticket replaces only for issuance.
- `docs/knowledge/features/development-verification.md` → change-surface and distinguishing-test guidance — requires binary tests whose no-write assertions separate discovery from minting.
- `docs/protocol-mobile.md` → Security model — bearer-token, static-key, relay, and terminal-output threats relevant to the new CLI boundary.

## Context

Bare `pyry pair` currently derives its instance from `defaultName`, so a shell's
`PYRY_NAME` can select dormant saved state even when another service is the only
one running. The daemon now exposes `pairing.mint`, whose provider owns the exact
identity, static key, relay URL, and device registry used by its active relay leg.
Issuance should select that live capability and never reconstruct it from saved
files in the CLI. The offline `list`, `revoke`, and `preflight` paths retain their
existing state-directory behavior.

No ADR is warranted. This change moves an existing caller to the ownership seam
already established by #2388 and #2389; it does not create a new persistence or
control-plane contract.

### Size check

The plan has one independently checkable deliverable: running-service selection
for bare pairing issuance. It modifies 3 production or test-support Go files
(`pair.go`, `main.go`, and the build-tagged e2e harness), approximately 600–700
written lines including replacement binary tests and this plan, adds no exported
production type or interface, has 6 direct or observable `runPairDefault`
consumers to update, 5 acceptance criteria, and 4 selection/refusal branches.
All six one-ticket boundaries hold. The estimate is slightly above the refiner's
~540-line hypothesis because the existing offline e2e setup must be moved to the
shared fixture, but remains below the 800-line boundary and close to #213's
457-line CLI-plus-binary-test analogue.

The refreshed remote-feature-branch overlap check found no other branch changing
the planned files.

## Design

### Parse explicit intent before doing work

`parsePairArgs` keeps the flag values but also records whether `-pyry-name` and
`--relay` were visited on the command line. This distinguishes an explicit name
from the same value supplied through `PYRY_NAME`, and distinguishes a supplied
empty `--relay=` from an absent relay flag. `runPairDefault` rejects every visited
relay flag as a usage error before service discovery or any control request.

### Select one live service

An explicit name resolves exactly once through `resolveSocketPath` and proceeds
directly to `control.MintPairing`; it does not enumerate or fall back. The service
name displayed on success is `sanitizeName`'s safe filename form, matching the
socket actually selected.

Without an explicit name, a discovery helper globs the current user's
`~/.pyry/*.sock` paths in lexical order. It accepts only basenames already in
`sanitizeName`'s terminal-safe alphabet, probes each with `control.Status`, and
discards every failed probe. Zero responsive services returns a clear error.
Several responsive services return a deterministic comma-separated name list and
require `-pyry-name`; no mint request is made. Exactly one becomes the selected
service. `PYRY_NAME` is never consulted on this branch beyond the unused flag
default produced by parsing.

The automatic status probe and following mint are separate control connections,
matching the existing one-request-per-connection protocol. A service disappearing
between them is reported as a mint failure; the CLI does not rediscover or retry
against another endpoint.

### Ask the daemon and render only a valid result

`runPairDefault` sends the device label and explicit permission boolean through
`control.MintPairing`. It decodes the returned opaque pairing before writing any
stdout, so an unsupported operation, transport failure, fixed daemon rejection,
or malformed success produces no selected-service line, QR, or paste form. On a
valid response it prints the selected service name, then calls the existing
`pair.Render` for the unchanged QR and paste representations.

The local config, identity, key, and registry loads are removed from
`runPairDefault`. `mintDevice` remains unchanged because both daemon mint paths
still share it. `runPairList`, `runPairRevoke`, and `runPairPreflight` remain
offline and retain `defaultName` behavior.

### Help and test support

`helpText` describes issuance as a running-service operation, names sole-service
automatic selection and explicit multi-service selection, rejects `--relay`, and
labels list/revoke/preflight as offline saved-state operations.

The e2e harness gains a narrow `RunBareInWithEnv` sibling so a binary invocation
can set `PYRY_NAME` deliberately while retaining HOME isolation. Existing
`RunBareIn` delegates with no extra environment. Tests expose a real harness
daemon's short Unix socket through the expected `~/.pyry/<name>.sock` path; the
daemon and control provider remain real, while the path stays below macOS's Unix
socket limit.

## Concurrency model

Production adds no goroutine. Discovery probes sockets sequentially in sorted
order; each `control.Status` inherits the control client's finite dial/exchange
deadline. `control.MintPairing` retains its single bounded exchange and the daemon
provider retains the existing finite registry-lock wait.

Binary tests use the harness's existing daemon lifecycle. A small fake older
control server has one accept-loop goroutine with listener close as its shutdown
path, used only to prove status succeeds while `pairing.mint` is rejected.

## Error handling

- Flag parse failure or any visited `--relay` prints pairing usage and exits 2
  before filesystem discovery or a control connection.
- Automatic discovery ignores missing, stale, and non-responsive socket paths.
- Zero responsive services returns an exit-1 error stating that no running
  service was found.
- Multiple responsive services returns an exit-1 error listing safe service names
  in lexical order and requiring `-pyry-name`; it never requests a pairing.
- Explicit selection reports the chosen name and control error without discovery
  or fallback. Provider-not-configured and older-daemon errors remain recognizable.
- A returned pairing is decoded before output. Decode errors contain no bearer
  bytes, and the opaque result is never formatted into an error or log.

## Testing strategy

RED is the replacement `cmd/pyry` parser/discovery tests and `internal/e2e`
binary pairing tests failing because issuance still reads saved state without a
daemon. GREEN covers:

- parser intent bits for command-line name, environment-only name, and supplied
  empty/non-empty relay flags;
- the incident shape: saved `pyry-agent` state plus one running `pyry` daemon,
  with `PYRY_NAME=pyry-agent`, proving output and mutation belong only to `pyry`;
- zero service, a stale socket, multiple responsive daemons with deterministic
  names, environment-only name under multiplicity, explicit selection, and an
  explicit missing target beside a live service;
- a status-responsive older control server rejecting `pairing.mint`;
- both remote-permission choices reaching the selected daemon's registry;
- `--relay` refusal with a listening endpoint that records zero connections;
- no pairing stdout and byte/nonexistence checks for identity, static key, and
  registry state on refusal paths;
- existing offline list/revoke/preflight unit behavior, with binary list/revoke
  fixtures created by `paireddevice.Setup` instead of bare issuance; and
- the existing relay mint e2e after exposing its real daemon socket by name.

Role-owned verification is `go test -race ./cmd/pyry/...`, the e2e package's
focused pair tests with the hermetic build tag, `go vet ./...`, and
`go build ./cmd/pyry`. The dispatcher retains the full-module race/e2e gate.

## Open questions

None. The ticket fixes selection, explicitness, relay ownership, output order,
and refusal side effects; #2388/#2389 fix the downstream daemon contract.

## Documentation handoff

Pending for the documentation stage:

- Update `docs/knowledge/features/pyry-pair-command.md`, the Surface, bare
  operation-order, exit-code, token-visibility, concurrency, tests, and related
  sections, to replace offline issuance with running-service discovery and
  `pairing.mint`, while retaining offline list/revoke/preflight behavior.
- Update the user-facing `pyry pair` examples in the owning command/reference
  documentation to show required running service, sole-service automatic
  selection, `-pyry-name` selection under multiplicity, `PYRY_NAME` not counting
  as explicit for issuance, and `--relay` refusal because relay choice is
  daemon-owned.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `runPairDefault` crosses only the current-user control socket boundary; automatic candidates must answer `control.Status`, and the mode-0600 socket retains the host operator's existing authority.
- [Tokens, secrets, credentials] SHOULD FIX — decode the `control.MintPairing` result before any stdout and never include the opaque pairing or decoded token in errors or logs; refusal tests must assert no pairing output and no credential-state mutation.
- [File operations] No findings — automatic discovery reads directory entries only, rejects basenames changed by `sanitizeName`, and never opens identity, key, registry, or config files. A same-user process can swap a socket path between status and mint, but that actor already holds the exact filesystem and control-socket authority being protected; the one-request control protocol cannot pin a connection across operations.
- [Subprocess execution] No findings — production spawns no subprocess. Test helpers pass fixed binary arguments through `exec.CommandContext` without a shell and use isolated environments.
- [Cryptographic primitives] No findings — the CLI generates no token or key. The selected daemon retains `mintDevice`'s `crypto/rand` token generation, hashed storage, existing static key, and per-device revocation behavior.
- [Network and I/O] No findings — only local Unix control requests are added; `Status` and `MintPairing` inherit finite whole-exchange deadlines and the protocol's existing request-size limit and one-request connection lifecycle.
- [Errors, logs, telemetry] SHOULD FIX — print only names accepted unchanged by `sanitizeName`; errors may name a safe service and socket path but must omit the returned pairing, label, token, token hash, identity contents, and static key.
- [Concurrency] No findings — production is sequential and creates no goroutine or lock. The daemon retains the lock ordering and shutdown path audited in #2389.
- [Threat model alignment] No findings — selecting the live daemon closes the saved-identity mismatch without changing the protocol's bearer-token, relay-MITM, per-device revocation, or static-key threat posture. Same-user socket replacement and local denial of service remain within the already-trusted host-user boundary.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-13
