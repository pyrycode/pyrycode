# E2E Harness

`internal/e2e` is a build-tag-isolated test harness that spawns `pyry` as a real
daemon in an isolated temp `$HOME`, blocks until the control socket is dialable,
drives CLI verbs against it, and tears down reliably on test cleanup.

Phase: tickets #68 (spawn + cleanup), #69 (CLI driver + first feature e2e), split
from #51.

## What It Does

- Builds `pyry` once per test process (or reuses `$PYRY_E2E_BIN`).
- Spawns it pointed at a `t.TempDir()` `$HOME`, with `/bin/sleep infinity` as the
  supervised "claude" and idle eviction disabled.
- Polls the Unix socket until `net.Dial` succeeds (5s deadline), short-circuiting
  if pyry exits early.
- On test cleanup: SIGTERM, escalate to SIGKILL after 3s, then `os.Remove` the
  socket. The temp `$HOME` is auto-cleaned by `t.TempDir`.

## Public API

Five exported names — `Harness`, `Start`, `RunResult`, `(*Harness).Run`, plus the
struct fields:

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
```

No `Option`s in this iteration. Per-verb typed wrappers (`Status()`, `Stop()`,
`Attach()`) land when the consuming session-verb tickets (#52, #54, #55, #56)
make them useful.

## Invocation

```
go test -tags=e2e ./internal/e2e/...
```

Default `go test ./...` does not compile the package — every file carries
`//go:build e2e`. Setting `PYRY_E2E_BIN=/path/to/pyry` skips the per-process
`go build` (CI optimization).

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

`pyry version` was rejected: it short-circuits in `main.go` before parsing
flags, so it doesn't exercise the socket plumbing the harness sells.

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
  `Harness.Attach()`) — land per-ticket as session verbs are built (#52, #54,
  #55, #56) if useful.
- `Option` type and any `WithFoo(...)` constructors.
- Stdin plumbing on `Run` — no current verb reads stdin; add when one does.
- CI wiring (`make e2e`, GitHub Actions matrix). Build-tag isolation means
  existing `go test ./...` keeps passing untouched.
- Race-mode harness build (`go build -race` inside `ensurePyryBuilt` when the
  parent suite uses `-race`).

## Related

- Specs: `docs/specs/architecture/68-e2e-harness-primitive.md`,
  `docs/specs/architecture/69-e2e-cli-driver.md`
- Pattern: lessons.md § Test helpers across packages (`/bin/sleep` as the
  benign fake claude)
- Consumers: Phase 1.1 session-verb tickets (#52, #54, #55, #56)
