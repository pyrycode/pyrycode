# Spec: relay inbound token validation per WS connection (#249)

## Files to read first

- `internal/devices/auth.go:32-46` — `Registry.Validate(plain) (Device, bool)` contract: empty input short-circuits to (Device{}, false); on hit, LastSeenAt is bumped under the registry's mutex; never logs the plain or hash.
- `internal/devices/registry.go:111-128` — `Add` / `Remove` semantics used by tests to construct paired / revoked fixtures.
- `internal/devices/device.go:24-43` — `Device` struct (TokenHash, Name, PairedAt, LastSeenAt).
- `internal/protocol/handshake.go:30-44` — `HelloAckPayload` (protocol_version, server_id, conn_id) and `ErrorPayload` (code, message, retryable, retry_after_s) wire shapes.
- `internal/protocol/envelope.go:23-43` — `Envelope` (id, type, ts, payload, in_reply_to, payload_encrypted) and `RoutingEnvelope` (conn_id, frame) outer shapes.
- `internal/protocol/codes.go:14-15, 38-41` — `CodeAuthInvalidToken`, `TypeHello`, `TypeHelloAck`, `TypeError` wire constants.
- `internal/relay/connection.go:214-266` — existing binary↔relay `handshake`: shows the envelope-build → json.Marshal → routing wrap pattern this ticket mirrors for the binary→phone leg.
- `internal/protocol/handshake_test.go:101-180` — fixture round-trip test pattern for `hello_ack` and `error` envelopes; the new tests reuse the marshalling style (no shared helpers required).
- `docs/protocol-mobile.md` § Authentication (line 67–) and the inline summary at line 98 — first-frame validation contract: valid → `hello_ack`; invalid → `error` envelope code `auth.invalid_token` + relay closes phone WS with code `4401`.
- `docs/protocol-mobile.md` § hello_ack (line 240–255) and § error (line 257–278) — exact wire shape, including `in_reply_to` echoing the hello envelope's id and the canonical error message string.
- `docs/protocol-mobile.md` § Error codes (line 534–550) — the `auth.invalid_token` row, the line 535 `auth.token_revoked` "Same UX as invalid_token" equivalence, and the `4401` close-code row.
- `docs/protocol-mobile.md` § Worked example (line 720–732) — frame addressing pattern showing the binary's outbound `hello_ack` uses `conn_id` = phone's id with `frame.id` = 1, `frame.in_reply_to` = the hello's id.

## Context

Phase 3 Track C composes A5 (`devices.Registry.Validate`, shipped 2026-05-09 as #210) with the v1 handshake/control payload structs (`protocol.HelloAckPayload`, `protocol.ErrorPayload`, shipped as #271). The relay performs no token validation per spec § Authentication phone→relay→binary; the binary owns the entire trust check. The relay forwards every phone frame to the binary in a `RoutingEnvelope`; the binary validates on receipt of the phone's first frame for a given `conn_id`.

This ticket adds `internal/relay/auth.go` as a sibling to `connection.go` and exposes a single pure function: given the first frame's routing envelope, the device token (as an explicit argument, carrier-agnostic), and a handle to the devices registry, return a structured outcome the relay-conn layer (sibling future ticket) will act on — send the response envelope back, and (on reject) close the phone WS with code `4401`.

The ticket is deliberately scoped to the *decision* — token in, structured outcome out. The wire mechanism that delivers the token to the binary (extended routing envelope, synthesized `connection_opened` control frame, or amended `hello` payload) lives in the relay-conn ticket and does not affect this handler's signature.

## Design

### Package

Add `internal/relay/auth.go` as a sibling to the existing `internal/relay/connection.go`. No changes to `connection.go` — the handler is a free function callable from the (future) relay-conn layer; today's binary↔relay `Connection.handshake` is a separate concern.

### Types

```go
// StatusUnauthorized is the WS close code the relay sends when the binary
// rejects a phone's device token (docs/protocol-mobile.md § Error codes,
// line 550). Typed locally so callers don't import the websocket package
// for the value; parallel to statusServerIDConflict in connection.go.
const StatusUnauthorized websocket.StatusCode = 4401

// AuthOutcome is the structured result of AuthenticateFirstFrame. The
// relay-conn caller forwards Response back through the binary→relay leg
// and, when CloseConn is true, asks the relay to close the phone WS with
// StatusUnauthorized after the Response has been written.
//
// CloseConn carries the protocol-level intent (close-because-auth-failed)
// rather than the wire code itself; the close code is fixed at 4401 by
// spec and lives on the StatusUnauthorized constant.
type AuthOutcome struct {
    Response  protocol.RoutingEnvelope
    CloseConn bool
}
```

### Function

```go
// AuthenticateFirstFrame is the per-connection token-validation predicate
// for the phone→relay→binary auth phase (docs/protocol-mobile.md §
// Authentication). It is invoked exactly once per phone conn — on the
// binary's receipt of the first frame for a given conn_id — and returns
// the routing envelope the binary should send back plus the close-or-keep
// signal the relay-conn layer will act on.
//
// The function is carrier-agnostic with respect to how `token` reached
// the binary: it never parses WS headers, never reads `env.Frame`'s
// payload for a token field, and never inspects a hello payload. The
// relay-conn ticket that wires this handler into phone traffic picks one
// of (a) extended routing envelope, (b) synthesized connection_opened
// control frame, or (c) amended hello payload, and forwards the extracted
// token here unchanged.
//
// `env` is the routing envelope that wrapped the phone's hello frame.
// The function reads env.ConnID (echoed back on Response) and parses
// env.Frame as a protocol.Envelope to extract its outer `id` — only the
// id, never the typed payload — so the returned hello_ack / error can
// echo it in `in_reply_to` per docs/protocol-mobile.md § hello_ack and
// § error. A malformed env.Frame returns ErrMalformedHelloFrame and no
// outcome; the caller's existing protocol-malformed handler owns that
// path.
//
// `serverID` is the binary's own server-id, included verbatim in the
// hello_ack payload. The caller already holds it (Config.ServerID on the
// relay Connection); passing it explicitly avoids coupling auth.go to
// Connection state.
//
// Two-state semantics: reg.Validate returns (Device, bool). A `true`
// result means the device row exists and was just bumped (the predicate
// already handled LastSeenAt under reg.mu — the handler does not call
// any further mutator). A `false` result means either never-paired or
// the row was removed via reg.Remove; both produce the same
// `auth.invalid_token` outcome per docs/protocol-mobile.md § Error codes
// line 535 ("Same UX as invalid_token"). The CodeAuthTokenRevoked
// constant is reserved for a future tombstone primitive and is NOT
// emitted by this handler.
//
// SECURITY:
//   - `token` is never logged, never wrapped into any returned error,
//     and never echoed to the phone (the rejection message is a fixed
//     string).
//   - The matched device's name IS logged on accept (paired-device
//     identification is operationally useful) but is NOT logged on
//     reject — emitting "name X was not recognised" would let an
//     attacker probe for known names.
//   - The handler computes no hash itself; it delegates to
//     reg.Validate, which owns the plain→hash boundary and the
//     constant-time concerns documented in devices/auth.go.
//
// Concurrency: the function is stateless and may be called from any
// goroutine. reg is the only shared object and its own mutex guards
// Validate's critical section.
func AuthenticateFirstFrame(
    env protocol.RoutingEnvelope,
    token string,
    reg *devices.Registry,
    serverID string,
    logger *slog.Logger,
) (AuthOutcome, error)
```

### Errors

A single new sentinel for the malformed-frame path:

```go
// ErrMalformedHelloFrame is returned by AuthenticateFirstFrame when the
// inner envelope inside env.Frame cannot be JSON-decoded. The relay-conn
// caller maps this to its existing protocol.malformed handling; this
// package does not synthesize an error envelope for it (the malformed-
// frame response shape is owned by a sibling ticket).
var ErrMalformedHelloFrame = errors.New("relay: malformed hello frame")
```

Rationale: missing/empty `token`, unknown token, and revoked-then-removed device are NOT errors — they are valid `auth.invalid_token` outcomes and return (AuthOutcome{..., CloseConn: true}, nil). Only a structurally broken `env.Frame` returns a Go error.

### Behavioural contract

Given inputs `(env, token, reg, serverID, logger)`:

1. JSON-decode `env.Frame` into a `protocol.Envelope`. On failure → return `(AuthOutcome{}, ErrMalformedHelloFrame)`. (The handler does NOT decode `env.Payload` — only the outer envelope shape is read.)
2. Let `helloID := decoded.ID`.
3. Call `device, ok := reg.Validate(token)`.
4. If `ok`:
   - Build `protocol.Envelope{ID: 1, Type: TypeHelloAck, TS: time.Now().UTC(), InReplyTo: &helloID, Payload: marshal(HelloAckPayload{ProtocolVersion: "v1", ServerID: serverID, ConnID: env.ConnID})}`.
   - Wrap in `protocol.RoutingEnvelope{ConnID: env.ConnID, Frame: marshal(envelope)}`.
   - Log at INFO: `event=auth.accept`, `conn_id=env.ConnID`, `device_name=device.Name`. Never `token`, never the matched hash.
   - Return `(AuthOutcome{Response, CloseConn: false}, nil)`.
5. If `!ok`:
   - Build `protocol.Envelope{ID: 1, Type: TypeError, TS: time.Now().UTC(), InReplyTo: &helloID, Payload: marshal(ErrorPayload{Code: CodeAuthInvalidToken, Message: "device token not recognised; re-pair via pyry pair on the binary", Retryable: false})}`.
   - Wrap in `protocol.RoutingEnvelope{ConnID: env.ConnID, Frame: marshal(envelope)}`.
   - Log at WARN: `event=auth.reject`, `conn_id=env.ConnID`, `code=auth.invalid_token`. Never `token`, never any device name (no name is known on reject — see SECURITY note).
   - Return `(AuthOutcome{Response, CloseConn: true}, nil)`.

The outer envelope's `ID: 1` reflects that this is the binary's first outbound frame on the phone's conn — matches the spec's worked-example line 731. Subsequent envelopes on the same conn are allocated by the relay-conn layer from a per-conn counter starting at 2; not this handler's concern.

The error message string is defined as an exported `const` (`MsgInvalidToken`) so a future cmd/integration test can pin it without re-typing the spec sentence.

### Data flow

```
relay-conn layer
  │
  │ envelope := <routing envelope wrapping phone's first frame>
  │ token    := <extracted from a/b/c carrier — relay-conn's choice>
  │
  ▼
AuthenticateFirstFrame(envelope, token, reg, serverID, logger)
  │
  │   1. decode env.Frame → helloID
  │   2. reg.Validate(token)  ──► LastSeenAt bump (inside reg)
  │   3. build hello_ack OR error envelope
  │   4. wrap in RoutingEnvelope, conn_id echoed
  │   5. log (no token, no name on reject)
  │
  ▼
AuthOutcome { Response, CloseConn }
  │
  ▼
relay-conn layer
  │ — writes Response back through binary→relay client
  │ — if CloseConn: signals relay to WS-close phone with 4401
```

## Concurrency model

No goroutines spawned; no channels owned. The function is a pure call-and-return on top of `reg.Validate`, which is mutex-protected. Safe for arbitrary concurrent invocations across distinct phone conns (one per first-frame, per-conn).

## Error handling

- `ErrMalformedHelloFrame` for a JSON-unparseable `env.Frame`. The caller maps it to existing protocol.malformed handling; this handler does NOT synthesize an error envelope for it (response shape belongs to the malformed-frame ticket).
- All other input pathologies (empty token, never-paired token, revoked-then-removed token) are valid `auth.invalid_token` outcomes returned with `nil` error.

## Testing strategy

New file `internal/relay/auth_test.go`. Stdlib `testing` only, table-driven where it fits.

Helper (file-local, NOT exported):

```go
// makeHelloRouting builds a RoutingEnvelope wrapping a hello frame with
// the supplied conn_id and hello envelope id. Used by every test below.
func makeHelloRouting(t *testing.T, connID string, helloID uint64) protocol.RoutingEnvelope
```

Tests:

1. **TestAuthenticateFirstFrame_ValidToken** — paired registry (one device added via `reg.Add(Device{TokenHash: HashToken("plain-token"), Name: "Pixel", PairedAt: <fixed>, LastSeenAt: <fixed past>})`). Call handler with token `"plain-token"`.
   - Assert `outcome.CloseConn == false`.
   - Assert `outcome.Response.ConnID == "c-test"` (echoed).
   - Decode `outcome.Response.Frame` → envelope; assert `Type == TypeHelloAck`, `InReplyTo != nil && *InReplyTo == helloID`.
   - Decode envelope's `Payload` → `HelloAckPayload`; assert `ProtocolVersion == "v1"`, `ServerID == "test-server"`, `ConnID == "c-test"`.
   - Assert LastSeenAt bump: `reg.FindByTokenHash(HashToken("plain-token"))` returns a device whose `LastSeenAt` is After the initial fixed past time. Use `time.Time.Equal`-aware comparisons (NOT `==`), per the time-round-trip discipline already pinned in project conventions.

2. **TestAuthenticateFirstFrame_UnknownToken** — empty registry. Call handler with token `"never-paired"`.
   - Assert `outcome.CloseConn == true`.
   - Assert `outcome.Response.ConnID == "c-test"` (echoed even on reject — relay needs it to address the close).
   - Decode envelope; assert `Type == TypeError`, `InReplyTo != nil && *InReplyTo == helloID`.
   - Decode payload → `ErrorPayload`; assert `Code == CodeAuthInvalidToken`, `Message == MsgInvalidToken`, `Retryable == false`, `RetryAfterS == nil`.

3. **TestAuthenticateFirstFrame_RevokedTokenSameUX** — paired registry, then `reg.Remove("Pixel")`. Call handler with the originally-paired token.
   - Assert outcome is byte-for-byte identical (ignoring TS) to TestAuthenticateFirstFrame_UnknownToken's outcome (same code, same message, same CloseConn=true). This locks in the spec § Error codes line 535 equivalence and ensures a future regression doesn't accidentally introduce `auth.token_revoked` here without an explicit ADR.

4. **TestAuthenticateFirstFrame_EmptyToken** — paired registry, but token=`""`. Asserts the same `auth.invalid_token` rejection outcome as case 2. Defends against a buggy relay-conn caller that forwards an empty string; `reg.Validate("")` already returns (Device{}, false) without touching the registry, so this is observational.

5. **TestAuthenticateFirstFrame_MalformedHelloFrame** — call handler with `env.Frame = []byte("not-json")`. Assert returned error `Is` `ErrMalformedHelloFrame`; assert `outcome` is the zero value.

6. **TestStatusUnauthorized_Value** — single-line guard that `StatusUnauthorized == 4401`. Catches accidental edits to the constant.

No table-driven mega-test: each scenario asserts a different shape, and a flat `t.Run` layout reads better than a struct-of-structs.

## Open questions

None. The previous open question (token wire-carrier) was resolved before refinement closed: the handler is carrier-agnostic and the relay-conn ticket picks the carrier. The `CodeAuthTokenRevoked` constant remains defined in `internal/protocol/codes.go` but is not used by this handler; a future tombstone primitive can enable it without churn here.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The function is the single explicit boundary — `token` is untrusted on entry, and the only "trusted" output is `(Device, true)` from `reg.Validate`, which never escapes the function (only `device.Name` is consumed for logging on accept, and never echoed to the phone). Downstream callers receive only the structured `AuthOutcome` whose contents are wire-safe protocol envelopes.
- **[Tokens, secrets, credentials]** No findings. The token is consumed via `reg.Validate` (which owns plain→hash and never logs); the handler never logs the plain token, never wraps it into any returned error, and never echoes it on the wire. The fixed `MsgInvalidToken` string is constant — no interpolation of the supplied token. Token lifecycle (creation/rotation/revocation) is owned by `internal/devices` and `pyry pair` (out of scope for this ticket).
- **[File operations]** N/A — handler is pure-compute over in-memory registry + envelope marshalling. No file I/O. LastSeenAt persistence is the supervisor's responsibility per `devices/auth.go`'s documented contract.
- **[Subprocess / external command execution]** N/A — no exec.
- **[Cryptographic primitives]** No findings. The handler delegates all crypto to `devices.Registry.Validate` → `HashToken` (SHA-256, deterministic, per the package's intentional design for 256-bit random tokens). No new RNG, no new comparisons. Constant-time hash equality is not required at the hash↔hash boundary inside `Validate`; the plain→hash boundary is handled there.
- **[Network & I/O]** No findings. The handler does not touch sockets; it produces wire envelopes consumed by the relay-conn layer. Input size: `env.Frame` size cap is the transport layer's concern (transport.Client already enforces a max read size on the binary↔relay leg). Slow-loris / connection caps are upstream concerns (relay ticket, not binary).
- **[Error messages, logs, telemetry]** No findings, but explicit logging contract pinned in the design (`event=auth.accept` with `device_name` on success; `event=auth.reject` WITHOUT any name on failure to prevent name-enumeration probes). The error envelope `message` field is a fixed sentence with no attacker-supplied substring. `ErrMalformedHelloFrame` carries no parser detail in its message; the caller logs the wrapped json decode error if it wants the diagnostic, but that decision lives at the call site, not here.
- **[Concurrency]** No findings. The function is stateless; only shared state is the `*devices.Registry` whose mutex guards the read-and-bump critical section. No lock ordering concerns (a single lock is taken inside `Validate`). No goroutine lifecycle (no goroutines spawned).
- **[Threat model alignment]** Covers `docs/protocol-mobile.md` § Authentication (phone→relay→binary) and § Error codes rows for `auth.invalid_token` and close code `4401`. Out of scope and explicitly named: rate limiting (deferred per ticket body), replay attacks (deferred per spec § Replay attacks), distinguishing revoked-vs-unknown (deferred per A5 two-state primitive; revisit when/if a tombstone primitive is scoped). Server-id auth (binary→relay direction) is a separate concern owned by the prior ticket #248.

Two adversarial scenarios specifically considered and dismissed:

- **Name-enumeration via reject path** — initially the design logged `device_name` on reject too; the security pass tightened it to log no name on reject so an attacker brute-forcing tokens cannot enumerate paired-device names from binary logs. (The accept path still logs the name; an attacker who reaches the accept path already has a valid token.)
- **Token leakage via wrapped error** — `ErrMalformedHelloFrame` was reviewed for whether a developer might `fmt.Errorf("%w: token=%s", ErrMalformedHelloFrame, token)` at the caller. The spec explicitly forbids it, but the bigger defence is that the malformed-frame path does NOT take `token` as a contributing input — by the time `ErrMalformedHelloFrame` returns, the handler has not yet read `token`, so there is no semantic reason for a caller to include it in any wrapping context.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-12
