# Conversations-read payloads (#273)

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
- **`ConversationSummary.Name` is `*string` WITHOUT `omitempty` — same discipline as `SessionTransitionPayload.WorkspaceCwd`.** Spec example shows literal `"name": null` on one of the two rows (an unnamed scratch conversation). `*string` distinguishes "null on wire" (nil pointer) from "absent" and from "empty string"; dropping `omitempty` keeps the `null` literal on re-marshal (byte-identical to the spec fixture). The AC body said "`*T` + `omitempty`"; honouring that literally would silently break the byte-equivalent round-trip the same AC requires — spec wire shape wins. A multi-line WHY comment on the field is mandatory (the only field-level comment in `conversations_read.go` under the "default to no comments" rule); `TestConversationsPayload_RoundTrip`'s byte-equal check is the regression detector. The fixture carries both branches (one row `name=<string>`, one row `name=null`) so the round-trip exercises both.
- **`ConversationsPayload.Conversations` order is preserved verbatim from the wire — this type does not reorder.** Doc comment notes that the binary is the source of truth for ordering (e.g. most-recently-used first); a `Sort` / `SortMRU` helper or any ordering predicate is explicitly out of scope.
- **`LastMessageTS` / `LastUsedAt` are `time.Time` (RFC3339Nano-on-the-wire envelope rule).** Spec example uses `"2026-05-08T10:31:02Z"` (no fractional seconds); `time.Time.MarshalJSON` emits RFC3339Nano which omits the fractional component when none is present, so the round-trip is byte-identical with no custom marshaller. Tests use `time.Time.Equal`, never `==`. Same discipline as `Envelope.TS`.
- **Other required fields are non-pointer, no `omitempty`.** `ID` / `Cwd` are required `string`; `IsPromoted` is required `bool` (fixture covers both `true` and `false`). Validation that `IsPromoted == true` implies `Name != nil`, ID uniqueness, ordering invariants — all dispatcher / registry concerns.
- **`ConversationSummary` field declaration order matches the fixture's per-row key order** (`id, name, is_promoted, cwd, last_message_ts, last_used_at`); Go's `encoding/json` emits in declaration order, so this is what makes the byte-equal round-trip survive.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to `RegisterPushTokenPayload` and the messaging slice. The future dispatch handler reads `internal/conversations.Registry`, maps each `Conversation` row to a `ConversationSummary`, and sends a `conversations` envelope with `in_reply_to` set to the request id — registry-to-payload mapping is a downstream concern, not this package's.

Golden round-trip tests in `conversations_read_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against `testdata/list_conversations.json` / `testdata/conversations.json`. `TestConversationsPayload_RoundTrip` asserts both rows: row 0 has a non-nil `Name` pointer; row 1 has `Name == nil` (NOT `*c1.Name == ""` — would panic on nil deref AND be the wrong check). The `conversations.json` envelope rides with `in_reply_to: 3`, the first protocol fixture pinning `in_reply_to` alongside an array-carrying payload.
