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
`TestE2E_Startup_MissingClaudeProjectsDir`, no harness changes), #115
(idle-eviction + lazy-respawn e2e — variadic flags on `StartIn` / `spawn`
+ two new tests asserting eviction and respawn at the binary boundary),
#125 (attach PTY harness — `AttachHarness` + `StartAttach(t, sessionID)`
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
daemon-survives + supervised-child-still-`Phase: running`), split
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

Ten exported names — `Harness`, `Start`, `StartIn`, `StartRotation`,
`StartExpectingFailureIn`, `(*Harness).Stop`, `RunResult`, `(*Harness).Run`,
`RunBare`, plus the struct fields:

```go
type Harness struct {
    SocketPath        string         // dial-able after Start returns
    HomeDir           string         // child's $HOME (registry, claude dir live underneath)
    ClaudeSessionsDir string         // populated by StartRotation; empty otherwise
    PID               int            // captured at spawn for leak verification
    Stdout            *bytes.Buffer  // safe to read after process exit
    Stderr            *bytes.Buffer
}

func Start(t *testing.T) *Harness  // fail-fast: t.Fatalf on any error

// StartIn behaves like Start but uses the caller-supplied home directory
// instead of allocating a fresh t.TempDir(). Pre-populate it (e.g.
// <home>/.pyry/test/sessions.json) before calling to drive a daemon
// against a chosen on-disk state. Caller owns the directory's lifecycle.
//
// Optional extraFlags are appended to the standard test flag set before
// the `--` claude-arg sentinel. Go's flag package is last-wins, so
// `StartIn(t, home, "-pyry-idle-timeout=1s")` overrides the harness
// default of `=0` to enable idle eviction in-test.
func StartIn(t *testing.T, home string, extraFlags ...string) *Harness

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

// StartRotation spawns pyry with the fake-claude test binary
// (internal/e2e/internal/fakeclaude) as the supervised child, propagating
// the three PYRY_FAKE_CLAUDE_* env vars via cmd.Env so the supervisor
// inherits them through os.Environ() and forwards them to the PTY child.
// sessionsDir is auto-created with 0o700 if missing and recorded on
// h.ClaudeSessionsDir. initialUUID is the stem for fake-claude's first
// jsonl; trigger is the filesystem path the test creates to signal
// rotation. Idle eviction is left at the spawn default (-pyry-idle-
// timeout=0). Used by rotation-watcher e2e tests; this primitive ships
// independent of any consumer (#123).
func StartRotation(t *testing.T, home, sessionsDir, initialUUID, trigger string) *Harness

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
<extraFlags...>          # variadic, last-wins via Go's flag package
-- 99999
```

`/bin/sleep 99999` exists on Linux + macOS, survives ~27 hours (longer than
any test runs), and the readiness gate doesn't depend on the child being a
real claude. `99999` (a plain integer in seconds) is the only argv form
portable across both: `infinity` is GNU coreutils only and macOS BSD sleep
rejects it (see `lessons.md § Test helpers across packages`). #115 changed
the harness from `infinity` to `99999` because the lazy-respawn test waits
for `Phase: running` after a respawn — under `infinity`, macOS BSD sleep
exits immediately, the supervisor enters perpetual backoff, and `Phase:
running` is never observed. `IdleTimeout=0` defeats the eviction timer by
default; tests that need eviction pass `-pyry-idle-timeout=<dur>` via the
variadic on `StartIn`.

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
`spawn(t, home, extraFlags...)` that does the fork + wait-goroutine +
child-env wiring (the body that used to live inline in `StartIn`). #123
generalised `spawn` further: it is now a thin wrapper over a new
`spawnWith(t, home, spawnOpts)` core (see § Rotation Primitive); zero-
value `spawnOpts` reproduces the historical `/bin/sleep 99999` shape, and
`StartRotation` populates the options to swap in fake-claude. `spawn`
deliberately does **not** register `t.Cleanup`, build the `Harness`, or
call `waitForReady` — each caller owns those policies:

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

## Idle-Eviction + Lazy-Respawn Pattern (`idle_test.go`, #115)

Two tests in `internal/e2e/idle_test.go` (build tag `e2e`) exercise the
idle-eviction state machine and lazy respawn at the binary boundary —
the assembled `pyry` daemon, the real `internal/sessions` lifecycle
goroutine, the real control server, the real on-disk `sessions.json`.
Package-level integration tests in `internal/sessions/` already cover
the in-process pool primitives; #115 closes the binary-boundary gap.

| Test | Asserts |
|---|---|
| `TestE2E_IdleEviction_EvictsBootstrap` | with `-pyry-idle-timeout=1s`, the bootstrap entry's `lifecycle_state` becomes `"evicted"` on disk within 5s; `pyry status` does not report `Phase: running` |
| `TestE2E_IdleEviction_LazyRespawn` | after eviction, a raw `VerbAttach` over the control socket triggers respawn; `lifecycle_state` returns to active and `pyry status` reports `Phase: running` while the conn is held |

### Variadic-flags harness extension

Both tests need `-pyry-idle-timeout=1s` (the harness default is `=0`).
`spawn` and `StartIn` grew a variadic `extraFlags ...string` parameter;
the standard set is built into a slice, `extraFlags` are appended, then
`--` and the claude args. Go's `flag` package processes left-to-right
with last-wins semantics, so a duplicate flag in `extraFlags` overrides
the standard default. `StartExpectingFailureIn` was deliberately *not*
extended — no failed-start test in #115 needs flags; one-line change
when a future scenario does.

Existing call sites (`Start(t)`, `StartIn(t, home)`, restart-test calls)
are unchanged — variadic is backwards-compatible at every site. Net
public API delta: zero new exported names; two existing signatures grew
one variadic parameter.

### Why raw `VerbAttach` over `pyry attach` as a subprocess

Determinism. `pyry attach` enters a stdin-byte loop (`copyWithEscape`)
that requires careful pipe management to keep the conn alive while
assertions run; closing stdin too early ends the attach before the next
idle eviction fires. A raw `net.Dial` + `json.Encode` + `defer
conn.Close()` lets the test own the timeline exactly. The control
protocol's `Request` / `Response` / `AttachPayload` types are public on
`internal/control`; importing them from `internal/e2e` is in-module and
intended.

### Why poll for `Phase: running` and not `attached > 0`

`attached` is package-private state inside `Session`. The wire surface
(`pyry status`) reports the supervisor's phase, which is what "alive
again" means at the AC level. Phase transitions to `running` once the
supervisor's child-process spawn loop has the PID — exactly the
"lazy respawn happened" signal the AC asks for.

### `waitForBootstrapState` helper — file-local

Polls the registry file for the bootstrap entry's `lifecycle_state` to
match `"evicted"` or `"active"`. The `"active"` arm tolerates either an
empty/missing field (today's `omitempty` default for `stateActive`) or
the literal string `"active"` — decouples the test from a future toggle
that starts writing the field explicitly. File-local; promoted to
`harness.go` only if a third caller justifies it (rule of three).

### Why poll registry first, then `pyry status`

The registry is the load-bearing AC ("registry state observable").
`pyry status` is the cross-check — its phase string comes from
`internal/supervisor`'s state and is byte-stable in `runStatus`'s
`"Phase:         %s\n"` format. The eviction test asserts *negation*
(`!Contains "Phase:         running"`), not the exact non-running phase,
so a future change from `PhaseStopped` to a sibling phase doesn't break
the test.

### Why hold the conn open across Phase C

`handleAttach` writes the OK ack and binds the bridge to the conn. The
conn now streams PTY bytes from `/bin/sleep 99999` (which writes
nothing) and back. The test never reads from the conn after the ack —
just holds it open via `defer conn.Close()` so `attached > 0` defers
the next idle eviction while the assertions run. Bridge teardown fires
server-side on `conn.Close()` when the test returns.

### Sleep-arg portability — `99999`, not `infinity`

Pre-#115 the harness passed `infinity` as the `/bin/sleep` argument.
GNU coreutils accepts it; macOS BSD sleep does not (and the
unit-suffixed forms its man page advertises don't all work — `99999d`
exits with the usage banner on macOS Tahoe 26.3). For tests that don't
care about the supervised child surviving (`TestStatus_E2E`, etc.) the
short-circuit child exit was invisible — the readiness gate trips on
the control socket, not the child. Lazy respawn surfaces it: the test
needs the child to live long enough for `Phase: running` to be
observed after respawn, otherwise the supervisor enters perpetual
backoff and the assertion never fires. `99999` (a plain integer in
seconds, ~27h) works on both. See `lessons.md § Test helpers across
packages`.

### Production diff is zero

`-pyry-idle-timeout` already existed (`cmd/pyry/main.go:257`). The
state machine, persistence, and control-plane Activate-before-Attach
all shipped in #40. #115 adds binary-boundary coverage. Test diff
~125 LOC (single new file) plus the variadic-flags signature change in
`harness.go`.

## Attach PTY Harness Pattern (`attach_pty.go`, `attach_pty_test.go`, #125)

The non-interactive `Harness` drives daemon-only verbs over the control socket
with stdio pipes. `pyry attach` is the only interactive surface in the product
and needs a controlling terminal — pipes don't satisfy `term.IsTerminal`. #125
adds a sibling **`AttachHarness`** in the same package (build tag `e2e ||
e2e_install`) that owns:

1. A `pyry` daemon in bridge mode whose supervised "claude" is the e2e test
   binary running `TestHelperProcess` in echo mode.
2. A `creack/pty` master/slave pair.
3. A `pyry attach` subprocess whose stdin/stdout/stderr are the slave fd.

```go
func TestE2E_Attach_RoundTripsBytes(t *testing.T) {
    a := StartAttach(t, "")
    payload := []byte("pyry-attach-roundtrip-" + tinyNonce() + "\n")
    if _, err := a.Master.Write(payload); err != nil {
        t.Fatalf("write master: %v", err)
    }
    if err := readUntilContains(a.Master, payload, 5*time.Second); err != nil {
        t.Fatalf("did not observe payload back: %v", err)
    }
}
```

### Public API

```go
type AttachHarness struct {
    Master     *os.File   // PTY master — write input, read output
    SocketPath string     // daemon's control socket
    HomeDir    string     // daemon's $HOME (fresh t.TempDir)
    // ... unexported fields
}

// StartAttach probes pty.Open (t.Skip on failure — AC#5), spawns a
// bridge-mode daemon with the e2e test binary as claude, then spawns
// pyry attach with the slave on stdio. sessionID="" → bootstrap.
func StartAttach(t *testing.T, sessionID string) *AttachHarness

// WaitDetach blocks until the attach client process exits or timeout
// elapses, then returns its exit code. Fails the test on timeout.
// Safe to call after writing the detach sequence to Master; subsequent
// calls return the same exit code (#127).
func (a *AttachHarness) WaitDetach(t *testing.T, timeout time.Duration) int

// Run invokes the cached pyry binary against this harness's daemon
// socket with HOME=a.HomeDir. Mirrors Harness.Run — same auto-injection
// of -pyry-socket=, same RunResult shape, same timeout. Used by tests
// that need to drive a CLI verb against the same daemon the attach
// client is bound to (#127).
func (a *AttachHarness) Run(t *testing.T, verb string, args ...string) RunResult
```

Cleanup is registered via `t.Cleanup`: master+slave close, SIGTERM-grace-
SIGKILL on the attach client and daemon (reusing `killSpawned` from
`harness.go`), socket remove, defensive `term.Restore` on the parent's stdin
state (snapshotted at `StartAttach` for AC#4). Idempotent via `sync.Once`.

### Three independent OS resources, ordered teardown

```
master.Close()      // flush master writes
slave.Close()       // attach client still has its dup'd copies
killSpawned(attach) // SIGTERM → grace → SIGKILL
killSpawned(daemon) // SIGTERM → grace → SIGKILL — pyry kills helper
os.Remove(sock)     // defensive; pyry removes it on clean shutdown
term.Restore(...)   // parent's stdin state, if snapshotted
```

The slave fd held by the harness and the slave fds dup'd into `attachCmd`'s
stdin/stdout/stderr are independent — closing the harness's slave does not
SIGHUP the attach client. The kill sequence does that explicitly.

### Helper "claude" via `TestHelperProcess` re-exec

```
spawnAttachableDaemon args:
  -pyry-claude=os.Args[0]                    # the e2e test binary
  --                                         # arg sentinel
  -test.run=TestHelperProcess                # claude args (passed to helper)

daemon env:
  GO_TEST_HELPER_PROCESS=1
  GO_TEST_HELPER_MODE=echo
```

`supervisor.runOnce` does `cmd.Env = append(os.Environ(), helperEnv...)`, so
env vars set on the daemon's `cmd.Env` flow through to the supervised
helper. The helper gates on `GO_TEST_HELPER_PROCESS=1` (no-op in normal `go
test` runs), switches on `GO_TEST_HELPER_MODE`, calls `term.MakeRaw` on
stdin, then `io.Copy(stdout, stdin)`.

This pattern (#125) coexists with #122's separate `package main`
fakeclaude binary (`internal/e2e/internal/fakeclaude`) — they target
different shapes:

| Pattern                   | Shape                                | Why                                 |
|---------------------------|--------------------------------------|-------------------------------------|
| Test-binary re-exec (#125) | `if env != "1" { return }` + io.Copy | Echo is one-line; no extra binary  |
| Separate `package main` (#122) | Opens fds, polls trigger, rotates  | Rotation needs a stable build target |

Each test binary's `os.Args[0]` is its own — the helper test cannot be
reused across packages.

### Why ECHO must be disabled in the helper

Bridge mode does **not** put the supervisor's PTY into raw mode — the
kernel's line discipline still runs with default ECHO on. Without
`term.MakeRaw` in the helper, the kernel reflects every input byte back to
the master *before* the helper's `io.Copy` runs, so the test sees each byte
twice. The attach client's `term.MakeRaw(slave)` (in
`attach_client.go:68-74`) silences echo on the *slave* side; the helper's
`term.MakeRaw(stdin)` silences echo on the *supervisor's* PTY slave (which
is the helper's stdin).

### `Setsid + Setctty` on the attach client

```go
attachCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
```

Without these, the attach client inherits the test process's controlling
terminal; `IsTerminal(0)` returns true on the slave fd but writes go to the
*test's* terminal, not the slave. `Setsid` puts the attach client in a fresh
session; `Setctty` makes the slave its controlling terminal. Now
`term.MakeRaw(slave)` runs against the right tty and the round-trip is
deterministic.

### Skip-on-no-PTY at `pty.Open`

`pty.Open` is the cleanest gate: it exercises `/dev/ptmx` directly. Sandboxed
CI and minimal containers fail here; GitHub Actions `ubuntu-latest` does not
(per `lessons.md § PTY Testing` — CI lacks a *controlling* terminal, but
`pty.Open` works). Probe before spawning the daemon — a clean `t.Skip` is
faster than a daemon spawn + readiness race + teardown.

### Why a generous read deadline, not exact-byte equality

`readUntilContains(r, needle, total)` reads in a loop until the needle
appears or the overall deadline elapses. The attach client's banner ("pyry:
attached. Press Ctrl-B d to detach.") is printed before raw-mode and arrives
at the master before the payload echo; the loop swallows pre-payload bytes
naturally. Asserting on exact bytes would require explicit banner skipping
or a `2>/dev/null` redirect (extra fd plumbing, since stderr is the slave
PTY here).

### `SetReadDeadline` does not work on PTY masters on darwin

The runtime poller reports `ErrNoDeadline` for PTY master fds on macOS,
so the timeout is enforced by the *caller* via `select { case <-ch:
case <-time.After(...) }`, not by `r.SetReadDeadline`. On timeout the
reader goroutine is left running; the harness's teardown closes Master,
which unblocks the `Read` with EOF. See `lessons.md § PTY master fds on
darwin do not support SetReadDeadline`.

### What this slice does not verify

- SIGWINCH propagation (#126).
- Restart survival across daemon SIGTERM/respawn (#128).
- Per-session attach exclusivity (`ErrBridgeBusy`).

Clean detach via `Ctrl-B d` is covered by #127 (see § Attach Detach
Pattern below). The harness's `SocketPath` field is exposed so a
follow-up can drive a second `pyry attach` against an already-bound
bridge to assert `ErrBridgeBusy`.

### Production diff is zero

`pyry attach`, the bridge, the control plane, and supervisor were all
already shipping. Test diff: ~351 LOC across two new files
(`attach_pty.go`, `attach_pty_test.go`).

## Rotation Primitive (`StartRotation`, `fakeclaude_test.go`, #123)

`StartRotation(t, home, sessionsDir, initialUUID, trigger) *Harness` is the
constructor that swaps `/bin/sleep 99999` for the [fake-claude
binary](fakeclaude-binary.md) (#122) so e2e tests can exercise pyry's
rotation watcher against a child that produces realistic JSONL behaviour.

Used today only by `TestE2E_StartRotation_PrimitiveWiresFakeClaude` (a
binary-boundary smoke test that does *not* touch
`internal/sessions/rotation`). The next consumer ticket drives pyry's
watcher against this primitive.

### What it wires

```
StartRotation(t, home, sessionsDir, initialUUID, trigger)
  │
  ├─ os.MkdirAll(sessionsDir, 0o700)         # auto-create
  ├─ ensureFakeClaudeBuilt(t)                # build (or reuse) fakeclaude
  └─ spawnWith(t, home, spawnOpts{
        claudeBin:  fakeBin,
        claudeArgs: []string{},
        extraFlags: []string{"-pyry-workdir=" + home},
        extraEnv: []string{
          "PYRY_FAKE_CLAUDE_SESSIONS_DIR=" + sessionsDir,
          "PYRY_FAKE_CLAUDE_INITIAL_UUID=" + initialUUID,
          "PYRY_FAKE_CLAUDE_TRIGGER="      + trigger,
        },
     })
```

`Harness.ClaudeSessionsDir` is populated to `sessionsDir`; left empty for
`Start` / `StartIn`.

### Why env vars on pyry, not flags

`supervisor.runOnce` does `cmd.Env = append(os.Environ(), s.cfg.helperEnv...)`
(`internal/supervisor/supervisor.go:234`). Setting the three
`PYRY_FAKE_CLAUDE_*` vars on pyry's `cmd.Env` flows them through to
fake-claude unchanged — no `helperEnv` knob, no supervisor changes. The
fake-claude binary's input surface is env-only by design (see
[fakeclaude-binary.md § Configuration](fakeclaude-binary.md)).

### `ensureFakeClaudeBuilt` — sibling of `ensurePyryBuilt`

```go
func ensureFakeClaudeBuilt(t *testing.T) string  // sync.Once + cached bin path
```

Mirrors `ensurePyryBuilt`: `sync.Once`-guarded `go build` into
`os.MkdirTemp("", "pyry-e2e-fakeclaude-*")`. `PYRY_E2E_FAKE_CLAUDE_BIN`
short-circuits to a pre-built binary on disk for CI prebuild.

### `spawnOpts` — shared spawn core

`spawn(t, home, extraFlags...)` and `StartRotation` both forward to a new
`spawnWith(t, home, spawnOpts) (socket, *exec.Cmd, *bytes.Buffer,
*bytes.Buffer, doneCh)` core. `spawnOpts` zero-value yields the historical
`/bin/sleep 99999` behaviour, so `spawn` is now a one-liner over
`spawnWith`. Existing call sites (`StartIn`, `StartExpectingFailureIn`)
unchanged.

```go
type spawnOpts struct {
    claudeBin   string   // default "/bin/sleep"
    claudeArgs  []string // nil → {"99999"}
    extraEnv    []string // appended to childEnv(home)
    extraFlags  []string // appended after standard set, before `--`
}
```

`spawnOpts` is unexported — generalises cleanly when a third caller
appears, without committing to a public-surface shape today.

### `-pyry-workdir=<home>` is needed for the fake-claude path

Without `-pyry-workdir`, pyry's supervisor inherits the test process's
cwd; the supervised child's relative paths (and any production code that
encodes cwd into a path) drift away from the test's HOME. Pinning it to
`home` makes the test's view match pyry's view of "where the supervised
child is rooted." The flag exists today (`cmd/pyry/main.go:174-180`); no
production change.

### Test scope: primitive only

`TestE2E_StartRotation_PrimitiveWiresFakeClaude` (in
`internal/e2e/fakeclaude_test.go`, build tag `e2e`) verifies the wiring,
not pyry's rotation watcher:

1. `StartRotation` returns successfully (pyry came up; fake-claude opened
   its initial fd; readiness gate tripped).
2. `h.ClaudeSessionsDir == sessionsDir`.
3. Poll (5s deadline, 50ms gap) until `<sessionsDir>/<initialUUID>.jsonl`
   appears.
4. `os.WriteFile(trigger, nil, 0o600)`.
5. Poll until a *different* `<uuid>.jsonl` (matching `uuidV4Re`) appears
   in `sessionsDir`.
6. Assert `os.Stat(rotated).Size() > 0` — combined with #122's strict
   close-OLD-before-open-NEW order, this implies the initial fd is no
   longer being written.

Deliberately does **not** assert on pyry's session registry, run
`/proc/<pid>/fd` probes, or drive `internal/sessions/rotation` — that's
the next consumer ticket. When the consumer fails, it fails for one
reason at a time.

### Why short-prefix `os.MkdirTemp` for HOME

Same `sun_path` budget rationale as the restart tests:
`TestE2E_StartRotation_PrimitiveWiresFakeClaude` is a long test name and
`t.TempDir()` would push `<home>/pyry.sock` past macOS's 104-byte limit.
`os.MkdirTemp("", "pyry-fc-*")` + `t.Cleanup(os.RemoveAll)` keeps the
prefix tiny.

### Production diff is zero

`-pyry-workdir`, the supervisor env-propagation, and fake-claude's env
contract all already shipped (#122 for the binary; pyry's flag/supervisor
are pre-Phase-1.0). #123 is harness + test diff: ~130 LOC across
`harness.go` and `fakeclaude_test.go`.

## Attach Detach Pattern (`attach_detach_test.go`, #127)

`TestE2E_Attach_DetachesCleanly` drives the documented `Ctrl-B d`
sequence (bytes `0x02 0x64`) into a live attach session and asserts
the triple invariant of clean detach: attach client exits 0, daemon
survives, supervised child still in `Phase: running`. Builds on the
PTY harness from #125 with two new methods on the existing
`*AttachHarness` and one shared-body refactor in `harness.go`.

### Methods added to `*AttachHarness`

- `WaitDetach(t, timeout) int` — blocks on `attachDone` (the channel
  the wait goroutine closes after `attachCmd.Wait()` returns), then
  reads `attachCmd.ProcessState.ExitCode()`. Channel close is the
  happens-before edge that makes `ProcessState` safe to read on the
  test goroutine. `t.Fatalf` on timeout (clean detach is near-instant
  in practice; the 5s budget is a safety net).
- `Run(t, verb, args...) RunResult` — mirror of `Harness.Run` against
  the attach harness's `SocketPath` + `HomeDir`. Used to drive
  `pyry status` against the same daemon the attach client is bound to.

### `runVerb` — shared body extracted from `Harness.Run`

Both methods needed the same body: `exec.CommandContext(ctx, binPath,
verb, "-pyry-socket="+socket, args...)`, `cmd.Env = childEnv(home)`,
stdout/stderr capture, `runTimeout` deadline, exit-code mapping. The
body moved into a package-private `runVerb(t, socket, home, verb,
args...) RunResult` free function; `Harness.Run` and
`AttachHarness.Run` are 2-line wrappers (`return runVerb(t,
h.SocketPath, h.HomeDir, verb, args...)`). Net effect: ~25 lines move
out of `Harness.Run`; behaviour for existing callers is unchanged.

The refactor is bounded — `Harness.Run` had no callers outside the
harness package, so the rename is private and `gofmt`-clean. A 25-line
duplication would have been acceptable for an XS ticket; extraction
won because the two methods diverge only in two field reads.

### Master-drain goroutine — load-bearing for the test

`pyry attach` writes `pyry: detached.` to its own stderr after
`copyWithEscape` returns on `Ctrl-B d`. With `cmd.Stderr = slave`
that write goes through the kernel PTY into the master buffer; if no
goroutine is reading the master, the buffer fills and the slave write
blocks — the attach client never returns from `runAttach` and
`cmd.Wait()` never fires. Symptom: `WaitDetach` hits its 5s deadline
even though `Ctrl-B d` was correctly recognised.

The fix lives in the test, not the harness: spawn a background
master-drain goroutine before writing the detach sequence and let it
ride until teardown closes `Master` and `Read` errors out. The #125
round-trip test got away without one because `readUntilContains`
consumed the master continuously through the assertion phase. See
`lessons.md § PTY master backpressure stalls slave-side process
exit`.

### Why a generous timeout, not a tight one

`WaitDetach`'s timeout is a safety net, not a steady-state
expectation. Steady-state detach is single-digit milliseconds (no I/O
between `Ctrl-B d` recognition and process exit). The 5s budget gives
1–2 orders of magnitude of headroom and lets a flaky CI scheduler
skate. A tight deadline would convert scheduler jitter into
intermittent failures without catching real regressions any earlier.

### Acceptance-criteria mapping

| AC | Asserted by |
|---|---|
| Daemon spawn + attach via PTY harness; detach bytes written to PTY | `StartAttach(t, "")` + `a.Master.Write([]byte{0x02, 0x64})` |
| Attach client exits 0 within ≥5s | `a.WaitDetach(t, 5*time.Second)` + exit-code check |
| Daemon alive after detach | `a.Run(t, "status")` exit 0 check |
| Supervised child in `Phase: running` | `bytes.Contains(r.Stdout, []byte("Phase:         running"))` (multi-space gap is significant — column-aligned status output, mirrors `idle_test.go`) |
| Skip cleanly on hosts without usable PTY | inherited from `StartAttach`'s `pty.Open` skip |

### What this test does not verify

- Detach against a non-bootstrap session (`StartAttach` accepts
  `sessionID` but the test passes `""`).
- Behaviour when the user holds `Ctrl-B` but never types `d` — that's
  a `control.Attach` unit-test concern, not e2e.
- Bridge-busy semantics on a second concurrent attach.

### Production diff is zero

`pyry attach`, `control.Attach`'s prefix-key state machine, and the
detach handshake all shipped pre-#127. Test diff: ~55 LOC for the new
test, ~32 LOC for the two `*AttachHarness` methods, ~12 LOC for the
`runVerb` extraction.

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
  `docs/specs/architecture/112-e2e-missing-claude-projects-dir.md`,
  `docs/specs/architecture/115-e2e-idle-eviction-lazy-respawn.md`,
  `docs/specs/architecture/125-e2e-attach-pty-harness.md`,
  `docs/specs/architecture/123-e2e-startrotation-primitive.md`,
  `docs/specs/architecture/127-e2e-attach-detach-clean.md`
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
  `AttachHarness.Run`), Phase 1.1 session-verb tickets (#54, #55, #56),
  install-service round-trip ([install-e2e.md](install-e2e.md))
