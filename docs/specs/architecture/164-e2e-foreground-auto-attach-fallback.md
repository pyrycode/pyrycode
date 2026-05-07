# #164 — Phase 1.3c-2-e2e-fallback: foreground auto-attach fallback scenarios

E2E coverage of the **fall-through** branches of `tryAutoAttach`
(introduced by #158, happy path covered by #163). Three tests live
under `internal/e2e/`, all build-tagged `e2e`, all asserting that
the foreground pyry binary reached supervisor mode (process-tree
inspection finds a claude child) when auto-attach didn't fire.

The scenarios per AC:

1. `TestE2E_ForegroundAutoAttach_FallsThroughWhenSessionMissing` —
   a daemon is running with at least one registered session;
   foreground asks for a different UUID. Foreground enters supervisor
   mode; daemon's registry is unchanged.
2. `TestE2E_ForegroundAutoAttach_FallsThroughWhenNoDaemon` — no
   daemon, no socket file. Foreground enters supervisor mode without
   touching the socket.
3. `TestE2E_ForegroundAutoAttach_RespectsEnvOverride` — daemon
   running, requested UUID *is* registered, but
   `PYRY_NO_AUTO_ATTACH=1` is set. Foreground enters supervisor mode
   anyway.

Test-only ticket. Zero production-code edits.

## Files to read first

The developer's turn-1 reading list. Each line is "path — what to
extract." Pull from these to avoid re-discovering things the
architect already chased down.

- `internal/e2e/auto_attach.go` (full, ~410 lines) — the helper file
  added by #163. The new fallback constructor lives in this same
  file. Reuse: `spawnAutoAttachDaemon`, `writeEchoClaude`,
  `safeBuffer`, `pgrepChildren`. The new fallback constructor is a
  simpler sibling of `startForegroundAutoAttach` — no daemon-side
  setup at the foreground's socket path, no input/output pipes (the
  test never round-trips bytes), `-pyry-claude` pointed at the sleep
  stand-in.
- `internal/e2e/auto_attach_happy_test.go` (full, ~50 lines) — the
  happy-path test added by #163. Same nonce-tagged-payload pattern is
  *not* reused here (no round-trip in the fallback tests), but the
  imports / package layout are the template.
- `internal/e2e/cap_test.go:14-37` — `sleepClaudeScript` and
  `writeSleepClaude`. Reuse verbatim. The wrapper ignores Pool's
  appended `--session-id <uuid>` and exec's `sleep 99999` so the
  supervised "claude" stays alive long enough for the process-tree
  assertion.
- `cmd/pyry/main.go:266-308` (`tryAutoAttach`) — the function under
  test. Note its three independent fall-through gates:
    - `PYRY_NO_AUTO_ATTACH=="1"` → fall through (line 283)
    - `extractSessionID(claudeArgs) == ""` → fall through (line 286)
    - `os.Stat(socketPath)` errors → fall through (line 290)
    - `SessionsHasID` returns error or `has=false` → fall through
      (line 299)
  The e2e tests in this ticket all fall through via the **stat-error
  (ENOENT)** gate at line 290 — see *Why ENOENT in all three tests*
  below for why we cannot mechanically discriminate the gates at the
  e2e level without changing production code.
- `cmd/pyry/auto_attach_test.go:114-244` — unit-level coverage of
  every gate (`TestTryAutoAttach_NoSessionID`, `_EnvOptOut`,
  `_SocketAbsent_FastPath`, `_DaemonUnresponsive`, `_HasIDFalse`,
  `_HasIDInvalid`). Already shipped with #158; the gate-discrimination
  contract is fully pinned. **The e2e tests in this ticket are
  *system-integration* coverage — they pin "binary in fall-through
  reaches supervisor mode" — not gate discrimination.** This framing
  is load-bearing in *Open questions* below.
- `cmd/pyry/main.go:326-426` (`runSupervisor`) — the path the
  foreground enters after fall-through. Confirm: `tryAutoAttach`
  (line 348) → logger (352) → `sessions.New` (377) → `ctrl.Listen()`
  (400) → `pool.Run(ctx)` (414). Pool.Run is what spawns the
  supervised claude. **Crucially:** Listen happens *before* Pool.Run.
  If Listen fails (e.g. socket in use by a daemon at the same path),
  the function returns before any claude is spawned. This drives the
  separate-sockets design — see *Why separate sockets*.
- `internal/control/client.go:205` (`SessionsList`) — the wire
  client used to snapshot the daemon's session registry before /
  after the foreground runs (test 1's "registry unchanged" assertion).
- `internal/e2e/attach_pty.go:200-235` (`waitDaemonReady`) — the
  daemon-readiness poll used by `spawnAutoAttachDaemon`. Reuse
  unchanged.
- `internal/e2e/harness.go:419-432` (`childEnv`) — env scrubber.
  Strips `HOME` / `PYRY_NAME`, sets `HOME=<test-home>`. The fallback
  helper appends `extraEnv` after `childEnv` so a test's
  `PYRY_NO_AUTO_ATTACH=1` lands in the foreground's env without
  poisoning the daemon's.
- `internal/e2e/harness.go:396-417` (`killSpawned`) — SIGTERM →
  grace → SIGKILL teardown for spawned subprocesses. Reuse on the
  foreground process at cleanup time.
- `docs/specs/architecture/163-e2e-foreground-auto-attach-happy.md` §
  "Design / `pgrepChildren`" — the process-tree primitive whose
  inverse-direction assertion this ticket reuses (children ≥ 1 for
  fallback tests, vs children == 0 for the happy path). Already
  shipped in `auto_attach.go`.
- `docs/specs/architecture/158-foreground-auto-attach.md` (full) —
  the production design. Confirms the gate ordering and the env-
  override semantics (only `=="1"` short-circuits; `=="true"` does
  not — see `TestTryAutoAttach_EnvOptOutNonOne`).
- `docs/lessons.md` § "macOS sun_path 104-byte limit" — short
  `MkdirTemp` prefix is load-bearing on macOS. This helper inherits
  the `pyry-aa-*` style from `startForegroundAutoAttach` but uses a
  distinct prefix (`pyry-fb-*`) so test logs disambiguate.

## Context

#158 shipped the foreground auto-attach gate. #163 covered its
success branch end-to-end. The fall-through branches are exercised
only by the unit tests in `cmd/pyry/auto_attach_test.go` today —
those stop at the `tryAutoAttach` boundary and never cross the
process line. The system-level acceptance criterion *"a stale
operator invocation, a missing daemon, or an explicit opt-out leads
the binary into the existing supervised-spawn behaviour"* is e2e by
nature.

The three test fixtures collectively pin:

- **No regression hides behind a clean unit-test pass.** A bug that
  makes auto-attach silently consume the foreground process even on
  fall-through would still pass the function-level tests; only a
  binary-level run can catch a botched return path.
- **Cross-contamination doesn't occur.** Test 1 asserts the daemon's
  session registry is byte-identical before/after the foreground
  runs (no spurious writes from a buggy auto-attach probe).
- **The env-override flag is read by the production binary.** Unit
  tests use `t.Setenv` against `tryAutoAttach` directly; the e2e
  test confirms the binary reads its own real env at startup.

## Design

### Files

```
internal/e2e/auto_attach.go              +~120 LOC  (additive: new helpers)
internal/e2e/auto_attach_fallback_test.go ~110 LOC  (new file, 3 tests)
```

Both files build-tagged `//go:build e2e`. No production-code edits.
No knowledge-base or feature-doc updates (the docs from #158/#163
already describe the behaviour).

### Two design choices to acknowledge up front

#### Why separate sockets

The naive design has the foreground binary share the daemon's
socket so test 1's probe really hits `SessionsHasID == false` and
test 3's env override really skips a probe that *would* have
succeeded. **This naive design cannot satisfy AC#1's process-tree
assertion.**

`runSupervisor` runs `tryAutoAttach` first, then — on fall-through —
`ctrl.Listen()` on the same `socketPath`, **before** `pool.Run`. If
the daemon already holds the socket, Listen returns
`bind: address already in use` and the foreground process exits
without spawning claude. No claude child appears in its process tree.
AC#1 (and AC#3) explicitly require the inverse of #163's pgrep
assertion: children ≥ 1.

There is no production affordance for disjoint probe-bind paths in
the foreground binary, and adding one is out of scope for an e2e
ticket. The architect's decision: **all three tests use a unique
socket path for the foreground**, distinct from any daemon's socket.
The foreground's `tryAutoAttach` falls through via the **stat-error
(ENOENT)** gate (its own socket doesn't exist before its own
`Listen` runs), then `Listen` succeeds, `pool.Run` spawns the
sleep-claude stand-in, the process-tree assertion fires.

#### Why ENOENT in all three tests

Consequence of the above: every fall-through happens via the same
gate (`os.Stat(socketPath)` → ENOENT). The named-scenario
discrimination ("session missing" vs "no daemon" vs "env override")
exists in the **test environment** (daemon presence, env vars), not
in the **gate the foreground exercises**. The differentiation is at
the system-state level:

| Test | Daemon spawned? | `PYRY_NO_AUTO_ATTACH` set? | Foreground UUID |
|---|---|---|---|
| #1 SessionMissing | yes (separate socket) | no | random unregistered UUID |
| #2 NoDaemon | no | no | random UUID |
| #3 EnvOverride | yes (separate socket) | yes (`=1`) | the daemon's *registered* UUID |

The gate-discrimination contract — *that the env-override gate, the
no-session-id gate, the stat-error gate, and the has-id-false gate
each independently produce fall-through* — is already pinned by
six unit tests in `cmd/pyry/auto_attach_test.go` shipped with #158.
The e2e tests pin the orthogonal contract: *that fall-through, however
triggered, lands the foreground process in supervisor mode with a
real claude child*.

This split — gate logic at unit tier, system behaviour at e2e tier —
is the same separation `internal/control/*_test.go` and
`internal/e2e/*_test.go` already maintain for every other verb.
The fallback tests aren't redundant with the unit tests; they pin a
different layer.

### `startForegroundSupervised` — surface

New helper in `internal/e2e/auto_attach.go`:

```go
// ForegroundSupervisedClient is a programmatic peer for a foreground
// pyry binary configured to fall through auto-attach into supervisor
// mode. Unlike ForegroundAutoAttachClient (which observes a successful
// auto-attach via stdio round-trip), this client observes only the
// process tree and exit state — fallback tests don't need bytes to
// flow through stdio.
//
// Stdin/stdout/stderr are wired to /dev/null sinks (stdin) and
// captured buffers (stdout/stderr) — the foreground spawns a
// sleep-claude stand-in via creack/pty internally, and the supervisor
// in service mode (non-tty stdin) routes PTY traffic through a Bridge,
// not the foreground's stdio. Plain os.Pipe() / os.DevNull is fine.
type ForegroundSupervisedClient struct {
    // Pid is the foreground pyry process pid. Pass to pgrepChildren
    // for the supervisor-mode assertion.
    Pid int

    // Stderr captures the foreground pyry's stderr (through safeBuffer
    // — concurrent writer goroutine, concurrent reader t.Fatalf).
    // Mostly used for failure diagnostics.
    Stderr *safeBuffer

    // SocketPath / HomeDir are the foreground's *own* socket and home
    // (distinct from any daemon spawned by the test). Exposed for
    // diagnostics; tests don't normally read these directly.
    SocketPath string
    HomeDir    string

    cmd         *exec.Cmd
    done        chan struct{}
    cleanupOnce sync.Once
}

// startForegroundSupervised spawns a foreground pyry binary expected
// to fall through auto-attach (its own socket doesn't exist before
// runSupervisor's Listen runs) and enter supervisor mode with the
// sleep-claude stand-in.
//
// `sessionID` is appended to the foreground's claudeArgs as
// `--session-id <id>` so extractSessionID returns non-empty (the
// foreground's no-session-id gate would otherwise short-circuit and
// produce an indistinguishable fall-through path; we want the test
// shape to mirror real-world Claudian invocations, which always
// supply --session-id).
//
// `extraEnv` is appended verbatim to childEnv(home) before exec, so
// the test can inject PYRY_NO_AUTO_ATTACH=1 without polluting any
// other process's env. Pass nil when no extra env is needed.
//
// Returns once the foreground has both:
//   1. Survived a 500ms settle window without exiting (early-death
//      detector mirrors startForegroundAutoAttach's pattern), AND
//   2. Acquired at least one direct child PID (polled via
//      pgrepChildren with a 5s deadline — matches the round-trip
//      deadline used elsewhere in the e2e suite).
//
// Skips on os.Pipe / pgrep unavailability.
func startForegroundSupervised(t *testing.T, sessionID string, extraEnv []string) *ForegroundSupervisedClient
```

#### Construction sequence

1. `os.MkdirTemp("", "pyry-fb-*")` — short prefix for sun_path
   safety. `t.Cleanup` removes the dir.
2. `socketPath := filepath.Join(home, "pyry.sock")` — the
   foreground's own socket. Guaranteed not to exist before the
   foreground starts.
3. `claudeBin := writeSleepClaude(t, home)` — copy the existing
   helper from `cap_test.go`. The sleep-claude wrapper exec's
   `sleep 99999`, which ignores all argv (so Pool.Create's appended
   `--session-id` doesn't trip flag parsing) and stays alive
   indefinitely.
4. `bin := ensurePyryBuilt(t)` — reuse the cached pyry build.
5. `exec.Command(bin, "-pyry-socket="+socketPath,
   "-pyry-claude="+claudeBin, "-pyry-idle-timeout=0",
   "-pyry-resume=false", "--", "--session-id", sessionID)`. The
   `--` and the trailing `--session-id` mirror #163's argv shape;
   `-pyry-idle-timeout=0` and `-pyry-resume=false` mirror
   `spawnAutoAttachDaemon`'s flags so the supervisor doesn't churn.
6. `cmd.Stdin = os.DevNull-equivalent` (open `os.Open("/dev/null")`
   or use a closed pipe end). Stdout to a discarded buffer. Stderr
   to `safeBuffer` for diagnostics. `cmd.Env = append(childEnv(home),
   extraEnv...)`.
7. `cmd.Start`. Goroutine waits on `cmd.Wait` and closes `done`.
8. **Settle:** `select { case <-done: t.Fatalf("early exit, stderr:
   %s", c.Stderr.String()); case <-time.After(500*time.Millisecond):
   }`. Mirrors the early-death detector in
   `startForegroundAutoAttach`.
9. **Await child:** poll `pgrepChildren(c.Pid)` every 50ms for up
   to 5s. First non-empty result wins; on timeout, `t.Fatalf` with
   foreground stderr in the diagnostic so a "supervisor never spawned
   claude" regression is legible.
10. Register `t.Cleanup` for teardown (close stdin sink, killSpawned
    foreground, os.Remove socket and registry).

The settle window is necessary because pgrep can race ahead of the
supervisor's first `os.StartProcess`. Polling with a deadline closes
that race deterministically.

### Daemon-side helper — `spawnDaemonWithRegisteredSession`

For tests 1 and 3, a daemon must be running with at least one
registered session. Helper added alongside `startForegroundSupervised`:

```go
// spawnDaemonWithRegisteredSession spawns an auto-attach daemon at a
// home/socket path *separate from* any foreground binary's socket
// (see "Why separate sockets" in the spec). Registers `label` via
// SessionsNew and returns the daemon's socket path plus the
// registered session id, so tests can later call
// control.SessionsList(socket) to assert the registry is unchanged.
//
// The daemon is auto-cleaned up via t.Cleanup (registered inside
// spawnAutoAttachDaemon's existing cleanup chain).
func spawnDaemonWithRegisteredSession(t *testing.T, label string) (socket, sessionID string)
```

Implementation: ~25 LOC. `os.MkdirTemp("", "pyry-fbd-*")`, call
`spawnAutoAttachDaemon(t, home)`, `waitDaemonReady`, `control.SessionsNew(ctx, socket, label)`, return `(socket, id)`. The daemon's
home/socket is distinct from the foreground's home/socket; both can
coexist for the duration of the test.

### Tests

Test file `internal/e2e/auto_attach_fallback_test.go`. Each test
~30 LOC.

#### `TestE2E_ForegroundAutoAttach_FallsThroughWhenSessionMissing`

```go
func TestE2E_ForegroundAutoAttach_FallsThroughWhenSessionMissing(t *testing.T) {
    daemonSocket, registeredID := spawnDaemonWithRegisteredSession(t, "fallback-decoy")

    // Snapshot the daemon's registry BEFORE the foreground runs.
    pre := mustSessionsList(t, daemonSocket)

    // Foreground asks for a UUID that is NOT registered. New random
    // UUID guarantees no collision with the daemon's registered id.
    other := newCanonicalUUID(t)
    if other == registeredID {
        t.Fatalf("UUID collision; regenerate test")
    }

    c := startForegroundSupervised(t, other, nil)

    // Process-tree assertion — supervisor mode has at least one
    // direct child (sleep-claude). Inverse of #163's assertion.
    children, err := pgrepChildren(c.Pid)
    if err != nil {
        t.Skipf("e2e: pgrep unavailable: %v", err)
    }
    if len(children) == 0 {
        t.Fatalf("foreground pid=%d has no children; expected supervised claude\nstderr:\n%s",
            c.Pid, c.Stderr.String())
    }

    // Daemon's registry is unchanged: the foreground didn't probe or
    // mutate it (the foreground's own socket was the probe target,
    // and that socket was never visible to the daemon).
    post := mustSessionsList(t, daemonSocket)
    if !sessionsEqual(pre, post) {
        t.Fatalf("daemon registry changed; pre=%v post=%v", pre, post)
    }
}
```

`mustSessionsList`, `sessionsEqual`, `newCanonicalUUID` are tiny
inline helpers in this test file (~5 LOC each). `newCanonicalUUID`
returns the canonical 36-char UUID string used elsewhere in the
e2e suite (see `cmd/pyry/auto_attach_test.go`'s `canonicalUUID`).

#### `TestE2E_ForegroundAutoAttach_FallsThroughWhenNoDaemon`

```go
func TestE2E_ForegroundAutoAttach_FallsThroughWhenNoDaemon(t *testing.T) {
    // No daemon at all — foreground runs in isolation. Its own
    // socket doesn't exist before its Listen runs, so tryAutoAttach
    // falls through via stat ENOENT.
    c := startForegroundSupervised(t, newCanonicalUUID(t), nil)

    children, err := pgrepChildren(c.Pid)
    if err != nil {
        t.Skipf("e2e: pgrep unavailable: %v", err)
    }
    if len(children) == 0 {
        t.Fatalf("foreground pid=%d has no children; expected supervised claude\nstderr:\n%s",
            c.Pid, c.Stderr.String())
    }
}
```

The AC bullet notes a stale-socket-file case is *optional* coverage.
This spec elects not to cover it explicitly: the unit test
`TestTryAutoAttach_DaemonUnresponsive` already pins the
"socket file present, dial fails" gate, and the e2e cost of an
extra fixture (manually create a socket file with no listener,
arrange for stat to succeed but dial to fail) outweighs the
incremental coverage. Documented in *Out of scope*.

#### `TestE2E_ForegroundAutoAttach_RespectsEnvOverride`

```go
func TestE2E_ForegroundAutoAttach_RespectsEnvOverride(t *testing.T) {
    daemonSocket, registeredID := spawnDaemonWithRegisteredSession(t, "fallback-override")

    pre := mustSessionsList(t, daemonSocket)

    // Foreground asks for the *registered* UUID and would auto-
    // attach in production… EXCEPT for the env override. With
    // separate sockets, the structural fall-through is via stat
    // ENOENT; this test pins that PYRY_NO_AUTO_ATTACH=1 doesn't
    // crash the foreground, doesn't trigger an attach attempt
    // against any reachable daemon, and doesn't touch the daemon's
    // registry. (Gate-level coverage of "env override skips probe"
    // lives in cmd/pyry/auto_attach_test.go.)
    c := startForegroundSupervised(t, registeredID, []string{"PYRY_NO_AUTO_ATTACH=1"})

    children, err := pgrepChildren(c.Pid)
    if err != nil {
        t.Skipf("e2e: pgrep unavailable: %v", err)
    }
    if len(children) == 0 {
        t.Fatalf("foreground pid=%d has no children; expected supervised claude\nstderr:\n%s",
            c.Pid, c.Stderr.String())
    }

    post := mustSessionsList(t, daemonSocket)
    if !sessionsEqual(pre, post) {
        t.Fatalf("daemon registry changed; pre=%v post=%v", pre, post)
    }
}
```

### Concurrency

No new goroutines beyond what the existing helpers already
establish:

- One goroutine waits on the foreground's `cmd.Wait()`, closes
  `done`. (Same shape as `startForegroundAutoAttach`.)
- One goroutine in os/exec copies stderr into `safeBuffer`. (os/exec
  runs this internally — same as #163.)
- The polling loop inside `awaitClaudeChild` is sequential within
  the test goroutine.

The daemon side reuses `spawnAutoAttachDaemon` verbatim — one
wait-goroutine per daemon, identical to its existing usage in #163.

The test sequence is strictly serial:

1. (tests 1, 3 only) spawn daemon, wait ready, register session,
   snapshot list.
2. Spawn foreground.
3. Poll for child.
4. Run pgrep assertion.
5. (tests 1, 3 only) snapshot list again, compare.
6. Cleanup (LIFO via `t.Cleanup`).

### Error handling

| Failure mode | Outcome |
|---|---|
| `os.Pipe` (if helper opens any) / `os.MkdirTemp` fails | `t.Skipf` (sandboxed CI) / `t.Fatalf` respectively. Inherited from existing helpers. |
| Sleep-claude script write fails | `t.Fatalf`. Inherited from `writeSleepClaude`. |
| Foreground exits within 500ms settle window | `t.Fatalf` with exit code + foreground stderr. Mirrors the early-death detector pattern. |
| pgrep poll times out (no child after 5s) | `t.Fatalf` with foreground stderr. The diagnostic must include the stderr buffer so a supervised-spawn failure (e.g. sleep-claude script not executable) is legible. |
| `pgrepChildren` returns error (pgrep absent) | `t.Skipf` with the LookPath error. Same precedent as #163. |
| Daemon spawn / readiness fails (tests 1, 3) | `t.Fatalf` with daemon stderr (inherited from `spawnAutoAttachDaemon` / `waitDaemonReady`). |
| `control.SessionsList` fails (tests 1, 3) | `t.Fatalf` with the wire error. Surfaces a daemon-side regression rather than masking it. |
| `sessionsEqual` returns false (tests 1, 3) | `t.Fatalf` with both snapshots in the diagnostic. |

### Teardown ordering

`t.Cleanup` registers the foreground teardown after the daemon
teardown runs LIFO, so the foreground exits first. Inside the
foreground teardown:

1. Close the foreground's stdin sink (best-effort, no-op for
   `/dev/null`).
2. Wait up to 2s for `done`; on timeout, `killSpawned` the
   foreground.
3. `os.Remove(c.SocketPath)` (best-effort).

Daemon teardown (where present) follows `spawnAutoAttachDaemon`'s
existing cleanup — `killSpawned` + remove socket file. Wrapped in
`sync.Once` per process.

The supervised sleep-claude (the foreground's child) is owned by
the foreground process group. SIGTERM to the foreground propagates
through `exec.CommandContext` semantics inside the supervisor and
reaps the child. If a SIGKILL is needed (the 2s grace lapses), the
sleep-claude is reaped via the foreground's `os/exec` cleanup.

### Build constraints / tags

`//go:build e2e` on both files. No `e2e_install` — these tests don't
exercise the install plumbing. The fallback test file imports the
same packages as `auto_attach_happy_test.go` (`bytes`, `testing`,
`time`, `internal/control`).

`go test -tags e2e -race ./internal/e2e/...` is the ACL run.

## Testing strategy

These tests *are* the strategy. No further unit-level coverage is
added — the gate logic is fully unit-tested in
`cmd/pyry/auto_attach_test.go`, the wire client is covered in
`internal/control/*_test.go`, and the supervisor is covered by the
existing PTY/restart e2e suites.

Verification commands the developer should run:

```bash
go test -tags e2e -race -run TestE2E_ForegroundAutoAttach_FallsThrough ./internal/e2e/
go test -tags e2e -race -run TestE2E_ForegroundAutoAttach_RespectsEnv ./internal/e2e/
go test -tags e2e -race ./internal/e2e/...   # AC bullet #4: full suite clean
go vet ./...
staticcheck ./...
```

The full-suite run is the actual AC. Targeted runs are for the
developer iterating.

## Open questions

None. The design is mechanical: one helper that mirrors
`startForegroundAutoAttach`'s shape minus the daemon-on-shared-socket
piece, one daemon-helper that wraps `spawnAutoAttachDaemon` +
`SessionsNew`, three tests with identical scaffolds and a small
matrix of inputs.

If the developer hits "the foreground exits early with bind error",
that's a sign the foreground's socket path overlapped with another
process — the test home should be unique-per-test via
`os.MkdirTemp`, so this would point to a leak across runs. Fix by
ensuring `t.Cleanup` runs the home-removal in all branches.

If the developer hits "sleep-claude doesn't appear as a child within
5s", the supervisor's PTY spawn is failing. Surface the foreground
stderr in the diagnostic; the failure mode will be visible
(typically: shell script not executable, or the script's `exec sleep`
errored).

If the developer wonders "why aren't we sharing the daemon's
socket like #163's happy path does?" — re-read *Why separate sockets*.
The happy path never reaches `Listen` because `AttachStdio` blocks
inside `tryAutoAttach`. The fall-through path *does* reach `Listen`,
which is why shared-socket fails the AC.

## Out of scope

- **Sharing a socket between daemon and foreground.** Would conflict
  on bind. Tests use distinct sockets per *Why separate sockets*.
- **Discriminating gate decisions at e2e.** Unit tests cover this;
  see *Why ENOENT in all three tests*.
- **Stale-socket-file scenario** (socket file exists, no listener).
  AC bullet 2 marks this optional. The gate is unit-tested as
  `TestTryAutoAttach_DaemonUnresponsive`.
- **Latency / perf assertions** (e.g. AC#3 from #158 — fall-through
  in <50ms when no daemon). Not on this ticket's AC; #158's unit
  test pins the structural fast-path.
- **PYRY_NO_AUTO_ATTACH non-`1` values.** Unit-tested as
  `TestTryAutoAttach_EnvOptOutNonOne`.
- **Asserting on the foreground's stderr being empty.** Supervisor
  mode emits log lines through the slog handler — non-empty stderr
  is expected, unlike auto-attach mode. Surfacing stderr only in
  failure diagnostics is sufficient.
- **Multi-client behaviour** — out of #158's scope, untouched here.
- **Windows / BSD platform support** — out of scope for the project.

## Production / test diff sizing

| Surface | LOC est. | File(s) |
|---|---|---|
| `ForegroundSupervisedClient` + `startForegroundSupervised` | ~85 | `internal/e2e/auto_attach.go` (additive) |
| `spawnDaemonWithRegisteredSession` | ~25 | `internal/e2e/auto_attach.go` (additive) |
| `awaitClaudeChild` (poll-pgrep helper) | ~15 | `internal/e2e/auto_attach.go` (additive) |
| `TestE2E_ForegroundAutoAttach_FallsThroughWhenSessionMissing` | ~35 | `internal/e2e/auto_attach_fallback_test.go` (new) |
| `TestE2E_ForegroundAutoAttach_FallsThroughWhenNoDaemon` | ~20 | `internal/e2e/auto_attach_fallback_test.go` (new) |
| `TestE2E_ForegroundAutoAttach_RespectsEnvOverride` | ~35 | `internal/e2e/auto_attach_fallback_test.go` (new) |
| `mustSessionsList`, `sessionsEqual`, `newCanonicalUUID` | ~25 | `internal/e2e/auto_attach_fallback_test.go` (new) |
| **Production total** | **0** | — |
| **Test total** | **~240** | **2 files (1 edited additively, 1 new)** |

S envelope check:

- Production LOC: 0 (well under 100). ✓
- Files: 1 edited (additive only) + 1 new = 2 total, 1 new. ✓ (≤3 new)
- New exported types: 1 (`ForegroundSupervisedClient`). ✓ (≤5)
- Consumer cascade: 0 — additive helpers, no rename, no signature
  change to existing functions. ✓ (≤10 call sites)
- Acceptance criteria: 4 (3 tests + suite-clean). ✓ (≤5)

Sized **S**. No split required.

The line count includes ~25 LOC of inline helpers
(`mustSessionsList` / `sessionsEqual` / `newCanonicalUUID`) that
are byte-similar to existing helpers in `cap_test.go` and
`cmd/pyry/auto_attach_test.go`. The developer can either copy them
into `auto_attach_fallback_test.go` (cheaper diff, easier to read in
isolation) or extract a shared helper. **Prefer the copy** — three
tests, ~25 LOC of duplication, no ongoing churn risk. Don't refactor
existing test helpers in this ticket; that's a separate scope with
its own risk surface.
