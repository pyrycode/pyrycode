# Queue v2 wire payloads (#720)

The wire vocabulary for the **queued-message backlog** over the encrypted mobile
wire (`docs/protocol-mobile.md` § Queue; epic #597 Phase 3, [ADR 025]). A phone
that types while `claude` is busy has its turn buffered in `internal/msgqueue`
(#704, extended #719); the phone can **view** the backlog (`queue_state`, daemon →
phone, the wire form of `msgqueue.Snapshot(convID)`) and **cancel** an entry
(`dequeue_message`, phone → daemon, driving `msgqueue.Remove(convID, id)`). Three
new exported types in `messaging.go` (a queue snapshot is daemon **state**, not a
turn-stream event, so `messaging.go` not `interactive.go`; it already houses the
`time.Time` + nested-array precedents), mapping to two `Type*` constants.
`queue_state` is an outbound binary → phone event; `dequeue_message` is an
**inbound phone → binary control** envelope the v2 session manager intercepts at
`v2session.go`'s `dispatchAppFrame` **before** `dispatch.Route` (the
`ModalAnswerPayload` / `RequestSnapshotPayload` precedent — **no `dispatch.Route`
handler**). **Wire shape only** — the emit-on-change/fan-out producer is #722, the
intercept/resolve-convID/remove handler is #723.

```go
type QueuedItem struct { // one element of QueueStatePayload.Queued
    QueuedMsgID uint64    `json:"queued_msg_id"` // plain per-conversation counter (≥1), NOT a nonce
    Text        string    `json:"text"`          // untrusted, phone-originated transit content
    TS          time.Time `json:"ts"`            // enqueue time, RFC3339Nano
}

type QueueStatePayload struct { // binary → phone; wire form of msgqueue.Snapshot(convID)
    ConversationID string       `json:"conversation_id"` // daemon's own resolved id (#722), never attacker-derived
    Queued         []QueuedItem `json:"queued"`          // ordered FIFO/enqueue order (the options []ModalOption precedent)
}

type DequeueMessagePayload struct { // phone → binary, inbound control (intercepted pre-dispatch.Route, no handler)
    ConversationID string `json:"conversation_id"` // untrusted phone input; #723 resolves to an authorized conversation
    QueuedMsgID    uint64 `json:"queued_msg_id"`   // the id to remove (msgqueue.Remove(convID, id))
}
```

- **No `omitempty` on any field** — the same deliberate inverse as the #607
  interactive / #617 snapshot / #701 modal payloads. Every field is always present
  so the fixtures pin the full shape. `package protocol` already imports `time`
  (for `Envelope.TS`) — **no new import**.
- **`queued_msg_id` is a plain `uint64` per-conversation counter (≥ 1), NOT a
  nonce.** It matches `msgqueue.QueuedMessage.ID` exactly so the producer maps
  `QueuedMessage.ID → QueuedMsgID` with no translation. The mobile client (#429)
  must decode it as a JSON **integer**, not a string. This is the discriminator
  from #701's `modal_id` (an unguessable nonce) — there is no secrecy property, so
  this slice is **unlabelled** (see below).
- **`queued` ordering + empty-backlog `[]` vs `null`.** Array order is canonical
  FIFO/enqueue order (the `Options []ModalOption` precedent). `[]QueuedItem(nil)`
  marshals to `"queued":null`, a non-nil empty slice to `[]`; the leaf type cannot
  force non-nil, so the contract is only "`queued` always present". The docs
  **recommend** the producer (#722) emit `[]` (not `null`) for an empty backlog so
  the mobile decoder keeps `queued` a plain array — not enforced here; both
  round-trip.
- **`TS` is `time.Time` (RFC3339Nano on the wire)** per the envelope timestamp
  rule; marshal strips the monotonic clock so tests compare with `.Equal`, never
  `==` / `reflect.DeepEqual`. Same discipline as `Envelope.TS` /
  `SessionTransitionPayload.OccurredAt`.
- **Unlabelled (`security-sensitive`: no) — mirrors #656, not #701.** Dequeuing is
  **ungated** for any paired phone (ADR 025 § Security model); only answering a
  permission-class modal is gated. No nonce, no per-device gate. `Text` and the
  inbound `ConversationID` are **untrusted, phone-originated** content (never log
  `Text`; resolve `ConversationID` to an authorized conversation before acting) —
  but the slice stores/inspects neither; that discipline lives in the
  `security-sensitive` siblings #722/#723/#721.
- **No `turnevent`/`turnbridge` neutral hop.** Queue backlog is daemon state, not a
  turn-stream event, so the producer builds the `protocol.*Payload` directly from
  engine state and the inbound frame is decoded at `dispatchAppFrame` — neither
  direction routes through `turnevent`/`turnbridge` (that path is reserved for
  tui-driver turn-stream events). See [codebase/720.md](../codebase/720.md) for the
  mechanism rationale and the two-opposite-direction-sums note.

`TestQueueStatePayload_RoundTrip` (`messaging_test.go`) asserts `len(Queued)==2` +
positional `QueuedMsgID`/`Text` + per-item `TS` via `.Equal`, then byte-equal
round-trips via `roundTripEnvelope`; `TestDequeueMessagePayload_RoundTrip` covers
the inbound control; table-driven `TestDequeueMessagePayload_Malformed` pins the
AC's "rejected cleanly (error, no panic)". Two fixtures (`queue_state.json` with
N=2 items, `dequeue_message.json`) authored in **struct-field order**. See
[codebase/720.md](../codebase/720.md).
