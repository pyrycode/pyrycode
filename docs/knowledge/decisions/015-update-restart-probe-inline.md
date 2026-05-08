# ADR 015: `pyry update` daemon-restart probe lives inline in `cmd/pyry`; executor is an injectable seam

## Status

Accepted (ticket #190).

## Context

Ticket #181 landed `internal/update/restart.go`, a pure decision function: `DetectRestartCommand(probe RestartProbe) []string`. Given booleans for "launchd plist exists" and "systemd unit exists" plus a UID, it returns the argv of the right restart command (or nil), with launchd winning the tie. No I/O, no filesystem.

Ticket #190 wires that decision into `pyry update`: after the binary is replaced on disk, probe for a managed daemon unit and (unless `--no-restart`) exec the restart command. Two design questions:

1. **Where do the `os.Stat` calls live?** Inside `internal/update/restart.go` next to `DetectRestartCommand` (one combined `ProbeAndDetect` call), or inline in `cmd/pyry/update.go` next to the subcommand handler that consumes them?
2. **How does the integration test assert argv without exec'ing real `launchctl` / `systemctl`?** Build-tag the production executor out, swap `exec.CommandContext` via a package-level var, or inject the executor through the existing `updateOptions` seam pattern.

## Decision

**Probe inline in `cmd/pyry/update.go`.** A package-private `defaultProbeRestart` stats `~/Library/LaunchAgents/dev.pyrycode.pyry.plist` and `~/.config/systemd/user/pyry.service` and returns a populated `update.RestartProbe`. `internal/update/restart.go` stays a pure function with no `os.Stat`, no `os` import beyond what `RestartProbe` needs.

**Executor injectable via `updateOptions.runRestart`.** A new field `runRestart func(ctx context.Context, argv []string) error` on the existing `updateOptions` struct, populated by `runUpdate` with `defaultRunRestart` (an `exec.CommandContext` wrapper) and overridden by tests with a closure that records argv into a captured slice.

**Probe seam too.** `updateOptions.probeRestart func() update.RestartProbe`, populated with `defaultProbeRestart` in production and a test-local closure returning a fixtured probe in integration tests. Avoids stat'ing the real filesystem inside `t.TempDir()` and lets each test pin one shape (launchd-only, systemd-only, neither).

## Rationale

### Why probe inline rather than in `internal/update`

The boundary was already drawn in #181: `internal/update/restart.go` is a *pure decision*, not a probe. Three reasons to keep it that way:

1. **One responsibility per package boundary.** `internal/update` houses pure transforms (`ParseLatestRelease`, `CompareVersions`, `AssetName`, `DetectRestartCommand`, `ExtractBinary`) plus thin I/O wrappers with documented contracts (`Fetcher`, `AtomicReplace`). Folding "stat these specific paths in $HOME" into that package conflates the layer that decides *what* with the layer that probes *where*. The same `DetectRestartCommand` is callable from a future remote-control path, a sysadmin tool, or a unit test with no `$HOME` at all — none of which want a built-in probe.

2. **The probe paths are platform conventions, not update-flow logic.** The `~/Library/LaunchAgents/dev.pyrycode.pyry.plist` and `~/.config/systemd/user/pyry.service` paths come from `internal/install/install.go:184-186`. They're conventions about where pyry installs its own service files. The update flow consumes those conventions; it doesn't own them. Inlining the stats in `cmd/pyry/update.go` keeps the dependency direction one-way: the verb knows about install conventions; install conventions don't know about the update verb.

3. **Filesystem dependence makes pure functions hard to test.** `DetectRestartCommand`'s test (`internal/update/restart_test.go`) is four trivial cases driven by struct literals — `{LaunchdPlistExists: true, SystemdUnitExists: false, UID: "501"}` and assert argv. Fold the probe in and that test now needs a tempdir, a `t.Setenv("HOME", ...)`, real plist/unit files, and platform branching. The pure-function shape was deliberately chosen at #181 to dodge all of that.

### Why injectable executor

The integration test must assert the exact argv that `runRestart` receives — `[launchctl, kickstart, -k, gui/501/dev.pyrycode.pyry]` for darwin, `[systemctl, --user, restart, pyry]` for linux — without spawning a real `launchctl` or `systemctl`. Three options:

- **Build-tag production code.** `restart_real.go` for `//go:build !test` and `restart_test.go` with no build tag. Compounds the existing `updateOptions` seam pattern with a parallel build-tag mechanism for one new function. Rejected: more machinery, nothing gained over a function field.
- **Package-level var.** `var execRestart = exec.CommandContext` in `update.go`, swapped via `t.Cleanup(func() { execRestart = original })` in tests. Mutable global state, race-prone if anything in `cmd/pyry` ever runs tests in parallel against the verb. Rejected.
- **Function field on `updateOptions` (chosen).** Mirrors the existing `replace`, `executablePath`, and `fetcher` seams already in `updateOptions`. Each test populates the field with a closure that records argv into a captured slice or returns a sentinel error. Production callers in `runUpdate` populate it once with `defaultRunRestart`. No globals, no build tags, one consistent pattern across all the seams the test surface needs.

### Why a separate `probeRestart` seam alongside the executor

Could fold the probe into the executor (e.g. a single `restartIfManaged func(ctx) error` that hides both the probe and the exec). Rejected because:

- The progress line needs the manager label (`launchd` vs `systemd`) before the executor runs, and the cleanest way to derive that is from the `RestartProbe` directly (rather than string-matching `argv[0]`). Hiding the probe behind the executor means the wrapper would have to expose a second return value just for the label, or `argv` would have to encode it.
- Keeping the probe and executor as two seams matches the natural decomposition: probe = "what's there", decide = "what to run" (the pure `DetectRestartCommand`), execute = "run it". Each step is independently testable; the wiring composes them.

### Why stat both paths regardless of `runtime.GOOS`

`defaultProbeRestart` runs `os.Stat` on both the launchd plist and the systemd unit on every invocation. Could branch on `runtime.GOOS == "darwin"` to skip the systemd stat (and vice versa), saving one syscall per `pyry update` run on systems with a managed daemon. Not worth it: `os.Stat` of a non-existent path is microseconds, the whole flow already does multi-megabyte HTTP transfers, and the branch would add a `runtime.GOOS` check the rest of the codebase deliberately doesn't have. Stat both, treat any error as "not present", let the boolean fall out.

### Why the executor is `func(ctx, argv) error`, not a more general handle

Could expose `func(ctx, argv) (*exec.Cmd, error)` so tests inspect richer detail (env, working dir). Rejected: the integration test cares about argv only; AC #5 says "assert that argv matches the expected launchctl/systemctl command." Anything richer is YAGNI. The wrapper signature mirrors what the production default actually does — exec the argv, return its error — and gives the test the one thing it needs.

## Consequences

**Going forward:**

- New "where is the daemon installed" probes (e.g. for `pyry restart`, `pyry status` augmentations) follow the same pattern: stat live next to the consuming verb in `cmd/pyry`, decision lives as a pure function in `internal/update` or `internal/install`.
- `internal/update/restart.go` continues to grow only by extending the `RestartProbe` struct or refining `DetectRestartCommand`'s logic; no `os.Stat` ever lands there.
- Renamed daemons (`pyry install-service --name <other>`) are silently skipped on update because the probe hardcodes `dev.pyrycode.pyry.plist` / `pyry.service`. Documented limitation; if observed, the fix is a new probe variant (still inline in `cmd/pyry`), not a refactor of `internal/update/restart.go`.
- Integration tests of any future restart-related verbs follow the same seam pattern (`probeRestart` + `runRestart` on the verb's options struct). No build tags, no globals.

**Trade-offs accepted:**

- The real `defaultProbeRestart` and `defaultRunRestart` are not unit-tested. Manual smoke test (Mac with `pyry install-service`-installed daemon) covers them. Both are short and well-understood; risk-vs-cost favours leaving them uncovered rather than building a tempdir-based fake-`$HOME` rig.
- Two seams instead of one. Marginal cost; balanced by clean separation of "what's there" from "run this" and by the manager-label derivation reading naturally from the probe.

## Related

- [ADR 002](002-pty-supervisor.md) — establishes `cmd/pyry` as the integration layer.
- [`internal/update/restart.go`](../../../internal/update/restart.go) — the pure decision function this ADR keeps pure.
- [`features/pyry-update-command.md`](../features/pyry-update-command.md) — the verb where the probe lives.
- [`features/update-package.md`](../features/update-package.md) — the `internal/update` package layout.
- [`docs/specs/architecture/190-update-daemon-restart-wiring.md`](../../specs/architecture/190-update-daemon-restart-wiring.md) — build-time spec.
