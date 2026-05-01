# Lessons Learned

Gotchas, anti-patterns, and mistakes. Read this before every session so you don't repeat them.

## QMD Indexing

- **Always `qmd update && qmd embed`, never just `qmd embed`.** `embed` only refreshes vectors for already-indexed files. It does NOT detect new, changed, or deleted files. Without `update`, the index goes stale and agents find references to nonexistent files.

## PTY Testing

- **CI runners have no TTY.** GitHub Actions `ubuntu-latest` has no controlling terminal. Code that calls `term.IsTerminal()` will return false. Tests must either:
  - Use `TestHelperProcess` with fake child processes (no real PTY needed)
  - Skip PTY-specific assertions with `if !term.IsTerminal(os.Stdin.Fd()) { t.Skip("no TTY") }`
  - Test the logic (backoff, config, parsing) separately from the PTY I/O

## Cross-Platform

- **`creack/pty` and `golang.org/x/term` both support darwin natively.** Cross-compile for macOS works with zero code changes. Verified for darwin/amd64 and darwin/arm64.
- **Windows would need ConPTY** — completely different API. Out of scope.

## Test helpers across packages

- **`supervisor.Config.helperEnv` is unexported.** External packages (e.g. `internal/sessions`) cannot reuse the supervisor's `TestHelperProcess` re-exec pattern without one of: (a) exporting the field, (b) `t.Setenv` (pollutes parent process env, fights `t.Parallel`), or (c) using a real benign binary like `/bin/sleep` as the fake claude. Option (c) is what `internal/sessions` adopted — zero new test infra, supervisor's surface unchanged, and it exercises the only contract that matters (ctx-cancel delegation).
- **`/bin/sleep` exists on Linux and macOS.** Safe default for "I just need a child that won't exit until killed." If `exec.LookPath` ever fails, `t.Skipf` rather than passing silently.
