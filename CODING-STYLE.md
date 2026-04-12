# Go Coding Style — Pyrycode

Conventions for Go code in this project. `gofmt` handles formatting; this covers everything else.

## Package Layout

```
cmd/<name>/           Binary entry points (main packages)
internal/<pkg>/       Private packages (not importable by other modules)
```

- Flat packages preferred. Don't nest until you have a reason.
- One package per concern. `internal/supervisor` owns process lifecycle, `internal/control` (future) will own the Unix socket.
- Avoid `pkg/`, `util/`, `common/`, `helpers/`. If code doesn't have a clear home, the package boundaries are wrong.

## Naming

Follow stdlib conventions:

- **Packages:** short, lowercase, no underscores. `supervisor`, not `process_supervisor`.
- **Exported names:** `MixedCaps`. The package name is part of the caller's context — `supervisor.New()`, not `supervisor.NewSupervisor()`.
- **Unexported names:** `mixedCaps`. Short names for narrow scopes (`i`, `err`, `ctx`), descriptive for wide scopes (`backoffTimer`, `restoreTerm`).
- **Interfaces:** name by what they do, usually ending in `-er`: `io.Reader`, `io.Closer`. Single-method interfaces preferred.
- **Acronyms:** all caps when alone (`ID`, `HTTP`, `PTY`), leading caps in compounds (`HTTPClient`, `sessionID`).
- **Test helpers:** prefix with `test` or use `t.Helper()`.

## Error Handling

- **Return errors, never panic.** `panic` is for programmer bugs (unreachable code), not runtime failures.
- **Wrap with context:** `fmt.Errorf("pty start: %w", err)` — the caller should know what operation failed without reading the source.
- **Use `errors.Is` and `errors.As`** for matching. Never compare error strings.
- **Custom error types** when callers need to distinguish errors. Otherwise, `fmt.Errorf` wrapping is enough.
- **Don't swallow errors silently.** If you deliberately ignore an error, document why: `_ = ptmx.Close() // best-effort cleanup, child already exited`.

## Logging

- **`log/slog` everywhere.** No `fmt.Println` or `log.Printf` for operational output.
- **Structured fields:** `s.log.Info("spawning claude", "args", args, "workdir", dir)`.
- **Levels:**
  - `Debug` — internal state, only useful during development
  - `Info` — lifecycle events (starting, stopping, restarting, config loaded)
  - `Warn` — recovered errors, degraded operation
  - `Error` — unrecoverable errors (use sparingly — prefer returning the error)
- **Logger is injected**, not global. Pass `*slog.Logger` via config or constructor.

## Interface Design

- **Accept interfaces, return structs.** Define the interface where it's consumed, not where it's implemented.
- **Small interfaces.** 1-2 methods. Go's implicit interface satisfaction makes small interfaces composable.
- **Don't define interfaces preemptively.** Wait until you have two implementations or a testing need.
- **`io.Reader`, `io.Writer`, `io.Closer`** are your friends. Compose with `io.ReadCloser` etc.

## Concurrency

- **`context.Context` for cancellation.** Every goroutine that can be stopped takes a context.
- **Channels for coordination, mutexes for state.** If goroutines need to signal each other, use channels. If they share a counter or map, use `sync.Mutex`.
- **`errgroup.Group`** when you need to wait for multiple goroutines and collect errors.
- **Always clean up goroutines.** A goroutine that outlives its parent is a leak. Use `defer`, `done` channels, or context cancellation.
- **`go test -race`** catches data races. Run it always.

## Testing

- **Table-driven tests.** Define inputs and expected outputs in a slice, loop over them with `t.Run`.
- **stdlib `testing` only.** No testify, no gomock. Use interfaces and simple test doubles.
- **`t.Parallel()`** on tests that can run concurrently (most of them).
- **`t.Helper()`** on shared assertion functions so failures report the caller's line.
- **TestHelperProcess** for exec-based integration tests. The test binary re-execs itself as a fake child:
  ```go
  func TestHelperProcess(t *testing.T) {
      if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
          return
      }
      // Behave as the fake child process
  }
  ```
- **No `_test` package** unless testing unexported behavior would be wrong. Same-package tests are fine.
- **Test file placement:** `foo.go` -> `foo_test.go` in the same directory.

## Dependencies

- **Stdlib first.** Go's standard library covers HTTP, JSON, crypto, testing, concurrency, OS interaction, and more.
- **Justify external deps.** Each dep in `go.mod` should earn its place. Current justified deps:
  - `creack/pty` — PTY allocation (no stdlib equivalent)
  - `golang.org/x/term` — terminal raw mode and state management
- **Pin versions.** `go.sum` provides integrity checking. Don't use `latest` in `go.mod`.
- **Audit new deps** for maintenance status, license compatibility (MIT/BSD/Apache OK), and transitive dependency count.

## Git Conventions

- **Commit messages:** imperative mood, concise subject line. E.g., "Extract backoff timer into testable type".
- **No force-push to main.** Feature branches are fine to rebase.
- **One concern per commit.** Refactors and features in separate commits.
