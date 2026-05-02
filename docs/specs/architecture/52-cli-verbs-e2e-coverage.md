# Spec: CLI verbs e2e coverage (`stop`, `logs`, `version`, `status` stopped path)

**Ticket:** [#52](https://github.com/pyrycode/pyrycode/issues/52)
**Size:** S
**Depends on:** #68 + #69 (harness already shipped)

## Context

#69 shipped the e2e harness — `Start(t) *Harness` + `Harness.Run(t, verb, args...) RunResult` — and one proof-of-life test, `TestStatus_E2E`. The remaining shipped non-interactive verbs (`stop`, `logs`, `version`, plus `status` against a stopped/missing daemon) have unit-level coverage but no binary-boundary coverage.

This ticket fills the gap. No new harness behaviour is required for three of the four tests. One small additive helper (`RunBare`) is justified by the two cases where spawning a daemon is wasteful or wrong: `version` short-circuits in `main.go` before any socket use, and the `status` stopped-path test is most robustly expressed as "point status at a socket that has no daemon," not "spawn → stop → race the teardown."

`pyry attach` is interactive (PTY) and explicitly out of scope. `install-service` already has `internal/e2e/install_{darwin,linux}_test.go`.

## Design

### Files

| File | Change | Lines (approx) |
|---|---|---|
| `internal/e2e/harness.go` | Add `RunBare(t, args...) RunResult` | +25 |
| `internal/e2e/cli_verbs_test.go` | New file: 4 tests | +110 |

Total: 2 files, ~135 lines under `//go:build e2e`. Default `go test ./...` is unchanged.

### New harness surface: `RunBare`

```go
// RunBare invokes the cached pyry binary with args verbatim — no daemon
// spawn, no auto-injected -pyry-socket. For verbs that don't touch the
// control socket (e.g. `version`) or for negative tests where the caller
// wants to drive a verb against a deliberately-bogus socket path. Reuses
// the same binary cache (`ensurePyryBuilt`) and the same exit-code /
// timeout / capture machinery as Harness.Run.
func RunBare(t *testing.T, args ...string) RunResult
```

Implementation mirrors `Harness.Run` but:
- No `h.SocketPath` injection (caller passes whatever flags they want).
- No `childEnv(h.HomeDir)` — uses the test process env unchanged. The bare verbs we drive (`version`, `status` against a bogus socket) don't read `$HOME`, and adding HOME isolation we don't use is dead weight.
- Same `runTimeout` (10s) and same `*exec.ExitError` → `ExitCode` mapping.

This is the *only* new harness API. `Stop()` mid-test, `Status()` helper, etc. are explicitly **not** added — keep harness surface minimal per ticket's constraint.

### Test 1: `stop` — `TestStop_E2E`

Spawn the daemon. Capture `pid := h.PID` and `sock := h.SocketPath` *before* invoking stop (the harness fields stay valid, but read once for clarity). Run `pyry stop`. Assert:

- `r.ExitCode == 0`.
- `bytes.Contains(r.Stdout, []byte("stop requested"))` — the verb prints `pyry: stop requested` (`cmd/pyry/main.go:491`); assert on the stable fragment, not the full string with whitespace.

`pyry stop` returns once the server has acknowledged the request, but the daemon may still be unwinding its child. So:

- Bounded poll loop, deadline 3s, gap 50ms, check both `!processAlive(pid)` AND `os.Stat(sock)` returning `fs.ErrNotExist`. Both conditions in the same iteration — they don't have to be observed simultaneously, but waiting until both hold avoids racing the supervisor's socket-cleanup defer.
- On deadline: `t.Fatalf` with the observed states for diagnosis.

Reuses `processAlive` from `harness_test.go` (already in the same package).

The harness's `t.Cleanup` will fire after the test returns; with the daemon already gone, `teardown`'s SIGTERM hits ESRCH (silently ignored), the wait goroutine has already closed `doneCh` (immediate select return), and `os.Remove(socket)` is a no-op. No coordination needed — `cleanupOnce` makes this safe.

### Test 2: `status` stopped-path — `TestStatus_E2E_Stopped`

Use `RunBare`, no daemon. Point status at a fresh non-existent socket path:

```go
bogusSock := filepath.Join(t.TempDir(), "no-such.sock")
r := RunBare(t, "status", "-pyry-socket="+bogusSock)
```

Assertions:

- `r.ExitCode != 0` (don't pin to a specific code — `runStatus` uses `fmt.Errorf` which `main` exits with 1, but coupling to that is needless coupling).
- `len(bytes.TrimSpace(r.Stderr)) > 0` — there must be an error message.
- Negative assertion that the failure is *clean*, not a crash:
  - `!bytes.Contains(r.Stderr, []byte("panic"))`
  - `!bytes.Contains(r.Stderr, []byte("goroutine "))` — Go's stack trace header is "goroutine N [state]:".
  - `!bytes.Contains(r.Stderr, []byte("runtime/"))` — Go runtime frames in tracebacks.

These three substrings are deliberately conservative — they catch panics and stack traces without coupling to the exact wording of the dial-failure error message. (Today it surfaces something like `pyry: status: ... connect: no such file or directory`; that string is allowed to evolve.)

**Why bogus socket, not spawn → stop → status:** the spawn-then-stop-then-status path requires the test to wait for the daemon to actually shut down (otherwise status hits a still-listening socket and succeeds, defeating the test). That's the same poll loop as `TestStop_E2E`, plus ordering glue, plus a second `Run` call. The bogus-socket variant exercises the same code path (`net.Dial` fails → error surfaces clean to stderr → non-zero exit) without any timing dependency. Strictly simpler, strictly more deterministic.

### Test 3: `logs` — `TestLogs_E2E`

Spawn the daemon, run `logs`:

```go
h := Start(t)
r := h.Run(t, "logs")
```

Assertions:

- `r.ExitCode == 0`.
- `len(bytes.TrimSpace(r.Stdout)) > 0` — the in-memory ring captures supervisor lifecycle lines from startup (e.g. "pyrycode starting" at `cmd/pyry/main.go:316`), so a healthy daemon's log buffer is never empty by the time `Start(t)` returns ready.

Deliberately *not* asserted: any specific log string. The supervisor's wording is internal and free to evolve; coupling tests to it would invert the right dependency direction.

### Test 4: `version` — `TestVersion_E2E`

`version` short-circuits in `main.go` before any flag parsing (`cmd/pyry/main.go:144`), so it doesn't need the harness's daemon. Use `RunBare`:

```go
r := RunBare(t, "version")
```

Assertions:

- `r.ExitCode == 0`.
- After `bytes.TrimSpace`, the output begins with `pyry ` (literal `"pyry "` prefix — the format from `fmt.Println("pyry", Version)`).
- After stripping that prefix, what remains is non-empty (the version token; `dev` in test builds, a real version in tagged builds).

Use `bytes.HasPrefix` and `bytes.TrimPrefix` against `bytes.TrimSpace(r.Stdout)`. Don't pin the version to `dev` — a tagged build (`-ldflags "-X main.Version=v0.4.0"`) would still satisfy the AC and shouldn't break the test.

### File layout: `cli_verbs_test.go` vs. appending to `harness_test.go`

New file. `harness_test.go` is about *harness behaviour* (smoke, no-leak-on-fatal, the canonical proof-of-life status test). `cli_verbs_test.go` is about *CLI surface coverage*. Splitting keeps each file's purpose legible as we accumulate verbs.

Build tag identical to `harness_test.go`: `//go:build e2e`. (Not `e2e_install` — these tests don't touch the installer.)

`processAlive` stays in `harness_test.go`; the new file imports it via package scope (same package). No need to move or duplicate.

## Concurrency model

None new. Each test:
- Spawns at most one daemon (3 of 4 tests; `version` and `status`-stopped-path don't).
- Drives one CLI verb in-process via `exec.Command`.
- For `stop`, polls the existing `processAlive` probe — POSIX zero-signal, no goroutines.

The harness's existing wait goroutine (closes `doneCh` after `cmd.Wait`) is unchanged. `RunBare` synchronously runs its child via `cmd.Run` and reads buffers after `Run` returns — same race-free pattern as `Harness.Run`.

`t.Parallel` is not requested. The harness builds pyry once per process (`sync.Once`), but each test owns its own `t.TempDir` for HOME isolation, so parallelism is safe in principle. Following the precedent of `TestStatus_E2E` (which is also serial), defer `t.Parallel` to a follow-up if e2e wall-clock becomes an issue.

## Error handling

Negative paths under test:

| Test | Expected failure shape |
|---|---|
| `TestStop_E2E` (deadline) | `t.Fatalf` with `processAlive(pid)` and `os.Stat(sock)` results dumped |
| `TestStatus_E2E_Stopped` | exit != 0, non-empty stderr, no `panic`/`goroutine `/`runtime/` substrings |
| `TestLogs_E2E` | exit 0; non-empty stdout |
| `TestVersion_E2E` | exit 0; output starts with `pyry ` and has a non-empty token |

All four tests on failure dump the captured `Stdout`/`Stderr` and exit code in the `t.Fatalf`/`t.Errorf` message — the existing pattern from `TestStatus_E2E` (`harness_test.go:115-117`).

## Testing strategy

Self-validation:

```bash
go test -tags=e2e -race ./internal/e2e/...
```

Each test exercises a real binary against either a real daemon or a real (failed) dial. There is no mocking. The supervised "claude" is `/bin/sleep infinity` per the harness's existing config, which keeps PTY/child-startup variability out of the assertions.

Manual smoke once before merging:

```bash
go test -tags=e2e -race -run='TestStop_E2E|TestStatus_E2E_Stopped|TestLogs_E2E|TestVersion_E2E' -v ./internal/e2e/...
```

Verify each test passes once and produces the expected stdout/stderr in `-v` mode (sanity check that the assertions actually exercise the surfaces).

## Open questions

None. Implementation is mechanical against the existing harness.

## Out of scope (explicit)

- `pyry attach` e2e (interactive PTY, separate work).
- `pyry install-service` e2e (already covered).
- New harness methods beyond `RunBare` (e.g. `Stop()`, `Status()` typed helper). The ticket's constraint is "coverage, not new harness surface area"; `RunBare` is the minimum addition that makes two tests cleanly expressible.
- Asserting on specific log line content in `TestLogs_E2E` — couples to wording.
- Asserting on specific dial-error wording in `TestStatus_E2E_Stopped` — couples to platform/syscall library.
- `t.Parallel` migration — defer until wall-clock pressure surfaces.
