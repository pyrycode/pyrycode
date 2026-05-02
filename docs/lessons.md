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

## State-machine wake-up channels

- **Snapshot the broadcast channel under the mutex *before* waiting, not after.** `Session.Activate` waits on `s.activeCh` for a "state became active" wakeup. The lifecycle goroutine replaces `activeCh` with a fresh open channel when entering `evicted`, and closes it when entering `active`. If `Activate` reads `s.activeCh` after releasing `lcMu`, a concurrent evict-replace can swap the channel out from under the waiter and the wakeup is dropped (the close fires on the new channel; the waiter is parked on the old open channel forever). Read once under the lock, then wait on the captured local. Same shape applies to any "broadcast wakeup with replacement on state change" channel pattern.
- **Pair the closed-on-state broadcast with a buffered(1) signal channel.** `activateCh` (buffered 1) lets `Activate` send without coordinating with the lifecycle goroutine's exact select position, and concurrent `Activate`s collapse to one signal via the non-blocking send. The closed-on-state `activeCh` is the broadcast every waiter eventually sees. Two channels, two jobs — don't try to make one channel do both.

## Lock order with callback into the host

- **Release the inner lock before calling back into the host's lock-protected persistence.** `Session.transitionTo` mutates `lcState` under `Session.lcMu`, then needs `Pool.persist` to write the registry. `Pool.persist` takes `Pool.mu`, then `saveLocked` re-takes each `Session.lcMu` briefly to read the snapshot. If `transitionTo` held `lcMu` across the callback, the inner re-acquire would deadlock. The fix: release `lcMu` *before* `pool.persist()`. Document the lock order (`Pool.mu → Session.lcMu`) at every site that takes both. Same shape applies to any "child mutates state then asks parent to persist" coupling.
- **Some legacy paths mutate without the inner lock — call out the invariant.** `Pool.RotateID` mutates `session.id` without `Session.lcMu` because today's only callers (startup reconciliation, fsnotify watcher) run before any lifecycle goroutine begins observing the id. That's a real invariant, not a missing lock — comment it loudly so a future caller doesn't add a parallel reader and silently break the assumption.

## Idle-timer activity signal

- **Tie activity to attach state, not bytes-through-bridge, when the bridge already knows.** `Session`'s idle eviction defers while `attached > 0`. Counting bytes through the bridge would require either reader plumbing (cross-package surgery for one feature) or a sampling timer (the same tick problem you're trying to solve). The bridge knows when it's bound and unbound — that's the cheap signal.
- **Poll-with-grace overshoots by up to one window — accept and document.** When the timer fires while `attached > 0`, re-arming with the full timeout is simpler than tracking "remaining time on current window." Real eviction may overshoot the configured timeout by up to one window; document this in the user-facing latency story instead of building exact-deadline machinery.
- **`time.Timer` with a nil channel placeholder for "disabled."** When `IdleTimeout == 0`, leave `timerCh` as a nil `<-chan time.Time`. `select` on a nil channel never selects — the eviction case is genuinely unreachable, no `if timer != nil` guards inside the select. Cleaner than constructing a timer you'll never fire.

## Foreground vs Bridge mode in concurrent supervisor tests

- **Foreground mode used to leak one stdin-bound `io.Copy` goroutine per `runOnce`.** Pre-#78, `io.Copy(ptmx, os.Stdin)` had no way to wake when the child exited (closing `ptmx` only drains the *output* goroutine; `os.Stdin` stays open at the OS level), so each restart stranded a reader on `os.Stdin`'s `fdMutex`. In concurrent-supervisor tests (e.g. `TestPool_ActiveCap_RaceConcurrentActivate`'s N parallel `Activate`s) the readers piled up and the next `pty.Start` call deadlocked on the same `fdMutex`. **Fixed in #78** by opening `/dev/tty` with `O_NONBLOCK` as a separate fd for the input bridge — closing the fd when the child exits unblocks the read via the Go runtime poller, the goroutine drains, no `os.Stdin` involvement. The Bridge-mode-fixture migration (kept for cap/race tests) is no longer load-bearing for correctness; can be retired in a follow-up.
- **Bridge mode in cap/race tests is still the most surgical setup.** `supervisor.Config.Bridge = supervisor.NewBridge(logger)` swaps the stdin/stdout pump to per-supervisor pipes — no terminal involvement at all, which simplifies test reasoning even though the foreground leak is gone. The cap test helper (`internal/sessions/pool_cap_test.go`'s `helperPoolCap` / `addCapTestSession`) sets this for every supervisor it builds.
- **`pty.Start` is not interruptible.** `drainSup` waits for it to complete before the kill cycle can run. Under `-race` and concurrent contention this stretches into hundreds-of-ms-to-seconds per cycle. Two consequences for cap-policy tests: (a) keep N modest in the race test (we use N=6), (b) wait for the bootstrap to settle into `stateActive` *before* kicking off the race — without this, the first eviction races bootstrap's still-in-progress `pty.Start` and the test sometimes hangs for the race detector's full slowdown.

## Lock-order pitfalls when a callee persists

- **A "primitive promoted to public method" can re-enter the host's main mutex.** ADR 005 kept eviction internal to `runActive`'s select loop. Ticket #41 promoted it to a public `Session.Evict` so the cap path could call it externally. `Session.Evict` triggers `transitionTo(stateEvicted)` which calls `Pool.persist` which takes `Pool.mu` (write). If `Pool.Activate` had held `Pool.mu` (write) across the cap sequence as the spec sketched, `persist` would have deadlocked on its own re-entry.
- **The fix is a dedicated outer mutex, not a recursive lock.** `Pool.capMu` serialises the cap decision; `Pool.mu` is taken and released for the read-side LRU iteration in `pickLRUVictim` and re-taken inside `persist` without re-entrancy. Document the lock order (`capMu → Pool.mu → Session.lcMu`) and the never-re-acquired-by-callees invariant explicitly — that's what makes the discipline checkable.
- **The general lesson:** when a state-machine method becomes a public callable, audit its full transitive callgraph for the host's main mutex *before* deciding which lock to hold across calls into it. "It used to work when only the lifecycle goroutine called it" doesn't survive promotion.

## Buffered-signal lifecycle and caller-ctx cancellation

- **A buffered(1) "wake-up" send may have happened before the ctx check that returned `ctx.Err`.** `Session.Activate(ctx)` sends on `activateCh` (buffered 1) and then waits on `activeCh`. If the caller cancels `ctx` after the buffered send but before the wait completes, `Activate` returns `ctx.Err` — but the lifecycle goroutine has already received the wake-up and proceeds to `stateActive` regardless of the *caller's* ctx (it respects the *pool's* run-ctx via the errgroup). Net effect: a caller can see `(id, ctx.Err)` from `Pool.Create` while claude is, in fact, spinning up correctly under pyry's supervision.
- **Don't write tests that assume "Activate-error → not running."** Assert the registry shape and the returned id; let the lifecycle goroutine do whatever it does. The buffered-signal pattern is intentional (lets concurrent `Activate`s collapse to one signal without coordinating on the lifecycle's exact select position) — the race is its inherent shape, not a bug to plug.

## Test helpers across packages

- **`supervisor.Config.helperEnv` is unexported.** External packages (e.g. `internal/sessions`) cannot reuse the supervisor's `TestHelperProcess` re-exec pattern without one of: (a) exporting the field, (b) `t.Setenv` (pollutes parent process env, fights `t.Parallel`), or (c) using a real benign binary like `/bin/sleep` as the fake claude. Option (c) is what `internal/sessions` adopted — zero new test infra, supervisor's surface unchanged, and it exercises the only contract that matters (ctx-cancel delegation).
- **`/bin/sleep` exists on Linux and macOS.** Safe default for "I just need a child that won't exit until killed." If `exec.LookPath` ever fails, `t.Skipf` rather than passing silently.

## E2E harness: same-process `t.Fatal` doesn't exercise cleanup-on-failure

- **Inner `t.Run("crash", ...)` with `t.Fatal` inside is not a substitute for the real failure path.** Go's testing framework propagates the inner failure to the parent and ends the outer test before its post-subtest assertions run. Worse, `t.Cleanup` registered by the *inner* subtest does run (LIFO before the parent's), so it *looks* like the right shape — but you can't observe leak state from the parent because the parent is already failing too. Verified the hard way for ticket #68's `TestHarness_NoLeakOnFatal`.
- **Re-exec the test binary instead.** `exec.Command(os.Args[0], "-test.run=^TestInnerChild$", "-test.count=1")` with an env-var flag (`PYRY_E2E_INNER_FATAL_OUT=<state-file>`) runs the failing path in a fresh process; the inner test writes observed state (pid, socket path) to the file before its `t.Fatal`; the parent reads the file after the child exits and asserts liveness/cleanup externally. Gate the inner test on the env var (`os.Getenv(...) == "" → t.Skip`) so normal `go test` runs are no-ops.
- **POSIX zero-signal probe is the right liveness check.** `os.FindProcess(pid)` always succeeds on Unix (it's a pure construction, no syscall). The probe is `p.Signal(syscall.Signal(0)) == nil` — sends no signal, returns ESRCH if gone. Zero side-effect, no `ps`-grep magic.

## E2E harness: read after `cmd.Wait`, not during

- **`exec.Cmd` synchronizes its `Stdout`/`Stderr` writers with `Wait`.** Wiring `cmd.Stdout = &buf` and reading `buf.String()` from the test goroutine *after* `<-doneCh` (where `doneCh` is closed by a goroutine that called `cmd.Wait()`) is race-free without an additional mutex. Reading concurrently with `Wait` is undefined.
- **Don't keep the wait goroutine "for cleanup only."** It serves double duty: closing `doneCh` is the readiness-poll's early-exit signal (so a daemon that crashes during startup short-circuits the 5s deadline) AND the gate that makes captured I/O safe to read. One goroutine, two contracts.

## E2E against the operator's real systemd `--user`

- **`systemctl --user` doesn't honor a redirected `$HOME`.** The user systemd
  manager runs in the operator's real session; redirecting the test child's
  `HOME` to `t.TempDir()` (the standard e2e isolation envelope) does *not*
  redirect where `systemctl --user` looks for unit files. The install round-trip
  test must use the real `~/.config/systemd/user/` and clean up rigorously —
  unique per-invocation unit names (`pyry-e2e-<pid>-<unixnano>`), `t.Cleanup`
  registered before any state-changing step (so `t.Fatal` mid-test still
  removes the unit), and an idempotent best-effort cleanup helper that
  swallows errors from already-stopped/already-removed steps. The PATH
  inheritance test, which doesn't touch real systemd, can stay
  `t.TempDir()`-isolated.
- **`is-system-running` exits non-zero on degraded but usable states.** Using
  exit code alone as the "system is usable" check would over-skip: degraded /
  maintenance / starting / stopping all return non-zero but are still usable.
  The unusable states are `offline` (no manager running) and `unknown` (no
  D-Bus session — the common CI-runner shape). Skip on those plus a missing
  `systemctl` binary; everything else proceeds. `loginctl enable-linger
  <user>` may be needed once on dedicated test hosts where the test runs
  outside an interactive login session.
- **Reject hidden env vars added "just for the test."** `install.Install`
  defaults `Options.Binary` to `os.Executable()` — for a test process, that's
  the test binary, not pyry. The CLI exposes no `--binary` override. The
  tempting fix is `PYRY_INSTALL_BINARY=...`-as-a-test-seam in production code,
  but that's exactly the pattern #34/#38/#69 already rejected. Better: import
  `internal/install` and call `install.Install(opts)` directly with
  `opts.Binary = bin` — the e2e value is in the systemd round-trip, not in
  re-testing the flag-parsing layer (`install_test.go` already covers that).
  General rule: when the test needs a knob, prefer the existing `Options`
  surface (`EnvPath` was already there for testing) over inventing a new
  production-side seam.

## Closing a fd to interrupt a goroutine's Read requires O_NONBLOCK

- **Plain `os.OpenFile("/dev/tty", os.O_RDONLY, 0)` produces a blocking fd that `Close()` cannot interrupt.** Without `O_NONBLOCK`, `internal/poll.(*FD).Read` calls `syscall.Read` directly and parks in the kernel; POSIX `close(2)` on another goroutine is a no-op for the in-flight read. The Go runtime poller only mediates Reads when the syscall returns `EAGAIN` — i.e., the fd must be non-blocking. With `os.O_RDONLY|syscall.O_NONBLOCK`, Read returns EAGAIN, the goroutine parks on the poller, and `Close()` calls `runtime_pollUnblock` which wakes it with `os.ErrClosed`. This is the entire mechanism that makes "open a side fd, close it to drain the goroutine" work — drop O_NONBLOCK and you reproduce the original `os.Stdin` leak (#78). Sockets and pipes get this for free; character devices like /dev/tty do not.
