# #2716 — an idle daemon installs a new release by itself, off by default

## Files read

- `cmd/pyry/update.go` → `doUpdate`, `updateOptions`, `defaultProbeRestart`, `defaultRunRestart`, `resolveExecutable`: the fetch/decide/install pass this ticket splits, and the seams the daemon reuses.
- `cmd/pyry/update_test.go` → `fakeRelease`, `newFakeReleaseServer`, `testSigningPub`, `TestUpdate_BadSignature`: the signed fake-release fixtures the new tests reuse.
- `cmd/pyry/update_e2e_test.go` → `TestUpdate_BrokenNewBinary_E2E`, `assertNoStragglersE2E`: build-tagged `e2e_update`. Stragglers is asserted only on fetch-failure paths, where no `pyry.prev` is written, so it stays true. The broken-binary doc comment says "NO rollback" and becomes stale; correct that comment.
- `internal/update/version.go` → `ParseLatestRelease`, `CompareVersions`, `parseSemver`: reads only `tag_name`; strips `-rc1` before comparing, so a suffixed tag must be refused explicitly.
- `internal/update/replace.go` → `AtomicReplace`: temp + fsync + rename in the target's directory; keeps nothing.
- `internal/update/restart.go` → `DetectRestartCommand`, `RestartProbe`: nil when no managed unit.
- `internal/update/fetch.go` → `Fetcher`: 512 MiB body cap, ctx-cancellable.
- `cmd/pyry/stream_turn_busy.go` → `turnBusyTracker`, `Busy`: membership set of conversations mid-turn; its doc forbids exposing conversation ids.
- `cmd/pyry/codex_runner.go` → `codexHarness`: Codex sessions feed "the one turn-event fan-in Claude sessions also feed" (`sinkForTag`, `exitForTag`), and the tracker observes that fan-in, so a Codex turn marks the tracker exactly as a Claude turn does.
- `cmd/pyry/main.go` → `runSupervisor`: flag set, `turnBusy` construction, `pool`, the `qDone`/`ctrlDone` join pattern at shutdown.
- `internal/sessions/pool.go` → `Pool.List`, `SessionInfo.LastActiveAt`: read-only snapshot of every session's last activity.
- `scripts/install-dev.sh` → `PREV="$INSTALL_DIR/pyry.prev"`, `cmd_rollback`: the name and place `make rollback` restores from.
- `docs/knowledge/features/pyry-update-command.md` § "Daemon-restart wiring (#190)", § "Tests": success line is terminal; same-package tests drive `doUpdate` directly, no release-URL flag or env var.

No other feature branch touches these files.

## Context

`pyry update` only runs when someone logs in, so daemons reached only to update fall behind. This adds an opt-in daemon loop that checks the latest release on a schedule and installs it through the same verified path once the daemon is idle, then restarts the managed unit. A decision record is worth writing for "auto-update follows only operator-made latest releases and waits for idle" — the documentation stage owns it.

## Design

### `internal/update/version.go` (pure)

- `type Release struct { Tag string; Draft, Prerelease bool }`
- `ParseRelease(body []byte) (Release, error)` — decodes `tag_name`, `draft`, `prerelease`; same `ErrMalformedRelease` contract as `ParseLatestRelease` (which stays, unchanged).
- `ErrUpToDate`, `ErrNotEligible` sentinels.
- `Eligible(current string, rel Release) error` — nil means installable. In order: `current` not parseable (covers `dev`) → `ErrNotEligible`; `rel.Draft` → `ErrNotEligible`; `rel.Prerelease` → `ErrNotEligible`; tag carries a `-` or `+` suffix after the `v` → `ErrNotEligible`; tag not exactly `v?<digits>.<digits>.<digits>` → `ErrNotEligible` (strict: `strconv.Atoi` alone would accept `+1`, and the tag is later joined into download URLs); current == tag → `ErrUpToDate`; current newer than tag → `ErrNotEligible` (downgrade). Each wraps the sentinel with a short reason (`"draft release"`, `"downgrade"` …) that becomes the log line's `reason`.

### `cmd/pyry/update.go` (split)

`doUpdate` keeps its signature, output and behaviour. Its tail — asset name, download tarball/checksums/signature, verify signature, parse checksums, verify SHA-256, extract, replace — moves into `installRelease(ctx, o updateOptions, target, tag string) error`, which `doUpdate` calls with no change to the lines it prints. One addition inside `installRelease`, after extraction and immediately before replacing the target: `keepPrevious(o, target)` copies the current target's bytes to `filepath.Join(filepath.Dir(target), "pyry.prev")` through `o.replace` with the target's permission bits. When the target's bytes already equal the extracted binary, both the copy and the replace are skipped, so a second install of the same release (two daemons sharing one binary) cannot overwrite `pyry.prev` with the new build. A target that does not exist skips the copy (AtomicReplace's own contract allows a fresh target); any other read/write error aborts the install before the target is touched. Because it runs after both verifications, a signature or checksum failure leaves the binary and any existing `pyry.prev` untouched. `pyry update` gains `pyry.prev` as the ticket wants; no new output line.

### `cmd/pyry/auto_update.go` (new)

- `daemonIdle(anyTurnOpen bool, lastActive []time.Time, now time.Time, quiet time.Duration) bool` — pure: false when a turn is open or any `lastActive` is after `now - quiet`.
- `type autoUpdater struct` holding an `updateOptions` (out = `io.Discard`, `checkOnly`/`pinVersion`/`noRestart` unused), `idle func() bool`, `logger`, `startDelay`, `interval`.
- `(*autoUpdater).Run(ctx)` — waits `startDelay`, runs `check`, then every `interval`; returns on ctx done, or after a check that installed (the process is being restarted; a second install would overwrite `pyry.prev` with the new binary and lose the rollback copy).
- `(*autoUpdater).check(ctx) (installed bool)` — ends in exactly one `slog` line `"auto-update check"` with `outcome` ∈ `up_to_date | waiting_for_idle | installed | skipped | failed` plus `reason`/`err`, `current`, `latest` where known. Order: target under `/opt/homebrew/` → skipped `homebrew`; `DetectRestartCommand(probeRestart())` nil → skipped `no managed unit` (both before any request); fetch + `ParseRelease` error → failed; `Eligible` → `ErrUpToDate` up_to_date / `ErrNotEligible` skipped with the reason; `idle()` false → waiting_for_idle (nothing downloaded); `installRelease` error → failed; else log installed, THEN restart: `idle()` is asked again first, and while it is false the restart waits, re-asking every minute (ctx-aware, no log line), so a turn that opened during the download is not cut off; then `runRestart(ctx, argv)`. A restart error is logged as a separate Error line `"auto-update restart failed"` — the check's own outcome line has already been written, as the ticket requires before the restart is issued.
- Defaults: `autoUpdateStartDelay = 2m`, `autoUpdateInterval = 4h`, `autoUpdateQuietWindow = 15m`.

### `cmd/pyry/stream_turn_busy.go`

`(*turnBusyTracker).AnyBusy() bool` — `len(t.busy) > 0` under `mu`. Returns a bool only, no ids or count, preserving the existence-oracle posture.

### `cmd/pyry/main.go`

Flag `-pyry-auto-update` (bool, default false). No config key: one switch is enough, and the flag lives in the launchd/systemd unit the operator already edits. When set, build the updater with the same production options `runUpdate` uses (factored as `productionUpdateOptions()` in `update.go` so the two cannot drift) and `idle` = `daemonIdle(turnBusy == nil || turnBusy.AnyBusy(), lastActives(pool.List()), time.Now(), autoUpdateQuietWindow)`. A nil tracker fails closed (never idle). Start `go func(){ auDone <- au.Run(ctx) }()` after the pool is built and join `<-auDone` beside `<-qDone`. When unset, nothing is constructed and no request is made.

## Concurrency model

One goroutine, `autoUpdater.Run`, started only with the flag. It exits on ctx cancellation (timer select) or after an install; the shutdown path joins it. Every network read uses ctx and the fetcher's 60 s client timeout, so shutdown never waits longer than one in-flight request's cancellation. `idle` reads `turnBusy.AnyBusy` (tracker mutex) and `pool.List` (pool RLock → session lcMu) sequentially, no lock held across the two; no new lock-order edge. The idle decision is a snapshot; a turn opening between the check and the restart is accepted as the residual window (seconds of download/verify).

Restart inside the unit: `runRestart` gets the daemon ctx. The service manager's SIGTERM is what cancels that ctx, and it arrives only after the manager accepted the restart request, so CommandContext killing `launchctl`/`systemctl` then cannot cancel the restart, and the goroutine is not left blocking shutdown.

## Error handling

- Fetch/parse/download/verify/extract/keep-previous/replace errors → one `failed` line with the wrapped error; next scheduled check retries.
- Signature or checksum failure happens before `keepPrevious` and before `replace` — both files untouched.
- Restart failure after a successful swap → Error line; the loop has already stopped, so the next daemon start runs the new binary and finds itself up to date.

## Testing strategy

- `internal/update/version_test.go`: table test for `Eligible` — newer installs; equal → `ErrUpToDate`; older, `dev`, draft, prerelease, `v0.10.0-rc1`, `+meta`, unparseable tag → `ErrNotEligible`. Table test for `ParseRelease` fields and malformed input.
- `cmd/pyry/auto_update_test.go`: table test for `daemonIdle` (turn open; activity inside window; at boundary; outside window; no sessions). Check-level tests against the signed httptest release with a slog handler capturing records: up to date; newer + not idle (no asset request, target unchanged); newer + idle installs, writes `pyry.prev` with the old bytes, logs installed before `runRestart` is called (recorded order); bad signature installs nothing and leaves an existing `pyry.prev` untouched; no managed unit and Homebrew skip with no request; draft release skipped. Each asserts exactly one `auto-update check` record. `Run` exits on ctx cancel.
- `cmd/pyry/update_test.go`: `TestUpdate_Success` additionally asserts `pyry.prev` holds the old bytes.
- `cmd/pyry/stream_turn_busy_test.go`: `AnyBusy` false empty, true after a mark, false after close.
- Compile check `go vet -tags e2e_update ./cmd/pyry/`.

## Open questions

- Whether `pyry update`'s output should mention `pyry.prev`. Decided no: the ticket asks it to keep its current output.

## Documentation handoff

Pending for the documentation stage — `docs/knowledge/features/pyry-update-command.md`, new section "Automatic update": the switch `-pyry-auto-update` and that it is off by default (no config key); the 4 h check interval and 2 min startup delay; the idle rule (no open turn, Claude or Codex, and no session `LastActiveAt` in the last 15 min; a connected phone is not an input); drafts, pre-releases, `-rc`/`+` tags, downgrades and `dev` builds are never installed; nothing is installed without a managed unit or under `/opt/homebrew/`; the previous binary is kept as `pyry.prev` beside the binary by both auto-update and `pyry update`, so `make rollback` restores either; the per-check log line `auto-update check` with `outcome` `up_to_date | waiting_for_idle | installed | skipped | failed`, plus the separate `auto-update restart failed` line; the loop stops after an install. The § "Out of scope" note "Rollback / `.bak` behaviour stays out of scope" needs revising for `pyry.prev`.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The one new untrusted input is the latest-release JSON, decoded only in `ParseRelease` and judged only in `Eligible`; nothing downstream reads the body. SHOULD FIX (folded into Design): `Eligible` requires the tag to be exactly `v?<digits>.<digits>.<digits>` before `installRelease` joins it into download URLs, since `parseSemver`'s `strconv.Atoi` accepts a sign and the suffix strip hides `-rc1`. Bytes are trusted only after `update.VerifySignature` against the baked-in key and `update.VerifySHA256`, both in `installRelease`, both before `keepPrevious` and the replace.
- [Tokens] No findings. No credential is used: the GitHub API is read anonymously and the signing key is public. One request per 4 h stays far under the anonymous rate limit.
- [File operations] `pyry.prev` is a fixed name in the directory of `os.Executable`'s answer, never caller input. It is written through `AtomicReplace`, whose rename replaces a planted symlink rather than following it, at the target's own permission bits. Writing it before the target means a signal between the two leaves old/old, never a lost rollback copy. SHOULD FIX (folded into Design): skip both writes when the target already holds the new bytes, so a repeated install cannot overwrite `pyry.prev` with the new build.
- [Subprocesses] No findings. The restart argv comes only from `DetectRestartCommand` (fixed strings plus `os.Getuid`), exec'd without a shell through the existing `defaultRunRestart`.
- [Cryptography] No findings. Reuses `update.VerifySignature` (Ed25519) and `update.VerifySHA256`; nothing new. The signature does not bind the version string, but `AssetName` embeds the version and `ParseChecksumsFile` looks the asset up by that name, so an old signed checksums file cannot vouch for a new tag's asset — a property of `pyry update` today, unchanged.
- [Network and I/O] No new reader. `Fetcher` caps every body at 512 MiB and the client has a 60 s timeout; all requests carry the daemon ctx. The 512 MiB ceiling now applies inside a long-lived daemon rather than a one-shot CLI — OUT OF SCOPE: tightening the cap for the release JSON is not needed while the host is TLS-authenticated GitHub; no ticket.
- [Errors, logs] The check line logs the tag, the outcome, a fixed reason and the wrapped error (which carries public release URLs). SHOULD FIX: the tag comes from the network and can be as long as the body cap, so the logged `latest` is truncated to 64 bytes. No body, header or path beyond the executable's is logged.
- [Concurrency] One goroutine, exiting on ctx or after an install, joined at shutdown. The idle answer is a snapshot; the residual window of a turn opening during download is closed by re-asking `idle()` before the restart. A restart cannot deadlock shutdown: the manager's SIGTERM, which cancels the ctx given to `runRestart`, follows its acceptance of the request.
- [Threat model] Auto-update widens the effect of a stolen signing key from "operators who run `pyry update`" to "every opt-in daemon within 4 h". The switch is off by default, drafts and pre-releases are refused twice (endpoint and `Eligible`), and downgrades are refused, so a stolen publishing token without the key installs nothing. OUT OF SCOPE: key rotation and revocation, owned by the release tooling (docs/release-tooling.md), unchanged here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03
