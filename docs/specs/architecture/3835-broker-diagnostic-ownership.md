# Broker diagnostic ownership

## Files read

- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: callback-created diagnostics currently register later LIFO cleanup than server join; final witness/artifact audits must remain unchanged.
- `internal/e2e/internal/claudetape/lifecycle_server.go` → `NewLifecycleBrokerServer`, `handle`, `Join`: validated Unix descriptor transfer, owned-child cancellation, and bounded join that can leave a callback running.
- `internal/e2e/internal/claudetape/lifecycle_server_test.go` → rejection and late-child cancellation tests: preserve all validation controls.
- `internal/e2e/internal/claudetape/lifecycle_test.go` → `TestLifecycleExecKeepsActualPIDAndJournalsAfterKill`: constructor consumer.
- `internal/e2e/internal/claudetape/fallback_worker_test.go` → `TestFallbackWorkerNativeProcessBoundary`: constructor consumer without an owned diagnostic.
- `docs/knowledge/features/e2e-harness.md` § Recorded lifecycle timing and settlement: input, completion, EOF, receipts and artifact audits are separate proof obligations.
- `docs/knowledge/features/development-verification-prove-that-tests-distinguish-the-change.md`: synchronize cleanup and callback barriers; retain failing evidence separately from a passing focused run.

## Context

One behaviour: retain each broker's diagnostic until its handler has finished using it, including failures and callbacks that outlast bounded outer cleanup. This is independent of predecessor retirement in #3834 and evidence preservation in #3833. No daemon behaviour changes or decision record are needed.

Sizing: approximately 400 total written lines including plan, shared diagnostic helper and regression; zero new exported types, six existing callback construction sites, three acceptance criteria, and no new rejection state machine. All limits remain below their ceilings. No fetched feature branch overlaps the planned existing files.

## Design

Extend the launch callback result to `(*exec.Cmd, io.Closer, error)`. A non-nil closer transfers ownership to `LifecycleBrokerServer.handle`, including when the callback also returns an error or an invalid command. The handler defers exactly one close after callback return and before marking handling complete. It retains ownership through `Start` and `Wait`. A nil closer preserves callers that have no diagnostic resource.

Extract diagnostic creation/attachment into `recordedLifecycleDiagnostic(cmd, path) (io.Closer, error)` in the suite file. Keep exclusive creation, mode 0600 and retained paths. The suite returns this closer to the server instead of registering `t.Cleanup` in a handler. Its other cleanup, verdict, broker inventory, terminal and artifact audits are unchanged. Other constructor consumers return a nil closer.

## Concurrency model

The existing handler goroutine owns the returned closer; outer cleanup never closes it. `Join` still closes acceptance, cancels connections and kills only tracked actual children under the existing mutex. An in-flight callback can outlast both bounded waits; on returning it transfers its closer to the handler, which observes aborting and closes without starting a child. No new production goroutine or lock is needed. Regression subprocesses and callback holds have bounded watchdog/release and joined shutdown paths.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Outer assertion failure starts LIFO cleanup while callback holds a created diagnostic; successful start after release | `TestRecordedLifecycleDiagnosticCleanup/success` |
| Held callback returns a command whose Start fails | `TestRecordedLifecycleDiagnosticCleanup/start-failure` |
| Held callback rejects launch after creating its diagnostic | `TestRecordedLifecycleDiagnosticCleanup/launch-rejection` |
| Join returns after cancellation with callback still held; callback returns late, child must never start | `TestRecordedLifecycleDiagnosticCleanup/cancel-before-start` |
| Child started and then cancelled | Existing server cancellation/owned child checks, plus affected package race tests |

Each callback is admitted once by the existing claimed-job check; no identifier is reused by this change. The regression verifies closure after handler completion, exactly one owner close, retained content and no surviving owned child.

## Error handling

Open failures return to the callback without transferring a closer. Once created, the diagnostic closes on success, callback rejection, invalid command, Start failure or cancellation, including late callback return. Close is best effort as with current cleanup; it does not overwrite the original assertion or broker failure. Join timeouts remain failures, never evidence of handler completion.

## Testing strategy

Write a helper-process regression with an intentionally failing inner subtest, real Unix seven-pipe transfer and actual test cleanup. Hold callback progress using channels across outer cleanup, assert the file is still usable, release, then await server completion and check closure/count and child reaping. The parent requires the original failure marker/verdict and rejects extra lifetime failures. Demonstrate failure with the extracted old cleanup registration before repairing ownership; preserve JSON and stderr externally.

Run the affected claudetape package under the race detector, the synchronized e2e regression uncached, and the uncached offline `TestRecordedClaude/session-resume` on Linux with fresh isolated homes and external evidence. Record exact revisions, selections and executed/passed/failed/skipped counts. The managed selector parser refuses per-component anchors in the ticket's literal selection; the equivalent supported named-subtest selection is `^TestRecordedClaude/session-resume$` with the scenario environment restricting iteration. Preserve the refusal (zero execution). Run `go vet ./...` and `go build ./cmd/pyry`. Complete gates belong to the dispatcher; macOS is outside this ticket's verification scope.

## Open questions

None. Original-root replay failures remain failures tracked by #3834 and do not prove or disprove synchronized descriptor ownership.

## Security review

**Verdict:** PASS

- [Trust boundaries] `handle` retains kernel peer identification, exact seven distinct directional pipe checks, allowed-job/claimed-job checks; `ValidateLifecycleLaunch` stays before diagnostic creation.
- [Tokens and cryptography] This offline resource ownership change creates no credentials, keys or cryptographic decisions. Existing offline environment scrubbing is unchanged.
- [File operations] Diagnostic creation remains exclusive at a retained role-derived path with mode 0600. MUST FIX addressed in design: closure belongs to the handler, including callback rejection and return after a join timeout; outer cleanup must never close it.
- [Subprocesses] Commands remain source-bound and validated; cancellation still targets only actual server-owned `exec.Cmd` children. No native PID from a fixture becomes a kill target. A late callback must encounter the existing abort guard before Start.
- [Network and I/O] Existing bounded transfer, read deadline and Unix socket mode 0600 remain unchanged. The closer result is trusted harness ownership, not broker input.
- [Errors and logs] Original failure status and retained diagnostic content remain visible. No new payload, environment or credential logging.
- [Concurrency] Deferred closure precedes handler wait-group completion and follows its final command use. No new lock ordering; Join return alone grants no resource ownership.
- [Threat model] Harness-local offline ownership only. Broader recording authority and daemon lifecycle semantics are unchanged; predecessor retirement belongs to #3834.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-22

## Revisions

- 2026-10-22: Regression construction additionally covers `cancel-after-start`: a real broker child signals readiness on its transferred descriptor and blocks on its transferred input; bounded Join must kill and reap it and close its diagnostic once. Source pipe cleanup belongs to the outer probe so it cannot accidentally release that child before cancellation. The diagnostic helper returns the concrete `*os.File` (which implements the planned `io.Closer`) so tests can assert descriptor usability/closure. The supported anchored-root selection is `^TestRecordedClaude$` plus the single-scenario environment: Go treats root/subtest anchors separately, and the earlier `^TestRecordedClaude/session-resume$` also selected the similarly prefixed startup test. Subsequent replay uses the exact root; preserve that earlier result and its actual selection separately.
