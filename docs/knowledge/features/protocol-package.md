# `internal/protocol` — wire-format envelope, routing, error codes, v1 predicate

Pure-data leaf package. Declares the wire-format types for the mobile WebSocket protocol v1 — outer envelope, relay↔binary routing wrapper, error-code constants, type-name constants, and the `IsV1Compatible` predicate. No I/O, no goroutines, no `context`, no `slog`. Spec source-of-truth is `docs/protocol-mobile.md`.

Landed in #255. Per-type payload structs (the catalog the 16 type discriminators select) are #256 sibling tickets and slot into `Envelope.Payload (json.RawMessage)` via a second-pass `json.Unmarshal` at the dispatcher; first slice (`RegisterPushTokenPayload`) landed in #275, second slice (messaging + backfill payloads) landed in #272, third slice (conversations-read payloads) landed in #273, fourth slice (conversations-write payloads) landed in #274, fifth slice (handshake/control: `HelloServerPayload` / `HelloClientPayload` / `HelloAckPayload` / `ErrorPayload` / `AckPayload`) landed in #271.

## Files

```
internal/protocol/
├── envelope.go                  Envelope, RoutingEnvelope, ErrUnknownType / ErrUnsupported, IsV1Compatible, v1TypeSet
├── codes.go                     12 Code* string constants + 16 v1 Type* + v2-control Type* (TypeRekeyRequest, #454) + v2-interactive Type* (turn_state … turn_end, #607; + stall, #638) + v2-snapshot Type* (request_snapshot / screen_snapshot, #617) + v2-resync Type* (TypeResync, #647) + v2-session-boundary Type* (TypeSessionTransition, #656) + v2-modal Type* (modal_shown / modal_answer / modal_cancel / modal_dismissed, #701)
├── push.go                      RegisterPushTokenPayload (#275) — register_push_token body
├── messaging.go                 SendMessagePayload, MessagePayload, BackfillSincePayload, MessageChunkPayload, BackfillDonePayload (#272); SessionTransitionPayload (#656, v2 session-boundary marker body); ModalOption + ModalShownPayload / ModalAnswerPayload / ModalCancelPayload / ModalDismissedPayload (#701, v2 modal vocabulary bodies)
├── conversations_read.go        ListConversationsPayload, ConversationsPayload, ConversationSummary (#273)
├── conversations_write.go       CreateConversationPayload, ConversationCreatedPayload, PromoteConversationPayload, ConversationUpdatedPayload (#274)
├── handshake.go                 HelloServerPayload, HelloClientPayload, HelloAckPayload, ErrorPayload, AckPayload (#271); Capabilities []string on the two phone-facing hello payloads + CapabilityInteractive const (#607); LastEventID *uint64 on HelloClientPayload (#647, inbound reconnect-replay cursor)
├── interactive.go               TurnStatePayload, AssistantDeltaPayload, ToolUsePayload, ToolResultPayload, TurnEndPayload (#607), StallPayload (#638) — v2 interactive binary→phone event bodies
├── snapshot.go                  RequestSnapshotPayload, ScreenSnapshotPayload (#617) — v2 screen-snapshot request (phone→binary) / response (binary→phone) bodies
├── envelope_test.go             golden round-trip for Envelope (full + minimal) and RoutingEnvelope
├── compat_test.go               truth-table for IsV1Compatible + drift detectors
├── push_test.go                 golden round-trip for RegisterPushTokenPayload via Envelope.Payload
├── messaging_test.go            golden round-trip for each of the five #272 payloads via Envelope.Payload; + SessionTransitionPayload round-trip (#656); + four modal payload round-trips (#701)
├── conversations_read_test.go   golden round-trip for ListConversationsPayload / ConversationsPayload via Envelope.Payload
├── conversations_write_test.go  golden round-trip for each of the four #274 payloads via Envelope.Payload
├── handshake_test.go            per-type round-trip for handshake/control payloads (#271) + capabilities round-trips (#607)
├── interactive_test.go          golden round-trip for each of the five #607 interactive payloads + the #638 stall payload via Envelope.Payload
├── snapshot_test.go             golden round-trip for the two #617 snapshot payloads + empty-conversation_id boundary
└── testdata/                    envelope_full.json, envelope_minimal.json, routing_envelope.json,
                                 register_push_token.json, send_message.json, message.json,
                                 backfill_since.json, message_chunk.json, backfill_done.json,
                                 list_conversations.json, conversations.json,
                                 create_conversation.json, conversation_created.json,
                                 promote_conversation.json, conversation_updated.json,
                                 hello_server.json, hello_client.json, hello_ack.json, error.json, ack.json,
                                 turn_state.json, assistant_delta.json, tool_use.json, tool_result.json, turn_end.json, stall.json,
                                 request_snapshot.json, screen_snapshot.json,
                                 modal_shown.json, modal_answer.json, modal_cancel.json, modal_dismissed.json
```

Nine production files. `envelope.go` carries the package's behaviour surface (two structs, two sentinels, one predicate). `codes.go` carries the wire-string constants (pure data, grouped by spec table order). `push.go` + `messaging.go` + `conversations_read.go` + `conversations_write.go` + `handshake.go` carry the v1 per-type payload DTOs, one file per spec-section group — the full #256 catalog is wired — `interactive.go` (#607) carries the first v2 additive application-event DTOs, and `snapshot.go` (#617) carries the v2 screen-snapshot request/response DTOs.

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

### Session-transition payload (#656)

Body of an `Envelope` whose `Type == TypeSessionTransition` (`docs/protocol-mobile.md` § session_transition). Binary → phone; the wire form of a session boundary the phone renders as a `ThreadItem.SessionBoundary` marker (`pyrycode-mobile#336`) when the daemon's session rotates (a `/clear`, an idle eviction, or a workspace change) — instead of inferring the boundary from message fields that do not exist. Lives in `messaging.go` (not `interactive.go`: a session boundary is not a turn-stream event, and `messaging.go` already houses the `time.Time` + `*string`-no-omitempty precedents this struct copies). **Wire shape only** — the producer that emits it is sibling #657 (`security-sensitive`, blocked on #656).

```go
type SessionTransitionPayload struct {
    PreviousSessionID string    `json:"previous_session_id"`
    NewSessionID      string    `json:"new_session_id"`
    Reason            string    `json:"reason"`
    OccurredAt        time.Time `json:"occurred_at"`
    WorkspaceCwd      *string   `json:"workspace_cwd"` // *string + no omitempty: literal `null` for non-workspace_change reasons
}
```

- **`WorkspaceCwd` is `*string` WITHOUT `omitempty` — encodes a cross-field invariant on the wire.** Same discipline as `BackfillSincePayload.ConversationID` (#272): non-nil **iff** `Reason == "workspace_change"` (the new workspace dir), literal JSON `null` for `clear` / `idle_evict`. `omitempty` would drop the key and lose the "absent vs null vs value" distinction, so the *workspaceCwd-non-null-iff-`workspace_change`* invariant is decodable from the wire alone. The byte-equal round-trip is the regression detector against an accidental `omitempty` re-add (the `backfill_since.json` guard role).
- **`Reason` stays a plain `string`, not a named enum** — `MessagePayload.Role` / `TurnEndPayload.StopReason` precedent. Closed wire set `{clear, idle_evict, workspace_change}`; the closed-set guarantee belongs at the decoder. The mobile enum names (`Clear`/`IdleEvict`/`WorkspaceChange`) map to the lowercase-snake wire values by the mobile decoder. **The type admits `workspace_change` even though the producer (#657) cannot emit it** until a server-side workspace-change source exists — so the mobile decoder stays exhaustive and the invariant is expressible (type child carries the full vocabulary; producer child defers the unemittable value).
- **`OccurredAt` is `time.Time` (RFC3339Nano on the wire) per the envelope timestamp rule.** Marshal strips the monotonic clock; tests compare with `.Equal`, never `==`. Same discipline as `Envelope.TS` / `BackfillSincePayload.SinceTS`.
- **No `event_id` field.** `event_id` is an `Envelope`-level field (#649) stamped by the producer on structured-stream frames; a session boundary is **not** a structured turn-stream event and carries no `event_id`.

`TestSessionTransitionPayload_RoundTrip` (`messaging_test.go`) is table-driven over two fixtures authored in **struct-field order** (`canonical()` compacts but does not sort keys): `session_transition.json` (cwd-unset, `reason: "idle_evict"`, `"workspace_cwd": null` — the `omitempty`-regression guard) and `session_transition_workspace.json` (cwd-set, `reason: "workspace_change"`, `"workspace_cwd": "/home/user/project"`). See [codebase/656.md](../codebase/656.md).

### Modal v2 wire payloads (#701)

The wire vocabulary for a **modal** the supervised `claude` surfaces over the
encrypted mobile wire (`docs/protocol-mobile.md` § Modal; epic #597 Phase 3,
[ADR 025]). Lifecycle `modal_shown` → `modal_answer` / `modal_cancel` →
`modal_dismissed`. Five new exported types in `messaging.go` (a modal is a
control/boundary concern, not a turn-stream event, so `messaging.go` not
`interactive.go`), mapping to the four `Type*` constants. `modal_shown` /
`modal_dismissed` are outbound binary → phone events; `modal_answer` /
`modal_cancel` are **inbound phone → binary control** envelopes the v2 session
manager intercepts at `v2session.go`'s `dispatchAppFrame` **before**
`dispatch.Route` (the `RequestSnapshotPayload` / `TypeRekeyRequest` precedent —
**no `dispatch.Route` handler**). **Wire shape only** — the minting/dedup/
validation/fan-out runtime is the producer's (#703, with #706/#702 building
ownership/gating).

```go
type ModalOption struct { // a single ordered choice
    ID    string `json:"id"`
    Label string `json:"label"`
}

type ModalShownPayload struct { // binary → phone
    ModalID         string        `json:"modal_id"`
    Class           string        `json:"class"`
    Title           string        `json:"title"`
    Prompt          string        `json:"prompt"`
    Options         []ModalOption `json:"options"`           // ordered: array order is display/selection order
    DefaultOptionID string        `json:"default_option_id"` // MUST equal one of Options[].ID (documented invariant)
}

type ModalAnswerPayload struct { // phone → binary, inbound control
    ModalID     string `json:"modal_id"`
    OptionID    string `json:"option_id"`
    AnswerToken string `json:"answer_token"` // client-minted idempotency key
}

type ModalCancelPayload struct { // phone → binary, inbound control
    ModalID string `json:"modal_id"`
}

type ModalDismissedPayload struct { // binary → phone
    ModalID string `json:"modal_id"`
    Outcome string `json:"outcome"` // selected option id, or producer-defined cancel/timeout sentinel
    Source  string `json:"source"`  // closed set {remote, local, timeout}
}
```

- **No `omitempty` on any field** — the same deliberate inverse as the #607
  interactive and #617 snapshot payloads. Every field is always present so the
  fixtures pin the full shape and boundary values (an empty `default_option_id`,
  an empty `option_id`) cannot silently vanish. No `time.Time` field — the
  envelope's `ts` covers timing — so **no new import**.
- **`modal_id` is the sole correlation key — there is no `conversation_id`.** The
  daemon resolves `modal_id` against its **own** outstanding-modal state and never
  trusts a phone-asserted conversation; `option_id` maps against the daemon's own
  recorded option list. A shape carrying both `conversation_id` and `modal_id`
  would admit a disagreeing pair the daemon must adjudicate — the single-key shape
  forecloses cross-conversation `modal_id` confusion structurally. (Producer
  obligation: `modal_id` minted from `crypto/rand`, globally unique across
  concurrently-outstanding modals.)
- **`answer_token` is an idempotency key, not a credential.** Uniqueness and
  stability matter; secrecy does not. It lets the daemon collapse a replayed/
  reordered `modal_answer` to a no-op via `(modal_id, answer_token)`. It is **not**
  the authorization — that is `modal_id` validity (#706) + the per-device answer
  gate (#702, default OFF); `answer_token` only deduplicates among already-
  authorized answers.
- **`Options` is ordered + `DefaultOptionID ∈ Options[].ID`.** JSON-array order is
  the canonical display/selection order; the default-in-options invariant is
  documented (the producer enforces it).
- **`Source` is the closed set `{remote, local, timeout}`**; `Class` / `Outcome`
  stay plain strings whose exhaustive vocabularies the producer (#703) owns
  (documented, not enforced) — the `MessagePayload.Role` /
  `SessionTransitionPayload.Reason` leaf-data convention. Only `source` is pinned
  to a closed set because it is fully determined by the resolution mechanism.
- **`security-sensitive` rides the *shape* review, not code** — no handler ships,
  but this is the new inbound (phone→daemon) control surface for a high-consequence
  action. Architect security pass verdict **PASS**; it forecloses the
  cross-conversation-confusion class and keeps validity-gate vs dedup-key separate.

Four flat **one-func-per-type** round-trips in `messaging_test.go`
(`TestModalShownPayload_RoundTrip` asserts `len(Options)==2` + positional ids +
`DefaultOptionID`; `TestModalAnswerPayload_RoundTrip` asserts `AnswerToken`
round-trips per the AC; `TestModalDismissedPayload_RoundTrip` asserts
`Source=="remote"`), each on the shared `roundTripEnvelope` helper, over four
single-line fixtures authored in **struct-field order**. See
[codebase/701.md](../codebase/701.md).

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
    ID        uint64          `json:"id"`
    Type      string          `json:"type"`
    TS        time.Time       `json:"ts"`
    Payload   json.RawMessage `json:"payload"`
    InReplyTo *uint64         `json:"in_reply_to,omitempty"`

    // EventID — durable per-conversation event id (eventring); #649.
    EventID *uint64 `json:"event_id,omitempty"`

    PayloadEncrypted bool `json:"payload_encrypted,omitempty"`
}
```

- `TS` is `time.Time` (not `string`) — the dispatcher needs typed time for the binary's 7-day-back / 5-min-forward clock-skew cap (spec § Clock-skew handling) without re-parsing on every read. Marshals as RFC 3339 nano; round-trip caveat: `time.Time` carries a monotonic-clock reading stripped by JSON marshal, so tests compare via `time.Time.Equal`, never `==` or `reflect.DeepEqual` (per `docs/PROJECT-MEMORY.md:1071`).
- `Payload` is `json.RawMessage` to enable deferred decode: the dispatcher reads `Type` from the outer envelope, then unmarshals `Payload` into the per-type struct that `Type` selects. Also lets a malformed payload of a known type fail-loud at `protocol.malformed` with the offending envelope's `id` intact, instead of failing the outer parse.
- `InReplyTo`, `EventID`, and `PayloadEncrypted` are `omitempty`. `payload_encrypted: false` MUST be omitted on the wire (the `envelope_full.json` fixture pins this).
- **`EventID *uint64` (#649)** is the durable, per-conversation event id from the `internal/eventring` ring (`eventring.Ring.Append`'s return) — distinct from `ID`, the per-conn envelope counter that resets each reconnect. It is stamped **only** by the interactive structured-stream emitter (`cmd/pyry/interactive_turn_v2.go`'s `emit`), so a reconnecting phone can advertise the latest one it saw as `last_event_id` (`HelloClientPayload.LastEventID`, #647); the inbound consumer that accepts and replays from it is sibling #647 (`security-sensitive`; daemon code not yet merged — see [codebase/647.md](../codebase/647.md)). **Pointer + `omitempty`, mirroring `InReplyTo` exactly:** every other `Envelope{...}` construction site (v1 messaging, dispatch, non-interactive) leaves it nil → omitted → byte-identical wire ("absent, not null/0"). Ring ids are always ≥ 1, so a non-nil pointer never encodes `0`. `TestEnvelope_EventIDOmitempty` pins the omit/round-trip shape; the unchanged `envelope_full.json` / `envelope_minimal.json` fixtures are the byte-stability regression guard. See [codebase/649.md](../codebase/649.md) and [eventring-package.md](eventring-package.md).

### `RoutingEnvelope`

The relay-prepended `{conn_id, frame}` wrapper used on the binary↔relay leg only (spec § Routing envelope, lines 100–122). Phones never see it. The relay strips it before forwarding to phones and prepends it before forwarding to the binary.

```go
type RoutingEnvelope struct {
    ConnID    string          `json:"conn_id"`
    Frame     json.RawMessage `json:"frame"`
    Token     string          `json:"token,omitempty"`        // #308; phone→binary, first frame per conn_id only
    CloseCode uint16          `json:"close_code,omitempty"`   // #308; binary→relay only
}
```

`Frame` is `json.RawMessage` so the relay can splice without parsing payloads — a structural property of the design (the relay holds zero per-user state). The `routing_envelope.json` round-trip test pins the byte-preservation invariant: a future change to typed `*Envelope` for `Frame` would surface as a fixture mismatch.

`Token` (#308) carries the phone's device-pairing token from the relay to the binary on the **first** frame for a given `ConnID` only. Empty on subsequent frames and on every binary→phone frame. Populated by the relay from the `x-pyrycode-token` HTTP header at WS upgrade; consumed by `relay.AuthenticateFirstFrame` via the dispatcher's `FirstFrameGate` (#308). **SECURITY:** plaintext credential material — no layer may log it. `TestRoutingEnvelope_TokenOmitempty` pins the omitempty wire shape.

`CloseCode` (#308), when non-zero on a binary→relay routing envelope, asks the relay to forward `Frame` (if non-empty) to the phone and then close that phone's WS with this WS close code. Zero on every phone→binary frame; the dispatcher ignores `CloseCode` on inbound frames (a malicious relay cannot induce a self-close). Used today for the auth-reject path (4401); reserved for future binary-side close intents. `TestRoutingEnvelope_CloseCodeOmitempty` pins the omitempty wire shape.

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
    Token            string     `json:"token,omitempty"`        // #308; in-band device-pairing token under v2 (plaintext — MUST NOT be logged)
    Capabilities     []string   `json:"capabilities,omitempty"` // #607; phone's advertised feature set, e.g. [CapabilityInteractive]
    LastEventID      *uint64    `json:"last_event_id,omitempty"` // #647; durable event_id the phone last saw, for mid-turn reconnect replay (untrusted — consumer range/ring-bounds it)
}

type HelloAckPayload struct {
    ProtocolVersion string   `json:"protocol_version"`
    ServerID        string   `json:"server_id"`
    ConnID          string   `json:"conn_id"`
    Capabilities    []string `json:"capabilities,omitempty"` // #607; daemon's supported feature set (intersection with the phone's claim — enforced in #608)
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
- **`Capabilities []string` is additive + `omitempty` (#607).** Both phone-facing hello payloads gained it: the phone advertises its understood features in `hello`, the daemon echoes its supported set in `hello_ack`. `omitempty` is the byte-identical lever — a nil/empty slice drops the key (absent, not `null`), so the unedited `hello_client.json` / `hello_ack.json` fixtures round-trip byte-identically (same precedent as `RoutingEnvelope.Token`). The single defined value is `CapabilityInteractive = "interactive"` (the wire-vocabulary constant lives in `handshake.go` next to the field). This is **advertisement only** — the daemon intersecting the phone's claimed set with its own (echoing only what *it* supports, never blindly mirroring) and the capability-gated fan-out are the consumer's trust decision (#608), not this layer's. `TestHelloClientPayload_CapabilitiesRoundTrip` / `TestHelloAckPayload_CapabilitiesRoundTrip` pin both the round-trip and the omit shape; the pre-existing fixture round-trips stay unchanged as the byte-stability regression guard.
- **`LastEventID *uint64` is additive + `omitempty` (#647).** The phone's inbound reconnect-replay cursor: the durable `event_id` (the `Envelope.EventID` #649 surfaces outbound) it last saw, advertised on mid-turn reconnect so the daemon replays the missed tail from the `internal/eventring` ring or emits a `resync` marker. **Pointer + `omitempty` is load-bearing** — ring ids are always ≥ 1, so a non-nil pointer never encodes `0` and a nil pointer is omitted; a phone advertising none keeps the v1 hello byte-identical (key absent, not `null`). Same shape as `LastSeenTS`. This wire-type layer does **no enforcement** — `LastEventID` is **untrusted remote input**, and the consumer (`internal/relay`, #647, `security-sensitive`) range/shape-validates it and bounds replay by the ring. `TestHelloClientPayload_LastEventIDRoundTrip` pins the omit/round-trip shape. **Implementation caveat:** the #647 daemon consumer carries an unresolved code-review MUST FIX and is not yet merged — see [codebase/647.md](../codebase/647.md). The wire field itself is stable.

Five fixture files under `testdata/` (one per type, each a complete `Envelope` with the payload inlined) drive five per-type `*_RoundTrip` tests in `handshake_test.go`. The tests reuse `readFixture` and `canonical` helpers from `envelope_test.go`. The byte-equivalence check (`canonical(out) == canonical(raw)`) is the load-bearing assertion; per-type field asserts exist to localise failure messages. The `hello_client.json` fixture's `last_seen_ts: "2026-05-08T08:14:02Z"` (no fractional seconds) pins the `time.RFC3339Nano` no-fractional round-trip behaviour.

Sibling payload slices not yet landed: messaging (`send_message` / `message`), conversations (`list_conversations` / `conversations` / `create_conversation` / `conversation_created` / `promote_conversation` / `conversation_updated`), backfill (`backfill_since` / `message_chunk` / `backfill_done`), push (`register_push_token`).

## Interactive event payloads (#607, #638)

The **v2 additive application events** — the wire representation of
`internal/turnevent`'s neutral turn-event model (#606). All six are **binary →
phone only**, sent **only** to a phone whose `interactive` capability was echoed in
`hello_ack`; an old phone never sees them and keeps the coarse v1 `message`
fan-out. Spec source: `docs/protocol-mobile.md` § Interactive events. They map 1:1
to the `Type*` constants `TypeTurnState` / `TypeAssistantDelta` / `TypeToolUse` /
`TypeToolResult` / `TypeTurnEnd` (all #607) and `TypeStall` (#638). The first five
are the wire form of ACP-shaped turn events; `stall` is the wire form of an
**internal-only** signal (no ACP equivalent), added in #638.

```go
type TurnStatePayload struct {
    ConversationID string `json:"conversation_id"`
    State          string `json:"state"` // "thinking" | "responding" | "idle"
}

type AssistantDeltaPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    Seq            int    `json:"seq"`  // per-turn, non-negative, resets each turn
    Text           string `json:"text"` // coalesced chunk, not per-token
}

type ToolUsePayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    ToolUseID      string `json:"tool_use_id"`
    Name           string `json:"name"`
    InputSummary   string `json:"input_summary"` // human-readable précis, not raw input
}

type ToolResultPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    ToolUseID      string `json:"tool_use_id"` // matches the tool_use this completes
    IsError        bool   `json:"is_error"`
    ResultSummary  string `json:"result_summary"` // human-readable précis, not raw output
}

type TurnEndPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    StopReason     string `json:"stop_reason"` // turnevent.TurnEndReason values, verbatim
}

// #638 — the wire form of the internal-only turnevent.Stall onset marker.
type StallPayload struct {
    ConversationID string `json:"conversation_id"`
}
```

- **No `omitempty` on any field — the deliberate inverse of the handshake/optional
  discipline.** Every field is always present on the wire so the fixtures pin the
  full shape and boundary zero-values can't silently vanish: `assistant_delta` with
  `seq: 0` and `tool_result` with `is_error: false` are pinned exactly. Pick the tag
  by whether a field's absence is meaningful — here it never is.
- **`State` and `StopReason` stay plain `string`, not named enums.** Same
  `MessagePayload.Role` precedent: the closed-set guarantee belongs at the consumer,
  not in the wire type. `State` is documented (`thinking` / `responding` / `idle`)
  in the struct doc comment; #608 picks the exact internal-event → state mapping.
- **`StopReason` carries the `turnevent.TurnEndReason` strings verbatim** (`end_turn`
  / `max_tokens` / `max_turn_requests` / `refusal` / `cancelled`) **without
  importing `internal/turnevent`** — `protocol` stays a stdlib-only leaf, and #608
  produces the field via `string(turnevent.TurnEnd.Reason)`. The wire-value/taxonomy
  alignment is documented, not enforced by a shared type. ADR 025's base `turn_end`
  shape is `{conversation_id, turn_id}`; `stop_reason` is the #607 extension per the
  ticket title, following the "spec follows the code" convention (ADR 025
  § Consequences).
- **`Seq` is `int`, not `uint64`.** A per-turn counter that resets each turn (the
  package count-field idiom: `BackfillDonePayload.Delivered`,
  `BackfillSincePayload.MaxMessages`); `uint64` is reserved for the
  session-monotonic `Envelope.ID`.
- **`StallPayload` (#638) carries `conversation_id` only — no `turn_id`.** Like
  `turn_state`, a stall is a coarse conversation-level signal, not turn-scoped. It
  is the wire form of the internal-only `turnevent.Stall` (an onset-only marker:
  no clearing field — the phone self-clears on the next turn activity). The
  internal `Stall` carries no identity, so the bridge (#608 / #624-B) supplies
  `ConversationID` at wire-mapping time. Same no-`omitempty` discipline as its five
  predecessors. `internal/protocol` does **not** import `internal/turnevent` — the
  two layers are decoupled, bridged only at the string value `"stall"`.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to
  every v1 slice. The intersection-of-capabilities trust decision, the
  internal-event → envelope mapping, and the capability-gated push all live in the
  consumer (#608).

Six golden round-trip tests in `interactive_test.go` decode each fixture through
`Envelope` → `Envelope.Payload` → per-type struct, assert each field (incl. the
boundary `Seq == 0` / `IsError == false` and `StopReason == "end_turn"`), then
re-marshal byte-equivalently. The shared `roundTripEnvelope` helper re-marshals the
**decoded payload struct** (not the original `RawMessage`) back into the envelope —
that is what pins struct → wire shape, since a missing or reordered json tag only
surfaces when the bytes are actually re-encoded (the original-`RawMessage`-passthrough
variant cannot catch it).

## Screen-snapshot payloads (#617)

The request/response pair behind ADR 025's always-available, parser-independent
**screen snapshot** — the floor of the safe-degradation strategy (ADR 025 § Safe
degradation). The phone may ask for a one-shot text picture of the current claude
screen at any time; because the snapshot depends on no screen parser it survives any
parser break and backs the stall fallback. Spec source: `docs/protocol-mobile.md`
§ Screen snapshot. The pair maps 1:1 to the `Type*` constants `TypeRequestSnapshot`
(phone → binary control) and `TypeScreenSnapshot` (binary → phone event).

```go
type RequestSnapshotPayload struct {
    ConversationID string `json:"conversation_id"`
}

type ScreenSnapshotPayload struct {
    ConversationID string    `json:"conversation_id"`
    Text           string    `json:"text"` // plain rendered text only; never raw control codes
    TS             time.Time `json:"ts"`
}
```

- **`request_snapshot` is an inbound v2 *control* envelope, not an application event.**
  Structurally like `TypeRekeyRequest`: the v2 session manager intercepts it at the
  dispatch boundary **before** `dispatch.Route`. There is **no `dispatch.Route`
  handler** for it — the doc comment says so explicitly so the next reader does not
  look for a handler that isn't there. The interception, the render via tui-driver,
  and the push of `screen_snapshot` back are the consumer ticket's job.
- **`ScreenSnapshotPayload.Text` is plain rendered text only, NEVER raw terminal
  control codes.** This is the load-bearing invariant: it preserves ADR 025's
  no-raw-bytes guarantee and the substrate seal — the snapshot is a literal-screen
  picture rendered to text, not a stream of escape sequences. The struct doc comment
  states this.
- **`TS` is `time.Time` (RFC3339Nano on the wire).** Records when the snapshot was
  rendered. The monotonic-clock reading strips on JSON marshal, so tests compare with
  `time.Time.Equal`, never `==` or `reflect.DeepEqual` — same discipline as
  `Envelope.TS` and every other `time.Time` payload field.
- **No `omitempty` on any field — the same deliberate inverse as the #607 interactive
  payloads.** Every field is always present on the wire so the fixtures pin the full
  shape and boundary values (an empty `conversation_id`, a zero `ts`) cannot silently
  vanish. `TestSnapshotPayloads_EmptyConversationID` pins the empty-`conversation_id`
  boundary for both payloads.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to
  every prior slice. Accepting the inbound frame, rendering the screen, and returning
  content to the remote party are the consumer's trust decision — that consumer (the
  screen-snapshot handler child) carries the `security-sensitive` label; this leaf
  declaration does not.

Two golden round-trips in `snapshot_test.go` decode each fixture through `Envelope`
→ `Envelope.Payload` → per-type struct and re-marshal byte-equivalently via the shared
`roundTripEnvelope`. `screen_snapshot.json` carries a **multi-line** `text`
(`"line one\nline two\nline three\n"`, asserted with `strings.Contains(…, "\n")`) so
the canonical compare pins the escaped multi-line shape, and a whole-second payload
`ts` (`"2026-05-08T10:33:14Z"`) so the re-marshal is byte-identical — `time.Time`'s
RFC3339Nano output trims trailing fractional zeros, so a `.120Z` value would re-emit
as `.12Z` and break the compare (the same fixture-`ts` gotcha that governs the
envelope's own timestamp).

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

### Envelope types

Wire values for `Envelope.Type` (spec § Message types). Two architectural partitions: 16 v1 application types (closed; consumed by `dispatch.Route` via `v1TypeSet`) and the **v2-only** set whose members are **deliberately NOT** in `v1TypeSet`. The v2-only set itself spans two flavours: **inbound control envelopes** (`TypeRekeyRequest` (#454), `TypeRequestSnapshot` (#617), and `TypeModalAnswer` / `TypeModalCancel` (#701)), intercepted at the v2 dispatch boundary (`internal/relay/v2session.go`'s `dispatchAppFrame`) before `dispatch.Route` is called; and **outbound binary → phone events** never dispatched inbound (the five #607 interactive types, `TypeStall` (#638), `TypeScreenSnapshot` (#617), `TypeResync` (#647), `TypeSessionTransition` (#656), and `TypeModalShown` / `TypeModalDismissed` (#701)). Adding either to `v1TypeSet` would silently route the envelope to the v1 handler chain (or expose it to an old phone) — exactly the opposite of what's wanted.

**v1 application types** (16; spec § v1 Message types):

| Group | Constants |
|-------|-----------|
| Handshake / control | `TypeHello`, `TypeHelloAck`, `TypeError`, `TypeAck` |
| Messaging | `TypeSendMessage`, `TypeMessage` |
| Conversations | `TypeListConversations`, `TypeConversations`, `TypeCreateConversation`, `TypeConversationCreated`, `TypePromoteConversation`, `TypeConversationUpdated` |
| Backfill | `TypeBackfillSince`, `TypeMessageChunk`, `TypeBackfillDone` |
| Push | `TypeRegisterPushToken` |

**v2 control-envelope types** (#454; spec `docs/protocol-mobile.md` § Re-key):

| Group | Constants |
|-------|-----------|
| Re-key | `TypeRekeyRequest` |

**v2 interactive application-event types** (#607, extended #638; spec `docs/protocol-mobile.md` § Interactive events):

| Group | Constants |
|-------|-----------|
| Interactive | `TypeTurnState`, `TypeAssistantDelta`, `TypeToolUse`, `TypeToolResult`, `TypeTurnEnd` (#607), `TypeStall` (#638) |

These six live in their **own** const block (not merged into the `TypeRekeyRequest` block) so the doc comment can distinguish control envelopes from application events — but both are "v2-only" for the partition's purpose. `TypeStall` (#638) is the wire form of an internal-only `turnevent.Stall` signal; on the wire it is just another v2 capability-gated event, so it lives in this block with its ACP-shaped siblings (the internal-vs-ACP distinction is an adapter concern, invisible to the phone). `CapabilityInteractive = "interactive"` (the wire-vocabulary constant a phone advertises to opt into this stream) lives in `handshake.go` next to the `Capabilities` field, not here.

**v2 screen-snapshot types** (#617; spec `docs/protocol-mobile.md` § Screen snapshot):

| Group | Constants |
|-------|-----------|
| Screen snapshot | `TypeRequestSnapshot`, `TypeScreenSnapshot` |

These two live in their **own** cohesive const block, grouping the request/response pair so a reader greps "snapshot" and finds both adjacent with their shared rationale. The pair straddles both v2-only flavours — `TypeRequestSnapshot` is an inbound control envelope intercepted before `dispatch.Route` (like `TypeRekeyRequest`), `TypeScreenSnapshot` is an outbound binary → phone event — but `TestTypeConstants_V1V2Partition` is grouping-independent, so which block a constant lives in is purely a readability choice. Both stay out of `v1TypeSet`. The interception, render (via tui-driver), and push are the consumer ticket's job (`security-sensitive`), not this package's.

**v2 reconnect-resync marker** (#647; spec `docs/protocol-mobile.md` § Interactive events / Reconnect replay & resync):

| Group | Constants |
|-------|-----------|
| Resync | `TypeResync` |

`TypeResync = "resync"` is an outbound binary → phone control marker the daemon emits when a reconnecting phone's advertised `last_event_id` aged out of the bounded event ring (it must full-reload). Its **own** const block; like `TypeRekeyRequest` it has **no named payload struct** — `internal/relay` marshals an inline `struct{ ConversationID string }` at emit time. Stays out of `v1TypeSet` (an old phone must never receive it; `{"resync-rejected", TypeResync, false, ErrUnknownType}` in `compat_test.go` pins that v1 `IsV1Compatible` rejects it).

**v2 session-boundary marker** (#656; spec `docs/protocol-mobile.md` § Interactive events / session_transition):

| Group | Constants |
|-------|-----------|
| Session boundary | `TypeSessionTransition` |

`TypeSessionTransition = "session_transition"` is an outbound binary → phone marker the daemon emits when its session rotates (a `/clear`, an idle eviction, or a workspace change), so a phone renders a `ThreadItem.SessionBoundary` marker (`pyrycode-mobile#336`). Its **own** const block; **unlike** `TypeResync` it carries a real multi-field named payload (`SessionTransitionPayload` in `messaging.go` — see [Session-transition payload](#session-transition-payload-656)) rather than an inline struct. A **session boundary is distinct from the six turn-stream events** and carries **no** `event_id`. Stays out of `v1TypeSet` (an old phone must never receive it; `{"session_transition-rejected", TypeSessionTransition, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection). The producer is sibling #657 (`security-sensitive`); this slice is wire vocabulary only.

**v2 modal vocabulary** (#701; spec `docs/protocol-mobile.md` § Modal):

| Group | Constants |
|-------|-----------|
| Modal | `TypeModalShown`, `TypeModalAnswer`, `TypeModalCancel`, `TypeModalDismissed` |

The four modal types share **one** const block with **one** rationale comment — the **mixed inbound+outbound** cluster precedent set by the `request_snapshot`/`screen_snapshot` pair. `TypeModalShown` / `TypeModalDismissed` are outbound binary → phone events; `TypeModalAnswer` / `TypeModalCancel` are inbound phone → binary **control** envelopes intercepted at `internal/relay/v2session.go`'s `dispatchAppFrame` **before** `dispatch.Route` (the `TypeRekeyRequest` / `TypeRequestSnapshot` precedent — **no `dispatch.Route` handler**). All four carry real named payload structs in `messaging.go` (`ModalShownPayload` / `ModalAnswerPayload` / `ModalCancelPayload` / `ModalDismissedPayload` — see [Modal v2 wire payloads](#modal-v2-wire-payloads-701)). All four stay out of `v1TypeSet` (four `{"modal_*-rejected", …, ErrUnknownType}` rows in `compat_test.go` pin the v1 rejection). The producers are siblings #703 (control loop) / #706 (two-heads ownership) / #702 (per-device answer gate); this slice is wire vocabulary only, `security-sensitive` for the **shape** review (no handler ships).

`TypeRekeyRequest` carries the doc-comment load-bearing instruction "MUST NOT be added to `v1TypeSet` in `internal/protocol/envelope.go`"; a companion doc-comment **above** `v1TypeSet` names `TypeRekeyRequest` as the canonical example of a v2-only type that must stay out; the interactive block carries the same MUST-NOT instruction. The advisory comments form the stochastic-rule rails; the deterministic rail is `TestTypeConstants_V1V2Partition` in `compat_test.go` (see drift detectors below).

## Drift detectors

The v1 type list appears three times: in the `Type*` constants block (`codes.go`), in the `v1TypeSet` map literal (`envelope.go`), and in two test slices (`compat_test.go`). The triple-copy is **deliberate** — explicit drift detectors fail loudly in CI when a new constant lands without the corresponding map entry:

- `TestIsV1Compatible` — runs every v1 `Type*` constant through `IsV1Compatible` and asserts `nil` (catches "added a v1 `Type*` const, forgot the map").
- `TestV1TypeSet_CoversAllExportedTypeConstants` — asserts every v1 application `Type*` constant is keyed in `v1TypeSet`.
- `TestTypeConstants_V1V2Partition` (#454, extended #607/#617/#638/#647/#656/#701) — every exported `Type*` constant must be in `v1TypeSet` **OR** in the test-local `v2OnlyTypes` allowlist; never both, never neither. The allowlist now holds fifteen entries (`TypeRekeyRequest` + the five interactive types + `TypeStall` + the two snapshot types + `TypeResync` + `TypeSessionTransition` + the four modal types), so the partition size assertion is `len(v1TypeSet) + len(v2OnlyTypes) == 16 + 15 == 31`. Forces a future contributor adding any v2-only type to amend the allowlist explicitly — adding a `Type*` constant without partitioning it fails the build. The `v2OnlyTypes` literal lives in the test rather than as an exported production symbol so production callers cannot accidentally import it for dispatch logic — v2 dispatch switches on individual constants, not on partition membership.
- `TestErrorCode_Constants_MatchSpec` — exact-string match for each `Code*` constant against the spec's dotted string. Catches the "fat-fingered `protocol.unkown_type`" regression at the lowest possible cost.

Reflection over `go/types` was considered and rejected — heavier than explicit assertions for a closed set. If the v1 type set ever grows past ~50 entries (no plausible path under the protocol's versioning policy), revisit.

## Concurrency

Pure-data package. No goroutines, no locks, no shared-mutable state. `IsV1Compatible` is a pure function: same input, same output, allocation-free on the rejection path (returns one of three pre-existing values: `nil`, `ErrUnsupported`, `ErrUnknownType`). `v1TypeSet` is initialised at package init and never mutated.

## What's deliberately NOT in the package

- `Envelope.Validate()` method or `NewEnvelope(...)` constructor — `Envelope` has no construction-time invariants (every field independently settable, optional fields zero-valuable). Struct-literal-with-named-fields is the canonical shape.
- `AllV1Types []string` exported slice — no consumer needs it; YAGNI.
- `go:generate`-driven membership check — overkill for a 16-entry closed set.
- A `[]string` slice + linear scan for membership — duplicates the constant names twice (slice + constants); the map literal duplicates them once at the same indentation as the constants block, making drift visible at code review.
- Per-type payload structs beyond the now-complete #256 catalog — `RegisterPushTokenPayload` (#275), the five messaging + backfill payloads (#272), the conversations-read pair plus row type (#273), the four conversations-write payloads (#274), and the handshake/control payloads (#271). All slices are wired; no more #256 sub-tickets pending.
- **The interactive bridge, push, and capability trust decision (#607's consumer surface)** — mapping `turnevent` events → the five interactive payloads, the actual push/fan-out, and the daemon intersecting the phone's advertised capabilities with its own supported set all live in #608, never in this leaf package. `interactive.go` is wire vocabulary only.
- **The screen-snapshot intercept, render, and push (#617's consumer surface)** — intercepting `request_snapshot` at the v2 dispatch boundary (before `dispatch.Route`), rendering the current screen to text via tui-driver, and pushing `screen_snapshot` back all live in the consumer (the screen-snapshot handler child, which carries `security-sensitive`), never in this leaf package. `snapshot.go` is wire vocabulary only; the trust boundary — accepting a remote inbound frame and returning rendered screen content — is the consumer's, not this declaration's.
- **The modal control-loop runtime (#701's consumer surface)** — the four modal wire types landed in #701 as wire vocabulary (`ModalShownPayload` / `ModalAnswerPayload` / `ModalCancelPayload` / `ModalDismissedPayload` above). The runtime that mints `modal_id` nonces, emits `modal_shown`, intercepts `modal_answer` / `modal_cancel` at `dispatchAppFrame` before `dispatch.Route` (→ tui-driver keystroke), dedups by `answer_token`, and runs deny-on-timeout is #703 (with #706 two-heads ownership, #702 the per-device answer gate) — all `security-sensitive`, never in this leaf package. `messaging.go`'s modal structs are wire vocabulary only.
- **Other v2 event/control types** — `queue_state` and the remaining phone → binary control verbs (`interrupt`, …) are deliberately out of scope here; they belong to other #596 children and Phase 3 (#597). (`request_snapshot` / `screen_snapshot` landed in #617 as wire vocabulary; their consumer is the separate `security-sensitive` ticket above. The `stall` event landed in #638 as wire vocabulary — `StallPayload` above; its bridge consumer, mapping tui-driver's `stall_detected` → `turnevent.Stall` → `stall` and gating the fan-out, is #624-B, which carries `security-sensitive`.)
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
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § Decision 2 (v2-additive + capability negotiation) and § Wire-protocol extension; the decision the #607 interactive payloads + `capabilities` field implement. § Safe degradation pins the parser-independent screen-snapshot floor the #617 `request_snapshot` / `screen_snapshot` pair backs.
- [turnevent-package.md](turnevent-package.md) — the neutral internal turn-event model (#606) the interactive payloads are the wire representation of; `stop_reason` carries its `TurnEndReason` strings verbatim
- [codebase/607.md](../codebase/607.md) — the #607 implementation note (interactive payloads + capabilities negotiation)
- [codebase/617.md](../codebase/617.md) — the #617 implementation note (screen-snapshot wire types + v2 partition)
- [codebase/638.md](../codebase/638.md) — the #638 implementation note (the `stall` wire type + its internal-only `turnevent.Stall` peer; the sixth member of the v2 interactive partition)
- [codebase/649.md](../codebase/649.md) — the #649 implementation note (the additive `Envelope.EventID *uint64` field surfacing the eventring durable id on the interactive stream; producer half of mid-turn reconnect)
- [codebase/647.md](../codebase/647.md) — the #647 implementation note (`HelloClientPayload.LastEventID` + `TypeResync`; the inbound reconnect-replay consumer — `security-sensitive`, carries an unresolved code-review MUST FIX, not yet merged)
- [codebase/656.md](../codebase/656.md) — the #656 implementation note (the `session_transition` wire type; the vocab→producer split this slice and #701 both mirror)
- [codebase/701.md](../codebase/701.md) — the #701 implementation note (the four modal wire types + `ModalOption`; the `modal_id` nonce / `answer_token` idempotency contract; `security-sensitive` for the wire-shape security pass, producers #703/#706/#702)
- Future consumers: `internal/dispatch` (#248), `internal/relay-client`, the interactive event-stream bridge + capability enforcement (#608), the screen-snapshot handler (intercept `request_snapshot` + render + push `screen_snapshot`; `security-sensitive`)
