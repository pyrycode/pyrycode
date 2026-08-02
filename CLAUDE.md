# Pyrycode

## Headless Mode

If you are running headless (`claude -p`, dispatch, cron), do not prompt for input. Make decisions, document your reasoning, and move on. Read `docs/PROJECT-MEMORY.md` and `docs/knowledge/INDEX.md` before starting any work.

## Project

Pyrycode is a process supervisor for Claude Code. It wraps the `claude` CLI in a long-lived daemon (`pyry`) that handles crash recovery, session persistence, multi-session routing, and remote access (live mobile relay). Channels integration and voice remain on the roadmap.

- **Language:** Go
- **Binary:** `pyry`
- **Repo:** `github.com/pyrycode/pyrycode`
- **License:** MIT
- **Platforms:** Linux + macOS (Windows out of scope)

## Architecture

`internal/` holds ~33 packages. The load-bearing ones:

```
cmd/pyry/                      Binary entry point: CLI parsing, daemon composition root
cmd/substrate-guard/           Build gate: no claude-TUI substrate literals outside tui-driver
internal/supervisor/           PTY-hosted claude supervision (rollback interactive path)
internal/streamsup/            Stream-json supervision (production interactive path)
internal/sessions/             Multi-session pool: registry, /clear rotation, idle eviction
internal/agentrun/             `pyry agent-run` runners (ptyrunner, streamrunner/streamjson)
internal/control/              Unix-socket control plane (status, logs, attach, stop)
internal/relay/ + transport/   Remote access: relay WSS client + handlers
internal/noise/ + keys/        Noise_IK E2E crypto + static keypair
internal/protocol/             Mobile wire-format types
internal/e2e/                  Fake-daemon e2e harness (fakeclaude/fakerelay/fakephone)
launchd/                       macOS service config
systemd/                       Linux service config
docs/                          Knowledge base, specs, project memory
```

The full architecture picture — every package, data flow, key types — lives in `docs/knowledge/architecture/system-overview.md`. Trust it over this list.

## Build Commands

```bash
make check            # hermetic CI gate: vet + race tests + staticcheck + substrate-guard + fake-claude e2e
make build            # build ./pyry
make e2e-realclaude   # opt-in: live-claude suite (needs real creds, spends tokens)
make e2e-liverelay    # opt-in: one round-trip against a real relay binary (hermetic loopback)
make preship          # full pre-binary-swap gate: check + e2e-realclaude + e2e-liverelay
```

See `docs/release-tooling.md` for the test-tier runbook (including the opt-in `e2e-install` / `e2e-update` suites on the release checklist).

## Conventions

See `CODING-STYLE.md` for Go-specific conventions covering package layout, error handling, naming, testing patterns, and concurrency.

Key points:
- `gofmt` is non-negotiable
- `log/slog` for all logging, structured fields
- Return errors, don't panic
- Wrap errors with context: `fmt.Errorf("doing X: %w", err)`
- Table-driven tests, stdlib `testing` only (no testify)
- `context.Context` for cancellation everywhere

## Documentation Structure

```
docs/
  knowledge/                   Evergreen documentation
    INDEX.md                   One-line summary per doc (documentation phase is the sole writer)
    architecture/              System design, module interactions
    codebase/                  Per-ticket implementation + patterns + lessons (<N>.md; the directory listing is its index)
    decisions/                 ADRs (numbered: 001-*.md)
    features/                  Feature documentation
  specs/                       Per-ticket build artifacts
  PROJECT-MEMORY.md            Index of where things live — read-only for agents
  lessons.md                   Frozen 2026-05-11 — historical reference only
  plan.md                      Phase roadmap
```

- **Knowledge docs** are evergreen — updated when things change, not appended to
- **Specs** are build-time artifacts created during ticketed work
- **PROJECT-MEMORY.md** is read-only for agents (since 2026-05-11); humans maintain it directly
- **lessons.md** is frozen (2026-05-11); new lessons go in the ticket's `docs/knowledge/codebase/<N>.md`

## Search Before You Build

Use QMD to search project documentation before writing code or making decisions:

```
mcp__qmd__query(collection: "pyrycode-docs", query: "backoff restart strategy")
mcp__qmd__query(collection: "pyrycode-root", query: "error handling convention")
```

**Collections:**
- `pyrycode-docs` — indexes `docs/` (knowledge base, specs, lessons, project memory)
- `pyrycode-root` — indexes root markdown (CLAUDE.md, CODING-STYLE.md, README.md) and `agents/**/CLAUDE.md`

Use **Context7** for Go library documentation:
```
mcp__context7__resolve-library-id(libraryName: "creack/pty")
mcp__context7__query-docs(context7CompatibleLibraryID: "<id>", topic: "start process in pty")
```

**Always search QMD before:** writing new code, making architectural decisions, creating new files, fixing bugs. The answer may already be documented.

**Always `qmd update && qmd embed` after:** adding or modifying docs. `embed` alone doesn't detect new files.

## Session Memory

**On start:**
1. Read `docs/PROJECT-MEMORY.md` — index of where things live
2. Read `docs/knowledge/INDEX.md` — one-line map of the evergreen docs
3. Read `docs/knowledge/features/` for the area you touch; architecture truth is `docs/knowledge/architecture/system-overview.md`

**During work:**
- Update `docs/knowledge/` evergreen docs when the thing they describe changes

**On finish:**
- Verify all knowledge changes are committed
- Run `qmd update && qmd embed` if docs changed

## Knowledge Capture

When making decisions, discovering gotchas, or completing features:

| What happened | Where to write |
|---|---|
| Chose X over Y with reasoning | `docs/knowledge/decisions/NNN-*.md` (ADR) |
| How a feature works | `docs/knowledge/features/*.md` |
| System design, module interactions | `docs/knowledge/architecture/*.md` |
| Per-ticket implementation, patterns, lessons | `docs/knowledge/codebase/<N>.md` — written by the pipeline's documentation phase |

Update `docs/knowledge/INDEX.md` when adding new knowledge docs.

Do **not** write to `docs/lessons.md` (frozen 2026-05-11) or `docs/PROJECT-MEMORY.md` (read-only for agents). Lessons and status land in the ticket's `docs/knowledge/codebase/<N>.md` via the documentation phase.

## Testing

Tiers, cheapest first — `docs/release-tooling.md` is the runbook:

- **`make check` (hermetic, the standard gate):** vet + race-enabled unit tests + staticcheck + substrate-guard + the fake-daemon e2e suite (real daemons against fakeclaude/fakerelay/fakephone; no creds, no network)
- **`make e2e-realclaude` (opt-in):** live-claude suite — needs real claude creds, spends real tokens
- **`make e2e-liverelay` (opt-in):** one round-trip against a locally built real relay binary — hermetic loopback, no creds
- **`make preship`:** check + e2e-realclaude + e2e-liverelay — run before every binary swap

Conventions:

- **Unit tests:** Table-driven, `go test -race`, stdlib only
- **No mocking frameworks.** Use interfaces and simple test doubles.
- Tests for PTY-dependent code must handle non-TTY environments (CI runners have no terminal)

## Working Principles

1. **Simplicity first.** Don't add abstraction layers before something needs them.
2. **Stdlib over dependencies.** Go's standard library is excellent. Add external deps only when they provide significant value (like `creack/pty`).
3. **Errors are values.** Handle them explicitly. Never ignore errors silently.
4. **Context flows down.** Every long-running operation takes a `context.Context`.
5. **Test the behavior, not the implementation.** Tests should verify what the supervisor does, not how it's wired internally.
