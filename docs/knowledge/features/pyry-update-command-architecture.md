# `pyry update` — Installation architecture and error contract

Part of the [`pyry update` overview](pyry-update-command.md).

## Architecture

`runUpdate(args []string) error` delegates to writer-aware `runUpdateArgs`, which parses daemon-selection flags through `parseClientFlags` before update flags. `--when-idle` calls the selected daemon through `control.UpdateWhenIdle`; ordinary update builds `productionUpdateOptions` and calls `doUpdate(ctx, updateOptions) error`. The daemon also builds those production options, then calls `installRelease` after its own eligibility and idle checks. Tests substitute signed `httptest` releases and temporary targets through the same `updateOptions` seams. See [Flags](pyry-update-command.md#flags) and [Automatic update](pyry-update-command-automatic-update.md).

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

Since #2716, the download/verify/replace tail — steps 7–13 below — lives in `installRelease(ctx, o updateOptions, target, tag string) error`, called by both `doUpdate` and the daemon's auto-updater (see Automatic update above). `productionUpdateOptions(out io.Writer) (updateOptions, error)` builds the real seam values (signing key, fetcher, replace, probe/run-restart) that both callers populate from, so the CLI and the daemon cannot drift apart; `doUpdate`'s own output and behaviour are unchanged.

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
13. **Keep the previous binary (#2716).** If `target` exists and its bytes differ from the extracted binary, `keepPrevious` copies the current bytes to `pyry.prev` beside `target` — the same name and place `make rollback` reads — at `target`'s own permission bits, through `o.replace`. When the bytes already match (a repeat install of the same release, e.g. two daemons sharing one binary), both this step and the replace are skipped, so `pyry.prev` is never overwritten with the new build. A target that does not yet exist skips the copy. Any other read/write error aborts before `target` is touched. `update.AtomicReplace(target, bin, 0o755)` then swaps the on-disk binary.
14. **Daemon restart (#190).** Unless `--no-restart` is set, call `o.probeRestart()` to stat the canonical launchd plist (`~/Library/LaunchAgents/dev.pyrycode.pyry.plist`) and systemd user-unit (`~/.config/systemd/user/pyry.service`) paths. Pass the resulting `RestartProbe` to `update.DetectRestartCommand`. If non-nil argv is returned, print `==> Restarting daemon (<manager>: <last-argv-element>)...` and call `o.runRestart(ctx, argv)`. If the probe returns no managed unit (both stats fail), the step is silently skipped.
15. Print `==> Updated to <v>.` — last in the happy path so it terminates the output.

**Why the guard sits below `--check` (#1498).** Placing it above (e.g. as a fourth compare-switch arm) would make `pyry update --check` exit non-zero against a rolled-back latest, breaking the documented exit-0 contract that `docs/release-tooling.md`'s release checklist depends on. Placing it below `AssetName` would let a refused downgrade still issue fetches. The guard's position is an observable ordering constraint, pinned by `TestUpdate_CheckOnlyOlderLatest` (fails if the guard drifts above the `checkOnly` return) and `TestUpdate_UnpinnedDowngradeRefused`'s request-recording assertion (fails if the guard drifts below `AssetName`).

### Signature gate (#776)

The trust root is a package-level constant in `cmd/pyry/update.go`:

```go
const releaseSigningPublicKeyHex = "<64 hex chars>" // raw 32-byte Ed25519 public key
```

`productionUpdateOptions` decodes it (`hex.DecodeString` + a 32-byte length check → `update: signing key: …` on failure) and supplies `updateOptions.signingPubKey` to both local and daemon installation. Its doc comment records the `openssl genpkey` / `openssl pkey … | tail -c 32 | xxd -p` generation recipe, notes that the private half lives only as the `PYRY_RELEASE_SIGNING_KEY` Actions secret, and points at [`docs/release-tooling.md`](../../release-tooling.md).

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

## Error contract

CLI errors propagate up through `main()`'s wrapper, which prints `pyry: <err>` to stderr and exits 1.

The following table describes ordinary local installation. For `--when-idle`, the daemon's three decisions exit 0; control/metadata failures exit nonzero with no local fallback. Later install or restart failures appear in daemon logs after the CLI has received acceptance. See [Flags](pyry-update-command.md#flags).

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

Each `AtomicReplace` is all-or-nothing and removes its temporary file on its own error paths. `installRelease` also writes `pyry.prev` through `keepPrevious` before replacing the target. A failed restart leaves the new binary on disk by design; ordinary update reports both facts in one error line, while daemon updates log the restart error and retain installed state.
