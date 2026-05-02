# Spec: E2E install-service round-trip on macOS (launchd)

**Ticket:** [#81](https://github.com/pyrycode/pyrycode/issues/81)
**Size:** S (1 new file ~220 lines, 0 edits to existing code)
**Builds on:** #68 (e2e harness), #69 (CLI driver), #80 (Linux sibling — established the
`e2e_install` build tag and widened `harness.go` to `e2e || e2e_install`)

## Context

`pyry install-service` writes launchd plists (`internal/install/install.go` +
`templates/launchd.plist.tmpl`) that real launchd loads via
`launchctl bootstrap`. Today we have unit-test coverage of the file generator,
but nothing exercises the round-trip:

```
pyry install-service → ~/Library/LaunchAgents/dev.pyrycode.<name>.plist
launchctl bootstrap gui/<uid> <plist>     (loads + starts via RunAtLoad=true)
launchctl print gui/<uid>/<label>         → state = running    (poll — async)
pyry status                               → exit 0
launchctl bootout gui/<uid>/<label>
rm <plist>
```

This is the failure mode that produced bug #19 (`os.Getenv("PATH")` printed
verbatim into the unit) — caught only post-release. An e2e test that parses the
generated plist's `EnvironmentVariables.PATH` and asserts it mirrors the
install-time `$PATH` shifts that class of regression left.

The Linux/systemd sibling shipped in #80; this is the macOS half. Both use the
same `e2e_install` build tag so a single CI matrix invocation covers both
platforms when intended.

## Design

### File layout

One new test file. Zero edits to existing code:

```
internal/e2e/install_darwin_test.go    NEW. Both required tests + the
                                       cleanup-on-fatal re-exec test +
                                       launchctl helpers.
```

Why `internal/e2e/` and not `internal/install/`:

- The cached `pyry` binary (`ensurePyryBuilt`, `binPath`) and the `childEnv`
  helper already live here. Reusing them avoids ~20 lines of duplicated
  `go build` / env-scrubbing boilerplate per package.
- Both required tests exercise the CLI binary, not the `install` package
  surface — they belong with the other binary-driven tests.
- `harness.go`'s build tag was widened to `//go:build e2e || e2e_install` by
  #80, so `darwin && e2e_install` already pulls in `ensurePyryBuilt`,
  `runTimeout`, and `childEnv` for free.

### Build tag

```go
//go:build darwin && e2e_install
```

The doc comment at the top of the file documents the invocation:

```
// Run with: go test -tags=e2e_install ./internal/e2e/...
//
// Same tag as the Linux sibling (install_linux_test.go) so a single
// `go test -tags=e2e_install` invocation covers both platforms in CI.
// Tests skip with a clear message when launchctl is missing or when
// running as root (system-domain bootstrap is not what we ship).
```

`darwin` constraint is non-negotiable (launchd is macOS-only).

### Coordination with #80 — duplicated identifiers

`install_linux_test.go` defines `uniqueName()`, `fatalNameEnv`, `fatalOutEnv`,
and `TestInstallFatalChild`. The darwin file needs the same shapes. Two
non-overlapping build tags (`linux && e2e_install` vs
`darwin && e2e_install`) mean the two files never compile together — duplicate
identifier names are *legal* and the simplest path.

We take that path. Names mirror the Linux file exactly so a reader switching
between the two can rely on muscle memory:

- `uniqueName()` — same body
- `fatalNameEnv` = `"PYRY_E2E_INSTALL_FATAL_NAME"` — same constant
- `fatalOutEnv`  = `"PYRY_E2E_INSTALL_FATAL_OUT"` — same constant
- `TestInstallFatalChild` — same name, gated on the same env vars; under
  re-exec on macOS it runs the launchd flow, on Linux the systemd flow

The alternative — extracting these into a third platform-neutral file gated on
`e2e_install` only — saves ~10 lines of duplication at the cost of a third
file with cross-platform test plumbing. Not worth it for a project this size,
and it would force the reader to chase the helpers across files. Defer until
a third platform appears (which would never, since we ship only Linux + macOS).

### Unique instance naming

The round-trip test bootstraps a real launchd job into the operator's `gui/<uid>`
domain and writes a real plist into `~/Library/LaunchAgents/`. Collision with
the operator's installed pyry — or with a prior failed run — would be a
footgun. Every test computes:

```go
name := fmt.Sprintf("pyry-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())
```

The full launchd label becomes `dev.pyrycode.<name>` (per
`templates/launchd.plist.tmpl:6`). PID + nanosecond is collision-resistant for
the relevant scale (one test process at a time per host; nanosecond clock guards
back-to-back invocations).

### Test 1: `TestE2EInstall_RoundTrip_macOS`

Cannot isolate `$HOME`: `launchctl bootstrap gui/<uid>` runs services in the
real user GUI domain, not the test's temp HOME. Test must use the operator's
`~/Library/LaunchAgents/` and clean up rigorously.

```
1. skipIfNoLaunchctl(t)        — see "Skip discipline" below
2. skipIfRoot(t)               — gui/<uid> is for non-root users
3. homeDir := os.UserHomeDir() — t.Skip on failure (CI without HOME)
4. bin := ensurePyryBuilt(t)
5. name := uniqueName()
6. label := "dev.pyrycode." + name
7. plistPath := filepath.Join(homeDir, "Library/LaunchAgents", label+".plist")
8. uid := os.Getuid()
9. t.Cleanup(func() { cleanupLaunchdJob(t, uid, label, plistPath, homeDir, name) })
   — registered BEFORE install so a failure in step 10 still cleans up
10. install.Install(Options{
        Platform: PlatformLaunchd,
        Name:     name,
        Binary:   bin,
        HomeDir:  homeDir,
        ClaudeArgs: []string{
            "-pyry-claude=/bin/sleep",
            "-pyry-idle-timeout=0",
            "--", "infinity",
        },
    })
    — assert returned path == plistPath, plat == PlatformLaunchd
11. assert plistPath exists, regular file
12. assert plist body contains <key>EnvironmentVariables</key> and
    <key>PATH</key>
13. launchctl bootstrap gui/<uid> <plistPath>
14. waitForRunning(t, uid, label)
    — polls `launchctl print gui/<uid>/<label>` for `state = running`
15. exec pyry status -pyry-name=<name>
    — assert exit 0, stdout contains "Phase:"
16. launchctl bootout gui/<uid>/<label>
17. waitForUnloaded(t, uid, label)
    — polls until `launchctl print gui/<uid>/<label>` exits non-zero
      (job no longer registered)
18. (cleanup handles plist removal + best-effort runtime artefacts)
```

#### Binary path resolution

`install.Install` defaults `Options.Binary` to `os.Executable()` — which, for
a test process, is the test binary, not pyry. The CLI flag `pyry
install-service` doesn't expose a `--binary` override.

**Decision: call `install.Install` directly from the test, same as #80.**

The CLI mapping (`runInstallService` → `install.Options`) is mechanical and
already covered by `internal/install/install_test.go`. Adding a hidden env
var to production code purely for test seam-injection is exactly the
"test-only branch in production code" pattern that #34 / #38 / #69 / #80 all
rejected. The e2e value is in the launchd round-trip and the PATH
inheritance, not in re-testing flag parsing. The plist's `ProgramArguments`
will contain `bin` as element 0 — that's the production codepath.

`-pyry-claude` and `-pyry-idle-timeout` are pyry flags, not claude flags, but
`install.Install` just appends `ClaudeArgs` verbatim to `ExecArgs` (same
behaviour as systemd; `install.go:154-158`). When launchd starts the job,
pyry's `flag.Parse` consumes them; the `--` separator hands `infinity` to
claude (`= /bin/sleep`).

`homeDir` is `os.UserHomeDir()` — the operator's real home. launchd writes
its log files (`/tmp/pyry.<name>.{out,err}.log` per the template) and the
daemon writes `~/.pyry/<name>.{sock,pid,...}`; both must be cleaned up. The
plist's `HOME` env is whatever launchd inherits from the user's GUI session —
typically the operator's real `$HOME` regardless of the test process's
`$HOME`, which is exactly why isolation isn't possible here.

#### Status invocation

The harness's `Run(t, verb, args...)` auto-injects `-pyry-socket=<h.SocketPath>`
which is wrong here — the launchd-spawned daemon's socket is at
`~/.pyry/<name>.sock`. So the test invokes status directly:

```go
ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
defer cancel()
cmd := exec.CommandContext(ctx, bin, "status", "-pyry-name="+name)
```

Pyry's `resolveSocketPath` derives the socket from `-pyry-name` when
`-pyry-socket` is unset (`cmd/pyry/main.go:70`) — same machinery the
operator uses. Identical pattern to the Linux sibling
(`install_linux_test.go:221-232`).

#### Cleanup helper

```go
func cleanupLaunchdJob(t *testing.T, uid int, label, plistPath, homeDir, name string) {
    t.Helper()
    // Idempotent best-effort. Each step ignores errors but logs.
    if out, err := runLaunchctl("bootout", fmt.Sprintf("gui/%d/%s", uid, label)); err != nil {
        t.Logf("cleanup: launchctl bootout %s: %v\n%s", label, err, out)
    }
    if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
        t.Logf("cleanup: remove %s: %v", plistPath, err)
    }
    // Best-effort runtime artefact cleanup: registry dir, socket file, log files.
    _ = os.RemoveAll(filepath.Join(homeDir, ".pyry", name))
    _ = os.Remove(filepath.Join(homeDir, ".pyry", name+".sock"))
    _ = os.Remove(fmt.Sprintf("/tmp/pyry.%s.out.log", name))
    _ = os.Remove(fmt.Sprintf("/tmp/pyry.%s.err.log", name))
}
```

`runLaunchctl` is `exec.CommandContext(ctx, "launchctl", args...)` with a
bounded timeout, collected combined output logged via `t.Logf` so debugging
is possible without masking the test outcome. `bootout` against an
already-removed job returns non-zero — that's not a cleanup failure.

Note the **log file cleanup**: unlike systemd (which streams to journald), the
launchd template hardcodes `/tmp/pyry.<name>.{out,err}.log`. Leaving these
behind clutters `/tmp` across test runs but isn't a correctness leak. We
remove them best-effort.

### Test 2: `TestE2EInstall_PathInheritance_macOS`

This test does NOT need real launchd. It only needs to verify that the
generator emits an `EnvironmentVariables` dict whose `PATH` value matches the
test process's effective PATH.

Unlike systemd, `derivePathEnv` does **no `$HOME/` → `%h/` substitution** for
launchd (`install.go:300` — substitution gated on `plat == PlatformSystemd`).
The literal absolute paths land in the plist directly. So the assertion is
simpler than the Linux sibling: each non-empty entry from `$PATH` should
appear verbatim in the rendered `PATH`.

Fully isolatable via `t.TempDir()` as `HomeDir`:

```
1. envPath := os.Getenv("PATH")        — captured for assertion
2. if envPath == "" { t.Skip("$PATH empty") }
3. tempHome := t.TempDir()
4. install.Install(Options{
       Platform: PlatformLaunchd,
       Name:     uniqueName(),
       Binary:   "/usr/bin/true",
       HomeDir:  tempHome,
       EnvPath:  envPath,
   })
5. extract PATH via plutil (see "Plist parsing" below)
6. assert each non-empty entry from envPath appears in the rendered PATH
```

Why call `install.Install` directly, not via the CLI binary: same reason as
Test 1. Using `EnvPath` (the test seam already exposed by `Options`) avoids a
second test seam.

#### Plist parsing

The plist is XML with a key/value alternation pattern that's awkward to walk
with `encoding/xml`. Three options:

**Option A — `plutil -extract`.** macOS ships `plutil` as part of the base
system. `plutil -extract EnvironmentVariables.PATH raw -o - <plist>` prints
just the PATH string. One shell-out, one string-trim, no XML parser. Fails
loudly if the key is missing.

**Option B — line scan for `<key>PATH</key>` then next `<string>`.** Brittle
to template reformatting but the template is ours. ~10 lines of pure Go.

**Option C — `encoding/xml` with a custom decoder for the dict alternation.**
~30 lines, fragile, and reinvents what plutil does correctly.

**Decision: Option A.** Apple-blessed tooling, one line of test code,
crisp error messages on shape violations. `plutil` is mandatory on macOS
(part of `/usr/bin`) — its absence is a broken host, not a skip condition.

```go
ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
defer cancel()
out, err := exec.CommandContext(ctx,
    "plutil", "-extract", "EnvironmentVariables.PATH", "raw", "-o", "-", plistPath,
).CombinedOutput()
if err != nil {
    t.Fatalf("plutil -extract EnvironmentVariables.PATH: %v\n%s", err, out)
}
renderedPath := strings.TrimSpace(string(out))
```

Then split on `:` and assert each non-empty `$PATH` entry is present.

### Test 3: `TestE2EInstall_CleanupOnFatal_macOS`

AC #4 requires that cleanup runs on both pass and failure paths, "Verified by
a deliberate mid-test `t.Fatal` injection."

Per the lesson captured in `docs/lessons.md` ("E2E harness: same-process
`t.Fatal` doesn't exercise cleanup-on-failure"), the only reliable
verification is re-exec — same shape as `TestE2EInstall_CleanupOnFatal_Linux`
in the sibling.

```
Parent (env vars unset):
  1. skipIfNoLaunchctl(t); skipIfRoot(t)
  2. homeDir := os.UserHomeDir()  (skip on failure)
  3. name := uniqueName(); label := "dev.pyrycode." + name
  4. plistPath := filepath.Join(homeDir, "Library/LaunchAgents", label+".plist")
  5. statePath := filepath.Join(t.TempDir(), "state")
  6. t.Cleanup(belt-and-suspenders cleanupLaunchdJob)
  7. exec os.Args[0] -test.run=^TestInstallFatalChild$ -test.count=1
       env: PYRY_E2E_INSTALL_FATAL_NAME=<name>
            PYRY_E2E_INSTALL_FATAL_OUT=<statePath>
  8. assert child exited non-zero (it's expected to fatal)
  9. read statePath, parse {name, plistPath}
  10. assert plistPath does not exist
  11. assert `launchctl print gui/<uid>/<label>` exits non-zero
      (job is no longer registered)

Inner test (gated on env var):
  TestInstallFatalChild:
    if PYRY_E2E_INSTALL_FATAL_NAME == "" { t.Skip }
    skipIfNoLaunchctl(t); skipIfRoot(t)
    homeDir := os.UserHomeDir() (skip on failure)
    bin := ensurePyryBuilt(t)
    name := os.Getenv(fatalNameEnv)
    label := "dev.pyrycode." + name
    plistPath := filepath.Join(homeDir, "Library/LaunchAgents", label+".plist")
    register cleanup (same helper as Test 1)
    install.Install(...)
    launchctl bootstrap gui/<uid> <plistPath>
    waitForRunning(t, uid, label)
    write {name, plistPath} to PYRY_E2E_INSTALL_FATAL_OUT
    t.Fatal("simulated mid-test failure")
```

The inner test's `t.Cleanup` runs on `t.Fatal`, doing the full
bootout + remove sequence. The parent inspects post-state externally — same
shape as `TestHarness_NoLeakOnFatal` from #68 and the Linux sibling's
cleanup-on-fatal test (`install_linux_test.go:315-369`).

The parent also registers its own `t.Cleanup` with the same helper as a
belt-and-suspenders guard — if the child's cleanup ever fails to fire, the
parent catches the leak before the next test run inherits a stale plist.
This is the same pattern used in the Linux sibling
(`install_linux_test.go:330`).

The shared `TestInstallFatalChild` test is the same name in both files but
under non-overlapping build tags — only one definition compiles per platform.

## Concurrency model

Tests are sequential within a process. No goroutines spawned by the tests
themselves. `launchctl bootstrap` returns once launchd has *accepted* the
bootstrap request; the job transitions to `state = running` asynchronously
(noted explicitly in the AC), polled via `launchctl print`.

`exec.CommandContext` for every `launchctl` and `plutil` invocation is bounded
by a 10s deadline matching the harness's `runTimeout` to prevent test hangs.

`waitForRunning` / `waitForUnloaded` use a 100ms poll gap with a per-test
deadline:

```go
const (
    launchctlTimeout   = 10 * time.Second
    runningPollGap     = 100 * time.Millisecond
    runningDeadline    = 10 * time.Second
    unloadedDeadline   = 5 * time.Second
)

func waitForRunning(t *testing.T, uid int, label string) {
    t.Helper()
    target := fmt.Sprintf("gui/%d/%s", uid, label)
    end := time.Now().Add(runningDeadline)
    for time.Now().Before(end) {
        out, err := exec.Command("launchctl", "print", target).CombinedOutput()
        if err == nil && bytes.Contains(out, []byte("state = running")) {
            return
        }
        time.Sleep(runningPollGap)
    }
    out, _ := exec.Command("launchctl", "print", target).CombinedOutput()
    t.Fatalf("service %s did not reach running within %s\n%s", label, runningDeadline, out)
}

func waitForUnloaded(t *testing.T, uid int, label string) {
    t.Helper()
    target := fmt.Sprintf("gui/%d/%s", uid, label)
    end := time.Now().Add(unloadedDeadline)
    for time.Now().Before(end) {
        // print exits non-zero when the job is no longer registered.
        if err := exec.Command("launchctl", "print", target).Run(); err != nil {
            return
        }
        time.Sleep(runningPollGap)
    }
    t.Fatalf("service %s still registered after %s", label, unloadedDeadline)
}
```

Pyry startup is sub-second; 10s deadline is comfortable. Failure dumps the
last `launchctl print` output for diagnosis. Note that `launchctl print` is
a debug command whose output format is technically unstable across macOS
releases — `state = running` has been stable since macOS 10.10 when the
modern launchd CLI shipped, but if Apple ever reformats it, this test
breaks loudly with a clear diagnostic. Acceptable risk for a debug-only
test surface.

## Error handling

| Failure | Behavior |
|---|---|
| launchctl missing | `t.Skip("launchctl not on PATH")` (defensive — should never happen on macOS) |
| running as root | `t.Skip("gui/<uid> domain is for non-root users; system-domain is out of scope")` |
| `os.UserHomeDir()` fails | `t.Skip` (CI without HOME) |
| `install.Install` returns error | `t.Fatal` |
| `launchctl bootstrap` fails | `t.Fatalf` + log combined output |
| Service never reaches `state = running` | `t.Fatal` after deadline + dump last `print` output |
| `pyry status` exits non-zero | `t.Fatalf` with stderr |
| `plutil -extract` fails | `t.Fatalf` (host is broken if plutil errors on a well-formed plist) |
| Cleanup helper sub-step fails | `t.Logf`, continue (cleanup is best-effort) |

Cleanup intentionally swallows errors. A `bootout` call against an
already-removed job returns non-zero; that's not a test failure. The
post-conditions (file absent, service unregistered) are what the
cleanup-on-failure test asserts.

## Testing strategy

The deliverable is the tests themselves. Verification of the spec:

```bash
# Build-tag isolation: default build does not pull in the new file
go build ./...
go test ./...                    # must still pass, no e2e_install code touched

# Compile check with new tag (works on any platform — file is darwin-gated)
go test -tags=e2e_install -run=NONE ./internal/e2e/...

# Run tests on a macOS dev machine
go test -tags=e2e_install -v ./internal/e2e/

# Linux dev machine — darwin file is excluded by GOOS, sibling Linux file runs
go test -tags=e2e_install -v ./internal/e2e/
```

Exercise PATH inheritance with a sentinel:

```bash
PATH="$PATH:/tmp/pyry-pathmarker-$$" go test -tags=e2e_install \
    -run TestE2EInstall_PathInheritance_macOS -v ./internal/e2e/
```

The sentinel must appear in the rendered `EnvironmentVariables.PATH`. If it
doesn't, the test fails — that's the bug-#19 regression guard.

## Open questions

- **Background sessions and headless macOS CI.** `gui/<uid>` requires a logged-in
  GUI session for that uid — this works on a developer's interactive Mac but
  not in a sshd-only headless context. CI on macOS runners (GitHub
  `macos-latest`) does have a logged-in session for the runner user, so this
  should work; but if it doesn't, the fallback is `launchctl bootstrap user/<uid>`
  (background-only domain). Defer the `user/<uid>` fallback until we
  observe the failure on real CI — the AC explicitly specifies `gui/<uid>`,
  matching what the operator runs.

- **`launchctl print` output format stability.** The `state = running` line has
  been stable across macOS 10.10 → 14.x but Apple has reserved the right to
  reformat at any time. If a future macOS release breaks this, the test
  fails loudly and we add a tolerant matcher (e.g. regex on
  `state\s*=\s*running` or fall back to `launchctl list <label>` parsing).
  Don't pre-emptively defend.

- **Should `TestE2EInstall_PathInheritance_macOS` move under the `e2e` tag
  instead of `e2e_install`?** It has no launchd dependency. Same trade-off
  the Linux spec deferred (#80, "Open questions" §3): two tags within one
  file is not possible, splitting it off would need a second file. Defer
  until the e2e tag set matters more for CI sequencing.

## Out of scope

- Linux / systemd parity. Shipped in #80.
- System-scope (`launchctl bootstrap system/`). Requires root, not what we
  ship.
- Re-running install with `--force` over an existing plist. Covered by
  `internal/install` unit tests.
- Multiple concurrent test invocations on the same host. Unique-name guards
  against collision but full parallelism would also need lock coordination
  on `~/Library/LaunchAgents/`. Single-test-runner-per-host is a reasonable
  CI assumption, same as the Linux sibling.
- Replacing `plutil` with a Go XML decoder. Apple's tool is the
  authoritative parser; reimplementing is anti-simplicity.
