# `pyry update` — user-facing self-update command

The CLI wiring (#189, extended in #190) that ties the `internal/update` primitives into a single self-update flow plus daemon-restart wiring. See [update-package.md](update-package.md) for the building-block primitives this command composes.

## What it does

```
$ pyry update
==> Current version: 0.9.1
==> Latest version:  v0.9.2
==> Downloading pyry_0.9.2_Darwin_arm64.tar.gz...
==> Verifying signature... ok
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

Errors print as `pyry: update: <step>: <inner>` to stderr (via `main()`'s standard wrapper) and exit non-zero. Each step in the flow contributes its own context prefix: `update: fetch latest release: …`, `update: download tarball: …`, `update: download signature: …`, `update: verify signature: …`, `update: verify checksum: …`, `update: replace binary: …`.

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
    signingPubKey  ed25519.PublicKey                                // #776 — verify checksums.txt.sig against this
    out            io.Writer
    checkOnly      bool
    pinVersion     string
    noRestart      bool
    probeRestart   func() update.RestartProbe                       // probe seam (#190)
    runRestart     func(ctx context.Context, argv []string) error   // executor seam (#190)
}
```

Seven field-level seams the integration tests need: (a) `Fetcher.BaseURL` for the latest-release call (already a Fetcher field), (b) `releaseBaseURL` for the tarball + checksums URL templating, (c) `executablePath()` so tests point at a tempdir file rather than the real `/usr/local/bin/pyry`, (d) `out` for capturing progress lines without racing stdout, (e) `probeRestart` so tests fixture a `RestartProbe` instead of stat'ing real plist/unit paths, (f) `runRestart` so tests record argv instead of exec'ing real `launchctl` / `systemctl`, and (g) `signingPubKey` (#776) so tests inject a throwaway test key and sign fixtures with its private half rather than needing the production private key. Bundling them into one struct keeps the `runUpdate → doUpdate` boundary single-argument and lets every default land in one place. No `init()`, no global vars, no `httptest` baked into production code.

### Flow

1. Resolve the executable path (`os.Executable()`, falling back to `os.Args[0]`); print Homebrew hint if applicable.
2. Print `==> Current version: <v>`.
3. If `--version <tag>` is set, use that. Otherwise call `Fetcher.FetchLatestRelease(ctx, "pyrycode/pyrycode")` → `update.ParseLatestRelease(body)` to extract `tag_name`. Print `==> Latest version:  <v>`.
4. `update.CompareVersions(current, target)` — branches: `ErrInvalidVersion` (dev build) → "skipping update", return nil; `Same` → "already at latest", return nil; else continue.
5. If `--check`, return nil here.
6. **Refuse a non-pinned downgrade (#1498).** If `o.pinVersion == ""` and `cmp == update.Newer` (the running binary is newer than the advertised latest — a yanked release or a compromised publishing token re-marking an old genuine release as latest), return `update: refuse downgrade: …` naming both versions and the sanctioned `--version <tag>` route. No fetch has happened yet. An explicit `--version <older-tag>` pin skips this guard entirely and proceeds as the intentional downgrade route.
7. `update.AssetName(target, runtime.GOOS, runtime.GOARCH)` produces the GoReleaser tarball filename. URLs are templated against `releaseBaseURL`: `<base>/<tag>/<asset>` and `<base>/<tag>/checksums.txt`.
8. `Fetcher.FetchAsset` for the tarball, then for `checksums.txt`.
9. **Signature gate (#776).** `Fetcher.FetchAsset` for `checksums.txt.sig` (the checksums URL + `.sig`), then `update.VerifySignature(sumsBytes, sig, o.signingPubKey)` over the **raw** checksums bytes. Prints `==> Verifying signature... ok`/`FAIL`. A missing `.sig` (404) aborts here with `update: download signature: …` — the fail-closed point, structurally identical to a missing tarball, with **no unsigned fallback**. A non-verifying signature aborts with `update: verify signature: …`. Both abort *before* any digest is parsed or the binary extracted/replaced. See [ADR 028](../decisions/028-ed25519-checksums-signature.md).
10. `update.ParseChecksumsFile(body, asset)` plucks the SHA-256 hex (only reached once the signature has vouched for the checksums bytes).
11. `update.VerifySHA256(tgz, digest)`. On mismatch, print `FAIL` and return wrapped `ErrChecksumMismatch`.
12. `update.ExtractBinary(tgz, "pyry")` returns the new binary's bytes.
13. `update.AtomicReplace(target, bin, 0o755)` swaps the on-disk binary.
14. **Daemon restart (#190).** Unless `--no-restart` is set, call `o.probeRestart()` to stat the canonical launchd plist (`~/Library/LaunchAgents/dev.pyrycode.pyry.plist`) and systemd user-unit (`~/.config/systemd/user/pyry.service`) paths. Pass the resulting `RestartProbe` to `update.DetectRestartCommand`. If non-nil argv is returned, print `==> Restarting daemon (<manager>: <last-argv-element>)...` and call `o.runRestart(ctx, argv)`. If the probe returns no managed unit (both stats fail), the step is silently skipped.
15. Print `==> Updated to <v>.` — last in the happy path so it terminates the output.

**Why the guard sits below `--check` (#1498).** Placing it above (e.g. as a fourth compare-switch arm) would make `pyry update --check` exit non-zero against a rolled-back latest, breaking the documented exit-0 contract that `docs/release-tooling.md`'s release checklist depends on. Placing it below `AssetName` would let a refused downgrade still issue fetches. The guard's position is an observable ordering constraint, pinned by `TestUpdate_CheckOnlyOlderLatest` (fails if the guard drifts above the `checkOnly` return) and `TestUpdate_UnpinnedDowngradeRefused`'s request-recording assertion (fails if the guard drifts below `AssetName`).

### Signature gate (#776)

The trust root is a package-level constant in `cmd/pyry/update.go`:

```go
const releaseSigningPublicKeyHex = "<64 hex chars>" // raw 32-byte Ed25519 public key
```

`runUpdate` decodes it once (`hex.DecodeString` + a 32-byte length check → `update: signing key: …` on failure) and threads it into `doUpdate` as `updateOptions.signingPubKey`. Kept as a constant (not a new file) to hold the new-file count at three (`signature.go`, `signature_test.go`, `release-tooling.md`). Its doc comment records the `openssl genpkey` / `openssl pkey … | tail -c 32 | xxd -p` generation recipe, notes that the private half lives only as the `PYRY_RELEASE_SIGNING_KEY` Actions secret, and points at [`docs/release-tooling.md`](../../release-tooling.md).

**Fail-closed is structural, not a flag.** The sig-fetch + `VerifySignature` are *unconditional* steps between the checksums fetch and the checksums parse. There is no code path from "signature missing/malformed/non-verifying" to "proceed unsigned." A regenerated keypair means updating the constant **in the same change** as rotating the secret — a mismatch fails every `pyry update` closed (safe, but broken). `TestReleaseSigningKey_Decodes` asserts the constant decodes to 32 bytes so a typo is caught at test time, not release time.

The signing side of the wire lives in `.goreleaser.yaml`'s `signs:` block (`openssl pkeyutl -sign -rawin` over `checksums.txt`) and `.github/workflows/release.yml`'s key-materialization step (`PYRY_RELEASE_SIGNING_KEY` secret → `0600` temp file under `${RUNNER_TEMP}` → `PYRY_SIGNING_KEY_FILE`). See [ADR 028](../decisions/028-ed25519-checksums-signature.md) for the scheme rationale and [`docs/release-tooling.md`](../../release-tooling.md) for the operator procedure.

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
| Non-pinned downgrade (#1498: `o.pinVersion == "" && cmp == update.Newer`) | No sentinel — a plain `update: refuse downgrade: …` string naming both versions and `pyry update --version <tag>` as the sanctioned route. Refuses before any fetch (no tarball, `checksums.txt`, or `checksums.txt.sig` request). `--check` is unaffected — the guard sits below the `checkOnly` early return. `--version <older-tag>` bypasses the guard entirely; it remains the sole unguarded downgrade route. |
| `update.ErrUnsupportedPlatform` (from `AssetName` on e.g. `freebsd/amd64`) | Propagates as `pyry: update: asset name for freebsd/amd64: unsupported os/arch`. The four supported `linux/darwin × amd64/arm64` combos cover the project's published targets. |
| Missing signature asset (`checksums.txt.sig` 404s, or any fetch failure) | `==> Verifying signature... FAIL` followed by `pyry: update: download signature: …`. **Fail-closed (#776, AC-2):** aborts before the digest is parsed, structurally identical to a missing tarball — there is **no** fall-through to the old unsigned behaviour. An attacker who deletes the signature cannot downgrade the check. |
| `update.ErrInvalidSignature` / `update.ErrInvalidPublicKey` (from `VerifySignature`) | `==> Verifying signature... FAIL` followed by `pyry: update: verify signature: …`. A tampered/mis-signed `checksums.txt`, a wrong-length `.sig`, or a malformed baked-in key all abort **before** extract/replace (#776, AC-3). `ErrInvalidPublicKey` only fires on a corrupt `releaseSigningPublicKeyHex` — caught earlier by `TestReleaseSigningKey_Decodes`. |
| `update.ErrChecksumMismatch` | `==> Verifying SHA-256... FAIL` followed by `pyry: update: verify checksum: …`. No retry, no fallback — a checksum miss is either GitHub serving a corrupt asset or a hostile MITM, and silently retrying masks both. (Reached only after the signature gate passes — i.e. a genuinely-signed release whose checksums don't match the tarball.) |
| `update.ErrBinaryNotInArchive` / `update.ErrMalformedArchive` | Indicates an upstream packaging regression; the user sees the wrapped error and re-files an issue. |
| Restart-step failure (#190) | Wrapped as `update: binary replaced to <v>, but daemon restart failed: <inner>` — surfaces under `pyry:` prefix, exit 1. The binary swap already succeeded; the user retries restart manually with `launchctl kickstart` / `systemctl --user restart pyry`. |
| `context.Canceled` (test-only path until Ctrl-C handling lands) | Propagates verbatim. |

No partial-failure cleanup: `AtomicReplace` is the only filesystem-mutating step, and it's all-or-nothing (the temp file is removed on its own error paths per #187). A failed restart leaves the new binary on disk by design — the user is told both facts in one error line.

## Tests

`cmd/pyry/update_test.go` (~600 LOC). A dozen integration tests, each driving `doUpdate` with the `Fetcher` pointed at an `httptest.NewServer` (canned release JSON + tar.gz fixture + matching `checksums.txt` + auto-signed `checksums.txt.sig`) and the install path set to a tempdir, plus one pure `TestReleaseSigningKey_Decodes` unit test.

| Test | Pins |
|------|------|
| `TestUpdate_Success` | AC #2: fetch + verify + extract + replace. On-disk binary swapped; all progress lines print verbatim. `runRestart` is a `t.Fatalf` sentinel since the probe returns zero-value `RestartProbe{}`. |
| `TestUpdate_AlreadyAtLatest` | AC #2 short-circuit: when current == latest, `AtomicReplace` is not called and the "already at latest" line prints. |
| `TestUpdate_CheckOnly` | AC #3: `--check` prints current + latest and exits without downloading. |
| `TestUpdate_PinVersion` | AC #3: `--version <v>` skips the latest-release API call (the fake handler 500s if hit) and downloads from the pinned URL. Doubles as the scope regression guard for #1498's refusal guard: current `0.9.1`, pin `v0.9.0` is itself a downgrade, and must stay green unmodified. |
| `TestUpdate_UnpinnedDowngradeRefused` (#1498) | AC #1/#2: no `--version` pin, served latest older than current → `doUpdate` returns an error containing `update:`, `--version`, and both version strings; a request-recording fake server proves zero asset/checksums/signature requests were made; `replace`/`runRestart` are `t.Fatalf` sentinels; the `==> Updated to` success line is absent. |
| `TestUpdate_CheckOnlyOlderLatest` (#1498) | AC #4: `--check` against the same older-than-current latest still prints both version lines and returns nil — pins the guard's placement below the `checkOnly` early return. |
| `TestUpdate_DevBuildSkips` | The `currentVersion == "dev"` branch: `CompareVersions` returns `ErrInvalidVersion`, the wiring prints "skipping update" and exits 0 without `AtomicReplace`. |
| `TestUpdate_RestartLaunchd` | #190 happy path on darwin shape: probe returns `{LaunchdPlistExists: true, UID: "501"}`, `runRestart` records argv. Asserts argv == `[launchctl, kickstart, -k, gui/501/dev.pyrycode.pyry]` and the `(launchd: gui/501/dev.pyrycode.pyry)` progress line. |
| `TestUpdate_RestartSystemd` | #190 happy path on linux shape: probe returns `{SystemdUnitExists: true}`, argv == `[systemctl, --user, restart, pyry]`, progress line says `systemd:`. |
| `TestUpdate_NoRestartFlag` | #190 AC #1: `noRestart: true` plus a probe that *would* match. `runRestart` is a `t.Fatalf` sentinel — proves the flag short-circuits before the probe even runs. |
| `TestUpdate_NoManagedUnit` | #190 silent-skip AC: zero-value `RestartProbe{}`. Success line prints, no restart progress line, `runRestart` never called. |
| `TestUpdate_RestartFailure` | #190 AC #4: `runRestart` returns `errors.New("exit status 1")`. Asserts the error contains both `binary replaced to v0.9.2` and `daemon restart failed`, and that the success line is NOT in the captured output (returned early). |
| `TestUpdate_MissingSignature` (#776) | AC #2: a server serving tarball + checksums but 404ing `checksums.txt.sig` → `doUpdate` returns an error containing `download signature`; the `replace` seam is a `t.Fatalf` sentinel (proves no `AtomicReplace`); success line absent. |
| `TestUpdate_BadSignature` (#776) | AC #3: a `.sig` signed with a *different* key → `doUpdate` returns an error containing `verify signature`; `replace` sentinel not called. |
| `TestReleaseSigningKey_Decodes` (#776) | Asserts `releaseSigningPublicKeyHex` decodes to exactly 32 bytes — catches a typo/placeholder in the baked-in constant at test time, not release time. |

Helpers `buildTarGzForTest`, `fakeRelease`, `newFakeReleaseServer` are inline in the test file rather than shared with `internal/update/install_test.go` — the test surface is ~10 lines and an `internal/testutil` package would be heavier than the duplication.

**Signature test seam (#776).** A deterministic throwaway keypair (`testSigningPriv/testSigningPub` from `ed25519.NewKeyFromSeed` with a fixed seed) drives the signed-path tests — never the production key. `newFakeReleaseServer` **auto-signs** the checksums bytes it is handed with `testSigningPriv` and serves the detached signature at `checksums.txt.sig`; its call signature is unchanged, so the existing callers keep working (the server signs whatever checksums it serves, so the gate passes and the flow continues to each test's real assertion). `updateOptions` literals that reach the gate set `signingPubKey: testSigningPub`; the early-abort tests (`AlreadyAtLatest`, `CheckOnly`, `DevBuildSkips`) never reach verification and leave it nil.

Real `os.Stat` paths and the real `exec.CommandContext` wrapper are deliberately not unit-tested. The probe helper is two stats + a `strconv.Itoa`; the executor is three lines. Testing them would require manipulating `$HOME` + creating fake plist/unit files, and putting a fake `launchctl` on `$PATH`. Manual smoke test on a Mac with the daemon installed via `pyry install-service` covers the production paths.

### E2E happy-path (#260)

`cmd/pyry/update_e2e_test.go` (~420 LOC, `//go:build (darwin || linux) && e2e_update`) is the release-acceptance gate for the full fetch → verify → atomic-replace → restart → smoke chain. One test (`TestUpdate_HappyPath_E2E`) drives `doUpdate` against the in-process fake release server, with a real running daemon stapled on the back of the `runRestart` seam.

Run with:

```bash
make e2e-update                                                    # go test -tags e2e_update -count=1 ./cmd/pyry/...
PYRY_E2E_BIN=$(pwd)/pyry go test -tags=e2e_update ./cmd/pyry/...   # CI prebuild short-circuit
```

`make e2e-update` (#969) is the named entry point — run it on the release
checklist and before touching `pyry update` code. Unlike `e2e-install`, it is
hermetic (temp HOME only) but stays out of `make check`/`make preship`
alongside it by convention. See [release-tooling.md § Install & update e2e
suites](../../release-tooling.md#install--update-e2e-suites).

| AC | Pinned by |
|----|-----------|
| Atomic replace happened | inode comparison via `syscall.Stat_t.Ino` before/after on `<home>/bin/pyry`; `os.Rename` swaps in a fresh inode so an inode change is reliable. |
| Daemon was restarted | `cmd1.Process.Pid != cmd2.Process.Pid`. |
| Post-update `pyry status` succeeds and Phase advances past `starting` | `waitForPhasePastStartingE2E` polls `pyry status`, parses the `Phase:` line, fails after `e2eUpdPhaseDeadline = 3s` if Phase stays `starting` (the v0.10.1-shaped startup hang signature). |
| Post-update `pyry sessions list` succeeds | `runVerbE2E(... "sessions", "list")` exit 0 within `e2eUpdRunTimeout = 10s`. |
| Supervisor-mode-stdin-startup smoke check | Daemons spawn with `cmd.Stdin = nil`; Go's `os/exec` wires `/dev/null` automatically, no `os.Open(os.DevNull)` needed. |
| No real GitHub round-trip | `fakeRelease` + `newFakeReleaseServer` (reused from `update_test.go` — same package, no build tag) build the tarball + checksums in-process; `doUpdate` is called with `Fetcher.BaseURL` and `releaseBaseURL` pointed at the test server. |

**`runRestart` is a kill-and-respawn stand-in, not a launchctl/systemctl exec.** The test passes a `RestartProbe` whose platform-discriminant flag (`LaunchdPlistExists` on darwin, `SystemdUnitExists` on linux) makes `DetectRestartCommand` return non-nil argv, so the wiring fires `runRestart`. The closure ignores the argv: it stops daemon 1 (SIGTERM → 3s grace → SIGKILL → 1s grace), respawns from the same `targetPath` (now holding the new bytes), and waits for the new socket to be dialable. The test never touches the operator's real launchd/systemd — see [`docs/lessons.md` § "E2E against the operator's real systemd `--user` / launchd `gui/<uid>`"](../../lessons.md).

**Why drive `doUpdate` directly from `package main` rather than exec the binary.** Adding a `--release-base-url` flag or `PYRY_RELEASE_BASE_URL` env var to production code is exactly the "hidden env var added 'just for the test'" pattern lessons.md rejects. The principled alternative — `internal/e2e` imports the package — fails because `cmd/pyry` is `package main`. Same-package e2e is the only seam that satisfies both the AC and the env-var-discipline rule. Cost: the `internal/e2e` harness is unreachable, so the spawn/socket-dial/teardown scaffolding is replicated inline (~120 LOC of helpers) — the same trade `update_test.go` already accepted for `buildTarGzForTest`/`fakeRelease`/`newFakeReleaseServer`.

**Sun_path-safe temp HOME.** Uses `os.MkdirTemp("", "pyry-up-")` rather than `t.TempDir()`. The test name extends `t.TempDir()`'s path past macOS APFS's 104-byte sun_path limit when `<home>/pyry.sock` is appended; same workaround as `internal/e2e/restart_test.go:newRegistryHome`.

**Phase polling, not single-shot.** `supervisor.Run` sets Phase to Running asynchronously after `onSpawn` fires; the control socket can be dialable a few ms before that. `waitForPhasePastStartingE2E` polls every 50ms up to a 3s deadline so a healthy startup never flakes on the timing window, while a v0.10.1-shaped hang (Phase stuck at `starting`) trips the deadline and fails loud with the captured stdout. Single-shot status would race the supervisor's Phase update.

**Helpers (~120 LOC, all `_test.go`-scoped, `_E2E` suffix to avoid collision with `update_test.go`'s helpers):** `buildPyryBinE2E` (honours `PYRY_E2E_BIN`, else `go build`), `copyFileE2E`, `inodeOfE2E` (linux + macOS only — matches the build tag), `childEnvE2E` (HOME isolation + `PYRY_NAME` strip, mirrors `internal/e2e/harness.go:childEnv`), `spawnDaemonE2E` (returns `*exec.Cmd`, `*bytes.Buffer` stdout, `*bytes.Buffer` stderr, `chan struct{}` doneCh; flag set `-pyry-socket=… -pyry-name=test -pyry-claude=/bin/sleep -pyry-idle-timeout=0 -- 99999` — `99999` not `infinity`, see lessons.md), `waitForSocketE2E` (poll-and-dial loop, 50ms gap, short-circuits on doneCh), `stopDaemonE2E` (SIGTERM → 3s → SIGKILL → 1s, idempotent), `runVerbE2E` (`exec.CommandContext` with 10s timeout, auto-injects `-pyry-socket=`), `waitForPhasePastStartingE2E`, `parsePhaseE2E` (returns `(string, bool)` — distinguishes "no Phase: line" from "Phase: line present but empty"). Pre-existing `update_test.go` helpers (`fakeRelease`, `newFakeReleaseServer`, `buildTarGzForTest`) are reused verbatim — they live in the same `package main` with no build tag, so the tagged file picks them up automatically; no extraction was required to satisfy the AC's "reusable helper" criterion.

**`cmd1Stopped` flag avoids double-stop in `t.Cleanup`.** The closure-side stop in `runRestart` and the `t.Cleanup`-side stop both target the same `cmd1`; the boolean ensures only one fires. Same shape as the `cmd2 == nil` guard in the post-test cleanup (handles the "runRestart never fired" case where `doUpdate` errored before the restart step).

**Out of scope for this slice:** cross-architecture updates; a different `Version` string for the served binary (the happy path needs only "different inode + working binary," not behavioural drift). Failure-path coverage landed in #261 — see below.

### E2E failure paths (#261)

Same file (`cmd/pyry/update_e2e_test.go`), same build tag, three new sibling tests (+333 LOC). Each test reuses the spawn/dial/teardown helpers from #260, drives `doUpdate` against a server tailored to inject one failure, and asserts a small set of structural properties.

| Test | What's broken | doUpdate exits at | Asserts |
|------|---------------|-------------------|---------|
| `TestUpdate_FetchFailure_E2E` | Release-asset URL returns HTTP 500 | `FetchAsset` (before AtomicReplace) | Binary unchanged (inode equal), daemon 1 still answering on socket (PID unchanged), no `.pyry.*.tmp` stragglers in `<home>/bin/`, success line absent. |
| `TestUpdate_VerifyFailure_E2E` | `checksums.txt` body lists 64-zero digest for the real tarball | `VerifySHA256` (before AtomicReplace) | Binary unchanged, daemon 1 still answering, success line absent. |
| `TestUpdate_BrokenNewBinary_E2E` | Served tarball contains the `internal/brokenpyry` helper bytes | `runRestart` (AFTER AtomicReplace) | Error message contains `"binary replaced to "` AND `"daemon restart failed"` (version-agnostic, same shape as `TestUpdate_RestartFailure`), inode changed, on-disk bytes equal the broken bytes, broken-pyry stderr contains `BROKEN_PYRY_TOKEN`, success line absent. |

**The broken-binary case is an asserts-the-current-design test, not a rollback test.** Per [Out of scope](#out-of-scope-handled-in-follow-up-tickets-or-deferred) above and `docs/specs/architecture/187-update-atomic-replace.md`: once `AtomicReplace` swaps in the new bytes, the old binary is gone — operator intervention is the only recovery. The error contract pinned by the unit-shaped `TestUpdate_RestartFailure` (`cmd/pyry/update_test.go:438-460`) extends end-to-end here against a real spawned-and-immediately-dead child process.

**`runRestart`-as-sentinel for paths that shouldn't reach it.** Fetch-failure and verify-failure return errors from `doUpdate` BEFORE AtomicReplace, so `runRestart` is structurally unreachable. The tests register a `t.Fatalf`-on-call closure (mirroring `TestUpdate_Success`'s shape at `cmd/pyry/update_test.go:113-116`) — a regression that DID reach `runRestart` on these paths fails loud at the wrong-path-reached moment, instead of producing a misleading "daemon 1 dead" failure several assertions later.

**Fetch failure uses a parallel constructor, not a knob on `newFakeReleaseServer`.** Growing a failure-injection knob for a single caller would contaminate the four happy-path callers in `update_test.go`. `newFetchFailReleaseServer(t, version)` is ~20 lines: serves `/repos/.../releases/latest` (200 with `tag_name`) but returns HTTP 500 on the asset download URL. Verify failure passes a deliberately-wrong checksums body directly to the existing `newFakeReleaseServer` — no new helper needed.

**Shared pre-update setup helper.** `installPreUpdateDaemonE2E(t) *preUpdateState` bundles the install + spawn + waitForSocket + capture-inode-PID block. Returns a struct (`targetPath`, `home`, `socket`, `inodeBefore`, `pidBefore`, `cmd1`, `done1`, `stdout1`, `stderr1`). Deliberately does NOT register `cmd1` cleanup — the broken-binary test's `runRestart` closure stops cmd1 mid-test, and registering cleanup inside the helper would force a `cmd1Stopped` flag visible to the closure, coupling the helper to test-local control flow. Fetch-/verify-failure tests register `stopDaemonE2E(s.cmd1, ...)` in `t.Cleanup` themselves; broken-binary gates on the closure-local `cmd1Stopped`. `TestUpdate_HappyPath_E2E` does NOT switch to this helper — append-only.

**Shared assertion helpers.** `assertBinaryUnchangedE2E` (inode comparison), `assertDaemonAliveE2E` (PID-unchanged + `Signal(0)` localizer + `pyry status` round-trip), `assertNoStragglersE2E` (single-`pyry`-entry assertion on `<home>/bin/`), `assertNoSuccessLineE2E` (substring-not-present on `==> Updated to <v>.`). The stragglers check is skipped on the verify-failure path (structurally identical to fetch-failure — exits before AtomicReplace) and the broken-binary path (AtomicReplace succeeded; the inode + on-disk-bytes assertions already pin post-replace state).

**`internal/brokenpyry/main.go`** is the broken-pyry stand-in: 19 LOC `package main` that writes `BROKEN_PYRY_TOKEN: broken pyry stand-in exiting non-zero` to stderr and `os.Exit(1)` on every invocation. No flag parsing, no signal handling, no `-pyry-socket=` consumer — the binary exits before any of that would run. Built on demand via `buildBrokenPyryBinE2E(t)` which mirrors `buildPyryBinE2E`'s shape (`PYRY_E2E_BROKEN_BIN` short-circuit + `go build` fallback). Location is `internal/brokenpyry/` (not `cmd/pyry/internal/brokenpyry/`): a `package main` directory cannot be imported by other Go code, so the visibility tightening that `cmd/pyry/internal/` would provide doesn't apply (the path is just a build-target string for `go build`); flat `internal/<X>/` matches `internal/e2e/`, `internal/install/`, `internal/update/` repo shape.

**Diagnostic stderr-token assertion.** The broken-binary test asserts `stderr2` contains `BROKEN_PYRY_TOKEN`. Without it, a future regression where the spawn target gets wired to a different broken binary (e.g. `/bin/false`) would still produce a `daemon exited before ready` → `daemon restart failed` chain that passes the structural error-contract assertion — but the test wouldn't actually be exercising the broken-pyry path it claims to. The token assertion localizes "broken helper ran" from "some other binary exited early" the moment the regression lands.

**No `--release-url` flag, no rollback work.** Per ticket #261's AC body and lessons.md: a `--release-url` CLI flag (or `PYRY_RELEASE_BASE_URL` env var) is forbidden — `doUpdate` is driven directly from the same-package test, same seam #260 established. Rollback / `.bak` behaviour stays out of scope; if a future ticket introduces it, that ticket owns the broken-binary test's revised assertions.

### E2E signature fail-closed (#776)

`TestUpdate_MissingSignature_E2E` mirrors `TestUpdate_FetchFailure_E2E`'s structure with a server that serves tarball + checksums but 404s the `.sig`. Asserts `doUpdate` errors before `AtomicReplace` (inode unchanged), the pre-update daemon is still answering, and the success line is absent — the integration-level proof of the AC-2 downgrade defense. Bad-signature is covered at the `VerifySignature` + `doUpdate` unit level; one e2e for the fail-closed/downgrade case is sufficient. The tagged suite reuses the same `newFakeReleaseServer` (auto-signing) so `HappyPath`/`VerifyFailure`/`BrokenNewBinary` keep working once their `updateOptions` literals set `signingPubKey: testSigningPub`.

**Pre-existing suite bug uncovered.** The `//go:build e2e_update` suite was un-runnable before #776: `spawnDaemonE2E` never set `cmd.Dir`, so the spawned daemon inherited the test process's cwd (the package dir) and the workdir-confinement guard aborted startup for the whole suite. It went unnoticed because **CI does not run the `e2e_update` tag**. Fixed with a one-line `cmd.Dir = home` in the shared helper (required for the new e2e daemon to start at all). See [`codebase/776.md`](../codebase/776.md) § Lessons learned.

## Files

- `cmd/pyry/update.go` (~285 LOC) — `runUpdate`, `resolveExecutable`, `updateOptions`, `defaultProbeRestart`, `defaultRunRestart`, `doUpdate`, plus the `releaseSigningPublicKeyHex` baked-in constant (#776) and the non-pinned-downgrade refusal guard (#1498).
- `cmd/pyry/update_test.go` (~730 LOC) — integration tests + httptest fixtures + the auto-signing server + signature tests (#776) + the downgrade-refusal tests and `newDowngradeReleaseServer` (#1498).
- `cmd/pyry/main.go` — `case "update":` dispatch + `printHelp` entry.
- `internal/update/signature.go` (#776) — `VerifySignature` + `ErrInvalidSignature` / `ErrInvalidPublicKey`; see [`update-package.md`](update-package.md).
- `internal/update/restart.go` — `RestartProbe` + `DetectRestartCommand` (#181, consumed unchanged).
- `.goreleaser.yaml` — `signs:` block that signs `checksums.txt` → `checksums.txt.sig` (#776).
- `.github/workflows/release.yml` — materializes `PYRY_RELEASE_SIGNING_KEY` into `PYRY_SIGNING_KEY_FILE` for the goreleaser step (#776).
- `docs/release-tooling.md` — keypair generation, custody, matching, and the first-release smoke test (#776).
- `internal/brokenpyry/main.go` (#261, ~19 LOC) — deliberately-broken pyry stand-in for `TestUpdate_BrokenNewBinary_E2E`; writes a recognizable stderr token and exits non-zero.
- `cmd/pyry/update_e2e_test.go` (#260 + #261, ~750 LOC) — happy-path + three failure-path e2e tests, build tag `(darwin || linux) && e2e_update`.
- `docs/guide.md` — "Updating pyry" section.

## Related

- [`update-package.md`](update-package.md) — the `internal/update` primitives this command composes.
- [ADR 028](../decisions/028-ed25519-checksums-signature.md) — the raw-Ed25519 checksums-signature scheme the gate implements (#776).
- [`codebase/776.md`](../codebase/776.md) — the per-ticket implementation summary + lessons for the signature gate.
- [`codebase/1498.md`](../codebase/1498.md) — the per-ticket implementation summary + lessons for the non-pinned-downgrade refusal guard.
- [`docs/release-tooling.md`](../../release-tooling.md) — the operator procedure for the signing keypair.
- [ADR 015](../decisions/015-update-restart-probe-inline.md) — daemon-restart probe placement and executor seam.
- [`docs/specs/architecture/189-update-subcommand-wiring.md`](../../specs/architecture/189-update-subcommand-wiring.md) — #189 build-time spec.
- [`docs/specs/architecture/190-update-daemon-restart-wiring.md`](../../specs/architecture/190-update-daemon-restart-wiring.md) — #190 build-time spec.
- `cmd/pyry/main.go` — `var Version = "dev"` is the input that triggers the dev-build skip branch.
- `.goreleaser.yaml` — the build matrix `AssetName` mirrors and the source of the published tarballs the command consumes.
