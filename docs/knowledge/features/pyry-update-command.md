# `pyry update` — user-facing self-update command

The CLI wiring (#189, extended in #190) that ties the `internal/update` primitives into a single self-update flow plus daemon-restart wiring. See [update-package.md](update-package.md) for the building-block primitives this command composes.

## What it does

```
$ pyry update
==> Current version: 0.9.1
==> Latest version:  v0.9.2
==> Downloading pyry_0.9.2_Darwin_arm64.tar.gz...
==> Verifying SHA-256... ok
==> Replacing /Users/me/.local/bin/pyry...
==> Restarting daemon (launchd: gui/501/dev.pyrycode.pyry)...
==> Updated to v0.9.2.
```

When already at latest, prints `==> Current version: <v> — already at latest.` and exits 0 without downloading. When `currentVersion == "dev"`, prints `==> Running a development build (dev); skipping update.` and exits 0 (a development build comes from `go install` or `make build`; replacing it with a release tarball would silently revert the working copy).

When no managed daemon unit is present (or `--no-restart` is set), the restart progress line is skipped silently and the success line still prints.

## Flags

| Flag | Behaviour |
|------|-----------|
| `--check` | Print current + latest versions, then exit 0. Skips download/verify/replace. |
| `--version <tag>` | Pin the target tag (e.g. `--version v0.9.0` for a downgrade). Skips the latest-release API call entirely. |
| `--no-restart` | Skip the daemon-restart step even if a managed unit is detected. The binary swap still happens; the user runs `launchctl kickstart` / `systemctl --user restart pyry` themselves later. |

Errors print as `pyry: update: <step>: <inner>` to stderr (via `main()`'s standard wrapper) and exit non-zero. Each step in the flow contributes its own context prefix: `update: fetch latest release: …`, `update: download tarball: …`, `update: verify checksum: …`, `update: replace binary: …`.

## Homebrew hint

If `os.Executable()` resolves under `/opt/homebrew/`, the command prints a one-line hint before the rest of the flow runs:

```
Hint: this pyry was installed via Homebrew; consider 'brew upgrade pyry' instead.
```

Non-blocking — the update still proceeds. The hint exists because a Homebrew-installed pyry will hit `AtomicReplace` and successfully overwrite the binary in `/opt/homebrew/bin/pyry`, but the user's *next* `brew upgrade` will revert it. Refusing would block the Homebrew user who knowingly wants the GitHub-release version.

## Architecture

`runUpdate(args []string) error` parses flags and dispatches to a private `doUpdate(ctx, updateOptions) error`. The `updateOptions` struct is the integration-test seam — production callers populate it once with real defaults inside `runUpdate`; tests substitute `httptest`-driven equivalents.

```go
type updateOptions struct {
    currentVersion string
    goos, goarch   string
    repo           string
    releaseBaseURL string                                // "https://github.com/<owner>/<repo>/releases/download"
    fetcher        *update.Fetcher
    executablePath func() string
    replace        func(target string, data []byte, mode os.FileMode) error
    out            io.Writer
    checkOnly      bool
    pinVersion     string
    noRestart      bool
    probeRestart   func() update.RestartProbe                       // probe seam (#190)
    runRestart     func(ctx context.Context, argv []string) error   // executor seam (#190)
}
```

Six field-level seams the integration tests need: (a) `Fetcher.BaseURL` for the latest-release call (already a Fetcher field), (b) `releaseBaseURL` for the tarball + checksums URL templating, (c) `executablePath()` so tests point at a tempdir file rather than the real `/usr/local/bin/pyry`, (d) `out` for capturing progress lines without racing stdout, (e) `probeRestart` so tests fixture a `RestartProbe` instead of stat'ing real plist/unit paths, and (f) `runRestart` so tests record argv instead of exec'ing real `launchctl` / `systemctl`. Bundling them into one struct keeps the `runUpdate → doUpdate` boundary single-argument and lets every default land in one place. No `init()`, no global vars, no `httptest` baked into production code.

### Flow

1. Resolve the executable path (`os.Executable()`, falling back to `os.Args[0]`); print Homebrew hint if applicable.
2. Print `==> Current version: <v>`.
3. If `--version <tag>` is set, use that. Otherwise call `Fetcher.FetchLatestRelease(ctx, "pyrycode/pyrycode")` → `update.ParseLatestRelease(body)` to extract `tag_name`. Print `==> Latest version:  <v>`.
4. `update.CompareVersions(current, target)` — branches: `ErrInvalidVersion` (dev build) → "skipping update", return nil; `Same` → "already at latest", return nil; else continue.
5. If `--check`, return nil here.
6. `update.AssetName(target, runtime.GOOS, runtime.GOARCH)` produces the GoReleaser tarball filename. URLs are templated against `releaseBaseURL`: `<base>/<tag>/<asset>` and `<base>/<tag>/checksums.txt`.
7. `Fetcher.FetchAsset` for the tarball, then for `checksums.txt`. `update.ParseChecksumsFile(body, asset)` plucks the SHA-256 hex.
8. `update.VerifySHA256(tgz, digest)`. On mismatch, print `FAIL` and return wrapped `ErrChecksumMismatch`.
9. `update.ExtractBinary(tgz, "pyry")` returns the new binary's bytes.
10. `update.AtomicReplace(target, bin, 0o755)` swaps the on-disk binary.
11. **Daemon restart (#190).** Unless `--no-restart` is set, call `o.probeRestart()` to stat the canonical launchd plist (`~/Library/LaunchAgents/dev.pyrycode.pyry.plist`) and systemd user-unit (`~/.config/systemd/user/pyry.service`) paths. Pass the resulting `RestartProbe` to `update.DetectRestartCommand`. If non-nil argv is returned, print `==> Restarting daemon (<manager>: <last-argv-element>)...` and call `o.runRestart(ctx, argv)`. If the probe returns no managed unit (both stats fail), the step is silently skipped.
12. Print `==> Updated to <v>.` — last in the happy path so it terminates the output.

### Daemon-restart wiring (#190)

The probe lives **inline in `cmd/pyry/update.go`**, not in `internal/update`. `internal/update/restart.go` (#181) stays a pure function (`DetectRestartCommand(probe) → argv`) with no filesystem dependencies; the `os.Stat` calls on platform-specific unit paths are the only such calls in the codebase and they're co-located with the subcommand handler that consumes them. See [ADR 015](../decisions/015-update-restart-probe-inline.md).

**`defaultProbeRestart`** stats both paths regardless of `runtime.GOOS` — on Linux the launchd plist won't exist anyway (bool stays false), so the `os.Stat` is one extra wasted syscall and removes a `runtime.GOOS` branch from the wiring. Stat errors of any kind (`ENOENT`, `EACCES`, `EIO`, broken symlink) collapse to "not present"; the only question the probe answers is "is the file there." If `os.UserHomeDir()` fails, both stats fail → both bools false → `DetectRestartCommand` returns nil → silent skip. The probe hardcodes the daemon name `pyry`; a renamed install (`pyry install-service --name elli`) is silently skipped on update — known limitation, follow-up if anyone hits it.

**`defaultRunRestart`** uses `exec.CommandContext` so a cancelled `doUpdate` propagates to a slow `launchctl kickstart`. Child stdio is wired to the real terminal so `launchctl` / `systemctl` diagnostics reach the user verbatim while the wrapper's own `==> Restarting daemon (...)` line goes to `o.out`.

**Manager label** (`launchd` / `systemd`) is derived from the `RestartProbe` directly, not by string-matching on `argv[0]`. The tie-break (launchd wins when both are present) is encoded once in `DetectRestartCommand`; the wiring reads from the probe to stay consistent.

**Progress-line shape.** The argv's last element is the unit identifier — for launchd it's the domain target `gui/<uid>/dev.pyrycode.pyry` (matches the issue body example exactly); for systemd it's the unit name `pyry` (without the `.service` suffix).

**Restart-failure error message foregrounds "binary replaced".** Under main's `pyry: <err>` prefix the user sees `pyry: update: binary replaced to v0.9.2, but daemon restart failed: exit status 1`. The new version is on disk; the user retries the restart manually. Single line, exit 1.

**`==> Updated to <v>.` is last.** It used to print immediately after replace; now the restart line comes between replace and success. The success line is terminal — "doing the last step → all done." If the restart fails, `doUpdate` returns early and the success line never prints, which is correct: a failed restart is a partial-success the user must act on.

### Why URL-template the asset URLs

`ParseLatestRelease` extracts `tag_name` only — it does not decode `assets[*].browser_download_url`. The URL template `https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>` is GitHub's documented stable scheme and matches what `.goreleaser.yaml` produces. The same `.goreleaser.yaml`-coupling caveat that already applies to `AssetName` covers this — both values live in this repo, both move together if GitHub ever changes the scheme.

### Why a 60-second HTTP timeout

The fetcher exposes `HTTPClient` for caller-supplied timeouts; the wiring caller picks a sane default. 60 s is generous for a ~20 MiB tarball on a slow connection but bounded enough that a hung `pyry update` doesn't sit forever. Configurable via `--http-timeout` is YAGNI — users can re-run if they need a higher value.

### Why `fmt.Fprint`/`fmt.Fprintf` rather than `log/slog`

This is a one-shot CLI verb, not a daemon. Output is human-facing progress lines on stdout, not structured-logging events for log aggregation. `runInstallService` already uses `fmt.Printf` for the same reason.

## Concurrency

Sequential. One goroutine (the calling one). One `context.Context` (`context.Background()` from `runUpdate`; tests pass `t.Context()`). No locks, no channels, no goroutine fan-out.

The two HTTP fetches (tarball + checksums) are issued back-to-back rather than in parallel. Parallelising would shave maybe a second on a fast connection, requires an `errgroup`, and complicates the `==> Downloading <asset>...` progress-line ordering — not worth the complexity.

If a future ticket adds Ctrl-C handling, swap `context.Background()` for `signal.NotifyContext(ctx, os.Interrupt)`. The Fetcher already honours context cancellation per #182 AC.

## Out of scope (handled in follow-up tickets or deferred)

- **Renamed daemons** (`pyry install-service --name <other>`). The probe is hardcoded to `dev.pyrycode.pyry.plist` / `pyry.service`; renamed installs are silently skipped on update (treated as "no managed unit"). Follow-up if observed.
- **Rollback / `.bak` of the old binary.** Deferred entirely. `AtomicReplace` already provides crash-safety up to the rename; partial-write corruption is structurally unreachable. Rollback after a successful but undesired update is a separate feature with its own design (where to store the old binary, how to garbage-collect, how the user invokes it).
- **Scheduled auto-update / channel selection.** Deferred. Only the `latest` GitHub release is consulted; pre-release tags are ignored.
- **Ctrl-C / SIGINT handling.** `context.Background()` today; swap to `signal.NotifyContext` if the need arises.
- **Retry on transient network failure.** Forbidden by `Fetcher`'s AC. Operator re-runs `pyry update`.

## Error contract

All errors propagate up through `main()`'s wrapper at `cmd/pyry/main.go:141-144`, which prints `pyry: <err>` to stderr and exits 1.

| `errors.Is` predicate | Behaviour |
|-----------------------|-----------|
| `update.ErrInvalidVersion` (from `CompareVersions` with `currentVersion == "dev"`) | Print "running a development build" and return nil (exit 0). The only case where a primitive's error is converted to a non-error path. |
| `update.ErrUnsupportedPlatform` (from `AssetName` on e.g. `freebsd/amd64`) | Propagates as `pyry: update: asset name for freebsd/amd64: unsupported os/arch`. The four supported `linux/darwin × amd64/arm64` combos cover the project's published targets. |
| `update.ErrChecksumMismatch` | `==> Verifying SHA-256... FAIL` followed by `pyry: update: verify checksum: …`. No retry, no fallback — a checksum miss is either GitHub serving a corrupt asset or a hostile MITM, and silently retrying masks both. |
| `update.ErrBinaryNotInArchive` / `update.ErrMalformedArchive` | Indicates an upstream packaging regression; the user sees the wrapped error and re-files an issue. |
| Restart-step failure (#190) | Wrapped as `update: binary replaced to <v>, but daemon restart failed: <inner>` — surfaces under `pyry:` prefix, exit 1. The binary swap already succeeded; the user retries restart manually with `launchctl kickstart` / `systemctl --user restart pyry`. |
| `context.Canceled` (test-only path until Ctrl-C handling lands) | Propagates verbatim. |

No partial-failure cleanup: `AtomicReplace` is the only filesystem-mutating step, and it's all-or-nothing (the temp file is removed on its own error paths per #187). A failed restart leaves the new binary on disk by design — the user is told both facts in one error line.

## Tests

`cmd/pyry/update_test.go` (~460 LOC). Ten integration tests, each driving `doUpdate` with the `Fetcher` pointed at an `httptest.NewServer` (canned release JSON + tar.gz fixture + matching `checksums.txt`) and the install path set to a tempdir.

| Test | Pins |
|------|------|
| `TestUpdate_Success` | AC #2: fetch + verify + extract + replace. On-disk binary swapped; all progress lines print verbatim. `runRestart` is a `t.Fatalf` sentinel since the probe returns zero-value `RestartProbe{}`. |
| `TestUpdate_AlreadyAtLatest` | AC #2 short-circuit: when current == latest, `AtomicReplace` is not called and the "already at latest" line prints. |
| `TestUpdate_CheckOnly` | AC #3: `--check` prints current + latest and exits without downloading. |
| `TestUpdate_PinVersion` | AC #3: `--version <v>` skips the latest-release API call (the fake handler 500s if hit) and downloads from the pinned URL. |
| `TestUpdate_DevBuildSkips` | The `currentVersion == "dev"` branch: `CompareVersions` returns `ErrInvalidVersion`, the wiring prints "skipping update" and exits 0 without `AtomicReplace`. |
| `TestUpdate_RestartLaunchd` | #190 happy path on darwin shape: probe returns `{LaunchdPlistExists: true, UID: "501"}`, `runRestart` records argv. Asserts argv == `[launchctl, kickstart, -k, gui/501/dev.pyrycode.pyry]` and the `(launchd: gui/501/dev.pyrycode.pyry)` progress line. |
| `TestUpdate_RestartSystemd` | #190 happy path on linux shape: probe returns `{SystemdUnitExists: true}`, argv == `[systemctl, --user, restart, pyry]`, progress line says `systemd:`. |
| `TestUpdate_NoRestartFlag` | #190 AC #1: `noRestart: true` plus a probe that *would* match. `runRestart` is a `t.Fatalf` sentinel — proves the flag short-circuits before the probe even runs. |
| `TestUpdate_NoManagedUnit` | #190 silent-skip AC: zero-value `RestartProbe{}`. Success line prints, no restart progress line, `runRestart` never called. |
| `TestUpdate_RestartFailure` | #190 AC #4: `runRestart` returns `errors.New("exit status 1")`. Asserts the error contains both `binary replaced to v0.9.2` and `daemon restart failed`, and that the success line is NOT in the captured output (returned early). |

Helpers `buildTarGzForTest`, `fakeRelease`, `newFakeReleaseServer` are inline in the test file rather than shared with `internal/update/install_test.go` — the test surface is ~10 lines and an `internal/testutil` package would be heavier than the duplication.

Real `os.Stat` paths and the real `exec.CommandContext` wrapper are deliberately not unit-tested. The probe helper is two stats + a `strconv.Itoa`; the executor is three lines. Testing them would require manipulating `$HOME` + creating fake plist/unit files, and putting a fake `launchctl` on `$PATH`. Manual smoke test on a Mac with the daemon installed via `pyry install-service` covers the production paths.

## Files

- `cmd/pyry/update.go` (~215 LOC) — `runUpdate`, `resolveExecutable`, `updateOptions`, `defaultProbeRestart`, `defaultRunRestart`, `doUpdate`.
- `cmd/pyry/update_test.go` (~460 LOC) — integration tests + httptest fixtures.
- `cmd/pyry/main.go` — `case "update":` dispatch + `printHelp` entry.
- `internal/update/restart.go` — `RestartProbe` + `DetectRestartCommand` (#181, consumed unchanged).
- `docs/guide.md` — "Updating pyry" section.

## Related

- [`update-package.md`](update-package.md) — the `internal/update` primitives this command composes.
- [ADR 015](../decisions/015-update-restart-probe-inline.md) — daemon-restart probe placement and executor seam.
- [`docs/specs/architecture/189-update-subcommand-wiring.md`](../../specs/architecture/189-update-subcommand-wiring.md) — #189 build-time spec.
- [`docs/specs/architecture/190-update-daemon-restart-wiring.md`](../../specs/architecture/190-update-daemon-restart-wiring.md) — #190 build-time spec.
- `cmd/pyry/main.go` — `var Version = "dev"` is the input that triggers the dev-build skip branch.
- `.goreleaser.yaml` — the build matrix `AssetName` mirrors and the source of the published tarballs the command consumes.
