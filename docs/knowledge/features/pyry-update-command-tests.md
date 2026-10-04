# `pyry update` — Update tests

Part of the [`pyry update` overview](pyry-update-command.md).

## Tests

Request tests in `cmd/pyry/update_when_idle_test.go` use real Unix control sockets and the signed `newAutoUpdateFixture` with injected idle/time and restart seams. `TestUpdateWhenIdleCLI` and `TestUpdateWhenIdleNamedDaemon` pin decision output, errors, conflicting flag presence and explicit-socket precedence. Shared-attempt tests hold metadata, idle, download or restart phases to prove requests join existing work; retry tests distinguish failed metadata/install from a successful installation whose restart failed.

When counting installs through `updateOptions.replace`, count only calls whose target is the actual binary. `keepPrevious` uses the same replacement seam to write `pyry.prev`; counting every call mistakes the rollback-copy write for a second installation. `TestUpdateWhenIdleRetryAndRestartFailure` and `TestUpdateWhenIdlePendingPhases` filter on the target path.

`TestUpdateWhenIdleDisconnectAndShutdown` proves connection-independent work still stops and is joined on daemon cancellation. `TestUpdateWhenIdleImmediateRestartResponse` holds the provider's acceptance until an immediate installation has triggered shutdown, then checks that `Serve` has not returned and the connected client receives its tag. Merely starting installation after decision publication, or sleeping before restart, would not prove response delivery. These are offline race-test proofs; no live Claude or real service-manager restart is required.

Scheduled-update tests reuse `newAutoUpdateFixture` with fake idle answers and controllable duration waits. `TestAutoUpdater_CancelIdleWait` cancels as a poll wakes, testing both a ready wake and a cancelled wait: testing only the cancellation branch would miss a timer wake permitting work after cancellation. `TestAutoUpdater_RetainsReleaseWhileBusy` crosses four hours of minute polls with changed latest metadata and proves the original tag, one wait record and zero asset requests while busy.

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

**The broken-binary case pins failure reporting, not automatic rollback.** As described in [Out of scope](pyry-update-command.md#out-of-scope-handled-in-follow-up-tickets-or-deferred), the new binary remains after restart fails. `installRelease` keeps the old bytes as `pyry.prev`, but restoring them needs operator intervention. The error contract pinned by `TestUpdate_RestartFailure` extends end-to-end here against a real spawned-and-immediately-dead child process.

**`runRestart`-as-sentinel for paths that shouldn't reach it.** Fetch-failure and verify-failure return errors from `doUpdate` BEFORE AtomicReplace, so `runRestart` is structurally unreachable. The tests register a `t.Fatalf`-on-call closure (mirroring `TestUpdate_Success`) — a regression that DID reach `runRestart` on these paths fails loud at the wrong-path-reached moment, instead of producing a misleading "daemon 1 dead" failure several assertions later.

**Fetch failure uses a parallel constructor, not a knob on `newFakeReleaseServer`.** Growing a failure-injection knob for a single caller would contaminate the four happy-path callers in `update_test.go`. `newFetchFailReleaseServer(t, version)` is ~20 lines: serves `/repos/.../releases/latest` (200 with `tag_name`) but returns HTTP 500 on the asset download URL. Verify failure passes a deliberately-wrong checksums body directly to the existing `newFakeReleaseServer` — no new helper needed.

**Shared pre-update setup helper.** `installPreUpdateDaemonE2E(t) *preUpdateState` bundles the install + spawn + waitForSocket + capture-inode-PID block. Returns a struct (`targetPath`, `home`, `socket`, `inodeBefore`, `pidBefore`, `cmd1`, `done1`, `stdout1`, `stderr1`). Deliberately does NOT register `cmd1` cleanup — the broken-binary test's `runRestart` closure stops cmd1 mid-test, and registering cleanup inside the helper would force a `cmd1Stopped` flag visible to the closure, coupling the helper to test-local control flow. Fetch-/verify-failure tests register `stopDaemonE2E(s.cmd1, ...)` in `t.Cleanup` themselves; broken-binary gates on the closure-local `cmd1Stopped`. `TestUpdate_HappyPath_E2E` does NOT switch to this helper — append-only.

**Shared assertion helpers.** `assertBinaryUnchangedE2E` (inode comparison), `assertDaemonAliveE2E` (PID-unchanged + `Signal(0)` localizer + `pyry status` round-trip), `assertNoStragglersE2E` (single-`pyry`-entry assertion on `<home>/bin/`), `assertNoSuccessLineE2E` (substring-not-present on `==> Updated to <v>.`). The stragglers check is skipped on the verify-failure path (structurally identical to fetch-failure — exits before AtomicReplace) and the broken-binary path (AtomicReplace succeeded; the inode + on-disk-bytes assertions already pin post-replace state).

**`internal/brokenpyry/main.go`** is the broken-pyry stand-in: 19 LOC `package main` that writes `BROKEN_PYRY_TOKEN: broken pyry stand-in exiting non-zero` to stderr and `os.Exit(1)` on every invocation. No flag parsing, no signal handling, no `-pyry-socket=` consumer — the binary exits before any of that would run. Built on demand via `buildBrokenPyryBinE2E(t)` which mirrors `buildPyryBinE2E`'s shape (`PYRY_E2E_BROKEN_BIN` short-circuit + `go build` fallback). Location is `internal/brokenpyry/` (not `cmd/pyry/internal/brokenpyry/`): a `package main` directory cannot be imported by other Go code, so the visibility tightening that `cmd/pyry/internal/` would provide doesn't apply (the path is just a build-target string for `go build`); flat `internal/<X>/` matches `internal/e2e/`, `internal/install/`, `internal/update/` repo shape.

**Diagnostic stderr-token assertion.** The broken-binary test asserts `stderr2` contains `BROKEN_PYRY_TOKEN`. Without it, a future regression where the spawn target gets wired to a different broken binary (e.g. `/bin/false`) would still produce a `daemon exited before ready` → `daemon restart failed` chain that passes the structural error-contract assertion — but the test wouldn't actually be exercising the broken-pyry path it claims to. The token assertion localizes "broken helper ran" from "some other binary exited early" the moment the regression lands.

**No `--release-url` flag.** `doUpdate` is driven directly from the same-package test, using the existing seams rather than a production CLI flag or environment variable for test release URLs. Automatic rollback remains out of scope; keeping `pyry.prev` does not change the broken-binary test's restart-failure contract.

### E2E signature fail-closed (#776)

`TestUpdate_MissingSignature_E2E` mirrors `TestUpdate_FetchFailure_E2E`'s structure with a server that serves tarball + checksums but 404s the `.sig`. Asserts `doUpdate` errors before `AtomicReplace` (inode unchanged), the pre-update daemon is still answering, and the success line is absent — the integration-level proof of the AC-2 downgrade defense. Bad-signature is covered at the `VerifySignature` + `doUpdate` unit level; one e2e for the fail-closed/downgrade case is sufficient. The tagged suite reuses the same `newFakeReleaseServer` (auto-signing) so `HappyPath`/`VerifyFailure`/`BrokenNewBinary` keep working once their `updateOptions` literals set `signingPubKey: testSigningPub`.

**Pre-existing suite bug uncovered.** The `//go:build e2e_update` suite was un-runnable before #776: `spawnDaemonE2E` never set `cmd.Dir`, so the spawned daemon inherited the test process's cwd (the package dir) and the workdir-confinement guard aborted startup for the whole suite. It went unnoticed because **CI does not run the `e2e_update` tag**. Fixed with a one-line `cmd.Dir = home` in the shared helper (required for the new e2e daemon to start at all). See [`codebase/776.md`](../codebase/776.md) § Lessons learned.
