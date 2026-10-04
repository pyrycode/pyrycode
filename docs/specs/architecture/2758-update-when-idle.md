# #2758 — request the running daemon's next idle update

## Files read

- `cmd/pyry/update.go` → `runUpdate`, `productionUpdateOptions`, `installRelease`, `keepPrevious`: CLI parsing and the shared signed, atomic install path.
- `cmd/pyry/auto_update.go` → `autoUpdater.Run`, `check`, `restart`, `waitUntilIdle`, `daemonIdle`: schedule, retained release, minute polling and quiet-window policy.
- `cmd/pyry/main.go` → `runSupervisor`, `parseClientFlags`, `splitClientFlags`: provider wiring, instance selection and shutdown joins.
- `cmd/pyry/auto_update_test.go` → `newAutoUpdateFixture`, check/idle/retry tests: reuse signed fixtures and existing refusal matrix.
- `cmd/pyry/rekey_test.go` → `rekeyTestResolver`: reusable Unix control test resolver.
- `internal/control/server.go` → `SetUpdateWhenIdleProvider`, `handleUpdateWhenIdle`, `Serve`: provider errors are hidden; shutdown drains handlers through their response writes.
- `internal/control/client.go` → `UpdateWhenIdle`: bounded exchange and validated decisions.
- `internal/control/protocol.go` → `UpdateWhenIdleResult`: unchanged wire contract and 70-second response deadline.
- `internal/update/version.go` → `Eligible`, `ParseRelease`: the existing eligibility/refusal contract remains authoritative.
- `internal/update/fetch.go` → `Fetcher.get`: HTTP context, timeout and bounded body reads.
- `docs/knowledge/features/pyry-update-command.md` → Automatic update: retain the chosen tag and preserve `pyry.prev` on repeated installs.
- `docs/knowledge/features/control-plane.md` → Update-when-idle provider and Shutdown: connection-independent work and synchronous response draining.
- `docs/knowledge/features/development-verification.md` → Verify inherited premises and Prove that tests distinguish the change: test ordering and lifecycle explicitly.
- `CODING-STYLE.md`: context cancellation, stdlib tests and structured logging.

## Context

The operator needs one selected daemon to decide on latest and install at its next idle moment. The requesting CLI must never install locally or wait for idle. The merged #2756 and #2757 supply idle polling and the control contract. This is one deliverable: explicitly requesting the daemon's existing update behavior. No decision record is needed.

Sizing: approximately 230 production + 420 test + 80 plan lines, below 800 total written lines; zero exported types/interfaces, two production consumers (CLI/provider and supervisor), five acceptance criteria and seven failure/refusal outcomes. No other remote feature branches exist at planning time. Existing check-level tests continue to cover installation safeguards.

## Design

`runUpdate` delegates to a writer-aware parsing helper for CLI tests. Parse daemon selection through `parseClientFlags`, then parse update flags. Enabled `--when-idle` rejects the presence of `--check`, `--version` and `--no-restart`, even when explicitly false/empty. It calls `control.UpdateWhenIdle` and prints the three decision strings. All control errors return directly; ordinary update continues through `productionUpdateOptions` and `doUpdate`.

The updater owns a mutex-protected active attempt. An attempt publishes a metadata decision through a closed channel and separately publishes its completed install/restart outcome. The first requester starts it; later requesters share that attempt. Refusals/metadata errors clear it; eligible attempts remain active while waiting, downloading and restarting. Install failure clears it. Install success retains its tag permanently, protecting `pyry.prev` even after restart failure. `check(ctx) bool` remains a synchronous wrapper for scheduled work and existing tests; explicit request returns after the decision publication only.

`runSupervisor` builds the updater and installs its provider before `Serve`, regardless of scheduling. Only the flag starts `Run`; disabled scheduling performs no unsolicited checks/retries. Scheduling waits retain the existing two-minute startup/four-hour retry behavior. Successful attempt completion wakes a sleeping schedule so `Run` returns rather than checking again.

## Concurrency model

Each active attempt runs one daemon-context goroutine; metadata, idle polling, install and restart use that context. Request handlers wait on its decision channel, independent of their connection. A mutex protects active-attempt creation/clearing and the installed notification; published result and terminal fields are read only after their respective channels close. No locks are held across network I/O or channel waits.

The scheduler owns a cancellable wait context and a notification watcher, joined before `Run` returns. Supervisor shutdown cancels the daemon context, closes/drains control handlers through `ctrlDone`, joins the scheduler, then joins updater workers. No attempt producer remains when the worker wait group is joined. Existing `Serve` handler draining and the supervisor's `ctrlDone` join guarantee that even an immediately completed installation triggering shutdown cannot end the daemon before a connected handler writes acceptance within its deadline. The request path reads the published decision even if restart has already cancelled the daemon context.

## Error handling

Homebrew and missing managed-unit refusals precede HTTP. `Eligible` supplies other refusal reasons and up-to-date. Fetch/parse failures log internally and reach the caller through the existing generic provider error. Install failures log and permit a new explicit attempt; automatic retry exists only when scheduling is enabled. Restart failure logs but preserves installed state. Cancellation stops metadata/idle/download/restart work and all workers are joined.

## Testing strategy

Write tests first and observe failure. Use real Unix sockets and the existing signed httptest release fixture. Cover CLI decisions/errors, selection and conflicting flags; explicitly requested work without a scheduler; shared in-flight metadata decisions (including failures), scheduled idle-wait joining, download/restart pending decisions and one install; retry after metadata/install failure; retained tag after restart failure; client disconnect and shutdown joins. Force immediate restart cancellation while holding a provider after its decision to prove the control drain preserves acceptance. Reuse the existing eligibility, idle-window and installation matrix. Run scoped race tests, `go vet ./...`, and `go build ./cmd/pyry`; the dispatcher owns the full-module verifier gate. No live Claude test is needed.

## Open questions

None. Daemon-selection flags use the existing prefix-before-verb-flags convention.

## Documentation handoff

Pending for the documentation stage:

- `docs/knowledge/features/pyry-update-command.md`, **Flags** and **Automatic update (#2716)**: document `--when-idle`, conflicts, daemon selection, the three decisions and error exit behavior, support without auto-update enabled, shared pending-tag deduplication, failure retry behavior, and minute polling with the existing 15-minute quiet window. Clarify that "returns at once" excludes the bounded metadata check but never waits for installation. Replace the claim that a daemon without auto-update builds no updater with the absence of unsolicited checks/retries.
- `docs/knowledge/features/control-plane.md`, **Update-when-idle provider**: replace the unwired-provider statement with production daemon/CLI wiring; state that accepted work survives the requesting connection, stops and is joined on daemon shutdown, and the acceptance response survives an update-triggered shutdown within the response deadline.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `handleUpdateWhenIdle` accepts no release/path payload; only the selected daemon's `ParseRelease`/`Eligible` determines the target. The Unix socket retains its owner-only 0600 boundary.
- [Tokens] No credentials or tokens are introduced or captured; HTTP uses the existing unauthenticated production fetcher.
- [File operations] `installRelease` retains signature/checksum validation, `keepPrevious` and `AtomicReplace`; the CLI socket path never becomes an installation path. No new file-write mechanism is added.
- [Subprocesses] Restart argv still comes from `DetectRestartCommand`, never client or metadata text; `defaultRunRestart` uses `exec.CommandContext`, no shell.
- [Cryptography] Existing Ed25519/SHA-256 validation remains mandatory before replacement; no new primitive/key handling.
- [Network and I/O] Existing bounded socket decoding, handshake/write deadlines, 70-second exchange and production 60-second HTTP timeout are preserved; `Fetcher.get` caps bodies.
- [Errors/logs] Existing provider error masking prevents internal errors from reaching the CLI. Log tags remain bounded with `truncateTag`.
- [Concurrency] Shared attempt state is protected under one mutex. Work uses daemon cancellation and is joined after control/scheduler producers drain. Published acceptance must survive cancellation after decision; the immediate-shutdown test pins this requirement.
- [Threat model] This is local owner-authorized daemon control, with unchanged relay/mobile boundaries. No new external endpoint or filesystem trust assumption.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-04
