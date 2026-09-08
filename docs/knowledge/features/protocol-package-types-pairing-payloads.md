# Pairing request/reply payloads (#2126)

Wire vocabulary, in `internal/protocol/pairing.go` — the request an
already-paired client sends to mint a pairing for a second device, and the
daemon's reply carrying it. Declared here; served by [Inbound `mint_pairing`
(#2127) — `PairingMinter` seam](v2-session-manager-state-machine-inbound-mint-pairing-pairingminter-seam.md),
which was blocked on the redemption window (#1528, #1529) and the
`devices.json` lock (#1531) until both landed. Published contract:
[`docs/protocol-mobile.md` § Minting a pairing from a paired client](../../protocol-mobile.md#minting-a-pairing-from-a-paired-client).

```go
const MaxDeviceNameBytes = 128

type MintPairingPayload struct {
    DeviceName string `json:"device_name"` // no omitempty
}
```

`MaxDeviceNameBytes` bounds `MintPairingPayload.DeviceName` and, since #2219, a second
consumer: `RegisterPushTokenPayload.DeviceName`, checked in the handler rather than at
decode — see [`relay-package-handlers.md` § Display-safety
gate](relay-package-handlers.md#display-safety-gate-on-device_name-and-platform-2219). The
constant's placement (here, not beside `RegisterPushTokenPayload` in `push.go`) means a
reader of that type alone cannot discover from the type itself that its `DeviceName` is
bounded — unlike `MaxWorkspaceLabelBytes`, which sits beside the payload it bounds.

```go
type PairingMintedPayload struct {
    Pairing string `json:"pairing"` // opaque pair.Encode string
}
```

`TypeMintPairing = "mint_pairing"` / `TypePairingMinted = "pairing_minted"`,
filed in `v2OnlyTypes`, the partition test's `all` slice and the
`IsKnownAppType` rejects table. Since #2127 wired `dispatchAppFrame`'s
switch-intercept case, `TypeMintPairing` moved from `excludedTypes` to
`inboundTypes` (`cmd/pyry/relay_guard_test.go`) — the guard makes that move
mandatory the moment the case exists, so a handler landing without it reddens
the build on its own; `excludedTypes["TypePairingMinted"] = "reply"` is
unchanged, replies never join `inboundTypes`. See [Drift
detectors](protocol-package-drift-detectors.md) for what actually reddens the
build on an unpartitioned constant.

## The verb is named for what it does, not for the family it resembles

`mint_pairing` / `pairing_minted` was chosen over the `request_pairing` name
the ticket itself offered as a defensible alternative "for consistency" with
the six inbound `request_*` verbs. Every `request_*` verb reads something that
already exists and answers with a projection of daemon state; this one
**creates** — it mints a bearer credential and, via #2127, writes a durable
`devices.json` record. Filing it under the read family's prefix would make the
one distinction a client most needs to see — *this frame has a side effect on
the host* — invisible in the type string. It took the existing write-verb
shape instead: imperative verb in, past participle out, the same pattern as
`create_conversation` → `conversation_created` and `set_system_prompt`. A
future request-shaped verb that mutates state should ask the same question
before defaulting to `request_*` out of habit: does the reply project existing
state, or does the request cause a write? The two families are not
interchangeable dressing on the same mechanism.

## The reply carries the opaque encoded string, never the decoded fields

`PairingMintedPayload` has one field, `pairing` — the `pair.Encode` string a
paste-fallback dialog already accepts unchanged — and not the four fields
`pair.Decode` would produce. Three of those four (`server`, `relay`,
`server_static_pubkey`) are values the requesting client already holds, since
it is itself paired and connected through them; the fourth, `token`, is the
secret. Carrying it a second time as a flat field alongside the encoded string
would put two copies of a bearer credential on one wire frame and invite a
client to treat the loose copy as less sensitive than the string duplicating
it. Carrying none of the four is also what keeps `internal/protocol` free of
an `internal/pair` import — a reply payload that wraps another package's
already-encoded output should ship the opaque wrapper, not decode it and
re-declare its fields, or the wrapping package stops being a leaf.

## The pure-DTO posture now has a second decode-time reject, and the two conditions that earn one

[Messaging payloads](protocol-package-types-messaging-payloads.md) already
carries the package's one decode hook, `SendMessagePayload.UnmarshalJSON` —
normalisation, explicitly not validation, since nothing there is rejected on
content. `MintPairingPayload.UnmarshalJSON` widens the departure one step
further: it rejects a `device_name` over `MaxDeviceNameBytes`, an outright
content check in a package whose stated posture is "no `Validate()`, no
construction-time invariants." Two conditions earned it, not license to add a
check wherever a field wants one:

- **No downstream shape validator would otherwise carry it.** Every other
  bounded string in the package (`filename`, `attachment_id`) is bounded for a
  consumer that checks the value's *shape* anyway — the constant is cap
  arithmetic and enforcement sits at that consumer. `device_name` is a free
  display string with no shape rule at all; length is the only check that will
  ever exist for it, and no second validator downstream is coming to carry it.
- **More than one independent implementation is racing the same contract.**
  #2127 and two unfiled client slices are each being built against this
  declaration without visibility into each other. A bound that lives only in a
  doc comment is a bound each of them enforces differently, or forgets.

`MaxDeviceNameBytes = 128` is a native anchor, not a borrowed one — it is
**not** copied from `MaxAttachmentFilenameBytes` (255) even though the shape
matches, because that constant's value is POSIX `NAME_MAX` for a sanitised path
component and a device name is never a path. 128 is sized against the daemon's
own generated label (`device-` + 8 hex chars) and a hand-typed name, not
against a filesystem limit that has nothing to do with this field. As
`MaxAttachmentIDBytes`' own doc block warns, the bound is not a safety
property either way: 128 bytes holds an ANSI escape run or `../../../../etc/passwd`
several times over, so whoever renders the label to a terminal (`pyry pair
list`) or writes it to a line-oriented log owes its own escaping regardless of
the cap.

## A declare-ahead slice for a credential-minting verb has to carry its own authorization obligation forward

Security review's one MUST FIX that changed this plan: a one-key request that
yields a live bearer credential, served by a *different ticket*, concentrates
the whole risk of the pair in an obligation nothing in a wire-vocabulary-only
slice would otherwise record. `#2127` is where the handler is written, but the
requirement it must satisfy — answer only on a conn that is itself an
AEAD-authenticated paired device, and never accept an interactive-capability
flag as a substitute, since capability negotiation is an advertisement, not an
authorization — is stated in this ticket's own doc block on `TypeMintPairing`
and in the published spec section, not left for #2127 to rediscover.
Generalizes past this pair: any declare-ahead-of-handler slice whose reply
mints or returns a credential should write the handler's authorization
requirement into the declaration itself, because a split that separates
"what the frame looks like" from "who may trigger it" is exactly the shape
that drops the second half on the floor if nothing carries it across the gap.

## Related

- [Inbound `mint_pairing` (#2127) — `PairingMinter` seam](v2-session-manager-state-machine-inbound-mint-pairing-pairingminter-seam.md) — the handler this vocabulary was declared ahead of.
- [Messaging payloads](protocol-package-types-messaging-payloads.md) — the
  package's other decode hook, `SendMessagePayload.UnmarshalJSON`.
- [Attachment envelope types](protocol-package-constants-codes-go-envelope-types-attachments.md) —
  `MaxAttachmentIDBytes`' "a length ceiling is not a safety property" rule,
  applied here to `MaxDeviceNameBytes`.
- [History request/reply payloads](protocol-package-types-history-payloads.md) —
  the nearest declare-ahead-of-handler precedent (`excludedTypes["pending
  handler"]` filing, no e2e for the same reason).
- [Drift detectors](protocol-package-drift-detectors.md) — the classifier
  registries this pair's constants move through, and why only a committed
  fixture pins the wire string.
