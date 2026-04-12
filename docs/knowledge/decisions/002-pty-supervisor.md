# ADR 002: PTY-Level Process Wrapping

**Date:** 2026-04-10
**Status:** Accepted
**Context:** Claude Code is a TUI application that expects a terminal. Need to supervise it while preserving full interactive functionality.

## Decision

Wrap claude in a pseudo-terminal (PTY) using `creack/pty`, bridging the PTY's master fd to the supervisor's stdin/stdout.

## Rationale

- **Claude Code expects a terminal** — it renders ANSI sequences, handles cursor positioning, reads raw keystrokes. Piping stdin/stdout without a PTY breaks interactive features.
- **Transparent passthrough** — with the supervisor's terminal in raw mode, every keystroke reaches claude unmodified. The user can't tell there's a supervisor in between.
- **SIGWINCH forwarding** — terminal resizes propagate from the real terminal → supervisor → PTY → claude. Claude Code sees the correct dimensions.
- **Foundation for multiplexing** — a PTY-per-session model naturally extends to Phase 1 multi-session support.
- **Proven pattern** — Happy (`slopus/happy`) validates this approach at production scale.

## Alternatives Considered

- **Pipe stdin/stdout** — breaks TUI features. Claude Code would fall back to non-interactive mode or crash.
- **tmux wrapper** — what we're replacing. Adds a dependency, tmux's session model doesn't map cleanly to programmatic control, and scripting tmux is fragile.
- **screen** — similar problems to tmux, older, less maintained.
- **Direct terminal inheritance** — supervisor gives up its own terminal to claude. Works for single-session but prevents the supervisor from logging or multiplexing.

## Consequences

- **Platform-specific code** — PTY APIs differ between Unix and Windows (ConPTY). This is why Windows is out of scope.
- **Raw mode management** — the supervisor must save/restore terminal state correctly, especially on crashes. Deferred cleanup via `defer term.Restore()`.
- **Testing complexity** — PTY-dependent code can't be fully tested in headless CI. Use `TestHelperProcess` pattern with fake child processes and skip PTY assertions when `!term.IsTerminal(stdin)`.
- **Goroutine lifecycle** — two goroutines bridge stdin→PTY and PTY→stdout. Both must be cleaned up when the child exits to avoid leaks.
