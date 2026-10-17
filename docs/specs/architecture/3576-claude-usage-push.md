# #3576 — push each new Claude usage allowance reading to open clients

## Files read

- `cmd/pyry/claude_usage.go` → `claudeUsageStore`, `record`, `ClaudeUsage`, `claudeUsageWindow`: #3575's store. `record` runs on every Claude session's stdout forwarder; `ClaudeUsage` already copies values into fresh pointers, so a payload held outside the lock shares nothing with the store (#3575's security SHOULD FIX, written with this ticket in mind).
- `internal/streamsup/parser_usage_reading.go` → `readUsageWindows`: calls the handler only when a line carries at least one decodable window. A line with no `unifiedWindows` never reaches `record`, so "unchanged reading pushes nothing" falls out of #3575's rule.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveBroadcaster` (`ActiveConns` + `Push`): the fan-out surface.
- `cmd/pyry/queue_state_v2.go` → `queueStateEmitterV2`, `Run`, `broadcast`, `startQueueStateStreamV2`; `cmd/pyry/session_error_v2.go` → `sessionErrorNotify`: the hand-off-channel shape. Their comments record why `ActiveConns` must not run inline on a goroutine the manager can end up waiting on: it is funnelled onto the manager's Run goroutine.
- `cmd/pyry/conversation_update_v2.go` → `announce`: the uncorrelated-push precedent (no `in_reply_to`, shared `ts`, per-emitter envelope ids, a failed push logged and skipped).
- `internal/relay/v2session_conns.go` → `ActiveConn`, `ActiveConns`: every authenticated `V2StateOpen` session. `internal/relay/v2session_push.go` → `Push`: enqueues without blocking, never touches the replay ring, answers `ErrConnNotFound` for a closed conn.
- `cmd/pyry/relay.go` → `relayWiring.claudeUsage`, `startRelayV2` (the `startSessionErrorStreamV2` start and the drain's cleanup list): where the v2 manager exists.
- `cmd/pyry/main.go` → `runSupervisor`'s single `claudeUsageStore` mint and the `relayWiring{...}` literal.
- `cmd/pyry/relay_guard_test.go` → `TestClaudeUsageWiredToTheCompositionRoot`, the `TypeClaudeUsage` entry of the type classification map (`"reply"`; `TypeConversationUpdated` is the `"reply+push"` precedent).
- `docs/knowledge/features/claude-usage-reading.md`: #3575's overview; its concurrency section names this ticket as the consumer the copy-out protects.

No other `feature/*` branch touches these files.

## Context

#3575 keeps the daemon's latest allowance reading and answers it on request. Between requests a client's copy goes stale, since claude reports a new reading on every model call. This ticket pushes the same `claude_usage` frame, uncorrelated, to every open connection each time the store takes a new reading. No decision record is needed: it reuses the established hand-off-channel emitter shape.

## Design

### Store hook (`cmd/pyry/claude_usage.go`)

- New field `onRecord func(protocol.ClaudeUsagePayload)`, guarded by `mu`.
- `setOnRecord(f)` installs or (with nil) removes it under the lock.
- `record` stores the reading, then, still under the lock, calls `onRecord` with the payload built from what it just stored. Calling under the lock makes the push order the store order, so two sessions recording back to back cannot leave a client holding the older reading. The contract: `onRecord` must not block.
- `ClaudeUsage` and the hook share one unexported `payloadLocked` builder, so the push and the reply carry byte-identical payloads for one reading.

"A new reading" is every `record` call: each one restamps `observed_at`, so the stored reading changes even when claude's numbers repeat. A line that never reaches `record` (no `unifiedWindows`, `null`, `{}`, undecodable) pushes nothing.

### Emitter (`cmd/pyry/claude_usage_push_v2.go`, new)

`claudeUsageEmitterV2{in chan protocol.ClaudeUsagePayload; logger; nextID uint64}`:

- `newClaudeUsageEmitterV2(logger)` — buffered `in` (16).
- `notify(p)` — non-blocking send; on a full buffer, drop with a content-free Debug line. Never blocks the forwarder.
- `Run(ctx, bcast interactiveBroadcaster)` — drains `in` until ctx is done.
- `broadcast(ctx, bcast, p)` — marshal once, one shared `ts`, then for every conn in `ActiveConns` (no interactive gate: the request has none and the AC says every connection) push `Envelope{ID: next, Type: TypeClaudeUsage, TS, Payload}` with no `InReplyTo` and no `EventID`. A push error is logged at Debug and the loop continues; a cancelled ctx returns.
- `startClaudeUsageStreamV2(ctx, feed *claudeUsageStore, bcast, logger) func()` — nil feed returns a no-op. Otherwise builds the emitter, starts `Run`, then `feed.setOnRecord(e.notify)`. The cleanup removes the hook and waits for `Run` to exit.

### Wiring

- `relayWiring` gains `claudeUsageFeed *claudeUsageStore` beside `claudeUsage`. A concrete pointer, separate from the `handlers.ClaudeUsageSource` field, so a nil feed is a plain nil check and the request handler's nil-source behaviour is untouched.
- `runSupervisor` passes the same `claudeUsage` value as `claudeUsageFeed:`.
- `startRelayV2` starts the stream beside `startSessionErrorStreamV2` and runs its cleanup in the drain beside the other stream cleanups. The hook is installed only when a v2 manager exists, so a daemon without a relay never fills a buffer nobody drains.

Exported types added: none.

## Concurrency model

- One new goroutine, `Run`, per v2 relay leg. It exits when the daemon ctx is cancelled; the drain's cleanup waits for it.
- Producers: each Claude forwarder goroutine via `record` → `onRecord` → `notify` (a non-blocking channel send under `claudeUsageStore.mu`). Lock order: `claudeUsageStore.mu` is taken alone; the channel send never blocks, so no lock waits on the relay.
- `ActiveConns` and `Push` run only on `Run`, never on a forwarder: a forwarder blocked on the manager could stall claude's stdout while a dispatch handler waits on claude's stdin.
- `nextID` is read and written only on `Run`.
- Teardown: `setOnRecord(nil)` stops new hand-offs; a send racing it lands in the open buffer or drops. `in` is never closed, so a late send cannot panic.

## State transitions and identity reuse

| Event | Covered by |
|---|---|
| Two readings recorded back to back reach every conn in store order | `TestClaudeUsagePush_ReachesEveryConnInOrder` (`-race`) |
| Line without `unifiedWindows` after a reading: nothing pushed | `TestClaudeUsagePush_LineWithoutWindowsPushesNothing` |
| Reading stored with no conn open, then a conn opens: no error, nothing replayed, a request answers the reading | `TestClaudeUsagePush_NoConnOpen` |
| Relay leg teardown then a later reading: no push, no panic, record still stores | `TestClaudeUsagePush_AfterCleanup` (`-race`) |
| Reconnect | No state: a push is not recorded anywhere, so a new conn gets only later pushes. Covered by the no-conn row. |

## Error handling

- Push to a closed conn (`ErrConnNotFound`) or any push error: Debug log with event, `conn_id`, `env_id` and the transport error; fan-out continues.
- ctx cancelled mid-fan-out: return.
- Marshal failure: unreachable (fixed struct of a string, a time and numbers); Debug log, drop.
- Full buffer: Debug log, drop. Reachable only if `Run` is wedged.
- Nothing from the payload is logged on any branch.

## Testing strategy

New `cmd/pyry/claude_usage_push_v2_test.go`, driving real claude lines through a bare `streamsup.Parser` with `store.record` installed, a real `claudeUsageStore`, `startClaudeUsageStreamV2`, and a goroutine-safe broadcaster double that delivers pushes on a channel:

- two conns (one interactive, one not) each receive one `claude_usage` push per reading with no `in_reply_to`, payload equal to the request reply's bytes; two readings arrive in order.
- a line with no `unifiedWindows` after a reading pushes nothing (checked by recording a following reading and seeing it is the next push).
- one conn whose `Push` fails: the other still receives.
- no conn open: record, then the reply still answers the reading; nothing panics.
- after cleanup: a record pushes nothing.
- `TestClaudeUsageWiredToTheCompositionRoot` extended with `claudeUsageFeed: claudeUsage` and `startClaudeUsageStreamV2(ctx, w.claudeUsageFeed, mgr, logger)`.
- `TypeClaudeUsage` reclassified `"reply+push"` in the guard map.

`rate_limited` is untouched: no change to `emitRateLimit` or the parser.

## Open questions

- Interactive gate: none, chosen to match the AC ("every connection") and the request's own lack of a gate. Older clients drop the unknown type.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md` § Claude usage allowance reading: the daemon also pushes the `claude_usage` frame, uncorrelated (no `in_reply_to`), to every open connection each time it stores a new reading; pushes are not replayed on reconnect; a client should ask once with `request_claude_usage` after connecting.
- `docs/protocol-mobile.md` v2 type table row for `claude_usage`: it is no longer "never broadcast"; it is a reply and an uncorrelated push.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. No new boundary: the pushed payload is built by the store's `payloadLocked` from `streamsup.UsageWindow` values, which #3575's `readUsageWindows` already reduced to `*float64` / `*int64` plus the daemon's own clock. No claude-authored string and nothing client-supplied reaches the push. The emitter reads no inbound frame.
- [Tokens, secrets, credentials] No findings. The payload holds no credential and the emitter never touches the account token provider.
- [File operations] No findings. Memory only; nothing is written or replayed from disk.
- [Subprocesses] No findings. No argv, environment or child lifecycle change; the reading still comes off the existing claude stdout.
- [Cryptography] No findings. Pushes travel over each conn's existing Noise_IK session through `V2SessionManager.Push`.
- [Network and I/O] No findings. One small fixed-size frame per conn per model call. Each conn's existing push queue bounds what a slow client can retain, and `Push` enqueues without blocking. The emitter's own buffer is bounded at 16 with drop-on-full.
- [Errors, logs, telemetry] No findings. Every log line in the emitter carries only `event`, `conn_id`, `env_id` and the transport error; no branch logs the payload or a marshal error's text.
- [Concurrency] SHOULD FIX: `onRecord` runs under `claudeUsageStore.mu` on every Claude session's stdout forwarder, so a hook that blocked would stall claude's output for every session at once. `notify` must be a select with a default, and a test must show it returns with the buffer full. Lock order otherwise: the store lock is the only lock taken on the producer side and nothing else is taken under it. The one new goroutine exits on the daemon ctx and the drain waits for it; the hook is removed before that wait, and `in` is never closed, so a late send cannot panic.
- [Threat model] No findings. Audience: every authenticated `V2StateOpen` conn, the same paired devices that may already send `request_claude_usage` (no interactive gate on either). A push tells a paired device, in real time, that some Claude session made a model call; such a device can already see conversation activity and could poll for the same `observed_at`, so no new exposure class. Per-device revocation (threat 4) removes the conn from `ActiveConns`. A malicious relay sees ciphertext whose timing now correlates with model calls; frame timing of `assistant_delta` already reveals that (threat 3).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17
