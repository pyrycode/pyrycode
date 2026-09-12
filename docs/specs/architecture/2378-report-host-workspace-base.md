# #2378 — report the host workspace base for relative path previews

## Files read

- `internal/protocol/handshake.go` → `HelloAckPayload` — defines the encrypted greeting payload and the existing
  additive-field `omitempty` convention.
- `internal/protocol/handshake_test.go` → `TestHelloAckPayload_RoundTrip`,
  `TestHelloAckPayload_CapabilitiesRoundTrip` — pin the legacy fixture's byte stability and optional-field wire
  behavior.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — constructs `HelloAckPayload` and passes its envelope
  only to Noise responder `WriteResp` early data.
- `internal/relay/v2session_test.go` → `TestV2Session_HappyPath`, `driveToOpenCaps`, `decodeHelloAck` — provide the
  real Noise handshake harness that can distinguish decrypted early data from the relay-visible outer frame.
- `docs/knowledge/features/protocol-package.md` and
  `docs/knowledge/features/protocol-package-handshake-control-payloads.md` → `HelloAckPayload` — establish that the
  protocol package is a pure wire-shape leaf and optional greeting fields preserve the old fixture when absent.
- `docs/knowledge/features/relay-package.md` → `Connection establishment` and `Logging discipline` — establish that
  phone↔daemon `hello_ack` is Noise-encrypted and that payload data is excluded from logs.
- `docs/knowledge/features/v2-session-manager.md` and
  `docs/knowledge/features/v2-session-manager-state-machine-noise-init-happy-and-failure-path.md` →
  `handleNoiseInit` — document the handshake state transition, early-data seal, and rejection behavior this change
  must leave intact.
- `docs/knowledge/features/development-verification.md` → `Protocol boundaries` — requires the test to decode the
  new field and separately inspect the unencrypted outer wire shape.
- `docs/specs/architecture/2172-protocol-capability-model-list.md` → file-overlap note and handshake test strategy —
  the nearest shipped two-file `HelloAckPayload` analogue and the prior validation of abandoned `feature/449`.

## Context

The desktop client can accept a relative workspace path but cannot preview its absolute destination without knowing
which home directory belongs to the selected daemon host. The daemon already uses `~/pyry-workspace/` as the
convention; this ticket reports that convention as optional `hello_ack.workspace_root`. It does not create, inspect,
canonicalize through, or configure the directory.

No ADR is warranted. This is one additive field on the existing encrypted handshake payload, with no new policy or
configurable default.

## Design

`HelloAckPayload` gains `WorkspaceRoot string` with JSON name `workspace_root` and `omitempty`. A zero value is an
absent key, preserving every existing greeting that cannot supply the value and the existing fixture's bytes.

`handleNoiseInit` resolves the host home with `os.UserHomeDir`, requires it to be absolute, and lexically joins it
with `pyry-workspace` through `filepath.Join`. A small package-private `workspaceRoot` helper owns that resolution
and collapses an error, empty result, or non-absolute result to `""`. The helper performs no filesystem operation;
in particular it does not call `Stat`, `MkdirAll`, `EvalSymlinks`, or open the reported path. `handleNoiseInit` puts
the result directly in the `HelloAckPayload` literal. `omitempty` turns the failure result into omission while the
rest of the handshake follows its existing success path.

The field remains inside the `hello_ack` envelope supplied to Noise responder `WriteResp`. No routing envelope,
header, close reason, error message, or log attribute gains the value.

The change has two production files, no exported type or interface, no consumer update, two acceptance criteria,
and one non-fatal resolution branch. Estimated written work is about 120 implementation/test lines plus this plan,
well within the one-ticket boundary. `HelloAckPayload` impact is additive; source verification found its producer in
`handleNoiseInit` and decode-only consumers in tests and e2e helpers, so there is no simultaneous call-site cascade.

## Concurrency model

No goroutine, channel, lock, or shared mutable state is added. `handleNoiseInit` continues to run on the session
manager's existing `Run` goroutine. Home resolution and lexical joining are synchronous and happen once per initial
handshake before the ack is sealed.

## Error handling

`os.UserHomeDir` failure, an empty home, or a non-absolute home is a degradation of optional metadata, not a
handshake failure. `workspaceRoot` returns the empty string, JSON omits `workspace_root`, and `handleNoiseInit`
continues to marshal and seal the otherwise unchanged greeting. JSON/Noise failures retain their existing close
behavior.

The resolution error itself is discarded rather than logged: logging it is unnecessary for an optional preview and
could disclose the host path through platform-specific error text. No path value is used in an error.

## Testing strategy

- Extend the protocol optional-field test beside `TestHelloAckPayload_CapabilitiesRoundTrip`: the zero value omits
  the raw key, while an absolute sentinel value survives marshal/unmarshal exactly.
- Add a relay handshake test using `driveToOpenCaps` with a temporary absolute `HOME`. Assert the decrypted
  `hello_ack` reports `<temp-home>/pyry-workspace`, existing greeting identifiers remain unchanged, the directory is
  absent before and after the handshake, the relay-visible recorded frame does not contain the path, and a captured
  logger does not contain it.
- In the same relay test, run the handshake with an unavailable home and with a relative home. Each must still reach
  the open state and omit the raw `workspace_root` key.
- RED is the focused protocol and relay tests before production edits. GREEN is those tests after implementation,
  followed by `go test -race ./internal/protocol/...`, `go test -race ./internal/relay/...`, `go vet ./...`, and
  `go build ./cmd/pyry`.

## Open questions

None. The field name, host meaning, directory convention, omission behavior, transport confidentiality, and
non-creation behavior are fixed by the ticket. A relative `HOME` cannot satisfy the absolute-path contract and is
therefore treated as unavailable.

## File-overlap check

The required remote-branch scan found only `origin/feature/449` touching `internal/relay/v2session_test.go`. It is
not in flight: issue #449 is closed, has no PR, and the branch tip is dated 2026-05-17. The shipped #2172 plan records
the same abandoned branch and conclusion. No active feature branch overlaps this ticket's file set.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md`'s `hello_ack` contract to document optional
`workspace_root`, its meaning as the daemon host's absolute `~/pyry-workspace/` base, omission when the base cannot
be resolved, and transport only inside Noise-encrypted early data.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. `workspaceRoot` accepts only daemon-local `os.UserHomeDir` output, requires an
  absolute value, and the only consumer is the daemon-authored `HelloAckPayload` literal in `handleNoiseInit`.
- **[Tokens, secrets, credentials]** No findings. The change does not inspect or alter token handling; the new host
  path is metadata rather than a credential and remains inside the same authenticated encrypted ack as the existing
  server and connection identifiers.
- **[File operations]** No findings. `workspaceRoot` performs a lexical join only and never checks, opens, resolves
  symlinks within, or creates the path, eliminating traversal, TOCTOU, permission, and partial-write surfaces.
- **[Subprocess execution]** Not applicable. No command is launched and the path is never supplied as an argument or
  environment value.
- **[Cryptographic primitives]** No findings. `handleNoiseInit` continues to give the complete ack envelope to the
  existing Noise responder `WriteResp`; the change introduces no key, nonce, RNG, or comparison behavior.
- **[Network and I/O]** No findings. The payload remains bounded by the host home string and existing Noise message
  limits. The path is absent from the relay-visible `RoutingEnvelope` and has no header or plaintext-frame surface.
- **[Error messages, logs, telemetry]** No findings. Resolution failure is intentionally silent, the error is not
  wrapped or logged, and the path appears in no log attribute; tests inspect both the captured log and outer wire.
- **[Concurrency]** No findings. There is no shared seam or mutable package variable; resolution stays on the
  existing `handleNoiseInit` execution path and adds no goroutine or lock.
- **[Threat model alignment]** No findings. Paired-client authentication and Noise confidentiality remain unchanged;
  an authenticated phone learns only the selected daemon host's workspace convention. Relay blindness is asserted
  directly. Host-path enumeration beyond this fixed base and using the reported path for filesystem access are out
  of scope and are not capabilities implied by this field.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-12
