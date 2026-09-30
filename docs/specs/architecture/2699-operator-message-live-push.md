# #2699 — Push each delivered user message live to every interactive client

## Files read

- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory` — the one place the operator's `message` payload is built, from `msgqueue.QueuedMessage` (never the delivery bytes). The push must reuse its bytes and timestamp.
- `cmd/pyry/queue_state_v2.go` → `queueStateNotify`, `queueStateEmitterV2.Run` / `broadcast`, `startQueueStateStreamV2` — the seam→channel→Run→broadcaster shape this producer mirrors.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.emit` — ring `Append` once per logical event, one `ts`, shared `&eventID` across per-conn envelopes; `interactiveBroadcaster`.
- `cmd/pyry/main.go` → `runSupervisor` — the pre-`msgqueue.New` channel block (`queueChanges`, `giveUps`), `OnDelivered: deliveredFuncs(...)`, `qse` construction, `relayWiring` literal.
- `cmd/pyry/channel_carry.go` → `deliveredFuncs` — history must stay first in the fan-out.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2` — the ring is born in the `w.streamSink != nil` branch (`newInteractiveTurnEmitterV2`, `SetReplaySource`); producer starts sit below it; the returned cleanup joins them.
- `internal/eventring/ring.go` → `Ring.Append` — own mutex, returns the ring-wide id; safe from any goroutine.
- `internal/relay/v2session_replay.go` → `replayMissed` — replays `ring.After(currentConv, afterID)` with no type filter, so a `message` event in the ring replays like any other.
- `internal/msgqueue/delivered_test.go` → `TestQueue_OnDelivered_FiresOnceAcrossRetries`, `TestQueue_OnDelivered_SilentOnGiveUp`, `TestQueue_OnDelivered_SilentWhenHeadRemovedBeforeCommit` — the seam already pins AC 2's once/never semantics.
- `internal/protocol/codes.go` → the comment block above `TypeAttachmentOffered` — carries the sentence "not pushed to an attached client at all", now false.
- `docs/knowledge/features/` overviews for cmd/pyry relay producers — content-free logging discipline, drop-on-full hand-off.

In-flight overlap: `origin/feature/449` touches `internal/protocol/codes.go` (stale since May, different block). Not a dependency; the edit here is one comment sentence.

## Context

The operator's own message reaches the durable log on confirmed delivery (#2115) but is never pushed; another client sees it only as a `queue_state` row and afterwards via `request_history`. Owner decision: the daemon pushes it live to every interactive conn. Both clients already decode a live `message` frame, so no new type.

## Design

One new file `cmd/pyry/operator_message_v2.go`:

- `type operatorMessage struct { convID string; payload json.RawMessage; ts time.Time }` — one confirmed user turn, marshalled once.
- `const operatorMessageQueueSize = 16` — same bound and reasoning as `queueStateQueueSize` (human-paced).
- `operatorMessageNotify(ch chan<- operatorMessage, logger) func(operatorMessage)` — non-blocking send; on full, content-free `Warn` (`event=operator_message.queue_full`, `conversation_id` only) and drop. Never blocks the drain goroutine.
- `type operatorMessageEmitterV2 struct { in <-chan operatorMessage; logger; nextID uint64 }` + `newOperatorMessageEmitterV2(in, logger)`.
- `(*operatorMessageEmitterV2).Run(ctx, bcast interactiveBroadcaster, ring *eventring.Ring)` — drains `in` until ctx done; `ring` and `bcast` are goroutine-local parameters supplied at start (the ring does not exist at queue construction).
- `broadcast(ctx, bcast, ring, m)`: if `ring != nil`, `id := ring.Append(m.convID, protocol.TypeMessage, m.payload, m.ts)` BEFORE the fan-out (so it lands with no conn open); then for each `Interactive` conn push `Envelope{ID: ++nextID, Type: TypeMessage, TS: m.ts, Payload: m.payload, EventID: &id or nil}`. Push errors logged at Debug with conn/env ids only; ctx-cancel returns.
- `startOperatorMessageStreamV2(ctx, e, bcast, ring) func()` — mirrors `startQueueStateStreamV2`; cleanup joins Run, idempotent, does not close `in`.

`newOperatorMessageHistory(store, push func(operatorMessage), logger)` — gains `push`. After the history append it calls `push(operatorMessage{convID, payload, ts})` with the SAME `payload` slice and `ts` variable. nil `push` = no push. A marshal failure returns before either.

Wiring:
- `runSupervisor`: `operatorMessages := make(chan operatorMessage, operatorMessageQueueSize)` in the pre-`msgqueue.New` block; history producer built with `operatorMessageNotify(operatorMessages, logger)`; still first in `deliveredFuncs`. After the queue: `ome := newOperatorMessageEmitterV2(operatorMessages, logger)`, passed as `relayWiring.operatorMessages`.
- `startRelayV2`: `var replayRing *eventring.Ring` declared before the stream-sink branch and set to `emitter.ring` inside it; after the queue_state start, `startOperatorMessageStreamV2(ctx, w.operatorMessages, mgr, replayRing)`; cleanup called with the other producer cleanups.
- `codes.go`: correct the "not pushed" sentence — `message` is now pushed live, role `user` only, after confirmed delivery; the argument that widening it would not announce a file still holds (it fires on the operator's delivery, not when claude produces a file).

Data flow: `drain goroutine → OnDelivered → history append → operatorMessageNotify (chan) → Run goroutine → ring.Append → Push per interactive conn`.

## Concurrency model

- Producer side runs on msgqueue drain goroutines; it only does a non-blocking channel send.
- One Run goroutine owns `nextID` and all `bcast` calls. `eventring.Ring` has its own lock. The interactive turn emitter's `emit` is never called from here.
- Shutdown: ctx cancel ends Run; cleanup waits on its done channel. A late send after teardown drops into the unread buffer.

## Error handling

- Full channel: drop + content-free Warn; delivery is never blocked.
- No relay leg (no Run goroutine): the channel fills and later sends warn-and-drop — identical to `queueChanges` today.
- No ring (wiring with no stream sink): push without `event_id`.
- Push error: Debug, continue with other conns.
- Text, message ids and attachment ids never reach the logger.

## Testing strategy

New `cmd/pyry/operator_message_v2_test.go`:
- broadcast with one interactive + one non-interactive conn and a ring: exactly one push, to the interactive conn; type `message`; payload bytes and TS equal the input; `EventID` non-nil and equals the id the ring holds for a `message` event.
- broadcast with no conns: ring still holds the event.
- broadcast with nil ring: push carries no `EventID`.
- replay ordering: prior ring event id P, broadcast message, then a later ring append; `ring.After(conv, P)` returns the message first, then the later event.
- `operatorMessageNotify` on a full channel: returns without blocking, Warn logged, log carries no text.
- `startOperatorMessageStreamV2` end to end via channel → push; cleanup joins on cancel.

In `cmd/pyry/operator_message_history_test.go`:
- through a real `msgqueue.Queue`: #2038 shape (queued text + attachment ids vs delivery naming a host path), deliver fails once then succeeds → exactly one `operatorMessage` on the channel; its payload bytes equal the log entry's payload; its ts equals the entry TS; payload lacks the host path and carries `attachment_ids`.
- existing tests updated for the new `push` argument (nil).

Removed/given-up never firing `OnDelivered` is pinned by the msgqueue tests named above; the push rides only that seam.

## Open questions

- None blocking. Whether the non-relay daemon should skip the channel entirely to avoid the warn stream: mirror queue_state (warn-and-drop) — not observed as a problem there.

## Documentation handoff (pending — documentation stage)

`docs/protocol-mobile.md` § Application message types, the `message` row ("Not minted on the v2 interactive path"): rewrite to say `message` is pushed to interactive clients after confirmed delivery, role `user` only, carries a ring `event_id`, and clients de-duplicate their own echo by `message_id`. Add a dated changelog entry for #2699.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The authorization boundary is the Noise_IK handshake against paired device keys: a conn reaches `ActiveConns` only once it has a push queue, which exists only from `V2StateOpen`, and `forwardEnvelope` re-checks the state before sealing, so a buffered push to a conn that de-authed is dropped, never delivered. The `Interactive` flag checked in `operatorMessageEmitterV2.broadcast` is a rendering capability, not an authorization: every authenticated conn can already read the same entry through `request_history` (`v2session_history_request.go` applies no interactive gate). The fan-out to all interactive conns, the sender's included, therefore adds no new reader of the operator's text or `attachment_ids`. The conversation id is taken from the daemon's queued message, never from the receiving conn.
- [Trust boundaries — replay] No findings, one note. The ring now also holds the `message` event. `replayMissed` scopes replay to the daemon-resolved current conversation (never one the client names) and range-bounds the untrusted `last_event_id`; it does not check the `Interactive` flag, so a non-interactive conn that sends `last_event_id` would replay the message as it already replays every other ring event. That is pre-existing behaviour for the whole ring and grants nothing `request_history` does not; unchanged here.
- [Delivery payload vs queued text] No findings. `newOperatorMessageHistory` builds the payload once from `msgqueue.QueuedMessage` — `Text` and `AttachmentIDs` — and hands the same bytes to both the log and the push; the delivery bytes, which in the #2038 attachment shape name an on-host path, are never read. Evidence: `TestOperatorMessageHistory_PushesLogEntryOnceAfterRetry` delivers a queued message whose delivery payload names a host path, fails once, succeeds, and asserts exactly one push whose payload bytes equal the log entry's, carry `attachment_ids`, and do not contain the host path.
- [Tokens, secrets, credentials] No findings. The producer handles no keys or tokens; sealing happens in the existing session layer under each conn's own transport keys.
- [File operations] No findings. No file is created or read; the durable append is the existing #2115 path, unchanged.
- [Subprocess execution] Not applicable — no process is spawned.
- [Cryptographic primitives] Not applicable — no primitive is used or added; `event_id` is the ring's sequence number, not a secret.
- [Network & I/O] No findings. Payload size is bounded upstream by the inbound frame cap on `send_message`; the push rides the existing per-conn push queue with its byte ceiling (`pushQueueByteCeiling`), which tears down a conn that stops draining rather than growing without bound.
- [Resource bounds] No findings. The hand-off channel holds at most `operatorMessageQueueSize` (16) entries; `operatorMessageNotify` sends non-blocking and drops on full, so delivery never waits on the relay leg and a daemon with no relay leg drops rather than accumulates. The ring retains at most `eventring.MaxEventsPerConversation` (1024) events per conversation, evicting the oldest, the same bound every other ring event already lives under.
- [Error messages, logs] No findings. The only new log lines are the drop-on-full Warn in `operatorMessageNotify` and the push-error Debug in `broadcast`; both carry only `event`, `conversation_id` and, for the Debug, `conn_id` and `env_id`. The `err` field there is one of `V2SessionManager.Push`'s two returns, `ctx.Err()` or the sentinel `ErrConnNotFound`, neither of which carries envelope bytes. `TestOperatorMessageNotify_DropOnFullDoesNotBlock` asserts the Warn carries no message text. Text, message ids and attachment ids never reach the logger.
- [Concurrency] No findings. One Run goroutine owns `nextID` and every broadcaster call; `eventring.Ring` has its own mutex; `interactiveTurnEmitterV2.emit` is never called from the queue goroutine. Run exits on ctx cancel and the cleanup returned by `startOperatorMessageStreamV2` joins it; a send racing teardown lands in the unread buffer.
- [Threat model alignment] No findings. `docs/protocol-mobile.md` § Security model treats every paired device as an equal holder of the conversation; this ticket widens delivery timing, not the set of devices that can read.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-30

## Revisions

- **2026-09-30 — security review added after the code (verifier finding, PR #2701).** The ticket carried `security-sensitive` from creation, but the plan was committed without the `## Security review` section the label requires. The section above is the pass run against this plan and the landed implementation. Its verdict is PASS with no MUST FIX or SHOULD FIX findings, so the design and code are unchanged.
