# `session/new` and the embedded pool (#761)

The first ACP method that does real work, plus the composition root it needs: an
**embedded `internal/sessions` pool** inside the `pyry acp` subprocess, trimmed
to the ACP subset — **no relay, control socket, conversations registry, or
message queue**. ACP maps one session onto exactly one running interactive
`claude` (**divergence 6**); `session/new` allocates one pool session, starts
one supervised interactive claude, and returns a fresh id the host addresses in
later calls — **no more, no fewer** claudes.

### Standup (`runACP` → `serveACPWithPool`)

`runACP` trims `runSupervisor`'s spine to the ACP subset before serving:
`confineWorkdirToHome("")` (process cwd, confined to `$HOME`) → the shared
cmd-layer `trustMark` (marks the workdir trusted in `~/.claude.json` so claude
never wedges on the workspace-trust modal) → `sessions.New` with **two
load-bearing settings**:

- **`BootstrapEvicted: true`** — the pool always eager-spawns a bootstrap claude
  (`sessions.New` forces the bootstrap `stateActive`; `pool.Run` supervises it
  immediately). A naive standup that then `Pool.Create`s per `session/new` would
  yield **two** claudes for one ACP session — a divergence-6 and hard-cost
  violation. `BootstrapEvicted` parks the bootstrap in `runEvicted` (PID 0, no
  claude), so `Pool.Create` is the sole spawn. See
  [`sessions-package.md`](sessions-package.md#configbootstrapevicted--poolready-761)
  and [ADR 026](../decisions/026-embedded-acp-pool-exact-one-claude.md).
- **`Bootstrap.Bridge: supervisor.NewBridge(logger)`** — **service mode is
  mandatory.** Foreground `supervisor.runOnce` copies claude's PTY output to
  `os.Stdout`, which ACP owns for the JSON-RPC frame stream. Service mode routes
  each Created session's output to a per-session Bridge (discarded with no
  attacher/observer), keeping stdout clean for frames. The Bridge is the
  supervisor's I/O mediator — **not** one of the prohibited subsystems.

`serveACPWithPool` backgrounds `pool.Run` on a buffered(1) `poolErr` channel and
`select`s on `pool.Ready()` before serving — the **readiness gate** that closes
the unrecoverable first-`Create → ErrPoolNotRunning` startup race (a stranded,
never-supervised session with a non-empty id and no recovery; see
[`sessions-package.md`](sessions-package.md#configbootstrapevicted--poolready-761)).
On EOF/signal it cancels and joins `<-poolErr` so no supervisor goroutine
outlives the call.

### The handler

`newSessionHandler(pool)` satisfies `acp.Handler`. It calls `pool.Create(ctx,
"")` (empty label → a fresh UUID), and returns `newSessionResult{SessionID:
string(id)}` marshalling to `{"sessionId": "<uuid>"}`. `Pool.Create` spawns
exactly one `claude --session-id <uuid>` through the tui-driver — the
**interactive path, no `-p`/`--print`, no Agent SDK** (the hard cost invariant).
A `Create` error is wrapped (`fmt.Errorf("session/new: %w", …)`) → the mapped
`CodeInternalError` on the wire, detail logged not leaked.

`newSessionResult` is an **unexported** type with one exported json-tagged field
— zero new exported types.

### `cwd` is deliberately ignored (why NOT security-sensitive)

ACP `session/new` params carry `cwd`/`mcpServers`; the handler **reads neither**.
The spawn reuses the daemon's own workdir + the shared `trustMark` seam, so no
caller-supplied path reaches it — a local, same-user, trusted stdio host like the
control socket's session verbs. **Flip condition:** if a later ticket honours a
caller-supplied `cwd` that reaches the spawn *bypassing* the trust seam, the
cwd-confinement surface returns and the work becomes `security-sensitive`
(deferred to #762+).

Full per-ticket detail in [`codebase/761.md`](../codebase/761.md).
