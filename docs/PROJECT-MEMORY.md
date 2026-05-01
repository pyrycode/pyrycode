# Project Memory — Pyrycode

Repo-level session memory. Read this at the start of every session.

## What's Built

### Codebase (Phase 0)
- **Supervisor core** — PTY spawn via `creack/pty`, raw-mode stdin/stdout bridging in foreground mode, Bridge-mediated I/O in service mode, exponential backoff restart with stability reset, `--continue` injection on restart for session persistence
- **SIGWINCH forwarding** — terminal resizes propagate from controlling terminal to child PTY (foreground mode only; attach mode locks the size at attach time)
- **Control plane** — Unix domain socket (`~/.pyry/<name>.sock`, 0600), line-delimited JSON protocol, verbs: `status`, `stop`, `logs`, `attach`
- **CLI transparency** — unknown args forward verbatim to claude; pyry's own flags use `-pyry-*` prefix; `-pyry-name` plus `PYRY_NAME` env var for named multi-instance
- **Graceful shutdown** — SIGINT/SIGTERM cancel the supervisor context, child is killed via `exec.CommandContext`, socket removed on exit
- **Service configs** — systemd user unit (`systemd/pyry.service`), macOS launchd plist (`launchd/dev.pyrycode.pyry.plist`)
- **~1700 source + ~1100 test Go lines** as of late Apr 2026, 10+ PRs merged

### Codebase (Phase 1.0a, ticket #28)
- **`internal/sessions` package** — `SessionID` (UUIDv4 via `crypto/rand`, stdlib only), `Session` (wraps one `*supervisor.Supervisor` + optional `*supervisor.Bridge`), `Pool` (single-bootstrap registry with `RWMutex`-protected map). Sentinel errors `ErrSessionNotFound`, `ErrAttachUnavailable`. `Pool.Lookup("")` resolves to the bootstrap entry — the seam Phase 1.1's `Request.SessionID` plugs into. **No production consumers yet** — `cmd/pyry/main.go` and `internal/control` still use `*supervisor.Supervisor` directly. Child B (#29) flips them.
- See [knowledge/features/sessions-package.md](knowledge/features/sessions-package.md) and [ADR 003](knowledge/decisions/003-session-addressable-runtime.md).

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
- **Empty ID resolves to default** — `Pool.Lookup("")` returns the bootstrap session, so future `req.SessionID` fields can be added with no handler-side branching (old clients send empty, get the bootstrap; new clients send a real ID, get the right entry)
- **Introduce-then-rewire slicing** — split #27 into #28 (new package + tests, no consumers) and #29 (mechanical consumer rewiring) to keep each PR focused

## Open Questions

- **Backoff cooldown/bail-out** — if crashes happen N times in T seconds, should the supervisor give up? Currently retries forever, which is the right default for a service supervisor (a supervised child that never starts is the operator's problem to investigate, not for pyry to give up on).
- **Phase 0.5 — Real production test** — supervisor hasn't been tested with a real `claude` child on pyrybox running as a launchd/systemd service. The tmux setup is still running. This is the only Phase 0 item left after PRs #1-#10.

(Earlier "Session ID tracking" and "Control socket design" questions were resolved by the PR series that landed Phase 0.2–0.4: `--continue` for session continuity, line-delimited JSON over a Unix socket for control.)
