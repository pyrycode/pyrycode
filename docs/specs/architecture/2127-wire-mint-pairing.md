# #2127 — mint a pairing for another device from a privileged paired client

Serves the wire contract #2126 declared: `mint_pairing` → `pairing_minted`, intercepted
in `dispatchAppFrame` as a v2 control frame, answered off the Run goroutine on the
conn's `appFrameWorker` because the mint takes the `devices.json` file lock.

## Files read

- `cmd/pyry/pair.go` → `runPairDefault` — the mint the wire path must not drift from:
  `crypto/rand` 32 bytes → hex → `devices.HashToken`, the `device-<hash8>` name
  fallback, ONE clock read feeding `PairedAt` and `RedeemBy`, and load-mutate-save
  inside a single `devices.WithLock` region (#1531). Also `pairLockWait`,
  `resolveDevicesPath`, `resolveRelay`, `resolveServerIDPath`,
  `resolveStaticKeyBaseDir`, and the package's no-logger discipline.
- `internal/protocol/pairing.go` → `MintPairingPayload`, its `UnmarshalJSON`,
  `MaxDeviceNameBytes`, `PairingMintedPayload` — the contract this serves. Two
  obligations it places on this ticket verbatim: a payload decode failure is
  answered `protocol.malformed`, and `DeviceName` is "LOGGABLE ONLY AFTER SHAPE
  VALIDATION, which no code in this package performs".
- `internal/relay/v2session_history_request.go` → `handleRequestHistory`,
  `serveHistoryPage`, `historyFrame`, `rejectHistoryRequest`, `historyReplyError` —
  the handler shape this copies wholesale: ordered gates, static reply constants, one
  emission route through `forwardToRun`, a per-verb error helper.
- `internal/relay/v2session.go` → `dispatchAppFrame`, `appFrameWorker`,
  `enqueueAppFrame`, `appFrameJob` and its kind constants — the interception point and
  the worker hand-off.
- `internal/relay/v2session_seams.go` → `V2SessionConfig`, `HistoryPager` /
  `HistoryPageResult`, `ModalResolver`, `QuestionResolver`, `AttachmentResolve` — the
  seam conventions: consumer-side declaration, outcome discriminant rather than an
  error, nil ⇒ consumed-but-inert.
- `internal/relay/v2session_redemption.go` → `redemptionLockWait` — the request-path
  lock bound and the promise it makes about its peers ("sub-millisecond regions"),
  which the wire mint must keep.
- `cmd/pyry/question_resolve_v2.go` → `questionResolverV2.ResolveAnswer`, `admit`,
  `auditQuestion` — the gate-and-record pattern: the function that denies is the
  function that writes the `audit.OutcomeDeniedUnauthorized` record.
- `internal/audit/audit.go` → `Entry`, `Log`, `Outcome`, `Source` — the record shape,
  and its guarantee that no field can hold a plain token.
- `internal/devices/auth.go` → `MayAnswerRemotePermission` (nil-receiver-safe, so a
  device-less conn denies) and `AuthorizeRemotePermission`.
- `internal/devices/lock.go` → `WithLock`, `DefaultLockWait`, `ErrLockBusy`.
- `internal/pair/payload.go` → `Payload`, `Encode`, `Decode`.
- `cmd/pyry/relay.go` → `startRelayV2`, `relayWiring` (`relayURL`, `instanceName`),
  and the `staticKey` / `serverID` locals — everything the reply needs is already in
  scope at the wiring site.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes` — `TypeMintPairing` must move to
  `inboundTypes` as "switch-intercepted" the moment the `dispatchAppFrame` case exists.
- `internal/e2e/relay_v2_history_test.go` → `TestRelayV2_ConversationHistory` — the
  end-to-end shape: `RunBareIn` + `decodePairPayload` + `fakerelay` + `fakephone` +
  `driveHandshakeToOpenDaemonInteractive`.
- `docs/knowledge/features/pyry-pair-command.md` § "`pyry pair` (bare) — operation
  order", § "Token visibility (SECURITY)", § "Concurrency model" — the operation
  order this must not perturb, the single-egress rule for the plaintext token, and
  #1531's warning that wrapping `Save` alone leaves the stale-snapshot bug intact.
- `docs/protocol-mobile.md` § "Minting a pairing from a paired client", § "Application
  message types" — the two places that say nothing answers the frame.

## Context

`pyry pair` is the only minter, and it needs a shell on the daemon's host. A browser
build of the desktop client and a second person's client have none, so neither can
ever be paired. #2126 published the frame pair; this slice serves it. The four
blockers — the redemption window (#1528, #1529) and the `devices.json` lock (#1531) —
have landed, and together they are what makes wire minting acceptable: a minted token
that is never redeemed dies at `RedeemBy`, and a concurrent CLI write can no longer be
erased.

**No ADR.** Every decision here is an application of one already recorded — ADR 021
(pair CLI order of operations), ADR 025 § Security model (the remote-permissions gate)
— or of #2126's published contract. Nothing new is being decided about the pairing
model itself; the wire verb was decided in #2126.

**Sizing — the boundary is exceeded on two lines, deliberately, under the floor-wins
rule.** Production files: 7 against a ceiling of 5. Total written work: ~1900 lines
against 800. The other four lines hold comfortably — 3 new exported types, no
consumer call-site cascade, 5 acceptance criteria, 3 reject branches. Every candidate
child of this slice is a one-consumer slice of it: the shared mint step's only new
caller is this handler, the two reject codes have nothing emitting them without it,
a relay-side-only slice ships a handler whose seam is nil at every construction site
and is unverifiable end to end, and the docs correction means nothing without the
code. Cutting them apart reproduces the #1720 shape. The overage is house style
rather than scope — the same 400–650-line spec plus doc-comment density every analogue
of this slice paid (#1984 at 1244, #2054 at 1655, #2116 at 2974).

**File-overlap check (§ A2): one hit, and it is stale.** `origin/feature/449` touches
`internal/protocol/codes.go` and `internal/relay/v2session.go`. Issue #449 is CLOSED
(2026-05-17), has no PR in any state, and its last commit is four months old; its work
reached main by another route, and the branch is an uncleaned leftover rather than
in-flight work. No blocker is set. No other remote feature branch touches any file
this plan prescribes.

## Design

Three layers, and the split between them is the whole design: the relay is a courier,
the composition root decides, and the mint step is shared with the CLI so the two
stampings cannot drift.

### The seam — `internal/relay/v2session_seams.go`

`internal/relay` must not learn to mint. Minting needs `crypto/rand`,
`internal/pair`, `internal/keys`, `internal/identity` and `internal/audit`, none of
which this package imports, and the relay URL and server id it would have to be told.
So the handler is a courier and the decision lives at the wiring site — the shape
`HistoryPage`, `ModalResolver` and `AttachmentResolve` already use.

```go
type PairingMintOutcome int

const (
    PairingMintOK PairingMintOutcome = iota
    PairingMintUnauthorized
    PairingMintFailed
)

type PairingMintResult struct {
    Outcome PairingMintOutcome
    Pairing string // the pair.Encode string; A CREDENTIAL, and empty unless OK
}

type PairingMinter interface {
    MintPairing(requester *devices.Device, deviceName string) PairingMintResult
}
```

Four properties this shape buys, each of which is why it is not something else:

- **An outcome discriminant, never an error.** `HistoryPageResult`'s decision. The
  errors behind `PairingMintFailed` wrap the absolute `devices.json` path
  (`WithLock`'s "open lock %s", `Load`'s and `Save`'s own wraps), so an error crossing
  this seam would put a host path in reach of a handler whose whole discipline is
  that it holds none. The error dies at the one scope that ever holds it.
- **The authorization decision is the implementation's, not the handler's.** AC-2
  requires the refusal to be audited through `audit.Log`, which lives in `cmd/pyry`.
  Splitting "deny" from "record the denial" across the two packages is how an edit
  ends up with one and not the other — `questionResolverV2.admit`'s stated reason.
- **`Pairing` is the only field.** The success log carries the requesting and the
  minted device's names, and it is written by the implementation, which holds both.
  Carrying the minted name back across the seam would put a client-authored string
  into a package that has no reason to hold one, for a record it does not write.
- **Nil ⇒ consumed but inert.** The frame is intercepted, so it no longer draws
  `dispatch.Route`'s unknown-type reply, and not one byte of its payload is parsed.
  Every non-production construction site stays byte-identical, `AttachmentIntake`'s
  and `HistoryPage`'s posture.

`V2SessionConfig` gains one optional field, `PairingMint PairingMinter`.

### The handler — `internal/relay/v2session_mint_pairing.go` (new)

`handleMintPairing(ctx, s, plaintext)`, reached from a new `appFrameMintPairing` arm
of `appFrameWorker`, enqueued by a new `protocol.TypeMintPairing` case in
`dispatchAppFrame`. On the worker rather than inline on Run for the reason the two
attachment legs and the history leg are there: it takes a `flock(2)` and writes a
file, and Run holds the single-owner send `CipherState`.

Ordered gates, and the ordering is the security property:

1. **Nil `PairingMint` ⇒ inert**, before anything else, so no remote-authored byte is
   parsed by an unwired daemon.
2. **Envelope decode** — unreachable (`dispatchAppFrame` already decoded these bytes
   to match the type), no reply, never echo the error.
3. **Payload decode failure ⇒ `protocol.malformed`.** `MintPairingPayload`'s own
   block assigns this answer here. It covers the over-length `device_name`, which
   that type's `UnmarshalJSON` rejects. Nothing about the failure is echoed or
   logged: `encoding/json` quotes offending input into its error string.
4. **A `device_name` that is not a safe display string ⇒ `protocol.malformed`.** See
   *The label gate* below. This gate is what discharges the seam's precondition that
   the name has been shape-validated before it can be stored, rendered or logged.
5. **The seam**, whose outcome selects the answer:
   - `PairingMintOK` → one `pairing_minted` envelope, `InReplyTo` the request's id,
     unicast through `forwardToRun`.
   - `PairingMintUnauthorized` → `pairing.not_permitted`, not retryable.
   - `PairingMintFailed` → `pairing.unavailable`, retryable.

**The identity is `s.device` and nothing else.** The payload has no field the handler
reads as identity, and `s.device` is the record `handleNoiseInit` bound after
validating the presented token. A device-less conn denies: `MayAnswerRemotePermission`
is nil-receiver-safe.

**One emission route.** The reply is a single envelope, so there is no `Push` leg: the
reply and every reject alike go through `forwardToRun`, where Run seals them under
`s.send`. Emitting from the worker would be a concurrent `Encrypt` on the send
`CipherState` — a nonce reuse. Unicast to `s.connID`; never broadcast.

**No byte budgeting.** `serveHistoryPage`'s re-ask loop has no counterpart here: the
reply is one fixed-shape payload of roughly 400 bytes against the 65519-byte cap. The
only input to its size that is not fixed-width is the daemon's own configured relay
URL, which is operator-authored and is the URL the daemon dials.

#### The label gate

`MintPairingPayload.DeviceName` is remote-authored and bounded only in length; its own
doc block names two hazards the bound does nothing about, and says the value is
loggable only after shape validation. AC-4 requires the minted device's name in a log
record, and `pyry pair list` renders it to a terminal. Before this ticket the only
author of a device label was an operator at their own shell; after it, a paired client
is one.

So the handler refuses a name carrying any C0 control character, DEL, any C1 control
(U+0080–U+009F), or invalid UTF-8, and answers `protocol.malformed`. One predicate,
at the trust boundary, fail-closed, killing the log-injection newline and the ANSI
escape run (which begins with ESC, a C0 control) at the same time. The empty string is
not a refusal — it is the "client named no device" case the fallback serves.

It does **not** live in the shared mint step, and that is deliberate: `pyry pair
--name` is operator-authored input whose observable behaviour this ticket must not
change, and a check there would refuse a name the CLI accepts today.

### The wire codes — `internal/protocol/codes.go`

Two new constants in one `pairing.*` block, the family shape `attachment.*`,
`history.*` and `model_list.*` already use:

- `CodePairingNotPermitted = "pairing.not_permitted"` — permanent. The device is
  authenticated; the refusal is about privilege, which is why `auth.invalid_token` is
  wrong for it (it would tell a legitimate client its token was rejected and invite a
  re-pair).
- `CodePairingUnavailable = "pairing.unavailable"` — retryable, and one merged answer
  for a busy lock, a registry read or write failure, and an RNG failure. Merged
  deliberately: every cause can clear without the client changing anything, and
  distinguishing them publishes facts about the host rather than about the request.

**Neither is an oracle.** `pairing.not_permitted` tells a device only that it lacks
the remote-permissions flag — which it can already learn by answering any permission
modal and being denied — and nothing about the host, the registry, or any other
device. It is not conditioned on the requested name, so it cannot be used to probe
which names exist.

### The shared mint step — `cmd/pyry/pair.go`

One function both `runPairDefault` and the wire minter call, so the two stampings
cannot drift and `pyry pair`'s observable behaviour is unchanged. It is the block
`runPairDefault` holds today, lifted verbatim: the CSPRNG draw, the hash, the name
fallback, the single clock read, and load-mutate-save inside one `devices.WithLock`
region.

```go
type mintRequest struct {
    devicesPath            string
    lockWait               time.Duration
    deviceName             string // "" ⇒ the device-<hash8> fallback
    allowRemotePermissions bool
    grantorHash            string // "" ⇒ no grantor check (the CLI)
}

type mintedDevice struct {
    token string // plaintext; A CREDENTIAL
    name  string // the label the record was filed under
}

func mintDevice(req mintRequest) (mintedDevice, error)
```

- **`lockWait` is a parameter, not a constant.** The CLI is an operator-invoked
  one-shot and keeps `pairLockWait` (`devices.DefaultLockWait`, 5s). The wire path is
  a request path with a client waiting and a per-conn FIFO worker behind it, so it
  passes its own short bound — `redemptionLockWait`'s reasoning, and its doc comment's
  promise that its peers hold sub-millisecond regions is what makes a short wait
  sufficient rather than optimistic.
- **`grantorHash` is the revocation re-check**, and it is the one thing here that is
  not a lift. See § Security review finding 8-A. When non-empty, the step confirms
  inside the held lock, against the snapshot it is about to mutate, that a device with
  that hash is still present and still carries `AllowRemotePermissions`; otherwise it
  returns `errGrantorRevoked` having written nothing. The CLI passes "" and is
  byte-identical to today.
- **It never wraps the plaintext token into an error**, and none of the errors it
  returns can carry one: `rand.Read`, `WithLock`, `devices.Load` and `Registry.Save`
  are the only sources, and the registry holds hashes only. § Token visibility's
  argument, restated at the new call site.
- **The clock is read once**, so `RedeemBy - PairedAt` is exactly
  `devices.RedemptionWindow` rather than that window plus scheduling delay.

`runPairDefault` keeps its operation order: steps 1–5 (parse, config, relay,
server-id, static key) still run before any token exists, `mintDevice` is steps 6–9,
and step 10 (`pair.Render`) still gates on it.

### The minter — `cmd/pyry/pairing_mint_v2.go` (new)

`pairingMinterV2` implements the seam. Constructed in `startRelayV2` over values
already in scope there.

```go
func newPairingMinterV2(devicesPath, relayURL, serverID string, pub [32]byte, logger *slog.Logger) *pairingMinterV2
func (m *pairingMinterV2) MintPairing(requester *devices.Device, deviceName string) relay.PairingMintResult
```

Order, and it mirrors `questionResolverV2.admit`:

1. **The privilege gate, first, so nothing is created before it.**
   `requester.MayAnswerRemotePermission()` false ⇒ write the
   `audit.OutcomeDeniedUnauthorized` record and return `PairingMintUnauthorized`. No
   CSPRNG draw, no lock, no registry read.
2. **`mintDevice`** with `allowRemotePermissions: false` — a literal, not a parameter
   threaded from anywhere — and `grantorHash: requester.TokenHash`. An
   `errGrantorRevoked` maps to `PairingMintUnauthorized` and is audited the same way;
   any other error maps to `PairingMintFailed`.
3. **`pair.Encode`** over the four daemon-authored values, and the single mint log
   record.

**`AllowRemotePermissions` is false by construction, twice over**: the wire type has no
field for it (#2126's decision), and this call site passes the literal. A test pins
that a request cannot produce a privileged record.

**The relay URL is the daemon's**, taken from `relayWiring.relayURL` — the URL
`startRelay` actually dials, which `resolveRelayURL` resolved with `PYRY_RELAY_URL`
consulted. It can differ from what `resolveRelay` gives the CLI, and when it does the
daemon's is the one the new device has to reach.

**The audit record.** `audit.Entry` is modal-shaped; the mint rides it the way
`auditQuestion` rides it for a batch: `DeviceHash` / `DeviceLabel` from the requester,
`ModalClass` a compile-time class constant for this verb, `ModalID` empty (a mint has
no one-time nonce to name — inventing one would put a value in a field whose meaning
is "the modal nonce"), `Source` always remote.

### Wiring — `cmd/pyry/relay.go`

`startRelayV2` constructs the minter beside `attachmentResolve` and assigns
`PairingMint:` in the `V2SessionConfig` literal. `staticKey`, `serverID`,
`w.relayURL` and `resolveDevicesPath(w.instanceName)` are all already in scope; no
signature changes, and no other construction site of `V2SessionConfig` is touched.

## Concurrency model

- **No goroutine is spawned.** The handler runs on the conn's existing
  `appFrameWorker`, which exits on `ctx.Done()` or `s.done`.
- **One mint in flight per conn**, structurally: the worker is strictly FIFO and
  there is one per conn. That is the worker's shape rather than an invariant this
  file maintains, and it is why no per-verb concurrency limit is minted here.
- **Lock ordering is file lock → `Registry.mu`**, matching `runPairDefault` and
  `recordRedemption`. `devices.WithLock` is the `devices` package's sole acquirer and
  neither closure nests a second acquisition, so no inversion is reachable.
- **Read, mutate and save are one critical section.** #1531's lesson applies
  unchanged: wrapping the `Save` alone would still mutate a snapshot taken outside the
  lock and leave the bug intact. `mintDevice` loads inside the region.
- **The worker blocks for at most `lockWait`** while a peer holds the lock. Every
  peer's region is sub-millisecond, so the short bound is sufficient; a refusal is a
  retryable `pairing.unavailable` rather than a longer wait, because the frame behind
  it on this conn is waiting too.

## Error handling

| Condition | Wire answer | Retryable | Logged |
|---|---|---|---|
| `PairingMint` nil | none (consumed, inert) | — | debug, conn id only |
| Envelope undecodable | none (no id to correlate) | — | warn, conn id only |
| Payload undecodable, incl. over-length name | `protocol.malformed` | no | warn, static reason |
| Name carries a control character or invalid UTF-8 | `protocol.malformed` | no | warn, static reason — **never the name** |
| Requester unprivileged, or revoked mid-session | `pairing.not_permitted` | no | one `audit.Log` record |
| Lock busy, registry read/write failure, RNG failure | `pairing.unavailable` | yes | warn, the wrapped error (daemon-local; carries a host path, never a token) |
| Reply marshal failure | none | — | warn, no payload byte |
| `forwardToRun` refused (session tearing down) | none | — | debug |

Every reject message is a static constant. No value derived from an error, a path, a
device name or the encoded pairing reaches the wire on any arm.

**A mint that succeeds but whose reply is dropped** leaves a record whose token nobody
holds. That is harmless and self-clearing: the token is unusable to anyone (it was
never emitted) and the record expires at `RedeemBy`. The alternative — writing the
record only after the reply is sealed — is ADR 021's rejected ordering, where a
failure after emission prints a working-looking pairing the daemon would refuse.

## Testing strategy

**Unit — `internal/relay/v2session_mint_pairing_test.go`** (a fake `PairingMinter`;
table-driven where the arms differ only in input):

- Nil seam: the frame is consumed — no reply, and the fake is never called.
- An undecodable payload and an over-length `device_name` both answer
  `protocol.malformed`, and the reply carries none of the sent bytes.
- Each refused label shape — a newline, a bare CR, an ESC-prefixed ANSI run, a DEL, a
  C1 control, invalid UTF-8 — answers `protocol.malformed` and the seam is never
  called. The empty name reaches the seam.
- A permitted mint answers `TypePairingMinted`, `in_reply_to` the request's id, with
  the payload's `pairing` exactly what the seam returned.
- `PairingMintUnauthorized` → `pairing.not_permitted`, `Retryable` false;
  `PairingMintFailed` → `pairing.unavailable`, `Retryable` true.
- The requester handed to the seam is `s.device`, not anything from the payload.
- A no-leak assertion over every emitted frame and every log record on every arm: the
  credential the fake returns is a marker string that must appear in exactly one place
  — the success reply's `pairing` field — and nowhere else.

**Unit — `cmd/pyry/pairing_mint_v2_test.go`** (over a temp `devices.json`):

- An unprivileged requester: no record is created (the registry is byte-identical),
  the outcome is `PairingMintUnauthorized`, and exactly one audit record with
  `outcome=denied_unauthorized` is written.
- A privileged requester: the returned string `pair.Decode`s to the daemon's server
  id, relay URL and static pubkey, and its token hashes to a record that is present,
  named as asked, and has `AllowRemotePermissions` false.
- The name fallback is `device-<hash8>` of the minted token's hash when the request
  named none.
- `RedeemBy - PairedAt == devices.RedemptionWindow` exactly.
- A requester whose hash is no longer in the registry, and one whose record has had
  the flag cleared, are both refused and write nothing.
- Log capture: exactly one mint record, carrying both names and neither the token, the
  hash, nor any substring of the encoded pairing.

**Unit — `cmd/pyry/pair_test.go`**: the existing `pyry pair` assertions carry the
regression proof for the extraction; add one that `mintDevice` with `grantorHash: ""`
performs no grantor check.

**Guard — `cmd/pyry/relay_guard_test.go`**: `TypeMintPairing` moves from
`excludedTypes` to `inboundTypes` as "switch-intercepted". The guard makes this
mandatory the moment the case exists, so the build reddens on its own.

**End-to-end — `internal/e2e/relay_v2_mint_pairing_test.go`** (`//go:build e2e`, one
run, the `TestRelayV2_ConversationHistory` harness):

- `pyry pair --allow-remote-permissions` mints the requester; a second bare `pyry
  pair` mints an unprivileged device. A real daemon starts against `fakerelay`.
- The privileged phone sends `mint_pairing`; the reply decodes, and its `server` and
  `server_static_pubkey` equal the ones `pyry pair` printed on this host.
- **A second `fakephone` dials with the token out of that reply and completes the
  handshake to open** — the pairing works, end to end.
- `pyry pair list` shows the minted device; `pyry pair revoke` removes it. No new CLI
  verb.
- The unprivileged phone's `mint_pairing` is answered `pairing.not_permitted` and
  `pyry pair list` shows no new row.
- **The lock claim:** a `pyry pair` run is interleaved between daemon start and the
  wire mint, and both records are present afterwards — the CLI's is not erased.

## Open questions

1. **Does the reply publish the redemption deadline?** #2126 left the call here.
   Resolved: **no**. `PairingMintedPayload`'s key set is pinned by a committed test,
   a client's paste dialog has nowhere to put a deadline, and the 15-minute window is
   prose. Publishing one is a protocol change and belongs in its own ticket.
2. **Where does the mint step live?** Resolved: `cmd/pyry`, because the handler is a
   seam implementation there, which puts both callers in one package and needs no
   relay-side import of `internal/pair`.
3. **Is rate-limiting built?** Weighed in § Security review, threat 7. Resolved: no
   limiter, and the reasoning is recorded rather than left silent.
4. **`ModalID` on the audit record.** Resolved: empty. A mint has no one-time nonce,
   and filling the field with the conn id or the minted hash would put a value in a
   field whose documented meaning is the modal nonce.

## Security review

**Verdict:** PASS (one MUST FIX found and folded into the design above before commit;
recorded here as 8-A with its resolution).

**Findings:**

- **[Trust boundaries]** No findings. One explicit boundary: `handleMintPairing`,
  where the payload is decoded and the label gated. Past it the seam receives a
  shape-validated label and an authenticated `*devices.Device`. The requester's
  identity crosses no boundary at all — it is `s.device`, bound by `handleNoiseInit`
  after token validation, and the payload has no field the handler reads as identity.
  The seam's doc block states the discharged precondition the way `AttachmentIntake`'s
  and `HistoryPage`'s do.

- **[Tokens, secrets, credentials]** No findings; all four lifecycle stages are
  addressed. Creation: `crypto/rand`, 32 bytes, in the step `runPairDefault` already
  uses. Storage: `devices.HashToken` (SHA-256) only — the plaintext never reaches
  disk. Expiry: `RedeemBy` = `PairedAt + devices.RedemptionWindow` from one clock
  read (#1528, #1529), which is the property that makes wire minting acceptable at
  all. Revocation: per-device via `pyry pair revoke`, and see 8-A for its enforcement
  at write time. **Egress is exactly one place** — the `pairing` field of a
  `pairing_minted` payload, sealed in the AEAD envelope and unicast to the asking
  conn. It is never broadcast (no `Push` leg exists on this path), never logged, and
  never wrapped into an error: `PairingMintResult.Pairing` is empty on both non-OK
  outcomes, and the no-leak unit assertion checks every frame and every record.

- **[File operations]** No findings. The only file written is `devices.json`, through
  `Registry.Save`'s existing temp-file-plus-rename, so an interrupted mint cannot
  leave partial state. **No client input becomes a path component**: `device_name` is
  a JSON value inside the registry and nothing more, and `resolveDevicesPath` applies
  `sanitizeName` to the *instance* name, which is daemon-side. `WithLock` creates its
  sidecar at 0600 under a 0700 directory. No `Stat`-then-`Open` on this path.

- **[Subprocess / external command execution]** Not applicable — no `exec.Command`,
  no shell, no environment mutation anywhere on this path.

- **[Cryptographic primitives]** No findings, and no new primitive is introduced.
  `crypto/rand` for the token; SHA-256 for the stored hash; the auto-name exposes 8
  hex characters of that hash (32 bits, no preimage). No secret comparison happens
  here — token validation is `Registry.Validate`'s, at handshake, unchanged.

- **[Network & I/O]** No findings. Inbound size is capped twice already: the Noise
  transport frame at 65535 bytes, and `device_name` at `MaxDeviceNameBytes` by
  `MintPairingPayload.UnmarshalJSON`. Outbound is one fixed-shape payload of roughly
  400 bytes; the only non-fixed-width contributor is the daemon's own configured relay
  URL, which is operator-authored and is the URL the daemon dials, so an oversized one
  yields the transport's own `message.too_long` rather than a leak. Timeouts and
  deadlines are the transport's, unchanged. Per-conn concurrency is bounded to one
  mint at a time by the FIFO `appFrameWorker`.

- **[Error messages, logs, telemetry]** No findings, after the label gate. Every wire
  message is a static constant; no error, path, id or name is interpolated into a
  reply. **The log-injection hazard is real and closed at the boundary**: the minted
  device's name must be logged (AC-4) and rendered by `pyry pair list`, and until this
  ticket no client could author one — so the handler refuses any control character,
  C1 control or invalid UTF-8 before the name can be stored, logged or rendered. The
  `pairing.unavailable` cause is logged daemon-locally with its wrapped error, which
  carries the `devices.json` path and never a token; nothing derived from it reaches
  the wire. No metric or telemetry is emitted.

- **[Concurrency]** **8-A, MUST FIX — resolved in the design above.** As first
  drafted, the privilege gate read only `s.device`, the record captured at handshake.
  v2 revocation is connection-scoped — `handleNoiseInit` reloads `devices.json` before
  each `Validate`, so a revoked device's *live* session survives until it reconnects.
  On every other gated verb that staleness costs one more modal answer; on this one it
  lets a device the operator has just revoked mint a *fresh* credential, defeating the
  only remedy the operator has for the stolen-pairing threat this ticket's own context
  names. The window is closed by `mintRequest.grantorHash`: inside the held lock,
  against the snapshot about to be mutated, the mint confirms the requester's
  `TokenHash` is still present and still privileged, and returns `errGrantorRevoked`
  having written nothing otherwise. Deterministic code inside a lock already held —
  not a second stochastic gate — and it costs no extra read. **The broader
  connection-scoped-revocation posture is OUT OF SCOPE**: it is `handleNoiseInit`'s
  and it predates this ticket; it is unfiled and worth a ticket of its own.
  Otherwise: lock ordering is file lock → `Registry.mu` at every site, matching
  `runPairDefault` and `recordRedemption`; the read-mutate-save is one critical
  section (#1531's whole lesson); no goroutine is spawned; and an interrupted mint
  leaves either the old file or the new one, never a partial.

- **[Threat model alignment]** — `docs/protocol-mobile.md` § Security model.
  **Threat 3 (a hostile relay):** the credential exists on the wire only inside the
  AEAD-sealed envelope, unicast to the conn that asked; the relay sees ciphertext.
  **Threat 4 (token leak via phone):** widened on purpose, and #2126 published the
  widening — minting authority moves from "a shell on the host" to "any privileged
  paired device". Three things bound it: a minted device is *always* unprivileged, so
  the compromise cannot escalate itself (the wire type has no field for the flag and
  the call site passes the literal false); an unredeemed token dies in 15 minutes; and
  8-A makes revocation effective against the minting path immediately rather than at
  the thief's next reconnect. **Threat 7 (rate-limiting, deferred posture)** —
  weighed, and **no limiter is built**. The request is authenticated, gated on a flag
  only a shell on the host can grant, and serialised to one mint per round trip per
  conn by the FIFO worker, so the achievable rate is bounded by the client's own
  latency; the marginal cost of a mint is one sub-millisecond lock region and one
  registry append. The residual is `devices.json` growth from a compromised privileged
  client minting in a loop — it is bounded by that serialisation, the records expire
  at `RedeemBy`, and no abuse has been observed. Ship without a limiter and revisit on
  evidence; a limiter built now would be a defence for an unobserved failure mode, and
  the honest place for the argument is here rather than in silence.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07

## Revisions

### 2026-09-08 — the label gate does not check UTF-8 validity

**What changed.** § The label gate and § Testing strategy both prescribed refusing
invalid UTF-8 alongside control characters. `mintLabelIsDisplaySafe` checks only
the control-character predicate, and the test table has no invalid-UTF-8 row.

**Why.** The check is unreachable, so a test could not have reddened it.
`encoding/json` replaces every invalid input byte and every unpaired surrogate
with U+FFFD while decoding a string, so a decoded Go string is valid UTF-8 by
construction — the gate is only ever handed one. An assertion no test can fail is
worse than no assertion: it reads as coverage of a hazard that was closed
somewhere else. The reason now lives in the function's own doc block, where the
next reader meets it.

**What is unchanged.** Everything the gate exists for. Control characters, DEL and
the C1 range are refused, which is what kills the log-injection newline and the
ANSI escape run, and the answer is still `protocol.malformed`.

### 2026-09-08 — the Security review's "until this ticket no client could author a device label" is false

**What changed.** § Security review, *Error messages, logs, telemetry* argued the
log-injection hazard was fully closed on the premise that a device label had no
remote author before this ticket. It has had one since `register_push_token`:
`RegisterPushToken` hands the payload's client-supplied `device_name` to
`devices.Registry.UpdatePushRegistration`, which assigns it to
`devices.Device.Name` — the overwrite is deliberate and that function's doc block
says so — and nothing on that path checks the value's shape. So the *requesting*
device's name, which AC-4 requires in the success record and which `auditMint`
puts in `audit.Entry.DeviceLabel`, can carry a control character. The premise let
that name skip the gate the minted name gets.

**Why the fix is a correction rather than a second gate.** The exposure is
pre-existing and wider than this path: the same unchecked `Device.Name` already
reaches a daemon log from `RegisterPushToken` itself and from the rekey handler,
and an audit record from `auditQuestion` and both `modalResolverV2` sites. A
predicate applied at `MintPairing`'s log call alone would close none of those
while reading as though the hazard were handled — the "one and not the other"
split the `PairingMinter` seam comment warns about — and `cmd/pyry` cannot reach
`internal/relay`'s unexported predicate, so it would also be a second copy of the
rule. Filed as **#2219**, whose shape is one gate where the label enters the
registry, the way `MintPairingPayload`'s bound is one gate. Widening it across
those call sites is out of scope here (§ Scope Discipline).

**What is unchanged.** The minted label's gate, which is what this ticket owns:
`mintLabelIsDisplaySafe` still refuses every C0 control, DEL and C1 control at the
trust boundary before the name can be stored, logged or rendered. The corrected
claim, with #2219 named, now lives at `pairingMinterV2.MintPairing`'s success
record where the two names sit side by side.

### 2026-09-08 — two reject-table literals are respelled to satisfy build gates

**What changed.** § Testing strategy lists the refused label shapes, two of which
could not be written the obvious way:

- The C1 control row carried a **raw U+0085 byte** in its source literal.
  staticcheck ST1018 fails the build on that; it is now `\u0085`, which is the
  convention every other row in the table already followed.
- The ANSI row spelled a full colour run, whose CSI introducer `substrate-guard`
  bans in pyrycode source outside its allowlist — in a comment as well as in a
  literal. The row is now a **bare ESC**, and the coverage is identical:
  `mintLabelIsDisplaySafe` refuses at the first offending rune, and ESC is the
  byte every escape run starts with.

**Why it is recorded.** Both gates run late in `make check` — staticcheck fourth,
`substrate-guard` fifth — so the first red hid the second, and each cost a lap to
find. The reasons now live at the rows themselves so the next editor does not
restore either spelling.

**What is unchanged.** The minted label's gate, which is what this ticket owns:
`mintLabelIsDisplaySafe` still refuses every C0 control, DEL and C1 control at the
trust boundary before the name can be stored, logged or rendered. The corrected
claim, with #2219 named, now lives at `pairingMinterV2.MintPairing`'s success
record where the two names sit side by side.
