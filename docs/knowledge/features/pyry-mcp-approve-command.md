# `pyry mcp-approve` — MCP stdio approve tool, forwarding over the control socket

The tool host for the Streamrunner Interactive permission bridge (T1 spike #1075). A non-YOLO headless claude spawned with `--permission-prompt-tool mcp__pyry_approve__approve` synchronously calls the registered `approve` MCP tool for every non-allowlisted tool use and blocks on its allow/deny JSON before running the tool. `pyry mcp-approve` is that MCP server: a short-lived stdio subprocess claude spawns (like `pyry status`/`pyry rekey`/`pyry acp`), which dials the daemon's control unix socket, forwards each approval request via the `mcp.approve` verb ([`control-plane.md` § Approve](control-plane.md#approve-mcpapprove-verb--forward-to-permbridge-block-default-deny-1104), #1104), and returns the daemon's verdict as the MCP tool result.

The sibling #1106 generates the mcp-config that registers this server as `pyry_approve` with tool `approve` and points claude's `--permission-prompt-tool` at it — that spawn-arg wiring is out of scope here. This subcommand ships the server the config names.

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
4. `cctx, cancel := context.WithTimeout(ctx, mcpApprovalTimeout + mcpApproveClientMargin)`; `control.Approve(cctx, socketPath, payload)`.
5. Map the outcome:
   - error (socket unreachable, daemon `Response.Error`, client read-deadline) → `deny("approval unavailable")`
   - `res == nil` (belt-and-suspenders; `control.Approve` already errors on this) → `deny("no verdict")`
   - else → marshal `res` verbatim as the verdict text (both allow and daemon-deny pass through here, `updatedInput` byte-preserved)
6. Wrap as `{content:[{type:"text", text:<verdict>}], isError:false}`.
7. Log `tool_use_id` + `behavior` only — never `input`, `tool_name`, or the raw `params`/`arguments` bytes, on any branch including the early parse-failure denies. Logger writes to **stderr only**; stdout is exclusively the JSON-RPC frame stream.

`mcpApproveClientMargin = 30s` — added to `mcpApprovalTimeout` (2 min, #1104) so the daemon's own approval timer fires first (its informative timeout-deny message passes through) rather than the client's generic read-deadline error. Both outcomes are denies; the margin only changes which message reaches claude. Inert until #1106 wires `--permission-prompt-tool` in production.

## `internal/control.Approve` client helper

`Approve(ctx, socketPath, req ApprovePayload) (*ApproveResult, error)` in `internal/control/client.go` — mirrors `Rekey`. Sends `Request{Verb: VerbMCPApprove, Approve: &req}` through the shared `request` helper. **Callers must pass a ctx with a deadline ≥ the daemon's approval window**: `request` installs that deadline as the conn read deadline, keeping the read patient while the daemon holds the conn open for the human decision; an undeadlined ctx falls back to `DialTimeout` (5s) and would prematurely deny a live approval. The dial itself still fails fast (≤`dialRetryBudget`≈1.5s) on an unreachable socket even under a long-deadline ctx, so a missing daemon yields a bounded error, not a hang. No `ErrorCode` mapping — any error is returned verbatim since the caller fail-closes to deny regardless of cause.

## Framing integrity under adversarial `input`

The verdict JSON is embedded as a *string* inside `content[].text`, and the whole tool result is emitted by one outer `json.Marshal` — so a control byte (e.g. a literal newline) inside an echoed `updatedInput` is escaped in the outer string, keeping the MCP stdout frame a single physical line. `input` is validated as well-formed JSON at the `arguments` unmarshal step; a malformed value denies there rather than producing a partial frame. On the daemon side, the control-socket response is read with a whitespace-agnostic streaming `json.Decoder`, so `Input`'s internal whitespace round-trips regardless.

## Concurrency

`acp.Transport.Serve` runs one read-loop goroutine dispatching inline; a blocking `tools/call` blocks that loop for up to the timeout window — safe here because claude's `--permission-prompt-tool` path calls `approve` synchronously and blocks on the result before emitting any further MCP frame (at most one in-flight approval per process), and the handler issues no outbound `Transport.Call` (no read-loop deadlock risk). `approveServer` is read-only after construction — no locks, no shared mutable state. A mid-approval SIGTERM cancels `cctx` (which derives from the `Serve` ctx) → the control-socket read unblocks → deny → clean exit.

## Error → verdict table

| Path | Tool result (`isError` always `false`) |
|---|---|
| Daemon resolver allows (#1080) | **allow** — passed through verbatim |
| Daemon denies (resolver / timeout / disconnect / shutdown / dup-id) | **deny** — daemon's message passed through |
| Control socket unreachable | deny (`"approval unavailable"`) |
| Daemon `Response.Error` (nil registry / missing id) | deny (`"approval unavailable"`) |
| Client read deadline (daemon wedged past timeout) | deny (`"approval unavailable"`) |
| `params`/`arguments` malformed JSON | deny (`"malformed approval request"`) |
| `name != "approve"` | deny (`"unknown tool"`) |
| `res == nil` on no error | deny (`"no verdict"`) |

## Tests

`cmd/pyry/mcp_approve_test.go` — a fake control-socket peer (`net.Listen("unix", ...)`) drives: MCP handshake (protocolVersion echo + fallback, `tools/list` shape), allow round-trip (byte-exact golden JSON), deny round-trip (byte-exact golden JSON), socket-unreachable default-deny (fast-fail, well under the timeout), daemon-error default-deny, malformed/unknown-tool deny (no crash), a no-byte-leak logging assertion, and an undeadlined-ctx no-hang property test for `control.Approve`. All stdlib `testing`, table-driven, `-race`-clean.

## Out of scope (deferred)

- mcp-config generation + `--permission-prompt-tool`/spawn-arg injection — sibling **#1106**.
- The daemon-side verb, wire types, registry wiring — **#1104** (merged).
- The in-process modal-resolve consumer producing the trusted allow — **#1080**.

## Related

- [control-plane.md](control-plane.md) § Approve — the daemon-side `mcp.approve` verb this subcommand's client calls.
- [permbridge-package.md](permbridge-package.md) — the pending-approval registry underneath.
- [acp-package.md](acp-package.md) — the transport (`acp.Transport`/`serveACP`) this subcommand reuses for a second, non-ACP JSON-RPC dialect.
- [codebase/1105.md](../codebase/1105.md) — ticket implementation notes, patterns established, lessons learned.
