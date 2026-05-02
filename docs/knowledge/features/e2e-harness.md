# E2E Harness

`internal/e2e` is a build-tag-isolated test harness that spawns `pyry` as a real
daemon in an isolated temp `$HOME`, blocks until the control socket is dialable,
drives CLI verbs against it, and tears down reliably on test cleanup.

Phase: tickets #68 (spawn + cleanup), #69 (CLI driver + first feature e2e),
#52 (CLI verbs e2e coverage — `stop`, `logs`, `version`, `status` stopped path
+ `RunBare` helper), split from #51.

## What It Does

- Builds `pyry` once per test process (or reuses `$PYRY_E2E_BIN`).
- Spawns it pointed at a `t.TempDir()` `$HOME`, with `/bin/sleep infinity` as the
  supervised "claude" and idle eviction disabled.
- Polls the Unix socket until `net.Dial` succeeds (5s deadline), short-circuiting
  if pyry exits early.
- On test cleanup: SIGTERM, escalate to SIGKILL after 3s, then `os.Remove` the
  socket. The temp `$HOME` is auto-cleaned by `t.TempDir`.

## Public API

Six exported names — `Harness`, `Start`, `RunResult`, `(*Harness).Run`,
`RunBare`, plus the struct fields:

```go
type Harness struct {
    SocketPath string         // dial-able after Start returns
    HomeDir    string         // child's $HOME (registry, claude dir live underneath)
    PID        int            // captured at spawn for leak verification
    Stdout     *bytes.Buffer  // safe to read after process exit
    Stderr     *bytes.Buffer
}

func Start(t *testing.T) *Harness  // fail-fast: t.Fatalf on any error

type RunResult struct {
    ExitCode int
    Stdout   []byte
    Stderr   []byte
}

func (h *Harness) Run(t *testing.T, verb string, args ...string) RunResult

// RunBare invokes the cached pyry binary with args verbatim — no daemon
// spawn, no auto-injected -pyry-socket, no HOME redirection. For verbs
// that don't touch the control socket (e.g. `version`) or for negative
// tests that want to drive a verb against a deliberately-bogus socket
// path. Reuses the same binary cache and exit-code/timeout/capture
// machinery as Harness.Run.
func RunBare(t *testing.T, args ...string) RunResult
```

No `Option`s in this iteration. Per-verb typed wrappers (`Status()`, `Stop()`,
`Attach()`) intentionally not added — `Harness.Run` + `RunBare` cover every
shipped non-interactive verb. Wrappers land if a consumer materially benefits.

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
-pyry-claude=/bin/sleep
-pyry-idle-timeout=0
-- infinity
```

`/bin/sleep infinity` exists on Linux + macOS (per `lessons.md § Test helpers
across packages`), survives until SIGTERM, and the readiness gate doesn't depend
on the child being a real claude. `IdleTimeout=0` defeats the eviction timer.

## Readiness Signal

Poll `os.Stat` + `net.Dial` on the socket with a 5s deadline and 50ms gap.
Once `Dial` succeeds, the control server is in `Serve` (per
`cmd/pyry/main.go`'s `ctrl.Listen → go ctrl.Serve(ctx)` ordering), so the
daemon is responsive even if the supervised child hasn't spawned yet —
sufficient for the "daemon is alive" contract.

A second `select` watches `doneCh` (closed by the wait goroutine on
`cmd.Wait` return). An early pyry exit short-circuits the deadline and surfaces
captured stderr in the `t.Fatalf` message.

## CLI Driver (`Harness.Run`)

`Run(t, verb, args...)` invokes the cached pyry binary with `<verb>
-pyry-socket=<h.SocketPath> <args...>`, waits for it to exit (10s
`context.WithTimeout`), and returns a `RunResult{ExitCode, Stdout, Stderr}`.

```go
func TestStatusReportsRunning(t *testing.T) {
    h := e2e.Start(t)

    r := h.Run(t, "status")
    if r.ExitCode != 0 {
        t.Fatalf("pyry status exit=%d stderr=%s", r.ExitCode, r.Stderr)
    }
    if !bytes.Contains(r.Stdout, []byte("Phase:")) {
        t.Fatalf("status output missing Phase: line: %s", r.Stdout)
    }
}
```

### Argument Layout

```
[binPath]
"status"                          // verb (caller-provided, positional)
"-pyry-socket=" + h.SocketPath    // injected
<caller's args...>
```

Verb is positional because pyry dispatches subcommands on `os.Args[1]` — flags
must come *after* the verb. Encoding that into the signature
(`verb string, args ...string`) prevents the obvious footgun of writing
`h.Run(t, "-pyry-socket=other", "status")`.

Caller-side override is naturally available: Go's `flag` package takes the
*last* value, so `h.Run(t, "status", "-pyry-socket=somewhere-else")` wins
without any special-case logic in the harness.

### Why `RunResult` (struct), not a tuple

Future-proofs for `Duration`/`Combined`/`OOMed` additions without call-site
churn. Named fields prevent the obvious `[]byte` mix-up between stdout and
stderr that a positional tuple invites.

### Reusing harness state

- `binPath` is the package-level var written by `ensurePyryBuilt` inside
  `Start`. `sync.Once`'s happens-before guarantee means any post-`Start`
  read is safe — no need to plumb the path through `Harness`.
- `childEnv(h.HomeDir)` is reused verbatim. The CLI client doesn't strictly
  *need* `HOME` redirection (`-pyry-socket=` is explicit), but stripping
  `PYRY_NAME` defends against the operator's shell alias leaking into a
  future verb that resolves an instance by name independently of the socket.

### Failure Posture

| Failure | Response |
|---|---|
| `cmd.Run` returns `*exec.ExitError` | `RunResult` with non-zero `ExitCode` (caller asserts) |
| `cmd.Run` returns any other error | `t.Fatalf` (exec/fork failure — caller can't recover) |
| 10s deadline expires | `t.Fatalf` with stdout + stderr (daemon-side hang) |
| `cmd.Run` returns nil | `RunResult` with `ExitCode = 0` |

The asymmetry — non-zero exit returned, exec failure fatal — is intentional:
non-zero exit is *data the test asserts on*; a fork failure is infrastructure
breaking, with no useful recovery in test code.

The 10s timeout is the wrapper budget; `pyry status` itself uses a 5s
socket-dial timeout in `runStatus`, so the wrapper budget gives a comfortable
margin without letting a hung daemon stall a test indefinitely. No regression
test for the timeout path — constructing a daemon that hangs `pyry status`
for >10s would require either a real claude that doesn't respond or test-only
socket injection, both significantly more invasive than the safety net buys
us. Per evidence-based fix selection, the deadline branch is defensive only.

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

The helper is the *only* harness API added in #52. `Harness.Stop()` mid-test,
typed `Status()` / `Logs()` wrappers, etc. were explicitly declined to keep the
harness surface minimal.

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
6. `HomeDir` is auto-cleaned by `t.TempDir`.

The `sync.Once` makes this safe to call from a future manual `Stop()` plus
`t.Cleanup` without double-firing.

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

## Known Limitations

- **Race detector.** When `go test -tags=e2e -race` is invoked, the parent
  binary is race-instrumented but the harness's `go build` runs without
  `-race`. The follow-up may want `go build -race` when the parent suite uses
  it. Not load-bearing for the primitive; filed for the follow-up.
- **Windows.** Out of scope per CLAUDE.md. The harness uses POSIX signals
  (SIGTERM, SIGKILL) and Unix sockets; no build constraint beyond the e2e tag
  is needed because pyry itself is Linux + Darwin only.

## Deliberately Out of Scope

- Per-verb typed wrappers (`Harness.Status()`, `Harness.Stop()`,
  `Harness.Attach()`) — `Run` + `RunBare` cover every shipped verb; add
  wrappers if a consumer materially benefits.
- `Harness.Stop()` mid-test helper — `Run(t, "stop")` plus the bounded
  `processAlive`/`os.Stat` poll covers the only consumer that needs it.
- `Option` type and any `WithFoo(...)` constructors.
- Stdin plumbing on `Run` — no current verb reads stdin; add when one does.
- `pyry attach` e2e — interactive PTY, separate work; the harness's
  non-interactive `Run` is not the right driver for it.
- Asserting on specific log line content (couples tests to supervisor
  wording) or specific dial-error wording (couples to platform/syscall
  library).
- CI wiring (`make e2e`, GitHub Actions matrix). Build-tag isolation means
  existing `go test ./...` keeps passing untouched.
- Race-mode harness build (`go build -race` inside `ensurePyryBuilt` when the
  parent suite uses `-race`).
- `t.Parallel` migration on the e2e tests — defer until wall-clock pressure
  surfaces. Each test owns its own `t.TempDir` HOME, so parallelism is safe
  in principle.

## Related

- Specs: `docs/specs/architecture/68-e2e-harness-primitive.md`,
  `docs/specs/architecture/69-e2e-cli-driver.md`,
  `docs/specs/architecture/52-cli-verbs-e2e-coverage.md`,
  `docs/specs/architecture/80-e2e-install-systemd-roundtrip.md`
- Pattern: lessons.md § Test helpers across packages (`/bin/sleep` as the
  benign fake claude)
- Consumers: shipped CLI verbs (#52: `stop`, `logs`, `version`,
  `status` stopped path; #69: `status` running path), Phase 1.1 session-verb
  tickets (#54, #55, #56), install-service round-trip ([install-e2e.md](install-e2e.md))
