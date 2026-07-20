# Spec: Control-socket `mcp.approve` verb — forward request → permbridge, block for verdict, default-deny on disconnect (#1104)

## Files to read first

- `internal/permbridge/permbridge.go:44-176` — the blocking prerequisite (#1103), now merged. `Register(id, Request, timeout) (*Pending, error)` / `Pending.Await() Verdict` / `Resolve(id, Verdict) bool` / `Lookup(id) (Request, bool)`; `Request{ToolName, Input json.RawMessage, ToolUseID}`; `Verdict{Behavior, UpdatedInput omitempty, Message omitempty}` + `Allow`/`Deny` constructors; `ErrDuplicateID`. This verb is the first consumer of the registry. **Extract:** the exact method contracts and the fail-closed one-shot (`Await` always returns within `timeout` because the registry-owned timer denies).
- `internal/control/server.go:135-164` — `Rekeyer` consumer-interface pattern (defined-at-consumer, standalone). **Extract:** the "optional dependency installed via a setter" shape this verb mirrors.
- `internal/control/server.go:292-306` — `SetRekeyer`: the between-`NewServer`-and-`Serve` install pattern (keeps `NewServer`'s signature frozen). **Extract:** the setter shape for `SetApprovalRegistry`.
- `internal/control/server.go:765-803` — `handleRekey`: the exact nil-dependency-replies-"unavailable" + guard-before-call + typed-error-mapping template this handler follows. **Extract:** guard ordering and the `Response{Error: "…"}` unavailable shape.
- `internal/control/server.go:485-540` — `handle`: the per-conn dispatch loop, the shared `dec.Decode(&req)` (malformed requests already answered here — do NOT add a custom decoder), and the `closeConn`/deferred-`conn.Close` lifecycle. **Extract:** where to add the `case VerbMCPApprove:` and how one-shot verbs own the conn (handle closes it on return).
- `internal/control/server.go:369-410, 414-452` — `Serve` + `Close`: `s.closedCh` is closed by `Close()` on shutdown, and `Serve` waits on `handleWG` before returning. **Extract:** `s.closedCh` is the in-handler shutdown signal (no ctx-threading needed); the handler MUST unblock on it so `handleWG.Wait()` does not stall for the approval timeout.
- `internal/control/server.go:829-955` — `handleAttach`: the precedent for a long-lived verb that `conn.SetDeadline(time.Time{})`-clears the handshake deadline before blocking, and for a per-conn watcher goroutine. **Extract:** deadline-clear before the block; watcher-goroutine reaping via conn close.
- `internal/control/protocol.go:16-94, 138-243` — `Verb` consts, `Request`/`Response` structs, the `RekeyPayload`/`SessionsNewResult` payload patterns and camelCase-tag convention. **Extract:** where `VerbMCPApprove`, `ApprovePayload`, `ApproveResult`, and the `Request.Approve`/`Response.Approve` fields land.
- `internal/control/client.go:245-300` — `Rekey` free-function client helper + shared `request(ctx, socketPath, Request) (*Response, error)`. **Extract:** the client-helper shape the *sibling* `pyry mcp-approve` ticket will add (NOT this ticket — see Scope).
- `internal/control/rekey_test.go:1-70` — `fakeRekeyer` + `startServerWithRekeyer` test-harness pattern. **Extract:** the `startServerWith…` helper shape for `startServerWithApprovalRegistry` (this verb uses a REAL `permbridge.New()`, not a fake — the registry is a stdlib leaf).
- `cmd/pyry/main.go:911-955` — the daemon composition root (`runSupervisor`): builds `relayWiring`, then `control.NewServer(...)`, then `ctrl.Serve(ctx)`. **Extract:** the single scope that sees both the relay composition and the control server — where the shared `permbridge.New()` is created (AC-4) and `SetApprovalRegistry` is called.
- `docs/knowledge/architecture/system-overview.md` § `internal/control/` + § "Refusal-to-wire-code mapping" (PROJECT-MEMORY convention) — control-plane layering and the sentinel-at-primitive / code-at-consumer rule.

## Context

Part of the Streamrunner interactive permission bridge (the #1079 M→4×S split; #1103 landed the registry). A non-YOLO headless claude spawned with `--permission-prompt-tool` blocks synchronously on an MCP tool's allow/deny JSON. The human-facing decision must be made **in the daemon** (where modal surfacing lives, #1080), not in the ephemeral MCP child.

The `pyry mcp-approve` subcommand (sibling ticket) is a short-lived process claude talks to over MCP stdio. Like every other short-lived `pyry` subcommand that reaches the running daemon (`pyry status`, `pyry sessions new`, **`pyry rekey <conn_id>`**), it dials the **control unix socket**. This ticket adds the **daemon side**: a control-socket verb that receives a forwarded approval request, registers it with `internal/permbridge`, blocks for the verdict, and returns it — defaulting to **deny** on caller disconnect, daemon shutdown, or timeout.

> **Note on the ticket's degradation model.** The ticket cites `request_debug_bundle` / `set_session_settings` as the "nil dependency replies unavailable" precedent. Those are **relay-plane phone frames** (phone → binary via the v2 Noise manager), not control-socket verbs — the wrong structural cousin for a local subprocess caller. The correct in-package model is **`handleRekey` / `Rekeyer` / `SetRekeyer`**, which is *also* a "nil dependency replies unavailable" verb and lives on the same control socket. This spec follows the Rekeyer template. The degradation semantics the ticket asked for are preserved exactly.

## Design

### Transport & layering

Control unix socket, `internal/control`. One JSON `Request` in, one JSON `Response` out, conn closed — the standard control-plane shape, except the handler **blocks** between decode and response for the approval verdict. `internal/control` gains a dependency on `internal/permbridge` (a stdlib-only leaf — no cycle, and permbridge's own package doc names "the control-socket verb" as an intended consumer). `internal/protocol` is untouched — this is control-plane, not mobile-WS.

### Wire types (`internal/control/protocol.go`)

Additive. Keep `protocol.go` internal-import-free except adding `encoding/json` (for `json.RawMessage`) — mirrors the `SessionInfo`-in-protocol.go rule so the sibling and any hand-written client consume the wire types without transitively importing `internal/permbridge`.

- `VerbMCPApprove Verb = "mcp.approve"` — dotted namespace like `sessions.*`.
- `ApprovePayload` — the forwarded request. Fields (mirror `permbridge.Request`, snake_case tags to match the T1 spike contract the sibling marshals from claude):
  - `ToolName string \`json:"tool_name"\``
  - `Input json.RawMessage \`json:"input"\``
  - `ToolUseID string \`json:"tool_use_id"\``
- `ApproveResult` — the verdict, **byte-identical to `permbridge.Verdict`'s wire shape** so the sibling can marshal it straight to claude as the MCP tool result:
  - `Behavior string \`json:"behavior"\``
  - `UpdatedInput json.RawMessage \`json:"updatedInput,omitempty"\`` (allow only)
  - `Message string \`json:"message,omitempty"\`` (deny only)
- `Request.Approve *ApprovePayload \`json:"approve,omitempty"\``
- `Response.Approve *ApproveResult \`json:"approve,omitempty"\``

`ApproveResult` deliberately duplicates `permbridge.Verdict`'s field/tag layout (same justification as `SessionInfo` vs `sessions.SessionInfo`). Document the mirror in a doc-comment so a future drift is caught.

### Handler seam (`internal/control/server.go`)

Optional dependency, installed Rekeyer-style so `NewServer`'s signature stays frozen:

- New `Server` fields: `approvals *permbridge.Registry` and `approvalTimeout time.Duration` (guarded by the existing `s.mu`, read once per request).
- `SetApprovalRegistry(reg *permbridge.Registry, timeout time.Duration)` — sets both under `s.mu`. Canonically called once between `NewServer` and `Serve`. Nil registry is the production state until this ticket's composition wires it (and stays nil in v1/foreground).
- Dispatch: `case VerbMCPApprove: s.handleApprove(conn, req.Approve)`.

`handleApprove(conn net.Conn, payload *ApprovePayload)` contract (guard order matters — mirrors `handleRekey`):

1. Snapshot `reg, timeout` under `s.mu`.
2. `reg == nil` → `Response{Error: "mcp.approve: no approval registry configured"}`; return. *(The unavailable degrade. The sibling maps any control error to a claude **deny** — see § Cross-ticket contract.)*
3. `payload == nil || payload.ToolUseID == ""` → `Response{Error: "mcp.approve: missing tool_use_id"}`; return. *(Missing-input at the boundary, like `handleRekey`'s empty-connID guard.)*
4. `pending, err := reg.Register(payload.ToolUseID, permbridge.Request{…}, timeout)`. On `err` (ErrDuplicateID / empty id) → **fail-closed**: `Response{Approve: <deny with a fixed message, no host content>}`; return.
5. `conn.SetDeadline(time.Time{})` — clear the 5s handshake deadline; the wait is bounded by permbridge's registry-owned timer, not the conn (mirrors `handleAttach`).
6. Start the disconnect/shutdown watcher (below).
7. `verdict := pending.Await()` — blocks; **guaranteed to return** within `timeout` (permbridge's timer) or sooner (resolver / disconnect / shutdown).
8. Stop the watcher (idempotent — see below).
9. `enc.Encode(Response{Approve: verdictToResult(verdict)})` — best-effort; a dead conn's write error is ignored.
10. Content-free decision log: `s.log.Info("control: approval resolved", "tool_use_id", payload.ToolUseID, "behavior", verdict.Behavior)`. **Never** log `Input` or `ToolName`.

`verdictToResult` maps `permbridge.Verdict` → `ApproveResult` (three fields, straight copy). A symmetric `denyResult(msg)` builds an `ApproveResult` for the fail-closed branches.

## Concurrency model

Two short-lived goroutines per in-flight approval, both provably reaped. `Pending.Await()` has no ctx by design (#1103) — the consumer maps its cancellation sources (**disconnect**, **shutdown**) into `reg.Resolve(id, Deny(...))`, then always blocks on `Await`, which is guaranteed to return.

**Watcher** (`watchApproveConn`), started in step 6, stopped in step 8 via a `stop chan struct{}` the handler closes:

- Inner reader goroutine: `conn.Read(oneByte)` — the client sends nothing after its request, so this blocks until **EOF/error (disconnect)** or `handle` closes the conn on return. On return it closes a `readCh`.
- Select:
  - `<-stop` → verdict already landed (handler called stop after `Await` returned); return without resolving.
  - `<-s.closedCh` → **daemon shutdown**; `reg.Resolve(id, Deny(<fixed shutdown reason>))`.
  - `<-readCh` → **caller disconnected**; `reg.Resolve(id, Deny(<fixed disconnect reason>))`.

Reaping:
- On the **normal path** (resolver/timeout resolves the entry → `Await` returns): handler closes `stop`; the watcher selects `<-stop` and returns. The inner reader goroutine stays parked on `conn.Read` until `handle`'s deferred `conn.Close()` (after `handleApprove` returns) unblocks it — bounded, same shape as `handleAttach`'s detach-watcher.
- On **disconnect**: inner reader returns → `readCh` → watcher `Resolve`s deny → `Await` returns → handler closes `stop` (watcher already past select — harmless) → Encode to dead conn (ignored) → return. Entry deleted by the winning `Resolve`; permbridge's one-shot makes a late resolver/timer a no-op.
- On **shutdown**: `s.closedCh` closes → watcher `Resolve`s deny → `Await` returns → handler completes → `handleWG.Wait()` in `Serve` proceeds promptly (does NOT stall for `approvalTimeout`).

`s.closedCh` is created in `NewServer` (never nil) and reading a closed channel broadcasts to all concurrent watchers — safe. Concurrent `conn.Read` (watcher) and `conn.Write` (handler Encode) on one `net.Conn` are safe per the stdlib contract.

## Error handling & fail-closed

`allow` is reachable **only** through an explicit `reg.Resolve(id, permbridge.Allow(...))` by a trusted in-process resolver (#1080). Every other terminal path yields **deny**:

| Path | Result |
|---|---|
| Resolver allows | `ApproveResult{behavior:"allow", updatedInput:…}` |
| Resolver denies | `ApproveResult{behavior:"deny", message:…}` |
| `approvalTimeout` elapses (no resolver — the state until #1080 lands) | deny (`"approval request timed out"`, permbridge's fixed reason) |
| Caller disconnects mid-wait | deny (fixed reason); entry eagerly deleted |
| Daemon shutdown mid-wait | deny (fixed reason); entry eagerly deleted |
| Duplicate / empty id at Register | deny (fixed message) |
| Registry not wired (v1/foreground) | `Response{Error:"… no approval registry configured"}` → sibling denies |

All daemon-originated deny messages are **fixed constants** (no host-derived content). claude's deny path does not hang the turn, so a deny is always the safe default.

### Wiring (AC-4) — `cmd/pyry/main.go` `runSupervisor`

One shared `*permbridge.Registry`, created at the composition root — the only scope that sees both the relay composition and the control server:

- `approvals := permbridge.New()` before the `relayWiring`/`NewServer` block.
- After `ctrl := control.NewServer(...)` (main.go:948) and before `ctrl.Serve` (main.go:955): `ctrl.SetApprovalRegistry(approvals, mcpApprovalTimeout)`.
- `mcpApprovalTimeout` — a `cmd/pyry` const (the human-approval window). Propose **2 minutes**; see Open questions.

**#1080 shares this exact instance.** It is created in `runSupervisor` where the v2 modal-resolve composition (`cmd/pyry/modal_resolve_v2.go`) is also assembled, so #1080's modal wiring threads *this* `approvals` value into its resolve path and calls `Resolve`/`Lookup` against the same registry — no second instance. **This ticket does NOT add a `relayWiring.approvals` field:** with no reader until #1080 lands, a set-but-unread unexported field trips `staticcheck` U1000 (CI-gated) and would fail the build. The shared instance living in `runSupervisor` scope makes #1080 a thin threading change. *(If a reviewer reads AC-4 as demanding the field now: it is unsatisfiable without a reader and must wait for #1080 — flag, don't add dead wiring.)*

## Testing strategy

New `internal/control/approve_test.go` (same-package, stdlib `testing`, `-race`). Use a **real** `permbridge.New()` (stdlib leaf — no fake needed); drive the resolver by calling `reg.Resolve` from the test, simulating #1080. Helper `startServerWithApprovalRegistry(t, resolver, reg, timeout)` mirrors `startServerWithRekeyer`.

- **allow** — send `mcp.approve` with `input` = a known object; concurrently `reg.Resolve(id, permbridge.Allow(input))`; assert `Response.Approve.Behavior == "allow"` and `UpdatedInput` round-trips byte-verbatim.
- **deny (resolver)** — `reg.Resolve(id, permbridge.Deny("no"))`; assert `behavior=="deny"`, `message=="no"`, `UpdatedInput` empty.
- **deny (timeout)** — `timeout` ≈ 50ms, never resolve; assert deny with `"approval request timed out"`. Bounds the "block for verdict" contract without a resolver.
- **disconnect-before-resolution** — raw `net.Dial`, `json.Encode` the request, poll `reg.Lookup(id)` until **present** (handler registered), `conn.Close()`, then poll `reg.Lookup(id)` until **absent** within a short deadline. Proves eager cleanup (no leaked entry) and, implicitly, no hung goroutine (the deny lands without the timeout elapsing). Assert the poll completes well under `timeout`.
- **unavailable** — server with nil registry (`startServerWithApprovalRegistry(t, res, nil, …)` or reuse a no-setter helper); assert `Response.Error` contains `"no approval registry configured"`.
- **missing tool_use_id** — empty `ToolUseID`; assert `Response.Error` contains `"missing tool_use_id"`; assert nothing was registered.
- **shutdown-unblocks** *(optional, if cheap)* — register, then `stop()`/`Close()` the server; assert the handler returns and the entry is gone without waiting `timeout`. Guards the `s.closedCh` path and the `handleWG.Wait()` promptness.

Write scenarios as bullets → the developer writes them in the project's table-driven idiom. `json.RawMessage` round-trip: compare via `json.Compact`/byte-equality, not struct `==`.

## Scope

**In:** the wire types, the `handleApprove` handler + `Approver` seam (`SetApprovalRegistry`), the dispatch case, the shared-registry wiring in `main.go`, and the tests above.

**Out (sibling `pyry mcp-approve` ticket):** the `pyry mcp-approve` subcommand, the `control.Approve(ctx, socketPath, …)` client helper (mirrors `client.go`'s `Rekey`), the MCP-tool registration / `--permission-prompt-tool` plumbing, and claude-result construction from the verdict. **Out (#1080):** the v2 modal-resolve consumer that calls `reg.Resolve`/`Lookup`.

### Cross-ticket contract (for the sibling architect)

The `pyry mcp-approve` subcommand MUST **fail-closed**: any control error (`Response.Error != ""`, dial failure, decode failure, timeout) or an absent `Response.Approve` maps to a claude **deny** — never a hang, never a synthesized allow. The daemon returns `Response.Error` for "unavailable"/"missing id" (not a baked deny) to match the control-plane precedent; baking the deny is the subcommand's job.

## Open questions

- **`mcpApprovalTimeout` value.** Proposed 2 min. Until #1080 lands there is no resolver, so *every* production approval times out to deny after this window (claude blocks that long per tool call). Nothing invokes the verb in production until the sibling wires `--permission-prompt-tool`, so the value is inert for now. Revisit / make configurable when the full chain lands. Developer: pick the const, doc-comment it as tunable.
- **Duplicate `tool_use_id`.** Register rejects a live-id collision → fail-closed deny. tool_use_ids are unique per claude tool call; a genuine retry would only collide with a still-pending prior attempt, which fail-closed deny handles safely. No dedup/replace logic in this slice.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. `ApprovePayload.{Input,ToolName,ToolUseID}` cross socket→process untrusted at `handle`'s shared `dec.Decode(&req)` (`server.go:498`); the boundary is explicit and single (that decode + the step-2/3 guards). The daemon **never parses, executes, or dispatches on** `Input`/`ToolName` — `Input` is `json.RawMessage` round-tripped byte-verbatim into the registry and echoed only on an explicit resolver `Allow`; `ToolUseID` is the registry key, a **correlation key, not a credential** (permbridge doc). The one enforcement is `reg.Register` id-validation (`permbridge.go:121`, empty/dup → `ErrDuplicateID`) plus the handler's empty-id guard. No untrusted value reaches a path, shell, spawn arg, or routing decision.
- **[Fail-closed — the security-critical core]** No findings. `allow` is reachable **only** via a trusted in-process `reg.Resolve(id, Allow(...))` (#1080); the untrusted socket peer can only submit-and-await, never inject an allow. Every daemon terminal path (timeout, disconnect, shutdown, dup/empty-id, nil-registry) yields deny — cross-checked against the § Error handling table.
- **[Tokens, secrets, credentials]** N/A — the verb mints/handles no token. The control socket's sole auth remains the `0600` mode (`server.go:357`); `ApproveResult` carries no secret.
- **[File operations]** N/A — no filesystem path is derived from the payload; the verb opens/creates/writes no file.
- **[Subprocess execution]** N/A — the daemon execs nothing from the payload; `Input` is passed to no command. (claude may later act on an *allowed* tool, but that is claude's own sandbox, downstream of a trusted resolver — not this verb.)
- **[Cryptographic primitives]** N/A — no RNG, keys, or secret comparison. `ToolUseID` matching is a plain map lookup on a correlation key; constant-time comparison is not warranted.
- **[Network & I/O]** SHOULD FIX (not gating). The handler clears the 5s handshake deadline to block for the verdict — bounded, not slow-loris-open, because permbridge's registry-owned timer always fires within `approvalTimeout`, then denies+closes. Resource note: this verb is the first to store attacker-influenced `json.RawMessage` in an in-memory registry keyed by an attacker-chosen id; a same-user flood grows the pending map (each entry auto-deleted at `approvalTimeout`). No max-input-size cap exists (inherited — the control decoder is uncapped for every verb). Bounded by the existing `0600` same-user threat model (`server.go:479-484` TODO); a boundary input-size guard would cap it if a flood is ever observed. Not gated — no observed failure (Evidence-Based Fix Selection).
- **[Error messages, logs, telemetry]** No findings. All daemon-originated deny messages are **fixed constants** (no host-derived content); `Response.Error` strings are fixed operator diagnostics with no payload echo. The decision log emits `tool_use_id` (correlation key) + `behavior` only — `Input`/`ToolName` are MUST-NOT-log. permbridge is log-free by construction. Inverse of the [[architect-security-decode-handler-drops-err-in-malformed-log]] hazard: the handler reuses `handle`'s shared decoder (no custom decode dropping err+id) and adds no content to logs.
- **[Concurrency]** No findings. `s.mu` guards only the snapshot read of `approvals`/`approvalTimeout`, released before `Register`/`Await` (never held across the block or a channel send) — matches the `handleRekey` + permbridge leaf-mutex discipline. Both spawned goroutines are reaped (watcher on `stop`/`s.closedCh`/`readCh`; inner reader on conn close); shutdown unblocks via `s.closedCh` so `handleWG.Wait()` never stalls for `approvalTimeout`. The registry's delete-under-lock one-shot arbitrates all terminal writes — no check-then-mutate race, no double-send. Detailed in § Concurrency model.
- **[Threat model alignment]** CLI/control-plane ticket (not relay). Relevant threat — an untrusted MCP payload must never yield an unauthorized allow — addressed by fail-closed. No `docs/threat-model.md` exists; the de-facto CLI model is the `0600` owner-only socket (`server.go:351-356`). **OUT OF SCOPE:** cross-user socket exposure (containers / multi-tenant), which `server.go` already names as a future revisit — not introduced or widened here.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-20
