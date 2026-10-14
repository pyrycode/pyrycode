# Owner reconciliation cancellation proof

## Files read

- `internal/apps/reconcile_test.go` → `TestOwnerReconcileCancellation`, `recoveryAdvance`: four arms, stale snapshots and owner clock control.
- `internal/apps/health_test.go` → `healthFixture`, `healthStart`, `TestHealthHelperProcess`: child readiness differs from request entry; existing short deadlines and delayed bodies.
- `internal/apps/owner_test.go` → `ownerFixture`, `waitFile`: joined owner cleanup, 100 ms fixture grace and five-second child-signal bound.
- `internal/apps/owner.go` → `Stop`, `launch`, `cleanup`: intent persistence, cancellation, SIGTERM/grace/SIGKILL and child join precede completion.
- `internal/apps/health.go` → `supervise`, `probeHealth`: cancellable request/body read and natural startup/request expiry.
- `internal/apps/reconcile.go` → `completed`, `reconcileRecords`: retirement schedules retry; stale snapshots cannot admit work.
- `docs/knowledge/features/apps-package-concurrency-and-testing.md` § Concurrency and persistence: registry-before-owner locks and joins outside locks.
- `docs/knowledge/features/apps-package-testing.md` § Health-failure observations: observable probe phases distinguish real work from scheduler timing.
- `docs/knowledge/features/development-verification.md`: release blocked fixtures before cleanup joins; time alone cannot establish a phase.
- `docs/specs/architecture/3207-app-recovery.md` § Concurrency model: independently cancellable workers and joined retirement.
- `CODING-STYLE.md`: real helper children, race tests and contract comments.

## Context

The historical failure at `7332d538` bundled persistence, scheduling and child retirement into 500 ms. Its log is absent on this host. Ten uncached baseline focused repetitions at `dee9b204` passed; this does not diagnose that host's historical scheduling or establish a production regression. Current phase and deadline gaps are independently observable. No decision record is needed for this test-only repair.

One deliverable; sketch and final plan estimate 280–350 written lines, zero exported types/interfaces, zero consumer changes, three acceptance criteria and no production reject branches. #3364 is merged in this base. Feature #3359/#3360 overlaps production lifecycle files but neither fixture/test file; no prerequisite or shared-block rewrite is needed.

## Design

Add an opt-in health-helper gate with request-entry and response-release markers inside fixture data. It flushes headers, marks body withholding, and waits for test release or request cancellation. Existing fixture behavior stays unchanged when the gate is absent.

Startup arms use a valid withheld body with one-minute test-only startup/request limits, far beyond bounded phase/completion waits. They must retire and join the child while the release marker is absent, before natural expiry, without ever becoming healthy. Five-second observation bounds match the existing child-signal bound and exceed the reported 1.78-second historical arm, while the gate supplies the discriminating assertion. The startup timeout is a failure escape, not evidence of cancellation.

Backoff arms freeze the owner clock before launch, wait boundedly for failed generation completion, and require a nonzero retry due strictly after frozen time with unchanged generation before stop/removal. Both phases advance time past retry eligibility and reconcile stale pre-event and current snapshots, checking the same completed generation and absent child.

Record fixture/phase, event dispatch, cancellation, cleanup signal, persistence and retirement timings separately through test-local observation. Event operations run asynchronously so Stop cannot hide a hang before the completion wait. Failure cleanup releases the gate, cancels generation work and joins started event work before the existing owner shutdown.

## Concurrency model

Production goroutines and policies stay unchanged. Test event workers use buffered result channels and explicit completion joins. Helper request work observes request cancellation or a release file and dies with the joined child. Install observation hooks before launch; freeze/read the retry clock under owner locking. No callback re-enters owner or registry locks.

## State transitions and identity reuse

| Event | Race proof |
|---|---|
| Startup stop during withheld body | `TestOwnerReconcileCancellation/startup_stop` |
| Startup removal during withheld body | `TestOwnerReconcileCancellation/startup_remove` |
| Failed generation, pending ineligible retry, stop | `TestOwnerReconcileCancellation/backoff_stop` |
| Failed generation, pending ineligible retry, removal | `TestOwnerReconcileCancellation/backoff_remove` |
| Retry clock advance and stale/current replay after each event | All four arms of `TestOwnerReconcileCancellation` |
| Assertion failure with withheld work/event worker | Test-local release/cancel/join cleanup; deliberate cancellation mutation run |

## Error handling

Report named phase/completion timeouts with timings. File marker errors fail the helper/test. Register gate release after fixture cleanup registration so reverse cleanup order releases before shutdown. Do not swallow Stop/Remove or Shutdown errors.

## Testing strategy

First run the rewritten startup test without helper gate support and observe missing phase failure. Then implement the gate and prove cancellation. Temporarily disable request cancellation in `supervise` for a negative control: startup must fail while work is withheld, then release and join cleanup; restore before candidate validation.

Run `go test -race -json -count=10 -run '^TestOwnerReconcileCancellation$' ./internal/apps`, complete apps race tests uncached, `go vet ./...`, and `go build ./cmd/pyry`. The assigned acceptance criterion additionally authorizes uncached complete `make check` with default package concurrency, overriding the usual builder scope limit. Capture verbose output/counts outside the worktree; do not run live-Claude or platform gates. Full check includes merged #3364. Record commands/revisions/counts and attribution limits in the PR; file or reuse follow-ups for unrelated failures.

## Open questions

- Resolve measured phase/cleanup costs during implementation and validation; the missing historical log prevents assigning a historical root cause.
- Confirm a test-only repair suffices; change production only if a reproduced cancellation defect requires it.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Fixture gate configuration is supplied only by `healthFixture`; production `probeHealth` continues validating bounded fresh identity-matching health.
- [Tokens/secrets] No credentials are accessed; helper environment remains the owner allowlist in `launch`.
- [Files] Gate markers use fixed names under private temporary fixture data, mode 0600; no caller-controlled path or production persistence change.
- [Subprocesses] Existing helper re-exec and process-group cleanup are retained. SHOULD FIX: release before shutdown and join asynchronous event workers even after assertion failure.
- [Cryptography] No cryptographic operation or identity generation changes.
- [Network/I/O] Gate applies only to the loopback helper; retain server timeouts, with a finite gate-specific write timeout sufficient for the deliberately withheld body. Production reply limits and policies remain unchanged.
- [Errors/logs] Diagnostics contain phase durations and static state, never payloads or credentials.
- [Concurrency] Hooks installed before start; shared counters synchronized; registry-before-owner lock order preserved. Every test worker is joined.
- [Threat model] No relay/mobile boundary changes. This repair tests local retirement guarantees; no unrelated security work is deferred.

**Reviewer:** builder (self-review)
**Date:** 2026-10-14

## Revisions

- 2026-10-14: both open questions resolved by controlled experiments. With helper gating absent, startup phase entry fails at five seconds. Stripping request cancellation with `context.WithoutCancel(request)` makes both startup arms fail their five-second completion/retirement waits; releasing the fixture joins cleanup and the test process exits after 10.297 seconds. Restoring cancellation and injecting two seconds before event dispatch makes both arms pass at about 2.14 seconds each while health remains withheld. These changes were temporary and are excluded from the candidate. This supports a finite five-second observation bound over the reported 1.78-second arm and the controlled 2.1-second scheduling-plus-cleanup path; it is not a production latency contract or a reconstruction of the historical host load.
- Measurements separate roughly 28–30 ms fixture/startup request entry, 0.81 s natural failure/retirement setup for backoff, sub-millisecond intent persistence and approximately 104 ms SIGTERM-to-retirement (fixture grace is 100 ms). Healthy cancellation already works without production changes. Diagnostic observation durations include observer scheduling, not exact internal execution intervals. The absent historical log still prevents assigning its root cause; no queue-handle regression is established.
- 2026-10-14: main advanced to `03b0cf2c` (publication #3359/PR #3366) during the first full gate. The original candidate `3b14db51` completed that gate successfully: unit 26397 RUN nodes/26379 pass/0 fail/18 skip, fake e2e 697 RUN nodes/695 pass/0 fail/2 skip, plus vet/staticcheck/substrate-guard. Merged that main non-destructively as `c5b1d9c6` before final handoff. Read the new `Registry.Remove`/`removeRegistration` path, `Stop`, `completed` and publication knowledge: removal now joins generation retirement directly, so the asynchronous event bound still proves retirement with the response withheld. No test design or production change is needed. Repeat ten focused race repetitions, complete apps race tests and the uncached full gate on the integrated candidate; preserve all publication changes from main.
