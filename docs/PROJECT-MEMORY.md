# Project Memory — Pyrycode

Repo-level session memory. Read this at the start of every session.

## What's Built

### Codebase (Phase 0)
- **Supervisor core** — PTY spawn via `creack/pty`, transparent stdin/stdout bridging with raw terminal mode, exponential backoff restart with stability reset, `--resume` flag for session persistence across crashes
- **SIGWINCH forwarding** — terminal resizes propagate from controlling terminal to child PTY
- **CLI** — flags for claude binary path, workdir, resume, verbose; subcommands for version/status/help
- **Graceful shutdown** — SIGINT/SIGTERM cancel the supervisor context, child is cleaned up
- **Service configs** — systemd user unit (`systemd/pyry.service`), macOS launchd plist (`launchd/dev.pyrycode.pyry.plist`)
- **~400 lines across 3 Go files**, 2 commits

### Documentation
- README, plan.md (phase roadmap), CLAUDE.md, CODING-STYLE.md
- Knowledge base: system-overview, 2 ADRs (Go language, PTY supervisor)
- This file, lessons.md

### Infrastructure
- GitHub Actions CI: go vet, staticcheck, go test -race
- QMD search: pyrycode-docs, pyrycode-root collections
- .claude/settings.json with safety rules

## Patterns Established

- **Config struct pattern** — all configuration in a single `Config` struct, defaults applied in `New()`
- **Context-based cancellation** — `context.Context` flows through `Run()` → `runOnce()`, checked at every wait point
- **Structured logging** — `log/slog` with injected logger, not a global
- **Exponential backoff with stability reset** — backoff doubles on restart, resets to initial if child stayed up longer than `BackoffReset`
- **Deferred cleanup** — `defer` for terminal restore, PTY close, signal stop

## Open Questions

- **Backoff cooldown/bail-out** — if crashes happen N times in T seconds, should the supervisor give up? Currently retries forever.
- **Session ID tracking** — `--resume` uses Claude Code's built-in heuristic. Should we track session IDs explicitly and pass `--resume <id>`?
- **Control socket design** — `pyry status`, `pyry logs`, `pyry attach` need a Unix socket protocol. What commands? What format (JSON, plain text, protobuf)?
- **Real production test** — supervisor hasn't been tested with a real `claude` child on pyrybox. The tmux setup is still running.
