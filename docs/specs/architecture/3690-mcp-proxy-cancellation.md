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

## Revisions

- 2026-10-20: require the owned context live immediately before Join and observe cancellation no earlier than its 25 ms budget. This rejects a spontaneous child failure masquerading as budget-driven cancellation. A testdata script prepares repeatable old-oracle, disabled-cancellation and phase-observer overlays without changing runtime code.

## Builder evidence

Native Linux/amd64, Go 1.26.9, runtime GOMAXPROCS 8 and 12 CPUs; GOFLAGS/GORACE unset, default parallelism, serial named leaves. All executions used `-race` and uncached `-count=1` or `-count=10`. Build caches were warm; each leaf allocated fresh bindings, homes and children. Incidental fleet load was not controlled (load snapshots 5.36/3.91/3.16 and 2.85/3.98/3.56). Admission waits are retained separately from test execution, including the final pipe run's 594-second queue wait.

Go source revision: `641c1edf7b2e21e5ceda2f75a5fff05371f8bde9`; corrected diagnostic script: `90596be8`. Subsequent evidence-only commits leave these Go inputs unchanged. [Retained results](../../../internal/e2emcp/testdata/3690-cancellation-evidence.json) contain exact commands, source/input/overlay hashes, counts, each selected leaf's measurements, failed attempts and historical bundle provenance. [Diagnostic preparation](../../../internal/e2emcp/testdata/3690-diagnostics.py) produces the three overlays in a supplied scratch directory; execute them through the recorded admission commands. The initial old-oracle overlay failed compilation with zero tests; the corrected replay below executed all named leaves.

| Native check | Executed / passed / failed / skipped events | Leaf result |
| --- | --- | --- |
| Affected package | 145 / 145 / 0 / 0 | 135 / 135 / 0 / 0 |
| Full named test within that package | 15 / 15 / 0 / 0 | 14 / 14 / 0 / 0 |
| Ten ordinary backpressure repetitions, each transport separately | 20 / 20 / 0 / 0 each | 10 / 10 / 0 / 0 each |
| Corrected old-oracle replay, expected failure | 15 / 12 / 3 / 0 | 14 / 12 / 2 / 0; both delayed leaves fail |
| Cancellation-disabled control, each transport separately, expected failure | 2 / 0 / 2 / 0 each | 1 / 0 / 1 / 0 each |
| Owned-phase diagnostic, full named test | 15 / 15 / 0 / 0 | 14 / 14 / 0 / 0 |

The full named runs preserve preceding `complete-duplex`, `complete-sockets`, `quiet-input` and `quiet-sockets` leaves. Parent events account for the extra failures in control totals. `go vet ./...` passed at the final Go source; `go build -o /tmp/builder-3690/pyry ./cmd/pyry` passed with identical production inputs.

Across 24 ordinary backpressure observations (package, repetitions and phase run), validation notification preceded the first byte by 0.513–0.566 seconds, owned cancellation was observed after 25.27–25.84 ms, and Join returned after 27.74–31.56 ms with a live parent and no handlers. Partial EOF retained 69,632 bytes for pipes and 219,264 for sockets, below the two-MiB payload. Post-Join EOF took at most 0.462 ms and subsequent native proxy Wait at most 0.057 ms. In the phase overlay, all pumps returned within 0.143 ms of cancellation-watcher entry into descriptor closure; actual owned server Wait returned within 3.257 ms. Source reading of `RPCSession.Observe` in `internal/e2emcp/rpc.go` confirmed the second envelope parse after the validator and before forwarding.

The 1.2-second validation delay moved first forwarding to 1.740/1.741 seconds after notification; repaired Join still returned in 28.23/29.18 ms. Restoring the premature observer and one-second predicate failed at Join 1.729/1.724 seconds while both parents remained live. The earlier pre-implementation red run independently failed both controls at 1.727/1.756 seconds, also with live parents. These controlled experiments prove the oracle defect, not a historical scheduler cause. Both disabled-cancellation controls reached the 15-second watchdog, explicitly failed parent independence, and released/joined their controlled work.

The longest successful selected leaf was 2.47 seconds. The 15-second owned-work watchdog provides over six times that observed envelope, bounds broken cancellation, and fails explicitly if used; the independent parent deadline is one minute. This is a fixture bound, not a product latency guarantee. A separate same-binary early-exit experiment measured actual Wait at 0.010/1.212 seconds with GORACE exit delay 0/1200 ms. Those two helper processes executed zero tests and supply no test pass; they isolate normal race-helper exit overhead, whereas cancelled owned Wait was observed directly in milliseconds.

Open questions resolved: ordinary focused runs did not reproduce the historical failure. Validation-delay dependence is reproduced; no runtime cancellation defect was demonstrated, so runtime code remains unchanged. Historical parent state, phase timing, cold state, host contention and Go/race settings remain unavailable. The original full host log is unavailable here; its bundle retains the tail and full-log pointer/hash. The unexplained candidate/base difference and every historical failure remain preserved.

Pending later stages: documentation handoff above, full-module/final dispatcher acceptance, and recovery assessment of concurrent race work in `internal/e2emcp` and `internal/e2e/internal/claudetape`. The investigation receipt is carried in the PR. This run executes no wider suite and completes none of #3668's unfinished gates. macOS is outside this Linux ticket's verification scope.
