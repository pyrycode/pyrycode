# Foreground Auto-Attach Harness Pattern (`auto_attach.go`, `auto_attach_happy_test.go`, #163)

The stdio-attach harness drives `pyry attach --stdio <uuid>`. Phase
1.3c-2 (#158) added a *second* dispatch path to `control.AttachStdio`:
the foreground binary path, where `pyry --session-id <uuid> …` (no
`attach` verb) is invoked while a daemon already hosts the UUID and
`tryAutoAttach` short-circuits the supervised spawn. #163 adds the
sibling **`ForegroundAutoAttachClient`** + `startForegroundAutoAttach(t,
label)` constructor for e2e proof of that branch.

```go
func TestE2E_ForegroundAutoAttach_AttachesWhenDaemonHasSession(t *testing.T) {
    c := startForegroundAutoAttach(t, "auto-attach-happy")

    payload := []byte("pyry-auto-attach-" + tinyNonce() + "\n")
    if _, err := c.Write(payload); err != nil { t.Fatalf("write: %v", err) }
    seen, err := c.ReadUntil(payload, 5*time.Second)
    if err != nil { t.Fatalf("did not observe payload back: %v\nstderr:\n%s", err, c.Stderr.String()) }
    if !bytes.Contains(seen, payload) { t.Fatalf("ReadUntil returned without payload: %q", seen) }

    children, err := pgrepChildren(c.Pid)
    if err != nil { t.Skipf("e2e: pgrep unavailable: %v", err) }
    if len(children) > 0 {
        t.Fatalf("foreground pyry pid=%d has children %v; expected zero (auto-attach should not spawn)", c.Pid, children)
    }
}
```

Two assertions. The byte round-trip proves the foreground pyry reached
`control.AttachStdio` and the daemon's bridge round-tripped bytes
through the supervised echo helper. The `pgrep -P` proves the
foreground pyry has zero direct children — auto-attach must NOT spawn
a competing supervisor. A bug that wired auto-attach correctly *and*
forked a supervised claude would still pass the byte assertion (the
daemon's helper echoes regardless), so the process-tree check is the
orthogonal pin.

### Why a separate harness, not a flag on `StdioAttachClient`

The two harnesses share `spawnAttachableDaemon`'s shape (and #163
specialises it — see "Daemon variant" below) but the **child argv
under test** differs at the heart of the contract. Stdio-attach
spawns `pyry attach --stdio <id>` (verb-dispatch path); auto-attach
spawns `pyry -pyry-socket=… -- --session-id <id> …` (foreground-binary
path through `runSupervisor` → `tryAutoAttach`). The whole point of
the test is *which dispatch path is taken*; folding both into one
type with a flag would obscure exactly the distinction the test
exists to pin.

The architect's spec accepts the ~35 LOC of `Write` / `ReadUntil` /
`Close` duplication between the two harnesses as the simpler choice;
a refactor to share a tiny inner struct is explicitly **out of scope
for #163** and not needed by #164 either.

### Public API

```go
type ForegroundAutoAttachClient struct {
    SessionID  string         // UUID returned by control.SessionsNew
    SocketPath string         // daemon's control socket
    HomeDir    string         // daemon's $HOME (fresh os.MkdirTemp)
    Stderr     *safeBuffer    // foreground pyry stderr; expected empty in steady state
    Pid        int            // foreground pyry's PID — input to pgrepChildren
    // ... unexported plumbing identical in shape to StdioAttachClient
}

func startForegroundAutoAttach(t *testing.T, label string) *ForegroundAutoAttachClient

func (c *ForegroundAutoAttachClient) Write(b []byte) (int, error)
func (c *ForegroundAutoAttachClient) ReadUntil(needle []byte, total time.Duration) ([]byte, error)
func (c *ForegroundAutoAttachClient) Close(t *testing.T) int  // returns child exit code

// Process-tree primitive. Reused by sibling #164.
func pgrepChildren(pid int) ([]int, error)
```

`Pid` is exported (unlike the stdio harness's hidden `attachCmd`)
because the process-tree assertion is the test's headline check — and #164 needs the same field for its fallback tests' inverse assertion
(supervised spawn fired → exactly one child). Surface it once.

### Argv shape: foreground binary, NOT the `attach` verb

The defining argv:

```
pyry -pyry-socket=<sock> -- --session-id <uuid> --input-format stream-json --output-format stream-json
```

- **No `attach` verb.** `os.Args[1]` is `-pyry-socket=…`, which doesn't
  match any verb in `run()`'s switch, so `runSupervisor(os.Args[1:])`
  runs and `tryAutoAttach` is the entry point. Pinning this dispatch
  path is the whole point of the test.
- **`--` is defensive.** `splitArgs` already tips into `claudeArgs` at
  the first non-pyry-flag arg, so `--` is not strictly required.
  Including it documents intent ("everything after this is claude
  args") and is forward-compatible if pyry ever adds a flag whose name
  overlaps with one of claude's.
- **`--input-format stream-json --output-format stream-json` is
  functionally inert** in this test — auto-attach short-circuits before
  any claude binary is consulted, so the flags are never parsed by
  anything. They mirror Claudian's invocation literal so the test
  pins the AC's actual user-facing shape ("Obsidian's claude binary
  path = pyry"), and guard against a future regression where pyry
  parses claude flags client-side.
- **No `-pyry-claude` override.** The default (`/usr/bin/env claude` or
  similar) is fine because we *expect* auto-attach to fire and *no
  claude spawn to happen*. If a bug causes fall-through, the supervised
  spawn fails trying to exec a claude binary that isn't on the test
  runner's PATH; the byte round-trip times out; the failure diagnostic
  is legible. Setting `-pyry-claude=/bin/false` would mask the real
  failure with a deliberately-bogus path.

### Neither #167 nor #257 reaches this code path

The `pyry attach --stdio` CLI bug (#167, fixed) blocked #161/#162's
`TestE2E_AttachStdio_*` tests because `parseClientFlags` rejected
`--stdio` before `parseAttachArgs` ran. The follow-up harness bug
(#257, fixed — the `--session-id`-vs-test-binary collision exposed when #167's fix lifted the skip) kept those tests skipped until it migrated
`spawnAttachableDaemon` onto the wrapper. **#163 was unaffected by
both, from the start.** `tryAutoAttach` calls `control.AttachStdio`
*directly* from inside the foreground binary's `runSupervisor` — no
verb dispatch, no `parseClientFlags`, no `parseAttachArgs`; and #163's
own daemon variant used a shell-wrapper `claude` (`echoClaudeScript`)
which dodged #257 by construction, ahead of #257 applying the same fix
to `spawnAttachableDaemon`. Confirmed against `cmd/pyry/main.go` (the
in-process `control.AttachStdio` call site in `tryAutoAttach`).

This also means #163's test was the **first end-to-end proof** that the
`AttachStdio` byte path works against a real daemon — the stdio-attach
test that was meant to be the first proof stayed `t.Skip`'d until #257
landed and unskipped it (see § Round-trip test unskipped by #257
above).

### Daemon variant: `spawnAutoAttachDaemon` + `echo-claude.sh` shell wrapper

`startForegroundAutoAttach` cannot reuse `spawnAttachableDaemon`
verbatim. Reason: `Pool.Create` appends `--session-id <uuid>` to the
supervised claude's argv on every non-bootstrap session
(`internal/sessions/pool.go:852`). `control.SessionsNew` (called by
the harness to mint the test's UUID) drives `Pool.Create`, which means
the supervised "claude" is invoked as `<bin> -test.run=TestHelperProcess
--session-id <uuid>`. Go's `flag.Parse()` rejects the unknown
`-session-id` flag and exits 2 *before* `TestHelperProcess` runs — the
session is dead-on-arrival, no echo, the byte round-trip times out.

Workaround: a `<home>/echo-claude.sh` shell wrapper that ignores its
argv entirely and `exec`s the test binary with fixed args. Mirrors the
`writeSleepClaude` pattern (#116, now the harness-wide default — see
§ Default Stand-In Argv Tolerance) — see
[lessons.md § `Pool.Create` appends `--session-id`, breaking naive
claude stand-ins](../../lessons.md#poolcreate-appends---session-id-breaking-naive-claude-stand-ins).

```sh
#!/bin/sh
exec "$E2E_HELPER_BIN" -test.run=TestHelperProcess
```

`E2E_HELPER_BIN` is exported in the daemon's environment so
`supervisor.runOnce` flows it through to the wrapper at exec time.
Helper-mode env vars (`GO_TEST_HELPER_PROCESS`, `GO_TEST_HELPER_MODE`)
are preserved across the shell `exec` automatically.

`spawnAutoAttachDaemon` is otherwise structurally identical to
`spawnAttachableDaemon`: bridge mode, helper-as-claude in echo mode,
`waitDaemonReady` polls on the same protocol. The #161 stdio harness had
the *same bug* (its daemon also drives `SessionsNew` and, pre-#257,
wired the test binary directly as `claude`); once #167's CLI fix landed
and the skip lifted, the bug surfaced as a real harness failure tracked
in #257. Folding `spawnAttachableDaemon` over to this shell-wrapper
shape was the body of #257 (landed) — see § Helper "claude" via
`TestHelperProcess` re-exec above.

### `Stderr` is a `safeBuffer`, not a bare `bytes.Buffer`

`cmd.Stderr = &bytes.Buffer{}` races with test-goroutine reads of
`.String()` because `os/exec`'s stderr-copy goroutine writes the
buffer concurrently. The race is real on `-race` even when the buffer
is empty in steady state. `safeBuffer` is a tiny mutex-wrapped
`bytes.Buffer` (two methods: `Write` under lock, `String` snapshot
under lock); see [lessons.md § `cmd.Stderr` reads race the os/exec
copy goroutine](../../lessons.md#cmdstderr-reads-race-the-osexec-copy-goroutine).
The stdio harness's `StdioAttachClient.Stderr` had the same latent race
but was not refactored here (#163) — its proof-of-life test was still
`t.Skip`'d on #257 at the time, so the race never fired under `-race`. #257 later applied the identical `safeBuffer` swap to
`StdioAttachClient.Stderr` (AC#4, § Public API above) once unskipping
the test made the race live.

### `pgrepChildren` — process-tree primitive

```go
func pgrepChildren(pid int) ([]int, error)
```

`exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()`, parse
newline-separated PIDs, return `[]int`. `pgrep -P` exits 1 with empty
stdout when no children exist — the function returns `(nil, nil)` for
that case (success path for #163; failure path for #164's fallback
tests). `exec.LookPath("pgrep")` failure → returns an error the caller
turns into `t.Skip` (matches the precedent in
`openPTYDeviceTargetsDarwin` for `lsof`).

Why `pgrep -P` over alternatives:

| Alternative | Why not |
|---|---|
| `/proc/<pid>/task/<tid>/children` | Linux-only; requires `CONFIG_PROC_CHILDREN`. Adds a `runtime.GOOS` branch we don't need. |
| `ps -A -o pid,ppid` | Scales with the entire process table; we'd parse N rows to find children of one. Wasteful when `pgrep -P` exists. |
| `lsof`-style fd inspection | Targets file descriptors, not parent-child relationships. Wrong tool. |

`pgrep` ships with macOS Mavericks+ and is in coreutils on Linux —
identical surface, identical parsing rules, no GOOS switch.

### Why `pgrepChildren` lives in `auto_attach.go`, not `harness.go`

Co-location with the feature follows the `attach_pty.go` /
`attach_stdio.go` precedent. The architect's spec marks the
"add to `harness.go` once" hint (from #164's body) as **non-binding**:
the policy is "reuse once written," not "live in `harness.go`
specifically." Keeping `auto_attach.go` self-contained keeps the
harness file from growing into a junk drawer; #164 imports
`pgrepChildren` from the auto-attach file.

### Test ordering: round-trip BEFORE pgrep

The test runs the byte round-trip first, then the `pgrep`. By the
time the round-trip succeeds, the foreground pyry has dialed,
attached, and is steady-state in `AttachStdio`'s I/O loop — if it
*had* spawned a claude child during a hypothetical fall-through, the
child would already be in the tree and the assertion catches it.
Reversed ordering would race the pgrep against the foreground pyry's
own boot; sequencing is load-bearing.

### Cleanup ordering

Identical in shape to `StdioAttachClient.teardown`, `sync.Once`-wrapped:

1. `inputW.Close()` — propagates EOF to the foreground pyry's stdin;
   `control.AttachStdio`'s input loop returns nil and the conn closes.
2. Wait on `foregroundDone` ≤2s; on timeout, `killSpawned` the
   foreground process.
3. `outputR.Close()` — releases any in-flight `ReadUntil`.
4. `killSpawned(daemonCmd)` — SIGTERM the daemon.
5. `os.Remove(socketPath)` — defensive against SIGKILL paths skipping
   pyry's own socket cleanup.

### Error handling

| Failure | Behaviour |
|---|---|
| `os.Pipe()` returns error | `t.Skipf` (heavily sandboxed CI) |
| Daemon spawn / readiness / `sessions.new` failure | `t.Fatalf` with daemon stderr |
| Foreground pyry exits within 500ms settle window | `t.Fatalf` with exit code + foreground stderr + daemon stderr |
| `ReadUntil` deadline elapses | `t.Fatalf` with seen bytes + foreground stderr (catches "fall-through spawned a real claude that errored on stream-json input" regressions legibly) |
| `pgrepChildren` returns error (pgrep absent) | `t.Skip` |
| `pgrepChildren` returns non-empty | `t.Fatalf` listing the child PIDs |

### What this slice does not verify

- **Fallback paths** (no session / no daemon / `PYRY_NO_AUTO_ATTACH=1`)
  — sibling #164 (`auto_attach_fallback_test.go`).
- **Multiple-clients-on-one-session** — out of #158's scope entirely.
- **Latency / perf assertions** — the 5s `ReadUntil` deadline is a
  liveness check, not a tight latency budget.
- **Foreground pyry stderr is empty** — the auto-attach branch produces
  no human-affordance stderr (inherited from `--stdio` mode), but a
  non-empty stderr would be a diagnostic for a different bug. Surfaced
  in failure diagnostics; not asserted on success.

### Production diff is zero

Two new files, ~365 LOC: `internal/e2e/auto_attach.go` (~290) +
`internal/e2e/auto_attach_happy_test.go` (~50). No edits to existing
files. Default `go test ./...` unaffected. `go test -tags e2e -race
./internal/e2e/...` clean.
