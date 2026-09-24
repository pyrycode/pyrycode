# #2563 — request_context_usage waits off the conn's frame worker

## Files read

- `internal/relay/v2session_contextusage.go` → `handleRequestContextUsage`, `forwardContextUsageReply` — the handler whose step 5 (the `ContextUsageFor` seam call) blocks the worker for a whole turn.
- `internal/relay/v2session.go` → `appFrameWorker` (the `appFrameContextUsageRequest` arm and the strict-FIFO doc), `appFrameKind`, `forwardToRun`, `forwardAppReply`, `closeWith` — the worker, its teardown (`s.done`), and the reply funnel whose `s.done` / `ctx` arms and `V2StateOpen` gate already stop a late reply being sealed.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` open tail — spawns the one worker per conn with `runCtx`.
- `cmd/pyry/relay_context_usage.go` → `contextUsageResolver.fresh`, `contextUsageFlight.await` — production seam: waits on `WaitIdle` with no timeout, joiners share one flight per conversation, and `await` honours `ctx.Done`.
- `internal/relay/v2session_contextusage_test.go`, `internal/relay/v2session_appframe_test.go` → `ctxUsageManagerFor`, `blockingHandler`, `prolificHandler`, `waitForConnNoiseMsg`, `waitForCloseCode` — test scaffolding to reuse.

Overlap: `origin/feature/449` touches `v2session.go` (a stale May branch); edits here are comment-local plus one arm, so a later merge is additive.

## Context

A mid-turn `request_context_usage` parks the conn's `appFrameWorker` inside the seam until the turn ends. Every later frame on the conn queues behind it, including the `send_message` that would end the turn — a deadlock observed in pyrycode-mobile #946 (91 s until teardown). The protocol promise ("deferred until the turn ends, answered after it") stays; only the wait moves off the worker.

## Design

Split `handleRequestContextUsage` at step 5:

- **Steps 1–4 stay on the worker, unchanged** — envelope decode, tolerated payload decode, membership. These are cheap and their ordering carries the security property; keeping them synchronous means the not_found reject still answers in FIFO order with no goroutine spawned for an unhosted id.
- **Step 5 onward runs on a per-ask goroutine.** Past membership, the handler takes a slot from a per-conn semaphore and spawns a goroutine that calls `ContextUsageFor`, then emits the reading or the unavailable reject through the existing `emitContextUsageReply` / `rejectContextUsageRequest` → `forwardContextUsageReply` → `forwardToRun`. Nothing new calls `Encrypt`; the single-owner send CipherState rule is unchanged.

### Bound: at most `maxContextUsageAsksPerConn = 4` waiting asks per conn

- The semaphore is a `chan struct{}` of capacity 4, **local to `appFrameWorker`** and passed into the handler. No `V2Session` field: acquire happens only on the worker, release only on the ask goroutine, and a reconnect gets a fresh worker and so a fresh semaphore.
- Acquire is non-blocking. When all four slots are taken, the ask is refused at once on the worker with the existing retryable `context_usage.unavailable` (reason "too many context usage requests waiting on this connection", conversation id logged since it passed membership). Blocking instead would re-park the worker — the bug itself.
- Why 4: a waiting ask costs one goroutine plus its plaintext, and below the seam joiners on one conversation share a flight, so the cost is small; a phone normally watches one conversation, and 4 leaves room for a few open at once without letting one untrusted conn grow goroutines without limit. Total is bounded by conns × 4.

### Teardown

- `appFrameWorker` derives `connCtx, cancel := context.WithCancel(ctx)` and `defer cancel()`. The worker already returns on `s.done` (conn teardown) and on `ctx` (manager exit), so its return cancels every waiting ask's seam context. Only the ask path receives `connCtx`; other arms keep `ctx` as today.
- After the seam returns, if `connCtx` is done the goroutine returns without replying (Debug `v2.context_usage.request.abandoned`) — no reply is built, let alone sealed, for a torn-down conn. `forwardToRun`'s escape arms and `forwardAppReply`'s open-state gate remain the belt behind it.
- The goroutine then releases its slot and exits; its lifetime is bounded by the seam honouring ctx (production `await` does) plus `forwardToRun`'s escape arms.

### Ordering change (comment updates)

This verb's reply can now arrive after replies to frames sent later; clients correlate on `in_reply_to`. Update the `appFrameWorker` doc (strict FIFO emission holds for every arm except this one's deferred reply), the `appFrameContextUsageRequest` arm and kind comments, and the file header / handler doc in `v2session_contextusage.go` ("WHERE IT RUNS").

## Concurrency model

- Worker goroutine: steps 1–4, slot acquire, spawn. Never blocks on the seam.
- Ask goroutine (≤ 4 per conn): seam call → ctx check → emit via `forwardToRun` → release slot. Communicates with Run only through `m.appReply`.
- Shutdown: `closeWith` closes `s.done` → worker returns → `cancel()` → seam returns → goroutine drops the reply and exits. Manager exit: `runCtx` cancels both directly.

## Error handling

- Slot exhausted → retryable unavailable reject (a client can retry after its earlier asks are answered).
- Seam refusal → unchanged retryable unavailable.
- Seam returns after teardown → no reply, Debug log only.

## Testing strategy

New tests in `v2session_contextusage_test.go`:

- **Later frame not blocked (AC-1):** seam blocks until released; ask id 1, then a v1-routed `send_message` (id 2) served by an immediately replying handler. Its reply arrives while the seam is still blocked; after release exactly one `context_usage` with `in_reply_to` 1 follows (AC-2). Fails today: the second frame waits behind the seam.
- **Bound:** four blocked asks, a fifth is refused at once with retryable unavailable and `in_reply_to` its own id; releasing yields four readings — five replies in total, each request id answered exactly once.
- **Teardown (AC-3):** seam waits on `ctx.Done` only, then returns a *successful* reading; the conn is torn down with a malformed inner frame. The seam's ctx is cancelled (it returns), and no noise_msg reply appears for the conn. Manager-stop variant: stopping the manager also returns the seam.

Existing context-usage tests stay green unchanged (replies still correlate and count to one).

## Documentation handoff

Pending for the documentation stage: `docs/protocol-mobile.md` § "Asking for a context usage reading on demand" — next to the paragraph starting "A request that arrives while a turn is in flight", state that the deferred ask does not delay the connection's other frames and that its `context_usage` reply may arrive after replies to frames sent later, correlated by `in_reply_to`. Also worth recording: a conn holds at most 4 waiting asks; a fifth is refused with retryable `context_usage.unavailable`.

## Open questions

- None blocking. Whether the cap should be configurable — no; a constant until a client needs more.
