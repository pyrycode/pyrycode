# ADR 001: Go as Implementation Language

**Date:** 2026-04-10
**Status:** Accepted
**Context:** Need a language for a process supervisor daemon that runs on Linux and macOS.

## Decision

Go.

## Rationale

- **Single static binary** — no runtime dependencies, drop-in install. `go build` produces one file.
- **Process supervision is Go's sweet spot** — goroutines for concurrent I/O bridging, `os/exec` for child management, `os/signal` for signal handling, `context.Context` for cancellation. All stdlib.
- **PTY support** — `creack/pty` is the standard Go PTY library, mature and maintained.
- **Cross-compilation** — `GOOS=darwin GOARCH=arm64 go build` just works. Verified for darwin/amd64 and darwin/arm64 with zero code changes.
- **Future phases benefit** — `pion/webrtc` (pure Go WebRTC) for voice chat, `coreos/go-systemd` for deeper systemd integration, `flynn/noise` for Noise Protocol crypto.
- **Fast startup, low memory** — daemon starts in milliseconds, uses ~20MB RSS. Appropriate for an always-on service.

## Alternatives Considered

- **Rust** — similarly good for systems work, but slower compile times, steeper learning curve, and the PTY ecosystem is less mature.
- **Node.js/TypeScript** — wrong model for a long-running daemon. Runtime dependency, higher memory, and the npm distribution model doesn't fit a system service.
- **Python** — too slow for a process supervisor, no static binary story, GIL complicates concurrent I/O.
- **Bash** — what we're replacing. No type safety, no structured error handling, no testing infrastructure.

## Consequences

- Team (Juhana + AI agents) needs Go proficiency.
- Verbose error handling (no exceptions) — but this is a feature for a supervisor where every error path matters.
- No REPL for interactive development — compensate with fast compile-run cycles.
