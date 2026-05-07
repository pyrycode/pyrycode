---
ticket: 162
title: e2e — assert no PTY device in attach --stdio child fd table
status: spec
size: XS
---

# Files to read first

Read these before exploring on your own — they are the load-bearing
surfaces this spec composes.

- `internal/e2e/attach_stdio.go` — full file (~265 lines). The harness
  this test consumes. Critical references:
  - `attach_stdio.go:50-53` — `attachCmd *exec.Cmd` and `attachDone
    chan struct{}` are unexported but in-package; the new test reads
    `c.attachCmd.Process.Pid` directly. No new exported field required.
  - `attach_stdio.go:155-167` — the harness already burns 500 ms after
    `attachCmd.Start()` waiting for an early-exit handshake failure. By
    the time `startStdioAttach` returns, the child has been alive at
    least 500 ms and is past handshake. The fd-inspection probe runs
    immediately on return, so any PTY allocation that would have
    happened during initialization is already visible.
  - `attach_stdio.go:244-265` — `teardown` ordering. The new test does
    not change cleanup; harness `t.Cleanup` runs after the test body
    and tears down the attach client + daemon as usual.

- `internal/e2e/attach_stdio_test.go` — full file (~57 lines). The
  byte-flow sibling test. Two things to mirror:
  - `attach_stdio_test.go:27-31` — the `t.Skip("blocked on #167 …")`
    guard. The new test takes the same skip until #167 lands; at that
    point both skips come off in one PR. Do not invoke the harness
    from a test that knows the harness can't actually drive the CLI.
  - `attach_stdio_test.go:26` — function naming convention
    (`TestE2E_AttachStdio_*`). Match it.

- `internal/e2e/attach_pty.go:62-74` — precedent for the
  `t.Skipf("e2e: <mechanism> unavailable: %v", err)` shape on platform
  capability gating. The new test uses the same wording for fd
  inspection unavailability (AC#2).

- The `cmd/pyry/runAttach --stdio` path
  (`cmd/pyry/main.go:438-522`, summarised in spec #154 / #161) and
  `internal/control/attach_stdio_client.go` — for context only. The
  contract under test is "`pyry attach --stdio` does not allocate a
  PTY anywhere in its own process." The unit tests at
  `internal/control/attach_stdio_client_test.go` already prove the
  client function never imports `creack/pty`; this e2e test is the
  *binary-level* defence: a future refactor that pulls in a PTY
  somewhere inside `cmd/pyry/runAttach`'s `--stdio` branch (e.g. a
  helper that wraps stdio in a pty.Open before bridging) would slip
  past the unit boundary, and this test catches it.

- Go stdlib `os/exec` and `os` packages — `os.ReadDir`,
  `os.Readlink`, `runtime.GOOS`, `exec.LookPath`, `exec.Command`. No
  new dependencies; stdlib only (matches CODING-STYLE.md "stdlib over
  dependencies").

# Context

## Why this slice exists

#161 (now closed) landed `startStdioAttach` and the byte-flow proof-
of-life test. AC#1 of #161 pinned the *positive* property: bytes
travel parent → attach client → bridge → claude → and back. The
*negative* property — that the attach client process itself never
allocates a PTY — is currently enforced only by code review of
`cmd/pyry/main.go` and `internal/control/attach_stdio_client.go`. A
future refactor could re-introduce a `pty.Open` (e.g. "wrap stdio in
a pty for raw-mode safety in some edge case") and slip past every
existing test:

- Unit tests in `internal/control/` only exercise the
  `AttachStdio` function — they don't observe `runAttach`.
- The e2e byte-flow test would still pass: bytes still round-trip
  even if the client opens a useless PTY on the side.

This ticket lands the binary-level defence: inspect the running
attach child's open fd table, assert no PTY device is present.

## Independence from #161

The byte-flow test and this fd-inspection test share the harness but
verify orthogonal properties:

| Test | Property | If it fails |
|---|---|---|
| `…_BytesRoundTrip` (#161) | Bytes flow E2E | The shape doesn't work at all |
| `…_NoPTYInProcessTree` (#162) | No PTY allocated client-side | The shape works but isn't `--stdio`-shaped |

A regression that allocates a PTY but still passes bytes through fails
only this test, not the round-trip test. Hence two tests, not one.

## Carry-over of #167's skip

The harness can't drive `pyry attach --stdio` end-to-end until #167
(`pyry attach --stdio` rejected by parseClientFlags before
parseAttachArgs runs) lands. The byte-flow test at
`attach_stdio_test.go:31` skips on this. This test takes the **same
skip with the same message**; whoever lands #167 lifts both skips in
one commit. Do not gate the new test on a different signal.

# Design

## Approach

One new file, no edits to existing files. The test:

1. Calls `startStdioAttach(t, "stdio-no-pty")`. The harness has
   already confirmed the attach client is past handshake by the time
   it returns (its 500 ms early-exit window).
2. Reads `c.attachCmd.Process.Pid` (in-package, no exported field
   needed).
3. Dispatches on `runtime.GOOS` to the platform's fd-inspection
   primitive: `/proc/<pid>/fd/` on linux, `lsof -p <pid> -Fn` on
   darwin.
4. Filters the inspected names through `isPTYDevicePath` and asserts
   the resulting slice is empty. If non-empty, `t.Fatalf` with the
   pid and the offending paths so the failure mode is unambiguous.
5. If the inspection mechanism itself is unavailable (no `/proc` on
   linux, no `lsof` on darwin, or an unsupported GOOS), `t.Skipf` per
   AC#2 with the same wording shape as `attach_pty.go:73`'s skip.

The harness's `t.Cleanup` (registered in `startStdioAttach`) tears
down the attach client and daemon after the test body returns; the
test does nothing in cleanup beyond what the harness already does.

## Package structure

### NEW `internal/e2e/attach_stdio_no_pty_test.go`

Build tag: `//go:build e2e` (matches `attach_stdio.go` /
`attach_stdio_test.go`; no installer-test consumer, so no
`e2e_install` alternation).

Approximate shape (~110 LOC total, including comments and the
two-platform helpers):

```go
//go:build e2e

package e2e

import (
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "strings"
    "testing"
)

// TestE2E_AttachStdio_NoPTYInProcessTree asserts that the
// `pyry attach --stdio` child process holds no PTY device fds
// (/dev/ptmx, /dev/pts/*, /dev/ttys* on darwin) while attached to a
// supervised session via plain os.Pipe()s. The unit-level guarantee
// — that internal/control/attach_stdio_client.go imports no PTY
// machinery — is supplemented here at the binary boundary so a future
// refactor that wraps stdio in a pty inside cmd/pyry/runAttach's
// --stdio branch fails CI instead of shipping.
//
// Independent of TestE2E_AttachStdio_BytesRoundTrip: that test
// asserts bytes flow (positive); this one asserts no PTY allocation
// (negative). A regression that allocates a useless PTY but still
// passes bytes would fail only here.
func TestE2E_AttachStdio_NoPTYInProcessTree(t *testing.T) {
    // Same #167 gate as the byte-flow test; remove together when #167
    // lands.
    t.Skip("blocked on #167 — pyry attach --stdio rejected by parseClientFlags")

    c := startStdioAttach(t, "stdio-no-pty")

    pid := c.attachCmd.Process.Pid
    hits, err := openPTYDeviceTargets(pid)
    if err != nil {
        // AC#2: skip cleanly if inspection mechanism is unavailable.
        t.Skipf("e2e: fd inspection unavailable: %v", err)
    }
    if len(hits) > 0 {
        t.Fatalf("attach client (pid=%d) holds PTY device fd(s): %v\nattach stderr:\n%s",
            pid, hits, c.Stderr.String())
    }
}

// openPTYDeviceTargets returns the PTY device paths that pid has
// open. Empty slice + nil error means "no PTY devices held" (the
// success case). Non-nil error means the inspection mechanism itself
// is not available; the caller should t.Skip.
func openPTYDeviceTargets(pid int) ([]string, error) {
    switch runtime.GOOS {
    case "linux":
        return openPTYDeviceTargetsLinux(pid)
    case "darwin":
        return openPTYDeviceTargetsDarwin(pid)
    default:
        return nil, fmt.Errorf("unsupported GOOS %q", runtime.GOOS)
    }
}

func openPTYDeviceTargetsLinux(pid int) ([]string, error) {
    fdDir := fmt.Sprintf("/proc/%d/fd", pid)
    entries, err := os.ReadDir(fdDir)
    if err != nil {
        return nil, fmt.Errorf("read %s: %w", fdDir, err)
    }
    var hits []string
    for _, e := range entries {
        target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
        if err != nil {
            // fd may have closed mid-iteration; non-fatal — keep going.
            continue
        }
        if isPTYDevicePath(target) {
            hits = append(hits, target)
        }
    }
    return hits, nil
}

func openPTYDeviceTargetsDarwin(pid int) ([]string, error) {
    if _, err := exec.LookPath("lsof"); err != nil {
        return nil, fmt.Errorf("lsof not found: %w", err)
    }
    // -Fn emits one record per open fd. Name records start with 'n'.
    out, err := exec.Command("lsof", "-p", fmt.Sprintf("%d", pid), "-Fn").Output()
    if err != nil {
        return nil, fmt.Errorf("lsof -p %d: %w", pid, err)
    }
    var hits []string
    for _, line := range strings.Split(string(out), "\n") {
        if !strings.HasPrefix(line, "n") {
            continue
        }
        name := strings.TrimPrefix(line, "n")
        if isPTYDevicePath(name) {
            hits = append(hits, name)
        }
    }
    return hits, nil
}

// isPTYDevicePath returns true for paths the kernel exposes as PTY
// devices on linux or darwin. The set is conservative and explicit —
// /dev/tty (the controlling terminal device) is included because a
// --stdio attach client that has stdin/stdout/stderr wired to pipes
// should never have any reason to open the controlling tty; doing so
// would indicate an incorrect terminal-mode dispatch in
// cmd/pyry/runAttach.
func isPTYDevicePath(p string) bool {
    switch p {
    case "/dev/ptmx", "/dev/tty":
        return true
    }
    return strings.HasPrefix(p, "/dev/pts/") || // linux pty slaves
        strings.HasPrefix(p, "/dev/ttys")        // darwin BSD-style pty slaves (/dev/ttys000…)
}
```

## Why no platform build tags

`runtime.GOOS` dispatch keeps everything in one file. Build-tagged
`_linux.go` / `_darwin.go` files would force the `isPTYDevicePath`
helper to be duplicated or split into a third shared file. The
test runs on linux + darwin only (the project's supported platforms);
the `unsupported GOOS` branch returns an inspection-unavailable
error which the test treats as a skip per AC#2.

## PTY device matchers

The matcher set is **conservative + explicit**:

| Path | Platforms | Why match |
|---|---|---|
| `/dev/ptmx` | linux, darwin | The PTY master multiplexer. Opening it allocates a new (master, slave) pair. Holding an fd to it is the canonical signal of "this process allocated a PTY." |
| `/dev/pts/*` | linux | PTY slave devices on Linux. |
| `/dev/ttys*` | darwin | PTY slave devices on macOS (`/dev/ttys000`, `/dev/ttys001`, …). Note: the user's own controlling terminal is also a `/dev/ttysNNN` device, but `exec.Cmd` with explicit Stdin/Stdout/Stderr does **not** propagate the parent's tty fds to the child. The attach client sees only the three pipe fds plus anything it opens itself; if it opens `/dev/ttysNNN` directly, that's the bug we're catching. |
| `/dev/tty` | both | The controlling-tty device. Opens to whatever tty the process is associated with. A `--stdio` client should have no business touching this. Including it surfaces a related class of bugs (raw-mode dispatch on the wrong code path). |

Not matched (intentionally):

- `/dev/null`, `/dev/urandom`, etc. — Not PTY devices; routine for any
  process to have open.
- BSD legacy `/dev/pty[m-z]*` and `/dev/tty[m-z]*` — Modern darwin
  uses ptmx-cloned slaves under `/dev/ttysNNN`; the legacy
  pre-grantpt naming is effectively unreachable on supported macOS
  versions. If a future failure mode points there, extend the matcher.

## Concurrency model

None beyond what the harness already provides. The fd-inspection
probe is a synchronous function call from the test goroutine. The
attach client + daemon goroutines (each `cmd.Wait → close(done)`)
are owned by the harness and untouched. No new goroutines, no
channels, no deadlines.

The probe is "racy" only in the trivial sense that a fd may close
between `os.ReadDir` and `os.Readlink` on linux — handled by
ignoring `Readlink` errors (the `continue` in
`openPTYDeviceTargetsLinux`). A PTY fd that exists at the start of
the iteration but closes mid-iteration is still a positive hit —
the dirent-level snapshot includes its symlink, which we read
either successfully (hit recorded) or with an error (skipped, which
biases toward false-negative — acceptable; a stable PTY fd would
not race a single-pass directory read).

## Cleanup

No new cleanup code. The harness's existing `t.Cleanup` (registered
in `startStdioAttach`) tears down the attach client and daemon. This
test owns no resources beyond the inspection probe's transient
slices and (on darwin) one short `lsof` subprocess that exits before
the call returns.

## Error handling

| Failure | Behaviour | Why |
|---|---|---|
| `runtime.GOOS` not in {linux, darwin} | `t.Skipf` via the inspection-unavailable path | AC#2; pyrycode supports linux + darwin only. A future windows port adds a third arm. |
| Linux: `os.ReadDir("/proc/<pid>/fd")` fails | `t.Skipf` | AC#2. Realistic in heavily sandboxed containers without procfs (rare on supported CI). |
| Darwin: `exec.LookPath("lsof")` fails | `t.Skipf` | AC#2. lsof is bundled with macOS; absence indicates a stripped image. |
| Darwin: `lsof -p <pid> -Fn` exits non-zero | `t.Skipf` with the wrapped error | The pid may have raced exit. Probabilistically negligible (≥500 ms after Start), but `t.Skip` over `t.Fatal` because the failure is environmental, not a property violation. |
| Linux: `os.Readlink` on a single dirent fails | Continue (skip that entry) | Mid-iteration fd close. Silently dropping is correct: the dirent listing was the snapshot; if a symlink can't be read, we have no path to assert against. |
| `len(hits) > 0` | `t.Fatalf` with pid + paths + attach stderr | The actual property violation. Stderr included for diagnostic context. |

## Testing strategy

The test *is* the strategy — it is one assertion against one running
binary. There are no helpers worth unit-testing in isolation:

- `isPTYDevicePath` is a 6-line pure string function; reading the
  source is faster than reading a unit test that re-states the
  matchers.
- `openPTYDeviceTargets{Linux,Darwin}` are thin wrappers around
  syscalls / external commands; mocking the filesystem or `lsof`
  would test the mock, not the behaviour.

If the matcher set proves wrong (false-positive on a legitimate
non-PTY device, or false-negative on a real PTY allocation), the
fix is to extend `isPTYDevicePath` and re-run the test against the
known regression. The whole point of this ticket is the binary-level
probe; unit-testing the helpers would dilute that signal.

# Out of scope

- **Stress / repeated-spawn / fd leak testing.** Ticket body
  explicitly excludes. One-shot inspection at one moment in the
  attach client's lifetime is sufficient — a regression that
  intermittently allocates a PTY would still trip this test on at
  least some runs, and the `-race` suite already runs the e2e
  package on every CI invocation.
- **Daemon-side PTY assertions.** The supervised claude on the
  daemon side runs under a PTY (owned by the supervisor); that's
  expected and unchanged. This test inspects only the attach
  client's pid, not the daemon's.
- **Windows platform support.** Out of project scope.
- **A separate "pyry helper command" for fd inspection.** Tempting
  to add a `pyry debug fds` verb that the test calls, but: (a) it
  would expose internal state for one test's benefit; (b) it would
  inspect the wrong process (the daemon, not the attach client);
  (c) `/proc` and `lsof` are stable, well-known, and require no
  product surface. Reject.
- **Asserting the matcher set's exhaustiveness.** Future PTY device
  paths the kernel might expose (e.g. a hypothetical
  `/dev/pty-vNNN`) are unenumerable in advance. The matcher is
  evidence-based: extend when an actual regression escapes it.

# Open questions

- **Should `/dev/tty` really be matched?** It's a controlling-
  terminal device, not strictly a PTY. Argument for inclusion: a
  `--stdio` client with stdin/stdout wired to pipes has no
  legitimate reason to open `/dev/tty`; doing so means it took a
  terminal-mode code path it shouldn't have. Argument against:
  conflates "PTY allocation" with "terminal-mode regression",
  which are distinct failure modes. Decision: include — the AC's
  spirit ("no PTY device") is closer to "no terminal device of any
  kind", and the false-positive cost is a clearer error message
  on a real bug.

- **Should the test assert the attach client process is still
  alive *at probe time*?** The harness's 500 ms early-exit check
  is recent (just before return), so a pid-still-alive assertion is
  redundant. If the child died between handshake and the probe
  (~tens of µs after harness return), `os.ReadDir`/`lsof` would
  fail with ESRCH and the test would skip — preferable to a
  spurious failure on a different code path. Leave it.

- **Should the matcher set be extracted into a shared package for
  future tests?** No — the only consumer is this one test. If a
  second consumer appears, hoist then. YAGNI per
  CLAUDE.md "simplicity first."
