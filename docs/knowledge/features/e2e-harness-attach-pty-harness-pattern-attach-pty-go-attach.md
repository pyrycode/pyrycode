# Attach PTY Harness Pattern (`attach_pty.go`, `attach_pty_test.go`, #125)

The non-interactive `Harness` drives daemon-only verbs over the control socket
with stdio pipes. `pyry attach` is the only interactive surface in the product
and needs a controlling terminal — pipes don't satisfy `term.IsTerminal`. #125
adds a sibling **`AttachHarness`** in the same package (build tag `e2e ||
e2e_install`) that owns:

1. A `pyry` daemon in bridge mode whose supervised "claude" is the
   `echoClaudeScript` shell wrapper (#257), which `exec`s the e2e test
   binary running `TestHelperProcess` in echo mode while dropping its own
   argv — see § Helper "claude" via `TestHelperProcess` re-exec below.
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

### Helper "claude" via `TestHelperProcess` re-exec, through the `echoClaudeScript` wrapper (#257)

```
spawnAttachableDaemon args:
  -pyry-claude=<home>/echo-claude.sh         # writeEchoClaude(t, home)
  -pyry-resume=false

daemon env:
  GO_TEST_HELPER_PROCESS=1
  GO_TEST_HELPER_MODE=echo
  E2E_HELPER_BIN=os.Args[0]                  # the e2e test binary
```

Through #257, `-pyry-claude` pointed directly at `os.Args[0]` with the
trailing args `-- -test.run=TestHelperProcess`. That reached `Pool.Create`
for any non-bootstrap session: `Pool.Create` appends `--session-id <uuid>`
to the supervised claude's argv (`internal/sessions/pool.go`), which the Go
test binary's `flag.Parse()` rejects with `flag provided but not defined:
-session-id`, exiting 2 before `TestHelperProcess` ever runs. Every test
routing through `spawnAttachableDaemon` was exposed to this — including the
bootstrap session, since `Pool.Create` is unconditional, not
non-bootstrap-only.

\#257 migrated `spawnAttachableDaemon` onto the same `echoClaudeScript` /
`writeEchoClaude` shell wrapper `spawnAutoAttachDaemon` (#163) already used
— see § Daemon variant below for the wrapper's shape. The wrapper ignores
its own argv and `exec`s `$E2E_HELPER_BIN -test.run=TestHelperProcess`, so
the appended `--session-id <uuid>` is dropped before the Go binary ever
parses flags. `supervisor.runOnce` does `cmd.Env = append(os.Environ(),
helperEnv...)`, so env vars set on the daemon's `cmd.Env` (including the new
`E2E_HELPER_BIN`) flow through to the wrapper, which forwards them across
its `exec` to the re-exec'd test binary. The helper itself is unchanged: it
gates on `GO_TEST_HELPER_PROCESS=1` (no-op in normal `go test` runs),
switches on `GO_TEST_HELPER_MODE`, calls `term.MakeRaw` on stdin, then
`io.Copy(stdout, stdin)`.

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

- Per-session attach exclusivity (`ErrBridgeBusy`).

Clean detach via `Ctrl-B d` is covered by #127 (see § Attach Detach
Pattern below). Restart survival is covered by #128 (see § Attach
Restart Pattern below). Live SIGWINCH propagation is covered by #126
(see § Attach SIGWINCH Pattern below). The harness's `SocketPath`
field is exposed so a follow-up can drive a second `pyry attach`
against an already-bound bridge to assert `ErrBridgeBusy`.

### Production diff is zero

`pyry attach`, the bridge, the control plane, and supervisor were all
already shipping. Test diff: ~351 LOC across two new files
(`attach_pty.go`, `attach_pty_test.go`).
