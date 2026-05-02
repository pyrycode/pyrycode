# Spec: E2E install-service round-trip on Linux (systemd)

**Ticket:** [#80](https://github.com/pyrycode/pyrycode/issues/80)
**Size:** S (1 new file ~180 lines, 1 trivial 1-line edit)
**Builds on:** #68 (e2e harness), #69 (CLI driver)

## Context

`pyry install-service` writes systemd unit files (`internal/install/install.go`)
that real systemd loads. Today we have unit-test coverage of the file generator,
but nothing exercises the round-trip:

```
pyry install-service → ~/.config/systemd/user/<name>.service
systemctl --user start <name>
systemctl --user is-active <name>  → active
pyry status                        → exit 0
systemctl --user stop <name>
rm <unit>
systemctl --user daemon-reload
```

This is the failure mode that produced bug #19 (`os.Getenv("PATH")` printed
verbatim into the unit) — caught only post-release. An e2e test that parses the
generated unit file and asserts on the rendered `Environment="PATH=..."` line
shifts that class of regression left.

The macOS/launchd parity ships in a sibling ticket (out of scope here).

## Design

### File layout

One new test file plus a one-line build-tag widening on the existing harness:

```
internal/e2e/install_linux_test.go    NEW. Both tests + helpers.
internal/e2e/harness.go               EDIT. Build tag becomes
                                      //go:build e2e || e2e_install
```

Why `internal/e2e/` and not `internal/install/`:

- The cached `pyry` binary (`ensurePyryBuilt`, `binPath`) and the `childEnv`
  helper already live here. Reusing them avoids ~20 lines of duplicated
  `go build` / env-scrubbing boilerplate per package.
- Both tests exercise the CLI binary, not the `install` package surface — they
  belong with the other binary-driven tests.
- One-line tag-widening is cheaper than a third file extracting the binary
  cache into its own package.

The cost of widening the harness's build tag is that compiling
`-tags e2e_install` also compiles `harness.go`. That's free — we want the
binary cache available to the install tests.

### Build tag

```go
//go:build linux && e2e_install
```

The doc comment at the top of the file documents the invocation:

```
// Run with: go test -tags=e2e_install ./internal/e2e/...
//
// Separate from the `e2e` tag so default e2e CI runs don't require a
// running systemd --user session. Tests skip with a clear message when
// `systemctl --user is-system-running` reports an unusable state.
```

`linux` constraint is non-negotiable (systemd is Linux-only).

### Unique instance naming

The round-trip test writes a real unit file into the operator's
`~/.config/systemd/user/`. Collision with the operator's installed pyry — or
with concurrent test runs — would be a footgun. Every test computes:

```go
name := fmt.Sprintf("pyry-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())
```

PID + nanosecond is collision-resistant for the relevant scale (one test
process at a time per host; nanosecond clock guards back-to-back invocations).
Sanitization is a no-op for this character set.

### Test 1: `TestE2EInstall_RoundTrip_Linux`

Cannot isolate `$HOME`: `systemctl --user` runs unit files in the user's real
session, not the test's temp HOME. Test must use the operator's
`~/.config/systemd/user/` and clean up rigorously.

```
1. skipIfNoUserSystemd(t)      — see "Skip discipline" below
2. name := uniqueName()
3. binPath := ensurePyryBuilt(t)
4. unitPath := filepath.Join(home, ".config/systemd/user", name+".service")
5. t.Cleanup(func() { cleanupSystemdUnit(t, name, unitPath) })
   — registered BEFORE install so a failure in step 6 still cleans up
6. exec pyry install-service
      -pyry-name=<name>
      --
      -pyry-claude=/bin/sleep
      -pyry-idle-timeout=0
      -- infinity
   Override Options.Binary indirectly: install.Install uses os.Executable() of
   the running process, and our test process IS pyry-e2e binary. Solution: set
   the BINARY argument explicitly via a hidden flag... actually no. See
   "Binary path resolution" below.
7. assert unitPath exists, file mode 0644-ish, content has Environment="PATH=
8. systemctl --user daemon-reload
9. systemctl --user start <name>
10. waitForActive(t, name, 10*time.Second)
    — polls `systemctl --user is-active <name>` for "active"
11. exec pyry status -pyry-name=<name>
    — assert exit 0, stdout contains "Phase:"
12. systemctl --user stop <name>
13. waitForInactive(t, name, 5*time.Second)
14. (cleanup handles unit removal + daemon-reload)
```

#### Binary path resolution

`install.Install` defaults `Options.Binary` to `os.Executable()` — which, for
a test process, is the test binary, not pyry. The CLI flag `pyry
install-service` doesn't expose a `--binary` override.

Two clean options:

**Option A — environment variable.** Export a new `PYRY_INSTALL_BINARY` env
var that, when set, overrides `Options.Binary` resolution. Three lines in
`runInstallService` (or `Install` itself), zero impact on the existing CLI
surface, exists purely for the test seam. Hidden, undocumented in CLI help.

**Option B — call `install.Install` directly from the test.** The test
imports `internal/install` and calls `install.Install(opts)` with
`opts.Binary = binPath` set explicitly. Skips the CLI binary entirely for the
unit-file write step, but still uses the binary for `pyry status` and as the
unit's `ExecStart`. Loses some "actually exercise the CLI" coverage but the
CLI codepath is one `flag.Parse` and one struct literal — the unit-test
suite already covers that mapping.

**Decision: Option B.** Adding a hidden env var to production code purely
for testing is exactly the "test-only branch in production code" pattern
that #34 / #38 / #69 all rejected. The CLI mapping
(`runInstallService` → `install.Options`) is mechanical and already covered
by `install_test.go`; the e2e value is in the systemd round-trip and the
PATH inheritance, not in re-testing the flag-parsing.

Test imports `github.com/pyrycode/pyrycode/internal/install` and writes:

```go
unitPath, _, err := install.Install(install.Options{
    Platform:   install.PlatformSystemd,
    Name:       name,
    Binary:     binPath,
    HomeDir:    homeDir,
    ClaudeArgs: []string{"-pyry-claude=/bin/sleep", "-pyry-idle-timeout=0", "--", "infinity"},
})
```

`ClaudeArgs` is a slight abuse — `-pyry-claude` and `-pyry-idle-timeout` are
pyry flags, not claude flags. But install.Install just appends them verbatim
to ExecArgs after `[binary, -pyry-name name]`. When systemd starts the unit,
pyry's own `flag.Parse` consumes them; the `--` separator hands `infinity`
to claude (= `/bin/sleep`). Verified by reading `install.go:154-158` and the
existing harness's spawn pattern (`harness.go:133-139`).

`homeDir` is `os.UserHomeDir()` — the operator's real home, since systemd
--user uses it. `os.UserHomeDir()` failure is a t.Skip (CI without HOME set).

#### Status invocation

The harness's `Run(t, verb, args...)` auto-injects `-pyry-socket=<h.SocketPath>`
which is wrong here — the systemd-spawned daemon's socket is at
`~/.pyry/<name>.sock`. So the test invokes status directly:

```go
cmd := exec.Command(binPath, "status", "-pyry-name="+name)
```

Pyry's `resolveSocketPath` derives the socket from the name when `-pyry-socket`
is unset (`cmd/pyry/main.go:70`). Same machinery the operator uses.

#### Cleanup helper

```go
func cleanupSystemdUnit(t *testing.T, name, unitPath string) {
    // Idempotent. Each step ignores errors but logs.
    runSystemctl("stop", name)        // may already be stopped
    runSystemctl("disable", name)     // defensive — we never enabled
    _ = os.Remove(unitPath)           // OK if absent
    runSystemctl("daemon-reload")     // refresh systemd's view
}
```

`runSystemctl` is `exec.Command("systemctl", "--user", verb, args...)`,
collected stderr logged via `t.Logf` so debugging is possible without
masking the test outcome.

### Test 2: `TestE2EInstall_PathInheritance_Linux`

This test does NOT need real systemd. It only needs to verify that the
generator emits `Environment="PATH=..."` matching the test process's
effective PATH (with `$HOME` → `%h` substitution per
`install.derivePathEnv`'s contract).

Fully isolatable via `t.TempDir()` as `HomeDir`:

```
1. tempHome := t.TempDir()
2. testPath := os.Getenv("PATH")   — captured for assertion
3. install.Install(Options{
       Platform: PlatformSystemd,
       Name:     uniqueName(),
       Binary:   "/usr/bin/true",
       HomeDir:  tempHome,
       EnvPath:  testPath,
   })
4. read the unit file
5. extract the Environment="PATH=..." line via simple line scan
6. assert each non-empty entry from testPath appears in the rendered PATH,
   with $HOME/ → %h/ substitution
```

Why call `install.Install` directly, not via the CLI binary: same reason as
Test 1 — the CLI mapping is unit-tested, the e2e value is in the rendered
output. Using `EnvPath` (the test seam already exposed by `Options`) avoids
a second test seam.

Skip if `os.Getenv("PATH") == ""` (defensive — should never happen on a real
host).

### Skip discipline

```go
func skipIfNoUserSystemd(t *testing.T) {
    if _, err := exec.LookPath("systemctl"); err != nil {
        t.Skip("systemctl not on PATH")
    }
    cmd := exec.Command("systemctl", "--user", "is-system-running")
    out, err := cmd.CombinedOutput()
    state := strings.TrimSpace(string(out))
    // is-system-running exits non-zero on degraded/maintenance/etc but
    // those states are still usable. The unusable states are:
    //   "offline"  — no user systemd manager running
    //   "unknown"  — no D-Bus session, common in containers/CI
    // and any error invoking systemctl at all.
    if err != nil && (state == "offline" || state == "unknown" || state == "") {
        t.Skipf("user systemd unusable: state=%q err=%v", state, err)
    }
}
```

The CI runner `ubuntu-latest` does not have a usable user systemd by default
(no D-Bus session for the `runner` user). These tests skip cleanly there.
They run on developer machines, dedicated Linux test boxes, and any future CI
that sets up a user systemd session (e.g. via `loginctl enable-linger`).

### Cleanup-on-failure verification (re-exec pattern from #68)

AC #4 requires that cleanup runs on both pass and failure paths, "Verified by
a deliberate mid-test `t.Fatal` injection."

Per the lesson captured in `docs/lessons.md` ("E2E harness: same-process
`t.Fatal` doesn't exercise cleanup-on-failure"), the only reliable
verification is re-exec.

Add a third test, `TestE2EInstall_CleanupOnFatal_Linux`:

```
Parent (env var unset):
  1. skipIfNoUserSystemd(t)
  2. name := uniqueName()
  3. statePath := filepath.Join(t.TempDir(), "state")
  4. exec os.Args[0] -test.run=^TestInstallFatalChild$ -test.count=1
       env: PYRY_E2E_INSTALL_FATAL_NAME=<name>
            PYRY_E2E_INSTALL_FATAL_OUT=<statePath>
  5. assert child exited non-zero (it's expected to fatal)
  6. read statePath, parse {Name, UnitPath}
  7. assert UnitPath does not exist
  8. assert `systemctl --user is-active <name>` reports inactive/unknown

Inner test (gated on env var):
  TestInstallFatalChild:
    if PYRY_E2E_INSTALL_FATAL_NAME == "" { t.Skip }
    name := os.Getenv(PYRY_E2E_INSTALL_FATAL_NAME)
    register cleanup (same helper as Test 1)
    install + start + wait-for-active
    write {name, unitPath} to PYRY_E2E_INSTALL_FATAL_OUT
    t.Fatal("simulated mid-test failure")
```

The inner test's `t.Cleanup` runs on `t.Fatal`, doing the full
stop/disable/remove/reload sequence. The parent inspects post-state
externally — same shape as `TestHarness_NoLeakOnFatal` from #68.

Same gating discipline as #68: inner test skips when its env var is unset, so
normal `go test` runs treat it as a no-op.

## Concurrency model

Tests are sequential within a process. No goroutines spawned by the tests
themselves. `systemctl --user start` is itself synchronous (returns once
systemd has accepted the start request); the unit transitions to `active`
asynchronously, polled via `is-active`.

`cmd.Run()` for systemctl invocations is bounded by the OS-level systemctl
timeout (default ~30s). We wrap each in `exec.CommandContext` with a 10s
deadline matching the harness's `runTimeout` to prevent test hangs.

`waitForActive` / `waitForInactive` use a 100ms poll gap with a per-test
deadline:

```go
func waitForActive(t *testing.T, name string, deadline time.Duration) {
    t.Helper()
    end := time.Now().Add(deadline)
    for time.Now().Before(end) {
        out, _ := exec.Command("systemctl", "--user", "is-active", name).Output()
        if strings.TrimSpace(string(out)) == "active" {
            return
        }
        time.Sleep(100 * time.Millisecond)
    }
    t.Fatalf("service %s did not reach active within %s", name, deadline)
}
```

Pyry startup is sub-second; 10s deadline is comfortable. Failure dumps
`systemctl --user status <name>` for diagnosis.

## Error handling

| Failure | Behavior |
|---|---|
| systemctl missing | `t.Skip("systemctl not on PATH")` |
| user systemd offline/unknown | `t.Skip` with state string |
| `os.UserHomeDir()` fails | `t.Skip` (CI without HOME) |
| `install.Install` returns error | `t.Fatal` |
| `systemctl start` fails | `t.Fatalf` + log `systemctl status` output |
| Service never reaches `active` | `t.Fatal` after deadline |
| `pyry status` exits non-zero | `t.Fatalf` with stderr |
| Cleanup helper sub-step fails | `t.Logf`, continue (cleanup is best-effort) |

Cleanup intentionally swallows errors. A `stop` call against an
already-stopped unit returns non-zero; that's not a test failure. The
post-conditions (file absent, service inactive) are what the
cleanup-on-failure test asserts.

## Testing strategy

The deliverable is the tests themselves. Verification of the spec:

```bash
# Build-tag isolation: default build does not pull in the new file
go build ./...
go test ./...                    # must still pass, no e2e_install code touched

# Compile check with new tag
go test -tags=e2e_install -run=NONE ./internal/e2e/...

# Run tests on a Linux dev machine with user systemd
go test -tags=e2e_install -v ./internal/e2e/

# Skip cleanly on CI runner
# (TestE2EInstall_RoundTrip_Linux + CleanupOnFatal skip via skipIfNoUserSystemd;
#  TestE2EInstall_PathInheritance_Linux runs everywhere Linux)
```

Exercise PATH inheritance with a sentinel:

```bash
PATH="$PATH:/tmp/pyry-pathmarker-$$" go test -tags=e2e_install -run TestE2EInstall_PathInheritance_Linux -v ./internal/e2e/
```

The sentinel must appear in the rendered `Environment="PATH=..."` line. If it
doesn't, the test fails — that's the bug-#19 regression guard.

## Open questions

- **Does `loginctl enable-linger $USER` need to run on the dev box first?**
  Without linger, user systemd terminates when the user logs out. For a
  developer running `go test` in a SSH session, linger isn't required (the
  session keeps the user systemd alive). For CI that runs as `runner` over
  SSH-less invocation, linger may be needed. Document in the test file's
  doc comment: "requires `systemctl --user is-system-running` to report a
  usable state; if running as a service account, may require
  `loginctl enable-linger <user>` once."

- **Is `os.UserHomeDir()` the right home for the round-trip test?**
  `$HOME` could be overridden by the parent test's env. The test assumes the
  systemd user manager's view of `$HOME` matches the test process's view —
  true under normal operation, but if the parent suite ever sets `HOME=...`
  in `os.Setenv` before running this test, the unit would land in one place
  and systemd would look in another. Defensive: `os.UserHomeDir()` reads
  `$HOME` first, so they will agree. No action needed unless this becomes
  a real failure mode.

- **Should `TestE2EInstall_PathInheritance_Linux` move under the `e2e` tag
  instead of `e2e_install`?** It has no systemd dependency. Splitting it off
  would let it run in default e2e CI. Trade-off: two tags within one file is
  not possible, so it'd need its own file. Defer until the e2e tag set
  matters more for CI sequencing — for now, the convenience of one file wins.

## Out of scope

- macOS / launchd parity. Sibling ticket.
- System-scope (`systemctl` without `--user`). Requires root, not what we
  ship.
- Re-running install with `--force` over an existing unit. Covered by
  `internal/install` unit tests.
- Multiple concurrent test invocations on the same host. Unique-name guards
  against collision but full parallelism would also need lock coordination
  on `~/.config/systemd/user/`. Single-test-runner-per-host is a reasonable
  CI assumption.
