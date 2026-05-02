# Install-Service E2E (Linux / systemd)

`internal/e2e/install_linux_test.go` exercises the `pyry install-service` →
`systemctl --user start` → `pyry status` → `systemctl --user stop` round-trip
against the operator's real systemd `--user` session, plus a regression guard
for the bug-#19 PATH-inheritance class. Phase: ticket #80, sibling of the e2e
harness from #68/#69.

## What's Tested

| Test | What it asserts |
|------|-----------------|
| `TestE2EInstall_RoundTrip_Linux` | Unit file written under `~/.config/systemd/user/`, `daemon-reload` + `start` brings the unit to `active`, `pyry status -pyry-name=<name>` exits 0 with `Phase:` in stdout, `stop` lands the unit out of `active`, cleanup removes the unit file. |
| `TestE2EInstall_PathInheritance_Linux` | Generated unit's `Environment="PATH=..."` line contains every non-empty entry from the install-time process's `$PATH`, with `$HOME/` rewritten to `%h/`. Bug-#19 regression guard. |
| `TestE2EInstall_CleanupOnFatal_Linux` | Re-execs `TestInstallFatalChild` which installs + starts a real unit then `t.Fatal`s; parent verifies post-state externally (unit file absent, service not `active`). |

## Invocation

```
go test -tags=e2e_install ./internal/e2e/...
```

Default `go test ./...` does not compile the file. The `e2e_install` tag is
**separate from `e2e`** so default e2e CI runs (which use the `e2e` tag) don't
require a running systemd `--user` session — the user-systemd dependency is
opt-in.

`internal/e2e/harness.go`'s build tag was widened from `//go:build e2e` to
`//go:build e2e || e2e_install` so `ensurePyryBuilt`, `binPath`, and
`childEnv` are reusable from the install tests without duplicating the
binary-cache boilerplate. Compiling under `-tags=e2e_install` therefore also
compiles `harness.go`; that's a feature, not a cost.

## Why `internal/e2e/`, not `internal/install/`

- The cached `pyry` binary (`ensurePyryBuilt`, `binPath`) and `childEnv` already
  live in `internal/e2e`. Reusing them avoids ~20 lines of `go build` /
  env-scrubbing duplication per package.
- Both round-trip tests exercise the CLI binary, not the `install` package
  surface — they belong with the binary-driven tests.

## Skip Discipline

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

## Why `install.Install` Directly, Not via the CLI Binary

`install.Install` defaults `Options.Binary` to `os.Executable()` — for a test
process, that's the test binary, not pyry. The CLI's `pyry install-service`
exposes no `--binary` override.

**Decision: import `internal/install` and call `install.Install(opts)` from
the test with `opts.Binary = bin` set explicitly.**

Adding a hidden `PYRY_INSTALL_BINARY` env var to production code purely for
testing is the "test-only branch in production code" pattern that #34, #38,
and #69 all rejected. The CLI mapping (`runInstallService` → `install.Options`)
is mechanical and already covered by `install_test.go`; the e2e value here is
in the systemd round-trip and the rendered `Environment="PATH=..."` line, not
in re-testing flag-parsing.

`Options.EnvPath` (already exposed for testing) is reused by the
`PathInheritance` test as the test seam — same shape, no new seam invented.

## Why Cannot Isolate `$HOME`

`systemctl --user` runs unit files in the user's real session manager, not
the test's temp HOME. The round-trip and cleanup-on-fatal tests must use the
operator's `~/.config/systemd/user/` directly. Conventional `t.TempDir()`
HOME isolation (the e2e harness's pattern) doesn't apply here.

Mitigations:

- **Unique unit names per invocation.** Every test computes
  `fmt.Sprintf("pyry-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())`. PID
  guards cross-process collision; nanosecond clock guards back-to-back
  invocations within one process.
- **Cleanup registered before any state-changing step.** `t.Cleanup(...)`
  runs even on `t.Fatal`, so a failure between `install.Install` and
  `systemctl stop` still removes the unit file and stops the service.
- **Cleanup is idempotent.** `stop`/`disable`/`remove`/`daemon-reload` each
  ignore errors and log via `t.Logf`; an already-stopped unit is fine, an
  absent file is fine.

## `pyry status` Direct Invocation

The harness's `Harness.Run(t, verb, args...)` auto-injects
`-pyry-socket=<h.SocketPath>`, which is wrong here — the systemd-spawned
daemon's socket is at `~/.pyry/<name>.sock`, derived by `resolveSocketPath`
from `-pyry-name`. The round-trip test invokes status directly:

```go
exec.CommandContext(ctx, bin, "status", "-pyry-name="+name)
```

Same machinery the operator uses on a real install.

## Cleanup-on-Failure: Re-exec Pattern

Per `lessons.md § E2E harness: same-process t.Fatal...`, an inner
`t.Run` + `t.Fatal` is not a substitute for the real failure path — Go's
testing framework propagates the inner failure to the parent and ends the
outer test before its assertions can run. Same shape as
`TestHarness_NoLeakOnFatal` from #68:

```
parent (env vars unset)
  └── exec.Command(os.Args[0], -test.run=^TestInstallFatalChild$, -test.count=1)
        with PYRY_E2E_INSTALL_FATAL_NAME=<name>
             PYRY_E2E_INSTALL_FATAL_OUT=<state-file>
        │
        └── child test process
              ├── skipIfNoUserSystemd(t)
              ├── register cleanup (same helper as round-trip)
              ├── install + daemon-reload + start + waitForActive
              ├── write {name, unitPath} to state-file
              └── t.Fatal — exercises full cleanup
        ↓ child exits ↓
  ├── read state-file
  ├── stat(unitPath) → must be ErrNotExist
  └── systemctl --user is-active <name> → must not be "active"
```

`TestInstallFatalChild` is gated on the env vars (unset → `t.Skip`) so
normal `go test -tags=e2e_install` runs treat it as a no-op. The parent
also registers its own `cleanupSystemdUnit` as belt-and-suspenders: if the
child's cleanup ever fails, subsequent test runs don't inherit a stale
unit.

## Concurrency Model

Tests are sequential within a process. `systemctl --user start` is itself
synchronous (returns once systemd accepts the request); the unit's transition
to `active` is asynchronous, polled via `is-active` with a 100ms gap and a
10s deadline. Each `systemctl` invocation is wrapped in
`exec.CommandContext` with a 10s timeout to prevent test hangs.

`waitForInactive` uses a 5s deadline — `pyry`'s SIGTERM grace + socket cleanup
is sub-second on a healthy system, so 5s is comfortable.

## Failure Posture

| Failure | Behavior |
|---|---|
| `systemctl` missing | `t.Skip("systemctl not on PATH")` |
| user systemd offline/unknown | `t.Skip` with state string |
| `os.UserHomeDir()` fails | `t.Skip` (CI without HOME set) |
| `install.Install` fails | `t.Fatal` |
| `systemctl start` fails | `t.Fatalf` + log `systemctl status` output |
| Service never reaches `active` | `t.Fatal` after deadline + log `systemctl status` |
| `pyry status` exits non-zero | `t.Fatalf` with stdout + stderr |
| Cleanup helper sub-step fails | `t.Logf`, continue (best-effort) |

Cleanup intentionally swallows errors. A `stop` against an already-stopped
unit returns non-zero; that's not a test failure. Post-conditions (file
absent, service inactive) are what the cleanup-on-fatal test asserts.

## Out of Scope

- macOS / launchd parity. Sibling ticket.
- System-scope `systemctl` (no `--user`). Requires root, not what we ship.
- Re-running install with `--force` over an existing unit. Covered by
  `internal/install` unit tests.
- Multiple concurrent test invocations on the same host. Unique-name guards
  collision but full parallelism would also need a lock on
  `~/.config/systemd/user/`. Single-runner-per-host is a reasonable CI
  assumption.

## Related

- Spec: `docs/specs/architecture/80-e2e-install-systemd-roundtrip.md`
- Generator: `internal/install/install.go` — `derivePathEnv` (the
  `$HOME/` → `%h/` substitution under test by `PathInheritance`)
- Bug being guarded: #19 (raw `os.Getenv("PATH")` printed verbatim into
  the systemd unit, missed at unit-test level, caught only post-release)
- Re-exec pattern lineage: #68's `TestHarness_NoLeakOnFatal`
- Pattern: lessons.md § Test helpers across packages (`/bin/sleep` as the
  benign fake claude — also used here as the `-pyry-claude` value)
