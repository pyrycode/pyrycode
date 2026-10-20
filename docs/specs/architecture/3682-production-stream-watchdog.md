# Production interactive stream stall detection

## Files read

- `cmd/pyry/streamsup_runner.go`: `newStreamRunnerFactory`, `streamRunner.Run`, `WriteUserTurn`, and `streamApprovalConfig` own adapter composition.
- `internal/streamsup/watchdog.go`: `Watchdog`, `stallTracker`, and `Start` provide bounded, type-aware, emit-only detection.
- `internal/streamsup/runner.go`: `spawnAndWait` closes stdin before `Run` invokes `OnChildExit`, allowing write/exit serialization without preventing pipe teardown.
- `internal/streamsup/envelope.go`: `WriteTurn` returns nil only after the gated user write succeeds.
- `cmd/pyry/main.go`, `inbound_deliver.go`, `relay.go`: `approvalParkedReport` is installed before pool execution and answers through `streamApprovalBridge.ApprovalParked`.
- `cmd/pyry/stream_turn_drain.go`, `stream_turn_busy.go`: session-tagged fan-in attributes stalls without closing busy state.
- `cmd/pyry/daemon_live_state.go`, `daemon_live_source.go`: retained stall readings use producing-session identity.
- `docs/knowledge/features/streamsup-package.md` and its watchdog/construction topics: stdout wiring was deferred; latch rearming requires a poll to observe progress.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`: count executed tests and retain exact source evidence; keep new tests in a separate family file.

## Context

The production factory installs the parser without the existing watchdog. An alive silent child therefore never emits a stall. This repair delivers one observable behavior: bounded owed-silence publication, with lifecycle and attribution controls required to make it reliable. No new protocol or automatic recovery is introduced. No decision record is needed.

Sizing: approximately 650 written lines including this plan and regression helpers, zero exported types/interfaces, two production configuration consumers, four acceptance criteria, and fewer than ten reject branches. This remains below all five ceilings after planning. Feature branch #2939 also touches the factory for stdout capture; its changes are independent, and composition edits here stay local.

## Design

The factory constructs one watchdog, fans stdout to its writer and the parser, and explicitly preserves `ControlParser` when wrapping stdout. Its stall callback feeds `turnevent.Stall` into the same live-tag sink as parser events. `WatchdogConfig.Idle` stays zero in production; a private factory configuration field shortens fixture thresholds.

The adapter owns a small watchdog lifecycle helper. `WriteUserTurn` serializes the underlying write and successful arming with `ChildExited` reset. The exit callback runs after stdin has been closed, so a blocked pipe write can finish before the callback takes this lock. Reset completes before the supervisor can spawn a replacement. Failed/refused writes never arm. Existing permission/task/exit callbacks remain chained.

`Watchdog.Start` supports sequential lifetimes after the preceding cancellation and `Wait`; it allocates a fresh done channel for each start. `streamRunner.Run` creates a child context, starts polling, drives the runner, then cancels and joins polling before returning. One tracker persists across child respawns and Run reactivation.

The composition root passes a session-keyed pending-approval callback using `conversationForSession` and the existing `approvalParkedReport`. Lookup reads the live session tag at firing, never the active conversation. The report is installed before `pool.Run`. Pending permission annotates the existing content-free diagnostic only; publication and wire shape remain unchanged.

## Concurrency model

The stdout copy feeds the tracker under its leaf mutex. One poller runs per active Run, exits on cancellation, and is joined before reactivation. The adapter write/exit mutex nests only the tracker leaf mutex; the poller never takes the adapter mutex. Stdin I/O remains outside streamsup runner locks and child teardown closes stdin before calling adapter reset. Existing fan-in and approval/report synchronization remain in force.

## State transitions and identity reuse

| Event | Race regression |
| --- | --- |
| Successful write, stall onset, repeated polls, observed progress and another owed episode | `TestStreamWatchdog_ProductionLifecycle` |
| Child exit, idle replacement, subsequent user turn | `TestStreamWatchdog_ProductionLifecycle` |
| Cancellation and sequential Run reactivation on the same adapter | `TestStreamWatchdog_ProductionLifecycle` |
| Write racing child exit | `TestStreamWatchdog_WriteExitOrdering` |
| Rotation and a non-active producing conversation | `TestStreamWatchdog_ProductionLifecycle` |
| Refused/failed write and assistant/tool silence | `TestStreamWatchdog_Controls` |
| Approval pending on own versus another conversation, verdict/timeout preservation | `TestStreamWatchdog_ApprovalScope` plus existing approval tests |

## Error handling

Preserve underlying write and Run errors. No watchdog callback kills or restarts a child. Exit resets owed state even after a successful predecessor write. Nil approval reporting answers false. Existing bounded parser buffers and fan-in behavior apply to stdout and synthetic stalls.

## Testing strategy

Write factory regressions before wiring, observe missing stall failure with a short fixture threshold, then repair. Use real child stdin/stdout and production drain/emitter/retained state; verify PID/restart count, busy retention and exit cleanup. Use controllably silent fake children and bounded waits. Cover tool silence, failed/refused writes, approval annotation, and same-adapter reactivation. Retain exact revisions, selectors and test counts in the PR. Run affected Linux race tests through admission, vet and build. Complete suites belong to dispatcher; macOS is outside scope.

## Open questions

None. Sequential Start requires the previous Wait, matching the sessions adapter's existing Run contract.

## Documentation handoff

- Pending documentation stage: in `docs/knowledge/features/streamsup-package-idle-stall-watchdog-receive-side-emit-not-kill.md`, replace deferred stdout/lifecycle wiring claims with production ownership, arming/reset, Run reactivation, conversation-scoped publication and annotation-only approval behavior. State that detection neither kills nor confirms an OS-permission cause.
- Pending documentation stage: update the construction description in `docs/knowledge/features/streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md` with that ownership.

## Security review

**Verdict:** PASS

- [Trust boundaries] Child stdout remains untrusted: `stallTracker.consumeLine` decodes only type with existing bounded partial buffering. Synthetic stalls carry no child content.
- [Tokens] No credentials or tokens are created, stored, or copied; watchdog diagnostics contain duration and a boolean.
- [File operations] No production file operations are added. Fake-child files exist only under test temporary directories.
- [Subprocesses] Existing runner spawn/teardown owns argv, environment and cancellation. The watchdog holds no process handle and cannot terminate a child.
- [Cryptography] No cryptographic operations or key/nonce handling change.
- [Network and I/O] Existing fan-in and publication bounds remain in force; the new event uses the existing empty stall variant and unchanged protocol payload.
- [Errors/logs] Pending approval is a content-free boolean and never an inferred OS-consent cause. No payloads, approval text or verdicts enter diagnostics.
- [Concurrency] Successful write and exit reset share one adapter mutex; teardown closes stdin before reset. Sequential pollers are joined before restart. Approval lookup uses conversation identity from the current session tag.
- [Threat model] Remote sessions retain existing authenticated publication and conversation routing. SHOULD FIX: regress non-active/rotated attribution and another conversation's approval so active-cursor fallback cannot mislabel a stall. Permission identity and delivery accounting remain owned by #3681/#3683.

**Reviewer:** builder (self-review)
**Date:** 2026-10-20
