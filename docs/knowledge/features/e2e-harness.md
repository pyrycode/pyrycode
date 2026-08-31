# E2E Harness

`internal/e2e` is a build-tag-isolated test harness that spawns `pyry` as a real
daemon in an isolated temp `$HOME`, blocks until the control socket is dialable,
drives CLI verbs against it, and tears down reliably on test cleanup.

Phase: tickets #68 (spawn + cleanup), #69 (CLI driver + first feature e2e), #52 (CLI verbs e2e coverage — `stop`, `logs`, `version`, `status` stopped path
+ `RunBare` helper), #106 (restart primitive — `StartIn` / `Stop` + first
restart-survival test), #107 (two more restart-survival tests — evicted
state + `lastActiveAt` timestamps — plus file-local `newRegistryHome`
helper), #111 (failed-start primitive — `StartExpectingFailureIn` + the
corrupt-registry fail-loud test), #112 (positive-outcome startup test —
`TestE2E_Startup_MissingClaudeProjectsDir`, no harness changes), #115
(idle-eviction + lazy-respawn e2e — variadic flags on `StartIn` / `spawn`
+ two new tests asserting eviction and respawn at the binary boundary), #125 (attach PTY harness — `AttachHarness` + `StartAttach(t, sessionID)`
in `attach_pty.go` + `TestE2E_Attach_RoundTripsBytes` proving terminal →
attach client → control socket → bridge → supervisor PTY → claude →ack
flow at the binary boundary), #123 (rotation primitive — `StartRotation(t,
home, sessionsDir, initialUUID, trigger)` constructor wires #122's
fake-claude binary as the supervised child via `-pyry-claude=<fakeBin>` +
three `PYRY_FAKE_CLAUDE_*` env vars; refactors `spawn` over a shared
`spawnWith(t, home, spawnOpts)` core), #127 (attach clean-detach proof —
`AttachHarness.WaitDetach(t, timeout)` + `AttachHarness.Run(t, verb,
args...)` methods on the existing struct, `runVerb` extracted from
`Harness.Run` as the shared body, plus
`TestE2E_Attach_DetachesCleanly` driving the documented `Ctrl-B d`
sequence and asserting the triple invariant attach-exits-0 +
daemon-survives + supervised-child-still-`Phase: running`), #120
(rotation-watcher e2e — `TestE2E_RotationWatcher_DetectsClear` consumes
`StartRotation` + the fake-claude binary to drive a real pyry through
one `/clear`-shaped JSONL rotation; asserts the registry's bootstrap
id follows from the pre-created `<initialUUID>.jsonl` to the post-trigger
fresh UUID against the real `/proc`-or-`lsof` probe; no harness changes), #128 (attach survives claude restart e2e —
`TestE2E_Attach_SurvivesClaudeRestart` extends `TestHelperProcess`'s
`echo` mode with a startup PID marker + `__EXIT__\n`/`__PID__\n`
control lines and drives an attach across a forced child restart; the
spec was billed as test-only but the test surfaced a real production
bug in the supervisor's bridge input path — the input pump leaked
across `runOnce` iterations and silently corrupted bytes typed during
a restart. Fix: replaced `io.Pipe` in `Bridge` with a `chan []byte` +
per-iteration cancel signal (`BeginIteration` / `EndIteration`),
supervisor now drains both copy goroutines on each iteration. See
[ADR 007](../decisions/007-bridge-iteration-boundaries.md)),
split from #51.

## What It Does

- Builds `pyry` once per test process (or reuses `$PYRY_E2E_BIN`).
- Spawns it pointed at a `t.TempDir()` `$HOME`, with the sleep-claude wrapper
  (see § Isolation Strategy) as the default supervised "claude" and idle
  eviction disabled.
- Polls the Unix socket until `net.Dial` succeeds (5s deadline), short-circuiting
  if pyry exits early.
- On test cleanup: SIGTERM, escalate to SIGKILL after 3s, then `os.Remove` the
  socket. The temp `$HOME` is auto-cleaned by `t.TempDir`.

## Invocation

```
go test -tags=e2e ./internal/e2e/...
go test -tags=e2e_install ./internal/e2e/...   # install-service round-trip (Linux)
```

Default `go test ./...` does not compile the package. The harness file's
build tag is `//go:build e2e || e2e_install` so the binary cache and
`childEnv` helper are reusable from the install-e2e tests (see
[install-e2e.md](install-e2e.md)) without duplicating boilerplate. Setting
`PYRY_E2E_BIN=/path/to/pyry` skips the per-process `go build` (CI
optimization).

## Isolation Strategy

Pyry resolves `~/.pyry/<name>.sock`, `~/.pyry/<name>/sessions.json`, and
`~/.claude/projects/<encoded-cwd>/<uuid>.jsonl` via `os.UserHomeDir()`, which
honors `$HOME` on Unix. The harness redirects `HOME` to `t.TempDir()` so every
path the daemon would touch under a real home is contained, with one env var.

Belt-and-suspenders: `-pyry-socket=<HomeDir>/pyry.sock` is also passed
explicitly. The registry still lands at `<HomeDir>/.pyry/test/` via HOME
redirection — no new `-pyry-registry` flag was needed.

`PYRY_NAME` is stripped from the child's env so the operator's shell alias can't
leak into a test daemon.

Spawn args:

```
-pyry-socket=<HomeDir>/pyry.sock
-pyry-name=test
-pyry-claude=<home>/sleep-claude.sh
-pyry-idle-timeout=0
<extraFlags...>          # variadic, last-wins via Go's flag package
-- 99999
```

`-pyry-claude` defaults (zero-value `spawnOpts.claudeBin`) to the
sleep-claude wrapper — `writeSleepClaude(t, home)` writes `#!/bin/sh` +
`exec sleep 99999` to `<home>/sleep-claude.sh` (0o755) and returns its path
(#918; was a bare `/bin/sleep` before). The wrapper ignores all argv, so it
tolerates both the bootstrap invocation's trailing `99999` and any
daemon-appended spawn-time flag — see § Default Stand-In Argv Tolerance
below for why a bare `/bin/sleep` stopped working. `exec sleep 99999`
survives ~27 hours (longer than any test runs), and the readiness gate
doesn't depend on the child being a real claude. `99999` (a plain integer in
seconds) is the only argv form portable across BSD and GNU `sleep`:
`infinity` is GNU coreutils only and macOS BSD sleep rejects it (see
`lessons.md § Test helpers across packages`). #115 changed the harness from
`infinity` to `99999` because the lazy-respawn test waits for `Phase:
running` after a respawn — under `infinity`, macOS BSD sleep exits
immediately, the supervisor enters perpetual backoff, and `Phase: running`
is never observed. `IdleTimeout=0` defeats the eviction timer by default;
tests that need eviction pass `-pyry-idle-timeout=<dur>` via the variadic on
`StartIn`.

## Readiness Signal

Poll `os.Stat` + `net.Dial` on the socket with a 5s deadline and 50ms gap.
Once `Dial` succeeds, the control server is in `Serve` (per
`cmd/pyry/main.go`'s `ctrl.Listen → go ctrl.Serve(ctx)` ordering), so the
daemon is responsive even if the supervised child hasn't spawned yet —
sufficient for the "daemon is alive" contract.

A second `select` watches `doneCh` (closed by the wait goroutine on
`cmd.Wait` return). An early pyry exit short-circuits the deadline and surfaces
captured stderr in the `t.Fatalf` message.

## First Feature E2E (`TestStatus_E2E`)

```go
func TestStatus_E2E(t *testing.T) {
    h := Start(t)

    r := h.Run(t, "status")
    if r.ExitCode != 0 {
        t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
            r.ExitCode, r.Stdout, r.Stderr)
    }
    if !bytes.Contains(r.Stdout, []byte("Phase:")) {
        t.Errorf("status stdout missing %q line:\n%s", "Phase:", r.Stdout)
    }
}
```

`"Phase:"` is the leading literal in `runStatus`'s output (`fmt.Printf("Phase:
        %s\n", resp.Phase)`) and is stable across phase values, restart counts,
and future field additions. Asserting on the *value* (`PhaseRunning` etc.)
would couple the test to claude-child startup timing — exactly what
`/bin/sleep infinity` was chosen to avoid. The contract this test verifies is
"daemon is up, socket answers, status verb round-trips."

`pyry version` was rejected as the *proof-of-life* verb (it short-circuits in
`main.go` before parsing flags, so it doesn't exercise the socket plumbing the
harness sells), but is covered by `TestVersion_E2E` below via `RunBare`.

## Bare CLI Driver (`RunBare`)

`RunBare(t, args...)` is the daemon-free sibling of `Harness.Run`. Same binary
cache (`ensurePyryBuilt`), same `runTimeout` (10s), same exit-code mapping —
but no daemon spawn, no auto-injected `-pyry-socket`, no `childEnv(h.HomeDir)`.
The test process env passes through unchanged.

Two use cases motivated the helper:

1. **Verbs that don't touch the socket.** `pyry version` short-circuits in
   `main.go` before flag parsing. Spinning up a daemon to test it is wasted
   wall-clock and inverts the test's intent.
2. **Negative tests against a known-bad socket path.** "Run `status` against a
   socket with no daemon" is most cleanly expressed as "point at a fresh temp
   path and assert the failure shape" — no spawn-then-stop-then-race-the-
   teardown ordering glue.

The helper is the *only* harness API added in #52. (`Harness.Stop()` mid-test
was deferred at the time and shipped later in #106 — see the Restart Pattern
section above. Typed `Status()` / `Logs()` wrappers remain declined.)

## HOME-Isolated Bare Driver (`RunBareIn`)

`RunBareIn(t, home, args...)` (added in #213) is `RunBare` with one
line spliced in: `cmd.Env = childEnv(home)`. The pinned `HOME` lets a
daemon-free verb read its `~`-relative state from a `t.TempDir()`
without disturbing the test process's environment. `pyry pair` is the
first consumer — its target paths (`~/.pyry/config.json`,
`~/.pyry/<name>/devices.json`, `~/.pyry/<name>/server-id`) all resolve
through `os.UserHomeDir()`, and the e2e test asserts the post-run
contents of `<home>/.pyry/pyry/devices.json`. Default instance name
`"pyry"` is used because no `-pyry-name` flag is passed and `childEnv`
does not set `PYRY_NAME`. See
[features/pyry-pair-command.md](pyry-pair-command.md) for the
verb-side contract.

## CLI Verb Coverage Tests (`cli_verbs_test.go`)

`internal/e2e/cli_verbs_test.go` (build tag `//go:build e2e`) covers the
remaining shipped non-interactive verbs. Lives in its own file alongside
`harness_test.go` — the latter is about *harness behaviour* (smoke,
no-leak-on-fatal, the canonical `TestStatus_E2E` proof-of-life), the former
about *CLI surface coverage*. `processAlive` from `harness_test.go` is reused
via package scope.

| Test | What it asserts |
|---|---|
| `TestStop_E2E` | exit 0, stdout contains `"stop requested"` fragment, then bounded poll (3s deadline, 50ms gap) until both `!processAlive(pid)` AND `os.Stat(sock)` returns `fs.ErrNotExist` |
| `TestStatus_E2E_Stopped` | `RunBare("status", "-pyry-socket="+bogusSock)` against a fresh non-existent path: exit != 0, non-empty stderr, no `panic` / `goroutine ` / `runtime/` substrings |
| `TestLogs_E2E` | exit 0, non-empty `bytes.TrimSpace(r.Stdout)` (the supervisor's in-memory ring captures startup lines, so a healthy daemon's log buffer is never empty by the time `Start(t)` returns) |
| `TestVersion_E2E` | `RunBare("version")`: exit 0, output starts with literal `"pyry "` prefix, remaining token is non-empty (`dev` in test builds, real version under `-ldflags`) |

### Why bogus-socket, not spawn-then-stop, for the stopped-status test

The spawn-then-stop-then-status path needs the test to wait for the daemon to
actually shut down (otherwise status hits a still-listening socket and
succeeds, defeating the test). That's the same poll loop as `TestStop_E2E`,
plus ordering glue, plus a second `Run` call. The bogus-socket variant
exercises the same code path (`net.Dial` fails → error surfaces clean to
stderr → non-zero exit) without any timing dependency. Strictly simpler,
strictly more deterministic.

### Why poll *both* `processAlive` and `os.Stat(sock)` in `TestStop_E2E`

`pyry stop` returns once the server has acknowledged the request, but the
daemon's child unwind and the supervisor's deferred socket cleanup happen
asynchronously after `Wait` returns. Asserting on either condition alone
admits a flake. Both in the same iteration costs nothing (each probe is
syscall-cheap) and avoids racing the cleanup defer.

### Negative assertion vocabulary for "clean error"

`TestStatus_E2E_Stopped` deliberately doesn't pin the dial-failure error
wording (today: `pyry: status: ... connect: no such file or directory`) — that
string is allowed to evolve. Instead it asserts the *shape* of the failure:

- `panic` — Go's panic header
- `goroutine ` — Go's stack-trace header (`goroutine N [state]:`)
- `runtime/` — Go runtime frames in tracebacks

Three conservative substrings catch panics and stack traces without coupling
to the exact wording. The same pattern fits any "clean error, not a crash"
assertion.

## Concurrency Model

| Goroutine | Owns | Lifetime |
|---|---|---|
| Test goroutine | `Start` flow, teardown | Test scope |
| Wait goroutine | `cmd.Wait()`, `close(doneCh)` | From `cmd.Start` until child exits |

`Stdout`/`Stderr` are `bytes.Buffer`s wired into `cmd.Stdout`/`cmd.Stderr`
directly — `exec.Cmd` synchronizes its writers with `Wait`, so reads after
`<-doneCh` are race-free without an explicit mutex.

`sync.Once` guards build (`binOnce`) and teardown (`cleanupOnce`). No locks.

## Teardown Sequence

Registered via `t.Cleanup`:

1. `cmd.Process.Signal(SIGTERM)`
2. Wait on `doneCh` with a 3s grace timer.
3. On grace expiry: `SIGKILL`, wait another 1s on `doneCh`.
4. On SIGKILL grace expiry: `t.Logf` warning; let leak verification surface it.
5. `os.Remove(SocketPath)` — defensive, since SIGKILL bypasses pyry's own
   socket cleanup.
6. `HomeDir` is auto-cleaned by `t.TempDir` when allocated by `Start(t)`.
   Under `StartIn(t, home)` the caller owns the directory's lifecycle —
   teardown leaves `HomeDir` intact so a subsequent `StartIn` can reuse it.

The `sync.Once` makes this safe to call from a manual `Stop()` (shipped in #106) plus `t.Cleanup` without double-firing.

## Failure Posture

Fail-fast — `Start` calls `t.Fatalf` rather than returning an error, since the
only reasonable response in test code is to abort.

| Failure | Response |
|---|---|
| `go build` fails | `t.Fatalf` with build output |
| `cmd.Start` fails | `t.Fatalf` |
| Readiness deadline | `t.Fatalf` with stderr tail |
| Pyry exits during readiness | `t.Fatalf` with stderr tail |
| SIGTERM grace expires | escalate to SIGKILL |
| SIGKILL grace expires | `t.Logf` warning |
| `os.Remove(socket)` post-kill | best-effort, ignore err |

## Failure-Injection Verification

`TestHarness_NoLeakOnFatal` verifies the load-bearing safety property: a
`t.Fatal` mid-test must not leak a `pyry` process or socket file.

The naive in-process subtest (`t.Run("crash", ...)`) doesn't work — Go's testing
framework propagates an inner `t.Fatal` to the parent, ending the outer test
before it can inspect leak state. The harness re-execs the test binary instead:

```
parent test
  └── exec.Command(os.Args[0], -test.run=^TestInnerFatalChild$, ...)
        with PYRY_E2E_INNER_FATAL_OUT=<state-file>
        │
        └── child test process
              ├── Start(t) → Harness
              ├── write (pid, socket) to state-file
              └── t.Fatal — exercises harness cleanup
        ↓ child exits ↓
  ├── read state-file
  ├── processAlive(pid)?  via `kill -0` (POSIX zero-signal probe)
  └── os.Stat(sock) is fs.ErrNotExist?
```

`TestInnerFatalChild` is gated on `PYRY_E2E_INNER_FATAL_OUT` — unset in normal
runs (`t.Skip`), set under the parent's re-exec. The state file passes the
observed pid + socket path across the process boundary.

`processAlive` uses `os.FindProcess` + `Signal(syscall.Signal(0))` — POSIX
"is this PID alive" probe, zero-cost, returns ESRCH if gone.

## Build Helper

`ensurePyryBuilt(t)` builds pyry once per test process via `sync.Once` into a
persistent `os.MkdirTemp` (intentionally not cleaned — `go test`'s own cleanup
takes /tmp eventually, and there's no `TestMain` hook this package owns).
`PYRY_E2E_BIN` short-circuits to a known-good binary on disk for CI.

**An `-overlay` mutant does not reach a spawned daemon by default.** `ensurePyryBuilt`
shells a plain `go build ./cmd/pyry` with none of the parent `go test` invocation's
flags, so mutating `cmd/pyry` source and re-running the suite under `-overlay=<path>`
only mutates the *test binary's own compilation unit* — the child `pyry` these specs
spawn is unaffected and the mutant silently fails to redden anything. To exercise a
mutant against the spawned daemon, build it separately
(`go build -overlay=<path> -o <bin> ./cmd/pyry`) and inject it via `PYRY_E2E_BIN`
(#1512). A mutation run that skips this reads as "the assertion under test is
vacuous" when the real cause is "the mutant never shipped".

**A green run under `PYRY_E2E_BIN` still needs a control (#1845).** A mutant
built and injected this way can be green for two indistinguishable reasons:
the assertion is genuinely dead weight, or the injection silently didn't take
(stale cached binary, a build that failed in a way that still left a binary
on disk). Pair every `PYRY_E2E_BIN` mutant run with a control run of the
*clean* binary through the identical `PYRY_E2E_BIN` invocation — only a
clean-PASS / mutant-FAIL pair proves the mutant actually reached the spawned
daemon.

**A test-local `t.Setenv("HOME", …)` must come after `ensurePyryBuilt`/
`ensureFakeClaudeBuilt`, not before (#1631).** Both are `sync.Once`-guarded, so
whichever caller runs first performs the actual `go build`; every later call
(including `spawnWith`'s own) is a no-op that just returns the cached path.
Overriding `HOME` in the test process *before* either call sends that `go build`
subprocess an empty module cache under the temp home, which tries to re-download
the whole module graph and fails on a private dependency. This only bites a test
that needs `HOME` overridden in the test process itself (e.g. to re-derive a
daemon-side value that reads `$HOME`, as #1631's own AC 2 test does) — the
ordinary `Harness.Run`/`StartIn` path never hits it, since it redirects `HOME` for
the **spawned daemon's** environment via `childEnv`, not the test process's own.

## Known Limitations

- **Race detector.** When `go test -tags=e2e -race` is invoked, the parent
  binary is race-instrumented but the harness's `go build` runs without
  `-race`. The follow-up may want `go build -race` when the parent suite uses
  it. Not load-bearing for the primitive; filed for the follow-up.
- **Windows.** Out of scope per CLAUDE.md. The harness uses POSIX signals
  (SIGTERM, SIGKILL) and Unix sockets; no build constraint beyond the e2e tag
  is needed because pyry itself is Linux + Darwin only.

## Deliberately Out of Scope

- Per-verb typed wrappers (`Harness.Status()`, `Harness.Attach()`) — `Run`
  + `RunBare` cover every shipped verb; add wrappers if a consumer
  materially benefits.
- `Options` struct for `StartIn` — today there's exactly one knob (`home`).
  Migration to `Options{Home: ..., ...}` is mechanical and non-breaking
  (`StartIn` becomes a thin alias) when a second knob lands.
- `Option` type and any `WithFoo(...)` constructors.
- Stdin plumbing on `Run` — no current verb reads stdin; add when one does.
- `pyry attach` e2e — interactive PTY, separate work; the harness's
  non-interactive `Run` is not the right driver for it.
- Asserting on specific log line content (couples tests to supervisor
  wording) or specific dial-error wording (couples to platform/syscall
  library).
- GitHub Actions matrix running the suite on push/PR — no such workflow
  exists (`release.yml` is tag-triggered, `self-check-daily.yml` is a daily
  cron). `make e2e` (standalone target) and its wiring into the local
  `make check` gate landed separately — see below.
- Race-mode harness build (`go build -race` inside `ensurePyryBuilt` when the
  parent suite uses `-race`).
- `t.Parallel` migration on the e2e tests — defer until wall-clock pressure
  surfaces. Each test owns its own `t.TempDir` HOME, so parallelism is safe
  in principle.

## Build Gate

`make e2e` (`go test -tags e2e -race -count=1 ./internal/e2e/...`) is part of
`make check` as of #919 — a core-daemon regression now fails the standard
local gate instead of sitting red on `main` unnoticed (the #918 incident: eight #839-era regressions accumulated because the suite ran nowhere by default).
`make preship`'s prerequisite list dropped its own explicit `e2e` token in the
same change since `check` now covers it; `preship` still runs `e2e-realclaude`
(live claude, separate suite) on top. No GitHub Actions workflow runs this on
push/PR yet — see the out-of-scope note above. Details: [codebase/919.md](../codebase/919.md).

## Related

- Specs: `docs/specs/architecture/68-e2e-harness-primitive.md`,
  `docs/specs/architecture/69-e2e-cli-driver.md`,
  `docs/specs/architecture/52-cli-verbs-e2e-coverage.md`,
  `docs/specs/architecture/80-e2e-install-systemd-roundtrip.md`,
  `docs/specs/architecture/106-e2e-restart-primitive.md`,
  `docs/specs/architecture/107-e2e-restart-evicted-and-lastactiveat.md`,
  `docs/specs/architecture/111-e2e-corrupt-registry.md`,
  `docs/specs/architecture/112-e2e-missing-claude-projects-dir.md`,
  `docs/specs/architecture/115-e2e-idle-eviction-lazy-respawn.md`,
  `docs/specs/architecture/125-e2e-attach-pty-harness.md`,
  `docs/specs/architecture/123-e2e-startrotation-primitive.md`,
  `docs/specs/architecture/127-e2e-attach-detach-clean.md`,
  `docs/specs/architecture/128-e2e-attach-survives-claude-restart.md`,
  `docs/specs/architecture/956-fakeclaude-rotation-nonempty-gate.md`,
  `docs/specs/architecture/1141-stream-e2e-harness-send-message.md`
- Pattern: lessons.md § Test helpers across packages (`/bin/sleep` as the
  benign fake claude); lessons.md § Unix-socket sun_path limits and
  t.TempDir(); lessons.md § PTY master backpressure stalls slave-side
  process exit
- Consumers: shipped CLI verbs (#52: `stop`, `logs`, `version`,
  `status` stopped path; #69: `status` running path), restart-survival
  proofs (#106: `TestE2E_Restart_PreservesActiveSessions`; #107:
  `TestE2E_Restart_PreservesEvictedSessions`,
  `TestE2E_Restart_LastActiveAtSurvives`), startup-failure proofs
  (#111: `TestE2E_Startup_CorruptRegistryFailsClean`),
  startup positive-outcome proofs (#112:
  `TestE2E_Startup_MissingClaudeProjectsDir`), idle-eviction +
  lazy-respawn proofs (#115: `TestE2E_IdleEviction_EvictsBootstrap`,
  `TestE2E_IdleEviction_LazyRespawn`), attach PTY round-trip proof
  (#125: `TestE2E_Attach_RoundTripsBytes` via `AttachHarness`),
  rotation primitive (#123: `TestE2E_StartRotation_PrimitiveWiresFakeClaude`
  via `StartRotation` + [fakeclaude-binary.md](fakeclaude-binary.md)),
  attach clean-detach proof (#127:
  `TestE2E_Attach_DetachesCleanly` via `AttachHarness.WaitDetach` +
  `AttachHarness.Run`),
  attach restart-survival proof (#128:
  `TestE2E_Attach_SurvivesClaudeRestart` — also surfaced and fixed
  the bridge input-pump leak; see
  [ADR 007](../decisions/007-bridge-iteration-boundaries.md)),
  Phase 1.1 session-verb tickets (#54, #55, #56),
  install-service round-trip ([install-e2e.md](install-e2e.md)),
  stream-interactive turn-drain proof (#1141:
  `TestRelayV2_StreamSendMessageDrainsTurn` via
  `StartStreamInteractiveWithRelay` + [fakeclaude-binary.md § Stream-json mode](fakeclaude-binary.md#stream-json-mode-1140);
  the harness the rider specs #1136–#1139 and the real-claude capstone #1083 ride)


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Public API](e2e-harness-public-api.md) — Twelve exported names — `Harness`, `Start`, `StartIn`, `StartInWithEnv`, `StartRotation`, `StartRotationWithRelay`,…
- [CLI Driver (`Harness.Run`)](e2e-harness-cli-driver-harness-run.md) — `Run(t, verb, args...)` invokes the cached pyry binary with `<verb> -pyry-socket=<h.SocketPath> <args...>`, waits for it to exit (10s…
- [Restart Pattern (`StartIn` + `Stop`)](e2e-harness-restart-pattern-startin-stop.md) — `StartIn` + `Stop` together let a test prove on-disk invariants survive daemon restart: pre-populate `HOME` → `Start` → `Stop` → second…
- [Failed-Start Pattern (`StartExpectingFailureIn`)](e2e-harness-failed-start-pattern-startexpectingfailurein.md) — `StartExpectingFailureIn(t, home) RunResult` is the failure-side sibling of `StartIn`. 
- [Idle-Eviction + Lazy-Respawn Pattern (`idle_test.go`, #115)](e2e-harness-idle-eviction-lazy-respawn-pattern-idle-test-go.md) — Two tests in `internal/e2e/idle_test.go` (build tag `e2e`) exercise the idle-eviction state machine and lazy respawn at the binary boundary…
- [Active-Cap Eviction Pattern (`cap_test.go`, #116)](e2e-harness-active-cap-eviction-pattern-cap-test-go.md) — Two tests in `internal/e2e/cap_test.go` (build tag `e2e`) close the binary-boundary gap on the concurrent active cap (#41). 
- [Default Stand-In Argv Tolerance (#918)](e2e-harness-default-stand-in-argv-tolerance.md) — The zero-value `spawnWith` path (used by `Start` / `StartIn` / `StartInWithEnv`, and by `StartExpectingFailureIn` via `spawn`) invoked…
- [Attach PTY Harness Pattern (`attach_pty.go`, `attach_pty_test.go`, #125)](e2e-harness-attach-pty-harness-pattern-attach-pty-go-attach.md) — The non-interactive `Harness` drives daemon-only verbs over the control socket with stdio pipes. 
- [Rotation Primitive (`StartRotation`, `fakeclaude_test.go`, #123)](e2e-harness-rotation-primitive-startrotation-fakeclaude-test.md) — `StartRotation(t, home, sessionsDir, initialUUID, trigger) *Harness` is the constructor that swaps `/bin/sleep 99999` for the [fake-claude…
- [Attach Detach Pattern (`attach_detach_test.go`, #127)](e2e-harness-attach-detach-pattern-attach-detach-test-go.md) — `TestE2E_Attach_DetachesCleanly` drives the documented `Ctrl-B d` sequence (bytes `0x02 0x64`) into a live attach session and asserts the…
- [Attach Restart Pattern (`attach_restart_test.go`, #128)](e2e-harness-attach-restart-pattern-attach-restart-test-go.md) — `TestE2E_Attach_SurvivesClaudeRestart` is the load-bearing proof of the supervisor's restart loop: an attached `pyry attach` client remains…
- [Attach SIGWINCH Pattern (`attach_pty_test.go`, #126)](e2e-harness-attach-sigwinch-pattern-attach-pty-test-go.md) — `TestE2E_Attach_HandlesSIGWINCH` proves the full live-resize chain end-to-end at the binary boundary: a real `pty.Setsize` on the harness's…
- [Rotation Watcher Pattern (`rotation_test.go`, #120)](e2e-harness-rotation-watcher-pattern-rotation-test-go.md) — `TestE2E_RotationWatcher_DetectsClear` is the consumer for #122's fake-claude binary and #123's `StartRotation` primitive. 
- [Stdio-Attach Harness Pattern (`attach_stdio.go`, `attach_stdio_test.go`, #161)](e2e-harness-stdio-attach-harness-pattern-attach-stdio-go-att.md) — The PTY harness (`AttachHarness`, #125) drives `pyry attach` against a controlling terminal. 
- [Foreground Auto-Attach Harness Pattern (`auto_attach.go`, `auto_attach_happy_test.go`, #163)](e2e-harness-foreground-auto-attach-harness-pattern-auto-atta.md) — The stdio-attach harness drives `pyry attach --stdio <uuid>`. 
- [Foreground Auto-Attach Fallback Pattern (`auto_attach_fallback_test.go`, #164)](e2e-harness-foreground-auto-attach-fallback-pattern-auto-att.md) — The happy-path test (#163) pins that auto-attach fires when the daemon hosts the requested UUID. 
- [Stream Interactive Harness Pattern (`StartStreamInteractiveWithRelay`, #1141)](e2e-harness-stream-interactive-harness-pattern-startstreamin.md) — First e2e opt-in to the stream-json `interactive_runner` (#1081 shipped the production toggle; #1140 shipped the stream-json fakeclaude…
