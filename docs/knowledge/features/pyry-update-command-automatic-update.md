# `pyry update` — Automatic update and explicit idle requests

Part of the [`pyry update` overview](pyry-update-command.md).

## Automatic update (#2716)

The daemon installs an eligible release once idle, either on an explicit `pyry update --when-idle` request or through opt-in `-pyry-auto-update` scheduling. `runSupervisor` constructs the updater and registers `SetUpdateWhenIdleProvider` before serving control requests regardless of that flag. Scheduling is off by default: without the flag, the daemon makes no unsolicited release checks or scheduled retries, but explicit requests work. There is no `config.json` key; the scheduling flag lives in the launchd/systemd unit file the operator already edits. See [Flags](pyry-update-command.md#flags) for request output, conflicts and daemon selection.

With the flag set, the daemon checks the latest release 2 minutes after startup (`autoUpdateStartDelay`). An up-to-date, ineligible, host-refusal, metadata-failure or install-failure outcome schedules the next check 4 hours (`autoUpdateInterval`) after that check completes.

Explicit requests and scheduled checks share one active attempt. Concurrent callers during metadata fetch/parse receive that check's decision, including its failure. Once accepted, requests during the idle wait, download or restart wait return the pending tag without another check or install; an explicit request also joins a scheduled idle wait. Metadata or installation failure clears the attempt, allowing a fresh explicit request immediately. Four-hour scheduled retries occur only with scheduling enabled. A successful installation retains its reportable tag and prevents another install in that daemon even if restart fails; the scheduler returns when that installed attempt finishes, including when an explicit request completed it while the scheduler was sleeping. This protects `pyry.prev` from another install.

**Eligibility** (`update.Eligible`, pure, table-tested in `internal/update/version_test.go`) refuses a release unless all hold: the running build is a release, not `dev`; the release is not a draft and not marked pre-release; its tag is exactly `v?<digits>.<digits>.<digits>` — a `-rc1` or `+meta` suffix is refused explicitly, since `update.CompareVersions` would otherwise strip it before comparing; and it is strictly newer than the running version. An equal version returns `ErrUpToDate`; anything else ineligible returns `ErrNotEligible` wrapping a short reason, which becomes the log line's `reason` and the explicit request's `not eligible: <reason>` output.

**Idleness** (`daemonIdle`, pure, table-tested in `cmd/pyry/auto_update_test.go`) requires both: no conversation has a turn open — `turnBusyTracker.AnyBusy()`, fed by Claude and Codex turns alike, since both share the one turn-event fan-in the tracker observes — and no session's `LastActiveAt` falls within the last 15 minutes (`autoUpdateQuietWindow`). A connected phone is deliberately not an input: a phone left connected overnight would otherwise hold every update off indefinitely. A nil turn tracker fails closed (counts as a turn being open), never defaulting to idle.

An eligible tag found while busy stays selected within the active attempt. `waitUntilIdle` polls the idle predicate once a minute (`autoUpdateRestartPoll`); at the first idle poll, the worker downloads and installs that selected release. Waiting makes no asset downloads or repeated latest-release requests, even across four-hour boundaries; a later change to latest does not replace the selected tag. An ineligible release is never downloaded. Before any network request, the daemon refuses a binary under `/opt/homebrew/` with `homebrew install`, or an absent managed unit from `update.DetectRestartCommand` with `no managed unit`.

**Install** goes through `installRelease`, the same function ordinary `pyry update` calls (see [Architecture](pyry-update-command-architecture.md#architecture)): the same signature check against `releaseSigningPublicKeyHex`, the same SHA-256 checksum check, and the same `update.AtomicReplace`, keeping the prior binary as `pyry.prev`. A signature or checksum failure leaves the binary and any existing `pyry.prev` untouched.

Once the swap succeeds and the "installed" log line is written, the daemon asks `idle()` again before restarting — a turn may have opened while the release downloaded — polling once a minute until it is, then hands the restart to the detected `launchctl`/`systemctl` command. `Run` stops after installation succeeds, including if restart fails, so later checks cannot overwrite the rollback copy in `pyry.prev`. Cancelling the daemon context ends an idle wait and returns from `Run` without starting the pending download, install or restart.

Accepted work uses the daemon context, so CLI disconnect or timeout does not cancel it. Daemon shutdown cancels and joins the workers even with scheduling disabled. For a connected caller within the response deadline, [control-handler draining](control-plane.md#update-when-idle-provider) preserves acceptance across an immediate update-triggered shutdown.

**Logging.** A check that waits before installation emits one `auto-update check` slog record with `waiting_for_idle` on entering the wait, then an eventual `installed` or `failed` record unless the wait is cancelled. Polls emit no records; cancellation during this wait leaves only the entry record. Other completed checks emit one outcome record. Restart waiting adds no check records. The `outcome` values are:

| Outcome | Meaning |
|---------|---------|
| `up_to_date` | The latest release is the running version. |
| `waiting_for_idle` | An eligible tag is retained while busy; emitted once on entering the pre-install wait, with no downloads while waiting. |
| `installed` | The release was downloaded, verified and swapped in; logged before the restart is issued. |
| `skipped` | The release is not eligible, or no managed unit was found, or the binary is a Homebrew install — `reason` names which. |
| `failed` | A fetch, parse, download, verification or install step errored; `err` carries the wrapped error. The tag is truncated to 64 bytes before it reaches any log line. |

A restart failure after a successful install is logged as a separate `auto-update restart failed` Error line, since the check's own `installed` outcome line has already been written. **Known log quirk:** because the restart command runs inside the unit it restarts, a restart that *succeeds* can also produce this line — the service manager's SIGTERM, sent once it has accepted the restart request, can reach and kill the restart command before it returns, so `runRestart` reports an error for a handoff that is in fact going through. Whether the daemon actually comes back at the new version is the real signal; the Error line alone does not mean the restart failed.
