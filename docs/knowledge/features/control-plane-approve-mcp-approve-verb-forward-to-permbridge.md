# Approve: mcp.approve verb — forward to permbridge, block, default-deny (#1104)

`VerbMCPApprove` ("mcp.approve", dotted like `sessions.*`) forwards a claude tool-approval request from the `pyry mcp-approve` subcommand (sibling ticket) into `internal/permbridge`, blocks for the allow/deny verdict, and returns it. The human-facing decision is made in the daemon — where modal surfacing lives, via the `streamApprovalBridge`/`SetApprovalSurfacer` seam below (#1080) — not in the ephemeral MCP child claude spawns for `--permission-prompt-tool`. Same structural cousin as `rekey`, not the relay-plane `request_debug_bundle`/`set_session_settings` phone frames: both are control-socket verbs with an optional dependency that replies "unavailable" when unwired.

### `Approver` seam: optional dependency, setter-installed

Mirrors `SetRekeyer` exactly — `NewServer`'s signature stays frozen, the dependency is installed post-construction:

```go
func (s *Server) SetApprovalRegistry(reg *permbridge.Registry, timeout time.Duration) {
    s.mu.Lock()
    s.approvals = reg
    s.approvalTimeout = timeout
    s.mu.Unlock()
}
```

`s.approvals`/`s.approvalTimeout` are read once under `s.mu` at the top of `handleApprove`, then the lock is released before the (blocking) `Await` — same leaf-lock discipline as `Rekeyer`. Nil registry is the production state until the daemon composition wires it (AC-4, below), and stays nil in v1/foreground.

### Wire shape

```go
type ApprovePayload struct {
    ToolName  string          `json:"tool_name"`
    Input     json.RawMessage `json:"input"`
    ToolUseID string          `json:"tool_use_id"`
}

type ApproveResult struct {
    Behavior     string          `json:"behavior"`
    UpdatedInput json.RawMessage `json:"updatedInput,omitempty"` // allow only
    Message      string          `json:"message,omitempty"`      // deny only
}
```

Snake_case tags on `ApprovePayload` (unlike every other camelCase control-socket payload) deliberately match the T1 spike's approval contract (`fixture-p4-approval-contract.json`) so the `pyry mcp-approve` subcommand marshals straight from claude's tool call with no field renaming. `ApproveResult`'s field/tag layout is byte-identical to `permbridge.Verdict`'s wire shape (see [permbridge-package.md](permbridge-package.md)) so the subcommand can marshal it straight to claude as the MCP tool result — a deliberate mirror, same justification as `SessionInfo` vs `sessions.SessionInfo`.

### `handleApprove`: guard order, then block

Guard order mirrors `handleRekey` (nil-dependency check before payload validation):

| Precondition | Server reply |
| --- | --- |
| `reg == nil` | `Response{Error: "mcp.approve: no approval registry configured"}` |
| `payload == nil \|\| payload.ToolUseID == ""` | `Response{Error: "mcp.approve: missing tool_use_id"}` |
| `reg.Register(...)` returns `ErrDuplicateID` | `Response{Approve: <deny, fixed message>}` — fail-closed, not a wire error |
| verdict resolves (allow or deny) | `Response{Approve: <verdict>}` |

Unlike `handleRekey`'s fire-and-ack shape, `handleApprove` **blocks** between decode and response: `conn.SetDeadline(time.Time{})` clears the 5s handshake deadline (mirrors `handleAttach`) before `pending.Await()`, which is guaranteed to return within `approvalTimeout` because permbridge's registry-owned timer always fires. Immediately after `Register` succeeds, `handleApprove` also `defer`s the surfacer's `retire` closure (see § Approval surfacer seam below) — before `Await`, so it covers every terminal path. A per-conn watcher goroutine (`watchApproveConn`) maps the two cancellation sources `Await` cannot see — caller disconnect (`conn.Read` on the client's silent conn hits EOF) and daemon shutdown (`s.closedCh`) — into `reg.Resolve(id, Deny(...))`, so a lost caller or a shutting-down daemon does not park an entry for the full timeout. The handler closes a `stop` channel after `Await` returns so the watcher does not also resolve a verdict on the normal path; the watcher's inner reader goroutine stays parked on `conn.Read` until `handle`'s deferred `conn.Close()` reaps it — bounded, not leaked (code review, PR #1112: flagged as untracked-on-`streamingWG` but judged NIT because conn-close bounds it, same shape as `handleAttach`'s detach watcher).

Fail-closed is structural: `allow` is reachable **only** through an explicit `reg.Resolve(id, permbridge.Allow(...))` by the trusted in-process `streamApprovalBridge.ResolveStream` (#1080), itself gated behind `modalResolverV2.ResolveAnswer`'s `MayAnswerRemotePermission` device check. The untrusted socket peer can only submit-and-await. Every other terminal path — timeout, disconnect, shutdown, duplicate/empty id, nil registry — yields deny or the "unavailable" error. All daemon-originated deny messages are fixed constants (`reasonApproveDisconnect`, `reasonApproveShutdown`, `reasonApproveDuplicate`) — no host-derived content. The decision log (`s.log.Info("control: approval resolved", "tool_use_id", ..., "behavior", ...)`) emits only the correlation key and outcome; `Input`/`ToolName` are never logged.

### Approval surfacer seam: `SetApprovalSurfacer` (#1080)

A second optional dependency, installed the same `SetRekeyer`/`SetApprovalRegistry` way, raises a parked approval to interactive clients as a `modal_shown` and hands back the cleanup closure `handleApprove` must run on every terminal path:

```go
func (s *Server) SetApprovalSurfacer(surface func(permbridge.Request) func()) {
    s.mu.Lock()
    s.approvalSurfacer = surface
    s.mu.Unlock()
}
```

`s.approvalSurfacer` is read once under `s.mu` alongside `s.approvals`/`s.approvalTimeout` at the top of `handleApprove`. It is **not** consulted on the `ErrDuplicateID` early return (a request that never parked has nothing to surface or retire). Once `Register` succeeds, `handleApprove` calls `surface(req)` — if non-nil — and `defer`s the returned `retire` closure; a nil surfacer degrades to a no-op `retire := func(){}`, so `handleApprove` parks-and-blocks exactly as it did pre-#1080, just without a client-facing modal. The production implementer is `cmd/pyry`'s `streamApprovalBridge.Surface` — see [v2-session-manager.md § Stream-json approval bridge](v2-session-manager.md#stream-json-approval-bridge--the-verdict-arm-1080) for the correlation it owns (`modal_id ⇄ tool_use_id`) and the single-arbiter dismissal `retire` performs on answer/timeout/disconnect/shutdown.

### Wiring (AC-4, extended #1080): one shared registry at the composition root

`cmd/pyry/main.go`'s `runSupervisor` creates `approvals := permbridge.New()` before `startRelay`, then calls `ctrl.SetApprovalRegistry(approvals, approvalTimeout())` between `control.NewServer(...)` and `ctrl.Listen()`. `relayWiring` now carries an `approvals *permbridge.Registry` field (added by #1080 — U1000 was the reason it was withheld until #1080 gave it a reader): `startRelayV2` constructs the `streamApprovalBridge` over this **same** instance and the daemon-singleton `modalReg`, and `startRelay`/`startRelayV2` each gained a `surface func(permbridge.Request) func()` return value threaded back out to `main.go`, which calls `ctrl.SetApprovalSurfacer(surface)` right after `SetApprovalRegistry`. So the control server and the v2 modal-resolve composition share the *one* `*permbridge.Registry` instance end to end — no second registry is ever constructed. A nil `w.approvals` (foreground/v1/relay disabled) makes `startRelayV2` return a nil `surface`, and `SetApprovalSurfacer(nil)` restores the pre-#1080 behaviour.

`mcpApprovalTimeout` (`cmd/pyry/main.go`, 2 minutes) is the human-approval window handed to `permbridge.Register`. It is inert in production until the `pyry mcp-approve` sibling wires `--permission-prompt-tool` — until then nothing calls `VerbMCPApprove`. Since #1080, an approval that *is* requested and surfaced can also be answered by a client before the window elapses; only an unanswered (or unsurfaced) approval still times out to deny after this window. **Configurable since #1139**: `approvalTimeout()` reads `PYRY_APPROVAL_TIMEOUT` (`time.ParseDuration`) and falls back to `mcpApprovalTimeout` when unset/unparseable, so production behaviour is byte-identical with the env absent — only the timer's *duration* is tunable, never `permbridge`'s deny-on-deadline logic. This is what lets the stream permission-round-trip e2e ([codebase/1139.md](../codebase/1139.md)) shrink the fail-closed window to ~2s without touching the timer itself. Known gap: `cmd/pyry/mcp_approve.go`'s client-side read-deadline margin (`mcpApprovalTimeout + mcpApproveClientMargin`, see [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md)) still derives from the const, not `approvalTimeout()` — inert until `--permission-prompt-tool` wiring lands, flagged as a follow-up in [codebase/1139.md](../codebase/1139.md).

### Testing

`internal/control/approve_test.go` uses a **real** `permbridge.New()` (stdlib leaf, no fake needed) and drives the resolver side directly via `reg.Resolve` to simulate the eventual #1080 resolver. Covers: allow, resolver-deny, timeout-deny (short timeout, never resolved), disconnect-before-resolution (raw dial, poll `reg.Lookup` present → `conn.Close()` → poll `reg.Lookup` absent, proving eager cleanup with no leaked entry), unavailable (nil registry), missing-`tool_use_id`, and shutdown-unblocks (server `Close()` mid-wait resolves promptly without waiting out `approvalTimeout`, so `Serve`'s `handleWG.Wait()` never stalls). `internal/control/approve_surfacer_test.go` (#1080) covers the surfacer seam itself with a fake `surface func`: invoked exactly once per parked approval, the returned `retire` invoked exactly once on the terminal `Await` return, `ErrDuplicateID` never surfaces or retires, and a nil surfacer reproduces the pre-#1080 modal-less behaviour.

See [permbridge-package.md](permbridge-package.md) for the registry primitive's own design and fail-closed proof, [v2-session-manager.md § Stream-json approval bridge](v2-session-manager.md#stream-json-approval-bridge--the-verdict-arm-1080) for the resolver/bridge side, and [codebase/1104.md](../codebase/1104.md) / [codebase/1080.md](../codebase/1080.md) for the ticket records.
