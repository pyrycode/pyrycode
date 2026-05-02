# System Overview

Pyrycode is a process supervisor that keeps a Claude Code session alive across crashes and reboots. Phase 0 is a single-session supervisor; later phases add multi-session routing, Channels integration, and remote access.

## Module Structure

```
pyrycode/
├── cmd/pyry/                  Binary entry point
│   └── main.go                CLI parsing, signal setup, supervisor init
├── internal/supervisor/       Core process supervision
│   ├── supervisor.go          Supervisor type: PTY spawn, I/O bridge, restart loop
│   ├── backoff.go             Backoff timer: exponential delay with stability reset
│   └── winsize.go             SIGWINCH → PTY size sync
├── internal/sessions/         Session-addressable runtime (Phase 1.0+)
│   ├── id.go                  SessionID + UUIDv4 NewID() via crypto/rand
│   ├── session.go             Session: wraps one supervisor + optional bridge; persisted metadata (label, created/last-active, bootstrap)
│   ├── pool.go                Pool: in-memory registry, Config (RegistryPath + ClaudeSessionsDir), load-or-mint bootstrap on New, RotateID seam, saveLocked, errgroup Run, allocated-UUID skip set, Snapshot
│   ├── registry.go            On-disk schema (registryFile, registryEntry); loadRegistry, saveRegistryLocked (atomic temp+rename), pickBootstrap, sortEntriesByCreatedAt
│   ├── reconcile.go           Startup JSONL scan: encodeWorkdir, mostRecentJSONL, reconcileBootstrapOnNew, DefaultClaudeSessionsDir
│   └── rotation/              Live /clear watcher (Phase 1.2b-B)
│       ├── watcher.go         fsnotify lifecycle, event loop, probe orchestration with bounded retry
│       ├── probe.go           Probe interface, parseProcFD, parseLsofOutput, noopProbe fallback
│       ├── probe_linux.go     //go:build linux  — /proc/<pid>/fd walk
│       └── probe_darwin.go    //go:build darwin — lsof -nP -p <pid> -F fn shell-out
├── internal/control/          Control-plane server (Unix socket, JSON)
│   ├── server.go              Server, SessionResolver / Session interfaces, verb dispatch
│   ├── attach.go              Attach handoff to supervisor bridge
│   └── logs.go                Ring-buffer log streaming
├── systemd/pyry.service       Linux systemd user unit
└── launchd/dev.pyrycode.pyry.plist   macOS launchd plist
```

Dependency direction: `cmd/pyry → internal/sessions → internal/supervisor`, with `internal/control` importing `internal/sessions` for the `SessionID` type referenced by its `SessionResolver` interface. `internal/sessions/rotation` is downstream of `internal/sessions` (no back-edge — the contract is closures over primitive types so the rotation package never imports its host). `internal/supervisor` has no upward imports — verifiable with `go list -deps ./internal/supervisor/...`.

## Data Flow

### Interactive Session

```
User terminal
    │
    ├── stdin ──────> pyry ──> PTY master fd ──> claude (child process)
    │
    └── stdout <───── pyry <── PTY master fd <── claude (child process)
```

The supervisor puts the controlling terminal into raw mode so keystrokes pass through unmodified. SIGWINCH signals are forwarded to the PTY so terminal resizes propagate to the child.

### Restart Cycle

```
supervisor.Run()
    │
    ├── runOnce() ──> spawn claude in PTY, bridge I/O
    │                 wait for child exit
    │
    ├── child exited? ──> apply backoff delay
    │                     if uptime > resetAfter: reset backoff to initial
    │                     respawn with --continue (after first run, when ResumeLast is true)
    │
    └── ctx cancelled? ──> graceful shutdown
```

### Backoff Strategy

- Initial delay: 500ms
- Doubles on each restart: 500ms → 1s → 2s → 4s → ... → 30s (max)
- Resets to initial if the child stayed up longer than 60s (stability indicator)
- Context cancellation (SIGINT/SIGTERM) breaks out of the backoff wait

## Key Types

### `supervisor.Config`

All supervisor configuration in a single struct. Passed to `supervisor.New()`.

Fields: ClaudeBin (path to claude), WorkDir (child's cwd), ResumeLast (use --continue after first run), ClaudeArgs (pass-through args), Bridge (optional service-mode I/O mediator), Logger (*slog.Logger), backoff params (Initial, Max, Reset durations).

### `supervisor.Supervisor`

Owns the child process lifecycle. Two methods:
- `New(cfg Config) (*Supervisor, error)` — validates config, applies defaults
- `Run(ctx context.Context) error` — the main loop: spawn, wait, backoff, repeat

### `supervisor.backoffTimer`

Extracted backoff logic. Computes the next delay based on how long the previous child ran:
- `next(uptime time.Duration) time.Duration` — returns the delay and advances internal state
- `reset()` — returns to initial delay

## Platform Support

- **Linux:** Primary target. systemd user unit for daemon management.
- **macOS:** Supported. launchd plist provided. Cross-compile verified for darwin/amd64 and darwin/arm64.
- **Windows:** Out of scope. Would need ConPTY instead of Unix PTY, different signal handling, and a service wrapper.

## Dependencies

| Module | Purpose | Why not stdlib |
|--------|---------|----------------|
| `creack/pty` | PTY allocation and size management | No stdlib PTY support |
| `fsnotify/fsnotify` | Live `/clear` rotation detection on the claude sessions dir (Phase 1.2b-B) | Cross-platform inotify+kqueue without owning two stacks. See [ADR 004](../decisions/004-fsnotify-for-rotation-detection.md). |
| `golang.org/x/term` | Terminal raw mode, state save/restore | Extended terminal ops not in stdlib |
| `golang.org/x/sync` | `errgroup` for `Pool.Run`'s bootstrap+watcher fan-out (Phase 1.1+ extends to N sessions) | Semi-official extension; clearer than ad-hoc 2-goroutine coordination |
| `golang.org/x/sys` | System calls (indirect, via x/term and fsnotify) | — |

### Session Registry (Phase 1.2a)

```
~/.pyry/<sanitized-name>/sessions.json    (file 0600, dir 0700)
~/.pyry/<sanitized-name>.sock             (sibling — single-writer per name)
```

`Pool.New` reads the registry on startup. Missing or empty file → cold start (mint UUID, write file). Valid file → warm start (reuse persisted UUID, no rewrite). Malformed JSON → fatal at startup.

`saveLocked` writes via `os.CreateTemp` → fsync → `os.Rename` in the same directory. Rename is the commit point; partial JSON is unreachable in the target file. Called under `Pool.mu` (write) by mutating ops; in 1.2a only `Pool.New`'s cold-start path invokes it.

Forward-compat: `version` is a future hook; unknown top-level and per-session fields are silently ignored on read.

### Startup JSONL Reconciliation (Phase 1.2b-A)

```
~/.claude/projects/<encoded-cwd>/<uuid>.jsonl    (claude's own files)
```

`Pool.New` scans the per-workdir claude session dir, finds the most-recently-modified `<uuid>.jsonl`, and rotates the registry's bootstrap entry to that UUID if it disagrees. Self-heals across `/clear` (claude rotates UUIDs on `/clear`; without reconciliation, post-`pyry stop` the registry would still point at the pre-`/clear` UUID).

`encodeWorkdir` maps cwd → claude's path component by replacing both `/` and `.` with `-`. The pre-rotation JSONL is never modified — only the registry pointer moves. Missing/unreadable claude dir is logged and ignored (startup proceeds with the existing bootstrap). The mutation goes through `Pool.RotateID`, the load-bearing seam reused by Phase 1.2b-B's live-detection watcher.

### Live `/clear` Rotation Watcher (Phase 1.2b-B)

```
Pool.Run (errgroup)
    ├── bootstrap.Run(gctx)
    └── rotation.Watcher.Run(gctx)
              │
              ▼
        fsnotify CREATE on ~/.claude/projects/<encoded-cwd>/<new>.jsonl
              │
              ▼
        IsAllocated(<new>)? → consume + skip (Phase 1.1's --session-id mints)
        Snapshot()         → [{id: <old>, pid}, ...]
        probeWithRetry(pid) → /proc/<pid>/fd walk (Linux) or `lsof -F fn` (Darwin)
              │
              ▼
        match → OnRotate(<old>, <new>) → Pool.RotateID
```

`internal/sessions/rotation` is its own package, dependency-direction-respecting (no import of `internal/sessions`). The contract is `rotation.Config` closures over primitive types, wired in `Pool.Run`. Watcher disabled (and pyry startup proceeds) when `ClaudeSessionsDir` is empty, `fsnotify` init fails, or — on darwin — `lsof` is missing from PATH (`noopProbe` fallback). See [features/rotation-watcher.md](../features/rotation-watcher.md) and [ADR 004](../decisions/004-fsnotify-for-rotation-detection.md).

## Future Architecture (not yet implemented)

- **Phase 1.1+:** `Pool.Add(SessionConfig)`, N-session fan-out in `Pool.Run`'s errgroup (the wrapper landed in 1.2b-B), `Request.SessionID` on the wire, `pyry sessions new` calling `Pool.RegisterAllocatedUUID` before `claude --session-id <uuid>`, `pyry attach <id>`, per-session log lines
- **Phase 2:** Channels — inbound event routing from Discord/Telegram
- **Phase 3:** Cross-cutting services — knowledge capture, memsearch, cron runner in-process
- **Phase 4:** Remote access — relay server, E2E encryption (Noise Protocol), QR pairing
- **Phase 5:** Voice — WebRTC via pion/webrtc, STT/TTS pipeline
