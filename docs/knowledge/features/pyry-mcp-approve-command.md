# `pyry mcp-approve` — MCP stdio approve tool, forwarding over the control socket

The tool host for the Streamrunner Interactive permission bridge (T1 spike #1075). A non-YOLO headless claude spawned with `--permission-prompt-tool mcp__pyry_approve__approve` synchronously calls the registered `approve` MCP tool for every non-allowlisted tool use and blocks on its allow/deny JSON before running the tool. `pyry mcp-approve` is that MCP server: a short-lived stdio subprocess claude spawns (like `pyry status`/`pyry rekey`/`pyry acp`), which dials the daemon's control unix socket, forwards each approval request via the `mcp.approve` verb ([`control-plane.md` § Approve](control-plane.md#approve-mcpapprove-verb--forward-to-permbridge-block-default-deny-1104), #1104), and returns the daemon's verdict as the MCP tool result.

The sibling [#1106](../codebase/1106.md) generates the mcp-config that registers this server as `pyry_approve` with tool `approve` and points claude's `--permission-prompt-tool` at it (`cmd/pyry/mcp_config.go`'s `permissionArgs`/`renderMCPApproveConfig`, deriving the tool reference from this subcommand's own `mcpServerName`/`approveToolName` constants so it can't drift) — that spawn-arg wiring is a separate unit, documented in [codebase/1106.md](../codebase/1106.md). This subcommand ships the server the config names.

## Role in the chain

```
claude (non-YOLO, --permission-prompt-tool mcp__pyry_approve__approve)
  │  tools/call approve {tool_name, input, tool_use_id}   (MCP over stdio)
  ▼
pyry mcp-approve                                           (this ticket, #1105)
  │  control.Approve(ctx, socketPath, ApprovePayload)       (control unix socket, mcp.approve verb)
  ▼
daemon: handleApprove → permbridge.Registry.Register/Await  (#1104, #1103)
  │  blocks until Resolve(id, Verdict) or timeout
  ▼
in-process modal-resolve consumer                           (#1080 — produces the trusted allow)
```

This subcommand is a **pure forwarder + fail-closed adapter**: it re-frames claude's MCP `tools/call` into a control-socket request and re-frames the control-socket verdict into an MCP tool result. It parses nothing it doesn't have to — `input` (the tool call's arguments) is model-controlled and potentially adversarial, and is carried as opaque `json.RawMessage` end to end, on both legs, never parsed or dispatched on.

## MCP surface

Speaks JSON-RPC 2.0 over stdio using the same line-delimited framing as `internal/acp.Transport` (reused verbatim via `serveACP`, not re-derived — this is the second `cmd/pyry` subcommand, after `pyry acp` (#756), to drive that transport with a different method table, confirming it is genuinely protocol-neutral rather than ACP-specific).

- **`initialize`** → `{protocolVersion, capabilities:{tools:{}}, serverInfo:{name:"pyry_approve", version}}`. Echoes the client's `protocolVersion` when present and non-empty; falls back to `"2025-06-18"` otherwise (our surface is version-invariant across MCP revisions, so echoing maximizes acceptance per the MCP lifecycle rule). Non-object `params` → `CodeInvalidParams`; absent/empty tolerated.
- **`tools/list`** → one tool, `{"name":"approve", "description":"...", "inputSchema":{...}}`. The name `"approve"` is load-bearing — combined with `serverInfo.name`, it forms the tool reference `mcp__pyry_approve__approve` claude is pointed at. `inputSchema` is advisory only.
- **`tools/call`** → params `{"name": string, "arguments": json.RawMessage}` → result `{"content":[{"type":"text","text":"<verdict JSON>"}], "isError": false}`, where `<verdict JSON>` is the T1 approval contract: `{"behavior":"allow","updatedInput":{...}}` or `{"behavior":"deny","message":"..."}` — byte-identical to `control.ApproveResult`/`permbridge.Verdict`.

`notifications/initialized` (and any other notification) needs no handler — `acp.Transport`'s `dispatchNotification` drops an unregistered notification silently.

## The fail-closed core (`tools/call approve`)

**Invariant (security-critical): the handler always returns a well-formed tool result with `isError:false` whose first text block is a verdict JSON — never a JSON-RPC error, never a hang.** The subcommand's *only* self-originated verdict is **deny**; **allow reaches claude solely by passing through the daemon's `resp.Approve`**, which the daemon produces only via its trusted in-process resolver (#1080). There is no code path in this subcommand that constructs an allow.

Steps, each failure terminating in a deny result:

1. Unmarshal `params` into `{name, arguments}`. Failure → `deny("malformed approval request")`. Deliberately a deny result, not a JSON-RPC error — an error on the permission path risks hanging or confusing claude's turn.
2. `name != "approve"` → `deny("unknown tool")`.
3. Unmarshal `arguments` into `control.ApprovePayload{tool_name, input, tool_use_id}`; `input` stays `json.RawMessage`. Failure → `deny("malformed approval request")`.
4. `control.Approve(ctx, socketPath, payload)` — `ctx` is the `Serve` ctx, passed straight through with **no per-call deadline added**. The client carries no duration of its own (`approveServer` has no timeout field); the bound is liveness-shaped and lives entirely in `control.Approve`/`requestPatient` — see § `internal/control.Approve` client helper (#1929).
5. Map the outcome:
   - error (socket unreachable, daemon `Response.Error`, conn ended with no verdict, ctx cancelled) → `deny("approval unavailable")`
   - `res == nil` (belt-and-suspenders; `control.Approve` already errors on this) → `deny("no verdict")`
   - else → marshal `res` verbatim as the verdict text (both allow and daemon-deny pass through here, `updatedInput` byte-preserved)
6. Wrap as `{content:[{type:"text", text:<verdict>}], isError:false}`.
7. Log `tool_use_id` + `behavior` only — never `input`, `tool_name`, or the raw `params`/`arguments` bytes, on any branch including the early parse-failure denies. Logger writes to **stderr only**; stdout is exclusively the JSON-RPC frame stream.

**The daemon's own deny message wins at every window, and no margin buys that anymore (#1929).** Through #1929 this step used a fixed `mcpApproveClientMargin = 30s` added to `approvalTimeout()`, so the client's deadline expired just after the daemon's — the margin decided only which deny message reached claude, never the outcome. #1929 deleted the margin along with the client's deadline entirely: since the client itself no longer expires, the daemon's timeout-deny is now the *only* way this path resolves to a deny on a live conn, at every configured window, structurally rather than by margin sizing. The pre-#1929 daemon-message-wins guarantee is what #1929's `TestMCPApprove_DaemonMessageWinsAtEveryWindow` now pins directly against the production constructor.

The whole `approveServer{...}` literal is built exactly once, at construction, by `newMCPApproveServer(socketPath, log)` — the sole production construction site (`grep -n 'approveServer{' cmd/pyry/mcp_approve.go` returns exactly one hit, inside that function). Since #1929 that literal is `{socketPath, log}` only: **no duration field, and `newMCPApproveServer` reads no duration source.** That absence is itself the enforcement — no cheap test can pin "the client never re-grows a large bound," so a reviewer verifies it structurally (no timeout field on `approveServer`, `mcpApproveClientMargin` gone, the constructor free of `approvalTimeout()`) rather than by reading an assertion. The pre-existing test helper `newApproveServer` (`mcp_approve_test.go`) now delegates straight to `newMCPApproveServer` — the reason it didn't (eleven `t.Parallel()` tests would have read the ambient `PYRY_APPROVAL_TIMEOUT` through a shared derived deadline) dies along with the env read this constructor used to do.

`--permission-prompt-tool` argv/config generation landed in [#1106](../codebase/1106.md); the live `streamsup` interactive-spawn wiring landed in [#1168](../codebase/1168.md) — `newStreamRunnerFactory`'s closure injects `permissionArgs(false, mcpApprovePath)` on every non-yolo spawn, so this server now has a live production caller beyond `pyry agent-run`.

## `internal/control.Approve` client helper

`Approve(ctx, socketPath, req ApprovePayload) (*ApproveResult, error)` in `internal/control/client.go` — mirrors `Rekey`. Sends `Request{Verb: VerbMCPApprove, Approve: &req}` through `requestPatient`, a sibling of the shared `request` helper that every other client verb still uses (#1929). **Bounded by liveness, not duration**: `requestPatient` installs no conn read deadline at all, and instead registers `stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })` before the exchange — a cancelled ctx pokes the deadline into the past, which expires a parked or future read immediately, so cancellation reaches a blocked `Decode` for the first time (previously nothing watched `ctx.Done()` past the dial). `defer stop()` deregisters the watcher on every return so a never-cancelled ctx leaves nothing parked. The three things that end the wait are: the daemon answering, the caller cancelling `ctx`, and the conn ending (daemon exit or close mid-approval → deny promptly, not after any window). The dial itself is unaffected by this and still fails fast (≤`dialRetryBudget`≈1.5s) independently of the ctx's deadline or absence of one, so a missing daemon yields a bounded error, not a hang. No `ErrorCode` mapping — any error is returned verbatim since the caller fail-closes to deny regardless of cause.

**Before #1929, this was inverted**: callers were *required* to pass a ctx deadline ≥ the daemon's approval window, because `request`'s conn deadline came from that ctx and a client-side expiry did not just lose the race for the deny message — closing the conn on the way out is what the daemon's `watchApproveConn` reads as a lost caller, so a client-side give-up **terminated the daemon's approval outright** and `permbridge`'s one-shot resolution discarded every later answer (see [control-plane.md § Approve](control-plane.md#approve-mcpapprove-verb--forward-to-permbridge-block-default-deny-1104)). That coupling is why #1929 exists: a daemon-side hold that can legitimately outlast a fixed window (#1912) has no client-side duration that can distinguish "still being answered" from "wedged" — any ceiling picked would just truncate the first to guard the second. A daemon that is alive, holds the conn open, and never resolves is knowingly **not** bounded by this helper; the daemon owns that guarantee via `permbridge`'s registry-owned timer plus `watchApproveConn`'s disconnect/shutdown denies (both described in [control-plane.md § Approve](control-plane.md#approve-mcpapprove-verb--forward-to-permbridge-block-default-deny-1104)).

## Framing integrity under adversarial `input`

The verdict JSON is embedded as a *string* inside `content[].text`, and the whole tool result is emitted by one outer `json.Marshal` — so a control byte (e.g. a literal newline) inside an echoed `updatedInput` is escaped in the outer string, keeping the MCP stdout frame a single physical line. `input` is validated as well-formed JSON at the `arguments` unmarshal step; a malformed value denies there rather than producing a partial frame. On the daemon side, the control-socket response is read with a whitespace-agnostic streaming `json.Decoder`, so `Input`'s internal whitespace round-trips regardless.

## Concurrency

`acp.Transport.Serve` runs one read-loop goroutine dispatching inline; a blocking `tools/call` now blocks that loop for as long as the daemon holds the approval — no longer capped at a window plus margin (#1929) — which is safe because claude's `--permission-prompt-tool` path calls `approve` synchronously and blocks on the result before emitting any further MCP frame (at most one in-flight approval per process), and the handler issues no outbound `Transport.Call` (no read-loop deadlock risk). `approveServer` is read-only after construction — no locks, no shared mutable state.

**A mid-approval SIGTERM unblocking the control read is true only since #1929, and this section used to claim otherwise incorrectly.** Before #1929, `toolsCall` wrapped `ctx` in a `context.WithTimeout` and the resulting `cctx` was never otherwise watched: `request` installed a conn deadline and returned, and nothing selected on `ctx.Done()` downstream of the dial — so a signal arriving mid-approval left the process parked on the control-socket read until that deadline, not until the signal. `#1929`'s `requestPatient` fixes this for real: `context.AfterFunc(ctx, …)` registers one goroutine that pokes the conn's deadline into the past the instant `ctx` is cancelled, which is what a `Serve`-ctx SIGTERM now actually reaches. The lesson generalizes: a comment (or a doc) asserting a ctx cancellation path is live is only true if something downstream of the last blocking call actually selects on `ctx.Done()` — a deadline derived from the ctx at call time is not the same thing as watching it.

## Error → verdict table

| Path | Tool result (`isError` always `false`) |
|---|---|
| Daemon resolver allows (#1080) | **allow** — passed through verbatim |
| Daemon denies (resolver / timeout / disconnect / shutdown / dup-id) | **deny** — daemon's message passed through |
| Control socket unreachable | deny (`"approval unavailable"`) — bounded by `dialRetryBudget` (~1.5s), independent of any ctx deadline |
| Daemon `Response.Error` (nil registry / missing id) | deny (`"approval unavailable"`) |
| Conn ends without a verdict (daemon exits or closes mid-approval) | deny (`"approval unavailable"`) — immediate, `Decode` returns EOF |
| Signal (SIGTERM/SIGINT) mid-approval | deny (`"approval unavailable"`) — ctx cancellation wakes the read via `requestPatient`'s watcher (#1929) |
| `params`/`arguments` malformed JSON | deny (`"malformed approval request"`) |
| `name != "approve"` | deny (`"unknown tool"`) |
| `res == nil` on no error | deny (`"no verdict"`) |
| Daemon alive, conn open, never resolves | **none** — blocks until signal or daemon action; knowingly out of scope since #1929, see § `internal/control.Approve` client helper |

## Tests

`cmd/pyry/mcp_approve_test.go` — a fake control-socket peer (`net.Listen("unix", ...)`) drives: MCP handshake (protocolVersion echo + fallback, `tools/list` shape), allow round-trip (byte-exact golden JSON), deny round-trip (byte-exact golden JSON), socket-unreachable default-deny, daemon-error default-deny, malformed/unknown-tool deny (no crash), and a no-byte-leak logging assertion. All stdlib `testing`, table-driven, `-race`-clean.

**Since #1929, the client-deadline table is gone and the daemon-wins guarantee is pinned directly against liveness instead of a duration.** `TestMCPApproveServer_ClientDeadline` (#1507) — which pinned `newMCPApproveServer`'s derived deadline across `PYRY_APPROVAL_TIMEOUT` values — was retired along with the field it tested (`approveServer` has no timeout to derive). Its only load-bearing numeric pin, the default `mcpApprovalTimeout` window, was never orphaned: `TestApprovalTimeout` (`cmd/pyry/approval_timeout_test.go`) asserts `approvalTimeout() != 10*time.Minute` directly in the file that owns the accessor (#1909). Its guard against a same-as-default override row is carried forward into the replacement, `TestMCPApprove_DaemonMessageWinsAtEveryWindow`: built through `newMCPApproveServer` (the production constructor), it varies `PYRY_APPROVAL_TIMEOUT` (unset, and two distinct overrides) against a peer that replies well past the shrunk window, and asserts the daemon's message wins byte-exactly on every row — content only, never latency, since there is no upper bound left to assert. `TestControlApprove_UndeadlinedCtxReturns` (which pinned the *old* `DialTimeout`-fallback contract) was replaced rather than weakened: `TestControlApprove_PatientPastDialTimeout` proves an undeadlined `Approve` now outlives `control.DialTimeout` and still returns the daemon's verdict, and `TestControlApprove_CtxCancelUnblocks` / `TestMCPApprove_SignalCancelMidApproval_Deny` pin the `context.AfterFunc` watcher's cancellation path at both the `control` and `toolsCall` layers. `TestMCPApprove_ConnEndsWithoutVerdict_Deny` and the still-present `TestMCPApprove_SocketUnreachable_Deny` cover the two conn-ended paths in the table above.

**Confirming RED against the pre-#1929 tree looks like a hang, not a failure, and that is expected.** The two `control` tests and the signal-cancel test are red on the old tree by *parking* rather than by an assertion failing — the signal test for the old default ten-minute-plus-margin bound — so a bare `go test -run …` against pre-change code hits its own test timeout instead of printing a mismatch. When the defect under test is "the bound is too long," that shape (a timeout, not a red assertion) is the correct RED, not a broken test.

## Out of scope (deferred)

- The in-process modal-resolve consumer producing the trusted allow — **#1080**.

Resolved since the sections above were written: wiring `permissionArgs`/`writeMCPApproveConfig` (#1106) into a live `streamsup` spawn — the source of the `yolo` boolean at a live spawn and the per-spawn config-file removal lifecycle — landed in [#1168](../codebase/1168.md). The client-side deadline coupling to `approvalTimeout()` (#1507) was removed by #1929 (see § `internal/control.Approve` client helper) — a precondition for #1912, which wants the daemon to hold an approval past its current window and could not while this client was the shorter bound.

## Related

- [control-plane.md](control-plane.md) § Approve — the daemon-side `mcp.approve` verb this subcommand's client calls.
- [permbridge-package.md](permbridge-package.md) — the pending-approval registry underneath.
- [acp-package.md](acp-package.md) — the transport (`acp.Transport`/`serveACP`) this subcommand reuses for a second, non-ACP JSON-RPC dialect.
- [codebase/1105.md](../codebase/1105.md) — ticket implementation notes, patterns established, lessons learned.
- [codebase/1106.md](../codebase/1106.md) — the spawn-arg injection (`permissionArgs`) and mcp-config generation (`renderMCPApproveConfig`/`writeMCPApproveConfig`) that point claude at this server.
- [codebase/1168.md](../codebase/1168.md) — the live wiring of both #1106 primitives onto the interactive `streamsup` spawn.
