# Failed-Start Pattern (`StartExpectingFailureIn`)

`StartExpectingFailureIn(t, home) RunResult` is the failure-side sibling of
`StartIn`. The caller pre-populates HOME with state designed to make pyry
refuse to come up (e.g. a corrupt `<home>/.pyry/test/sessions.json`); the
helper spawns pyry, watches the readiness window for an early exit, and
returns the captured exit code + streams. No `Harness` is returned — there
is no live daemon to drive and no socket to clean up.

```go
home, regPath := newRegistryHome(t)
_ = os.WriteFile(regPath, []byte("{not valid json"), 0o600)

res := e2e.StartExpectingFailureIn(t, home)
if res.ExitCode == 0 {
    t.Errorf("exit code = 0, want non-zero (stderr=%s)", res.Stderr)
}
if !bytes.Contains(res.Stderr, []byte("registry")) {
    t.Errorf("stderr does not mention registry: %s", res.Stderr)
}
```

### Internal shape: shared `spawn` helper

`StartIn` and `StartExpectingFailureIn` both forward to an unexported
`spawn(t, home, extraFlags...)` that does the fork + wait-goroutine +
child-env wiring (the body that used to live inline in `StartIn`). #123
generalised `spawn` further: it is now a thin wrapper over a new
`spawnWith(t, home, spawnOpts)` core (see § Rotation Primitive); zero-
value `spawnOpts` reproduces the historical `/bin/sleep 99999` shape, and
`StartRotation` populates the options to swap in fake-claude. `spawn`
deliberately does **not** register `t.Cleanup`, build the `Harness`, or
call `waitForReady` — each caller owns those policies:

- `StartIn` builds the `Harness`, registers cleanup, then waits for ready.
- `StartExpectingFailureIn` runs a select-driven loop bounded by
  `readyDeadline` over `(net.Dial, doneCh, time.After(readyPollGap))`,
  returns `RunResult` populated from `cmd.ProcessState` on `<-doneCh`,
  and tears the daemon down + `t.Fatalf`s on either of the defensive
  branches (daemon unexpectedly came up; deadline elapsed with neither).

The defensive teardown reuses a small `killSpawned(t, cmd, doneCh)` helper
that mirrors `Harness.teardown`'s SIGTERM → `termGrace` → SIGKILL →
`killGrace` escalation. Inlined into a function rather than constructing a
throwaway `Harness` for the cleanup path: ~10 lines, no leak risk.

### Why an alternate constructor (not Options on `StartIn`)

The shape was chosen against three alternatives:

| Option                       | Why not                                                              |
|------------------------------|----------------------------------------------------------------------|
| `Options` field on `StartIn` | Forces a polymorphic return — `*Harness` doesn't fit the failure path |
| Lower-level `spawn` helper   | Bigger public surface than the one test needs                         |
| **Alternate constructor**    | Single-purpose; mirrors `Run` / `RunBare`; shared body via private `spawn` |

`StartExpectingFailure(t)` (zero-arg) deliberately not added — the failure
path always wants caller-supplied HOME (to seed the on-disk failure state),
so the `In` suffix is the only useful shape. Adding the no-`In` form would
be unused surface.

### Constants reuse

Reuses the existing `readyDeadline = 5 * time.Second` and `readyPollGap`.
The corrupt-registry path exits in milliseconds (synchronous JSON parse),
so 5 seconds is generous; no new constant.

### `startup_test.go` — `TestE2E_Startup_CorruptRegistryFailsClean` (#111)

Lives in its own file rather than extending `restart_test.go` — domain is
*startup failure*, not *restart survival*. Future startup-shaped e2e tests
(missing claude binary, unreachable workdir, port-in-use socket) have a
natural home next to it.

The test reuses `newRegistryHome(t)` from `restart_test.go` (same package,
same `e2e` build tag), seeds `<home>/.pyry/test/sessions.json` with
`{not valid json`, calls `StartExpectingFailureIn`, then asserts:

| Assertion | What it pins |
|---|---|
| `res.ExitCode != 0` | Daemon refused to come up. Any non-zero is sufficient — exit code is not over-specified. |
| `bytes.Contains(res.Stderr, []byte("registry"))` | Operator-facing diagnostic still names the failing subsystem. |
| `bytes.Equal(diskBytes, corrupt)` | Daemon left the corrupt file untouched on disk. |

The byte-equal assertion is the load-bearing one — it catches the
worst-possible regression ("corrupt file → empty registry → drop
everything") without depending on JSON-parsing the corrupt input. The
substring `registry` is chosen over the path or `sessions.json` because
the path varies per run and `sessions.json` is just the filename, while
"registry" is the domain concept the operator needs to recognise. The
production error chain happens to contain `registry` twice (`pool init:
sessions: load registry: registry: parse <path>: <unmarshal err>`); a
future refactor that changes the wrap chain but still names "registry"
keeps the test green; one that loses the word fails loudly — the right
outcome (operator diagnostic regressed).

### Coverage of the helper's defensive branches

The test exercises only the success path of `StartExpectingFailureIn` (the
child exits before ready). The two `t.Fatalf` branches — "daemon
unexpectedly came up" and "neither exit nor readiness within
`readyDeadline`" — are defensive and would only trigger on a production
regression (corrupt JSON stops failing) or a hung test environment. No
unit tests added for them; per the ticket's "exercised exclusively by this
test" constraint, they earn their keep as crash-loud guards, not as
behaviours under coverage. Future failed-start tests that reuse the helper
provide additional implicit coverage as they land.

### `startup_test.go` — `TestE2E_Startup_MissingClaudeProjectsDir` (#112)

Positive-outcome sibling of the corrupt-registry test — same file, opposite
verdict. A first-run user has never invoked `claude`, so
`~/.claude/projects/` does not exist. The reconcile path's `MissingDir`
branch (`internal/sessions/pool.go`) treats `os.Stat` returning
`fs.ErrNotExist` as "no transcripts to reconcile," not as an error; the
daemon must come up with an empty registry. Unit tests already cover this;
the e2e adds binary-boundary proof.

Sketch:

```go
func TestE2E_Startup_MissingClaudeProjectsDir(t *testing.T) {
    home, err := os.MkdirTemp("", "pyry-mp-*")
    if err != nil { t.Fatalf("mkdir home: %v", err) }
    t.Cleanup(func() { _ = os.RemoveAll(home) })

    claudeProjects := filepath.Join(home, ".claude", "projects")
    if _, err := os.Stat(claudeProjects); !errors.Is(err, fs.ErrNotExist) {
        t.Fatalf(".claude/projects/ unexpectedly exists at %s (err=%v); test premise invalidated",
            claudeProjects, err)
    }

    h := StartIn(t, home)
    r := h.Run(t, "status")
    if r.ExitCode != 0 {
        t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
            r.ExitCode, r.Stdout, r.Stderr)
    }
    h.Stop(t)
}
```

| Assertion | What it pins |
|---|---|
| `fs.ErrNotExist` on `<home>/.claude/projects/` | Test premise: the missing-dir case is what's actually under test. If a future harness change pre-creates that directory, this test fails loudly instead of silently passing on a different path. |
| `Start`/`StartIn` returns | Daemon reaches ready with the missing dir — the `MissingDir` branch did not return an error up the stack. |
| `pyry status` exit 0 | Control socket is responsive; the daemon is functional, not just up. |
| `h.Stop(t)` | Shutdown is verdict-bearing: explicit `Stop` surfaces shutdown errors at the assertion point rather than from `t.Cleanup` after the test has already passed. |

No log-line assertion: production may or may not log the no-op, and tying
the test to a specific line would lock production into emitting it.

#### Why `StartIn` + `os.MkdirTemp` instead of `Start(t)` + `t.TempDir()`

`Start(t)` would suffice for the missing-dir case in principle (a fresh
`t.TempDir()` HOME has no `.claude/projects/` by construction). Two reasons
to use `StartIn` + `os.MkdirTemp` here:

1. **`sun_path` budget.** `TestE2E_Startup_MissingClaudeProjectsDir` is a
   long test name; `t.TempDir()` embeds it into the path and overflows
   macOS's 104-byte socket-path limit. `os.MkdirTemp("", "pyry-mp-*")` keeps
   the prefix tiny — same lesson the restart tests apply.
2. **Caller-owned cleanup.** The failed-start test next door uses
   `os.MkdirTemp` + `t.Cleanup(os.RemoveAll)` for the same reason. Keeping
   the two startup tests structurally similar makes the file scannable.

#### Why explicit `Stop(t)` despite `t.Cleanup`

`StartIn(t, home)` registers `h.teardown` via `t.Cleanup`, which handles
process liveness and socket removal. But cleanup runs *after* the test
function returns, so any `t.Logf` about a stuck shutdown gets attributed
"after the test." Calling `h.Stop(t)` inside the test body makes "shuts
down cleanly" a verdict-bearing step. `cleanupOnce` (existing `sync.Once`)
makes this idempotent with the cleanup hook — the second fire is a no-op.

#### Why not a table-driven test combining both startup cases

The two startup tests assert opposite outcomes: ready+responsive vs.
exit-before-ready. They use different harness entry points (`StartIn` vs.
`StartExpectingFailureIn`) returning different types (`*Harness` vs.
`RunResult`). A table that switches on outcome shape is more code than two
flat tests. Per #111's spec and #112's guidance, keep them flat.

#### Production diff is zero

The `MissingDir` branch already exists in `internal/sessions/pool.go`. This
ticket adds binary-boundary coverage; no harness changes either. Test diff
is one new test in the existing `startup_test.go`, ~25 LOC.
