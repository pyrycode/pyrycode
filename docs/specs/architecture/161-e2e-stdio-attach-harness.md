---
ticket: 161
title: stdio-attach test harness + byte-flow proof-of-life
status: spec
size: S
---

# Files to read first

Read these before doing any exploration of your own — they are the load-bearing
surfaces this spec composes.

- `internal/e2e/attach_pty.go` — full file (~275 lines). The PTY-mode sibling.
  Reuses three private helpers the new harness also calls:
  - `attach_pty.go:156-196` — `spawnAttachableDaemon` spawns pyry with the
    e2e test binary as the supervised "claude" in echo mode
    (`GO_TEST_HELPER_PROCESS=1`, `GO_TEST_HELPER_MODE=echo`,
    `-pyry-resume=false`, no sleep sentinel). **Reuse verbatim.**
  - `attach_pty.go:200-217` — `waitDaemonReady` (socket-dial poll with daemon
    early-exit short-circuit). **Reuse verbatim.**
  - `attach_pty.go:255-274` — `teardown` shape (sync.Once-wrapped, ordered
    SIGTERM→grace→SIGKILL, defensive socket removal). The new harness
    follows the same discipline minus the PTY-master/slave close pair.
- `internal/e2e/attach_pty_test.go:21-133` — the existing `TestHelperProcess`
  echo mode. **Reuse as-is.** It is line-buffered, intercepts `__EXIT__` and
  `__PID__`, and emits a `PYRY_E2E_STARTED pid=<pid>\n` banner on startup.
  The new test file has build tag `e2e` and lives in the same package, so
  both test files compile into the same test binary and share the helper
  — do **not** define a second `TestHelperProcess`.
- `internal/control/attach_stdio_client.go` — full file (~80 lines). The
  client surface this harness drives via `pyry attach --stdio`. Confirms:
  (a) no `term.MakeRaw`, no SIGWINCH watcher, no escape detection — bytes
  pass through `io.Copy` in both directions; (b) `in` EOF returns nil
  (clean detach without destroying the session); (c) the output goroutine
  is joined before return (the harness can rely on attach-client exit
  meaning "all server-emitted bytes flushed to the parent's pipe").
- `cmd/pyry/main.go:438-522` — `parseAttachArgs` + `runAttach` `--stdio`
  dispatch. Confirms the CLI shape the harness invokes:
  `pyry attach -pyry-socket=<sock> --stdio <session-id>`. Stderr noise
  ("pyry: attached…", "pyry: detached.") is suppressed in `--stdio` mode,
  so capturing stderr to a buffer for diagnostics yields a clean stream.
- `internal/e2e/sessions_rm_test.go:31-61` — pattern reference for "create
  a session via `control.SessionsNew`, then drive a CLI verb against it".
  The new test mirrors this shape: `home, _ := newRegistryHome(t)` → spawn
  daemon → `control.SessionsNew(ctx, h.SocketPath, "<label>")` → spawn
  attach → assert. The ticket's AC says "create session via
  `pyry sessions new`" — using the in-process `control.SessionsNew` client
  satisfies the same wire-level contract without a second `exec.Command`,
  matching every other e2e test in this package (`cap_test.go`,
  `sessions_list_test.go`, `sessions_rm_test.go`, `idle_test.go`).
- `internal/e2e/cap_test.go:26-35` — `writeSleepClaude` is **not** used by
  this harness. The supervised claude here is the e2e test binary running
  TestHelperProcess in echo mode, not /bin/sleep. The reference is
  defensive: a developer skimming the e2e package may assume sleep-claude
  is the standard; for stdio-attach round-trip, the helper-as-claude
  shape (already in `spawnAttachableDaemon`) is the only one that echoes
  bytes back.
- `docs/specs/architecture/154-attach-stdio-mode.md` — the spec for the
  `pyry attach --stdio` surface this ticket exercises end-to-end.
  Particularly its "Concurrency model" section, which establishes the
  invariants this test verifies at the binary boundary: stdin EOF →
  clean detach → exit 0; bytes pass through unfiltered; goroutine joined
  before return.
- `docs/specs/architecture/125-e2e-attach-pty-harness.md` — the precedent
  this ticket mirrors in shape ("intentionally minimal harness + one
  proof-of-life test"). Particularly its "Out of scope" framing — the
  follow-on coverage tickets (#162 no-PTY-fd assertion; 1.3c-2 foreground
  auto-attach) reuse the harness this ticket lands.

# Context

## Why this slice exists

#154 landed `pyry attach --stdio` for SDK consumers. Unit tests (in
`internal/control/attach_stdio_client_test.go`) cover the wire protocol
and the byte-bridging primitive against fake servers; the existing
`internal/e2e` package has zero coverage of `--stdio` because the only
attach harness (`attach_pty.go`) allocates a PTY and uses the PTY-mode
attach client.

There is no test that proves a byte written to a programmatic parent's
stdout pipe travels: parent → `pyry attach --stdio` stdin pipe → control
socket → bridge → supervisor PTY → claude → and back through the
`pyry attach --stdio` stdout pipe → parent's stdin pipe. This is the
exact byte path Claudian (Obsidian + `@anthropic-ai/claude-agent-sdk`)
will exercise in production: SDK `spawn()` over plain pipes, no PTY
anywhere on the client side.

This ticket lands the harness alongside its first consumer test — the
smallest meaningful proof that the no-PTY shape works end-to-end. Two
follow-on tickets (1.3a-e2e-no-pty: assert no PTY fds open in the
attach client; 1.3c-2-e2e-*: foreground auto-attach) reuse this same
harness and are out of scope here, mirroring the #125 / #126 / #127 /
#128 staging.

## Why the harness lives in `internal/e2e/` not `internal/control/`

`internal/control/attach_stdio_client_test.go` already covers the
client function with fake servers. The gap is at the **binary**
boundary: `pyry attach --stdio` as a child process, with real stdin /
stdout pipes, against a real daemon. That is what `internal/e2e/`
exists for — it owns the daemon-spawn + child-spawn + capture
machinery (`ensurePyryBuilt`, `childEnv`, `killSpawned`,
`spawnAttachableDaemon`, `waitDaemonReady`).

## What "no PTY anywhere" means here

- The harness does **not** call `pty.Open()`. No `creack/pty` import.
- The attach client's stdin / stdout / stderr are bound to plain
  `os.Pipe()` ends. The pipes are **not** TTYs — `term.IsTerminal(fd)`
  returns false for both ends.
- The attach client runs in `--stdio` mode, which has no
  `IsTerminal` branch (see `attach_stdio_client.go:26-27` godoc): it
  forwards bytes via straight `io.Copy` regardless of stdio shape.
- The supervised claude on the daemon side **does** still run under a
  PTY — that's owned by the supervisor and unchanged by this ticket.
  The bridge bridges the supervisor's PTY master to the attach client's
  socket conn either way; what's PTY-less here is the client → user
  hop, which is exactly what `--stdio` was built for.

# Design

## Approach

Two new files. No edits to existing files.

The harness reuses `spawnAttachableDaemon` and `waitDaemonReady` from
`attach_pty.go` to bring up a daemon whose supervised claude is the
e2e test binary running `TestHelperProcess` in echo mode. It then:

1. Creates a session via `control.SessionsNew(ctx, h.SocketPath, "<label>")`
   — an in-process wire call, same shape as every other e2e test in the
   package.
2. Allocates two `os.Pipe()` pairs: one for the attach client's stdin
   (test → child), one for stdout (child → test).
3. Spawns `pyry attach -pyry-socket=<sock> --stdio <id>` with stdin
   bound to the input pipe's read end and stdout bound to the output
   pipe's write end. Stderr captured to a `*bytes.Buffer` for failure
   diagnostics.
4. Closes the parent's copies of `inputR` and `outputW` after Start so
   that EOF on `outputR` corresponds to child-stdout-close (not a
   reference held by the parent), and so that closing `inputW` from
   the test propagates EOF to the child's stdin.
5. Returns a `*StdioAttachClient` whose methods write to `inputW` and
   read from `outputR`.

The supervised helper's echo mode is line-buffered (it emits each `\n`
-terminated line as one write). The round-trip test writes one
`<nonce>\n` line and reads until that line appears in the accumulated
output, swallowing the helper's pre-attach `PYRY_E2E_STARTED pid=…\n`
banner along the way (same pattern `attach_pty_test.go:170-203` uses).

## End-to-end trajectory

```
test                         harness                            daemon (pyry, bridge mode)             supervised helper (test bin, echo mode)
─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
startStdioAttach(t, "label") ─> ensurePyryBuilt
                                home := newRegistryHome(t)
                                socket, daemonCmd, …  := spawnAttachableDaemon(t, home)
                                                                ─> pyry stdin = /dev/null (not a TTY)
                                                                   bridge mode enabled
                                                                   bootstrap session created
                                                                   supervisor.runOnce
                                                                   pty.Start(testBin -test.run=TestHelperProcess)
                                                                                                       ─> emits "PYRY_E2E_STARTED pid=<pid>\n"
                                                                                                          (lost to /dev/null until attach)
                                waitDaemonReady(socket, …)
                                ctx, cancel := context.WithTimeout(... 5s)
                                id, err := control.SessionsNew(ctx, socket, "<label>")
                                                                ─> sessions.new wire verb:
                                                                   spawn second helper for new session
                                                                                                       ─> emits banner (same)

                                inputR, inputW   := os.Pipe()
                                outputR, outputW := os.Pipe()
                                stderrBuf        := &bytes.Buffer{}

                                attachCmd := exec.Command(bin,
                                  "attach",
                                  "-pyry-socket="+socket,
                                  "--stdio", id)
                                attachCmd.Stdin  = inputR
                                attachCmd.Stdout = outputW
                                attachCmd.Stderr = stderrBuf
                                attachCmd.Env    = childEnv(home)
                                attachCmd.Start()                ─> attach client:
                                                                   IsTerminal(0)/IsTerminal(1) == false
                                                                   AttachStdio(ctx, sock, id, os.Stdin, os.Stdout)
                                                                   handshake: VerbAttach{SessionID:id, Cols:0, Rows:0}
                                                                   server: handleAttach → bridge.Attach
                                _ = inputR.Close()                  (parent drops its dup so EOF flows from inputW alone)
                                _ = outputW.Close()                 (parent drops its dup so EOF on outputR == child closed stdout)

                                // Detect early-exit handshake failure
                                go cmd.Wait → close(attachDone)
                                select { <-attachDone (within 500ms) → t.Fatalf
                                       ; <-time.After(500ms)         → continue }

                                return *StdioAttachClient{ inputW, outputR, … }

c.Write([]byte("ping-X\n"))  ────────────────────────────────────>  attach reads inputR
                                                                    io.Copy(conn, in) writes "ping-X\n" to socket
                                                                                                       <─ helper reads, accumulates until "\n",
                                                                                                          writes line to stdout
                                                                    io.Copy(out, conn) writes to outputW       ─>
c.ReadUntil([]byte("ping-X\n"), 5s)
                             <───────────────────────────────────────────────────────────────────────────  outputR.Read drains the line
                                (and any prior banner bytes)

c.Close() (or t.Cleanup):
                                inputW.Close()  ─> child stdin EOF ─> AttachStdio in-loop returns ─> conn.Close ─> server bridge detach
                                                                      (session stays alive — lazy-eviction)
                                                                      AttachStdio joins output goroutine, returns nil
                                                                      runAttach prints nothing (--stdio suppresses), exit 0
                                <-attachDone (within ~2s); else killSpawned(attachCmd)
                                outputR.Close()
                                killSpawned(daemonCmd)
                                os.Remove(socket)
```

## Package structure

Two new files. No edits to existing files.

### NEW `internal/e2e/attach_stdio.go`

```go
//go:build e2e
```

The build tag is `e2e` only — the PTY harness uses `e2e || e2e_install`
for installer-test reuse, but the stdio harness has no installer-test
consumer (the installer doesn't ship `pyry attach --stdio` semantics
beyond the binary itself, and the existing installer tests already
cover binary presence). Keeping the gate to `e2e` matches the test
file's gate and avoids compiling dead weight on installer runs.

Approximate shape (~110 LOC excluding comments):

```go
package e2e

import (
    "bytes"
    "context"
    "fmt"
    "os"
    "os/exec"
    "sync"
    "testing"
    "time"

    "github.com/pyrycode/pyrycode/internal/control"
)

// StdioAttachClient is a programmatic peer for `pyry attach --stdio`,
// wired via plain os.Pipe() — no PTY, no terminal, no raw mode. Tests
// drive the supervised session by writing to the client's stdin pipe
// (Write) and reading from its stdout pipe (ReadUntil / Read). Close
// closes the stdin pipe, which propagates EOF through the attach
// client and ends the attach cleanly without destroying the session.
//
// Returned by startStdioAttach. Cleanup is registered via t.Cleanup
// at construction.
type StdioAttachClient struct {
    // SessionID is the id of the session this client is attached to,
    // as returned by control.SessionsNew. Exposed for diagnostics and
    // for tests that want to drive other CLI verbs against the same
    // session.
    SessionID string

    // SocketPath / HomeDir mirror the daemon harness fields.
    SocketPath string
    HomeDir    string

    // Stderr captures the attach client's stderr. Empty in steady
    // state — `--stdio` mode suppresses pyry's own stderr noise — so
    // any content here is a failure diagnostic.
    Stderr *bytes.Buffer

    inputW  *os.File   // parent's write end of attach client's stdin
    outputR *os.File   // parent's read end of attach client's stdout

    daemonCmd  *exec.Cmd
    daemonDone chan struct{}
    daemonErr  *bytes.Buffer

    attachCmd  *exec.Cmd
    attachDone chan struct{}

    cleanupOnce sync.Once
}

// startStdioAttach brings up a pyry daemon in bridge mode (helper-as-
// claude in echo mode), creates a fresh session via control.SessionsNew
// with the given label, and spawns `pyry attach --stdio <id>` whose
// stdin and stdout are wired to plain os.Pipe()s. Returns a
// StdioAttachClient the test uses to write/read bytes.
//
// label is the human-facing session label passed to sessions.new. Pass
// "" for unlabeled (the server treats nil and empty-Label identically).
//
// Skips the test (t.Skip) if os.Pipe() fails — extremely rare, only
// observed in heavily sandboxed containers without /dev/null-style fd
// allocation. Fails the test on any other startup error (daemon spawn,
// readiness timeout, sessions.new wire error, attach client spawn,
// attach client early exit).
func startStdioAttach(t *testing.T, label string) *StdioAttachClient { ... }

// Write writes b to the attach client's stdin pipe. Returns the
// underlying os.File.Write result; callers typically expect len(b),
// nil on a healthy attach.
func (c *StdioAttachClient) Write(b []byte) (int, error) { ... }

// ReadUntil reads from the attach client's stdout pipe in a loop,
// accumulating bytes, until needle appears in the accumulated buffer
// or the overall deadline elapses. The accumulated bytes (including
// any pre-needle banner emitted by the helper) are visible in the
// returned buffer regardless of outcome.
//
// Mirrors readUntilContains in attach_pty_test.go (which os.Pipe ends
// share the no-SetReadDeadline trait with PTY masters on darwin —
// the deadline is enforced via a select against a background read
// goroutine).
func (c *StdioAttachClient) ReadUntil(needle []byte, total time.Duration) ([]byte, error) { ... }

// Close closes the attach client's stdin pipe (delivering EOF to the
// child's AttachStdio input loop), waits up to ~2s for the child to
// exit cleanly, and returns its exit code. Subsequent calls return
// the same exit code from a cached ProcessState.
//
// Idempotent. Also called from t.Cleanup; whichever fires first wins.
func (c *StdioAttachClient) Close(t *testing.T) int { ... }

// teardown is the sync.Once-wrapped cleanup body. Ordering:
//   1. inputW.Close()       — sends EOF to child stdin → clean detach
//   2. wait attachDone (≤ ~2s) → killSpawned(attachCmd) on timeout
//   3. outputR.Close()      — unblocks any in-flight ReadUntil
//   4. killSpawned(daemonCmd)
//   5. os.Remove(socketPath)
func (c *StdioAttachClient) teardown(t *testing.T) { ... }
```

### NEW `internal/e2e/attach_stdio_test.go`

```go
//go:build e2e
```

Approximate shape (~40 LOC):

```go
package e2e

import (
    "bytes"
    "testing"
    "time"
)

// TestE2E_AttachStdio_BytesRoundTrip proves that a byte written into a
// programmatic parent's stdin-of-`pyry attach --stdio` travels: parent
// pipe → attach client → control socket → bridge → supervisor PTY →
// supervised helper → and back through the attach client's stdout into
// the parent's read pipe. The helper is the e2e test binary running
// TestHelperProcess in echo mode (line-buffered). The test writes one
// nonce-tagged line and asserts the same line appears on output within
// a generous deadline; pre-line banner bytes (the helper's startup
// "PYRY_E2E_STARTED pid=…\n") are accumulated and ignored, identical
// to the PTY harness's readUntilContains discipline.
//
// This test is the smallest meaningful exercise of the stdio-attach
// shape end-to-end. Follow-up tickets (1.3a-e2e-no-pty: assert no PTY
// fd open in the client; 1.3c-2-e2e-*: foreground auto-attach) reuse
// startStdioAttach for their own scenarios.
func TestE2E_AttachStdio_BytesRoundTrip(t *testing.T) {
    c := startStdioAttach(t, "stdio-roundtrip")

    // Trailing \n is required: the helper's echo mode is line-buffered
    // and only flushes a line when it sees \n.
    payload := []byte("pyry-stdio-roundtrip-" + tinyNonce() + "\n")

    if _, err := c.Write(payload); err != nil {
        t.Fatalf("write: %v", err)
    }

    seen, err := c.ReadUntil(payload, 5*time.Second)
    if err != nil {
        t.Fatalf("did not observe payload back: %v\nstderr:\n%s",
            err, c.Stderr.String())
    }

    // Defensive: the needle must appear *after* any banner. The
    // ReadUntil contract already guarantees needle ∈ seen on nil err,
    // so this is a smoke check that the buffer wasn't returned empty
    // by an unlikely API regression.
    if !bytes.Contains(seen, payload) {
        t.Fatalf("ReadUntil returned without payload in buffer: %q", seen)
    }
}
```

`tinyNonce` already exists in `attach_pty_test.go` (same package, same
build tag) and is reused as-is.

## Concurrency model

- **Daemon process**: spawned by `spawnAttachableDaemon`, monitored by
  the existing single-goroutine `cmd.Wait → close(daemonDone)` pattern
  inside that function. No change.

- **Attach client process**: spawned by the new harness with one
  `cmd.Wait → close(attachDone)` goroutine. Same shape as the PTY
  harness's `attach_pty.go:127-131`.

- **Read goroutine inside ReadUntil**: a single short-lived goroutine
  per outstanding read, identical to `readUntilContains` in
  `attach_pty_test.go:170-203`. The goroutine reads into a 4096-byte
  buffer and pushes the result onto a `chan readResult`; the caller
  selects between the channel and a deadline. No deadline is set on
  the file (os.Pipe ends on darwin do not support SetReadDeadline,
  per `attach_pty_test.go:165-168`'s comment).

- **Test goroutines**: none beyond the implicit `cmd.Wait` drainer and
  the per-call read goroutine. The test writes synchronously, reads
  with a deadline, asserts.

- **No channels-with-cancel.** The conn-as-rendezvous-point discipline
  from `AttachStdio` (closing inputW is the wake-up for the attach
  client's input copy) carries through here: closing inputW from the
  test process unblocks the child's `io.Copy(conn, in)` and ends the
  attach.

## Cleanup ordering

Load-bearing — get this wrong and EOF / SIGHUP cascades through the
wrong end. The harness's `teardown` runs (via `t.Cleanup` and / or an
explicit `Close`) once, in this order:

1. **`inputW.Close()`** — propagates EOF through the kernel pipe to
   the attach client's stdin. The client's `AttachStdio` input loop
   returns nil, the conn closes, the output goroutine joins, and
   `pyry attach --stdio` exits 0. Clean-detach contract.

2. **Wait on `attachDone` with a short timeout (~2s)**. On clean detach
   the child exits within milliseconds. If it doesn't, `killSpawned`
   escalates SIGTERM → grace → SIGKILL (existing helper).

3. **`outputR.Close()`** — releases any in-flight ReadUntil that's
   blocked on `outputR.Read`. Done after the child has exited so we
   don't race the child's stdout writes.

4. **`killSpawned(daemonCmd)`** — SIGTERM the daemon. Pyry's signal
   handler tears down the supervisor, which SIGKILLs the helper child.
   Wait on `daemonDone`.

5. **`os.Remove(socketPath)`** — defensive; pyry removes the socket on
   clean shutdown, but a SIGKILL path (step 4 escalation) skips that.

Wrapped in `sync.Once` so an explicit `c.Close(t)` from the test body
plus the `t.Cleanup` registered at construction don't double-fire.

## Error handling

| Failure | Behaviour |
|---|---|
| `os.Pipe()` returns error | `t.Skipf("e2e: os.Pipe unavailable: %v", err)`. AC#3 — same gating shape as #125's `pty.Open` skip. Realistic only in heavily sandboxed containers; mainline CI has plenty of fd headroom. |
| `spawnAttachableDaemon` failure | `t.Fatalf` (existing helper already does this). |
| `waitDaemonReady` timeout / early exit | `t.Fatalf` with daemon stderr. |
| `control.SessionsNew` error | `t.Fatalf` with the wrapped error. Includes wire errors (`ErrorCode`-bearing responses) and dial / decode failures. |
| `attachCmd.Start()` error | `t.Fatalf` — fork failure is environmental, not a test bug. |
| Attach client exits within 500ms of start (handshake failure) | `t.Fatalf` with attach client's exit code and the captured `Stderr` buffer (e.g. "attach: no attach provider configured…" if the daemon somehow landed in foreground mode, or a wire-decode error if pyry's protocol shifted). The PTY harness uses the same pattern at `attach_pty.go:137-146`. |
| `c.Write` error | Surface as the underlying `os.File.Write` error to the caller. The test will `t.Fatalf` on the spot. |
| `c.ReadUntil` deadline | Return a `fmt.Errorf("timeout after %s; seen %d bytes: %q", total, len(seen), seen)`. The test wraps with the captured Stderr in its `t.Fatalf` for diagnostics. |
| Cleanup partial failure | `t.Logf` only, never `t.Fatal` from a `t.Cleanup`. Matches `harness.teardown` discipline. A leaked pyry process in the temp HOME has bounded lifetime — the test process exit closes its socket and idle eviction is disabled, but the daemon's signal handler also fires on the parent's death via SIGHUP. |

The clean-detach contract (`inputW.Close()` → attach client exit 0) is
load-bearing: it's the production shape an SDK consumer will see when
its parent process closes the spawned child's stdin. A non-zero exit
on this path is a real bug — `c.Close(t)` returns the exit code so
follow-up tickets can assert on it.

# Testing strategy

The harness has no dedicated unit tests — it exists to serve
`TestE2E_AttachStdio_BytesRoundTrip`, and that test is its acceptance.
Properties exercised:

1. **Daemon comes up in bridge mode with helper-as-claude.** Implicit:
   if pyry runs in foreground mode or claude is misconfigured, the
   server's `handleAttach` rejects with "no attach provider configured"
   and the harness's early-exit detector surfaces it.
2. **`sessions.new` wire verb works under the helper claude.** Implicit:
   if `SessionsNew` fails (server can't spawn the new session because
   the helper rejects its CLI args), the harness fails before reaching
   attach.
3. **`pyry attach --stdio` handshake completes against a real daemon.**
   Implicit: handshake errors surface in the early-exit window with the
   captured stderr.
4. **Bytes round-trip through pipes (no PTY) end-to-end.** The proof-
   of-life test asserts this directly.
5. **Clean detach via stdin EOF returns exit 0.** Implicit in the
   teardown path; the harness asserts cleanly via `attachDone` waited
   in `Close(t)`. A future ticket can assert exit 0 explicitly when
   the AC calls for it.

What this slice does **not** verify:

- That no PTY fd is open in the attach client (1.3a-e2e-no-pty, #162).
  That ticket will inspect `/proc/<pid>/fd` (Linux) or `lsof -p <pid>`
  (macOS) for the attach client and assert no `/dev/pt[my]/*`-style
  entries.
- Foreground auto-attach scenarios (1.3c-2-e2e-*).
- Server-initiated detach (session evicted from under the client).
- Multi-session attach exclusivity.
- Binary-safe transport for arbitrary byte sequences (NUL bytes, CR
  without LF). The unit tests in
  `internal/control/attach_stdio_client_test.go` already pin this at
  the wire boundary; e2e adds nothing.

## Skip-on-no-pipe

`AC#3` requires the test to skip cleanly on hosts without spawn
capability. `os.Pipe()` is the cleanest gate — it exercises kernel fd
allocation directly. Place the `os.Pipe()` call before
`spawnAttachableDaemon` so a clean `t.Skip` is faster than spawning
pyry and tearing it down. In practice this skip never fires on the
project's CI matrix; it exists as a defense against future changes
that drop the e2e suite into a more restrictive environment.

```go
inputR, inputW, err := os.Pipe()
if err != nil {
    t.Skipf("e2e: os.Pipe unavailable: %v", err)
}
outputR, outputW, err := os.Pipe()
if err != nil {
    _ = inputR.Close()
    _ = inputW.Close()
    t.Skipf("e2e: os.Pipe unavailable: %v", err)
}
```

# What this spec does not solve

- **No reuse of an "attach harness" abstraction across PTY and stdio
  modes.** The two harnesses share `spawnAttachableDaemon` and
  `waitDaemonReady` (free functions in `attach_pty.go`); they do not
  share a `Harness` or `AttachHarness` type because the surface they
  expose differs in a load-bearing way: the PTY harness exposes a
  `*os.File` master, the stdio harness exposes Read / Write methods
  with line-aware semantics. A common interface would force one shape
  to bear the other's wart and saves no code in either.

- **No new options-struct on `spawnAttachableDaemon`.** The existing
  function already does exactly what stdio needs (helper-as-claude,
  echo mode, idle timeout off, resume off). Reuse without parameter
  growth.

- **No CLI-level driver for `pyry sessions new`.** The AC says "via
  `pyry sessions new`" colloquially; every other e2e test in the
  package uses `control.SessionsNew` (the in-process wire client) for
  the same effect. Spawning a third subprocess for one wire call is
  pure cost. The wire-level contract is identical.

- **No `lsof`/`/proc/<pid>/fd` inspection.** That assertion is the
  whole point of the follow-up ticket #162; folding it in here would
  rebuild that ticket prematurely and on the same harness anyway.

# Open questions

- **Does the helper's startup banner need to be drained before the
  test writes its payload?** No — `ReadUntil` accumulates all bytes
  until the needle appears, so banner bytes that arrive on the output
  pipe ahead of the echoed payload are harmless. (Same disposition as
  the PTY harness; see `attach_pty_test.go:135-152`.)

- **Should the harness support concurrent attach clients to the same
  session for an `ErrBridgeBusy` test?** Not now. The bridge-busy
  surface is unit-tested at `internal/control/attach_test.go`; an e2e
  proof at the binary boundary would belong to a separate ticket if
  the gap is ever observed in practice. Keep this harness single-
  client for now — the ticket body is explicit.

- **Should `Close(t)` return an error rather than calling `t.Fatalf`
  on cleanup failure?** No — `t.Cleanup`-discipline says cleanup logs,
  never fails. The exit-code return is the only signal `Close` exposes
  to the test body. Follow-up tickets that need richer cleanup-error
  surfaces can add them when they have a concrete need.

- **Is `newRegistryHome` mandatory, or is a bare `t.TempDir()` enough?**
  `newRegistryHome` returns `(home, regPath)` — the harness needs the
  home but not the regPath. A bare `t.TempDir()` is sufficient. The
  developer should choose `newRegistryHome` only if a future test
  using the harness will assert on registry contents.

# Out of scope

- The follow-up tests #162 (no-PTY-fd assertion), 1.3c-2-e2e-*
  (foreground auto-attach), session-eviction-from-under-client.
- Multi-attach behaviour against the same session.
- A real claude binary in the loop.
- Any change to `attach_pty.go`, `attach_pty_test.go`, or the
  client-side `AttachStdio` function.
