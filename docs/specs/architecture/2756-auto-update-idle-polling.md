# #2756 — retain scheduled releases while waiting for idle

## Files read

- `cmd/pyry/auto_update.go` → `autoUpdater.Run`, `check`, `restart`, `daemonIdle`: current schedule, busy discard, restart poll and quiet-window contract.
- `cmd/pyry/auto_update_test.go` → `newAutoUpdateFixture`, install/signature/rollback/restart tests: signed release fixture and existing assertions to reuse.
- `cmd/pyry/update.go` → `installRelease`, `keepPrevious`: signature/checksum gates and atomic replacement remain the installation boundary.
- `cmd/pyry/main.go` → `runSupervisor` auto-updater construction and shutdown join: one context-bound worker, phone-independent idle predicate.
- `internal/update/version.go` → `ParseRelease`, `Eligible`: strict eligibility precedes retention and asset URL construction.
- `internal/update/fetch.go` → `Fetcher.get`: context-bound requests and bounded bodies.
- `internal/update/restart.go` → `DetectRestartCommand`: fixed service-manager argv.
- `docs/knowledge/features/pyry-update-command.md` § Automatic update: install and rollback guarantees, restart re-check and logging contract.
- `docs/knowledge/decisions/040-auto-update-eligible-release-and-idle-are-independent.md` § Decision: eligibility and idle remain independent predicates.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: exercise actual ordering and cancellation, not only final file state.
- `CODING-STYLE.md`: stdlib tests, cancellation and structured logging.

## Context

An eligible release discovered while busy currently waits for another four-hour metadata check. Retain the selected release inside the ongoing check and poll idle once a minute, so quiet daemons install promptly. Update ADR 040 through the documentation handoff; no new decision record is needed.

One deliverable: scheduled-update idle waiting. Estimated total written work: 390 lines (about 70 production, 250 tests, 70 plan); zero exported types/interfaces, zero changed consumer contracts, four acceptance criteria, at most ten reject/error branches. This is smaller than #2716's shared-install and daemon-wiring change. The completed plan remains within all five limits. No other fetched feature branch overlaps the two target code files.

## Design

- `check(ctx) bool` retains its signature and selected `Release` locally after host checks, metadata parsing and eligibility. Wait for idle before calling the existing `installRelease` with that tag. Log `waiting_for_idle` once on entering a busy wait, then `installed` or `failed` when installation completes; cancellation ends the wait without a terminal record.
- Introduce private `waitUntilIdle(ctx, onWait) bool`: ask the existing idle predicate immediately, call the optional entry callback once if busy, and poll at `restartPoll` until idle or cancelled. Both pre-install and restart use it; restart still re-checks idle after download.
- Introduce a private context-aware duration wait method with an optional injected wait function for deterministic tests. Production uses a stopped-on-exit timer. Check context before and after a wake, including when timer readiness and cancellation coincide.
- `Run` waits the startup delay, runs one check synchronously, and waits `interval` after each unsuccessful completed check. A pending idle wait cannot trigger another scheduled check. Successful installation ends `Run` even if restart fails.
- Keep startup, eligibility, managed-unit/Homebrew refusals, the 15-minute quiet window and phone-independent wiring unchanged. Update stale code comments about single-line checks and timing.

## Concurrency model

No new goroutines or shared mutable production state. The existing `Run` worker owns the selected release and waits. Daemon cancellation ends startup, retry, install-idle and restart-idle waits; the supervisor still joins the worker. Poll wakes must check cancellation before download/install/restart. Idle remains a snapshot; a turn opening during download is handled by the restart re-check.

## Error handling

Host refusal, up-to-date, ineligibility, metadata failure and install failure return false and schedule a four-hour wait from completion. Installation errors retain existing wrapped errors and rollback behavior. Restart failure logs separately and does not resume checking. Cancellation during an idle wait performs no installation work or restart.

## Testing strategy

Reuse the signed fixture and existing rollback/signature/quiet-window tests. Inject controllable waits and fake idle answers to prove:

- A busy eligible check makes one metadata request and zero asset requests over more than four hours of minute polls, emits one waiting record, retains the original tag even if latest changes, then installs at the first idle poll.
- Wait entry is followed by either installed or failed; no per-poll logs.
- Cancellation during a wait, including cancellation visible on a successful poll wake, exits `Run` without asset requests, replacement or restart. Also cover cancelled restart polling.
- Each unsuccessful outcome requests a four-hour delay only after completion; startup requests two minutes and idle polling requests one minute.
- A successful install with failed restart exits without another interval or check and preserves `pyry.prev`; reuse restart-busy coverage with controllable waits.

Run focused tests red before implementation, then race tests for `./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-2756/pyry ./cmd/pyry`. The verifier owns the full-module gate.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage:

- `docs/knowledge/features/pyry-update-command.md`, **Automatic update** and its logging table: replace the claims that a busy check is discarded and retried at the next scheduled tick. State that an eligible tag is retained, idle is polled once a minute without downloading while busy, and `waiting_for_idle` is emitted once followed by the terminal outcome unless cancelled. Failed checks still use the four-hour schedule, measured from check completion. Replace the one-log-line-per-check claim to account for the wait-entry and terminal records.
- `docs/knowledge/decisions/040-auto-update-eligible-release-and-idle-are-independent.md`, **Decision**: replace the later-tick wording with retaining the eligible release and polling idle; preserve the independent eligibility and idle predicates.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `ParseRelease` and `Eligible` still validate network metadata before retention; only the eligible tag reaches `installRelease`. Retention does not weaken draft, prerelease, suffix or downgrade refusals.
- [Tokens] No credentials or new secrets: anonymous metadata/asset reads and an existing public verification key.
- [File operations] All writes remain in `installRelease`/`keepPrevious` through `AtomicReplace`, preserving permissions and rollback; waiting touches no files. No new path construction or symlink handling.
- [Subprocesses] Restart uses only `DetectRestartCommand` argv and existing context-aware execution without a shell; no network-derived command arguments.
- [Cryptography] Existing Ed25519 and SHA-256 gates still precede every replacement; no new primitive or key use.
- [Network and I/O] Existing bounded `Fetcher` and production HTTP timeout remain. A pending release performs no network I/O while busy and cannot cause repeated metadata requests at four-hour boundaries.
- [Errors, logs, telemetry] Keep `truncateTag` for network tags and existing wrapped errors; log one wait entry and one terminal outcome, avoiding per-minute log growth. No payload or secret logging.
- [Concurrency] Cancellation can coincide with a timer wake. The design checks context after every duration wait and before idle-gated actions, so already-visible cancellation cannot start a download or restart. No new lock order or goroutine.
- [Threat model] The existing off-by-default updater's verified release trust remains. Retaining a tag deliberately means later latest changes do not change this pending selection; bytes still require valid signatures. Key rotation remains owned by release tooling, unchanged by this ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
