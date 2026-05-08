# Architecture — `pyry update` daemon-restart wiring (#190)

## Files to read first

- `cmd/pyry/update.go` (whole file, 159 lines) — existing `runUpdate` / `doUpdate` / `updateOptions`. New code lands here. The seam pattern (function-typed fields on `updateOptions` populated by `runUpdate`, swapped by tests) is established; copy it for the two new seams.
- `cmd/pyry/update_test.go` (whole file, 293 lines) — the four existing tests. Two of them (`TestUpdate_Success`, `TestUpdate_PinVersion`) reach the replace step and therefore the new restart step; both need the two new seams populated. The other three short-circuit before replace and don't.
- `internal/update/restart.go` (whole file, 36 lines) — `RestartProbe` struct (3 fields) and `DetectRestartCommand` pure function. Already merged in #181. Spec consumes it as-is; do not modify.
- `internal/update/restart_test.go` (whole file, ~50 lines) — confirms the four DetectRestartCommand outputs (`launchd_only`, `systemd_only`, `both_present_launchd_wins`, `none`). Re-read to match exact argv shape when asserting in the new test.
- `cmd/pyry/main.go:140-145` — `main()` calls `run()`; any error returned becomes `pyry: <err>\n` on stderr + exit 1. The shape of the restart-failure error message must read sensibly under that prefix.
- `cmd/pyry/main.go:165-167` — dispatch into `runUpdate`. Unchanged by this ticket.
- `internal/install/install.go:184-186` — canonical service-file paths. Match these exactly: `~/.config/systemd/user/<name>.service` and `~/Library/LaunchAgents/dev.pyrycode.<name>.plist`. The probe uses the production `pyry` name (no `--name` override flow for restart).

## Context

The `pyry update` verb (#189) lands the new binary on disk via `update.AtomicReplace` and stops. Users with a managed daemon unit (launchd plist on macOS or systemd `--user` service on Linux) currently have to run `launchctl kickstart -k gui/<uid>/dev.pyrycode.pyry` or `systemctl --user restart pyry` themselves. This ticket closes that gap: after the replace step succeeds, probe for a managed unit, and if present, exec the restart command. The pure decision function (`internal/update/restart.go`, #181) is already in place; this is purely the wiring slice.

Three constraints from the issue body shape the design:

1. **Probing lives in `cmd/pyry`, not `internal/update`.** The `os.Stat` calls on platform-specific unit paths are co-located with the subcommand handler so `internal/update/restart.go` stays a pure function with no filesystem dependencies. This is a deliberate boundary established in #181 and reaffirmed here.
2. **Restart executor must be injectable.** The integration test asserts argv without exec'ing real binaries. A function-typed seam on `updateOptions` (matching the existing pattern for `replace`, `executablePath`, etc.) is the natural fit.
3. **`--no-restart` is a silent skip, not a warning.** Both the no-flag-but-no-unit-detected case and the `--no-restart` case print nothing about the restart step — only the existing `==> Updated to <v>.` line.

## Design

### Surface change in `cmd/pyry/update.go`

Three new fields on `updateOptions`:

```go
type updateOptions struct {
    // ... existing fields ...
    noRestart    bool
    probeRestart func() update.RestartProbe                         // probe seam
    runRestart   func(ctx context.Context, argv []string) error     // executor seam
}
```

One new flag on `runUpdate`'s flag set:

```go
noRestart := fs.Bool("no-restart", false, "skip daemon restart even if a managed unit is detected")
```

`runUpdate` populates the two new seams with production defaults (described below) and `noRestart` from the flag, then calls `doUpdate` as today.

### Probe helper (production default for `probeRestart`)

A package-private helper in `cmd/pyry/update.go`:

```go
func defaultProbeRestart() update.RestartProbe {
    home, _ := os.UserHomeDir() // empty home → both Stat calls fail → both bools false → DetectRestartCommand returns nil → silent skip. Acceptable.
    _, plistErr := os.Stat(filepath.Join(home, "Library/LaunchAgents", "dev.pyrycode.pyry.plist"))
    _, unitErr  := os.Stat(filepath.Join(home, ".config/systemd/user", "pyry.service"))
    return update.RestartProbe{
        LaunchdPlistExists: plistErr == nil,
        SystemdUnitExists:  unitErr == nil,
        UID:                strconv.Itoa(os.Getuid()),
    }
}
```

Notes:

- Stat both paths regardless of `runtime.GOOS`. The `DetectRestartCommand` doc already notes that `launchctl` does not exist on Linux; in practice the launchd plist path will not exist on Linux either, so the bool stays false. Keeping the probe platform-agnostic costs one extra syscall and removes a `runtime.GOOS` branch.
- Hardcode the daemon name `pyry`. The `pyry install-service --name <other>` path exists, but updating a renamed daemon is out of scope for this ticket — the issue body explicitly reaches for the canonical `dev.pyrycode.pyry` label / `pyry.service` unit. If someone has installed under a different name, no managed unit is detected and the restart step is silently skipped, which is the documented "no managed daemon" behaviour.
- Don't propagate `os.Stat` errors. `os.IsNotExist` and any other Stat error (permissions, EIO, broken symlink) all collapse to `exists == false`. The sole question the probe answers is "is the file there"; if we can't tell, treat it as absent.

### Executor (production default for `runRestart`)

```go
func defaultRunRestart(ctx context.Context, argv []string) error {
    cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}
```

- Use `exec.CommandContext` so a cancelled `doUpdate` (e.g. SIGINT during a slow `launchctl kickstart`) propagates. The existing flow already plumbs `context.Background()` from `runUpdate`; this is forward-compatible.
- Wire the child's stdio to the real terminal so any error output from `launchctl` / `systemctl` reaches the user verbatim. The wrapper's own progress line (`==> Restarting daemon (...)`) prints to `o.out`; the child's diagnostics print to the real stdio.

### Restart block at the end of `doUpdate`

Inserted immediately after the existing `fmt.Fprintf(o.out, "==> Updated to %s.\n", targetVer)` is **moved** to after the restart step (so the success line is the very last thing printed in the happy path):

```go
// existing code through replace step ...
if err := o.replace(target, bin, 0o755); err != nil {
    return fmt.Errorf("update: replace binary: %w", err)
}

if !o.noRestart {
    probe := o.probeRestart()
    if argv := update.DetectRestartCommand(probe); argv != nil {
        manager := "launchd"
        if probe.SystemdUnitExists && !probe.LaunchdPlistExists {
            manager = "systemd"
        }
        // The argv's last element is the unit identifier — useful in the progress line.
        fmt.Fprintf(o.out, "==> Restarting daemon (%s: %s)...\n", manager, argv[len(argv)-1])
        if err := o.runRestart(ctx, argv); err != nil {
            return fmt.Errorf("update: binary replaced to %s, but daemon restart failed: %w", targetVer, err)
        }
    }
}

fmt.Fprintf(o.out, "==> Updated to %s.\n", targetVer)
return nil
```

Three deliberate choices in this block:

- **The progress line follows the issue body's shape.** Issue example: `==> Restarting daemon (launchd: gui/501/dev.pyrycode.pyry)...`. The argv's last element is the launchd domain target or systemd unit name, which matches that example for launchd. For systemd, the last element is `pyry` (the unit name without `.service`). If the line should read `(systemd: pyry)` that's fine; if it should be `(systemd: pyry.service)` we'd need to adjust — flag this in **Open Questions** below.
- **Tie-break the manager label using the probe, not the argv.** `DetectRestartCommand`'s tie-breaker (launchd wins when both are present) is preserved by reading from the probe directly. This avoids string-matching on argv[0].
- **Error message foregrounds "binary replaced" first.** Under main's `pyry: <err>` prefix the user sees: `pyry: update: binary replaced to v0.9.2, but daemon restart failed: exit status 1`. Single line, exit code 1 — meets AC #4. The new version is on disk; the user can retry the restart manually.

### Why move the `==> Updated to ...` line

Today it prints immediately after replace. With the restart step inserted, two orderings are possible:

1. Print `Updated to ...`, then `Restarting daemon ...`. Reads: "we're done — oh, and one more thing".
2. Print `Restarting daemon ...`, then `Updated to ...`. Reads: "doing the last step → all done."

Option 2 matches the issue body's example output and the natural shell convention (the success line is terminal). The move is one line, no semantic risk: if the restart fails, we return early and the success line never prints — which is correct, because a failed restart is a partial-success case the user must act on.

## Concurrency model

None. `doUpdate` is sequential and single-threaded. `exec.CommandContext` blocks until the child exits; that's intentional — we want the user to see the restart complete (or fail) before pyry's process exits. No goroutines, no channels, no locks introduced.

## Error handling

Three failure modes for the restart step:

1. **No managed unit detected.** `DetectRestartCommand` returns nil → skip silently → print success line → exit 0. Matches AC silent-skip requirement.
2. **`--no-restart` set.** Skip the entire block → print success line → exit 0. The probe is not even called (cheap optimisation; also avoids a stat on systems where the user explicitly opted out).
3. **Restart command fails (exec error or non-zero exit).** Return wrapped error → main prints `pyry: update: binary replaced to <v>, but daemon restart failed: <reason>` → exit 1. The new binary remains on disk; the user retries the restart manually. AC #4 satisfied.

No new error sentinels, no new error types. The existing wrap-with-context pattern (`fmt.Errorf("update: ...: %w", err)`) is preserved.

## Testing strategy

Extend `cmd/pyry/update_test.go` with three new tests, plus one update to existing tests.

### New tests

**`TestUpdate_RestartLaunchd`** — happy path on darwin shape:
- Reuse `newFakeReleaseServer` and the existing tempdir target.
- Set `probeRestart: func() update.RestartProbe { return update.RestartProbe{LaunchdPlistExists: true, UID: "501"} }`.
- Set `runRestart` to a closure that records its argv into a captured slice and returns nil.
- Assert recorded argv == `[]string{"launchctl", "kickstart", "-k", "gui/501/dev.pyrycode.pyry"}`.
- Assert `out` contains `==> Restarting daemon (launchd: gui/501/dev.pyrycode.pyry)...` and `==> Updated to v0.9.2.`.

**`TestUpdate_RestartSystemd`** — happy path on linux shape:
- Probe returns `{SystemdUnitExists: true}`.
- Recorded argv == `[]string{"systemctl", "--user", "restart", "pyry"}`.
- Assert progress line includes `systemd:` not `launchd:`.

**`TestUpdate_NoRestartFlag`** — AC #1:
- Probe returns `{LaunchdPlistExists: true, UID: "501"}` (a managed unit IS present).
- `noRestart: true`.
- `runRestart` is a closure that calls `t.Fatalf("runRestart must not be called when --no-restart is set")`.
- Assert success line still prints, no restart progress line in output.

**`TestUpdate_NoManagedUnit`** — AC silent-skip:
- Probe returns zero-value `RestartProbe{}` (nothing detected).
- `runRestart` is a `t.Fatalf` sentinel.
- Assert success line prints, no restart progress line.

**`TestUpdate_RestartFailure`** — AC #4:
- Probe returns `{LaunchdPlistExists: true, UID: "501"}`.
- `runRestart` returns `errors.New("exit status 1")`.
- Assert `doUpdate` returns an error whose `.Error()` contains both `binary replaced to v0.9.2` and `daemon restart failed`.
- Assert the success line `==> Updated to v0.9.2.` is NOT in the output (because we returned early).

### Update to existing tests

`TestUpdate_Success` and `TestUpdate_PinVersion` reach the replace step and therefore execute the restart block. Add to both:

```go
probeRestart: func() update.RestartProbe { return update.RestartProbe{} },
runRestart:   func(context.Context, []string) error { t.Fatalf("runRestart must not be called when no managed unit is detected"); return nil },
```

The other three existing tests (`TestUpdate_AlreadyAtLatest`, `TestUpdate_CheckOnly`, `TestUpdate_DevBuildSkips`) short-circuit before replace and never reach the restart block; they need no changes. The dev-build path returns nil from `doUpdate` before `o.replace` is even consulted, so the restart fields can stay nil there.

### What we're not testing

- The real `os.Stat` probe paths. That's the production default; testing it would require manipulating `$HOME` and creating real plist/unit files in a tempdir. The probe helper is dead simple (two stats and a `strconv.Itoa`); the risk-vs-cost tradeoff favours leaving it covered only by the manual smoke test below.
- The real `exec.CommandContext` path. Same reasoning — the wrapper is three lines and well-understood; testing it would require a fake `launchctl` on `$PATH`.

### Manual smoke test (post-merge)

On a Mac with the daemon installed via `pyry install-service` and bootstrapped:

```
$ pyry update --version v<previous>
$ pyry update    # exercises real probe + real launchctl kickstart
```

Expected: `==> Restarting daemon (launchd: gui/<uid>/dev.pyrycode.pyry)...` followed by `==> Updated to <v>.`. `launchctl print gui/<uid>/dev.pyrycode.pyry | grep "last exit code"` should show a fresh start time.

## Open questions

1. **Systemd progress-line argv element.** The current design renders the systemd case as `==> Restarting daemon (systemd: pyry)...` (last argv element is the unit name without `.service`). The launchd case renders as `==> Restarting daemon (launchd: gui/501/dev.pyrycode.pyry)...`. The launchd shape exactly matches the issue body example; the systemd shape is unspecified. Acceptable as-is; resolve during implementation if it reads weirdly.

2. **Renamed daemons (`pyry install-service --name elli`).** The probe is hardcoded to `dev.pyrycode.pyry.plist` / `pyry.service`. Renamed installations are silently skipped on update. This matches the issue body's framing ("no managed unit is detected → skip silently") but is a known limitation worth a follow-up ticket if anyone hits it. Out of scope here.

3. **`--no-restart` interaction with `--check`.** `--check` exits before the replace step, so `--no-restart` is a no-op on that path. No special-casing needed; the existing `if o.checkOnly { return nil }` short-circuit handles it.
