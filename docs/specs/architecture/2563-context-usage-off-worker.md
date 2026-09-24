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

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The request is untrusted network input, and the boundary is unchanged: steps 1–4 of `handleRequestContextUsage` (envelope decode, tolerated payload decode, `IsHostedConversation` membership) still run on the worker, in order, before anything else. The ask goroutine is spawned only past membership, so it only ever holds a registry-canonical conversation id. An unhosted id takes the not_found reject on the worker and spawns no goroutine and takes no slot.
- [Resource exhaustion: waiting asks per conn] No findings. `maxContextUsageAsksPerConn = 4` is a buffered `chan struct{}` local to `appFrameWorker`. The acquire in `handleRequestContextUsage` is a non-blocking `select`, so a full semaphore refuses the fifth ask on the worker at once with retryable `context_usage.unavailable` and never re-parks the worker. A waiting ask costs one goroutine plus the envelope it closes over, and that envelope is bounded by the transport's 1 MiB `maxFrameBytes` read limit in `internal/transport`. Below the seam, joiners on one conversation share one flight (`contextUsageResolver.fly`), so four asks on one conversation cost one child round trip, not four.
- [Resource exhaustion: daemon-wide ceiling] No findings. The ceiling is (open conns) × 4. A conn reaches `appFrameWorker` only after the Noise_IK handshake in `handleNoiseInit` has authenticated a paired device key, so every waiting ask belongs to a paired device, and the idle sweep closes conns that go quiet. The manager has no cap on concurrent conns. That is an existing property of the whole v2 surface, not something this change adds, so it is OUT OF SCOPE here. This change multiplies a per-conn cost by a constant; it adds no path whose cost grows with a client's input.
- [Resource exhaustion: hostile retry loop] No findings. A client that keeps re-sending asks while four wait gets one Warn log line and one small error reply per frame. That is the same cost the existing not_found and unavailable refusals already charge per frame, and the frame rate is bounded by the transport, not by this handler. No slot is taken, no goroutine is spawned, and the seam is not called, so the loop gains nothing over sending any other refused frame. Inbound frame rate limiting is not part of this ticket and is OUT OF SCOPE.
- [Cryptographic primitives: single-owner send CipherState] No findings. The ask goroutine emits only through `emitContextUsageReply` / `rejectContextUsageRequest` → `forwardContextUsageReply` → `forwardToRun`, a channel post to `m.appReply`. `forwardAppReply`, on Run, is the only sealer. Nothing new touches `s.send`, `s.recv` or calls `Encrypt`; the file header of `v2session_contextusage.go` now names the ask goroutine alongside the worker as a place that must never emit directly.
- [Concurrency: teardown and late replies] No findings. `appFrameWorker` derives `connCtx` and cancels it when it returns, which is exactly on `s.done` (closed by `closeWith`) or on the manager's `ctx`. The production seam's `contextUsageFlight.await` selects on `ctx.Done()`, so the wait ends at teardown. `resolveContextUsageRequest` checks `ctx.Err()` after the seam returns and answers nothing when the conn is gone, so no reply is built for a torn-down conn. If teardown lands after that check, `forwardToRun`'s `s.done` / `ctx` arms and `forwardAppReply`'s `V2StateOpen` gate drop the reply before sealing. A reconnect on the same conn id gets a fresh `*V2Session` from `handleFrame` after `closeWith` deletes the old one, so a late reply carrying the old session pointer can never be sealed under the new session's keys.
- [Concurrency: a seam that ignores ctx] No findings for production, noted for future seams. A `ContextUsageFor` implementation that ignored ctx would keep its ask goroutine alive until the seam returned on its own. Nothing else is held: the slot belongs to a semaphore whose worker has already returned, so it blocks no later conn, and `forwardToRun` still returns on `s.done` so the goroutine cannot block on the reply post. The production seam honours ctx (`contextUsageFlight.await`), and the underlying flight runs under the resolver's `base` context, shared per conversation, so it is bounded by hosted conversations, not by asks. The seam's contract that it must honour ctx is the bound; tests pin that the seam's ctx is cancelled on conn teardown and on manager stop.
- [Error messages, logs] No findings. The conversation id is logged only past membership: step 4's not_found passes `""` and logs no id, and both the new overflow refusal and the seam-refusal log only after membership succeeded, when the id is registry-canonical and cannot carry log-injection bytes. The overflow reason string goes to the log only; the wire carries the fixed `rejectContextUsageUnavailable` message. The new `v2.context_usage.request.abandoned` Debug line carries the event slug and conn id only.
- [Tokens, secrets; File operations; Subprocess execution] Not applicable. The change creates no token, touches no file and executes nothing. The seam's child round trip is unchanged.
- [Threat model alignment] No findings. `docs/protocol-mobile.md` § Security model is unaffected: the verb's authentication, membership and reply envelope are unchanged. Only its position in the conn's reply order moves, and clients already correlate on `in_reply_to`.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24

## Revisions

- **2026-09-24, verifier round 1:** added the `## Security review` section above. The ticket carried `security-sensitive` when the plan was written and the section was missing. The review found nothing outstanding, so the design and code are unchanged.
