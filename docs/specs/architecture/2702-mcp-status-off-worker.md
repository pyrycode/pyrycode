# #2702 — mcp_status_request waits off the worker, under a bound

## Files read

- `internal/relay/v2session_mcpstatus.go` → `handleMCPStatusRequest`, `rejectMCPStatusRequest`, `forwardMCPStatusReply`: the handler that today calls `MCPStatusFor` inline on the worker; every emission already goes through `forwardToRun`.
- `internal/relay/v2session_contextusage.go` → `handleRequestContextUsage`, `resolveContextUsageRequest`, `maxContextUsageAsksPerConn`: #2563's shape this ticket copies (membership on the worker, non-blocking slot, goroutine under the conn-scoped ctx, nothing sent once ctx ended).
- `internal/relay/v2session.go` → `appFrameWorker` (derives `connCtx`, owns the `asks` semaphore; doc "ONE EXCEPTION"), the `appFrameMCPStatusRequest` / `appFrameContextUsageRequest` kind comments, and the `TypeMCPStatusRequest` dispatch arm on Run (inert gates stay there).
- `cmd/pyry/main.go` → `resolveBoundMCPStatus`, `mcpStatusFor`: the production seam, which passes the conn ctx to `QueryMCPStatus` with no deadline; `effectiveEffortQueryTimeout` / `resolveBoundEffectiveEffort`: the precedent for a bound placed in `cmd/pyry`.
- `internal/streamsup/runner.go` → `actuateMCP` doc: "the caller's context is the only bound on the wait … a caller must pass a deadline"; `QueryMCPStatus` shares that contract, so the bound belongs to the caller.
- `cmd/pyry/session_memory_search.go` → `memorySearchMCPQueryTimeout`: the other caller of `QueryMCPStatus`, which already bounds itself; untouched.
- `internal/relay/v2session_mcpstatus_test.go`, `internal/relay/v2session_contextusage_test.go` → the manager/conn helpers and #2563's three tests to mirror.
- `cmd/pyry/session_mcp_status_test.go` → `mcpStatusQueryPlan`: the runner double to extend with deadline recording; `session_effective_effort_test.go` → `deadlineFor`, the precedent assertion.

## Context

An unanswered `mcp_status_request` holds the conn's `appFrameWorker`, so every later frame on that conn — including `send_message` — waits until the connection closes. Two halves, both in this ticket: move the wait off the worker (unblocks later frames), and bound the production seam's wait (gets the ask an answer and frees its slot). No decision record needed; this applies #2563's decision to a second verb.

No other feature branch touches these three files.

## Design

**Relay (`v2session_mcpstatus.go`).** `handleMCPStatusRequest(ctx, s, asks chan struct{}, plaintext)`:

1. Envelope decode (unchanged, unreachable arm).
2. Payload decode → `protocol.malformed` (unchanged).
3. Membership → `conversation.not_found` (unchanged).
4. Non-blocking slot take on `asks`; on full, refuse at once with `rejectMCPStatusUnavailable` (retryable), reason "too many MCP status requests waiting on this connection".
5. `go` a goroutine that releases the slot on return and calls `resolveMCPStatusRequest(ctx, s, id, convID)`: seam call; if `ctx.Err() != nil` after it returns, log a Debug `v2.mcp_status.request.abandoned` and send nothing; else reject-unavailable on `!ok` or emit the reply.

New constant `maxMCPStatusAsksPerConn = 4`, its own cap rather than sharing context-usage's so neither verb can starve the other's slots. The conversation id is still never logged (the existing no-remote-values test pins it).

**Worker (`v2session.go`).** `appFrameWorker` makes a second semaphore `mcpAsks` sized `maxMCPStatusAsksPerConn` and passes `connCtx` + `mcpAsks` to `handleMCPStatusRequest`. Update the doc comments on `appFrameMCPStatusRequest`, `appFrameContextUsageRequest` ("the only member whose wait runs off the worker"), `appFrameWorker` ("ONE EXCEPTION" → two exceptions) and the worker arm.

**Bound (`cmd/pyry/main.go`).** New `mcpStatusQueryTimeout = 30 * time.Second` beside `effectiveEffortQueryTimeout` (same value, same reason). `resolveBoundMCPStatus` wraps the `QueryMCPStatus` call in `context.WithTimeout(ctx, mcpStatusQueryTimeout)`. A silent live child then yields `false` within the bound, which the relay answers with the retryable `mcp_status.unavailable`.

## Concurrency model

One goroutine per accepted ask, at most `maxMCPStatusAsksPerConn` per conn. Exit paths: the seam returns (bounded by `mcpStatusQueryTimeout` in production), or `connCtx` ends — cancelled when the worker returns, i.e. on `s.done` (conn teardown) or Run's ctx (manager stop). The slot is released by `defer` on every path. Replies leave only through `forwardToRun`; nothing off Run touches `s.send`, preserving the single-owner CipherState.

## Error handling

- Silent child → bound expires → `ok=false` → retryable `mcp_status.unavailable` with `in_reply_to`.
- All slots taken → retryable `mcp_status.unavailable` at once, from the worker.
- Teardown mid-wait → no reply, Debug log only.
- `forwardToRun` refusing (teardown race) → existing Debug "reply dropped" log.

## Testing strategy

Relay (`v2session_mcpstatus_test.go`), with a `mcpStatusManagerWith` helper adding a v1 handler table and stop:

- **Wait does not block later frames (AC1):** a seam that blocks until its ctx ends; send `mcp_status_request` then `send_message`; the `send_message` reply arrives while the ask waits. Fails on `main` (the reply never arrives).
- **Waiting asks are bounded and teardown ends them (AC3):** fill four slots with a blocking seam; the fifth is refused at once with retryable `mcp_status.unavailable` correlated to its id; tear the conn down; all four seams see ctx cancelled; exactly one sealed reply exists (the refusal), even though the seam answers `true` after cancellation.
- Existing tests (unavailable reject, success, other-conn isolation, no-remote-values logs) keep passing through the goroutine path, covering AC2's relay half.

cmd/pyry (`session_mcp_status_test.go`):

- **Bound (AC2):** record the query ctx deadline in `mcpStatusQueryPlan`; with a background ctx the child query carries a deadline within `(0, mcpStatusQueryTimeout + 1s]`; a blocked child under a short parent deadline returns `(zero, false)`.

## Open questions

- None outstanding; cap sharing settled as separate caps above.

## Documentation handoff

Pending for the documentation stage — `docs/protocol-mobile.md`, section **Asking for MCP status on demand**:

- Add a paragraph matching `request_context_usage`'s: the wait does not delay the connection's other frames (#2702), so the `mcp_status` reply or its `error` may arrive after replies to frames sent later; clients match by `in_reply_to`. State the per-connection limit of four waiting asks, and that an ask beyond it is refused at once with a retryable `mcp_status.unavailable`.
- In the reject table, extend the `mcp_status.unavailable` row's condition to cover "no child reply within the daemon's bound" and "too many asks already waiting on this connection".

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The request is untrusted network input; decode and the membership gate in `handleMCPStatusRequest` stay on the worker before any goroutine is spawned, and the inert gates (nil seam, non-interactive) stay on Run in the `TypeMCPStatusRequest` dispatch arm. The seam receives only a membership-checked id; `resolveBoundMCPStatus` stamps the registry id, not the request's.
- [Tokens] No findings. No secret or credential is created, stored or read on this path.
- [File operations] No findings. No filesystem access is added.
- [Subprocesses] No findings. The child round trip is the existing `QueryMCPStatus`; this change only adds a deadline to its ctx.
- [Cryptography] SHOULD FIX (designed in): moving the reply onto a goroutine makes a concurrent `Encrypt` possible if it ever seals directly. Every emission must stay on `forwardMCPStatusReply` → `forwardToRun`; the verifier checks no new `s.send` use appears.
- [Network and I/O] SHOULD FIX (designed in): off-worker waits remove the worker's implicit one-at-a-time limit, so a hostile client could otherwise spawn unbounded goroutines. `maxMCPStatusAsksPerConn` caps per-conn waiting asks with a non-blocking refusal; `mcpStatusQueryTimeout` caps each wait in production. Daemon-wide total is conns × 4, the same envelope context usage already accepts.
- [Errors, logs, telemetry] No findings. Refusal messages are the existing static constants; the overflow and abandoned logs carry `conn_id`, `code` and a daemon-authored reason, never the conversation id or payload (pinned by `TestV2Session_MCPStatusRequest_LogsContainNoRemoteValues`).
- [Concurrency] No findings. Each goroutine exits when the seam returns, bounded by the timeout or by `connCtx`, which the worker cancels on `s.done` or Run's ctx; the slot release is deferred. A seam returning after teardown sends nothing (`ctx.Err()` check), so no frame is forwarded for a dead conn.
- [Threat model] No findings beyond the above: the relevant threat in `docs/protocol-mobile.md` § Security model is resource exhaustion by an authenticated-but-hostile peer, addressed by the cap and the bound. `appFrameMCPReconnect` / `appFrameMCPToggle` keep their unbounded inline wait — OUT OF SCOPE per the ticket, picked up by a separate ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03
