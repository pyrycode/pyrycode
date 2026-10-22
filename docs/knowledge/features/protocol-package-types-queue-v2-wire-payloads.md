# Queue v2 wire payloads (#720)

Queue controls remain inbound phone → daemon envelopes, intercepted by
`dispatchAppFrame` before `dispatch.Route`. `dequeue_message` removes a waiting
entry; `send_queued_now` writes it into a running Claude turn. Queue commands
retain paired authentication, the `interactive` capability and optional
current-run acceptance binding. See [Queue (v2)](../../protocol-mobile.md#queue-v2).

Outbound `queue_state`, `TypeQueueState`, `QueueStatePayload`, `QueuedItem`,
the snapshot fixture and positive snapshot tests are retired for every app
(#3727). Engine `Snapshot`/`SnapshotAll` remain source APIs, not wire DTOs.
Apps negotiate `thread`, explicitly catch up each open conversation and apply
live item updates. A queued active acceptance exposes `content.queued_msg_id`
and `item.id`; terminal items retain their handle only as historical data.
No handshake snapshot or connect-time thread inventory replaces the old frame.

```go
type DequeueMessagePayload struct { // phone → binary, inbound control (intercepted pre-dispatch.Route, no handler)
    ConversationID string `json:"conversation_id"` // untrusted phone input; #723 resolves to an authorized conversation
    QueuedMsgID    uint64 `json:"queued_msg_id"`   // the id to remove (msgqueue.Remove(convID, id))
    AcceptedItemID json.RawMessage `json:"accepted_item_id,omitempty"` // optional durable acceptance guard
}

type SendQueuedNowPayload struct { // phone → binary; writes into the running turn
    ConversationID string `json:"conversation_id"`
    QueuedMsgID    uint64 `json:"queued_msg_id"`
    AcceptedItemID json.RawMessage `json:"accepted_item_id,omitempty"`
}
```

- **Required fields remain always present; only `accepted_item_id` is omitted
  when unset.** Both command DTOs use `json.RawMessage` so omission and explicit
  `null` remain distinct: nil permits the legacy operation, while present-invalid
  input must never fall back to it. A nullable numeric pointer would collapse
  those cases. Relay validation accepts only JSON integers in
  `1..9007199254740991`, sourced from accepted thread `item.id`, and rejects every
  payload decode error before acting. `TestQueueActionItemBindingWire` pins the
  literal wire key and omission/null preservation for both DTOs; see
  [protocol boundary tests](development-verification-protocol-boundaries.md#protocol-boundaries).
- **`queued_msg_id` is a positive `uint64` per-conversation, per-daemon-run
  handle, not a nonce.** Preserve it as an exact JSON integer. It matches the
  queue's `QueuedMessage.ID`; never infer it from text, order, `message_id` or
  history ID. Include accepted thread `item.id` as `accepted_item_id` to guard
  against a stale handle targeting another entry after restart.
- **Correlation is separate from command identity.** A client `message_id` may
  be empty or repeated across devices. Both commands select conversation plus
  queue handle, optionally guarded by the daemon-recorded acceptance item ID;
  `message_id` provides no authorization or deduplication. See
  [safe queue projections](msgqueue-package-api.md#exported-surface).
- **Payload types do not authorize operations.** The relay rejects payload
  decode errors and malformed bindings before queue mutation. Omission retains
  the legacy unbound operation; present-invalid data never falls back. A matching
  binding preserves the existing committing-head protection and lifecycle
  ordering. Neither command has a dedicated reply; observe accepted-item updates.

`TestQueueActionItemBindingWire` pins omission versus explicit `null` for both
commands. `TestDequeueMessagePayload_RoundTrip` and
`TestDequeueMessagePayload_Malformed` retain command serialization coverage;
`TestQueueSnapshotsRetired` prevents the retired vocabulary and either snapshot
publication arm from returning. The original snapshot design is historical in
[codebase/720.md](../codebase/720.md).
