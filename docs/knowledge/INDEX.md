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

## Features

| File | Topic |
|------|-------|
| [sessions-package.md](features/sessions-package.md) | `internal/sessions` — `SessionID`, `Session`, `Pool`; supervisor wrapper for multi-session readiness |
| [sessions-registry.md](features/sessions-registry.md) | `~/.pyry/<name>/sessions.json` — schema, atomic write, load semantics; sessions survive `pyry stop` |
| [jsonl-reconciliation.md](features/jsonl-reconciliation.md) | Startup scan of `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`; `Pool.RotateID` self-heals registry across `/clear` |
| [control-plane.md](features/control-plane.md) | `internal/control` — Unix-socket JSON server, `SessionResolver` seam, verb dispatch, attach handoff |
