# Knowledge Base Index

Evergreen documentation for Pyrycode. Updated as things change, not appended to.

Search with QMD: `mcp__qmd__query(collection: "pyrycode-docs", query: "your query")`

## Architecture

| File | Topic |
|------|-------|
| [system-overview.md](architecture/system-overview.md) | Module structure, data flows, platform support |

## Decisions

| # | File | Decision |
|---|------|----------|
| 001 | [001-go-language.md](decisions/001-go-language.md) | Go as the implementation language |
| 002 | [002-pty-supervisor.md](decisions/002-pty-supervisor.md) | PTY-level wrapping over alternatives |
| 003 | [003-session-addressable-runtime.md](decisions/003-session-addressable-runtime.md) | `internal/sessions` Pool wraps the supervisor for additive Phase 1.1+ |
| 004 | [004-fsnotify-for-rotation-detection.md](decisions/004-fsnotify-for-rotation-detection.md) | `fsnotify` for live `/clear` detection (over polling or raw inotify+kqueue) |
| 005 | [005-idle-eviction-state-machine.md](decisions/005-idle-eviction-state-machine.md) | Per-session two-state machine + explicit `Activate` for idle eviction / lazy respawn |
| 006 | [006-concurrent-active-cap-lru.md](decisions/006-concurrent-active-cap-lru.md) | `Config.ActiveCap` + LRU victim selection at `Pool.Activate`; force-eviction `Session.Evict` primitive |

## Features

| File | Topic |
|------|-------|
| [sessions-package.md](features/sessions-package.md) | `internal/sessions` — `SessionID`, `Session`, `Pool`; supervisor wrapper for multi-session readiness |
| [sessions-registry.md](features/sessions-registry.md) | `~/.pyry/<name>/sessions.json` — schema, atomic write, load semantics; sessions survive `pyry stop` |
| [jsonl-reconciliation.md](features/jsonl-reconciliation.md) | Startup scan of `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`; `Pool.RotateID` self-heals registry across `/clear` |
| [rotation-watcher.md](features/rotation-watcher.md) | Live `/clear` detection: fsnotify on the claude dir + per-PID FD probe (Linux `/proc/<pid>/fd`, macOS `lsof`) drives `Pool.RotateID` |
| [control-plane.md](features/control-plane.md) | `internal/control` — Unix-socket JSON server, `SessionResolver` seam, verb dispatch, attach handoff |
| [idle-eviction.md](features/idle-eviction.md) | Per-session active↔evicted state machine; idle timer + concurrent-active-cap (LRU) eviction triggers; `Activate` / `Evict` primitives |
| [e2e-harness.md](features/e2e-harness.md) | `internal/e2e` (build tag `e2e`) — `Harness` + `Start(t)` spawn pyry in temp HOME, poll socket for ready, SIGTERM/SIGKILL teardown, leak verification via re-exec |
