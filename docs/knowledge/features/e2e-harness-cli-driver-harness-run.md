# CLI Driver (`Harness.Run`)

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
