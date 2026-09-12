# #2388 — Local pairing-code mint operation

## Files read

- `internal/control/protocol.go` → `VerbChannelNew`, `Request`, `Response`,
  `ChannelPayload`, `ChannelNewResult` — the additive `omitempty` wire pattern
  that preserves every existing verb's encoded bytes.
- `internal/control/server.go` → `Server`, `NewServer`, `SetChannelCreator`,
  `Server.handle`, `handleChannelNew`, `defaultHandshakeTimeout` — the
  late-bound optional-dependency pattern, leaf-lock discipline, dispatch point,
  and finite per-connection read/write deadline.
- `internal/control/client.go` → `ChannelNew`, `request`, `exchange`,
  `DialTimeout` — the one-shot client lifecycle and the distinction between an
  operation-specific timeout and the shared clients whose callers may choose a
  longer deadline.
- `internal/control/dial.go` → `dial`, `dialWithRetry` — the dial path already
  observes a supplied context deadline, allowing the new client helper to place
  one deadline around the complete dial/write/read exchange.
- `internal/control/channel_new_test.go` → `fakeChannelCreator`,
  `startServerWithChannelCreator`, `channelRoundTrip` — the nearest focused
  server-test shape for an optional closure installed after construction.
- `internal/control/client_test.go` → `startMisbehavingServer` — the local Unix
  peer fixture used to prove malformed and silent-peer client behaviour.
- `internal/control/sessions_new_test.go` →
  `TestProtocol_SessionsRoundTripBackCompat` — the existing byte-equality guard
  that will redden if new `Request` or `Response` members lack `omitempty`.
- `internal/control/sessions_deadline_test.go` →
  `TestServer_SessionsVerbs_SlowClientStillTimedOut` — proof pattern for the
  server's finite handshake read deadline.
- `cmd/pyry/pairing_mint_v2.go` → `pairingMinterV2`,
  `pairingMinterV2.MintPairing` — downstream context only: #2389 will adapt the
  already-loaded daemon values to this ticket's local provider seam.
- `cmd/pyry/pair.go` → `mintDevice` — downstream context only: the current mint
  operation is synchronous, accepts a device label and permission choice, and
  does not accept a cancellable context.
- `docs/knowledge/features/control-plane.md` → “Server Construction”,
  “Handshake Deadline”, and “Channel: new verb” — current package rules for
  optional setters, one-request/one-response connections, and deadline safety.
- `docs/knowledge/features/control-plane-client-dial-transient-startup-retry.md`
  → `dialWithRetry` contract — confirms that supplying a deadline before dial
  bounds startup retries as part of the same exchange budget.
- `docs/knowledge/features/development-verification.md` → “Prove that tests
  distinguish the change” and “Protocol boundaries” — requires distinctive
  secret sentinels and exact raw JSON assertions rather than self-round-trips.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-mint-pairing-pairingminter-seam.md`
  → “Inbound `mint_pairing`” — establishes that the encoded pairing is a
  plaintext bearer credential and provider failures must not cross a seam with
  their internal detail.

## Context

Offline pairing can choose saved identity files that do not belong to the daemon
selected by an operator-facing client. This slice adds only the daemon-agnostic
local control contract: a client supplies a display label and the explicit
remote-permission choice, and an optional provider returns the opaque existing
pairing string. The server identity, static key, relay destination, registry
path, token generation, and persistence remain impossible to supply through
this request. Ticket #2389 installs the provider from live daemon state.

No ADR is warranted. The change applies the established late-bound control
operation pattern and introduces no new package boundary or persistence rule.

### Size check

The plan prescribes 3 modified production files, approximately 600–700 total
written lines including focused tests and this plan, 2 new exported wire types,
0 existing consumer call sites requiring simultaneous migration, 4 acceptance
criteria, and no state-machine reject fan-out. The ticket has one independently
checkable deliverable: the local control operation and its Go client. All six
one-ticket boundaries hold, and the estimate remains consistent with the
479-line control-only analogue in #2155.

The refreshed remote-branch overlap check found no other `feature/<ticket>`
branch changing the planned files.

## Design

### Wire contract in `internal/control/protocol.go`

- Add `VerbPairingMint Verb = "pairing.mint"`.
- Add `PairingPayload` with exactly `DeviceLabel string` and
  `AllowRemotePermissions bool`. Neither field uses `omitempty`, so an empty
  fallback label and a false permission choice are still explicit wire values.
- Add `PairingResult` with exactly `Pairing string`, the opaque bearer string.
- Add `Request.Pairing *PairingPayload` and
  `Response.Pairing *PairingResult`, both with `omitempty`. Existing request and
  response values therefore preserve their current JSON bytes.
- Do not add identity, key, relay, registry, token, hash, expiry, or diagnostic
  fields. The provider is the only authority for all mint inputs not present in
  `PairingPayload`.

### Optional provider and handler in `internal/control/server.go`

`Server.SetPairingProvider` installs a narrow closure with the contract
`func(deviceLabel string, allowRemotePermissions bool) (string, error)`.
Using an unnamed function type follows `SetChannelCreator`, keeps `NewServer`
unchanged, and lets #2389 close over the already-constructed runtime minter
without importing pairing implementation packages into `internal/control`.

`handlePairingMint` will:

1. Copy the provider under `Server.mu`, then release the lock before any call.
2. Retain the finite handshake read deadline established by `Server.handle` and
   establish a fresh finite response-write deadline using `DialTimeout` before
   invoking the provider.
3. Return the fixed `pairing.mint: provider not configured` response when the
   closure is absent, with no pairing field.
4. Return the fixed `pairing.mint: operation failed` response for a missing
   pairing payload or any provider error, with no pairing field.
5. Otherwise invoke the provider exactly once with the decoded label and
   permission boolean, then place its returned string only in
   `Response.Pairing.Pairing`.

The handler adds no logs on any branch. In particular, it never formats a
provider error, request payload, or pairing into a log or response error. A new
`VerbPairingMint` dispatch arm is the only change to `Server.handle`.

### Go client in `internal/control/client.go`

`MintPairing(ctx, socketPath, deviceLabel, allowRemotePermissions) (string,
error)` creates a derived context whose deadline is the earlier of the caller's
deadline and `time.Now().Add(DialTimeout)`, then passes that same context into
the existing `request` helper. Because `dial` and the connection deadline both
observe it, a single budget covers dial, encode, and decode. This policy remains
local to `MintPairing`; changing `request` globally would incorrectly shorten
operations such as `AttachFile` whose documented contract allows a caller to
choose a longer deadline.

The client returns the response pairing verbatim on success. A server error is
returned as its fixed text with an empty pairing. A missing or empty pairing
result becomes the fixed client-side error `control: empty pairing.mint
response`, also with an empty pairing. Transport and deadline failures already
carry no decoded response payload, so those paths likewise return an empty
pairing.

## Concurrency model

No new goroutine is introduced. `Serve` continues to allocate one existing
per-connection goroutine, and a pairing provider runs synchronously on it.
`Server.mu` protects installing and loading the provider pointer, but is released
before the call so a slow mint neither blocks other control verbs nor deadlocks
provider-owned registry locks.

The client deadline can end the caller's exchange while an already-entered
provider is still running. The control layer cannot forcibly cancel the current
synchronous provider contract; the handler's response write remains bounded and
will fail harmlessly after the client closes. Tests release their deliberately
held provider, while #2389 supplies the real operation with its own finite lock
wait and bounded local I/O paths.

Shutdown remains the existing `Serve` drain: it waits for in-flight handlers,
including a provider already running. No detached worker survives the handler.

## Error handling

| Failure | Client-visible result | Pairing field | Logged by control |
| --- | --- | --- | --- |
| Provider absent | `pairing.mint: provider not configured` | empty | nothing |
| Payload absent | `pairing.mint: operation failed` | empty | nothing |
| Provider returns any error | `pairing.mint: operation failed` | empty | nothing |
| Empty/malformed success response | `control: empty pairing.mint response` or existing decode error | empty | nothing |
| Dial/write/read/deadline failure | existing transport error | empty | nothing |
| Success | no error | exact provider string | nothing |

The fixed operation error deliberately discards all provider detail. Ticket
#2389 owns actionable daemon-side logging at the implementation boundary, where
it can redact the credential while retaining safe operational context.

## Testing strategy

Focused tests under `internal/control` will cover:

- Exact client wire bytes for both permission values, proving the request
  contains only `verb`, `deviceLabel`, and `allowRemotePermissions`; the fixture
  replies through the declared pairing result field and the client returns that
  exact opaque string.
- Full server/client round trips for both permission values with a recording
  provider, proving verbatim argument forwarding, exactly one call, and an exact
  pairing result.
- An unconfigured provider and a provider returning both a pairing sentinel and
  an error sentinel, proving their fixed messages, nil wire result, empty client
  result, discarded provider value, and no provider call on the absent arm.
- Distinctive success-pairing and provider-error sentinels against an injected
  `slog` buffer and every client-visible error/diagnostic field. Only the
  successful returned string may contain the pairing sentinel; neither sentinel
  may appear on an error arm.
- A server returning an empty pairing response, proving the client's empty-result
  guard.
- A silent Unix peer with a caller deadline later than `DialTimeout`, proving the
  whole exchange ends at the existing control timeout rather than the later
  caller deadline.
- A provider held beyond an earlier caller deadline, proving the client exits on
  the caller's bound with an empty pairing while the provider is released and the
  server handler drains afterward.
- The existing `TestProtocol_SessionsRoundTripBackCompat` byte assertions,
  proving the new nil `Request` and `Response` fields do not alter existing verb
  encodings.
- The existing `TestServer_SessionsVerbs_SlowClientStillTimedOut`, proving the
  server's request-read deadline remains finite; the pairing held-provider test
  additionally exercises its finite response-write deadline.

Verification is `go test -race ./internal/control/...`, `go vet ./...`, and
`go build ./cmd/pyry`.

## Documentation handoff

Pending for the documentation stage: update
`docs/knowledge/features/control-plane.md` in the verb inventory, “Server
Construction”, “Handshake Deadline”, and “Testing” sections with
`pairing.mint`, `SetPairingProvider`, the opaque bearer-result rule, fixed error
surface, and the operation-wide single timeout budget. `docs/protocol.md` is an
explicit historical Phase-0 snapshot and should remain unchanged.

## Open questions

None. The refiner's clarification fixes the liveness boundary: this slice proves
the client and response write are bounded, not that an arbitrary synchronous
provider can be forcibly cancelled after entry.

## Revisions

None.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the mode-`0600` Unix socket remains the
  existing same-user authentication boundary; `PairingPayload` is the only new
  caller-authored input, and its two fields reach only the narrow provider.
  Runtime identity, key, relay, and registry state cannot cross this wire
  boundary by construction.
- [Tokens, secrets, credentials] No findings — `internal/control` neither mints
  nor persists credentials. It holds the provider's opaque bearer string only
  long enough to place it in `Response.Pairing` and returns it only on the
  nil-error branch. `handlePairingMint` must discard a non-empty provider value
  whenever the provider also returns an error; the focused sentinel test makes
  this requirement deterministic. Token generation, hash-only persistence,
  redemption expiry, and revocation remain owned by `mintDevice` and the #2389
  provider.
- [File operations] No findings — the plan adds no file path, read, write,
  permission, symlink, or persistence operation. `PairingPayload` has no field
  capable of selecting a registry path.
- [Subprocess / external commands] No findings — no subprocess is started and no
  request value becomes an argument, environment variable, or shell string.
- [Cryptographic primitives] No findings — the control layer treats the result
  as opaque and adds no randomness, hashing, key handling, comparison, or token
  format. The cryptographic implementation remains outside this ticket in
  `mintDevice` and the existing pair encoder.
- [Network & I/O] No findings — the existing one-request Unix framing, owner-only
  socket, and `Server.handle` request-read deadline are retained. The pairing
  handler adds a fresh finite response-write deadline, while `MintPairing`
  derives one earlier-of deadline before dial so dial, write, and read share the
  same cap. The shared decoder's absence of a byte-count limit is an inherited
  same-user control-plane property, not a new remote ingress or newly observed
  failure; this ticket does not widen that transport contract.
- [Error messages, logs, telemetry] No findings — the provider's error is
  projected to fixed content-free text and is never logged by this package.
  Neither request fields nor response pairing are logged. Tests inject separate
  bearer and error sentinels, examine the control logger and every returned
  error/result field, and fail if either reaches a forbidden sink.
- [Concurrency] OUT OF SCOPE — an already-entered synchronous provider cannot be
  forcibly cancelled when its client deadline expires. The client and response
  write still terminate on finite deadlines, no new goroutine is detached, and
  the provider call runs without `Server.mu`. Ticket #2389 supplies the concrete
  provider with its operation-owned lock wait and error handling; changing the
  existing synchronous mint contract here would cross the package slice.
- [Threat model alignment] No findings — this is a local owner-authorized
  operation, and allowing that owner to request a remote-permission-capable
  pairing is the explicit product contract. No relay request gains the boolean:
  remote `mint_pairing` remains a separate authenticated path whose provider
  hard-codes an unprivileged device. Ticket #2389 binds daemon-authored runtime
  values, and #2387 later selects this operation from the operator CLI.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-12
