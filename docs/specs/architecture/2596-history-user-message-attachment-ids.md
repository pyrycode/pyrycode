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

## Revisions

### 2026-09-24 — security review added (verifier finding on PR #2597)

The verifier failed the first pass because this `security-sensitive` plan had no `## Security review` section. The section below is that pass, run against the committed design and the diff that implements it. It raises nothing that changes the design, so the code is unchanged by this revision.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The ids are client-authored, and they cross into trusted state at one point: `resolveAttachments` in `internal/relay/handlers/send_message.go`. That function enforces the count bound (`protocol.MaxAttachmentIDsPerMessage`) before resolving. It then deduplicates, and it keeps only ids the resolver accepted. The resolver checks the canonical shape (`conversations.ValidID`, lowercase UUIDv4, `[0-9a-f-]` only) and confines each id to the message's own conversation, which comes from session context rather than the wire. Only the `resolved` list it returns reaches `EnqueueAttached`. `p.AttachmentIDs` never reaches the queue raw, and a message with any refused id is rejected before enqueue. Every stored id therefore names an attachment that existed in that conversation, and it cannot carry injection text into claude, the log or a peer's UI.
- [Host-path non-exposure] No findings. The stored entry is built in `newOperatorMessageHistory` from `msgqueue.QueuedMessage` alone. `QueuedMessage` still has no field for `queued.delivery`, the only place the on-host path lives, and `notifyDelivered` projects `attachmentIDs`, never `delivery`. The ids are a separate argument to `EnqueueAttached`, taken from `resolveAttachments`, and are never derived from the composed prompt. The seam test's raw-bytes check asserts that neither the host path nor the delivery prompt header appears in the stored entry.
- [Other clients reading the ids] No findings: returning them is the point of the change. `request_history` serves a conversation only to a session admitted to it by the relay's membership gate. Every such session belongs to a device paired to the same operator, the trust domain that authored the ids. The ids are not a capability, per the stance in `SendMessagePayload`'s doc: fetching the bytes is confined to the requester's own conversation, as naming them is, so a peer learns an id it could already reach and nothing it could not. A peer that did not send the message sees only a file of its own conversation.
- [Tokens, secrets, credentials] Not applicable. Nothing in the design generates, stores or compares a secret. Per the protocol stance, attachment ids are explicitly not secrets.
- [File operations] No findings. No new path is built from the ids here; they are written as JSON strings inside the existing history log, through `history.Store.Append`, which keeps its own file modes and its segment-bound check. The only filesystem use of the ids is the existing `resolveAttachments` call, which this ticket does not change.
- [Subprocess execution] Not applicable. No command is run. The ids reach claude only through the pre-existing composed prompt, and only as paths the resolver produced.
- [Cryptographic primitives] Not applicable.
- [Network & I/O / resource bounds] No findings. At most 32 ids of 36 bytes each (about 1.2 KB) are added to one entry. They are the same bytes the inbound `send_message` frame already carried inside the envelope cap, and deduplication only shrinks them. `serveHistoryPage` budgets each page in bytes against the application-envelope cap, so a larger entry yields fewer entries per page, never an oversized frame. `Store.Append` still refuses a line over the segment bound.
- [Error messages, logs] No findings. The queue logs no id: `EnqueueAttached` logs nothing new, and the queue's rule of never logging a record's strings covers the new field. On refusal the handler logs `attachment_count` only, as before. The producer's marshal-failure branch logs neither the payload nor `err.Error()`.
- [Concurrency / aliasing] No findings. `EnqueueAttached` clones the ids with `slices.Clone` before taking `q.mu`, so the record never aliases the handler's slice. A test mutates the caller's slice after enqueue and asserts that the delivered projection is unchanged. `notifyDelivered` reads the ids from the drain's value copy, off-lock, as it reads `text`. `Snapshot` and `SnapshotAll` do not project the field, so their promise that a snapshot cannot mutate engine state still holds.
- [Threat model alignment] § Security model threat 1 (prompt injection) is covered by the shape check above: a stored id draws from `[0-9a-f-]` only. The threat of exposing host paths to the phone is covered by the non-exposure finding. No threat is deferred.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24
