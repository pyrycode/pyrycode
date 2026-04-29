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
├── systemd/pyry.service       Linux systemd user unit
└── launchd/dev.pyrycode.pyry.plist   macOS launchd plist
```

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
| `golang.org/x/term` | Terminal raw mode, state save/restore | Extended terminal ops not in stdlib |
| `golang.org/x/sys` | System calls (indirect, via x/term) | — |

## Future Architecture (not yet implemented)

- **Phase 1:** Multi-session — session registry, routing API, lifecycle management
- **Phase 2:** Channels — inbound event routing from Discord/Telegram
- **Phase 3:** Cross-cutting services — knowledge capture, memsearch, cron runner in-process
- **Phase 4:** Remote access — relay server, E2E encryption (Noise Protocol), QR pairing
- **Phase 5:** Voice — WebRTC via pion/webrtc, STT/TTS pipeline
