# `internal/protocol` — wire-format envelope, routing, error codes, v1 predicate

Pure-data leaf package. Declares the wire-format types for the mobile WebSocket protocol v1 — outer envelope, relay↔binary routing wrapper, error-code constants, type-name constants, and the `IsV1Compatible` predicate. No I/O, no goroutines, no `context`, no `slog`. Spec source-of-truth is `docs/protocol-mobile.md`.

Landed in #255. Per-type payload structs (the catalog the 16 type discriminators select) are #256 sibling tickets and slot into `Envelope.Payload (json.RawMessage)` via a second-pass `json.Unmarshal` at the dispatcher; first slice (`RegisterPushTokenPayload`) landed in #275, second slice (messaging + backfill payloads) landed in #272.

## Files

```
internal/protocol/
├── envelope.go         Envelope, RoutingEnvelope, ErrUnknownType / ErrUnsupported, IsV1Compatible, v1TypeSet
├── codes.go            12 Code* string constants + 16 Type* string constants
├── push.go             RegisterPushTokenPayload (#275) — register_push_token body
├── messaging.go        SendMessagePayload, MessagePayload, BackfillSincePayload, MessageChunkPayload, BackfillDonePayload (#272)
├── envelope_test.go    golden round-trip for Envelope (full + minimal) and RoutingEnvelope
├── compat_test.go      truth-table for IsV1Compatible + drift detectors
├── push_test.go        golden round-trip for RegisterPushTokenPayload via Envelope.Payload
├── messaging_test.go   golden round-trip for each of the five #272 payloads via Envelope.Payload
└── testdata/           envelope_full.json, envelope_minimal.json, routing_envelope.json,
                        register_push_token.json, send_message.json, message.json,
                        backfill_since.json, message_chunk.json, backfill_done.json
```

Four production files. `envelope.go` carries the package's behaviour surface (two structs, two sentinels, one predicate). `codes.go` carries the wire-string constants (pure data, grouped by spec table order). `push.go` + `messaging.go` carry the per-type payload DTOs, one file per spec-section group; sibling slices from the #256 catalog (handshake/control #271 and the TBD conversations slice) will each own their own `*.go` file in the same shape.

## Types

### `RegisterPushTokenPayload` (#275)

Body of a `register_push_token` frame (`docs/protocol-mobile.md` § Message types → `register_push_token`). Phone → binary, sent on every WS connect; the future dispatch handler persists `(platform, token, device_name)` to `devices.json` and de-duplicates against the stored triple.

```go
type RegisterPushTokenPayload struct {
    Platform   string `json:"platform"`
    Token      string `json:"token"`
    DeviceName string `json:"device_name"`
}
```

- `Platform` is one of `"fcm"` (Android) or `"apns"` (iOS). Stays `string`, not an enum — an enum would force a converter at every internal call site for no observable wire-format gain, and per-spec the dispatcher is the validation point.
- All three fields are required (no `omitempty`, no pointers). Encode-side absence surfaces as zero-value `""` on the wire, which the dispatcher rejects via shape validation.
- Pure DTO: no methods, no constructors, no `Validate()`. The dispatcher (future ticket) owns validation and is the only legitimate consumer; logging `Payload` is forbidden (may contain tokens) per the security posture below.

Golden round-trip test in `push_test.go` decodes the spec example through `Envelope` → `Envelope.Payload` → `RegisterPushTokenPayload` and re-marshals byte-equivalently against `testdata/register_push_token.json`. The decode-from-`Envelope.Payload` path (not decode-from-raw-payload-bytes) exercises the exact composition the dispatcher will use.

This is the first slice of the #256 per-type payload catalog. Sibling slices for the remaining 15 v1 type discriminators land in their own tickets and own `*.go` files.

### Messaging + backfill payloads (#272)

Bodies of the five conversation-flow envelopes (`docs/protocol-mobile.md` § Message types → `send_message` / `message` / `backfill_since` / `message_chunk` / `backfill_done`). Phone↔binary direction varies by type; all five are grouped because `MessageChunkPayload.Messages` reuses `MessagePayload` per spec ("same shape as `message.payload`, multiple") and splitting them across files would either duplicate the row type or force a cross-slice dependency.

```go
type SendMessagePayload struct {
    ConversationID string `json:"conversation_id"`
    MessageID      string `json:"message_id"`
    Text           string `json:"text"`
}

type MessagePayload struct {
    ConversationID string `json:"conversation_id"`
    MessageID      string `json:"message_id"`
    Role           string `json:"role"`
    Text           string `json:"text"`
}

type BackfillSincePayload struct {
    SinceTS        time.Time `json:"since_ts"`
    ConversationID *string   `json:"conversation_id"` // *string + no omitempty
    MaxMessages    int       `json:"max_messages"`
}

type MessageChunkPayload struct {
    Messages []MessagePayload `json:"messages"`
}

type BackfillDonePayload struct {
    Delivered int `json:"delivered"`
}
```

- **`MessagePayload.Role` stays `string`, not a named `Role` enum.** Spec defines a closed set (`"user"`, `"assistant"`, `"system"`) but the binary already treats role-strings as `string`-typed elsewhere; a typed `Role` would force a converter at every internal call site for no observable wire-format gain, and the closed-set guarantee belongs at the dispatcher. Matches `RegisterPushTokenPayload.Platform`'s rationale.
- **`BackfillSincePayload.ConversationID` is `*string` WITHOUT `omitempty` — single subtle interaction.** Spec example shows literal `"conversation_id": null` on the wire (meaning "all conversations"). `*string` distinguishes "null on wire" (nil pointer) from "empty-string conversation id" at the boundary. `omitempty` is dropped because with it a nil pointer marshals as absent (key dropped); without it a nil pointer marshals as `null` — byte-identical to the fixture. A one-line WHY comment on the field is mandatory (the only comment in `messaging.go` under the project's "default to no comments" rule) so a future contributor doesn't "fix" the missing `omitempty` and silently break the round-trip. The byte-equal check in `TestBackfillSincePayload_RoundTrip` is the regression detector.
- **`BackfillSincePayload.SinceTS` is `time.Time` (RFC3339Nano on the wire) per the envelope timestamp rule.** Consumers compute on it (skew checks, ordering); tests use `time.Time.Equal`, never `==`. Same discipline as `Envelope.TS`.
- **`MessageChunkPayload.Messages` reuses `MessagePayload` directly — no duplicate row type.** Spec says "same shape as `message.payload`, multiple"; a `MessageChunkRow` clone would silently drift over time. The reuse is an explicit AC, pinned by `TestMessageChunkPayload_RoundTrip` which asserts ≥2 messages with distinct roles and IDs.
- **All required fields are non-pointer, no `omitempty`.** Encode-side absence surfaces as zero-value `""` / `0` on the wire; the dispatcher rejects malformed frames via shape validation. Empty `text` is wire-legitimate (semantic validation lives at the dispatcher); empty `messages` slice and zero `delivered` are wire-legitimate.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to `RegisterPushTokenPayload`. Required-field validation, role-set enforcement, ID monotonicity, clock-skew bounds — all dispatcher concerns.

Golden round-trip tests in `messaging_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against the matching fixture. `TestBackfillSincePayload_RoundTrip`'s byte-equal check doubles as a regression guard against `omitempty` being re-added to `ConversationID`. `message_chunk.json` carries 2 messages so the slice round-trip is non-trivially covered.

### `Envelope`

The outer wire shape every application frame conforms to (`docs/protocol-mobile.md` § Message envelope, lines 177–201). Field order matches the spec table verbatim.

```go
type Envelope struct {
    ID               uint64          `json:"id"`
    Type             string          `json:"type"`
    TS               time.Time       `json:"ts"`
    Payload          json.RawMessage `json:"payload"`
    InReplyTo        *uint64         `json:"in_reply_to,omitempty"`
    PayloadEncrypted bool            `json:"payload_encrypted,omitempty"`
}
```

- `TS` is `time.Time` (not `string`) — the dispatcher needs typed time for the binary's 7-day-back / 5-min-forward clock-skew cap (spec § Clock-skew handling) without re-parsing on every read. Marshals as RFC 3339 nano; round-trip caveat: `time.Time` carries a monotonic-clock reading stripped by JSON marshal, so tests compare via `time.Time.Equal`, never `==` or `reflect.DeepEqual` (per `docs/PROJECT-MEMORY.md:1071`).
- `Payload` is `json.RawMessage` to enable deferred decode: the dispatcher reads `Type` from the outer envelope, then unmarshals `Payload` into the per-type struct that `Type` selects. Also lets a malformed payload of a known type fail-loud at `protocol.malformed` with the offending envelope's `id` intact, instead of failing the outer parse.
- `InReplyTo` and `PayloadEncrypted` are `omitempty`. `payload_encrypted: false` MUST be omitted on the wire (the `envelope_full.json` fixture pins this).

### `RoutingEnvelope`

The relay-prepended `{conn_id, frame}` wrapper used on the binary↔relay leg only (spec § Routing envelope, lines 100–122). Phones never see it. The relay strips it before forwarding to phones and prepends it before forwarding to the binary.

```go
type RoutingEnvelope struct {
    ConnID string          `json:"conn_id"`
    Frame  json.RawMessage `json:"frame"`
}
```

`Frame` is `json.RawMessage` so the relay can splice without parsing payloads — a structural property of the design (the relay holds zero per-user state). The `routing_envelope.json` round-trip test pins the byte-preservation invariant: a future change to typed `*Envelope` for `Frame` would surface as a fixture mismatch.

## Predicate: `IsV1Compatible`

```go
func IsV1Compatible(env Envelope) error
```

Returns:
- `nil` when `env.Type` is in the v1 type set and `env.PayloadEncrypted` is false.
- `ErrUnsupported` when `env.PayloadEncrypted` is true (reserved for v2; spec § Reserved for v2, lines 684–699).
- `ErrUnknownType` when `env.Type` is empty or not in the v1 set.

**Check order is pinned: `PayloadEncrypted` first, `Type` second.** A frame failing both checks reports as `ErrUnsupported` — the stricter rejection wins. The order is observable through `errors.Is` at the call site; the truth-table test row `encrypted-with-unknown-type` pins it.

`v1TypeSet` is a package-private `map[string]bool` initialised at package init from the 16 `Type*` constants. The map is read-only after init; concurrent reads of an unmutated Go map are race-free per the Go memory model.

### What the predicate does NOT validate

- `Envelope.ID` non-zero or monotonic — connection-state, not framing.
- `Envelope.TS` skew bounds — clock-skew enforcement is the dispatcher's.
- `Envelope.Payload` shape — owned by the per-type structs (#256).
- `InReplyTo` references a real prior `id` — connection-state.
- Role-restricted types (e.g. a phone sending `hello_ack`) — dispatch concern.

These exclusions are restated in the predicate's doc-comment so a future regression can't widen the surface by accident.

## Sentinels and wire-code mapping

```go
var (
    ErrUnknownType = errors.New("protocol: unknown envelope type")
    ErrUnsupported = errors.New("protocol: unsupported envelope feature")
)
```

The package returns Go sentinels; **the dotted-string wire codes live at the call site**, not here. This follows the convention pinned in `docs/PROJECT-MEMORY.md` § "Refusal-to-wire-code mapping is the consumer's job, NOT the primitive's." `internal/conversations` already exports `ErrConversationNotFound` / `ErrConversationAlreadyPromoted` and lets the consumer (CLI, wire layer) map them. `internal/protocol` follows the same idiom.

Returning `string` from `IsV1Compatible` would couple this package to wire format. The cost of the convention is a single switch at the dispatcher (#248):

```go
if err := protocol.IsV1Compatible(env); err != nil {
    code := protocol.CodeProtocolMalformed
    switch {
    case errors.Is(err, protocol.ErrUnsupported):
        code = protocol.CodeProtocolUnsupported
    case errors.Is(err, protocol.ErrUnknownType):
        code = protocol.CodeProtocolUnknownType
    }
    return sendError(env.ID, code, err.Error(), false)
}
```

Sentinel error strings carry no input bytes and no payload contents — a malformed-envelope error returned upward never leaks token-shaped or PII-shaped data via the message.

## Constants (`codes.go`)

### Error codes (12)

Wire values for the `code` field of error payloads (spec § Error codes, lines 525–542). Naming convention: `Code<Category><Reason>` mirrors the dotted-string `category.reason` shape.

| Constant | Wire string |
|----------|-------------|
| `CodeProtocolUnknownType` | `protocol.unknown_type` |
| `CodeProtocolMalformed` | `protocol.malformed` |
| `CodeProtocolUnsupported` | `protocol.unsupported` |
| `CodeAuthInvalidToken` | `auth.invalid_token` |
| `CodeAuthTokenRevoked` | `auth.token_revoked` |
| `CodeServerBinaryOffline` | `server.binary_offline` |
| `CodeServerBinaryBusy` | `server.binary_busy` |
| `CodeConversationNotFound` | `conversation.not_found` |
| `CodeConversationAlreadyPromoted` | `conversation.already_promoted` |
| `CodeMessageTooLong` | `message.too_long` |
| `CodeRelayNoServer` | `relay.no_server` |
| `CodeRelayServerIDConflict` | `relay.server_id_conflict` |

### Envelope types (16)

Wire values for `Envelope.Type` (spec § Message types). The set is closed in v1; new types require a v2 envelope per the protocol's versioning policy (additive changes stay v1, breaking changes go v2).

| Group | Constants |
|-------|-----------|
| Handshake / control | `TypeHello`, `TypeHelloAck`, `TypeError`, `TypeAck` |
| Messaging | `TypeSendMessage`, `TypeMessage` |
| Conversations | `TypeListConversations`, `TypeConversations`, `TypeCreateConversation`, `TypeConversationCreated`, `TypePromoteConversation`, `TypeConversationUpdated` |
| Backfill | `TypeBackfillSince`, `TypeMessageChunk`, `TypeBackfillDone` |
| Push | `TypeRegisterPushToken` |

## Drift detectors

The 16-entry type list appears three times: in the `Type*` constants block (`codes.go`), in the `v1TypeSet` map literal (`envelope.go`), and in two test slices (`compat_test.go`). The triple-copy is **deliberate** — three explicit drift detectors fail loudly in CI when a new constant lands without the corresponding map entry:

- `TestIsV1Compatible` — runs every `Type*` constant through `IsV1Compatible` and asserts `nil` (catches "added a `Type*` const, forgot the map").
- `TestV1TypeSet_CoversAllExportedTypeConstants` — asserts `len(v1TypeSet) == 16` and every constant is keyed in the map.
- `TestErrorCode_Constants_MatchSpec` — exact-string match for each `Code*` constant against the spec's dotted string. Catches the "fat-fingered `protocol.unkown_type`" regression at the lowest possible cost.

Reflection over `go/types` was considered and rejected — heavier than three explicit assertions for a closed 16-entry set. If the v1 type set ever grows past ~50 entries (no plausible path under the protocol's versioning policy), revisit.

## Concurrency

Pure-data package. No goroutines, no locks, no shared-mutable state. `IsV1Compatible` is a pure function: same input, same output, allocation-free on the rejection path (returns one of three pre-existing values: `nil`, `ErrUnsupported`, `ErrUnknownType`). `v1TypeSet` is initialised at package init and never mutated.

## What's deliberately NOT in the package

- `Envelope.Validate()` method or `NewEnvelope(...)` constructor — `Envelope` has no construction-time invariants (every field independently settable, optional fields zero-valuable). Struct-literal-with-named-fields is the canonical shape.
- `AllV1Types []string` exported slice — no consumer needs it; YAGNI.
- `go:generate`-driven membership check — overkill for a 16-entry closed set.
- A `[]string` slice + linear scan for membership — duplicates the constant names twice (slice + constants); the map literal duplicates them once at the same indentation as the constants block, making drift visible at code review.
- Per-type payload structs beyond `RegisterPushTokenPayload` (#275) and the five messaging + backfill payloads (#272) — the remaining 10 slices of the #256 catalog land per-ticket, each in its own `*.go` file.
- WS close codes (`1000`/`1011`/`4401`/`4404`/`4409`) — transport concern, lives with #247 (WSS dial+handshake).
- Auth/dispatch wiring (`hello_ack`-on-connect, role-based type restriction) — #248–#250.
- A `Validate(*Envelope)` that gates on payload shape, ID monotonicity, or TS skew — those are dispatcher obligations, named in the predicate's doc-comment as out-of-scope.

## Security posture

Trust boundary is `json.Unmarshal` at the dispatcher. After the unmarshal succeeds, an `Envelope` value is "structurally well-formed JSON" but **not** "semantically validated." `IsV1Compatible` is the next gate after unmarshal, but it intentionally checks only the framing bits (`PayloadEncrypted`, `Type`).

Downstream obligations the dispatcher (#248) inherits:
- Apply a max-frame-size cap at the WS read boundary BEFORE this package's types are constructed; unbounded `[]byte` reaching `json.Unmarshal` is a DoS vector.
- Run `IsV1Compatible` on every decoded envelope.
- Decode `Payload` against the per-type struct selected by `Type` (#256 catalog).
- Enforce clock-skew caps on `TS`.
- Track `ID` monotonicity per connection.
- Never log `Envelope.Payload` (may contain tokens or PII).

The `payload_encrypted: true` v2 reservation is rejected via `ErrUnsupported` before any v2-shaped data could be processed.

## Consumers (deferred)

No production consumers in this slice. Future:
- `internal/relay-client` (binary→relay WS connection) — marshals `Envelope`, wraps in `RoutingEnvelope` for the relay leg.
- `internal/dispatch` (#248) — calls `IsV1Compatible`, maps sentinels to wire codes, decodes per-type payloads from #256's catalog.
- `cmd/pyry-relay` (future) — splices `RoutingEnvelope.Frame` byte-for-byte without parsing.
- Mobile clients — consume the JSON wire format directly (no Go binding); the test fixtures under `testdata/` double as the cross-language schema reference.

## Related

- Spec: `docs/protocol-mobile.md` — single source of truth for field names, optionality, wire semantics
- Convention: `docs/PROJECT-MEMORY.md` § "Refusal-to-wire-code mapping is the consumer's job"
- Sentinel-pattern precedent: `internal/conversations` (`ErrConversationNotFound` etc.)
- Future consumers: `internal/dispatch` (#248), `internal/relay-client`, payload catalog (#256)
