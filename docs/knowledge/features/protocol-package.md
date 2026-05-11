# `internal/protocol` — wire-format envelope, routing, error codes, v1 predicate

Pure-data leaf package. Declares the wire-format types for the mobile WebSocket protocol v1 — outer envelope, relay↔binary routing wrapper, error-code constants, type-name constants, and the `IsV1Compatible` predicate. No I/O, no goroutines, no `context`, no `slog`. Spec source-of-truth is `docs/protocol-mobile.md`.

Landed in #255. Per-type payload structs (the catalog the 16 type discriminators select) are #256 sibling tickets and slot into `Envelope.Payload (json.RawMessage)` via a second-pass `json.Unmarshal` at the dispatcher; first slice (`RegisterPushTokenPayload`) landed in #275, second slice (messaging + backfill payloads) landed in #272, third slice (conversations-read payloads) landed in #273, fourth slice (conversations-write payloads) landed in #274, fifth slice (handshake/control: `HelloServerPayload` / `HelloClientPayload` / `HelloAckPayload` / `ErrorPayload` / `AckPayload`) landed in #271.

## Files

```
internal/protocol/
├── envelope.go                  Envelope, RoutingEnvelope, ErrUnknownType / ErrUnsupported, IsV1Compatible, v1TypeSet
├── codes.go                     12 Code* string constants + 16 Type* string constants
├── push.go                      RegisterPushTokenPayload (#275) — register_push_token body
├── messaging.go                 SendMessagePayload, MessagePayload, BackfillSincePayload, MessageChunkPayload, BackfillDonePayload (#272)
├── conversations_read.go        ListConversationsPayload, ConversationsPayload, ConversationSummary (#273)
├── conversations_write.go       CreateConversationPayload, ConversationCreatedPayload, PromoteConversationPayload, ConversationUpdatedPayload (#274)
├── handshake.go                 HelloServerPayload, HelloClientPayload, HelloAckPayload, ErrorPayload, AckPayload (#271)
├── envelope_test.go             golden round-trip for Envelope (full + minimal) and RoutingEnvelope
├── compat_test.go               truth-table for IsV1Compatible + drift detectors
├── push_test.go                 golden round-trip for RegisterPushTokenPayload via Envelope.Payload
├── messaging_test.go            golden round-trip for each of the five #272 payloads via Envelope.Payload
├── conversations_read_test.go   golden round-trip for ListConversationsPayload / ConversationsPayload via Envelope.Payload
├── conversations_write_test.go  golden round-trip for each of the four #274 payloads via Envelope.Payload
├── handshake_test.go            per-type round-trip for handshake/control payloads (#271)
└── testdata/                    envelope_full.json, envelope_minimal.json, routing_envelope.json,
                                 register_push_token.json, send_message.json, message.json,
                                 backfill_since.json, message_chunk.json, backfill_done.json,
                                 list_conversations.json, conversations.json,
                                 create_conversation.json, conversation_created.json,
                                 promote_conversation.json, conversation_updated.json,
                                 hello_server.json, hello_client.json, hello_ack.json, error.json, ack.json
```

Seven production files. `envelope.go` carries the package's behaviour surface (two structs, two sentinels, one predicate). `codes.go` carries the wire-string constants (pure data, grouped by spec table order). `push.go` + `messaging.go` + `conversations_read.go` + `conversations_write.go` + `handshake.go` carry the per-type payload DTOs, one file per spec-section group — the full #256 catalog is now wired.

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

### Conversations-read payloads (#273)

Bodies of the conversation-listing request/response pair (`docs/protocol-mobile.md` § Message types → `list_conversations` / `conversations`). `list_conversations` is phone → binary; `conversations` is the binary's reply with `in_reply_to` set to the request's id. `ConversationSummary` is the row type, exported because it is the element type of `ConversationsPayload.Conversations`.

```go
type ListConversationsPayload struct{}

type ConversationsPayload struct {
    Conversations []ConversationSummary `json:"conversations"`
}

type ConversationSummary struct {
    ID            string    `json:"id"`
    Name          *string   `json:"name"` // *string + no omitempty: spec wire shows literal `null`; omitempty would drop the key.
    IsPromoted    bool      `json:"is_promoted"`
    Cwd           string    `json:"cwd"`
    LastMessageTS time.Time `json:"last_message_ts"`
    LastUsedAt    time.Time `json:"last_used_at"`
}
```

- **`ListConversationsPayload` is `struct{}`.** Spec shows `{}` on the wire; the type exists so the dispatcher can decode into a concrete value rather than `json.RawMessage`.
- **`ConversationSummary.Name` is `*string` WITHOUT `omitempty` — same discipline as `BackfillSincePayload.ConversationID`.** Spec example shows literal `"name": null` on one of the two rows (an unnamed scratch conversation). `*string` distinguishes "null on wire" (nil pointer) from "absent" and from "empty string"; dropping `omitempty` keeps the `null` literal on re-marshal (byte-identical to the spec fixture). The AC body said "`*T` + `omitempty`"; honouring that literally would silently break the byte-equivalent round-trip the same AC requires — spec wire shape wins. A multi-line WHY comment on the field is mandatory (the only field-level comment in `conversations_read.go` under the "default to no comments" rule); `TestConversationsPayload_RoundTrip`'s byte-equal check is the regression detector. The fixture carries both branches (one row `name=<string>`, one row `name=null`) so the round-trip exercises both.
- **`ConversationsPayload.Conversations` order is preserved verbatim from the wire — this type does not reorder.** Doc comment notes that the binary is the source of truth for ordering (e.g. most-recently-used first); a `Sort` / `SortMRU` helper or any ordering predicate is explicitly out of scope.
- **`LastMessageTS` / `LastUsedAt` are `time.Time` (RFC3339Nano-on-the-wire envelope rule).** Spec example uses `"2026-05-08T10:31:02Z"` (no fractional seconds); `time.Time.MarshalJSON` emits RFC3339Nano which omits the fractional component when none is present, so the round-trip is byte-identical with no custom marshaller. Tests use `time.Time.Equal`, never `==`. Same discipline as `Envelope.TS`, `BackfillSincePayload.SinceTS`.
- **Other required fields are non-pointer, no `omitempty`.** `ID` / `Cwd` are required `string`; `IsPromoted` is required `bool` (fixture covers both `true` and `false`). Validation that `IsPromoted == true` implies `Name != nil`, ID uniqueness, ordering invariants — all dispatcher / registry concerns.
- **`ConversationSummary` field declaration order matches the fixture's per-row key order** (`id, name, is_promoted, cwd, last_message_ts, last_used_at`); Go's `encoding/json` emits in declaration order, so this is what makes the byte-equal round-trip survive.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to `RegisterPushTokenPayload` and the messaging slice. The future dispatch handler reads `internal/conversations.Registry`, maps each `Conversation` row to a `ConversationSummary`, and sends a `conversations` envelope with `in_reply_to` set to the request id — registry-to-payload mapping is a downstream concern, not this package's.

Golden round-trip tests in `conversations_read_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against `testdata/list_conversations.json` / `testdata/conversations.json`. `TestConversationsPayload_RoundTrip` asserts both rows: row 0 has a non-nil `Name` pointer; row 1 has `Name == nil` (NOT `*c1.Name == ""` — would panic on nil deref AND be the wrong check). The `conversations.json` envelope rides with `in_reply_to: 3`, the first protocol fixture pinning `in_reply_to` alongside an array-carrying payload.

### Conversations-write payloads (#274)

Bodies of the conversation create/promote lifecycle (`docs/protocol-mobile.md` § Message types → `create_conversation` / `conversation_created` / `promote_conversation` / `conversation_updated`). Phone → binary: `create_conversation`, `promote_conversation`. Binary → phone: `conversation_created` (reply, rides `in_reply_to`), `conversation_updated` (broadcast on the server-id).

```go
type CreateConversationPayload struct {
    IsPromoted *bool   `json:"is_promoted"` // *T + no omitempty (see WHY comment on the struct)
    Name       *string `json:"name"`
    Cwd        *string `json:"cwd"`
}

type ConversationCreatedPayload struct {
    ID         string    `json:"id"`
    IsPromoted bool      `json:"is_promoted"`
    Cwd        string    `json:"cwd"`
    Name       *string   `json:"name"`
    LastUsedAt time.Time `json:"last_used_at"`
}

type PromoteConversationPayload struct {
    ConversationID string `json:"conversation_id"`
    Name           string `json:"name"`
    Cwd            string `json:"cwd"`
}

type ConversationUpdatedPayload struct {
    ID         string    `json:"id"`
    IsPromoted bool      `json:"is_promoted"`
    Name       *string   `json:"name"`
    Cwd        string    `json:"cwd"`
    LastUsedAt time.Time `json:"last_used_at"`
}
```

- **`*T` WITHOUT `omitempty` for every spec-optional field whose example wire shows `null`** — `CreateConversationPayload.{IsPromoted, Name, Cwd}`, `ConversationCreatedPayload.Name`, `ConversationUpdatedPayload.Name`. Same discipline as `BackfillSincePayload.ConversationID` (#272) and `ConversationSummary.Name` (#273). The rationale is documented once in detail on `CreateConversationPayload` and cross-referenced from the others; this is the only struct in the slice with three optional pointers in a row, so it's the natural home for the comment block. `omitempty` on a nil pointer would drop the key entirely and break byte-equivalent round-trip with the spec example.
- **`CreateConversationPayload.IsPromoted` is `*bool` — pointer-to-zero round-trips as the scalar.** Wire `false` survives as a pointer-to-false (NOT collapsed to nil); wire `null` would survive as nil. The test pins the pointer-to-false branch (spec example is `"is_promoted": false`); the wire-null branch is covered by `Name` / `Cwd` on the same struct, so the `*bool` shape is exercised end-to-end across the slice.
- **Field declaration order matches each spec example verbatim** — `_created` has `{ID, IsPromoted, Cwd, Name, LastUsedAt}`, `_updated` has `{ID, IsPromoted, Name, Cwd, LastUsedAt}` (note `Name` / `Cwd` swap). Go's `encoding/json` emits fields in declaration order; the byte-equal round-trip enforces the swap is correct.
- **`LastUsedAt` is `time.Time` (RFC3339Nano-on-the-wire envelope rule).** Spec example values (`"2026-05-08T10:34:01Z"` / `"2026-05-08T10:34:30Z"`) have no fractional seconds; `time.Time.MarshalJSON` emits RFC3339Nano which omits the fractional component when none is present, so the round-trip is byte-identical with no custom marshaller. Padding fixtures with `.000Z` would break it. Tests use `time.Time.Equal`, never `==`.
- **`PromoteConversationPayload` is the only fully-required struct in the slice.** All three fields (`ConversationID`, `Name`, `Cwd`) are non-pointer `string`, no `omitempty`. Promotion requires a name and an effective cwd, and the conversation_id must resolve to an existing row — semantic gates the dispatcher / registry (`Registry.Promote`'s `ErrPromotion*` sentinels) enforce.
- **`conversation_created.json` is the only fixture in the slice carrying `in_reply_to`** (`in_reply_to: 4`, matching the `create_conversation` frame at id 4). The test pins `env.InReplyTo != nil && *env.InReplyTo == 4`.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to #275, #272, #273. Required-field validation, name uniqueness, ID resolution, broadcast fan-out — all dispatcher / registry concerns.

Golden round-trip tests in `conversations_write_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against the matching fixture. Four flat test functions (no table-driven), each follows the sibling-slice template.

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

## Handshake / control payloads (#271)

Five DTOs that slot into `Envelope.Payload (json.RawMessage)` once the dispatcher reads `Envelope.Type`. Pure data — no methods, no constructors, no validation. Spec source: `docs/protocol-mobile.md` § Message types — `hello`, `hello_ack`, `error`, `ack`.

```go
type HelloServerPayload struct {
    Role             string   `json:"role"` // always "server"
    ServerID         string   `json:"server_id"`
    BinaryVersion    string   `json:"binary_version"`
    ProtocolVersions []string `json:"protocol_versions"`
}

type HelloClientPayload struct {
    Role             string     `json:"role"` // always "client"
    DeviceName       string     `json:"device_name"`
    ClientVersion    string     `json:"client_version"`
    ProtocolVersions []string   `json:"protocol_versions"`
    LastSeenTS       *time.Time `json:"last_seen_ts,omitempty"`
}

type HelloAckPayload struct {
    ProtocolVersion string `json:"protocol_version"`
    ServerID        string `json:"server_id"`
    ConnID          string `json:"conn_id"`
}

type ErrorPayload struct {
    Code        string `json:"code"`
    Message     string `json:"message"`
    Retryable   bool   `json:"retryable"`
    RetryAfterS *int   `json:"retry_after_s,omitempty"`
}

type AckPayload struct{}
```

Conventions:

- **Two `Hello*Payload` structs, not a union.** The binary's hello and the phone's hello share only the envelope type name (`"hello"`) and dispatch site; field sets diverge. `role` is the discriminator. Modelling as a single struct with mostly-optional fields would lose type-level encoding of which fields belong with which role and force every consumer to validate role-field consistency by hand.
- **Optional fields are `*T` + `omitempty`; required fields are non-pointer.** Only `LastSeenTS` and `RetryAfterS` carry `omitempty`. `time.Time` zero-value as sentinel for `LastSeenTS` was rejected — `time.Time{}` marshals as `"0001-01-01T00:00:00Z"`, which would pollute the wire.
- **`AckPayload` is `struct{}`.** `json.Marshal(AckPayload{})` emits `{}` byte-for-byte, matching the spec's `"payload": {}`.
- **Field declaration order matches the spec example order.** The JSON encoder emits fields in struct-declaration order; that's what the round-trip byte-equivalence check verifies. Reordering breaks tests.
- **No constructors, no methods, no validation.** Runtime enforcement of `Role` discriminators (a phone sending `role: "server"`, etc.) is the dispatcher's concern (#248–#250). The `Role` constant is documented in struct comments only.

Five fixture files under `testdata/` (one per type, each a complete `Envelope` with the payload inlined) drive five per-type `*_RoundTrip` tests in `handshake_test.go`. The tests reuse `readFixture` and `canonical` helpers from `envelope_test.go`. The byte-equivalence check (`canonical(out) == canonical(raw)`) is the load-bearing assertion; per-type field asserts exist to localise failure messages. The `hello_client.json` fixture's `last_seen_ts: "2026-05-08T08:14:02Z"` (no fractional seconds) pins the `time.RFC3339Nano` no-fractional round-trip behaviour.

Sibling payload slices not yet landed: messaging (`send_message` / `message`), conversations (`list_conversations` / `conversations` / `create_conversation` / `conversation_created` / `promote_conversation` / `conversation_updated`), backfill (`backfill_since` / `message_chunk` / `backfill_done`), push (`register_push_token`).

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
- Per-type payload structs beyond the now-complete #256 catalog — `RegisterPushTokenPayload` (#275), the five messaging + backfill payloads (#272), the conversations-read pair plus row type (#273), the four conversations-write payloads (#274), and the handshake/control payloads (#271). All slices are wired; no more #256 sub-tickets pending.
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
- Future consumers: `internal/dispatch` (#248), `internal/relay-client`, remaining payload slices (sibling tickets to #271)
