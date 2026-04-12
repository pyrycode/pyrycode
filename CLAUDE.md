# Pyrycode

## Headless Mode

If you are running headless (`claude -p`, dispatch, cron), do not prompt for input. Make decisions, document your reasoning, and move on. Read `docs/PROJECT-MEMORY.md` and `docs/lessons.md` before starting any work.

## Project

Pyrycode is a process supervisor for Claude Code. It wraps the `claude` CLI in a long-lived daemon (`pyry`) that handles crash recovery, session persistence, and eventually multi-session routing, Channels integration, and remote access.

- **Language:** Go
- **Binary:** `pyry`
- **Repo:** `github.com/pyrycode/pyrycode`
- **License:** MIT
- **Platforms:** Linux + macOS (Windows out of scope)

## Architecture

```
cmd/pyry/main.go              Entry point: CLI flags, signal handling, supervisor init
internal/supervisor/
  supervisor.go                Core loop: PTY spawn, I/O bridge, backoff restart
  backoff.go                   Backoff timer: exponential delay with reset-on-long-uptime
  winsize.go                   SIGWINCH forwarding to PTY
launchd/                       macOS service config
systemd/                       Linux service config
docs/                          Knowledge base, specs, project memory
agents/                        Agent instruction files
```

### Data Flow

```
Terminal stdin ──> pyry main ──> supervisor.Run() ──> PTY ──> claude process
Terminal stdout <──────────────────────────────────── PTY <── claude process

On child exit:
  supervisor.Run() applies backoff, respawns with --resume
```

### Key Types

- `supervisor.Config` — all configuration: claude binary path, work dir, resume flag, backoff params, logger
- `supervisor.Supervisor` — owns the child process lifecycle
- `supervisor.backoffTimer` — computes restart delays with exponential backoff and reset-on-stability

## Build Commands

```bash
go build -o pyry ./cmd/pyry     # Build the binary
go test -race ./...              # Run all tests with race detector
go vet ./...                     # Static analysis
staticcheck ./...                # Extended static analysis (install: go install honnef.co/go/tools/cmd/staticcheck@latest)
```

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
    INDEX.md                   One-line summary per doc
    architecture/              System design, module interactions
    decisions/                 ADRs (numbered: 001-*.md)
    features/                  Feature documentation
  specs/                       Per-ticket build artifacts
  PROJECT-MEMORY.md            What's built, patterns, open questions
  lessons.md                   Gotchas and anti-patterns
  plan.md                      Phase roadmap
```

- **Knowledge docs** are evergreen — updated when things change, not appended to
- **Specs** are build-time artifacts created during ticketed work
- **PROJECT-MEMORY.md** is the repo-level session memory (distinct from the Obsidian vault's PROJECT-MEMORY)
- **lessons.md** captures mistakes so they aren't repeated

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
1. Read `docs/PROJECT-MEMORY.md` — what's built, patterns, open questions
2. Read `docs/lessons.md` — mistakes to avoid
3. If working on a specific area, search `pyrycode-docs` for relevant knowledge

**During work:**
- Update `docs/knowledge/` when you learn something that should persist
- Update `docs/lessons.md` when something goes wrong or is surprising
- Update `docs/PROJECT-MEMORY.md` when completing significant work

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
| Something broke or surprised you | `docs/lessons.md` |
| Milestone or status change | `docs/PROJECT-MEMORY.md` |

Update `docs/knowledge/INDEX.md` when adding new knowledge docs.

## Testing

- **Unit tests:** Table-driven, `go test -race`, stdlib only
- **Integration tests:** `TestHelperProcess` pattern — test binary re-execs as fake child
- **CI:** GitHub Actions runs `go vet`, `staticcheck`, `go test -race` on every push and PR
- **No mocking frameworks.** Use interfaces and simple test doubles.
- Tests for PTY-dependent code must handle non-TTY environments (CI runners have no terminal)

## Working Principles

1. **Simplicity first.** This is a ~400 line project. Don't add abstraction layers it doesn't need yet.
2. **Stdlib over dependencies.** Go's standard library is excellent. Add external deps only when they provide significant value (like `creack/pty`).
3. **Errors are values.** Handle them explicitly. Never ignore errors silently.
4. **Context flows down.** Every long-running operation takes a `context.Context`.
5. **Test the behavior, not the implementation.** Tests should verify what the supervisor does, not how it's wired internally.
