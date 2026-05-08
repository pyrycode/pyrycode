# Spec: `internal/update` restart-command detection (#181)

## Files to read first

- `internal/update/version.go:1-58` — package doc-comment voice, sentinel-error idiom (`ErrMalformedRelease`), exported-symbol comment style. Mirror the tone exactly.
- `internal/update/checksum.go:27-54` — `AssetName` is the closest sibling: pure function, takes plain inputs, table-driven body. Same shape applies here.
- `internal/update/version_test.go:1-60` — table-driven test pattern with `t.Parallel()`, named cases, `wantErr` (not used here — no errors — but case naming + `tests := []struct{...}` shape carries over).
- `CODING-STYLE.md` — package-level conventions; in particular doc-comment-on-every-exported-symbol and `gofmt` non-negotiable.
- Issue #181 body — acceptance criteria are the contract; tie-breaker rule (launchd wins) must appear verbatim in the doc comment.

No prior knowledge doc on this slice exists — sister tickets (#178, #179, #180) covered version compare, asset-name, checksum verify. This is the third pure-function slice.

## Context

`pyry update` swaps the binary on disk; the running daemon is still the old version until something kicks it. The decision of *which* command kicks it is platform-dependent (launchctl vs systemctl), but the *probing* of the local environment (does the plist exist? does the unit exist? what's the uid?) is I/O. We split the two so the decision is exhaustively unit-testable without filesystem fixtures or `runtime.GOOS` shims.

This ticket is the decision half. The wiring ticket will perform the probes and call this function with the results.

## Design

### Package placement

Lives in `internal/update` alongside `version.go` and `checksum.go`. No new sub-package — this is a single small pure function, same shape as its siblings.

### Exported surface

```go
// RestartProbe carries the local-environment signals DetectRestartCommand
// needs to choose a restart command. The wiring ticket fills these from
// os.Stat on the platform-specific service file paths and from
// strconv.Itoa(os.Getuid()).
type RestartProbe struct {
    LaunchdPlistExists bool   // ~/Library/LaunchAgents/dev.pyrycode.pyry.plist
    SystemdUnitExists  bool   // ~/.config/systemd/user/pyry.service
    UID                string // numeric uid as string, templated into the launchctl gui/<uid>/... domain
}

// DetectRestartCommand returns the argv (program plus args) of the command
// that restarts a managed pyry daemon based on the supplied probe results,
// or nil when no managed daemon is detected and the caller should print
// "restart your pyry yourself" guidance.
//
// Tie-breaker: when both LaunchdPlistExists and SystemdUnitExists are true,
// launchd wins. Rationale: macOS is pyrycode's primary daily-driver
// platform, so a stray systemd user unit on a Mac (e.g. left over from a
// dotfiles sync) is more likely cruft than the active manager. The reverse
// case — a launchd plist on Linux — cannot occur because launchctl does
// not exist on Linux; the probe will return false.
//
// Pure function: no os.Stat, no runtime.GOOS, no exec. Caller probes and
// supplies inputs.
func DetectRestartCommand(probe RestartProbe) []string
```

### Body

Two-branch switch on the probe, launchd checked first so the tie-breaker falls out naturally:

```go
func DetectRestartCommand(probe RestartProbe) []string {
    switch {
    case probe.LaunchdPlistExists:
        return []string{"launchctl", "kickstart", "-k", "gui/" + probe.UID + "/dev.pyrycode.pyry"}
    case probe.SystemdUnitExists:
        return []string{"systemctl", "--user", "restart", "pyry"}
    default:
        return nil
    }
}
```

That's it. No helpers, no constants, no error path — the function cannot fail, only return `nil` to mean "caller, print guidance".

### Why a struct (`RestartProbe`) rather than three positional bools

Two booleans plus a string read identically at the call site (`DetectRestartCommand(true, false, "501")`) — the struct labels the signals and survives signal additions in future tickets (e.g. an SMF unit on illumos, a Windows service entry) without breaking existing callers.

### Why no `runtime.GOOS` filter

The acceptance criteria spell launchd-wins as the tie-breaker without conditioning on OS. Filtering by GOOS would re-introduce non-purity (the test would need to set GOOS) and would forbid the legitimate "wrong-OS file lying around" case from being deterministic. The probe is the OS filter — the wiring ticket may simply not call `os.Stat` on the launchd path under Linux, in which case `LaunchdPlistExists` stays false and the systemd branch wins. That's a wiring decision, not a decision-half decision.

### Why launchctl `kickstart -k` and not `unload`/`load`

`kickstart -k` SIGTERMs the running instance and starts a fresh one in a single command. `unload`/`load` round-trips the plist and races with `KeepAlive=true`. The `-k` flag is the canonical way to tell launchd "restart this label now" and matches what `man launchctl` recommends. (Confirmed by the broader update-flow design notes; this slice just emits the argv.)

### Why systemctl `--user restart pyry` (not `try-restart` or `reload`)

`restart` is unconditional and matches the user's intent (the binary changed; restart it). `try-restart` would silently no-op if the unit is inactive, and `reload` requires `ExecReload=` which the unit file doesn't define.

## Concurrency model

None. Pure function.

## Error handling

None. The function returns `nil` for the "no managed daemon" case rather than an error — this is a normal outcome, not a failure (foreground users running `pyry` from a terminal are a supported deployment).

## Testing strategy

`internal/update/restart_test.go`, table-driven with `t.Parallel()`, mirroring `checksum_test.go` shape. Four cases, all from the AC:

```go
func TestDetectRestartCommand(t *testing.T) {
    t.Parallel()
    tests := []struct {
        name  string
        probe RestartProbe
        want  []string
    }{
        {
            name:  "launchd_only",
            probe: RestartProbe{LaunchdPlistExists: true, UID: "501"},
            want:  []string{"launchctl", "kickstart", "-k", "gui/501/dev.pyrycode.pyry"},
        },
        {
            name:  "systemd_only",
            probe: RestartProbe{SystemdUnitExists: true},
            want:  []string{"systemctl", "--user", "restart", "pyry"},
        },
        {
            name:  "both_present_launchd_wins",
            probe: RestartProbe{LaunchdPlistExists: true, SystemdUnitExists: true, UID: "1000"},
            want:  []string{"launchctl", "kickstart", "-k", "gui/1000/dev.pyrycode.pyry"},
        },
        {
            name:  "neither_present",
            probe: RestartProbe{},
            want:  nil,
        },
    }
    for _, tc := range tests {
        tc := tc
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()
            got := DetectRestartCommand(tc.probe)
            if !slices.Equal(got, tc.want) {
                t.Errorf("DetectRestartCommand(%+v) = %v, want %v", tc.probe, got, tc.want)
            }
        })
    }
}
```

Use `slices.Equal` (Go 1.21+) for the comparison; `reflect.DeepEqual` would also work but `slices.Equal` is the idiomatic choice for `[]string`. The two bare-string `nil` vs empty-slice distinction is handled correctly by `slices.Equal` (`nil` and `[]string{}` are equal under it — but the AC says `nil` and the implementation returns `nil`, so both sides match exactly).

The UID-templating verification falls naturally out of the `launchd_only` and `both_present_launchd_wins` cases using different UID values ("501" and "1000") so a hard-coded UID would fail at least one case.

No mocking, no filesystem, no goroutines. The whole test file is ~50 lines.

## Open questions

None. The acceptance criteria are exhaustive; the tie-breaker is specified; the tests follow directly from the AC. Wiring (the actual probe + exec) is the next ticket and is explicitly out of scope.
