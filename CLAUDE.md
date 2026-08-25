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
internal/streamsup/            Stream-json supervision (production interactive path)
internal/sessions/             Multi-session pool: registry, /clear rotation, idle eviction
internal/agentrun/             `pyry agent-run` runner (streamrunner/streamjson; the terminal runner was deleted in #1348)
internal/control/              Unix-socket control plane (status, logs, stop, sessions.*, rekey, mcp.approve)
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

## Memory index entries

The Claude Code memory index is the discovery map every dispatched agent reads at
startup. The dispatcher keeps it small on its own, but it is only allowed to drop
an entry whose title starts with a ticket number. Every other entry is protected
for good, and once the protected part grows past the watermark it forces a slow,
expensive curation pass that blocks the next dispatch.

So the title decides the entry's lifetime. Pick by what the note is:

- **A note about one ticket's work.** Start the title with the ticket number, as
  in `#784 unrecognized-message arm widening`. Stars, bold and status text go
  after the number, never in front of it. The dispatcher retires these for free
  once they age out, and the note file itself stays on disk either way.
- **A lesson that outlives its ticket.** Start the title with a word, as in
  `structured clone preserves an undefined property`. These stay indexed until a
  curation pass relocates them by hand.

A star or an emoji in front of the number is what breaks this, because it stops
the title starting with a digit and the entry is read as a permanent lesson.
Measured on 2026-08-25: six finished ticket notes were holding 11 KB of this
index that way, and curation fired 30 times in a single day as a result.

## Testing

Tiers, cheapest first — `docs/release-tooling.md` is the runbook:

- **`make check` (hermetic, the standard gate):** vet + race-enabled unit tests + staticcheck + substrate-guard + the fake-daemon e2e suite (real daemons against fakeclaude/fakerelay/fakephone; no creds, no network)
- **`make e2e-realclaude` (opt-in):** live-claude suite — needs real claude creds, spends real tokens
- **`make e2e-liverelay` (opt-in):** one round-trip against a locally built real relay binary — hermetic loopback, no creds
- **`make preship`:** check + e2e-realclaude + e2e-liverelay — run before every binary swap

**`make check` cannot see the live-claude suite, so its green says nothing about that package.** `internal/e2e/realclaude` is gated behind the `e2e_realclaude` build tag, so the standard gate never compiles it. The package can fail to build while `make check` passes honestly. `make preship` is the gate that compiles it.

**After deleting or moving test files, run `make preship`, not `make check`.** Deleting a test file also deletes whatever shared helpers lived in it, and those helpers are often used by tests in other files. On 2026-08-16 a deletion removed seven files from `internal/e2e/realclaude` and took thirty shared helpers with them; fourteen surviving tests still called them, and the package did not compile for a day while every `make check` stayed green.

**Read the count of tests executed from the live suite, never its exit code.** There are two ways it reports success while proving nothing, and they look identical from outside:

- No claude credentials: every live test skips, exit 0.
- The package fails to build: zero tests run, exit 0 through any shell wrapper.

Count the `=== RUN` lines. A healthy full run is in the 700s as of August 2026 and reads zero when the build is broken. Roughly a dozen tests skip by design on every run — opt-in evidence probes behind their own environment flags, plus MCP smoke tests needing `ANTHROPIC_API_KEY`, which is a different credential from the subscription login. Read the skip reasons; the skip count alone cannot tell design from breakage.

**A test that writes a fixture does not commit it, and a pipeline run throws the file away.** Agent runs happen in a detached worktree that is removed when the run ends, so anything a test writes under `testdata/` goes out with it. Only what reaches a commit survives. On 2026-08-25 #1763's live gate ran its three-arm `initialize` capture green — 749 checks executed, 0 failed, real tokens spent — and landed zero of the three artifacts its acceptance criteria asked for. That blocked #1764, which reads them, until the capture was re-run by hand from the main checkout and committed. **A ticket that asks for a captured artifact must have the agent `git add` what the run wrote**; #1688's PR is the pattern to copy. A green gate and a spent budget look identical whether the bytes landed or not.

Conventions:

- **Unit tests:** Table-driven, `go test -race`, stdlib only
- **No mocking frameworks.** Use interfaces and simple test doubles.

## Working Principles

1. **Simplicity first.** Don't add abstraction layers before something needs them.
2. **Stdlib over dependencies.** Go's standard library is excellent. Add external deps only when they provide significant value (like `creack/pty`).
3. **Errors are values.** Handle them explicitly. Never ignore errors silently.
4. **Context flows down.** Every long-running operation takes a `context.Context`.
5. **Test the behavior, not the implementation.** Tests should verify what the supervisor does, not how it's wired internally.
