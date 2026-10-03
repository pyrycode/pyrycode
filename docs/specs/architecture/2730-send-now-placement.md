# #2730 — report mid_turn_input for claude; place a sent-now message where claude read it

## Files read

- `cmd/pyry/send_now.go` → `newSendNowDeliver`, `sendNowGrace`, `scheduleCarryRelease`: the send-now write and the carry that keeps a conversation busy past a turn end the write may outlive.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory`: builds the operator `message` payload from `msg.Text`, stamps it, appends history, hands the push to `operatorMessageNotify`. Today it commits at the stdin write.
- `cmd/pyry/main.go` → the `msgqueue.New` literal (`OnDelivered`, `SendNow`), `turnBusy` construction, `settingsUpdaterAdapter.Capabilities` (`MidTurnInput: false`).
- `internal/msgqueue/queue.go` → `SendNowFunc`, `Queue.SendNow`: writes through the seam, then fires `OnDelivered` with `SentNow`; the queue id is the only key both calls share.
- `cmd/pyry/stream_turn_busy.go` → `turnBusyTracker.WaitIdle`, `openForSendNow`, `applyBusyLocked`: idle is membership leaving `busy`; a carried close keeps the conversation busy through `sendNowGrace`.
- `cmd/pyry/stream_turn_drain.go` → `startStreamTurnDrainV2`, `streamTurnSink.setCrashLoopNotify`/`crashLoop`: the single fan-in goroutine where `ToolUpdate` is handled, and the late-bound atomic hook precedent.
- `internal/streamsup/parser.go` → `emitUser`, `userLine`, `dropHarnessProseLine`: a block-array `user` text block on an unflagged main-conversation line reaches `emitUnrecognized` today.
- `internal/streamsup/runner.go` → `buildArgs`: the interactive spawn's fixed flags. `internal/streamsup/envelope.go` → `marshalTurnEnvelope`: one text block holding the payload verbatim.
- `internal/turnevent/event.go` → the `Event` variants; `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` classes an unknown variant as none/droppable.
- `internal/e2e/realclaude/testdata/mid_turn_user_v2.1.280.json` and `docs/knowledge/features/e2e-realclaude-mid-turn-user-capture-test-go.md`: echo shape, echo text equals the written text, back-to-back writes give two echo lines in write order, every turn's opener echoes too.

## Context

`send_queued_now` (#2729) works, but the operator `message` push and history entry are committed at the stdin write, which puts them before the tool result claude actually read them after. And `mid_turn_input` is still false, so clients do not show Send now. Under `--replay-user-messages` claude echoes each user message where it reads it; this ticket uses that echo as the placement signal and flips the capability in the same change.

## Design

**Spawn.** `buildArgs` adds `--replay-user-messages` after `--forward-subagent-text`. `pyry agent-run` builds its own argv in `internal/agentrun/streamrunner` and is untouched.

**Parser.** `userLine` gains `IsReplay bool \`json:"isReplay"\``. In `emitUser`, a `text` block on a line with `IsReplay` true and no parent tool-use id is never surfaced: the parser emits `turnevent.UserEcho{TextSHA256: sha256.Sum256(text)}` and logs content-free at Debug. Only a digest crosses into the event stream, so no echo text can reach the wire, a log line or history by construction. Subagent replay lines keep falling to the existing harness guard. Non-text blocks on a replay line keep today's handling.

**New variant** `turnevent.UserEcho{TextSHA256 [32]byte}` (the only new exported type). `turnMarkFor` and the emitter never see it as an opener or closer (default arm → none).

**Queue seam.** `msgqueue.SendNowFunc` gains the queued id: `func(ctx, convID string, id uint64, payload []byte) error`. That id is what `OnDelivered`'s `msg.ID` carries, so the write and the delivered notification can be paired without any payload reaching `OnDelivered`.

**`sendNowPlacement`** (cmd/pyry/send_now.go), per conversation a list of pending entries in registration order, each `{id, digest, ready, commit}`:

- `expect(convID, id, payload) (cancel func())` — called by `newSendNowDeliver` *before* the write, so an echo arriving before `OnDelivered` still finds its entry. `cancel` removes it when the write fails.
- `attach(convID, id, commit)` — called by `newOperatorMessageHistory` for a `SentNow` message. Unknown entry or `ready` → commit now. Otherwise store `commit` and start one waiter goroutine.
- `echo(sessionID, ev)` — resolves session → conversation (same `conversationForSession` the tracker uses), takes the first not-ready entry with an equal digest; commits it if attached, else marks it `ready`. No match → nothing (an ordinary message's opener echo).
- waiter: `waitIdle(ctx, convID)` then commit-if-still-pending. Because a carried close keeps the conversation busy through `sendNowGrace`, the fallback cannot fire inside that window and then duplicate on a second-turn opener.

Every commit takes its entry out under the placement mutex, and runs after the mutex is released, so echo, waiter and attach can each fire it at most once. A nil `*sendNowPlacement` makes `expect` a no-op and `attach` commit immediately.

**History/push.** `newOperatorMessageHistory` gains a `place *sendNowPlacement` parameter. Payload marshal stays at `OnDelivered` from `msg.Text`; the stamp, history append and push move into `commit`, which runs immediately for ordinary messages and through `place.attach` for `SentNow` ones. So ordinary messages are unchanged and each message commits once.

**Fan-in hook.** `streamTurnSink` gains `echo atomic.Pointer[func(sessionID string, ev turnevent.UserEcho)]` and `setEchoObserver`, the `crashLoop` pattern. `startStreamTurnDrainV2` hands a `UserEcho` to it after `busy.observe` and before the active-session gate, and never to `emitter.Handle`. The commit runs on the drain goroutine, after the drain has already handled the preceding `ToolUpdate`, so the push is enqueued after the tool result.

**Capability.** `settingsUpdaterAdapter.Capabilities`: `MidTurnInput: claude`.

Overlap: none of these files is touched by another open feature branch.

## Concurrency model

- `echo` runs on the drain goroutine; `expect`/`cancel` on the send-now caller's goroutine; `attach` on the same goroutine right after (from `Queue.SendNow`'s `notifyDelivered`); the waiter on its own goroutine. One mutex (`sendNowPlacement.mu`), a leaf: no other lock is taken while it is held, and `commit` is always called after unlock.
- Waiter goroutine: one per attached, not-yet-echoed send-now message. Exits when `WaitIdle` returns (idle or daemon ctx done). Commits on either exit; an already-taken entry makes it a no-op.

## Error handling

- Write fails → `cancel` removes the entry; msgqueue reinserts the message; no commit.
- No echo (child exits, turn interrupted, the echo dropped by the fan-in under pressure, or the text altered) → waiter commits at idle.
- Echo with no matching entry → ignored, nothing logged with content.
- `resolve` fails for the echo's session → ignored; waiter covers it.

## Testing strategy

- `sendNowPlacement` unit tests (table-free scenarios, fake `waitIdle` gated by a channel): echo after attach commits once; echo before attach commits at attach; idle without echo commits once and a later echo does nothing; two identical payloads commit in order; cancel then echo commits nothing; nil placement commits immediately.
- `newOperatorMessageHistory`: a `SentNow` message with a placement does not push until echo; an ordinary one pushes at once (existing tests cover the payload).
- Parser: the fixture's replay line produces one `UserEcho` with the digest of the text and no `Unrecognized`; a subagent replay line produces neither.
- `buildArgs` expectations in `runner_test.go` gain the flag; capability tests flip `MidTurnInput` for claude.
- msgqueue send-now tests pass the id.
- Real-claude e2e `TestRealClaude_SendQueuedNowPlacement` under `internal/e2e/realclaude` (needs-real-claude; the dispatcher runs it): long Bash turn, queue a marker message, `send_queued_now` mid-call; assert one `result`/turn end, marker in final text, exactly one user `message` push after the Bash tool result and before the final assistant text, `queue_state` ends empty.

## Open questions

- Does the drain-goroutine commit order the push after the tool_result on the wire in practice? The e2e asserts it.

## Documentation handoff

Pending for the documentation stage, `docs/protocol-mobile.md`:
- § `capabilities` (multi_agent, #2646): replace the `mid_turn_input` row's "False for both agents today" with: true for Claude sessions, meaning a queued message can be delivered into the running turn with `send_queued_now`; false for Codex, whose write path starts a new turn.
- § Queue (v2), `send_queued_now`: the `message` push for a send-now message sits in the stream at the point claude read it, after the tool result it followed, or as the opener of the next turn when claude took it there. A message whose echo never arrived is pushed by the time the turn goes idle.
- A changelog entry for both changes.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] SHOULD FIX: the echo is subprocess output, and its text is the DELIVERY payload, which for an attachment-bearing message names on-host paths. Before this ticket a block-array user text line went to `emitUnrecognized`, whose frame carries the raw block, so turning on `--replay-user-messages` without the parser arm would put every delivered prompt, paths included, on the wire. The boundary is the replay arm in `emitUser`: it emits only a SHA-256 digest, so no downstream code holds echo text. Land the flag and the parser arm in the same commit and pin it with a parser test that the fixture's replay line yields no `Unrecognized`. The conversation an echo applies to is resolved daemon-side from the producing session (`conversationForSession`), never read from the line.
- [Trust boundaries] No finding on spoofing. A line claude emits can at worst match a pending digest early, which moves where the push sits; the pushed content is always built from `msg.Text` in `newOperatorMessageHistory`, never from the echo. Tool output reaches the parser inside `tool_result` content, not as a top-level replay line.
- [Tokens] No findings. Nothing here generates, stores or compares a secret.
- [File operations] No findings. The history append path (`appendConversationHistory`) is unchanged; only when it runs moves.
- [Subprocesses] No findings. `buildArgs` adds one fixed literal; no caller-controlled value reaches the argv, and the environment is unchanged.
- [Cryptography] No findings. SHA-256 is an equality key between two daemon-held values, not a security check; nothing is compared against a secret.
- [Network and I/O] No findings. No new socket read. `UserEcho` takes the fan-in's droppable class (`turnMarkFor` default), so a flood of echoes cannot starve turn closers; a dropped echo falls back to the idle commit.
- [Errors, logs] SHOULD FIX: the replay arm's Debug line carries the site and block type only — not the text, and not the digest either, since a digest of a short message is a fingerprint of it. The placement logs nothing.
- [Concurrency] SHOULD FIX: one leaf mutex in `sendNowPlacement`, `commit` always runs after unlock, and each entry is removed under the lock by whichever of echo, waiter or attach takes it, so commit runs at most once. Waiter goroutines exist only for attached, un-echoed send-now messages and exit on idle or daemon ctx done; a client spamming `send_queued_now` is bounded by the queue's own backlog cap and by needing a busy turn. An entry `expect`ed but never `attach`ed (no `OnDelivered` wired, test-only) would linger; production always wires both, and the tests cover the wired pair.
- [Threat model] No findings beyond the above. `docs/protocol-mobile.md` § Security model's rule that daemon layout never reaches the wire is the one this ticket could break, and the first finding is what keeps it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03
