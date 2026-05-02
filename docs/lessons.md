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

## Atomic on-disk writes

- **`os.Rename` on the same filesystem is the commit point.** Write to a temp file (`os.CreateTemp(dir, ".prefix-*.tmp")`) in the *same directory* as the target — cross-filesystem rename is not atomic. Encode → `f.Sync()` → `f.Close()` → `os.Rename(tmp, path)`. SIGKILL anywhere before the rename leaves the pre-existing target byte-identical; SIGKILL after leaves the post-update file. Partial JSON in the target is unreachable.
- **Always `defer os.Remove(tmp)` after `os.CreateTemp`.** If anything between `CreateTemp` and `Rename` fails, the orphan tmp is cleaned up. After a successful rename, the file is no longer at `tmp` — `os.Remove` is a harmless no-op.
- **Don't fsync the parent directory unless you need it.** For operator-recoverable JSON (pyry's `sessions.json`), the rename's directory entry update is durable enough on Linux ext4 / macOS APFS. Adds one syscall per write and defends against a power-loss window we don't optimize for. Revisit only if real-world corruption surfaces.
- **Map-iteration order is not stable.** Before serializing a Go map to disk, copy to a slice and sort by a stable key — otherwise round-tripping the same in-memory state produces different bytes each time, and "load twice → same state" stops being a real property. For `sessions.json`: sort by `created_at` then `id`.
- **Default `encoding/json` decoder is the right call for forward compat.** Don't reach for `DisallowUnknownFields` on a file format you intend to evolve — it converts "future field added" into a load failure. Reserve strict decoding for wire protocols where unknown fields signal a real client/server mismatch.

## Claude session storage on disk

- **Don't trust ticket bodies on filesystem layout — observe.** The #38 ticket body said `~/.claude/projects/<encoded-cwd>/sessions/<uuid>.jsonl`. Reality (verified 2026-05-02): files live **directly** in `<encoded-cwd>/`, no `sessions/` subdir. Always observe an existing entry under `~/.claude/projects/` before coding against the path; same goes for the encoding rule.
- **Claude's cwd encoding replaces both `/` AND `.` with `-`.** A leading `/` becomes a leading `-`; a hidden `.dir` collapses to `--`. Example: `/Users/juhana/.pyrycode-worktrees/x` → `-Users-juhana--pyrycode-worktrees-x`. The doubled dash is real, not a typo. A naive "replace `/` with `-`" implementation will miss the `.dir` case and look in the wrong directory forever.
- **`/clear` rotates claude's session UUID — even with `--resume <uuid>`.** Claude stops writing to the original `<uuid>.jsonl` and starts a fresh `<new-uuid>.jsonl`. Pyry can't prevent this; the registry has to self-heal by following the most-recently-modified JSONL on the next read. The pre-`/clear` JSONL is preserved on disk (frozen mid-conversation), so destructive recovery is unnecessary — just move the pointer.

## Cross-package callbacks without import cycles

- **Don't reach for an interface when the natural shape is closures.** When `internal/sessions/rotation` needed to call back into `internal/sessions` (snapshot pids, register skip-set hits, drive `RotateID`), the obvious design was a `Rotator` interface in the rotation package, satisfied by `*sessions.Pool`. That requires the rotation package to know about `sessions.SessionID` (or to invent its own equivalent type), which leaks the host's domain across the boundary. Closures over primitives (`func() []SessionRef { ID string; PID int }`, `func(id string) bool`) make the rotation package zero-knowledge about its host; the wiring site does the `SessionID ↔ string` conversion exactly once.
- **Why this is the right default for "downstream calls upstream":** the downstream package's API stays describable in terms of its own domain (file paths, PIDs, UUID strings); the upstream package decides how to translate. Phase 1.1's fan-out can change `Snapshot` to return N refs without touching the rotation package.

## Probing open files cross-platform

- **`lsof` exit code 1 is not an error.** `lsof -nP -p <pid> -F fn` returns 1 for "no matching files" and "process gone" — both are valid "nothing to report" outcomes for a probe. Treat `*exec.ExitError` with `ExitCode() == 1` as `("", nil)`, not as a probe failure, or the watcher will spam warnings every time a session has no JSONL open yet.
- **`exec.LookPath` at construction, not on every event.** A missing `lsof` should surface once at startup ("rotation probe disabled") and degrade to a `noopProbe`, not bubble up as `exec.ErrNotFound` on every fsnotify CREATE. Same shape would apply to any subprocess-backed probe.
- **Linux `/proc/<pid>/fd` symlinks include non-files.** Targets like `socket:[123]`, `pipe:[456]`, `anon_inode:[bpf-prog]`, `[eventfd]` are normal entries and not regular paths. A pure parser (`parseProcFD`) that returns `""` for anything not starting with `/` keeps the platform code thin and the test cases obvious.
- **fsnotify CREATE can race the file's `open(2)`.** A bounded retry with a small step-up schedule (0 / 50ms / 200ms) inside the event handler handles the race without a goroutine-per-event dance. Total worst-case latency stays well inside the AC. If all attempts miss, accept the rare miss — the next CREATE on the same dir won't re-fire for an existing file.

## Test helpers across packages

- **`supervisor.Config.helperEnv` is unexported.** External packages (e.g. `internal/sessions`) cannot reuse the supervisor's `TestHelperProcess` re-exec pattern without one of: (a) exporting the field, (b) `t.Setenv` (pollutes parent process env, fights `t.Parallel`), or (c) using a real benign binary like `/bin/sleep` as the fake claude. Option (c) is what `internal/sessions` adopted — zero new test infra, supervisor's surface unchanged, and it exercises the only contract that matters (ctx-cancel delegation).
- **`/bin/sleep` exists on Linux and macOS.** Safe default for "I just need a child that won't exit until killed." If `exec.LookPath` ever fails, `t.Skipf` rather than passing silently.
