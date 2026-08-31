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

The Claude Code memory index is injected into every agent run and it is capped. Keep tickets out of it entirely.

**Never name a ticket in a memory index entry.** No ticket number in the title, none in the note filename. A ticket's knowledge belongs in the repo, where it is committed, reviewed and unbounded. The documentation phase folds each ticket's lessons into the package overview at `docs/knowledge/features/<package>.md`. A refinement, split or prerequisite decision belongs on the ticket itself as a comment, which the dispatcher already feeds to every later agent on that ticket.

**The index is only for a lesson that outlives its ticket.** Title it with what was learned and no number at all, as in `structured clone preserves an undefined property`. Cite the ticket in the summary if that helps, never in the title and never in the filename.

The dispatcher enforces this deterministically. Between cycles it strips every entry whose title or note filename carries a ticket number, so an entry that names one disappears on its own. The note file stays on disk; only the pointer goes. Writing one is not dangerous, it is just wasted.

## Package overview size

**A document under `docs/knowledge/features/` is capped at 50000 bytes.** Past that it stops being findable. Search cuts a document into roughly 900-token chunks and prefers to cut at a heading, scoring `#` highest down to a bare line break lowest, but it only searches a narrow window around each 900-token mark. When a document's sections are much larger than one chunk, the nearest heading falls outside that window, so most chunks are cut at a paragraph break and carry no heading. Measured 2026-08-31 against the then-315KB `v2-session-manager.md`, whose sections averaged 7000 bytes: three query shapes aimed at a topic whose canonical home was a section of that file, and none of the three returned it. The cap keeps sections near chunk size so the heading preference can actually fire. A lesson folded into a document that size is a lesson lost.

**Past the cap, split it.** Cut at `##` headings, and where a `##` section is itself over the cap cut it at its `###` headings. Each child is named `<package>-<section>.md` and lives beside its parent. **The parent keeps its own path**, its title and its intro, and its body becomes a map linking to the children: every agent prompt names the parent path and hundreds of documents link to it. A section under 3000 bytes stays in the parent rather than becoming a file of its own.

`make docs-guard`, wired into `make check`, fails the build on an over-cap document and on a line that parses as a heading because a wrapped paragraph put a ticket reference first.

Why this rule exists, measured 2026-08-25: the old classifier keyed on the title's first character, ticket entries titled with a decoration in front of the number were read as permanent lessons, and 216 curation passes and about 34 hours of blocked dispatch went into hand-trimming a file that should never have held them.
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
