# #2596 — a stored user message keeps its attachment_ids

## Files read

- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory` — the #2115 producer that writes the operator's turn; builds `protocol.MessagePayload` from `msgqueue.QueuedMessage` only.
- `cmd/pyry/operator_message_history_test.go` → `opQueue`, `onlyEntry`, `TestOperatorMessageHistory_AppendsUserMessageWithQueuedText` — the seam test the new assertions sit beside; its raw-bytes host-path leak check is the pattern to reuse.
- `internal/msgqueue/queue.go` → `QueuedMessage`, `queued`, `Enqueue`, `EnqueueDelivery`, `notifyDelivered`, `Snapshot`, `SnapshotAll` — where the ids must be stored and projected.
- `internal/msgqueue/delivered_test.go` → `deliveredRecorder` — the recorder a copy/projection test reuses.
- `internal/relay/handlers/send_message.go` → `Enqueuer`, `SendMessage`, `resolveAttachments` — the only production enqueue caller and the dedup that already runs over the ids.
- `internal/relay/handlers/send_message_test.go` → `fakeEnqueuer`, `enqueueCall`, `TestSendMessage_ComposesPromptFromAttachments`, `TestSendMessage_NoAttachments_DeliveredVerbatim` — the single fake that implements `Enqueuer`.
- `internal/protocol/messaging.go` → `MessagePayload`, `SendMessagePayload.AttachmentIDs` — the entry type and the name/shape to mirror.

## Context

`request_history` serves a user `message` entry without the attachments the `send_message` named, so a second client (or a phone that dropped its cache) never learns the file exists. The fix carries the resolved ids onto the stored entry. Found by pyrycode-mobile#1020.

## Design

**Wire.** `protocol.MessagePayload` gains `AttachmentIDs []string` tagged `json:"attachment_ids,omitempty"` — same key as `SendMessagePayload`'s. Empty or nil → key omitted, so every assistant `message` emission and every attachment-less user entry marshal byte-identically to today. Older decoders ignore the new key.

**Queue.** The internal `queued` record gets `attachmentIDs []string`; `QueuedMessage` gets `AttachmentIDs []string`. New method:

- `(*Queue).EnqueueAttached(convID, messageID, text, delivery string, attachmentIDs []string) uint64` — `EnqueueDelivery` with ids; copies the slice on entry (`slices.Clone`, which keeps nil as nil) so the record never aliases the caller's slice.
- `EnqueueDelivery` becomes a one-line wrapper passing nil, so its 14 test calls and `Enqueue` are untouched.
- `notifyDelivered` projects `AttachmentIDs` onto the `QueuedMessage` it hands `OnDelivered`. `Snapshot`/`SnapshotAll` do **not** project it: queue_state is out of scope, and leaving it off keeps their "value copy, cannot mutate engine state" promise without cloning per snapshot. The field doc says it is populated on the delivered projection only.

Ids are client-authored and already passed the resolver's canonical-shape check; like `text` and `messageID` they are never logged by the queue. Provenance, not shape, is the security distinction the `EnqueueDelivery` doc draws — the ids go back to the trust domain that authored them, and `delivery` (host path) still has no projected field.

**Handler.** `Enqueuer`'s method becomes `EnqueueAttached(conversationID, messageID, text, delivery string, attachmentIDs []string) uint64`. `resolveAttachments` returns the deduplicated ids beside the paths — `(ids, paths []string, ok bool)` — since it already walks them in first-occurrence order after the shape check; one caller. The handler passes those ids, never anything derived from `delivery`. `fakeEnqueuer` (the only implementer besides `*msgqueue.Queue`) renames its method and records the ids.

**Producer.** `newOperatorMessageHistory` sets `AttachmentIDs: msg.AttachmentIDs`. Still reads only `QueuedMessage`, so no path is reachable.

## Concurrency model

Unchanged. The ids are written under `q.mu` inside the existing append; `notifyDelivered` reads them from the value copy the drain already holds, off-lock.

## Error handling

No new failure modes. Marshal of `[]string` cannot fail; the existing defensive drop stays.

## Testing strategy

- `cmd/pyry/operator_message_history_test.go`: new test — `EnqueueAttached` with ids `[a, b]` (already deduped, as the handler hands them) and a host-path delivery → one entry, `role: "user"`, `attachment_ids == [a, b]`, raw bytes contain neither `opHostPath` nor the delivery prompt header. The existing attachment-less test gains an assertion that the raw entry has no `attachment_ids` key. (AC 1, AC 2.)
- `internal/msgqueue/delivered_test.go`: `EnqueueAttached` then mutate the caller's slice → the delivered `QueuedMessage.AttachmentIDs` still holds the original ids (copy-on-entry).
- `internal/relay/handlers/send_message_test.go`: `TestSendMessage_ComposesPromptFromAttachments` asserts the enqueued ids equal the first-occurrence dedup (`wantResolve`); `TestSendMessage_NoAttachments_DeliveredVerbatim` asserts none enqueued. (AC 1's dedup/order half.)

## Open questions

None.

## Documentation handoff (pending — documentation stage)

`docs/protocol-mobile.md` § Conversation history (v2): document the optional `attachment_ids` on a stored user `message` entry — omitted when the message named none, never carries a path — and add a changelog line.
