# Install-Service E2E (systemd / launchd)

`internal/e2e/install_linux_test.go` and `internal/e2e/install_darwin_test.go`
exercise the `pyry install-service` round-trip against the operator's real
service manager on each platform: write the unit/plist, start it, hit
`pyry status` against the daemon, stop it, clean up. They also carry a
regression guard for the bug-#19 PATH-inheritance class. Phase: tickets
#80 (Linux/systemd) and #81 (macOS/launchd), siblings of the e2e harness
from #68/#69.

Both files share the `e2e_install` build tag so a single CI invocation
covers both platforms when intended.

## What's Tested

### Linux — `internal/e2e/install_linux_test.go` (`//go:build linux && e2e_install`)

| Test | What it asserts |
|------|-----------------|
| `TestE2EInstall_RoundTrip_Linux` | Unit file written under `~/.config/systemd/user/`, `daemon-reload` + `start` brings the unit to `active`, `pyry status -pyry-name=<name>` exits 0 with `Phase:` in stdout, `stop` lands the unit out of `active`, cleanup removes the unit file. |
| `TestE2EInstall_PathInheritance_Linux` | Generated unit's `Environment="PATH=..."` line contains every non-empty entry from the install-time process's `$PATH`, with `$HOME/` rewritten to `%h/`. Bug-#19 regression guard. |
| `TestE2EInstall_CleanupOnFatal_Linux` | Re-execs `TestInstallFatalChild` which installs + starts a real unit then `t.Fatal`s; parent verifies post-state externally (unit file absent, service not `active`). |

### macOS — `internal/e2e/install_darwin_test.go` (`//go:build darwin && e2e_install`)

| Test | What it asserts |
|------|-----------------|
| `TestE2EInstall_RoundTrip_macOS` | Plist written under `~/Library/LaunchAgents/dev.pyrycode.<name>.plist`, `launchctl bootstrap gui/<uid>` followed by polling `launchctl print` for `state = running`, `pyry status -pyry-name=<name>` exits 0 with `Phase:` in stdout, `launchctl bootout` followed by polling for the job to be unregistered, cleanup removes the plist. |
| `TestE2EInstall_PathInheritance_macOS` | Plist's `EnvironmentVariables.PATH` (extracted via `plutil -extract`) contains every non-empty entry from the install-time process's `$PATH`. **No `$HOME/` → `%h/` substitution** on launchd (`derivePathEnv` gates that on `PlatformSystemd`). Bug-#19 regression guard. |
| `TestE2EInstall_CleanupOnFatal_macOS` | Re-execs `TestInstallFatalChild` which installs + bootstraps a real launchd job then `t.Fatal`s; parent verifies post-state externally (plist absent, `launchctl print gui/<uid>/<label>` exits non-zero). |

## Invocation

```
go test -tags=e2e_install ./internal/e2e/...
```

Default `go test ./...` does not compile either file. The `e2e_install` tag
is **separate from `e2e`** so default e2e CI runs (which use the `e2e` tag)
don't require a running user systemd / GUI launchd session — that
dependency is opt-in.

`internal/e2e/harness.go`'s build tag was widened from `//go:build e2e` to
`//go:build e2e || e2e_install` (in #80) so `ensurePyryBuilt`, `binPath`,
`childEnv`, and `runTimeout` are reusable from the install tests without
duplicating the binary-cache or env-scrubbing boilerplate. Compiling under
`-tags=e2e_install` therefore also compiles `harness.go`.

## Why `internal/e2e/`, not `internal/install/`

- The cached `pyry` binary (`ensurePyryBuilt`, `binPath`) and `childEnv`
  already live in `internal/e2e`. Reusing them avoids ~20 lines of
  `go build` / env-scrubbing duplication per package.
- Both round-trip tests exercise the CLI binary, not the `install` package
  surface — they belong with the binary-driven tests.

## Cross-Platform Decisions

Three design choices are shared by both platforms because the tradeoffs are
identical regardless of the underlying service manager.

### `install.Install` Directly, Not via the CLI Binary

`install.Install` defaults `Options.Binary` to `os.Executable()` — for a
test process, that's the test binary, not pyry. The CLI's `pyry
install-service` exposes no `--binary` override.

**Decision: import `internal/install` and call `install.Install(opts)` from
the test with `opts.Binary = bin` set explicitly.**

Adding a hidden `PYRY_INSTALL_BINARY` env var to production code purely for
testing is the "test-only branch in production code" pattern that #34, #38,
and #69 all rejected. The CLI mapping (`runInstallService` →
`install.Options`) is mechanical and already covered by `install_test.go`;
the e2e value here is in the round-trip and the rendered PATH, not in
re-testing flag parsing.

`Options.EnvPath` (already exposed for testing) is reused by both
`PathInheritance` tests as the test seam — same shape, no new seam invented.

### Duplicated Helper Identifiers Across the Two Files

`uniqueName()`, `fatalNameEnv`, `fatalOutEnv`, and `TestInstallFatalChild`
are defined in both files with identical names and bodies. The two files
have non-overlapping build tags (`linux && e2e_install` vs
`darwin && e2e_install`), so they never compile together — duplicate
identifiers are legal and the simplest path. Names mirror exactly so a
reader switching between the two relies on muscle memory.

The alternative — extracting into a third platform-neutral file gated on
`e2e_install` only — saves ~10 lines of duplication at the cost of a third
file with cross-platform test plumbing. Deferred until a third platform
appears.

### Cleanup-on-Failure: Re-exec Pattern

Per `lessons.md § E2E harness: same-process t.Fatal...`, an inner `t.Run` +
`t.Fatal` is not a substitute for the real failure path. Both platforms use
re-exec — same shape as `TestHarness_NoLeakOnFatal` from #68:

```
parent (env vars unset)
  └── exec.Command(os.Args[0], -test.run=^TestInstallFatalChild$, -test.count=1)
        with PYRY_E2E_INSTALL_FATAL_NAME=<name>
             PYRY_E2E_INSTALL_FATAL_OUT=<state-file>
        │
        └── child test process
              ├── platform skip checks
              ├── register cleanup (same helper as round-trip)
              ├── install + start + wait-for-running
              ├── write {name, unitOrPlistPath} to state-file
              └── t.Fatal — exercises full cleanup
        ↓ child exits ↓
  ├── read state-file
  ├── stat(unitOrPlistPath) → must be ErrNotExist
  └── platform liveness check → must be unregistered/inactive
```

`TestInstallFatalChild` is gated on the env vars (unset → `t.Skip`) so
normal `go test -tags=e2e_install` runs treat it as a no-op. The parent
also registers its own platform cleanup helper as belt-and-suspenders: if
the child's cleanup ever fails, subsequent test runs don't inherit a stale
unit/plist.

## Why Cannot Isolate `$HOME` (Round-Trip)

Neither service manager honors a redirected `$HOME`:

- **systemd `--user`** runs unit files in the operator's real session
  manager, not the test's temp HOME.
- **`launchctl bootstrap gui/<uid>`** runs services in the user's GUI
  domain, with `HOME` inherited from the GUI session — not from the test
  process.

The round-trip and cleanup-on-fatal tests must use the operator's real
config directory (`~/.config/systemd/user/` or `~/Library/LaunchAgents/`)
and clean up rigorously. The conventional `t.TempDir()` HOME isolation
(the e2e harness's pattern) doesn't apply.

`PathInheritance` doesn't touch the service manager and stays
`t.TempDir()`-isolated.

Mitigations:

- **Unique names per invocation.** Every test computes
  `fmt.Sprintf("pyry-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())`. PID
  guards cross-process collision; nanosecond clock guards back-to-back
  invocations within one process. macOS labels become
  `dev.pyrycode.<name>` per the launchd template.
- **Cleanup registered before any state-changing step.** `t.Cleanup(...)`
  runs even on `t.Fatal`, so a failure between `install.Install` and
  the explicit teardown still removes the unit/plist and stops the
  service.
- **Cleanup is idempotent.** Each step (`stop`/`disable`/`bootout`/
  `daemon-reload`) ignores errors and logs via `t.Logf`; an
  already-stopped unit is fine, an absent file is fine. macOS additionally
  best-effort removes runtime artefacts (`~/.pyry/<name>` registry dir,
  `<name>.sock`, `/tmp/pyry.<name>.{out,err}.log`).

## `pyry status` Direct Invocation

The harness's `Harness.Run(t, verb, args...)` auto-injects
`-pyry-socket=<h.SocketPath>`, which is wrong here — the
service-manager-spawned daemon's socket is at `~/.pyry/<name>.sock`,
derived by `resolveSocketPath` from `-pyry-name`. The round-trip tests
invoke status directly:

```go
exec.CommandContext(ctx, bin, "status", "-pyry-name="+name)
```

Same machinery the operator uses on a real install.

## Linux / systemd Specifics

### Skip Discipline

```go
func skipIfNoUserSystemd(t *testing.T) {
    if _, err := exec.LookPath("systemctl"); err != nil {
        t.Skip("systemctl not on PATH")
    }
    out, err := exec.Command("systemctl", "--user", "is-system-running").CombinedOutput()
    state := strings.TrimSpace(string(out))
    // is-system-running exits non-zero on degraded/maintenance/etc — usable.
    // Unusable: "offline" (no manager) / "unknown" (no D-Bus session).
    if err != nil && (state == "offline" || state == "unknown" || state == "") {
        t.Skipf("user systemd unusable: state=%q err=%v", state, err)
    }
}
```

`ubuntu-latest` GitHub runners have no D-Bus session for the `runner` user;
the round-trip and cleanup-on-fatal tests skip cleanly. Service accounts on
dedicated Linux test boxes may need `loginctl enable-linger <user>` once.
`PathInheritance` doesn't touch systemd, so it runs on any Linux host.

### Liveness Polling

`waitForActive` polls `systemctl --user is-active <name>` with a 100ms gap
and a 10s deadline. `waitForInactive` uses a 5s deadline — pyry's SIGTERM
grace + socket cleanup is sub-second on a healthy system.

### PATH Substitution

`derivePathEnv` (in `internal/install/install.go`) rewrites `$HOME/` to
`%h/` for systemd (`plat == PlatformSystemd`). The Linux PATH-inheritance
test asserts the rewrite happens.

## macOS / launchd Specifics

### Skip Discipline

- `skipIfNoLaunchctl(t)` — `exec.LookPath("launchctl")` (defensive; should
  never fail on macOS).
- `skipIfRoot(t)` — `gui/<uid>` is for non-root users; system-domain
  bootstrap requires root and is not what we ship.
- `os.UserHomeDir()` failure → `t.Skip` (CI without HOME).

### Liveness Polling

```go
func waitForRunning(t *testing.T, uid int, label string) {
    target := fmt.Sprintf("gui/%d/%s", uid, label)
    end := time.Now().Add(runningDeadline)
    for time.Now().Before(end) {
        out, err := exec.Command("launchctl", "print", target).CombinedOutput()
        if err == nil && bytes.Contains(out, []byte("state = running")) {
            return
        }
        time.Sleep(runningPollGap)
    }
    // ... fail with last `print` output dumped for diagnosis
}
```

`launchctl bootstrap` returns once launchd has *accepted* the request; the
job transitions to `state = running` asynchronously. `waitForUnloaded`
polls until `launchctl print gui/<uid>/<label>` exits non-zero (job no
longer registered) within a 5s deadline.

`launchctl print` is a debug command whose output format Apple has
reserved the right to reformat. `state = running` has been stable since
macOS 10.10; if a future release breaks this, the test fails loudly with a
clear diagnostic. Acceptable risk for a debug-only test surface.

### Plist Parsing via `plutil -extract`

The plist is XML with a key/value alternation pattern that's awkward to
walk with `encoding/xml`. We shell out to Apple's `plutil`:

```go
out, err := exec.CommandContext(ctx,
    "plutil", "-extract", "EnvironmentVariables.PATH", "raw", "-o", "-", plistPath,
).CombinedOutput()
```

One shell-out, one string-trim, no XML parser. Fails loudly if the key is
missing. `plutil` is mandatory on macOS (part of `/usr/bin`) — its absence
is a broken host, not a skip condition.

A pure-Go XML decoder for the dict alternation would be ~30 lines, fragile,
and reinvent what `plutil` does correctly. Rejected.

### No `$HOME/` → `%h/` Substitution on launchd

`derivePathEnv` gates the substitution on `plat == PlatformSystemd` only.
The literal absolute `$PATH` entries land in the plist directly, so the
macOS PATH-inheritance assertion is simpler than the Linux sibling: each
non-empty entry from `$PATH` should appear verbatim in the rendered PATH.

### Log File Cleanup

Unlike systemd (which streams to journald), the launchd template hardcodes
`/tmp/pyry.<name>.{out,err}.log`. The cleanup helper best-effort removes
both. Leftover log files clutter `/tmp` across runs but aren't a
correctness leak.

## Concurrency Model

Tests are sequential within a process. No goroutines spawned by the tests.
Each `systemctl` / `launchctl` / `plutil` invocation is wrapped in
`exec.CommandContext` with a 10s timeout to prevent test hangs. Polling
loops use a 100ms gap with a 10s liveness deadline / 5s teardown deadline.

## Failure Posture

| Failure | Behavior |
|---|---|
| service-manager binary missing | `t.Skip` |
| user systemd offline/unknown | `t.Skip` (Linux) |
| running as root | `t.Skip` (macOS — `gui/<uid>` is non-root) |
| `os.UserHomeDir()` fails | `t.Skip` (CI without HOME set) |
| `install.Install` fails | `t.Fatal` |
| `bootstrap` / `start` fails | `t.Fatalf` + log diagnostic output |
| Service never reaches running/active | `t.Fatal` after deadline + dump diagnostic |
| `pyry status` exits non-zero | `t.Fatalf` with stdout + stderr |
| `plutil -extract` fails (macOS) | `t.Fatalf` (host is broken) |
| Cleanup helper sub-step fails | `t.Logf`, continue (best-effort) |

Cleanup intentionally swallows errors. A `bootout` against an
already-removed job, or `stop` against an already-stopped unit, returns
non-zero — that's not a test failure. Post-conditions (file absent,
service unregistered/inactive) are what the cleanup-on-fatal test asserts.

## Out of Scope

- System-scope (`launchctl bootstrap system/`, `systemctl` without
  `--user`). Requires root, not what we ship.
- Re-running install with `--force` over an existing unit/plist. Covered
  by `internal/install` unit tests.
- Multiple concurrent test invocations on the same host. Unique-name
  guards collision but full parallelism would need a lock on the OS
  config directory. Single-runner-per-host is a reasonable CI assumption.
- Replacing `plutil` with a Go XML decoder. Apple's tool is the
  authoritative parser; reimplementing is anti-simplicity.

## Open Question — Headless macOS CI

`gui/<uid>` requires a logged-in GUI session for that uid. GitHub
`macos-latest` runners do have one for the runner user, so this should
work; if it doesn't, the fallback is `launchctl bootstrap user/<uid>`
(background-only domain). Defer the `user/<uid>` fallback until we observe
the failure on real CI — the AC explicitly specifies `gui/<uid>`, matching
what the operator runs.

## Related

- Specs: `docs/specs/architecture/80-e2e-install-systemd-roundtrip.md`,
  `docs/specs/architecture/81-e2e-install-launchd-roundtrip.md`
- Generator: `internal/install/install.go` — `derivePathEnv` (the
  `$HOME/` → `%h/` substitution gated on `PlatformSystemd`)
- Templates: `internal/install/templates/systemd.service.tmpl`,
  `internal/install/templates/launchd.plist.tmpl`
- Bug being guarded: #19 (raw `os.Getenv("PATH")` printed verbatim into
  the systemd unit, missed at unit-test level, caught only post-release)
- Re-exec pattern lineage: #68's `TestHarness_NoLeakOnFatal`
- Pattern: lessons.md § Test helpers across packages (`/bin/sleep` as the
  benign fake claude — also used here as the `-pyry-claude` value)
