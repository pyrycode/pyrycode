# Spec — E2E harness: spawn + cleanup primitive (#68)

## Context

Coverage today is unit + package-level. Failure modes only visible at the binary boundary
— daemon startup, socket readiness, IPC contract, install-service round-trips — are
uncovered. Phase 1.1's session-management verbs (#45–49) need e2e tests, but each must
stay within architect's S-cap, so the spawn/cleanup machinery has to land first as
standalone infrastructure.

This ticket delivers **only** the process-lifecycle primitive: build `pyry`, spawn it in
isolation, block until ready, expose captured I/O after exit, tear down reliably even
when the test panics or `t.Fatal`s. CLI-driver wrappers (`harness.Status()`,
`harness.Run()`) and the first feature-flavoured e2e land in the follow-up.

Split from #51.

## Design

### Package layout

```
internal/e2e/
    harness.go        Production code: Harness, Start, build helper, readiness poll, teardown
    harness_test.go   Smoke + failure-injection tests
```

Path rationale: `internal/` keeps the harness un-importable outside the module. Sibling
package directories (`internal/e2e/sessions/`, etc.) for feature-specific e2e tests can
import this primitive in follow-up tickets without renaming.

### Build-tag isolation

Every file in the package carries:

```go
//go:build e2e
```

Default `go test ./...` does not compile or run anything in the package. Invocation:

```
go test -tags=e2e ./internal/e2e/...
```

The package doc comment (top of `harness.go`) carries the tag instruction so godoc
surfaces it.

### Public API (minimal)

```go
package e2e

// Harness owns one running pyry daemon. Returned by Start; cleanup is
// registered via t.Cleanup at construction.
type Harness struct {
    // SocketPath is the Unix socket the daemon listens on. Tests can dial
    // it directly (e.g. via internal/control client helpers).
    SocketPath string

    // HomeDir is the temp dir the daemon sees as $HOME. Registry, claude
    // sessions dir, and any other ~-relative paths live underneath.
    HomeDir string

    // PID of the running pyry process. Captured at spawn so failure-injection
    // tests can verify it is gone after cleanup runs.
    PID int

    // Stdout / Stderr accumulate the child's output. Safe to read after
    // the process has exited (cleanup waits for the wait goroutine).
    Stdout *bytes.Buffer
    Stderr *bytes.Buffer

    // unexported: cmd, doneCh, cleanupOnce
}

// Start builds pyry once per test process, spawns it in an isolated
// temp HOME with a custom socket path, blocks until the control socket
// is dialable, and registers teardown via t.Cleanup. Fails the test
// (t.Fatalf) on any error before returning a usable Harness.
func Start(t *testing.T) *Harness
```

No `Option`s in this ticket. The AC parenthesises "or equivalent"; per
working-principle "don't design for hypothetical future requirements", options land when
the first consumer needs one. Only three exported names: `Harness`, `Start`, plus the
struct fields.

### Isolation strategy

The daemon resolves `~/.pyry/<name>.sock`, `~/.pyry/<name>/sessions.json`, and
`~/.claude/projects/<encoded-cwd>/<uuid>.jsonl` from `os.UserHomeDir()`, which honors
`$HOME` on Unix. The harness redirects `HOME` to `t.TempDir()` so **every** path the
daemon would touch under a real home is contained, with one env var.

For belt-and-suspenders explicitness on the load-bearing path, also pass
`-pyry-socket=<tmp>/pyry.sock`. The registry still lands at `<tmp>/.pyry/<name>/`
courtesy of HOME redirection — covering the AC's "custom socket and registry paths"
without inventing a new `-pyry-registry` flag this ticket would have to also expose
through main.

Spawn args:

```
-pyry-socket=<HomeDir>/pyry.sock
-pyry-name=test
-pyry-claude=/bin/sleep
-pyry-idle-timeout=0
-- infinity                # claude args; /bin/sleep accepts "infinity"
```

`/bin/sleep infinity` is the supervised "claude" — exists on Linux + macOS (per
`lessons.md § Test helpers across packages`), survives until SIGTERM, and the readiness
gate doesn't depend on the child being a real claude. `IdleTimeout=0` disables eviction
so the smoke test isn't racing the timer.

`cmd.Env` carries the parent env minus any `HOME=` plus `HOME=<HomeDir>`. `PYRY_NAME` is
explicitly unset to defeat the operator's shell alias from leaking in.

### Build helper (sync.Once)

```go
var (
    binOnce sync.Once
    binPath string
    binErr  error
)

func ensurePyryBuilt(t *testing.T) string {
    binOnce.Do(func() {
        if env := os.Getenv("PYRY_E2E_BIN"); env != "" {
            binPath = env
            return
        }
        dir, err := os.MkdirTemp("", "pyry-e2e-*")
        if err != nil { binErr = err; return }
        binPath = filepath.Join(dir, "pyry")
        cmd := exec.Command("go", "build", "-o", binPath, "github.com/pyrycode/pyrycode/cmd/pyry")
        out, err := cmd.CombinedOutput()
        if err != nil { binErr = fmt.Errorf("go build pyry: %w\n%s", err, out) }
    })
    if binErr != nil { t.Fatalf("e2e: %v", binErr) }
    return binPath
}
```

Built once per test process. The temp dir is intentionally not cleaned — `go test`'s own
cleanup path takes /tmp eventually, and there's no convenient `TestMain` hook this
package owns. `PYRY_E2E_BIN` env var lets CI skip the rebuild when a known-good binary
is already on disk.

### Readiness signal

Poll loop with a 5-second deadline:

```
deadline := time.Now().Add(5 * time.Second)
for time.Now().Before(deadline) {
    if _, err := os.Stat(socketPath); err == nil {
        c, err := net.Dial("unix", socketPath)
        if err == nil { c.Close(); return nil }
    }
    select {
    case <-doneCh: return fmt.Errorf("pyry exited before ready: %s", stderr.String())
    case <-time.After(50 * time.Millisecond):
    }
}
return fmt.Errorf("pyry not ready within 5s")
```

Once `Dial` succeeds, the control server has called `Listen` + entered `Serve`. The
control server starts before `pool.Run` enters its supervise loop (cmd/pyry/main.go:307
`ctrl.Listen` → 313 `go ctrl.Serve(ctx)` → 322 `pool.Run`), so dial-success means the
daemon is responsive even if the supervised child hasn't spawned yet. Sufficient for the
"daemon is alive" contract this ticket sells.

`doneCh` short-circuit prevents wasting the full 5s when `pyry` exits early (e.g. flag
parse error from a buggy follow-up wrapper).

### Concurrency model

```
test goroutine
    │
    ├── ensurePyryBuilt (one-time, package sync.Once)
    ├── exec.Cmd.Start
    │       └── child process (pyry)
    │
    ├── go waitGoroutine: cmd.Wait → close(doneCh)
    │
    ├── waitForReady (polls socket, watches doneCh)
    └── t.Cleanup(harness.teardown)
```

One auxiliary goroutine: a `wait` goroutine that calls `cmd.Wait()` and closes
`doneCh`. It owns `cmd.Wait`'s exclusive call. `Stdout` / `Stderr` are
`bytes.Buffer`s wired into `cmd.Stdout` / `cmd.Stderr` directly — `exec.Cmd`
synchronizes writers with `Wait`, so reads after `<-doneCh` are race-free without an
explicit mutex.

### Teardown sequence

Registered via `t.Cleanup`. Wrapped in `sync.Once` so a manual `Stop()` (future
extension) and t.Cleanup don't double-fire.

```
1. cmd.Process.Signal(syscall.SIGTERM)
2. select on doneCh with 3s grace timer
   - exited cleanly → step 4
   - timeout → cmd.Process.Signal(syscall.SIGKILL); wait another 1s on doneCh
3. on SIGKILL timeout: log via t.Logf, give up — leak surfaces in step-5 verification
4. os.Remove(SocketPath)  // defensive: SIGTERM path lets pyry clean it; SIGKILL doesn't
5. (HomeDir auto-cleaned by t.TempDir)
```

The HomeDir cleanup is implicit — `t.TempDir()` registers its own cleanup. The harness
doesn't re-do it.

### Failure-injection verification

The harness records `cmd.Process.Pid` at spawn. Tests that need to assert "pyry didn't
leak after a panicking test" use the standard subtest-with-fatal pattern:

```go
func TestHarness_NoLeakOnFatal(t *testing.T) {
    var pid int
    var sock string
    t.Run("crash", func(t *testing.T) {
        h := harness.Start(t)
        pid, sock = h.PID, h.SocketPath
        t.Fatal("inject failure")
    })
    // inner subtest's t.Cleanup has run by here
    if processAlive(pid) {
        t.Errorf("pyry pid=%d still alive after cleanup", pid)
    }
    if _, err := os.Stat(sock); !errors.Is(err, fs.ErrNotExist) {
        t.Errorf("socket %s not removed: %v", sock, err)
    }
}

func processAlive(pid int) bool {
    p, err := os.FindProcess(pid)
    if err != nil { return false }
    return p.Signal(syscall.Signal(0)) == nil
}
```

`syscall.Signal(0)` is the standard POSIX "is this PID alive" probe — zero-cost, no
side effect, returns ESRCH if gone. `os.FindProcess` on Unix never errors, but the
guard above is harmless.

## Concurrency model summary

| Goroutine | Owns | Lifetime |
|---|---|---|
| Test goroutine | `Start` flow, teardown | Test scope |
| Wait goroutine | `cmd.Wait()`, `close(doneCh)` | From `cmd.Start` until child exits |

No locks. `sync.Once` guards the build and the teardown. Channel semantics carry the
cross-goroutine signal.

## Error handling

| Failure | Response |
|---|---|
| `go build` fails | `t.Fatalf` with build output |
| `cmd.Start` fails | `t.Fatalf` with err |
| Readiness deadline | `t.Fatalf` with stderr tail |
| Pyry exits during readiness | `t.Fatalf` with stderr tail |
| SIGTERM grace expires | escalate to SIGKILL |
| SIGKILL grace expires | `t.Logf` warning; let leak verification surface it |
| `os.Remove(socket)` after SIGKILL | best-effort, ignore err |

`Start` is fail-fast: it calls `t.Fatalf` rather than returning an error, since the only
reasonable response in test code is to abort. The "or equivalent" language in the AC
covers this shape.

## Testing strategy

Two tests, both in-package (`internal/e2e/harness_test.go`):

1. **`TestHarness_Smoke`** — `h := Start(t)`; assert `h.SocketPath` non-empty,
   `h.PID > 0`. Dial the socket once more to confirm it stayed up. Let `t.Cleanup`
   handle teardown. Verifies the spawn + ready + clean-shutdown path end-to-end.

2. **`TestHarness_NoLeakOnFatal`** — subtest with `t.Fatal`; outer test asserts process
   is gone and socket file is removed. Verifies cleanup-on-failure (the AC's
   load-bearing safety property).

CI: a follow-up ticket will add a `make e2e` target and wire it into the GitHub Actions
job. Out of scope here — the build-tag isolation means the existing `go test ./...` job
keeps passing untouched.

## Open questions

- **Race detector under e2e tag.** Default is to inherit the parent test invocation; if
  someone runs `go test -tags=e2e -race ./internal/e2e/...`, the harness binary is
  built without `-race` (separate `go build`). The follow-up may want
  `go build -race` when the parent suite uses it. Not load-bearing for #68's
  primitive — file as a follow-up enhancement.

- **Windows.** Platform is out of scope per CLAUDE.md; the harness uses POSIX signals
  (`SIGTERM`, `SIGKILL`) and Unix sockets. No build constraint beyond the e2e tag is
  needed because the project itself is Linux + Darwin only.

## Out of scope (defer to #51-followup)

- `Harness.Status()`, `Harness.Stop()`, `Harness.Attach()`, generic `Harness.Run(args...)`
- Option type and any `WithFoo(...)` constructors
- First feature-flavoured e2e test (sessions verbs etc.)
- CI wiring (`make e2e`, GitHub Actions matrix)
- Race-mode harness build
