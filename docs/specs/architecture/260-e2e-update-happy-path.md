# #260 — e2e: `pyry update` happy-path against fake release server

## Files to read first

- `cmd/pyry/update.go:25-209` — `runUpdate`, `updateOptions`, `defaultProbeRestart`, `defaultRunRestart`, `doUpdate`. The `updateOptions` struct (lines 70–84) is the test seam; `doUpdate` (lines 111–209) is the function this test drives directly.
- `cmd/pyry/update_test.go:23-85` — existing `buildTarGzForTest`, `fakeRelease`, `newFakeReleaseServer`. Reuse verbatim — same package, no build tag, automatically visible to the new tagged file.
- `cmd/pyry/update_test.go:90-143` — `TestUpdate_Success`. Mirror the `updateOptions` wiring shape (fetcher BaseURL, releaseBaseURL, executablePath, replace, out, probeRestart, runRestart).
- `internal/update/replace.go:9-64` — `AtomicReplace` semantics. Confirms inode change on success: `os.Rename(tmp, targetPath)` swaps in a fresh inode, so an inode comparison before/after is a reliable structural assertion.
- `internal/update/restart.go:27-37` — `DetectRestartCommand`. Test must pass a `RestartProbe` whose discriminant flags produce a non-nil argv (`LaunchdPlistExists` on darwin, `SystemdUnitExists` on linux), otherwise the wiring silently skips `runRestart` and the test never exercises the restart seam.
- `internal/e2e/harness.go:184-394` — `Start`/`StartIn`/`spawn`/`spawnWith` patterns: childEnv with HOME isolation and `PYRY_NAME` stripped, `-pyry-name=test` + `-pyry-claude=/bin/sleep` + `-pyry-idle-timeout=0` flag set, `99999`-second sleep argv (NOT `infinity` — see lessons.md "/bin/sleep infinity" — macOS BSD sleep rejects it), the wait-goroutine + doneCh pattern, the `waitForReady` socket-dial poll loop with 5-second deadline. Replicate inline (do not import — see § Why an inline spawn helper).
- `internal/e2e/install_darwin_test.go:1-9` — build-tag header `//go:build darwin && e2e_install` and the run-with-tag invocation comment. Mirror the pattern for `e2e_update`.
- `internal/e2e/restart_test.go:34-49,51-115` — `newRegistryHome` (sun_path-safe temp HOME via `os.MkdirTemp("", "pyry-rs-*")`, NOT `t.TempDir()` — the longer test name overflows macOS's 104-byte sun_path limit). The two-daemon restart pattern (`StartIn` → `Stop` → `StartIn`) is the structural analogue of what runRestart performs.
- `docs/lessons.md` § "E2E against the operator's real systemd `--user` / launchd `gui/<uid>`" — explains why this test does NOT touch the operator's real launchd/systemd. Path the test takes instead: spawn pyry directly, restart by killing+respawning the same subprocess.
- `docs/lessons.md` § "Reject hidden env vars added 'just for the test.'" — load-bearing for the seam choice. The drive-`doUpdate`-directly approach was chosen over a `PYRY_RELEASE_BASE_URL` env var precisely on this principle.
- `docs/lessons.md` § "PTY Testing" — `cmd.Stdin = nil` makes Go's `os/exec` route stdin from `/dev/null` automatically (no explicit `os.Open(os.DevNull)` needed). This is what satisfies AC#2's "stdin closed" requirement structurally.
- `docs/knowledge/features/pyry-update-command.md` — the command's existing surface. Don't duplicate facts the doc already records; this spec only adds the e2e gate.

## Context

The update mechanism (#184/#186/#187/#189/#190) and post-install smoke check (#203) collectively wired fetch → verify → atomic-replace → restart → smoke. The v0.10.1 supervisor-startup-hang regression slipped past unit tests because the auto-restart added by #190 replaced the manual `launchctl kickstart` step that had given operators a natural moment to run a smoke check. There is no e2e test exercising the `pyry update` subcommand or its restart-into-new-binary path.

This ticket adds one happy-path e2e test that covers the full chain end-to-end against an in-process fake release server. It is the release-acceptance check formalised; it also lays down the reusable fake-release-server scaffolding subsequent failure-path tests will reuse.

## Design

### One file, one test, gated behind `e2e_update`

```
cmd/pyry/update_e2e_test.go    (new — //go:build (darwin || linux) && e2e_update)
```

The test, helpers, and test-only types all live in this single file. Existing `cmd/pyry/update_test.go` is **not modified** — its helpers (`buildTarGzForTest`, `fakeRelease`, `newFakeReleaseServer`) are visible from the new file because both are in `package main` of `cmd/pyry`, and `update_test.go` carries no build tag (so it compiles for any tag, including `e2e_update`).

Run with:

```bash
go test -tags=e2e_update ./cmd/pyry/...
```

Same shape as `e2e_install` for `install_*_test.go`. Default `go test ./...` does not compile the file. CI gets one new job (or one new tag in the existing e2e job) that runs `e2e_update`.

### Why `cmd/pyry`, not `internal/e2e`

Two seams get the test most of the way for free:

1. `doUpdate(ctx, updateOptions)` accepts every input the test needs (`releaseBaseURL`, `fetcher.BaseURL`, `executablePath`, `replace`, `probeRestart`, `runRestart`, `out`). Driving it directly from a same-package test is what `cmd/pyry/update_test.go` already does for the unit-shaped tests; the e2e shape is the same with a real running daemon stapled on.
2. Putting the test in `internal/e2e` would force a binary-exec path. There is no `--release-base-url` flag and adding one (or a `PYRY_RELEASE_BASE_URL` env var) is exactly the "hidden env var added 'just for the test'" pattern lessons.md rejects. The principled alternative is to import the package and call its function — but `cmd/pyry` is `package main` and external packages cannot import it. Therefore the test must live in `cmd/pyry`.

The cost: the `internal/e2e` harness is not reachable, so the spawn/restart/dial scaffolding is replicated inline (~50 lines). This is the same trade `cmd/pyry/update_test.go` already accepted for `buildTarGzForTest`/`fakeRelease`/`newFakeReleaseServer` — see the comment at `update_test.go:23` ("Inline-copied (rather than shared) — the test surface is ~10 lines and an internal/testutil package would be heavier than the duplication").

### Why an inline spawn helper, not a harness extension

The test wants to spawn pyry from `<home>/bin/pyry` (the path AtomicReplace will overwrite), not from `internal/e2e`'s package-cached `binPath`. Making `Start`/`StartIn` take a custom binary path would either grow `spawnOpts` with a new field used by exactly one caller, or add a new exported entry point. The replicated spawn logic is ~50 lines and trivially correct; the harness API stays clean.

### Test outline

```go
//go:build (darwin || linux) && e2e_update

package main

func TestUpdate_HappyPath_E2E(t *testing.T) {
    // 1. Build pyry once into a per-test temp dir (or reuse PYRY_E2E_BIN if set).
    //    Same env-var short-circuit shape as internal/e2e/harness.go's
    //    ensurePyryBuilt — supports CI prebuild without editing the spec.
    srcBin := buildPyryBin(t)

    // 2. Sun_path-safe temp HOME (mkdtemp under /tmp, not t.TempDir()).
    //    macOS APFS's 104-byte sun_path limit forbids the long t.TempDir() name
    //    when extended with /pyry.sock. See restart_test.go:newRegistryHome.
    home, err := os.MkdirTemp("", "pyry-up-")
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = os.RemoveAll(home) })

    // 3. Install srcBin → targetPath. AtomicReplace requires the parent dir to
    //    exist and to be writable; <home>/bin/ both holds.
    targetPath := filepath.Join(home, "bin", "pyry")
    if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil { t.Fatal(err) }
    copyFile(t, srcBin, targetPath, 0o755)
    inodeBefore := inodeOf(t, targetPath)

    // 4. Spawn the pre-update daemon from targetPath. Stdin defaults to nil →
    //    Go's exec wires /dev/null. childEnv mirrors the harness: HOME isolated,
    //    PYRY_NAME stripped (operator alias must not leak into the child).
    socket := filepath.Join(home, "pyry.sock")
    cmd1, done1 := spawnDaemon(t, targetPath, home, socket)
    if err := waitForSocket(t, socket, done1, 5*time.Second); err != nil {
        t.Fatalf("daemon 1: %v\nstderr: %s", err, /* captured */)
    }
    pidBefore := cmd1.Process.Pid

    // 5. Build fake-release artefacts. The "new" binary is the same srcBin
    //    bytes re-tarred — happy path needs a different inode and a working
    //    binary that responds to version/status/sessions list, not a different
    //    Version string. asset, tgz, sums = fakeRelease(...) uses the existing
    //    helper from update_test.go (same package, no build tag).
    newBytes, err := os.ReadFile(srcBin)
    if err != nil { t.Fatal(err) }
    asset, tgz, sums := fakeRelease(t, "v999.0.0", runtime.GOOS, runtime.GOARCH, newBytes)
    srv := newFakeReleaseServer(t, "v999.0.0", asset, tgz, []byte(sums))

    // 6. The runRestart seam: kill daemon 1, respawn from targetPath (which by
    //    now points at the new bytes), wait until the new socket is dialable.
    //    The argv that DetectRestartCommand passes here is ignored — the test
    //    is exercising what the supervisor-restart MUST do, not which CLI it
    //    invokes. Real launchctl/systemctl is not on the path the test takes.
    var cmd2 *exec.Cmd
    var done2 chan struct{}
    runRestart := func(ctx context.Context, _ []string) error {
        if err := stopDaemon(t, cmd1, done1, socket); err != nil { return err }
        cmd2, done2 = spawnDaemon(t, targetPath, home, socket)
        return waitForSocket(t, socket, done2, 5*time.Second)
    }

    // 7. Drive the full update flow. probeRestart returns a probe that produces
    //    non-nil argv on the running platform so runRestart actually fires.
    //    The argv content is whatever DetectRestartCommand emits; we don't care.
    var out bytes.Buffer
    err = doUpdate(t.Context(), updateOptions{
        currentVersion: "0.0.1",
        goos:           runtime.GOOS,
        goarch:         runtime.GOARCH,
        repo:           "pyrycode/pyrycode",
        releaseBaseURL: srv.URL + "/releases/download",
        fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
        executablePath: func() string { return targetPath },
        replace:        update.AtomicReplace,
        out:            &out,
        probeRestart: func() update.RestartProbe {
            return update.RestartProbe{
                LaunchdPlistExists: runtime.GOOS == "darwin",
                SystemdUnitExists:  runtime.GOOS == "linux",
                UID:                strconv.Itoa(os.Getuid()),
            }
        },
        runRestart: runRestart,
    })
    if err != nil {
        t.Fatalf("doUpdate: %v\n--- output ---\n%s", err, out.String())
    }
    t.Cleanup(func() { _ = stopDaemon(t, cmd2, done2, socket) })

    // 8. AC#1 — atomic replace happened: inode changed.
    inodeAfter := inodeOf(t, targetPath)
    if inodeAfter == inodeBefore {
        t.Errorf("inode unchanged after AtomicReplace: %d", inodeBefore)
    }

    // 9. AC#1 — daemon was restarted: PID changed.
    pidAfter := cmd2.Process.Pid
    if pidAfter == pidBefore {
        t.Errorf("daemon PID unchanged after restart: %d", pidBefore)
    }

    // 10. AC#1 / AC#2 — pyry status succeeds against the new binary, and
    //     Phase advanced past `starting`. Run via subprocess against the
    //     post-update daemon's socket.
    statusRes := runVerb(t, targetPath, home, socket, "status")
    if statusRes.ExitCode != 0 {
        t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
            statusRes.ExitCode, statusRes.Stdout, statusRes.Stderr)
    }
    if !bytes.Contains(statusRes.Stdout, []byte("Phase:")) {
        t.Fatalf("pyry status missing Phase: line:\n%s", statusRes.Stdout)
    }
    // Tighter v0.10.1 guard: Phase must NOT be `starting` — that's exactly the
    // value the supervisor would be stuck on under the regression.
    if bytes.Contains(statusRes.Stdout, []byte("Phase: starting")) {
        t.Fatalf("post-update daemon stuck at Phase: starting (v0.10.1 regression):\n%s",
            statusRes.Stdout)
    }

    // 11. AC#1 / AC#2 — pyry sessions list succeeds. The exact registry
    //     content is not asserted (the harness's bootstrap is implementation
    //     detail); the assertion is "exit 0 within the harness timeout".
    listRes := runVerb(t, targetPath, home, socket, "sessions", "list")
    if listRes.ExitCode != 0 {
        t.Fatalf("pyry sessions list exit=%d\nstdout:\n%s\nstderr:\n%s",
            listRes.ExitCode, listRes.Stdout, listRes.Stderr)
    }
}
```

### Helper inventory

All in the same file, all `_test.go`-scoped, all behind the `e2e_update` build tag.

| Helper | Lines | Purpose |
|---|---|---|
| `buildPyryBin(t) string` | ~15 | Honours `PYRY_E2E_BIN`; otherwise `go build -o <tmp>/pyry github.com/pyrycode/pyrycode/cmd/pyry`. Mirrors `internal/e2e/harness.go:ensurePyryBuilt` shape (without the `sync.Once` since one test = one call). |
| `copyFile(t, src, dst, mode)` | ~10 | `os.ReadFile` + `os.WriteFile`. The new binary is small enough that a streaming copy is overkill. |
| `inodeOf(t, path) uint64` | ~8 | `os.Stat` + `Sys().(*syscall.Stat_t).Ino`. Linux + macOS only — matches the build tag. |
| `spawnDaemon(t, bin, home, socket) (*exec.Cmd, chan struct{})` | ~25 | Builds `exec.Command(bin, -pyry-socket=<sock>, -pyry-name=test, -pyry-claude=/bin/sleep, -pyry-idle-timeout=0, --, 99999)`, wires `cmd.Env` via `childEnv(home)` (HOME replaced, PYRY_NAME stripped — copy from harness.go), captures stdout/stderr into buffers attached to the test, starts a wait goroutine that closes `doneCh`. Returns the running `*exec.Cmd` and `doneCh`. **Stdin is left nil** — Go's exec wires /dev/null, satisfying AC#2's "stdin closed" structurally. |
| `waitForSocket(t, socket, doneCh, timeout) error` | ~20 | Poll-and-dial loop, 50ms gap, deadline-bounded. Short-circuits on `<-doneCh` (daemon exited before ready → return wrapped error including stderr — same shape as `Harness.waitForReady`). |
| `stopDaemon(t, cmd, doneCh, socket) error` | ~20 | SIGTERM → 3s grace → SIGKILL → 1s grace; `os.Remove(socket)` defensively. Mirrors `Harness.teardown`. |
| `runVerb(t, bin, home, socket, verb, args...) RunResult` | ~25 | `exec.CommandContext` with bounded timeout, stdout/stderr captured. Auto-injects `-pyry-socket=<socket>` after the verb. Returns `{ExitCode, Stdout, Stderr}`. Reuses the local-shape `RunResult` (defined inline; ~3 fields). |

Total helper lines: ~120. Test body: ~80. Combined: ~200 lines. Within the slight margin discussed in the size check.

### `RunResult` reuse

A 3-field local struct (`ExitCode int; Stdout, Stderr []byte`). Do NOT import `internal/e2e.RunResult` — that would drag in the harness build tag tree and force tagged-import gymnastics. Three fields, defined once, done.

## Concurrency model

One test goroutine. Two child processes (pre- and post-update pyry daemons), each with one wait-goroutine that closes a per-daemon `doneCh`. `runRestart` runs synchronously on the test goroutine — it kills daemon 1, awaits doneCh1, spawns daemon 2, awaits the new socket. No fan-out, no errgroup, no shared state beyond the `cmd2 *exec.Cmd` / `done2 chan struct{}` pointers the closure assigns to.

`pyry status` and `pyry sessions list` run sequentially, each as its own short-lived `exec.CommandContext`. Their wait goroutines are scoped to `cmd.Run()` — no leaks.

## Error handling

The test fails fast (`t.Fatalf`) on every spawn failure, every dial failure, every non-zero exit it asserts to be zero. Diagnostic output dumps stderr verbatim — same pattern as `install_*_test.go`.

`stopDaemon` returns an error so `runRestart` can propagate "couldn't stop daemon 1 in time" to `doUpdate` cleanly. Inside the restart-failure path, `doUpdate` would wrap it as `update: binary replaced to v999.0.0, but daemon restart failed: ...`. The test would see that error and `t.Fatalf` — clear diagnostic.

`t.Cleanup` registers `stopDaemon(cmd2, done2, socket)`. The test's cleanup MUST handle `cmd2 == nil` (the case where `runRestart` was never called or failed before assigning) — guard with `if cmd2 == nil { return }`.

## Testing strategy

The test IS the test. No unit tests for the inline helpers — they are simpler than the test they support and exist only to make the test readable. If the helpers grow beyond what one test needs, lift them; do not test them in isolation.

Run locally:

```bash
go test -tags=e2e_update -count=1 ./cmd/pyry/...
```

`PYRY_E2E_BIN=$(pwd)/pyry go test -tags=e2e_update ...` short-circuits the per-test `go build`, useful in CI prebuild.

The test should be robust to back-to-back invocations on the same host: every artefact is under `<home>` (mkdtemp), nothing escapes to the operator's `~/.pyry`, `~/Library/LaunchAgents`, `~/.config/systemd/user`, or any system-level path. No `t.Cleanup` should leave stragglers.

## Open questions

- **Should the test pre-populate a registry to amplify the v0.10.1 reproducer?** The pre-update daemon naturally bootstraps a session (mints a UUID, writes `<home>/.pyry/test/sessions.json`). That session survives daemon-1's stop and is what daemon-2 loads on startup — exactly the "bootstrap-loaded-as-evicted" scenario v0.10.1 hung on. **Recommendation: do nothing extra.** The natural bootstrap-then-reload is the reproducer; explicit pre-population would tightly couple the test to the registry schema (the schema is unexported per `restart_test.go:18`'s `registryEntry` comment), buying nothing for the AC.
- **Should `runVerb` use `t.Context()`?** `t.Context()` is the right default but it's not always cancelled before child cleanup; the existing harness uses `context.WithTimeout(context.Background(), runTimeout)` for verb invocations. **Recommendation: copy harness pattern verbatim** — same 10s `runTimeout` constant, same `WithTimeout(Background)`. Predictable across `t.Run` boundaries.
- **Should the test assert specific progress lines from `doUpdate`'s stdout (`==> Updated to v999.0.0.`)?** The unit-shaped `TestUpdate_Success` already asserts every progress line. Repeating those assertions here adds churn without independent signal. **Recommendation: no.** Assert only the e2e-shaped properties (inode, PID, status, sessions list). If `doUpdate` returns nil, the wiring is satisfied; the verbose-output check belongs in `update_test.go`.

## Out of scope

Per the ticket body:

- Failure-path tests (covered by the follow-up ticket).
- The actual GitHub release fetch URL.
- The `install.sh` path (covered by `install_*_test.go`).
- Cross-architecture updates.

Additionally explicitly out of scope for this spec:

- Refactoring `update_test.go`'s helpers into a new file or package. The AC's "extracted into a reusable helper" requirement is met structurally — the helpers are already factored out as named functions in `update_test.go`, visible to the new file because both share `package main` and `update_test.go` carries no build tag. No textual extraction is required for them to be reusable.
- Touching the operator's real launchd/systemd. The lessons.md "E2E against the operator's real systemd `--user` / launchd `gui/<uid>`" section catalogues why this would be a much heavier pattern; the AC can be satisfied without it, by replacing the supervisor's role with a test-controlled kill+respawn.
- Adding a `--release-base-url` flag or `PYRY_RELEASE_BASE_URL` env var to production code. Lessons.md "Reject hidden env vars added 'just for the test.'" applies directly — `doUpdate` is invoked from a same-package test instead.
