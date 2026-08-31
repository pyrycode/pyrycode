# Foreground Auto-Attach Fallback Pattern (`auto_attach_fallback_test.go`, #164)

The happy-path test (#163) pins that auto-attach fires when the daemon
hosts the requested UUID. #164 pins the inverse: each of `tryAutoAttach`'s
fall-through arms lands the foreground binary in **supervisor mode** —
spawning its own claude child rather than auto-attaching. Three tests,
all build-tagged `e2e`, all additive to `auto_attach.go`'s harness
surface; production diff is zero.

```go
// 1. Daemon up, but UUID is not registered.
func TestE2E_ForegroundAutoAttach_FallsThroughWhenSessionMissing(t *testing.T) {
    daemonSocket, registeredID := spawnDaemonWithRegisteredSession(t, "fallback-decoy")
    pre := mustSessionsList(t, daemonSocket)

    other := newCanonicalUUID(t)             // guaranteed != registeredID
    c := startForegroundSupervised(t, other, nil)

    children, err := pgrepChildren(c.Pid)
    if err != nil { t.Skipf("e2e: pgrep unavailable: %v", err) }
    if len(children) == 0 { t.Fatalf("expected supervised claude") }

    post := mustSessionsList(t, daemonSocket)
    if !sessionsEqual(pre, post) { t.Fatalf("daemon registry changed") }
}

// 2. No daemon, no socket.
// 3. Daemon up, UUID registered, PYRY_NO_AUTO_ATTACH=1 in env.
```

The headline assertion is the **inverse** of #163's: `pgrepChildren(c.Pid)`
returns ≥1 child PID (the supervised sleep-claude). Tests #1 and #3 also
snapshot `control.SessionsList` before/after to pin the daemon-side
registry-unchanged invariant.

### Why all three tests fall through via the same gate

`tryAutoAttach` has four independent fall-through arms (env opt-out, no
session-id, stat error, has-id false). One might expect each e2e test to
exercise a different arm — e.g. test #1 hits `has-id false`, test #3 hits
the env opt-out — to mechanically discriminate. **They cannot, given the
no-production-edits constraint.** All three e2e tests fall through via
the same arm: `os.Stat(socketPath) → ENOENT` on the *foreground's own*
socket.

The driver is a forced architectural choice the spec calls "separate
sockets":

- After fall-through, `runSupervisor` runs `ctrl.Listen()` on
  `socketPath` *before* `pool.Run` spawns the supervised child.
- If the foreground binary shared the daemon's socket (so its
  `tryAutoAttach` probe really hit `SessionsHasID == false`), `Listen`
  would return `bind: address already in use` and the foreground would
  exit before any claude child appeared.
- The pgrep-children-≥1 assertion then could not fire.

So the foreground gets its **own** socket path, distinct from any daemon
spawned by the test. Its socket doesn't exist before its own `Listen`
runs, so `tryAutoAttach`'s `os.Stat` returns ENOENT and the function
falls through. The test environment varies (daemon up/down, env var
present/absent, UUID registered/not) but the gate exercised is always
ENOENT.

This is fine because **gate discrimination is a unit-tier contract,
not an e2e-tier contract.** The six unit tests in
`cmd/pyry/auto_attach_test.go` (`TestTryAutoAttach_NoSessionID`,
`_EnvOptOut`, `_SocketAbsent_FastPath`, `_DaemonUnresponsive`,
`_HasIDFalse`, `_HasIDInvalid`, shipped with #158) already pin that each
arm independently produces fall-through. The e2e tests pin an
orthogonal contract: that fall-through, *however triggered*, lands the
foreground process in supervisor mode with a real claude child. Same
unit-vs-e2e split every other verb in this suite already maintains.

### What would break if a test naively shared the daemon's socket

A buggy test that does `socketPath := daemonSocket` for the foreground
fails like this:

1. `tryAutoAttach` probes the daemon's socket — for tests #1/#3 this
   succeeds (daemon is up); for test #1 the has-id query returns false,
   for test #3 the env opt-out short-circuits the probe.
2. Either way, `tryAutoAttach` returns `(false, nil)`.
3. `runSupervisor` proceeds to `ctrl.Listen(daemonSocket)` → `bind:
   address already in use` from the kernel.
4. Foreground `runSupervisor` returns the bind error. `cmd.Wait`
   completes; no claude child was ever spawned.
5. `pgrepChildren(foregroundPid)` returns `[]int{}` — the test's
   inverse-of-#163 assertion fails with a confusing diagnostic.

Adding a foreground-only `-pyry-no-listen` flag (or splitting probe-bind
paths) would let a test exercise the real `has-id-false` gate at e2e —
out of scope for this ticket per the architect's design. The spec's
"Why separate sockets" section is the reference if a future ticket
revisits this.

### Public API additions in `auto_attach.go`

```go
type ForegroundSupervisedClient struct {
    Pid        int          // pass to pgrepChildren for the assertion
    Stderr     *safeBuffer  // diagnostics; supervisor mode emits log lines, non-empty is expected
    SocketPath string       // foreground's own socket (distinct from any daemon's)
    HomeDir    string       // foreground's own $HOME
    // unexported plumbing
}

func startForegroundSupervised(t *testing.T, sessionID string, extraEnv []string) *ForegroundSupervisedClient
func awaitClaudeChild(pid int, total time.Duration) error
func spawnDaemonWithRegisteredSession(t *testing.T, label string) (socket, sessionID string)
```

`startForegroundSupervised` is the simpler sibling of
`startForegroundAutoAttach`:

- No bidirectional pipes — the test never round-trips bytes; `cmd.Stdin`
  is `/dev/null`, `cmd.Stdout` is `io.Discard`.
- Argv shape: `pyry -pyry-socket=<own-sock> -pyry-claude=<sleep-claude>
  -pyry-idle-timeout=0 -pyry-resume=false -- --session-id <uuid>`.
  `--session-id` is appended so `extractSessionID` returns non-empty
  (otherwise the no-session-id gate would short-circuit and fall through
  via a different arm — we want shape parity with real Claudian
  invocations).
- `extraEnv` is appended to `childEnv(home)` so a test injects
  `PYRY_NO_AUTO_ATTACH=1` into the foreground without polluting a
  daemon's env.
- Two-phase readiness: 500ms settle window (early-death detector
  identical to `startForegroundAutoAttach`), then `awaitClaudeChild`
  polls `pgrepChildren` every 50ms with a 5s deadline. The poll closes
  the race where `pgrep` outruns the supervisor's first
  `os.StartProcess` inside `pool.Run`.
- Skip on `pgrep` unavailability up front (sandboxed CI).

`spawnDaemonWithRegisteredSession` wraps `spawnAutoAttachDaemon` +
`waitDaemonReady` + `control.SessionsNew`, returning `(socket, id)` so
tests #1 and #3 can call `control.SessionsList(socket)` for the
registry-unchanged assertion.

### Sleep-claude over echo-claude for the supervised child

The fallback tests reuse `writeSleepClaude` (#116, relocated to
`harness.go` by #918) — the supervised child is
`#!/bin/sh\nexec sleep 99999\n`, which ignores
the appended `--session-id <uuid>` from `Pool.Create` and stays alive
indefinitely. The happy-path test (#163) needs an *echo-claude* helper
because it round-trips bytes; the fallback tests don't, and `sleep` is
the simpler stand-in. (Both wrappers exist for the same underlying
reason — see [lessons.md § `Pool.Create` appends
`--session-id`](../../lessons.md#poolcreate-appends---session-id-breaking-naive-claude-stand-ins).)

### Inline test helpers — copied, not extracted

`mustSessionsList`, `sessionsEqual`, `newCanonicalUUID` (~25 LOC total)
live in `auto_attach_fallback_test.go` rather than in a shared helper.
Three tests, ~25 LOC of byte-similar duplication, no ongoing churn risk;
extracting into `harness.go` or a third file would invert the
dependency direction for one ticket's benefit. Same posture as the
`registryEntry` mirror types in `restart_test.go` (#106). `sessionsEqual`
explicitly uses `time.Time.Equal` for `LastActive`, not `==`, because
the JSON roundtrip through the wire strips monotonic-clock state — see
[lessons.md § JSON roundtrip strips monotonic-clock
state](../../lessons.md#json-roundtrip-strips-monotonic-clock-state-from-timetime).

### Supervisor stderr is expected to be non-empty

Unlike `ForegroundAutoAttachClient` (which expects a clean stderr in
steady state because the auto-attach branch suppresses human-affordance
lines), `ForegroundSupervisedClient` runs the full supervisor path —
the slog handler emits log lines to stderr in steady state. The
`Stderr` field is surfaced **only in failure diagnostics**, never as a
positive assertion target.

### Stale-socket-file scenario deliberately skipped

AC bullet #2 marks "stale socket file with no listener" as optional
coverage. The unit test `TestTryAutoAttach_DaemonUnresponsive` already
pins the "socket file present, dial fails" gate; arranging the e2e
fixture (manually create a socket file with no listener, hope `os.Stat`
succeeds and `net.Dial` fails) would add complexity for incremental
coverage of a path the unit test already pins.

### Cleanup ordering

`t.Cleanup` runs LIFO. Registration order:

1. (tests 1, 3) daemon home cleanup, then daemon `killSpawned`.
2. Foreground home cleanup.
3. Foreground teardown (close stdin sink → `killSpawned` foreground
   → `os.Remove(c.SocketPath)`).

LIFO means foreground tears down first; SIGTERM to the foreground
propagates through `exec.CommandContext` semantics inside the
supervisor and reaps the supervised sleep-claude child (the foreground
owns the child process group). Daemon teardown follows.

### Production diff is zero

Two files: `internal/e2e/auto_attach.go` (+199 LOC, additive) and
`internal/e2e/auto_attach_fallback_test.go` (+143 LOC, new). No edits
to existing tests, no new exported package types. Default `go test
./...` unaffected. `go test -tags e2e -race ./internal/e2e/...` clean.
