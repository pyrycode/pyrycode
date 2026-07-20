# Spec: `pyry mcp-approve` — MCP stdio approve server forwarding over the control socket (#1105)

## Files to read first

- `internal/control/client.go:260-303` — `Rekey` free-function client helper + shared `request(ctx, socketPath, Request) (*Response, error)`. **Extract:** the exact client-helper shape `Approve` mirrors, and `request`'s deadline rule — it sets `conn.SetDeadline(ctx.Deadline())` when the ctx has one (else `now+DialTimeout=5s`). This is the *patient-read* seam: pass a long-deadline ctx and the read waits for the human window.
- `internal/control/dial.go:20-95` — `dial` / `dialWithRetry`. **Extract:** the retry loop is bounded by `dialRetryBudget = 1500ms`, **not** by the ctx deadline. So a long-deadline ctx still fails *fast* (≤1.5s) on an unreachable socket (ENOENT/ECONNREFUSED). This is what makes reusing `request` correct — patient read, fast-fail dial, no hang.
- `internal/control/protocol.go:232-263` — `ApprovePayload` (snake_case wire: `tool_name`/`input`/`tool_use_id`) + `ApproveResult` (byte-identical to `permbridge.Verdict`: `behavior`/`updatedInput`/`message`). **Extract:** the exact types `Approve` sends and returns; no new wire types are needed (all landed in #1104).
- `internal/control/server.go:865-908` — `handleApprove` (daemon side, **already merged**, #1104). **Extract:** the blocking-wait + fail-closed contract this subcommand pairs with; the daemon returns `Response.Error` for "unavailable"/"missing id" (not a baked deny) — baking the deny is *this* subcommand's job (cross-ticket contract).
- `cmd/pyry/acp.go:302-364` — `serveACP(ctx, stdin, stdout, logger, register func(*acp.Transport)) error`. **Extract:** this is a **protocol-neutral** stdio JSON-RPC serve loop — io.Pipe stdin bridge, prompt shutdown on signal, EOF→clean exit. `runMCPApprove` reuses it verbatim, passing an MCP `register`. Do **not** re-derive the parked-Read-on-non-pollable-stdin machinery (docs/lessons.md #78).
- `cmd/pyry/acp.go:28-81` — `runACP`. **Extract:** the subcommand skeleton `runMCPApprove` mirrors — `signal.NotifyContext(…, os.Interrupt, SIGTERM)`, a **stderr** slog logger (diagnostics MUST stay off stdout; the JSON-RPC frame stream owns stdout), then serve.
- `cmd/pyry/acp_handshake.go:78-110` — `initializeHandler` + `registerHandshake`. **Extract:** the `acp.Handler` idiom — unexported result structs with json tags, a decode-as-shape-gate for `initialize` params, `t.Register(method, handler)`.
- `internal/acp/acp.go:53-129, 221-276` — `acp.Handler` contract, `Register`, `dispatchRequest`, `dispatchNotification`. **Extract:** (a) the `Transport` is a generic JSON-RPC 2.0 dispatch table — reused for MCP as-is; (b) handlers dispatch **inline on the single read-loop goroutine**, so a blocking `tools/call` blocks the loop — safe here (claude serializes permission calls); (c) **unregistered notifications are dropped silently** (Debug log, no frame) — so `notifications/initialized` needs no handler.
- `cmd/pyry/main.go:1296-1306` — `parseClientFlags(name, args) (socketPath, rest, err)`. **Extract:** resolves the control socket from `-pyry-socket`/`-pyry-name`; returns leftover positionals in `rest`. This is the seam the sibling #1106's generated mcp-config points at (`pyry mcp-approve -pyry-socket=<daemon.sock>`).
- `cmd/pyry/main.go:1172-1181` + `:209-244` + `:2034` (`printHelp`) — `mcpApprovalTimeout` const (2 min), the `run()` subcommand switch, and the help block. **Extract:** the const the client-side ctx deadline derives from; where the `case "mcp-approve":` line and one help line land.
- `cmd/pyry/rekey.go:79-112` — `runRekey`. **Extract:** the "subcommand dials a control verb via a `control.*` helper" template. **Contrast:** `runRekey` `os.Exit`s on reject; `mcp-approve` must **never** exit-on-reject — it fail-closes to a *deny tool result* so claude's turn continues.
- `internal/permbridge/permbridge.go:30-73` — `BehaviorAllow`/`BehaviorDeny` consts, `Request`, `Verdict`, `Allow`/`Deny`. **Extract:** the canonical verdict shape + the `"deny"` string constant the subcommand reuses to synthesize its fail-closed deny (permbridge is already imported by `cmd/pyry` — see `main.go:58`).
- Ticket body § "Approval contract (T1 spike)" — the golden request/response JSON shapes. No `fixture-p4-approval-contract.json` exists on disk; the ticket body is the source of truth. MCP wire shapes (initialize/tools/list/tools/call) are documented inline in § Design below (codegraph/QMD won't know an external protocol).

## Context

Part of the Streamrunner interactive permission bridge (the #1079 M→4×S split). #1103 landed `internal/permbridge` (the fail-closed pending-approval registry); #1104 landed the daemon-side control-socket `mcp.approve` verb (`handleApprove` + `ApprovePayload`/`ApproveResult` wire types + `SetApprovalRegistry` wiring). Both are merged.

This ticket delivers the **tool host** that claude actually talks to. A non-YOLO headless claude spawned with `--permission-prompt-tool mcp__pyry_approve__approve` synchronously calls that MCP tool for every non-allowlisted tool use and **blocks on its allow/deny JSON** before running the tool. `pyry mcp-approve` is that MCP server: a short-lived stdio subprocess claude spawns, exactly like the other short-lived `pyry` subcommands that reach the daemon (`pyry status`, `pyry sessions new`, `pyry rekey`) — it dials the **control unix socket**, forwards each approval request via the merged `mcp.approve` verb, and returns the daemon's verdict as the tool result claude expects.

The daemon owns the human-facing decision (modal surfacing, #1080). This subcommand is a **pure forwarder + fail-closed adapter**: it re-frames claude's MCP `tools/call` into a control-socket request and re-frames the control-socket verdict into an MCP tool result. The `input` it forwards is **model-controlled and potentially adversarial** — it is carried as opaque `json.RawMessage` and never parsed, executed, or dispatched on, on either side.

The sibling #1106 (spawn-arg injection) generates the mcp-config that registers this server as `pyry_approve` with tool `approve` and points claude at `mcp__pyry_approve__approve`. That config generation is out of scope here; this ticket ships the server the config names.

## Design

### Components

Three additive pieces, no consumer cascade:

1. **`control.Approve` client helper** (`internal/control/client.go`, ~25 lines) — mirrors `Rekey`.
2. **`pyry mcp-approve` subcommand** (`cmd/pyry/mcp_approve.go`, new file) — the MCP stdio server.
3. **Dispatch + help** (`cmd/pyry/main.go`, 2 lines) — `case "mcp-approve": return runMCPApprove(os.Args[2:])` in `run()`, plus one `printHelp` line.

`cmd/pyry` already imports `internal/control`, `internal/acp`, and `internal/permbridge` — no new module deps.

### 1. `control.Approve` helper (`internal/control/client.go`)

Contract (signature + behavior; body mirrors `Rekey` + the shared `request`):

```
func Approve(ctx context.Context, socketPath string, req ApprovePayload) (*ApproveResult, error)
```

- Sends `Request{Verb: VerbMCPApprove, Approve: &req}` through the existing `request(ctx, socketPath, …)`.
- `err != nil` → return it (dial/transport/decode). Dial fails fast (≤`dialRetryBudget`=1.5s) on an unreachable socket even under a long-deadline ctx (see dial.go).
- `resp.Error != ""` → return `errors.New(resp.Error)`. **No `ErrorCode` mapping** — the daemon returns plain `Response.Error` for "no approval registry configured" / "missing tool_use_id", and the caller fail-closes to deny on *any* error, so a typed sentinel would be dead code.
- `resp.Approve == nil` → return `errors.New("control: empty mcp.approve response")`.
- else → return `resp.Approve`.

Doc-comment MUST state: **callers pass a ctx with a deadline ≥ the daemon's approval window** — `request` uses that deadline as the conn read deadline, keeping the read patient while the daemon holds the conn open for the human decision. An undeadlined ctx falls back to `DialTimeout` (5s) and would prematurely time out a live approval (a *safe but premature* deny). This is the sole behavioral difference from the other client helpers (which are sub-second round-trips).

### 2. `pyry mcp-approve` subcommand (`cmd/pyry/mcp_approve.go`)

**Entry point** `runMCPApprove(args []string) error` — mirrors `runACP`:

- `socketPath, rest, err := parseClientFlags("pyry mcp-approve", args)`; reject non-empty `rest` (no positionals).
- `ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`.
- stderr slog logger (Info level) — **never** log to stdout.
- Build the server value `s := &approveServer{socketPath, timeout: mcpApprovalTimeout + mcpApproveClientMargin, log}`.
- `return serveACP(ctx, os.Stdin, os.Stdout, logger, s.register)` — reuse the neutral serve loop. (The `"acp:"` error prefix `serveACP` adds on a genuine stream break is a stderr-only cosmetic; acceptable. Do not fork the function.)

`mcpApproveClientMargin` — a new `cmd/pyry` const (propose **30s**). The client read deadline = `mcpApprovalTimeout + margin` so the **daemon's own timer** fires first (its informative `"approval request timed out"` deny passes through) rather than the client read deadline (a generic transport error → the subcommand's own deny). Both are denies; the margin just prefers the daemon's message.

**MCP is JSON-RPC 2.0, one object per line over stdio** — the exact framing `acp.Transport` already implements (`bufio.Scanner` line-read + `json.Encoder` with `SetEscapeHTML(false)`, trailing `\n`, no embedded newlines). The transport is protocol-neutral; MCP method names + result shapes are the only new surface. `s.register(t *acp.Transport)` binds three request handlers; notifications need none:

```
t.Register("initialize",  s.initialize)
t.Register("tools/list",  s.toolsList)
t.Register("tools/call",  s.toolsCall)
// notifications/initialized (and any other notification) are dropped
// safely by dispatchNotification — no handler, no response frame.
```

**MCP wire contract** (result structs are unexported `cmd/pyry` types with json tags — zero new exported types, matching `newSessionResult` in acp.go):

- **`initialize`** → result:
  ```
  {"protocolVersion": <version>,
   "capabilities": {"tools": {}},
   "serverInfo": {"name": "pyry_approve", "version": Version}}
  ```
  `<version>`: **echo** the client's `params.protocolVersion` when present and non-empty; fall back to `defaultMCPProtocolVersion = "2025-06-18"` when absent. Our surface (initialize/tools/list/tools/call) is version-invariant across all MCP revisions, so echoing the client's requested version guarantees it accepts (per the MCP lifecycle rule "if the server supports the requested version it responds with the same version"). Non-object `params` → `acp.NewError(acp.CodeInvalidParams, …)` (shape gate only, like `initializeHandler`); absent/empty params tolerated.
- **`tools/list`** → result `{"tools": [approveTool]}` where:
  ```
  approveTool = {"name": "approve",
                 "description": "<one line: gate a claude tool use; returns allow/deny>",
                 "inputSchema": {"type":"object",
                                 "properties":{"tool_name":{"type":"string"},
                                               "input":{"type":"object"},
                                               "tool_use_id":{"type":"string"}}}}
  ```
  The tool **name `"approve"` is load-bearing** — it forms `mcp__pyry_approve__approve`. `inputSchema` is advisory (claude's permission path fills the fixed `{tool_name,input,tool_use_id}` shape regardless).
- **`tools/call`** → params `{"name": string, "arguments": json.RawMessage}` → result:
  ```
  {"content": [{"type": "text", "text": "<verdict JSON>"}], "isError": false}
  ```
  where `<verdict JSON>` is `{"behavior":"allow","updatedInput":{…}}` or `{"behavior":"deny","message":"…"}` (the T1 contract, byte-identical to `control.ApproveResult`/`permbridge.Verdict`).

### 3. `tools/call approve` handler — the fail-closed core

**Invariant (security-critical):** the handler **always** returns a well-formed MCP tool result with `isError:false` whose first text block is a verdict JSON — never a JSON-RPC error, never a hang. The **only** verdict the subcommand self-originates is **deny**; **allow passes through only from the daemon's `Response.Approve`**. The subcommand has no code path that constructs an allow. (Mirrors the daemon's fail-closed and the permbridge package invariant.)

Contract (steps; each failure → a deny result, `isError:false`):

1. Unmarshal `params` → `{name string, arguments json.RawMessage}`. Failure → `denyResult("malformed approval request")`. *(Deliberately a deny result, NOT `CodeInvalidParams` — a JSON-RPC error on the permission path risks hanging/confusing claude's turn. Fail-closed = deny.)*
2. `name != "approve"` → `denyResult("unknown tool")`. (This server advertises only `approve`; its sole caller is the permission path — uniform deny keeps claude unblocked.)
3. Unmarshal `arguments` → `control.ApprovePayload{tool_name, input, tool_use_id}`. `input` stays `json.RawMessage` — **never parsed or dispatched on**. Failure → `denyResult("malformed approval request")`.
4. `cctx, cancel := context.WithTimeout(ctx, s.timeout); defer cancel()` — `ctx` is the Serve ctx, so `cctx` carries both the per-call deadline **and** signal cancellation. `res, err := control.Approve(cctx, s.socketPath, payload)`.
5. Map to a verdict:
   - `err != nil` → `denyResult("approval unavailable")`. *(Covers the AC's socket-unreachable case AND the daemon's "no approval registry" / "missing tool_use_id" errors AND a client-deadline timeout.)*
   - `res == nil` → `denyResult("no verdict")`. *(Belt-and-suspenders; `control.Approve` already errors on nil.)*
   - else → marshal `res` (`*control.ApproveResult`) verbatim → the verdict text (allow **and** daemon-deny both pass through here, `updatedInput` byte-preserved).
6. Wrap the verdict JSON as `{content:[{type:"text", text:<verdict>}], isError:false}`.
7. Content-free log: `s.log.Info("mcp-approve: verdict", "tool_use_id", payload.ToolUseID, "behavior", <behavior>)`.

**Logging discipline (ALL branches, not just step 7).** `input`, `tool_name`, and the raw `params`/`arguments` bytes are **MUST-NOT-log** on *every* path — including the early fail-closed deny branches (steps 1–3). A diagnostic like `log.Warn("malformed", "params", string(params))` would leak adversarial, possibly secret-bearing model input into stderr; the fail-closed branches log at most a **fixed reason string** (and `tool_use_id` when already parsed), never the offending bytes. This is the exact inverse of the decode-handler-leak hazard (a sec handler copied from a non-sec template that logs the bad input): here the bytes are model-controlled and untrusted, so they never enter a log field. The logger writes to **stderr only** — stdout is exclusively the JSON-RPC frame stream (a stray stdout write corrupts the MCP stream).

Helper `denyResult(reason string)` builds the tool result end-to-end: marshal `control.ApproveResult{Behavior: permbridge.BehaviorDeny, Message: reason}` → put its JSON string in the first text block → `isError:false`. Reusing `permbridge.BehaviorDeny` (already imported) keeps the `"deny"` string canonical across the whole chain. All deny reasons are **fixed constants** — no host/`input`-derived content ever enters a verdict message.

**Framing integrity under adversarial `input`.** The verdict JSON is embedded as a *string* in `content[].text`, and the whole tool result is emitted by one outer `json.Marshal` — so any control byte (a literal `\n`) inside the echoed `updatedInput` is escaped to `\n` in the outer string, keeping the MCP stdout frame a single physical line (no forged-frame injection). In the forward direction, the daemon reads the control socket with a streaming `json.Decoder` (whitespace-agnostic, not line-delimited), so a well-formed `Input` `json.RawMessage` round-trips regardless of internal whitespace. `Input` is validated as well-formed JSON at the `arguments` unmarshal (step 3) — a malformed value fails there → deny, never a partial frame.

**`isError` is always `false`, even for internal-error denies.** A deny verdict is a *successful* tool result claude must honor; `isError:true` risks claude discarding the content and mishandling the turn.

## Concurrency model

- One `acp.Transport.Serve` loop on stdin (single read-loop goroutine); handlers dispatch **inline**. A blocking `tools/call` blocks the read loop for up to `s.timeout` — **safe**: claude's `--permission-prompt-tool` path calls `approve` synchronously and blocks on the result before emitting any further MCP frame, so there is no concurrent MCP traffic to starve. The handler issues no outbound `Transport.Call`, so it cannot deadlock the read loop.
- `serveACP` bridges real stdin through an `io.Pipe` so a SIGTERM/SIGINT unblocks a parked read promptly and an EOF (claude closes the pipe) exits cleanly — reused, not re-derived.
- Per `tools/call`, `control.Approve` opens exactly one control conn (dial → write → read → close, via `request`'s `defer conn.Close()`). `cctx` propagates signal cancellation: a mid-approval SIGTERM cancels `cctx` → the conn read returns → the handler returns a deny result → `Serve` returns. No goroutine outlives the process.
- The `approveServer` value is read-only after construction (`socketPath`, `timeout`, `log`) — no shared mutable state, no locks.

## Error handling & fail-closed

| Path | Tool result (`isError` always `false`) |
|---|---|
| Daemon resolver allows (#1080) | **allow** — passed through verbatim (`updatedInput` byte-preserved) |
| Daemon denies (resolver / timeout / disconnect / shutdown / dup-id) | **deny** — daemon's message passed through |
| Control socket unreachable (dial ENOENT/ECONNREFUSED, ≤1.5s) | deny (`"approval unavailable"`) |
| Daemon `Response.Error` (nil registry / missing id) | deny (`"approval unavailable"`) |
| Client read deadline (daemon wedged past `timeout`) | deny (`"approval unavailable"`) |
| `params` / `arguments` malformed JSON | deny (`"malformed approval request"`) |
| `name != "approve"` | deny (`"unknown tool"`) |
| `res == nil` on no error | deny (`"no verdict"`) |

Every non-allow terminus is a deny result → claude honors it and the turn proceeds; it never hangs. Allow is unreachable without the daemon's trusted in-process resolver.

## Testing strategy

New `cmd/pyry/mcp_approve_test.go` (package main, stdlib `testing`, `-race`). A **fake control-socket peer** helper: `net.Listen("unix", filepath.Join(t.TempDir(), "c.sock"))` with a goroutine that `json.Decode`s one `control.Request` and `json.Encode`s a canned `control.Response` (or closes the conn / never listens for the unreachable case). Drive handlers either directly (`s.toolsCall(ctx, rawParams)`) or through a `acp.Transport` over `io.Pipe`s for the handshake. Write scenarios in the project's table-driven idiom; compare `json.RawMessage` via `json.Compact`/byte-equality, never struct `==`.

- **MCP handshake** — feed `initialize` (with and without a client `protocolVersion`) through a transport-over-pipes; assert the response echoes the client version when sent, falls back to `"2025-06-18"` when absent, and carries `capabilities.tools` + `serverInfo.name=="pyry_approve"`. Feed `tools/list`; assert exactly one tool named `"approve"`.
- **allow round-trip** — fake peer replies `Response{Approve:{Behavior:"allow", UpdatedInput:<input>}}`; call `tools/call` with `arguments={tool_name,input,tool_use_id}`; assert the first text block **byte-equals** `{"behavior":"allow","updatedInput":<input>}` and `isError:false`. (AC-4 golden-shape assertion.)
- **deny round-trip** — fake peer replies `Response{Approve:{Behavior:"deny", Message:"nope"}}`; assert first text block byte-equals `{"behavior":"deny","message":"nope"}`, no `updatedInput` key, `isError:false`. (AC-4 golden-shape assertion.)
- **socket-unreachable default-deny** — `socketPath` = a nonexistent path (no listener); assert `tools/call` returns a deny result **well under `timeout`** (proves fast-fail, no hang) with `isError:false`. (AC-3.)
- **daemon-error default-deny** — fake peer replies `Response{Error:"mcp.approve: no approval registry configured"}`; assert deny result (fail-closed on control error).
- **malformed arguments → deny, no crash** — `arguments` is a JSON array / wrong shape; assert deny result, no panic. (Ticket: "malformed input must fail closed, never crash".)
- **`control.Approve` deadline note** *(optional, cheap)* — assert that `control.Approve` with an undeadlined `context.Background()` still returns (falls back to `DialTimeout`) rather than blocking indefinitely against a fake peer that never replies — documents the deadline requirement.

The T1 golden strings are in the ticket body; restate them as expected literals in the table so the author needn't hunt.

## Open questions

- **`protocolVersion`: echo-with-fallback (recommended) vs hard-pin.** Echo maximizes compatibility with whatever claude version the user runs; fallback `"2025-06-18"` covers absent params. Implement echo-with-fallback unless a real-claude smoke (a #1106-era e2e) shows claude rejects an echoed version — then hard-pin.
- **Inline-blocking `tools/call` vs deferred (`ErrDeferred` + `Responder`).** Inline chosen (simpler; claude serializes permission calls, so the read loop is never starved). Revisit with the deferred-response pattern only if a future flow interleaves other MCP traffic during an in-flight approval.
- **`mcpApproveClientMargin` value (proposed 30s).** Inert until #1106 wires `--permission-prompt-tool` in production; tune when the full chain runs against real claude.
- **`isError:false` for internal-error denies.** Chosen so claude always honors the deny content. Flag if a real-claude run shows claude treats a false-`isError` deny differently than expected.

## Scope

**In:** the `control.Approve` client helper; the `pyry mcp-approve` subcommand (MCP handshake + `tools/list` + `tools/call` fail-closed forwarding) reusing `acp.Transport`/`serveACP`; the `run()` dispatch case + one help line; the tests above.

**Out:** the mcp-config generation + `--permission-prompt-tool mcp__pyry_approve__approve` / `--dangerously-skip-permissions` spawn-arg injection (sibling **#1106**); the daemon-side verb, wire types, and registry wiring (**#1104**, merged); the in-process modal-resolve consumer that produces the trusted `Allow` (**#1080**).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. One explicit untrusted→trusted boundary: claude's MCP `tools/call` stdin frame → the `toolsCall` handler. `input` is **model-controlled and adversarial**; it is carried as `json.RawMessage` and **never parsed, dispatched on, or interpreted** — only forwarded to the daemon and echoed back to its originator. The type (`json.RawMessage`, never a decoded struct) is the standing signal that it stays opaque. The daemon's control-socket response is trusted (0600 owner-only socket, same-user threat model inherited from `server.go`). The security-critical property — **an adversarial payload can never forge an allow** — holds structurally: the subcommand self-originates **only** deny (`denyResult`); `allow` reaches claude solely by passing through `resp.Approve`, which the daemon produces only via a trusted in-process `Resolve(Allow)` (#1080, #1104's verified fail-closed). `tool_use_id` is a correlation key, not a credential; an empty/duplicate one is rejected by the daemon's guard → `Response.Error` → subcommand deny.
- **[Tokens, secrets, credentials]** N/A. The subcommand mints and handles no token. The control socket's sole auth is the inherited `0600` mode (daemon-side). Should `input` ever carry secret-bearing model output, it is never logged (see [Logs]) and flows only to the trusted daemon and back to claude (the originator) — no new exposure.
- **[File operations]** N/A. No filesystem path is derived from the model-controlled payload; `socketPath` comes from `-pyry-socket`/`-pyry-name` (operator/#1106-config-controlled, same-user trust). The subcommand opens/creates/writes no file — it dials a socket and reads/writes stdio. No path traversal, TOCTOU, symlink, or atomic-write surface.
- **[Subprocess execution]** N/A. The subcommand execs nothing; `input` reaches no command. (claude may later run an *allowed* tool — but that is claude's own sandbox, downstream of the daemon's trusted resolver, not this forwarder.)
- **[Cryptographic primitives]** N/A. No RNG, keys, or secret comparison. `tool_use_id` matching (daemon-side) is a plain map lookup on a correlation key; constant-time comparison is not warranted.
- **[Network & I/O]** SHOULD FIX (inherited, not gating) + framing analyzed-safe. (a) Inbound stdin is capped at `acp`'s `maxLineBytes = 16 MiB` per line (over-long → `bufio.ErrTooLong` → clean exit), bounding a single request; there is no per-`input` size guard, so a 16 MiB payload forwards to the daemon's uncapped registry — the **same** inherited resource note #1104 flagged (bounded by the `0600` same-user model; a boundary size guard would cap a flood if ever observed; not gated per Evidence-Based Fix Selection). The subcommand does not amplify it (one line → one forward). (b) **Timeout discipline is sound**: the outbound round-trip's read deadline is `mcpApprovalTimeout + margin` and the dial fails fast (≤1.5s), so a wedged/absent daemon yields a bounded deny, never a hang. (c) **Framing integrity under adversarial `input`** analyzed and safe — see § "Framing integrity" (double-marshal escapes control bytes; daemon uses a whitespace-agnostic streaming `json.Decoder`; `Input` is validated well-formed at step 3).
- **[Error messages, logs, telemetry]** No MUST FIX (SHOULD FIX addressed inline). All deny `message`s returned to claude are **fixed constants** — no `input`/`tool_name` echo. Logging discipline is now explicit on **all** branches (including early fail-closed returns): raw `params`/`arguments`/`input` are MUST-NOT-log; the decision log emits only `tool_use_id` + `behavior`. This directly forecloses the [[architect-security-decode-handler-drops-err-in-malformed-log]] hazard (a sec handler copied from a non-sec template that logs the offending bytes). Diagnostics go to **stderr only**; stdout is exclusively the JSON-RPC frame stream.
- **[Concurrency]** No findings. The `approveServer` value is read-only after construction — no shared mutable state, no locks, no TOCTOU. One `Serve` read loop; a blocking `tools/call` dispatches inline and holds the loop for a bounded window — safe because claude serializes permission calls (at most one in-flight approval per process) and the handler issues no outbound `Call` (no read-loop deadlock). All goroutines are reaped: `serveACP`'s pipe-closer/stdin-bridge (inherited, already-analyzed) and the control conn (closed by `request`'s defer; `cctx` cancellation unblocks the read on signal/deadline). Shutdown mid-approval cancels `cctx` → deny → clean exit; nothing is persisted, so no partial state.
- **[Threat model alignment]** CLI/control-plane ticket (not relay); no `docs/threat-model.md` exists — the de-facto model is the daemon's `0600` owner-only control socket. The core threat — an untrusted MCP payload must never yield an unauthorized allow — is addressed by fail-closed. The secondary threat — malformed/adversarial input must not crash — is addressed by the total (never-panic) `acp` classifier plus per-branch `json.Unmarshal`-error → deny. **OUT OF SCOPE:** cross-user socket exposure (containers / multi-tenant), which `server.go` already names as a future revisit — not introduced or widened here (same disposition as #1104).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-20
