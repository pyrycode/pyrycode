# #2126 — declare the `mint_pairing` request and the `pairing_minted` reply

Wire vocabulary only. Two v2 control envelope types and their payloads, so the
handler slice (#2127) and the two unfiled client slices can each be built
against a published contract. Nothing here dispatches, mints, validates or
persists anything.

## Files read

- `internal/protocol/codes.go` → `TypeRequestModelList`, `TypeRequestSystemPrompt`,
  `TypeRequestHistory` / `TypeHistoryPage`, `TypeRecentWorkspaces` — the doc-block
  shape to copy, the v2-control lane argument stated three times over, and (in
  `TypeRecentWorkspaces`) the `dispatch.Route` family this ticket must **not** join.
- `internal/protocol/envelope.go` → `IsKnownAppType`, `inboundAppTypeSet` — read to
  confirm the refusal semantics this slice relies on. **Not modified.**
- `internal/protocol/system_prompt.go` → `RequestSystemPromptPayload`,
  `SystemPromptPayload` — the nearest whole-file template: a request/reply pair in
  its own file, one field each, doc blocks that argue the shape.
- `internal/protocol/history.go` → `RequestHistoryPayload` — the declare-ahead-of-handler
  precedent's payload, including the "every field is an unverified claim" posture.
- `internal/protocol/attachments.go` → `MaxAttachmentFilenameBytes`, `MaxAttachmentIDBytes`
  — the bound-constant shape, and `MaxAttachmentIDBytes`' honest "a length ceiling is
  not a safety property" paragraph, which this ticket's bound needs verbatim in
  substance.
- `internal/protocol/messaging.go` → `SendMessagePayload.UnmarshalJSON` — the package's
  only decode hook and the argument for why it exists; this slice extends that
  departure from normalisation to rejection and must say so.
- `internal/pair/payload.go` → `Payload`, `Encode`, `Decode`, package doc — the four
  fields, and the MUST-NOT-log rule the reply inherits.
- `internal/devices/device.go` → `Device.Name`, `Device.AllowRemotePermissions`,
  `Device.RedeemBy` — the destination of `device_name`, the flag that must **not** be
  on this wire, and the redemption window this slice cannot honour.
- `cmd/pyry/pair.go` → `runPairDefault`, `resolveDevicesPath`, `sanitizeName` — the one
  existing minter. Confirms `sanitizeName` guards the **instance** name, never the
  device name, so `device_name` is a JSON value and never a path component.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes` — the second,
  AST-walking drift detector; Assertion #3 is what actually reddens on a new constant.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `v2OnlyTypes`,
  `TestTypeConstants_V1V2Partition`, `TestInboundAppTypeSet_CoversAllExportedTypeConstants`
  — the four classifier surfaces, and the count that must **not** move.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — two lessons that shape
  the testing strategy below: the registries key on the Go **symbol**, so only a
  committed fixture catches a wire-**string** typo; and the classic `env`-round-trip
  test never marshals the payload struct, so it exercises no struct tag.
- `docs/knowledge/features/protocol-package-what-s-deliberately-not-in-the-package.md`
  — the "pure DTOs, no `Validate()`" posture this slice departs from once, and the
  wire-vocabulary-then-handler split it follows.
- `docs/knowledge/features/protocol-package-types-system-prompt-payloads.md` — the
  overview of the nearest analogue, including its classifier filing.
- `docs/protocol-mobile.md` → § Pairing flow, § Application message types,
  § Reading a conversation's system prompt, § Security model — the publication sites
  and the threat numbering.

## Context

`pyry pair` is the only minter of a pairing today, and it needs a shell on the
daemon's host. `runPairDefault` reads 32 random bytes, hashes them into a
`devices.Device`, and hands the tuple to `pair.Encode`. A browser build of the
desktop client and a second person's client both want a device paired without a
shell — the shape WhatsApp Web and Signal Desktop use, where an already-paired
device links the next one.

This slice declares the frame pair and publishes it. #2127 serves it, blocked on
the redemption window (#1528, #1529) and the `devices.json` lock (#1531).
Declaring ahead of serving is this repo's established sequencing —
#2052→#2054, #1983→#1984, #2113→#2116 — and it is what lets the two client
slices start now instead of waiting on three blockers.

No ADR is warranted: the lane decision, the naming rule and the
wire-vocabulary-then-handler split are all applications of decisions already
recorded (ADR 024's v2 hard cutover; #2052's naming rule). The documentation
phase should fold this pair into
`docs/knowledge/features/protocol-package-*` beside the system-prompt pair.

### Sizing — measured, over the line ceiling, built anyway

`parent none`, `grandparent none`, so a split is available. It is still the wrong
call, and the six boundaries say why:

| Limit | Boundary | This ticket |
|---|---|---|
| Production source files | ≤ 5 | **2** — `codes.go`, new `pairing.go` |
| Total written work | ≤ 800 | **~870** — over by ~9% |
| New exported types | ≤ 5 | **2** payload structs (+1 bound constant, +2 `Type*`) |
| Consumer call sites | ≤ 10 | **5** classifier edit sites, no cascade |
| Acceptance criteria | ≤ 5 | **5** |
| Reject branches | ≤ 10 | **1** — the `device_name` bound |

One line trips, and only once the spec doc counts. The two available split axes
both fail the **floor** rule rather than the ceiling: a request type with no
correlated reply declares half a contract, and each half's only consumer would
be its sibling plus #2127. § A1's tie-break is explicit — when the floor and the
ceiling disagree, the floor wins; state the overage and build. The nearest
analogue, #2113, shipped this exact shape at 885 insertions plus a 365-line spec
and merged clean as PR #2120.

This is smaller than the previous run's measurement (3 files, ~1050 lines): the
re-refined body moved the pair off `inboundAppTypeSet`, which drops the
`envelope.go` edit and the whole e2e.

## Design

### The lane: v2 control, and the refusal is the point

Both types are v2 **control** envelopes, intercepted at `dispatchAppFrame`
before `dispatch.Route` is consulted — the lane `TypeRequestHistory`,
`TypeRequestSessionSettings`, `TypeRequestModelList` and
`TypeRequestSystemPrompt` all ride. Neither joins `inboundAppTypeSet`, so
`internal/protocol/envelope.go` is not modified and
`TestInboundAppTypeSet_CoversAllExportedTypeConstants`' hardcoded `24` does not
move.

The lane binds harder here than on any of those four, because this verb's reply
is a bearer credential: `IsKnownAppType` returning `ErrUnknownType` is the
structural bar that stops a v1 client pushing a credential-minting verb into the
v1 handler chain at all. That refusal is asserted positively (§ Testing
strategy), and a mutant that files either type in `inboundAppTypeSet` reddens
both the refusal cases and the disjointness branch of
`TestTypeConstants_V1V2Partition`.

`TypeRecentWorkspaces` is the other family and is **not** the precedent: its own
doc block files it as a `dispatch.Route` read verb and justifies that with "no
untrusted input drives a filesystem operation, so it is NOT security-sensitive".
The inverse of that sentence is this ticket.

### The names: the write-verb family, not the `request_*` read family

`mint_pairing` / `pairing_minted`. The ticket offers `request_pairing` as a
defensible alternative for consistency with the six inbound `request_*` verbs;
this plan declines it, and the reason is a distinction those six share and this
verb breaks.

Every `request_*` verb is a **read**: it asks for something that already exists
and its reply is a projection of daemon state. This one **creates** — it mints a
credential and (via #2127) writes a durable `devices.json` record. The
vocabulary already has a family for that, named by action rather than by asking:
`create_conversation`→`conversation_created`,
`create_workspace_folder`→`workspace_folder_created`,
`set_system_prompt`, `new_session`, `archive_conversation`. `mint_pairing` /
`pairing_minted` is that family's shape exactly — imperative verb in, past
participle out.

Filing a state-mutating verb under the read family's prefix would make the one
distinction a client most needs to see — *this frame has a side effect on the
host* — invisible in the type string. #2052's rule cuts the same way it does for
the read verbs: the wire type string IS the contract, and a name chosen twice is
a name chosen wrong once.

### `internal/protocol/pairing.go` — a new file

Two payloads, one bound constant, one decode hook. Its own file for the reason
`system_prompt.go`, `history.go`, `settings.go` and `snapshot.go` each have one:
a frame family owns a file. This keeps the production surface at two files.

```go
// MaxDeviceNameBytes bounds MintPairingPayload.DeviceName. UTF-8 bytes, not runes.
const MaxDeviceNameBytes = 128

// MintPairingPayload — phone → binary, inbound v2 control.
type MintPairingPayload struct {
    DeviceName string `json:"device_name"` // optional; no omitempty
}

// UnmarshalJSON rejects a DeviceName over MaxDeviceNameBytes. The error names the
// bound and the observed byte count and NEVER the bytes themselves.
func (p *MintPairingPayload) UnmarshalJSON(b []byte) error

// PairingMintedPayload — binary → phone, correlated by the envelope's InReplyTo.
type PairingMintedPayload struct {
    Pairing string `json:"pairing"` // the pair.Encode string, opaque on this wire
}
```

Four shape decisions, each argued in the doc block rather than left to review:

**`device_name` carries no `omitempty`**, matching `RequestSystemPromptPayload`,
`RequestSessionSettingsPayload` and `RequestModelListPayload`, and for their
stated reason: there is no presence contract. Absent and empty are the *same*
case — "the client named no device" — which the handler answers by falling back
to the label `runPairDefault` already generates (`device-` + the first 8 hex of
the token hash). Keeping the key always on the wire lets a fixture pin the full
shape, and a `ZeroValue_KeyPresent` test reddens if an `omitempty` is added later
for tidiness.

**There is no field for the remote-permissions flag**, and the doc block says
why. `Device.AllowRemotePermissions` is documented as "never set or carried over
the wire"; a minted device is always unprivileged. A field here would let an
already-paired client escalate a *new* device to answer permission, trust and
destructive modals — the one gate ADR 025 § "Security model" puts on remote
answering — so its absence is a security property, not an omission.

**The reply carries the encoded string and nothing else.** AC #3 leaves the
decoded fields to the builder; this plan omits them. Three of `pair.Payload`'s
four (`server`, `relay`, `server_static_pubkey`) are values the requesting client
already holds — it is paired, and connected through that relay to that server —
so the only genuinely new one is `token`, the secret. Carrying it flat as well
would put a second copy of a bearer credential on the wire and invite a client to
treat the loose field as less sensitive than the string it duplicates. Carrying
none of them leaves one source of truth, keeps the frame at roughly 400 bytes
against a 65519-byte cap, and means `internal/protocol` needs no import of
`internal/pair` — which is what keeps this leaf package leaf-shaped.

**No field of the request is echoed into the reply**, and that is checked rather
than asserted: `pair.Payload` has exactly four fields and none is a name. So the
reply is daemon-authored end to end, and the sentence is total for this payload
rather than borrowed from a neighbour that enumerated different fields.

### `MaxDeviceNameBytes = 128` — a native anchor, and what it is not

The value is not borrowed from `MaxAttachmentFilenameBytes`. That constant is
255 because a filename is a sanitiser input for a single path component and
POSIX `NAME_MAX` is the ceiling; a device name is never a path, so the anchor
does not transfer even though the constant's *shape* does.

The honest anchor: a device label is one human-typed line rendered in a
`pyry pair list` column and in a client's device list. 128 bytes is eight times
the daemon's own generated label and far past any hand-typed name, while keeping
`devices.json` records small and one row unwrapped in a terminal.

**A length ceiling is not a safety property** — `MaxAttachmentIDBytes`' warning
transfers verbatim in substance. 128 bytes accommodates an ANSI escape run or a
newline-injection payload many times over, so this constant does nothing about
either hazard. Whoever renders the label to a terminal and whoever writes it to a
line-oriented log each owe their own escaping; the doc block says so at the
field.

### The decode hook — a stated posture departure

`internal/protocol` is a pure-DTO package: no `Validate()`, no construction-time
invariants. `SendMessagePayload.UnmarshalJSON` is the one existing hook and its
block is explicit that it is *normalisation, not validation* — nothing is checked
and nothing is rejected on content.

This slice widens that departure to **rejection**, once, and the argument is
specific to this field rather than a general licence:

- Every other bounded string in the package (`filename`, `attachment_id`) is
  bounded for a downstream consumer that must check the value's *shape* anyway,
  so the constant is cap arithmetic and enforcement belongs at that consumer.
  `device_name` has no shape rule at all — it is a free display string — so
  length is the only check that will ever exist, and there is no second validator
  downstream to carry it.
- Three separate implementations are being written against this contract right
  now (#2127 and two client slices), none of which can see the others. A bound
  that lives only in a doc comment is a bound each of them implements
  differently or forgets.
- The reject is fail-closed: an over-bound name is a rejected frame, never a
  silently truncated one. Truncating would let the label a client displays differ
  from the one the daemon stored.

Mechanics follow `SendMessagePayload.UnmarshalJSON` exactly: a local defined
`type alias MintPairingPayload` with no methods, so `json.Unmarshal` does not
recurse; a pointer receiver, so the method writes only through the caller's own
value; a decode failure propagated, never swallowed. The one production decode
site will be #2127, which answers `protocol.malformed` on it.

## Concurrency model

None. `internal/protocol` is a stdlib-only leaf data package: pure structs and
their (de)serialization, no goroutines, no shared state, no I/O. `UnmarshalJSON`
has a pointer receiver and writes only through the caller's own value; the
decoded string is freshly allocated and aliases nothing in the input buffer.

## Error handling

One reject branch in this slice: `device_name` over `MaxDeviceNameBytes` fails
the decode. The error names the failure category, the bound and the observed byte
count, and **never the bytes** — the rule `pair.ErrInvalidPayload`'s block states
for its own decoder, applied here because the value is remote-authored and lands
in line-oriented logs. A device name's *length* is not sensitive; its content is
untrusted.

No error **code** is minted. The rejects this frame pair will need —
"the daemon refuses to mint" and "the request was malformed" — are #2127's,
because that is the ticket that sends them, and minting a code here would publish
a vocabulary nothing emits (the #2090 shape this ticket's AC #5 warns about in
the doc). `protocol.malformed` already exists for the decode failure.

## Testing strategy

`internal/protocol/pairing_test.go`, plus two committed fixtures. Two lessons
from the drift-detectors overview shape the list, and both are the difference
between a real gate and a vacuous one:

1. **The three `compat_test.go` registries key on the Go symbol, never the wire
   literal.** Renaming `TypeMintPairing`'s value to `"mint_pairings"` moves
   consistently through all three and reddens none. Only a committed fixture read
   from `testdata/` catches a wire-string typo — so both fixtures are mandatory,
   not decorative.
2. **The classic `env`-round-trip shape never marshals the payload struct.**
   `env.Payload` is `json.RawMessage`, so `json.Marshal(env)` re-emits the
   fixture's own bytes and exercises no struct tag at all. A key-set assertion
   that marshals the bare payload is the only thing that pins the wire keys, and
   it also outlives a fixture regeneration that a round trip does not.

Cases:

- `TestMintPairingPayload_RoundTrip` — decode `testdata/mint_pairing.json` through
  `Envelope`, assert `Type == TypeMintPairing` and the decoded field, re-marshal,
  compare bytes. Pins the wire string.
- `TestPairingMintedPayload_RoundTrip` — same against
  `testdata/pairing_minted.json`, whose `in_reply_to` names the request fixture's
  envelope id, so the correlation scheme is described by landed bytes.
- `TestMintPairingPayload_WireKeys` / `TestPairingMintedPayload_WireKeys` — marshal
  a freshly populated struct, assert the key set is exactly `{device_name}` /
  `{pairing}`. This is where "the reply carries no second copy of the token and no
  decoded field" and "the request has no remote-permissions field" are *checked*.
- `TestMintPairingPayload_ZeroValue_KeyPresent` — the zero value marshals with
  `device_name` present, pinning the no-`omitempty` decision.
- `TestMintPairingPayload_OverBoundDeviceNameRejected` — a name of
  `MaxDeviceNameBytes + 1` bytes fails decode; one at exactly the bound succeeds
  (both edges, so an off-by-one in either direction reddens). Asserts the error
  text does **not** contain the offending bytes.
- `TestMintPairingPayload_UnknownFieldIgnored` — an unknown key decodes cleanly and
  leaves the declared field intact, per the spec's forward-compatibility rule.

Classifier edits, all in the same commit because a new exported `Type*` constant
is red by construction until every classifier names it:

- `compat_test.go` — both types into `v2OnlyTypes`; both into
  `TestTypeConstants_V1V2Partition`'s `all` slice, **v2 section**; two
  `-rejected` cases in `TestIsKnownAppType` expecting `ErrUnknownType`. Neither
  joins `allTypes`' known-good list, and
  `TestInboundAppTypeSet_CoversAllExportedTypeConstants`' `24` does not move.
- `cmd/pyry/relay_guard_test.go` — `excludedTypes["TypeMintPairing"] =
  "pending handler (#2127)"` per the `TypeRequestHistory` precedent, and
  `excludedTypes["TypePairingMinted"] = "reply"` per `TypeHistoryPage`. Neither
  goes in `inboundTypes`: Assertion #1 requires a live `dispatchAppFrame` case and
  this slice ships none.

**No e2e, deliberately.** A sealed `mint_pairing` is refused
`protocol.unknown_type` both before and after this commit, so an e2e asserting a
refusal code would pass identically on `main` and witness nothing. The
partition tests are the red-by-construction witness instead — which is what
#2113 and #2125 both shipped; neither added an `internal/e2e` file.

Gate: `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Documentation

`docs/protocol-mobile.md` only. The `docs/knowledge/features/` overview is the
documentation phase's.

- Two rows in § Application message types, in the shape of the
  `request_system_prompt` / `system_prompt` rows: direction, `no` handshake
  early-data, and a Notes cell that marks the request **security-sensitive because
  its reply is a bearer credential**, states in one sentence that the frame is
  **declared ahead of its handler** and names **#2127**, and links the section
  below.
- A short `### Minting a pairing from a paired client` section immediately after
  § Pairing flow — the placement AC #5 asks for, and the right one: this is the
  pairing flow's second entry point, beside the `pyry pair` one already described
  there. Field tables for both payloads, one example envelope pair, the
  never-logged rule, and the same declared-ahead sentence.

The declared-ahead sentence is load-bearing rather than courtesy: #2090 is the
standing example of a protocol document asserting behaviour nothing implements.
#2127 removes it.

## Open questions

1. **Should the reply carry the redemption deadline?** `Device.RedeemBy` exists,
   and without a deadline on the wire a client shows a pairing string with no
   expiry hint. Resolved as **no, for this slice**: AC #3 bounds the reply to the
   encoded string plus at most `pair.Payload`'s four fields, and the window's
   semantics are #1528/#1529's — publishing a field this slice cannot honour is
   the #2090 shape again. Recorded in § Security review as an out-of-scope finding
   naming the ticket that should decide it.
2. **Does the `device_name` bound belong at decode or at the handler?** Resolved
   as **decode**, argued above. If Phase B finds the hook fights the package's
   test helpers, the fallback is the constant plus a handler-side check, which
   would need a `## Revisions` entry and would leave AC #4 unmet — so it is a
   fallback, not an option.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The design has exactly one boundary: `device_name`
  crosses network → process at `MintPairingPayload.UnmarshalJSON`, which is a
  single named function rather than three scattered parses. The gap the first
  pass found was that the *type* signals nothing downstream — a handler author
  holding a `MintPairingPayload` has no cue that its one field is hostile.
  Addressed pre-commit: the field's doc block carries `RequestHistoryPayload`'s
  posture verbatim in substance — **one direction only, so every field is an
  unverified claim, always** — and names the two hazards by name (terminal
  rendering, log injection) rather than leaving them to be rediscovered.
  `PairingMintedPayload` is the inverse and the sentence is *total* rather than
  borrowed: `pair.Payload` has four fields, none of them a name, so no field of
  the request is echoed into the reply.

- **[Tokens, secrets, credentials]** MUST FIX, raised and addressed before this
  commit — the first draft of the Design section described the reply's field
  without stating the handling rule. `pair.Payload`'s package doc binds *its*
  callers; nothing carries that rule transitively to a different type in a
  different package. The doc block on `PairingMintedPayload` now states it
  directly: the string is a plaintext bearer credential, it is never logged, no
  decoded field of it is ever logged, and it must never leave the AEAD-sealed
  envelope. Generation is not this slice's — the doc names `runPairDefault` as the
  authority (32 bytes of `crypto/rand`, hashed with SHA-256 before storage) so
  #2127 and the two client slices do not each invent one.

- **[Tokens, secrets, credentials — lifecycle]** OUT OF SCOPE, named. Creation and
  storage are answered by `runPairDefault` and `devices.Registry`; **expiry** is
  `Device.RedeemBy`, whose semantics are #1528/#1529's, and **revocation** is
  `pyry pair rm` on the host with no wire verb at all. This slice publishes
  neither a deadline field nor a revocation verb — see Open question 1. The ticket
  that should decide whether a client can see the deadline is #2127, which is the
  first one blocked on the window work and therefore the first one that knows the
  answer.

- **[Network & I/O — the ungated-mint hazard]** MUST FIX, raised and addressed
  before this commit. This is the finding that changed the plan. A one-key request
  yields a reply carrying a live credential, and the handler is a *different
  ticket* — so the whole risk of the pair concentrates in an obligation nothing
  in this slice would otherwise record. The declaration now carries it as part of
  its own contract, in both the `TypeMintPairing` doc block and the
  `protocol-mobile.md` section: the handler MUST answer only on a conn that is
  itself an AEAD-authenticated paired device, and an interactive-capability gate
  is **not** a substitute for that check — capability negotiation is a feature
  advertisement, not an authorization. Leaving this to #2127 to remember is what a
  declare-ahead slice is structurally bad at, which is why it is written down
  here.

- **[Network & I/O — bounds and amplification]** No further findings. `device_name`
  is bounded at `MaxDeviceNameBytes`; the whole envelope is bounded at 65519 bytes
  by the transport; the reply is roughly 400 bytes (a server id, a relay URL, a
  64-hex token and a 44-character base64 key, base64url'd), so there is no size
  amplification. Unbounded *minting* by an already-paired hostile client — a
  `devices.json` that grows without bound, and many live credentials — is real and
  is § Security model threat 7's deferred rate-limiting posture; recorded in the
  doc as an obligation #2127 must weigh, not solved here.

- **[Error messages, logs, telemetry]** The decode error names the category, the
  bound and the observed byte count, and never the offending bytes —
  `pair.ErrInvalidPayload`'s own rule, applied because the value is remote-authored
  and reaches line-oriented logs. A device name's length is not sensitive; its
  content is untrusted. The assertion that the error text excludes the bytes is a
  test case above, not a review note.

- **[File operations]** No findings, and the reason is checked rather than assumed:
  `resolveDevicesPath` applies `sanitizeName` to the **instance** name, never to a
  device name, so `device_name` becomes a JSON *value* in `devices.json` and never
  a path component. The doc block states that as a constraint on #2127 — and states
  that `MaxDeviceNameBytes` would not be what protected it if that ever changed,
  since 128 bytes holds `../../../../etc/passwd` several times over.

- **[Subprocess / external command execution]** Not applicable by construction:
  `internal/protocol` is a stdlib-only leaf data package with no I/O and no exec.
  `device_name` reaches no `exec.Command` argument on any path this slice or
  #2127 describes — it is stored and displayed, never executed.

- **[Cryptographic primitives]** Not applicable to this slice, which mints nothing
  and compares nothing. The one crypto-adjacent decision it *makes* is negative and
  deliberate: the reply is an opaque string rather than the four decoded fields, so
  no key material is re-encoded here and `internal/protocol` gains no dependency on
  `internal/pair`.

- **[Concurrency]** Not applicable. Pure structs; no goroutines, no locks, no
  shared state. `UnmarshalJSON` takes a pointer receiver and writes only through
  the caller's own value, and the local defined `alias` type has no methods, so
  `json.Unmarshal` cannot recurse into it.

- **[Threat model alignment]** § Security model threat 4 (*token leak via phone*)
  is the threat this verb **widens**, and that is the design's intent rather than a
  regression: it moves minting authority from "someone with a shell on the host" to
  "any paired device", so a single compromised phone can mint further devices. The
  mitigation named there — per-device revocation — still applies, and the blast
  radius is unchanged in kind. The doc row states the widening plainly so a client
  author reads the frame as a privilege-propagation verb rather than a read. Threat
  3 (*relay operator MITM*) is why the reply must never leave the sealed envelope,
  stated above. Threat 1 (*prompt injection*) does **not** land: `device_name`
  reaches no `claude` prompt on any path — it is a registry label. A separate,
  smaller hazard in the same shape does land, and is named at the field rather than
  borrowed from threat 1: the label is rendered to a terminal by `pyry pair list`
  and may reach a log, so escaping is owed by those consumers.

- **[Privilege escalation]** No findings, by an absence that is load-bearing.
  `MintPairingPayload` has no field for `Device.AllowRemotePermissions`, whose own
  doc says it is "never set or carried over the wire". A minted device is always
  unprivileged, so an already-paired client cannot use this verb to create a device
  authorized to answer permission, trust or destructive modals — the one gate
  ADR 025 § "Security model" places on remote answering.
  `TestMintPairingPayload_WireKeys` is what keeps the absence checked rather than
  reviewed.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07

## Revisions

### 2026-09-07 — implementation

Two departures from the plan as committed, both additive; no design decision moved.

**A changelog entry was added to `docs/protocol-mobile.md`.** The plan's
Documentation section enumerated the table rows and the section only. Every
protocol change on this document carries a dated newest-first Changelog entry —
#2152, #2166, #2146, #2103 and #2099 all do — and AC #5's "dated" reads on it.
Adding one is the house pattern rather than a scope widening.

**One cross-reference was corrected before it landed.** The first draft of the
section linked "`allow_remote_permissions` is never carried over the wire" to
§ Modal (v2). That section does not state the rule — it is a daemon-side registry
field this document does not otherwise describe — so the link pointed at a section
that would not have supported the claim. Replaced with the plain statement plus
ADR 025 § "Security model", which is the form `devices.Device` itself uses.

**The plan's Testing strategy claims were mutation-verified rather than asserted**,
which the drift-detectors overview requires and which is what the four fixture and
key-set tests exist for. Four mutants, each run under `go test -overlay`:

1. `TypeMintPairing`'s wire value → `"mint_pairings"`: **only**
   `TestMintPairingPayload_RoundTrip` reddens. Confirms the drift-detectors
   overview's rule on this pair specifically — all four classifier registries key
   on the Go identifier and none of them moves, so the committed fixture is the
   sole pin on the wire string.
2. An `allow_remote_permissions` field added to `MintPairingPayload`:
   `_WireKeys`, `_ZeroValue_KeyPresent` and `_RoundTrip` all redden. The
   privilege-escalation absence is checked, not reviewed.
3. The bound comparison `>` → `>=`: `_DeviceNameBound/at-bound-accepted` reddens,
   so both edges are live.
4. `omitempty` added to `device_name`: **only** `_ZeroValue_KeyPresent` reddens —
   the round trip and the key set stay green, exactly as that test's own comment
   claims, because the fixture carries a name.

Both Open Questions resolved as the plan predicted: the bound is enforced at
decode (question 2), and no expiry field is published (question 1).
