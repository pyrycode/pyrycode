# Messaging payloads (#272)

Bodies of the two conversation-flow envelopes (`docs/protocol-mobile.md` § Message types → `send_message` / `message`). `send_message` is phone → binary; `message` is binary → phone. (This slice originally also shipped the v1 bulk-history backfill payloads — `BackfillSincePayload` / `MessageChunkPayload` / `BackfillDonePayload`, for `backfill_since` / `message_chunk` / `backfill_done` — but that flow had zero emitters and zero handlers on either v2 dispatch surface; removed as dead code in #967. The v2 reconnect path, `request_snapshot` + `hello.last_event_id` bounded-ring replay (ADR 025 / #646/#647), replaced it entirely. See [codebase/967.md](../codebase/967.md).)

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
```

- **`MessagePayload.Role` stays `string`, not a named `Role` enum.** Spec defines a closed set (`"user"`, `"assistant"`, `"system"`) but the binary already treats role-strings as `string`-typed elsewhere; a typed `Role` would force a converter at every internal call site for no observable wire-format gain, and the closed-set guarantee belongs at the dispatcher. Matches `RegisterPushTokenPayload.Platform`'s rationale.
- **All required fields are non-pointer, no `omitempty`.** Encode-side absence surfaces as zero-value `""` on the wire; the dispatcher rejects malformed frames via shape validation. Empty `text` is wire-legitimate (semantic validation lives at the dispatcher).
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to `RegisterPushTokenPayload`. Required-field validation, role-set enforcement, ID monotonicity, clock-skew bounds — all dispatcher concerns.

Golden round-trip tests in `messaging_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against the matching fixture. The package's canonical `*string`-WITHOUT-`omitempty` "literal `null` on the wire" idiom is now `SessionTransitionPayload.WorkspaceCwd` (#656, below) — the original example, `BackfillSincePayload.ConversationID`, was removed with the rest of the backfill flow in #967.
