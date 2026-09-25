# #2644 — Frames about a Codex conversation reach only `multi_agent` conns

## Files read

- `internal/relay/v2session.go` → `forwardEnvelope`, `drainOnce`, `Push`, `forwardAppReply` — `forwardEnvelope` is the one seal-and-forward path every pushed frame reaches: the push-queue drain (`drainOnce`), the replay pump (`drainReplayOnce`), the resync marker (`emitResync`) and the inline `InReplyTo` replies. Handler replies take `forwardAppReply` instead and never reach it.
- `internal/relay/v2session_replay.go` → `drainReplayOnce`, `emitResync`, `snapshotReplyError` — a `forwardEnvelope` error abandons the replay tail and a nil return advances `replayThrough`, so a withheld frame must return nil.
- `internal/relay/v2session_seams.go` → `V2SessionConfig`, `KnownConversation`, `RetainedModelLists` — where the new seam sits; `RetainedModelLists` is the precedent for a registry-plus-pool read on the Run goroutine.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — sets `s.multiAgent` before `V2StateOpen`; re-key never rewrites it (#2643).
- `internal/relay/v2session_{modelreconcile,slashreconcile,rosterreconcile,questionreconcile,modal}.go`, `v2attachmentstream.go`, `v2bundlestream.go` — the relay-internal `Push` sites; all reach `forwardEnvelope` through `drainOnce`.
- `cmd/pyry/*_v2.go` emitters (`conversation_update_v2`, `interactive_turn_v2`, `session_transition_v2`, `session_error_v2`, `resetting_v2`, `queue_state_v2`, `attachment_offer_v2`, `channel_post_v2`, `workspace_update_v2`, `modal_resolve_v2`) — the ten `bcast.Push` fan-outs; the pushed types are listed under Design.
- `internal/protocol/*` → the pushed payload structs. Every one of them carries a top-level `conversation_id` except `ConversationUpdatedPayload` (`id`), `ModalDismissedPayload`, `QuestionDismissedPayload` and `WorkspaceUpdatedPayload`.
- `internal/relay/handlers/list_conversations.go` → `agentOf`, `SessionHarnessFunc` — the conversation-to-agent rule to reuse, not re-implement.
- `cmd/pyry/relay.go` → `relayWiring.sessionHarness`, `relayWiring.convReg`, the `V2SessionConfig` literal in `startRelayV2` — where the seam is built.
- `cmd/pyry/list_conversations_agent_test.go`, `cmd/pyry/dormant_settings_write_test.go` → `newDormantWritePool` — real pool with a dormant Codex entry, reused for the wiring test.
- `internal/relay/v2session_multiagent_test.go`, `v2session_replay_test.go` → `TestV2Session_Reconnect_OtherConnsUnaffected`, `v2session_test.go` → `buildHelloEarlyDataCaps`, `decryptAppFrame`, `waitForEnvelopes` — the multi-conn and decrypt patterns the new tests mirror.

In-flight overlap: `origin/feature/449` (stale since May) also touches `internal/relay/v2session.go`, in unrelated functions. Not a dependency; the edit here is one added call.

## Context

Slice S4a of Codex support, second half. #2643 recorded `multi_agent` on the session and filtered Codex rows out of `list_conversations`. Pushed frames still reach every conn. This ticket stops frames about a Codex conversation from reaching a conn that did not negotiate `multi_agent`, so an old client never sees a conversation or event it cannot handle.

## Design

### One choke point: `forwardEnvelope`

`forwardEnvelope` gains one check after its `V2StateOpen` gate:

```go
if m.withheldFromConn(s, env) {
    return nil
}
```

It returns nil, not an error: `drainReplayOnce` abandons the rest of a conn's replay tail on an error and advances `replayThrough` on nil, and the tail must keep draining past a withheld Codex event to the Claude events behind it. `drainOnce` and the inline callers only log errors, so nil is correct for them too.

Why here and not in `Push`: the replay pump and `emitResync` call `forwardEnvelope` directly and never pass through `Push`. `forwardEnvelope` runs on the Run goroutine, where `s.multiAgent` is readable without a lock. `Push` runs on the producer's goroutine and does not touch `m.sessions`.

`forwardAppReply` (the handler-reply path) is not gated: its frames are replies by construction.

### `withheldFromConn(s *V2Session, env protocol.Envelope) bool` (new file `v2session_agentgate.go`)

True only when all of these hold, checked in this order so the cheap exits come first:

1. `!s.multiAgent` — a capable conn gets everything, at zero added cost.
2. `m.cfg.CodexConversation != nil` — unwired (foreground, v1, tests) keeps today's delivery.
3. `env.InReplyTo == nil` — a reply is delivered as today, whatever conversation it names.
4. `pushedConversationID(env) != ""` — a frame about no conversation is delivered as today.
5. `m.cfg.CodexConversation(id)` reports true.

A withheld frame logs one Debug line with `event=v2.push.withheld_multi_agent`, `conn_id`, `type`. No payload bytes and no conversation id are logged.

### `pushedConversationID(env protocol.Envelope) string`

The conversation a pushed frame is about:

- `TypeConversationUpdated`: the payload's top-level `id`. This is the one pushed conversation type whose key is not `conversation_id`.
- Every other type: the payload's top-level `conversation_id`.
- An undecodable or non-object payload, or a missing or empty key, returns `""`, which means the frame is not about a conversation and is delivered.

It decodes into a two-field struct and ignores everything else. It reads `id` only for `conversation_updated`, because other payloads use `id` for other things.

Pushed types and their key, confirmed against `internal/protocol`:

| Keyed on `conversation_id` | Keyed on `id` | Not about a conversation (delivered unchanged) |
|---|---|---|
| every turn event from `interactive_turn_v2` (assistant_delta, turn_state, tool events, model_list, slash_command_list, background tasks, etc.), `session_transition`, `session_error`, `resetting`, `queue_state`, `attachment_offered`, `modal_shown`, `question_shown`, the connect-time reconciles (model list, slash commands, roster, queues, modals, questions), replayed ring events, `resync` | `conversation_updated` | `modal_dismissed`, `question_dismissed` (opaque ids only), `workspace_updated` (a workspace), debug-bundle and attachment chunks (`InReplyTo` replies), `rekey_request` (sent through `m.send` directly) |

The two dismissals carry only a modal or question batch id. The `*_shown` frame that introduced that id carried the `conversation_id` and was withheld, so an old conn is being told to dismiss an id it never received. The dismissal names no conversation and is harmless. Adding a conversation id to the dismissal payloads would change the wire protocol and is out of scope.

### Seam: `V2SessionConfig.CodexConversation func(conversationID string) bool`

The seam reports whether the conversation's bound session runs Codex. It is optional: nil means no conversation is Codex. It is called on the Run goroutine, like `RetainedModelLists`.

### Resolution rule, reused

In `handlers`, `agentOf` is renamed to exported `AgentOf`. It has one caller, in the same file, and the body does not change. In `cmd/pyry/relay.go`:

- `codexConversation(reg *conversations.Registry, harnessFor handlers.SessionHarnessFunc) func(string) bool` returns nil when either argument is nil. Otherwise it returns a closure: `reg.Get(id)`; a miss means false; otherwise `handlers.AgentOf(conv, harnessFor) == protocol.AgentCodex`.
- `startRelayV2` sets `CodexConversation: codexConversation(w.convReg, w.sessionHarness)`.

This gives the list's resolution rule. An unknown conversation, one with no bound session, and a harness miss all read as Claude, so the frame is delivered.

## Concurrency model

This adds no goroutines and no locks. `s.multiAgent` is written on Run before `V2StateOpen` and read on Run in `forwardEnvelope`. The seam takes the registry mutex (`Registry.Get`) and the pool's read lock (`Pool.HarnessFor`) from Run. `RetainedModelLists` already takes the same two locks on Run, from `handleNoiseInit`, so this adds no new lock-ordering edge. Neither lock is held across a call into the relay.

Cost: one small JSON decode plus one registry scan per frame, only for conns without `multi_agent` and only when the seam is wired. A capable conn exits at check 1.

## Error handling

There are no new error values. A payload that fails to decode reads as "no conversation" and the frame is delivered. That fails open to today's behaviour, the same way a harness miss does in #2643. A withheld frame returns nil from `forwardEnvelope`, and no caller treats it as a drop error.

## Testing strategy

`internal/relay/v2session_agentgate_test.go`:

- `TestPushedConversationID`, a table: `conversation_updated` returns `id`; `assistant_delta` returns `conversation_id`; `conversation_updated` carrying only `conversation_id` returns `""`; a non-`conversation_updated` type carrying only `id` returns `""`; a missing key, a JSON `null` payload, a non-object payload and malformed JSON all return `""`.
- `TestV2Session_CodexFrames_ReachOnlyMultiAgentConns` (AC 1, 2, 3). One manager with `CodexConversation` reporting true for `conv-codex`, and two conns on it: `capable` (`interactive`, `multi_agent`) and `old` (`interactive`). Push to each conn, in order: assistant_delta(conv-codex), conversation_updated(conv-codex), assistant_delta(conv-claude), conversation_updated(conv-claude), an assistant_delta(conv-codex) with `InReplyTo` set, then workspace_updated as the sentinel. Decrypt each conn's frames in order. `capable` gets all six. `old` gets exactly conv-claude delta, conv-claude update, the reply and the sentinel, in that order.
- `TestV2Session_CodexReplay_WithheldFromOldConn` (AC 1, replay path). The ring holds three events whose payload carries `conversation_id: conv-codex`, and the cursor is conv-codex. Each conn reconnects with `last_event_id=1`, then gets a sentinel pushed. The live push is held until the replay drains (#777), so the sentinel's arrival means replay has finished. `capable` gets two replay frames and then the sentinel. `old` gets only the sentinel, which shows the tail was not abandoned and the live stream was released.

`cmd/pyry/codex_conversation_test.go`:

- `TestCodexConversation_FromRealPool` uses `newDormantWritePool(t, "\"harness\":\"codex\",")`. `conv-claude` bound to the bootstrap reads false. `conv-codex` bound to the dormant Codex entry reads true. An unbound conversation reads false. A conversation bound to a session the pool does not hold reads false. An unknown id reads false.
- A nil registry or a nil harness func yields a nil seam.

## Open questions

- None blocking. If profiling ever shows the per-frame registry scan on old conns is hot, a per-conversation cache would be the next step. Nothing observed calls for it.

## Documentation handoff (pending — documentation stage)

`docs/protocol-mobile.md` § Capability negotiation (v2): a client without `multi_agent` is sent no pushed frame about a Codex conversation. That covers live turn events, conversation broadcasts, connect-time reconciles, reconnect replay and resync. Replies to its own requests are unchanged. `modal_dismissed` and `question_dismissed` carry no conversation, so such a client may receive a dismissal for an id it never saw and should ignore it.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. No new client-controlled input is read. The gate is keyed on `s.multiAgent`, a bool that `handleNoiseInit` derives from the negotiated set (#2643). It also reads the daemon's own outbound payloads, which the daemon authored, and the daemon's registry and pool. The conversation id the gate extracts comes from a frame the daemon is about to send. It never comes from the peer.
- [Trust boundaries — direction of failure] No findings. Every uncertain case fails open to today's delivery: an unwired seam, an undecodable payload, an unknown conversation, no bound session, a harness miss. This is the intended direction. The ticket's AC 2 requires Claude and unknown frames to be delivered exactly as today. The gate is a compatibility filter, not access control: it keeps an old client from being shown a conversation it cannot render. Hiding on uncertainty would drop Claude frames, which AC 2 forbids. A frame the gate fails open on names a conversation the daemon cannot place under Codex, so it cannot lead the client into a Codex session.
- [Trust boundaries — what the gate is not] OUT OF SCOPE. Replies to a request (`InReplyTo` set) are exempt, as the ticket states. An old client that names a Codex conversation id in a request still gets a reply. It can learn such an id only from a frame this gate now withholds, or from `list_conversations`, which #2643 already filters. How each request verb treats a Codex conversation belongs to #2645 (`model_list`) and #2647 (creation).
- [Security gate ordering] No findings. The check sits after `forwardEnvelope`'s existing `V2StateOpen` gate and before the seal. It can only drop a frame, never deliver one that the state gate would have refused. It returns before `s.send.Encrypt`, so a withheld frame spends no Noise send-nonce, and the phone's receive nonce stays in step.
- [Replay integrity] No findings. A withheld replay frame returns nil, so `drainReplayOnce` advances `replayThrough` past it and keeps draining. The live-push gate (#777) releases on schedule. The withheld event's id is behind the watermark, so the dedup guard cannot resurrect it.
- [Tokens, secrets, credentials] No findings. None are touched.
- [File operations] No findings. None.
- [Subprocess execution] No findings. None. `Pool.HarnessFor` reads in-memory state and never starts a session.
- [Cryptographic primitives] No findings. Sealing, re-key and the handshake are unchanged.
- [Network & I/O] No findings. An old conn receives a subset of what it received before, never more. The payload decode is bounded by the frame the daemon already built, and the push queue caps it.
- [Error messages, logs, telemetry] No findings. The single new Debug line carries `event`, `conn_id` and `type`. It carries no payload bytes and no conversation id. Payloads can hold Claude-authored text, which must never be logged (#833).
- [Concurrency] No findings. All reads run on the Run goroutine. The seam's two locks are already taken from Run by `RetainedModelLists`, so no lock-ordering edge is new. The frequency is new, though: the connect-time reconcile takes those locks once per handshake, and this gate takes them once per frame to an old conn. That frequency would turn a latent deadlock into a live one, so I checked for a holder of either lock that waits on Run. `Registry.Delete` calls its `onDelete` observer after releasing `r.mu`. `Registry.Update` runs only field-mutating closures from the handlers. `Pool`'s only callback under its lock is `TurnBusy`, which queries the turn tracker in `cmd/pyry`, not the relay. Nothing calls `ActiveConns`, `Push` or any other Run-synchronous manager method while holding the registry mutex or the pool lock. `Push` would not block anyway.
- [Threat model alignment] No findings. The change leaves authentication, E2E encryption, replay protection and pairing in `docs/protocol-mobile.md` § Security model untouched. It only narrows what an already-authenticated conn is sent.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
