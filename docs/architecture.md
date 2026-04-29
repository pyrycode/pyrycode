# Pyrycode architecture

A short tour of how the pieces fit together. For the user-facing surface see [`guide.md`](guide.md); this document covers the *why* and the internal layout.

## High level

```
                ┌───────────────────┐
   user shell ─►│   pyry CLI        │
                └─────────┬─────────┘
                          │
            ┌─────────────┴─────────────┐
            ▼                           ▼
    ┌───────────────┐         ┌───────────────────┐
    │  supervisor   │◄────────┤  control plane    │
    │ (PTY + child) │         │ (Unix socket)     │
    └───────┬───────┘         └───────────────────┘
            │                           ▲
            ▼                           │
       ┌─────────┐                ┌─────┴──────┐
       │ claude  │                │  pyry      │
       │ (child) │                │  status,   │
       └─────────┘                │  stop,     │
                                  │  logs,     │
                                  │  attach    │
                                  │ (clients)  │
                                  └────────────┘
```

Three components:

1. **Supervisor** (`internal/supervisor`) — owns the lifecycle of one `claude` child. Spawns it in a PTY, watches for exit, applies exponential backoff, restarts with `--continue` so the session resumes.
2. **Control plane** (`internal/control`) — listens on a Unix domain socket. Speaks line-delimited JSON for verb dispatch (`status`, `stop`, `logs`, `attach`); attach upgrades the connection to raw bytes.
3. **CLI** (`cmd/pyry`) — wires the two together for the supervisor invocation, and acts as a thin client for the control verbs.

## Supervisor

### PTY allocation

`claude` is an interactive TUI: it checks `isatty()` to decide whether to render colors, prompts, line editing. To make claude believe it has a terminal even when pyry is running it under launchd, the supervisor allocates a pseudo-terminal pair (via `creack/pty`), gives claude the slave end as its stdin/stdout/stderr, and keeps the master end (`ptmx`) for itself.

```
pyry ── master ↔ kernel PTY ↔ slave ── claude (sees: "I'm in a terminal!")
```

Bytes pyry writes to `ptmx` arrive at claude's stdin; bytes claude writes to its stdout flow back out of `ptmx`. The supervisor's job is to be the thing on the master side.

### Foreground vs service mode

Pyry has one binary and one supervisor implementation, but two operating modes selected at startup:

| Mode | Trigger | What `runOnce` does |
|---|---|---|
| **Foreground** | `term.IsTerminal(stdin)` is true | Puts local stdin in raw mode, watches SIGWINCH for resize, copies `os.Stdin ↔ ptmx` directly. The supervisor's own terminal is the user's terminal. |
| **Service** | No TTY | Routes PTY I/O through a `Bridge` — a switchable I/O mediator with an internal `io.Pipe`. Output is discarded when no client is attached; input is gated on the pipe. |

Mode is detected from stdin in `cmd/pyry/main.go`; the supervisor itself just observes whether `Config.Bridge` is nil.

### Restart loop

```go
for {
    spawn claude
    wait for exit
    if ctx is cancelled → return
    apply backoff (exponential, with stability reset)
    sleep
}
```

Backoff:

- Initial 500 ms, doubles per restart, capped at 30 s
- Resets to initial when a child has stayed up longer than 60 s (configurable via `Config.BackoffReset`)
- The sleep is a `select` against `ctx.Done()`, so SIGINT / SIGTERM during backoff cancels promptly

Restarts pass `--continue` (not `--resume`) so claude rejoins the most-recent session for the working directory. This is robust against the user typing `/clear` inside claude — `--clear` rotates the session-id file on disk, but `--continue`'s "most recent" heuristic still finds the right one. Tracking session IDs explicitly is on the roadmap (Phase 1) but not needed yet.

### State

The supervisor maintains a small `State` struct (`Phase`, `ChildPID`, `RestartCount`, `LastUptime`, `NextBackoff`, `StartedAt`) under a mutex. The control plane reads it via `Supervisor.State()` to answer `status` queries. Updates happen at every transition: `starting → running → backoff → running → … → stopped`.

## The bridge

In service mode, the PTY master can outlive any single attaching client. Bridge is the glue — a single `Bridge` instance persists across child restarts.

```go
type Bridge struct {
    pipeR *io.PipeReader  // supervisor reads, copies to ptmx
    pipeW *io.PipeWriter  // attach handler writes from conn
    output io.Writer      // ptmx writes route here (or nil = discard)
    attached bool
}
```

Two directions, decoupled:

- **PTY → client**: supervisor calls `Bridge.Write` on every byte from `ptmx`. If `output` is set, it forwards to the attached client. If not, the bytes are discarded silently. Crucially, `Bridge.Write` *never* propagates conn errors — a half-broken client write must not stop the supervisor's PTY-drain goroutine, or claude blocks on stdout and the daemon wedges.
- **Client → PTY**: when a client attaches, a goroutine runs `io.Copy(b.pipeW, conn)`. Bytes flow through the internal pipe to the supervisor, which is reading from `Bridge.Read` and writing to `ptmx`.

Pipe-based input means `Bridge.Read` blocks while no client is attached — the supervisor's input goroutine stays alive but idle, no spinning, no data loss.

At-most-one-attacher is enforced via the `attached` flag under a mutex. A second `Attach` call returns `ErrBridgeBusy`.

## Control plane

A Unix domain socket at `~/.pyry/<name>.sock` (configurable via `-pyry-name` and `-pyry-socket`). Permissions are `0600` — single-user file-permission auth is the entire security model.

### Wire protocol

Line-delimited JSON for the handshake. After the handshake, the connection's behavior depends on the verb:

| Verb | Connection lifecycle |
|---|---|
| `status` | One JSON request, one JSON response, close |
| `logs` | One JSON request, one JSON response (with the lines payload), close |
| `stop` | One JSON request, one `OK` response, close — server immediately invokes the configured shutdown callback |
| `attach` | One JSON request, one `OK` response, then the connection is **upgraded** to raw bytes flowing between the client's terminal and the PTY. Connection closes when either side disconnects. |

The protocol upgrade for `attach` is the only departure from the simple request-response shape. See [`protocol.md`](protocol.md) for the message types in detail.

### Lifecycle

- `NewServer` validates state is non-nil, accepts optional dependencies (logs, attach, shutdown).
- `Listen` creates the parent dir if needed, removes any stale socket file, binds, chmods to `0600`. Split from `Serve` so the caller can fail fast on permission errors before starting the supervisor.
- `Serve(ctx)` runs the accept loop, dispatches to per-conn `handle` goroutines tracked in a `WaitGroup`. When ctx is cancelled, the listener closes; `Serve` waits for all in-flight handlers and attach detach-watchers before returning.
- `Close` is idempotent and safe to call from multiple goroutines (used by both the ctx-watcher inside `Serve` and main's defer).

### Per-verb handlers

Split into `handleLogs`, `handleStop`, `handleAttach` to keep each verb's connection-lifecycle obvious:

- `handleLogs` and `handleStop` are one-shot — write a response, return, the deferred close fires.
- `handleAttach` is streaming — clears the handshake deadline, registers the bridge, writes `OK`, and hands off connection ownership to a streaming detach-watcher goroutine that closes the conn when the bridge's `done` channel fires.

The `closeConn` flag in the dispatcher (`handle`) tracks ownership: `true` for one-shot verbs (deferred close), `false` for `attach` after a successful handoff (the spawned goroutine closes).

## CLI

`cmd/pyry/main.go` owns:

- **Supervisor mode** (no recognised verb as `os.Args[1]`): split args via `splitArgs` into pyry's flags vs claude's pass-through, parse pyry flags with `flag.NewFlagSet`, build a `supervisor.Config`, optionally create a `Bridge` (only when stdin isn't a TTY), wire up the control server, run.
- **Client verbs**: `runStatus`, `runStop`, `runLogs`, `runAttach`. Each calls `parseClientFlags` to resolve the socket path (via `-pyry-name` / `PYRY_NAME` / `-pyry-socket` precedence) and dials the appropriate `control.X` client function.

The `splitArgs` walker is the part that makes pyry feel like a transparent claude wrapper — anything not matching a known `-pyry-*` flag tips the rest of the argv list into claude territory.

## Lifecycle of a typical session

Foreground:

```
pyry "summarize foo.md"
├─ splitArgs: pyryArgs=[], claudeArgs=["summarize foo.md"]
├─ supervisor.Config{ClaudeArgs: ["summarize foo.md"], Bridge: nil}
├─ supervisor.New → defaults applied
├─ control.NewServer(..., bridge=nil) → attach disabled
├─ ctrl.Listen → ~/.pyry/pyry.sock created
├─ go ctrl.Serve(ctx)
└─ sup.Run(ctx)
   ├─ runOnce: pty.Start(claude "summarize foo.md")
   ├─ raw mode on, SIGWINCH watcher on
   ├─ io.Copy(ptmx, os.Stdin) + io.Copy(os.Stdout, ptmx)
   ├─ cmd.Wait() blocks until claude exits
   ├─ exponential backoff
   └─ runOnce again, this time with "--continue" prepended

(SIGINT or pyry stop → ctx cancel → loop exits, defers fire, exit 0)
```

Service mode (under launchd / systemd):

```
launchd starts /usr/local/bin/pyry --channels plugin:discord …
├─ stdin not a TTY → bridge = supervisor.NewBridge(logger)
├─ supervisor.Config{... Bridge: bridge}
├─ control.NewServer(..., bridge=bridge) → attach enabled
└─ sup.Run(ctx)
   └─ runOnce: pty.Start(claude --channels plugin:discord)
      ├─ NO raw mode (no terminal to put in raw mode)
      ├─ NO SIGWINCH watcher
      ├─ go io.Copy(ptmx, bridge) + io.Copy(bridge, ptmx)
      └─ cmd.Wait()

(meanwhile, in a separate shell:)
pyry attach
├─ control.Attach(ctx, sock, cols, rows)
├─ json handshake → server
├─ server.handleAttach: bridge.Attach(conn, conn) → done channel
├─ ack OK back to client
├─ raw mode on local stdin, escape-detector pumping bytes to conn
└─ (until Ctrl-B d, server hangup, or local stdin EOF)
```

## Why these choices

A few decisions worth justifying:

**Two modes (foreground / service), one binary.** Easier than asking the user to remember a `--service` flag. The TTY check is the canonical Unix way to ask "is there a human at the keyboard?" — and it's exactly the right question for "should I bridge to that keyboard?"

**JSON over Unix socket.** Simple, debuggable, extensible. `socat - UNIX:~/.pyry/pyry.sock` lets you poke at the protocol manually. JSON handshake before raw-byte upgrade is the same pattern WebSocket uses.

**No socket-level auth.** `0600` permissions cover single-user dev/service deployment, which is the entire Phase 0 target. Multi-tenant or cross-user setups need a different threat model; explicit non-goal for now.

**Bridge-based service mode.** Keeps the supervisor's per-restart code path simple — `runOnce` doesn't care whether the bytes are coming from `os.Stdin` or a `Bridge`. The mode toggle is a single `Config.Bridge != nil` check.

**`--continue` over `--resume <id>`.** Lets the user `/clear` inside claude (which rotates the session ID on disk) without orphaning pyry's bookmark. The roadmap revisits explicit session-ID tracking when multi-session lands.

**Names, not cwd-derived paths, for multi-instance.** Tried cwd-derived as a brief detour (PR #7) and reverted (PR #9). The tmux model — explicit names, env var for shell-scoped defaults — is cleaner and matches user mental models better. No `cd` choreography needed.

## Pointers

- [`internal/supervisor/supervisor.go`](../internal/supervisor/supervisor.go) — `Config`, `Supervisor`, `Run`, `runOnce`
- [`internal/supervisor/bridge.go`](../internal/supervisor/bridge.go) — service-mode I/O mediator
- [`internal/supervisor/backoff.go`](../internal/supervisor/backoff.go) — exponential timer with stability reset
- [`internal/control/server.go`](../internal/control/server.go) — listener, dispatch, per-verb handlers
- [`internal/control/protocol.go`](../internal/control/protocol.go) — wire types
- [`internal/control/logs.go`](../internal/control/logs.go) — ring buffer + `slog.Handler` tee
- [`internal/control/attach_client.go`](../internal/control/attach_client.go) — client side of attach, escape-key detector
- [`cmd/pyry/main.go`](../cmd/pyry/main.go) — CLI, mode detection, wiring
