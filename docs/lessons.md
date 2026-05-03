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
- **`/bin/sleep infinity` is GNU coreutils only — macOS BSD sleep rejects it (and the unit-suffixed forms its man page advertises don't all work either).** On macOS Tahoe (26.3) the man page lists `s/m/h/d` as accepted units, but `/bin/sleep 99999d` exits immediately with the usage banner. A plain integer (`99999` ≈ 27h) works on both Linux GNU sleep and macOS BSD sleep — the only argv form actually portable across hosts. The e2e harness uses `99999`; tests that depend on the supervised child staying alive long enough to observe `Phase: running` (e.g. lazy respawn) will go into perpetual backoff if they pass `infinity`.

## E2E harness: same-process `t.Fatal` doesn't exercise cleanup-on-failure

- **Inner `t.Run("crash", ...)` with `t.Fatal` inside is not a substitute for the real failure path.** Go's testing framework propagates the inner failure to the parent and ends the outer test before its post-subtest assertions run. Worse, `t.Cleanup` registered by the *inner* subtest does run (LIFO before the parent's), so it *looks* like the right shape — but you can't observe leak state from the parent because the parent is already failing too. Verified the hard way for ticket #68's `TestHarness_NoLeakOnFatal`.
- **Re-exec the test binary instead.** `exec.Command(os.Args[0], "-test.run=^TestInnerChild$", "-test.count=1")` with an env-var flag (`PYRY_E2E_INNER_FATAL_OUT=<state-file>`) runs the failing path in a fresh process; the inner test writes observed state (pid, socket path) to the file before its `t.Fatal`; the parent reads the file after the child exits and asserts liveness/cleanup externally. Gate the inner test on the env var (`os.Getenv(...) == "" → t.Skip`) so normal `go test` runs are no-ops.
- **POSIX zero-signal probe is the right liveness check.** `os.FindProcess(pid)` always succeeds on Unix (it's a pure construction, no syscall). The probe is `p.Signal(syscall.Signal(0)) == nil` — sends no signal, returns ESRCH if gone. Zero side-effect, no `ps`-grep magic.

## E2E harness: read after `cmd.Wait`, not during

- **`exec.Cmd` synchronizes its `Stdout`/`Stderr` writers with `Wait`.** Wiring `cmd.Stdout = &buf` and reading `buf.String()` from the test goroutine *after* `<-doneCh` (where `doneCh` is closed by a goroutine that called `cmd.Wait()`) is race-free without an additional mutex. Reading concurrently with `Wait` is undefined.
- **Don't keep the wait goroutine "for cleanup only."** It serves double duty: closing `doneCh` is the readiness-poll's early-exit signal (so a daemon that crashes during startup short-circuits the 5s deadline) AND the gate that makes captured I/O safe to read. One goroutine, two contracts.

## E2E against the operator's real systemd `--user` / launchd `gui/<uid>`

- **Neither `systemctl --user` nor `launchctl bootstrap gui/<uid>` honors a
  redirected `$HOME`.** The user systemd manager runs in the operator's real
  session; launchd's GUI domain inherits `HOME` from the user's GUI login,
  not from the test process. Redirecting the test child's `HOME` to
  `t.TempDir()` (the standard e2e isolation envelope) does *not* redirect
  where these service managers look for unit/plist files. Round-trip tests
  must use the real `~/.config/systemd/user/` (Linux) or
  `~/Library/LaunchAgents/` (macOS) and clean up rigorously — unique
  per-invocation names (`pyry-e2e-<pid>-<unixnano>`), `t.Cleanup` registered
  before any state-changing step (so `t.Fatal` mid-test still removes the
  unit/plist), and an idempotent best-effort cleanup helper that swallows
  errors from already-stopped/already-removed steps. PATH-inheritance tests,
  which don't touch the service manager, can stay `t.TempDir()`-isolated.
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

## launchd-specific E2E gotchas

- **`launchctl bootstrap` is asynchronous — poll for `state = running`.** The
  command returns once launchd has *accepted* the bootstrap request, not when
  the job is actually running. Polling `launchctl print gui/<uid>/<label>`
  for `state = running` (100ms gap, 10s deadline) is the right liveness
  signal. `launchctl print` is technically a debug command whose output
  format Apple reserves the right to reformat, but `state = running` has
  been stable since macOS 10.10. If a future release breaks the matcher,
  the test fails loudly with the last `print` output dumped for diagnosis
  — accept the rare-future risk over a more contorted check.
- **Use `plutil -extract`, not `encoding/xml`, to read plist contents.**
  Plist XML is a flat alternation of `<key>` / `<string>|<dict>|...` siblings
  inside a `<dict>` — awkward to walk with `encoding/xml`. `plutil -extract
  EnvironmentVariables.PATH raw -o - <plist>` returns the value as a plain
  string in one shell-out, with crisp error messages if the key is missing.
  `plutil` is part of `/usr/bin` on macOS — its absence is a broken host,
  not a skip condition. Reimplementing the parser in Go reinvents what
  Apple's tool does correctly.
- **`gui/<uid>` is for non-root users; system-domain bootstrap is a different
  product.** Skip the test when running as root (`os.Getuid() == 0`). The
  system domain (`launchctl bootstrap system/`) is what root daemons use;
  pyry's install-service doesn't ship that path.
- **`gui/<uid>` requires a logged-in GUI session for that uid.** GitHub
  `macos-latest` runners have one for the runner user; pure sshd-only
  headless contexts don't. The fallback is `launchctl bootstrap user/<uid>`
  (background-only domain) — defer it until the failure is observed on real
  CI rather than pre-emptively branching.
- **launchd template hardcodes `/tmp/pyry.<name>.{out,err}.log`.** Unlike
  systemd (which streams to journald), launchd jobs need explicit log file
  paths. The cleanup helper must best-effort `os.Remove` both log files in
  addition to the plist and runtime artefacts (`~/.pyry/<name>` registry
  dir, `<name>.sock`). Leftover logs aren't a correctness leak but they
  pile up in `/tmp` across runs.
- **`derivePathEnv` substitutes `$HOME/` → `%h/` only for systemd.** The
  launchd plist gets literal absolute paths in `EnvironmentVariables.PATH`,
  so the macOS PATH-inheritance assertion is "every non-empty `$PATH` entry
  appears verbatim" — simpler than the Linux sibling's substitution check.
  If you copy-paste assertions between platforms, this is the line that has
  to differ.

## Unix-socket sun_path limits and `t.TempDir()`

- **`sun_path` caps at 104 bytes on macOS, 108 on Linux — `t.TempDir()` can overflow it.** `t.TempDir()` embeds the sanitised test name in its path (`/var/folders/.../TestSomeReallyLongDescriptiveName/001/`). Append `pyry.sock` and a descriptive name like `TestE2E_Restart_PreservesActiveSessions` and the resulting `sun_path` can exceed 104 bytes on darwin. `bind(2)` returns `EINVAL`; in pyry that surfaces as a daemon startup failure and a harness "ready-deadline exceeded" — neither obviously points at path length.
- **Use `os.MkdirTemp("", "<short-prefix>-*")` + `t.Cleanup(os.RemoveAll)` for path-budget-tight cases.** Keeps the prefix tiny so `<home>/pyry.sock` fits. Tests with short names or short HOME budgets can keep `t.TempDir()`; only switch when the test name + macOS folder prefix puts the path over the limit. Used by `TestE2E_Restart_PreservesActiveSessions` (#106).
- **Heuristic.** An e2e test that passes on Linux but fails the readiness gate on macOS, or fails after a benign test rename, is the path-length shape — suspect it before suspecting timing.

## JSON roundtrip strips monotonic-clock state from `time.Time`

- **`time.Now()` carries both wall-clock and monotonic readings; JSON marshal/unmarshal preserves only wall-clock.** `time.Time.MarshalJSON` writes RFC3339Nano (wall-clock only); `UnmarshalJSON` parses it back without the monotonic component. The two values compare *equal* under `time.Time.Equal` (which ignores monotonic) but *unequal* under `==` and `reflect.DeepEqual` even when the bytes on disk are byte-identical.
- **In tests that read back what they just wrote, capture the "want" via the same JSON trip the production code takes.** Concretely: after `writeRegistry(t, path, pre)`, re-read the file with `readRegistry(t, path)` and use *that* parsed value as the expectation, not the in-memory `pre` struct. `TestE2E_Restart_LastActiveAtSurvives` (#107) does this — comparing pre-write in-memory vs. post-restart parsed would diverge on monotonic alone, masquerading as a real timestamp regression.
- **Always `time.Time.Equal`, never `==` or `reflect.DeepEqual`, for cross-process / cross-roundtrip time comparison.** `Equal` ignores location and monotonic differences and compares wall-clock instant only — the durable property a roundtrip is supposed to preserve.

## fsnotify reports as-watched, kernel probes report canonicalised — match in one form

- **fsnotify event paths are the watched path with `base` appended; kernel-side fd inspection (`lsof`, `/proc/<pid>/fd`) returns paths with symlinks resolved.** When a comparison gate matches one against the other and the watched directory crosses a symlink, the strings differ even though they refer to the same inode. `filepath.Clean` is lexical only — it does not consult the filesystem and will not fix this. Use `filepath.EvalSymlinks` (one syscall, hits the filesystem) to canonicalise the watched directory once at watcher construction, then build the comparison path as `filepath.Join(resolvedDir, base)` per event. The rotation watcher (#118) hit this on macOS where `/var → /private/var` is a default symlink and `t.TempDir()` lands under `/var/folders/...` — fsnotify reported `/var/...` and `lsof` returned `/private/var/...`; the gate dropped the rotation silently and the session UUID stopped updating after `/clear`.
- **Resolve once, not per event.** Per-event `EvalSymlinks` of the probe output adds a syscall to every CREATE *and* opens a race window: the file may have been unlinked between the event and the resolution. Resolving the *directory* once at startup avoids the window entirely — the directory's lifetime spans the watcher's. If `EvalSymlinks` fails at startup (broken symlink components, permission flake), `Warn`-log and fall back to the unresolved path; the watcher remains functional and the symlink-bridge case continues to drop matches for that run, no worse than before.
- **The bug class is "two sources of paths, one canonical and one not."** Audit any cross-source path comparison you write — fsnotify vs. `lsof`, fsnotify vs. `os.Readlink`, user-config-string vs. `os.Stat`-resolved-path — for the same shape. Tests that rely on `t.TempDir()` *will* exercise this on macOS regardless of platform; tests that need it on Linux too should use an explicit `os.Symlink` (the rotation watcher's regression test does).

## PTY master fds on darwin do not support SetReadDeadline

- **`(*os.File).SetReadDeadline` returns `ErrNoDeadline` on PTY master fds on macOS.** The Go runtime poller refuses to attach a deadline to PTY master fds even though `os.NewFile` produced an `*os.File`. Net effect: per-read timeouts via `r.SetReadDeadline(time.Now().Add(d))` cannot enforce a test's overall budget — the call returns `*PathError{Op: "set", Path: "/dev/ptyXX", Err: ErrNoDeadline}` and the next `Read` blocks forever.
- **Enforce the timeout in the caller, not in the syscall.** Run the read on a goroutine with the result delivered via `chan readResult`, then `select { case res := <-ch: ... case <-time.After(deadline): return errTimeout }`. On timeout the reader goroutine is left running; the harness's teardown closes the master, which unblocks the `Read` with EOF. This is acceptable for test code because cleanup is deterministic; for production code consider pairing the goroutine with a context-cancellation seam.
- **The same shape probably applies to PTY slave fds.** Not directly verified, but the runtime poller's PTY support is symmetric. If you need deadlines on PTY I/O, plan for caller-side timing.

## Daemon env flows through to the supervised child via supervisor.runOnce

- **`internal/supervisor/supervisor.go:226-268` (bridge mode) does `cmd.Env = append(os.Environ(), s.cfg.helperEnv...)` for the supervised child.** Practically this means: env vars set on the *daemon's* `cmd.Env` (e.g. when spawning pyry from an e2e test) appear in the supervised claude child's env unchanged. The e2e PTY harness (#125) uses this to ferry `GO_TEST_HELPER_PROCESS=1` + `GO_TEST_HELPER_MODE=echo` from the test process → daemon → helper, without exposing a `-pyry-helper-env` flag.
- **Bridge mode does NOT raw-mode the supervisor's PTY.** The kernel's line discipline runs with default ECHO on. A helper that does `io.Copy(stdout, stdin)` without first calling `term.MakeRaw(stdin)` will see every input byte echoed by the kernel before the copy runs — round-trip tests observe each byte twice. Helpers reading from the supervisor's PTY slave must `MakeRaw` themselves. (The attach client's `MakeRaw` on its own slave is a separate line discipline.)

## PTY master backpressure stalls slave-side process exit

- **An attach-client process exiting after detach can hang on its own stderr write if no one is draining the master.** When `cmd.Stderr = slave`, the attach client's `fmt.Fprintln(os.Stderr, "pyry: detached.")` goes through the kernel's PTY into the master buffer. With nothing reading the master, the buffer fills and the slave write blocks — the process never returns from runAttach and `cmd.Wait()` never fires. Symptom: e2e detach test hits its 5s deadline even though `copyWithEscape` correctly returned on `Ctrl-B d`.
- **`readUntilContains` (attach_pty_test.go) returns on first match without spawning a follow-up read.** After it returns, no goroutine is consuming the master. Any subsequent slave-side traffic queues until something drains it. For tests that drive an action after a round-trip and then wait on process exit, spawn a background master-drain goroutine before triggering the action; let it ride until teardown closes Master and Read errors out.

## Closing a fd to interrupt a goroutine's Read requires O_NONBLOCK

- **Plain `os.OpenFile("/dev/tty", os.O_RDONLY, 0)` produces a blocking fd that `Close()` cannot interrupt.** Without `O_NONBLOCK`, `internal/poll.(*FD).Read` calls `syscall.Read` directly and parks in the kernel; POSIX `close(2)` on another goroutine is a no-op for the in-flight read. The Go runtime poller only mediates Reads when the syscall returns `EAGAIN` — i.e., the fd must be non-blocking. With `os.O_RDONLY|syscall.O_NONBLOCK`, Read returns EAGAIN, the goroutine parks on the poller, and `Close()` calls `runtime_pollUnblock` which wakes it with `os.ErrClosed`. This is the entire mechanism that makes "open a side fd, close it to drain the goroutine" work — drop O_NONBLOCK and you reproduce the original `os.Stdin` leak (#78). Sockets and pipes get this for free; character devices like /dev/tty do not.

## PTY resize seam: clear-fd before iteration-end, swap cols/rows once at the wire boundary

- **`SetPTY(nil)` must run BEFORE `EndIteration`, not after, when the bridge owns a per-iteration `*os.File`.** A `Resize` that races iteration teardown can otherwise observe the registered `*os.File` *after* `ptmx.Close` ran, then `pty.Setsize` returns `EBADF`. The lock-protected ordering in `runOnce` is: `cmd.Wait` → `ptmx.Close` → `Bridge.SetPTY(nil)` → `Bridge.EndIteration`. Holding `Bridge.ptyMu` across `pty.Setsize` tightens the residual `EBADF` window to microseconds; the alternative ordering (clear-then-close) opens a longer window where new resizes target the to-be-closed fd, which is strictly worse. The narrow residual `EBADF` is logged at Warn and the attach proceeds — geometry is best-effort, not load-bearing for connection setup.
- **The wire protocol's `cols, rows` field order does NOT have to match the supervisor seam's argument order — pick rows-then-cols for the seam and swap at the boundary.** `pty.Winsize{Rows, Cols, ...}` is rows-first; `AttachPayload{Cols, Rows}` is cols-first (back-compat with the v0.5.x JSON shape). Picking rows-then-cols for `Bridge.Resize` / `Session.Resize` minimises adapter friction at the `pty.Setsize` callsite (no reordering inside the supervisor) and concentrates the swap to a single line in `handleAttach`. Document the swap at the boundary site so a future refactor doesn't silently flip the dimensions.
- **`int → uint16` at the boundary clamps silently.** A real terminal will never report dimensions over 65535; a client that does is buggy or hostile. Logging the clamp gives a slow-DoS amplification path with no operator-actionable signal. `clampUint16` returns `math.MaxUint16` and moves on.

## Bridge input pump must be scoped per-iteration to survive child restart

- **`io.Copy(ptmx, bridge)` is per-iteration; the bridge persists across iterations. Without a per-iteration cancel signal, the input goroutine blocks indefinitely on bridge.Read after `cmd.Wait` + `ptmx.Close`, then races with the *next* iteration's goroutine for queued attach-client bytes.** The dead goroutine wins some chunks, writes them to a closed ptmx, fails, and the bytes are silently lost. Symptom (`TestE2E_Attach_SurvivesClaudeRestart` against the original code): post-restart payload `"post-restart-XXXXXXXX\n"` arrives at the test as `"restart-XXXXXXXX\n"` — a 5-byte prefix consumed by the leaked goroutine.
- **Fix shape: replace `io.Pipe` in `Bridge` with a `chan []byte` + per-iteration cancel channel; supervisor calls `Bridge.BeginIteration` / `EndIteration` around each `runOnce` lifecycle, and waits for *both* `io.Copy` goroutines to drain (not one-of-two with a timeout).** Go's select non-determinism makes this safe: when cancel fires concurrently with a chunk arriving, an unselected `<-b.in` case does NOT consume the chunk — it stays queued for the next iteration's Read.

## `Pool.Create` appends `--session-id`, breaking naive claude stand-ins

- **`Pool.Create` constructs new-session args as `append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))`.** That means every "fake claude" used in multi-session e2e tests must accept (or ignore) the unknown `--session-id <uuid>` positional pair. `/bin/sleep 99999 --session-id <uuid>` exits immediately on both BSD and GNU sleep (usage banner). Pool's lifecycle state still flips to `stateActive` (Pool tracks lifecycle independently of supervisor health), so cap-evict logic uncouples — but the supervisor crash-loops the child, generating noisy stderr and racing the backoff window against assertions.
- **A two-line shell script is the simplest stand-in.** Write `<home>/sleep-claude.sh` containing `#!/bin/sh\nexec sleep 99999\n`, `chmod 0o755`, pass via `-pyry-claude=<path>` through `StartIn`'s variadic flags. Both bootstrap (`<script> 99999`) and new-session (`<script> 99999 --session-id <uuid>`) invocations ignore positional args and `exec sleep`. Used by `internal/e2e/cap_test.go` (#116). Preferred over extending `internal/e2e/internal/fakeclaude` with a "no-rotation" mode — that binary exists for rotation tests; mixing concerns is anti-simplicity for two lines.
- **Bootstrap-only e2e tests don't hit this** because `Pool.New`'s bootstrap path uses `tpl.ClaudeArgs` verbatim (no `--session-id` append). Every test before #116 only used the bootstrap session, so `/bin/sleep 99999` worked. The first test that calls `sessions.new` (or any future verb that drives `Pool.Create`) needs the script.

## Aggregate sub-interfaces into a facade rather than threading new constructor parameters

- **When a host's constructor has many call sites and each new optional dependency would otherwise add a parameter (and a mechanical `, nil` to every site), embed the dependency into an existing aggregate interface the constructor already takes.** `internal/control.NewServer` has ~27 call sites; #75 added `sessioner Sessioner` (one method: `Create`); #98 needed a second seam (`Remove`). Adding a 7th `NewServer` parameter (`remover Remover`) would have cascaded the same 27-call-site fan-out the post-#75 architect prompt was rewritten to prevent. The fix: define `Remover` as a named single-method interface next to `Sessioner` (so it remains addressable in tests and the AC can name it), then **embed** it into `Sessioner`. `NewServer`'s signature is unchanged; `*sessions.Pool` (already supplying both `Create` and `Remove`) continues to satisfy the broader `Sessioner` interface; the only test-side cascade is one new method on the existing `fakeSessioner` struct, not a new fake type.
- **The grow-the-facade pattern beats both alternatives for this growth shape.** Switching `NewServer` to a `Config` struct *is* the 27-call-site refactor we're trying to avoid (proper, but belongs in a dedicated ticket). Keeping `Sessioner` as a one-method interface and adding a sibling `Remover` parameter splits the call surface for symmetric concerns. Embedding scales linearly: each subsequent verb adds one method (or one named sub-interface embed) to the same aggregate, with zero call-site churn at every step. The cost is that `Sessioner` becomes a "lifecycle facade" broader than any single verb consumes — accept it; named sub-interfaces (`Remover`, future `Renamer`/`Lister`) stay reusable for tests that want to fake one method without implementing the others.

## Wire-level error code over message-string matching for typed-sentinel propagation

- **When typed Go sentinels need to survive a JSON round-trip and `errors.Is` is the canonical client-side matcher, add a stable wire-level token field rather than matching on `Response.Error` text.** Sentinel error message strings are documentation, not API; coupling the wire contract to them turns a benign rename into a wire-protocol break. The fix: a separate `Response.ErrorCode ErrorCode` field (omitempty, so non-typed responses round-trip byte-identically), populated server-side via `errors.Is` against each sentinel (so future server-side wrapping with `fmt.Errorf("%w: …")` doesn't break the wire token), mapped client-side back to the bare sentinel. Same shape gRPC and HTTP-status conventions use. Used for `Pool.Remove`'s `ErrSessionNotFound` and `ErrCannotRemoveBootstrap` in `sessions.rm` (#98); the `ErrorCode` envelope is reusable for any future verb that needs to propagate typed sentinels (no new wire field required, only new code values).
- **Return the bare sentinel from the client, not `fmt.Errorf("%s: %w", resp.Error, sentinel)`.** When `Pool.Remove` returns the sentinels bare, `err.Error()` already equals `sentinel.Error()` — the server's `Response.Error` is verbatim the sentinel's message. Wrapping client-side with `%s: %w` produces a doubled prefix (`"sessions: session not found: sessions: session not found"`). The bare-return shape preserves the message and supports `errors.Is`. If the server ever wraps the sentinel with extra context (e.g. `fmt.Errorf("removing %s: %w", id, ErrSessionNotFound)`), the right response is to introduce a small unexported `wireError` type carrying both the message and the sentinel — defer until needed.
- **Detect with `errors.Is` server-side, not bare equality.** `Pool.Remove` returns the sentinels bare today, but a future change could legitimately wrap (e.g. for diagnostic context). `errors.Is(err, sessions.ErrSessionNotFound)` survives wrapping; the typed `ErrorCode` continues to fire on the wire even if the message string changes. This is the durability the wire-level error code buys.

## Wire enums: prefer self-documenting strings, keep `internal/control/protocol.go` import-free

- **Don't share an internal `uint8`-backed enum across the wire boundary.** `sessions.JSONLPolicy` is `uint8` (`JSONLLeave` = 0, `JSONLArchive` = 1, `JSONLPurge` = 2); marshalling it as JSON produces opaque integers (`0`/`1`/`2`) and silently drifts if the underlying enum order ever changes. Define a parallel string newtype on the wire (`control.JSONLPolicy = "leave" | "archive" | "purge"`), translate in one place (`toSessionsPolicy` next to `handleSessionsRm`), and document why the duplication is intentional.
- **`internal/control/protocol.go` stays import-free by design.** Wire types are primitives — keeping `protocol.go`'s import set empty means external Go callers (or future hand-written clients) don't drag in `internal/sessions` transitively, and the wire contract has zero dependence on supervisor-package internals. The translation cost is one helper function per cross-boundary enum.
- **Empty wire value should map to the internal zero value.** `JSONLPolicy("")` → `sessions.JSONLLeave` keeps an omitted JSON field equivalent to passing the documented default. Unknown values surface as `"unknown jsonl policy %q"` rather than silent fallback — fail-loud is the right default for forward-incompatible clients (e.g. a future `"compress"` policy a v0.x server doesn't yet implement).

## Don't pick a real verb name as the "still-unknown" placeholder in CLI tests

- **An "asserts unknown verb" test that hard-codes a verb name silently flips behaviour the moment that verb becomes valid.** `TestSessionsNew_E2E_UnknownVerb` originally fired `pyry sessions list` as the unknown verb (line 122). When `list` shipped (#88), the test would have started asserting "unknown verb error" against successful `list` output instead — exit 0, no diagnostic in stderr — and failed in a confusing way. The architect's spec called this out and the same edit changed the placeholder to a synthetic name (`bogus`). Lesson: when a router has a closed verb namespace and a "rejects unknown verb" test, use a synthetic placeholder that won't collide with any planned verb (`bogus`, `__noverb__`, etc.), not a name from the roadmap.
- **Same anti-pattern applies to JSON envelope shapes.** The `{"sessions":[...]}` envelope (vs a bare top-level array) was chosen specifically so future top-level fields (`generated_at`, `schema_version`, paging cursors) can be added without breaking jq pipelines. A bare `[...]` would have made any future addition a wire break. The envelope shape is the future-proofing equivalent of the placeholder-verb lesson: don't pick a shape that pins you to one possible expansion.
