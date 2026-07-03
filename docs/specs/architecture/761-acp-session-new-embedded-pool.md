# Spec: `session/new` over an embedded tui-driver-hosted pool (#761)

Epic #600 — `pyry acp` as a thin adapter over the shared remote-head core.

## Files to read first

Turn-1 data load. Read these before writing any code.

- `cmd/pyry/acp.go` (whole, 96 lines) — the composition root you extend. `runACP` (the subcommand entry, registers no handlers today) and `serveACP` (the io.Pipe stdin bridge + ctx-cancel/EOF shutdown machinery). You add the pool standup to `runACP` and a handler-registration seam to `serveACP`; **do not** disturb the io.Pipe/closer/bridge shutdown logic.
- `internal/acp/acp.go:53-59` — `Handler` type (the contract your `session/new` handler satisfies: `func(ctx, json.RawMessage) (result any, err error)`; a returned `*acp.Error` controls the wire code, any other error → `CodeInternalError` with the detail logged not leaked).
- `internal/acp/acp.go:118-129` — `Register(method, h)` (must be called **before** `Serve`; panics on duplicate or post-Serve).
- `internal/acp/acp.go:221-244` — `dispatchRequest` (what happens to your handler's return value; `writeSuccess` marshals `result any` into the JSON-RPC `result`).
- `internal/sessions/pool.go:299-441` — `New` + the bootstrap-construction block. This is where `BootstrapEvicted` branches (the `lcState`/`activeCh`/`evictedCh` init at :405-411) and where `readyCh` is created.
- `internal/sessions/pool.go:786-847` — `Run` (where `runGroup` is set at :802-804 and where you close `readyCh` after `supervise(bootstrap)`).
- `internal/sessions/pool.go:859-950` — `supervise` (`ErrPoolNotRunning` seam) + `Create`/`CreateIn` (the mint→persist→supervise→Activate sequence; **read Create's docstring** — an `ErrPoolNotRunning` return leaves a non-empty id for an unrecoverable stuck session; that is why the readiness gate is mandatory).
- `internal/sessions/pool.go:966-1026` — `buildSession` (the `if tpl.Bridge != nil { bridge = supervisor.NewBridge }` line — this is why setting `Bootstrap.Bridge` puts Created sessions in service mode; and the `--session-id <uuid>` args that make the spawn interactive with no `-p`).
- `internal/sessions/session.go:270-404` — `Run` / `runActive` / `runEvicted`. Confirm an **evicted** bootstrap parks in `runEvicted` and spawns no supervisor (no claude).
- `internal/supervisor/supervisor.go:656-796` — `runOnce`. **Load-bearing:** the foreground path (`Bridge == nil`) writes `sess.MirrorOutput()` chunks to `os.Stdout` at **:771-775**; the service path (`Bridge != nil`) writes them to the Bridge at :701-706. ACP owns stdout for JSON-RPC — service mode is mandatory.
- `cmd/pyry/main.go:631-733` — `runSupervisor`, the standup you trim (config/path/trust → `sessions.New` → `pool.Run`). Lift only the trust + `sessions.New` + `pool.Run` spine; drop relay/control/conversations/queue.
- `cmd/pyry/main.go:438-459` — `confineWorkdirToHome` (`""` → `filepath.Abs` → process cwd, confined to `$HOME`; the workdir realpath you thread into `Bootstrap.WorkDir`).
- `cmd/pyry/agent_run.go:28` — `trustMark` package var (`= trust.MarkWorkdirTrusted`, test-overridable). Same-package (`main`), so `runACP` calls it directly.
- `internal/sessions/pool_test.go:304` (`helperPoolReconciling`) and `cmd/pyry/session_router_test.go:18` (`newRouterTestPool`) — pool test-construction patterns (`sessions.New` with `ClaudeBin: "/bin/sleep"` / `os.Args[0]`).
- `CODING-STYLE.md` § Testing — table-driven, stdlib `testing` only, `TestHelperProcess` re-exec pattern (used for the fake claude that records argv).

## Context

`pyry acp` (#756) is today a bare JSON-RPC stdio transport: it registers no handlers and drives no claude. #757 added the outbound `Call` primitive. This ticket delivers the **first ACP method that does real work** (`session/new`) plus the **composition root** it needs: an embedded `internal/sessions` pool, trimmed to what ACP needs (no relay, control socket, conversations registry, or message queue).

ACP maps one ACP session onto exactly one running interactive `claude` (**divergence 6**). `session/new` must allocate one pool session, start one supervised interactive `claude`, and return a fresh session id — **no more, no fewer** claudes, and the claude must be the real interactive one (**hard cost invariant**: no `claude -p`, no Agent SDK).

Two facts about the existing pool shape the whole design (verified against live code):

1. **The pool always eager-spawns a bootstrap claude.** `sessions.New` forces the bootstrap to `stateActive` (pool.go:339, and the fresh-mint else-branch defaults to it), and `pool.Run` unconditionally `supervise(bootstrap)`s it → `runActive` → interactive claude. A naive standup that then `Pool.Create`s per `session/new` yields **two** claudes for one ACP session — a divergence-6 and hard-cost violation.
2. **Foreground mode clobbers stdout.** `supervisor.runOnce` in foreground mode (`Bridge == nil`) copies claude's PTY output to `os.Stdout`. ACP owns stdout for the JSON-RPC frame stream, so the ACP-hosted claude **must** run in service mode (`Bridge` set), where output routes to the Bridge (discarded when nothing attaches).

## Design

Two minimal, additive primitives in `internal/sessions` + the composition root and handler in `cmd/pyry/acp.go`. Total: **2 production files modified, 0 new production files.**

### 1. `internal/sessions` — two additive Config/API primitives

Both are purely additive (zero-value preserves today's behaviour) with a single new consumer (the ACP root) — no call-site fan-out.

**(a) `Config.BootstrapEvicted bool`** — when true, `New` constructs the bootstrap session in `stateEvicted` instead of forcing `stateActive`. `pool.Run`→`supervise(bootstrap)`→`bootstrap.Run`→`runEvicted` parks; **no claude spawns from the bootstrap.** The eager claude is now solely `Pool.Create`'s job, so `session/new` produces exactly one.

- Contract: `BootstrapEvicted == false` (zero value) is byte-identical to today. `true` sets `lcState = stateEvicted` and the matching channel init (`activeCh = make(chan struct{})`, `evictedCh = closedChan()`) at the existing New:405-411 branch.
- The #202 evicted-bootstrap-hang risk (New:330-340 comment) does **not** apply here: ACP uses `RegistryPath == ""` (nothing persisted) and never `Activate`s the bootstrap — it stays a dormant placeholder for the pool's `Default()`/`Lookup("")` invariant until `Run`'s ctx cancels.
- Invariant to assert: with `BootstrapEvicted: true`, after `Run` is up, `Pool.Snapshot()` shows the bootstrap with `PID == 0`.

**(b) `Pool.Ready() <-chan struct{}`** — a channel closed by `Run` once `runGroup` is wired and the bootstrap is supervised, i.e. once `Pool.Create` will succeed rather than return `ErrPoolNotRunning`.

- Why this is a **correctness gate, not a nicety**: `Create` calls `supervise`, which returns `ErrPoolNotRunning` when `runGroup` is nil. On that path Create returns a **non-empty id** for a session that is minted + (would-be) persisted but **never supervised** — there is no public re-supervise, and `Activate` on it blocks forever (no lifecycle goroutine reads `activateCh`). The session is permanently stuck. So the very first `session/new` must not race `pool.Run`'s startup. A microsecond window is enough to strand the first call; the failure is unrecoverable.
- Contract: `Ready()` returns a channel that is closed exactly once, after `Run` sets `runGroup`/`runCtx` and `supervise(bootstrap)` returns nil (pool.go:811-813). Create an internal `readyCh chan struct{}` in `New`; close it in `Run` under a `sync.Once` (guards the theoretical double-`Run`). Before `Run` has ever been called the channel is open (never-ready), which is correct — the ACP root always backgrounds `Run` before selecting on `Ready()`.
- This is the deterministic safety net the pipeline principle demands (a channel, not a retry/poll).

### 2. `cmd/pyry/acp.go` — composition root + handler

**Standup in `runACP`** (after the logger, before serving). Trim `runSupervisor`'s spine to the ACP subset:

```
workdirReal ← confineWorkdirToHome("")        // process cwd, confined to $HOME (reuse)
trustedWorkdir ← trustMark(workdirReal)        // reuse the shared cmd-layer pre-mark
bridge ← supervisor.NewBridge(logger)          // service mode → claude output off stdout
pool ← sessions.New(sessions.Config{
    Logger:           logger,
    BootstrapEvicted: true,                     // no eager bootstrap claude
    Bootstrap: sessions.SessionConfig{
        ClaudeBin: "claude",                    // runACP takes no flags; default binary
        WorkDir:   trustedWorkdir,
        Bridge:    bridge,                       // service mode
    },
    // RegistryPath / ClaudeSessionsDir / ConversationsRegistry / SweepInterval / ActiveCap
    // all left zero → no persistence, no reconcile, no rotation watcher, no sweep,
    // no relay, no control socket, no message queue.  (AC-1)
})
```

- **Service mode rationale (AC-1, stdout hygiene):** setting `Bootstrap.Bridge` makes `buildSession` mint a per-session `Bridge` for every Created session, so claude's `MirrorOutput` routes to the Bridge's two heads. With no attacher and no observer, both heads are nil → discarded → stdout stays clean for JSON-RPC frames. The Bridge is the supervisor's I/O mediator, **not** one of the prohibited subsystems (relay/control/conversations/queue). It is the mechanism that keeps AC-1's "diagnostics/PTY off stdout" true.
- **Trust reuse (why NOT security-sensitive):** the spawn reuses the daemon's own workdir + the shared `trustMark` seam; no caller-supplied path reaches the spawn. The handler **must not** read `cwd` from params (see below) — doing so would re-open the confinement surface and flip the ticket to `security-sensitive`.

**Concurrent lifecycle in `runACP`:**

```
poolErr ← buffered(1)
go { poolErr <- pool.Run(ctx) }
select {
  <-pool.Ready():   proceed to serve
  err <- poolErr:   Run failed before readiness (e.g. bootstrap supervisor.New) → cancel, map non-Canceled err, return
  <-ctx.Done():     signal during startup → drain poolErr, return nil
}
serveErr ← serveACP(ctx, os.Stdin, os.Stdout, logger, register)   // register wires session/new
cancel()          // stop pool.Run on EOF/return path
<-poolErr         // join
return serveErr
```

- `serveACP` keeps its exact io.Pipe/closer/bridge shutdown behaviour; the **only** change to its signature is a new final `register func(*acp.Transport)` parameter, invoked after `acp.New` and before `t.Serve` (mirroring the acp "register before Serve" invariant). `register == nil` ⇒ register nothing (preserves the #756 behaviour any existing caller/test relies on).
- The `register` closure: `func(t *acp.Transport) { t.Register("session/new", newSessionHandler(pool)) }`.

**`session/new` handler** (`newSessionHandler(pool) acp.Handler`):

- Signature (contract, not body): `func(ctx, params json.RawMessage) (any, error)`.
- Behaviour: call `id, err := pool.Create(ctx, "")` (empty label). On success return a result marshalling to `{"sessionId": "<uuid>"}`. On error return it wrapped (`fmt.Errorf("session/new: %w", err)`) → maps to `CodeInternalError`, detail logged not leaked.
- **Params are ignored.** ACP `session/new` carries `cwd`/`mcpServers`; this ticket honours neither. Do **not** parse or read `cwd` — the spawn uses the daemon workdir. (Recorded so the security posture is explicit; #762+ owns any caller-cwd support and its `security-sensitive` flip.)
- Result type: an unexported struct with one exported, json-tagged field — `type newSessionResult struct { SessionID string \`json:"sessionId"\` }`. (Unexported type + exported field marshals fine; **zero new exported types.**)
- `ctx` is the transport's Serve ctx (the `runACP` signal ctx). `Create` uses it for the `Activate`/`WaitForPTY` spawn wait; on shutdown it surfaces `ctx.Err()`.

### Data flow

```
ACP host ──stdin(JSON-RPC)──▶ serveACP io.Pipe ──▶ acp.Transport.Serve
                                                        │ dispatchRequest("session/new")
                                                        ▼
                                              newSessionHandler(pool)
                                                        │ pool.Create(ctx,"")
                                                        ▼
   pool.Run errgroup ◀── supervise(sess) ──  mint fresh UUID → buildSession
     (bootstrap parked                         (service-mode Bridge, args
      in runEvicted,                            = [--session-id <uuid>])
      PID 0)                                          │
                                                      ▼  supervisor.runOnce → tui-driver.Spawn
                                            ONE interactive claude (PID>0)
                                                      │ MirrorOutput
                                                      ▼
                                            per-session Bridge (no attach/observer → discarded)
                                                      ⇒ os.Stdout stays clean for frames
   handler returns {"sessionId": "<uuid>"} ──▶ writeSuccess ──stdout──▶ host
```

## Concurrency model

- **Two long-lived goroutines** in the ACP subprocess: `pool.Run(ctx)` (backgrounded, supervises the parked bootstrap + every Created session via its errgroup) and the `serveACP` read loop (foreground). Plus `serveACP`'s existing two helper goroutines (ctx-close watcher, stdin→pipe bridge) — unchanged.
- **Startup ordering** is enforced by the `Ready()` gate: no frame is dispatched (serving hasn't started) until `pool.Run` has wired `runGroup`, so `Create`'s `supervise` never hits `ErrPoolNotRunning`.
- **Shutdown:** EOF (host closes stdin) → `serveACP` returns nil → `cancel()` → `pool.Run` tears down claude via the errgroup's `gctx` (each `runActive` cancels its supervisor's ctx) → `<-poolErr` joins. Signal (SIGINT/SIGTERM) → ctx cancels → `serveACP` returns nil (ctx.Err filtered) → same join. `pool.Run` returns `context.Canceled`, which is filtered.
- **Locks:** unchanged. The two new primitives touch only `New`/`Run` — `readyCh` closed once under `sync.Once`; `BootstrapEvicted` read once in `New`. No new lock-order edges.

## Error handling

| Failure | Behaviour |
|---|---|
| `claude` not on PATH | `sessions.New` → `supervisor.New` fails → `runACP` returns the error before serving (fail-fast; correct — cannot serve without claude). |
| `confineWorkdirToHome`/`trustMark` fail (cwd outside `$HOME`, `~/.claude.json` write error) | `runACP` returns wrapped error before serving (same posture as `runSupervisor`). |
| `pool.Run` errors before readiness | `select` catches `poolErr`; `cancel()`, return non-`Canceled` error wrapped. |
| `Pool.Create` error inside handler | wrapped, returned → `CodeInternalError` on the wire, detail logged (never leaked). `Create`'s own rollback keeps the pool consistent. |
| Signal during startup or serving | clean exit (nil), pool joined. |

`ErrPoolNotRunning` is designed out by the readiness gate — it should never reach a handler.

## Testing strategy

Table-driven, stdlib `testing`, `TestHelperProcess` re-exec fake claude (no real claude, no TTY).

**`internal/sessions/pool_test.go` (unit):**
- `BootstrapEvicted` suppresses the eager spawn: `New(Config{BootstrapEvicted: true, Bootstrap: {ClaudeBin: "/bin/sleep", Bridge: NewBridge}})`, `go pool.Run(ctx)`, `<-pool.Ready()`, then assert `Pool.Snapshot()` shows the bootstrap `PID == 0`. `Create` → assert exactly one entry has `PID > 0` (the created session) and the bootstrap is still `PID == 0`. Covers divergence-6 at the pool layer.
- `Ready()` closes after `Run` is up and is safe to select on before/after; a `Create` issued after `<-Ready()` does not return `ErrPoolNotRunning`.
- Zero-value regression: `BootstrapEvicted` omitted ⇒ bootstrap is active (existing tests already cover this; add one explicit assertion if convenient).

**`cmd/pyry/acp_test.go` (new test file — not counted as production):**
- Drive `session/new` end-to-end over the transport using paired `io.Pipe`s (the `liveTransport` harness shape already used in `internal/acp/acp_test.go` for #757) with the standup wired to a **fake claude** re-exec (`TestHelperProcess`) that appends its `os.Args` to a temp file (one line per spawn) and blocks until killed. Override `trustMark` (the package var) with a no-op in the test.
- After one `session/new`: (1) the argv file has **exactly one** line → AC-3 (one claude); (2) that line contains `--session-id <returned-uuid>` and **no** `-p`/`--print` → AC-4 (interactive path); (3) the response is `{"sessionId":"<valid-uuid>"}` (validate via `sessions.ValidID`) → AC-2.
- Stdout hygiene: assert the frame-stream writer received only JSON-RPC frames (no claude screen bytes) — a scenario where the fake claude writes to its PTY and the test confirms nothing but frames reached the transport's writer. This is the AC-1 "off stdout" assertion.
- Shutdown: closing the host stdin (EOF) returns cleanly and the pool tears the claude down.

## Out of scope (explicit)

- `session/load`, `session/cancel`, and the no-double-spawn-for-same-id guard → **#762**. `session/new` mints a fresh id each call, so a same-id respawn cannot arise here.
- Honouring a caller-supplied `cwd` (ACP request field). Deferred with its `security-sensitive` flip condition to a later ticket.
- Turn delivery, transcript reconciliation, `/clear` rotation watcher, idle eviction — none are wired (`ClaudeSessionsDir`/`RegistryPath` empty).
- Connecting to a running daemon instead of embedding a pool. Scope satisfies either, but the embedded model is what this spec builds (per #756's `runACP` framing).

## Open questions

- **`session/new` method string.** ACP uses `session/new`; register that exact string. If the host requires an `initialize` handshake before `session/new`, that is a separate handler (out of scope here) — an unregistered `initialize` returns `CodeMethodNotFound`, which conformant hosts tolerate for this floor ticket. If integration shows the host hard-requires `initialize`, raise it (does not change this design's shape).
- **Bootstrap `ClaudeBin` default.** `runACP` has no flags, so `"claude"` (PATH lookup) is the default, matching `supervisor.New`'s own default. If a future ACP flag surface is added, thread it through `Bootstrap.ClaudeBin` — additive, no redesign.
