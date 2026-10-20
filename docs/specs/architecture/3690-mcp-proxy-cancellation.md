# MCP proxy backpressure cancellation proof

## Files read

- `internal/e2emcp/proxy_test.go` → `TestMCPProxyActualSCMTransferCancellationAndBindings`: actual SCM_RIGHTS transport, current premature observer and elapsed-only assertion.
- `internal/e2emcp/server_test.go` → `TestMain`: same-binary server/proxy helpers and actual child exits.
- `internal/e2emcp/server.go` → `ProxyServer.Join`, `bad`, `handle`, `runOwnedServer`: cancel before coordinator locking; handlers join all three pumps and actual child Wait.
- `internal/e2emcp/coordinator.go` → `ForwardRPC`, `Abort`, `Finish`: validation and trial observation precede forwarding under one mutex; cancellation cannot seal evidence.
- `internal/e2emcp/proxy.go`, `launch.go` → `receiveLaunch`, `ProxyCommand`, `serverEnvironment`: authenticated current-child rights and environment boundaries.
- `docs/knowledge/features/development-verification.md` and its test-execution and distinguishing-tests topics: later focused passes cannot attribute historical failures; controls must discriminate the repair.
- `docs/knowledge/features/e2e-realclaude.md`: offline same-binary proof supplies no live-provider acceptance. QMD searches for e2emcp/cancellation found no owning package overview.
- `CODING-STYLE.md`: bounded goroutines, race checks and contract comments.

## Context

One behavior: reliable owned-I/O cancellation proof, with its controlled regression, for pipes and Unix sockets. No independent deliverable or runtime repair is planned. Historical elapsed-only failures remain failed. No decision record is needed.

Retrieved verifier bundle `59f34a5e89d6eda739c79622644f69309728f54a13150df827f4da9132e3feb4`, digest `92e4e5e675d92788e76d6560b8b4c4d3e7848c00cedaec9932f8dba0639fc5c9`, through fleet evidence before focused execution. Candidate `09b00361798e8decb20462cfc90f87473d19a8ab` failed each backpressure leaf 1/0/1/0 in both original gate and named comparison; base `147b5a59e8ab53654f440c2f4a67b1e9b9e56a6d` passed each 1/1/0/0. The 21 local build inputs and manifests were unchanged. Candidate/base difference remains unexplained. Whole-leaf comparison durations were 3.62/3.19 seconds versus 2.30/2.25, not measured Join durations or parent expiry.

No other fetched feature branch changes the planned files. #3695 overlaps failure tracking; #3680/#3676 supply no required implementation.

## Design

Keep runtime unchanged unless a controlled defect demonstrates otherwise. Preserve every existing transport/binding case. Replace the pre-write observation as readiness with receipt of one byte of the large response; keep the remaining response unread through Join. Require a still-held forwarding mutex at that point and verify the cancelled response was partial after shutdown. This places Join after validation and trial observation, in actual forwarding.

Observe the owned context cancellation separately from Join completion. Require Join error, a live independent parent, completed handler wait (which includes owned server Wait and pumps), empty live connection inventory, actual native proxy Wait with failure, partial output EOF, and rejected `Coordinator.Finish`. Log content-free phase durations. A watchdog closes test-owned stdio and cancels the parent solely to recover a broken test, and its firing fails the proof. Start with a 15-second watchdog, separate from a one-minute parent deadline; compare with measured phases before handoff. It is a deadlock bound, not a latency claim.

Add two controlled validation-delay leaves using the same actual transports. Hold the validator for 1.2 seconds after its observer notification: this models descheduling/validation work before forwarding and distinguishes premature readiness from actual forwarding. The delay is an injected experimental condition, not the cancellation oracle. Re-run the old observation/elapsed assertion via a scratch overlay to demonstrate its false attribution. Disable owned cancellation via a separate scratch overlay; both ordinary backpressure leaves must fail and release/join controlled work during cleanup.

## Concurrency model

Runtime ownership stays unchanged. Test cancellation observer exits on the owned context; the test joins it. A finite watchdog releases parent-owned descriptors and cancels the parent on failure, and its callback is joined if it fires. All started native helper commands are Waited; cleanup kills only their `exec.Cmd.Process`. No fixture PID or process group is signalled.

## State transitions and identity reuse

| Event | Race proof |
| --- | --- |
| Real unread partial large response → budget expiry → owned cancellation → joined handlers/proxy | `TestMCPProxyActualSCMTransferCancellationAndBindings/backpressure`, `/backpressure-sockets` |
| Delayed validation → first forwarding byte → cancellation | same test's `/backpressure-validation-delay`, `/backpressure-validation-delay-sockets` |
| Complete EOF/Wait, quiet cancellation, foreign bindings/rights | existing leaves of the same named test |

Each leaf owns fresh bindings, contexts, descriptors and children. No identifier is reactivated or reused.

## Error handling

Watchdog firing, parent expiry, missing owned cancellation, early Join, full-response completion or a successful cancelled proxy/seal fail independently. Cleanup closes native stdio and cancels the parent before joining incomplete work so a disabled cancellation mutation cannot strand the test.

## Testing strategy

Commit this plan first. Add controlled delay cases and demonstrate failure with the old oracle, then implement direct observations. Run uncached native Linux race checks for the full named test (including its preceding success/quiet leaves), ten repetitions of each ordinary backpressure leaf, controlled delay cases and a disabled-cancellation negative control. Retain settings, counts and phase measurements below; investigate helper exit overhead separately from Join. Run affected package race checks, `go vet ./...` and `go build ./cmd/pyry`. Full suites and final acceptance remain dispatcher-owned. If focused experiments leave suite attribution unresolved, submit retained evidence through fleet investigation rather than launch a complete suite.

Sizing before plan commit: one behavior, four acceptance criteria, approximately 400 written lines including tests/plan/evidence; no exports, no consumer migrations, no added runtime reject branches. Within all five limits.

## Open questions

- Can ordinary focused execution reproduce the historical elapsed failure? Phase measurements will distinguish pre-forward work, owned cancellation, handler completion and native proxy Wait; unavailable historical measurements stay unknown.
- Does the 15-second watchdog provide adequate measured race headroom? Resolve from focused results without treating elapsed time as cancellation proof.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/development-verification-test-execution-and-artifact-survival.md`, add “MCP proxy cancellation diagnosis” explaining proven cancellation observations, measured timing limits and remaining attribution uncertainty. State that elapsed time alone does not establish parent-context expiry and the same-binary helper proves offline ownership/I/O, not live-provider acceptance.

## Security review

**Verdict:** PASS

- Trust boundaries: retain actual `receiveLaunch` authentication and all foreign-binding/invalid-rights rejection leaves; synthetic timings never become authority.
- Tokens/secrets: log only phases and counts; never capabilities, IPC or environment bodies. Token generation/comparison unchanged.
- Files: reuse private fixture roots and existing descriptor ownership; no new persisted credentials or paths.
- Subprocesses: use the existing allowlisted current test binary/environment. Kill only actual owned `exec.Cmd.Process` during failure cleanup; no shell, fixture PID or group signals.
- Cryptography: no changes; source SHA256 binding and random capability remain intact.
- Network/I/O: keep framing limits, actual transport transfer and incomplete-frame rejection. SHOULD FIX: watchdog must close test descriptors before waiting for a broken Join.
- Errors/telemetry: retain generic boundary errors and add only content-free diagnostics.
- Concurrency: SHOULD FIX: join observer/watchdog callbacks and release controls on every failure path; do not mutate cancellation concurrently in normal tests.
- Threat model: offline test oracle only; no new product access surface or live-provider acceptance claim. No deferred security finding.

Reviewer: builder self-review, 2026-10-20.
