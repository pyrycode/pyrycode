# Stdio-Attach Harness Pattern (`attach_stdio.go`, `attach_stdio_test.go`, #161)

The PTY harness (`AttachHarness`, #125) drives `pyry attach` against a
controlling terminal. SDK consumers (Claudian, `@anthropic-ai/claude-agent-sdk`)
spawn `pyry attach --stdio` over plain pipes — no PTY anywhere on the client
side. #161 adds a sibling **`StdioAttachClient`** + `startStdioAttach(t,
label)` constructor in the same package (build tag `e2e` only — no
installer-test consumer) that proves the byte path:

```
parent's inputW → os.Pipe → attach client stdin → control socket →
bridge → supervisor PTY → supervised helper → echo back →
attach client stdout → os.Pipe → parent's outputR
```

```go
func TestE2E_AttachStdio_BytesRoundTrip(t *testing.T) {
    c := startStdioAttach(t, "stdio-roundtrip")
    payload := []byte("pyry-stdio-roundtrip-" + tinyNonce() + "\n")
    if _, err := c.Write(payload); err != nil {
        t.Fatalf("write: %v", err)
    }
    seen, err := c.ReadUntil(payload, 5*time.Second)
    if err != nil {
        t.Fatalf("did not observe payload back: %v\nstderr:\n%s",
            err, c.Stderr.String())
    }
    _ = seen
}
```

### Why a separate harness, not a flag on `AttachHarness`

The two harnesses share `spawnAttachableDaemon` and `waitDaemonReady` (free
functions in `attach_pty.go`). They do **not** share a `Harness` /
`AttachHarness` type because their public surface differs in a load-bearing
way: PTY exposes a `*os.File` master; stdio exposes `Write` / `ReadUntil`
methods with line-aware semantics (`os.Pipe` ends are not seekable, not
deadline-able, and the helper's echo mode is line-buffered — flushing only
on `\n`). A common interface would force one shape to bear the other's
wart and saves no code in either.

### Public API

```go
type StdioAttachClient struct {
    SessionID  string         // returned by control.SessionsNew
    SocketPath string         // daemon's control socket
    HomeDir    string         // daemon's $HOME (fresh os.MkdirTemp)
    Stderr     *safeBuffer    // attach client stderr (empty in steady state; mutex-guarded since #257 AC#4)
    // ... unexported fields
}

// startStdioAttach probes os.Pipe (t.Skip on failure — AC#3), spawns a
// bridge-mode daemon (helper-as-claude in echo mode), creates a fresh
// session via control.SessionsNew(label), then spawns
// `pyry attach --stdio <id>` with stdin/stdout wired to plain os.Pipe()s.
func startStdioAttach(t *testing.T, label string) *StdioAttachClient

func (c *StdioAttachClient) Write(b []byte) (int, error)
func (c *StdioAttachClient) ReadUntil(needle []byte, total time.Duration) ([]byte, error)
func (c *StdioAttachClient) Close(t *testing.T) int  // returns child exit code
```

`Write` writes to the parent's `inputW` half of the input pipe; the child's
stdin is the `inputR` half. `ReadUntil` reads from `outputR` in a background
goroutine and selects against a deadline — `os.Pipe` ends share the
no-`SetReadDeadline` trait with PTY masters on darwin, so the timeout is
caller-side. `Close` closes `inputW` (delivering EOF to the child's
`AttachStdio` input loop), waits on the child for ≤2s, then runs the
balance of the teardown.

### Pipe-handle ownership and EOF discipline

After `attachCmd.Start()`, the kernel has dup'd `inputR` and `outputW` into
the child. The parent's copies must be closed:

```go
attachCmd.Stdin = inputR
attachCmd.Stdout = outputW
attachCmd.Start()
_ = inputR.Close()   // parent drops its dup so EOF flows from inputW alone
_ = outputW.Close()  // parent drops its dup so EOF on outputR == child closed stdout
```

If the parent retains `inputR`, `inputW.Close()` does **not** propagate EOF
to the child — both writers (parent and child's read-half-as-write?) keep
the kernel pipe open. Symmetric for `outputW`: the parent's read on
`outputR` won't see EOF until every writer is gone.

### Cleanup ordering

Load-bearing — get this wrong and EOF / SIGHUP cascades through the wrong
end. `sync.Once`-wrapped, runs via `t.Cleanup` and / or explicit `Close`:

1. **`inputW.Close()`** — propagates EOF to the attach client's stdin. The
   client's `AttachStdio` input loop returns nil, the conn closes, the
   output goroutine joins, `pyry attach --stdio` exits 0. Clean-detach
   contract.
2. **Wait on `attachDone` with ~2s timeout.** On clean detach the child
   exits in milliseconds. Timeout escalates via `killSpawned`
   (SIGTERM → grace → SIGKILL).
3. **`outputR.Close()`** — releases any in-flight `ReadUntil`. Done
   *after* the child has exited so we don't race the child's stdout
   writes.
4. **`killSpawned(daemonCmd)`** — SIGTERM the daemon; pyry tears down the
   supervisor (which SIGKILLs the helper).
5. **`os.Remove(socketPath)`** — defensive; pyry removes it on clean
   shutdown but SIGKILL paths (step 4 escalation) skip that.

### Error handling

| Failure | Behaviour |
|---|---|
| `os.Pipe()` returns error | `t.Skipf` — same gating shape as #125's `pty.Open` skip; only fires in heavily sandboxed containers |
| Daemon spawn / readiness / `sessions.new` failure | `t.Fatalf` with daemon stderr |
| Attach client exits within 500ms of Start | `t.Fatalf` with attach client's exit code + captured `Stderr` (handshake-failure detector — same shape as `attach_pty.go:137-146`) |
| `c.Write` error | Surfaced to caller; test `t.Fatalf`s on the spot |
| `c.ReadUntil` deadline | `fmt.Errorf("timeout after %s; seen %d bytes: %q", …)` — caller wraps with captured `Stderr` for diagnostics |
| Cleanup partial failure | `t.Logf` only, never `t.Fatal` from a `t.Cleanup` |

The clean-detach contract (`inputW.Close()` → exit 0) is the production
shape an SDK consumer sees when its parent process closes the spawned
child's stdin. `Close(t)` returns the exit code so follow-up tickets can
assert on it.

### Why `control.SessionsNew`, not `pyry sessions new`

The AC says "create session via `pyry sessions new`" colloquially; every
other e2e test in the package uses the in-process `control.SessionsNew`
client for the same effect (`cap_test.go`, `sessions_list_test.go`,
`sessions_rm_test.go`, `idle_test.go`). Spawning a third subprocess for
one wire call is pure cost; the wire-level contract is identical.

### Why supervised claude is helper-as-echo, not `/bin/sleep`

The byte-flow proof needs a writer that echoes — `/bin/sleep` writes
nothing back. `spawnAttachableDaemon` wires the e2e test binary as the
supervised claude (via the `echoClaudeScript` wrapper as of #257 — see §
Helper "claude" via `TestHelperProcess` re-exec above) with
`GO_TEST_HELPER_PROCESS=1` and `GO_TEST_HELPER_MODE=echo`. The two test
files (`attach_pty_test.go` and `attach_stdio_test.go`) compile into the
same test binary and share the helper — there is exactly one
`TestHelperProcess` in the package.

### Skip-on-no-pipe ordering

`os.Pipe()` is the cleanest gate for AC#3 (skip on hosts without spawn
capability) — it exercises kernel fd allocation directly. Place the call
**before** `spawnAttachableDaemon` so a clean `t.Skip` is faster than
spawning pyry and tearing it down. Never fires on the project's CI
matrix; defensive against future restrictive environments.

### Round-trip test unskipped by #257

`TestE2E_AttachStdio_BytesRoundTrip` was originally **skipped pending #167**, the `parseClientFlags` rejection of `--stdio` before
`parseAttachArgs` ever saw the flag. Unit tests in
`internal/control/attach_stdio_client_test.go` and
`cmd/pyry/args_test.go` bypassed `parseClientFlags`, so the harness was
the surface that surfaced the bug. #167 landed (CLI flag pass-through
via `splitClientFlags`); removing that skip exposed a **different**,
pre-existing harness bug: `spawnAttachableDaemon` wired the Go test
binary directly as `claude`, so `Pool.Create`'s appended `--session-id
<uuid>` reached the test framework's `flag.Parse()` and was rejected
before `TestHelperProcess` ran. The skip rotated from #167 to #257 in
the same commit that landed the #167 fix, with the harness body itself
unchanged. #257 then migrated `spawnAttachableDaemon` onto the
`echoClaudeScript` wrapper `auto_attach.go` (#163) already used — see §
Helper "claude" via `TestHelperProcess` re-exec above — and removed the
skip. The test now passes under `-race -tags e2e`.

### What this slice does not verify

- **Foreground auto-attach scenarios** — deferred to 1.3c-2-e2e-*.
- **Server-initiated detach, multi-session attach exclusivity, binary-safe
  transport for arbitrary byte sequences** — covered (or out of scope) at
  the unit boundary in `internal/control/attach_stdio_client_test.go`.

### No-PTY-fd assertion (`attach_stdio_no_pty_test.go`, #162)

Sibling test consumer of the stdio harness. Asserts the **negative**
property the byte-flow test cannot: the attach client process holds no
PTY-device fd while attached. The unit-level guarantee — that
`internal/control/attach_stdio_client.go` imports no PTY machinery — is
supplemented at the binary boundary so a future refactor that wraps
stdio in a PTY inside `cmd/pyry/runAttach`'s `--stdio` branch fails CI
instead of shipping. A regression that allocates a useless PTY but
still passes bytes through fails only this test, not
`…_BytesRoundTrip`.

```go
func TestE2E_AttachStdio_NoPTYInProcessTree(t *testing.T) {
    c := startStdioAttach(t, "stdio-no-pty")
    pid := c.attachCmd.Process.Pid
    hits, err := openPTYDeviceTargets(pid)
    if err != nil {
        t.Skipf("e2e: fd inspection unavailable: %v", err)
    }
    if len(hits) > 0 {
        t.Fatalf("attach client (pid=%d) holds PTY device fd(s): %v", pid, hits)
    }
}
```

The harness's 500ms early-exit window means `startStdioAttach` only
returns once the child is past handshake — any PTY allocation during
init is already visible by probe time. The probe itself is a synchronous
read from the test goroutine; no new goroutines, no channels, no
deadlines. The harness's `t.Cleanup` owns teardown.

**Platform dispatch via `runtime.GOOS`, not build tags.** One file with
two helpers (`openPTYDeviceTargetsLinux` reads `/proc/<pid>/fd/`,
`openPTYDeviceTargetsDarwin` shells out to `lsof -p <pid> -Fn`) and one
shared matcher (`isPTYDevicePath`). Build-tagged `_linux.go` /
`_darwin.go` files would force the matcher into a third file or
duplicate it — `runtime.GOOS` keeps everything in ~110 LOC. `unsupported
GOOS` returns an inspection-unavailable error which the test treats as
a skip per AC#2.

**Matcher set is conservative + explicit.** Matched paths:

| Path | Platforms | Why |
|---|---|---|
| `/dev/ptmx` | linux, darwin | The PTY master multiplexer — the canonical signal of "this process allocated a PTY." |
| `/dev/pts/*` | linux | PTY slave devices on Linux. |
| `/dev/ttys*` | darwin | PTY slave devices on macOS (`/dev/ttys000`, `/dev/ttys001`, …). `exec.Cmd` with explicit Stdin/Stdout/Stderr does **not** propagate the parent's tty fds; if a slave appears, the client opened it. |
| `/dev/tty` | both | Controlling-terminal device. A `--stdio` client with stdio wired to pipes has no business touching it; including it surfaces terminal-mode dispatch on the wrong code path. |

Not matched intentionally: `/dev/null`, `/dev/urandom` (not PTYs), and
the BSD-legacy `/dev/pty[m-z]*` / `/dev/tty[m-z]*` (effectively
unreachable on supported macOS). If a future failure mode escapes the
matcher, extend `isPTYDevicePath` and re-run.

**Linux symlink-read race is silently dropped.** A fd may close between
`os.ReadDir` and `os.Readlink`; the loop ignores `Readlink` errors. A
stable PTY fd would not race a single-pass directory read, so the bias
is toward false-negative on a closing fd — acceptable.

**Carried the same `t.Skip("blocked on #257")` as the byte-flow test.**
Both skips originally tracked #167 (CLI flag rejection); rotated to #257 once #167 landed and exposed the underlying `--session-id`-vs-
test-binary harness bug. #257 lifted both skips in the same commit —
this test also self-skips gracefully if fd-inspection is unavailable
(`openPTYDeviceTargets` err → `t.Skipf`), independent of the harness fix.

**Production diff is zero. Test diff ~110 LOC, one new file.**
`go test -tags e2e -race ./internal/e2e/...` clean.

### Production diff is zero

Two new files, ~321 LOC: `internal/e2e/attach_stdio.go` (~265) +
`internal/e2e/attach_stdio_test.go` (~56). No edits to existing files.
Default `go test ./...` unaffected. `go test -tags e2e -race
./internal/e2e/...` clean.
