# #163 — Phase 1.3c-2-e2e-happy: foreground auto-attach happy path

E2E coverage of the success branch added by #158 (foreground binary
auto-attach). Test-only ticket: zero production-code edits.

`pyry --session-id <uuid> …` invoked while a daemon hosts that UUID
must (1) dispatch to `control.AttachStdio` instead of spawning a
supervised claude, (2) round-trip bytes through the parent's plain
`os.Pipe()` stdio, and (3) leave no claude child process behind in
the foreground binary's process tree. All three bullets become a
single Go test in `internal/e2e/`.

The fallback scenarios (no session, no daemon, env override) are
covered by sibling **#164**. The process-tree inspection helper added
here is shared with #164 verbatim — that ticket's body explicitly
calls for reuse, so this spec keeps the helper's surface narrow and
sibling-friendly.

This is **not** blocked by #167 (`pyry attach --stdio` rejected by
`parseClientFlags`). #167 affects only the `attach --stdio` verb.
The auto-attach branch in `tryAutoAttach` calls `control.AttachStdio`
**directly** from inside the foreground binary's `runSupervisor`,
bypassing the verb-dispatch / `parseClientFlags` / `parseAttachArgs`
chain entirely. #167's bug doesn't reach this code path. (See the
cross-check at the end of *Files to read first*.)

## Files to read first

The developer's turn-1 reading list. Pull from these to avoid
re-discovering the harness shape and the auto-attach contract.

- `internal/e2e/attach_stdio.go` (full, ~265 lines) — the closest
  precedent. `startStdioAttach` is the template the new
  `startForegroundAutoAttach` mirrors: probe `os.Pipe`, allocate a
  short-prefix `MkdirTemp` home (sun_path-safe), `spawnAttachableDaemon`,
  `waitDaemonReady`, `control.SessionsNew`, spawn the child with pipes
  on stdio, register `t.Cleanup` once with the same teardown ordering.
  The new helper changes only the `exec.Command` line — everything
  else is structurally identical.
- `internal/e2e/attach_pty.go:151-217` (`spawnAttachableDaemon` +
  `waitDaemonReady`) — the daemon side. Spawns pyry in bridge mode
  with the e2e test binary as supervised claude in echo mode
  (`GO_TEST_HELPER_PROCESS=1 GO_TEST_HELPER_MODE=echo`). Already
  used by both PTY and stdio harnesses; reuse verbatim.
- `internal/e2e/attach_pty.go:114-122` — the established attach-cmd
  spawn shape (binary path, `-pyry-socket=`, env via `childEnv(home)`,
  `cmd.Start`, wait goroutine). The new helper diverges only at the
  `exec.Command` argv.
- `internal/e2e/attach_stdio_test.go:26-56` (`TestE2E_AttachStdio_BytesRoundTrip`)
  — the byte-flow test it parallels. Same nonce-tagged payload pattern,
  same `ReadUntil` call shape, same 5s deadline. The #163 test omits
  the `t.Skip("blocked on #167…")` line — see the next bullet.
- `cmd/pyry/main.go:266-308` (`tryAutoAttach`) — the production-side
  branch under test. The auto-attach branch calls `control.AttachStdio`
  **directly**, not via `runAttach` / `parseAttachArgs` / `parseClientFlags`.
  This is why #167 (which breaks the `attach --stdio` verb) does not
  block #163 — the bug is in a code path the test never enters.
  Skim this function to confirm: it stats the socket, dials
  `SessionsHasID`, then calls `control.AttachStdio` from inside
  `runSupervisor`, then returns. The verb-dispatch in `run()` never
  runs.
- `cmd/pyry/main.go:191-237` (`splitArgs`) — confirms that
  `--session-id <uuid>` after pyry's own flags lands in `claudeArgs`,
  which is where `extractSessionID` reads from. The test's argv shape
  (`pyry -pyry-socket=… --session-id <uuid> …`) is correct by
  construction: nothing after `-pyry-socket=…` is a known pyry-* flag,
  so the first non-pyry arg tips into claude territory and the
  `--session-id` lookup succeeds.
- `internal/e2e/harness.go:418-432` (`childEnv`) — env scrubbing
  rules. Strips `HOME` and `PYRY_NAME` from the parent env, sets
  `HOME=<test-home>`. We append nothing extra; the foreground pyry
  child must not inherit any operator env that would change its
  behaviour.
- `internal/e2e/attach_pty_test.go:154-160` (`tinyNonce`) — the
  4-byte-hex random suffix used for round-trip payloads so concurrent
  test runs don't pattern-match each other.
- `internal/control/client.go:106` (`SessionsNew(ctx, socket, label) (string, error)`)
  — the wire client that returns the freshly-allocated session UUID.
  The label is opaque ("auto-attach-happy" or similar).
- `docs/specs/architecture/158-foreground-auto-attach.md` § "Auto-attach
  flow" + § "Affordance" — confirms that the auto-attach branch is
  byte-identical to `pyry attach --stdio` on the dispatch tail (same
  `control.AttachStdio` call, same stdin/stdout wiring, same
  `createIfMissing=false`) **and** that the `--stdio` mode suppresses
  pyry's human-affordance stderr lines. Round-trip semantics carry
  over verbatim.
- `docs/specs/architecture/161-e2e-stdio-attach-harness.md` (full)
  — the parent harness spec for `startStdioAttach`. The reuse policy
  is "same skeleton, different child argv".
- `docs/lessons.md` § "macOS sun_path 104-byte limit" — the
  `MkdirTemp("", "pyry-as-*")` short prefix in `startStdioAttach` is
  load-bearing on macOS. The new helper inherits the same constraint;
  use the same prefix style (e.g. `pyry-aa-*`).

**Cross-check before coding.** Confirm `tryAutoAttach`'s call to
`control.AttachStdio` happens *inside* the foreground binary process
(not via `os/exec` of `pyry attach --stdio`). It does — `cmd/pyry/main.go:304`
is `control.AttachStdio(context.Background(), socketPath, id, os.Stdin, os.Stdout, false)`.
This is the in-process bridge call. #167 cannot reach it.

## Context

Phase 1.3c-2 (#158) shipped the foreground auto-attach gate. Today
it is exercised only by unit tests in `cmd/pyry/auto_attach_test.go`,
which stop at the `SessionsHasID` decision and don't cross the
process boundary. The acceptance criterion *"pointing Claudian at
pyry Just Works against a running daemon"* is end-to-end — it
involves a foreground binary process, a real Unix socket, the
control wire, the bridge, the supervised echo helper, and bytes
flowing back. Only an e2e test can mechanically verify that.

The test must answer two questions a unit test can't:

1. **Does `pyry --session-id <uuid>` (no `attach` verb) actually
   reach `control.AttachStdio` when the daemon hosts the UUID?**
   The verb-dispatch in `run()` doesn't match `--session-id`, so the
   path through `runSupervisor` → `tryAutoAttach` is the only one
   that can take the attach branch. A binary-level test pins that.
2. **Does the foreground binary leave a claude child behind?** A
   bug that wires auto-attach correctly *and* spawns a competing
   supervisor would still pass any byte-flow assertion (the daemon's
   echo helper returns bytes regardless of the foreground pid's
   children). The process-tree inspection is the orthogonal
   assertion that catches that class of regression.

Both questions are mechanical. The test is one round-trip plus one
`pgrep -P`.

## Design

### Files added

```
internal/e2e/auto_attach.go              ~120 LOC  test-helper file
internal/e2e/auto_attach_happy_test.go    ~65 LOC  the actual test
```

Both files build-tagged `//go:build e2e` (test-helper file uses the
same tag as `attach_stdio.go` — no `e2e_install` because no
install-tagged tests reuse it).

The helper file holds two surfaces:

1. `startForegroundAutoAttach(t, label)` — the harness constructor.
2. `pgrepChildren(pid) ([]int, error)` — the process-tree inspection
   primitive that #164 will also import.

No additions to `harness.go`. Co-locate with the auto-attach feature
to keep the harness file from growing into a junk drawer; #164's
hint to "add it to harness.go once" is non-binding (the policy is
"reuse once written", not "live in harness.go specifically").

### `startForegroundAutoAttach` — surface

```go
// ForegroundAutoAttachClient is a programmatic peer for
// `pyry --session-id <uuid>` invoked as the foreground binary while
// a daemon already hosts the UUID. Wired via plain os.Pipe() — no
// PTY, no terminal, no raw mode. Mirrors StdioAttachClient's surface
// so tests share the Write / ReadUntil / Close contract.
//
// The crucial difference from StdioAttachClient: the spawned process
// is `pyry --session-id <uuid> …` (no `attach` verb), exercising the
// auto-attach gate in tryAutoAttach. control.AttachStdio is called
// in-process by the foreground pyry, not via the `pyry attach --stdio`
// verb (which is blocked on #167).
type ForegroundAutoAttachClient struct {
    SessionID  string         // UUID returned by control.SessionsNew
    SocketPath string
    HomeDir    string
    Stderr     *bytes.Buffer  // foreground pyry's stderr; expected empty in steady state

    // Pid is exposed so the test (and #164's siblings) can call
    // pgrepChildren(Pid) for the no-claude-child assertion.
    Pid int

    // unexported plumbing identical in shape to StdioAttachClient:
    //   inputW, outputR (parent ends of pipes)
    //   daemonCmd / daemonDone / daemonErr
    //   foregroundCmd / foregroundDone
    //   cleanupOnce sync.Once
}

// startForegroundAutoAttach brings up a pyry daemon (helper-as-claude
// echo mode), creates a session via control.SessionsNew with `label`,
// then spawns a SECOND pyry process invoked as the foreground binary
// (`pyry -pyry-socket=<sock> -- --session-id <uuid>
//        --input-format stream-json --output-format stream-json`)
// with stdin/stdout wired to plain os.Pipe()s.
//
// Returns once the foreground process has been alive past a 500ms
// settle window without exiting (mirrors startStdioAttach's
// early-death detector). Test then writes/reads via the returned
// client.
//
// Skips on os.Pipe() failure (heavily-sandboxed CI). Fatals on any
// other startup error.
func startForegroundAutoAttach(t *testing.T, label string) *ForegroundAutoAttachClient
```

The constructor's body is a near-copy of `startStdioAttach` with
exactly one substantive change — the `exec.Command` line:

```go
// startStdioAttach has:
attachCmd := exec.Command(bin, "attach", "-pyry-socket="+socket, "--stdio", id)

// startForegroundAutoAttach has:
foregroundCmd := exec.Command(bin,
    "-pyry-socket="+socket,
    "--",                                    // explicit; defensive
    "--session-id", id,
    "--input-format", "stream-json",
    "--output-format", "stream-json",
)
```

Notes on the argv shape:

- **No `attach` verb.** The whole point of the test: exercise the
  *foreground binary* path, not the verb path. `os.Args[1]` is
  `-pyry-socket=…`, which doesn't match any verb in `run()`'s
  switch, so `runSupervisor(os.Args[1:])` runs and `tryAutoAttach`
  fires.
- **`--` is explicit.** `splitArgs` doesn't require it (the first
  non-pyry-flag arg tips into claudeArgs naturally), but the
  separator pins the test's intent — *"these are claude flags, not
  pyry flags"* — and is forward-compatible if pyry ever adds a flag
  whose name overlaps. Cheap insurance.
- **The `--input-format stream-json --output-format stream-json`
  flags are present** to mirror the AC's literal shape (Claudian's
  invocation pattern). Functionally inert in this test: auto-attach
  fires before any claude binary is consulted, so the flags are
  never parsed by anything. Including them documents the intent
  *"this is the Claudian-shape invocation"* and guards against a
  future regression where pyry sniffs claude flags client-side.
- **No `-pyry-claude` flag is set on the foreground binary.** The
  default (`/usr/bin/env claude` or similar) is fine because we
  expect auto-attach to fire and *no claude spawn to happen*. If a
  bug causes fall-through, the supervised spawn will fail trying to
  exec a claude binary that isn't on the test runner's PATH; the
  byte round-trip will time out; the test fails with a clear stderr
  diagnostic. Not setting `-pyry-claude` to a deliberately-bogus
  path is intentional: it keeps the test diagnostic close to the
  real failure (no claude available) instead of masking it with
  `/bin/false` weirdness.

### Daemon shape (unchanged)

Reuses `spawnAttachableDaemon` verbatim. The daemon supervises the
e2e test binary (`os.Args[0]`) running `TestHelperProcess` in echo
mode, which round-trips bytes line-by-line. Identical to the
existing PTY and stdio attach tests; nothing new to design.

### `pgrepChildren` — surface and shape

```go
// pgrepChildren returns the PIDs whose direct parent is pid, or an
// error if the inspection mechanism is unavailable on this platform
// (caller should t.Skip).
//
// macOS / Linux:
//   pgrep -P <pid>
//
// pgrep is in coreutils on Linux and ships with macOS Mavericks+.
// Out of scope: BSDs, Solaris, Windows.
func pgrepChildren(pid int) ([]int, error)
```

Implementation is ~25 LOC: `exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()`,
parse newline-separated PIDs, return `[]int`. `pgrep` exits 1 with
empty output when no children exist — the function returns
`(nil, nil)` for that case (it's the success path for #163; the
failure path for #164's fallback tests).

#### Why `pgrep -P`, not `/proc/<pid>/task/<tid>/children` or `ps -A -o pid,ppid`

- **`pgrep -P` is one syscall and one parse.** Cross-platform on the
  two GOOS the test suite supports (linux, darwin). Same surface,
  same parsing rules. No `runtime.GOOS` switch.
- **`/proc/<pid>/task/<tid>/children`** is Linux-only and requires
  `CONFIG_PROC_CHILDREN` (default y, but not contractual). Adds a
  GOOS branch we don't need.
- **`ps -A -o pid,ppid`** scales with the entire process table —
  we'd parse N rows to find children of one. Wasteful when `pgrep -P`
  exists.
- **`lsof`-style inspection** (the pattern in
  `attach_stdio_no_pty_test.go`) targets file descriptors, not
  parent-child relationships. Wrong tool.

`pgrep` not on PATH is exceedingly rare on modern Linux/macOS but
should be handled: `exec.LookPath("pgrep")` failure → return an
error the caller turns into `t.Skip` (matches the precedent in
`openPTYDeviceTargetsDarwin` for `lsof`).

### The test

```go
//go:build e2e

package e2e

import (
    "bytes"
    "testing"
    "time"
)

// TestE2E_ForegroundAutoAttach_AttachesWhenDaemonHasSession proves
// that `pyry --session-id <uuid> …` invoked while a daemon hosts that
// UUID dispatches to control.AttachStdio (no claude spawn). Asserts:
//
//   1. Bytes written to the foreground pyry's stdin pipe round-trip
//      through control socket → bridge → supervisor PTY → echo helper
//      and back through the foreground pyry's stdout pipe.
//   2. The foreground pyry process has zero direct children
//      (process-tree inspection via pgrep -P). Auto-attach is a
//      stdio-bridge — no exec, no PTY, no goroutine that forks.
//
// Skips when os.Pipe() is unavailable or pgrep is missing
// (matches the harness's existing skip discipline).
func TestE2E_ForegroundAutoAttach_AttachesWhenDaemonHasSession(t *testing.T) {
    c := startForegroundAutoAttach(t, "auto-attach-happy")

    payload := []byte("pyry-auto-attach-" + tinyNonce() + "\n")
    if _, err := c.Write(payload); err != nil {
        t.Fatalf("write: %v", err)
    }
    seen, err := c.ReadUntil(payload, 5*time.Second)
    if err != nil {
        t.Fatalf("did not observe payload back: %v\nstderr:\n%s",
            err, c.Stderr.String())
    }
    if !bytes.Contains(seen, payload) {
        t.Fatalf("ReadUntil returned without payload: %q", seen)
    }

    // Process-tree assertion. Run AFTER the round-trip succeeds —
    // by then we know the foreground pyry has dialed, attached, and
    // is steady-state in AttachStdio's I/O loop. If it had spawned
    // a claude child during runSupervisor's fall-through path, the
    // child would already be in the tree.
    children, err := pgrepChildren(c.Pid)
    if err != nil {
        t.Skipf("e2e: pgrep unavailable: %v", err)
    }
    if len(children) > 0 {
        t.Fatalf("foreground pyry pid=%d has children %v; expected zero (auto-attach should not spawn)\nstderr:\n%s",
            c.Pid, children, c.Stderr.String())
    }
}
```

Total test body: ~30 LOC. Helper file: ~120 LOC. No production
edits, no new exported types in production code.

### Concurrency

No new goroutines beyond what `startStdioAttach` already establishes:

- One goroutine waiting on the foreground pyry's `cmd.Wait()`,
  closing `foregroundDone` on exit. Identical to the pattern in
  `startStdioAttach`.
- One read goroutine inside `ReadUntil` (carried over verbatim).
- Inside the foreground pyry process, `control.AttachStdio` runs
  one output-copy goroutine joined before return — already covered
  by #154's contract.

Strictly sequential at the test level: spawn daemon, wait ready,
sessions.new, spawn foreground pyry, settle 500ms, write, read,
pgrep, teardown.

### Error handling

| Failure mode | Outcome |
|---|---|
| `os.Pipe()` fails | `t.Skip` (sandboxed CI). Inherited from `startStdioAttach`. |
| Daemon spawn / readiness fail | `t.Fatalf` with daemon stderr (inherited). |
| `control.SessionsNew` fails | `t.Fatalf` with the wire error (inherited). |
| Foreground pyry exits within 500ms settle window | `t.Fatalf` with exit code + foreground stderr + daemon stderr. Mirrors the early-death detector in `startStdioAttach`. |
| `ReadUntil` deadline elapses without seeing payload | `t.Fatalf` with seen bytes + foreground stderr. Diagnostic includes the full output buffer so a "spawned a real claude that errored on stream-json input" regression is legible. |
| `pgrepChildren` returns error (pgrep absent) | `t.Skip` with the LookPath error. |
| `pgrepChildren` returns non-empty | `t.Fatalf` listing the child PIDs. The diagnostic is sufficient: the only thing pyry-as-foreground-binary should ever spawn is a supervised claude. |

### Teardown ordering

Identical to `StdioAttachClient.teardown`:

1. Close foreground pyry's stdin pipe (`inputW.Close()`) — sends EOF
   to its input loop, which propagates through `control.AttachStdio`
   and exits cleanly.
2. Wait up to ~2s for `foregroundDone`; on timeout, `killSpawned`
   the foreground process.
3. Close `outputR` to unblock any in-flight `ReadUntil`.
4. `killSpawned` the daemon.
5. `os.Remove(socketPath)` (best-effort).

Wrapped in `sync.Once` so a manual `Close(t)` and `t.Cleanup` don't
double-fire. The whole helper is a copy-and-tweak of
`startStdioAttach`'s teardown.

## Testing strategy

The test in this ticket *is* the e2e test. No further unit-level
coverage is added — the auto-attach logic itself is already
unit-tested in `cmd/pyry/auto_attach_test.go` (#158), and the
control wire is covered by `internal/control/*_test.go`.

Verification commands:

```bash
go test -tags e2e -race -run TestE2E_ForegroundAutoAttach_AttachesWhenDaemonHasSession ./internal/e2e/
go test -tags e2e -race ./internal/e2e/...   # AC bullet: full suite clean
```

The full-suite run is the actual AC. The targeted run is for the
developer iterating.

## Open questions

None. The scaffolding is mechanical: one harness helper that
clones `startStdioAttach` with one different argv, one process-tree
helper that wraps `pgrep -P`, one test that writes one nonce-tagged
line and reads it back. Every primitive consumed (`spawnAttachableDaemon`,
`waitDaemonReady`, `control.SessionsNew`, `tryAutoAttach`,
`control.AttachStdio`, the echo helper) is shipped and tested.

If the developer hits "the foreground pyry exits immediately with
exit code 1 and stderr says `pyry: …`," that is a surfaced bug in
`tryAutoAttach`'s decision or in `control.AttachStdio`'s handshake —
not a test design issue. Surface the stderr in the failure
diagnostic and report up.

If the developer hits "pgrep returns the wait-goroutine's reaped
zombie," that's not a real failure mode — `cmd.Wait()` reaps the
child immediately on exit, and `pgrep` doesn't list zombies on
either Linux or macOS pgrep implementations. (Documented for
peace-of-mind; do not over-engineer.)

## Out of scope

- Fallback paths (no session / no daemon / env override) — **#164**.
- Multiple-clients-on-one-session behaviour — out of #158's scope.
- Latency / perf assertions — not on the AC. The byte round-trip
  uses a 5s deadline (matching the existing harness convention),
  not a tight latency budget.
- Windows / BSD platform support — out of scope for the project.
- Asserting on the foreground pyry's stderr being empty. The auto-
  attach branch produces no human-affordance stderr (inherited from
  `--stdio` mode per #154). A non-empty stderr would be a
  diagnostic for a different bug; surfacing it on every failure
  diagnostic is sufficient and matches the existing harnesses.
- Adding the auto-attach helper to `harness.go`. #164's "add to
  harness.go once" is non-binding — co-location with the feature
  (`auto_attach.go`) is closer to the existing
  `attach_pty.go` / `attach_stdio.go` precedent.

## Documentation

No knowledge-base or feature-doc edits. Test-only ticket. The
existing `docs/knowledge/features/control-plane.md` § "Foreground
binary auto-attach (1.3c-2)" already covers the behaviour under
test; this ticket only mechanically pins it.

`docs/PROJECT-MEMORY.md` gets a one-line entry under Phase 1.3c-2's
e2e coverage when this ticket lands, mirroring the 1.3a /
1.3a-no-pty entries' shape (test name, file path, what it pins).

After editing, run `qmd update && qmd embed` (per CLAUDE.md).

## Production / test diff sizing

| Surface | LOC est. | File(s) |
|---|---|---|
| `ForegroundAutoAttachClient` + `startForegroundAutoAttach` | ~95 | `internal/e2e/auto_attach.go` (new) |
| `pgrepChildren` | ~25 | `internal/e2e/auto_attach.go` (new) |
| `TestE2E_ForegroundAutoAttach_AttachesWhenDaemonHasSession` | ~30 | `internal/e2e/auto_attach_happy_test.go` (new) |
| Write / ReadUntil / Close (copied verbatim from StdioAttachClient) | ~35 | `internal/e2e/auto_attach.go` (new) |
| `docs/PROJECT-MEMORY.md` Phase 1.3c-2 e2e entry | ~3 | `docs/PROJECT-MEMORY.md` |
| **Production total** | **0** | — |
| **Test total** | **~185** | **2 files (both new)** |

Comfortably within the S envelope (0 production LOC, 0 production
files, 0 new exported types, no consumer cascade, no cross-package
coordination). Test code dominates, as it must — this is an e2e
ticket. The LOC count includes ~35 lines of Write / ReadUntil /
Close that are byte-identical to `StdioAttachClient`'s methods; the
developer can either copy them and accept the duplication (cheaper
diff, easier to read in isolation) or extract a tiny shared
struct between the two harnesses (more elegant but invites churn
in #164). The duplication is fine — two ~35-line copies, no shared
test-helper churn — pick the simpler path. **Do not refactor
`StdioAttachClient` to share with the new struct in this ticket.**
That's a separate refactor with its own scope risk; #164 doesn't
need it either.
