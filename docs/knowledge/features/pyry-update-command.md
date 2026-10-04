# `pyry update` — user-facing self-update command

The CLI wires the `internal/update` primitives into a self-update flow with daemon restart. `--when-idle` instead requests an update from a selected running daemon, which shares its verified installation path with automatic scheduling. See [update-package.md](update-package.md) for the building-block primitives this command composes.

## Sections

| Document | Topics |
| --- | --- |
| [Automatic update and explicit idle requests](pyry-update-command-automatic-update.md) | Eligibility, idle polling, pending attempts and scheduling. |
| [Installation architecture and error contract](pyry-update-command-architecture.md) | Shared signed installation, restart wiring and failure behavior. |
| [Update tests](pyry-update-command-tests.md) | Signed fixtures, Unix control requests and update e2e tests. |

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
| `--when-idle` | Ask the selected running daemon to decide on latest and install at its next idle moment. The daemon selects, installs and restarts; the requesting CLI performs no local update. |

With enabled `--when-idle`, supplying `--check`, `--version` or `--no-restart` is an error before contacting the daemon, including `--check=false`, `--version=` and `--no-restart=false`. `--when-idle=false` retains ordinary update behavior.

Daemon selection follows `parseClientFlags`: put `--pyry-name` and `--pyry-socket` before the update flags, for example `pyry update --pyry-name elli --when-idle`. A name selects `~/.pyry/<name>.sock`; an explicit `--pyry-socket <path>` wins over the name. Without either, `PYRY_NAME` or the default instance name selects the socket.

The request prints exactly one decision and exits 0:

| Output | Meaning |
| --- | --- |
| `up to date` | Latest matches the daemon's running version. |
| `not eligible: <reason>` | Host or release policy refuses the update. |
| `will install <tag> when idle` | The daemon accepted the tag, or already has that tag pending or installed. This is acceptance, not an installation-completion report. |

The CLI waits for the bounded metadata/eligibility decision: production HTTP has a 60-second budget and the control exchange has a 70-second ceiling. “Returns at once” means after that decision; it never waits for idle, asset download, installation or restart. Transport failures, an unsupported verb on an older daemon, and metadata fetch/parse failures exit nonzero with no local-update fallback. Provider failures expose only `update.when-idle: operation failed`; details stay in daemon logs. Accepted work can continue after a failed exchange or disconnect. See the [provider contract](control-plane.md#update-when-idle-provider).

Ordinary installation errors print as `pyry: update: <step>: <inner>` to stderr (via `main()`'s standard wrapper) and exit non-zero. Each step in the flow contributes its own context prefix: `update: fetch latest release: …`, `update: download tarball: …`, `update: download signature: …`, `update: verify signature: …`, `update: verify checksum: …`, `update: replace binary: …`.

## Homebrew hint

For ordinary local updates, if `os.Executable()` resolves under `/opt/homebrew/`, the command prints a one-line hint before the rest of the flow runs:

```
Hint: this pyry was installed via Homebrew; consider 'brew upgrade pyry' instead.
```

Non-blocking — the update still proceeds. The hint exists because a Homebrew-installed pyry will hit `AtomicReplace` and successfully overwrite the binary in `/opt/homebrew/bin/pyry`, but the user's *next* `brew upgrade` will revert it. Refusing would block the Homebrew user who knowingly wants the GitHub-release version.

## Automatic update (#2716)

See [Automatic update and explicit idle requests](pyry-update-command-automatic-update.md#automatic-update-2716).

## Architecture

See [Installation architecture and error contract](pyry-update-command-architecture.md#architecture).

## Concurrency

Ordinary CLI installation is sequential in the calling goroutine, with one `context.Context` (`context.Background()` from `runUpdate`; tests pass `t.Context()`). `runUpdateArgs` sends `--when-idle` requests through the bounded control helper instead.

`autoUpdater.begin` serializes explicit and scheduled requests under a mutex and starts one daemon-context worker per active attempt. Decision and completion channels publish immutable results separately; request handlers await the decision, while `check` awaits completion. No mutex is held over network I/O or waits. Clear a refused or failed metadata attempt before closing its decision channel: waking callers first lets an immediate retry inherit the failed check. Eligible decisions publish before installation, so acceptance does not wait for the worker to finish.

A `select` can choose a ready timer even when cancellation is also ready, so `waitFor` checks context both before and after waking. `waitUntilIdle` also checks before polling and after an idle answer, preventing already-visible cancellation from permitting installation or restart. On shutdown, `runSupervisor` cancels the daemon context, drains control handlers through `ctrlDone`, joins the scheduler, then calls `autoUpdater.join`. Stopping the attempt producers before joining their workers avoids a late request racing shutdown. The request path still reads a published decision after restart cancellation; [control-handler draining](control-plane.md#update-when-idle-provider) preserves its response write.

The asset HTTP fetches (tarball, checksums and signature) run sequentially. Keeping that order avoids coordinating parallel failures and progress output.

If a future ticket adds Ctrl-C handling, swap `context.Background()` for `signal.NotifyContext(ctx, os.Interrupt)`. The Fetcher already honours context cancellation per #182 AC.

## Out of scope (handled in follow-up tickets or deferred)

- **Renamed daemons** (`pyry install-service --name <other>`). The probe is hardcoded to `dev.pyrycode.pyry.plist` / `pyry.service`; renamed installs are silently skipped on update (treated as "no managed unit"). Follow-up if observed.
- **Rollback beyond `pyry.prev`.** Since #2716, `pyry update` and the daemon's auto-updater both keep the replaced binary as `pyry.prev` beside the target before swapping in the new one — the location `make rollback` already restores from — so either kind of install can be rolled back by hand. Nothing restores it automatically, only one previous binary is ever kept, and a repeat install of the same release skips the copy rather than overwriting it with the new build. Automatic rollback, keeping more than one prior binary, and garbage-collecting old ones remain a separate, undesigned feature.
- **Release channel selection.** Deferred. Only the `latest` GitHub release is consulted; drafts and pre-releases are refused both by GitHub's endpoint and, since #2716, by `update.Eligible`.
- **Ctrl-C / SIGINT handling.** `context.Background()` today; swap to `signal.NotifyContext` if the need arises.
- **Retry within a fetch.** `Fetcher` does not retry transient failures. Operators can re-run ordinary updates or explicitly request a fresh daemon attempt after metadata/install failure; automatic scheduling retries unsuccessful checks four-hourly only when enabled.

## Error contract

See [Installation architecture and error contract](pyry-update-command-architecture.md#error-contract).

## Tests

See [Update tests](pyry-update-command-tests.md#tests).

## Files

- `cmd/pyry/update.go` (~285 LOC) — `runUpdate`, `resolveExecutable`, `updateOptions`, `defaultProbeRestart`, `defaultRunRestart`, `doUpdate`, plus the `releaseSigningPublicKeyHex` baked-in constant (#776) and the non-pinned-downgrade refusal guard (#1498). Since #2716: `productionUpdateOptions` (the shared seam builder), `installRelease` (the download/verify/replace tail `doUpdate` now calls), and `keepPrevious` (writes `pyry.prev`).
- `cmd/pyry/update_test.go` (~730 LOC) — integration tests + httptest fixtures + the auto-signing server + signature tests (#776) + the downgrade-refusal tests and `newDowngradeReleaseServer` (#1498). `TestUpdate_Success` also asserts `pyry.prev` holds the pre-update bytes (#2716).
- `cmd/pyry/auto_update.go` — `daemonIdle`, `autoUpdater`, `begin`, `request`, `Run`, `check`, `restart`, `join`, `newAutoUpdater`: shared explicit/scheduled attempts and daemon-owned installation.
- `cmd/pyry/update_when_idle_test.go` — real Unix-socket request, shared-attempt, retry and shutdown-response tests using signed fixtures.
- `cmd/pyry/auto_update_test.go` (#2716, ~370 LOC) — table test for `daemonIdle`; check-level tests against a signed httptest release covering every outcome, including that a bad signature leaves `pyry.prev` untouched and that `installed` logs before `runRestart` is called.
- `cmd/pyry/main.go` — `runSupervisor` installs the updater provider before serving control requests; `-pyry-auto-update` starts scheduling, and shutdown drains control handlers and joins updater work. `parseClientFlags` selects the daemon for explicit requests.
- `cmd/pyry/stream_turn_busy.go` — since #2716, `(*turnBusyTracker).AnyBusy()`, a bool-only "does any conversation have a turn open" query alongside `Busy`.
- `internal/update/version.go` — since #2716, also `Release`, `ParseRelease`, `Eligible`, `ErrUpToDate`, `ErrNotEligible`, alongside the existing `ParseLatestRelease`/`CompareVersions` (unchanged).
- `internal/update/version_test.go` (#2716) — table tests for `Eligible` and `ParseRelease`.
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
- [`docs/specs/architecture/2716-daemon-auto-update.md`](../../specs/architecture/2716-daemon-auto-update.md) — #2716 build-time spec and security review for the auto-updater.
- [ADR 040](../decisions/040-auto-update-eligible-release-and-idle-are-independent.md) — why eligibility and idleness are kept as two independent, pure predicates.
- `cmd/pyry/main.go` — `var Version = "dev"` is the input that triggers the dev-build skip branch.
- `.goreleaser.yaml` — the build matrix `AssetName` mirrors and the source of the published tarballs the command consumes.
