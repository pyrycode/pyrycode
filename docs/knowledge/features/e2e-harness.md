# E2E Harness

`internal/e2e` is a build-tag-isolated test harness that spawns `pyry` as a real
daemon in an isolated temp `$HOME`, blocks until the control socket is dialable,
drives CLI verbs against it, and tears down reliably on test cleanup.

Phase: tickets #68 (spawn + cleanup), #69 (CLI driver + first feature e2e),
#52 (CLI verbs e2e coverage — `stop`, `logs`, `version`, `status` stopped path
+ `RunBare` helper), #106 (restart primitive — `StartIn` / `Stop` + first
restart-survival test), #107 (two more restart-survival tests — evicted
state + `lastActiveAt` timestamps — plus file-local `newRegistryHome`
helper), #111 (failed-start primitive — `StartExpectingFailureIn` + the
corrupt-registry fail-loud test), #112 (positive-outcome startup test —
`TestE2E_Startup_MissingClaudeProjectsDir`, no harness changes), split
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

Nine exported names — `Harness`, `Start`, `StartIn`, `StartExpectingFailureIn`,
`(*Harness).Stop`, `RunResult`, `(*Harness).Run`, `RunBare`, plus the struct
fields:

```go
type Harness struct {
    SocketPath string         // dial-able after Start returns
    HomeDir    string         // child's $HOME (registry, claude dir live underneath)
    PID        int            // captured at spawn for leak verification
    Stdout     *bytes.Buffer  // safe to read after process exit
    Stderr     *bytes.Buffer
}

func Start(t *testing.T) *Harness  // fail-fast: t.Fatalf on any error

// StartIn behaves like Start but uses the caller-supplied home directory
// instead of allocating a fresh t.TempDir(). Pre-populate it (e.g.
// <home>/.pyry/test/sessions.json) before calling to drive a daemon
// against a chosen on-disk state. Caller owns the directory's lifecycle.
func StartIn(t *testing.T, home string) *Harness

// Stop gracefully terminates the daemon (SIGTERM, grace, escalate to
// SIGKILL — same path as t.Cleanup teardown), waits for exit, and
// removes the socket. HomeDir is left intact. Idempotent with t.Cleanup
// teardown via sync.Once.
func (h *Harness) Stop(t *testing.T)

// StartExpectingFailureIn spawns pyry against the given home, expects it
// to exit before the readiness deadline elapses, and returns the captured
// exit code, stdout, and stderr. Fails the test if pyry instead becomes
// ready (control socket dialable) or if it neither exits nor becomes
// ready within the readiness deadline. No Harness is returned: there is
// no live daemon to drive, no socket to clean up.
func StartExpectingFailureIn(t *testing.T, home string) RunResult

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

`Start(t) *Harness` is now a one-line `return StartIn(t, t.TempDir())` —
existing call sites unchanged. `StartIn` is the workhorse; `Start` is the
common-case sugar. `Stop` is a public wrapper around the internal `teardown`
(name kept private to make the public/private split obvious to readers).

No `Option`s in this iteration. Per-verb typed wrappers (`Status()`,
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

The helper is the *only* harness API added in #52. (`Harness.Stop()` mid-test
was deferred at the time and shipped later in #106 — see the Restart Pattern
section above. Typed `Status()` / `Logs()` wrappers remain declined.)

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

## Restart Pattern (`StartIn` + `Stop`)

`StartIn` + `Stop` together let a test prove on-disk invariants survive
daemon restart: pre-populate `HOME` → `Start` → `Stop` → second `StartIn`
against the same `HOME` → assert the file directly.

```go
home, err := os.MkdirTemp("", "pyry-rs-*")
if err != nil { t.Fatalf("mkdir home: %v", err) }
t.Cleanup(func() { _ = os.RemoveAll(home) })

regDir := filepath.Join(home, ".pyry", "test")
_ = os.MkdirAll(regDir, 0o700)
_ = os.WriteFile(filepath.Join(regDir, "sessions.json"), []byte(registryJSON), 0o600)

h1 := e2e.StartIn(t, home)
h1.Stop(t)

h2 := e2e.StartIn(t, home) // same socket path, same registry; reads back the pre-write
_ = h2
// Inspect the registry file at <home>/.pyry/test/sessions.json directly.
```

### Why `os.MkdirTemp` instead of `t.TempDir()` for the HOME

Unix sockets cap `sun_path` at 104 bytes on macOS (108 on Linux).
`t.TempDir()` embeds the (long) test name into its path; for tests with
descriptive names (e.g. `TestE2E_Restart_PreservesActiveSessions`) the
appended `pyry.sock` overflows the limit. `os.MkdirTemp("", "pyry-rs-*")`
keeps the prefix tiny. Tests using `Start(t)` (short name or short dir) are
unaffected; the restart test's tighter budget motivates the explicit
`os.MkdirTemp` + `t.Cleanup(os.RemoveAll)`. See `lessons.md § Unix-socket
sun_path limits and t.TempDir()`.

### Why the same socket path works across the two spawns

`StartIn` derives `socket := filepath.Join(home, "pyry.sock")` — both
spawns use the same path. The second daemon's `Server.Listen`
(`internal/control/server.go`) handles a stale socket file via dial-probe
→ ECONNREFUSED → `os.Remove` → `net.Listen`; no test-level coordination
needed. By the time `Stop` returns, `cmd.Wait` has reaped the first
process, the listener fd is closed, and ECONNREFUSED is deterministic.
The defensive `os.Remove(h.SocketPath)` in teardown belt-and-suspenders
the SIGKILL path.

### Idempotency invariant

`cleanupOnce` (a `sync.Once`) guards a single teardown. Whichever fires
first — explicit `Stop(t)` or `t.Cleanup`'s deferred call — wins; the
other is a no-op. Two harnesses (`h1`, `h2`) own independent
`cleanupOnce` / `doneCh` / `cmd`; `t.Cleanup` runs LIFO, so `h2.teardown`
fires first against the live second daemon, then `h1.teardown` (no-op,
already torn down via `Stop`).

### `restart_test.go` — three restart-survival tests

Three tests live in `restart_test.go`, all built on the same `StartIn → Stop
→ StartIn` cycle against a pre-populated `<HOME>/.pyry/test/sessions.json`:

| Test | Ticket | Asserts |
|---|---|---|
| `TestE2E_Restart_PreservesActiveSessions` | #106 | registry file present after first `Stop`; `version` preserved; session count preserved; per-session `lifecycle_state` and `bootstrap` flag preserved |
| `TestE2E_Restart_PreservesEvictedSessions` | #107 | a non-bootstrap entry pre-written with `lifecycle_state: "evicted"` is still `"evicted"` after restart (no silent warm-promotion); paired with bootstrap-active and a non-bootstrap-active control so "evicted stays evicted" is meaningful next to a sibling that's provably not evicted |
| `TestE2E_Restart_LastActiveAtSurvives` | #107 | three sessions with `lastActiveAt` values spread by 10 min and 1 hour roundtrip across restart via `time.Time.Equal` (catches a re-stamp to `time.Now()` that would silently break the cap-policy LRU order) |

Deliberately **not** asserted by any of them: byte-identity of the file
(coupling to `MarshalIndent` output inverts the dependency direction — a
benign formatting change would break the tests). The first test also
deliberately omits `LastActiveAt` equality; that property is the dedicated
subject of the third test.

#### Helper: `newRegistryHome` (rule of three)

Once #107 landed, all three tests share the same four-line HOME bootstrap
(`os.MkdirTemp` for sun_path safety, `t.Cleanup(RemoveAll)`, `mkdir -p
<home>/.pyry/test`). #107 extracted this into a file-local helper —
package-internal, intentionally not promoted to `harness.go`'s public
surface (three callers ≠ a public API):

```go
// newRegistryHome creates a short-named temp HOME (sun_path-safe), pre-creates
// <home>/.pyry/test/, registers cleanup, and returns the home dir and the
// sessions.json path the harness's -pyry-name=test daemon will read.
func newRegistryHome(t *testing.T) (home, regPath string)
```

`registryEntry` / `registryFile` mirror types and the `writeRegistry` /
`readRegistry` / `mustReadFile` helpers from #106 stay file-local and
unchanged — same dependency-direction reasoning (importing the unexported
production schema solely for tests would invert it).

#### Fixture choice: bootstrap-active anchors every restart test

Each restart test pre-writes exactly one `bootstrap: true, lifecycle_state:
"active"` entry alongside the entries it cares about. The bootstrap-active
anchor keeps the harness's ready gate working the conventional way: the
supervisor spawns `/bin/sleep infinity`, the control server comes up, the
ready-poll succeeds. This deliberately avoids the bootstrap-evicted
permutation (warm-starting the bootstrap *itself* in `stateEvicted` enters
`runEvicted` instead of spawning the child). That path is functionally
distinct — "daemon comes up cleanly with an evicted bootstrap" — and
deserves its own ticket so failures isolate cleanly. The three current
tests are scoped to non-bootstrap survival.

The lifecycle strings written to disk are `"active"` and `"evicted"` —
exactly what `lifecycleState.String()` (`internal/sessions/session.go`)
emits and `parseLifecycleState` parses. Don't invent or guess values; the
production code is the source of truth.

#### Equality, not byte-identity, for `LastActiveAt`

`TestE2E_Restart_LastActiveAtSurvives` uses `time.Time.Equal` per entry,
not byte-equal on the file:

- **What `Equal` accepts.** Today's roundtrip is byte-exact for any UTC,
  monotonic-stripped `time.Time`. `Equal` also tolerates a future
  re-encode through `time.Now().UTC()` (which strips monotonic but
  preserves wall time) — the AC's "tight tolerance".
- **What `Equal` rejects.** A re-stamp to `time.Now()` produces a delta of
  seconds-to-hours against the 10-min and 1-hour pre-write offsets;
  `Equal` rejects loudly. The 10-min / 1-hour spread is far larger than
  any plausible JSON-roundtrip drift or test wall-clock.
- **Monotonic-clock trap.** The "want" values are obtained by re-reading
  the file with `readRegistry` *after* `writeRegistry`, not by reusing the
  in-memory pre-write struct. `time.Time` written via `MarshalIndent`
  retains monotonic-clock state in the original Go value but strips it
  after the JSON unmarshal trip the daemon takes. Comparing pre-write
  in-memory vs. post-restart parsed would diverge on monotonic alone even
  though the bytes on disk are identical. See `lessons.md § JSON
  roundtrip strips monotonic-clock state from time.Time`.

Cross-axis combinations (lifecycle × timestamp survival in one test) are
not the AC's ask and would confuse failure isolation. Each test pins one
invariant.

#### Why this works against today's pyry without behaviour changes

The restart-time code path against a pre-populated registry is:
`loadRegistry` reads → `pickBootstrap` selects the lone `bootstrap: true`
entry; non-bootstrap entries are *not* materialised into `Pool.sessions` →
`reg != nil` skips the cold-start save → `reconcileBootstrapOnNew`
no-ops because `~/.claude/projects/<encoded-cwd>` doesn't exist under the
test HOME → bootstrap enters `runActive`, idle timer disabled → SIGTERM
cancels ctx → `runActive` returns `ctx.Err` *before* `transitionTo
(stateEvicted)`, so no terminal save fires. Net: nothing in pyry calls
`saveLocked` between pre-write and the second `loadRegistry`. The non-
bootstrap entries persist on disk *because pyry doesn't touch them*, not
because pyry materialises them — that is the realistic-today shape of the
guarantee, and the test locks in the no-save-without-state-change
invariant. Future tickets that materialise non-bootstrap entries will need
to preserve their lifecycle state across restart explicitly; this test
will then catch any regression.

#### File split rationale

Lives in its own `restart_test.go` rather than extending
`cli_verbs_test.go`. The latter is *CLI surface coverage* (one test per
shipped verb); this test is *daemon-level disk-state survival* and doesn't
drive a CLI verb. Mirrors the `harness_test.go` (mechanics) vs.
`cli_verbs_test.go` (verb surface) split #52 established.

The local `registryFile` / `registryEntry` mirror types are duplicated
intentionally — `internal/sessions`'s on-disk types are unexported, and
exporting them solely for one test would invert the dependency direction.
The schema is small and stable; if a field is added, the mirror grows it
too.

## Failed-Start Pattern (`StartExpectingFailureIn`)

`StartExpectingFailureIn(t, home) RunResult` is the failure-side sibling of
`StartIn`. The caller pre-populates HOME with state designed to make pyry
refuse to come up (e.g. a corrupt `<home>/.pyry/test/sessions.json`); the
helper spawns pyry, watches the readiness window for an early exit, and
returns the captured exit code + streams. No `Harness` is returned — there
is no live daemon to drive and no socket to clean up.

```go
home, regPath := newRegistryHome(t)
_ = os.WriteFile(regPath, []byte("{not valid json"), 0o600)

res := e2e.StartExpectingFailureIn(t, home)
if res.ExitCode == 0 {
    t.Errorf("exit code = 0, want non-zero (stderr=%s)", res.Stderr)
}
if !bytes.Contains(res.Stderr, []byte("registry")) {
    t.Errorf("stderr does not mention registry: %s", res.Stderr)
}
```

### Internal shape: shared `spawn` helper

`StartIn` and `StartExpectingFailureIn` both forward to an unexported
`spawn(t, home) (socket, *exec.Cmd, *bytes.Buffer, *bytes.Buffer, doneCh)`
that does the fork + wait-goroutine + child-env wiring (the body that used
to live inline in `StartIn`). `spawn` deliberately does **not** register
`t.Cleanup`, build the `Harness`, or call `waitForReady` — each caller
owns those policies:

- `StartIn` builds the `Harness`, registers cleanup, then waits for ready.
- `StartExpectingFailureIn` runs a select-driven loop bounded by
  `readyDeadline` over `(net.Dial, doneCh, time.After(readyPollGap))`,
  returns `RunResult` populated from `cmd.ProcessState` on `<-doneCh`,
  and tears the daemon down + `t.Fatalf`s on either of the defensive
  branches (daemon unexpectedly came up; deadline elapsed with neither).

The defensive teardown reuses a small `killSpawned(t, cmd, doneCh)` helper
that mirrors `Harness.teardown`'s SIGTERM → `termGrace` → SIGKILL →
`killGrace` escalation. Inlined into a function rather than constructing a
throwaway `Harness` for the cleanup path: ~10 lines, no leak risk.

### Why an alternate constructor (not Options on `StartIn`)

The shape was chosen against three alternatives:

| Option                       | Why not                                                              |
|------------------------------|----------------------------------------------------------------------|
| `Options` field on `StartIn` | Forces a polymorphic return — `*Harness` doesn't fit the failure path |
| Lower-level `spawn` helper   | Bigger public surface than the one test needs                         |
| **Alternate constructor**    | Single-purpose; mirrors `Run` / `RunBare`; shared body via private `spawn` |

`StartExpectingFailure(t)` (zero-arg) deliberately not added — the failure
path always wants caller-supplied HOME (to seed the on-disk failure state),
so the `In` suffix is the only useful shape. Adding the no-`In` form would
be unused surface.

### Constants reuse

Reuses the existing `readyDeadline = 5 * time.Second` and `readyPollGap`.
The corrupt-registry path exits in milliseconds (synchronous JSON parse),
so 5 seconds is generous; no new constant.

### `startup_test.go` — `TestE2E_Startup_CorruptRegistryFailsClean` (#111)

Lives in its own file rather than extending `restart_test.go` — domain is
*startup failure*, not *restart survival*. Future startup-shaped e2e tests
(missing claude binary, unreachable workdir, port-in-use socket) have a
natural home next to it.

The test reuses `newRegistryHome(t)` from `restart_test.go` (same package,
same `e2e` build tag), seeds `<home>/.pyry/test/sessions.json` with
`{not valid json`, calls `StartExpectingFailureIn`, then asserts:

| Assertion | What it pins |
|---|---|
| `res.ExitCode != 0` | Daemon refused to come up. Any non-zero is sufficient — exit code is not over-specified. |
| `bytes.Contains(res.Stderr, []byte("registry"))` | Operator-facing diagnostic still names the failing subsystem. |
| `bytes.Equal(diskBytes, corrupt)` | Daemon left the corrupt file untouched on disk. |

The byte-equal assertion is the load-bearing one — it catches the
worst-possible regression ("corrupt file → empty registry → drop
everything") without depending on JSON-parsing the corrupt input. The
substring `registry` is chosen over the path or `sessions.json` because
the path varies per run and `sessions.json` is just the filename, while
"registry" is the domain concept the operator needs to recognise. The
production error chain happens to contain `registry` twice (`pool init:
sessions: load registry: registry: parse <path>: <unmarshal err>`); a
future refactor that changes the wrap chain but still names "registry"
keeps the test green; one that loses the word fails loudly — the right
outcome (operator diagnostic regressed).

### Coverage of the helper's defensive branches

The test exercises only the success path of `StartExpectingFailureIn` (the
child exits before ready). The two `t.Fatalf` branches — "daemon
unexpectedly came up" and "neither exit nor readiness within
`readyDeadline`" — are defensive and would only trigger on a production
regression (corrupt JSON stops failing) or a hung test environment. No
unit tests added for them; per the ticket's "exercised exclusively by this
test" constraint, they earn their keep as crash-loud guards, not as
behaviours under coverage. Future failed-start tests that reuse the helper
provide additional implicit coverage as they land.

### `startup_test.go` — `TestE2E_Startup_MissingClaudeProjectsDir` (#112)

Positive-outcome sibling of the corrupt-registry test — same file, opposite
verdict. A first-run user has never invoked `claude`, so
`~/.claude/projects/` does not exist. The reconcile path's `MissingDir`
branch (`internal/sessions/pool.go`) treats `os.Stat` returning
`fs.ErrNotExist` as "no transcripts to reconcile," not as an error; the
daemon must come up with an empty registry. Unit tests already cover this;
the e2e adds binary-boundary proof.

Sketch:

```go
func TestE2E_Startup_MissingClaudeProjectsDir(t *testing.T) {
    home, err := os.MkdirTemp("", "pyry-mp-*")
    if err != nil { t.Fatalf("mkdir home: %v", err) }
    t.Cleanup(func() { _ = os.RemoveAll(home) })

    claudeProjects := filepath.Join(home, ".claude", "projects")
    if _, err := os.Stat(claudeProjects); !errors.Is(err, fs.ErrNotExist) {
        t.Fatalf(".claude/projects/ unexpectedly exists at %s (err=%v); test premise invalidated",
            claudeProjects, err)
    }

    h := StartIn(t, home)
    r := h.Run(t, "status")
    if r.ExitCode != 0 {
        t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
            r.ExitCode, r.Stdout, r.Stderr)
    }
    h.Stop(t)
}
```

| Assertion | What it pins |
|---|---|
| `fs.ErrNotExist` on `<home>/.claude/projects/` | Test premise: the missing-dir case is what's actually under test. If a future harness change pre-creates that directory, this test fails loudly instead of silently passing on a different path. |
| `Start`/`StartIn` returns | Daemon reaches ready with the missing dir — the `MissingDir` branch did not return an error up the stack. |
| `pyry status` exit 0 | Control socket is responsive; the daemon is functional, not just up. |
| `h.Stop(t)` | Shutdown is verdict-bearing: explicit `Stop` surfaces shutdown errors at the assertion point rather than from `t.Cleanup` after the test has already passed. |

No log-line assertion: production may or may not log the no-op, and tying
the test to a specific line would lock production into emitting it.

#### Why `StartIn` + `os.MkdirTemp` instead of `Start(t)` + `t.TempDir()`

`Start(t)` would suffice for the missing-dir case in principle (a fresh
`t.TempDir()` HOME has no `.claude/projects/` by construction). Two reasons
to use `StartIn` + `os.MkdirTemp` here:

1. **`sun_path` budget.** `TestE2E_Startup_MissingClaudeProjectsDir` is a
   long test name; `t.TempDir()` embeds it into the path and overflows
   macOS's 104-byte socket-path limit. `os.MkdirTemp("", "pyry-mp-*")` keeps
   the prefix tiny — same lesson the restart tests apply.
2. **Caller-owned cleanup.** The failed-start test next door uses
   `os.MkdirTemp` + `t.Cleanup(os.RemoveAll)` for the same reason. Keeping
   the two startup tests structurally similar makes the file scannable.

#### Why explicit `Stop(t)` despite `t.Cleanup`

`StartIn(t, home)` registers `h.teardown` via `t.Cleanup`, which handles
process liveness and socket removal. But cleanup runs *after* the test
function returns, so any `t.Logf` about a stuck shutdown gets attributed
"after the test." Calling `h.Stop(t)` inside the test body makes "shuts
down cleanly" a verdict-bearing step. `cleanupOnce` (existing `sync.Once`)
makes this idempotent with the cleanup hook — the second fire is a no-op.

#### Why not a table-driven test combining both startup cases

The two startup tests assert opposite outcomes: ready+responsive vs.
exit-before-ready. They use different harness entry points (`StartIn` vs.
`StartExpectingFailureIn`) returning different types (`*Harness` vs.
`RunResult`). A table that switches on outcome shape is more code than two
flat tests. Per #111's spec and #112's guidance, keep them flat.

#### Production diff is zero

The `MissingDir` branch already exists in `internal/sessions/pool.go`. This
ticket adds binary-boundary coverage; no harness changes either. Test diff
is one new test in the existing `startup_test.go`, ~25 LOC.

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

The `sync.Once` makes this safe to call from a manual `Stop()` (shipped in
#106) plus `t.Cleanup` without double-firing.

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
  `docs/specs/architecture/80-e2e-install-systemd-roundtrip.md`,
  `docs/specs/architecture/106-e2e-restart-primitive.md`,
  `docs/specs/architecture/107-e2e-restart-evicted-and-lastactiveat.md`,
  `docs/specs/architecture/111-e2e-corrupt-registry.md`,
  `docs/specs/architecture/112-e2e-missing-claude-projects-dir.md`
- Pattern: lessons.md § Test helpers across packages (`/bin/sleep` as the
  benign fake claude); lessons.md § Unix-socket sun_path limits and
  t.TempDir()
- Consumers: shipped CLI verbs (#52: `stop`, `logs`, `version`,
  `status` stopped path; #69: `status` running path), restart-survival
  proofs (#106: `TestE2E_Restart_PreservesActiveSessions`; #107:
  `TestE2E_Restart_PreservesEvictedSessions`,
  `TestE2E_Restart_LastActiveAtSurvives`), startup-failure proofs
  (#111: `TestE2E_Startup_CorruptRegistryFailsClean`),
  startup positive-outcome proofs (#112:
  `TestE2E_Startup_MissingClaudeProjectsDir`), Phase 1.1
  session-verb tickets (#54, #55, #56), install-service round-trip
  ([install-e2e.md](install-e2e.md))
