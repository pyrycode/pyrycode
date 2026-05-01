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

## Interface adapters for covariant returns

- **Go does not do covariant return types on interface satisfaction.** If a concrete type returns `*Foo` and an interface method's declared return is `Bar` (an interface satisfied by `*Foo`), the concrete type does **not** satisfy the interface. Example: `*sessions.Pool.Lookup` returns `*sessions.Session`; `control.SessionResolver.Lookup` returns `control.Session` (which `*sessions.Session` satisfies). `*sessions.Pool` still does not satisfy `SessionResolver` directly. Bridge with a tiny adapter type at the call site (`cmd/pyry/main.go`'s `poolResolver`).
- **Don't push the adapter into the producer or consumer package.** The producer (`internal/sessions`) shouldn't know about the consumer interface (`control.SessionResolver`); the consumer (`internal/control`) shouldn't know the concrete producer type (`*sessions.Pool`). The only place that knows both is `cmd/pyry/main.go`, so the adapter lives there.

## Byte-identical wire output across refactors

- **Changing an error's package can change its `%v` output.** A bare `fmt.Sprintf("attach: %v", err)` will surface whatever string the new error chain produces. If an acceptance criterion requires byte-identical client output, map the new sentinel back to the old string explicitly: `if errors.Is(err, sessions.ErrAttachUnavailable) { _ = enc.Encode(Response{Error: "attach: no attach provider configured (daemon may be in foreground mode)"}); return }`.
- **The translation site is load-bearing.** Comment it as such so the next refactor doesn't drop the mapping. Cover it with a test that asserts the wire string verbatim (Phase 1.0b: `TestServer_AttachOnForegroundSession`).

## Test helpers across packages

- **`supervisor.Config.helperEnv` is unexported.** External packages (e.g. `internal/sessions`) cannot reuse the supervisor's `TestHelperProcess` re-exec pattern without one of: (a) exporting the field, (b) `t.Setenv` (pollutes parent process env, fights `t.Parallel`), or (c) using a real benign binary like `/bin/sleep` as the fake claude. Option (c) is what `internal/sessions` adopted — zero new test infra, supervisor's surface unchanged, and it exercises the only contract that matters (ctx-cancel delegation).
- **`/bin/sleep` exists on Linux and macOS.** Safe default for "I just need a child that won't exit until killed." If `exec.LookPath` ever fails, `t.Skipf` rather than passing silently.
