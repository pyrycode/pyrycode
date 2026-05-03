---
ticket: 125
title: e2e PTY-simulation harness + attach round-trip bytes
status: spec
size: S
---

# Files to read first

Read these before doing any exploration of your own — they are the load-bearing surfaces this spec composes.

- `internal/e2e/harness.go` — full file (~460 lines). The existing daemon harness. The new PTY harness lives in the same package and reuses three private helpers (`ensurePyryBuilt`, `childEnv`, `killSpawned`) and the shared timing constants (`readyDeadline`, `readyPollGap`, `termGrace`, `killGrace`). The new harness does **not** modify this file; it adds a new file alongside it. Pay particular attention to:
  - `harness.go:107-132` — `ensurePyryBuilt` (sync.Once-cached `go build` of pyry)
  - `harness.go:226-269` — `spawn` shape (stdout/stderr buffers, doneCh wait goroutine, readiness poll)
  - `harness.go:271-292` — `killSpawned` (SIGTERM → grace → SIGKILL teardown)
  - `harness.go:294-307` — `childEnv` (HOME substitution, PYRY_NAME strip)
- `internal/control/attach_client.go` — full file (~140 lines). The client side of attach. **Critical:** lines 68-74 — `Attach` calls `term.MakeRaw(os.Stdin.Fd())` if stdin is a TTY. When the harness hands `pyry attach` a PTY slave as stdin, the client puts the slave into raw mode (ECHO and ICANON off), which is what makes the round-trip deterministic.
- `internal/supervisor/supervisor.go:226-268` — `runOnce` bridge-mode branch. Confirms two facts the test depends on: (a) `cmd.Env = append(os.Environ(), s.cfg.helperEnv...)` propagates the daemon's own env to the supervised child — so env vars set on the daemon's `cmd.Env` flow through to the helper claude; (b) bridge mode does **not** put the supervisor's PTY into raw mode — the kernel's line discipline still runs with default ECHO on, so the helper must disable ECHO itself or the test sees doubled bytes.
- `internal/supervisor/supervisor_test.go:139-231` — the existing `TestHelperProcess` pattern. Shape reference: `if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" { return }`, switch on `GO_TEST_HELPER_MODE`. The new `internal/e2e/attach_pty_test.go` defines its **own** `TestHelperProcess` (different package, different test binary), with a single mode `"echo"`. Don't try to share this across packages — `os.Args[0]` differs per test binary.
- `cmd/pyry/main.go:278-285` — the foreground-vs-bridge detection. Pyry runs in bridge mode iff `term.IsTerminal(os.Stdin.Fd())` is false. `exec.Command` defaults stdin to `/dev/null` when not set, so a daemon spawned by the test always lands in bridge mode regardless of how `go test` is invoked. The new harness relies on this — it does not explicitly set `cmd.Stdin` on the daemon.
- `cmd/pyry/main.go:440-474` — `runAttach`. Confirms: `pyry attach` reads `term.GetSize` from stdout fd for the initial cols/rows, then calls `control.Attach(ctx, socket, cols, rows, sessionID)`. With the slave PTY on stdout, `GetSize` succeeds and a sane geometry flows through the handshake.
- `internal/supervisor/bridge.go:1-60` — confirms the bridge is bound to a single attacher; a second attach is rejected with `ErrBridgeBusy`. This is why the test attaches to a *single* session and does not run two attachers concurrently.
- `docs/lessons.md` § "PTY Testing" — CI runners on Linux do have `creack/pty` working (cross-platform reads + writes against a `pty.Open` pair are fine; what they lack is a *controlling* terminal). The skip-on-no-pty fallback in AC#5 is for hosts where `pty.Open` itself fails, not for absence of `term.IsTerminal(os.Stdin)` on CI.
- `docs/specs/architecture/122-fake-claude-test-binary.md` — the immediately-prior fake-binary spec. Pattern reference for an e2e-internal helper binary, but **the helper here is different**: ticket #122 uses a separate `package main` binary (rotation behaviour); this ticket uses the test binary itself via `TestHelperProcess` re-exec (echo behaviour). Both patterns appear in the AC; this one is simpler for echo because the work is `io.Copy` after `MakeRaw`.

# Context

`pyry attach` is the only interactive surface in the product, and the only one currently uncovered at the binary boundary. Unit tests cover the wire protocol (`internal/control/attach_*`) and the bridge pump (`internal/supervisor/bridge_test.go`); the existing e2e harness (`internal/e2e/harness.go`) drives non-interactive verbs against a daemon whose claude is `/bin/sleep 99999`. There is no test that proves a byte typed at a user's terminal travels: terminal → attach client → control socket → bridge → supervisor PTY → claude → and back.

This slice ships the harness alongside its first consumer test, which is the smallest meaningful proof that the harness works end-to-end. The follow-up tickets (#126 SIGWINCH, #127 clean detach, #128 restart survival) reuse this harness and are out of scope here.

The supervised child is not a real claude; it is a tiny echo helper invoked via the `TestHelperProcess` re-exec pattern already used in `internal/supervisor/supervisor_test.go`. The harness sets `-pyry-claude=os.Args[0]` (the e2e test binary) with `-- -test.run=TestHelperProcess` as claude args, plus `GO_TEST_HELPER_PROCESS=1` and `GO_TEST_HELPER_MODE=echo` in the daemon's env. The supervisor inherits the daemon's env onto the child, so the helper test runs as the supervised process and echoes bytes back.

# Design

## Approach

One new file under `internal/e2e/` for the harness, one for the test (which also defines `TestHelperProcess`). No edits to existing files. Reuse `ensurePyryBuilt`, `childEnv`, `killSpawned`, and the shared timing constants from `harness.go`.

The harness owns three OS resources:
1. The daemon process (`pyry` running in service/bridge mode, with the test binary as its claude).
2. A fresh `creack/pty` master/slave pair.
3. The `pyry attach` client process, whose stdin/stdout/stderr are bound to the slave fd.

The test interacts with the harness through the master fd: write input bytes, read output bytes, assert.

## End-to-end trajectory

```
test                           harness                                 daemon (pyry)                          supervised helper (test binary, echo mode)
─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
StartAttach(t, "")          ─> ensurePyryBuilt
                               home  := t.TempDir()
                               sock  := home/pyry.sock
                               env   := childEnv(home) ++
                                       GO_TEST_HELPER_PROCESS=1
                                       GO_TEST_HELPER_MODE=echo
                               cmd.Env = env
                               args  := -pyry-socket=sock
                                        -pyry-name=test
                                        -pyry-claude=os.Args[0]
                                        -pyry-idle-timeout=0
                                        -- -test.run=TestHelperProcess
                               cmd.Stdin defaults to /dev/null      ─>  pyry stdin not a TTY
                                                                        → bridge mode enabled
                                                                        → bootstrap session created
                                                                        → supervisor runOnce
                                                                        → pty.Start(testBin
                                                                            -test.run=TestHelperProcess)   ─> TestHelperProcess sees
                                                                                                                GO_TEST_HELPER_PROCESS=1
                                                                                                                GO_TEST_HELPER_MODE=echo
                                                                                                              → term.MakeRaw(0)  (kills ECHO on
                                                                                                                                  supervisor PTY slave)
                                                                                                              → io.Copy(stdout, stdin)
                               poll: net.Dial(sock) ok?
                               → ready

                               master, slave, _ := pty.Open()
                               attachCmd := exec.Command(
                                 binPath, "attach",
                                 "-pyry-socket="+sock)
                               attachCmd.Stdin  = slave
                               attachCmd.Stdout = slave
                               attachCmd.Stderr = slave
                               attachCmd.Start()                    ─>  attach client:
                                                                        IsTerminal(0) == true (slave is a TTY)
                                                                        term.MakeRaw(slave fd) (kills ECHO on slave too)
                                                                        json handshake on socket
                                                                        Attach OK
                                                                        bridge connected to bootstrap session

master.Write("ping\n")      ────────────────────────────────────────>   attach reads slave (raw)
                                                                        forwards to socket          ─────>   bridge.Read(p) blocks on pipe
                                                                                                              receives "ping\n", writes to ptmx
                                                                                                              kernel writes to slave (ECHO off)
                                                                                                              helper reads from stdin (raw, no ECHO)
                                                                                                              io.Copy writes "ping\n" to stdout
                                                                                                              kernel sends to supervisor PTY master
                                                                        bridge writes outbound      <─────   io.Copy(bridge, ptmx) drains
master.Read(buf)            <────────────────────────────────────────   attach writes to slave
assert: buf contains "ping\n"

t.Cleanup runs:
  master.Close
  slave.Close   (SIGHUP to attach via slave EOF)
  killSpawned(attachCmd)  (SIGTERM, grace, SIGKILL)
  killSpawned(daemonCmd)  (SIGTERM, grace, SIGKILL)
  remove socket
  if origTerm != nil: term.Restore(stdinFd, origTerm)  (defensive)
```

## Package structure

Two new files. No edits to existing files.

### NEW `internal/e2e/attach_pty.go`

```go
//go:build e2e || e2e_install

package e2e

// Build tag matches harness.go so the new code is gated identically.
```

Approximate shape (~110 LOC excluding comments):

```go
package e2e

import (
    "bytes"
    "context"
    "fmt"
    "net"
    "os"
    "os/exec"
    "path/filepath"
    "sync"
    "testing"
    "time"

    "github.com/creack/pty"
    "golang.org/x/term"
)

// AttachHarness owns one running pyry daemon configured for interactive
// attach (bridge mode, test-binary-as-claude in echo mode), the master
// side of a creack/pty pair, and the running `pyry attach` subprocess.
//
// Returned by StartAttach; cleanup is registered via t.Cleanup.
type AttachHarness struct {
    // Master is the PTY master. Tests write input bytes here and read
    // output bytes back. Closed by cleanup.
    Master *os.File

    // SocketPath is the daemon's control socket. Exposed for follow-up
    // tickets that may want to drive additional CLI verbs against the
    // same daemon (e.g. #127 sends `pyry attach` a second time to test
    // ErrBridgeBusy).
    SocketPath string

    // HomeDir is the daemon's $HOME (a fresh t.TempDir).
    HomeDir string

    daemonCmd  *exec.Cmd
    daemonDone chan struct{}
    daemonOut  *bytes.Buffer
    daemonErr  *bytes.Buffer

    attachCmd  *exec.Cmd
    attachDone chan struct{}

    slave    *os.File   // closed during cleanup before SIGTERM to attach
    origTerm *term.State // parent test stdin state, restored defensively

    cleanupOnce sync.Once
}

// StartAttach spawns a pyry daemon in bridge mode whose supervised
// claude is the e2e test binary running TestHelperProcess in echo mode.
// It opens a creack/pty pair, spawns `pyry attach` with the slave on
// stdio, and waits for the attach handshake to complete (best-effort —
// see "Readiness" below). The returned harness exposes Master for the
// test to write/read.
//
// sessionID selects the session to attach to. Empty means "bootstrap"
// (the only session this harness creates).
//
// Skips the test (t.Skip) if pty.Open fails — some hosts (sandboxed CI,
// minimal containers) lack a usable /dev/ptmx. Fails the test on any
// other startup error.
func StartAttach(t *testing.T, sessionID string) *AttachHarness { ... }

// internal: spawnAttachableDaemon mirrors harness.spawn but with a
// custom claude (os.Args[0] -test.run=TestHelperProcess), helper-env
// injection (GO_TEST_HELPER_PROCESS=1, GO_TEST_HELPER_MODE=echo), and
// no -- 99999 sentinel.
func spawnAttachableDaemon(t *testing.T, home string) (
    socket string,
    cmd *exec.Cmd,
    out, err *bytes.Buffer,
    doneCh chan struct{},
) { ... }

// internal: waitDaemonReady polls the socket like harness.waitForReady.
func waitDaemonReady(socket string, doneCh chan struct{}, errBuf *bytes.Buffer) error { ... }

// teardown closes master/slave (SIGHUP attach via slave EOF), then
// SIGTERM-grace-SIGKILLs both the attach client and the daemon, then
// removes the socket. Idempotent via sync.Once.
func (a *AttachHarness) teardown(t *testing.T) { ... }
```

### Why a single helper test binary, not a separate `package main`

Ticket #122 used a standalone `package main` because the rotation binary opens fds and sleeps — work that doesn't fit the test-binary `if os.Getenv("…") != "1" { return }` shape, and that benefits from a stable build target the harness can hand a path to.

Echo is `term.MakeRaw(0); io.Copy(os.Stdout, os.Stdin)`. The test-binary re-exec pattern (already proven in `internal/supervisor/supervisor_test.go`) is the simplest fit: no second binary to build, no second file under `internal/e2e/internal/`, no need for the sync.Once `go build` cache. `os.Args[0]` is the path of the e2e test binary that go-test already built; pyry execs it with `-test.run=TestHelperProcess`, the env-var gate runs the helper, and the test framework otherwise treats it as a normal test that just happens to never run during a default invocation.

### NEW `internal/e2e/attach_pty_test.go`

```go
//go:build e2e
```

The test file gates on `e2e` only (not `e2e_install`) — `e2e_install` is reserved for installer-only test variants and adding the round-trip test under it would compile dead weight on every install run.

Approximate shape (~80 LOC):

```go
package e2e

import (
    "io"
    "os"
    "regexp"
    "testing"
    "time"

    "golang.org/x/term"
)

// TestHelperProcess is not a real test. It is re-execed by pyry as the
// supervised "claude" when the e2e attach harness is configured:
//
//   GO_TEST_HELPER_PROCESS=1
//   GO_TEST_HELPER_MODE=echo
//
// Mode "echo": disable ECHO/ICANON on stdin (raw mode) so the kernel
// does not double-emit bytes through the supervisor's PTY line
// discipline, then io.Copy stdin → stdout until stdin closes.
//
// Process exit on stdin EOF (pyry SIGTERMs the child during teardown,
// closing the supervisor's PTY master, which the kernel surfaces as
// EOF on the slave).
func TestHelperProcess(t *testing.T) {
    if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
        return
    }
    mode := os.Getenv("GO_TEST_HELPER_MODE")
    switch mode {
    case "echo":
        // Killing ECHO on the supervisor's slave prevents the kernel
        // from reflecting input back to the master before we copy it
        // to stdout. Without this the test sees each byte twice.
        if term.IsTerminal(int(os.Stdin.Fd())) {
            if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
                os.Exit(98)
            }
        }
        _, _ = io.Copy(os.Stdout, os.Stdin)
        os.Exit(0)
    default:
        os.Exit(99)
    }
}

func TestE2E_Attach_RoundTripsBytes(t *testing.T) {
    a := StartAttach(t, "")

    payload := []byte("pyry-attach-roundtrip-" + tinyNonce() + "\n")

    if _, err := a.Master.Write(payload); err != nil {
        t.Fatalf("write master: %v", err)
    }

    // Read with a generous deadline. Bytes are NOT guaranteed to
    // arrive in one Read; loop until we've seen the full payload or
    // the deadline elapses.
    if err := readUntilContains(a.Master, payload, 5*time.Second); err != nil {
        t.Fatalf("did not observe payload back: %v", err)
    }
}

// tinyNonce returns a short random-ish string so concurrent test
// invocations don't accidentally match each other's payloads if
// streams ever cross-contaminate. Not a security property — just
// debug clarity.
func tinyNonce() string { ... }

// readUntilContains reads from r in a loop into a growing buffer
// (with a per-read SetReadDeadline) until needle appears or the
// overall deadline elapses. Implemented in the harness file or
// inline — developer's call.
func readUntilContains(r *os.File, needle []byte, total time.Duration) error { ... }
```

(Sketches only — actual byte counts and helper boundaries are the developer's call.)

## Concurrency model

**Daemon:** unchanged from existing harness — pyry forked with `cmd.Start`, a goroutine drains `cmd.Wait` into `daemonDone` so readiness polling can short-circuit on early exit. Stdout/stderr captured to buffers for failure diagnostics.

**Attach client:** same shape — `exec.Cmd.Start`, single goroutine on `cmd.Wait` into `attachDone`. The test does not need the attach client's stdout/stderr in normal flow (they go to the slave PTY and are read via Master); but errors from a failed attach handshake go to the client's *own* stderr before raw mode is set, so the harness redirects the client's `Stderr` to a separate buffer **before** the slave handoff would matter — actually no: the attach client writes to `os.Stderr` (e.g. "pyry: attached. Press Ctrl-B d to detach.") and that *is* the slave PTY in our setup, which the test reads via Master. The "attached" banner will appear in the master read stream. The test must handle this — either skip past it before asserting on the payload, or include the banner in the search-for-needle loop. The simplest approach: `readUntilContains` looks for the *payload* needle; banner bytes that arrive first are swallowed by the buffer.

**Test goroutines:** none beyond the implicit `cmd.Wait` drainers in the harness. The test writes synchronously, reads synchronously with a deadline. No channels, no select.

**Cleanup ordering** (load-bearing — get this wrong and SIGHUPs cascade through the wrong end):

1. `master.Close()` — flushes pending master-side writes; the kernel still holds the slave open via the attach process's fds.
2. `slave.Close()` — the harness's own slave fd. The attach process still has its own dup'd copies via `attachCmd.Stdin/Stdout/Stderr`; closing the harness's fd here does not deliver SIGHUP yet.
3. `killSpawned(attachCmd)` — SIGTERM the attach client. The client's deferred `term.Restore` runs (no-op on a slave PTY about to be torn down, but defensive). Wait on `attachDone`.
4. `killSpawned(daemonCmd)` — SIGTERM the daemon. Pyry's signal handler tears down the supervisor, which SIGKILLs the helper child (it ignores SIGTERM-on-stdin EOF; Go's default is fine for the helper since it exits on stdin EOF). Wait on `daemonDone`.
5. `os.Remove(socketPath)` — defensive; pyry removes it on clean shutdown but SIGKILL paths skip cleanup.
6. `term.Restore(stdinFd, origTerm)` if `origTerm != nil` — the parent test process's terminal was snapshotted (defensively) before any PTY work; restore now even though no code path in this harness should have modified it. AC#4 calls for this explicitly.

## Error handling

**Daemon spawn failure:** `t.Fatalf` with stderr buffer dumped. Same shape as existing harness.

**`pty.Open` failure:** `t.Skip` with a clear reason: `"e2e: pty.Open unavailable on this host: <err>"`. AC#5 requires this. Most failure modes are `permission denied on /dev/ptmx` (sandbox), `ENOENT on /dev/pts` (minimal container).

**Daemon-not-ready before deadline:** `t.Fatalf` with daemon stderr buffer. Surfaces helper-binary mis-config (e.g. wrong os.Args[0] path) as a daemon startup failure.

**Attach client fails handshake:** the client exits non-zero before any bytes flow. The harness detects this by polling `attachDone` after the slave handoff; if the client exited within ~1 second (a configurable but small constant), `t.Fatalf` with the captured exit reason. Otherwise, proceed — the client is alive and connected.

**Cleanup partial failure (e.g. one sub-process won't die after SIGKILL):** `t.Logf` only, never `t.Fatal` from a `t.Cleanup`. Same discipline as `harness.teardown`. A leaked pyry process in a t.TempDir HOME has a few seconds of liveness then exits on its own (idle eviction is disabled but the test process exit closes the socket — pyry's existing teardown handles that).

**Helper-binary `term.MakeRaw` failure:** the helper exits 98. The supervisor's runOnce sees the early child exit, applies backoff, restarts — and on this host the helper will fail the same way. The test will time out waiting for echo bytes and fail with the `readUntilContains` deadline error. That's the right surface: a non-PTY-capable host should have skipped via `pty.Open` already; reaching here means a deeper environment problem, and the timeout error message tells the developer to look at daemon stderr (which will show repeated child exits).

# Testing strategy

The harness has no dedicated unit tests — it exists to serve `TestE2E_Attach_RoundTripsBytes`, and that test is its acceptance. Properties exercised:

1. **Harness builds, daemon comes up in bridge mode** — implicit: if the daemon isn't bridge-mode, the attach handshake fails.
2. **Helper binary is dispatched correctly** — implicit: if `-pyry-claude=os.Args[0]` or env-var injection breaks, the supervisor restarts the child indefinitely and no echo flows; test times out with daemon stderr in the failure message.
3. **Slave PTY ↔ attach client interaction works** — the round-trip succeeds.
4. **Master PTY ↔ test process interaction works** — the test reads bytes back.
5. **Cleanup is reliable on success path** — `t.Cleanup` runs every time; manual run with `-count=10` should not accumulate leaked processes (developer verifies during implementation).

What this slice does **not** verify:

- SIGWINCH propagation (#126).
- Clean detach via Ctrl-B d (#127).
- Restart survival across daemon SIGTERM/respawn (#128).
- Per-session attach exclusivity (`ErrBridgeBusy`) — out of scope per ticket body; arrives with #127 if at all.
- That the helper binary's MakeRaw failure path is recoverable (it is not, by design).

## Skip-on-no-PTY

`AC#5` requires the test to skip on hosts without a usable PTY. The cleanest gate is `pty.Open()` itself: it exercises `/dev/ptmx` directly and surfaces the host's posture. Place the `pty.Open` call inside `StartAttach` *before* spawning the daemon — a clean `t.Skip` is faster than spawning pyry, racing readiness, then teardown.

```go
master, slave, err := pty.Open()
if err != nil {
    t.Skipf("e2e: pty.Open unavailable: %v", err)
}
```

GitHub Actions `ubuntu-latest` (per `docs/lessons.md` § "PTY Testing") supports `pty.Open` even though it lacks a controlling terminal. The skip path is for genuinely PTY-less hosts (sandboxed CI, minimal containers).

# What this spec does not solve

- **No reuse with the existing fakeclaude binary (#122).** The fakeclaude binary opens JSONL fds and rotates them on a trigger. Echoing bytes is a different shape; conflating modes onto one binary saves no code and obscures intent. Two binaries (one Go-built per fakeclaude usage, one TestHelperProcess re-exec) coexist cleanly.
- **No refactor of `harness.go::spawn` to take options.** The new harness has its own ~30-line `spawnAttachableDaemon` rather than parameterizing the existing one. The two functions diverge on three things (claude bin, claude args, helper env), and a bool/options parameter would be more code than the duplication. Revisit if a third spawn variant lands.
- **No status check before attach.** The harness polls socket dialability (matches existing `waitForReady`) and assumes the bootstrap session's bridge is wired by the time attach runs. If this proves flaky in practice (rare, but possible if `pyry sessions` activate races attach handshake), the fix is to add a brief `pyry status`-poll in the harness — not to architect around it now.

# Open questions

- **Does the attach client's "pyry: attached. Press Ctrl-B d to detach." banner reach the master before our payload echo?** Almost certainly yes — it's printed before raw-mode and bridge handoff complete. The `readUntilContains` loop swallows pre-payload bytes, so this is harmless, but if a future test wants byte-for-byte equality it must skip the banner explicitly. The simpler alternative is to invoke the client with `2>/dev/null` redirection — but stderr is the slave PTY here, and splitting it requires extra fd plumbing the round-trip test does not need. Defer.
- **Should the harness expose `attachCmd.ExitCode()` to the test for follow-up tickets?** #127 (clean detach) cares about the attach client's exit code (0 on Ctrl-B d). Add the field when #127 needs it, not now.
- **Is `t.TempDir` for HOME sufficient on macOS?** macOS sandboxing has historically intermittently rejected long t.TempDir paths (which include `/var/folders/...` segments). Existing `e2e.Start` uses `t.TempDir` without issue, and this harness reuses the same pattern. Revisit only if a flake surfaces.

# Out of scope

- The follow-up tests #126 (SIGWINCH), #127 (clean detach + ErrBridgeBusy), #128 (restart survival).
- A real claude binary in the loop.
- Foreground-mode supervisor coverage.
- Multi-attach behaviour.
