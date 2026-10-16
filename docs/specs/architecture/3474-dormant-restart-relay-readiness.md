# Dormant restart relay readiness

## Files read

- `internal/e2e/relay_v2_stream_new_session_dormant_test.go` → both dormant restart proofs and `dialPairedPhone`: preserve reset-first, persisted binding, predecessor input and A isolation assertions.
- `internal/e2e/harness.go` → `RestartStreamInteractiveWithRelay`, `Harness.Stop`: process collection is not relay deregistration; restart retains HOME and registry.
- `internal/e2e/relay_v2_daemon_test.go` → `waitBinaryHello`: readiness currently means map membership.
- `internal/e2e/internal/fakerelay/fakerelay.go` → `handleBinary`, `serveBinary`, `handlePhone`, `WaitBinary`: registration survives pump shutdown until handler cleanup.
- `internal/e2e/internal/fakerelay/fakerelay_test.go` → raw binary/phone dial helpers and bounded round trips.
- `docs/knowledge/features/fakerelay-harness.md` → registration barrier: client upgrade receipt precedes server map insertion.
- `docs/knowledge/features/e2e-harness.md` and its stream-interactive topic: joined process cleanup and readiness distinctions.
- `docs/knowledge/features/development-verification.md` → controlled evidence cannot attribute an unobserved historical failure.
- Originating #3467 verifier evidence → baseline EOF preceded M4; no historical relay phase evidence establishes cause.

## Context

The same relay and server ID survive daemon restart. Daemon process exit does not
join the relay handler. `WaitBinary` can therefore observe daemon one's retiring
registration when the phone needs daemon two. Demonstrate this dependency without
claiming it caused the historical macOS EOF. No production change or ADR is needed.
Remote feature branches checked: none overlap the two existing files to change.

## Design

Add `Server.WaitBinaryOffline(ctx, serverID) bool` to the test-only fakerelay:
return true only after registration absence is observed before cancellation.
Both dormant tests stop daemon one, await relay absence with the existing five
second readiness bound, then start daemon two and use the unchanged positive
registration barrier. Because daemon two does not start until absence, an old
registration cannot satisfy that barrier. No general helper signatures change.

Add content-free binary generation/lifecycle diagnostics and phone upgrade/Noise
phase logs. A controlled real-WebSocket regression blocks the retiring handler
after its pumps join but before deregistration, proves old membership still
admits a doomed phone, and requires the offline barrier to reject that state.
After release, reuse the ID and prove replacement routing. Separate deadline and
cancellation scenarios cover a missing retirement. No fixed sleeps or EOF retries.

## Concurrency model

The relay retains its existing mutex and joined receive/send pumps. The barrier
polls under that mutex on a stopped-on-return ticker and caller context. The
regression's logging gate is outside the relay mutex, has bounded release and is
released in cleanup before server shutdown. Existing daemon cleanup remains joined.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Pump exit with old registration retained; premature phone upgrade | `TestWaitBinaryOfflineRetiringRegistration` |
| Deregistration then same server ID registered again | `TestWaitBinaryOfflineRetiringRegistration` |
| Missing retirement, deadline, cancellation, already absent | `TestWaitBinaryOfflineBounds` |
| Restart with same HOME, dormant reset then successor input | `TestRelayV2_StreamNewSessionAfterDaemonRestartResetsDormantConversation` |
| Restart with same HOME, dormant typed clear | `TestRelayV2_StreamClearAfterDaemonRestartResetsDormantConversation` |

## Error handling

Missing deregistration fails within five seconds before starting daemon two.
Handshake failures remain fatal with phase evidence; they are never retried away.
The controlled gate releases on timeout as well as cleanup to avoid hanging joins.

## Testing strategy

Write the controlled regression first and run it red against a membership-only
barrier, then implement absence coordination. Run at least 20 uncached repetitions
of both dormant tests and the regression with race, GOMAXPROCS=2 and GOFLAGS=-p=2.
Record unchanged-fixture repetitions too. Run touched test packages with race,
`go vet ./...`, and build. The issue explicitly requests full e2e and make
check/build evidence; record complete versus stopped outcomes separately.
Keep default package timeouts. Request managed macOS evidence rather than treating
Linux/amd64 Go 1.26.9 results as macOS/arm64 Go 1.26.2 results. A platform probe
establishes availability only. Retain exact commands, revisions and counts here.

## Open questions

- Historical attribution remains unknown unless the original ordering recurs.
- Native macOS candidate execution route: requested platform probe; its fixed
  recipe cannot itself execute this ticket's custom checks.

Sizing recheck: one deliverable, four acceptance criteria, about 350 written lines,
zero exported types, one exported fixture method, two updated consumers, fewer
than ten failure branches. No in-flight dependency.

## Security review

**Verdict:** PASS

- Trust boundaries: only test fixtures change; raw sockets use existing header and frame validation in `handleBinary` and `handlePhone`.
- Tokens: lifecycle logs contain generation, phase and routing identity only; pairing tokens, keys and payloads are excluded.
- File operations: no persistence or path behavior changes; existing isolated HOME and registry ownership remain.
- Subprocesses: existing harness launch and joined process-group shutdown remain; no new subprocess commands in product code.
- Cryptography: Noise helpers and nonce order stay unchanged; the raw relay regression uses JSON routing, not altered cryptographic behavior.
- Network/I/O: existing loopback listeners and frame caps remain; new waits and controlled gates have finite contexts.
- Errors/logs: no payload or credential diagnostic capture is introduced.
- Concurrency: no locks held across the controlled gate; polling reads under `Server.mu`, and cleanup releases before joining handlers.
- Threat model: production relay/mobile authentication and authorization contracts do not change; no newly deferred threat.

**Reviewer:** builder self-review, 2026-10-15.

## Revisions

- 2026-10-15: Phase instrumentation also touches `internal/e2e/handshake_interactive_helpers_test.go` → `driveHandshakeToOpenDaemonInteractiveResuming`, adding content-free init-sent/response-received logs so a failed read identifies the Noise phase. Remote branches checked: no overlap. No handshake or caller contract changes.
- The controlled regression reproduced an accepted phone upgrade followed by normal WebSocket closure during its first handshake read, with old pumps joined and deregistration held. This demonstrates the stale-registration dependency, not the historical EOF's exact cause or close shape. The membership-only negative control failed all four new leaf scenarios.
- 2026-10-16: The issue's testing-policy update, following dispatcher PR #286,
  supersedes this plan's request for matched macOS execution and its native-route
  open question. This implementation runs on Linux; macOS checks are outside
  this ticket's verification scope and are not claimed as passed. Preserve the
  controlled negative test, repetitions, complete Linux gates and independent
  review. The fixture contract and security review are unchanged.

## Builder evidence

Environment: Linux 7.0.0-38-generic x86_64, Go 1.26.9 linux/amd64;
`GOMAXPROCS=2 GOFLAGS=-p=2`, default ten-minute package timeout. No prebuilt
daemon override was supplied; harness binaries were built from this tree.
No artificial CPU contenders were launched. Shared-host load averages ranged
from 7.92 to 15.06 during the focused work; host-wide attribution is unknown.
The baseline repetitions briefly overlapped this run's small negative-control
compilations; candidate regression and dormant repetitions ran sequentially,
then the combined gate ran alone among this run's test commands.

Originating evidence was retrieved with `fleet-task.py evidence 3467` under
`/tmp/builder-3474/evidence-3467`; verifier bundle
`d94856e4472fc668bb504f63ee4dad7a1436d16aa6991d3798c8e4c981df973c`
retains the baseline/head comparison. Its historical second-phone EOF remains
unattributed; neither the baseline nor candidate focused runs reproduced it.

| Check | Revision | Executed | Passed | Failed | Skipped |
| --- | --- | ---: | ---: | ---: | ---: |
| Unchanged dormant tests, 20 repetitions each | `ec136ff1` (plan only) | 40 | 40 | 0 | 0 |
| Controlled tests, 20 repetitions | repair subsequently committed as `167ea7b4` | 100 | 100 | 0 | 0 |
| Repaired dormant tests, 20 repetitions each | `167ea7b4` | 40 | 40 | 0 | 0 |

Commands (all prefixed `env GOMAXPROCS=2 GOFLAGS=-p=2`):

```sh
go test -tags e2e -race -json -count=20 -run '^TestRelayV2_Stream(NewSession|Clear)AfterDaemonRestartResetsDormantConversation$' ./internal/e2e
go test -race -json -count=20 -run '^TestWaitBinaryOffline' ./internal/e2e/internal/fakerelay
```

The first command was run unchanged and repaired. Raw JSON is retained as
`/tmp/builder-3474/{baseline-focused,candidate-focused,candidate-regression}.jsonl`.
The negative control replaced `WaitBinaryOffline`'s body with
`return s.WaitBinary(ctx, serverID)` and ran
`go test -race -count=1 -run '^TestWaitBinaryOffline' -v ./internal/e2e/internal/fakerelay`
under the same concurrency settings. It executed five test/subtest records,
failed all five (four leaf scenarios and the bounds parent), skipped zero.
`red.log` retains the failure evidence; the repair was then applied.
This negative-control tree was `ec136ff1` plus the new tests, pump-stop log
and temporary membership-only method, before implementation was committed.

In the first unchanged new-session repetition, UTC 20:40:23.136 registered
daemon one; 20:40:24.203 logged its relay disconnection; daemon shutdown was
then collected. Daemon two registered at 20:40:24.255 and phone `c-2` upgraded
at 20:40:24.293. M4 and M5 completed at test elapsed 7.291s and 8.297s.
This passing ordering does not reproduce the historical stale-read window.

The first controlled run retained these UTC phases: 20:42:00.483620483 old
socket closed/pumps joined/deregistration held; 20:42:00.484893732 premature
phone upgrade accepted; 20:42:00.485560773 first read failed with
`StatusNormalClosure`; 20:42:00.514105611 retirement released and absence
observed; 20:42:00.516602670 same-ID replacement routed the response.

Representative repaired new-session phases, UTC 2026-10-15 (same server ID):

| Time | Evidence |
| --- | --- |
| 20:42:32.111 | Relay registers generation 1 |
| 20:42:33.191 | Generation 1 receive/send pumps joined |
| 20:42:33.192 | Generation 1 deregistered |
| before generation 2 launch | Daemon PID 222263 collected; offline barrier observed absence |
| 20:42:33.238 | Relay registers generation 2 |
| after registration | Second phone upgrade completed; Noise init sent, response received, interactive hello_ack verified |
| test elapsed 2.647s | M4 fresh B transition, predecessor spawn/input and wrap-up evidence retained |
| test elapsed 3.656s | M5 successor input and fresh-identity spawn; A isolation assertion passed |

Native task `76b5199043b070c6d8bb1b3e73cc32f7f67405343882d6e8d0187699243271a6`
completed on `macbook`; `fleet-task.py read` retrieved
`platform.json`: Darwin/arm64, `candidate_tests_executed: false`.
This establishes route availability only. None of the offered fixed execution
recipes runs arbitrary fake-daemon e2e checks; matched macOS Go 1.26.2 baseline
and candidate repetitions and complete gates remain unexecuted, not passed.

### Complete Linux gates at `167ea7b4`

Both commands completed with exit 0, sequentially after focused repetitions:

```sh
env GOMAXPROCS=2 GOFLAGS=-p=2 make check GO=/tmp/builder-3474/go-record
env GOMAXPROCS=2 GOFLAGS=-p=2 make build BIN=/tmp/builder-3474/pyry
```

`go-record` forwards non-test commands unchanged and executes tests as
`go test -json -count=1 "$@"`. It changes neither the race flags supplied
by Make nor package timeouts. Logs: `/tmp/builder-3474/check.log` and
`/tmp/builder-3474/build.log`. The final plan/evidence commit changes no Go source.

| Completed tier | Executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| Full module race unit tests | 27,123 | 27,100 | 0 | 23 |
| Full fake-daemon e2e, including fixture subpackages | 715 | 713 | 0 | 2 |

`go vet ./...`, staticcheck, substrate guard and build all passed. No named
failures and no stopped/incomplete Linux gate. Unit JSON spans UTC
20:44:29.846058495–20:53:00.205397885; e2e JSON spans
20:53:47.871672535–20:56:53.999533128. The root e2e package took 186.128s.
Shared-host load was 5.78/6.58/6.50 during e2e; no other builder-owned test
or build command overlapped this complete gate.

Unit skip reasons (23 records):

- Fourteen `internal/apps` proofs require optional supported Node/npm,
  `HOSTED_APP_NODE`, SQL or maintained-template runtime inputs. These skips
  provide no supported-template/SQL execution evidence.
- Three case-sensitive-filesystem skips: `TestMarkWorkdirTrusted_CaseMismatchWritesOnDiskKey`, `TestResolve_CaseMismatchFoldsToOnDiskCase`, `TestMemoryPathsCaseInsensitive`.
- Three macOS-only skips: `TestResolve_DarwinRealpath`, `TestMacOSInstallCancelled`, `TestMacOSHostGate`.
- `TestFilesystemAttacks/owner`: ownership substitution requires root.
- `TestRunAgentRun_RealClaude`: live-Claude opt-in absent.
- `TestAccountTokenReadDeadline`: full production deadline belongs to `make test-slow`.

E2e skips: `TestInnerFatalChild` runs only in its fatal-cleanup child process;
`TestHostedAppsRecoverySQL` requires optional `HOSTED_APP_NODE` and
`HOSTED_APP_TEMPLATE_BUILD`. Both dormant proofs and all controlled regression
scenarios executed and passed in the complete gates as well as repetitions.

Linux implementation and validation are complete and published in PR #3476.
The current role's matched macOS baseline/candidate acceptance remains blocked
by the absence of an applicable managed execution recipe, despite the successful
route probe; it is not a passed check or a later documentation handoff.

### Linux-only scope reassessment, 2026-10-16

The testing-policy revision above supersedes the preceding blocked conclusion.
The builder stage is complete under the issue's updated acceptance criteria.
PR #3476 remains open for the verifier's independent review and full-module gate.

Reassessed the saved implementation at merged head `a4b6ec5f`: there are no
changes under `internal/e2e/` relative to tested repair `167ea7b4`. The diff
preserves the restart proofs' existing HOME, relay, persisted-session, reset-first,
fresh-transition/binding, predecessor-wrap-up, successor-spawn and A-isolation
assertions. Shared helper signatures and the sibling `/clear` proof remain intact.
The retirement barrier is bounded and precedes replacement startup; controlled
coverage retains same-ID reuse, deadline/cancellation and released cleanup gates.
No additional Go changes or repeated full gate were needed for this scope update.

Retrieved prior builder evidence bundle
`74523189e0162327884201dc21040f4d534482424f2bbd68f1b4bf9a5f18af43` with
`fleet-task.py evidence 3474 --out /tmp/builder-3474-resume/prior-evidence`.
Its builder log confirms completed exit-zero `make check` and `make build`;
the commands, tested revision, counts and skip reasons remain recorded above.
The historical macOS EOF remains undiagnosed; the demonstrated dependency is
separate from attribution, and later Linux passes do not change that conclusion.

Read task `0fa055d85ee78411905ae79b2691a8ab30e5065165c7904547f0403199f60859`
with `fleet-task.py read` into `/tmp/builder-3474-resume/native-withdrawn`.
Its result is withdrawn before startup under the policy update: no worker ran,
no test artifacts exist, and no native pass is claimed. The earlier platform
probe likewise supplies no candidate test evidence. No new native task is needed.
